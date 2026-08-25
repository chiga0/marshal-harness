package resultingress

import (
	"context"
	"errors"
	"testing"
)

// ── helpers for kind-specific test fixtures ───────────────────────────────────

func drcForKind(resultDigest string, kind EnvelopeKind) DRC {
	drc := validDRC(resultDigest)
	op, _ := kindToOperation(kind)
	drc.Operation = op
	return drc
}

func envForKind(kind EnvelopeKind, resultDigest string, seq uint64) ResultEnvelope {
	return ResultEnvelope{
		Kind:         kind,
		ResultDigest: resultDigest,
		Sequence:     seq,
	}
}

var allKinds = []EnvelopeKind{
	KindWorkerResult,
	KindCandidate,
	KindEvidenceRef,
	KindCheckpoint,
	KindHeartbeat,
	KindReceipt,
	KindLog,
	KindAssessment,
}

var hotPathKinds = []EnvelopeKind{KindCheckpoint, KindHeartbeat, KindLog}

var coldPathKinds = []EnvelopeKind{
	KindWorkerResult,
	KindCandidate,
	KindEvidenceRef,
	KindAssessment,
	KindReceipt,
}

// ── (1) All 8 kinds positive path ─────────────────────────────────────────────

func TestRecheck_AllKindsAdmitted(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(string(kind), func(t *testing.T) {
			ing := newTestIngress(t)
			digest := fixedDigest("payload-" + string(kind))
			drc := drcForKind(digest, kind)
			env := envForKind(kind, digest, 1)

			fact, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("Admit %q: %v", kind, err)
			}
			if fact.LedgerSequence != 1 {
				t.Fatalf("%q: expected ledgerSequence=1 got %d", kind, fact.LedgerSequence)
			}
			if fact.IdempotentReplay {
				t.Fatalf("%q: first admission must not be IdempotentReplay", kind)
			}
		})
	}
}

// ── (2) Kind→operation mismatch for every kind ────────────────────────────────

func TestRecheck_OperationMismatch(t *testing.T) {
	// For each kind, pair it with every wrong operation.
	for _, kind := range allKinds {
		correctOp, _ := kindToOperation(kind)
		for _, wrongOp := range []Operation{
			OpResult, OpLog, OpCheckpoint, OpCandidate,
			OpEvidenceRef, OpHeartbeat, OpReceipt,
		} {
			if wrongOp == correctOp {
				continue
			}
			label := string(kind) + "/" + string(wrongOp)
			t.Run(label, func(t *testing.T) {
				ing := newTestIngress(t)
				digest := fixedDigest("mismatch-" + label)
				drc := drcForKind(digest, kind)
				drc.Operation = wrongOp
				env := envForKind(kind, digest, 1)

				_, err := ing.Admit(context.Background(), drc, env)
				if !errors.Is(err, ErrOperationMismatch) {
					t.Fatalf("expected ErrOperationMismatch got %v", err)
				}
				q := ing.Quarantine()
				if len(q) != 1 || q[0].Reason != ReasonOperationMismatch {
					t.Fatalf("expected 1 quarantine with %q; got %v",
						ReasonOperationMismatch, q)
				}
			})
		}
	}
}

// ── (3) Hot path skips eligibility recheck ────────────────────────────────────

func TestRecheck_HotPathSkipsEligibility(t *testing.T) {
	for _, kind := range hotPathKinds {
		t.Run(string(kind), func(t *testing.T) {
			binding := validBinding()
			// Corrupt all three eligibility fields on the binding.
			binding.RegistrationID = "wrong-reg"
			binding.SnapshotDigest = fixedDigest("wrong-snap")
			binding.EvidenceDigest = fixedDigest("wrong-evi")
			ing, err := NewIngress(binding)
			if err != nil {
				t.Fatalf("NewIngress: %v", err)
			}

			digest := fixedDigest("hot-" + string(kind))
			// DRC carries the original (matching) eligibility fields,
			// which differ from the corrupted binding — hot path must
			// still admit because it skips eligibility recheck.
			drc := drcForKind(digest, kind)
			drc.RegistrationID = testRegID
			drc.SnapshotDigest = testSnapshot
			drc.EvidenceDigest = testEvidence
			env := envForKind(kind, digest, 1)

			fact, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("hot path %q must skip eligibility recheck: %v", kind, err)
			}
			if fact.LedgerSequence != 1 {
				t.Fatalf("expected ledgerSequence=1 got %d", fact.LedgerSequence)
			}
		})
	}
}

