package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func (s *RepositorySession) CancelTask(ctx context.Context, request application.CancelTaskRequest) (application.TaskProjection, error) {
	if ctx == nil || domain.ValidateID(request.TaskID) != nil || request.ExpectedRevision < 1 {
		return application.TaskProjection{}, application.NewError("cancel-task", application.ReasonInvalidRequest)
	}
	key, err := taskRequestKey(request.IdempotencyKey)
	if err != nil {
		return application.TaskProjection{}, err
	}
	borrow, err := s.borrow()
	if err != nil {
		return application.TaskProjection{}, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: s}
	if err := reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		_, found, err := s.ingress.ReadTaskProposal(s.acquisition.Scope, request.TaskID)
		if err != nil {
			return err
		}
		if !found {
			return application.NewError("cancel-task", application.ReasonTaskNotFound)
		}
		return nil
	}); err != nil {
		return application.TaskProjection{}, err
	}
	_, err = s.ingress.RequestTaskStop(ctx, reader, s.acquisition, request.TaskID, key, request.ExpectedRevision)
	if err != nil {
		return application.TaskProjection{}, taskError(err)
	}
	return s.readTaskBorrowed(ctx, request.TaskID)
}

// PendingTaskCancellation returns scheduling hints only. The resident takes
// Run lanes before global writer; the final producer independently rereads all
// facts and holds every existing Run lease through disposition append.
func (s *RepositorySession) PendingTaskCancellation(ctx context.Context, cursor ...string) (taskID string, runs []application.RunProjection, err error) {
	if len(cursor) > 1 {
		return "", nil, application.NewError("cancel-task", application.ReasonInvalidRequest)
	}
	after := ""
	if len(cursor) == 1 {
		after = cursor[0]
	}
	borrow, err := s.borrow()
	if err != nil {
		return "", nil, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: s}
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		stop, e := s.ingress.NextTaskStop(s.acquisition.Scope, after)
		if e != nil {
			return e
		}
		if stop.FactDigest != "" {
			id := stop.TaskID
			taskID = id
			plan, _, e := s.ingress.ReadTeamPlan(s.acquisition.Scope, id)
			if e != nil {
				return e
			}
			for _, node := range plan.Materializations {
				lease, e := s.runs.AcquireExisting(node.RunID)
				if errors.Is(e, os.ErrNotExist) || errors.Is(e, runstore.ErrLeaseHeld) {
					continue
				}
				if e != nil {
					return e
				}
				read, e := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
				e = errors.Join(e, lease.Release())
				if e != nil {
					return e
				}
				runs = append(runs, read.Run)
			}
			return nil
		}
		return nil
	})
	return
}

func (s *RepositorySession) FinishTaskCancellation(ctx context.Context, id string) error {
	borrow, err := s.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	_, err = s.ingress.RecordTaskCancellation(ctx, repositoryTaskCancellationVerifier{s}, s.acquisition, id)
	return err
}

type repositoryTaskCancellationVerifier struct{ session *RepositorySession }

// WithTaskRunNotStopped holds the same owner boundary as stop intake.
// The caller holds its Run lane/lease; fn is a short commit, not execution.
func (s *RepositorySession) WithTaskRunNotStopped(ctx context.Context, runID string, fn func() error) error {
	borrow, err := s.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	return (repositoryApprovedTeamVerifier{session: s}).WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		if err := s.ingress.RequireTaskRunNotStopped(s.acquisition.Scope.AuthorityNamespaceID, runID); err != nil {
			return taskError(err)
		}
		return fn()
	})
}

