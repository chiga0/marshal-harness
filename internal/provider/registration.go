package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// DigestPrefix prefixes every canonical content digest carried by provider
// registrations, capability snapshots and conformance evidence.
const DigestPrefix = "sha256:"

// LifecycleState is the closed enumeration of ProviderRegistration lifecycle
// states (ADR 0018 §5). Matching is case sensitive.
type LifecycleState string

// Closed lifecycle states of a ProviderRegistration.
const (
	LifecycleStateCreate  LifecycleState = "create"
	LifecycleStateActive  LifecycleState = "active"
	LifecycleStateRevoked LifecycleState = "revoked"
	LifecycleStateExpired LifecycleState = "expired"
)

// Validate rejects every value outside the closed enumeration.
func (state LifecycleState) Validate() error {
	switch state {
	case LifecycleStateCreate, LifecycleStateActive, LifecycleStateRevoked, LifecycleStateExpired:
		return nil
	default:
		return fmt.Errorf("provider: unknown lifecycleState %q", string(state))
	}
}

// IsTerminal reports whether the state is terminal for ordinary replays:
// revoked and expired registrations are never resurrected (ADR 0018 §5).
func (state LifecycleState) IsTerminal() bool {
	return state == LifecycleStateRevoked || state == LifecycleStateExpired
}

// Attestation freezes the attestation binding carried by every registration,
// snapshot and evidence record (ADR 0018 §11): the stable provider instance
// identity, the effective configuration digest and the issuing trust root.
// Any change to any member produces a new immutable snapshot or evidence
// record and forces eligibility to be re-evaluated; the identical software
// version under a new instance, configuration or trust root key can never
// reuse old snapshots or evidence.
type Attestation struct {
	ProviderInstanceId string `json:"providerInstanceId"`
	ConfigDigest       string `json:"configDigest"`
	TrustRootKeyId     string `json:"trustRootKeyId"`
	TrustRootAlgorithm string `json:"trustRootAlgorithm"`
}

// Validate fails closed on any empty or blank member and on any configDigest
// that is not a well-formed sha256 digest.
func (attestation Attestation) Validate() error {
	if err := requireText("attestation.providerInstanceId", attestation.ProviderInstanceId); err != nil {
		return err
	}
	if err := requireSHA256Digest("attestation.configDigest", attestation.ConfigDigest); err != nil {
		return err
	}
	if err := requireText("attestation.trustRootKeyId", attestation.TrustRootKeyId); err != nil {
		return err
	}
	return requireText("attestation.trustRootAlgorithm", attestation.TrustRootAlgorithm)
}

// Equal reports whether both attestations bind the identical provider
// instance, configuration and trust root.
func (attestation Attestation) Equal(other Attestation) bool {
	return attestation == other
}