// ── (4) Cold path eligibility recheck failures ────────────────────────────────

func TestRecheck_ColdPathEligibilityFailures(t *testing.T) {
	tests := []struct {
		name       string
		mutateDRC  func(*DRC)
		wantErr    error
		wantReason RejectionReason
	}{
		{
			name:       "registration mismatch",
			mutateDRC:  func(d *DRC) { d.RegistrationID = "wrong-reg" },
			wantErr:    ErrIneligibleRegistration,
			wantReason: ReasonIneligibleRegistration,
		},
		{
			name:       "snapshot mismatch",
			mutateDRC:  func(d *DRC) { d.SnapshotDigest = fixedDigest("wrong-snap") },
			wantErr:    ErrIneligibleSnapshot,
			wantReason: ReasonIneligibleSnapshot,
		},
		{
			name:       "evidence mismatch",
			mutateDRC:  func(d *DRC) { d.EvidenceDigest = fixedDigest("wrong-evi") },
			wantErr:    ErrIneligibleEvidence,
			wantReason: ReasonIneligibleEvidence,
		},
	}

	for _, kind := range coldPathKinds {
		for _, tc := range tests {
			label := string(kind) + "/" + tc.name
			t.Run(label, func(t *testing.T) {
				ing := newTestIngress(t)
				digest := fixedDigest("elig-" + label)
				drc := drcForKind(digest, kind)
				tc.mutateDRC(&drc)
				env := envForKind(kind, digest, 1)

				_, err := ing.Admit(context.Background(), drc, env)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v got %v", tc.wantErr, err)
				}
				q := ing.Quarantine()
				if len(q) != 1 || q[0].Reason != tc.wantReason {
					t.Fatalf("expected 1 quarantine with %q; got %v",
						tc.wantReason, q)
				}
			})
		}
	}
}

// ── (5) Replay idempotency for hot and cold path kinds ────────────────────────

func TestReplay_HotPathIdempotent(t *testing.T) {
	for _, kind := range hotPathKinds {
		t.Run(string(kind), func(t *testing.T) {
			ing := newTestIngress(t)
			digest := fixedDigest("replay-hot-" + string(kind))
			drc := drcForKind(digest, kind)
			env := envForKind(kind, digest, 1)

			first, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("first Admit: %v", err)
			}
			second, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("replay Admit: %v", err)
			}
			if !second.IdempotentReplay {
				t.Fatal("replay must be IdempotentReplay")
			}
			if second.LedgerSequence != first.LedgerSequence {
				t.Fatalf("sequence must not advance: first=%d second=%d",
					first.LedgerSequence, second.LedgerSequence)
			}
			if second.FactDigest != first.FactDigest {
				t.Fatalf("FactDigest must match: %q != %q",
					second.FactDigest, first.FactDigest)
			}
		})
	}
}

func TestReplay_ColdPathIdempotent(t *testing.T) {
	for _, kind := range coldPathKinds {
		t.Run(string(kind), func(t *testing.T) {
			ing := newTestIngress(t)
			digest := fixedDigest("replay-cold-" + string(kind))
			drc := drcForKind(digest, kind)
			env := envForKind(kind, digest, 1)

			first, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("first Admit: %v", err)
			}
			second, err := ing.Admit(context.Background(), drc, env)
			if err != nil {
				t.Fatalf("replay Admit: %v", err)
			}
			if !second.IdempotentReplay {
				t.Fatal("replay must be IdempotentReplay")
			}
			if second.LedgerSequence != first.LedgerSequence {
				t.Fatalf("sequence must not advance: first=%d second=%d",
					first.LedgerSequence, second.LedgerSequence)
			}
			if second.FactDigest != first.FactDigest {
				t.Fatalf("FactDigest must match: %q != %q",
					second.FactDigest, first.FactDigest)
			}
		})
	}
}

