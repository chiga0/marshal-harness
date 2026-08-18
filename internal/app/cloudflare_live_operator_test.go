package app

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/provider/cloudflare"
)

// liveOperatorTestSigner is a fixture ReceiptSigner. It lives in the test
// file only: production operator/verifier code never constructs a private
// key.
type liveOperatorTestSigner struct {
	keyId   string
	private ed25519.PrivateKey
}

func (s liveOperatorTestSigner) KeyId() string { return s.keyId }

func (s liveOperatorTestSigner) Algorithm() string { return cloudflare.SignatureAlgorithmEd25519 }

func (s liveOperatorTestSigner) Sign(digest string) ([]byte, error) {
	return ed25519.Sign(s.private, []byte(digest)), nil
}

// liveOperatorTestKeys returns a deterministic ed25519 key pair.
func liveOperatorTestKeys() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	private := ed25519.NewKeyFromSeed(seed)
	return private.Public().(ed25519.PublicKey), private
}

// newLiveOperatorVerifier builds one operator and one verifier sharing a
// single trust root and a fresh durable ledger.
func newLiveOperatorVerifier(t *testing.T) (*LiveEvidenceOperator, *LiveEvidenceVerifier, LiveEvidenceBinding) {
	t.Helper()
	publicKey, privateKey := liveOperatorTestKeys()
	signer := liveOperatorTestSigner{keyId: "root-1", private: privateKey}
	operator, err := NewLiveEvidenceOperator("cloudflare-bridge", signer)
	if err != nil {
		t.Fatalf("NewLiveEvidenceOperator: %v", err)
	}
	root := cloudflare.TrustRoot{KeyId: "root-1", Algorithm: cloudflare.SignatureAlgorithmEd25519, PublicKey: publicKey}
	ledger, err := cloudflare.NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	verifier, err := NewLiveEvidenceVerifier(map[string]cloudflare.TrustRoot{"root-1": root}, ledger)
	if err != nil {
		t.Fatalf("NewLiveEvidenceVerifier: %v", err)
	}
	binding := LiveEvidenceBinding{
		ServiceId:    "cloudflare-bridge",
		RunId:        "run-1",
		AttemptId:    "attempt-1",
		AllocationId: "alloc-1",
		Generation:   1,
	}
	return operator, verifier, binding
}

// validOperatorFacts returns one well-formed fact set matching the fixture
// binding.
func validOperatorFacts() cloudflare.LiveFacts {
	return cloudflare.LiveFacts{
		ServiceId:    "cloudflare-bridge",
		RunId:        "run-1",
		AttemptId:    "attempt-1",
		AllocationId: "alloc-1",
		Generation:   1,
		LiveResult:   cloudflare.LiveResultCompleted,
		ExitCode:     0,
		Provision:    cloudflare.ProvisionStatusActive,
		Terminate:    cloudflare.TerminateStatusTerminated,
		Bookkeeping: cloudflare.BookkeepingFacts{
			ActiveAllocationCount: 0,
			OrphanAllocationCount: 0,
			DriftDetected:         false,
		},
	}
}

