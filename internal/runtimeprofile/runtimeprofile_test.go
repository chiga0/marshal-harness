package runtimeprofile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ── test fixtures ─────────────────────────────────────────────────────────────

const (
	validRegistrationID        = "registration:aabbccdd00112233445566778899aabbccdd00112233445566778899aabbccdd"
	validRegistrationID2       = "registration:bbccdd00112233445566778899aabbccdd00112233445566778899aabbccdd00"
	validSandboxRegistrationID = "registration:ccdd00112233445566778899aabbccdd00112233445566778899aabbccdd0011"
	validSnapshotDigest        = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	validSnapshotDigest2       = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	validCompatDigest          = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	validCompatDigest2         = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
)

func makeAgentBinding(t *testing.T) AgentBinding {
	t.Helper()
	ab, err := NewAgentBinding(validRegistrationID, validSnapshotDigest, "providerA", "1.0.0", "v1")
	if err != nil {
		t.Fatalf("makeAgentBinding: %v", err)
	}
	return ab
}

func makeSandboxBinding(t *testing.T) SandboxBinding {
	t.Helper()
	sb, err := NewSandboxBinding(validSandboxRegistrationID, "alloc-001", 1)
	if err != nil {
		t.Fatalf("makeSandboxBinding: %v", err)
	}
	return sb
}

func makeProfile(t *testing.T) WorkerRuntimeProfile {
	t.Helper()
	p, err := NewProfile(makeAgentBinding(t), makeSandboxBinding(t), validCompatDigest)
	if err != nil {
		t.Fatalf("makeProfile: %v", err)
	}
	return p
}

// ── AgentBinding tests ────────────────────────────────────────────────────────

func TestNewAgentBinding_HappyPath(t *testing.T) {
	ab, err := NewAgentBinding(validRegistrationID, validSnapshotDigest, "agentX", "2.0.0", "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ab.AgentBindingDigest == "" {
		t.Fatal("AgentBindingDigest must not be empty")
	}
	if !strings.HasPrefix(ab.AgentBindingDigest, "sha256:") {
		t.Fatalf("AgentBindingDigest must have sha256: prefix, got %q", ab.AgentBindingDigest)
	}
}

