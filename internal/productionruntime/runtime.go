package productionruntime

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/application"
)

type Runtime struct {
	controller *controller
}

var _ application.PublicApplicationPort = (*Runtime)(nil)

// newRuntime is package-private until the complete composition root can
// construct all mandatory components. This foundation cannot be selected by
// CLI/server and cannot truthfully report production readiness.
func newRuntime(controller *controller) (*Runtime, error) {
	if _, err := platformProfile(); err != nil {
		return nil, err
	}
	if controller == nil || controller.profile.Validate() != nil || controller.ownerLock == nil {
		return nil, application.NewError("production-runtime", application.ReasonInvalidRequest)
	}
	if err := controller.ownerLock.claimRuntime(); err != nil {
		return nil, err
	}
	return &Runtime{controller: controller}, nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil || runtime.controller == nil || runtime.controller.ownerLock == nil {
		return nil
	}
	return runtime.controller.ownerLock.Close()
}

func (runtime *Runtime) Status(ctx context.Context, _ application.StatusRequest) (application.StatusProjection, error) {
	if runtime == nil || runtime.controller == nil || runtime.controller.profile.Validate() != nil {
		return application.StatusProjection{}, application.NewError("status", application.ReasonBridgeUnavailable)
	}
	owner, err := runtime.controller.status(ctx)
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
	return runtime.validatedStatus(application.AvailabilityUnavailable, application.ReasonCompositionIncomplete, owner)
}

func (runtime *Runtime) PrepareRunStart(ctx context.Context, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	if runtime == nil || runtime.controller == nil {
		return application.PreparedRunStart{}, application.NewError("prepare-run-start", application.ReasonBridgeUnavailable)
	}
	return runtime.controller.prepareRunStart(ctx, request)
}

func (runtime *Runtime) StartPreparedRun(ctx context.Context, prepared application.PreparedRunStart) (application.RunProjection, error) {
	if runtime == nil || runtime.controller == nil {
		return application.RunProjection{}, application.NewError("start-prepared-run", application.ReasonBridgeUnavailable)
	}
	return runtime.controller.startPreparedRun(ctx, prepared)
}

func (runtime *Runtime) InspectRun(ctx context.Context, request application.InspectRunRequest) (application.RunProjection, error) {
	if runtime == nil || runtime.controller == nil {
		return application.RunProjection{}, application.NewError("inspect-run", application.ReasonBridgeUnavailable)
	}
	return runtime.controller.inspectRun(ctx, request)
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
