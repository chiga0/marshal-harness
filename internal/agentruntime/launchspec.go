package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// MigrationProvenance marks a spec or runtime produced by the legacy compat
// mapping. Values other than this constant are not production-declared.
const MigrationProvenance = "legacy-compat-nonproduction"

const digestPrefix = "sha256:"

// AgentLaunchSpec is the immutable, frozen input to an agent execution stage.
// Callers generate it before Stage; the Sandbox only executes the received
// spec and must not rewrite or infer argv/environment.
//
// Created via NewAgentLaunchSpec; zero-value is not valid.
type AgentLaunchSpec struct {
	AdapterID           string
	AdapterVersion      string
	RunID               string
	AttemptID           string
	Executable          string
	ExecutableDigest    string // sha256:<64-hex>
	WorkingDirectory    string
	Arguments           []string
	Environment         []string // complete, not additive
	ProfileDigest       string   // sha256:<64-hex>
	MigrationProvenance string   // empty means production; "legacy-compat-nonproduction" means compat
}

// specJSON is the canonical serialisation shape for Digest().
type specJSON struct {
	AdapterID           string   `json:"adapterID"`
	AdapterVersion      string   `json:"adapterVersion"`
	RunID               string   `json:"runID"`
	AttemptID           string   `json:"attemptID"`
	Executable          string   `json:"executable"`
	ExecutableDigest    string   `json:"executableDigest"`
	WorkingDirectory    string   `json:"workingDirectory"`
	Arguments           []string `json:"arguments"`
	Environment         []string `json:"environment"`
	ProfileDigest       string   `json:"profileDigest"`
	MigrationProvenance string   `json:"migrationProvenance"`
}

// NewAgentLaunchSpec validates all fields and returns an immutable spec.
// Any invalid field fails closed.
func NewAgentLaunchSpec(
	adapterID, adapterVersion, runID, attemptID string,
	executable, executableDigest string,
	workingDirectory string,
	arguments, environment []string,
	profileDigest string,
	migrationProvenance string,
) (AgentLaunchSpec, error) {
	s := AgentLaunchSpec{
		AdapterID:           adapterID,
		AdapterVersion:      adapterVersion,
		RunID:               runID,
		AttemptID:           attemptID,
		Executable:          executable,
		ExecutableDigest:    executableDigest,
		WorkingDirectory:    workingDirectory,
		Arguments:           append([]string(nil), arguments...),
		Environment:         append([]string(nil), environment...),
		ProfileDigest:       profileDigest,
		MigrationProvenance: migrationProvenance,
	}
	if err := s.Validate(); err != nil {
		return AgentLaunchSpec{}, err
	}
	return s, nil
}

// Validate checks every field; any invalid input returns an error.
func (s AgentLaunchSpec) Validate() error {
	if strings.TrimSpace(s.AdapterID) == "" {
		return errors.New("agentruntime: AdapterID must not be empty")
	}
	if strings.TrimSpace(s.AdapterVersion) == "" {
		return errors.New("agentruntime: AdapterVersion must not be empty")
	}
	if strings.TrimSpace(s.RunID) == "" {
		return errors.New("agentruntime: RunID must not be empty")
	}
	if strings.TrimSpace(s.AttemptID) == "" {
		return errors.New("agentruntime: AttemptID must not be empty")
	}
	if strings.TrimSpace(s.Executable) == "" {
		return errors.New("agentruntime: Executable must not be empty")
	}
	if err := requireDigest("ExecutableDigest", s.ExecutableDigest); err != nil {
		return err
	}
	if strings.TrimSpace(s.WorkingDirectory) == "" {
		return errors.New("agentruntime: WorkingDirectory must not be empty")
	}
	if err := requireDigest("ProfileDigest", s.ProfileDigest); err != nil {
		return err
	}
	return nil
}

