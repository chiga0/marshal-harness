package provider

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// SnapshotState is the closed enumeration of ProviderCapabilitySnapshot
// states (ADR 0018 §5). Matching is case sensitive.
type SnapshotState string

// Closed states of a ProviderCapabilitySnapshot.
const (
	SnapshotStateActive     SnapshotState = "active"
	SnapshotStateExpired    SnapshotState = "expired"
	SnapshotStateSuperseded SnapshotState = "superseded"
)

// Validate rejects every value outside the closed enumeration.
func (state SnapshotState) Validate() error {
	switch state {
	case SnapshotStateActive, SnapshotStateExpired, SnapshotStateSuperseded:
		return nil
	default:
		return fmt.Errorf("provider: unknown snapshotState %q", string(state))
	}
}

// ProviderCapabilitySnapshot is the immutable capability snapshot captured
// for a registered provider (ADR 0018 §5). Once written it is never
// rewritten: supersede only ever produces a new snapshot, the old record
// keeps its digest, and its state transition is an authority ledger fact
// outside the snapshot bytes. The snapshot references its
// ProviderRegistration and binds the identical attestation
// (providerInstanceId, configDigest and trust root); a snapshot whose
// attestation does not match the registration never validates. This task
// freezes the type and validation only; durable capture and supersede
// storage are later gates.
type ProviderCapabilitySnapshot struct {
	ProviderCapabilitySnapshotDigest string            `json:"providerCapabilitySnapshotDigest"`
	RegistrationId                   string            `json:"registrationId"`
	ProtocolVersion                  string            `json:"protocolVersion"`
	ProviderType                     string            `json:"providerType"`
	ProviderName                     string            `json:"providerName"`
	ProviderVersion                  string            `json:"providerVersion"`
	Capabilities                     map[string]string `json:"capabilities"`
	ConformanceEvidenceDigests       []string          `json:"conformanceEvidenceDigests"`
	Scope                            string            `json:"scope"`
	SnapshotState                    SnapshotState     `json:"snapshotState"`
	CreatedAt                        string            `json:"createdAt"`
	Attestation                      Attestation       `json:"attestation"`
}

// Validate fails closed on any missing or malformed field and verifies that
// providerCapabilitySnapshotDigest equals the canonical content digest of
// the snapshot, so any rewrite of the immutable bytes is detectable.
func (snapshot ProviderCapabilitySnapshot) Validate() error {
	if err := snapshot.validateContent(); err != nil {
		return err
	}
	computed, err := snapshot.Digest()
	if err != nil {
		return err
	}
	if snapshot.ProviderCapabilitySnapshotDigest != computed {
		return fmt.Errorf("provider: providerCapabilitySnapshotDigest does not match the canonical content digest")
	}
	return nil
}

// Digest returns the canonical content digest of the snapshot: RFC 8785 JCS
// over all content fields with providerCapabilitySnapshotDigest detached.
func (snapshot ProviderCapabilitySnapshot) Digest() (string, error) {
	if err := snapshot.validateContent(); err != nil {
		return "", err
	}
	detached := snapshot
	detached.ProviderCapabilitySnapshotDigest = ""
	return canonicalDigestOf(detached)
}

// ValidateAgainstRegistration fails closed unless the snapshot is fully
// aligned with the registration it references: identical registrationId,
// protocolVersion, providerType, providerName, providerVersion, scope and
// the identical attestation binding (ADR 0018 §5 and §11). A snapshot whose
// attestation differs by providerInstanceId, configDigest or trust root key
// can never validate against the registration, so the identical software
// version under a substituted instance, configuration or key cannot reuse
// the snapshot.
func (snapshot ProviderCapabilitySnapshot) ValidateAgainstRegistration(registration ProviderRegistration) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := registration.Validate(); err != nil {
		return err
	}
	if snapshot.RegistrationId != registration.RegistrationId {
		return fmt.Errorf("provider: snapshot registrationId does not reference the registration")
	}
	if snapshot.ProtocolVersion != registration.ProtocolVersion {
		return fmt.Errorf("provider: snapshot protocolVersion does not align with the registration")
	}
	if snapshot.ProviderType != registration.ProviderType {
		return fmt.Errorf("provider: snapshot providerType does not align with the registration")
	}
	if snapshot.ProviderName != registration.ProviderName {
		return fmt.Errorf("provider: snapshot providerName does not align with the registration")
	}
	if snapshot.ProviderVersion != registration.ProviderVersion {
		return fmt.Errorf("provider: snapshot providerVersion does not align with the registration")
	}
	if snapshot.Scope != registration.Scope {
		return fmt.Errorf("provider: snapshot scope does not align with the registration")
	}
	if !snapshot.Attestation.Equal(registration.Attestation) {
		return fmt.Errorf("provider: snapshot attestation does not match the registration attestation")
	}
	return nil
}

