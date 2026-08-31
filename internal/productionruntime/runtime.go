package productionruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
)

type Runtime struct {
	controller *controller
	mu         sync.RWMutex
	closed     bool
	ready      bool
	resources  []io.Closer
}

var _ application.PublicApplicationPort = (*Runtime)(nil)

// newRuntime is package-private until the complete composition root can
// construct all mandatory components. This foundation cannot be selected by
// CLI/server and cannot truthfully report production readiness.
func newRuntime(controller *controller) (*Runtime, error) {
	return newRuntimeWithReadiness(controller, false)
}

// newProductionRuntime is deliberately package-private. Only the fixed
// production factory may call it after the complete mandatory graph has been
// constructed and recovered. Keeping the readiness bit out of Config prevents
// callers from turning a partially composed Runtime into an available one.
// Ownership of every non-nil resource transfers at entry: construction
// failure closes them in reverse order and closes the owner last.
func newProductionRuntime(controller *controller, resources ...io.Closer) (*Runtime, error) {
	for _, resource := range resources {
		if resource == nil {
			cleanupErr := closeRuntimeConstruction(controller, resources)
			return nil, errors.Join(application.NewError("production-runtime", application.ReasonInvalidRequest), cleanupErr)
		}
	}
	runtime, err := newRuntimeWithReadiness(controller, true)
	if err != nil {
		return nil, errors.Join(err, closeRuntimeConstruction(controller, resources))
	}
	runtime.resources = append([]io.Closer(nil), resources...)
	return runtime, nil
}

// newComposedProductionRuntime is the only composition-local constructor for
// the private controller and production Runtime. Keeping this mutation in the
// runtime construction file preserves the architecture gate while leaving
// ComposeRuntime as the public, validated composition entry point.
func newComposedProductionRuntime(ledger *CompositionLedger, profile PiProfile, inputs CompositionInputs) (*Runtime, error) {
	if ledger == nil || ledger.owner == nil {
		return nil, application.NewError("production-runtime", application.ReasonOwnerUnavailable)
	}
	// NewCompositionLedger accepts OwnerEpoch=0 as the composition-boundary
	// request for "acquire the next durable epoch" and replaces it with the
	// exact acquisition bound to the phase-B owner lock.  Never keep using the
	// caller candidate here: doing so makes the first real composition fail
	// newController even though the durable owner was acquired successfully.
	controller, err := newController(ledger, &piBridge{ledger: ledger}, ledger.owner, ledger.owner.acquisition(), profile)
	if err != nil {
		// Release every resource the ledger owns (Run Lease for path B, the
		// held result-ingress, and the bound owner) in reverse acquisition
		// order. Runtime.Close is not yet wired, so construction failure must
		// release everything by hand.
		_ = ledger.Close()
		return nil, fmt.Errorf("compose: controller: %v", err)
	}
	// The Runtime owns the result-ingress and the Run Lease for its lifetime.
	// Use the ledger's final ingress/runLease (path B acquires the Run Lease
	// inside NewCompositionLedger; the held ingress may differ from the
	// original inputs.Ingress, which is nil for the HeldIngressDir path).
	return newProductionRuntime(controller,
		&resourceCloser{name: "result-ingress", close: ledger.ingress.Close},
		&resourceCloser{name: "run-lease", close: func() error { return ledger.runLease.Release() }},
	)
}

func closeRuntimeConstruction(controller *controller, resources []io.Closer) error {
	var closeErrors []error
	for index := len(resources) - 1; index >= 0; index-- {
		if resources[index] != nil {
			closeErrors = append(closeErrors, resources[index].Close())
		}
	}
	if controller != nil && controller.ownerLock != nil {
		closeErrors = append(closeErrors, controller.ownerLock.Close())
	}
	return errors.Join(closeErrors...)
}

func newRuntimeWithReadiness(controller *controller, ready bool) (*Runtime, error) {
	if _, err := platformProfile(); err != nil {
		return nil, err
	}
	if controller == nil || controller.profile.Validate() != nil || controller.ownerLock == nil {
		return nil, application.NewError("production-runtime", application.ReasonInvalidRequest)
	}
	if err := controller.ownerLock.claimRuntime(); err != nil {
		return nil, err
	}
	return &Runtime{controller: controller, ready: ready}, nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return nil
	}
	runtime.closed = true
	var closeErrors []error
	for index := len(runtime.resources) - 1; index >= 0; index-- {
		closeErrors = append(closeErrors, runtime.resources[index].Close())
	}
	runtime.resources = nil
	if runtime.controller != nil && runtime.controller.ownerLock != nil {
		closeErrors = append(closeErrors, runtime.controller.ownerLock.Close())
	}
	return errors.Join(closeErrors...)
}

