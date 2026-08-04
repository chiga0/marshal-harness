package adapter

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Capability validation errors are fixed, categorized strings. They never echo
// provider free text such as capability notes or probeErrors, so Planning and
// Execution can compare and log them deterministically.
const (
	ErrCapabilityInvalidKind        = "validate capability: unexpected record kind"
	ErrCapabilityMalformed          = "validate capability: malformed snapshot"
	ErrCapabilityMissingAdapterID   = "validate capability: missing adapterId"
	ErrCapabilityUnsupported        = "validate capability: probe status is not supported"
	ErrCapabilityStructuredOutput   = "validate capability: structuredOutput does not include jsonl"
	ErrCapabilityNonInteractiveEdit = "validate capability: nonInteractiveEdit is not enabled"
	ErrCapabilityProcessTree        = "validate capability: processTreeCancellation is not enabled"
	ErrCapabilitySessionPolicy      = "validate capability: session policy not supported"
	ErrCapabilityExecutionProfile   = "validate capability: execution profile not supported"
	ErrCapabilityModelSelection     = "validate capability: model selection not supported"
)

// ValidateCapability checks that a capability snapshot record is compatible
// with the task's worker requirements and returns the snapshot's adapterId.
// It is provider-neutral and deterministic: it inspects only the record
// structure and the task requirements, never trusts notes or probeErrors, and
// performs no provider selection, probing, or file or environment access.
func ValidateCapability(record domain.Record, task domain.TaskSpec) (string, error) {
	if record.Kind != domain.KindCapabilitySnapshot {
		return "", port.Permanentf("%s", ErrCapabilityInvalidKind)
	}
	var snapshot struct {
		AdapterID    string `json:"adapterId"`
		ProbeStatus  string `json:"probeStatus"`
		Capabilities struct {
			StructuredOutput        []string `json:"structuredOutput"`
			NonInteractiveEdit      bool     `json:"nonInteractiveEdit"`
			ProcessTreeCancellation bool     `json:"processTreeCancellation"`
			SessionPolicies         []string `json:"sessionPolicies"`
			ExecutionProfiles       []string `json:"executionProfiles"`
			ModelSelection          bool     `json:"modelSelection"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		return "", port.Permanentf("%s", ErrCapabilityMalformed)
	}
	if strings.TrimSpace(snapshot.AdapterID) == "" {
		return "", port.Permanentf("%s", ErrCapabilityMissingAdapterID)
	}
	if snapshot.ProbeStatus != "supported" {
		return "", port.Permanentf("%s", ErrCapabilityUnsupported)
	}
	caps := snapshot.Capabilities
	if !slices.Contains(caps.StructuredOutput, "jsonl") {
		return "", port.Permanentf("%s", ErrCapabilityStructuredOutput)
	}
	if !caps.NonInteractiveEdit {
		return "", port.Permanentf("%s", ErrCapabilityNonInteractiveEdit)
	}
	if !caps.ProcessTreeCancellation {
		return "", port.Permanentf("%s", ErrCapabilityProcessTree)
	}
	if !slices.Contains(caps.SessionPolicies, task.Worker.SessionPolicy) {
		return "", port.Permanentf("%s", ErrCapabilitySessionPolicy)
	}
	if !slices.Contains(caps.ExecutionProfiles, task.Worker.ExecutionProfile) {
		return "", port.Permanentf("%s", ErrCapabilityExecutionProfile)
	}
	if task.Worker.Model != "" && !caps.ModelSelection {
		return "", port.Permanentf("%s", ErrCapabilityModelSelection)
	}
	return snapshot.AdapterID, nil
}
