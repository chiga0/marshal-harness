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
//	Terminate   -> destroy
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
//   - Provision and Terminate are side-effecting remote mutations, so they
//     run through the mandatory normalized effect authority seam
//     (effect_records.go): a put-if-absent SideEffectIntent is durably
//     acknowledged before the Bridge mutation, and a SideEffectReceipt plus
//     observation are resolved only after the mutation's terminal outcome is
//     observed. The effect targetRef is the Marshal allocation id for both
//     Provision and Terminate — never the Bridge locator — so the Core-owned
//     authority records never carry a Cloudflare-internal identity. The
//     Core-injected EffectAuthoritySink is the authority; the provider's
//     local map is a cache only. Lookup and LookupByTarget fail closed: an
//     intent must carry the Cloudflare port, a closed provision/terminate
//     operation, the disposition class consistent with that operation and a
//     recomputed target digest; a receipt must bind to the exact target and
//     recomputed intent digest without crossing effects; and an observation
//     requires a resolved receipt and must bind to both recomputed digests.
//     A terminate whose durable intent or receipt write fails after a
//     successful destroy never re-issues a second destroy on reopen+replay:
//     a pending intent fails closed as ambiguity, a resolved receipt
//     converges to terminal.
//
// The package uses only the Go standard library plus the harness canonical
// package: no Cloudflare SDK dependency, and no test ever connects to real
// Cloudflare infrastructure (tests drive the in-process fake Bridge of
// fake_bridge_test.go, which is also the conformance fixture).
package cloudflare
