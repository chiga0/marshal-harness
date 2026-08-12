package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
)

// testNow is the reference clock for the eligibility fixtures: after the
// fixture signedAt and before the default validUntil.
var testNow = time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

func testAuthorityNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "tenant-a",
		ControlPlaneId:   "control-plane-1",
		AuthorityScopeId: "authority-scope-1",
	}
}

func testSecurityDomain() authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   "tenant-a",
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "isolation-domain-1",
	}
}

func testAttestation() Attestation {
	return Attestation{
		ProviderInstanceId: "provider-instance-1",
		ConfigDigest:       "sha256:" + strings.Repeat("cd", 32),
		TrustRootKeyId:     "trust-root-key-1",
		TrustRootAlgorithm: "ed25519",
	}
}

// testRegistration builds a structurally valid registration whose
// registrationDigest is backfilled from Digest(), so the gate-2 Validate
// passes and only the lifecycleState under test drives eligibility.
func testRegistration(state LifecycleState, mutate func(*ProviderRegistration)) ProviderRegistration {
	registration := ProviderRegistration{
		RegistrationId:       "registration-1",
		AuthorityNamespaceId: testAuthorityNamespace(),
		SecurityDomainId:     testSecurityDomain(),
		Principal:            "principal-a",
		ProviderType:         "local-shell",
		ProviderName:         "provider-a",
		ProviderVersion:      "1.0.0",
		ProtocolVersion:      "v1",
		Scope:                "scope-a",
		IdempotencyKey:       "idempotency-key-1",
		RequestDigest:        "sha256:" + strings.Repeat("ab", 32),
		Attestation:          testAttestation(),
		LifecycleState:       state,
		CreatedAt:            "2026-01-01T00:00:00Z",
	}
	if mutate != nil {
		mutate(&registration)
	}
	digest, err := registration.Digest()
	if err != nil {
		panic(err)
	}
	registration.RegistrationDigest = digest
	return registration
}

func allDimensionsPassed() map[ConformanceDimension]DimensionResult {
	return map[ConformanceDimension]DimensionResult{
		ConformanceDimensionMount:      DimensionResultPassed,
		ConformanceDimensionNetwork:    DimensionResultPassed,
		ConformanceDimensionResource:   DimensionResultPassed,
		ConformanceDimensionCredential: DimensionResultPassed,
	}
}

// testEvidence builds a structurally valid evidence record whose
// evidenceDigest is backfilled from Digest() after the mutation, so the
// gate-2 Validate passes and only the mutated semantics drive eligibility.
func testEvidence(state EvidenceState, mutate func(*ConformanceEvidence)) ConformanceEvidence {
	evidence := ConformanceEvidence{
		AuthorityNamespaceId: testAuthorityNamespace(),
		SecurityDomainId:     testSecurityDomain(),
		ProviderInstanceId:   "provider-instance-1",
		ConfigDigest:         "sha256:" + strings.Repeat("cd", 32),
		TrustRootKeyId:       "trust-root-key-1",
		SuiteName:            "conformance-suite-a",
		ProbeArtifactDigest:  "sha256:" + strings.Repeat("ef", 32),
		DimensionResults:     allDimensionsPassed(),
		EvidenceState:        state,
		ProviderSelfSigned:   false,
		SignedAt:             "2026-01-01T00:00:00Z",
		ValidUntil:           "2027-01-01T00:00:00Z",
	}
	if mutate != nil {
		mutate(&evidence)
	}
	digest, err := evidence.Digest()
	if err != nil {
		panic(err)
	}
	evidence.EvidenceDigest = digest
	return evidence
}

// testSnapshot builds a structurally valid snapshot whose
// providerCapabilitySnapshotDigest is backfilled from Digest() after the
// mutation, so the gate-2 Validate passes and only the mutated semantics
// drive eligibility.
func testSnapshot(state SnapshotState, evidenceDigests []string, mutate func(*ProviderCapabilitySnapshot)) ProviderCapabilitySnapshot {
	if evidenceDigests == nil {
		evidenceDigests = []string{}
	}
	snapshot := ProviderCapabilitySnapshot{
		RegistrationId:             "registration-1",
		ProtocolVersion:            "v1",
		ProviderType:               "local-shell",
		ProviderName:               "provider-a",
		ProviderVersion:            "1.0.0",
		Capabilities:               map[string]string{"sandbox": "strict"},
		ConformanceEvidenceDigests: evidenceDigests,
		Scope:                      "scope-a",
		SnapshotState:              state,
		CreatedAt:                  "2026-01-01T00:00:00Z",
		Attestation:                testAttestation(),
	}
	if mutate != nil {
		mutate(&snapshot)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		panic(err)
	}
	snapshot.ProviderCapabilitySnapshotDigest = digest
	return snapshot
}

