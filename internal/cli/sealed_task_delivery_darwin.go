//go:build darwin && arm64

package cli

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
)

func (a *sealedRepositoryApplication) advanceTaskDelivery(ctx context.Context, router *fixedcontrolplane.HTTPRouter, admitted func()) error {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed || a.session == nil {
		return application.NewError("task-delivery", application.ReasonOwnerUnavailable)
	}
	if a.teamProgressStopped.Load() {
		return nil
	}
	taskID, runID, err := a.session.NextTaskDelivery(ctx)
	if err != nil {
		return err
	}
	if taskID == "" {
		return nil
	}
	_, err = router.TryBackgroundRunMutation(ctx, runID, true, func(step context.Context) error {
		if admitted != nil {
			admitted()
		}
		_, e := a.session.BuildTaskDelivery(step, taskID)
		if application.HasReason(e, application.ReasonTaskArtifactNotReady) || application.HasReason(e, application.ReasonCapacityBusy) {
			return nil
		}
		if e != nil && step.Err() == nil {
			a.teamProgressStopped.Store(true)
		}
		return e
	})
	return err
}
