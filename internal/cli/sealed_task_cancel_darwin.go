//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func (a *sealedRepositoryApplication) advanceTaskCancellations(ctx context.Context, router *fixedcontrolplane.HTTPRouter) error {
	var id string
	var runs []application.RunProjection
	err := a.withTaskCancellationRead(func() error {
		var e error
		id, runs, e = a.session.PendingTaskCancellation(ctx, a.taskCancelCursor)
		return e
	})
	if err != nil || id == "" {
		return err
	}
	a.taskCancelCursor = id
	var cleanupErr error
	for _, run := range runs {
		if run.State != domain.StateRunning && run.State != domain.StateReady {
			continue
		}
		_, err = router.TryBackgroundRunMutation(ctx, run.RunID, false, func(step context.Context) error {
			if run.State == domain.StateReady {
				a.mu.Lock()
				defer a.mu.Unlock()
				if a.closed {
					return application.NewError("cancel-task", application.ReasonOwnerUnavailable)
				}
				return a.session.CancelTaskReadyReservation(step, run.RunID, a.leaseLedger)
			}
			_, e := a.CancelRun(step, application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: run.RunID, AttemptID: run.AttemptID, ExpectedSequence: run.Sequence, ExpectedAuthorityHead: run.AuthorityHead}, RequestID: "task-cancel-" + id})
			return e
		})
		cleanupErr = errors.Join(cleanupErr, err)
	}
	_, err = router.TryBackgroundMutation(ctx, func(step context.Context) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return application.NewError("cancel-task", application.ReasonOwnerUnavailable)
		}
		return a.session.FinishTaskCancellation(step, id)
	})
	if errors.Is(err, runstore.ErrLeaseHeld) {
		return cleanupErr
	} // An admitted Verify is still draining.
	return errors.Join(cleanupErr, err)
}

// Read lifetime guard is released before any mutation takes mu. Holding this
// RLock across CancelRun would invert Close's mu -> statusMu lock order.
func (a *sealedRepositoryApplication) withTaskCancellationRead(read func() error) error {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed || a.session == nil {
		return nil
	}
	return read()
}
