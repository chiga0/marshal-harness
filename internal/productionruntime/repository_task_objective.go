package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/review"
)

// WithCurrentTaskObjective is internal process composition, not an HTTP
// endpoint. The original Run lease is held by its caller. The same owner
// remains held until the original Decision transaction commits.
func (session *RepositorySession) WithCurrentTaskObjective(ctx context.Context, runID string, taskData []byte, consume func(*review.ObjectivePolicy) error) error {
	if ctx == nil || domain.ValidateID(runID) != nil || consume == nil {
		return application.NewError("task-objective", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	return (repositoryApprovedTeamVerifier{session: session}).WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plans, err := session.ingress.ListTeamPlans(session.acquisition.Scope)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			for _, node := range plan.Materializations {
				if node.RunID != runID {
					continue
				}
				creation, found, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, plan.Revision.GoalId, node.NodeID)
				if err != nil {
					return err
				}
				if !found {
					return application.NewError("task-objective", application.ReasonAuthorityConflict)
				}
				var frozen struct {
					Task json.RawMessage `json:"task"`
				}
				if json.Unmarshal(creation.Inputs, &frozen) != nil || !bytes.Equal(taskData, frozen.Task) {
					return application.NewError("task-objective", application.ReasonAuthorityConflict)
				}
				policy, err := session.taskObjectiveUnderOwner(ctx, creation)
				if err != nil {
					return err
				}
				if policy == nil {
					return application.NewError("task-objective", application.ReasonInvalidRequest)
				}
				return consume(policy)
			}
		}
		return application.NewError("task-objective", application.ReasonAuthorityConflict)
	})
}

// Nil means an original AF_UNIX team, never automatic acceptance eligibility.
// Callers already hold the owner and creating Run lease.
func (session *RepositorySession) taskObjectiveUnderOwner(ctx context.Context, creation resultingress.TeamRunCreationState) (*review.ObjectivePolicy, error) {
	fail := func() (*review.ObjectivePolicy, error) {
		return nil, application.NewError("task-objective", application.ReasonAuthorityConflict)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	draft, found, err := session.ingress.ReadTaskDraft(session.acquisition.Scope, creation.GoalID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, creation.GoalID)
	if err != nil {
		return nil, err
	}
	if !found || draft.Request.Template != goal.TaskTemplateOrderQuote || plan.Approval.TaskDraftDigest != draft.FactDigest || plan.FactDigest != creation.PlanFactDigest || !bytes.Equal(plan.Inputs, draft.Inputs) || session.taskTemplate == nil {
		return fail()
	}
	if _, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, creation.GoalID); err != nil {
		return nil, err
	} else if halted {
		return fail()
	}
	nodes, err := session.taskTemplate.InspectTask(draft.Inputs)
	if err != nil {
		return fail()
	}
	oracle := ""
	for _, n := range nodes {
		if n.ID == creation.NodeID {
			oracle = n.OracleDigest
		}
	}
	var inputs goal.TeamInputs
	var prepared struct {
		Task domain.TaskSpec `json:"task"`
	}
	if oracle == "" || json.Unmarshal(draft.Inputs, &inputs) != nil || json.Unmarshal(creation.Inputs, &prepared) != nil {
		return fail()
	}
	for _, node := range inputs.Nodes {
		if node.NodeID != creation.NodeID {
			continue
		}
		var original domain.TaskSpec
		if json.Unmarshal(node.Task, &original) != nil {
			return fail()
		}
		if creation.Integration != nil {
			original.Repository.BaseRef = prepared.Task.Repository.BaseRef
		}
		if !reflect.DeepEqual(original, prepared.Task) || len(original.Acceptance.Commands) != 1 {
			return fail()
		}
		return &review.ObjectivePolicy{NodeID: node.NodeID, OracleDigest: oracle, Command: original.Acceptance.Commands[0]}, nil
	}
	return fail()
}
