package productionruntime

import (
	"context"
	"errors"
	"sort"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// InitialTeamProgress is only a scheduling hint. The application rechecks the
// exact Run before invoking the existing collect or verification producer.
type InitialTeamProgress struct {
	InitialTeamDispatch
	Run application.RunProjection
}

// NextInitialTeamProgress reads the existing owner, approved plans, creation
// facts and Run journal. No client projection or filesystem name grants team
// membership. A busy Run does not hide a completed sibling from the controller.
func (session *RepositorySession) NextInitialTeamProgress(ctx context.Context, after string, phase domain.State) (selected InitialTeamProgress, found bool, err error) {
	return session.nextInitialTeamProgress(ctx, after, phase, false)
}

// HaltColdInitialTeamVerifications runs before the endpoint or controllers
// start. A pre-existing VERIFYING Run cannot prove its command never began.
// Conservatively halt its team, rather than rerun potentially effectful tests.
// Unlike normal polling, a busy Run fails startup: skipping it here would let
// an unknown interrupted verification escape the cold-start barrier.
func (session *RepositorySession) HaltColdInitialTeamVerifications(ctx context.Context) error {
	for {
		selection, found, err := session.nextInitialTeamProgress(ctx, "", domain.StateVerifying, true)
		if err != nil || !found {
			return err
		}
		if _, err := session.HaltInitialTeam(ctx, selection.GoalID, selection.NodeID, selection.PlanFactDigest, "verify"); err != nil {
			return err
		}
	}
}

func (session *RepositorySession) nextInitialTeamProgress(ctx context.Context, after string, phase domain.State, rejectBusy bool) (selected InitialTeamProgress, found bool, err error) {
	if ctx == nil || ctx.Err() != nil || phase != domain.StateRunning && phase != domain.StateVerifying {
		return selected, false, application.NewError("team-progress", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return selected, false, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: session}
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plans, err := session.ingress.ListTeamPlans(session.acquisition.Scope)
		if err != nil || len(plans) == 0 {
			return err
		}
		ids, err := session.runs.ListExistingRunIDs()
		if err != nil {
			return err
		}
		existing := make(map[string]bool, len(ids))
		for _, id := range ids {
			existing[id] = true
		}
		var candidates []InitialTeamProgress
		seen := map[string]bool{}
		for _, plan := range plans {
			if _, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, plan.Revision.GoalId); err != nil {
				return err
			} else if halted {
				continue
			}
			for _, node := range plan.Materializations {
				if err := ctx.Err(); err != nil {
					return err
				}
				if seen[node.RunID] {
					return application.NewError("team-progress", application.ReasonAuthorityConflict)
				}
				seen[node.RunID] = true
				if !existing[node.RunID] {
					continue // An approved obligation need not be materialized yet.
				}
				creation, created, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, plan.Revision.GoalId, node.NodeID)
				if err != nil {
					return err
				}
				if !created || creation.PlanFactDigest != plan.FactDigest || creation.RunID != node.RunID {
					return application.NewError("team-progress", application.ReasonAuthorityConflict)
				}
				lease, err := session.runs.AcquireExisting(node.RunID)
				if errors.Is(err, runstore.ErrLeaseHeld) && !rejectBusy {
					continue
				}
				if err != nil {
					return err // A partial existing Run is not an absent obligation.
				}
				authority, readErr := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
				if err := errors.Join(readErr, lease.Release()); err != nil {
					return err
				}
				current := authority.Run
				if current.Validate() != nil || current.RunID != node.RunID || current.TaskID != node.TaskID {
					return application.NewError("team-progress", application.ReasonAuthorityConflict)
				}
				if current.State != phase {
					continue
				}
				request := application.CollectRunResultRequest{RunID: current.RunID, AttemptID: current.AttemptID, ExpectedSequence: current.Sequence, ExpectedAuthorityHead: current.AuthorityHead}
				if request.Validate() != nil {
					return application.NewError("team-progress", application.ReasonAuthorityConflict)
				}
				candidates = append(candidates, InitialTeamProgress{InitialTeamDispatch: InitialTeamDispatch{GoalID: plan.Revision.GoalId, NodeID: node.NodeID, RunID: node.RunID, PlanFactDigest: plan.FactDigest}, Run: current})
			}
		}
		selected, found = selectTeamProgress(candidates, after)
		return nil
	})
	return
}

func selectTeamProgress(candidates []InitialTeamProgress, after string) (InitialTeamProgress, bool) {
	if len(candidates) == 0 {
		return InitialTeamProgress{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RunID < candidates[j].RunID })
	index := sort.Search(len(candidates), func(i int) bool { return candidates[i].RunID > after })
	return candidates[index%len(candidates)], true
}

// TeamProgressAllowed is the scheduling admission recheck after acquiring the
// HTTP Run lane. A later halt drains already admitted work; it is not a cancel.
func (session *RepositorySession) TeamProgressAllowed(ctx context.Context, selection InitialTeamProgress) (allowed bool, err error) {
	borrow, err := session.borrow()
	if err != nil {
		return false, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: session}
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, selection.GoalID)
		if err != nil {
			return err
		}
		if !found || plan.FactDigest != selection.PlanFactDigest {
			return application.NewError("team-progress", application.ReasonAuthorityConflict)
		}
		for _, node := range plan.Materializations {
			if node.NodeID == selection.NodeID && node.RunID == selection.RunID && node.TaskID == selection.Run.TaskID && selection.RunID == selection.Run.RunID {
				_, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, selection.GoalID)
				allowed = err == nil && !halted
				return err
			}
		}
		return application.NewError("team-progress", application.ReasonAuthorityConflict)
	})
	return
}
