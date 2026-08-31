package productionruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// OwnerProjection is a secret-safe projection read from the single durable
// authority while the exact repository owner lock is held.
type OwnerProjection struct {
	OwnerEpoch      uint64
	OwnerFactDigest string
	PendingRecovery uint64
}

// DurableRunAuthority is implemented inside the future production composition
// root over the existing Run/ResultIngress stores. Prepared intents and start
// outcomes are rehydrated by digest; bridge return values are never authority.
type DurableRunAuthority interface {
	CurrentOwner(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition) (OwnerProjection, error)
	PrepareRunStart(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, application.PrepareRunStartRequest) (application.PreparedRunStart, error)
	RehydratePreparedRunStart(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, string) (application.PreparedRunStart, error)
	RehydrateRunStartOutcome(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, string) (application.RunProjection, bool, error)
	InspectRun(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, application.InspectRunRequest) (application.RunProjection, error)
}

// ProcessBridge is narrower than sandboxbridge/execution. Both methods execute
// while the repository owner lock is held. VerifyAgentProfile must compare the
// immutable configured Pi tuple against bridge-held executable/runtime objects.
// StartPreparedRun must repeat that check, recheck owner/prepared authority and
// durably commit the exact PreparationDigest outcome before returning.
type ProcessBridge interface {
	VerifyAgentProfile(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, OwnerProjection, PiProfile) error
	StartPreparedRun(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, OwnerProjection, PiProfile, application.PreparedRunStart) error
}

type runCompletionAuthority interface {
	CollectRunResult(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, string) (CollectedRunResult, error)
}

// controller is deliberately package-private. Only Runtime can reach mutation
// methods, after the real repository lock has accepted its one runtime claim.
type controller struct {
	authority   DurableRunAuthority
	bridge      ProcessBridge
	ownerLock   repositoryOwnerLock
	acquisition resultingress.ControlOwnerAcquisition
	profile     PiProfile
}

func newController(authority DurableRunAuthority, bridge ProcessBridge, ownerLock repositoryOwnerLock, acquisition resultingress.ControlOwnerAcquisition, profile PiProfile) (*controller, error) {
	if authority == nil || bridge == nil || ownerLock == nil || acquisition.Validate() != nil || ownerLock.acquisition() != acquisition || profile.Validate() != nil {
		return nil, application.NewError("production-controller", application.ReasonInvalidRequest)
	}
	return &controller{authority: authority, bridge: bridge, ownerLock: ownerLock, acquisition: acquisition, profile: profile}, nil
}

func (controller *controller) prepareRunStart(ctx context.Context, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	if err := request.Validate(); err != nil {
		return application.PreparedRunStart{}, err
	}
	var prepared application.PreparedRunStart
	err := controller.withOwner(ctx, true, func(verifier resultingress.CurrentOwnerLockVerifier, owner OwnerProjection) error {
		if err := controller.bridge.VerifyAgentProfile(ctx, verifier, controller.acquisition, owner, controller.profile); err != nil {
			return mapBridgeError(err)
		}
		var err error
		prepared, err = controller.authority.PrepareRunStart(ctx, verifier, controller.acquisition, request)
		if err != nil {
			return mapAuthorityError("prepare-run-start", err)
		}
		if prepared.Validate() != nil || prepared.RunID != request.RunID || prepared.Sequence != request.ExpectedSequence || prepared.AuthorityHead != request.ExpectedAuthorityHead {
			return application.NewError("prepare-run-start", application.ReasonAuthorityConflict)
		}
		durable, err := controller.authority.RehydratePreparedRunStart(ctx, verifier, controller.acquisition, prepared.PreparationDigest)
		if err != nil {
			return mapAuthorityError("prepare-run-start", err)
		}
		if durable != prepared {
			return application.NewError("prepare-run-start", application.ReasonAuthorityConflict)
		}
		return nil
	})
	if err != nil {
		return application.PreparedRunStart{}, err
	}
	return prepared, nil
}

