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
		id, runs, e = a.selectTaskCancellation(func(cursor string) (string, []application.RunProjection, error) {
			return a.session.PendingTaskCancellation(ctx, cursor)
		})
		return e
	})
	if err != nil || id == "" {
		return err
	}
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

// Selection can identify a Task before one of its local Run reads fails.
// Advance that scheduling hint even on failure so an intervention does not
// starve healthy siblings. The error is still returned, including owner/RB1
// failures; this cursor is never evidence for cleanup or cancellation.
func (a *sealedRepositoryApplication) selectTaskCancellation(read func(string) (string, []application.RunProjection, error)) (string, []application.RunProjection, error) {
	id, runs, err := read(a.taskCancelCursor)
	if id != "" {
		a.taskCancelCursor = id
	}
	return id, runs, err
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
