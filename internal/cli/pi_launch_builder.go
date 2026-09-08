package cli

import (
	"encoding/json"
	"strings"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

// piProductionLaunchBuilder returns the injected argv builder that maps the
// precise reserved attempt identity and the frozen task fields to the
// deterministic Pi 0.84.4 production argv through adapter/pi's
// BuildProductionLaunch. It is the only seam where the fixed CLI imports the
// pi adapter; productionruntime receives the result as an opaque
// AttemptLaunchArgvBuilder and never imports adapter/pi.
//
// The builder is pure: the canonical Node runtime and Pi entrypoint are
// frozen at composition time, and task.Worker.ExecutionProfile/Model plus
// task.Work Objective/Context/Constraints/NonGoals are frozen by the TaskSpec; the only
// per-attempt variable inputs are the precise TaskID/RunID/AttemptID handed
// in by productionruntime after ReserveAttempt and ensureAttemptLease.
// Identical identity inputs therefore produce identical argv bytes, so fresh
// and replay seal a byte-identical closure.
func piProductionLaunchBuilder(nodeRuntime, entrypoint string, task domain.TaskSpec) productionruntime.AttemptLaunchArgvBuilder {
	objective := task.Work.Objective
	if len(task.Work.Context) != 0 {
		objective += "\n\nContext:\n" + strings.Join(task.Work.Context, "\n")
	}
	if len(task.Work.NonGoals) != 0 {
		objective += "\n\nNon-goals:\n" + strings.Join(task.Work.NonGoals, "\n")
	}
	return func(identity productionruntime.AttemptLaunchIdentity) (productionruntime.AttemptLaunchArgv, error) {
		if task.Work.Objective == "" {
			return productionruntime.AttemptLaunchArgv{}, application.NewError("pi-launch-objective", application.ReasonInvalidRequest)
		}
		out, err := pi.BuildProductionLaunch(pi.ProductionLaunchInput{
			ResultContract: task.Worker.ResultContract,
			NodeRuntime:    nodeRuntime,
			Entrypoint:     entrypoint,
			Profile:        task.Worker.ExecutionProfile,
			Model:          task.Worker.Model,
			TaskID:         identity.TaskID,
			RunID:          identity.RunID,
			AttemptID:      identity.AttemptID,
			Objective:      objective,
			Constraints:    task.Work.Constraints,
		})
		if err != nil {
			return productionruntime.AttemptLaunchArgv{}, err
		}
		return productionruntime.AttemptLaunchArgv{Argv: out.Argv, Prompt: out.Prompt}, nil
	}
}

// Preflight the actual production builder before approving ANY node. This
// placeholder is only a worst-case byte-length bound, never an Attempt ID
// used for reservation, launch or durable evidence. Start builds again with
// the precise reserved identity and the same frozen business text.
func preflightPiTeamLaunch(nodeRuntime, entrypoint string, inputs goal.TeamInputs) error {
	for _, node := range inputs.Nodes {
		var task domain.TaskSpec
		if json.Unmarshal(node.Task, &task) != nil {
			return application.NewError("team-launch-preflight", application.ReasonInvalidRequest)
		}
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil {
			return application.NewError("team-launch-preflight", application.ReasonInvalidRequest)
		}
		_, err = piProductionLaunchBuilder(nodeRuntime, entrypoint, task)(productionruntime.AttemptLaunchIdentity{
			TaskID: taskID, RunID: runID, AttemptID: strings.Repeat("a", 128),
		})
		if err != nil {
			return application.NewError("team-launch-preflight", application.ReasonInvalidRequest)
		}
	}
	return nil
}
