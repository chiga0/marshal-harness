package writegate

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/engine"
	"github.com/chiga0/marshal-harness/internal/outbox"
)

// ── Test digests (valid sha256:<64-hex>) ─────────────────────────────────────

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digestC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	digestD = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	digestE = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	digestF = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

// ── Fake verifier ────────────────────────────────────────────────────────────

type fakeEntry struct {
	requestDigest  string
	idempotencyKey string
	sequence       int64
	receiptDigest  string
	dispatched     bool
	hasResult      bool
}

type fakeVerifier struct {
	entries map[string]fakeEntry
}

func newFakeVerifier() *fakeVerifier {
	return &fakeVerifier{entries: make(map[string]fakeEntry)}
}

func (v *fakeVerifier) addEntry(commandId string, e fakeEntry) {
	v.entries[commandId] = e
}

func (v *fakeVerifier) VerifyBinding(proof Proof, kind MutationKind) error {
	e, ok := v.entries[proof.CommandId]
	if !ok {
		return rejectVerify(ReasonNotCommitted, "commandId %q not committed", proof.CommandId)
	}
	if proof.RequestDigest != e.requestDigest {
		return rejectVerify(ReasonDigestMismatch, "requestDigest mismatch for %q", proof.CommandId)
	}
	if proof.IdempotencyKey != e.idempotencyKey {
		return rejectVerify(ReasonDigestMismatch, "idempotencyKey mismatch for %q", proof.CommandId)
	}
	if proof.ExpectedSequence != e.sequence {
		return rejectVerify(ReasonSequenceMismatch, "expected sequence %d but entry has %d",
			proof.ExpectedSequence, e.sequence)
	}
	if proof.ReceiptDigest != e.receiptDigest {
		return rejectVerify(ReasonDigestMismatch, "receiptDigest mismatch for %q", proof.CommandId)
	}
	switch kind {
	case MutateDispatch:
		if !e.dispatched {
			return rejectVerify(ReasonNotCommitted, "commandId %q not dispatched", proof.CommandId)
		}
	case MutateResultAccept:
		if !e.hasResult {
			return rejectVerify(ReasonNotCommitted, "commandId %q has no result", proof.CommandId)
		}
	}
	return nil
}

func rejectVerify(reason RejectionReason, format string, args ...any) error {
	return fmt.Errorf("%s: %s", reason, fmt.Sprintf(format, args...))
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func assertGateState(t *testing.T, g *Gate, seq int64, facts, dispatch, results int) {
	t.Helper()
	if g.LedgerSequence() != seq {
		t.Errorf("LedgerSequence: got %d, want %d", g.LedgerSequence(), seq)
	}
	if g.FactCount() != facts {
		t.Errorf("FactCount: got %d, want %d", g.FactCount(), facts)
	}
	if g.DispatchCount() != dispatch {
		t.Errorf("DispatchCount: got %d, want %d", g.DispatchCount(), dispatch)
	}
	if g.ResultCount() != results {
		t.Errorf("ResultCount: got %d, want %d", g.ResultCount(), results)
	}
}

func assertHardFail(t *testing.T, err error, expectedReason RejectionReason) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSecondWritePath) {
		t.Errorf("expected ErrSecondWritePath, got: %v", err)
	}
	if !strings.Contains(err.Error(), string(expectedReason)) {
		t.Errorf("expected reason %q in error, got: %v", expectedReason, err)
	}
	if !strings.HasPrefix(err.Error(), "writegate:") {
		t.Errorf("expected writegate: prefix, got: %v", err)
	}
}

// ── Negative test matrix ─────────────────────────────────────────────────────

