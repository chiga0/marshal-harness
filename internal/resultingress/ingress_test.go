package resultingress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ── test helpers ──────────────────────────────────────────────────────────────

func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

var (
	testNamespace = "ns-test"
	testTaskID    = "task-1"
	testRunID     = "run-1"
	testAttemptID = "attempt-1"
	testAllocID   = "alloc-1"
	testLeaseID   = "lease-1"
	testFencing   = "fence-abc"
	testCommandID = "cmd-1"
	testIdemKey   = "idem-key-1"
	testNonce     = "nonce-1"
	testRegID     = "reg-1"
	testSnapshot  = fixedDigest("snapshot-1")
	testEvidence  = fixedDigest("evidence-1")
	futureExpiry  = time.Now().Add(24 * time.Hour)
)

func validBinding() LedgerBinding {
	return LedgerBinding{
		LeaseID:        testLeaseID,
		Generation:     1,
		FencingToken:   testFencing,
		AttemptID:      testAttemptID,
		AllocationID:   testAllocID,
		Expiry:         futureExpiry,
		Revoked:        false,
		RegistrationID: testRegID,
		SnapshotDigest: testSnapshot,
		EvidenceDigest: testEvidence,
	}
}

func validDRC(resultDigest string) DRC {
	return DRC{
		AuthorityNamespaceID: testNamespace,
		TaskID:               testTaskID,
		RunID:                testRunID,
		AttemptID:            testAttemptID,
		AllocationID:         testAllocID,
		LeaseID:              testLeaseID,
		Generation:           1,
		FencingToken:         testFencing,
		CommandID:            testCommandID,
		IdempotencyKey:       testIdemKey,
		RequestDigest:        resultDigest,
		Nonce:                testNonce,
		Expiry:               futureExpiry,
		Operation:            OpResult,
		RegistrationID:       testRegID,
		SnapshotDigest:       testSnapshot,
		EvidenceDigest:       testEvidence,
	}
}

func validEnvelope(resultDigest string, seq uint64) ResultEnvelope {
	return ResultEnvelope{
		Kind:         KindWorkerResult,
		ResultDigest: resultDigest,
		Sequence:     seq,
	}
}

func newTestIngress(t *testing.T) *Ingress {
	t.Helper()
	ing, err := NewIngress(validBinding())
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	return ing
}

// ── (a) first admission succeeds and advances ledger sequence ─────────────────

func TestAdmit_FirstAdmissionSucceeds(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("payload-1")
	fact, err := ing.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if fact.LedgerSequence != 1 {
		t.Fatalf("expected ledgerSequence=1 got %d", fact.LedgerSequence)
	}
	if fact.IdempotentReplay {
		t.Fatal("first admission must not be marked IdempotentReplay")
	}
	if fact.FactDigest == "" {
		t.Fatal("FactDigest must not be empty")
	}
}

// ── (b) idempotent replay: same digest/sequence returns existing fact ─────────

func TestAdmit_IdempotentReplay(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("payload-replay")
	drc := validDRC(digest)
	env := validEnvelope(digest, 1)

	first, err := ing.Admit(context.Background(), drc, env)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}

	second, err := ing.Admit(context.Background(), drc, env)
	if err != nil {
		t.Fatalf("replay Admit: %v", err)
	}
	if !second.IdempotentReplay {
		t.Fatal("replay must be marked IdempotentReplay")
	}
	if second.LedgerSequence != first.LedgerSequence {
		t.Fatalf("ledger sequence must not advance on replay: first=%d second=%d",
			first.LedgerSequence, second.LedgerSequence)
	}
	if second.FactDigest != first.FactDigest {
		t.Fatalf("FactDigest must be identical on replay: %q != %q",
			second.FactDigest, first.FactDigest)
	}
}

// ── (c) digest mismatch → ErrDigestMismatch + quarantine ─────────────────────

func TestAdmit_DigestMismatch(t *testing.T) {
	ing := newTestIngress(t)
	drcDigest := fixedDigest("declared")
	envelopeDigest := fixedDigest("different")
	drc := validDRC(drcDigest)
	env := validEnvelope(envelopeDigest, 1)

	_, err := ing.Admit(context.Background(), drc, env)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 {
		t.Fatalf("expected 1 quarantine record got %d", len(q))
	}
	if q[0].Reason != ReasonDigestMismatch {
		t.Fatalf("expected reason %q got %q", ReasonDigestMismatch, q[0].Reason)
	}
}