// testEvidencePair builds two structurally valid evidence records with
// distinct suite names, and therefore distinct evidence digests.
func testEvidencePair() (ConformanceEvidence, ConformanceEvidence) {
	first := testEvidence(EvidenceStateValid, nil)
	second := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.SuiteName = "conformance-suite-b"
	})
	return first, second
}

// TestValidateSnapshotEligibleActive asserts that a structurally valid active
// capability snapshot is eligible.
func TestValidateSnapshotEligibleActive(t *testing.T) {
	if err := ValidateSnapshotEligible(testSnapshot(SnapshotStateActive, nil, nil)); err != nil {
		t.Fatalf("an active snapshot must be eligible: %v", err)
	}
}

// TestValidateSnapshotEligibleFailClosed asserts that expired and superseded
// snapshots fail closed with an error naming the snapshotState.
func TestValidateSnapshotEligibleFailClosed(t *testing.T) {
	for _, state := range []SnapshotState{SnapshotStateExpired, SnapshotStateSuperseded} {
		state := state
		err := ValidateSnapshotEligible(testSnapshot(state, nil, nil))
		if err == nil {
			t.Fatalf("a %s snapshot must fail closed", state)
		}
		if !strings.Contains(err.Error(), "snapshotState") {
			t.Fatalf("the error must name the snapshotState, got %v", err)
		}
	}
}

// TestValidateEvidenceEligibleValidWindow asserts that valid evidence within
// its validity window is eligible, and that an empty validUntil carries no
// expiry.
func TestValidateEvidenceEligibleValidWindow(t *testing.T) {
	if err := ValidateEvidenceEligible(testEvidence(EvidenceStateValid, nil), testNow); err != nil {
		t.Fatalf("valid evidence within its validity window must be eligible: %v", err)
	}

	noExpiry := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.ValidUntil = ""
	})
	if err := ValidateEvidenceEligible(noExpiry, testNow); err != nil {
		t.Fatalf("evidence without validUntil carries no expiry and must be eligible: %v", err)
	}
}

// TestValidateEvidenceEligibleFailClosed asserts that revoked evidence,
// evidence past its validUntil (including the now-equals-validUntil
// boundary) and provider self-signed evidence all fail closed on the
// eligibility chain.
func TestValidateEvidenceEligibleFailClosed(t *testing.T) {
	revoked := testEvidence(EvidenceStateRevoked, nil)
	if err := ValidateEvidenceEligible(revoked, testNow); err == nil {
		t.Fatal("revoked evidence must fail closed")
	} else if !strings.Contains(err.Error(), "evidenceState") {
		t.Fatalf("the error must name the evidenceState, got %v", err)
	}

	expired := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.ValidUntil = "2026-01-02T00:00:00Z"
	})
	if err := ValidateEvidenceEligible(expired, testNow); err == nil {
		t.Fatal("evidence past its validUntil must fail closed")
	} else if !strings.Contains(err.Error(), "validUntil") {
		t.Fatalf("the error must name the validUntil expiry, got %v", err)
	}

	boundary := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.ValidUntil = testNow.Format(time.RFC3339)
	})
	if err := ValidateEvidenceEligible(boundary, testNow); err == nil {
		t.Fatal("now equal to validUntil must fail closed: now must be strictly before validUntil")
	}

	selfSigned := testEvidence(EvidenceStateValid, nil)
	selfSigned.ProviderSelfSigned = true
	if err := ValidateEvidenceEligible(selfSigned, testNow); err == nil {
		t.Fatal("provider self-signed evidence must fail closed on the eligibility chain")
	} else if !strings.Contains(err.Error(), "self-sign") {
		t.Fatalf("the error must reject self-signed evidence, got %v", err)
	}
}

// TestIsHardenedEligibleAllPassed asserts that evidence with all four closed
// dimensions passed and a valid state within its validity window is hardened
// eligible.
func TestIsHardenedEligibleAllPassed(t *testing.T) {
	if !IsHardenedEligible(testEvidence(EvidenceStateValid, nil), testNow) {
		t.Fatal("all four dimensions passed with valid evidence must be hardened eligible")
	}
}

