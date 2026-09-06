package productionruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func stopMatchesRequest(s resultingress.AttemptStopIntent, request application.CancelRunRequest) bool {
	return s.RequestID == request.RequestID && s.ExpectedSequence == request.ExpectedSequence && s.ExpectedAuthorityHead == request.ExpectedAuthorityHead
}

// CancelRun consumes only this composition's held Run lease and current
// owner. No request field can select an external process or another worktree.
func (l *CompositionLedger) CancelRun(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, request application.CancelRunRequest) (application.CancelRunProjection, error) {
	if request.Validate() != nil || l == nil || ctx == nil || ctx.Err() != nil || verifier == nil || acquisition.Validate() != nil {
		return application.CancelRunProjection{}, application.NewError("cancel-run", application.ReasonInvalidRequest)
	}
	if _, err := l.CurrentOwner(ctx, verifier, acquisition); err != nil {
		return application.CancelRunProjection{}, err
	}
	read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
	if err != nil || read.Run.RunID != request.RunID || read.Run.AttemptID != request.AttemptID {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	states, err := l.ingress.AttemptStates()
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	var attempt resultingress.AttemptAuthorityState
	matches := 0
	for _, state := range states {
		id := state.Identity
		if id.AuthorityNamespaceID == l.namespace && id.OrchestratorID == l.orchestrator && id.TaskID == read.Run.TaskID && id.RunID == request.RunID && id.AttemptID == request.AttemptID {
			attempt = state
			matches++
		}
	}
	if matches != 1 {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	if read.Run.State == domain.StateBlocked {
		return l.rehydrateStoppedRun(ctx, request, attempt)
	}
	if read.Run.State != domain.StateRunning || read.Run.Sequence != request.ExpectedSequence || read.Run.AuthorityHead != request.ExpectedAuthorityHead {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	if attempt.StopIntent != (resultingress.AttemptStopIntent{}) {
		if !stopMatchesRequest(attempt.StopIntent, request) || attempt.StopIntent.Category != resultingress.StopOperatorRequest {
			return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
		}
	} else {
		if attempt.CommittedResultFactDigest != "" {
			return application.CancelRunProjection{}, resultingress.ErrStopTooLate
		}
		intent, err := resultingress.SealAttemptStopIntent(attempt.Identity, resultingress.AttemptStopIntent{RequestID: request.RequestID, ExpectedSequence: request.ExpectedSequence, ExpectedAuthorityHead: request.ExpectedAuthorityHead, OperatorUID: acquisition.OwnerUID, ObservedAt: l.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Category: resultingress.StopOperatorRequest})
		if err != nil {
			return application.CancelRunProjection{}, err
		}
		barrier, err := l.ingress.CompareAndAppendBarrier(ctx, l, attempt.Revision, attempt.HeadDigest, resultingress.BarrierAuthorizationRequest{Identity: attempt.Identity, CurrentRunAuthority: runAuthorityForAttempt(attempt.Identity)}, resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionTerminalizationBarrier, Identity: attempt.Identity, TerminalizationID: intent.IntentDigest, EligibilityTerminal: intent.Eligibility(), StopIntent: intent})
		if err != nil {
			return application.CancelRunProjection{}, err
		}
		attempt = barrier.State
	}
	terminal, err := l.terminalizeAttemptAfterBarrier(ctx, verifier, acquisition, read, attempt)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	if err := l.commitStoppedRun(ctx, request, terminal); err != nil {
		return application.CancelRunProjection{}, err
	}
	return l.rehydrateStoppedRun(ctx, request, terminal)
}

func stopEventPayload(attempt resultingress.AttemptAuthorityState) (map[string]any, error) {
	if _, err := terminalEligibilityProjection(attempt); err != nil || attempt.StopIntent == (resultingress.AttemptStopIntent{}) || attempt.ProcessTerminalDigest == "" || attempt.AllocationTerminalDigest == "" || attempt.SupervisorClosedDigest == "" || attempt.CleanupCompletedDigest == "" || attempt.CleanupReleasedDigest == "" {
		return nil, resultingress.ErrCleanupUnauthorized
	}
	reason := ""
	switch attempt.StopIntent.Category {
	case resultingress.StopOperatorRequest:
		reason = lifecycle.AbortTerminalReason
	case resultingress.StopAttemptDeadline:
		reason = "attempt-deadline-exceeded"
	case resultingress.StopRunDeadline:
		reason = "run-deadline-exceeded"
	default:
		return nil, resultingress.ErrCleanupUnauthorized
	}
	return map[string]any{"stopIntentDigest": attempt.StopIntent.IntentDigest, "stopRequestDigest": attempt.StopIntent.RequestDigest, "originalRunAuthorityHead": attempt.StopIntent.ExpectedAuthorityHead, "terminalizationBarrierFactDigest": attempt.BarrierDigest, "processTerminalFactDigest": attempt.ProcessTerminalDigest, "allocationTerminatedFactDigest": attempt.AllocationTerminalDigest, "supervisorClosedFactDigest": attempt.SupervisorClosedDigest, "cleanupReleasedFactDigest": attempt.CleanupReleasedDigest, "terminalReason": reason}, nil
}

func (l *CompositionLedger) commitStoppedRun(ctx context.Context, request application.CancelRunRequest, terminal resultingress.AttemptAuthorityState) error {
	current, found, err := l.ingress.AttemptState(terminal.Identity)
	if err != nil || !found || current.HeadDigest != terminal.HeadDigest || !stopMatchesRequest(current.StopIntent, request) {
		return resultingress.ErrAttemptAuthorityConflict
	}
	payload, err := stopEventPayload(current)
	if err != nil {
		return err
	}
	read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
	if err != nil || read.Run.State != domain.StateRunning || read.Run.RunID != request.RunID || read.Run.AttemptID != request.AttemptID || read.Run.Sequence != request.ExpectedSequence || read.Run.AuthorityHead != request.ExpectedAuthorityHead {
		return resultingress.ErrAttemptAuthorityConflict
	}
	state, err := runstore.InspectUnderLease(l.runLease)
	if err != nil {
		return err
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return err
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: request.RunID, AttemptID: request.AttemptID, Sequence: state.Sequence + 1, Type: lifecycle.WorkerStoppedEventType, StateFrom: domain.StateRunning, StateTo: domain.StateBlocked, Timestamp: l.now().UTC(), Actor: &domain.Actor{Type: "system", ID: "marshal-core"}, Payload: payload}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, StopAuthorized: true, ChildrenStopped: true, EvidenceCurrent: true, EvidenceFlushed: true})
	if err != nil {
		return err
	}
	if err := l.runs.Append(l.runLease, event, state.Sequence); err != nil {
		return err
	}
	return l.runs.WriteSnapshot(l.runLease, next)
}