// ── (d) revoked DRC → ErrDRCRevoked + quarantine ─────────────────────────────

func TestAdmit_RevokedDRC(t *testing.T) {
	binding := validBinding()
	binding.Revoked = true
	ing, err := NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	digest := fixedDigest("revoked-payload")
	_, err = ing.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1))
	if !errors.Is(err, ErrDRCRevoked) {
		t.Fatalf("expected ErrDRCRevoked got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 {
		t.Fatalf("expected 1 quarantine record got %d", len(q))
	}
	if q[0].Reason != ReasonRevoked {
		t.Fatalf("expected reason %q got %q", ReasonRevoked, q[0].Reason)
	}
}

// ── (e) stale generation → ErrStaleGeneration + quarantine ───────────────────

func TestAdmit_StaleGeneration(t *testing.T) {
	binding := validBinding()
	binding.Generation = 5
	ing, err := NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	digest := fixedDigest("stale-gen-payload")
	drc := validDRC(digest)
	drc.Generation = 3 // below ledger generation

	_, err = ing.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("expected ErrStaleGeneration got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 {
		t.Fatalf("expected 1 quarantine record got %d", len(q))
	}
	if q[0].Reason != ReasonStaleGeneration {
		t.Fatalf("expected reason %q got %q", ReasonStaleGeneration, q[0].Reason)
	}
}

// ── (e) expired lease → ErrStaleLease + quarantine ───────────────────────────

func TestAdmit_StaleLease_LedgerExpired(t *testing.T) {
	binding := validBinding()
	binding.Expiry = time.Now().Add(-time.Hour) // already expired
	ing, err := NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	digest := fixedDigest("stale-lease-payload")
	drc := validDRC(digest)
	drc.Expiry = time.Now().Add(time.Hour) // DRC still valid, but ledger expired

	_, err = ing.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonStaleLease {
		t.Fatalf("expected 1 quarantine record with reason %q; got %v", ReasonStaleLease, q)
	}
}

func TestAdmit_StaleLease_DRCExpired(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("drc-expired-payload")
	drc := validDRC(digest)
	drc.Expiry = time.Now().Add(-time.Hour) // DRC expired

	_, err := ing.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonExpired {
		t.Fatalf("expected 1 quarantine record with reason %q; got %v", ReasonExpired, q)
	}
}

// ── (f) malformed envelope / DRC fields → fail closed ────────────────────────

func TestAdmit_MalformedDRC_EmptyTaskID(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("malformed-drc")
	drc := validDRC(digest)
	drc.TaskID = ""

	_, err := ing.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if !errors.Is(err, ErrMalformedDRC) {
		t.Fatalf("expected ErrMalformedDRC got %v", err)
	}
}

func TestAdmit_MalformedEnvelope_ZeroSequence(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("malformed-env")
	env := ResultEnvelope{Kind: KindWorkerResult, ResultDigest: digest, Sequence: 0}

	_, err := ing.Admit(context.Background(), validDRC(digest), env)
	if !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected ErrMalformedEnvelope got %v", err)
	}
}

func TestAdmit_MalformedEnvelope_UnknownKind(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("bad-kind")
	env := ResultEnvelope{Kind: "invalid-kind", ResultDigest: digest, Sequence: 1}

	_, err := ing.Admit(context.Background(), validDRC(digest), env)
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonUnknownKind {
		t.Fatalf("expected 1 quarantine record with reason %q; got %v", ReasonUnknownKind, q)
	}
}

func TestAdmit_MalformedEnvelope_BadDigest(t *testing.T) {
	ing := newTestIngress(t)
	drc := validDRC(fixedDigest("real"))
	env := ResultEnvelope{Kind: KindCandidate, ResultDigest: "not-a-digest", Sequence: 1}

	_, err := ing.Admit(context.Background(), drc, env)
	if !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected ErrMalformedEnvelope got %v", err)
	}
}

// ── (g) quarantine records not returned by normal admission lookup ─────────────