// ── (6) Forgery: same key, different digest ───────────────────────────────────

func TestForgery_HotPath(t *testing.T) {
	for _, kind := range hotPathKinds {
		t.Run(string(kind), func(t *testing.T) {
			ing := newTestIngress(t)
			digest1 := fixedDigest("orig-hot-" + string(kind))
			drc1 := drcForKind(digest1, kind)
			_, err := ing.Admit(context.Background(), drc1, envForKind(kind, digest1, 1))
			if err != nil {
				t.Fatalf("first admission: %v", err)
			}

			digest2 := fixedDigest("forged-hot-" + string(kind))
			drc2 := drcForKind(digest2, kind)
			// Same IdempotencyKey, different digest.
			_, err = ing.Admit(context.Background(), drc2, envForKind(kind, digest2, 1))
			if !errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("expected ErrDigestMismatch got %v", err)
			}
			q := ing.Quarantine()
			if len(q) != 1 || q[0].Reason != ReasonDigestMismatch {
				t.Fatalf("expected 1 quarantine with %q; got %v",
					ReasonDigestMismatch, q)
			}
		})
	}
}

func TestForgery_ColdPath(t *testing.T) {
	for _, kind := range coldPathKinds {
		t.Run(string(kind), func(t *testing.T) {
			ing := newTestIngress(t)
			digest1 := fixedDigest("orig-cold-" + string(kind))
			drc1 := drcForKind(digest1, kind)
			_, err := ing.Admit(context.Background(), drc1, envForKind(kind, digest1, 1))
			if err != nil {
				t.Fatalf("first admission: %v", err)
			}

			digest2 := fixedDigest("forged-cold-" + string(kind))
			drc2 := drcForKind(digest2, kind)
			_, err = ing.Admit(context.Background(), drc2, envForKind(kind, digest2, 1))
			if !errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("expected ErrDigestMismatch got %v", err)
			}
			q := ing.Quarantine()
			if len(q) != 1 || q[0].Reason != ReasonDigestMismatch {
				t.Fatalf("expected 1 quarantine with %q; got %v",
					ReasonDigestMismatch, q)
			}
		})
	}
}

// ── (7) Hot/cold shared ledger sequence ───────────────────────────────────────

func TestRecheck_HotColdSharedLedger(t *testing.T) {
	ing := newTestIngress(t)

	// Hot path admission first.
	hotDigest := fixedDigest("hot-ledger")
	hotDRC := drcForKind(hotDigest, KindCheckpoint)
	hotEnv := envForKind(KindCheckpoint, hotDigest, 1)
	hotFact, err := ing.Admit(context.Background(), hotDRC, hotEnv)
	if err != nil {
		t.Fatalf("hot path admission: %v", err)
	}
	if hotFact.LedgerSequence != 1 {
		t.Fatalf("hot path: expected seq=1 got %d", hotFact.LedgerSequence)
	}

	// Cold path admission second — must advance the same ledger.
	coldDigest := fixedDigest("cold-ledger")
	coldDRC := drcForKind(coldDigest, KindWorkerResult)
	coldDRC.IdempotencyKey = "idem-cold-ledger"
	coldDRC.Nonce = "nonce-cold-ledger"
	coldEnv := envForKind(KindWorkerResult, coldDigest, 2)
	coldFact, err := ing.Admit(context.Background(), coldDRC, coldEnv)
	if err != nil {
		t.Fatalf("cold path admission: %v", err)
	}
	if coldFact.LedgerSequence != 2 {
		t.Fatalf("cold path: expected seq=2 got %d", coldFact.LedgerSequence)
	}
	if coldFact.LedgerSequence <= hotFact.LedgerSequence {
		t.Fatalf("ledger sequence must be strictly monotone: cold=%d <= hot=%d",
			coldFact.LedgerSequence, hotFact.LedgerSequence)
	}

	// Replay of hot path entry — must return existing fact.
	replayFact, err := ing.Admit(context.Background(), hotDRC, hotEnv)
	if err != nil {
		t.Fatalf("hot replay: %v", err)
	}
	if !replayFact.IdempotentReplay {
		t.Fatal("hot replay must be IdempotentReplay")
	}
	if replayFact.LedgerSequence != hotFact.LedgerSequence {
		t.Fatalf("hot replay seq must not advance: got %d want %d",
			replayFact.LedgerSequence, hotFact.LedgerSequence)
	}
}

