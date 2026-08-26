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
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// Policy validation errors are fixed, categorized strings. They never echo
// source paths, digests, environment values, or free text from the snapshot,
// so callers can compare and log them deterministically.
const (
	ErrPolicyNilValidator             = "validate policy: nil validator"
	ErrPolicySchemaInvalid            = "validate policy: schema invalid"
	ErrPolicyMalformed                = "validate policy: malformed snapshot"
	ErrPolicyTaskMismatch             = "validate policy: taskId does not match the frozen task"
	ErrPolicyRunMismatch              = "validate policy: runId does not match the frozen run"
	ErrPolicyGeneratedAt              = "validate policy: generatedAt is not a valid RFC 3339 timestamp"
	ErrPolicyDigestMismatch           = "validate policy: policyDigest does not match the detached snapshot digest"
	ErrPolicySourceDigest             = "validate policy: sources digest is not a valid sha256 digest"
	ErrPolicyProfileUnknown           = "validate policy: unknown execution profile"
	ErrPolicyProfile                  = "validate policy: task execution profile is below the policy minimum"
	ErrPolicyPublication              = "validate policy: publication is required but not allowed"
	ErrPolicyMerge                    = "validate policy: merge is not permitted (manual merge is not implemented)"
	ErrPolicyMergeNotAllowed          = "validate policy: merge requires the policy to allow both publication and merge"
	ErrPolicyMergeProvider            = "validate policy: merge requires provider github and draft mode"
	ErrPolicyMergeMethod              = "validate policy: merge requires a mergeMethod from the closed merge/squash/rebase enumeration"
	ErrPolicyMergeChecks              = "validate policy: merge requires non-empty unique requiredChecks"
	ErrPolicyOpenCode                 = "validate policy: OpenCode is ineligible for new tasks"
	ErrPolicyPreferredEmpty           = "validate policy: task preferredAdapter is empty"
	ErrPolicyNoAdapters               = "validate policy: allowedAdapters is empty"
	ErrPolicyNoCandidates             = "validate policy: no explicit task adapter candidate is allowed"
	ErrPolicyControlGates             = "validate policy: approval gates conflict with autonomy profile"
	ErrPolicySteering                 = "validate policy: mediated steering conflicts with steering round budget"
	ErrPolicyLocalBindingMissing      = "validate policy: local dogfood environment binding is required"
	ErrPolicyLocalBindingCrossProfile = "validate policy: local dogfood environment binding crossed profiles"
	ErrPolicyLocalBindingMismatch     = "validate policy: local dogfood environment binding does not match current identity"
	ErrPolicyLocalSurface             = "validate policy: local dogfood policy grants a prohibited surface"
	ErrPolicyLocalCapabilityAuthority = "validate policy: local dogfood capability authority is not ordinary-user"
)

// Acceptance floor errors (issue #87): a control-regime PolicySnapshot is
// accepted only when the frozen TaskSpec declares at least one acceptance
// command, a non-empty argv on every command, and at least one required:true
// command, so verification always has a mandatory command to run.
const (
	ErrPolicyAcceptanceFloorEmpty      = "validate policy: acceptance floor requires at least one acceptance command"
	ErrPolicyAcceptanceFloorNoRequired = "validate policy: acceptance floor requires at least one required:true acceptance command"
	ErrPolicyAcceptanceFloorArgv       = "validate policy: acceptance floor requires every acceptance command to declare a non-empty argv"
)

const (
	AutonomySupervised         = "supervised"
	AutonomyBalanced           = "balanced"
	AutonomyAutonomous         = "autonomous"
	DirectPTYDeny              = "deny"
	DirectPTYRecordAndReverify = "record-and-reverify"
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
	TaskID                string
	RunID                 string
	ExecutionProfile      string
	AllowedAdapters       []string
	AllowFallbackWorkers  bool
	EnvironmentAllowlist  []string
	RetentionDays         int
	AllowPublication      bool
	NetworkPolicy         string
	PreferredAdapter      string
	FallbackAdapters      []string
	AutonomyProfile       string
	RequiredApprovals     []string
	AllowMediatedSteering bool
	DirectPTYPolicy       string
	MaxSteeringRounds     uint
	LegacyControl         bool
	EnvironmentBinding    *LocalDogfoodEnvironmentBinding
}

const LocalDogfoodEnvironmentBindingSchema = "marshal.local-dogfood-environment-binding.v1"

