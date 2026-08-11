package domain

import (
	"fmt"
	"strings"
)

// TrustDomainKind is the sealed enumeration of the three Provider trust
// domains frozen by ADR 0018 §2/§10: execution, publication and
// data-capability. It is the trustDomainKind component of a SecurityDomainID
// and never participates in AuthorityNamespaceID partitioning. Empty,
// unknown, case-mangled or substituted values fail closed via
// ParseTrustDomainKind.
type TrustDomainKind string

const (
	TrustDomainKindExecution      TrustDomainKind = "execution"
	TrustDomainKindPublication    TrustDomainKind = "publication"
	TrustDomainKindDataCapability TrustDomainKind = "data-capability"
)

// TrustDomainKinds returns every TrustDomainKind in stable order. The
// returned slice is a freshly allocated defensive copy; mutating it cannot
// alter any package state.
func TrustDomainKinds() []TrustDomainKind {
	return []TrustDomainKind{
		TrustDomainKindExecution,
		TrustDomainKindPublication,
		TrustDomainKindDataCapability,
	}
}

// ParseTrustDomainKind fails closed on empty, unknown or case-mangled
// values. Only the three canonical wire strings are accepted; "default",
// "control-plane", "sandbox", "artifact", "data_capability" and any case
// variant of the canonical kinds are rejected. It consults no package
// state.
func ParseTrustDomainKind(value string) (TrustDomainKind, error) {
	switch TrustDomainKind(value) {
	case TrustDomainKindExecution:
		return TrustDomainKindExecution, nil
	case TrustDomainKindPublication:
		return TrustDomainKindPublication, nil
	case TrustDomainKindDataCapability:
		return TrustDomainKindDataCapability, nil
	default:
		return "", fmt.Errorf("unknown trust domain kind %q", value)
	}
}

// validateIdentityComponent reports whether a composite-identity component
// is non-empty and not pure whitespace. It deliberately performs no
// charset, length, default or canonicalisation handling, per ADR 0018 §10.
func validateIdentityComponent(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("identity component %s must be non-empty and not pure whitespace", name)
	}
	return nil
}

// AuthorityNamespaceID is the composite authority namespace frozen by ADR
// 0018 §10: (tenantNamespace, controlPlaneId, authorityScopeId). It owns
// Control Plane authority objects — Project/Goal, TaskSubmission,
// Task/Run/Attempt lifecycle, DispatchLease/Allocation, ReviewDecision,
// Outcome, SideEffectIntent/Receipt reconcile, typed edge records,
// Evidence graph, the event ledger, publication decisions, idempotency/
// outbox/audit records and SSE authority sequences. Only Control Plane Core
// may write objects owned by an AuthorityNamespaceID. It is not a Provider
// trustDomainKind dimension and never participates in Provider actor
// partitioning, so it cannot be interchanged with SecurityDomainID.
type AuthorityNamespaceID struct {
	TenantNamespace  string `json:"tenantNamespace"`
	ControlPlaneID   string `json:"controlPlaneId"`
	AuthorityScopeID string `json:"authorityScopeId"`
}

// NewAuthorityNamespaceID constructs an AuthorityNamespaceID after
// validating each component is non-empty and not pure whitespace. It never
// auto-fills a default for an empty component.
func NewAuthorityNamespaceID(tenantNamespace, controlPlaneID, authorityScopeID string) (AuthorityNamespaceID, error) {
	if err := validateIdentityComponent("tenantNamespace", tenantNamespace); err != nil {
		return AuthorityNamespaceID{}, err
	}
	if err := validateIdentityComponent("controlPlaneId", controlPlaneID); err != nil {
		return AuthorityNamespaceID{}, err
	}
	if err := validateIdentityComponent("authorityScopeId", authorityScopeID); err != nil {
		return AuthorityNamespaceID{}, err
	}
	return AuthorityNamespaceID{TenantNamespace: tenantNamespace, ControlPlaneID: controlPlaneID, AuthorityScopeID: authorityScopeID}, nil
}

// Validate reports whether an AuthorityNamespaceID has every component
// non-empty and not pure whitespace. A zero value fails closed.
func (a AuthorityNamespaceID) Validate() error {
	if err := validateIdentityComponent("tenantNamespace", a.TenantNamespace); err != nil {
		return err
	}
	if err := validateIdentityComponent("controlPlaneId", a.ControlPlaneID); err != nil {
		return err
	}
	if err := validateIdentityComponent("authorityScopeId", a.AuthorityScopeID); err != nil {
		return err
	}
	return nil
}

// SecurityDomainID is the composite security namespace frozen by ADR 0018
// §10: (tenantNamespace, trustDomainKind, isolationDomainId). It identifies
// only a Provider actor; it carries no Control Plane authority and is not
// owned by AuthorityNamespaceID objects. An identical SecurityDomainID
// triple is only an actor provenance/partition condition and never
// constitutes authorization or a same-domain bearer grant. It is a distinct
// Go type from AuthorityNamespaceID and cannot be interchanged with it.
type SecurityDomainID struct {
	TenantNamespace   string          `json:"tenantNamespace"`
	TrustDomainKind   TrustDomainKind `json:"trustDomainKind"`
	IsolationDomainID string          `json:"isolationDomainId"`
}

// NewSecurityDomainID constructs a SecurityDomainID after parsing the trust
// domain kind and validating the string components. It never auto-fills a
// default for an empty component.
func NewSecurityDomainID(tenantNamespace string, trustDomainKind TrustDomainKind, isolationDomainID string) (SecurityDomainID, error) {
	kind, err := ParseTrustDomainKind(string(trustDomainKind))
	if err != nil {
		return SecurityDomainID{}, err
	}
	if err := validateIdentityComponent("tenantNamespace", tenantNamespace); err != nil {
		return SecurityDomainID{}, err
	}
	if err := validateIdentityComponent("isolationDomainId", isolationDomainID); err != nil {
		return SecurityDomainID{}, err
	}
	return SecurityDomainID{TenantNamespace: tenantNamespace, TrustDomainKind: kind, IsolationDomainID: isolationDomainID}, nil
}

// Validate reports whether a SecurityDomainID has a valid trustDomainKind
// and every string component non-empty and not pure whitespace. A zero
// value fails closed.
func (s SecurityDomainID) Validate() error {
	if _, err := ParseTrustDomainKind(string(s.TrustDomainKind)); err != nil {
		return err
	}
	if err := validateIdentityComponent("tenantNamespace", s.TenantNamespace); err != nil {
		return err
	}
	if err := validateIdentityComponent("isolationDomainId", s.IsolationDomainID); err != nil {
		return err
	}
	return nil
}
