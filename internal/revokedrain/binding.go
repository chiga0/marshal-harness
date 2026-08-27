package revokedrain

import (
	"errors"
	"fmt"
	"strings"
)

// BindingPort 是 evidence boundary 允许的封闭 Provider Port 集合。
type BindingPort string

const (
	BindingPortAgent   BindingPort = "agent"
	BindingPortSandbox BindingPort = "sandbox"
)

func (p BindingPort) valid() bool { return p == BindingPortAgent || p == BindingPortSandbox }

// BindingMaterialKind 是不得跨 Port 复用的 authority material 类型。
type BindingMaterialKind string

const (
	BindingMaterialEvidence   BindingMaterialKind = "evidence"
	BindingMaterialCredential BindingMaterialKind = "credential"
	BindingMaterialToken      BindingMaterialKind = "token"
)

func (k BindingMaterialKind) valid() bool {
	return k == BindingMaterialEvidence || k == BindingMaterialCredential || k == BindingMaterialToken
}

// BindingState 是 material、registration 与 snapshot 共享的封闭状态集合。
type BindingState string

const (
	BindingStateActive    BindingState = "active"
	BindingStateSuspended BindingState = "suspended"
	BindingStateRevoked   BindingState = "revoked"
	BindingStateReplaced  BindingState = "replaced"
	BindingStateExpired   BindingState = "expired"
)

func (s BindingState) valid() bool {
	switch s {
	case BindingStateActive, BindingStateSuspended, BindingStateRevoked, BindingStateReplaced, BindingStateExpired:
		return true
	default:
		return false
	}
}

// BindingReason 是稳定的 fail-closed 结果码。
type BindingReason string

const (
	BindingAccepted                 BindingReason = "accepted"
	BindingInvalidReference         BindingReason = "invalid-reference"
	BindingMaterialLookupFailed     BindingReason = "material-lookup-failed"
	BindingMaterialAmbiguous        BindingReason = "material-ambiguous"
	BindingMaterialInvalid          BindingReason = "material-invalid"
	BindingRegistrationLookupFailed BindingReason = "registration-lookup-failed"
	BindingRegistrationInvalid      BindingReason = "registration-invalid"
	BindingSnapshotLookupFailed     BindingReason = "snapshot-lookup-failed"
	BindingSnapshotInvalid          BindingReason = "snapshot-invalid"
	BindingInactive                 BindingReason = "inactive"
	BindingReferenceMismatch        BindingReason = "reference-mismatch"
	BindingPortMismatch             BindingReason = "port-mismatch"
	BindingSecurityDomainMismatch   BindingReason = "security-domain-mismatch"
	BindingProtocolMismatch         BindingReason = "protocol-mismatch"
	BindingAudienceMismatch         BindingReason = "audience-mismatch"
	BindingPrincipalMismatch        BindingReason = "principal-mismatch"
	BindingKindMismatch             BindingReason = "kind-mismatch"
	BindingLabelMismatch            BindingReason = "label-mismatch"
)

var (
	ErrBindingNotFound    = errors.New("revokedrain: binding authority record not found")
	ErrBindingAmbiguous   = errors.New("revokedrain: binding authority record ambiguous")
	ErrBindingUnavailable = errors.New("revokedrain: binding authority unavailable")
)

// BindingTrustedTarget 是由调用方在构造时冻结、不可由 workload 提供的目标事实。
type BindingTrustedTarget struct {
	Port               BindingPort
	Kind               BindingMaterialKind
	SecurityDomainID   string
	ProtocolFamily     string
	Audience           string
	Label              string
	RegistrationID     string
	CurrentSnapshotRef string
}

func (t BindingTrustedTarget) validate() error {
	if !t.Port.valid() || !t.Kind.valid() || blank(t.SecurityDomainID) || blank(t.ProtocolFamily) || blank(t.Audience) ||
		blank(t.Label) || blank(t.RegistrationID) || !validBindingDigest(t.CurrentSnapshotRef) {
		return errors.New("revokedrain: trusted target is incomplete")
	}
	return nil
}