// Digest returns the deterministic sha256 digest of the canonical JSON
// representation of this spec.
func (s AgentLaunchSpec) Digest() (string, error) {
	raw, err := json.Marshal(specJSON{
		AdapterID:           s.AdapterID,
		AdapterVersion:      s.AdapterVersion,
		RunID:               s.RunID,
		AttemptID:           s.AttemptID,
		Executable:          s.Executable,
		ExecutableDigest:    s.ExecutableDigest,
		WorkingDirectory:    s.WorkingDirectory,
		Arguments:           s.Arguments,
		Environment:         s.Environment,
		ProfileDigest:       s.ProfileDigest,
		MigrationProvenance: s.MigrationProvenance,
	})
	if err != nil {
		return "", fmt.Errorf("agentruntime: spec serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// requireDigest validates that v has the sha256: prefix followed by exactly
// 64 lowercase hex characters.
func requireDigest(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("agentruntime: %s must not be empty", field)
	}
	if !strings.HasPrefix(v, digestPrefix) {
		return fmt.Errorf("agentruntime: %s must carry the sha256: prefix", field)
	}
	hex := strings.TrimPrefix(v, digestPrefix)
	if len(hex) != 64 {
		return fmt.Errorf("agentruntime: %s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("agentruntime: %s must be lowercase hex", field)
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRuntime protocol types
// ────────────────────────────────────────────────────────────────────────────

// AgentEvent is a single decoded event emitted by the agent process.
// Raw carries the original bytes; the event is untrusted workload output.
type AgentEvent struct {
	// Sequence is a 1-based position within the event stream.
	Sequence int
	// Raw is the original untrusted byte payload.
	Raw []byte
}

// ExecEvidence is the observable evidence of a completed sandbox execution
// (exit code, timing metadata, etc.). It is passed to FinalizeResult.
type ExecEvidence struct {
	ExitCode int
	// Stderr may contain partial output; it is treated as untrusted.
	Stderr string
}

// WorkloadResult is the untrusted, normalised result of a single agent
// execution. Trusted is always false; no caller may promote it to authority.
type WorkloadResult struct {
	// Trusted is always false; authority acceptance belongs to ResultIngress
	// (ADR 0044) and must never be set here.
	Trusted bool
	// EventCount is the number of events that contributed to this result.
	EventCount int
	// ExitCode is the raw exit code from ExecEvidence.
	ExitCode int
	// ProviderHint carries an opaque string from the adapter, if any.
	ProviderHint string
}

// AgentRuntime normalises the event stream produced by an agent process into
// an untrusted WorkloadResult.
type AgentRuntime interface {
	// DecodeEvent parses a single raw event payload. Empty or malformed input
	// fails closed.
	DecodeEvent(raw []byte) (AgentEvent, error)
	// FinalizeResult aggregates a non-empty event stream with exec evidence
	// into an untrusted WorkloadResult. An empty events slice fails closed.
	FinalizeResult(events []AgentEvent, evidence ExecEvidence) (WorkloadResult, error)
}

// ────────────────────────────────────────────────────────────────────────────
// Legacy compat mapping
// ────────────────────────────────────────────────────────────────────────────

// compatRuntime adapts a legacy port.WorkerAdapter to the AgentRuntime
// interface. It is explicitly nonproduction and carries the
// MigrationProvenance marker throughout.
//
// This type must not be used as a production AgentProvider.
type compatRuntime struct {
	adapter port.WorkerAdapter
}

// NewCompatRuntime wraps a legacy WorkerAdapter. The result is explicitly
// nonproduction; its migration provenance is always MigrationProvenance.
func NewCompatRuntime(a port.WorkerAdapter) AgentRuntime {
	return &compatRuntime{adapter: a}
}

// PrepareLaunch projects the adapter identity and a frozen spec template into
// an AgentLaunchSpec bearing the MigrationProvenance marker.
// If profileDigest is absent or invalid the call fails closed.
func (c *compatRuntime) PrepareLaunch(
	runID, attemptID string,
	executable, executableDigest string,
	workingDirectory string,
	arguments, environment []string,
	profileDigest string,
) (AgentLaunchSpec, error) {
	return NewAgentLaunchSpec(
		c.adapter.ID(),
		"legacy-compat",
		runID,
		attemptID,
		executable,
		executableDigest,
		workingDirectory,
		arguments,
		environment,
		profileDigest,
		MigrationProvenance,
	)
}

// DecodeEvent treats the raw bytes as a JSON object payload. Empty input fails
// closed. The event sequence is set to 1 (compat single-event semantics).
func (c *compatRuntime) DecodeEvent(raw []byte) (AgentEvent, error) {
	if len(raw) == 0 {
		return AgentEvent{}, errors.New("agentruntime: compat DecodeEvent received empty raw bytes")
	}
	if !json.Valid(raw) {
		return AgentEvent{}, errors.New("agentruntime: compat DecodeEvent received malformed JSON")
	}
	return AgentEvent{Sequence: 1, Raw: append([]byte(nil), raw...)}, nil
}

// FinalizeResult aggregates events and evidence into an untrusted WorkloadResult.
// An empty events slice fails closed. Trusted is always false.
func (c *compatRuntime) FinalizeResult(events []AgentEvent, evidence ExecEvidence) (WorkloadResult, error) {
	if len(events) == 0 {
		return WorkloadResult{}, errors.New("agentruntime: FinalizeResult requires at least one event; empty stream fails closed")
	}
	return WorkloadResult{
		Trusted:      false,
		EventCount:   len(events),
		ExitCode:     evidence.ExitCode,
		ProviderHint: MigrationProvenance,
	}, nil
}

// ProbeCompat invokes the underlying adapter Probe for compat callers that
// need to observe adapter identity before launching.
func ProbeCompat(ctx context.Context, r AgentRuntime) (domain.Record, error) {
	cr, ok := r.(*compatRuntime)
	if !ok {
		return domain.Record{}, errors.New("agentruntime: ProbeCompat requires a compat runtime")
	}
	return cr.adapter.Probe(ctx)
}

// ────────────────────────────────────────────────────────────────────────────
// FakeAgent
// ────────────────────────────────────────────────────────────────────────────

// FakeAgent is a deterministic AgentRuntime implementation for testing and
// walking-skeleton use. Given the same input it always produces the same
// output. It must not read or write the real filesystem or network.
type FakeAgent struct {
	// FixedEvent is the raw JSON returned for every DecodeEvent call.
	// Defaults to `{"fake":true}` when empty.
	FixedEvent []byte
	// FixedHint is the ProviderHint placed in every WorkloadResult.
	FixedHint string
}

var defaultFakeEvent = []byte(`{"fake":true}`)

// DecodeEvent returns a fixed deterministic AgentEvent for any non-empty
// valid input. Empty or invalid JSON fails closed.
func (f *FakeAgent) DecodeEvent(raw []byte) (AgentEvent, error) {
	if len(raw) == 0 {
		return AgentEvent{}, errors.New("agentruntime: FakeAgent.DecodeEvent received empty input")
	}
	if !json.Valid(raw) {
		return AgentEvent{}, errors.New("agentruntime: FakeAgent.DecodeEvent received malformed JSON")
	}
	payload := f.FixedEvent
	if len(payload) == 0 {
		payload = defaultFakeEvent
	}
	return AgentEvent{Sequence: 1, Raw: append([]byte(nil), payload...)}, nil
}

// FinalizeResult returns a fixed deterministic WorkloadResult. Trusted is
// always false. Empty events fails closed.
func (f *FakeAgent) FinalizeResult(events []AgentEvent, evidence ExecEvidence) (WorkloadResult, error) {
	if len(events) == 0 {
		return WorkloadResult{}, errors.New("agentruntime: FakeAgent.FinalizeResult requires at least one event")
	}
	hint := f.FixedHint
	if hint == "" {
		hint = "fake-agent"
	}
	return WorkloadResult{
		Trusted:      false,
		EventCount:   len(events),
		ExitCode:     evidence.ExitCode,
		ProviderHint: hint,
	}, nil
}
