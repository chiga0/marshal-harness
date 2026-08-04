// Package port defines provider-neutral interfaces consumed by Marshal Core.
package port

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// WorkerAdapter edits a task worktree but cannot verify or publish its own
// result authoritatively.
type WorkerAdapter interface {
	ID() string
	Probe(context.Context) (domain.Record, error)
	Run(context.Context, domain.Record) (domain.Record, error)
}

// Verifier independently observes a Worker result.
type Verifier interface {
	Verify(context.Context, domain.Record) (domain.Record, error)
}

// LeadAgentBridge exchanges bounded review records with the lead agent.
type LeadAgentBridge interface {
	Review(context.Context, domain.Record) (domain.Record, error)
}

// Publisher is the only port authorized to create credentialed publication
// side effects.
type Publisher interface {
	Publish(context.Context, domain.Record) (domain.Record, error)
}

type RemoteCheckObserver interface {
	ObserveChecks(context.Context, domain.Record, []string) (domain.Record, error)
}
