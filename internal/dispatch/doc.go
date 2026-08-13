// Package dispatch is gate-6 of the M8 hard gate sequence: the embedded
// minimal landing of DispatchLease match for ADR 0018 §5/§6/§7 and ADR 0017
// §7. It freezes the DispatchLease record bound into both key spaces — the
// authority owner authority.AuthorityNamespaceId copied from the durable
// provider registration and the actor authority.SecurityDomainId carried as
// provenance only — together with the registration attestation chain and a
// canonical leaseDigest computed under RFC 8785 JCS with the digest itself
// detached, so any rewrite of a bound reference or digest fails validation.
//
// Capability match consumes only the persisted
// provider.ProviderCapabilitySnapshot and its closed
// conformanceEvidenceDigests set. Claim reads the gate-4
// provider.RegistrationStore together with the gate-5
// provider.EvaluateProviderEligibility combination, and every missing
// prerequisite fails closed. The current-ledger recheck (Revalidate)
// re-reads the durable ledger and makes an in-flight lease lose eligibility
// the moment its registration, snapshot or evidence is invalidated,
// returning the machine-readable CancelReason the caller must carry into a
// cancel with a generation bump; continuation then requires a new attempt
// with a new claim, never an in-place renewal. The fencing guard rejects
// any stale generation or fencingToken at the isolated adjudication point.
// Push and Pull topologies are expressed through these identical
// topology-agnostic claim/revalidate semantics.
//
// DispatchLease is a dispatch-protocol lease record. It is entirely distinct
// from runstore.Lease, which is a worktree flock write lease guarding
// run-store journals on the local filesystem; DispatchLease never reuses
// runstore.Lease, its flock or its lifecycle.
//
// Gate-6 implements no transport. Deferred to M9: the Push/Pull transport
// (offer/poll/claim/ack), the heartbeat runtime and the dispatcher, the
// durable lease ledger with crash recovery and an atomic generation-bump
// sink, the reconcile runtime (Inspect/kill/drain) and the typed edge
// DispatchResultCapability.
package dispatch