// ── (8) Unknown kind → ErrUnknownKind + quarantine ────────────────────────────

func TestRecheck_UnknownKind(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("unknown-kind-payload")
	drc := validDRC(digest)
	env := ResultEnvelope{Kind: "totally-bogus", ResultDigest: digest, Sequence: 1}

	_, err := ing.Admit(context.Background(), drc, env)
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonUnknownKind {
		t.Fatalf("expected 1 quarantine with %q; got %v", ReasonUnknownKind, q)
	}
}

// ── (9) Operation field validation in DRC ────────────────────────────────────

func TestRecheck_OperationValidation(t *testing.T) {
	tests := []struct {
		name    string
		op      Operation
		wantErr error
	}{
		{"empty operation", "", ErrMalformedDRC},
		{"invalid operation", "bogus-op", ErrMalformedDRC},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := validDRC(fixedDigest("op-val"))
			d.Operation = tc.op
			if err := d.Validate(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v got %v", tc.wantErr, err)
			}
		})
	}
}

// ── (10) KindToOperation mapping correctness ──────────────────────────────────

func TestKindToOperation_Mapping(t *testing.T) {
	tests := []struct {
		kind EnvelopeKind
		op   Operation
	}{
		{KindWorkerResult, OpResult},
		{KindAssessment, OpResult},
		{KindCandidate, OpCandidate},
		{KindEvidenceRef, OpEvidenceRef},
		{KindCheckpoint, OpCheckpoint},
		{KindHeartbeat, OpHeartbeat},
		{KindReceipt, OpReceipt},
		{KindLog, OpLog},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, ok := kindToOperation(tc.kind)
			if !ok {
				t.Fatalf("kindToOperation(%q) returned ok=false", tc.kind)
			}
			if got != tc.op {
				t.Fatalf("kindToOperation(%q) = %q, want %q", tc.kind, got, tc.op)
			}
		})
	}
	// Unknown kind returns ok=false.
	if _, ok := kindToOperation("bogus"); ok {
		t.Fatal("kindToOperation must return ok=false for unknown kind")
	}
}

// ── (11) Hot/cold path classification ─────────────────────────────────────────

func TestIsHotPathKind(t *testing.T) {
	for _, kind := range hotPathKinds {
		if !isHotPathKind(kind) {
			t.Fatalf("%q must be hot path", kind)
		}
	}
	for _, kind := range coldPathKinds {
		if isHotPathKind(kind) {
			t.Fatalf("%q must be cold path", kind)
		}
	}
}

// ── (12) Cold path with revoked binding fails closed ──────────────────────────

func TestRecheck_ColdPathRevokedBinding(t *testing.T) {
	binding := validBinding()
	binding.Revoked = true
	ing, err := NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	digest := fixedDigest("revoked-cold")
	drc := drcForKind(digest, KindWorkerResult)
	_, err = ing.Admit(context.Background(), drc, envForKind(KindWorkerResult, digest, 1))
	if !errors.Is(err, ErrDRCRevoked) {
		t.Fatalf("expected ErrDRCRevoked got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonRevoked {
		t.Fatalf("expected 1 quarantine with %q; got %v", ReasonRevoked, q)
	}
}
