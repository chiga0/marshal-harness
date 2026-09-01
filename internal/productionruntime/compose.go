package productionruntime

import (
	"context"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// piBridge is the fixed-cli ProcessBridge. VerifyAgentProfile rechecks the
// immutable configured Pi tuple against the held launch closure;
// StartPreparedRun commits the exact PreparationDigest outcome through the
// runstore's only Run-start seam, which drives the sealed fresh-start
// mechanics (supervisor start, bind/spawn/resume checkpoints, proof mint)
// inside ResultIngress.
type piBridge struct {
	ledger *CompositionLedger
}

var _ ProcessBridge = (*piBridge)(nil)

func (bridge *piBridge) VerifyAgentProfile(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, owner OwnerProjection, profile PiProfile) error {
	closure := bridge.ledger.closure
	identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
	if err != nil || identity.IdentityDigest != profile.IdentityDigest() {
		return application.NewError("verify-agent-profile", application.ReasonBridgeUnavailable)
	}
	if closure.RuntimeExecutable.CanonicalPath != profile.ExecutablePath() {
		return application.NewError("verify-agent-profile", application.ReasonBridgeUnavailable)
	}
	return nil
}

func (bridge *piBridge) StartPreparedRun(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, owner OwnerProjection, profile PiProfile, prepared application.PreparedRunStart) error {
	if err := bridge.VerifyAgentProfile(ctx, verifier, acquisition, owner, profile); err != nil {
		return err
	}
	dispatchObservationDigest, err := bridge.ledger.localDispatchObservationDigest(prepared.AttemptID)
	if err != nil {
		return err
	}
	projection, err := bridge.ledger.runs.WithPreparedRunStartAuthority(ctx, bridge.ledger.runLease, prepared, dispatchObservationDigest, func(projector resultingress.RunStartProjector) error {
		return bridge.ledger.ingress.CommitMacRunStart(ctx, verifier, acquisition, prepared, projector)
	})
	if err != nil {
		return err
	}
	if projection.RunID != prepared.RunID || projection.AttemptID != prepared.AttemptID {
		return application.NewError("start-prepared-run", application.ReasonAuthorityConflict)
	}
	return nil
}

// resourceCloser adapts the composition resources to io.Closer for the
// runtime's reverse-order construction cleanup.
type resourceCloser struct {
	name  string
	close func() error
}

func (resource *resourceCloser) Close() error {
	if err := resource.close(); err != nil {
		return fmt.Errorf("%s: %w", resource.name, err)
	}
	return nil
}

// ComposeResult hands the CLI the ready Runtime plus the durable pieces the
// task-run path drives one attempt with. The controller stays private.
type ComposeResult struct {
	Runtime *Runtime
	RunID   string
}

// ComposeRuntime is the single exported production composition entry. It
// performs the two-phase repository owner acquisition over the held owner
// directory, builds the ledger, controller and bridge behind that one
// acquisition and returns a ready Runtime. Construction failure closes every
// transferred resource in reverse order; Runtime.Close closes the owner last.
func ComposeRuntime(ctx context.Context, inputs CompositionInputs, profile PiProfile) (*ComposeResult, error) {
	if profile.Validate() != nil || (inputs.RepositorySession == nil && inputs.OwnerDirectory == nil) {
		return nil, application.NewError("compose-runtime", application.ReasonInvalidRequest)
	}
	ledger, err := NewCompositionLedger(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if ledger.owner == nil {
		return nil, application.NewError("compose-runtime", application.ReasonOwnerUnavailable)
	}
	runtime, err := newComposedProductionRuntime(ledger, profile, inputs)
	if err != nil {
		return nil, fmt.Errorf("compose: runtime: %v", err)
	}
	return &ComposeResult{Runtime: runtime, RunID: inputs.RunID}, nil
}
