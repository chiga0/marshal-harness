//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
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
		if errors.Is(e, resultingress.ErrTeamOutcomeNotReady) || errors.Is(e, runstore.ErrLeaseHeld) {
			return nil
		}
		if e != nil && step.Err() == nil {
			a.teamProgressStopped.Store(true)
		}
		return e
	})
	return err
}
