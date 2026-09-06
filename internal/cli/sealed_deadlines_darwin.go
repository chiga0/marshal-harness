//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func (adapter *sealedRepositoryApplication) trackDeadlineRun(runID string) {
	if adapter.deadlineRuns == nil {
		adapter.deadlineRuns = make(map[string]struct{})
	}
	adapter.deadlineRuns[runID] = struct{}{}
}

// A bounded, fair batch prevents a failed early Run from starving successors.
// The cursor/index are hints and never stand in for ledger authority.
func deadlineRunBatch(runs map[string]struct{}, after string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	start := sort.SearchStrings(ids, after)
	for start < len(ids) && ids[start] <= after {
		start++
	}
	ordered := append(ids[start:], ids[:start]...)
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func (adapter *sealedRepositoryApplication) advanceBusinessDeadlines(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return application.NewError("advance-business-deadlines", application.ReasonInvalidRequest)
	}
	// Never queue a background writer behind an in-flight public mutation.
	if !adapter.mu.TryLock() {
		return nil
	}
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.session == nil {
		return application.NewError("advance-business-deadlines", application.ReasonOwnerUnavailable)
	}
	var failures []error
	for _, runID := range deadlineRunBatch(adapter.deadlineRuns, adapter.deadlineCursor, 3) {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		adapter.deadlineCursor = runID
		current, err := adapter.session.InspectRun(ctx, application.InspectRunRequest{RunID: runID})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current.State == domain.StateBlocked {
			if _, _, err := adapter.session.RecoverStoppedRun(ctx, runID); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if current.State != domain.StateRunning {
			delete(adapter.deadlineRuns, runID)
			continue
		}
		run, err := adapter.openRun(ctx, runID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		err = run.runtime.ReconcileBusinessStop(ctx, runID)
		err = errors.Join(err, run.Close())
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// The fixed server owns this loop and drains it before releasing its owner.
// No tick creates a Run, launches a Worker or supplies stop authority.
func driveBusinessDeadlines(ctx context.Context, ticks <-chan time.Time, advance func(context.Context) error, report func(error)) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			step, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := advance(step)
			cancel()
			if err != nil && ctx.Err() == nil {
				report(err)
			}
		}
	}
}