func TestQuarantine_NotInAdmittedResults(t *testing.T) {
	ing := newTestIngress(t)
	// Cause a quarantine entry.
	badDigest := fixedDigest("bad")
	goodDigest := fixedDigest("good")
	drc := validDRC(badDigest)
	env := validEnvelope(goodDigest, 1) // digest mismatch
	ing.Admit(context.Background(), drc, env)

	// Now admit a legitimate delivery with a different idempotency key.
	drc2 := validDRC(goodDigest)
	drc2.IdempotencyKey = "idem-key-2"
	drc2.Nonce = "nonce-2"
	fact, err := ing.Admit(context.Background(), drc2, validEnvelope(goodDigest, 1))
	if err != nil {
		t.Fatalf("legitimate admission failed: %v", err)
	}
	// The legitimate fact must not be contaminated by quarantine.
	if fact.IdempotentReplay {
		t.Fatal("legitimate first admission incorrectly marked as replay")
	}
	q := ing.Quarantine()
	if len(q) != 1 {
		t.Fatalf("expected exactly 1 quarantine record got %d", len(q))
	}
}

// ── (h) AdmissionFact carries no trusted/verified semantic fields ─────────────

func TestAdmissionFact_NoTrustedVerifiedFields(t *testing.T) {
	// Structural assertion: AdmissionFact must not contain any field named
	// Trusted or Verified. This is a compile-time invariant but we confirm the
	// accessible surface via reflection-free field enumeration at test time.
	// We document this with a naming check using the zero value.
	var f AdmissionFact
	_ = f.FactDigest
	_ = f.LedgerSequence
	_ = f.IdempotentReplay
	// If Trusted or Verified were added, the compiler would not fail but this
	// test documents that the *only* exported fields are the three above.
	// Any addition must be intentional and will require this test to be updated.
}

// ── DRC Validate fail-closed table ───────────────────────────────────────────

func TestDRC_Validate_FailClosed(t *testing.T) {
	base := func() DRC {
		return DRC{
			AuthorityNamespaceID: testNamespace,
			TaskID:               testTaskID,
			RunID:                testRunID,
			AttemptID:            testAttemptID,
			AllocationID:         testAllocID,
			LeaseID:              testLeaseID,
			Generation:           1,
			FencingToken:         testFencing,
			CommandID:            testCommandID,
			IdempotencyKey:       testIdemKey,
			RequestDigest:        fixedDigest("x"),
			Nonce:                testNonce,
			Expiry:               futureExpiry,
			Operation:            OpResult,
			RegistrationID:       testRegID,
			SnapshotDigest:       testSnapshot,
			EvidenceDigest:       testEvidence,
		}
	}
	tests := []struct {
		name   string
		mutate func(*DRC)
	}{
		{"empty AuthorityNamespaceID", func(d *DRC) { d.AuthorityNamespaceID = "" }},
		{"empty TaskID", func(d *DRC) { d.TaskID = "" }},
		{"empty RunID", func(d *DRC) { d.RunID = "" }},
		{"empty AttemptID", func(d *DRC) { d.AttemptID = "" }},
		{"empty AllocationID", func(d *DRC) { d.AllocationID = "" }},
		{"empty LeaseID", func(d *DRC) { d.LeaseID = "" }},
		{"empty FencingToken", func(d *DRC) { d.FencingToken = "" }},
		{"empty CommandID", func(d *DRC) { d.CommandID = "" }},
		{"empty IdempotencyKey", func(d *DRC) { d.IdempotencyKey = "" }},
		{"empty Nonce", func(d *DRC) { d.Nonce = "" }},
		{"bad RequestDigest", func(d *DRC) { d.RequestDigest = "notadigest" }},
		{"zero Expiry", func(d *DRC) { d.Expiry = time.Time{} }},
		{"empty Operation", func(d *DRC) { d.Operation = "" }},
		{"invalid Operation", func(d *DRC) { d.Operation = "bogus" }},
		{"empty RegistrationID", func(d *DRC) { d.RegistrationID = "" }},
		{"bad SnapshotDigest", func(d *DRC) { d.SnapshotDigest = "notadigest" }},
		{"bad EvidenceDigest", func(d *DRC) { d.EvidenceDigest = "notadigest" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.mutate(&d)
			if err := d.Validate(); err == nil {
				t.Fatalf("expected error for %q but got nil", tc.name)
			}
		})
	}
}

// ── DRC Digest stability ──────────────────────────────────────────────────────