func TestGateNegativeMatrix(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-1", 1, digestC, digestA)
	v.addEntry("cmd-1", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-1",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     true,
		hasResult:      true,
	})
	g := NewGate(v)

	tests := []struct {
		name   string
		proof  Proof
		kind   MutationKind
		reason RejectionReason
	}{
		{
			name:   "empty proof",
			proof:  Proof{},
			kind:   MutateFactAppend,
			reason: ReasonMissingProof,
		},
		{
			name: "malformed requestDigest — no prefix",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    "no-prefix",
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "malformed requestDigest — short hex",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    "sha256:aaaa",
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "malformed requestDigest — uppercase hex",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "malformed receiptDigest — empty",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    "",
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "zero sequence",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 0,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "negative sequence",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: -1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "empty commandId",
			proof: Proof{
				CommandId:        "",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "empty idempotencyKey",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
		{
			name: "uncommitted commandId",
			proof: Proof{
				CommandId:        "cmd-nonexistent",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonNotCommitted,
		},
		{
			name: "forged receiptDigest",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    digestF,
			},
			kind:   MutateFactAppend,
			reason: ReasonDigestMismatch,
		},
		{
			name: "requestDigest mismatch",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestD,
				ExpectedSequence: 1,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonDigestMismatch,
		},
		{
			name: "sequence ahead",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 999,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonSequenceMismatch,
		},
		{
			name: "sequence behind",
			proof: Proof{
				CommandId:        "cmd-1",
				IdempotencyKey:   "idem-1",
				RequestDigest:    digestA,
				ExpectedSequence: 0,
				ReceiptDigest:    receiptDigest,
			},
			kind:   MutateFactAppend,
			reason: ReasonMalformedProof,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.Apply(tc.proof, tc.kind)
			assertHardFail(t, err, tc.reason)
			assertGateState(t, g, 0, 0, 0, 0)
		})
	}
}

// ── Replay and dispatch/result negative tests ────────────────────────────────

func TestGateReplayAlreadyApplied(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-r", 1, digestC, digestA)
	v.addEntry("cmd-r", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-r",
		sequence:       1,
		receiptDigest:  receiptDigest,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-r",
		IdempotencyKey:   "idem-r",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	// First apply succeeds.
	res, err := g.Apply(proof, MutateFactAppend)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("first Apply sequence: got %d, want 1", res.Sequence)
	}
	assertGateState(t, g, 1, 1, 0, 0)

	// Replay fails with already-applied.
	_, err = g.Apply(proof, MutateFactAppend)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g, 1, 1, 0, 0)
}

func TestGateDispatchNotDispatched(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-d", 1, digestC, digestA)
	v.addEntry("cmd-d", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-d",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     false,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-d",
		IdempotencyKey:   "idem-d",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	_, err := g.Apply(proof, MutateDispatch)
	assertHardFail(t, err, ReasonNotCommitted)
	assertGateState(t, g, 0, 0, 0, 0)
}

func TestGateResultNoResult(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-res", 1, digestC, digestA)
	v.addEntry("cmd-res", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-res",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     true,
		hasResult:      false,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-res",
		IdempotencyKey:   "idem-res",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	_, err := g.Apply(proof, MutateResultAccept)
	assertHardFail(t, err, ReasonNotCommitted)
	assertGateState(t, g, 0, 0, 0, 0)
}

func TestGateForgedResultAfterDispatch(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-fr", 1, digestC, digestA)
	v.addEntry("cmd-fr", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-fr",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     true,
		hasResult:      true,
	})
	g := NewGate(v)

	// Apply fact-append first.
	factProof := Proof{
		CommandId:        "cmd-fr",
		IdempotencyKey:   "idem-fr",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}
	_, err := g.Apply(factProof, MutateFactAppend)
	if err != nil {
		t.Fatalf("fact-append: %v", err)
	}

	// Apply dispatch-mark.
	_, err = g.Apply(factProof, MutateDispatch)
	if err != nil {
		t.Fatalf("dispatch-mark: %v", err)
	}
	assertGateState(t, g, 1, 1, 1, 0)

	// Forged result proof — wrong receiptDigest.
	forgedProof := Proof{
		CommandId:        "cmd-fr",
		IdempotencyKey:   "idem-fr",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    digestF, // forged
	}
	_, err = g.Apply(forgedProof, MutateResultAccept)
	assertHardFail(t, err, ReasonDigestMismatch)
	assertGateState(t, g, 1, 1, 1, 0)
}

func TestGateDispatchReplayAlreadyApplied(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-dr", 1, digestC, digestA)
	v.addEntry("cmd-dr", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-dr",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     true,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-dr",
		IdempotencyKey:   "idem-dr",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	_, err := g.Apply(proof, MutateDispatch)
	if err != nil {
		t.Fatalf("first dispatch-mark: %v", err)
	}
	assertGateState(t, g, 0, 0, 1, 0)

	_, err = g.Apply(proof, MutateDispatch)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g, 0, 0, 1, 0)
}

func TestGateResultReplayAlreadyApplied(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-rr", 1, digestC, digestA)
	v.addEntry("cmd-rr", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-rr",
		sequence:       1,
		receiptDigest:  receiptDigest,
		hasResult:      true,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-rr",
		IdempotencyKey:   "idem-rr",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	_, err := g.Apply(proof, MutateResultAccept)
	if err != nil {
		t.Fatalf("first result-accept: %v", err)
	}
	assertGateState(t, g, 0, 0, 0, 1)

	_, err = g.Apply(proof, MutateResultAccept)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g, 0, 0, 0, 1)
}

// ── Positive path tests ──────────────────────────────────────────────────────

func TestGatePositiveFactAppend(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-p1", 1, digestC, digestA)
	v.addEntry("cmd-p1", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-p1",
		sequence:       1,
		receiptDigest:  receiptDigest,
	})
	g := NewGate(v)

	proof := Proof{
		CommandId:        "cmd-p1",
		IdempotencyKey:   "idem-p1",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}
	res, err := g.Apply(proof, MutateFactAppend)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("sequence: got %d, want 1", res.Sequence)
	}
	assertGateState(t, g, 1, 1, 0, 0)
	if !g.IsApplied("cmd-p1") {
		t.Error("expected cmd-p1 to be applied")
	}
}