// TestIsHardenedEligibleFailClosed asserts the hardened matrix: any failed or
// skipped dimension, an expired validUntil, a revoked evidenceState and
// provider self-signed evidence all yield false.
func TestIsHardenedEligibleFailClosed(t *testing.T) {
	for _, dimension := range []ConformanceDimension{
		ConformanceDimensionMount,
		ConformanceDimensionNetwork,
		ConformanceDimensionResource,
		ConformanceDimensionCredential,
	} {
		dimension := dimension
		for _, result := range []DimensionResult{DimensionResultFailed, DimensionResultSkipped} {
			result := result
			evidence := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
				evidence.DimensionResults[dimension] = result
			})
			if IsHardenedEligible(evidence, testNow) {
				t.Fatalf("a %q result on the %s dimension must not be hardened eligible", result, dimension)
			}
		}
	}

	expired := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.ValidUntil = "2026-01-02T00:00:00Z"
	})
	if IsHardenedEligible(expired, testNow) {
		t.Fatal("evidence past its validUntil must not be hardened eligible")
	}

	if IsHardenedEligible(testEvidence(EvidenceStateRevoked, nil), testNow) {
		t.Fatal("revoked evidence must not be hardened eligible")
	}

	selfSigned := testEvidence(EvidenceStateValid, nil)
	selfSigned.ProviderSelfSigned = true
	if IsHardenedEligible(selfSigned, testNow) {
		t.Fatal("provider self-signed evidence must not be hardened eligible")
	}
}

// TestValidateEvidenceSetForSnapshotExactReconciliation asserts that an exact
// digest reconciliation passes, and that an empty digest set with no
// evidences passes.
func TestValidateEvidenceSetForSnapshotExactReconciliation(t *testing.T) {
	first, second := testEvidencePair()

	exact := testSnapshot(SnapshotStateActive, []string{first.EvidenceDigest, second.EvidenceDigest}, nil)
	if err := ValidateEvidenceSetForSnapshot(exact, []ConformanceEvidence{first, second}); err != nil {
		t.Fatalf("an exact digest reconciliation must pass: %v", err)
	}

	emptyDigests := testSnapshot(SnapshotStateActive, nil, nil)
	if err := ValidateEvidenceSetForSnapshot(emptyDigests, nil); err != nil {
		t.Fatalf("an empty digest set with no evidences must pass: %v", err)
	}
}

// TestValidateEvidenceSetForSnapshotDigestFailClosed asserts the closed
// digest reconciliation: a declared digest without covering evidence, an
// evidence outside the declared set, two evidences covering the identical
// digest, and an empty digest set with evidences all fail closed.
func TestValidateEvidenceSetForSnapshotDigestFailClosed(t *testing.T) {
	first, second := testEvidencePair()

	exact := testSnapshot(SnapshotStateActive, []string{first.EvidenceDigest, second.EvidenceDigest}, nil)
	if err := ValidateEvidenceSetForSnapshot(exact, []ConformanceEvidence{first}); err == nil {
		t.Fatal("a declared digest without covering evidence must fail closed")
	} else if !strings.Contains(err.Error(), "no evidence covers") {
		t.Fatalf("the error must name the missing digest, got %v", err)
	}

	declaresFirstOnly := testSnapshot(SnapshotStateActive, []string{first.EvidenceDigest}, nil)
	if err := ValidateEvidenceSetForSnapshot(declaresFirstOnly, []ConformanceEvidence{first, second}); err == nil {
		t.Fatal("an evidence outside the declared digest set must fail closed")
	} else if !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("the error must name the undeclared evidence, got %v", err)
	}

	if err := ValidateEvidenceSetForSnapshot(declaresFirstOnly, []ConformanceEvidence{first, first}); err == nil {
		t.Fatal("two evidences covering the identical digest must fail closed")
	} else if !strings.Contains(err.Error(), "more than one evidence") {
		t.Fatalf("the error must name the duplicate coverage, got %v", err)
	}

	emptyDigests := testSnapshot(SnapshotStateActive, nil, nil)
	if err := ValidateEvidenceSetForSnapshot(emptyDigests, []ConformanceEvidence{first}); err == nil {
		t.Fatal("an empty digest set requires empty evidences")
	} else if !strings.Contains(err.Error(), "requires no evidences") {
		t.Fatalf("the error must name the empty digest set, got %v", err)
	}
}