// LocalDogfoodEnvironmentBinding is the closed, policy-owned applicability
// binding for ADR 0051. RunState and ApprovalRecord keep referring to it only
// through PolicyDigest, so this remains the single durable source of truth.
type LocalDogfoodEnvironmentBinding struct {
	SchemaVersion         string `json:"schemaVersion"`
	SelfProfile           string `json:"selfProfile"`
	ActivationDigest      string `json:"activationDigest"`
	IdentitySubjectDigest string `json:"identitySubjectDigest"`
	Assurance             string `json:"assurance"`
	Execution             string `json:"execution"`
	Production            bool   `json:"production"`
	Publication           string `json:"publication"`
}

// LocalDogfoodEnvironmentBindingForObservation projects one admitted
// Core-owned observation into the closed fields a policy issuer must copy and
// reseal. It does not issue or mutate a PolicySnapshot.
func LocalDogfoodEnvironmentBindingForObservation(observation selfidentity.LocalSelfIdentityObservationV1) LocalDogfoodEnvironmentBinding {
	return LocalDogfoodEnvironmentBinding{
		SchemaVersion:         LocalDogfoodEnvironmentBindingSchema,
		SelfProfile:           selfidentity.LocalProfile,
		ActivationDigest:      observation.ActivationDigest,
		IdentitySubjectDigest: observation.IdentitySubjectDigest,
		Assurance:             "ordinary-user",
		Execution:             "workspace-write",
		Production:            false,
		Publication:           "none",
	}
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
	TaskID       string         `json:"taskId"`
	RunID        string         `json:"runId"`
	Sources      []policySource `json:"sources"`
	PolicyDigest string         `json:"policyDigest"`
	GeneratedAt  string         `json:"generatedAt"`
	Effective    struct {
		MinimumExecutionProfile string   `json:"minimumExecutionProfile"`
		NetworkPolicy           string   `json:"networkPolicy"`
		AllowFallbackWorkers    bool     `json:"allowFallbackWorkers"`
		AllowPublication        bool     `json:"allowPublication"`
		AllowMerge              bool     `json:"allowMerge"`
		AllowedAdapters         []string `json:"allowedAdapters"`
		EnvironmentAllowlist    []string `json:"environmentAllowlist"`
		RetentionDays           int      `json:"retentionDays"`
	} `json:"effective"`
	Control *struct {
		AutonomyProfile       string   `json:"autonomyProfile"`
		RequiredApprovals     []string `json:"requiredApprovals"`
		AllowMediatedSteering bool     `json:"allowMediatedSteering"`
		DirectPTYPolicy       string   `json:"directPtyPolicy"`
		MaxSteeringRounds     uint     `json:"maxSteeringRounds"`
	} `json:"control,omitempty"`
	EnvironmentBinding *LocalDogfoodEnvironmentBinding `json:"environmentBinding,omitempty"`
}

// policySource mirrors one sources entry of the PolicySnapshot. Phase 1 only
// checks that each digest is a well-formed sha256 digest; comparing the
// digests against repository policy source content is phase-2 scope.
type policySource struct {
	Scope    string `json:"scope"`
	Path     string `json:"path"`
	Digest   string `json:"digest"`
	Required bool   `json:"required"`
}