func TestGatePositiveFullLifecycle(t *testing.T) {
	v := newFakeVerifier()
	receiptDigest := ComputeReceiptDigest("cmd-lc", 1, digestC, digestA)
	v.addEntry("cmd-lc", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-lc",
		sequence:       1,
		receiptDigest:  receiptDigest,
		dispatched:     true,
		hasResult:      true,
	})
	g := NewGate(v)
	proof := Proof{
		CommandId:        "cmd-lc",
		IdempotencyKey:   "idem-lc",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    receiptDigest,
	}

	// Fact append.
	res, err := g.Apply(proof, MutateFactAppend)
	if err != nil {
		t.Fatalf("fact-append: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("fact-append sequence: got %d, want 1", res.Sequence)
	}

	// Dispatch mark.
	res, err = g.Apply(proof, MutateDispatch)
	if err != nil {
		t.Fatalf("dispatch-mark: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("dispatch-mark sequence: got %d, want 1 (unchanged)", res.Sequence)
	}

	// Result accept.
	res, err = g.Apply(proof, MutateResultAccept)
	if err != nil {
		t.Fatalf("result-accept: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("result-accept sequence: got %d, want 1 (unchanged)", res.Sequence)
	}

	assertGateState(t, g, 1, 1, 1, 1)
	if !g.IsApplied("cmd-lc") {
		t.Error("expected cmd-lc applied")
	}
	if !g.IsDispatchMarked("cmd-lc") {
		t.Error("expected cmd-lc dispatched")
	}
	if !g.HasResult("cmd-lc") {
		t.Error("expected cmd-lc result")
	}
}

func TestGateMultipleFactAppends(t *testing.T) {
	v := newFakeVerifier()
	rd1 := ComputeReceiptDigest("cmd-m1", 1, digestC, digestA)
	rd2 := ComputeReceiptDigest("cmd-m2", 2, digestD, digestE)
	v.addEntry("cmd-m1", fakeEntry{
		requestDigest:  digestA,
		idempotencyKey: "idem-m1",
		sequence:       1,
		receiptDigest:  rd1,
	})
	v.addEntry("cmd-m2", fakeEntry{
		requestDigest:  digestE,
		idempotencyKey: "idem-m2",
		sequence:       2,
		receiptDigest:  rd2,
	})
	g := NewGate(v)

	p1 := Proof{
		CommandId:        "cmd-m1",
		IdempotencyKey:   "idem-m1",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    rd1,
	}
	p2 := Proof{
		CommandId:        "cmd-m2",
		IdempotencyKey:   "idem-m2",
		RequestDigest:    digestE,
		ExpectedSequence: 2,
		ReceiptDigest:    rd2,
	}

	res, err := g.Apply(p1, MutateFactAppend)
	if err != nil {
		t.Fatalf("Apply p1: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("p1 sequence: got %d, want 1", res.Sequence)
	}

	res, err = g.Apply(p2, MutateFactAppend)
	if err != nil {
		t.Fatalf("Apply p2: %v", err)
	}
	if res.Sequence != 2 {
		t.Errorf("p2 sequence: got %d, want 2", res.Sequence)
	}

	assertGateState(t, g, 2, 2, 0, 0)
}

// ── Proof and RejectionReason validation tests ───────────────────────────────

func TestProofValidate(t *testing.T) {
	tests := []struct {
		name   string
		proof  Proof
		reason RejectionReason
	}{
		{
			name:   "empty",
			proof:  Proof{},
			reason: ReasonMissingProof,
		},
		{
			name: "no commandId",
			proof: Proof{
				IdempotencyKey:   "k",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    digestB,
			},
			reason: ReasonMalformedProof,
		},
		{
			name: "no idempotencyKey",
			proof: Proof{
				CommandId:        "c",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    digestB,
			},
			reason: ReasonMalformedProof,
		},
		{
			name: "bad requestDigest",
			proof: Proof{
				CommandId:        "c",
				IdempotencyKey:   "k",
				RequestDigest:    "bad",
				ExpectedSequence: 1,
				ReceiptDigest:    digestB,
			},
			reason: ReasonMalformedProof,
		},
		{
			name: "zero sequence",
			proof: Proof{
				CommandId:        "c",
				IdempotencyKey:   "k",
				RequestDigest:    digestA,
				ExpectedSequence: 0,
				ReceiptDigest:    digestB,
			},
			reason: ReasonMalformedProof,
		},
		{
			name: "bad receiptDigest",
			proof: Proof{
				CommandId:        "c",
				IdempotencyKey:   "k",
				RequestDigest:    digestA,
				ExpectedSequence: 1,
				ReceiptDigest:    "no-prefix",
			},
			reason: ReasonMalformedProof,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.proof.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), string(tc.reason)) {
				t.Errorf("expected reason %q in error, got: %v", tc.reason, err)
			}
		})
	}
}