// TestValidateEvidenceSetForSnapshotAttestationSubstitution asserts that an
// attestation substitution (providerInstanceId, configDigest or
// trustRootKeyId) never validates against the snapshot.
func TestValidateEvidenceSetForSnapshotAttestationSubstitution(t *testing.T) {
	for _, substitute := range []func(*ConformanceEvidence){
		func(evidence *ConformanceEvidence) { evidence.ProviderInstanceId = "provider-instance-2" },
		func(evidence *ConformanceEvidence) { evidence.ConfigDigest = "sha256:" + strings.Repeat("11", 32) },
		func(evidence *ConformanceEvidence) { evidence.TrustRootKeyId = "trust-root-key-2" },
	} {
		substitute := substitute
		substituted := testEvidence(EvidenceStateValid, substitute)
		snapshot := testSnapshot(SnapshotStateActive, []string{substituted.EvidenceDigest}, nil)
		err := ValidateEvidenceSetForSnapshot(snapshot, []ConformanceEvidence{substituted})
		if err == nil {
			t.Fatal("an attestation substitution must fail closed")
		}
		if !strings.Contains(err.Error(), "attestation") {
			t.Fatalf("the error must name the attestation mismatch, got %v", err)
		}
	}
}

// eligibleBundle assembles a registration, snapshot and evidence set that
// must adjudicate eligible: everything active, attestations aligned, all four
// dimensions passed and the digest set reconciled exactly.
func eligibleBundle() (ProviderRegistration, ProviderCapabilitySnapshot, []ConformanceEvidence) {
	registration := testRegistration(LifecycleStateActive, nil)
	evidence := testEvidence(EvidenceStateValid, nil)
	snapshot := testSnapshot(SnapshotStateActive, []string{evidence.EvidenceDigest}, nil)
	return registration, snapshot, []ConformanceEvidence{evidence}
}

// TestEvaluateProviderEligibilityActiveBundle asserts that a fully aligned
// active bundle adjudicates eligible.
func TestEvaluateProviderEligibilityActiveBundle(t *testing.T) {
	registration, snapshot, evidences := eligibleBundle()
	if err := EvaluateProviderEligibility(registration, snapshot, evidences, testNow); err != nil {
		t.Fatalf("a fully aligned active bundle must be eligible: %v", err)
	}
}

// TestEvaluateProviderEligibilityFailClosed asserts the combined adjudication
// fails closed on a non-active registration lifecycleState, an expired
// snapshot and an evidence past its validUntil, with the error naming the
// failing stage.
func TestEvaluateProviderEligibilityFailClosed(t *testing.T) {
	registration, snapshot, evidences := eligibleBundle()

	for _, state := range []LifecycleState{LifecycleStateCreate, LifecycleStateRevoked, LifecycleStateExpired} {
		state := state
		err := EvaluateProviderEligibility(testRegistration(state, nil), snapshot, evidences, testNow)
		if err == nil {
			t.Fatalf("a %s registration must fail closed", state)
		}
		if !strings.Contains(err.Error(), "lifecycleState") {
			t.Fatalf("the error must name the lifecycleState stage, got %v", err)
		}
	}

	expiredSnapshot := testSnapshot(SnapshotStateExpired, []string{evidences[0].EvidenceDigest}, nil)
	if err := EvaluateProviderEligibility(registration, expiredSnapshot, evidences, testNow); err == nil {
		t.Fatal("an expired snapshot must fail closed in the combined adjudication")
	} else if !strings.Contains(err.Error(), "snapshotState") {
		t.Fatalf("the error must name the snapshot eligibility stage, got %v", err)
	}

	expiredEvidence := testEvidence(EvidenceStateValid, func(evidence *ConformanceEvidence) {
		evidence.ValidUntil = "2026-01-02T00:00:00Z"
	})
	snapshotForExpiredEvidence := testSnapshot(SnapshotStateActive, []string{expiredEvidence.EvidenceDigest}, nil)
	if err := EvaluateProviderEligibility(registration, snapshotForExpiredEvidence, []ConformanceEvidence{expiredEvidence}, testNow); err == nil {
		t.Fatal("an evidence past its validUntil must fail closed in the combined adjudication")
	} else if !strings.Contains(err.Error(), "validUntil") {
		t.Fatalf("the error must name the evidence eligibility stage, got %v", err)
	}
}