// Supersede validates that next is the lawful successor of snapshot and
// returns next. The old snapshot is never mutated: supersede only ever
// produces a new immutable snapshot with a new digest; the old record keeps
// its digest and its transition to superseded is an authority ledger fact
// (ADR 0018 §5).
func (snapshot ProviderCapabilitySnapshot) Supersede(next ProviderCapabilitySnapshot) (ProviderCapabilitySnapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return ProviderCapabilitySnapshot{}, err
	}
	if err := next.Validate(); err != nil {
		return ProviderCapabilitySnapshot{}, err
	}
	if snapshot.SnapshotState != SnapshotStateActive {
		return ProviderCapabilitySnapshot{}, fmt.Errorf("provider: only an active snapshot can be superseded")
	}
	if snapshot.RegistrationId != next.RegistrationId ||
		snapshot.ProtocolVersion != next.ProtocolVersion ||
		snapshot.ProviderType != next.ProviderType ||
		snapshot.ProviderName != next.ProviderName ||
		snapshot.ProviderVersion != next.ProviderVersion ||
		snapshot.Scope != next.Scope {
		return ProviderCapabilitySnapshot{}, fmt.Errorf("provider: superseding snapshot must carry the identical provider identity")
	}
	if !snapshot.Attestation.Equal(next.Attestation) {
		return ProviderCapabilitySnapshot{}, fmt.Errorf("provider: superseding snapshot must carry the identical attestation")
	}
	if snapshot.ProviderCapabilitySnapshotDigest == next.ProviderCapabilitySnapshotDigest {
		return ProviderCapabilitySnapshot{}, fmt.Errorf("provider: supersede must produce a new snapshot digest, never rewrite the old one")
	}
	return next, nil
}

// ParseProviderCapabilitySnapshot decodes a wire document into a validated
// ProviderCapabilitySnapshot. The document is first canonicalized under
// RFC 8785 JCS, which rejects duplicate members fail closed (ADR 0017 §11),
// and unknown members are rejected at every depth.
func ParseProviderCapabilitySnapshot(raw []byte) (*ProviderCapabilitySnapshot, error) {
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("provider: capability snapshot document rejected: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonicalized))
	decoder.DisallowUnknownFields()
	var snapshot ProviderCapabilitySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("provider: capability snapshot document decode: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// validateContent checks every content field except the
// providerCapabilitySnapshotDigest binding itself.
func (snapshot ProviderCapabilitySnapshot) validateContent() error {
	if err := requireText("registrationId", snapshot.RegistrationId); err != nil {
		return err
	}
	if err := requireText("protocolVersion", snapshot.ProtocolVersion); err != nil {
		return err
	}
	if err := requireText("providerType", snapshot.ProviderType); err != nil {
		return err
	}
	if err := requireText("providerName", snapshot.ProviderName); err != nil {
		return err
	}
	if err := requireText("providerVersion", snapshot.ProviderVersion); err != nil {
		return err
	}
	if len(snapshot.Capabilities) == 0 {
		return fmt.Errorf("provider: capabilities must be a non-empty frozen field set")
	}
	for key, value := range snapshot.Capabilities {
		if err := requireText("capabilities key", key); err != nil {
			return err
		}
		if err := requireText("capabilities value", value); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(snapshot.ConformanceEvidenceDigests))
	for index, digest := range snapshot.ConformanceEvidenceDigests {
		field := fmt.Sprintf("conformanceEvidenceDigests[%d]", index)
		if err := requireSHA256Digest(field, digest); err != nil {
			return err
		}
		if _, duplicate := seen[digest]; duplicate {
			return fmt.Errorf("provider: conformanceEvidenceDigests must be a closed set without duplicates")
		}
		seen[digest] = struct{}{}
	}
	if err := requireText("scope", snapshot.Scope); err != nil {
		return err
	}
	if err := snapshot.SnapshotState.Validate(); err != nil {
		return err
	}
	if err := requireRFC3339("createdAt", snapshot.CreatedAt); err != nil {
		return err
	}
	return snapshot.Attestation.Validate()
}
