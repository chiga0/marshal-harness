// Package planning implements the pure Planning gate over a frozen
// PolicySnapshot. It performs no Git operations, no adapter probing, and no
// file or state writes: it validates the snapshot against its schema, checks
// it against the frozen TaskSpec, and returns the effective policy that the
// adapter Selector must honor.
package planning

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Policy validation errors are fixed, categorized strings. They never echo
// source paths, digests, environment values, or free text from the snapshot,
// so callers can compare and log them deterministically.
const (
	ErrPolicyNilValidator   = "validate policy: nil validator"
	ErrPolicySchemaInvalid  = "validate policy: schema invalid"
	ErrPolicyMalformed      = "validate policy: malformed snapshot"
	ErrPolicyTaskMismatch   = "validate policy: taskId does not match the frozen task"
	ErrPolicyRunMismatch    = "validate policy: runId does not match the frozen run"
	ErrPolicyProfileUnknown = "validate policy: unknown execution profile"
	ErrPolicyProfile        = "validate policy: task execution profile is below the policy minimum"
	ErrPolicyPublication    = "validate policy: publication is required but not allowed"
	ErrPolicyMerge          = "validate policy: merge is not permitted in the Local MVP"
	ErrPolicyPreferredEmpty = "validate policy: task preferredAdapter is empty"
	ErrPolicyNoAdapters     = "validate policy: allowedAdapters is empty"
	ErrPolicyNoCandidates   = "validate policy: no explicit task adapter candidate is allowed"
)

// executionProfileRank orders profiles by the guarantees they require:
// read-only < workspace-write < hardened.
var executionProfileRank = map[string]int{
	"read-only":       0,
	"workspace-write": 1,
	"hardened":        2,
}

// EffectivePolicy is the validated, fail-closed view of a PolicySnapshot for
// one run. Adapter candidates preserve the TaskSpec declaration order and
// never include adapters the TaskSpec did not declare.
type EffectivePolicy struct {
	TaskID               string
	RunID                string
	ExecutionProfile     string
	AllowedAdapters      []string
	AllowFallbackWorkers bool
	EnvironmentAllowlist []string
	RetentionDays        int
	AllowPublication     bool
	NetworkPolicy        string
	PreferredAdapter     string
	FallbackAdapters     []string
}

// SelectionRequest builds the exact adapter.SelectionRequest for the Selector:
// the TaskSpec's preferred adapter first, then the fallback adapters that the
// policy permits, and the policy adapter allow-list. No adapter is added
// implicitly.
func (p EffectivePolicy) SelectionRequest() adapter.SelectionRequest {
	return adapter.SelectionRequest{
		PreferredAdapter: p.PreferredAdapter,
		FallbackAdapters: slices.Clone(p.FallbackAdapters),
		AllowedAdapters:  slices.Clone(p.AllowedAdapters),
	}
}

// policySnapshot mirrors only the fields the Planning gate consumes. It is
// decoded strictly after schema validation, which already rejects unknown
// properties and enforces every field's shape.
type policySnapshot struct {
	TaskID    string `json:"taskId"`
	RunID     string `json:"runId"`
	Effective struct {
		MinimumExecutionProfile string   `json:"minimumExecutionProfile"`
		NetworkPolicy           string   `json:"networkPolicy"`
		AllowFallbackWorkers    bool     `json:"allowFallbackWorkers"`
		AllowPublication        bool     `json:"allowPublication"`
		AllowMerge              bool     `json:"allowMerge"`
		AllowedAdapters         []string `json:"allowedAdapters"`
		EnvironmentAllowlist    []string `json:"environmentAllowlist"`
		RetentionDays           int      `json:"retentionDays"`
	} `json:"effective"`
}

// ValidatePolicy validates a PolicySnapshot against its schema and the frozen
// TaskSpec for one run, and returns the effective policy. It is pure: it
// performs no Git operations, no probing, and no file or state writes.
//
// The gate is fail-closed:
//   - taskId and runId must match the frozen task and run exactly;
//   - the task execution profile must not be below minimumExecutionProfile;
//   - a task that requires publication needs allowPublication;
//   - automatic merge is never permitted in the Local MVP: neither a
//     mergePolicy other than "never" nor allowMerge can relax this;
//   - an empty adapter allow-list, or one that shares no candidate with the
//     TaskSpec's explicit adapter declarations, is rejected;
//   - when fallback workers are not allowed, only the preferred adapter
//     remains a candidate (declared fallbacks are dropped, not an error).
func ValidatePolicy(data []byte, task domain.TaskSpec, runID string, validator *contract.Validator) (EffectivePolicy, error) {
	if validator == nil {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyNilValidator)
	}
	if err := validator.Validate(domain.KindPolicySnapshot, data); err != nil {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicySchemaInvalid)
	}
	var snapshot policySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMalformed)
	}
	if snapshot.TaskID != task.Metadata.ID {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyTaskMismatch)
	}
	if snapshot.RunID != runID {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyRunMismatch)
	}
	minimumRank, ok := executionProfileRank[snapshot.Effective.MinimumExecutionProfile]
	if !ok {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyProfileUnknown)
	}
	taskRank, ok := executionProfileRank[task.Worker.ExecutionProfile]
	if !ok {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyProfileUnknown)
	}
	if taskRank < minimumRank {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyProfile)
	}
	if task.Publication.Required && !snapshot.Effective.AllowPublication {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyPublication)
	}
	// The Local MVP never performs merges. Fail-closed: neither a task that
	// asks for merge nor a policy that grants it can relax this boundary.
	if task.Publication.MergePolicy != "never" || snapshot.Effective.AllowMerge {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMerge)
	}
	effective := EffectivePolicy{
		TaskID:               snapshot.TaskID,
		RunID:                snapshot.RunID,
		ExecutionProfile:     task.Worker.ExecutionProfile,
		AllowedAdapters:      slices.Clone(snapshot.Effective.AllowedAdapters),
		AllowFallbackWorkers: snapshot.Effective.AllowFallbackWorkers,
		EnvironmentAllowlist: slices.Clone(snapshot.Effective.EnvironmentAllowlist),
		RetentionDays:        snapshot.Effective.RetentionDays,
		AllowPublication:     snapshot.Effective.AllowPublication,
		NetworkPolicy:        snapshot.Effective.NetworkPolicy,
	}
	if len(effective.AllowedAdapters) == 0 {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyNoAdapters)
	}
	preferred := strings.TrimSpace(task.Worker.PreferredAdapter)
	if preferred == "" {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyPreferredEmpty)
	}
	candidates := []string{preferred}
	seen := map[string]bool{preferred: true}
	if snapshot.Effective.AllowFallbackWorkers {
		for _, raw := range task.Worker.FallbackAdapters {
			id := strings.TrimSpace(raw)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			candidates = append(candidates, id)
		}
	}
	allowed := make(map[string]bool, len(effective.AllowedAdapters))
	for _, id := range effective.AllowedAdapters {
		allowed[id] = true
	}
	overlap := false
	for _, candidate := range candidates {
		if allowed[candidate] {
			overlap = true
			break
		}
	}
	if !overlap {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyNoCandidates)
	}
	effective.PreferredAdapter = candidates[0]
	effective.FallbackAdapters = candidates[1:]
	return effective, nil
}
