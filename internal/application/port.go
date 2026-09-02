package application

import "context"

// PublicApplicationPort is the only production-shaped entry point exposed to
// CLI/server input adapters. StartRun owns preparation, execution and durable
// reconciliation as one bounded application operation so no input adapter
// carries an in-memory preparation across Runtime lifetimes.
type PublicApplicationPort interface {
	Status(context.Context, StatusRequest) (StatusProjection, error)
	StartRun(context.Context, StartRunRequest) (RunStartProjection, error)
	InspectRun(context.Context, InspectRunRequest) (RunProjection, error)
}
