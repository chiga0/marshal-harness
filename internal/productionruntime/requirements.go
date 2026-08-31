package productionruntime

import (
	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
)

// Pi0844Requirements maps the closed TaskSpec execution-profile vocabulary to
// the durable local allocation requirements. The darwin-local-dogfood profile
// is an ordinary-user host-process sandbox: hardened assurance is never
// admitted here, and unknown profiles fail closed instead of widening.
func Pi0844Requirements(executionProfile string) (allocationcontrol.SandboxRequirementsV1, error) {
	switch executionProfile {
	case "workspace-write":
		return allocationcontrol.SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"}, nil
	case "read-only":
		return allocationcontrol.SandboxRequirementsV1{AccessMode: "read-only", MinimumAssuranceLevel: "workspace-write"}, nil
	default:
		return allocationcontrol.SandboxRequirementsV1{}, application.NewError("pi-requirements", application.ReasonInvalidRequest)
	}
}
