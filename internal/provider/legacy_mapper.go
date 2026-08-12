package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Legacy mapping markers (frozen). Every ProviderCapabilitySnapshot derived
// from a legacy v1alpha1 CapabilitySnapshot is explicitly marked with these
// values so a legacy-mapped snapshot can never be confused with a snapshot
// captured for a Core-derived registration: the registrationId prefix, the
// protocolVersion marker and the scope marker are all reserved for the
// legacy mapping path.
const (
	// LegacyMappingRegistrationIdPrefix prefixes the registrationId of every
	// legacy-mapped snapshot: legacy snapshots have no registration, so the
	// source snapshot digest is bound in instead of a real registrationId.
	LegacyMappingRegistrationIdPrefix = "legacy-mapped:"
	// LegacyMappingProtocolVersion is the fixed protocolVersion marker of
	// every legacy-mapped snapshot.
	LegacyMappingProtocolVersion = "legacy:v1alpha1"
	// LegacyMappingProviderType is the providerType assigned to every
	// legacy-mapped snapshot.
	LegacyMappingProviderType = "agent"
	// LegacyMappingScope is the explicit scope marker of every legacy-mapped
	// snapshot: the mapper never invents a real scope for legacy input.
	LegacyMappingScope = "legacy-mapped"
)

// Closed admission values of the legacy v1alpha1 CapabilitySnapshot document.
const (
	// legacyAPIVersion is the only accepted apiVersion of a legacy snapshot.
	legacyAPIVersion = "marshal.dev/v1alpha1"
	// legacySnapshotKind is the only accepted kind of a legacy snapshot.
	legacySnapshotKind = "CapabilitySnapshot"
	// legacyInstanceIdPrefix prefixes the attestation providerInstanceId of
	// every legacy-mapped snapshot.
	legacyInstanceIdPrefix = "legacy:"
	// legacyTrustRootKeyId marks that a legacy-mapped attestation has no
	// trust root key.
	legacyTrustRootKeyId = "legacy-untrusted"
	// legacyTrustRootAlgorithm marks that a legacy-mapped attestation is
	// unsigned.
	legacyTrustRootAlgorithm = "none"
)

// Closed probeStatus enumeration of the legacy v1alpha1 snapshot.
const (
	legacyProbeSupported    = "supported"
	legacyProbeExperimental = "experimental"
	legacyProbeUnsupported  = "unsupported"
)

// legacyRequiredCapabilityKeys enumerates the six capability keys that the
// legacy v1alpha1 schema requires in every CapabilitySnapshot.
var legacyRequiredCapabilityKeys = []string{
	"structuredOutput",
	"nonInteractiveEdit",
	"sessionPolicies",
	"modelSelection",
	"executionProfiles",
	"nativeBudgets",
}

// LegacyCapabilitySnapshot mirrors the legacy v1alpha1 CapabilitySnapshot
// document produced by the old Worker adapter probe
// (schemas/capability-snapshot.schema.json). It is admission input only: it
// never enters the provider authority model except through
// MapLegacyCapabilitySnapshot.
type LegacyCapabilitySnapshot struct {
	APIVersion       string             `json:"apiVersion"`
	Kind             string             `json:"kind"`
	AdapterId        string             `json:"adapterId"`
	AdapterVersion   string             `json:"adapterVersion"`
	Executable       string             `json:"executable"`
	ExecutableDigest string             `json:"executableDigest"`
	BinaryVersion    string             `json:"binaryVersion"`
	ProbeStatus      string             `json:"probeStatus"`
	Capabilities     LegacyCapabilities `json:"capabilities"`
	ProbeErrors      []string           `json:"probeErrors"`
	ProbedAt         string             `json:"probedAt"`
}

// LegacyCapabilities mirrors the legacy capabilities object. The six
// required keys must always be present; processTreeCancellation and notes
// are optional and are only carried across when present.
type LegacyCapabilities struct {
	StructuredOutput        []string `json:"structuredOutput"`
	NonInteractiveEdit      bool     `json:"nonInteractiveEdit"`
	SessionPolicies         []string `json:"sessionPolicies"`
	ModelSelection          bool     `json:"modelSelection"`
	ExecutionProfiles       []string `json:"executionProfiles"`
	NativeBudgets           []string `json:"nativeBudgets"`
	ProcessTreeCancellation *bool    `json:"processTreeCancellation"`
	Notes                   []string `json:"notes"`
}