func (controller *controller) startPreparedRun(ctx context.Context, supplied application.PreparedRunStart) (application.RunProjection, error) {
	if err := supplied.Validate(); err != nil {
		return application.RunProjection{}, err
	}
	var successor application.RunProjection
	err := controller.withOwner(ctx, true, func(verifier resultingress.CurrentOwnerLockVerifier, owner OwnerProjection) error {
		durable, err := controller.authority.RehydratePreparedRunStart(ctx, verifier, controller.acquisition, supplied.PreparationDigest)
		if err != nil {
			return mapAuthorityError("start-prepared-run", err)
		}
		if durable != supplied {
			return application.NewError("start-prepared-run", application.ReasonAuthorityConflict)
		}
		if replay, found, err := controller.authority.RehydrateRunStartOutcome(ctx, verifier, controller.acquisition, supplied.PreparationDigest); err != nil {
			return mapAuthorityError("start-prepared-run", err)
		} else if found {
			if !validStartSuccessor(supplied, replay) {
				return application.NewError("start-prepared-run", application.ReasonAuthorityConflict)
			}
			successor = replay
			return nil
		}
		if err := controller.bridge.VerifyAgentProfile(ctx, verifier, controller.acquisition, owner, controller.profile); err != nil {
			return mapBridgeError(err)
		}

		bridgeErr := controller.bridge.StartPreparedRun(ctx, verifier, controller.acquisition, owner, controller.profile, supplied)
		replayed, found, outcomeErr := controller.authority.RehydrateRunStartOutcome(ctx, verifier, controller.acquisition, supplied.PreparationDigest)
		if outcomeErr != nil {
			return mapAuthorityError("start-prepared-run", outcomeErr)
		}
		if found {
			if !validStartSuccessor(supplied, replayed) {
				return application.NewError("start-prepared-run", application.ReasonAuthorityConflict)
			}
			successor = replayed
			return nil
		}
		if bridgeErr != nil {
			return mapBridgeError(bridgeErr)
		}
		return application.NewError("start-prepared-run", application.ReasonRecoveryRequired)
	})
	if err != nil {
		return application.RunProjection{}, err
	}
	return successor, nil
}

func validStartSuccessor(prepared application.PreparedRunStart, successor application.RunProjection) bool {
	return successor.Validate() == nil && successor.TaskID == prepared.TaskID && successor.RunID == prepared.RunID && successor.AttemptID == prepared.AttemptID && successor.State == domain.StateRunning && successor.Sequence > prepared.Sequence && successor.AuthorityHead != prepared.AuthorityHead
}

func (controller *controller) inspectRun(ctx context.Context, request application.InspectRunRequest) (application.RunProjection, error) {
	if err := request.Validate(); err != nil {
		return application.RunProjection{}, err
	}
	var projection application.RunProjection
	err := controller.withOwner(ctx, false, func(verifier resultingress.CurrentOwnerLockVerifier, _ OwnerProjection) error {
		var err error
		projection, err = controller.authority.InspectRun(ctx, verifier, controller.acquisition, request)
		if err != nil {
			return mapAuthorityError("inspect-run", err)
		}
		if projection.Validate() != nil || projection.RunID != request.RunID {
			return application.NewError("inspect-run", application.ReasonAuthorityConflict)
		}
		return nil
	})
	if err != nil {
		return application.RunProjection{}, err
	}
	return projection, nil
}

func (controller *controller) collectRunResult(ctx context.Context, runID string) (CollectedRunResult, error) {
	if runID == "" {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonInvalidRequest)
	}
	authority, ok := controller.authority.(runCompletionAuthority)
	if !ok {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonCompositionIncomplete)
	}
	var collected CollectedRunResult
	// A RUNNING attempt is deliberately projected as PendingRecovery so a
	// fresh fixed CLI knows it must attach/rebind. Collection is the operation
	// that resolves that condition; applying the generic mutation barrier here
	// would make every cross-process collection deterministically impossible.
	// The exact owner lock and the completion authority still gate all writes.
	err := controller.withOwner(ctx, false, func(verifier resultingress.CurrentOwnerLockVerifier, _ OwnerProjection) error {
		var err error
		collected, err = authority.CollectRunResult(ctx, verifier, controller.acquisition, runID)
		if errors.Is(err, ErrAttemptStillRunning) {
			return err
		}
		if err != nil {
			return mapAuthorityError("collect-run-result", err)
		}
		return nil
	})
	if err != nil {
		return CollectedRunResult{}, err
	}
	return collected, nil
}

