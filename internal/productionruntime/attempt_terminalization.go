package productionruntime

import (
	"context"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type attemptEligibilityVerifier struct {
	store    *resultingress.DurableStore
	identity resultingress.AttemptIdentity
}

func (verifier attemptEligibilityVerifier) VerifyAttemptEligibilityProjection(projection dispatch.AttemptEligibilityProjection) error {
	state, found, err := verifier.store.AttemptState(verifier.identity)
	if err != nil || !found || state.Identity != verifier.identity {
		return fmt.Errorf("productionruntime: stale Attempt eligibility projection")
	}
	expected, err := terminalEligibilityProjection(state)
	if err != nil || expected != projection {
		return fmt.Errorf("productionruntime: stale Attempt eligibility projection")
	}
	return nil
}

// Only three business terminal reasons are supported here. Other cleanup
// classes remain outside this production path; no arbitrary enum cast can
// turn a provider observation into terminal eligibility.
func terminalEligibilityProjection(state resultingress.AttemptAuthorityState) (dispatch.AttemptEligibilityProjection, error) {
	if state.Identity.Validate() != nil || state.BarrierDigest == "" || !state.AdmissionClosed || state.TerminalGeneration != state.Identity.DispatchGeneration+1 {
		return dispatch.AttemptEligibilityProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	projection := dispatch.AttemptEligibilityProjection{LeaseId: state.Identity.LeaseID, RunId: state.Identity.RunID, AttemptId: state.Identity.AttemptID, AllocationId: state.Identity.AllocationID, FromGeneration: state.Identity.DispatchGeneration, TerminalGeneration: state.TerminalGeneration, AttemptAuthorityHeadDigest: state.BarrierDigest}
	if state.StopIntent == (resultingress.AttemptStopIntent{}) {
		if state.EligibilityTerminal != (resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptCompleted}) {
			return dispatch.AttemptEligibilityProjection{}, resultingress.ErrAttemptAuthorityConflict
		}
		projection.TerminalState, projection.CompletionReason = dispatch.LeaseStateCompleted, dispatch.CompletionReasonAttemptCompleted
		return projection, nil
	}
	if state.StopIntent.Validate(state.Identity) != nil || state.StopIntent.Eligibility() != state.EligibilityTerminal || state.CommittedResultFactDigest != "" || state.BarrierAdmissionFactDigest != "" || state.BarrierAdmissionSequence != 0 {
		return dispatch.AttemptEligibilityProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	switch state.StopIntent.Category {
	case resultingress.StopOperatorRequest:
		projection.TerminalState, projection.CompletionReason = dispatch.LeaseStateCompleted, dispatch.CompletionReasonAttemptAborted
	case resultingress.StopAttemptDeadline, resultingress.StopRunDeadline:
		projection.TerminalState, projection.CancelReason = dispatch.LeaseStateCancelled, dispatch.CancelReasonDeadlineExceeded
	default:
		return dispatch.AttemptEligibilityProjection{}, resultingress.ErrAttemptAuthorityConflict
	}
	return projection, nil
}

func runAuthorityForAttempt(identity resultingress.AttemptIdentity) resultingress.RunAuthorityBinding {
	return resultingress.RunAuthorityBinding{AuthorityNamespaceID: identity.AuthorityNamespaceID, RunID: identity.RunID, OrchestratorID: identity.OrchestratorID, RunAuthorityDigest: identity.RunAuthorityDigest}
}

func cleanupRequest(identity resultingress.AttemptIdentity, state resultingress.AttemptAuthorityState, operation resultingress.CleanupOperation) resultingress.CleanupAuthorizationRequest {
	return resultingress.CleanupAuthorizationRequest{Identity: identity, CurrentRunAuthority: runAuthorityForAttempt(identity), TerminalizationID: state.TerminalizationID, TerminalGeneration: state.TerminalGeneration, CleanupBindingDigest: state.CleanupBindingDigest, Operation: operation}
}

// beginCompletedTerminalization appends or replays the result-closing barrier
// before the shared cleanup path projects eligibility into the dispatch model.
func (l *CompositionLedger) beginCompletedTerminalization(ctx context.Context, attempt resultingress.AttemptAuthorityState) (resultingress.AttemptAuthorityState, error) {
	state := attempt
	if state.StopIntent != (resultingress.AttemptStopIntent{}) {
		return resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityConflict
	}
	if state.BarrierDigest == "" {
		key, err := state.Identity.Key()
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		terminalizationID := canonical.DigestBytes([]byte("productionruntime:terminalization:" + key))
		result, err := l.ingress.CompareAndAppendBarrier(ctx, l, state.Revision, state.HeadDigest,
			resultingress.BarrierAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: runAuthorityForAttempt(state.Identity)},
			resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionTerminalizationBarrier, Identity: state.Identity, TerminalizationID: terminalizationID, EligibilityTerminal: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptCompleted}})
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		state = result.State
	}
	return state, nil
}

func (l *CompositionLedger) appendAllocationReleased(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, state resultingress.AttemptAuthorityState) (resultingress.AttemptAuthorityState, error) {
	if state.AllocationTerminalDigest != "" {
		return state, nil
	}
	receipt, err := l.releaseExistingWorktree(ctx, acquisition, read, state)
	if err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	current, found, err := l.ingress.AttemptState(state.Identity)
	if err != nil || !found || current.ExistingWorktreeReleaseReceiptDigest != receipt.ReceiptDigest {
		return resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityConflict
	}
	result, err := l.ingress.CompareAndAppendCleanup(ctx, l, current.Revision, current.HeadDigest, cleanupRequest(current.Identity, current, resultingress.CleanupReconcile), resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionAllocationTerminated, Identity: current.Identity, TerminalizationID: current.TerminalizationID, ReceiptDigest: receipt.ReceiptDigest})
	if err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	return result.State, nil
}