// ValidatePolicy validates a PolicySnapshot against its schema and the frozen
// TaskSpec for one run, and returns the effective policy. It is pure: it
// performs no Git operations, no probing, and no file or state writes.
//
// The gate is fail-closed:
//   - taskId and runId must match the frozen task and run exactly;
//   - generatedAt must parse as RFC 3339;
//   - the embedded policyDigest must equal the detached digest of the
//     snapshot: the document canonicalized with its policyDigest field
//     blanked and then digested; placeholders and tampered values fail;
//   - every sources entry digest must be a well-formed sha256 digest;
//   - the task execution profile must not be below minimumExecutionProfile;
//   - a task that requires publication needs allowPublication;
//   - controlled merge is admitted only under the ADR 0032 section-1
//     planning conditions: mergePolicy "never" stays compatible and never
//     merges (a policy that grants allowMerge to a never task fails closed
//     as a mismatch), "manual" stays fail closed (not implemented), and
//     "policy" additionally requires allowPublication and allowMerge,
//     provider github, mode draft, a closed mergeMethod and a non-empty
//     de-duplicated requiredChecks set;
//   - under a control-regime snapshot, the frozen TaskSpec must satisfy the
//     acceptance floor: at least one acceptance command, a non-empty argv
//     on every command, and at least one required:true command; a legacy
//     control-less snapshot keeps the pre-issue-87 planning semantics;
//   - OpenCode is ineligible in either the preferred or fallback candidates,
//     regardless of the policy allow-list or configured registry;
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
	if isOpenCode(task.Worker.PreferredAdapter) || slices.ContainsFunc(task.Worker.FallbackAdapters, isOpenCode) {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyOpenCode)
	}
	if _, err := time.Parse(time.RFC3339, snapshot.GeneratedAt); err != nil {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyGeneratedAt)
	}
	detached, err := detachedPolicyDigest(data)
	if err != nil || detached != snapshot.PolicyDigest {
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyDigestMismatch)
	}
	for _, source := range snapshot.Sources {
		if !validSHA256Digest(source.Digest) {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicySourceDigest)
		}
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
	// ADR 0032 controlled-merge admission (planning side). The Local MVP's
	// blanket merge denial is replaced by the section-1 admission checks:
	// mergePolicy "never" stays compatible and performs no merge, "manual"
	// stays fail closed (not implemented), and "policy" is admitted only
	// when the PolicySnapshot grants both publication and merge, the
	// TaskSpec pins provider=github and mode=draft, and it declares a
	// closed mergeMethod plus a non-empty de-duplicated requiredChecks set.
	switch task.Publication.MergePolicy {
	case domain.MergePolicyNever:
		// A never task stays backward compatible, but a policy that grants
		// merge cannot silently apply to it: the grant is a mismatch and
		// fails closed instead of being ignored.
		if snapshot.Effective.AllowMerge {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMerge)
		}
	case domain.MergePolicyManual:
		// Manual merge is not implemented and remains fail closed.
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMerge)
	case domain.MergePolicyPolicy:
		if !snapshot.Effective.AllowPublication || !snapshot.Effective.AllowMerge {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMergeNotAllowed)
		}
		if task.Publication.Provider != domain.PublicationProviderGitHub || task.Publication.Mode != domain.PublicationModeDraft {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMergeProvider)
		}
		switch task.Publication.MergeMethod {
		case domain.MergeMethodMerge, domain.MergeMethodSquash, domain.MergeMethodRebase:
		default:
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMergeMethod)
		}
		if !hasNonEmptyUniqueRequiredChecks(task.Publication.RequiredChecks) {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMergeChecks)
		}
	default:
		// Any mergePolicy outside the closed enumeration fails closed.
		return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyMerge)
	}
	// Acceptance floor (issue #87): the frozen TaskSpec must carry at least
	// one acceptance command, a non-empty argv on every command, and at
	// least one required:true command, so a planned Run always has a
	// mandatory acceptance command for independent Verification. Fail-closed
	// with fixed, readable errors. The floor binds snapshots that declare
	// the control regime; a legacy control-less snapshot keeps the
	// pre-issue-87 planning semantics exactly, so legacy runs revalidated
	// through this gate are never rejected by the floor.
	if snapshot.Control != nil {
		if len(task.Acceptance.Commands) == 0 {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyAcceptanceFloorEmpty)
		}
		hasRequiredCommand := false
		for _, command := range task.Acceptance.Commands {
			if command.Required {
				hasRequiredCommand = true
				break
			}
		}
		if !hasRequiredCommand {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyAcceptanceFloorNoRequired)
		}
		// The contract semantic layer repeats the full floor (issue #87
		// rework): after the two fixed-category checks above only an empty
		// argv remains reachable, reported under its own fixed category.
		if err := contract.ValidateTaskSpecAcceptanceFloor(task); err != nil {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyAcceptanceFloorArgv)
		}
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
		EnvironmentBinding:   snapshot.EnvironmentBinding,
	}
	if snapshot.Control == nil {
		effective.AutonomyProfile = AutonomySupervised
		effective.RequiredApprovals = []string{ApprovalGatePlan, ApprovalGatePublish}
		effective.DirectPTYPolicy = DirectPTYDeny
		effective.LegacyControl = true
	} else {
		effective.AutonomyProfile = snapshot.Control.AutonomyProfile
		effective.RequiredApprovals = slices.Clone(snapshot.Control.RequiredApprovals)
		effective.AllowMediatedSteering = snapshot.Control.AllowMediatedSteering
		effective.DirectPTYPolicy = snapshot.Control.DirectPTYPolicy
		effective.MaxSteeringRounds = snapshot.Control.MaxSteeringRounds
		if !validApprovalGates(effective.AutonomyProfile, effective.RequiredApprovals, snapshot.EnvironmentBinding != nil) {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicyControlGates)
		}
		if effective.AllowMediatedSteering != (effective.MaxSteeringRounds > 0) {
			return EffectivePolicy{}, port.Permanentf("%s", ErrPolicySteering)
		}
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

