package application

import "context"

// PublicApplicationPort is the only production-shaped entry point exposed to
// CLI/server input adapters. StartRun owns preparation, execution and durable
// reconciliation as one bounded application operation so no input adapter
// carries an in-memory preparation across Runtime lifetimes.
type PublicApplicationPort interface {
	Status(context.Context, StatusRequest) (StatusProjection, error)
	StartRun(context.Context, StartRunRequest) (RunStartProjection, error)
	// ReconcileStartRun performs a read-only current-ledger lookup for the
	// exact preparation and committed successor derived from one StartRun
	// intent. Input adapters use it only to close response-loss delivery; it
	// never authorizes a fresh mutation.
	ReconcileStartRun(context.Context, StartRunRequest) (RunStartProjection, bool, error)
	InspectRun(context.Context, InspectRunRequest) (RunProjection, error)
}
