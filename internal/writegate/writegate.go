package writegate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// ── Sentinel errors (fail closed) ────────────────────────────────────────────

// ErrSecondWritePath wraps every Gate rejection: any attempt to mutate
// authoritative state without a valid outbox-bound proof is hard-failed
// through this sentinel.
var ErrSecondWritePath = errors.New("writegate: second write path rejected")

// ── RejectionReason (closed enum) ────────────────────────────────────────────

// RejectionReason is the closed set of write-gate rejection labels.
// No other values are admitted.
type RejectionReason string

const (
	ReasonMissingProof     RejectionReason = "missing-proof"
	ReasonMalformedProof   RejectionReason = "malformed-proof"
	ReasonNotCommitted     RejectionReason = "not-committed"
	ReasonDigestMismatch   RejectionReason = "digest-mismatch"
	ReasonSequenceMismatch RejectionReason = "sequence-mismatch"
	ReasonAlreadyApplied   RejectionReason = "already-applied"
	ReasonUnknownCommand   RejectionReason = "unknown-command"
)

// allReasons enumerates every valid RejectionReason for Validate.
var allReasons = []RejectionReason{
	ReasonMissingProof,
	ReasonMalformedProof,
	ReasonNotCommitted,
	ReasonDigestMismatch,
	ReasonSequenceMismatch,
	ReasonAlreadyApplied,
	ReasonUnknownCommand,
}

// Validate rejects every value outside the closed enumeration.
func (r RejectionReason) Validate() error {
	for _, valid := range allReasons {
		if r == valid {
			return nil
		}
	}
	return fmt.Errorf("writegate: unknown rejection reason %q", string(r))
}

// ── Proof ────────────────────────────────────────────────────────────────────

// Proof is the closed write credential for Gate.Apply. Every field is
// required; Validate fails closed on any missing or malformed value.
type Proof struct {
	CommandId        string
	IdempotencyKey   string
	RequestDigest    string // sha256:<64-hex>
	ExpectedSequence int64  // strictly positive
	ReceiptDigest    string // sha256:<64-hex>
}

// Validate fails closed on any missing or structurally invalid field.
func (p Proof) Validate() error {
	if p.CommandId == "" && p.IdempotencyKey == "" && p.RequestDigest == "" &&
		p.ExpectedSequence == 0 && p.ReceiptDigest == "" {
		return fmt.Errorf("%s: proof is empty", ReasonMissingProof)
	}
	if p.CommandId == "" {
		return fmt.Errorf("%s: commandId must not be empty", ReasonMalformedProof)
	}
	if p.IdempotencyKey == "" {
		return fmt.Errorf("%s: idempotencyKey must not be empty", ReasonMalformedProof)
	}
	if err := requireDigest("requestDigest", p.RequestDigest); err != nil {
		return fmt.Errorf("%s: %v", ReasonMalformedProof, err)
	}
	if p.ExpectedSequence < 1 {
		return fmt.Errorf("%s: expectedSequence must be a positive integer, got %d",
			ReasonMalformedProof, p.ExpectedSequence)
	}
	if err := requireDigest("receiptDigest", p.ReceiptDigest); err != nil {
		return fmt.Errorf("%s: %v", ReasonMalformedProof, err)
	}
	return nil
}

// ── MutationKind (closed enum) ───────────────────────────────────────────────

// MutationKind is the closed set of state mutations the Gate permits.
type MutationKind string

const (
	// MutateFactAppend appends one fact to the append-only ledger and
	// advances the monotonic sequence by exactly one.
	MutateFactAppend MutationKind = "fact-append"

	// MutateDispatch records a dispatch marker for the commandId.
	MutateDispatch MutationKind = "dispatch-mark"

	// MutateResultAccept records a result acceptance for the commandId.
	MutateResultAccept MutationKind = "result-accept"
)

// Validate rejects every value outside the closed enumeration.
func (k MutationKind) Validate() error {
	switch k {
	case MutateFactAppend, MutateDispatch, MutateResultAccept:
		return nil
	default:
		return fmt.Errorf("writegate: unknown mutation kind %q", string(k))
	}
}

// ── ApplyResult ──────────────────────────────────────────────────────────────

// ApplyResult is the outcome of a successful Gate.Apply call.
type ApplyResult struct {
	// Sequence is the gate's ledger sequence after the mutation.
	Sequence int64
}

// ── OutboxVerifier interface ─────────────────────────────────────────────────

