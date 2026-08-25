package runtimeprofile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ── RejectReason (closed enum) ───────────────────────────────────────────────

// RejectReason is the closed set of CompatibilityCheck rejection labels.
type RejectReason string

const (
	RejectReasonAgentBindingMismatch   RejectReason = "agent-binding-mismatch"
	RejectReasonSandboxBindingMismatch RejectReason = "sandbox-binding-mismatch"
	RejectReasonCompatibilityMismatch  RejectReason = "compatibility-mismatch"
)

// ── AgentBinding ─────────────────────────────────────────────────────────────

// AgentBinding is the frozen identity record binding an agent registration and
// its snapshot to a WorkerRuntimeProfile. All fields are required; Validate
// fails closed on any missing or invalid value. No credential fields are present.
type AgentBinding struct {
	RegistrationID     string // "registration:<hex>"
	SnapshotDigest     string // sha256:<64-hex>
	ProviderName       string
	ProviderVersion    string
	ProtocolVersion    string
	AgentBindingDigest string // sha256:<64-hex>, canonical-derived
}

type agentBindingJSON struct {
	RegistrationID  string `json:"registrationId"`
	SnapshotDigest  string `json:"snapshotDigest"`
	ProviderName    string `json:"providerName"`
	ProviderVersion string `json:"providerVersion"`
	ProtocolVersion string `json:"protocolVersion"`
}

// Validate fails closed on any missing or structurally invalid field.
// AgentBindingDigest is not validated here; it is derived by Digest().
func (a AgentBinding) Validate() error {
	if strings.TrimSpace(a.RegistrationID) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.RegistrationID must not be empty")
	}
	if !strings.HasPrefix(a.RegistrationID, "registration:") {
		return fmt.Errorf("runtimeprofile: AgentBinding.RegistrationID must have registration: prefix")
	}
	if err := requireDigest("AgentBinding.SnapshotDigest", a.SnapshotDigest); err != nil {
		return fmt.Errorf("runtimeprofile: %w", err)
	}
	if strings.TrimSpace(a.ProviderName) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProviderName must not be empty")
	}
	if strings.TrimSpace(a.ProviderVersion) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProviderVersion must not be empty")
	}
	if strings.TrimSpace(a.ProtocolVersion) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProtocolVersion must not be empty")
	}
	if err := requireDigest("AgentBinding.AgentBindingDigest", a.AgentBindingDigest); err != nil {
		return fmt.Errorf("runtimeprofile: %w", err)
	}
	return nil
}

// Digest returns the canonical sha256 digest of the AgentBinding identity fields.
// The returned digest is independent of AgentBindingDigest itself (which is derived
// from the identity fields, not included in its own digest input).
func (a AgentBinding) Digest() (string, error) {
	raw, err := json.Marshal(agentBindingJSON{
		RegistrationID:  a.RegistrationID,
		SnapshotDigest:  a.SnapshotDigest,
		ProviderName:    a.ProviderName,
		ProviderVersion: a.ProviderVersion,
		ProtocolVersion: a.ProtocolVersion,
	})
	if err != nil {
		return "", fmt.Errorf("runtimeprofile: AgentBinding serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewAgentBinding constructs an AgentBinding and derives AgentBindingDigest
// from the identity fields. Fails closed if any field is invalid (before digest).
func NewAgentBinding(registrationID, snapshotDigest, providerName, providerVersion, protocolVersion string) (AgentBinding, error) {
	a := AgentBinding{
		RegistrationID:  registrationID,
		SnapshotDigest:  snapshotDigest,
		ProviderName:    providerName,
		ProviderVersion: providerVersion,
		ProtocolVersion: protocolVersion,
	}
	// validate identity fields before deriving digest
	if err := validateAgentBindingIdentity(a); err != nil {
		return AgentBinding{}, err
	}
	digest, err := a.Digest()
	if err != nil {
		return AgentBinding{}, err
	}
	a.AgentBindingDigest = digest
	return a, nil
}

// validateAgentBindingIdentity checks all fields except AgentBindingDigest.
func validateAgentBindingIdentity(a AgentBinding) error {
	if strings.TrimSpace(a.RegistrationID) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.RegistrationID must not be empty")
	}
	if !strings.HasPrefix(a.RegistrationID, "registration:") {
		return fmt.Errorf("runtimeprofile: AgentBinding.RegistrationID must have registration: prefix")
	}
	if err := requireDigest("AgentBinding.SnapshotDigest", a.SnapshotDigest); err != nil {
		return fmt.Errorf("runtimeprofile: %w", err)
	}
	if strings.TrimSpace(a.ProviderName) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProviderName must not be empty")
	}
	if strings.TrimSpace(a.ProviderVersion) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProviderVersion must not be empty")
	}
	if strings.TrimSpace(a.ProtocolVersion) == "" {
		return fmt.Errorf("runtimeprofile: AgentBinding.ProtocolVersion must not be empty")
	}
	return nil
}

// ── SandboxBinding ───────────────────────────────────────────────────────────

