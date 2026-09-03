// Package cloudflare implements the M10 Cloudflare SandboxProvider: the
// ten-operation SPI of internal/sandbox (ADR 0016 §4) mapped onto the
// official Cloudflare stable Sandbox Bridge HTTP API — health / create /
// running / exec SSE / file (raw bytes) / persist (raw tar) / hydrate (raw
// tar) / destroy / session — as re-verified online and frozen in
// bridge_client.go.
//
// Operation mapping:
//
//	Probe       -> read-only Bridge health endpoint (running-class read)
//	Provision   -> create (durable intent -> locator -> outcome)
//	Stage       -> file write + read-back (raw bytes, Marshal-side digests)
//	Exec        -> exec SSE stream (bounded capture, exactly one terminal)
//	Inspect     -> running observation
//	Signal      -> delete the exact session
//	Checkpoint  -> persist (raw tar, Marshal-side digest)
//	Restore     -> create + hydrate (+ destroy of the previous sandbox)
//	Terminate   -> destroy (durable prepared/delivered protocol)
//	Reconcile   -> local bookkeeping + running observation + intent ambiguity
//
// Frozen boundaries of this package:
//
//   - Cloudflare-specific concepts (Durable Objects, R2, Workers bindings)
//     never surface in Marshal Core: the Bridge-internal identity of a
//     sandbox travels only as the opaque bridgeLocator mapping and the
//     SandboxAllocation.AllocationId locator, which Core only ever compares
//     for equality (ADR 0016 §4).
//   - Credential discipline is fail closed: the Bridge Bearer token is a
//     transport credential only. It never substitutes for the fencingToken
//     (a non-credential stale-write guard); it never enters business JSON,
//     events, logs, digests or error messages; and Credential redacts its
//     literal under every formatting verb, pointer, carrier struct, error
//     wrap and log call (String, Format and GoString all redact).
//   - Every receipt this provider returns is an observation of Bridge
//     state, never authority. Stage digests are recomputed Marshal-side
//     before and after the raw write: echoing a declared digest can never
//     satisfy a receipt.
//   - Checkpoint covers the staged file-system content only; the persist
//     endpoint returns the raw tar snapshot and the provider recomputes its
//     digest. A container whose state was lost after hibernation fails
//     closed and is only recoverable through Restore's create + hydrate
//     path.
//   - The official Bridge exposes no remote listing endpoint, so Reconcile
//     can never enumerate unknown orphans. Any durable create intent whose
//     outcome is unknown is ambiguity and fails closed: reconcile never
//     reports clean while a side effect may exist.
//   - create/restore are crash-safe through the failure-atomic durable
//     FileStateStore: a durable intent is written before the create, the
//     Bridge locator is persisted immediately after the create succeeds,
//     and the active allocation is installed atomically with the committed
//     outcome. A failed store write never mutates the in-memory state, and
//     a crash at any write point converges on replay because the create is
//     idempotent under its idempotency key.
//   - The internal durable phase key and the external HTTP Idempotency-Key
//     are layered: the internal key is the operation identity ReplayKey
//     that keys the durable intent, while the external key is an
//     allocation-derived, HTTP-safe (base64url), stable and deterministic
//     derivation that travels only on the wire.
//   - Provision and Terminate normalize onto the Core authority records and
//     cross-bind them in one EffectAuthorityRecord whose Validate fails
//     closed on any namespace, scope, effect identity, reconcile identity,
//     operation, allocation target, allocation/phase-derived idempotency
//     key, disposition class or run/attempt-derived policy/authorization
//     digest inconsistency. The record is durably persisted through the
//     EffectAuthoritySink, whose reopen validates every persisted record and
//     rejects any divergent fork under an already-persisted effect id.
//   - Terminate is crash-safe through a durable prepared/delivered protocol:
//     a durable terminate intent precedes the idempotent destroy, and the
//     terminal state is installed atomically with the intent resolution, so
//     a crash at any write point converges on reopen with the destroy
//     delivered exactly once.
//   - The production composition root (NewProductionProvider) mechanically
//     forces a file-backed state store, a non-nil durable file-backed effect
//     authority sink and a non-nil Core-backed authority resolver whose
//     context resolves and matches the Core typed-edge runtime issuer before
//     any remote call; a missing, in-memory or typed-nil piece fails startup,
//     never an in-memory fallback.
//
// The package uses only the Go standard library plus the harness canonical
// package: no Cloudflare SDK dependency, and no test ever connects to real
// Cloudflare infrastructure (tests drive the in-process fake Bridge of
// fake_bridge_test.go, which is also the conformance fixture).
package cloudflare
