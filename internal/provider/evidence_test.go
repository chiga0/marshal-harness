package provider

import (
	"strings"
	"testing"
)

// setEvidenceDigest recomputes the canonical content digest of evidence
// after arbitrary field mutations.
func setEvidenceDigest(evidence *ConformanceEvidence) {
	detached := *evidence
	detached.EvidenceDigest = ""
	evidence.EvidenceDigest = mustCanonicalDigest(detached)
}

func passedDimensionResults() map[ConformanceDimension]DimensionResult {
	return map[ConformanceDimension]DimensionResult{
		ConformanceDimensionMount:      DimensionResultPassed,
		ConformanceDimensionNetwork:    DimensionResultPassed,
		ConformanceDimensionResource:   DimensionResultPassed,
		ConformanceDimensionCredential: DimensionResultPassed,
	}
}

func validEvidence(registration ProviderRegistration) ConformanceEvidence {
	evidence := ConformanceEvidence{
		AuthorityNamespaceId: registration.AuthorityNamespaceId,
		SecurityDomainId:     registration.SecurityDomainId,
		ProviderInstanceId:   registration.Attestation.ProviderInstanceId,
		ConfigDigest:         registration.Attestation.ConfigDigest,
		TrustRootKeyId:       registration.Attestation.TrustRootKeyId,
		SuiteName:            "marshal-sandbox-conformance-suite",
		ProbeArtifactDigest:  fixedDigest("probe-artifact-1"),
		DimensionResults:     passedDimensionResults(),
		EvidenceState:        EvidenceStateValid,
		ProviderSelfSigned:   false,
		SignedAt:             "2026-08-12T00:00:02Z",
		ValidUntil:           "2026-09-11T00:00:02Z",
	}
	setEvidenceDigest(&evidence)
	return evidence
}

// TestConformanceEvidenceRejectsUnknownDimensionResults freezes negative
// fixture (8): dimensionResults is closed to the four dimensions and the
// three results; unknown dimensions, illegal values and missing dimensions
// all fail closed.
func TestConformanceEvidenceRejectsUnknownDimensionResults(t *testing.T) {
	registration := validRegistration()

	unknownDimension := validEvidence(registration)
	unknownDimension.DimensionResults[ConformanceDimension("filesystem")] = DimensionResultPassed
	setEvidenceDigest(&unknownDimension)
	if err := unknownDimension.Validate(); err == nil {
		t.Fatal("Validate accepted a dimension outside the closed four")
	}

	for _, value := range []DimensionResult{"", "not-tested", "PASS", "passed ", "skipped2"} {
		illegal := validEvidence(registration)
		illegal.DimensionResults[ConformanceDimensionMount] = value
		setEvidenceDigest(&illegal)
		if err := illegal.Validate(); err == nil {
			t.Fatalf("Validate accepted illegal dimension result %q", string(value))
		}
	}

	missing := validEvidence(registration)
	delete(missing.DimensionResults, ConformanceDimensionCredential)
	setEvidenceDigest(&missing)
	if err := missing.Validate(); err == nil {
		t.Fatal("Validate accepted dimensionResults missing a closed dimension")
	}

	baseline := validEvidence(registration)
	for _, dimension := range []ConformanceDimension{ConformanceDimensionMount, ConformanceDimensionNetwork, ConformanceDimensionResource, ConformanceDimensionCredential} {
		if _, present := baseline.DimensionResults[dimension]; !present {
			t.Fatalf("baseline evidence must cover the %q dimension", string(dimension))
		}
	}
}

