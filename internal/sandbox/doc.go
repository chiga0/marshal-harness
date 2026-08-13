// Package sandbox freezes the first vertical slice of the M8 embedded
// sandbox: the SandboxProvider SPI with its ten operations (ADR 0016 §4),
// dispatch-bound operation identity with lease fencing (ADR 0017 §4/§6),
// content-addressed staging with sha256 digests recomputed before and after
// consumption (ADR 0017 §3), a scripted deterministic Fake provider, and a
// provider-agnostic conformance suite whose adversarial probes execute
// inside the target allocation that the provider under test provisions
// itself.
//
// M8 embedded scope only. This package contains no transport, no heartbeat,
// no dispatcher, no persistent lease ledger, no typed-edge runtime and no
// remote provider: those belong to M9. Every operation is synchronous and
// in-process; the deterministic Fake provider keeps all artifacts in memory
// and derives every value from its scripted inputs, with no random source
// and no clock read.
//
// SandboxAllocation is unrelated to runstore.Lease. The runstore lease is a
// worktree flock write lease that guards on-disk run journals; it must
// never be reused for, confused with, or conflated with the sandbox
// allocations defined here, which are provider-scoped execution
// environments keyed by allocationId and generation.
package sandbox
