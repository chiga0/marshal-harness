// Package engine freezes the M9-e DurableExecutionEngine single authority
// seam (ADR 0018 §15, ADR 0016 §5/§7, ADR 0019 §1): the sole outbound path
// between the deterministic Marshal Control Plane (Core) and any durable
// execution backend.
//
// The seam selection is frozen as the ledger-derived Core command journal.
// Commands are deterministically derived from authority ledger facts — the
// commandId is stably derived from the ledger fact digest — and the
// same-transaction outbox alternative was evaluated and rejected at spec
// time: the M9-a dispatch lease ledger is the single atomic sink with a
// frozen write path, the journal derivation by construction excludes the
// "command delivered but ledger not committed" state, and the "ledger
// committed but command not delivered" state is closed at crash/upgrade
// recovery by re-deriving every undelivered command from the ledger through
// this single seam, never from backend internal state. No second outbound
// authority exists.
//
// Authority boundary: a backend consumes Command{commandId, kind,
// payloadRef} and reports Receipt{commandId, deliveredAt, attemptSeq}.
// Under at-least-once delivery, duplicate delivery or consumption of the
// identical commandId merges idempotently. Timer wakeup, signal transport
// and crash recovery are backend transport responsibilities. Workflow and
// activity state is never business authority: a backend must never announce
// lifecycle transitions, ReviewDecisions, rework, terminal states or
// safe-to-publish — those are Core authority and any such claim fails
// closed at the seam. Delivery retry is backend transport retry:
// receipt.attemptSeq is the backend delivery attempt sequence only; it
// never creates a business Attempt and never consumes a business retry or
// rework budget.
//
// LocalBackend is the embedded/single-machine in-process first-class
// backend: durable append-only state under the construction-time stateRoot,
// receipt idempotency, timer scheduling, signal mailboxes and crash
// recovery by deterministic state replay. It is not a Temporal stub.
// TemporalProfile declares the Temporal backend profile (seam selection,
// workflow versioning/build ID, Continue-As-New boundary, payload
// externalization and limit, activity heartbeat/cancel/retry semantics)
// without any Temporal SDK dependency, without connecting to any Temporal
// service and without production storage dependencies (PostgreSQL/S3 belong
// to M11); profile consistency fixtures validate the declared semantics
// against fake backends.
//
// Deferred to later milestones: workload execution wiring toward the
// execution plane (SandboxProvider dispatch), Core/server wiring of the
// seam, and any remote transport. The engine package never reads or writes
// the reserved .marshal directory; state isolation is guaranteed by the
// caller-supplied stateRoot.
package engine