// BindingMaterialRef 只包含外部呈现的引用，不承载预期权威事实。
type BindingMaterialRef struct {
	Ref string
}

func (r BindingMaterialRef) valid() bool {
	return !blank(r.Ref)
}

// BindingMaterial 是 authority ledger 中 material producer 写入的不可变事实。
type BindingMaterial struct {
	Ref              string
	Digest           string
	Kind             BindingMaterialKind
	BearerPrincipal  string
	RegistrationID   string
	SnapshotRef      string
	Port             BindingPort
	SecurityDomainID string
	ProtocolFamily   string
	Audience         string
	Label            string
	State            BindingState
}

func (m BindingMaterial) valid() bool {
	return !blank(m.Ref) && validBindingDigest(m.Digest) && m.Kind.valid() && !blank(m.BearerPrincipal) && !blank(m.RegistrationID) &&
		validBindingDigest(m.SnapshotRef) && m.Port.valid() && !blank(m.SecurityDomainID) && !blank(m.ProtocolFamily) &&
		!blank(m.Audience) && !blank(m.Label) && m.State.valid()
}

// BindingRegistration 是 current authority registration 事实。
type BindingRegistration struct {
	RegistrationID   string
	Principal        string
	Port             BindingPort
	SecurityDomainID string
	ProtocolFamily   string
	Audience         string
	State            BindingState
}

func (r BindingRegistration) valid() bool {
	return !blank(r.RegistrationID) && !blank(r.Principal) && r.Port.valid() && !blank(r.SecurityDomainID) &&
		!blank(r.ProtocolFamily) && !blank(r.Audience) && r.State.valid()
}

// BindingSnapshot 是 current authority capability snapshot 事实。
type BindingSnapshot struct {
	SnapshotRef      string
	RegistrationID   string
	Port             BindingPort
	SecurityDomainID string
	ProtocolFamily   string
	Audience         string
	State            BindingState
}

func (s BindingSnapshot) valid() bool {
	return !blank(s.SnapshotRef) && !blank(s.RegistrationID) && s.Port.valid() && !blank(s.SecurityDomainID) &&
		!blank(s.ProtocolFamily) && !blank(s.Audience) && s.State.valid()
}

// BindingResolver 每次校验都必须读取当前 authority ledger。歧义必须显式返回 ErrBindingAmbiguous。
type BindingResolver interface {
	ResolveBindingMaterial(ref string) (BindingMaterial, error)
	LookupBindingRegistration(registrationID string) (BindingRegistration, error)
	LookupBindingSnapshot(snapshotRef string) (BindingSnapshot, error)
}

// BindingResult 仅返回值副本，resolver 后续 mutation 不会改变本次判定。
type BindingResult struct {
	Accepted     bool
	Reason       BindingReason
	Material     BindingMaterial
	Registration BindingRegistration
	Snapshot     BindingSnapshot
}

type BoundaryChecker struct {
	target   BindingTrustedTarget
	resolver BindingResolver
}

func NewBoundaryChecker(target BindingTrustedTarget, resolver BindingResolver) (*BoundaryChecker, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	if resolver == nil {
		return nil, errors.New("revokedrain: binding resolver is nil")
	}
	return &BoundaryChecker{target: target, resolver: resolver}, nil
}

