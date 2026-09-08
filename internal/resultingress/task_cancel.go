package resultingress

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const taskStopFactType = "task-stop-requested"
const taskCancelledFactType = "task-cancellation-disposed"
const taskCancelProtocol = "bounded-task-cancel/v1"

var ErrTaskStopped = errors.New("resultingress: task stop requested")
var ErrTaskCancelTooLate = errors.New("resultingress: task already completed")

// TaskStop is an immutable command, not a Run terminal or a refund. Revision
// concerns Task control commands; Run progress retains its own sequence/head.
type TaskStop struct {
	TaskID           string `json:"taskId"`
	DraftFactDigest  string `json:"draftFactDigest"`
	PlanFactDigest   string `json:"planFactDigest,omitempty"`
	RequestKeyDigest string `json:"requestKeyDigest"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestedAt      string `json:"requestedAt"`
	FactDigest       string `json:"factDigest"`
}

// TaskNodeDisposition preserves the actual Run head. It grants neither Run
// success nor permission to reuse an execution directory. The production
// verifier holds all extant Run leases through the same-store append.
type TaskNodeDisposition struct {
	NodeID                      string       `json:"nodeId"`
	RunID                       string       `json:"runId"`
	CreationFactDigest          string       `json:"creationFactDigest,omitempty"`
	RunHead                     string       `json:"runHead,omitempty"`
	RunSequence                 uint64       `json:"runSequence,omitempty"`
	RunState                    domain.State `json:"runState,omitempty"`
	Disposition                 string       `json:"disposition"`
	ReservationFactDigest       string       `json:"reservationFactDigest,omitempty"`
	ReservationResolutionDigest string       `json:"reservationResolutionDigest,omitempty"`
	AttemptHead                 string       `json:"attemptHead,omitempty"`
	CleanupReleasedDigest       string       `json:"cleanupReleasedDigest,omitempty"`
}

type TaskCancellation struct {
	TaskID         string                `json:"taskId"`
	StopFactDigest string                `json:"stopFactDigest"`
	PlanFactDigest string                `json:"planFactDigest,omitempty"`
	Nodes          []TaskNodeDisposition `json:"nodes"`
	FactDigest     string                `json:"factDigest"`
}

type CurrentTaskCancellationVerifier interface {
	// Evidence is produced from current Run journals and existing cleanup or
	// zero-side-effect contracts under held ownership, never supplied by HTTP.
	WithCurrentTaskCancellation(context.Context, ControlOwnerAcquisition, TaskStop, func(TaskCancellation) error) error
}

type taskCancelFact struct {
	ProtocolRevision string            `json:"protocolRevision"`
	FactType         string            `json:"factType"`
	Sequence         int64             `json:"sequence"`
	Scope            ControlOwnerScope `json:"scope"`
	OwnerFactDigest  string            `json:"ownerFactDigest"`
	Stop             *TaskStop         `json:"stop,omitempty"`
	Cancellation     *TaskCancellation `json:"cancellation,omitempty"`
	Digest           string            `json:"digest"`
}

func taskControlRevision(in *Ingress, key string) int64 {
	draft, found := currentTaskProposal(in, key)
	if !found {
		return 0
	}
	revision := draft.Revision
	if _, found := in.teamPlans[key]; found {
		revision++
	}
	if _, found := in.taskStops[key]; found {
		revision++
	}
	if _, found := in.taskCancellations[key]; found {
		revision++
	}
	return revision
}

func validateTaskStop(in *Ingress, scope ControlOwnerScope, stop TaskStop) error {
	key := teamPlanKey(scope, stop.TaskID)
	draft, found := currentTaskProposal(in, key)
	if !found || stop.DraftFactDigest != draft.RootFactDigest || requireDigest("cancel key", stop.RequestKeyDigest) != nil || stop.ExpectedRevision != taskControlRevision(in, key) {
		return ErrTeamPlanConflict
	}
	at, err := time.Parse(time.RFC3339Nano, stop.RequestedAt)
	if err != nil || at.IsZero() || at.UTC().Format(time.RFC3339Nano) != stop.RequestedAt {
		return ErrTeamPlanConflict
	}
	if _, found := in.teamOutcomes[key]; found {
		return ErrTaskCancelTooLate
	}
	plan := in.teamPlans[key]
	if stop.PlanFactDigest != plan.FactDigest {
		return ErrTeamPlanConflict
	}
	return nil
}

func (s *DurableStore) RequestTaskStop(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, taskID, keyDigest string, revision int64) (result TaskStop, err error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || domain.ValidateID(taskID) != nil || requireDigest("cancel key", keyDigest) != nil || revision < 1 {
		return result, ErrTeamPlanConflict
	}
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier, TeamPlanApproval{}}, owner, func() error {
		in := newAuthorityProjection()
		return s.transact(in, func() error {
			ownerKey, _ := owner.Scope.key()
			current, found := in.controlOwners[ownerKey]
			if !found || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, taskID)
			if old, found := in.taskStops[key]; found {
				if old.RequestKeyDigest != keyDigest || old.ExpectedRevision != revision {
					return ErrTeamPlanConflict
				}
				result = old
				return nil // Exact replay precedes current revision CAS.
			}
			proposal, _ := currentTaskProposal(in, key)
			value := TaskStop{TaskID: taskID, DraftFactDigest: proposal.RootFactDigest, PlanFactDigest: in.teamPlans[key].FactDigest, RequestKeyDigest: keyDigest, ExpectedRevision: revision, RequestedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := validateTaskStop(in, owner.Scope, value); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &taskCancelFact{ProtocolRevision: taskCancelProtocol, FactType: taskStopFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Stop: &value}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			value.FactDigest = fact.Digest
			in.taskStops[key] = value
			result = value
			return nil
		})
	})
	return
}

func (s *DurableStore) ReadTaskCancellation(scope ControlOwnerScope, taskID string) (stop TaskStop, disposition TaskCancellation, revision int64, err error) {
	if scope.Validate() != nil || domain.ValidateID(taskID) != nil {
		return stop, disposition, 0, ErrTeamPlanConflict
	}
	in := newAuthorityProjection()
	err = s.transact(in, func() error {
		key := teamPlanKey(scope, taskID)
		stop = in.taskStops[key]
		disposition = in.taskCancellations[key]
		revision = taskControlRevision(in, key)
		return nil
	})
	return
}

// RequireTaskNotStopped is a fence, not authorization. Old non-Task plans
// remain governed by their original contracts.
func (s *DurableStore) RequireTaskNotStopped(scope ControlOwnerScope, taskID string) error {
	stop, _, _, err := s.ReadTaskCancellation(scope, taskID)
	if err != nil {
		return err
	}
	if stop.FactDigest != "" {
		return ErrTaskStopped
	}
	return nil
}

// NextTaskStop selects among pending intents, not the first page of all Task
// history. The cursor is a disposable fairness hint, never authority.
func (s *DurableStore) NextTaskStop(scope ControlOwnerScope, after string) (result TaskStop, err error) {
	if scope.Validate() != nil || after != "" && domain.ValidateID(after) != nil {
		return result, ErrTeamPlanConflict
	}
	in := newAuthorityProjection()
	err = s.transact(in, func() error {
		var first TaskStop
		for key, stop := range in.taskStops {
			if key != teamPlanKey(scope, stop.TaskID) {
				continue
			}
			if _, done := in.taskCancellations[key]; done {
				continue
			}
			if first.TaskID == "" || stop.TaskID < first.TaskID {
				first = stop
			}
			if stop.TaskID > after && (result.TaskID == "" || stop.TaskID < result.TaskID) {
				result = stop
			}
		}
		if result.TaskID == "" {
			result = first
		}
		return nil
	})
	return
}

func requireTaskRunNotStopped(in *Ingress, namespace authority.AuthorityNamespaceId, runID string) error {
	for key, plan := range in.teamPlans {
		if _, stopped := in.taskStops[key]; !stopped {
			continue
		}
		scope := taskProposalScope(in, key)
		if !scope.AuthorityNamespaceID.Equal(namespace) {
			continue
		}
		for _, node := range plan.Materializations {
			if node.RunID == runID {
				return ErrTaskStopped
			}
		}
	}
	return nil
}

func (s *DurableStore) RequireTaskRunNotStopped(namespace authority.AuthorityNamespaceId, runID string) error {
	in := newAuthorityProjection()
	return s.transact(in, func() error { return requireTaskRunNotStopped(in, namespace, runID) })
}

// TaskRunExecution reads all historical reservation/Attempt records in this
// namespace. Absence means no record, not merely no currently active Attempt.
func (s *DurableStore) TaskRunExecution(namespace authority.AuthorityNamespaceId, runID string) (reservations []AttemptReservationState, attempts []AttemptAuthorityState, err error) {
	in := newAuthorityProjection()
	err = s.transact(in, func() error { reservations, attempts = taskRunExecution(in, namespace, runID); return nil })
	return
}

func taskRunExecution(in *Ingress, namespace authority.AuthorityNamespaceId, runID string) ([]AttemptReservationState, []AttemptAuthorityState) {
	var reservations []AttemptReservationState
	var attempts []AttemptAuthorityState
	for _, r := range in.reservations {
		if r.Reservation.Ready.RunID == runID && r.Reservation.Ready.AuthorityNamespaceID.Equal(namespace) {
			reservations = append(reservations, r)
		}
	}
	for _, a := range in.attempts {
		if a.Identity.RunID == runID && a.Identity.AuthorityNamespaceID.Equal(namespace) {
			attempts = append(attempts, a)
		}
	}
	return reservations, attempts
}

func validateTaskCancellation(in *Ingress, scope ControlOwnerScope, value TaskCancellation) error {
	key := teamPlanKey(scope, value.TaskID)
	stop, exists := in.taskStops[key]
	if !exists || stop.FactDigest != value.StopFactDigest || stop.PlanFactDigest != value.PlanFactDigest {
		return ErrTeamPlanConflict
	}
	plan, planned := in.teamPlans[key]
	if !planned {
		if len(value.Nodes) != 0 {
			return ErrTeamPlanConflict
		}
		return nil
	}
	if len(value.Nodes) != len(plan.Materializations) {
		return ErrTeamPlanConflict
	}
	seen := map[string]bool{}
	for _, node := range value.Nodes {
		if seen[node.NodeID] {
			return ErrTeamPlanConflict
		}
		seen[node.NodeID] = true
		member := false
		for _, m := range plan.Materializations {
			if node.NodeID == m.NodeID && node.RunID == m.RunID {
				member = true
			}
		}
		if !member {
			return ErrTeamPlanConflict
		}
		creation, created := in.teamRunCreations[teamRunCreationKey(scope, value.TaskID, node.NodeID)]
		if node.CreationFactDigest != creation.FactDigest {
			return ErrTeamPlanConflict
		}
		reservations, attempts := taskRunExecution(in, scope.AuthorityNamespaceID, node.RunID)
		switch node.Disposition {
		case "never-materialized":
			if !created || node.RunHead != "" || len(reservations) != 0 || len(attempts) != 0 {
				return ErrTeamPlanConflict
			}
		case "not-created":
			if created || node.RunHead != "" || len(reservations) != 0 || len(attempts) != 0 {
				return ErrTeamPlanConflict
			}
		case "never-started":
			if !created || node.RunState != domain.StateReady || node.RunSequence != 2 || requireDigest("ready head", node.RunHead) != nil || len(reservations) != 0 || len(attempts) != 0 {
				return ErrTeamPlanConflict
			}
		case "reservation-cancelled":
			if !created || node.RunState != domain.StateReady || node.RunSequence != 2 || len(reservations) != 1 || len(attempts) != 0 {
				return ErrTeamPlanConflict
			}
			r := reservations[0]
			if r.Status != AttemptReservationCancelled || node.RunHead != r.Reservation.Ready.ReadyAuthorityHead || node.ReservationFactDigest != r.ReservationFactDigest || node.ReservationResolutionDigest != r.ResolutionFactDigest || requireZeroAttemptProjection(in, r) != nil {
				return ErrTeamPlanConflict
			}
		case "execution-cleaned":
			if !created || len(attempts) != 1 || requireDigest("run head", node.RunHead) != nil || node.RunSequence < 3 || (node.RunState != domain.StateVerifying && node.RunState != domain.StateReviewPending && !node.RunState.Terminal()) {
				return ErrTeamPlanConflict
			}
			a := attempts[0]
			if a.HeadDigest != node.AttemptHead || a.CleanupReleasedDigest == "" || a.CleanupReleasedDigest != node.CleanupReleasedDigest || a.ProcessTerminalDigest == "" || a.AllocationTerminalDigest == "" || a.SupervisorClosedDigest == "" || a.PendingEffectIntentFactDigest != "" {
				return ErrTeamPlanConflict
			}
		default:
			return ErrTeamPlanConflict
		}
	}
	return nil
}

func (s *DurableStore) RecordTaskCancellation(ctx context.Context, verifier CurrentTaskCancellationVerifier, owner ControlOwnerAcquisition, taskID string) (result TaskCancellation, err error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil {
		return result, ErrTeamPlanConflict
	}
	stop, _, _, err := s.ReadTaskCancellation(owner.Scope, taskID)
	if err != nil {
		return result, err
	}
	if stop.FactDigest == "" {
		return result, ErrTeamPlanConflict
	}
	entered := false
	err = verifier.WithCurrentTaskCancellation(ctx, owner, stop, func(value TaskCancellation) error {
		if entered || value.TaskID != taskID || value.FactDigest != "" {
			return ErrTeamPlanConflict
		}
		entered = true
		value.Nodes = slices.Clone(value.Nodes)
		in := newAuthorityProjection()
		return s.transact(in, func() error {
			ownerKey, _ := owner.Scope.key()
			current, found := in.controlOwners[ownerKey]
			if !found || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			if err := validateTaskCancellation(in, owner.Scope, value); err != nil {
				return err
			}
			key := teamPlanKey(owner.Scope, taskID)
			if old, found := in.taskCancellations[key]; found {
				comparison := old
				comparison.FactDigest = ""
				if canonicalDigestOrEmpty(comparison) != canonicalDigestOrEmpty(value) {
					return ErrTeamPlanConflict
				}
				result = old
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &taskCancelFact{ProtocolRevision: taskCancelProtocol, FactType: taskCancelledFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Cancellation: &value}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			value.FactDigest = fact.Digest
			in.taskCancellations[key] = value
			result = value
			return nil
		})
	})
	if err == nil && !entered {
		err = ErrTeamPlanConflict
	}
	return
}

func applyTaskCancelLine(line []byte, in *Ingress, sequence int64) error {
	var fact taskCancelFact
	if len(line) > 16384 || decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != taskCancelProtocol || fact.Sequence != sequence {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("cancel fact", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, found := in.controlOwners[ownerKey]
	if !found || owner.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	switch fact.FactType {
	case taskStopFactType:
		if fact.Stop == nil || fact.Cancellation != nil || fact.Stop.FactDigest != "" {
			return ErrTeamPlanConflict
		}
		key := teamPlanKey(fact.Scope, fact.Stop.TaskID)
		if _, found := in.taskStops[key]; found {
			return ErrTeamPlanConflict
		}
		if err := validateTaskStop(in, fact.Scope, *fact.Stop); err != nil {
			return err
		}
		fact.Stop.FactDigest = digest
		in.taskStops[key] = *fact.Stop
	case taskCancelledFactType:
		if fact.Cancellation == nil || fact.Stop != nil || fact.Cancellation.FactDigest != "" {
			return ErrTeamPlanConflict
		}
		key := teamPlanKey(fact.Scope, fact.Cancellation.TaskID)
		if _, found := in.taskCancellations[key]; found {
			return ErrTeamPlanConflict
		}
		if err := validateTaskCancellation(in, fact.Scope, *fact.Cancellation); err != nil {
			return err
		}
		fact.Cancellation.FactDigest = digest
		in.taskCancellations[key] = *fact.Cancellation
	default:
		return ErrTeamPlanConflict
	}
	return nil
}
