package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/provider/cloudflare"
)

// NewCloudflareProvider wires the production Cloudflare Bridge provider
// composition root for one state root. Every durable piece is mandatory and
// fail closed: the file-backed state store, the durable file-backed effect
// authority sink and the real Core authority context resolver (backed by the
// Core typed-edge runtime) are all injected through the production
// constructor — a missing or invalid piece fails startup, never an
// in-memory fallback.
func NewCloudflareProvider(stateRoot, bridgeBaseURL, bridgeToken string, namespace authority.AuthorityNamespaceId, providerDomain authority.SecurityDomainId) (*cloudflare.Provider, error) {
	if strings.TrimSpace(stateRoot) == "" {
		return nil, fmt.Errorf("app: cloudflare provider: stateRoot must be a non-empty path")
	}
	edgeRuntime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		return nil, fmt.Errorf("app: cloudflare provider: Core authority runtime: %w", err)
	}
	stateStore, err := cloudflare.NewFileStateStore(filepath.Join(stateRoot, "cloudflare-state.json"))
	if err != nil {
		return nil, fmt.Errorf("app: cloudflare provider: durable state store: %w", err)
	}
	sink, err := cloudflare.NewFileEffectAuthoritySink(filepath.Join(stateRoot, "cloudflare-effects.json"))
	if err != nil {
		return nil, fmt.Errorf("app: cloudflare provider: durable effect authority sink: %w", err)
	}
	resolver := cloudflareAuthorityResolver{edgeRuntime: edgeRuntime, providerDomain: providerDomain}
	return cloudflare.NewProductionProvider(cloudflare.ProductionProviderConfig{
		ProviderConfig: cloudflare.ProviderConfig{
			BridgeBaseURL: bridgeBaseURL,
			BridgeToken:   bridgeToken,
			StateStore:    stateStore,
		},
		AuthoritySink:     sink,
		AuthorityResolver: resolver,
	})
}

// cloudflareAuthorityResolver resolves the Core authority context of the
// Cloudflare Bridge provider from the real Core typed-edge runtime: the Core
// authority namespace is the runtime issuer and the provider actor security
// domain is the execution provenance. It is the Core-backed resolver the
// production composition root binds, never a static identifier wrapper.
type cloudflareAuthorityResolver struct {
	edgeRuntime    *authority.EdgeRuntime
	providerDomain authority.SecurityDomainId
}

// Compile-time proof that the app resolver is Core-backed.
var _ cloudflare.CoreBackedAuthorityResolver = cloudflareAuthorityResolver{}

// CoreIssuer returns the Core typed-edge runtime issuer namespace the
// resolved authority context must equal.
func (resolver cloudflareAuthorityResolver) CoreIssuer() authority.AuthorityNamespaceId {
	return resolver.edgeRuntime.Issuer()
}

// ResolveAuthorityContext resolves the Core authority context, failing
// closed on an invalid namespace or provider actor domain.
func (resolver cloudflareAuthorityResolver) ResolveAuthorityContext() (cloudflare.AuthorityContext, error) {
	ctx := cloudflare.AuthorityContext{
		Namespace:              resolver.edgeRuntime.Issuer(),
		ProviderSecurityDomain: resolver.providerDomain,
	}
	if err := ctx.Validate(); err != nil {
		return cloudflare.AuthorityContext{}, err
	}
	return ctx, nil
}
