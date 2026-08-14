// Package goal freezes the first phase of M13 Goal orchestration (ADR 0019
// §4/§5): the Goal authority record layer, the deterministic plan admission
// evaluator and the budget reservation state machine.
//
// Every record in this package is owned by an authority.AuthorityNamespaceId,
// which is the sole owner identity field; only Core writes these records.
// Planner-produced input enters exclusively as an untrusted GoalPlanProposal
// and never becomes authority state without passing the fixed-order admission
// steps. All records serialize through RFC 8785 JCS canonical JSON and are
// addressed by the sha256 digest of their canonical bytes, so identical
// content always yields identical digests regardless of member order.
//
// Admission (admission.go) is a pure function: identical inputs always yield
// the identical decision, including the rejection step and reason
// classification. The six steps run in the frozen order 1-6 of ADR 0019 §4 —
// schema/canonical digest/revision CAS, goal identity and scope, node/edge
// integrity and node identity conflicts, executor/repository/path and
// side-effect class allowlists, edge/structure guardrails, and cumulative
// budget availability — and the first failing step produces the only
// rejection. A rejection is an auditable append-only record; admission never
// creates execution state and never persists a live reservation.
//
// Budget reservations (budget.go) form an append-only state machine,
// reserved → committed → settled or reserved → released|expired, guarded by
// CAS/current-state checks on every transition. Live reservations only ever
// arise in the same in-memory transaction as their accepted plan revision;
// duplicate settle/release, stale revision release and lost-response replay
// are idempotent or fail closed, and actual usage above the reserved
// estimate halts new dispatch by decision rather than by overselling.
//
// Phase boundary: this package produces records and decisions only. It never
// materializes Task/Run state, never executes side effects, and contains no
// controller wiring; the controller seams of ADR 0019 §4 steps 7-8 belong to
// a later phase.
package goal
