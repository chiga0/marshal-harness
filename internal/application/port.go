package application

import "context"

// PublicApplicationPort is the only production-shaped entry point exposed to
// CLI/server input adapters. Prepare and execute are separate so a durable
// authority head, not an in-memory plan, is carried into the process bridge.
type PublicApplicationPort interface {
	Status(context.Context, StatusRequest) (StatusProjection, error)
	PrepareRunStart(context.Context, PrepareRunStartRequest) (PreparedRunStart, error)
	StartPreparedRun(context.Context, PreparedRunStart) (RunProjection, error)
	InspectRun(context.Context, InspectRunRequest) (RunProjection, error)
}
