//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/taskhttp"
)

func taskCancelSessionFixture(t *testing.T) (publicFixedDeliveryInputs, *RepositorySession) {
	t.Helper()
	fixture, _, _, _ := materializationFixture(t)
	ns := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: fixture.repository}
	digest, err := ns.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.Acquisition.Scope.AuthorityNamespaceID = ns
	fixture.inputs.Acquisition.Scope.RepositoryIdentityDigest = digest
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TaskTemplate, err = planning.OpenTaskTemplate(repositoryTaskTemplate(t, fixture), validator)
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TeamInputPreflight = func(raw []byte) error { _, err := planning.PreviewTeamInputs(raw, validator); return err }
	s, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return fixture, s
}

func taskCancelHTTP(t *testing.T, s *RepositorySession, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	a, err := s.OpenFixedEndpointAuthority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h, err := taskhttp.NewHandler(taskhttp.HandlerConfig{Application: s, Host: "127.0.0.1:1234", Token: strings.Repeat("1", 64), Recheck: a.Recheck, Mutation: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if body != nil {
		raw = repositoryTaskJSON(t, body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Host = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("1", 64))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func taskCancelCreate(t *testing.T, s *RepositorySession) application.TaskProjection {
	t.Helper()
	w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks", "create-cancel", goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "构建订单报价 API 与客户端"})
	var task application.TaskProjection
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return task
}

func taskCancelApprove(t *testing.T, s *RepositorySession, task application.TaskProjection) application.TaskProjection {
	t.Helper()
	w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/approve", "approve-cancel", application.ApproveTaskRequest{ExpectedRevision: task.Revision, PreviewDigest: task.PreviewDigest})
	var approved application.TaskProjection
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &approved) != nil {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	return approved
}

func taskCancelTwoApproved(t *testing.T, s *RepositorySession) (application.TaskProjection, application.TaskProjection) {
	t.Helper()
	var tasks []application.TaskProjection
	for _, key := range []string{"fair-first", "fair-second"} {
		value, err := s.CreateTask(context.Background(), application.CreateTaskRequest{IdempotencyKey: key, Submission: goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "构建订单报价 API 与客户端"}})
		if err != nil {
			t.Fatal(err)
		}
		value, err = s.ApproveTask(context.Background(), application.ApproveTaskRequest{TaskID: value.ID, IdempotencyKey: key + "-approve", ExpectedRevision: value.Revision, PreviewDigest: value.PreviewDigest})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, value)
	}
	if tasks[0].ID > tasks[1].ID {
		tasks[0], tasks[1] = tasks[1], tasks[0]
	}
	return tasks[0], tasks[1]
}

func TestRepositoryTaskCancelLocalReadFailureDoesNotHideNextTask(t *testing.T) {
	ctx := context.Background()
	fixture, s := taskCancelSessionFixture(t)
	broken, healthy := taskCancelTwoApproved(t, s)
	created, err := s.MaterializeApprovedInitialTeamRun(ctx, broken.ID, "service", broken.Approval.FactDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []application.TaskProjection{broken, healthy} {
		if _, err := s.CancelTask(ctx, application.CancelTaskRequest{TaskID: task.ID, IdempotencyKey: "stop-" + task.ID, ExpectedRevision: task.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	// Only this fixture's Run snapshot is broken; owner and RB1 remain valid.
	statePath := filepath.Join(fixture.repository, ".marshal", "runs", created.RunID, "state.json")
	original, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(statePath, original, 0o600); err != nil {
			t.Error(err)
		}
	})
	id, _, err := s.PendingTaskCancellation(ctx, "")
	if err == nil || id != broken.ID {
		t.Fatalf("missing selected local failure: %s %v", id, err)
	}
	next, _, err := s.PendingTaskCancellation(ctx, id)
	if err != nil || next != healthy.ID {
		t.Fatalf("healthy sibling starved: %s %v", next, err)
	}
	if err := s.FinishTaskCancellation(ctx, next); err != nil {
		t.Fatal("healthy sibling failed disposition", err)
	}
	if _, done, _, err := s.ingress.ReadTaskCancellation(s.acquisition.Scope, broken.ID); err != nil || done.FactDigest != "" {
		t.Fatal("broken Task was falsely closed")
	}
}

func TestRepositoryTaskCancelFinalizerSkipsStoppedAndHealthyDispatchContinues(t *testing.T) {
	ctx := context.Background()
	_, s := taskCancelSessionFixture(t)
	stopped, healthy := taskCancelTwoApproved(t, s)
	if _, err := s.CancelTask(ctx, application.CancelTaskRequest{TaskID: stopped.ID, IdempotencyKey: "stop-finalizer", ExpectedRevision: stopped.Revision}); err != nil {
		t.Fatal(err)
	}
	plan, found, err := s.ingress.ReadTeamPlan(s.acquisition.Scope, stopped.ID)
	if err != nil || !found {
		t.Fatal("plan missing", err)
	}
	_, err = s.ingress.CompleteTeam(ctx, repositoryCompletedTeamVerifier{s}, s.acquisition, plan.Approval, stopped.ID, plan.FactDigest)
	if !errors.Is(err, resultingress.ErrTaskStopped) {
		t.Fatal("current-owner finalizer did not classify the exact stopped Task", err)
	}
	stale := s.acquisition
	stale.OwnerEpoch++
	err = (repositoryCompletedTeamVerifier{s}).WithCurrentCompletedTeam(ctx, stale, plan.Approval, stopped.ID, plan.FactDigest, func(resultingress.TeamDeliveryOutcome) error { t.Fatal("stale owner callback"); return nil })
	if err == nil || errors.Is(err, resultingress.ErrTaskStopped) {
		t.Fatal("owner failure was classified as Task stop", err)
	}
	if err := s.FinalizeReadyInitialTeams(ctx); err != nil {
		t.Fatal("stopped Task poisoned resident finalization", err)
	}
	selected, found, err := s.NextInitialTeamDispatch(ctx, 2)
	if err != nil || !found || selected.GoalID != healthy.ID {
		t.Fatal("healthy sibling not dispatched", err)
	}
	created, err := s.MaterializeApprovedInitialTeamRun(ctx, selected.GoalID, selected.NodeID, selected.PlanFactDigest)
	if err != nil || created.RunID != selected.RunID {
		t.Fatal("healthy sibling did not reach original materialization", err)
	}
	if _, found, err := s.ingress.ReadTeamOutcome(s.acquisition.Scope, stopped.ID); err != nil || found {
		t.Fatal("stopped Task gained an Outcome")
	}
}

func TestRepositoryTaskCancelHTTPResidentDispositionColdReplay(t *testing.T) {
	for _, mode := range []string{"draft", "approved", "ready", "reservation", "missing-reservation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			fixture, s := taskCancelSessionFixture(t)
			task := taskCancelCreate(t, s)
			original := task
			if mode != "draft" {
				task = taskCancelApprove(t, s, task)
				if task.Revision != 2 {
					t.Fatal("control revision did not advance")
				}
			}
			var ready application.RunProjection
			var leaseLedger *dispatch.LeaseLedger
			if mode == "ready" || mode == "reservation" || mode == "missing-reservation" {
				created, err := s.MaterializeApprovedInitialTeamRun(ctx, task.ID, "service", task.Approval.FactDigest)
				if err != nil {
					t.Fatal(err)
				}
				ready, err = s.InspectRun(ctx, application.InspectRunRequest{RunID: created.RunID})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "reservation" || mode == "missing-reservation" {
					lease, err := s.runs.AcquireExisting(ready.RunID)
					if err != nil {
						t.Fatal(err)
					}
					read, err := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
					if err != nil {
						t.Fatal(err)
					}
					r := resultingress.ReadyRunAuthority{AuthorityNamespaceID: s.acquisition.Scope.AuthorityNamespaceID, TaskID: read.Run.TaskID, RunID: read.Run.RunID, OrchestratorID: "orchestrator-cancel-fixture", ReadySequence: read.Run.Sequence, ReadyAuthorityHead: read.Run.AuthorityHead, AttemptsUsed: read.AttemptsUsed, MaxAttempts: read.MaxAttempts, SpecDigest: read.SpecDigest, PolicyDigest: read.PolicyDigest, CapabilityDigest: read.CapabilityDigest, BaseSHA: read.BaseSHA, WorktreePath: read.WorktreePath}
					verifier, err := runstore.NewAttemptRunAuthorityVerifier(s.runs, lease, r.AuthorityNamespaceID, r.OrchestratorID)
					if err != nil {
						t.Fatal(err)
					}
					_, err = s.ingress.ReserveAttempt(ctx, verifier, r)
					if err != nil {
						t.Fatal(err)
					}
					if err := lease.Release(); err != nil {
						t.Fatal(err)
					}
					_, dispatchPath, _, _ := CompositionPaths(filepath.Join(fixture.repository, ".marshal"))
					leaseLedger, err = dispatch.NewLeaseLedger(dispatchPath)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			body := application.CancelTaskRequest{ExpectedRevision: task.Revision}
			path := "/v1/tasks/" + task.ID + "/cancel"
			if mode != "draft" {
				bad := taskCancelHTTP(t, s, http.MethodPost, path, "cancel-once", application.CancelTaskRequest{ExpectedRevision: 1})
				if bad.Code != 409 {
					t.Fatalf("stale CAS: %d", bad.Code)
				}
			}
			w := taskCancelHTTP(t, s, http.MethodPost, path, "cancel-once", body)
			var stopping application.TaskProjection
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &stopping) != nil || stopping.Status != "cancelling" || !stopping.CancellationRequested {
				t.Fatalf("intent: %d %s", w.Code, w.Body.String())
			}
			if mode == "draft" {
				if w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/approve", "approve-late", application.ApproveTaskRequest{ExpectedRevision: 1, PreviewDigest: original.PreviewDigest}); w.Code != 409 {
					t.Fatalf("late approve: %d", w.Code)
				}
			} else {
				// Exact original confirmation remains recoverable after stop.
				if w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/approve", "approve-cancel", application.ApproveTaskRequest{ExpectedRevision: 1, PreviewDigest: original.PreviewDigest}); w.Code != 202 {
					t.Fatalf("approve replay: %d %s", w.Code, w.Body.String())
				}
			}
			id, _, err := s.PendingTaskCancellation(ctx)
			if err != nil || id != task.ID {
				t.Fatalf("resident selector: %s %v", id, err)
			}
			if mode == "reservation" {
				// Active reservation is an obligation, not proof of no launch.
				if err := s.FinishTaskCancellation(ctx, id); err == nil {
					t.Fatal("active reservation falsely closed")
				}
				if err := s.CancelTaskReadyReservation(ctx, ready.RunID, leaseLedger); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-reservation" {
				runPath := filepath.Join(fixture.repository, ".marshal", "runs", ready.RunID)
				saved := filepath.Join(fixture.repository, ".marshal", "runs", "saved-cancel-run")
				if err := os.Rename(runPath, saved); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Rename(saved, runPath); err != nil {
						t.Error(err)
					}
				}()
				if err := s.FinishTaskCancellation(ctx, id); !application.HasReason(err, application.ReasonRecoveryRequired) {
					t.Fatal("missing Run with live reservation did not require recovery", err)
				}
				_, done, _, err := s.ingress.ReadTaskCancellation(s.acquisition.Scope, id)
				if err != nil || done.FactDigest != "" {
					t.Fatal("missing Run minted disposition")
				}
				return
			}
			if err := s.FinishTaskCancellation(ctx, id); err != nil {
				t.Fatal(err)
			}
			done, err := s.ReadTask(ctx, id)
			if err != nil || done.Status != "cancelled" || !done.CancellationRequested || done.Delivery != nil {
				t.Fatalf("closed query: %+v %v", done, err)
			}
			if ready.RunID != "" {
				current, err := s.InspectRun(ctx, application.InspectRunRequest{RunID: ready.RunID})
				if err != nil || current != ready {
					t.Fatal("cancellation rewrote READY or budget")
				}
			}
			_, before, _, err := s.ingress.ReadTaskCancellation(s.acquisition.Scope, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenRepositorySession(ctx, fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			w = taskCancelHTTP(t, s, http.MethodPost, path, "cancel-once", body)
			if w.Code != 202 {
				t.Fatalf("cold lost response: %d %s", w.Code, w.Body.String())
			}
			_, after, _, err := s.ingress.ReadTaskCancellation(s.acquisition.Scope, id)
			if err != nil || before.FactDigest != after.FactDigest {
				t.Fatal("cold replay changed disposition")
			}
		})
	}
}

func TestRepositoryTaskCancelDoesNotCallVerifyOrInventMissingCleanup(t *testing.T) {
	ctx := context.Background()
	fixture, s := taskCancelSessionFixture(t)
	task := taskCancelApprove(t, s, taskCancelCreate(t, s))
	if err := s.ObserveColdTaskVerifications(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := s.MaterializeApprovedInitialTeamRun(ctx, task.ID, "service", task.Approval.FactDigest)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := s.InspectRun(ctx, application.InspectRunRequest{RunID: created.RunID})
	if err != nil {
		t.Fatal(err)
	}
	f := fixedDeliveryFixture{repository: fixture.repository, session: s, request: application.StartRunRequest{RunID: ready.RunID, ExpectedSequence: ready.Sequence, ExpectedAuthorityHead: ready.AuthorityHead}}
	running := advanceFixedDeliveryRunToRunningWithBudget(t, f, 1)
	advanceFixedDeliveryRunToVerifying(t, f, running) // Run journal only; deliberately no RB1 cleanup receipt.
	lease, err := s.runs.AcquireExisting(ready.RunID)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	creation, _, err := s.ingress.ReadTeamRunCreation(s.acquisition.Scope, task.ID, "service")
	if err != nil {
		t.Fatal(err)
	}
	if err := taskCancellationFrozenRun(lease, creation, read.Run); err != nil {
		t.Fatal("negative failed before cleanup gate", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	_, err = s.CancelTask(ctx, application.CancelTaskRequest{TaskID: task.ID, IdempotencyKey: "cancel-verify", ExpectedRevision: task.Revision})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.WithTaskRunNotStopped(ctx, ready.RunID, func() error { called = true; return nil }); !application.HasReason(err, application.ReasonRunStopped) || called {
		t.Fatalf("late Verify commit admitted: %v", err)
	}
	if err := s.FinishTaskCancellation(ctx, task.ID); !application.HasReason(err, application.ReasonRecoveryRequired) {
		t.Fatal("journal state without cleanup did not reach recovery gate", err)
	}
	current, err := s.ReadTask(ctx, task.ID)
	if err != nil || current.Status != "cancelling" {
		t.Fatalf("unknown not preserved: %v", err)
	}
	if selection, found, err := s.NextInitialTeamProgress(ctx, "", domain.StateVerifying); err != nil || found {
		t.Fatalf("stop admitted Verify: %+v %v", selection, err)
	}
	if _, err := s.MaterializeApprovedInitialTeamRun(ctx, task.ID, "client", task.Approval.FactDigest); err == nil {
		t.Fatal("stop materialized sibling")
	}
	if !errors.Is(s.ingress.RequireTaskNotStopped(s.acquisition.Scope, task.ID), resultingress.ErrTaskStopped) {
		t.Fatal("stop fence vanished")
	}
}

// This is a source-availability/forgery regression, not an executed Worker or
// cleanup receipt. Projection-only later states deliberately carry no RB1
// Attempt and therefore cannot pass FinishTaskCancellation.
func TestRepositoryTaskCancelFrozenSourcesAcrossStates(t *testing.T) {
	for _, target := range []domain.State{domain.StateReady, domain.StateRunning, domain.StateBlocked, domain.StateVerifying, domain.StateReviewPending, domain.StateAccepted} {
		t.Run(string(target), func(t *testing.T) {
			ctx := context.Background()
			fixture, s := taskCancelSessionFixture(t)
			task := taskCancelApprove(t, s, taskCancelCreate(t, s))
			created, err := s.MaterializeApprovedInitialTeamRun(ctx, task.ID, "service", task.Approval.FactDigest)
			if err != nil {
				t.Fatal(err)
			}
			ready, err := s.InspectRun(ctx, application.InspectRunRequest{RunID: created.RunID})
			if err != nil {
				t.Fatal(err)
			}
			f := fixedDeliveryFixture{repository: fixture.repository, session: s, request: application.StartRunRequest{RunID: ready.RunID, ExpectedSequence: ready.Sequence, ExpectedAuthorityHead: ready.AuthorityHead}}
			if target != domain.StateReady {
				advanceFixedDeliveryRunToRunningWithBudget(t, f, 1)
			}
			lease, err := s.runs.AcquireExisting(created.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			state, err := runstore.InspectUnderLease(lease)
			if err != nil {
				t.Fatal(err)
			}
			path := []domain.State{target}
			if target == domain.StateReviewPending {
				path = []domain.State{domain.StateVerifying, domain.StateReviewPending}
			} else if target == domain.StateAccepted {
				path = []domain.State{domain.StateVerifying, domain.StateReviewPending, domain.StateAccepted}
			}
			for _, next := range path {
				if state.State == next {
					continue
				}
				event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event-source-projection-" + strings.ToLower(string(next)), RunID: state.RunID, AttemptID: state.CurrentAttemptID, Sequence: state.Sequence + 1, Type: "run.transition", StateFrom: state.State, StateTo: next, Timestamp: time.Now().UTC(), Payload: map[string]any{}}
				if err := s.runs.Append(lease, event, state.Sequence); err != nil {
					t.Fatal(err)
				}
				state.Sequence, state.State = event.Sequence, next
				if err := s.runs.WriteSnapshot(lease, state); err != nil {
					t.Fatal(err)
				}
			}
			read, err := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			creation, _, err := s.ingress.ReadTeamRunCreation(s.acquisition.Scope, task.ID, "service")
			if err != nil {
				t.Fatal(err)
			}
			if target != domain.StateReady && target != domain.StateRunning && (read.SpecDigest != "" || read.PolicyDigest != "" || read.CapabilityDigest != "" || read.BaseSHA != "") {
				t.Fatal("test no longer exercises sparse projection")
			}
			if err := taskCancellationFrozenRun(lease, creation, read.Run); err != nil {
				t.Fatal("original frozen source rejected", err)
			}
			for _, name := range []string{"task-spec.json", "policy-snapshot.json", "capability-snapshot.json"} {
				path := filepath.Join(fixture.repository, ".marshal", "runs", created.RunID, name)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(bytes.Clone(raw), '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := taskCancellationFrozenRun(lease, creation, read.Run); err == nil {
					t.Fatal("changed frozen bytes accepted", name)
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			drift := state
			drift.BaseSHA = strings.Repeat("b", 40)
			if err := s.runs.WriteSnapshot(lease, drift); err != nil {
				t.Fatal(err)
			}
			if err := taskCancellationFrozenRun(lease, creation, read.Run); err == nil {
				t.Fatal("changed snapshot accepted")
			}
			if err := s.runs.WriteSnapshot(lease, state); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// These values test the exact event-consumer join only. stopEventPayload is
// the original producer formatter; the state is explicitly synthetic and is
// never written into RB1 or offered as completed Task cancellation evidence.
func TestRepositoryTaskCancelCleanupEventProducerShapes(t *testing.T) {
	attempt := stoppedAttemptFixture(t)
	current := application.RunProjection{TaskID: attempt.Identity.TaskID, RunID: attempt.Identity.RunID, AttemptID: attempt.Identity.AttemptID, State: domain.StateBlocked}
	payload, err := stopEventPayload(attempt)
	if err != nil {
		t.Fatal(err)
	}
	stopped := domain.RunEvent{RunID: current.RunID, AttemptID: current.AttemptID, Type: lifecycle.WorkerStoppedEventType, StateFrom: domain.StateRunning, StateTo: domain.StateBlocked, Timestamp: time.Now().UTC(), Actor: &domain.Actor{Type: "system", ID: "marshal-core"}, Payload: payload}
	if !taskCancellationCleanupEvent(stopped, current, attempt) {
		t.Fatal("original stopped formatter rejected")
	}
	completedAttempt := attempt
	completedAttempt.StopIntent = resultingress.AttemptStopIntent{}
	completedAttempt.CommittedResultFactDigest = canonical.DigestBytes([]byte("synthetic-admission"))
	completed := stopped
	completed.Type, completed.StateTo, completed.Actor = "worker.completed", domain.StateVerifying, &domain.Actor{Type: "system", ID: "marshal-production-runtime"}
	completed.Payload = map[string]any{"resultAdmissionFactDigest": completedAttempt.CommittedResultFactDigest, "terminalizationBarrierFactDigest": attempt.BarrierDigest, "processTerminalFactDigest": attempt.ProcessTerminalDigest, "allocationTerminatedFactDigest": attempt.AllocationTerminalDigest, "supervisorClosedFactDigest": attempt.SupervisorClosedDigest, "cleanupReleasedFactDigest": attempt.CleanupReleasedDigest}
	if !taskCancellationCleanupEvent(completed, current, completedAttempt) {
		t.Fatal("completed producer shape rejected")
	}
	for _, example := range []struct {
		event   domain.RunEvent
		attempt resultingress.AttemptAuthorityState
	}{{stopped, attempt}, {completed, completedAttempt}} {
		for _, key := range []string{"terminalizationBarrierFactDigest", "processTerminalFactDigest", "allocationTerminatedFactDigest", "supervisorClosedFactDigest", "cleanupReleasedFactDigest"} {
			bad := example.event
			bad.Payload = map[string]any{}
			for k, v := range example.event.Payload {
				bad.Payload[k] = v
			}
			delete(bad.Payload, key)
			if taskCancellationCleanupEvent(bad, current, example.attempt) {
				t.Fatal("missing joined evidence accepted", key)
			}
		}
		bad := example.event
		bad.Type = "unrelated.event"
		if taskCancellationCleanupEvent(bad, current, example.attempt) {
			t.Fatal("unrelated event accepted")
		}
		bad = example.event
		bad.Actor = &domain.Actor{Type: "worker", ID: "arbitrary"}
		if taskCancellationCleanupEvent(bad, current, example.attempt) {
			t.Fatal("worker-authored cleanup event accepted")
		}
		bad = example.event
		bad.RunID = "another-run"
		if taskCancellationCleanupEvent(bad, current, example.attempt) {
			t.Fatal("cross-Run event accepted")
		}
		badAttempt := example.attempt
		badAttempt.Identity.TaskID = "another-task"
		if taskCancellationCleanupEvent(example.event, current, badAttempt) {
			t.Fatal("cross-Task Attempt accepted")
		}
		badAttempt = example.attempt
		badAttempt.CleanupReleasedDigest = ""
		if taskCancellationCleanupEvent(example.event, current, badAttempt) {
			t.Fatal("missing current cleanup accepted")
		}
	}
}
