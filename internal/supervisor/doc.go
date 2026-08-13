// Package supervisor implements the Marshal supervisor core: it scans the
// run store, decides which Runs need a driver process, and starts those
// drivers as child processes through the marshal CLI binary itself.
//
// The supervisor addresses the failure mode in which `marshal task run` and
// `marshal task publish` drivers were background-owned by an orchestrator
// process group and died silently whenever that group was cleaned up,
// leaving Runs stuck in RUNNING or PUBLISHING with empty logs (issue #56).
// The supervisor owns its drivers instead: they are children of the
// long-lived supervisor process, and every supervise round re-evaluates all
// Runs rather than inheriting stale process ownership from a transient
// orchestrator session.
//
// # Boundary declaration
//
// The supervisor is strictly read-only plus spawn with respect to Run state.
// Scan reads exclusively through runstore.Inspect, which enforces snapshot
// and journal consistency; Decide is a pure function of one RunStatus;
// Supervise only starts child processes through the Executor seam. The
// supervisor itself never writes state.json, events.jsonl, leases or any
// other Run state: all Run state mutations happen exclusively inside the
// spawned marshal CLI child processes, which reuse the existing lifecycle
// semantics unchanged. This package therefore introduces no new lifecycle
// transitions and no new trust boundary.
//
// Phase-1 scope: this package provides the supervisor core only. CLI wiring
// of a supervisor subcommand and launchd/systemd integration are deliberately
// out of scope here.
package supervisor