func TestRejectionReasonValidate(t *testing.T) {
	for _, r := range allReasons {
		if err := r.Validate(); err != nil {
			t.Errorf("valid reason %q rejected: %v", r, err)
		}
	}
	if err := RejectionReason("bogus").Validate(); err == nil {
		t.Error("expected error for bogus reason")
	}
}

func TestMutationKindValidate(t *testing.T) {
	for _, k := range []MutationKind{MutateFactAppend, MutateDispatch, MutateResultAccept} {
		if err := k.Validate(); err != nil {
			t.Errorf("valid kind %q rejected: %v", k, err)
		}
	}
	if err := MutationKind("bogus").Validate(); err == nil {
		t.Error("expected error for bogus kind")
	}
}

func TestComputeReceiptDigestDeterministic(t *testing.T) {
	d1 := ComputeReceiptDigest("cmd", 1, digestA, digestB)
	d2 := ComputeReceiptDigest("cmd", 1, digestA, digestB)
	if d1 != d2 {
		t.Errorf("non-deterministic: %q != %q", d1, d2)
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Errorf("missing sha256: prefix: %q", d1)
	}
	if len(d1) != len("sha256:")+64 {
		t.Errorf("wrong digest length: %d", len(d1))
	}

	d3 := ComputeReceiptDigest("cmd", 2, digestA, digestB)
	if d1 == d3 {
		t.Error("different inputs produced same digest")
	}
}

// ── Cross-cut crash window regression (real outbox + real verifier) ──────────

func TestCrossCutCommitCrashWindow(t *testing.T) {
	obx := outbox.New(outbox.WithCrashPoint(outbox.CrashPointCommit))
	adapter := NewOutboxVerifierAdapter(obx)
	g := NewGate(adapter)

	req := outbox.Request{
		IdempotencyKey: "idem-cc",
		RequestDigest:  digestA,
		Kind:           engine.CommandKindDispatch,
		FactDigest:     digestC,
	}

	_, err := obx.Commit(req)
	if err == nil {
		t.Fatal("expected crash injection error from Commit")
	}

	// Proof for the would-be entry — should fail because entry doesn't exist.
	wouldBeReceiptDigest := ComputeReceiptDigest("would-be", 1, digestC, digestA)
	proof := Proof{
		CommandId:        "would-be",
		IdempotencyKey:   "idem-cc",
		RequestDigest:    digestA,
		ExpectedSequence: 1,
		ReceiptDigest:    wouldBeReceiptDigest,
	}
	_, err = g.Apply(proof, MutateFactAppend)
	assertHardFail(t, err, ReasonNotCommitted)
	assertGateState(t, g, 0, 0, 0, 0)

	// Retry without crash: fresh outbox.
	obx2 := outbox.New()
	adapter2 := NewOutboxVerifierAdapter(obx2)
	g2 := NewGate(adapter2)

	rcp, err := obx2.Commit(req)
	if err != nil {
		t.Fatalf("retry Commit: %v", err)
	}
	adapter2.ObserveCommit(req.IdempotencyKey, rcp)

	proof2 := Proof{
		CommandId:        rcp.CommandId,
		IdempotencyKey:   req.IdempotencyKey,
		RequestDigest:    rcp.RequestDigest,
		ExpectedSequence: rcp.Sequence,
		ReceiptDigest:    ComputeReceiptDigest(rcp.CommandId, rcp.Sequence, rcp.FactDigest, rcp.RequestDigest),
	}
	res, err := g2.Apply(proof2, MutateFactAppend)
	if err != nil {
		t.Fatalf("retry Apply: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("sequence: got %d, want 1", res.Sequence)
	}
	assertGateState(t, g2, 1, 1, 0, 0)

	// Replay: same proof again → already-applied.
	_, err = g2.Apply(proof2, MutateFactAppend)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g2, 1, 1, 0, 0)
}