// OutboxVerifier verifies that a Proof is bound to a committed outbox entry.
// Implementations must return an error whose message starts with the
// appropriate RejectionReason label (e.g. "not-committed: ..." or
// "digest-mismatch: ...") so the Gate can wrap it under ErrSecondWritePath.
type OutboxVerifier interface {
	VerifyBinding(proof Proof, kind MutationKind) error
}

// ── Gate ─────────────────────────────────────────────────────────────────────

// Gate is the single credential-gated write path for authoritative state.
// All mutations (fact append, dispatch mark, result acceptance) flow through
// Apply; no direct setter is exposed.
type Gate struct {
	mu       sync.Mutex
	verifier OutboxVerifier
	sequence int64
	facts    map[string]int64 // receiptDigest → sequence (append-only ledger)
	applied  map[string]bool  // commandId → fact-append applied
	dispatch map[string]bool  // commandId → dispatch-mark applied
	results  map[string]bool  // commandId → result-accept applied
}

// NewGate creates a Gate backed by the given verifier. The verifier is
// consulted on every Apply to confirm the proof is bound to a committed
// outbox entry.
func NewGate(verifier OutboxVerifier) *Gate {
	return &Gate{
		verifier: verifier,
		facts:    make(map[string]int64),
		applied:  make(map[string]bool),
		dispatch: make(map[string]bool),
		results:  make(map[string]bool),
	}
}

// Apply is the single credential-gated write entry point. The proof must
// validate structurally, must not have been applied for the same kind and
// commandId before, and must pass verifier binding checks. On success the
// authoritative state advances by exactly one mutation; on failure the
// state is unchanged and ErrSecondWritePath wraps the specific reason.
func (g *Gate) Apply(proof Proof, kind MutationKind) (ApplyResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := kind.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: %v", ErrSecondWritePath, err)
	}
	if err := proof.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: %v", ErrSecondWritePath, err)
	}

	switch kind {
	case MutateFactAppend:
		if g.applied[proof.CommandId] {
			return ApplyResult{}, fmt.Errorf("%w: %s: commandId %q already applied",
				ErrSecondWritePath, ReasonAlreadyApplied, proof.CommandId)
		}
	case MutateDispatch:
		if g.dispatch[proof.CommandId] {
			return ApplyResult{}, fmt.Errorf("%w: %s: commandId %q already dispatched",
				ErrSecondWritePath, ReasonAlreadyApplied, proof.CommandId)
		}
	case MutateResultAccept:
		if g.results[proof.CommandId] {
			return ApplyResult{}, fmt.Errorf("%w: %s: commandId %q already result-accepted",
				ErrSecondWritePath, ReasonAlreadyApplied, proof.CommandId)
		}
	}

	if err := g.verifier.VerifyBinding(proof, kind); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: %v", ErrSecondWritePath, err)
	}

	switch kind {
	case MutateFactAppend:
		g.sequence++
		g.facts[proof.ReceiptDigest] = g.sequence
		g.applied[proof.CommandId] = true
	case MutateDispatch:
		g.dispatch[proof.CommandId] = true
	case MutateResultAccept:
		g.results[proof.CommandId] = true
	}

	return ApplyResult{Sequence: g.sequence}, nil
}

// ── Read-only projections (no credentials required) ──────────────────────────

// LedgerSequence returns the current monotonic sequence counter.
func (g *Gate) LedgerSequence() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sequence
}

// FactCount returns the number of facts in the append-only ledger.
func (g *Gate) FactCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.facts)
}

// DispatchCount returns the number of dispatch markers.
func (g *Gate) DispatchCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.dispatch)
}

// ResultCount returns the number of result acceptance records.
func (g *Gate) ResultCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.results)
}

// IsApplied reports whether the commandId has been fact-appended.
func (g *Gate) IsApplied(commandId string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.applied[commandId]
}

// IsDispatchMarked reports whether the commandId has a dispatch marker.
func (g *Gate) IsDispatchMarked(commandId string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dispatch[commandId]
}

// HasResult reports whether the commandId has a result acceptance record.
func (g *Gate) HasResult(commandId string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.results[commandId]
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// ComputeReceiptDigest deterministically derives a sha256 receipt digest
// from the receipt's identity fields. Identical inputs always produce the
// identical digest; no random or clock source participates.
func ComputeReceiptDigest(commandId string, sequence int64, factDigest, requestDigest string) string {
	raw := fmt.Sprintf("%s\n%d\n%s\n%s", commandId, sequence, factDigest, requestDigest)
	h := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(h[:])
}

// requireDigest fails closed unless the value is a full lowercase hex
// sha256 digest with the sha256: prefix.
func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if len(value) < len(prefix) || value[:len(prefix)] != prefix {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hexPart := value[len(prefix):]
	if len(hexPart) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
