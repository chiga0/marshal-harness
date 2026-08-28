package sandboxbridge

import (
	"context"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processcontrol"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func (authority DurableProcessAuthority) BeginTerminalization(ctx context.Context, eligibility resultingress.EligibilityTerminal) (processcontrol.CleanupRef, resultingress.AttemptAuthorityState, error) {
	if authority.Store == nil || authority.Verifier == nil || eligibility.Validate() != nil {
		return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, resultingress.ErrCleanupUnauthorized
	}
	state, found, err := authority.Store.AttemptState(authority.Identity)
	if err != nil || !found {
		return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityUnknown
	}
	if state.BarrierDigest == "" {
		key, keyErr := authority.Identity.Key()
		if keyErr != nil {
			return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, keyErr
		}
		terminalizationID := canonical.DigestBytes([]byte("sandboxbridge:terminalization:" + key))
		run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: authority.Identity.AuthorityNamespaceID, RunID: authority.Identity.RunID, OrchestratorID: authority.Identity.OrchestratorID, RunAuthorityDigest: authority.Identity.RunAuthorityDigest}
		result, appendErr := authority.Store.CompareAndAppendBarrier(ctx, authority.Verifier, state.Revision, state.HeadDigest,
			resultingress.BarrierAuthorizationRequest{Identity: authority.Identity, CurrentRunAuthority: run},
			resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionTerminalizationBarrier, Identity: authority.Identity, TerminalizationID: terminalizationID, EligibilityTerminal: eligibility})
		if appendErr != nil {
			return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, appendErr
		}
		state = result.State
	} else if state.EligibilityTerminal != eligibility {
		return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityConflict
	}
	if state.TerminalizationID == "" || state.TerminalGeneration < 1 || state.CleanupBindingDigest == "" {
		return processcontrol.CleanupRef{}, resultingress.AttemptAuthorityState{}, resultingress.ErrCleanupUnauthorized
	}
	return processcontrol.CleanupRef{TerminalizationID: state.TerminalizationID, TerminalGeneration: state.TerminalGeneration, CleanupBindingDigest: state.CleanupBindingDigest}, state, nil
}

func (authority DurableProcessAuthority) RecordProcessTerminal(ctx context.Context, cleanup processcontrol.CleanupRef, kind resultingress.ProcessTerminalKind, observationDigest string) (resultingress.AttemptAuthorityState, error) {
	state, found, err := authority.Store.AttemptState(authority.Identity)
	if err != nil || !found {
		return resultingress.AttemptAuthorityState{}, resultingress.ErrAttemptAuthorityUnknown
	}
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: authority.Identity.AuthorityNamespaceID, RunID: authority.Identity.RunID, OrchestratorID: authority.Identity.OrchestratorID, RunAuthorityDigest: authority.Identity.RunAuthorityDigest}
	request := resultingress.CleanupAuthorizationRequest{Identity: authority.Identity, CurrentRunAuthority: run, TerminalizationID: cleanup.TerminalizationID, TerminalGeneration: cleanup.TerminalGeneration, CleanupBindingDigest: cleanup.CleanupBindingDigest, Operation: resultingress.CleanupInspect}
	result, err := authority.Store.CompareAndAppendCleanup(ctx, authority.Verifier, state.Revision, state.HeadDigest, request, resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionProcessTerminal, Identity: authority.Identity, TerminalizationID: cleanup.TerminalizationID, ProcessTerminalKind: kind, ObservationDigest: observationDigest})
	return result.State, err
}

// DurableProcessAuthority is the narrow composition adapter from RB1/RB3's
// single durable Attempt ledger to RB2 process control. It does not discover
// or fabricate identity: ProductionRuntime must supply the already-opened
// exact AttemptIdentity and the current Run verifier.
type DurableProcessAuthority struct {
	Store               *resultingress.DurableStore
	Verifier            resultingress.CurrentRunAuthorityVerifier
	Identity            resultingress.AttemptIdentity
	AllocationAuthority *resultingress.AllocationAuthority
}