// ProviderRegistration is the authority ledger record owned by
// authority.AuthorityNamespaceId for one registered provider (ADR 0018 §5).
// Only Core writes it. The record carries the actor SecurityDomainId as
// provenance only; the actor never owns the record. Its idempotency identity
// is the canonical binding of the septuple (securityDomainId, principal,
// providerType, providerName, providerVersion, protocolVersion, scope) with
// idempotencyKey and requestDigest: only the fully identical binding merges
// idempotently, the same key with a different digest is a conflict, cross
// scope or protocol repeats never modify the existing record, and revoked or
// expired registrations are never resurrected by an ordinary replay. This
// task freezes the type and validation only; durable ledger storage is a
// later gate.
type ProviderRegistration struct {
	RegistrationId       string                         `json:"registrationId"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	SecurityDomainId     authority.SecurityDomainId     `json:"securityDomainId"`
	Principal            string                         `json:"principal"`
	ProviderType         string                         `json:"providerType"`
	ProviderName         string                         `json:"providerName"`
	ProviderVersion      string                         `json:"providerVersion"`
	ProtocolVersion      string                         `json:"protocolVersion"`
	Scope                string                         `json:"scope"`
	IdempotencyKey       string                         `json:"idempotencyKey"`
	RequestDigest        string                         `json:"requestDigest"`
	Attestation          Attestation                    `json:"attestation"`
	LifecycleState       LifecycleState                 `json:"lifecycleState"`
	CreatedAt            string                         `json:"createdAt"`
	RegistrationDigest   string                         `json:"registrationDigest"`
}

// Validate fails closed on any missing or malformed field and verifies that
// registrationDigest equals the canonical content digest of the record, so a
// memory-only or tampered record without its canonical binding never
// validates.
func (registration ProviderRegistration) Validate() error {
	if err := registration.validateContent(); err != nil {
		return err
	}
	computed, err := registration.Digest()
	if err != nil {
		return err
	}
	if registration.RegistrationDigest != computed {
		return fmt.Errorf("provider: registrationDigest does not match the canonical content digest")
	}
	return nil
}

// Digest returns the canonical content digest of the registration: RFC 8785
// JCS over all content fields with registrationDigest detached. Identical
// field values always yield the identical digest, and member order in any
// transport encoding never changes it.
func (registration ProviderRegistration) Digest() (string, error) {
	if err := registration.validateContent(); err != nil {
		return "", err
	}
	detached := registration
	detached.RegistrationDigest = ""
	return canonicalDigestOf(detached)
}

// IdempotencyDigest returns the canonical digest of the full idempotency
// identity: the septuple (securityDomainId, principal, providerType,
// providerName, providerVersion, protocolVersion, scope) plus
// idempotencyKey and requestDigest (ADR 0018 §5).
func (registration ProviderRegistration) IdempotencyDigest() (string, error) {
	if err := registration.validateContent(); err != nil {
		return "", err
	}
	binding := struct {
		SecurityDomainId authority.SecurityDomainId `json:"securityDomainId"`
		Principal        string                     `json:"principal"`
		ProviderType     string                     `json:"providerType"`
		ProviderName     string                     `json:"providerName"`
		ProviderVersion  string                     `json:"providerVersion"`
		ProtocolVersion  string                     `json:"protocolVersion"`
		Scope            string                     `json:"scope"`
		IdempotencyKey   string                     `json:"idempotencyKey"`
		RequestDigest    string                     `json:"requestDigest"`
	}{
		SecurityDomainId: registration.SecurityDomainId,
		Principal:        registration.Principal,
		ProviderType:     registration.ProviderType,
		ProviderName:     registration.ProviderName,
		ProviderVersion:  registration.ProviderVersion,
		ProtocolVersion:  registration.ProtocolVersion,
		Scope:            registration.Scope,
		IdempotencyKey:   registration.IdempotencyKey,
		RequestDigest:    registration.RequestDigest,
	}
	return canonicalDigestOf(binding)
}

// ValidateReplay decides whether replay may be idempotently merged into
// registration, the existing authority record (ADR 0018 §5). Only the
// identical canonical identity merges; the same identity and idempotencyKey
// with a different requestDigest is a conflict, identity mismatches never
// merge, and revoked or expired registrations are terminal: no ordinary
// replay resurrects them or accepts a create state against them.
func (registration ProviderRegistration) ValidateReplay(replay ProviderRegistration) error {
	if err := registration.Validate(); err != nil {
		return err
	}
	if err := replay.Validate(); err != nil {
		return err
	}
	if registration.LifecycleState.IsTerminal() {
		return fmt.Errorf("provider: %s registration is terminal: an ordinary replay cannot resurrect it", string(registration.LifecycleState))
	}
	if !registration.identityEqual(replay) || registration.IdempotencyKey != replay.IdempotencyKey {
		return fmt.Errorf("provider: replay does not share the canonical idempotency identity; cross scope or protocol repeats never merge into the existing record")
	}
	if registration.RequestDigest != replay.RequestDigest {
		return fmt.Errorf("provider: idempotency conflict: identical identity and idempotencyKey with a different requestDigest")
	}
	return nil
}

// ParseProviderRegistration decodes a wire document into a validated
// ProviderRegistration. The document is first canonicalized under RFC 8785
// JCS, which rejects duplicate members fail closed (ADR 0017 §11), and
// unknown members are rejected at every depth, so a SecurityDomainId document
// can never impersonate the authorityNamespaceId owner field and an
// AuthorityNamespaceId document can never masquerade as the actor key space
// (ADR 0018 §10).
func ParseProviderRegistration(raw []byte) (*ProviderRegistration, error) {
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("provider: registration document rejected: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonicalized))
	decoder.DisallowUnknownFields()
	var registration ProviderRegistration
	if err := decoder.Decode(&registration); err != nil {
		return nil, fmt.Errorf("provider: registration document decode: %w", err)
	}
	if err := registration.Validate(); err != nil {
		return nil, err
	}
	return &registration, nil
}

// identityEqual reports whether both registrations share the canonical
// septuple identity of ADR 0018 §5.
func (registration ProviderRegistration) identityEqual(other ProviderRegistration) bool {
	return registration.SecurityDomainId.Equal(other.SecurityDomainId) &&
		registration.Principal == other.Principal &&
		registration.ProviderType == other.ProviderType &&
		registration.ProviderName == other.ProviderName &&
		registration.ProviderVersion == other.ProviderVersion &&
		registration.ProtocolVersion == other.ProtocolVersion &&
		registration.Scope == other.Scope
}

// validateContent checks every content field except the registrationDigest
// binding itself.
func (registration ProviderRegistration) validateContent() error {
	if err := requireText("registrationId", registration.RegistrationId); err != nil {
		return err
	}
	if err := registration.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := registration.SecurityDomainId.Validate(); err != nil {
		return err
	}
	textFields := []struct {
		name  string
		value string
	}{
		{"principal", registration.Principal},
		{"providerType", registration.ProviderType},
		{"providerName", registration.ProviderName},
		{"providerVersion", registration.ProviderVersion},
		{"protocolVersion", registration.ProtocolVersion},
		{"scope", registration.Scope},
		{"idempotencyKey", registration.IdempotencyKey},
	}
	for _, field := range textFields {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := requireSHA256Digest("requestDigest", registration.RequestDigest); err != nil {
		return err
	}
	if err := registration.Attestation.Validate(); err != nil {
		return err
	}
	if err := registration.LifecycleState.Validate(); err != nil {
		return err
	}
	return requireRFC3339("createdAt", registration.CreatedAt)
}

// requireText fails closed on empty or blank values.
func requireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("provider: %s must be a non-empty string", field)
	}
	return nil
}

// requireSHA256Digest fails closed unless the value is a full lowercase hex
// sha256 digest with the sha256: prefix.
func requireSHA256Digest(field, value string) error {
	if err := requireText(field, value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, DigestPrefix) {
		return fmt.Errorf("provider: %s must carry the %s digest prefix", field, DigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, DigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("provider: %s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("provider: %s must be lowercase hex", field)
		}
	}
	return nil
}

// requireRFC3339 fails closed unless the value parses as RFC 3339.
func requireRFC3339(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("provider: %s must be an RFC 3339 timestamp", field)
	}
	return nil
}

// canonicalDigestOf marshals value, canonicalizes it under RFC 8785 JCS and
// returns the sha256 digest of the canonical bytes. Member order in the
// input never changes the digest.
func canonicalDigestOf(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("provider: canonical marshal: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", fmt.Errorf("provider: canonical digest: %w", err)
	}
	return canonical.DigestBytes(canonicalized), nil
}