func terminalKindFromObservation(observation resultingress.PreparedExecutionTerminalObservation) (resultingress.ProcessTerminalKind, error) {
	switch observation.Evidence.Outcome.State {
	case resultingress.SupervisorProcessAbsent:
		return resultingress.ProcessAbsent, nil
	case resultingress.SupervisorProcessExited:
		return resultingress.ProcessTerminated, nil
	case resultingress.SupervisorProcessIdentityConflict:
		return resultingress.ProcessIdentityConflict, nil
	default:
		return "", resultingress.ErrPreparedExecutionNotTerminal
	}
}

// terminalizeCompletedAttempt drives the one closed success path from an
// admitted WorkerResult through supervisor-authenticated process terminal,
// path-B allocation release, supervisor close and cleanup release. Every
// stage checks the durable head and is safe to re-enter after response loss.
func (l *CompositionLedger) terminalizeCompletedAttempt(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, attempt resultingress.AttemptAuthorityState) (resultingress.AttemptAuthorityState, error) {
	state, err := l.beginCompletedTerminalization(ctx, attempt)
	if err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	return l.terminalizeAttemptAfterBarrier(ctx, verifier, acquisition, read, state)
}

// Both success and stop recovery consume a durable barrier before reaching
// mechanics. The recorded stop reason, not an HTTP context or caller flag,
// selects Terminate instead of Inspect. Allocation and supervisor cleanup is
// shared so cancellation cannot skip absence or release evidence.
func (l *CompositionLedger) terminalizeAttemptAfterBarrier(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, state resultingress.AttemptAuthorityState) (resultingress.AttemptAuthorityState, error) {
	projection, err := terminalEligibilityProjection(state)
	if err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	if err := l.leaseLedger.ProjectAttemptEligibility(attemptEligibilityVerifier{store: l.ingress, identity: state.Identity}, projection); err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	if state.ProcessTerminalDigest == "" {
		var observed resultingress.PreparedExecutionTerminalObservation
		operation := resultingress.CleanupInspect
		if state.StopIntent != (resultingress.AttemptStopIntent{}) {
			// The signal command has its own Terminate authority. Appending
			// its observed process-terminal fact is reconciliation, not a
			// second signal permission (cleanupTransitionAllowed).
			operation = resultingress.CleanupReconcile
			observed, err = l.ingress.TerminatePreparedExecution(ctx, verifier, acquisition, state.Identity)
		} else {
			observed, err = l.ingress.InspectPreparedExecution(ctx, verifier, acquisition, state.Identity)
		}
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		kind, err := terminalKindFromObservation(observed)
		if err != nil || kind == resultingress.ProcessIdentityConflict {
			return resultingress.AttemptAuthorityState{}, resultingress.ErrPreparedExecutionNotTerminal
		}
		current, found, err := l.ingress.AttemptState(state.Identity)
		if err != nil || !found {
			return resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityConflict
		}
		result, err := l.ingress.CompareAndAppendCleanup(ctx, l, current.Revision, current.HeadDigest, cleanupRequest(current.Identity, current, operation), resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionProcessTerminal, Identity: current.Identity, TerminalizationID: current.TerminalizationID, ProcessTerminalKind: kind, ObservationDigest: observed.Evidence.ObservationDigest, SupervisorOutcomeFactDigest: observed.OutcomeFactDigest})
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		state = result.State
	}
	state, err = l.appendAllocationReleased(ctx, acquisition, read, state)
	if err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	if state.SupervisorClosedDigest == "" {
		closedEvidence, err := l.ingress.ClosePreparedExecution(ctx, verifier, acquisition, state.Identity)
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		current, found, err := l.ingress.AttemptState(state.Identity)
		if err != nil || !found {
			return resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityConflict
		}
		closed, err := closedEvidence.SupervisorClosed(resultingress.ProcessSupervisorCloseAuthority{Owner: current.Owner, SupervisorStartedFactDigest: current.SupervisorStartedDigest, TerminalizationID: current.TerminalizationID, CleanupBindingDigest: current.CleanupBindingDigest, ProcessTerminalFactDigest: current.ProcessTerminalDigest, AllocationTerminatedFactDigest: current.AllocationTerminalDigest})
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		result, err := l.ingress.AppendSupervisorClosed(ctx, verifier, l, current.Revision, current.HeadDigest, cleanupRequest(current.Identity, current, resultingress.CleanupReconcile), closed, closedEvidence.OutcomeFactDigest)
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		state = result.State
	}
	if state.CleanupCompletedDigest == "" {
		result, err := l.ingress.CompareAndAppendCleanup(ctx, l, state.Revision, state.HeadDigest, cleanupRequest(state.Identity, state, resultingress.CleanupReconcile), resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionCleanupCompleted, Identity: state.Identity, TerminalizationID: state.TerminalizationID, SupervisorClosedFactDigest: state.SupervisorClosedDigest})
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		state = result.State
	}
	if state.CleanupReleasedDigest == "" {
		result, err := l.ingress.CompareAndAppendCleanup(ctx, l, state.Revision, state.HeadDigest, cleanupRequest(state.Identity, state, resultingress.CleanupReconcile), resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionCleanupReleased, Identity: state.Identity, TerminalizationID: state.TerminalizationID})
		if err != nil {
			return resultingress.AttemptAuthorityState{}, err
		}
		state = result.State
	}
	if state.CleanupReleasedDigest == "" {
		return resultingress.AttemptAuthorityState{}, resultingress.ErrCleanupUnauthorized
	}
	if err := l.adoptTerminalProjectionMutation(ctx, verifier, acquisition, read, state); err != nil {
		return resultingress.AttemptAuthorityState{}, err
	}
	return state, nil
}