func (authority DurableProcessAuthority) AuthorizeLaunch(ctx context.Context, request processcontrol.LaunchAuthorityRequest) (processcontrol.AppendResult, error) {
	if authority.Store == nil || authority.Verifier == nil || request.Authority.AttemptKey == "" {
		return processcontrol.AppendResult{}, resultingress.ErrRunAuthorityUnauthorized
	}
	if !sameProcessIdentity(request.Authority, authority.Identity) {
		return processcontrol.AppendResult{}, resultingress.ErrRunAuthorityUnauthorized
	}
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: authority.Identity.AuthorityNamespaceID, RunID: authority.Identity.RunID, OrchestratorID: authority.Identity.OrchestratorID, RunAuthorityDigest: authority.Identity.RunAuthorityDigest}
	result, err := authority.Store.CompareAndAppendAuthorized(ctx, authority.Verifier, request.ExpectedRevision, request.ExpectedHead, resultingress.AttemptAuthorizationRequest{Identity: authority.Identity, CurrentRunAuthority: run}, resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionLaunchAuthorized, Identity: authority.Identity, LaunchAuthorizationID: request.LaunchID, LaunchClosure: request.Closure})
	return processAppend(result), err
}

func (authority DurableProcessAuthority) RecordProcessStarted(ctx context.Context, request processcontrol.ProcessStartedAuthorityRequest) (processcontrol.AppendResult, error) {
	if authority.Store == nil || authority.Verifier == nil || !sameProcessIdentity(request.Authority, authority.Identity) {
		return processcontrol.AppendResult{}, resultingress.ErrRunAuthorityUnauthorized
	}
	current, found, err := authority.Store.AttemptState(authority.Identity)
	if err != nil || !found || current.LaunchAuthorizedDigest != request.LaunchTransition {
		return processcontrol.AppendResult{}, resultingress.ErrAttemptAuthorityConflict
	}
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: authority.Identity.AuthorityNamespaceID, RunID: authority.Identity.RunID, OrchestratorID: authority.Identity.OrchestratorID, RunAuthorityDigest: authority.Identity.RunAuthorityDigest}
	observation := request.Observation
	process, err := resultingress.SealProcessObservation(resultingress.ProcessObservation{PID: observation.PID, PGID: observation.PGID, BirthSeconds: observation.BirthSeconds, BirthMicroseconds: observation.BirthMicroseconds, WorkingDirectory: observation.WorkingDirectory, WorkingDirectoryDevice: observation.WorkingDirectoryDevice, WorkingDirectoryInode: observation.WorkingDirectoryInode, WorkingDirectoryType: observation.WorkingDirectoryType, WorkingDirectoryOwner: observation.WorkingDirectoryOwner, WorkingDirectoryMode: observation.WorkingDirectoryMode, ExecutablePath: observation.ExecutablePath, ExecutableDevice: observation.ExecutableDevice, ExecutableInode: observation.ExecutableInode, ExecutableSize: observation.ExecutableSize, ExecutableType: observation.ExecutableType, ExecutableOwner: observation.ExecutableOwner, ExecutableGroup: observation.ExecutableGroup, ExecutableMode: observation.ExecutableMode, ExecutableLinkCount: observation.ExecutableLinkCount, ExecutableSHA256: observation.ExecutableSHA256, ObserverIdentity: observation.ObserverIdentity})
	if err != nil || process.ObservationDigest != observation.ObservationDigest {
		return processcontrol.AppendResult{}, resultingress.ErrAttemptAuthorityConflict
	}
	result, err := authority.Store.CompareAndAppendAuthorized(ctx, authority.Verifier, request.ExpectedRevision, request.ExpectedHead, resultingress.AttemptAuthorizationRequest{Identity: authority.Identity, CurrentRunAuthority: run}, resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionProcessStarted, Identity: authority.Identity, CommandID: request.CommandID, ObservedAt: request.ObservedAt, Process: process, LaunchMaterialsDigest: request.LaunchMaterialsDigest, AgentLaunchSpecDigest: request.AgentLaunchSpecDigest})
	return processAppend(result), err
}

