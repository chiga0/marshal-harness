// Package local implements the Local SandboxProvider: the ordinary
// host-process provider of ADR 0016 §4 in its embedded form (ADR 0017).
//
// The Local provider executes workloads as ordinary processes of the host
// operating system, spawned directly by the harness (argv execution, never
// a shell interpreter). It exists so the M8 vertical slice can exercise the
// full ten-operation SPI against a real filesystem and real process
// lifecycle without any external sandbox runtime.
//
// Frozen semantics of this package:
//
//   - The Local provider is never hardened. Its assurance ceiling is
//     domain.AssuranceLevelWorkspaceWrite. A provision request whose
//     requirements demand hardened assurance is refused fail closed with
//     sandbox.ErrAssuranceNotMet and is never downgraded into a
//     workspace-write allocation.
//   - Every receipt this provider returns (ProvisionReceipt, StageReport,
//     ExecReceipt, RestoreReceipt, TerminateReceipt) is an observation of
//     host state, never authority. An ExecReceipt with status completed is
//     a lifecycle guard only: no acceptance, publication, fencing or
//     conformance verdict is ever derived from it.
//   - All dispatch-bound operations validate the OperationIdentity and its
//     fencing before any side effect; stale generations and mismatched
//     fencing tokens are rejected fail closed and recorded in the
//     per-allocation diagnostics.
//   - A single-active invariant holds per (runId, attemptId): at most one
//     allocation holds the current generation as active at any moment.
//
// Out of scope for this package (deferred to M9): transport, heartbeat and
// dispatcher wiring, a persistent lease ledger, and the typed-edge runtime.
// The SideEffectIntent/SideEffectReceipt records produced here are
// constructed in memory only; nothing writes an authority ledger.
//
// The allocation registry maintained by LocalRunner is unrelated to
// runstore.Lease: runstore.Lease is the flock write lease guarding the
// worktree store files, while a sandbox.SandboxAllocation is a
// provider-scoped execution environment identified by an opaque locator.
// The two concepts share no state and no lifecycle.
package local
