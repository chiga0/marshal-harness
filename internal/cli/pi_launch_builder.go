package cli

import (
	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/domain"
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
// task.Work.Objective/Constraints are frozen by the TaskSpec; the only
// per-attempt variable inputs are the precise TaskID/RunID/AttemptID handed
// in by productionruntime after ReserveAttempt and ensureAttemptLease.
// Identical identity inputs therefore produce identical argv bytes, so fresh
// and replay seal a byte-identical closure.
func piProductionLaunchBuilder(nodeRuntime, entrypoint string, task domain.TaskSpec) productionruntime.AttemptLaunchArgvBuilder {
	return func(identity productionruntime.AttemptLaunchIdentity) (productionruntime.AttemptLaunchArgv, error) {
		out, err := pi.BuildProductionLaunch(pi.ProductionLaunchInput{
			NodeRuntime: nodeRuntime,
			Entrypoint:  entrypoint,
			Profile:     task.Worker.ExecutionProfile,
			Model:       task.Worker.Model,
			TaskID:      identity.TaskID,
			RunID:       identity.RunID,
			AttemptID:   identity.AttemptID,
			Objective:   task.Work.Objective,
			Constraints: task.Work.Constraints,
		})
		if err != nil {
			return productionruntime.AttemptLaunchArgv{}, err
		}
		return productionruntime.AttemptLaunchArgv{Argv: out.Argv, Prompt: out.Prompt}, nil
	}
}