// SandboxBinding is the frozen identity record binding a sandbox allocation to a
// WorkerRuntimeProfile. All fields are required; Validate fails closed on any
// missing or invalid value. No credential fields are present.
// SandboxBinding is fully independent of AgentBinding: each has its own Validate,
// Digest, and no cross-reference of fields.
type SandboxBinding struct {
	SandboxProviderRegistrationID string // "registration:<hex>"
	AllocationID                  string
	Generation                    int64  // strictly positive
	SandboxBindingDigest          string // sha256:<64-hex>, canonical-derived
}

type sandboxBindingJSON struct {
	SandboxProviderRegistrationID string `json:"sandboxProviderRegistrationId"`
	AllocationID                  string `json:"allocationId"`
	Generation                    int64  `json:"generation"`
}

// Validate fails closed on any missing or structurally invalid field.
// SandboxBindingDigest is not validated here; it is derived by Digest().
func (s SandboxBinding) Validate() error {
	if strings.TrimSpace(s.SandboxProviderRegistrationID) == "" {
		return fmt.Errorf("runtimeprofile: SandboxBinding.SandboxProviderRegistrationID must not be empty")
	}
	if !strings.HasPrefix(s.SandboxProviderRegistrationID, "registration:") {
		return fmt.Errorf("runtimeprofile: SandboxBinding.SandboxProviderRegistrationID must have registration: prefix")
	}
	if strings.TrimSpace(s.AllocationID) == "" {
		return fmt.Errorf("runtimeprofile: SandboxBinding.AllocationID must not be empty")
	}
	if s.Generation < 1 {
		return fmt.Errorf("runtimeprofile: SandboxBinding.Generation must be a positive integer, got %d", s.Generation)
	}
	if err := requireDigest("SandboxBinding.SandboxBindingDigest", s.SandboxBindingDigest); err != nil {
		return fmt.Errorf("runtimeprofile: %w", err)
	}
	return nil
}