func (v repositoryTaskCancellationVerifier) WithCurrentTaskCancellation(ctx context.Context, owner resultingress.ControlOwnerAcquisition, stop resultingress.TaskStop, fn func(resultingress.TaskCancellation) error) (err error) {
	s := v.session
	reader := repositoryApprovedTeamVerifier{session: s}
	return reader.WithCurrentApprovedTeam(ctx, owner, resultingress.TeamPlanApproval{}, func() (err error) {
		current, _, _, err := s.ingress.ReadTaskCancellation(owner.Scope, stop.TaskID)
		if err != nil {
			return err
		}
		if current != stop {
			return resultingress.ErrTeamPlanConflict
		}
		plan, _, err := s.ingress.ReadTeamPlan(owner.Scope, stop.TaskID)
		if err != nil {
			return err
		}
		value := resultingress.TaskCancellation{TaskID: stop.TaskID, StopFactDigest: stop.FactDigest, PlanFactDigest: stop.PlanFactDigest, Nodes: []resultingress.TaskNodeDisposition{}}
		var leases []*runstore.Lease
		existing, err := s.runs.ListExistingRunIDs()
		if err != nil {
			return err
		}
		defer func() {
			for _, lease := range leases {
				err = errors.Join(err, lease.Release())
			}
		}()
		for _, node := range plan.Materializations {
			proof := resultingress.TaskNodeDisposition{NodeID: node.NodeID, RunID: node.RunID, Disposition: "not-created"}
			creation, created, e := s.ingress.ReadTeamRunCreation(owner.Scope, stop.TaskID, node.NodeID)
			if e != nil {
				return e
			}
			proof.CreationFactDigest = creation.FactDigest
			reservations, attempts, e := s.ingress.TaskRunExecution(owner.Scope.AuthorityNamespaceID, node.RunID)
			if e != nil {
				return e
			}
			lease, e := s.runs.AcquireExisting(node.RunID)
			if errors.Is(e, os.ErrNotExist) && !slices.Contains(existing, node.RunID) {
				if len(reservations) != 0 || len(attempts) != 0 {
					return application.NewError("cancel-task", application.ReasonRecoveryRequired)
				}
				if created {
					proof.Disposition = "never-materialized"
				}
				value.Nodes = append(value.Nodes, proof)
				continue
			}
			if e != nil {
				return e
			}
			leases = append(leases, lease)
			if !created {
				return resultingress.ErrTeamPlanConflict
			}
			read, e := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
			if e != nil {
				return e
			}
			if read.Run.TaskID != node.TaskID || read.Run.RunID != node.RunID {
				return resultingress.ErrTeamPlanConflict
			}
			if err := taskCancellationFrozenRun(lease, creation, read.Run); err != nil {
				return err
			}
			proof.RunHead, proof.RunSequence, proof.RunState = read.Run.AuthorityHead, read.Run.Sequence, read.Run.State
			if read.Run.State == domain.StateReady && read.Run.Sequence == 2 && read.Run.AttemptID == "" && read.AttemptsUsed == 0 && len(attempts) == 0 {
				proof.Disposition = "never-started"
				if len(reservations) == 1 && reservations[0].Status == resultingress.AttemptReservationCancelled {
					proof.Disposition = "reservation-cancelled"
					proof.ReservationFactDigest = reservations[0].ReservationFactDigest
					proof.ReservationResolutionDigest = reservations[0].ResolutionFactDigest
				} else if len(reservations) != 0 {
					return application.NewError("cancel-task", application.ReasonRecoveryRequired)
				}
			} else {
				if read.Run.State == domain.StateVerifying {
					if s.coldTaskVerifications == nil || s.coldTaskVerifications[node.RunID] {
						return application.NewError("cancel-task", application.ReasonRecoveryRequired)
					}
					halt, halted, e := s.ingress.ReadTeamPlanHalt(owner.Scope, stop.TaskID)
					if e != nil {
						return e
					}
					if halted && halt.Stage == "verify" {
						return application.NewError("cancel-task", application.ReasonRecoveryRequired)
					}
				}
				if len(attempts) != 1 || attempts[0].Identity.TaskID != read.Run.TaskID || attempts[0].Identity.AttemptID != read.Run.AttemptID {
					return application.NewError("cancel-task", application.ReasonRecoveryRequired)
				}
				proof.Disposition = "execution-cleaned"
				proof.AttemptHead = attempts[0].HeadDigest
				proof.CleanupReleasedDigest = attempts[0].CleanupReleasedDigest
				// The actual worker.completed/stop journal must bind the same
				// cleanup receipt; a terminal-looking Run alone is insufficient.
				events, truncated, e := runstore.ReadEventsUnderLease(lease)
				if e != nil || truncated {
					return errors.Join(e, resultingress.ErrTeamPlanConflict)
				}
				matched := false
				for _, event := range events {
					if taskCancellationCleanupEvent(event, read.Run, attempts[0]) {
						matched = true
					}
				}
				if !matched {
					return application.NewError("cancel-task", application.ReasonRecoveryRequired)
				}
			}
			value.Nodes = append(value.Nodes, proof)
		}
		return fn(value)
	})
}

func taskCancellationCleanupEvent(event domain.RunEvent, current application.RunProjection, attempt resultingress.AttemptAuthorityState) bool {
	if event.RunID != current.RunID || event.AttemptID != current.AttemptID || attempt.Identity.TaskID != current.TaskID || attempt.Identity.RunID != current.RunID || attempt.Identity.AttemptID != current.AttemptID ||
		attempt.BarrierDigest == "" || attempt.ProcessTerminalDigest == "" || attempt.AllocationTerminalDigest == "" || attempt.SupervisorClosedDigest == "" || attempt.CleanupReleasedDigest == "" ||
		event.Payload["terminalizationBarrierFactDigest"] != attempt.BarrierDigest || event.Payload["processTerminalFactDigest"] != attempt.ProcessTerminalDigest ||
		event.Payload["allocationTerminatedFactDigest"] != attempt.AllocationTerminalDigest || event.Payload["supervisorClosedFactDigest"] != attempt.SupervisorClosedDigest || event.Payload["cleanupReleasedFactDigest"] != attempt.CleanupReleasedDigest {
		return false
	}
	switch event.Type {
	case lifecycle.WorkerStoppedEventType:
		return lifecycle.ValidateWorkerStopped(event) == nil && attempt.StopIntent != (resultingress.AttemptStopIntent{}) &&
			event.Payload["stopIntentDigest"] == attempt.StopIntent.IntentDigest && event.Payload["stopRequestDigest"] == attempt.StopIntent.RequestDigest && event.Payload["originalRunAuthorityHead"] == attempt.StopIntent.ExpectedAuthorityHead
	case "worker.completed":
		return event.StateFrom == domain.StateRunning && event.StateTo == domain.StateVerifying && event.Actor != nil && event.Actor.Type == "system" && event.Actor.ID == "marshal-production-runtime" &&
			attempt.CommittedResultFactDigest != "" && event.Payload["resultAdmissionFactDigest"] == attempt.CommittedResultFactDigest
	default:
		return false
	}
}

