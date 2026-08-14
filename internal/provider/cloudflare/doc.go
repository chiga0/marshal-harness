// Package cloudflare implements the M10-a Cloudflare SandboxProvider: the
// ten-operation SPI of internal/sandbox (ADR 0016 §4) mapped onto the
// official Cloudflare Sandbox Bridge OpenAPI family — create / running /
// exec SSE / file / persist / hydrate / destroy — as recorded in
// docs/m10-cloud-deployment-research.md §3.
//
// Operation mapping:
//
//	Probe       -> read-only Bridge health endpoint (running-class read)
//	Provision   -> create
//	Stage       -> file write (inline bytes or bound-store locator)
//	Exec        -> exec SSE stream (bounded capture, exit observation)
//	Inspect     -> read-only sandbox observation endpoint + file reads
//	Signal      -> Bridge signal endpoint (closed enumeration)
//	Checkpoint  -> persist (staged file-system snapshot only)
//	Restore     -> create + hydrate (+ destroy of the previous sandbox)
//	Terminate   -> destroy
//	Reconcile   -> running-class listing reconciled against bookkeeping
//
// Frozen boundaries of this package:
//
//   - Cloudflare-specific concepts (Durable Objects, R2, Workers bindings)
//     never surface in Marshal Core: the Bridge-internal identity of a
//     sandbox travels only as the opaque SandboxAllocation.AllocationId
//     locator and as receipt fields, which Core only ever compares for
//     equality (ADR 0016 §4).
//   - Credential discipline is fail closed: the Bridge Bearer token is a
//     transport credential only. It never substitutes for the fencingToken
//     (a non-credential stale-write guard) and the two never replace each
//     other; the credential never enters business JSON, events, logs,
//     digests or error messages (the client scrubs every error it can
//     produce). Fencing/generation adjudication stays at the marshal-server
//     authoritative write boundary; this provider only validates and passes
//     through the OperationIdentity of internal/sandbox/identity.go and
//     never mints authority of its own.
//   - Every receipt this provider returns is an observation of Bridge
//     state, never authority: conformance, single-active and fencing
//     adjudication never trust a receipt alone. Stage digests are
//     recomputed before consumption (Bridge-side, plus Marshal-side for
//     inline bytes) and again after consumption through a Marshal-side
//     read-back recomputation: echoing a declared digest can never satisfy
//     a receipt.
//   - Checkpoint covers the staged file-system content only (SPI:
//     "snapshot the staged content"); platform-internal hibernation state
//     is never a CheckpointRecord. A container whose state was lost after
//     hibernation fails closed through Checkpoint and is only recoverable
//     through Restore's create + hydrate path, whose failures are always
//     deterministic.
//
// M10-a scope notes: the wire contract frozen in bridge_client.go is the
// M10-a fixture contract; every path, payload shape and version identifier
// is subject to the M10-b online re-verification of the official Bridge
// OpenAPI, and drift is fail-closed material. Platform quota numbers are
// deliberately not expressed as code constants here (research doc §6.3).
// The package uses only the Go standard library plus the harness canonical
// package: no Cloudflare SDK dependency, and no test ever connects to real
// Cloudflare infrastructure (tests drive the in-process fake Bridge of
// fake_bridge_test.go, which is also the conformance fixture).
package cloudflare
