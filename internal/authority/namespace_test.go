package authority

import (
	"encoding/json"
	"reflect"
	"testing"
)

func validNamespace() AuthorityNamespaceId {
	return AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

func validSecurityDomain() SecurityDomainId {
	return SecurityDomainId{
		TenantNamespace:   "default",
		TrustDomainKind:   TrustDomainKindExecution,
		IsolationDomainId: "isolation-1",
	}
}

func TestAuthorityNamespaceIdRejectsEmptyFields(t *testing.T) {
	base := validNamespace()
	cases := []struct {
		name   string
		change func(*AuthorityNamespaceId)
	}{
		{"empty tenantNamespace", func(id *AuthorityNamespaceId) { id.TenantNamespace = "" }},
		{"whitespace tenantNamespace", func(id *AuthorityNamespaceId) { id.TenantNamespace = "   " }},
		{"empty controlPlaneId", func(id *AuthorityNamespaceId) { id.ControlPlaneId = "" }},
		{"whitespace controlPlaneId", func(id *AuthorityNamespaceId) { id.ControlPlaneId = "\t" }},
		{"empty authorityScopeId", func(id *AuthorityNamespaceId) { id.AuthorityScopeId = "" }},
		{"whitespace authorityScopeId", func(id *AuthorityNamespaceId) { id.AuthorityScopeId = " \n " }},
		{"zero value", func(id *AuthorityNamespaceId) { *id = AuthorityNamespaceId{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if _, err := mutated.Canonical(); err == nil {
				t.Fatalf("Canonical accepted %s", tc.name)
			}
			if _, err := mutated.Digest(); err == nil {
				t.Fatalf("Digest accepted %s", tc.name)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid namespace: %v", err)
	}
}

func TestSecurityDomainIdRejectsEmptyFields(t *testing.T) {
	base := validSecurityDomain()
	cases := []struct {
		name   string
		change func(*SecurityDomainId)
	}{
		{"empty tenantNamespace", func(id *SecurityDomainId) { id.TenantNamespace = "" }},
		{"whitespace tenantNamespace", func(id *SecurityDomainId) { id.TenantNamespace = " " }},
		{"empty trustDomainKind", func(id *SecurityDomainId) { id.TrustDomainKind = "" }},
		{"empty isolationDomainId", func(id *SecurityDomainId) { id.IsolationDomainId = "" }},
		{"whitespace isolationDomainId", func(id *SecurityDomainId) { id.IsolationDomainId = "\t " }},
		{"zero value", func(id *SecurityDomainId) { *id = SecurityDomainId{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if _, err := mutated.Canonical(); err == nil {
				t.Fatalf("Canonical accepted %s", tc.name)
			}
			if _, err := mutated.Digest(); err == nil {
				t.Fatalf("Digest accepted %s", tc.name)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid security domain: %v", err)
	}
}

func TestSecurityDomainIdRejectsUnknownTrustDomainKind(t *testing.T) {
	for _, value := range []string{
		"",
		"default",
		"Execution",
		"EXECUTION",
		"worker",
		"publisher",
		"control-plane",
		"data_capability",
		"execution ",
	} {
		if err := TrustDomainKind(value).Validate(); err == nil {
			t.Fatalf("Validate accepted unknown trustDomainKind %q", value)
		}
		rejected := SecurityDomainId{
			TenantNamespace:   "default",
			TrustDomainKind:   TrustDomainKind(value),
			IsolationDomainId: "isolation-1",
		}
		if err := rejected.Validate(); err == nil {
			t.Fatalf("SecurityDomainId.Validate accepted unknown trustDomainKind %q", value)
		}
	}
}

func TestSecurityDomainIdAcceptsThreeTrustDomainKinds(t *testing.T) {
	for _, kind := range []TrustDomainKind{
		TrustDomainKindExecution,
		TrustDomainKindPublication,
		TrustDomainKindDataCapability,
	} {
		if err := kind.Validate(); err != nil {
			t.Fatalf("Validate rejected legal trustDomainKind %q: %v", kind, err)
		}
		domain := SecurityDomainId{
			TenantNamespace:   "default",
			TrustDomainKind:   kind,
			IsolationDomainId: "isolation-1",
		}
		if err := domain.Validate(); err != nil {
			t.Fatalf("SecurityDomainId.Validate rejected legal trustDomainKind %q: %v", kind, err)
		}
		if _, err := domain.Canonical(); err != nil {
			t.Fatalf("Canonical rejected legal trustDomainKind %q: %v", kind, err)
		}
	}
}

func TestAuthorityAndSecurityKeyspacesAreDistinctTypes(t *testing.T) {
	namespaceType := reflect.TypeOf(AuthorityNamespaceId{})
	domainType := reflect.TypeOf(SecurityDomainId{})
	if namespaceType == domainType {
		t.Fatal("AuthorityNamespaceId and SecurityDomainId share the same Go type")
	}
	if namespaceType.AssignableTo(domainType) || domainType.AssignableTo(namespaceType) {
		t.Fatal("authority and security key space types are assignable to each other")
	}

	namespaceCanonical, err := validNamespace().Canonical()
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	var impersonatedDomain SecurityDomainId
	if err := json.Unmarshal(namespaceCanonical, &impersonatedDomain); err != nil {
		t.Fatalf("unmarshal namespace canonical: %v", err)
	}
	if err := impersonatedDomain.Validate(); err == nil {
		t.Fatal("SecurityDomainId.Validate accepted an AuthorityNamespaceId document")
	}

	domainCanonical, err := validSecurityDomain().Canonical()
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	var impersonatedNamespace AuthorityNamespaceId
	if err := json.Unmarshal(domainCanonical, &impersonatedNamespace); err != nil {
		t.Fatalf("unmarshal domain canonical: %v", err)
	}
	if err := impersonatedNamespace.Validate(); err == nil {
		t.Fatal("AuthorityNamespaceId.Validate accepted a SecurityDomainId document")
	}
}