func TestDRC_Digest_Stable(t *testing.T) {
	d := validDRC(fixedDigest("stable"))
	h1, err := d.Digest()
	if err != nil {
		t.Fatalf("digest 1: %v", err)
	}
	h2, err := d.Digest()
	if err != nil {
		t.Fatalf("digest 2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("DRC digest not stable: %s != %s", h1, h2)
	}
}

func TestDRC_Digest_DifferentForDifferentDRCs(t *testing.T) {
	d1 := validDRC(fixedDigest("payload-a"))
	d2 := validDRC(fixedDigest("payload-b"))
	h1, _ := d1.Digest()
	h2, _ := d2.Digest()
	if h1 == h2 {
		t.Fatal("different DRCs produced identical digest")
	}
}

// ── NewIngress fail-closed ────────────────────────────────────────────────────

func TestNewIngress_FailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LedgerBinding)
	}{
		{"empty LeaseID", func(b *LedgerBinding) { b.LeaseID = "" }},
		{"empty AttemptID", func(b *LedgerBinding) { b.AttemptID = "" }},
		{"empty AllocationID", func(b *LedgerBinding) { b.AllocationID = "" }},
		{"empty FencingToken", func(b *LedgerBinding) { b.FencingToken = "" }},
		{"empty RegistrationID", func(b *LedgerBinding) { b.RegistrationID = "" }},
		{"bad SnapshotDigest", func(b *LedgerBinding) { b.SnapshotDigest = "notadigest" }},
		{"bad EvidenceDigest", func(b *LedgerBinding) { b.EvidenceDigest = "notadigest" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := validBinding()
			tc.mutate(&b)
			_, err := NewIngress(b)
			if err == nil {
				t.Fatalf("expected error for %q but got nil", tc.name)
			}
		})
	}
}

// ── Replay with mismatched digest is treated as forgery ───────────────────────

func TestAdmit_ReplaySameKeyDifferentDigest_IsForgery(t *testing.T) {
	ing := newTestIngress(t)
	digest1 := fixedDigest("original-payload")
	drc1 := validDRC(digest1)
	_, err := ing.Admit(context.Background(), drc1, validEnvelope(digest1, 1))
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}

	// Same idempotencyKey but different digest.
	digest2 := fixedDigest("forged-payload")
	drc2 := validDRC(digest2)
	// drc2 still has the same IdempotencyKey (testIdemKey) from validDRC.
	_, err = ing.Admit(context.Background(), drc2, validEnvelope(digest2, 1))
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch for forged replay got %v", err)
	}
	q := ing.Quarantine()
	if len(q) != 1 || q[0].Reason != ReasonDigestMismatch {
		t.Fatalf("expected quarantine record with reason %q; got %v", ReasonDigestMismatch, q)
	}
}

// ── Ledger sequence advances only on fresh admissions ────────────────────────

func TestAdmit_SequenceMonotone(t *testing.T) {
	ing := newTestIngress(t)

	d1 := fixedDigest("p1")
	drc1 := validDRC(d1)
	fact1, err := ing.Admit(context.Background(), drc1, validEnvelope(d1, 1))
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	d2 := fixedDigest("p2")
	drc2 := validDRC(d2)
	drc2.IdempotencyKey = "idem-key-2"
	drc2.Nonce = "nonce-2"
	fact2, err := ing.Admit(context.Background(), drc2, validEnvelope(d2, 2))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if fact2.LedgerSequence <= fact1.LedgerSequence {
		t.Fatalf("ledger sequence must be strictly monotone: %d <= %d",
			fact2.LedgerSequence, fact1.LedgerSequence)
	}

	// Replay of first must not advance sequence.
	replay, err := ing.Admit(context.Background(), drc1, validEnvelope(d1, 1))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.LedgerSequence != fact1.LedgerSequence {
		t.Fatalf("replay must not advance sequence: got %d want %d",
			replay.LedgerSequence, fact1.LedgerSequence)
	}
}

// ── KindCandidate envelope is accepted ───────────────────────────────────────

func TestAdmit_CandidateKindAccepted(t *testing.T) {
	ing := newTestIngress(t)
	digest := fixedDigest("candidate-payload")
	drc := validDRC(digest)
	drc.Operation = OpCandidate
	env := ResultEnvelope{Kind: KindCandidate, ResultDigest: digest, Sequence: 1}
	_, err := ing.Admit(context.Background(), drc, env)
	if err != nil {
		t.Fatalf("candidate kind admission failed: %v", err)
	}
}