// LegacySnapshotMapping is the result of mapping one legacy v1alpha1
// CapabilitySnapshot into the provider model. SourceCapabilitySnapshotDigest
// is the sha256 digest of the canonicalized legacy bytes and is bound into
// the mapped snapshot for traceability back to the exact legacy document.
type LegacySnapshotMapping struct {
	Snapshot                       ProviderCapabilitySnapshot
	SourceCapabilitySnapshotDigest string
}

// ParseLegacyCapabilitySnapshot decodes a legacy v1alpha1 CapabilitySnapshot
// document fail closed. The raw bytes are first canonicalized under RFC 8785
// JCS, which rejects duplicate members at every depth; unknown members are
// then rejected at every depth by the strict decoder; and the decoded fields
// are validated: apiVersion must be exactly marshal.dev/v1alpha1, kind must
// be exactly CapabilitySnapshot, the required text fields must be non-empty,
// probeStatus must be a closed enumeration value and only supported or
// experimental probes may be mapped, and probedAt must parse as RFC 3339.
func ParseLegacyCapabilitySnapshot(raw []byte) (*LegacyCapabilitySnapshot, error) {
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("provider: legacy capability snapshot rejected: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonicalized))
	decoder.DisallowUnknownFields()
	var snapshot LegacyCapabilitySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("provider: legacy capability snapshot decode: %w", err)
	}
	if err := snapshot.validate(canonicalized); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// MapLegacyCapabilitySnapshot parses a legacy v1alpha1 CapabilitySnapshot
// fail closed and derives the corresponding ProviderCapabilitySnapshot under
// the frozen derivation rules. The derivation is a pure function: identical
// legacy bytes always yield field-identical snapshots with the identical
// providerCapabilitySnapshotDigest. The mapper never fabricates conformance
// evidence and never assigns a real scope: ConformanceEvidenceDigests stays
// empty and Scope carries the explicit legacy-mapped marker.
func MapLegacyCapabilitySnapshot(raw []byte) (LegacySnapshotMapping, error) {
	legacy, err := ParseLegacyCapabilitySnapshot(raw)
	if err != nil {
		return LegacySnapshotMapping{}, err
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return LegacySnapshotMapping{}, err
	}
	sourceDigest := canonical.DigestBytes(canonicalized)
	capabilities, err := deriveLegacyCapabilities(legacy.Capabilities)
	if err != nil {
		return LegacySnapshotMapping{}, err
	}
	mapped := ProviderCapabilitySnapshot{
		RegistrationId:             LegacyMappingRegistrationIdPrefix + sourceDigest,
		ProtocolVersion:            LegacyMappingProtocolVersion,
		ProviderType:               LegacyMappingProviderType,
		ProviderName:               legacy.AdapterId,
		ProviderVersion:            legacy.BinaryVersion,
		Capabilities:               capabilities,
		ConformanceEvidenceDigests: nil,
		Scope:                      LegacyMappingScope,
		SnapshotState:              SnapshotStateActive,
		CreatedAt:                  legacy.ProbedAt,
	}
	// Legacy-mapped attestation: there is no real registration instance, so
	// the instance identity is synthesized from the legacy adapter identity
	// and the configDigest is bound to the real digest of the legacy content
	// itself. The attestation carries no trust root (trustRootKeyId is the
	// legacy-untrusted marker and trustRootAlgorithm is none), so a
	// legacy-mapped snapshot can never satisfy hardened eligibility.
	mapped.Attestation = Attestation{
		ProviderInstanceId: legacyInstanceIdPrefix + legacy.AdapterId + ":" + legacy.BinaryVersion,
		ConfigDigest:       sourceDigest,
		TrustRootKeyId:     legacyTrustRootKeyId,
		TrustRootAlgorithm: legacyTrustRootAlgorithm,
	}
	digest, err := mapped.Digest()
	if err != nil {
		return LegacySnapshotMapping{}, err
	}
	mapped.ProviderCapabilitySnapshotDigest = digest
	if err := mapped.Validate(); err != nil {
		return LegacySnapshotMapping{}, err
	}
	return LegacySnapshotMapping{
		Snapshot:                       mapped,
		SourceCapabilitySnapshotDigest: sourceDigest,
	}, nil
}

// validate fails closed on every legacy content rule. canonicalized is the
// RFC 8785 canonical form of the same document and is only used to verify
// the presence of the schema-required capability keys.
func (snapshot *LegacyCapabilitySnapshot) validate(canonicalized []byte) error {
	if snapshot.APIVersion != legacyAPIVersion {
		return fmt.Errorf("provider: legacy capability snapshot apiVersion must be exactly %q", legacyAPIVersion)
	}
	if snapshot.Kind != legacySnapshotKind {
		return fmt.Errorf("provider: legacy capability snapshot kind must be exactly %q", legacySnapshotKind)
	}
	requiredText := []struct {
		field string
		value string
	}{
		{"adapterId", snapshot.AdapterId},
		{"adapterVersion", snapshot.AdapterVersion},
		{"executable", snapshot.Executable},
		{"binaryVersion", snapshot.BinaryVersion},
		{"probedAt", snapshot.ProbedAt},
	}
	for _, field := range requiredText {
		if err := requireText(field.field, field.value); err != nil {
			return err
		}
	}
	switch snapshot.ProbeStatus {
	case legacyProbeSupported, legacyProbeExperimental:
	case legacyProbeUnsupported:
		return fmt.Errorf("provider: legacy capability snapshot probeStatus %q cannot be mapped: only supported and experimental legacy probes may enter the provider model", legacyProbeUnsupported)
	default:
		return fmt.Errorf("provider: legacy capability snapshot probeStatus must be one of supported, experimental or unsupported")
	}
	if err := requireRFC3339("probedAt", snapshot.ProbedAt); err != nil {
		return err
	}
	return legacyCapabilitiesPresent(canonicalized)
}

// legacyCapabilitiesPresent fails closed unless the capabilities object and
// all six schema-required capability keys are present in the document.
func legacyCapabilitiesPresent(canonicalized []byte) error {
	var presence struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(canonicalized, &presence); err != nil {
		return fmt.Errorf("provider: legacy capability snapshot capabilities: %w", err)
	}
	if presence.Capabilities == nil {
		return fmt.Errorf("provider: legacy capability snapshot capabilities must be present")
	}
	for _, key := range legacyRequiredCapabilityKeys {
		if _, present := presence.Capabilities[key]; !present {
			return fmt.Errorf("provider: legacy capability snapshot capabilities.%s must be present", key)
		}
	}
	return nil
}

// deriveLegacyCapabilities stringifies the legacy capabilities
// deterministically: arrays are joined with commas and booleans become
// true/false. The six required keys are always derived; the optional
// processTreeCancellation and notes keys are only carried across when
// present in the legacy document. No value is ever invented.
func deriveLegacyCapabilities(capabilities LegacyCapabilities) (map[string]string, error) {
	derived := make(map[string]string, len(legacyRequiredCapabilityKeys)+2)
	derived["structuredOutput"] = strings.Join(capabilities.StructuredOutput, ",")
	derived["nonInteractiveEdit"] = strconv.FormatBool(capabilities.NonInteractiveEdit)
	derived["sessionPolicies"] = strings.Join(capabilities.SessionPolicies, ",")
	derived["modelSelection"] = strconv.FormatBool(capabilities.ModelSelection)
	derived["executionProfiles"] = strings.Join(capabilities.ExecutionProfiles, ",")
	derived["nativeBudgets"] = strings.Join(capabilities.NativeBudgets, ",")
	for _, key := range []string{"structuredOutput", "sessionPolicies", "executionProfiles", "nativeBudgets"} {
		if derived[key] == "" {
			return nil, fmt.Errorf("provider: legacy capability snapshot capabilities.%s must stringify to a non-empty provider capability value", key)
		}
	}
	if capabilities.ProcessTreeCancellation != nil {
		derived["processTreeCancellation"] = strconv.FormatBool(*capabilities.ProcessTreeCancellation)
	}
	if len(capabilities.Notes) > 0 {
		derived["notes"] = strings.Join(capabilities.Notes, ",")
	}
	return derived, nil
}