// signOperatorReceipt builds and signs one receipt with an explicit class and
// fact set for negative fixtures.
func signOperatorReceipt(signer liveOperatorTestSigner, class cloudflare.LiveEvidenceClass, facts cloudflare.LiveFacts) cloudflare.LiveEvidenceReceipt {
	receipt := cloudflare.LiveEvidenceReceipt{
		EvidenceClass:      class,
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
	return receipt
}

// TestLiveEvidenceOperatorVerifierRoundTrip freezes the happy path: a signed
// receipt verifies and consumes exactly once, and the identical receipt is a
// replay that fails closed.
func TestLiveEvidenceOperatorVerifierRoundTrip(t *testing.T) {
	operator, verifier, binding := newLiveOperatorVerifier(t)

	receipt, err := operator.Sign(validOperatorFacts())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if receipt.EvidenceClass != cloudflare.LiveEvidenceClassLive {
		t.Fatalf("the operator must issue truthful live evidence, got %q", receipt.EvidenceClass)
	}
	if err := verifier.Verify(receipt, binding); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	consumed, err := verifier.Consume(receipt, binding)
	if err != nil || !consumed {
		t.Fatalf("first consume must succeed: consumed=%t err=%v", consumed, err)
	}
	replayed, err := verifier.Consume(receipt, binding)
	if !errors.Is(err, cloudflare.ErrLiveEvidenceReplay) || replayed {
		t.Fatalf("a replay must fail closed with ErrLiveEvidenceReplay, got consumed=%t err=%v", replayed, err)
	}
}

// TestLiveEvidenceOperatorRejectsForeignService freezes the operator's
// service binding: facts carrying another service id fail closed.
func TestLiveEvidenceOperatorRejectsForeignService(t *testing.T) {
	operator, _, _ := newLiveOperatorVerifier(t)
	facts := validOperatorFacts()
	facts.ServiceId = "cloudflare-other"
	if _, err := operator.Sign(facts); !errors.Is(err, cloudflare.ErrLiveEvidenceBindingMismatch) {
		t.Fatalf("Sign must reject a foreign service with ErrLiveEvidenceBindingMismatch, got %v", err)
	}
}

// TestLiveEvidenceVerifierRejectsBindingMismatch freezes the exact binding:
// any substituted service/run/attempt/allocation/generation fails closed.
func TestLiveEvidenceVerifierRejectsBindingMismatch(t *testing.T) {
	operator, verifier, binding := newLiveOperatorVerifier(t)
	receipt, err := operator.Sign(validOperatorFacts())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	cases := []struct {
		name   string
		change func(*LiveEvidenceBinding)
	}{
		{"service", func(b *LiveEvidenceBinding) { b.ServiceId = "cloudflare-other" }},
		{"run", func(b *LiveEvidenceBinding) { b.RunId = "run-other" }},
		{"attempt", func(b *LiveEvidenceBinding) { b.AttemptId = "attempt-other" }},
		{"allocation", func(b *LiveEvidenceBinding) { b.AllocationId = "alloc-other" }},
		{"generation", func(b *LiveEvidenceBinding) { b.Generation = 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mismatched := binding
			tc.change(&mismatched)
			if err := verifier.Verify(receipt, mismatched); !errors.Is(err, cloudflare.ErrLiveEvidenceBindingMismatch) {
				t.Fatalf("Verify must reject a %s binding mismatch, got %v", tc.name, err)
			}
		})
	}
}

// TestLiveEvidenceVerifierRejectsSimulated freezes that a simulated receipt
// never verifies, even with an intact signature.
func TestLiveEvidenceVerifierRejectsSimulated(t *testing.T) {
	_, verifier, binding := newLiveOperatorVerifier(t)
	_, privateKey := liveOperatorTestKeys()
	signer := liveOperatorTestSigner{keyId: "root-1", private: privateKey}
	simulated := signOperatorReceipt(signer, cloudflare.LiveEvidenceClassSimulated, validOperatorFacts())
	if err := simulated.Validate(); err != nil {
		t.Fatalf("a simulated receipt is structurally valid: %v", err)
	}
	if err := verifier.Verify(simulated, binding); !errors.Is(err, cloudflare.ErrLiveEvidenceSimulated) {
		t.Fatalf("Verify must reject simulated evidence, got %v", err)
	}
}

// TestLiveEvidenceVerifierRejectsUnavailable freezes that an unavailable
// live result never verifies.
func TestLiveEvidenceVerifierRejectsUnavailable(t *testing.T) {
	_, verifier, binding := newLiveOperatorVerifier(t)
	_, privateKey := liveOperatorTestKeys()
	signer := liveOperatorTestSigner{keyId: "root-1", private: privateKey}
	facts := validOperatorFacts()
	facts.LiveResult = cloudflare.LiveResultUnavailable
	unavailable := signOperatorReceipt(signer, cloudflare.LiveEvidenceClassLive, facts)
	if err := verifier.Verify(unavailable, binding); !errors.Is(err, cloudflare.ErrLiveEvidenceUnavailable) {
		t.Fatalf("Verify must reject an unavailable live result, got %v", err)
	}
}

// TestLiveEvidenceVerifierRejectsMissingTrustRoot freezes that a receipt
// signed under an unknown key id never verifies.
func TestLiveEvidenceVerifierRejectsMissingTrustRoot(t *testing.T) {
	_, verifier, binding := newLiveOperatorVerifier(t)
	_, privateKey := liveOperatorTestKeys()
	signer := liveOperatorTestSigner{keyId: "root-unknown", private: privateKey}
	receipt := signOperatorReceipt(signer, cloudflare.LiveEvidenceClassLive, validOperatorFacts())
	if err := verifier.Verify(receipt, binding); !errors.Is(err, cloudflare.ErrLiveEvidenceTrustRootMissing) {
		t.Fatalf("Verify must reject a missing trust root, got %v", err)
	}
}

// TestLiveEvidenceVerifierRejectsTamperedSignature freezes unforgeability at
// the verifier: a tampered fact breaks the signature and fails closed.
func TestLiveEvidenceVerifierRejectsTamperedSignature(t *testing.T) {
	operator, verifier, binding := newLiveOperatorVerifier(t)
	receipt, err := operator.Sign(validOperatorFacts())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	tampered := receipt
	tampered.Facts.ExitCode = 1
	if err := verifier.Verify(tampered, binding); err == nil {
		t.Fatal("Verify accepted a receipt with a tampered fact")
	}
}

// TestLiveEvidenceVerifierConsumeSurvivesRestart freezes durability at the
// verifier level: a fresh verifier over the same ledger rejects a replay.
func TestLiveEvidenceVerifierConsumeSurvivesRestart(t *testing.T) {
	operator, verifier, binding := newLiveOperatorVerifier(t)
	receipt, err := operator.Sign(validOperatorFacts())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := verifier.Consume(receipt, binding); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	// Rebuild a verifier over the same durable ledger.
	publicKey, _ := liveOperatorTestKeys()
	root := cloudflare.TrustRoot{KeyId: "root-1", Algorithm: cloudflare.SignatureAlgorithmEd25519, PublicKey: publicKey}
	reopened, err := NewLiveEvidenceVerifier(map[string]cloudflare.TrustRoot{"root-1": root}, verifier.Ledger())
	if err != nil {
		t.Fatalf("NewLiveEvidenceVerifier: %v", err)
	}
	if _, err := reopened.Consume(receipt, binding); !errors.Is(err, cloudflare.ErrLiveEvidenceReplay) {
		t.Fatalf("a fresh verifier over the same ledger must reject a replay, got %v", err)
	}
}

// TestLiveEvidenceVerifierConcurrentConsume freezes exactly-once under
// concurrency: many concurrent consumers of the identical receipt cannot all
// succeed.
func TestLiveEvidenceVerifierConcurrentConsume(t *testing.T) {
	operator, verifier, binding := newLiveOperatorVerifier(t)
	receipt, err := operator.Sign(validOperatorFacts())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	const consumers = 32
	var wg sync.WaitGroup
	successes := make(chan bool, consumers)
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumed, err := verifier.Consume(receipt, binding)
			if err != nil && !errors.Is(err, cloudflare.ErrLiveEvidenceReplay) {
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

// badAlgorithmSigner wraps a ReceiptSigner and lies about its algorithm, for
// the construction fail-closed fixture.
type badAlgorithmSigner struct {
	inner cloudflare.ReceiptSigner
}

func (s badAlgorithmSigner) KeyId() string { return s.inner.KeyId() }

func (s badAlgorithmSigner) Algorithm() string { return "rsa-pss" }

func (s badAlgorithmSigner) Sign(d string) ([]byte, error) { return s.inner.Sign(d) }

// TestNewLiveEvidenceOperatorRejectsInvalidConfig freezes operator
// construction fail closed.
func TestNewLiveEvidenceOperatorRejectsInvalidConfig(t *testing.T) {
	_, privateKey := liveOperatorTestKeys()
	valid := liveOperatorTestSigner{keyId: "root-1", private: privateKey}
	if _, err := NewLiveEvidenceOperator("", valid); err == nil {
		t.Fatal("NewLiveEvidenceOperator accepted an empty service id")
	}
	if _, err := NewLiveEvidenceOperator("cloudflare-bridge", nil); err == nil {
		t.Fatal("NewLiveEvidenceOperator accepted a nil signer")
	}
	if _, err := NewLiveEvidenceOperator("cloudflare-bridge", liveOperatorTestSigner{private: privateKey}); err == nil {
		t.Fatal("NewLiveEvidenceOperator accepted a signer with an empty key id")
	}
	if _, err := NewLiveEvidenceOperator("cloudflare-bridge", badAlgorithmSigner{inner: valid}); err == nil {
		t.Fatal("NewLiveEvidenceOperator accepted a signer with an unsupported algorithm")
	}
}

// TestNewLiveEvidenceVerifierRejectsInvalidConfig freezes verifier
// construction fail closed.
func TestNewLiveEvidenceVerifierRejectsInvalidConfig(t *testing.T) {
	publicKey, _ := liveOperatorTestKeys()
	ledger, err := cloudflare.NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("NewLiveEvidenceLedger: %v", err)
	}
	validRoot := cloudflare.TrustRoot{KeyId: "root-1", Algorithm: cloudflare.SignatureAlgorithmEd25519, PublicKey: publicKey}

	if _, err := NewLiveEvidenceVerifier(nil, ledger); err == nil {
		t.Fatal("NewLiveEvidenceVerifier accepted no trust roots")
	}
	if _, err := NewLiveEvidenceVerifier(map[string]cloudflare.TrustRoot{"root-1": validRoot}, nil); err == nil {
		t.Fatal("NewLiveEvidenceVerifier accepted a nil ledger")
	}
	if _, err := NewLiveEvidenceVerifier(map[string]cloudflare.TrustRoot{"root-1": validRoot, "root-other": {KeyId: "root-1", Algorithm: cloudflare.SignatureAlgorithmEd25519, PublicKey: publicKey}}, ledger); err == nil {
		t.Fatal("NewLiveEvidenceVerifier accepted a map key that mismatches the trust root key id")
	}
}
