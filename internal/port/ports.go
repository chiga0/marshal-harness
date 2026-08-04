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

// TerminalCompletionGate defines the evidence required to end an interactive
// transport. It only closes the transport; Verification and Review remain
// mandatory before Marshal can accept or publish Worker changes.
type TerminalCompletionGate string

const (
	TerminalCompletionSupervisedConfirmation TerminalCompletionGate = "supervised-confirmation"
)

// TerminalLaunchSpec is the provider-neutral, frozen input to a PTY backend.
// Environment is complete rather than additive: launchers must replace, not
// inherit, their ambient environment.
type TerminalLaunchSpec struct {
	AdapterID        string
	AdapterVersion   string
	BinaryVersion    string
	Executable       string
	ExecutableDigest string
	WorkingDirectory string
	Arguments        []string
	Environment      []string
	InitialPrompt    string
	CompletionGate   TerminalCompletionGate
}

// TerminalLaunchAdapter lets the same Adapter that owns captured execution
// freeze a native TUI launch without starting it. A PTY backend must not infer
// provider defaults or expand this specification.
type TerminalLaunchAdapter interface {
	WorkerAdapter
	PrepareTerminal(context.Context, domain.Record) (TerminalLaunchSpec, error)
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