// Digest returns the canonical sha256 digest of the SandboxBinding identity fields.
func (s SandboxBinding) Digest() (string, error) {
	raw, err := json.Marshal(sandboxBindingJSON{
		SandboxProviderRegistrationID: s.SandboxProviderRegistrationID,
		AllocationID:                  s.AllocationID,
		Generation:                    s.Generation,
	})
	if err != nil {
		return "", fmt.Errorf("runtimeprofile: SandboxBinding serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewSandboxBinding constructs a SandboxBinding and derives SandboxBindingDigest.
// Fails closed if any field is invalid.
func NewSandboxBinding(sandboxProviderRegistrationID, allocationID string, generation int64) (SandboxBinding, error) {
	s := SandboxBinding{
		SandboxProviderRegistrationID: sandboxProviderRegistrationID,
		AllocationID:                  allocationID,
		Generation:                    generation,
	}
	if err := validateSandboxBindingIdentity(s); err != nil {
		return SandboxBinding{}, err
	}
	digest, err := s.Digest()
	if err != nil {
		return SandboxBinding{}, err
	}
	s.SandboxBindingDigest = digest
	return s, nil
}

// validateSandboxBindingIdentity checks all fields except SandboxBindingDigest.
func validateSandboxBindingIdentity(s SandboxBinding) error {
	if strings.TrimSpace(s.SandboxProviderRegistrationID) == "" {
		return fmt.Errorf("runtimeprofile: SandboxBinding.SandboxProviderRegistrationID must not be empty")
	}
	if !strings.HasPrefix(s.SandboxProviderRegistrationID, "registration:") {
		return fmt.Errorf("runtimeprofile: SandboxBinding.SandboxProviderRegistrationID must have registration: prefix")
	}
	if strings.TrimSpace(s.AllocationID) == "" {
		return fmt.Errorf("runtimeprofile: SandboxBinding.AllocationID must not be empty")
	}
	if s.Generation < 1 {
		return fmt.Errorf("runtimeprofile: SandboxBinding.Generation must be a positive integer, got %d", s.Generation)
	}
	return nil
}

// ── WorkerRuntimeProfile ─────────────────────────────────────────────────────

// WorkerRuntimeProfile is the frozen composition of AgentBinding + SandboxBinding
// plus a compatibility digest and a profile digest. The two bindings are
// independent: each can be replaced without affecting the other's digest.
// No credential fields are present.
type WorkerRuntimeProfile struct {
	Agent               AgentBinding
	Sandbox             SandboxBinding
	CompatibilityDigest string // sha256:<64-hex>, supplied by caller
	ProfileDigest       string // sha256:<64-hex>, canonical-derived
}

type profileDigestInputJSON struct {
	AgentBindingDigest   string `json:"agentBindingDigest"`
	SandboxBindingDigest string `json:"sandboxBindingDigest"`
	CompatibilityDigest  string `json:"compatibilityDigest"`
}

// Digest returns the canonical sha256 digest of the profile's three digest inputs.
func (p WorkerRuntimeProfile) Digest() (string, error) {
	raw, err := json.Marshal(profileDigestInputJSON{
		AgentBindingDigest:   p.Agent.AgentBindingDigest,
		SandboxBindingDigest: p.Sandbox.SandboxBindingDigest,
		CompatibilityDigest:  p.CompatibilityDigest,
	})
	if err != nil {
		return "", fmt.Errorf("runtimeprofile: profile serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewProfile constructs a WorkerRuntimeProfile from validated bindings and a
// compatibility digest. Fails closed if either binding is invalid or if the
// compatibilityDigest is not a valid sha256:<64-hex> digest.
func NewProfile(agent AgentBinding, sandbox SandboxBinding, compatibilityDigest string) (WorkerRuntimeProfile, error) {
	if err := agent.Validate(); err != nil {
		return WorkerRuntimeProfile{}, fmt.Errorf("runtimeprofile: NewProfile agent binding invalid: %w", err)
	}
	if err := sandbox.Validate(); err != nil {
		return WorkerRuntimeProfile{}, fmt.Errorf("runtimeprofile: NewProfile sandbox binding invalid: %w", err)
	}
	if err := requireDigest("compatibilityDigest", compatibilityDigest); err != nil {
		return WorkerRuntimeProfile{}, fmt.Errorf("runtimeprofile: %w", err)
	}
	p := WorkerRuntimeProfile{
		Agent:               agent,
		Sandbox:             sandbox,
		CompatibilityDigest: compatibilityDigest,
	}
	digest, err := p.Digest()
	if err != nil {
		return WorkerRuntimeProfile{}, err
	}
	p.ProfileDigest = digest
	return p, nil
}

// ReplaceAgentBinding returns a new WorkerRuntimeProfile with the agent binding
// replaced. The sandbox binding and its digest are unchanged. The new profile
// has a freshly derived ProfileDigest. Fails closed if newAgent is invalid.
func ReplaceAgentBinding(p WorkerRuntimeProfile, newAgent AgentBinding) (WorkerRuntimeProfile, error) {
	if err := newAgent.Validate(); err != nil {
		return WorkerRuntimeProfile{}, fmt.Errorf("runtimeprofile: ReplaceAgentBinding: %w", err)
	}
	next := WorkerRuntimeProfile{
		Agent:               newAgent,
		Sandbox:             p.Sandbox,
		CompatibilityDigest: p.CompatibilityDigest,
	}
	digest, err := next.Digest()
	if err != nil {
		return WorkerRuntimeProfile{}, err
	}
	next.ProfileDigest = digest
	return next, nil
}

// ReplaceSandboxBinding returns a new WorkerRuntimeProfile with the sandbox
// binding replaced. The agent binding and its digest are unchanged. The new
// profile has a freshly derived ProfileDigest. Fails closed if newSandbox is invalid.
func ReplaceSandboxBinding(p WorkerRuntimeProfile, newSandbox SandboxBinding) (WorkerRuntimeProfile, error) {
	if err := newSandbox.Validate(); err != nil {
		return WorkerRuntimeProfile{}, fmt.Errorf("runtimeprofile: ReplaceSandboxBinding: %w", err)
	}
	next := WorkerRuntimeProfile{
		Agent:               p.Agent,
		Sandbox:             newSandbox,
		CompatibilityDigest: p.CompatibilityDigest,
	}
	digest, err := next.Digest()
	if err != nil {
		return WorkerRuntimeProfile{}, err
	}
	next.ProfileDigest = digest
	return next, nil
}

// CompatibilityCheckResult is the outcome of CompatibilityCheck.
type CompatibilityCheckResult struct {
	OK           bool
	RejectReason RejectReason // set only when OK == false
}

// CompatibilityCheck verifies that the provided agent and sandbox bindings are
// consistent with the profile's recorded digests, and that the profile's
// CompatibilityDigest matches the one recorded at profile construction time.
// Any mismatch fails closed with the appropriate RejectReason.
func CompatibilityCheck(profile WorkerRuntimeProfile, agentBinding AgentBinding, sandboxBinding SandboxBinding) CompatibilityCheckResult {
	if agentBinding.AgentBindingDigest != profile.Agent.AgentBindingDigest {
		return CompatibilityCheckResult{OK: false, RejectReason: RejectReasonAgentBindingMismatch}
	}
	if sandboxBinding.SandboxBindingDigest != profile.Sandbox.SandboxBindingDigest {
		return CompatibilityCheckResult{OK: false, RejectReason: RejectReasonSandboxBindingMismatch}
	}
	// recompute profile digest from the presented bindings and compatibility digest
	check := WorkerRuntimeProfile{
		Agent:               agentBinding,
		Sandbox:             sandboxBinding,
		CompatibilityDigest: profile.CompatibilityDigest,
	}
	derived, err := check.Digest()
	if err != nil || derived != profile.ProfileDigest {
		return CompatibilityCheckResult{OK: false, RejectReason: RejectReasonCompatibilityMismatch}
	}
	return CompatibilityCheckResult{OK: true}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