func TestCrossCutDispatchCrashWindow(t *testing.T) {
	obx := outbox.New(outbox.WithCrashPoint(outbox.CrashPointDispatch))
	adapter := NewOutboxVerifierAdapter(obx)
	g := NewGate(adapter)

	req := outbox.Request{
		IdempotencyKey: "idem-dc",
		RequestDigest:  digestA,
		Kind:           engine.CommandKindDispatch,
		FactDigest:     digestC,
	}

	rcp, err := obx.Commit(req)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adapter.ObserveCommit(req.IdempotencyKey, rcp)

	// Commit succeeded — fact-append should work.
	proof := Proof{
		CommandId:        rcp.CommandId,
		IdempotencyKey:   req.IdempotencyKey,
		RequestDigest:    rcp.RequestDigest,
		ExpectedSequence: rcp.Sequence,
		ReceiptDigest:    ComputeReceiptDigest(rcp.CommandId, rcp.Sequence, rcp.FactDigest, rcp.RequestDigest),
	}
	res, err := g.Apply(proof, MutateFactAppend)
	if err != nil {
		t.Fatalf("fact-append: %v", err)
	}
	if res.Sequence != 1 {
		t.Errorf("sequence: got %d, want 1", res.Sequence)
	}

	// Dispatch crashed — dispatch-mark should fail.
	_, err = obx.Dispatch(rcp.CommandId)
	if err == nil {
		t.Fatal("expected crash injection from Dispatch")
	}

	_, err = g.Apply(proof, MutateDispatch)
	assertHardFail(t, err, ReasonNotCommitted)

	// State: fact appended but dispatch NOT marked (no double advance).
	assertGateState(t, g, 1, 1, 0, 0)

	// Replay fact-append → already-applied.
	_, err = g.Apply(proof, MutateFactAppend)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g, 1, 1, 0, 0)
}

func TestCrossCutResultCrashWindow(t *testing.T) {
	obx := outbox.New(outbox.WithCrashPoint(outbox.CrashPointResult))
	adapter := NewOutboxVerifierAdapter(obx)
	g := NewGate(adapter)

	req := outbox.Request{
		IdempotencyKey: "idem-rc",
		RequestDigest:  digestA,
		Kind:           engine.CommandKindDispatch,
		FactDigest:     digestC,
	}

	rcp, err := obx.Commit(req)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adapter.ObserveCommit(req.IdempotencyKey, rcp)

	proof := Proof{
		CommandId:        rcp.CommandId,
		IdempotencyKey:   req.IdempotencyKey,
		RequestDigest:    rcp.RequestDigest,
		ExpectedSequence: rcp.Sequence,
		ReceiptDigest:    ComputeReceiptDigest(rcp.CommandId, rcp.Sequence, rcp.FactDigest, rcp.RequestDigest),
	}

	// Fact-append succeeds.
	_, err = g.Apply(proof, MutateFactAppend)
	if err != nil {
		t.Fatalf("fact-append: %v", err)
	}

	// Dispatch succeeds (crash is only at result).
	_, err = obx.Dispatch(rcp.CommandId)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	_, err = g.Apply(proof, MutateDispatch)
	if err != nil {
		t.Fatalf("dispatch-mark: %v", err)
	}

	// Result crashes.
	_, err = obx.RecordResult(rcp.CommandId, digestE)
	if err == nil {
		t.Fatal("expected crash injection from RecordResult")
	}

	_, err = g.Apply(proof, MutateResultAccept)
	assertHardFail(t, err, ReasonNotCommitted)

	// State: fact + dispatch applied, result NOT accepted (no double advance).
	assertGateState(t, g, 1, 1, 1, 0)

	// Replay dispatch-mark → already-applied.
	_, err = g.Apply(proof, MutateDispatch)
	assertHardFail(t, err, ReasonAlreadyApplied)
	assertGateState(t, g, 1, 1, 1, 0)
}
