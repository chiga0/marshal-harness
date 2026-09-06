package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// InitialTeamDispatch is a selector only. Materialization and Start must still
// recheck their original current-ledger and exact Run authority gates.
type InitialTeamDispatch struct {
	GoalID         string
	NodeID         string
	RunID          string
	PlanFactDigest string
}

// NextInitialTeamDispatch reads real authority while the caller serializes
// repository mutations. It never probes, creates a Run, or releases budget.
// A damaged/partial existing Run is not interpreted as a missing Run.
func (session *RepositorySession) NextInitialTeamDispatch(ctx context.Context, capacity int) (selection InitialTeamDispatch, found bool, err error) {
	if ctx == nil || capacity < 0 || capacity > 3 {
		return selection, false, application.NewError("team-dispatch", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return selection, false, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: session}
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plans, err := session.ingress.ListTeamPlans(session.acquisition.Scope)
		if err != nil || len(plans) == 0 || capacity == 0 {
			return err
		}
		halts := make(map[string]bool, len(plans))
		for _, plan := range plans {
			_, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, plan.Revision.GoalId)
			if err != nil {
				return err
			}
			halts[plan.Revision.GoalId] = halted
		}
		ids, err := session.runs.ListExistingRunIDs()
		if err != nil {
			return err
		}
		states := make(map[string]application.RunProjection, len(ids))
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			lease, err := session.runs.AcquireExisting(id)
			if errors.Is(err, runstore.ErrLeaseHeld) {
				// Verification legitimately holds its Run lease outside the
				// global writer lane. Unknown capacity means no dispatch this
				// tick, not corruption, a missing Run or a permanent halt.
				return nil
			}
			if err != nil {
				return err
			}
			authority, readErr := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
			if err := errors.Join(readErr, lease.Release()); err != nil {
				return err
			}
			states[id] = authority.Run
		}
		selection, found, err = selectInitialTeamDispatch(plans, halts, states, capacity)
		return err
	})
	return
}

func teamRunBusy(state domain.State) bool {
	switch state {
	case domain.StateRunning, domain.StateRetryPending, domain.StateVerifying, domain.StateReviewPending, domain.StateReworkRequested, domain.StatePublishing, domain.StatePublished, domain.StateCIPending:
		return true
	default:
		return false
	}
}

// Pure policy; its states must come from the method above, never caller claims.
func selectInitialTeamDispatch(plans []resultingress.TeamPlanState, halts map[string]bool, states map[string]application.RunProjection, capacity int) (InitialTeamDispatch, bool, error) {
	fail := func() (InitialTeamDispatch, bool, error) {
		return InitialTeamDispatch{}, false, application.NewError("team-dispatch", application.ReasonAuthorityConflict)
	}
	if capacity <= 0 {
		return InitialTeamDispatch{}, false, nil
	}
	runGoals := map[string]string{}
	inputs := map[string]goal.TeamInputs{}
	byGoal := map[string]resultingress.TeamPlanState{}
	order := make([]string, 0, len(plans))
	for _, plan := range plans {
		id := plan.Revision.GoalId
		if _, exists := byGoal[id]; exists {
			return fail()
		}
		var input goal.TeamInputs
		if json.Unmarshal(plan.Inputs, &input) != nil || input.Spec.GoalId != id {
			return fail()
		}
		inputs[id], byGoal[id] = input, plan
		order = append(order, id)
		for _, node := range plan.Materializations {
			if _, duplicate := runGoals[node.RunID]; duplicate {
				return fail()
			}
			runGoals[node.RunID] = id
		}
	}
	busy := 0
	busyGoals := map[string]int{}
	standalone := false
	for id, state := range states {
		if state.RunID != id || state.Validate() != nil {
			return fail()
		}
		if !teamRunBusy(state.State) {
			continue
		}
		busy++
		if id, found := runGoals[id]; found {
			busyGoals[id]++
		} else {
			standalone = true
		}
	}
	if standalone || busy >= capacity || len(busyGoals) > 1 {
		return InitialTeamDispatch{}, false, nil
	}
	sort.Strings(order)
	for _, id := range order {
		if halts[id] || (len(busyGoals) > 0 && busyGoals[id] == 0) {
			continue
		}
		input, plan := inputs[id], byGoal[id]
		if int64(busyGoals[id]) >= input.Limits.MaxConcurrentNodes {
			continue
		}
		for _, node := range input.Nodes {
			if node.Role != "implement" {
				continue
			}
			dependent := false
			for _, edge := range input.Proposal.Edges {
				if edge.To == node.NodeID {
					dependent = true
				}
			}
			if dependent {
				continue
			}
			_, runID, err := goal.TeamNodeIDs(input.Proposal, node.NodeID)
			if err != nil || runGoals[runID] != id {
				return fail()
			}
			if state, exists := states[runID]; exists {
				if state.State == domain.StateCreated || state.State == domain.StatePlanned {
					return fail()
				}
				if state.State != domain.StateReady {
					continue
				}
				if state.Sequence != 2 || state.AttemptID != "" {
					return fail()
				}
			}
			return InitialTeamDispatch{GoalID: id, NodeID: node.NodeID, RunID: runID, PlanFactDigest: plan.FactDigest}, true, nil
		}
	}
	return InitialTeamDispatch{}, false, nil
}
