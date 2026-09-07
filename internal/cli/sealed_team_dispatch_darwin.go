//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

// advanceInitialTeams shares the router writer lane and application write
// lock, but never holds the repository owner lock through preparation/launch.
// The initial profile deliberately caps repository busy work at two; it is
// not a claim of adaptive host resource scheduling.
func (adapter *sealedRepositoryApplication) advanceInitialTeams(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return application.NewError("advance-initial-teams", application.ReasonInvalidRequest)
	}
	if !adapter.mu.TryLock() {
		return nil
	}
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.session == nil {
		return application.NewError("advance-initial-teams", application.ReasonOwnerUnavailable)
	}
	if adapter.teamDispatchStopped {
		return nil
	}
	selection, found, err := adapter.session.NextInitialTeamDispatch(ctx, 2)
	if err != nil {
		if ctx.Err() != nil {
			// Selection is read-only. A canceled observation may be retried
			// by a later tick without repeating any paid work or mutation.
			return err
		}
		// No trustworthy selected plan exists on read failure. Do not retry an
		// unknown queue or fabricate a durable halt against a guessed Goal.
		adapter.teamDispatchStopped = true
		return err
	}
	if !found {
		err := adapter.session.FinalizeReadyInitialTeams(ctx)
		if err != nil && ctx.Err() == nil {
			adapter.teamDispatchStopped = true
		}
		return err
	}
	halt := func(stage string, cause error) error {
		// A canceled step must still attempt one bounded diagnostic commit.
		// Failure to prove that commit stops this process's team dispatch.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, haltErr := adapter.session.HaltInitialTeam(cleanup, selection.GoalID, selection.NodeID, selection.PlanFactDigest, stage)
		if haltErr != nil {
			adapter.teamDispatchStopped = true
		}
		return errors.Join(cause, haltErr)
	}
	created, err := adapter.session.MaterializeApprovedInitialTeamRun(ctx, selection.GoalID, selection.NodeID, selection.PlanFactDigest)
	if errors.Is(err, productionruntime.ErrTeamIntegrationWaiting) {
		// A lease can become occupied after selection. This sentinel is only
		// emitted before Prepare/Git/Run mutation, so wait for a later tick.
		return nil
	}
	if err != nil {
		return halt("materialize", err)
	}
	if created.RunID != selection.RunID {
		return halt("materialize", application.NewError("team-created-run", application.ReasonAuthorityConflict))
	}
	current, err := adapter.session.InspectRun(ctx, application.InspectRunRequest{RunID: selection.RunID})
	if err != nil {
		return halt("inspect", err)
	}
	if current.State != domain.StateReady || current.Sequence != 2 || current.AttemptID != "" {
		return halt("inspect", application.NewError("team-ready-run", application.ReasonAuthorityConflict))
	}
	_, err = adapter.startRunLocked(ctx, application.StartRunRequest{RunID: current.RunID, ExpectedSequence: current.Sequence, ExpectedAuthorityHead: current.AuthorityHead})
	if err != nil {
		return halt("start", err)
	}
	return nil
}