// Start projections populate frozen fields only for READY/RUNNING. Cancellation
// also closes already-cleaned BLOCKED/VERIFYING/REVIEW_PENDING/ACCEPTED Runs,
// so read the original snapshot, bytes and freeze journal under the same lease
// instead of treating an intentionally absent projection field as authority.
func taskCancellationFrozenRun(lease *runstore.Lease, creation resultingress.TeamRunCreationState, current application.RunProjection) error {
	fail := func() error { return resultingress.ErrTeamPlanConflict }
	var frozen struct {
		Task       json.RawMessage `json:"task"`
		Policy     json.RawMessage `json:"policy"`
		Capability json.RawMessage `json:"capability"`
		BaseSHA    string          `json:"baseSha"`
	}
	if json.Unmarshal(creation.Inputs, &frozen) != nil {
		return fail()
	}
	state, err := runstore.InspectUnderLease(lease)
	if err != nil || state.RunID != current.RunID || state.TaskID != current.TaskID || state.Sequence != current.Sequence || state.State != current.State || state.CurrentAttemptID != current.AttemptID || state.BaseSHA != frozen.BaseSHA {
		return errors.Join(err, fail())
	}
	for _, file := range []struct {
		name   string
		raw    []byte
		digest string
	}{
		{"task-spec.json", frozen.Task, state.SpecDigest},
		{"policy-snapshot.json", frozen.Policy, state.PolicyDigest},
		{"capability-snapshot.json", frozen.Capability, state.CapabilityDigest},
	} {
		if len(file.raw) == 0 || canonical.DigestBytes(file.raw) != file.digest {
			return fail()
		}
		raw, err := runstore.ReadFileUnderLease(lease, int64(len(file.raw)+1), file.name)
		if err != nil || !bytes.Equal(raw, file.raw) {
			return errors.Join(err, fail())
		}
	}
	events, truncated, err := runstore.ReadEventsUnderLease(lease)
	if err != nil || truncated || len(events) < 2 {
		return errors.Join(err, fail())
	}
	freeze := events[1]
	if freeze.Sequence != 2 || freeze.RunID != current.RunID || freeze.Type != "planning.inputs-frozen" || freeze.StateFrom != domain.StatePlanned || freeze.StateTo != domain.StateReady ||
		freeze.Payload["specDigest"] != state.SpecDigest || freeze.Payload["policyDigest"] != state.PolicyDigest || freeze.Payload["capabilityDigest"] != state.CapabilityDigest || freeze.Payload["baseSha"] != state.BaseSHA || freeze.Payload["worktreePath"] != state.WorktreePath {
		return fail()
	}
	return nil
}

// ObserveColdTaskVerifications runs once before publishing the server. It
// only records denial hints: a previously VERIFYING Run has no durable proof
// that a crashed verifier command completed. REVIEW_PENDING carries its real
// completed verification and is not held. The hints cannot grant a Decision.
func (s *RepositorySession) ObserveColdTaskVerifications(ctx context.Context) error {
	borrow, err := s.borrow()
	if err != nil {
		return err
	}
	cold := map[string]bool{}
	err = (repositoryApprovedTeamVerifier{session: s}).WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plans, e := s.ingress.ListTeamPlans(s.acquisition.Scope)
		if e != nil {
			return e
		}
		for _, plan := range plans {
			if plan.Approval.TaskDraftDigest == "" {
				continue
			}
			for _, node := range plan.Materializations {
				lease, e := s.runs.AcquireExisting(node.RunID)
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					return e
				}
				read, e := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
				e = errors.Join(e, lease.Release())
				if e != nil {
					return e
				}
				if read.Run.State == domain.StateVerifying {
					cold[node.RunID] = true
				}
			}
		}
		return nil
	})
	borrow.Close()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.coldTaskVerifications != nil {
		return application.NewError("cancel-task-cold", application.ReasonAuthorityConflict)
	}
	s.coldTaskVerifications = cold
	return nil
}
