package agentregistry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ── Capability vocabulary (closed set) ────────────────────────────────────────

// Capability is a single closed-vocabulary capability token.
type Capability string

const (
	CapabilityExecutionProfileWorkspaceWrite Capability = "execution-profile:workspace-write"
	CapabilitySessionPolicyEphemeral         Capability = "session-policy:ephemeral"
	CapabilityNetworkPolicyUnenforced        Capability = "network-policy:unenforced"
	CapabilityNetworkPolicyEnforced          Capability = "network-policy:enforced"
	CapabilityResultStagingCandidateEnvelope Capability = "result-staging:candidate-envelope"
)

// knownCapabilities is the authoritative closed set; unknown values fail closed.
var knownCapabilities = map[Capability]struct{}{
	CapabilityExecutionProfileWorkspaceWrite: {},
	CapabilitySessionPolicyEphemeral:         {},
	CapabilityNetworkPolicyUnenforced:        {},
	CapabilityNetworkPolicyEnforced:          {},
	CapabilityResultStagingCandidateEnvelope: {},
}

func (c Capability) validate() error {
	if _, ok := knownCapabilities[c]; !ok {
		return fmt.Errorf("agentregistry: unknown Capability %q", string(c))
	}
	return nil
}

// ── SnapshotState (closed enum) ───────────────────────────────────────────────

// SnapshotState is the closed set of capability snapshot states.
type SnapshotState string

const (
	SnapshotStateActive   SnapshotState = "active"
	SnapshotStateRevoked  SnapshotState = "revoked"
	SnapshotStateReplaced SnapshotState = "replaced"
)

func (s SnapshotState) validate() error {
	switch s {
	case SnapshotStateActive, SnapshotStateRevoked, SnapshotStateReplaced:
		return nil
	default:
		return fmt.Errorf("agentregistry: unknown SnapshotState %q", string(s))
	}
}

// ── EvidenceKind (closed enum) ────────────────────────────────────────────────

// EvidenceKind is the closed set of evidence record kinds.
type EvidenceKind string

const (
	EvidenceKindAttestation    EvidenceKind = "attestation"
	EvidenceKindConformance    EvidenceKind = "conformance"
	EvidenceKindAttestedResult EvidenceKind = "attested-result"
)

func (k EvidenceKind) validate() error {
	switch k {
	case EvidenceKindAttestation, EvidenceKindConformance, EvidenceKindAttestedResult:
		return nil
	default:
		return fmt.Errorf("agentregistry: unknown EvidenceKind %q", string(k))
	}
}

// ── AgentCapabilitySnapshot ───────────────────────────────────────────────────

// AgentCapabilitySnapshot is the durable, closed capability record for a
// registered AgentProvider. It must be bound to a specific AgentRegistration
// via RegistrationID; it is only usable when that registration is active.
type AgentCapabilitySnapshot struct {
	SnapshotDigest             string // sha256:<64-hex>
	RegistrationID             string
	ProtocolVersion            string
	ProviderName               string
	ProviderVersion            string
	Capabilities               []Capability  // closed vocabulary; unknown values fail closed
	ConformanceEvidenceDigests []string      // []sha256:<64-hex>; may be empty but elements must be valid
	SnapshotState              SnapshotState // closed enum
}

// Validate fails closed on any missing or structurally invalid field.
func (s AgentCapabilitySnapshot) Validate() error {
	if err := requireDigest("SnapshotDigest", s.SnapshotDigest); err != nil {
		return fmt.Errorf("agentregistry: %w", err)
	}
	if strings.TrimSpace(s.RegistrationID) == "" {
		return fmt.Errorf("agentregistry: SnapshotRegistrationID must not be empty")
	}
	if strings.TrimSpace(s.ProtocolVersion) == "" {
		return fmt.Errorf("agentregistry: SnapshotProtocolVersion must not be empty")
	}
	if strings.TrimSpace(s.ProviderName) == "" {
		return fmt.Errorf("agentregistry: SnapshotProviderName must not be empty")
	}
	if strings.TrimSpace(s.ProviderVersion) == "" {
		return fmt.Errorf("agentregistry: SnapshotProviderVersion must not be empty")
	}
	if len(s.Capabilities) == 0 {
		return fmt.Errorf("agentregistry: Capabilities must not be empty")
	}
	for _, cap := range s.Capabilities {
		if err := cap.validate(); err != nil {
			return err
		}
	}
	for i, d := range s.ConformanceEvidenceDigests {
		if err := requireDigest(fmt.Sprintf("ConformanceEvidenceDigests[%d]", i), d); err != nil {
			return fmt.Errorf("agentregistry: %w", err)
		}
	}
	if err := s.SnapshotState.validate(); err != nil {
		return err
	}
	return nil
}

// snapshotJSON is the canonical serialisation shape for Digest().
type snapshotJSON struct {
	SnapshotDigest             string   `json:"snapshotDigest"`
	RegistrationID             string   `json:"registrationId"`
	ProtocolVersion            string   `json:"protocolVersion"`
	ProviderName               string   `json:"providerName"`
	ProviderVersion            string   `json:"providerVersion"`
	Capabilities               []string `json:"capabilities"`
	ConformanceEvidenceDigests []string `json:"conformanceEvidenceDigests"`
	SnapshotState              string   `json:"snapshotState"`
}

// Digest returns the sha256 digest of the canonical JSON form of the snapshot.
func (s AgentCapabilitySnapshot) Digest() (string, error) {
	caps := make([]string, len(s.Capabilities))
	for i, c := range s.Capabilities {
		caps[i] = string(c)
	}
	ced := s.ConformanceEvidenceDigests
	if ced == nil {
		ced = []string{}
	}
	raw, err := json.Marshal(snapshotJSON{
		SnapshotDigest:             s.SnapshotDigest,
		RegistrationID:             s.RegistrationID,
		ProtocolVersion:            s.ProtocolVersion,
		ProviderName:               s.ProviderName,
		ProviderVersion:            s.ProviderVersion,
		Capabilities:               caps,
		ConformanceEvidenceDigests: ced,
		SnapshotState:              string(s.SnapshotState),
	})
	if err != nil {
		return "", fmt.Errorf("agentregistry: snapshot serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// ── EvidenceRecord ─────────────────────────────────────────────────────────────

// EvidenceRecord is the closed evidence binding for an AgentProvider registration.
// Agent evidence must be bound to ProviderType=agent (R3-D sandbox evidence
// boundary reservation: ProviderType field + binding check enforces the split).
type EvidenceRecord struct {
	EvidenceDigest string       // sha256:<64-hex>
	EvidenceKind   EvidenceKind // closed enum
	ProviderType   ProviderType // must match the owning registration's ProviderType
	RegistrationID string
}

// Validate fails closed on any missing or structurally invalid field.
func (e EvidenceRecord) Validate() error {
	if err := requireDigest("EvidenceDigest", e.EvidenceDigest); err != nil {
		return fmt.Errorf("agentregistry: %w", err)
	}
	if err := e.EvidenceKind.validate(); err != nil {
		return err
	}
	if err := e.ProviderType.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.RegistrationID) == "" {
		return fmt.Errorf("agentregistry: EvidenceRegistrationID must not be empty")
	}
	return nil
}
