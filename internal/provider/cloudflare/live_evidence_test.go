package cloudflare

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// fixedLiveDigest derives a well-formed sha256 digest from seed material.
func fixedLiveDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

// testEd25519Keys returns a deterministic ed25519 key pair for fixtures. It
// never touches crypto/rand, so the fixtures are reproducible.
func testEd25519Keys() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	private := ed25519.NewKeyFromSeed(seed)
	return private.Public().(ed25519.PublicKey), private
}

// ed25519TestSigner is a fixture ReceiptSigner. It lives in the test file
// only: the record layer and the app never construct a private key.
type ed25519TestSigner struct {
	keyId   string
	private ed25519.PrivateKey
}

func (s ed25519TestSigner) KeyId() string { return s.keyId }

func (s ed25519TestSigner) Algorithm() string { return SignatureAlgorithmEd25519 }

func (s ed25519TestSigner) Sign(digest string) ([]byte, error) {
	return ed25519.Sign(s.private, []byte(digest)), nil
}

// validLiveFacts returns one well-formed fact set.
func validLiveFacts() LiveFacts {
	return LiveFacts{
		ServiceId:    "cloudflare-bridge",
		RunId:        "run-live",
		AttemptId:    "attempt-live",
		AllocationId: "alloc-live",
		Generation:   1,
		LiveResult:   LiveResultCompleted,
		ExitCode:     0,
		Provision:    ProvisionStatusActive,
		Terminate:    TerminateStatusTerminated,
		Bookkeeping: BookkeepingFacts{
			ActiveAllocationCount: 0,
			OrphanAllocationCount: 0,
			DriftDetected:         false,
		},
	}
}