func TestAgentBinding_Validate_FailClosed(t *testing.T) {
	validDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validAB, _ := NewAgentBinding(validRegistrationID, validSnapshotDigest, "p", "1", "v1")

	tests := []struct {
		name    string
		mutate  func(AgentBinding) AgentBinding
		wantSub string
	}{
		{
			name:    "empty RegistrationID",
			mutate:  func(a AgentBinding) AgentBinding { a.RegistrationID = ""; return a },
			wantSub: "RegistrationID",
		},
		{
			name:    "missing registration: prefix",
			mutate:  func(a AgentBinding) AgentBinding { a.RegistrationID = "notregistration:abc"; return a },
			wantSub: "registration: prefix",
		},
		{
			name:    "empty SnapshotDigest",
			mutate:  func(a AgentBinding) AgentBinding { a.SnapshotDigest = ""; return a },
			wantSub: "SnapshotDigest",
		},
		{
			name:    "malformed SnapshotDigest no prefix",
			mutate:  func(a AgentBinding) AgentBinding { a.SnapshotDigest = "notsha256"; return a },
			wantSub: "sha256: prefix",
		},
		{
			name:    "SnapshotDigest wrong hex length",
			mutate:  func(a AgentBinding) AgentBinding { a.SnapshotDigest = "sha256:abc123"; return a },
			wantSub: "64-character",
		},
		{
			name:    "empty ProviderName",
			mutate:  func(a AgentBinding) AgentBinding { a.ProviderName = ""; return a },
			wantSub: "ProviderName",
		},
		{
			name:    "empty ProviderVersion",
			mutate:  func(a AgentBinding) AgentBinding { a.ProviderVersion = ""; return a },
			wantSub: "ProviderVersion",
		},
		{
			name:    "empty ProtocolVersion",
			mutate:  func(a AgentBinding) AgentBinding { a.ProtocolVersion = ""; return a },
			wantSub: "ProtocolVersion",
		},
		{
			name:    "empty AgentBindingDigest",
			mutate:  func(a AgentBinding) AgentBinding { a.AgentBindingDigest = ""; return a },
			wantSub: "AgentBindingDigest",
		},
		{
			name:    "malformed AgentBindingDigest",
			mutate:  func(a AgentBinding) AgentBinding { a.AgentBindingDigest = "sha256:short"; return a },
			wantSub: "64-character",
		},
	}
	_ = validDigest
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.mutate(validAB)
			err := bad.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "runtimeprofile:") {
				t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestNewAgentBinding_FailClosed(t *testing.T) {
	tests := []struct {
		name            string
		registrationID  string
		snapshotDigest  string
		providerName    string
		providerVersion string
		protocolVersion string
		wantSub         string
	}{
		{"empty registrationID", "", validSnapshotDigest, "p", "1", "v1", "RegistrationID"},
		{"no prefix", "noprefix:abc", validSnapshotDigest, "p", "1", "v1", "registration: prefix"},
		{"empty snapshotDigest", validRegistrationID, "", "p", "1", "v1", "SnapshotDigest"},
		{"bad snapshotDigest", validRegistrationID, "sha256:short", "p", "1", "v1", "64-character"},
		{"empty providerName", validRegistrationID, validSnapshotDigest, "", "1", "v1", "ProviderName"},
		{"empty providerVersion", validRegistrationID, validSnapshotDigest, "p", "", "v1", "ProviderVersion"},
		{"empty protocolVersion", validRegistrationID, validSnapshotDigest, "p", "1", "", "ProtocolVersion"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAgentBinding(tc.registrationID, tc.snapshotDigest, tc.providerName, tc.providerVersion, tc.protocolVersion)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "runtimeprofile:") {
				t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// ── SandboxBinding tests ──────────────────────────────────────────────────────

func TestNewSandboxBinding_HappyPath(t *testing.T) {
	sb, err := NewSandboxBinding(validSandboxRegistrationID, "alloc-999", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(sb.SandboxBindingDigest, "sha256:") {
		t.Fatalf("SandboxBindingDigest must have sha256: prefix, got %q", sb.SandboxBindingDigest)
	}
}

func TestSandboxBinding_Validate_FailClosed(t *testing.T) {
	validSB, _ := NewSandboxBinding(validSandboxRegistrationID, "alloc-001", 1)
	tests := []struct {
		name    string
		mutate  func(SandboxBinding) SandboxBinding
		wantSub string
	}{
		{
			name:    "empty SandboxProviderRegistrationID",
			mutate:  func(s SandboxBinding) SandboxBinding { s.SandboxProviderRegistrationID = ""; return s },
			wantSub: "SandboxProviderRegistrationID",
		},
		{
			name:    "missing registration: prefix in SandboxProviderRegistrationID",
			mutate:  func(s SandboxBinding) SandboxBinding { s.SandboxProviderRegistrationID = "nopfx:abc"; return s },
			wantSub: "registration: prefix",
		},
		{
			name:    "empty AllocationID",
			mutate:  func(s SandboxBinding) SandboxBinding { s.AllocationID = ""; return s },
			wantSub: "AllocationID",
		},
		{
			name:    "generation zero",
			mutate:  func(s SandboxBinding) SandboxBinding { s.Generation = 0; return s },
			wantSub: "positive integer",
		},
		{
			name:    "generation negative",
			mutate:  func(s SandboxBinding) SandboxBinding { s.Generation = -1; return s },
			wantSub: "positive integer",
		},
		{
			name:    "empty SandboxBindingDigest",
			mutate:  func(s SandboxBinding) SandboxBinding { s.SandboxBindingDigest = ""; return s },
			wantSub: "SandboxBindingDigest",
		},
		{
			name:    "malformed SandboxBindingDigest",
			mutate:  func(s SandboxBinding) SandboxBinding { s.SandboxBindingDigest = "sha256:short"; return s },
			wantSub: "64-character",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.mutate(validSB)
			err := bad.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "runtimeprofile:") {
				t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestNewSandboxBinding_FailClosed(t *testing.T) {
	tests := []struct {
		name         string
		regID        string
		allocationID string
		generation   int64
		wantSub      string
	}{
		{"empty regID", "", "alloc", 1, "SandboxProviderRegistrationID"},
		{"no prefix", "nopfx:abc", "alloc", 1, "registration: prefix"},
		{"empty allocationID", validSandboxRegistrationID, "", 1, "AllocationID"},
		{"generation zero", validSandboxRegistrationID, "alloc", 0, "positive integer"},
		{"generation negative", validSandboxRegistrationID, "alloc", -3, "positive integer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSandboxBinding(tc.regID, tc.allocationID, tc.generation)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "runtimeprofile:") {
				t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// ── WorkerRuntimeProfile tests ────────────────────────────────────────────────

func TestNewProfile_HappyPath(t *testing.T) {
	ab := makeAgentBinding(t)
	sb := makeSandboxBinding(t)
	p, err := NewProfile(ab, sb, validCompatDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(p.ProfileDigest, "sha256:") {
		t.Fatalf("ProfileDigest must have sha256: prefix, got %q", p.ProfileDigest)
	}
}

func TestNewProfile_FailClosed_InvalidAgent(t *testing.T) {
	bad := AgentBinding{} // zero value
	sb := makeSandboxBinding(t)
	_, err := NewProfile(bad, sb, validCompatDigest)
	if err == nil {
		t.Fatal("expected error for invalid agent binding")
	}
	if !strings.Contains(err.Error(), "runtimeprofile:") {
		t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
	}
}

func TestNewProfile_FailClosed_InvalidSandbox(t *testing.T) {
	ab := makeAgentBinding(t)
	bad := SandboxBinding{} // zero value
	_, err := NewProfile(ab, bad, validCompatDigest)
	if err == nil {
		t.Fatal("expected error for invalid sandbox binding")
	}
	if !strings.Contains(err.Error(), "runtimeprofile:") {
		t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
	}
}

func TestNewProfile_FailClosed_BadCompatibilityDigest(t *testing.T) {
	ab := makeAgentBinding(t)
	sb := makeSandboxBinding(t)
	tests := []struct {
		name    string
		digest  string
		wantSub string
	}{
		{"empty", "", "must not be empty"},
		{"no prefix", "notsha256", "sha256: prefix"},
		{"too short", "sha256:abc", "64-character"},
		{"uppercase", "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "lowercase hex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProfile(ab, sb, tc.digest)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "runtimeprofile:") {
				t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
			}
		})
	}
}

// ── Independent replacement tests ────────────────────────────────────────────

func TestReplaceAgentBinding_SandboxDigestUnchanged(t *testing.T) {
	p := makeProfile(t)
	originalSandboxDigest := p.Sandbox.SandboxBindingDigest
	originalProfileDigest := p.ProfileDigest

	newAB, err := NewAgentBinding(validRegistrationID2, validSnapshotDigest2, "providerB", "2.0.0", "v2")
	if err != nil {
		t.Fatalf("NewAgentBinding: %v", err)
	}
	p2, err := ReplaceAgentBinding(p, newAB)
	if err != nil {
		t.Fatalf("ReplaceAgentBinding: %v", err)
	}

	// sandbox binding digest must not change
	if p2.Sandbox.SandboxBindingDigest != originalSandboxDigest {
		t.Errorf("sandbox digest changed: was %q, now %q", originalSandboxDigest, p2.Sandbox.SandboxBindingDigest)
	}
	// profile digest must change because agent changed
	if p2.ProfileDigest == originalProfileDigest {
		t.Error("ProfileDigest must change after ReplaceAgentBinding")
	}
	// original profile unaffected
	if p.ProfileDigest != originalProfileDigest {
		t.Error("original profile must not be mutated by ReplaceAgentBinding")
	}
}

func TestReplaceSandboxBinding_AgentDigestUnchanged(t *testing.T) {
	p := makeProfile(t)
	originalAgentDigest := p.Agent.AgentBindingDigest
	originalProfileDigest := p.ProfileDigest

	newSB, err := NewSandboxBinding(validSandboxRegistrationID, "alloc-002", 2)
	if err != nil {
		t.Fatalf("NewSandboxBinding: %v", err)
	}
	p2, err := ReplaceSandboxBinding(p, newSB)
	if err != nil {
		t.Fatalf("ReplaceSandboxBinding: %v", err)
	}

	// agent binding digest must not change
	if p2.Agent.AgentBindingDigest != originalAgentDigest {
		t.Errorf("agent digest changed: was %q, now %q", originalAgentDigest, p2.Agent.AgentBindingDigest)
	}
	// profile digest must change because sandbox changed
	if p2.ProfileDigest == originalProfileDigest {
		t.Error("ProfileDigest must change after ReplaceSandboxBinding")
	}
	// original profile unaffected
	if p.ProfileDigest != originalProfileDigest {
		t.Error("original profile must not be mutated by ReplaceSandboxBinding")
	}
}

func TestReplaceAgentBinding_FailClosed_InvalidAgent(t *testing.T) {
	p := makeProfile(t)
	_, err := ReplaceAgentBinding(p, AgentBinding{})
	if err == nil {
		t.Fatal("expected error for invalid agent binding")
	}
	if !strings.Contains(err.Error(), "runtimeprofile:") {
		t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
	}
}

func TestReplaceSandboxBinding_FailClosed_InvalidSandbox(t *testing.T) {
	p := makeProfile(t)
	_, err := ReplaceSandboxBinding(p, SandboxBinding{})
	if err == nil {
		t.Fatal("expected error for invalid sandbox binding")
	}
	if !strings.Contains(err.Error(), "runtimeprofile:") {
		t.Errorf("error must have runtimeprofile: prefix, got %q", err.Error())
	}
}

// ── CompatibilityCheck tests ──────────────────────────────────────────────────

func TestCompatibilityCheck_OK(t *testing.T) {
	p := makeProfile(t)
	result := CompatibilityCheck(p, p.Agent, p.Sandbox)
	if !result.OK {
		t.Errorf("expected OK, got reject reason %q", result.RejectReason)
	}
}

func TestCompatibilityCheck_AgentBindingMismatch(t *testing.T) {
	p := makeProfile(t)
	// different agent binding
	differentAB, _ := NewAgentBinding(validRegistrationID2, validSnapshotDigest2, "other", "9.9.9", "v9")
	result := CompatibilityCheck(p, differentAB, p.Sandbox)
	if result.OK {
		t.Fatal("expected rejection")
	}
	if result.RejectReason != RejectReasonAgentBindingMismatch {
		t.Errorf("want %q, got %q", RejectReasonAgentBindingMismatch, result.RejectReason)
	}
}

func TestCompatibilityCheck_SandboxBindingMismatch(t *testing.T) {
	p := makeProfile(t)
	differentSB, _ := NewSandboxBinding(validSandboxRegistrationID, "alloc-999", 99)
	result := CompatibilityCheck(p, p.Agent, differentSB)
	if result.OK {
		t.Fatal("expected rejection")
	}
	if result.RejectReason != RejectReasonSandboxBindingMismatch {
		t.Errorf("want %q, got %q", RejectReasonSandboxBindingMismatch, result.RejectReason)
	}
}

func TestCompatibilityCheck_CompatibilityMismatch(t *testing.T) {
	p := makeProfile(t)
	// tamper ProfileDigest so digest recompute won't match
	tampered := p
	tampered.ProfileDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	result := CompatibilityCheck(tampered, tampered.Agent, tampered.Sandbox)
	if result.OK {
		t.Fatal("expected rejection")
	}
	if result.RejectReason != RejectReasonCompatibilityMismatch {
		t.Errorf("want %q, got %q", RejectReasonCompatibilityMismatch, result.RejectReason)
	}
}

// ── ProfileDigest stability tests ────────────────────────────────────────────

func TestProfileDigest_Stability_SameInput(t *testing.T) {
	ab := makeAgentBinding(t)
	sb := makeSandboxBinding(t)
	p1, _ := NewProfile(ab, sb, validCompatDigest)
	p2, _ := NewProfile(ab, sb, validCompatDigest)
	if p1.ProfileDigest != p2.ProfileDigest {
		t.Errorf("same inputs must produce same ProfileDigest: %q vs %q", p1.ProfileDigest, p2.ProfileDigest)
	}
}

func TestProfileDigest_Changes_OnAgentChange(t *testing.T) {
	ab1 := makeAgentBinding(t)
	ab2, _ := NewAgentBinding(validRegistrationID2, validSnapshotDigest2, "providerB", "2.0.0", "v2")
	sb := makeSandboxBinding(t)
	p1, _ := NewProfile(ab1, sb, validCompatDigest)
	p2, _ := NewProfile(ab2, sb, validCompatDigest)
	if p1.ProfileDigest == p2.ProfileDigest {
		t.Error("ProfileDigest must differ when agent binding changes")
	}
}

func TestProfileDigest_Changes_OnSandboxChange(t *testing.T) {
	ab := makeAgentBinding(t)
	sb1, _ := NewSandboxBinding(validSandboxRegistrationID, "alloc-001", 1)
	sb2, _ := NewSandboxBinding(validSandboxRegistrationID, "alloc-002", 2)
	p1, _ := NewProfile(ab, sb1, validCompatDigest)
	p2, _ := NewProfile(ab, sb2, validCompatDigest)
	if p1.ProfileDigest == p2.ProfileDigest {
		t.Error("ProfileDigest must differ when sandbox binding changes")
	}
}

func TestProfileDigest_Changes_OnCompatibilityDigestChange(t *testing.T) {
	ab := makeAgentBinding(t)
	sb := makeSandboxBinding(t)
	p1, _ := NewProfile(ab, sb, validCompatDigest)
	p2, _ := NewProfile(ab, sb, validCompatDigest2)
	if p1.ProfileDigest == p2.ProfileDigest {
		t.Error("ProfileDigest must differ when CompatibilityDigest changes")
	}
}

// ── Credential whitelist assertion ───────────────────────────────────────────

// credentialKeywords are terms that must never appear as JSON keys in any
// serialised output from AgentBinding, SandboxBinding, or WorkerRuntimeProfile.
var credentialKeywords = []string{
	"credential", "token", "secret", "apiKey", "api_key", "password", "passwd",
	"privateKey", "private_key", "accessKey", "access_key",
}

// canonicalKeysOf returns the top-level JSON keys produced by canonical.JSON
// applied to the marshalled value. Panics on serialisation failure.
func canonicalKeysOf(t *testing.T, v interface{}) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	canonical, err := canonical.JSON(raw)
	if err != nil {
		t.Fatalf("canonical.JSON failed: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestCredentialWhitelist_AgentBinding(t *testing.T) {
	ab := makeAgentBinding(t)
	// serialize the struct directly to check its exported field set
	raw, _ := json.Marshal(ab)
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	for k := range m {
		kLow := strings.ToLower(k)
		for _, kw := range credentialKeywords {
			if strings.Contains(kLow, strings.ToLower(kw)) {
				t.Errorf("AgentBinding JSON contains credential-like key %q (matches keyword %q)", k, kw)
			}
		}
	}
}

func TestCredentialWhitelist_SandboxBinding(t *testing.T) {
	sb := makeSandboxBinding(t)
	raw, _ := json.Marshal(sb)
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	for k := range m {
		kLow := strings.ToLower(k)
		for _, kw := range credentialKeywords {
			if strings.Contains(kLow, strings.ToLower(kw)) {
				t.Errorf("SandboxBinding JSON contains credential-like key %q (matches keyword %q)", k, kw)
			}
		}
	}
}

func TestCredentialWhitelist_WorkerRuntimeProfile(t *testing.T) {
	p := makeProfile(t)
	raw, _ := json.Marshal(p)
	var top map[string]json.RawMessage
	json.Unmarshal(raw, &top)
	// flatten all keys recursively
	var allKeys []string
	var collect func(data []byte)
	collect = func(data []byte) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		for k, v := range m {
			allKeys = append(allKeys, k)
			collect(v)
		}
	}
	collect(raw)
	for _, k := range allKeys {
		kLow := strings.ToLower(k)
		for _, kw := range credentialKeywords {
			if strings.Contains(kLow, strings.ToLower(kw)) {
				t.Errorf("WorkerRuntimeProfile JSON contains credential-like key %q (matches keyword %q)", k, kw)
			}
		}
	}
}

func TestCredentialWhitelist_CanonicalKeys(t *testing.T) {
	// verify canonical serialisation of digest input shapes has no credential keys
	ab := makeAgentBinding(t)
	sb := makeSandboxBinding(t)
	p := makeProfile(t)

	agentKeys := canonicalKeysOf(t, agentBindingJSON{
		RegistrationID:  ab.RegistrationID,
		SnapshotDigest:  ab.SnapshotDigest,
		ProviderName:    ab.ProviderName,
		ProviderVersion: ab.ProviderVersion,
		ProtocolVersion: ab.ProtocolVersion,
	})
	sandboxKeys := canonicalKeysOf(t, sandboxBindingJSON{
		SandboxProviderRegistrationID: sb.SandboxProviderRegistrationID,
		AllocationID:                  sb.AllocationID,
		Generation:                    sb.Generation,
	})
	profileKeys := canonicalKeysOf(t, profileDigestInputJSON{
		AgentBindingDigest:   p.Agent.AgentBindingDigest,
		SandboxBindingDigest: p.Sandbox.SandboxBindingDigest,
		CompatibilityDigest:  p.CompatibilityDigest,
	})

	for _, keys := range [][]string{agentKeys, sandboxKeys, profileKeys} {
		for _, k := range keys {
			kLow := strings.ToLower(k)
			for _, kw := range credentialKeywords {
				if strings.Contains(kLow, strings.ToLower(kw)) {
					t.Errorf("canonical digest shape contains credential-like key %q (matches keyword %q)", k, kw)
				}
			}
		}
	}
}
