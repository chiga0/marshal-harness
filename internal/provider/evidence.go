package provider

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// EvidenceState is the closed enumeration of ConformanceEvidence states
// (ADR 0017 §2, ADR 0018 §11). Matching is case sensitive.
type EvidenceState string

// Closed states of a ConformanceEvidence record.
const (
	EvidenceStateValid   EvidenceState = "valid"
	EvidenceStateRevoked EvidenceState = "revoked"
	EvidenceStateExpired EvidenceState = "expired"
)

// Validate rejects every value outside the closed enumeration.
func (state EvidenceState) Validate() error {
	switch state {
	case EvidenceStateValid, EvidenceStateRevoked, EvidenceStateExpired:
		return nil
	default:
		return fmt.Errorf("provider: unknown evidenceState %q", string(state))
	}
}

// ConformanceDimension is the closed enumeration of the four conformance
// probe dimensions (ADR 0017 §2). Matching is case sensitive.
type ConformanceDimension string

// Closed conformance dimensions.
const (
	ConformanceDimensionMount      ConformanceDimension = "mount"
	ConformanceDimensionNetwork    ConformanceDimension = "network"
	ConformanceDimensionResource   ConformanceDimension = "resource"
	ConformanceDimensionCredential ConformanceDimension = "credential"
)

// closedDimensions enumerates exactly the four closed dimensions.
var closedDimensions = []ConformanceDimension{
	ConformanceDimensionMount,
	ConformanceDimensionNetwork,
	ConformanceDimensionResource,
	ConformanceDimensionCredential,
}

// Validate rejects every dimension outside the closed four.
func (dimension ConformanceDimension) Validate() error {
	for _, closed := range closedDimensions {
		if dimension == closed {
			return nil
		}
	}
	return fmt.Errorf("provider: unknown conformance dimension %q", string(dimension))
}

// DimensionResult is the closed enumeration of per-dimension probe results.
// Matching is case sensitive.
type DimensionResult string

// Closed per-dimension probe results.
const (
	DimensionResultPassed  DimensionResult = "passed"
	DimensionResultFailed  DimensionResult = "failed"
	DimensionResultSkipped DimensionResult = "skipped"
)

// Validate rejects every value outside the closed enumeration.
func (result DimensionResult) Validate() error {
	switch result {
	case DimensionResultPassed, DimensionResultFailed, DimensionResultSkipped:
		return nil
	default:
		return fmt.Errorf("provider: unknown dimension result %q", string(result))
	}
}

// ConformanceEvidence is the sealed conformance admission record issued by
// the Marshal Control Plane together with an independent Conformance
// Verifier (ADR 0017 §2). A provider can never self-sign conformance
// evidence: its own completed or receipt reports are adjudication input
// only. The record is an authority ledger fact owned by
// authority.AuthorityNamespaceId; the actor securityDomainId is provenance
// only. Its attestation alignment (providerInstanceId, configDigest and
// trustRootKeyId) must match the referenced registration and snapshot, so
// the identical software version under a substituted instance,
// configuration or trust root key can never reuse old evidence (ADR 0018
// §11). This task freezes the type and validation only; the issuance chain
// is a later gate.
type ConformanceEvidence struct {
	EvidenceDigest       string                                   `json:"evidenceDigest"`
	AuthorityNamespaceId authority.AuthorityNamespaceId           `json:"authorityNamespaceId"`
	SecurityDomainId     authority.SecurityDomainId               `json:"securityDomainId"`
	ProviderInstanceId   string                                   `json:"providerInstanceId"`
	ConfigDigest         string                                   `json:"configDigest"`
	TrustRootKeyId       string                                   `json:"trustRootKeyId"`
	SuiteName            string                                   `json:"suiteName"`
	ProbeArtifactDigest  string                                   `json:"probeArtifactDigest"`
	DimensionResults     map[ConformanceDimension]DimensionResult `json:"dimensionResults"`
	EvidenceState        EvidenceState                            `json:"evidenceState"`
	ProviderSelfSigned   bool                                     `json:"providerSelfSigned"`
	SignedAt             string                                   `json:"signedAt"`
	ValidUntil           string                                   `json:"validUntil"`
}

// Validate fails closed on any missing or malformed field, rejects provider
// self-signed evidence, and verifies that evidenceDigest equals the
// canonical content digest of the record.
func (evidence ConformanceEvidence) Validate() error {
	if err := evidence.validateContent(); err != nil {
		return err
	}
	computed, err := evidence.Digest()
	if err != nil {
		return err
	}
	if evidence.EvidenceDigest != computed {
		return fmt.Errorf("provider: evidenceDigest does not match the canonical content digest")
	}
	return nil
}