func (l *CompositionLedger) rehydrateStoppedRun(ctx context.Context, request application.CancelRunRequest, terminal resultingress.AttemptAuthorityState) (application.CancelRunProjection, error) {
	return rehydrateStoppedRunUnderLease(ctx, l.runs, l.runLease, l.ingress, request, terminal)
}

// Terminal response repair must not depend on a live Worker, launch closure,
// or still-existing worktree. The caller holds the Run lease and owner lock.
func rehydrateStoppedRunUnderLease(ctx context.Context, runs *runstore.Store, lease *runstore.Lease, ingress *resultingress.DurableStore, request application.CancelRunRequest, terminal resultingress.AttemptAuthorityState) (application.CancelRunProjection, error) {
	current, found, err := ingress.AttemptState(terminal.Identity)
	if err != nil || !found || current.HeadDigest != terminal.HeadDigest || !stopMatchesRequest(current.StopIntent, request) {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	terminal = current
	want, err := stopEventPayload(terminal)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	transition, err := runs.ReadCurrentRunTransitionUnderLease(ctx, lease, request.ExpectedSequence, request.ExpectedAuthorityHead)
	if err != nil || lifecycle.ValidateWorkerStopped(transition.Event) != nil || transition.After.RunID != request.RunID || transition.After.AttemptID != request.AttemptID {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	wantRaw, _ := json.Marshal(want)
	gotRaw, err := json.Marshal(transition.Event.Payload)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	wantDigest, err := canonical.DigestJSON(wantRaw)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	gotDigest, err := canonical.DigestJSON(gotRaw)
	if err != nil || wantDigest != gotDigest {
		return application.CancelRunProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	outcome := domain.OutcomeBundle{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome, TaskID: transition.After.TaskID, RunID: request.RunID, TerminalState: domain.StateBlocked, Verdict: "abort", FinalReviewRound: 1, FinalReviewDigest: gotDigest, FinalEvidenceDigest: gotDigest, Summary: want["terminalReason"].(string), RetentionPolicy: "default", GeneratedAt: transition.Event.Timestamp}
	raw, err := json.Marshal(outcome)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	if err := validator.Validate(domain.KindOutcome, raw); err != nil {
		return application.CancelRunProjection{}, err
	}
	directory, err := runstore.OpenDirectoryUnderLease(lease)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	defer directory.Close()
	if err := runstore.WriteFileInDirectory(directory, "outcome.json", raw, 0o600); err != nil {
		return application.CancelRunProjection{}, err
	}
	summary := []byte(fmt.Sprintf("# Run 停止结果\n\n终态：BLOCKED\n\n原因：%s\n\n停止事件证据：%s\n\n本结果不代表任务成功，也不代表存在独立 ReviewDecision。\n", outcome.Summary, gotDigest))
	if err := runstore.WriteFileInDirectory(directory, "result.md", summary, 0o600); err != nil {
		return application.CancelRunProjection{}, err
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return application.CancelRunProjection{}, err
	}
	result := application.CancelRunProjection{ProtocolRevision: application.StopRunProtocolRevision, Run: transition.After, RequestDigest: terminal.StopIntent.RequestDigest, StopIntentDigest: terminal.StopIntent.IntentDigest, TerminalReason: outcome.Summary, OutcomeDigest: digest}
	if err := result.Validate(); err != nil {
		return application.CancelRunProjection{}, err
	}
	return result, nil
}