// ValidateLocalDogfoodEnvironmentBinding compares the frozen policy binding
// with a fresh Core observation. A nil observation denotes a non-local caller:
// in that profile the mere presence of a local binding is contamination and
// fails closed. It performs no writes and emits only stable reason strings.
func ValidateLocalDogfoodEnvironmentBinding(data []byte, validator *contract.Validator, observation *selfidentity.LocalSelfIdentityObservationV1) error {
	if validator == nil || validator.Validate(domain.KindPolicySnapshot, data) != nil {
		return port.Permanentf("%s", ErrPolicySchemaInvalid)
	}
	var snapshot policySnapshot
	if json.Unmarshal(data, &snapshot) != nil {
		return port.Permanentf("%s", ErrPolicyMalformed)
	}
	return validateLocalDogfoodBinding(snapshot.EnvironmentBinding, observation)
}

func validateLocalDogfoodBinding(binding *LocalDogfoodEnvironmentBinding, observation *selfidentity.LocalSelfIdentityObservationV1) error {
	if observation == nil {
		if binding != nil {
			return port.Permanentf("%s", ErrPolicyLocalBindingCrossProfile)
		}
		return nil
	}
	if binding == nil {
		return port.Permanentf("%s", ErrPolicyLocalBindingMissing)
	}
	if observation.SelfProfile != selfidentity.LocalProfile || binding.SelfProfile != selfidentity.LocalProfile ||
		binding.SchemaVersion != LocalDogfoodEnvironmentBindingSchema || binding.Assurance != "ordinary-user" ||
		binding.Execution != "workspace-write" || binding.Production || binding.Publication != "none" {
		return port.Permanentf("%s", ErrPolicyLocalBindingCrossProfile)
	}
	if binding.ActivationDigest != observation.ActivationDigest || binding.IdentitySubjectDigest != observation.IdentitySubjectDigest {
		return port.Permanentf("%s", ErrPolicyLocalBindingMismatch)
	}
	return nil
}

func validateLocalDogfoodSurface(effective EffectivePolicy, task domain.TaskSpec, observation *selfidentity.LocalSelfIdentityObservationV1) error {
	if observation == nil {
		return nil
	}
	if task.Worker.ExecutionProfile != "workspace-write" || effective.ExecutionProfile != "workspace-write" ||
		effective.AllowPublication || task.Publication.Required || task.Publication.MergePolicy != domain.MergePolicyNever ||
		slices.Contains(effective.RequiredApprovals, domain.ApprovalGatePublish) {
		return port.Permanentf("%s", ErrPolicyLocalSurface)
	}
	return nil
}

func isOpenCode(adapterID string) bool {
	return strings.EqualFold(strings.TrimSpace(adapterID), "opencode")
}

const (
	ApprovalGatePlan    = "plan"
	ApprovalGatePublish = "publish"
)

func validApprovalGates(profile string, gates []string, localDogfood bool) bool {
	if profile == AutonomyAutonomous {
		return len(gates) == 0
	}
	if profile != AutonomySupervised && profile != AutonomyBalanced {
		return false
	}
	if localDogfood {
		return len(gates) == 1 && gates[0] == ApprovalGatePlan
	}
	if len(gates) != 2 {
		return false
	}
	return slices.Contains(gates, ApprovalGatePlan) && slices.Contains(gates, ApprovalGatePublish)
}

// detachedPolicyDigest computes the integrity digest of a PolicySnapshot
// document with its embedded policyDigest field detached: the document is
// decoded as a generic map, the policyDigest value is blanked to the empty
// string, the map is re-encoded, canonicalized with canonical.JSON, and the
// canonical bytes are digested with canonical.DigestBytes. An intact
// snapshot carries exactly this digest in its policyDigest field; any
// placeholder or tampered value fails the comparison. Fail-closed: any
// decode, canonicalization, or digest failure surfaces as an error.
func detachedPolicyDigest(data []byte) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	document["policyDigest"] = ""
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// hasNonEmptyUniqueRequiredChecks reports whether the frozen requiredChecks
// set is non-empty and de-duplicated with no blank entries, as required by
// the ADR 0032 merge admission (M4).
func hasNonEmptyUniqueRequiredChecks(checks []string) bool {
	if len(checks) == 0 {
		return false
	}
	seen := make(map[string]bool, len(checks))
	for _, check := range checks {
		if check == "" || seen[check] {
			return false
		}
		seen[check] = true
	}
	return true
}

// validSHA256Digest reports whether value is exactly "sha256:" followed by
// 64 lowercase hexadecimal characters.
func validSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for i := len(prefix); i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
