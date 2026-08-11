package domain

import (
	"encoding/json"
	"testing"
)

func TestTrustDomainKindIsClosed(t *testing.T) {
	t.Parallel()

	// Each canonical kind's wire string parses back to itself.
	for _, kind := range TrustDomainKinds() {
		got, err := ParseTrustDomainKind(string(kind))
		if err != nil {
			t.Fatalf("ParseTrustDomainKind(%q) error = %v", kind, err)
		}
		if got != kind {
			t.Fatalf("ParseTrustDomainKind(%q) = %q, want %q", kind, got, kind)
		}
	}

	// Empty, unknown, case variants and substituted labels all fail closed.
	rejected := []string{
		"",
		"Execution",
		"EXECUTION",
		"Publication",
		"DATA-CAPABILITY",
		"data_capability",
		"default",
		"control-plane",
		"sandbox",
		"artifact",
		"secret",
		" execution",
		"publication ",
	}
	for _, in := range rejected {
		if _, err := ParseTrustDomainKind(in); err == nil {
			t.Fatalf("ParseTrustDomainKind(%q) unexpectedly succeeded", in)
		}
	}

	// TrustDomainKinds returns a defensive copy in the canonical stable order.
	kinds := TrustDomainKinds()
	wantKinds := []TrustDomainKind{TrustDomainKindExecution, TrustDomainKindPublication, TrustDomainKindDataCapability}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("TrustDomainKinds() len = %d, want %d", len(kinds), len(wantKinds))
	}
	for i, want := range wantKinds {
		if kinds[i] != want {
			t.Fatalf("TrustDomainKinds()[%d] = %q, want %q", i, kinds[i], want)
		}
	}

	// Mutating the returned slice must not affect the canonical enumeration.
	kinds[0] = TrustDomainKind("forged")
	if fresh := TrustDomainKinds(); fresh[0] != TrustDomainKindExecution {
		t.Fatalf("TrustDomainKinds() was mutated by caller: got %q", fresh[0])
	}
}

func TestAuthorityNamespaceIDValidation(t *testing.T) {
	t.Parallel()

	authority, err := NewAuthorityNamespaceID("tenant-a", "cp-1", "scope-1")
	if err != nil {
		t.Fatalf("NewAuthorityNamespaceID error = %v", err)
	}
	if err := authority.Validate(); err != nil {
		t.Fatalf("Validate error = %v", err)
	}

	// JSON round-trip preserves exact camelCase field names and values.
	data, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 3 {
		t.Fatalf("wire object = %v, want exactly three members", wire)
	}
	assertWireString(t, wire, "tenantNamespace", "tenant-a")
	assertWireString(t, wire, "controlPlaneId", "cp-1")
	assertWireString(t, wire, "authorityScopeId", "scope-1")

	var roundTrip AuthorityNamespaceID
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip != authority {
		t.Fatalf("round trip = %+v, want %+v", roundTrip, authority)
	}

	// Zero value fails closed.
	var zero AuthorityNamespaceID
	if err := zero.Validate(); err == nil {
		t.Fatal("zero AuthorityNamespaceID unexpectedly validated")
	}

	// Empty or whitespace components are rejected in every position and never
	// auto-filled with a default.
	empty := []string{"", "  \t ", "\n", "   "}
	for _, v := range empty {
		if got, err := NewAuthorityNamespaceID(v, "cp-1", "scope-1"); err == nil {
			t.Fatalf("NewAuthorityNamespaceID(%q, ...) unexpectedly produced %+v", v, got)
		}
		if got, err := NewAuthorityNamespaceID("tenant-a", v, "scope-1"); err == nil {
			t.Fatalf("NewAuthorityNamespaceID(..., %q, ...) unexpectedly produced %+v", v, got)
		}
		if got, err := NewAuthorityNamespaceID("tenant-a", "cp-1", v); err == nil {
			t.Fatalf("NewAuthorityNamespaceID(..., %q) unexpectedly produced %+v", v, got)
		}
	}

	// Explicit "default" components are accepted and preserved verbatim;
	// single-instance deployments may fix controlPlaneId to "default" (ADR
	// 0018 §10). The constructor never auto-fills a default for empty input.
	defaults, err := NewAuthorityNamespaceID("default", "default", "default")
	if err != nil {
		t.Fatalf("NewAuthorityNamespaceID(default, default, default) error = %v", err)
	}
	if defaults.TenantNamespace != "default" || defaults.ControlPlaneID != "default" || defaults.AuthorityScopeID != "default" {
		t.Fatalf("explicit default components were transformed: %+v", defaults)
	}
}

