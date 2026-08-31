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
	if err != nil || !found || state.Identity != verifier.identity || state.BarrierDigest == "" || state.BarrierDigest != projection.AttemptAuthorityHeadDigest ||
		state.EligibilityTerminal.Kind != resultingress.EligibilityTerminalCompleted || state.EligibilityTerminal.CompletionReason != resultingress.TerminalAttemptCompleted ||
		projection.LeaseId != verifier.identity.LeaseID || projection.RunId != verifier.identity.RunID || projection.AttemptId != verifier.identity.AttemptID || projection.AllocationId != verifier.identity.AllocationID ||
		projection.FromGeneration != verifier.identity.DispatchGeneration || projection.TerminalGeneration != verifier.identity.DispatchGeneration+1 || projection.TerminalState != dispatch.LeaseStateCompleted || projection.CompletionReason != dispatch.CompletionReasonAttemptCompleted || projection.CancelReason != "" {
		return fmt.Errorf("productionruntime: stale Attempt eligibility projection")
	}
	return nil
}

func runAuthorityForAttempt(identity resultingress.AttemptIdentity) resultingress.RunAuthorityBinding {
	return resultingress.RunAuthorityBinding{AuthorityNamespaceID: identity.AuthorityNamespaceID, RunID: identity.RunID, OrchestratorID: identity.OrchestratorID, RunAuthorityDigest: identity.RunAuthorityDigest}
}

func cleanupRequest(identity resultingress.AttemptIdentity, state resultingress.AttemptAuthorityState, operation resultingress.CleanupOperation) resultingress.CleanupAuthorizationRequest {
	return resultingress.CleanupAuthorizationRequest{Identity: identity, CurrentRunAuthority: runAuthorityForAttempt(identity), TerminalizationID: state.TerminalizationID, TerminalGeneration: state.TerminalGeneration, CleanupBindingDigest: state.CleanupBindingDigest, Operation: operation}
}

// beginCompletedTerminalization appends or replays the result-closing barrier
// and projects the exact terminal eligibility into the dispatch read model.
func (l *CompositionLedger) beginCompletedTerminalization(ctx context.Context, attempt resultingress.AttemptAuthorityState) (resultingress.AttemptAuthorityState, error) {
	state := attempt
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
	projection := dispatch.AttemptEligibilityProjection{LeaseId: state.Identity.LeaseID, RunId: state.Identity.RunID, AttemptId: state.Identity.AttemptID, AllocationId: state.Identity.AllocationID, FromGeneration: state.Identity.DispatchGeneration, TerminalGeneration: state.Identity.DispatchGeneration + 1, TerminalState: dispatch.LeaseStateCompleted, CompletionReason: dispatch.CompletionReasonAttemptCompleted, AttemptAuthorityHeadDigest: state.BarrierDigest}
	if err := l.leaseLedger.ProjectAttemptEligibility(attemptEligibilityVerifier{store: l.ingress, identity: state.Identity}, projection); err != nil {
		return resultingress.AttemptAuthorityState{}, err
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
	if state.ProcessTerminalDigest == "" {
		observed, err := l.ingress.InspectPreparedExecution(ctx, verifier, acquisition, state.Identity)
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
		result, err := l.ingress.CompareAndAppendCleanup(ctx, l, current.Revision, current.HeadDigest, cleanupRequest(current.Identity, current, resultingress.CleanupInspect), resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionProcessTerminal, Identity: current.Identity, TerminalizationID: current.TerminalizationID, ProcessTerminalKind: kind, ObservationDigest: observed.Evidence.ObservationDigest, SupervisorOutcomeFactDigest: observed.OutcomeFactDigest})
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
		closed, err := resultingress.NewProcessSupervisorClosedFromRecovery(resultingress.ProcessSupervisorCloseAuthority{Owner: current.Owner, SupervisorStartedFactDigest: current.SupervisorStartedDigest, TerminalizationID: current.TerminalizationID, CleanupBindingDigest: current.CleanupBindingDigest, ProcessTerminalFactDigest: current.ProcessTerminalDigest, AllocationTerminatedFactDigest: current.AllocationTerminalDigest}, closedEvidence.Recovery)
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
	return state, nil
}
