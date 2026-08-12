package provider

import (
	"strings"
	"testing"
)

// legacySnapshotFixture renders a schema-conformant legacy v1alpha1
// CapabilitySnapshot document for the given adapterId as raw JSON bytes.
func legacySnapshotFixture(adapterID string) string {
	return `{
  "apiVersion": "marshal.dev/v1alpha1",
  "kind": "CapabilitySnapshot",
  "adapterId": "` + adapterID + `",
  "adapterVersion": "0.9.3",
  "executable": "/opt/legacy/bin/legacy-worker",
  "executableDigest": "sha256:` + strings.Repeat("a", 64) + `",
  "binaryVersion": "1.4.2",
  "probeStatus": "supported",
  "capabilities": {
    "structuredOutput": ["json", "text"],
    "nonInteractiveEdit": true,
    "sessionPolicies": ["ephemeral"],
    "modelSelection": false,
    "executionProfiles": ["read-only", "workspace-write"],
    "nativeBudgets": ["wall-time", "turns"],
    "processTreeCancellation": true,
    "notes": ["legacy probe note"]
  },
  "probeErrors": ["transient probe retry"],
  "probedAt": "2026-01-02T03:04:05Z"
}`
}

// TestLegacyCapabilitySnapshotRejectsForeignApiVersionAndKind covers the
// closed-admission fixtures for apiVersion and kind: a legacy document whose
// apiVersion is not exactly marshal.dev/v1alpha1 or whose kind is not
// exactly CapabilitySnapshot must be rejected with an error by both
// ParseLegacyCapabilitySnapshot and MapLegacyCapabilitySnapshot, never
// producing a partial result.
func TestLegacyCapabilitySnapshotRejectsForeignApiVersionAndKind(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"foreign apiVersion", strings.Replace(base, `"apiVersion": "marshal.dev/v1alpha1"`, `"apiVersion": "marshal.dev/v1beta1"`, 1)},
		{"foreign kind", strings.Replace(base, `"kind": "CapabilitySnapshot"`, `"kind": "ProviderCapabilitySnapshot"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestLegacyCapabilitySnapshotRejectsMissingRequiredText covers the
// required-text fixtures: a legacy document missing any of adapterId,
// adapterVersion, executable, binaryVersion or probedAt must be rejected
// with an error by both ParseLegacyCapabilitySnapshot and
// MapLegacyCapabilitySnapshot, never producing a partial result.
func TestLegacyCapabilitySnapshotRejectsMissingRequiredText(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"missing adapterId", strings.Replace(base, `  "adapterId": "adapter-alpha",`+"\n", "", 1)},
		{"missing adapterVersion", strings.Replace(base, `  "adapterVersion": "0.9.3",`+"\n", "", 1)},
		{"missing executable", strings.Replace(base, `  "executable": "/opt/legacy/bin/legacy-worker",`+"\n", "", 1)},
		{"missing binaryVersion", strings.Replace(base, `  "binaryVersion": "1.4.2",`+"\n", "", 1)},
		{"missing probedAt", strings.Replace(base, ",\n  \"probedAt\": \"2026-01-02T03:04:05Z\"", "", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestLegacyCapabilitySnapshotRejectsBlankRequiredText covers the blank-text
// fixtures: a legacy document whose adapterId, adapterVersion, executable,
// binaryVersion or probedAt is empty or whitespace-only must be rejected
// with an error by both ParseLegacyCapabilitySnapshot and
// MapLegacyCapabilitySnapshot, never producing a partial result.
func TestLegacyCapabilitySnapshotRejectsBlankRequiredText(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"blank adapterId", strings.Replace(base, `"adapterId": "adapter-alpha"`, `"adapterId": "   "`, 1)},
		{"blank adapterVersion", strings.Replace(base, `"adapterVersion": "0.9.3"`, `"adapterVersion": ""`, 1)},
		{"blank executable", strings.Replace(base, `"executable": "/opt/legacy/bin/legacy-worker"`, `"executable": "  "`, 1)},
		{"blank binaryVersion", strings.Replace(base, `"binaryVersion": "1.4.2"`, `"binaryVersion": ""`, 1)},
		{"blank probedAt", strings.Replace(base, `"probedAt": "2026-01-02T03:04:05Z"`, `"probedAt": ""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestLegacyCapabilitySnapshotRejectsUnsupportedProbeStatus covers the
// probeStatus fixture: a legacy document probed unsupported must be rejected
// with an error by both ParseLegacyCapabilitySnapshot and
// MapLegacyCapabilitySnapshot, since only supported and experimental probes
// may enter the provider model, never producing a partial result.
func TestLegacyCapabilitySnapshotRejectsUnsupportedProbeStatus(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"unsupported probeStatus", strings.Replace(base, `"probeStatus": "supported"`, `"probeStatus": "unsupported"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestLegacyCapabilitySnapshotRejectsNonRFC3339ProbedAt covers the probedAt
// format fixture: a legacy document whose probedAt does not parse as
// RFC 3339 must be rejected with an error by both
// ParseLegacyCapabilitySnapshot and MapLegacyCapabilitySnapshot, never
// producing a partial result.
func TestLegacyCapabilitySnapshotRejectsNonRFC3339ProbedAt(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"non RFC 3339 probedAt", strings.Replace(base, `"probedAt": "2026-01-02T03:04:05Z"`, `"probedAt": "2026-01-02 03:04:05"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestLegacyCapabilitySnapshotRejectsDuplicateAndUnknownMembers covers the
// structural fixtures: duplicate object members and unknown members (at the
// top level and inside capabilities) must be rejected with an error by both
// ParseLegacyCapabilitySnapshot and MapLegacyCapabilitySnapshot, never
// producing a partial result.
func TestLegacyCapabilitySnapshotRejectsDuplicateAndUnknownMembers(t *testing.T) {
	base := legacySnapshotFixture("adapter-alpha")
	cases := []struct {
		name string
		raw  string
	}{
		{"duplicate member", strings.Replace(base, `"kind": "CapabilitySnapshot",`, `"kind": "CapabilitySnapshot", "kind": "CapabilitySnapshot",`, 1)},
		{"unknown member", strings.Replace(base, `"probeStatus": "supported",`, `"probeStatus": "supported", "rogueMember": true,`, 1)},
		{"unknown capability member", strings.Replace(base, `"notes": ["legacy probe note"]`, `"notes": ["legacy probe note"], "rogueCapability": true`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("ParseLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
			if _, err := MapLegacyCapabilitySnapshot([]byte(tc.raw)); err == nil {
				t.Fatalf("MapLegacyCapabilitySnapshot accepted a legacy snapshot with %s", tc.name)
			}
		})
	}
}

// TestMapLegacyCapabilitySnapshot covers the positive fixture: a valid
// legacy snapshot maps successfully, the source digest is a sha256-prefixed
// digest, the mapped snapshot validates, conformance evidence is never
// fabricated and the scope carries the explicit legacy-mapped marker.
func TestMapLegacyCapabilitySnapshot(t *testing.T) {
	raw := []byte(legacySnapshotFixture("adapter-alpha"))
	mapping, err := MapLegacyCapabilitySnapshot(raw)
	if err != nil {
		t.Fatalf("MapLegacyCapabilitySnapshot rejected a valid legacy snapshot: %v", err)
	}

	if !strings.HasPrefix(mapping.SourceCapabilitySnapshotDigest, DigestPrefix) {
		t.Fatalf("SourceCapabilitySnapshotDigest must carry the %s prefix, got %q", DigestPrefix, mapping.SourceCapabilitySnapshotDigest)
	}
	if len(mapping.SourceCapabilitySnapshotDigest) != len(DigestPrefix)+64 {
		t.Fatalf("SourceCapabilitySnapshotDigest must be a full sha256 hex digest, got %q", mapping.SourceCapabilitySnapshotDigest)
	}

	snapshot := mapping.Snapshot
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("mapped snapshot does not validate: %v", err)
	}
	if snapshot.RegistrationId != LegacyMappingRegistrationIdPrefix+mapping.SourceCapabilitySnapshotDigest {
		t.Fatalf("registrationId must be the legacy-mapped prefix bound to the source digest, got %q", snapshot.RegistrationId)
	}
	if snapshot.ProtocolVersion != LegacyMappingProtocolVersion {
		t.Fatalf("protocolVersion must be the fixed legacy marker, got %q", snapshot.ProtocolVersion)
	}
	if snapshot.ProviderType != LegacyMappingProviderType {
		t.Fatalf("providerType must be %q, got %q", LegacyMappingProviderType, snapshot.ProviderType)
	}
	if snapshot.ProviderName != "adapter-alpha" {
		t.Fatalf("providerName must be the legacy adapterId, got %q", snapshot.ProviderName)
	}
	if snapshot.ProviderVersion != "1.4.2" {
		t.Fatalf("providerVersion must be the legacy binaryVersion, got %q", snapshot.ProviderVersion)
	}
	if len(snapshot.ConformanceEvidenceDigests) != 0 {
		t.Fatalf("legacy mapping must never fabricate conformance evidence, got %v", snapshot.ConformanceEvidenceDigests)
	}
	if snapshot.Scope != LegacyMappingScope {
		t.Fatalf("scope must be the explicit legacy-mapped marker, got %q", snapshot.Scope)
	}
	if snapshot.SnapshotState != SnapshotStateActive {
		t.Fatalf("snapshotState must be active, got %q", string(snapshot.SnapshotState))
	}
	if snapshot.CreatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("createdAt must be the legacy probedAt, got %q", snapshot.CreatedAt)
	}

	attestation := snapshot.Attestation
	if attestation.ProviderInstanceId != "legacy:adapter-alpha:1.4.2" {
		t.Fatalf("attestation.providerInstanceId must bind the legacy adapter identity, got %q", attestation.ProviderInstanceId)
	}
	if attestation.ConfigDigest != mapping.SourceCapabilitySnapshotDigest {
		t.Fatalf("attestation.configDigest must be the source capability snapshot digest, got %q", attestation.ConfigDigest)
	}
	if attestation.TrustRootKeyId != legacyTrustRootKeyId {
		t.Fatalf("attestation.trustRootKeyId must be the legacy-untrusted marker, got %q", attestation.TrustRootKeyId)
	}
	if attestation.TrustRootAlgorithm != legacyTrustRootAlgorithm {
		t.Fatalf("attestation.trustRootAlgorithm must be %q, got %q", legacyTrustRootAlgorithm, attestation.TrustRootAlgorithm)
	}

	expectedCapabilities := map[string]string{
		"structuredOutput":        "json,text",
		"nonInteractiveEdit":      "true",
		"sessionPolicies":         "ephemeral",
		"modelSelection":          "false",
		"executionProfiles":       "read-only,workspace-write",
		"nativeBudgets":           "wall-time,turns",
		"processTreeCancellation": "true",
		"notes":                   "legacy probe note",
	}
	if len(snapshot.Capabilities) != len(expectedCapabilities) {
		t.Fatalf("mapped capabilities must contain exactly the stringified legacy capabilities, got %v", snapshot.Capabilities)
	}
	for key, value := range expectedCapabilities {
		if snapshot.Capabilities[key] != value {
			t.Fatalf("capabilities[%q] must be %q, got %q", key, value, snapshot.Capabilities[key])
		}
	}
}

// TestMapLegacyCapabilitySnapshotOmittedOptionalFields covers a minimal
// legacy snapshot: only the schema-required fields are present, the optional
// keys are absent, and an experimental probe is still mappable.
func TestMapLegacyCapabilitySnapshotOmittedOptionalFields(t *testing.T) {
	raw := []byte(`{
  "apiVersion": "marshal.dev/v1alpha1",
  "kind": "CapabilitySnapshot",
  "adapterId": "adapter-minimal",
  "adapterVersion": "0.1.0",
  "executable": "/opt/legacy/bin/minimal",
  "binaryVersion": "2.0.0",
  "probeStatus": "experimental",
  "capabilities": {
    "structuredOutput": ["json"],
    "nonInteractiveEdit": false,
    "sessionPolicies": ["ephemeral"],
    "modelSelection": false,
    "executionProfiles": ["read-only"],
    "nativeBudgets": ["turns"]
  },
  "probedAt": "2026-02-03T04:05:06Z"
}`)
	mapping, err := MapLegacyCapabilitySnapshot(raw)
	if err != nil {
		t.Fatalf("MapLegacyCapabilitySnapshot rejected a minimal valid legacy snapshot: %v", err)
	}
	if err := mapping.Snapshot.Validate(); err != nil {
		t.Fatalf("mapped snapshot does not validate: %v", err)
	}
	if len(mapping.Snapshot.Capabilities) != len(legacyRequiredCapabilityKeys) {
		t.Fatalf("capabilities must contain exactly the six required keys, got %v", mapping.Snapshot.Capabilities)
	}
	for _, key := range []string{"processTreeCancellation", "notes"} {
		if _, present := mapping.Snapshot.Capabilities[key]; present {
			t.Fatalf("capabilities must not carry the absent optional key %q", key)
		}
	}
}

// TestMapLegacyCapabilitySnapshotDeterministic freezes the determinism
// guarantees: identical legacy bytes map twice to the identical snapshot
// digest, member order never changes the source digest, and a different
// adapterId produces a different digest.
func TestMapLegacyCapabilitySnapshotDeterministic(t *testing.T) {
	raw := []byte(legacySnapshotFixture("adapter-alpha"))
	first, err := MapLegacyCapabilitySnapshot(raw)
	if err != nil {
		t.Fatalf("first mapping failed: %v", err)
	}
	second, err := MapLegacyCapabilitySnapshot(raw)
	if err != nil {
		t.Fatalf("second mapping failed: %v", err)
	}
	if first.Snapshot.ProviderCapabilitySnapshotDigest != second.Snapshot.ProviderCapabilitySnapshotDigest {
		t.Fatalf("identical legacy bytes must produce the identical snapshot digest: %q != %q",
			first.Snapshot.ProviderCapabilitySnapshotDigest, second.Snapshot.ProviderCapabilitySnapshotDigest)
	}
	if first.SourceCapabilitySnapshotDigest != second.SourceCapabilitySnapshotDigest {
		t.Fatalf("identical legacy bytes must produce the identical source digest: %q != %q",
			first.SourceCapabilitySnapshotDigest, second.SourceCapabilitySnapshotDigest)
	}

	reordered := []byte(`{
  "kind": "CapabilitySnapshot",
  "probedAt": "2026-01-02T03:04:05Z",
  "apiVersion": "marshal.dev/v1alpha1",
  "adapterId": "adapter-alpha",
  "adapterVersion": "0.9.3",
  "executable": "/opt/legacy/bin/legacy-worker",
  "executableDigest": "sha256:` + strings.Repeat("a", 64) + `",
  "binaryVersion": "1.4.2",
  "probeStatus": "supported",
  "capabilities": {
    "nativeBudgets": ["wall-time", "turns"],
    "structuredOutput": ["json", "text"],
    "nonInteractiveEdit": true,
    "sessionPolicies": ["ephemeral"],
    "modelSelection": false,
    "executionProfiles": ["read-only", "workspace-write"],
    "processTreeCancellation": true,
    "notes": ["legacy probe note"]
  },
  "probeErrors": ["transient probe retry"]
}`)
	canonicalOrder, err := MapLegacyCapabilitySnapshot(reordered)
	if err != nil {
		t.Fatalf("reordered mapping failed: %v", err)
	}
	if canonicalOrder.SourceCapabilitySnapshotDigest != first.SourceCapabilitySnapshotDigest {
		t.Fatalf("member order must never change the source digest: %q != %q",
			canonicalOrder.SourceCapabilitySnapshotDigest, first.SourceCapabilitySnapshotDigest)
	}
	if canonicalOrder.Snapshot.ProviderCapabilitySnapshotDigest != first.Snapshot.ProviderCapabilitySnapshotDigest {
		t.Fatalf("member order must never change the snapshot digest: %q != %q",
			canonicalOrder.Snapshot.ProviderCapabilitySnapshotDigest, first.Snapshot.ProviderCapabilitySnapshotDigest)
	}

	other, err := MapLegacyCapabilitySnapshot([]byte(legacySnapshotFixture("adapter-beta")))
	if err != nil {
		t.Fatalf("mapping of a different adapterId failed: %v", err)
	}
	if other.SourceCapabilitySnapshotDigest == first.SourceCapabilitySnapshotDigest {
		t.Fatalf("different adapterId must produce a different source digest")
	}
	if other.Snapshot.ProviderCapabilitySnapshotDigest == first.Snapshot.ProviderCapabilitySnapshotDigest {
		t.Fatalf("different adapterId must produce a different snapshot digest")
	}
}