func TestSecurityDomainIDValidation(t *testing.T) {
	t.Parallel()

	// Valid construction with each of the three trust domain kinds.
	for _, kind := range TrustDomainKinds() {
		security, err := NewSecurityDomainID("tenant-a", kind, "iso-1")
		if err != nil {
			t.Fatalf("NewSecurityDomainID(%q) error = %v", kind, err)
		}
		if err := security.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", kind, err)
		}
		if security.TrustDomainKind != kind {
			t.Fatalf("TrustDomainKind = %q, want %q", security.TrustDomainKind, kind)
		}
	}

	// JSON round-trip preserves exact camelCase field names and values.
	security, err := NewSecurityDomainID("tenant-a", TrustDomainKindDataCapability, "iso-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(security)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 3 {
		t.Fatalf("wire object = %v, want exactly three members", wire)
	}
	assertWireString(t, wire, "tenantNamespace", "tenant-a")
	assertWireString(t, wire, "trustDomainKind", "data-capability")
	assertWireString(t, wire, "isolationDomainId", "iso-1")

	var roundTrip SecurityDomainID
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip != security {
		t.Fatalf("round trip = %+v, want %+v", roundTrip, security)
	}

	// Zero value fails closed.
	var zero SecurityDomainID
	if err := zero.Validate(); err == nil {
		t.Fatal("zero SecurityDomainID unexpectedly validated")
	}

	// Invalid trust domain kind fails closed, including the underscore variant.
	if _, err := NewSecurityDomainID("tenant-a", TrustDomainKind("default"), "iso-1"); err == nil {
		t.Fatal("NewSecurityDomainID accepted an invalid trust domain kind")
	}
	if _, err := NewSecurityDomainID("tenant-a", TrustDomainKind("data_capability"), "iso-1"); err == nil {
		t.Fatal("NewSecurityDomainID accepted the data_capability underscore variant")
	}

	// Empty or whitespace string components are rejected in every position.
	empty := []string{"", "  ", "\t"}
	for _, v := range empty {
		if got, err := NewSecurityDomainID(v, TrustDomainKindExecution, "iso-1"); err == nil {
			t.Fatalf("NewSecurityDomainID(%q, ...) unexpectedly produced %+v", v, got)
		}
		if got, err := NewSecurityDomainID("tenant-a", TrustDomainKindExecution, v); err == nil {
			t.Fatalf("NewSecurityDomainID(..., %q) unexpectedly produced %+v", v, got)
		}
	}
}

func TestRuntimeIdentityTypesCannotBeInterchanged(t *testing.T) {
	t.Parallel()

	authority, err := NewAuthorityNamespaceID("tenant-a", "cp-1", "scope-1")
	if err != nil {
		t.Fatal(err)
	}
	security, err := NewSecurityDomainID("tenant-a", TrustDomainKindExecution, "iso-1")
	if err != nil {
		t.Fatal(err)
	}

	// An AuthorityNamespaceID must not be reinterpretable as a SecurityDomainID.
	authBytes, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	var misreadSecurity SecurityDomainID
	if err := json.Unmarshal(authBytes, &misreadSecurity); err != nil {
		t.Fatal(err)
	}
	// JSON round-trip silently drops controlPlaneId/authorityScopeId and leaves
	// trustDomainKind/isolationDomainId zero; Validate must fail closed.
	if err := misreadSecurity.Validate(); err == nil {
		t.Fatalf("AuthorityNamespaceID reinterpreted as SecurityDomainID validated: %+v", misreadSecurity)
	}

	// A SecurityDomainID must not be reinterpretable as an AuthorityNamespaceID.
	secBytes, err := json.Marshal(security)
	if err != nil {
		t.Fatal(err)
	}
	var misreadAuthority AuthorityNamespaceID
	if err := json.Unmarshal(secBytes, &misreadAuthority); err != nil {
		t.Fatal(err)
	}
	// JSON round-trip carries over only tenantNamespace; controlPlaneId and
	// authorityScopeId stay zero; Validate must fail closed.
	if err := misreadAuthority.Validate(); err == nil {
		t.Fatalf("SecurityDomainID reinterpreted as AuthorityNamespaceID validated: %+v", misreadAuthority)
	}
}

// assertWireString fails the test if wire[key] is not the expected string.
func assertWireString(t *testing.T, wire map[string]any, key, want string) {
	t.Helper()
	got, ok := wire[key].(string)
	if !ok || got != want {
		t.Fatalf("wire member %s = %v, want %q", key, wire[key], want)
	}
}