// Digest returns the canonical content digest of the evidence: RFC 8785 JCS
// over all content fields with evidenceDigest detached.
func (evidence ConformanceEvidence) Digest() (string, error) {
	if err := evidence.validateContent(); err != nil {
		return "", err
	}
	detached := evidence
	detached.EvidenceDigest = ""
	return canonicalDigestOf(detached)
}

// ValidateAgainstRegistration fails closed unless the evidence is aligned
// with the registration it references: identical authorityNamespaceId owner,
// identical actor securityDomainId provenance and identical attestation
// binding (providerInstanceId, configDigest and trustRootKeyId) (ADR 0018
// §5 and §11).
func (evidence ConformanceEvidence) ValidateAgainstRegistration(registration ProviderRegistration) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if err := registration.Validate(); err != nil {
		return err
	}
	if !evidence.AuthorityNamespaceId.Equal(registration.AuthorityNamespaceId) {
		return fmt.Errorf("provider: evidence authorityNamespaceId does not match the registration owner")
	}
	if !evidence.SecurityDomainId.Equal(registration.SecurityDomainId) {
		return fmt.Errorf("provider: evidence securityDomainId does not match the registration actor provenance")
	}
	return evidence.matchAttestation(registration.Attestation)
}

// ValidateAgainstSnapshot fails closed unless the evidence attestation
// (providerInstanceId, configDigest and trustRootKeyId) matches the snapshot
// it references (ADR 0018 §11).
func (evidence ConformanceEvidence) ValidateAgainstSnapshot(snapshot ProviderCapabilitySnapshot) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	return evidence.matchAttestation(snapshot.Attestation)
}

// ParseConformanceEvidence decodes a wire document into a validated
// ConformanceEvidence. The document is first canonicalized under RFC 8785
// JCS, which rejects duplicate members fail closed (ADR 0017 §11), and
// unknown members are rejected at every depth.
func ParseConformanceEvidence(raw []byte) (*ConformanceEvidence, error) {
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("provider: conformance evidence document rejected: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonicalized))
	decoder.DisallowUnknownFields()
	var evidence ConformanceEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, fmt.Errorf("provider: conformance evidence document decode: %w", err)
	}
	if err := evidence.Validate(); err != nil {
		return nil, err
	}
	return &evidence, nil
}

// matchAttestation fails closed unless the evidence carries the identical
// attestation binding as the referenced registration or snapshot.
func (evidence ConformanceEvidence) matchAttestation(attestation Attestation) error {
	if evidence.ProviderInstanceId != attestation.ProviderInstanceId {
		return fmt.Errorf("provider: evidence providerInstanceId does not match the attestation")
	}
	if evidence.ConfigDigest != attestation.ConfigDigest {
		return fmt.Errorf("provider: evidence configDigest does not match the attestation")
	}
	if evidence.TrustRootKeyId != attestation.TrustRootKeyId {
		return fmt.Errorf("provider: evidence trustRootKeyId does not match the attestation")
	}
	return nil
}

// validateContent checks every content field except the evidenceDigest
// binding itself.
func (evidence ConformanceEvidence) validateContent() error {
	if err := evidence.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := evidence.SecurityDomainId.Validate(); err != nil {
		return err
	}
	if err := requireText("providerInstanceId", evidence.ProviderInstanceId); err != nil {
		return err
	}
	if err := requireSHA256Digest("configDigest", evidence.ConfigDigest); err != nil {
		return err
	}
	if err := requireText("trustRootKeyId", evidence.TrustRootKeyId); err != nil {
		return err
	}
	if err := requireText("suiteName", evidence.SuiteName); err != nil {
		return err
	}
	if err := requireSHA256Digest("probeArtifactDigest", evidence.ProbeArtifactDigest); err != nil {
		return err
	}
	if len(evidence.DimensionResults) != len(closedDimensions) {
		return fmt.Errorf("provider: dimensionResults must cover exactly the four closed dimensions")
	}
	for _, dimension := range closedDimensions {
		result, present := evidence.DimensionResults[dimension]
		if !present {
			return fmt.Errorf("provider: dimensionResults must cover the %q dimension", string(dimension))
		}
		if err := result.Validate(); err != nil {
			return err
		}
	}
	for dimension := range evidence.DimensionResults {
		if err := dimension.Validate(); err != nil {
			return err
		}
	}
	if err := evidence.EvidenceState.Validate(); err != nil {
		return err
	}
	if evidence.ProviderSelfSigned {
		return fmt.Errorf("provider: a provider cannot self-sign conformance evidence")
	}
	if err := requireRFC3339("signedAt", evidence.SignedAt); err != nil {
		return err
	}
	if evidence.ValidUntil != "" {
		if err := requireRFC3339("validUntil", evidence.ValidUntil); err != nil {
			return err
		}
	}
	return nil
}
