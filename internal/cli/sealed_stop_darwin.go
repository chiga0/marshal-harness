//go:build darwin && arm64

package cli

import (
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func (adapter *sealedRepositoryApplication) CancelRun(ctx context.Context, request application.CancelRunRequest) (result application.CancelRunProjection, resultErr error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.session == nil || ctx == nil || request.Validate() != nil {
		return result, application.NewError("cancel-run", application.ReasonInvalidRequest)
	}
	// A completed stop must remain replayable without reconstructing a
	// Worker runtime or relying on a released worktree/launch closure.
	if recovered, found, err := adapter.session.ReconcileStoppedRun(ctx, request); err != nil || found {
		return recovered, err
	}
	current, err := adapter.session.InspectRun(ctx, application.InspectRunRequest{RunID: request.RunID})
	if err != nil {
		return result, err
	}
	if !currentRunMatches(current, request.CurrentRunRequest, domain.StateRunning) {
		return result, application.NewError("cancel-run", application.ReasonAuthorityConflict)
	}
	run, err := adapter.openRun(ctx, request.RunID)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, run.Close()) }()
	return run.runtime.CancelRun(ctx, request)
}