func (runtime *Runtime) Status(ctx context.Context, _ application.StatusRequest) (application.StatusProjection, error) {
	controller, ready, release, err := runtime.beginOperation("status")
	if err != nil {
		return application.StatusProjection{}, application.NewError("status", application.ReasonBridgeUnavailable)
	}
	defer release()
	if controller.profile.Validate() != nil {
		return application.StatusProjection{}, application.NewError("status", application.ReasonBridgeUnavailable)
	}
	owner, err := controller.status(ctx)
	if err != nil {
		reason := application.ReasonBridgeUnavailable
		for _, candidate := range []application.ReasonCode{application.ReasonOwnerUnavailable, application.ReasonOwnerNotCurrent, application.ReasonAuthorityConflict} {
			if application.HasReason(err, candidate) {
				reason = candidate
				break
			}
		}
		return runtime.validatedStatus(application.AvailabilityUnavailable, reason, OwnerProjection{})
	}
	if owner.PendingRecovery != 0 {
		return runtime.validatedStatus(application.AvailabilityRecoveryRequired, application.ReasonRecoveryRequired, owner)
	}
	// The foundation deliberately remains non-ready until a later composition
	// supplies a closed readiness projection for coordinator, allocation
	// journal, launch bridge, ResultIngress and the remaining mandatory parts.
	if !ready {
		return runtime.validatedStatus(application.AvailabilityUnavailable, application.ReasonCompositionIncomplete, owner)
	}
	return runtime.validatedStatus(application.AvailabilityReady, "", owner)
}

func (runtime *Runtime) PrepareRunStart(ctx context.Context, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	controller, _, release, err := runtime.beginOperation("prepare-run-start")
	if err != nil {
		return application.PreparedRunStart{}, err
	}
	defer release()
	return controller.prepareRunStart(ctx, request)
}

func (runtime *Runtime) StartPreparedRun(ctx context.Context, prepared application.PreparedRunStart) (application.RunProjection, error) {
	controller, _, release, err := runtime.beginOperation("start-prepared-run")
	if err != nil {
		return application.RunProjection{}, err
	}
	defer release()
	return controller.startPreparedRun(ctx, prepared)
}

func (runtime *Runtime) InspectRun(ctx context.Context, request application.InspectRunRequest) (application.RunProjection, error) {
	controller, _, release, err := runtime.beginOperation("inspect-run")
	if err != nil {
		return application.RunProjection{}, err
	}
	defer release()
	return controller.inspectRun(ctx, request)
}

// CollectRunResult advances an exact terminal production attempt from
// RUNNING to VERIFYING after descriptor-bound transcript collection and
// durable ResultIngress admission.
func (runtime *Runtime) CollectRunResult(ctx context.Context, runID string) (CollectedRunResult, error) {
	controller, _, release, err := runtime.beginOperation("collect-run-result")
	if err != nil {
		return CollectedRunResult{}, err
	}
	defer release()
	return controller.collectRunResult(ctx, runID)
}

// beginOperation keeps Runtime.Close behind every in-flight operation and
// rejects operations that begin after close. The owner verifier itself remains
// the authority gate; this lock only makes its process lifecycle deterministic.
func (runtime *Runtime) beginOperation(operation string) (*controller, bool, func(), error) {
	if runtime == nil {
		return nil, false, nil, application.NewError(operation, application.ReasonBridgeUnavailable)
	}
	runtime.mu.RLock()
	if runtime.closed || runtime.controller == nil {
		runtime.mu.RUnlock()
		return nil, false, nil, application.NewError(operation, application.ReasonBridgeUnavailable)
	}
	return runtime.controller, runtime.ready, runtime.mu.RUnlock, nil
}

func (runtime *Runtime) validatedStatus(availability application.Availability, reason application.ReasonCode, owner OwnerProjection) (application.StatusProjection, error) {
	profile := runtime.controller.profile
	status := application.StatusProjection{
		ProtocolRevision:    application.ProtocolRevision,
		Availability:        availability,
		ReasonCode:          reason,
		PlatformProfileID:   DarwinLocalDogfoodProfile,
		AgentProvider:       PiProviderName,
		AgentVersion:        PiProviderVersion,
		AgentClosureProfile: profile.ClosureProfileID(),
		AgentIdentityDigest: profile.IdentityDigest(),
		OwnerEpoch:          owner.OwnerEpoch,
		OwnerFactDigest:     owner.OwnerFactDigest,
		PendingRecovery:     owner.PendingRecovery,
	}
	if err := status.Validate(); err != nil {
		return application.StatusProjection{}, err
	}
	return status, nil
}
