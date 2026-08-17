// Package port defines provider-neutral interfaces consumed by Marshal Core.
package port

import (
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// ErrPRNotMerged is the sentinel merge observers return when the published
// PR node is not merged. Callers must treat it as "fall back to the ordinary
// check observation flow", never as a failure that mutates the run.
var ErrPRNotMerged = errors.New("pr-not-merged")

var (
	ErrMergeNotMergeable     = errors.New("merge-not-mergeable")
	ErrMergePermissionDenied = errors.New("merge-permission-denied")
	ErrMergeIdentityMismatch = errors.New("merge-identity-mismatch")
	ErrMergeRetryExhausted   = errors.New("merge-delivery-retry-exhausted")
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
	RunID            string
	AttemptID        string
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

// MergeReceiptObserver observes the immutable merge fact of a published PR
// and returns an ADR 0026 SCMMergeReceipt. It is strictly observational: it
// must never merge, edit, close or otherwise mutate the remote PR.
type MergeReceiptObserver interface {
	ObserveMergeReceipt(context.Context, domain.Record) (domain.Record, error)
}

// SCMMerger is the ADR 0032 independent credentialed merge port inside the
// Publication trust domain. It exposes exactly the two frozen mutations and
// nothing else: admin, force, bypass, branch-delete, close and remote
// auto-merge queue operations are never exposed through it.
type SCMMerger interface {
	// ReadyForReview transitions the intent-bound Draft PR to ready for
	// review. It is idempotent: an already-ready PR is observed as success.
	ReadyForReview(context.Context, domain.SCMMergeIntent) error
	// Merge executes the merge with expectedHeadOid == intent.HeadOid
	// mechanically bound into the merge request (when the provider can) and
	// intent.MergeMethod applied. Any response loss is reconciled by the
	// caller through ObserveMergeReceipt, never by blind retry.
	Merge(context.Context, domain.SCMMergeIntent) error
	// BindsExpectedHead reports whether the provider mechanically binds
	// intent.HeadOid into the merge request (ADR 0032 §4 atomicity). A
	// provider that cannot bind it must make the run BLOCKED rather than
	// fall back to before/after observation, which is not a fence.
	BindsExpectedHead() bool
	// SecurityDomainID returns the SCMMerger actor-side composite security
	// domain (ADR 0018 §10) the merger executes under. It is a mechanical,
	// secret-free declaration: admission freezes it into the intent and every
	// mutation re-checks it against the frozen value, so the actual mutation
	// actor is provably the one the intent bound.
	SecurityDomainID() string
}

// SCMMergeCredentialObserver observes the current authenticated merge
// executor principal and the credential-resolution identity under the
// Publisher-side credential path (ADR 0032 §4). It is strictly read-only and
// never carries credential material in either return value.
type SCMMergeCredentialObserver interface {
	// ObserveCredentialIdentity returns the canonical "github-login:<login>"
	// principal and the canonical digest of the (gh binary resolved path, gh
	// config dir resolved path, principal) tuple. An empty or ambiguous
	// observation must fail closed.
	ObserveCredentialIdentity(context.Context) (principal string, credentialIdentityDigest string, err error)
}

// MergeTargetObserver observes the pre-merge PR facts bound to an intent for
// admission re-observation and the ObserveReady recovery state machine
// (ADR 0032 §2, §5). It is strictly read-only and never mutates the remote.
type MergeTargetObserver interface {
	ObserveTarget(context.Context, domain.SCMMergeIntent) (domain.SCMMergeTarget, error)
}

// SandboxProvider is Marshal Core's dispatch-bound sandbox execution port
// (ADR 0016 §4 / ADR 0017): every SPI request must carry a dispatch-bound
// operation identity binding task/run/attempt/allocation to the lease
// generation and fencing token, and the provider must fail closed on an
// invalid, unknown or stale identity before any side effect. Every receipt a
// provider returns is an observation, never authority — fencing,
// single-active and conformance adjudication never trust a receipt alone.
// The embedded runtime binds the Local provider; remote providers bind
// through the identical dispatch-bound SPI.
type SandboxProvider = sandbox.SandboxProvider
