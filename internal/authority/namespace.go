package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// DigestPrefix prefixes every canonical content digest used by authority
// key spaces and authority records.
const DigestPrefix = "sha256:"

// TrustDomainKind is the closed enumeration of provider trust domains
// (ADR 0018 §10). Matching is case sensitive.
type TrustDomainKind string

const (
	TrustDomainKindExecution      TrustDomainKind = "execution"
	TrustDomainKindPublication    TrustDomainKind = "publication"
	TrustDomainKindDataCapability TrustDomainKind = "data-capability"
)

// Validate rejects every value outside the closed enumeration.
func (kind TrustDomainKind) Validate() error {
	switch kind {
	case TrustDomainKindExecution, TrustDomainKindPublication, TrustDomainKindDataCapability:
		return nil
	default:
		return fmt.Errorf("authority: unknown trustDomainKind %q", string(kind))
	}
}

// AuthorityNamespaceId is the composite authority-side key space that owns
// every Control Plane authority object; only Core may write it (ADR 0018 §10).
type AuthorityNamespaceId struct {
	TenantNamespace  string `json:"tenantNamespace"`
	ControlPlaneId   string `json:"controlPlaneId"`
	AuthorityScopeId string `json:"authorityScopeId"`
}

// Validate fails closed on any empty or blank member of the triple.
func (id AuthorityNamespaceId) Validate() error {
	if err := requireText("authorityNamespaceId.tenantNamespace", id.TenantNamespace); err != nil {
		return err
	}
	if err := requireText("authorityNamespaceId.controlPlaneId", id.ControlPlaneId); err != nil {
		return err
	}
	return requireText("authorityNamespaceId.authorityScopeId", id.AuthorityScopeId)
}

// Canonical returns the deterministic serialization; identical triples yield
// identical bytes and digests.
func (id AuthorityNamespaceId) Canonical() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(id)
}

// Digest returns the sha256 digest of the canonical serialization.
func (id AuthorityNamespaceId) Digest() (string, error) {
	canonical, err := id.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// Equal reports whether both key spaces carry the identical triple.
func (id AuthorityNamespaceId) Equal(other AuthorityNamespaceId) bool {
	return id == other
}

// SecurityDomainId is the composite actor-side key space identifying provider
// actors; it is provenance only and never owns authority objects (ADR 0018 §10).
type SecurityDomainId struct {
	TenantNamespace   string          `json:"tenantNamespace"`
	TrustDomainKind   TrustDomainKind `json:"trustDomainKind"`
	IsolationDomainId string          `json:"isolationDomainId"`
}

// Validate fails closed on any empty or blank member and on any trust
// domain kind outside the closed enumeration.
func (id SecurityDomainId) Validate() error {
	if err := requireText("securityDomainId.tenantNamespace", id.TenantNamespace); err != nil {
		return err
	}
	if err := id.TrustDomainKind.Validate(); err != nil {
		return err
	}
	return requireText("securityDomainId.isolationDomainId", id.IsolationDomainId)
}

// Canonical returns the deterministic serialization; identical triples yield
// identical bytes and digests.
func (id SecurityDomainId) Canonical() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(id)
}

// Digest returns the sha256 digest of the canonical serialization.
func (id SecurityDomainId) Digest() (string, error) {
	canonical, err := id.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// Equal reports whether both key spaces carry the identical triple.
func (id SecurityDomainId) Equal(other SecurityDomainId) bool {
	return id == other
}

func requireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("authority: %s must be a non-empty string", field)
	}
	return nil
}

func requireDigest(field, value string) error {
	if !strings.HasPrefix(value, DigestPrefix) || len(value) == len(DigestPrefix) {
		return fmt.Errorf("authority: %s must be a non-empty %s digest", field, DigestPrefix)
	}
	return nil
}

// canonicalJSON marshals the value and re-marshals the decoded document so
// object members are ordered lexicographically by field name at every depth
// with no insignificant whitespace, in the spirit of RFC 8785 JCS.
func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("authority: canonical marshal: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("authority: canonical decode: %w", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("authority: canonical remarshal: %w", err)
	}
	return canonical, nil
}

func digestBytes(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return DigestPrefix + hex.EncodeToString(sum[:])
}
