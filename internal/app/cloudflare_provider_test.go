package app

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
)

// validCloudflareAuthority returns a valid Core authority context fixture
// for the Cloudflare provider composition root.
func validCloudflareAuthority() (authority.AuthorityNamespaceId, authority.SecurityDomainId) {
	return authority.AuthorityNamespaceId{
			TenantNamespace:  "cloudflare",
			ControlPlaneId:   "default",
			AuthorityScopeId: "test-scope",
		}, authority.SecurityDomainId{
			TenantNamespace:   "cloudflare",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "bridge",
		}
}

// TestNewCloudflareProviderFailsClosedOnEmptyStateRoot freezes the startup
// negative contract: an empty state root fails closed before any durable
// piece is opened.
func TestNewCloudflareProviderFailsClosedOnEmptyStateRoot(t *testing.T) {
	namespace, providerDomain := validCloudflareAuthority()
	if _, err := NewCloudflareProvider("", "https://bridge.example", "cf-bridge"+"-fixture-token-app", namespace, providerDomain); err == nil {
		t.Fatal("an empty stateRoot must fail closed at startup")
	}
}

// TestNewCloudflareProviderFailsClosedOnInvalidAuthorityContext freezes that
// an invalid Core authority namespace fails closed at startup.
func TestNewCloudflareProviderFailsClosedOnInvalidAuthorityContext(t *testing.T) {
	namespace, providerDomain := validCloudflareAuthority()
	namespace.TenantNamespace = ""
	if _, err := NewCloudflareProvider(t.TempDir(), "https://bridge.example", "cf-bridge"+"-fixture-token-app", namespace, providerDomain); err == nil {
		t.Fatal("an invalid Core namespace must fail closed at startup")
	}
}

// TestNewCloudflareProviderFailsClosedOnMissingBridgeConfig freezes that a
// missing Bridge base URL or credential fails closed through the production
// constructor.
func TestNewCloudflareProviderFailsClosedOnMissingBridgeConfig(t *testing.T) {
	namespace, providerDomain := validCloudflareAuthority()
	if _, err := NewCloudflareProvider(t.TempDir(), "", "", namespace, providerDomain); err == nil {
		t.Fatal("a missing Bridge base URL/credential must fail closed at startup")
	}
}

// TestNewCloudflareProviderWiresProductionComposition freezes the positive
// startup path: a fully wired production composition constructs the provider
// through the fail-closed production root without ever connecting to a real
// Bridge.
func TestNewCloudflareProviderWiresProductionComposition(t *testing.T) {
	namespace, providerDomain := validCloudflareAuthority()
	provider, err := NewCloudflareProvider(t.TempDir(), "https://bridge.example", "cf-bridge"+"-fixture-token-app", namespace, providerDomain)
	if err != nil {
		t.Fatalf("a valid production composition must construct: %v", err)
	}
	if provider == nil {
		t.Fatal("the constructed provider must not be nil")
	}
}
