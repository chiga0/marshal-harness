//go:build darwin && arm64

package productionruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	domainpkg "github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// LocalProvisionDomain derives the Core-side provision security domain: the
// tenant follows the authority namespace, the trust kind is execution and the
// isolation domain is the frozen local host-process identifier.
func LocalProvisionDomain(namespace authority.AuthorityNamespaceId) authority.SecurityDomainId {
	return authority.SecurityDomainId{TenantNamespace: namespace.TenantNamespace, TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process"}
}

// LocalCleanupDomain is deliberately a distinct security domain object from
// the provision authority: the allocation port rejects one verifier authorizing
// both phases.
func LocalCleanupDomain(namespace authority.AuthorityNamespaceId) authority.SecurityDomainId {
	return authority.SecurityDomainId{TenantNamespace: namespace.TenantNamespace, TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process-cleanup"}
}

// LocalResultIngressDomain is the data-capability actor that receives sealed
// worker results. It is deliberately distinct from both execution actors so
// ClaimReserved issues only the closed execution→data-capability edge.
func LocalResultIngressDomain(namespace authority.AuthorityNamespaceId) authority.SecurityDomainId {
	return authority.SecurityDomainId{TenantNamespace: namespace.TenantNamespace, TrustDomainKind: authority.TrustDomainKindDataCapability, IsolationDomainId: "result-ingress"}
}

// LocalProviderAuthority constructs the stable ordinary-user Local provider
// registration and immutable workspace-write capability snapshot. The
// registration itself still has to be persisted through RegistrationStore.Put
// before it may participate in dispatch.
func LocalProviderAuthority(namespace authority.AuthorityNamespaceId, domain authority.SecurityDomainId, attestation provider.Attestation) (provider.ProviderRegistration, provider.ProviderCapabilitySnapshot, error) {
	registration := provider.ProviderRegistration{
		RegistrationId: "registration:darwin-local-host-process-v1", AuthorityNamespaceId: namespace,
		SecurityDomainId: domain, Principal: "darwin-local-host-process", ProviderType: "sandbox",
		ProviderName: "local", ProviderVersion: "v1", ProtocolVersion: "v1alpha1",
		Scope: namespace.AuthorityScopeId, IdempotencyKey: "darwin-local-host-process-v1",
		RequestDigest: canonical.DigestBytes([]byte("darwin-local-host-process-v1:" + namespace.AuthorityScopeId)),
		Attestation:   attestation, LifecycleState: provider.LifecycleStateActive, CreatedAt: "2026-08-31T00:00:00Z",
	}
	digest, err := registration.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, provider.ProviderCapabilitySnapshot{}, err
	}
	registration.RegistrationDigest = digest
	snapshot := provider.ProviderCapabilitySnapshot{
		RegistrationId: registration.RegistrationId, ProtocolVersion: registration.ProtocolVersion,
		ProviderType: registration.ProviderType, ProviderName: registration.ProviderName, ProviderVersion: registration.ProviderVersion,
		Capabilities:               map[string]string{"accessMode": string(domainpkg.AccessModeWorkspaceWrite), "minimumAssuranceLevel": string(domainpkg.AssuranceLevelWorkspaceWrite)},
		ConformanceEvidenceDigests: []string{}, Scope: registration.Scope, SnapshotState: provider.SnapshotStateActive,
		CreatedAt: "2026-08-31T00:00:00Z", Attestation: registration.Attestation,
	}
	snapshotDigest, err := snapshot.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, provider.ProviderCapabilitySnapshot{}, err
	}
	snapshot.ProviderCapabilitySnapshotDigest = snapshotDigest
	return registration, snapshot, nil
}

// LocalAttestation describes the fixed marshal image itself: the attestation
// is observed, not configured, so the config digest derives from the build
// identity and the instance from the binary path.
func LocalAttestation(fixedMarshalPath string) (provider.Attestation, error) {
	resolved, err := filepath.EvalSymlinks(fixedMarshalPath)
	if err != nil {
		return provider.Attestation{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return provider.Attestation{}, err
	}
	return provider.Attestation{
		ProviderInstanceId: "marshal-fixed-cli-" + fmt.Sprint(info.Size()),
		ConfigDigest:       canonical.DigestBytes([]byte(resolved)),
		TrustRootKeyId:     "marshal-self",
		TrustRootAlgorithm: "ed25519",
	}, nil
}
