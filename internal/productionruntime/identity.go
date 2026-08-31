//go:build darwin && arm64

package productionruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
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
