//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

type teamProgressApplication interface {
	InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error)
	CollectRunResult(context.Context, application.CollectRunResultRequest) (application.CollectedRunProjection, error)
	VerifyRun(context.Context, application.VerifyRunRequest) (application.VerificationProjection, error)
}

// advanceTeamRun never starts, retries, signs a Decision, or guesses a new
// head after a conflict. A selector that became stale simply awaits a new tick.
func advanceTeamRun(ctx context.Context, port teamProgressApplication, selected application.RunProjection) error {
	current, err := port.InspectRun(ctx, application.InspectRunRequest{RunID: selected.RunID})
	if err != nil {
		return err
	}
	if current != selected {
		return nil
	}
	input := application.CurrentRunRequest{RunID: current.RunID, AttemptID: current.AttemptID, ExpectedSequence: current.Sequence, ExpectedAuthorityHead: current.AuthorityHead}
	switch current.State {
	case domain.StateRunning:
		request := application.CollectRunResultRequest(input)
		if request.Validate() != nil {
			return application.NewError("team-progress", application.ReasonAuthorityConflict)
		}
		result, err := port.CollectRunResult(ctx, request)
		if application.HasReason(err, application.ReasonAttemptStillRunning) && result == (application.CollectedRunProjection{}) {
			return nil // Positive live observation, not an unknown delivery retry.
		}
		if err != nil {
			return err
		}
		if result.Validate() != nil || !teamProgressSuccessor(current, result.Run) {
			return application.NewError("team-progress", application.ReasonAuthorityConflict)
		}
	case domain.StateVerifying:
		request := application.VerifyRunRequest(input)
		if request.Validate() != nil {
			return application.NewError("team-progress", application.ReasonAuthorityConflict)
		}
		result, err := port.VerifyRun(ctx, request)
		if err != nil {
			return err
		}
		if result.Validate() != nil || !teamProgressSuccessor(current, result.Run) {
			return application.NewError("team-progress", application.ReasonAuthorityConflict)
		}
	default:
		return application.NewError("team-progress", application.ReasonInvalidRequest)
	}
	return nil
}

func teamProgressSuccessor(before, after application.RunProjection) bool {
	return before.RunID == after.RunID && before.TaskID == after.TaskID && before.AttemptID == after.AttemptID && after.Sequence == before.Sequence+1 && after.AuthorityHead != before.AuthorityHead
}

func (adapter *sealedRepositoryApplication) advanceInitialTeamProgress(ctx context.Context, router *fixedcontrolplane.HTTPRouter, phase domain.State) error {
	return adapter.advanceInitialTeamProgressAdmitted(ctx, router, phase, nil)
}

func (adapter *sealedRepositoryApplication) advanceInitialTeamProgressAdmitted(ctx context.Context, router *fixedcontrolplane.HTTPRouter, phase domain.State, admitted func()) error {
	if ctx == nil || ctx.Err() != nil || router == nil || phase != domain.StateRunning && phase != domain.StateVerifying {
		return application.NewError("team-progress", application.ReasonInvalidRequest)
	}
	if !adapter.mu.TryLock() {
		return nil
	}
	if adapter.closed || adapter.session == nil {
		adapter.mu.Unlock()
		return application.NewError("team-progress", application.ReasonOwnerUnavailable)
	}
	if adapter.teamProgressStopped.Load() {
		adapter.mu.Unlock()
		return nil
	}
	cursor := &adapter.teamCollectCursor
	if phase == domain.StateVerifying {
		cursor = &adapter.teamVerifyCursor
	}
	selection, found, err := adapter.session.NextInitialTeamProgress(ctx, *cursor, phase)
	if err != nil && ctx.Err() == nil {
		adapter.teamProgressStopped.Store(true) // No trustworthy Goal to halt.
	}
	if found {
		*cursor = selection.RunID
	}
	adapter.mu.Unlock()
	if err != nil || !found {
		return err
	}
	stage := "collect"
	if phase == domain.StateVerifying {
		stage = "verify"
	}
	_, err = router.TryBackgroundRunMutation(ctx, selection.RunID, phase == domain.StateVerifying, func(step context.Context) error {
		if adapter.teamProgressStopped.Load() {
			return nil
		}
		allowed, err := adapter.teamProgressAllowed(step, selection)
		if err != nil {
			if step.Err() == nil {
				adapter.teamProgressStopped.Store(true)
			}
			return err
		}
		if !allowed || step.Err() != nil {
			return step.Err()
		}
		if admitted != nil {
			// Selection, the original Run lane and current team admission are
			// complete. Keep the Run lane until the operation returns, but let
			// the short scheduler serve siblings during long verification.
			admitted()
		}
		if err := advanceTeamRun(step, adapter, selection.Run); err != nil {
			// An operation error is never retried on a fresh tick/timeout. A
			// stopped Run already has its own Outcome; halt does not replace it.
			return errors.Join(err, adapter.haltTeamProgress(step, selection, stage))
		}
		return nil
	})
	return err
}

func (adapter *sealedRepositoryApplication) teamProgressAllowed(ctx context.Context, selection productionruntime.InitialTeamProgress) (bool, error) {
	adapter.statusMu.RLock()
	defer adapter.statusMu.RUnlock()
	if adapter.closed || adapter.session == nil {
		return false, application.NewError("team-progress", application.ReasonOwnerUnavailable)
	}
	return adapter.session.TeamProgressAllowed(ctx, selection)
}

func (adapter *sealedRepositoryApplication) haltTeamProgress(ctx context.Context, selection productionruntime.InitialTeamProgress, stage string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	adapter.statusMu.RLock()
	defer adapter.statusMu.RUnlock()
	var err error
	if adapter.closed || adapter.session == nil {
		err = application.NewError("team-progress", application.ReasonOwnerUnavailable)
	} else {
		_, err = adapter.session.HaltInitialTeam(cleanup, selection.GoalID, selection.NodeID, selection.PlanFactDigest, stage)
	}
	if err != nil {
		adapter.teamProgressStopped.Store(true)
	}
	return err
}