// Validate 按 material→registration→snapshot→trusted-target 顺序做新鲜、完整、fail-closed 校验。
func (c *BoundaryChecker) Validate(ref BindingMaterialRef) BindingResult {
	if c == nil || c.resolver == nil || !ref.valid() {
		return bindingReject(BindingInvalidReference)
	}
	m, err := c.resolver.ResolveBindingMaterial(ref.Ref)
	if err != nil {
		if errors.Is(err, ErrBindingAmbiguous) {
			return bindingReject(BindingMaterialAmbiguous)
		}
		return bindingReject(BindingMaterialLookupFailed)
	}
	if !m.valid() {
		return bindingReject(BindingMaterialInvalid)
	}
	if m.State != BindingStateActive {
		return bindingRejectWith(BindingInactive, m, BindingRegistration{}, BindingSnapshot{})
	}
	if m.Ref != ref.Ref {
		return bindingRejectWith(BindingReferenceMismatch, m, BindingRegistration{}, BindingSnapshot{})
	}
	if m.Kind != c.target.Kind {
		return bindingRejectWith(BindingKindMismatch, m, BindingRegistration{}, BindingSnapshot{})
	}
	if m.Label != c.target.Label {
		return bindingRejectWith(BindingLabelMismatch, m, BindingRegistration{}, BindingSnapshot{})
	}

	reg, err := c.resolver.LookupBindingRegistration(m.RegistrationID)
	if err != nil {
		return bindingRejectWith(BindingRegistrationLookupFailed, m, reg, BindingSnapshot{})
	}
	if !reg.valid() {
		return bindingRejectWith(BindingRegistrationInvalid, m, reg, BindingSnapshot{})
	}
	if reg.State != BindingStateActive {
		return bindingRejectWith(BindingInactive, m, reg, BindingSnapshot{})
	}
	if reg.RegistrationID != m.RegistrationID || reg.RegistrationID != c.target.RegistrationID {
		return bindingRejectWith(BindingReferenceMismatch, m, reg, BindingSnapshot{})
	}
	if reg.Principal != m.BearerPrincipal {
		return bindingRejectWith(BindingPrincipalMismatch, m, reg, BindingSnapshot{})
	}

	snapshot, err := c.resolver.LookupBindingSnapshot(m.SnapshotRef)
	if err != nil {
		return bindingRejectWith(BindingSnapshotLookupFailed, m, reg, snapshot)
	}
	if !snapshot.valid() {
		return bindingRejectWith(BindingSnapshotInvalid, m, reg, snapshot)
	}
	if snapshot.State != BindingStateActive {
		return bindingRejectWith(BindingInactive, m, reg, snapshot)
	}
	if snapshot.SnapshotRef != m.SnapshotRef || snapshot.SnapshotRef != c.target.CurrentSnapshotRef ||
		snapshot.RegistrationID != reg.RegistrationID {
		return bindingRejectWith(BindingReferenceMismatch, m, reg, snapshot)
	}

	for _, record := range []struct {
		port     BindingPort
		domain   string
		protocol string
		audience string
	}{
		{m.Port, m.SecurityDomainID, m.ProtocolFamily, m.Audience},
		{reg.Port, reg.SecurityDomainID, reg.ProtocolFamily, reg.Audience},
		{snapshot.Port, snapshot.SecurityDomainID, snapshot.ProtocolFamily, snapshot.Audience},
	} {
		if record.port != c.target.Port {
			return bindingRejectWith(BindingPortMismatch, m, reg, snapshot)
		}
		if record.domain != c.target.SecurityDomainID {
			return bindingRejectWith(BindingSecurityDomainMismatch, m, reg, snapshot)
		}
		if record.protocol != c.target.ProtocolFamily {
			return bindingRejectWith(BindingProtocolMismatch, m, reg, snapshot)
		}
		if record.audience != c.target.Audience {
			return bindingRejectWith(BindingAudienceMismatch, m, reg, snapshot)
		}
	}
	return BindingResult{Accepted: true, Reason: BindingAccepted, Material: m, Registration: reg, Snapshot: snapshot}
}

func bindingReject(reason BindingReason) BindingResult { return BindingResult{Reason: reason} }

func bindingRejectWith(reason BindingReason, m BindingMaterial, r BindingRegistration, s BindingSnapshot) BindingResult {
	return BindingResult{Reason: reason, Material: m, Registration: r, Snapshot: s}
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

func validBindingDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, r := range value[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (r BindingResult) String() string {
	return fmt.Sprintf("accepted=%t reason=%s", r.Accepted, r.Reason)
}