func (authority DurableProcessAuthority) WithCurrentAuthority(ctx context.Context, request processcontrol.ControlAuthorization, effect func() error) error {
	if !sameProcessIdentity(request.Authority, authority.Identity) || effect == nil {
		return resultingress.ErrRunAuthorityUnauthorized
	}
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: authority.Identity.AuthorityNamespaceID, RunID: authority.Identity.RunID, OrchestratorID: authority.Identity.OrchestratorID, RunAuthorityDigest: authority.Identity.RunAuthorityDigest}
	switch request.Operation {
	case processcontrol.OperationInspect, processcontrol.OperationReconcile:
		if request.Cleanup != (processcontrol.CleanupRef{}) || authority.Store == nil {
			return resultingress.ErrCleanupUnauthorized
		}
		// Hold current Run authority while reading the Attempt head. A barrier
		// append takes the same outer authority, so it cannot race this check.
		return authority.Verifier.WithCurrentRunAuthority(ctx, run, func() error {
			state, found, err := authority.Store.AttemptState(authority.Identity)
			if err != nil || !found {
				return resultingress.ErrAttemptAuthorityUnknown
			}
			if state.BarrierDigest != "" {
				return resultingress.ErrCleanupUnauthorized
			}
			return effect()
		})
	case processcontrol.OperationCleanupInspect, processcontrol.OperationCleanupReconcile:
		if authority.Store == nil {
			return resultingress.ErrCleanupUnauthorized
		}
		operation := resultingress.CleanupInspect
		if request.Operation == processcontrol.OperationCleanupReconcile {
			operation = resultingress.CleanupReconcile
		}
		cleanup := resultingress.CleanupAuthorizationRequest{
			Identity: authority.Identity, CurrentRunAuthority: run,
			TerminalizationID: request.Cleanup.TerminalizationID, TerminalGeneration: request.Cleanup.TerminalGeneration,
			CleanupBindingDigest: request.Cleanup.CleanupBindingDigest, Operation: operation,
		}
		return authority.Store.WithAuthorizedCleanup(ctx, authority.Verifier, cleanup, func(resultingress.AttemptAuthorityState) error { return effect() })
	case processcontrol.OperationSignalTERM, processcontrol.OperationSignalKILL:
		if authority.Store == nil {
			return resultingress.ErrCleanupUnauthorized
		}
		cleanup := resultingress.CleanupAuthorizationRequest{
			Identity: authority.Identity, CurrentRunAuthority: run,
			TerminalizationID: request.Cleanup.TerminalizationID, TerminalGeneration: request.Cleanup.TerminalGeneration,
			CleanupBindingDigest: request.Cleanup.CleanupBindingDigest, Operation: resultingress.CleanupSignal,
		}
		return authority.Store.WithAuthorizedCleanup(ctx, authority.Verifier, cleanup, func(resultingress.AttemptAuthorityState) error { return effect() })
	default:
		return resultingress.ErrCleanupUnauthorized
	}
}

func processAppend(result resultingress.AttemptAppendResult) processcontrol.AppendResult {
	return processcontrol.AppendResult{Appended: result.Appended, Revision: result.State.Revision, HeadDigest: result.State.HeadDigest, TransitionDigest: result.TransitionDigest}
}

func sameProcessIdentity(ref processcontrol.AuthorityRef, identity resultingress.AttemptIdentity) bool {
	key, err := identity.Key()
	return err == nil && ref.AuthorityNamespaceID == identity.AuthorityNamespaceID && ref.AuthorityNamespaceRef == identity.AuthorityNamespaceRef && ref.AttemptKey == key && ref.TaskID == identity.TaskID && ref.RunID == identity.RunID && ref.AttemptID == identity.AttemptID && ref.AllocationID == identity.AllocationID && ref.LeaseID == identity.LeaseID && ref.LeaseDigest == identity.LeaseDigest && int64(ref.DispatchGeneration) == identity.DispatchGeneration && ref.FencingTokenDigest == identity.FencingTokenDigest && ref.OrchestratorID == identity.OrchestratorID && ref.RunAuthorityDigest == identity.RunAuthorityDigest
}

func ProcessAuthorityRef(identity resultingress.AttemptIdentity) (processcontrol.AuthorityRef, error) {
	key, err := identity.Key()
	if err != nil {
		return processcontrol.AuthorityRef{}, fmt.Errorf("sandboxbridge: process authority identity: %w", err)
	}
	return processcontrol.AuthorityRef{AuthorityNamespaceID: identity.AuthorityNamespaceID, AuthorityNamespaceRef: identity.AuthorityNamespaceRef, AttemptKey: key, TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID, AllocationID: identity.AllocationID, LeaseID: identity.LeaseID, LeaseDigest: identity.LeaseDigest, DispatchGeneration: uint64(identity.DispatchGeneration), FencingTokenDigest: identity.FencingTokenDigest, OrchestratorID: identity.OrchestratorID, RunAuthorityDigest: identity.RunAuthorityDigest}, nil
}