func (controller *controller) status(ctx context.Context) (OwnerProjection, error) {
	var projection OwnerProjection
	err := controller.withOwner(ctx, false, func(verifier resultingress.CurrentOwnerLockVerifier, owner OwnerProjection) error {
		projection = owner
		if owner.PendingRecovery != 0 {
			return nil
		}
		if err := controller.bridge.VerifyAgentProfile(ctx, verifier, controller.acquisition, owner, controller.profile); err != nil {
			return mapBridgeError(err)
		}
		return nil
	})
	return projection, err
}

// withOwner is the single critical section for each operation. It verifies a
// claimed real lock, obtains one exact current durable owner projection and,
// for mutations, requires zero unresolved recovery before continuing.
func (controller *controller) withOwner(ctx context.Context, mutation bool, fn func(resultingress.CurrentOwnerLockVerifier, OwnerProjection) error) error {
	if controller == nil || fn == nil || controller.ownerLock == nil || !controller.ownerLock.claimed() {
		return application.NewError("current-owner", application.ReasonOwnerUnavailable)
	}
	return controller.ownerLock.WithCurrentOwnerLock(ctx, controller.acquisition, func() error {
		borrowed := &borrowedOwnerVerifier{acquisition: controller.acquisition, active: true}
		defer borrowed.close()
		owner, err := controller.authority.CurrentOwner(ctx, borrowed, controller.acquisition)
		if err != nil {
			return mapAuthorityError("current-owner", err)
		}
		if owner.OwnerEpoch != controller.acquisition.OwnerEpoch || !profileDigestPattern.MatchString(owner.OwnerFactDigest) {
			return application.NewError("current-owner", application.ReasonOwnerNotCurrent)
		}
		if mutation && owner.PendingRecovery != 0 {
			return application.NewError("current-owner", application.ReasonRecoveryRequired)
		}
		return fn(borrowed, owner)
	})
}

type borrowedOwnerVerifier struct {
	mu          sync.Mutex
	acquisition resultingress.ControlOwnerAcquisition
	active      bool
}

func (verifier *borrowedOwnerVerifier) WithCurrentOwnerLock(_ context.Context, acquisition resultingress.ControlOwnerAcquisition, fn func() error) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if !verifier.active || acquisition != verifier.acquisition || fn == nil {
		return application.NewError("current-owner", application.ReasonOwnerNotCurrent)
	}
	return fn()
}

func (verifier *borrowedOwnerVerifier) close() {
	verifier.mu.Lock()
	verifier.active = false
	verifier.mu.Unlock()
}

func mapAuthorityError(operation string, err error) error {
	if err == nil {
		return nil
	}
	for _, reason := range []application.ReasonCode{application.ReasonOwnerUnavailable, application.ReasonOwnerNotCurrent, application.ReasonAuthorityConflict, application.ReasonRecoveryRequired} {
		if application.HasReason(err, reason) {
			return err
		}
	}
	return application.NewError(operation, application.ReasonAuthorityConflict)
}

func mapBridgeError(err error) error {
	if err == nil {
		return nil
	}
	for _, reason := range []application.ReasonCode{application.ReasonRecoveryRequired, application.ReasonBridgeUnavailable, application.ReasonAuthorityConflict, application.ReasonOwnerUnavailable, application.ReasonOwnerNotCurrent} {
		if application.HasReason(err, reason) {
			return err
		}
	}
	return application.NewError("start-prepared-run", application.ReasonBridgeUnavailable)
}