// signLiveReceipt builds and signs one receipt from facts with the fixed
// signer, returning the receipt and the matching trust root.
func signLiveReceipt(facts LiveFacts) (LiveEvidenceReceipt, TrustRoot) {
	publicKey, privateKey := testEd25519Keys()
	signer := ed25519TestSigner{keyId: "trust-root-key-1", private: privateKey}
	receipt := LiveEvidenceReceipt{
		EvidenceClass:      LiveEvidenceClassLive,
		Facts:              facts,
		TrustRootKeyId:     signer.KeyId(),
		SignatureAlgorithm: signer.Algorithm(),
		SignedAt:           "2026-08-19T00:00:00Z",
	}
	digest, err := receipt.Digest()
	if err != nil {
		panic(err)
	}
	receipt.ReceiptDigest = digest
	signature, err := signer.Sign(digest)
	if err != nil {
		panic(err)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(signature)
	root := TrustRoot{KeyId: signer.KeyId(), Algorithm: SignatureAlgorithmEd25519, PublicKey: publicKey}
	return receipt, root
}

// malformedReceipt builds one receipt over arbitrary (possibly malformed)
// facts without signing it, so content validation — not digest computation —
// is the gate under test.
func malformedReceipt(facts LiveFacts) LiveEvidenceReceipt {
	return LiveEvidenceReceipt{
		EvidenceClass:      LiveEvidenceClassLive,
		Facts:              facts,
		TrustRootKeyId:     "trust-root-key-1",
		SignatureAlgorithm: SignatureAlgorithmEd25519,
		SignedAt:           "2026-08-19T00:00:00Z",
		ReceiptDigest:      fixedLiveDigest("placeholder-digest"),
		Signature:          "cGxhY2Vob2xkZXI=",
	}
}

// TestLiveEvidenceReceiptRejectsMalformedContent freezes the fail-closed
// content rules: empty identity facts, a non-positive generation, unknown
// closed enums, a bad trust root key id, a bad signature algorithm and a bad
// timestamp.
func TestLiveEvidenceReceiptRejectsMalformedContent(t *testing.T) {
	cases := []struct {
		name   string
		change func(*LiveFacts)
	}{
		{"empty serviceId", func(f *LiveFacts) { f.ServiceId = "" }},
		{"blank runId", func(f *LiveFacts) { f.RunId = "  " }},
		{"empty attemptId", func(f *LiveFacts) { f.AttemptId = "" }},
		{"empty allocationId", func(f *LiveFacts) { f.AllocationId = "" }},
		{"zero generation", func(f *LiveFacts) { f.Generation = 0 }},
		{"negative generation", func(f *LiveFacts) { f.Generation = -1 }},
		{"unknown liveResult", func(f *LiveFacts) { f.LiveResult = "timeout" }},
		{"unknown provision", func(f *LiveFacts) { f.Provision = "pending" }},
		{"unknown terminate", func(f *LiveFacts) { f.Terminate = "gone" }},
		{"negative active count", func(f *LiveFacts) { f.Bookkeeping.ActiveAllocationCount = -1 }},
		{"negative orphan count", func(f *LiveFacts) { f.Bookkeeping.OrphanAllocationCount = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := validLiveFacts()
			tc.change(&facts)
			receipt := malformedReceipt(facts)
			if err := receipt.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}

	facts := validLiveFacts()
	receipt, _ := signLiveReceipt(facts)
	mutations := []struct {
		name   string
		change func(*LiveEvidenceReceipt)
	}{
		{"empty trustRootKeyId", func(r *LiveEvidenceReceipt) { r.TrustRootKeyId = "" }},
		{"unknown signatureAlgorithm", func(r *LiveEvidenceReceipt) { r.SignatureAlgorithm = "rsa-pss" }},
		{"malformed signedAt", func(r *LiveEvidenceReceipt) { r.SignedAt = "19 August 2026" }},
		{"empty receiptDigest", func(r *LiveEvidenceReceipt) { r.ReceiptDigest = "" }},
		{"empty signature", func(r *LiveEvidenceReceipt) { r.Signature = "" }},
		{"unknown evidenceClass", func(r *LiveEvidenceReceipt) { r.EvidenceClass = "hermetic" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			mutated := receipt
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}
}

// TestLiveEvidenceReceiptRejectsDigestMismatch freezes that a tampered fact
// (which the digest covers) or a substituted digest fails closed.
func TestLiveEvidenceReceiptRejectsDigestMismatch(t *testing.T) {
	receipt, _ := signLiveReceipt(validLiveFacts())

	tampered := receipt
	tampered.Facts.RunId = "run-substituted"
	if err := tampered.Validate(); err == nil {
		t.Fatal("Validate accepted a receipt whose content no longer matches its digest")
	}

	substituted := receipt
	substituted.ReceiptDigest = fixedLiveDigest("other-digest")
	if err := substituted.Validate(); err == nil {
		t.Fatal("Validate accepted a receipt with a substituted digest")
	}
}

// TestLiveEvidenceTrustRootVerifyRejectsTampering freezes unforgeability: a
// valid signature verifies against the bound trust root, and any substitution
// of a fact, digest or key fails closed.
func TestLiveEvidenceTrustRootVerifyRejectsTampering(t *testing.T) {
	receipt, root := signLiveReceipt(validLiveFacts())
	if err := receipt.Validate(); err != nil {
		t.Fatalf("baseline receipt must validate: %v", err)
	}
	if err := root.Verify(receipt.ReceiptDigest, receipt.Signature); err != nil {
		t.Fatalf("baseline signature must verify: %v", err)
	}

	// A tampered fact breaks the signature (the digest changed).
	tampered := receipt
	tampered.Facts.Generation = 2
	if err := root.Verify(receipt.ReceiptDigest, receipt.Signature); err != nil {
		t.Fatal("the untampered signature over the untampered digest must still verify")
	}
	tamperedDigest, err := tampered.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if tamperedDigest == receipt.ReceiptDigest {
		t.Fatal("the tampered fact must change the canonical digest")
	}
	if err := root.Verify(tamperedDigest, receipt.Signature); err == nil {
		t.Fatal("Verify accepted a signature over a substituted fact digest")
	}

	// A different trust root public key never verifies the same signature.
	otherPub, _ := testEd25519Keys()
	otherPub[0] ^= 0xff
	otherRoot := TrustRoot{KeyId: root.KeyId, Algorithm: SignatureAlgorithmEd25519, PublicKey: otherPub}
	if err := otherRoot.Verify(receipt.ReceiptDigest, receipt.Signature); err == nil {
		t.Fatal("Verify accepted a signature under a substituted trust root key")
	}
}

// TestLiveEvidenceTrustRootValidateRejectsMalformed freezes the trust root
// content rules.
func TestLiveEvidenceTrustRootValidateRejectsMalformed(t *testing.T) {
	pub, _ := testEd25519Keys()
	valid := TrustRoot{KeyId: "root-1", Algorithm: SignatureAlgorithmEd25519, PublicKey: pub}
	if err := valid.Validate(); err != nil {
		t.Fatalf("baseline trust root must validate: %v", err)
	}
	cases := []struct {
		name string
		root TrustRoot
	}{
		{"empty key id", TrustRoot{Algorithm: SignatureAlgorithmEd25519, PublicKey: pub}},
		{"bad algorithm", TrustRoot{KeyId: "root-1", Algorithm: "rsa-pss", PublicKey: pub}},
		{"short public key", TrustRoot{KeyId: "root-1", Algorithm: SignatureAlgorithmEd25519, PublicKey: pub[:8]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.root.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}
}

// TestLiveEvidenceLedgerConsumeExactlyOnce freezes compare-and-consume: the
// first consume succeeds, the identical digest is a replay that fails closed.
func TestLiveEvidenceLedgerConsumeExactlyOnce(t *testing.T) {
	ledger, err := NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	digest := fixedLiveDigest("receipt-1")
	consumed, err := ledger.Consume(digest)
	if err != nil || !consumed {
		t.Fatalf("first consume must succeed, got consumed=%t err=%v", consumed, err)
	}
	if !ledger.Consumed(digest) {
		t.Fatal("the consumed digest must be recorded")
	}
	replayed, err := ledger.Consume(digest)
	if !errors.Is(err, ErrLiveEvidenceReplay) || replayed {
		t.Fatalf("a replay must fail closed with ErrLiveEvidenceReplay, got consumed=%t err=%v", replayed, err)
	}
	if ledger.Count() != 1 {
		t.Fatalf("the ledger must contain exactly one digest, got %d", ledger.Count())
	}
}

// TestLiveEvidenceLedgerSurvivesRestart freezes durability: a re-opened
// ledger still rejects the identical digest as consumed.
func TestLiveEvidenceLedgerSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	digest := fixedLiveDigest("receipt-restart")
	first, err := NewLiveEvidenceLedger(path)
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	if consumed, err := first.Consume(digest); err != nil || !consumed {
		t.Fatalf("first consume must succeed: consumed=%t err=%v", consumed, err)
	}

	reopened, err := NewLiveEvidenceLedger(path)
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger (reopen): %v", err)
	}
	if !reopened.Consumed(digest) {
		t.Fatal("a re-opened ledger must recover the consumed digest")
	}
	if _, err := reopened.Consume(digest); !errors.Is(err, ErrLiveEvidenceReplay) {
		t.Fatalf("a re-opened ledger must reject a replay, got %v", err)
	}
}

// TestLiveEvidenceLedgerWriteFailureLeavesMemoryUnchanged freezes failure
// atomicity: a failed persist leaves the in-memory set untouched.
func TestLiveEvidenceLedgerWriteFailureLeavesMemoryUnchanged(t *testing.T) {
	ledger, err := NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	if consumed, err := ledger.Consume(fixedLiveDigest("receipt-a")); err != nil || !consumed {
		t.Fatalf("first consume must succeed: %v", err)
	}
	ledger.write = func([]byte) error { return errors.New("injected disk failure") }
	digest := fixedLiveDigest("receipt-b")
	if consumed, err := ledger.Consume(digest); err == nil || consumed {
		t.Fatal("the injected write failure must surface and never report success")
	}
	if ledger.Consumed(digest) {
		t.Fatal("a failed consume must not change the live set")
	}
	if ledger.Count() != 1 {
		t.Fatalf("the ledger must still contain exactly one digest, got %d", ledger.Count())
	}
}

// TestLiveEvidenceLedgerRejectsMalformedFile freezes that a corrupt ledger
// file fails closed at construction.
func TestLiveEvidenceLedgerRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, []byte(`{"consumed":["not-a-digest"]}`), 0o600); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if _, err := NewLiveEvidenceLedger(path); !errors.Is(err, ErrLiveEvidenceLedgerInvalid) {
		t.Fatalf("a malformed ledger file must fail closed, got %v", err)
	}
}

// TestLiveEvidenceLedgerConcurrentConsume freezes that two concurrent
// consumers of the identical digest cannot both succeed.
func TestLiveEvidenceLedgerConcurrentConsume(t *testing.T) {
	ledger, err := NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	digest := fixedLiveDigest("receipt-concurrent")
	const consumers = 32
	var wg sync.WaitGroup
	successes := make(chan bool, consumers)
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumed, err := ledger.Consume(digest)
			if err != nil && !errors.Is(err, ErrLiveEvidenceReplay) {
				t.Errorf("unexpected consume error: %v", err)
				return
			}
			successes <- consumed
		}()
	}
	wg.Wait()
	close(successes)
	total := 0
	for success := range successes {
		if success {
			total++
		}
	}
	if total != 1 {
		t.Fatalf("exactly one concurrent consumer must succeed, got %d", total)
	}
}

// TestLiveEvidenceReceiptParseRoundTrip freezes the wire admission: a
// canonical JSON round-trip of a valid receipt re-validates.
func TestLiveEvidenceReceiptParseRoundTrip(t *testing.T) {
	receipt, _ := signLiveReceipt(validLiveFacts())
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	parsed, err := ParseLiveEvidenceReceipt(raw)
	if err != nil {
		t.Fatalf("ParseLiveEvidenceReceipt: %v", err)
	}
	if parsed.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("round-trip digest %q != %q", parsed.ReceiptDigest, receipt.ReceiptDigest)
	}
	if !strings.HasPrefix(parsed.ReceiptDigest, liveEvidenceDigestPrefix) {
		t.Fatalf("receiptDigest must carry the %s prefix, got %q", liveEvidenceDigestPrefix, parsed.ReceiptDigest)
	}
}
