package provider

import (
	"strings"
	"testing"
)

// setSnapshotDigest recomputes the canonical content digest of snapshot
// after arbitrary field mutations.
func setSnapshotDigest(snapshot *ProviderCapabilitySnapshot) {
	detached := *snapshot
	detached.ProviderCapabilitySnapshotDigest = ""
	snapshot.ProviderCapabilitySnapshotDigest = mustCanonicalDigest(detached)
}

func validSnapshot(registration ProviderRegistration) ProviderCapabilitySnapshot {
	snapshot := ProviderCapabilitySnapshot{
		RegistrationId:             registration.RegistrationId,
		ProtocolVersion:            registration.ProtocolVersion,
		ProviderType:               registration.ProviderType,
		ProviderName:               registration.ProviderName,
		ProviderVersion:            registration.ProviderVersion,
		Capabilities:               map[string]string{"structuredOutput": "json", "nonInteractiveEdit": "true"},
		ConformanceEvidenceDigests: []string{},
		Scope:                      registration.Scope,
		SnapshotState:              SnapshotStateActive,
		CreatedAt:                  "2026-08-12T00:00:01Z",
		Attestation:                registration.Attestation,
	}
	setSnapshotDigest(&snapshot)
	return snapshot
}

// TestProviderCapabilitySnapshotRejectsAttestationMismatch freezes negative
// fixture (6): a snapshot whose attestation differs from the registration by
// providerInstanceId, configDigest or trustRootKeyId never validates, so the
// identical software version under a substituted instance, configuration or
// key cannot reuse the snapshot.
func TestProviderCapabilitySnapshotRejectsAttestationMismatch(t *testing.T) {
	registration := validRegistration()
	baseline := validSnapshot(registration)
	if err := baseline.ValidateAgainstRegistration(registration); err != nil {
		t.Fatalf("ValidateAgainstRegistration rejected an aligned snapshot: %v", err)
	}

	cases := []struct {
		name   string
		change func(*Attestation)
	}{
		{"different providerInstanceId", func(a *Attestation) { a.ProviderInstanceId = "provider-instance-substituted" }},
		{"different configDigest", func(a *Attestation) { a.ConfigDigest = fixedDigest("effective-config-substituted") }},
		{"different trustRootKeyId", func(a *Attestation) { a.TrustRootKeyId = "trust-root-key-substituted" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			substituted := validSnapshot(registration)
			tc.change(&substituted.Attestation)
			setSnapshotDigest(&substituted)
			if err := substituted.Validate(); err != nil {
				t.Fatalf("snapshot self-validation failed before the attestation cross-check: %v", err)
			}
			err := substituted.ValidateAgainstRegistration(registration)
			if err == nil {
				t.Fatalf("ValidateAgainstRegistration accepted a snapshot with %s", tc.name)
			}
			if !strings.Contains(err.Error(), "attestation") {
				t.Fatalf("expected an attestation mismatch rejection, got: %v", err)
			}
		})
	}
}

// TestProviderCapabilitySnapshotRejectsUnknownSnapshotState freezes negative
// fixture (7): snapshotState is a closed three-value enumeration.
func TestProviderCapabilitySnapshotRejectsUnknownSnapshotState(t *testing.T) {
	registration := validRegistration()
	for _, value := range []SnapshotState{"", "created", "ACTIVE", "active ", "supersede", "expired2"} {
		mutated := validSnapshot(registration)
		mutated.SnapshotState = value
		setSnapshotDigest(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown snapshotState %q", string(value))
		}
	}
	for _, value := range []SnapshotState{SnapshotStateActive, SnapshotStateExpired, SnapshotStateSuperseded} {
		legal := validSnapshot(registration)
		legal.SnapshotState = value
		setSnapshotDigest(&legal)
		if err := legal.Validate(); err != nil {
			t.Fatalf("Validate rejected legal snapshotState %q: %v", string(value), err)
		}
	}
}

// TestProviderCapabilitySnapshotRejectsMalformedContent guards the remaining
// fail-closed content rules: empty capabilities, malformed evidence digest
// references and duplicate evidence digests.
func TestProviderCapabilitySnapshotRejectsMalformedContent(t *testing.T) {
	registration := validRegistration()

	emptyCapabilities := validSnapshot(registration)
	emptyCapabilities.Capabilities = map[string]string{}
	setSnapshotDigest(&emptyCapabilities)
	if err := emptyCapabilities.Validate(); err == nil {
		t.Fatal("Validate accepted an empty capability set")
	}

	badEvidenceDigest := validSnapshot(registration)
	badEvidenceDigest.ConformanceEvidenceDigests = []string{"md5:0123456789"}
	setSnapshotDigest(&badEvidenceDigest)
	if err := badEvidenceDigest.Validate(); err == nil {
		t.Fatal("Validate accepted a conformance evidence digest without the sha256 form")
	}

	duplicateEvidenceDigest := validSnapshot(registration)
	evidenceDigest := fixedDigest("conformance-evidence-1")
	duplicateEvidenceDigest.ConformanceEvidenceDigests = []string{evidenceDigest, evidenceDigest}
	setSnapshotDigest(&duplicateEvidenceDigest)
	if err := duplicateEvidenceDigest.Validate(); err == nil {
		t.Fatal("Validate accepted duplicate conformance evidence digests")
	}
}

// TestProviderCapabilitySnapshotSupersedeNeverRewritesOldDigest freezes the
// immutability invariant: supersede only produces a new snapshot, the old
// record keeps its digest, and a non-active old record cannot be superseded.
func TestProviderCapabilitySnapshotSupersedeNeverRewritesOldDigest(t *testing.T) {
	registration := validRegistration()
	old := validSnapshot(registration)
	oldDigest := old.ProviderCapabilitySnapshotDigest

	next := validSnapshot(registration)
	next.Capabilities = map[string]string{"structuredOutput": "jsonl"}
	next.CreatedAt = "2026-08-12T01:00:00Z"
	setSnapshotDigest(&next)

	accepted, err := old.Supersede(next)
	if err != nil {
		t.Fatalf("Supersede rejected a lawful successor: %v", err)
	}
	if accepted.ProviderCapabilitySnapshotDigest == oldDigest {
		t.Fatal("supersede reused the old snapshot digest instead of producing a new one")
	}
	if old.ProviderCapabilitySnapshotDigest != oldDigest {
		t.Fatal("supersede mutated the old snapshot digest")
	}

	identical := old
	if _, err := old.Supersede(identical); err == nil {
		t.Fatal("Supersede accepted a successor carrying the old snapshot digest")
	}

	nonActive := old
	nonActive.SnapshotState = SnapshotStateSuperseded
	setSnapshotDigest(&nonActive)
	if _, err := nonActive.Supersede(next); err == nil {
		t.Fatal("Supersede accepted a non-active old snapshot")
	}
}