// TestConformanceEvidenceRejectsAttestationMismatch freezes negative fixture
// (9): evidence whose attestation differs from the referenced registration
// or snapshot by providerInstanceId, configDigest or trustRootKeyId never
// validates.
func TestConformanceEvidenceRejectsAttestationMismatch(t *testing.T) {
	registration := validRegistration()
	snapshot := validSnapshot(registration)
	baseline := validEvidence(registration)
	if err := baseline.ValidateAgainstRegistration(registration); err != nil {
		t.Fatalf("ValidateAgainstRegistration rejected aligned evidence: %v", err)
	}
	if err := baseline.ValidateAgainstSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateAgainstSnapshot rejected aligned evidence: %v", err)
	}

	cases := []struct {
		name   string
		change func(*ConformanceEvidence)
	}{
		{"different providerInstanceId", func(e *ConformanceEvidence) { e.ProviderInstanceId = "provider-instance-substituted" }},
		{"different configDigest", func(e *ConformanceEvidence) { e.ConfigDigest = fixedDigest("effective-config-substituted") }},
		{"different trustRootKeyId", func(e *ConformanceEvidence) { e.TrustRootKeyId = "trust-root-key-substituted" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			substituted := validEvidence(registration)
			tc.change(&substituted)
			setEvidenceDigest(&substituted)
			if err := substituted.Validate(); err != nil {
				t.Fatalf("evidence self-validation failed before the attestation cross-check: %v", err)
			}
			if err := substituted.ValidateAgainstRegistration(registration); err == nil {
				t.Fatalf("ValidateAgainstRegistration accepted evidence with %s", tc.name)
			}
			if err := substituted.ValidateAgainstSnapshot(snapshot); err == nil {
				t.Fatalf("ValidateAgainstSnapshot accepted evidence with %s", tc.name)
			}
		})
	}

	misowned := validEvidence(registration)
	misowned.SecurityDomainId.IsolationDomainId = "isolation-other"
	setEvidenceDigest(&misowned)
	if err := misowned.ValidateAgainstRegistration(registration); err == nil {
		t.Fatal("ValidateAgainstRegistration accepted evidence with a substituted actor securityDomainId")
	}
}

// TestConformanceEvidenceRejectsProviderSelfSigned freezes negative fixture
// (10): a provider can never self-sign conformance evidence; its own
// completed or receipt reports are adjudication input only.
func TestConformanceEvidenceRejectsProviderSelfSigned(t *testing.T) {
	registration := validRegistration()
	selfSigned := validEvidence(registration)
	selfSigned.ProviderSelfSigned = true
	setEvidenceDigest(&selfSigned)
	err := selfSigned.Validate()
	if err == nil {
		t.Fatal("Validate accepted provider self-signed conformance evidence")
	}
	if !strings.Contains(err.Error(), "self-sign") {
		t.Fatalf("expected a self-signing rejection, got: %v", err)
	}
}

// TestConformanceEvidenceRejectsUnknownEvidenceState freezes negative fixture
// (11): evidenceState is a closed three-value enumeration.
func TestConformanceEvidenceRejectsUnknownEvidenceState(t *testing.T) {
	registration := validRegistration()
	for _, value := range []EvidenceState{"", "active", "VALID", "valid ", "revoke", "expired2"} {
		mutated := validEvidence(registration)
		mutated.EvidenceState = value
		setEvidenceDigest(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown evidenceState %q", string(value))
		}
	}
	for _, value := range []EvidenceState{EvidenceStateValid, EvidenceStateRevoked, EvidenceStateExpired} {
		legal := validEvidence(registration)
		legal.EvidenceState = value
		setEvidenceDigest(&legal)
		if err := legal.Validate(); err != nil {
			t.Fatalf("Validate rejected legal evidenceState %q: %v", string(value), err)
		}
	}
}

// TestConformanceEvidenceRejectsMalformedContent guards the remaining
// fail-closed content rules: required text fields, digest forms and RFC 3339
// timestamps.
func TestConformanceEvidenceRejectsMalformedContent(t *testing.T) {
	registration := validRegistration()

	cases := []struct {
		name   string
		change func(*ConformanceEvidence)
	}{
		{"empty suiteName", func(e *ConformanceEvidence) { e.SuiteName = "" }},
		{"empty probeArtifactDigest", func(e *ConformanceEvidence) { e.ProbeArtifactDigest = "" }},
		{"probeArtifactDigest without prefix", func(e *ConformanceEvidence) {
			e.ProbeArtifactDigest = strings.TrimPrefix(e.ProbeArtifactDigest, DigestPrefix)
		}},
		{"empty providerInstanceId", func(e *ConformanceEvidence) { e.ProviderInstanceId = "" }},
		{"malformed signedAt", func(e *ConformanceEvidence) { e.SignedAt = "12 August 2026" }},
		{"malformed validUntil", func(e *ConformanceEvidence) { e.ValidUntil = "tomorrow" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := validEvidence(registration)
			tc.change(&mutated)
			setEvidenceDigest(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}

	emptyValidUntil := validEvidence(registration)
	emptyValidUntil.ValidUntil = ""
	setEvidenceDigest(&emptyValidUntil)
	if err := emptyValidUntil.Validate(); err != nil {
		t.Fatalf("Validate rejected an empty optional validUntil: %v", err)
	}
}
