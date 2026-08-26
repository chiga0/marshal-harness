package planning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

type fixtureEffective struct {
	MinimumExecutionProfile      string   `json:"minimumExecutionProfile"`
	RequireEnforcedNetworkPolicy bool     `json:"requireEnforcedNetworkPolicy"`
	NetworkPolicy                string   `json:"networkPolicy"`
	AllowFallbackWorkers         bool     `json:"allowFallbackWorkers"`
	AllowWorkerSubagents         bool     `json:"allowWorkerSubagents"`
	AllowPublication             bool     `json:"allowPublication"`
	AllowMerge                   bool     `json:"allowMerge"`
	AllowGateWaivers             bool     `json:"allowGateWaivers"`
	AllowedAdapters              []string `json:"allowedAdapters"`
	EnvironmentAllowlist         []string `json:"environmentAllowlist"`
	RetentionDays                int      `json:"retentionDays"`
}

type fixtureSnapshot struct {
	APIVersion         string                          `json:"apiVersion"`
	Kind               string                          `json:"kind"`
	TaskID             string                          `json:"taskId"`
	RunID              string                          `json:"runId"`
	Sources            []map[string]any                `json:"sources"`
	Effective          fixtureEffective                `json:"effective"`
	Control            *fixtureControl                 `json:"control,omitempty"`
	EnvironmentBinding *LocalDogfoodEnvironmentBinding `json:"environmentBinding,omitempty"`
	PolicyDigest       string                          `json:"policyDigest"`
	GeneratedAt        string                          `json:"generatedAt"`
}

func localDogfoodObservationFixture() selfidentity.LocalSelfIdentityObservationV1 {
	return selfidentity.LocalSelfIdentityObservationV1{
		SchemaVersion:         selfidentity.ObservationSchema,
		ActivationDigest:      "sha256:" + strings.Repeat("a", 64),
		IdentitySubjectDigest: "sha256:" + strings.Repeat("b", 64),
		SelfProfile:           selfidentity.LocalProfile,
	}
}

func localDogfoodPolicyFixture() fixtureSnapshot {
	fixture := defaultFixture()
	observation := localDogfoodObservationFixture()
	fixture.Effective.AllowPublication = false
	fixture.Control.RequiredApprovals = []string{ApprovalGatePlan}
	fixture.EnvironmentBinding = &LocalDogfoodEnvironmentBinding{
		SchemaVersion:         LocalDogfoodEnvironmentBindingSchema,
		SelfProfile:           selfidentity.LocalProfile,
		ActivationDigest:      observation.ActivationDigest,
		IdentitySubjectDigest: observation.IdentitySubjectDigest,
		Assurance:             "ordinary-user", Execution: "workspace-write", Production: false, Publication: "none",
	}
	return fixture
}

type fixtureControl struct {
	AutonomyProfile       string   `json:"autonomyProfile"`
	RequiredApprovals     []string `json:"requiredApprovals"`
	AllowMediatedSteering bool     `json:"allowMediatedSteering"`
	DirectPTYPolicy       string   `json:"directPtyPolicy"`
	MaxSteeringRounds     uint     `json:"maxSteeringRounds"`
}

func defaultFixture() fixtureSnapshot {
	return fixtureSnapshot{
		APIVersion: "marshal.dev/v1alpha1",
		Kind:       "PolicySnapshot",
		TaskID:     "task-1",
		RunID:      "run-1",
		Sources: []map[string]any{{
			"scope":    "builtin",
			"digest":   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"required": true,
		}},
		Effective: fixtureEffective{
			MinimumExecutionProfile:      "workspace-write",
			RequireEnforcedNetworkPolicy: false,
			NetworkPolicy:                "unenforced",
			AllowFallbackWorkers:         true,
			AllowWorkerSubagents:         false,
			AllowPublication:             true,
			AllowMerge:                   false,
			AllowGateWaivers:             false,
			AllowedAdapters:              []string{"adapter-a", "adapter-b", "adapter-c"},
			EnvironmentAllowlist:         []string{"PATH", "LANG"},
			RetentionDays:                30,
		},
		Control: &fixtureControl{
			AutonomyProfile:       AutonomyBalanced,
			RequiredApprovals:     []string{ApprovalGatePlan, ApprovalGatePublish},
			AllowMediatedSteering: true,
			DirectPTYPolicy:       DirectPTYRecordAndReverify,
			MaxSteeringRounds:     4,
		},
		PolicyDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		GeneratedAt:  "2026-08-03T10:00:01Z",
	}
}

func defaultTask() domain.TaskSpec {
	return domain.TaskSpec{
		Metadata: domain.TaskMetadata{ID: "task-1"},
		Worker: domain.TaskWorker{
			PreferredAdapter: "adapter-a",
			FallbackAdapters: []string{"adapter-b"},
			ExecutionProfile: "workspace-write",
			SessionPolicy:    "ephemeral",
		},
		Publication: domain.TaskPublication{
			Required:    false,
			MergePolicy: "never",
		},
		// The acceptance floor (issue #87) requires every planned task to
		// declare at least one acceptance command with at least one
		// required:true entry; the default fixture satisfies the floor.
		Acceptance: domain.TaskAcceptance{
			Commands: []domain.TaskCommand{{
				ID:             "acceptance-check",
				Argv:           []string{"true"},
				CWD:            ".",
				TimeoutSeconds: 60,
				Required:       true,
			}},
		},
	}
}

// optionalTaskCommand builds a non-required acceptance command for floor
// fixtures that need extra commands without satisfying the required leg.
func optionalTaskCommand(id string) domain.TaskCommand {
	return domain.TaskCommand{ID: id, Argv: []string{"true"}, CWD: ".", TimeoutSeconds: 60}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return data
}

// sealPolicyFixture renders fixture and stamps a freshly computed detached
// policyDigest onto it, exactly the way a policy issuer must seal it so the
// production integrity gate accepts it.
func sealPolicyFixture(t *testing.T, fixture fixtureSnapshot) []byte {
	t.Helper()
	fixture.PolicyDigest = ""
	digest, err := detachedPolicyDigest(mustMarshal(t, fixture))
	if err != nil {
		t.Fatalf("compute detached policy digest: %v", err)
	}
	fixture.PolicyDigest = digest
	return mustMarshal(t, fixture)
}

// sealPolicyDocument is the map-based counterpart of sealPolicyFixture for
// fixtures that mutate a decoded document. It recomputes the detached
// policyDigest at test runtime; fixtures must never hardcode a placeholder.
func sealPolicyDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	document["policyDigest"] = ""
	digest, err := detachedPolicyDigest(mustMarshal(t, document))
	if err != nil {
		t.Fatalf("compute detached policy digest: %v", err)
	}
	document["policyDigest"] = digest
	return mustMarshal(t, document)
}

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatalf("contract.NewValidator(): %v", err)
	}
	return validator
}

func TestLocalDogfoodEnvironmentBindingIsClosedAndProfileScoped(t *testing.T) {
	validator := newValidator(t)
	observation := localDogfoodObservationFixture()
	fixture := localDogfoodPolicyFixture()
	sealed := sealPolicyFixture(t, fixture)

	effective, err := ValidatePolicy(sealed, defaultTask(), fixture.RunID, validator)
	if err != nil {
		t.Fatalf("ValidatePolicy local binding: %v", err)
	}
	if effective.EnvironmentBinding == nil || effective.EnvironmentBinding.IdentitySubjectDigest != observation.IdentitySubjectDigest {
		t.Fatalf("local environment binding not preserved: %#v", effective.EnvironmentBinding)
	}
	if err := ValidateLocalDogfoodEnvironmentBinding(sealed, validator, &observation); err != nil {
		t.Fatalf("matching local binding rejected: %v", err)
	}
	if err := ValidateLocalDogfoodEnvironmentBinding(sealed, validator, nil); err == nil || err.Error() != ErrPolicyLocalBindingCrossProfile {
		t.Fatalf("non-local caller err=%v, want %q", err, ErrPolicyLocalBindingCrossProfile)
	}

	mismatch := observation
	mismatch.IdentitySubjectDigest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidateLocalDogfoodEnvironmentBinding(sealed, validator, &mismatch); err == nil || err.Error() != ErrPolicyLocalBindingMismatch {
		t.Fatalf("identity mismatch err=%v, want %q", err, ErrPolicyLocalBindingMismatch)
	}

	legacy := defaultFixture()
	legacy.Effective.AllowPublication = false
	legacyRaw := sealPolicyFixture(t, legacy)
	if err := ValidateLocalDogfoodEnvironmentBinding(legacyRaw, validator, &observation); err == nil || err.Error() != ErrPolicyLocalBindingMissing {
		t.Fatalf("missing binding err=%v, want %q", err, ErrPolicyLocalBindingMissing)
	}

	var partial map[string]any
	if err := json.Unmarshal(sealed, &partial); err != nil {
		t.Fatal(err)
	}
	delete(partial["environmentBinding"].(map[string]any), "publication")
	partialRaw := sealPolicyDocument(t, partial)
	if err := ValidateLocalDogfoodEnvironmentBinding(partialRaw, validator, &observation); err == nil || err.Error() != ErrPolicySchemaInvalid {
		t.Fatalf("partial binding err=%v, want %q", err, ErrPolicySchemaInvalid)
	}
}

func TestLocalDogfoodSurfaceRejectsPublicationAuthority(t *testing.T) {
	observation := localDogfoodObservationFixture()
	fixture := localDogfoodPolicyFixture()
	validator := newValidator(t)
	effective, err := ValidatePolicy(sealPolicyFixture(t, fixture), defaultTask(), fixture.RunID, validator)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLocalDogfoodSurface(effective, defaultTask(), &observation); err != nil {
		t.Fatalf("publication:none local surface rejected: %v", err)
	}
	effective.AllowPublication = true
	if err := validateLocalDogfoodSurface(effective, defaultTask(), &observation); err == nil || err.Error() != ErrPolicyLocalSurface {
		t.Fatalf("publication grant err=%v, want %q", err, ErrPolicyLocalSurface)
	}
	effective.AllowPublication = false
	hardened := defaultTask()
	hardened.Worker.ExecutionProfile = "hardened"
	effective.ExecutionProfile = "hardened"
	if err := validateLocalDogfoodSurface(effective, hardened, &observation); err == nil || err.Error() != ErrPolicyLocalSurface {
		t.Fatalf("hardened local surface err=%v, want %q", err, ErrPolicyLocalSurface)
	}
}

func TestValidatePolicySchemaInvalid(t *testing.T) {
	validator := newValidator(t)
	task := defaultTask()

	t.Run("invalid json", func(t *testing.T) {
		assertPolicyError(t, []byte("{"), task, "run-1", validator, ErrPolicySchemaInvalid)
	})

	t.Run("missing required effective field", func(t *testing.T) {
		fixture := defaultFixture()
		raw := mustMarshal(t, fixture)
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal fixture: %v", err)
		}
		effective := decoded["effective"].(map[string]any)
		delete(effective, "retentionDays")
		trimmed, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("remarshal fixture: %v", err)
		}
		assertPolicyError(t, trimmed, task, "run-1", validator, ErrPolicySchemaInvalid)
	})

	t.Run("unknown additional property", func(t *testing.T) {
		fixture := defaultFixture()
		raw := mustMarshal(t, fixture)
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal fixture: %v", err)
		}
		decoded["extra"] = "value"
		extra, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("remarshal fixture: %v", err)
		}
		assertPolicyError(t, extra, task, "run-1", validator, ErrPolicySchemaInvalid)
	})

	t.Run("invalid minimum execution profile", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.MinimumExecutionProfile = "root"
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicySchemaInvalid)
	})

	t.Run("empty allowlist is schema valid but gate rejected", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = []string{}
		assertPolicyError(t, sealPolicyFixture(t, fixture), task, "run-1", validator, ErrPolicyNoAdapters)
	})

	t.Run("nil validator", func(t *testing.T) {
		assertPolicyError(t, mustMarshal(t, defaultFixture()), task, "run-1", nil, ErrPolicyNilValidator)
	})
}

func TestValidatePolicyIdentity(t *testing.T) {
	validator := newValidator(t)

	t.Run("task id mismatch", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.TaskID = "task-other"
		assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicyTaskMismatch)
	})

	t.Run("run id mismatch", func(t *testing.T) {
		assertPolicyError(t, mustMarshal(t, defaultFixture()), defaultTask(), "run-other", validator, ErrPolicyRunMismatch)
	})
}

func TestValidatePolicyRejectsOpenCodeInAnyTaskCandidate(t *testing.T) {
	validator := newValidator(t)
	for _, mutate := range []func(*domain.TaskSpec){
		func(task *domain.TaskSpec) { task.Worker.PreferredAdapter = "opencode" },
		func(task *domain.TaskSpec) {
			task.Worker.FallbackAdapters = append(task.Worker.FallbackAdapters, "OpenCode")
		},
	} {
		task := defaultTask()
		mutate(&task)
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = append(fixture.Effective.AllowedAdapters, "opencode")
		assertPolicyError(t, sealPolicyFixture(t, fixture), task, "run-1", validator, ErrPolicyOpenCode)
	}
}

func TestValidatePolicyGeneratedAt(t *testing.T) {
	validator := newValidator(t)

	t.Run("schema rejects a non timestamp", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.GeneratedAt = "not-a-timestamp"
		assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicySchemaInvalid)
	})

	// The schema's date-time assertion accepts an RFC 3339 leap second, but
	// Go's RFC 3339 parser does not, so this value exercises the gate
	// itself instead of the schema.
	t.Run("leap second fails closed at the gate", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.GeneratedAt = "2026-08-03T23:59:60Z"
		assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicyGeneratedAt)
	})
}

func TestValidatePolicyDigestIntegrity(t *testing.T) {
	validator := newValidator(t)
	task := defaultTask()

	t.Run("all-zero placeholder rejected", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicyDigestMismatch)
	})

	t.Run("forged placeholder rejected", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.PolicyDigest = "sha256:" + strings.Repeat("c", 64)
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicyDigestMismatch)
	})

	t.Run("computed but altered digest rejected", func(t *testing.T) {
		sealed := sealPolicyFixture(t, defaultFixture())
		var document map[string]any
		if err := json.Unmarshal(sealed, &document); err != nil {
			t.Fatalf("unmarshal sealed fixture: %v", err)
		}
		digest, ok := document["policyDigest"].(string)
		if !ok || len(digest) == 0 {
			t.Fatalf("sealed fixture lost policyDigest: %#v", document["policyDigest"])
		}
		replacement := byte('0')
		if digest[len(digest)-1] == '0' {
			replacement = '1'
		}
		document["policyDigest"] = digest[:len(digest)-1] + string(replacement)
		assertPolicyError(t, mustMarshal(t, document), task, "run-1", validator, ErrPolicyDigestMismatch)
	})

	t.Run("mutation without reseal rejected", func(t *testing.T) {
		sealed := sealPolicyFixture(t, defaultFixture())
		var document map[string]any
		if err := json.Unmarshal(sealed, &document); err != nil {
			t.Fatalf("unmarshal sealed fixture: %v", err)
		}
		document["effective"].(map[string]any)["retentionDays"] = float64(31)
		assertPolicyError(t, mustMarshal(t, document), task, "run-1", validator, ErrPolicyDigestMismatch)
	})

	t.Run("correct detached digest accepted", func(t *testing.T) {
		policy, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), task, "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if policy.TaskID != "task-1" || policy.RunID != "run-1" {
			t.Fatalf("identity = (%q, %q), want (task-1, run-1)", policy.TaskID, policy.RunID)
		}
	})
}

func TestValidatePolicySourceDigest(t *testing.T) {
	validator := newValidator(t)

	// The schema's digest pattern rejects malformed digests first; the gate
	// repeats the shape check as defense in depth once schema validation is
	// past. Both layers fail closed with a fixed error.
	for _, test := range []struct {
		name    string
		digest  string
		wantErr string
	}{
		{"missing prefix", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ErrPolicySchemaInvalid},
		{"short hex", "sha256:bbbb", ErrPolicySchemaInvalid},
		{"uppercase hex", "sha256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", ErrPolicySchemaInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			fixture.Sources[0]["digest"] = test.digest
			assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, test.wantErr)
		})
	}

	t.Run("well-formed source digest accepted", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Sources = append(fixture.Sources, map[string]any{
			"scope":    "repository",
			"path":     "docs/policy.md",
			"digest":   "sha256:" + strings.Repeat("d", 64),
			"required": true,
		})
		if _, err := ValidatePolicy(sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator); err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
	})
}

func TestValidSHA256Digest(t *testing.T) {
	for _, value := range []string{
		"sha256:" + strings.Repeat("0", 64),
		"sha256:" + strings.Repeat("a", 64),
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if !validSHA256Digest(value) {
			t.Errorf("validSHA256Digest(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"",
		"sha256:",
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("g", 64),
		"md5:" + strings.Repeat("a", 64),
		strings.Repeat("a", 64),
	} {
		if validSHA256Digest(value) {
			t.Errorf("validSHA256Digest(%q) = true, want false", value)
		}
	}
}

func TestValidatePolicyControl(t *testing.T) {
	validator := newValidator(t)

	t.Run("balanced profile", func(t *testing.T) {
		policy, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), defaultTask(), "run-1", validator)
		if err != nil {
			t.Fatal(err)
		}
		if policy.LegacyControl || policy.AutonomyProfile != AutonomyBalanced ||
			!slices.Equal(policy.RequiredApprovals, []string{ApprovalGatePlan, ApprovalGatePublish}) ||
			!policy.AllowMediatedSteering || policy.DirectPTYPolicy != DirectPTYRecordAndReverify || policy.MaxSteeringRounds != 4 {
			t.Fatalf("control policy = %+v", policy)
		}
	})

	t.Run("missing control is legacy supervised", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Control = nil
		sealed := sealPolicyFixture(t, fixture)
		policy, err := ValidatePolicy(sealed, defaultTask(), "run-1", validator)
		if err != nil {
			t.Fatal(err)
		}
		if !policy.LegacyControl || policy.AutonomyProfile != AutonomySupervised || policy.AllowMediatedSteering ||
			policy.DirectPTYPolicy != DirectPTYDeny || policy.MaxSteeringRounds != 0 ||
			!slices.Equal(policy.RequiredApprovals, []string{ApprovalGatePlan, ApprovalGatePublish}) {
			t.Fatalf("legacy control policy = %+v", policy)
		}
		policy.RequiredApprovals[0] = "mutated"
		again, err := ValidatePolicy(sealed, defaultTask(), "run-1", validator)
		if err != nil || again.RequiredApprovals[0] != ApprovalGatePlan {
			t.Fatalf("legacy approval slice was not isolated: %+v, %v", again.RequiredApprovals, err)
		}
	})

	for _, test := range []struct {
		name    string
		mutate  func(*fixtureControl)
		wantErr string
	}{
		{"balanced missing publish", func(control *fixtureControl) { control.RequiredApprovals = []string{ApprovalGatePlan} }, ErrPolicyControlGates},
		{"supervised missing plan", func(control *fixtureControl) {
			control.AutonomyProfile = AutonomySupervised
			control.RequiredApprovals = []string{ApprovalGatePublish}
		}, ErrPolicyControlGates},
		{"autonomous has approval", func(control *fixtureControl) {
			control.AutonomyProfile = AutonomyAutonomous
		}, ErrPolicyControlGates},
		{"steering disabled with budget", func(control *fixtureControl) { control.AllowMediatedSteering = false }, ErrPolicySteering},
		{"steering enabled without budget", func(control *fixtureControl) { control.MaxSteeringRounds = 0 }, ErrPolicySteering},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			test.mutate(fixture.Control)
			assertPolicyError(t, sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator, test.wantErr)
		})
	}

	t.Run("autonomous without approvals", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Control.AutonomyProfile = AutonomyAutonomous
		fixture.Control.RequiredApprovals = []string{}
		if _, err := ValidatePolicy(sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*fixtureControl)
	}{
		{"unknown autonomy profile", func(control *fixtureControl) { control.AutonomyProfile = "unbounded" }},
		{"unknown direct PTY policy", func(control *fixtureControl) { control.DirectPTYPolicy = "unrecorded" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			test.mutate(fixture.Control)
			assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicySchemaInvalid)
		})
	}
}

func TestValidatePolicyProfile(t *testing.T) {
	validator := newValidator(t)
	tests := []struct {
		name        string
		taskProfile string
		minimum     string
		wantErr     string
		wantProfile string
	}{
		{"equal profile accepted", "workspace-write", "workspace-write", "", "workspace-write"},
		{"higher task profile accepted", "hardened", "workspace-write", "", "hardened"},
		{"lower task profile rejected", "workspace-write", "hardened", ErrPolicyProfile, ""},
		{"read-only below workspace-write", "read-only", "workspace-write", ErrPolicyProfile, ""},
		{"hardened above read-only", "hardened", "read-only", "", "hardened"},
		{"unknown task profile rejected", "root", "read-only", ErrPolicyProfileUnknown, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			fixture.Effective.MinimumExecutionProfile = test.minimum
			task := defaultTask()
			task.Worker.ExecutionProfile = test.taskProfile
			policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
			if test.wantErr != "" {
				assertError(t, err, test.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ValidatePolicy() error = %v, want nil", err)
			}
			if policy.ExecutionProfile != test.wantProfile {
				t.Fatalf("ExecutionProfile = %q, want %q", policy.ExecutionProfile, test.wantProfile)
			}
		})
	}
}

func TestValidatePolicyPublication(t *testing.T) {
	validator := newValidator(t)

	t.Run("required but denied", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowPublication = false
		task := defaultTask()
		task.Publication.Required = true
		assertPolicyError(t, sealPolicyFixture(t, fixture), task, "run-1", validator, ErrPolicyPublication)
	})

	t.Run("required and allowed", func(t *testing.T) {
		task := defaultTask()
		task.Publication.Required = true
		policy, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), task, "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if !policy.AllowPublication {
			t.Fatal("AllowPublication = false, want true")
		}
	})

	t.Run("not required and denied is allowed", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowPublication = false
		if _, err := ValidatePolicy(sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator); err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
	})
}

func TestValidatePolicyAcceptanceFloor(t *testing.T) {
	validator := newValidator(t)

	t.Run("no commands fails closed", func(t *testing.T) {
		task := defaultTask()
		task.Acceptance.Commands = nil
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyAcceptanceFloorEmpty)
	})

	t.Run("empty commands slice fails closed", func(t *testing.T) {
		task := defaultTask()
		task.Acceptance.Commands = []domain.TaskCommand{}
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyAcceptanceFloorEmpty)
	})

	t.Run("commands without any required fail closed", func(t *testing.T) {
		task := defaultTask()
		for index := range task.Acceptance.Commands {
			task.Acceptance.Commands[index].Required = false
		}
		task.Acceptance.Commands = append(task.Acceptance.Commands, optionalTaskCommand("optional-extra"))
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyAcceptanceFloorNoRequired)
	})

	t.Run("single required command satisfies the floor", func(t *testing.T) {
		policy, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), defaultTask(), "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if policy.TaskID != "task-1" || policy.RunID != "run-1" {
			t.Fatalf("identity = (%q, %q), want (task-1, run-1)", policy.TaskID, policy.RunID)
		}
	})

	t.Run("one required among optional commands satisfies the floor", func(t *testing.T) {
		task := defaultTask()
		task.Acceptance.Commands = append(task.Acceptance.Commands, optionalTaskCommand("optional-extra"), optionalTaskCommand("optional-more"))
		if _, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), task, "run-1", validator); err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
	})

	t.Run("command with empty argv fails closed", func(t *testing.T) {
		task := defaultTask()
		task.Acceptance.Commands[0].Argv = nil
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyAcceptanceFloorArgv)
	})

	t.Run("every command argv must be non-empty", func(t *testing.T) {
		task := defaultTask()
		task.Acceptance.Commands = append(task.Acceptance.Commands, optionalTaskCommand("optional-empty"))
		task.Acceptance.Commands[1].Argv = []string{}
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyAcceptanceFloorArgv)
	})

	// Backward compatibility (issue #87 rework): the floor binds snapshots
	// that declare the control regime; a legacy control-less snapshot keeps
	// the pre-issue-87 planning semantics exactly, so legacy runs — like the
	// embedded sandbox E2E fixture — are never rejected by the floor.
	t.Run("legacy control-less snapshot keeps pre-floor planning semantics", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Control = nil
		task := defaultTask()
		task.Acceptance.Commands = nil
		policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
		if err != nil {
			t.Fatalf("legacy control-less snapshot must not enforce the acceptance floor: %v", err)
		}
		if !policy.LegacyControl {
			t.Fatalf("policy.LegacyControl = false, want true for a control-less snapshot")
		}
	})
}

func TestValidatePolicyMerge(t *testing.T) {
	validator := newValidator(t)

	// policyMergeTask returns a TaskSpec whose publication declaration is
	// fully shaped for the ADR 0032 policy-merge admission; individual tests
	// override one field at a time to exercise each fail-closed condition.
	policyMergeTask := func() domain.TaskSpec {
		task := defaultTask()
		task.Publication.Provider = domain.PublicationProviderGitHub
		task.Publication.Mode = domain.PublicationModeDraft
		task.Publication.MergePolicy = domain.MergePolicyPolicy
		task.Publication.MergeMethod = domain.MergeMethodSquash
		task.Publication.RequiredChecks = []string{"ci/test"}
		return task
	}

	tests := []struct {
		name        string
		mergePolicy string
		allowMerge  bool
		allowPub    bool
		provider    string
		mode        string
		mergeMethod string
		checks      []string
		wantErr     string
	}{
		{"never and denied is accepted", "never", false, true, "github", "draft", "", nil, ""},
		{"never with allowMerge rejected", "never", true, true, "github", "draft", "", nil, ErrPolicyMerge},
		{"manual merge rejected", "manual", false, true, "github", "draft", "", nil, ErrPolicyMerge},
		{"manual with allowMerge still rejected", "manual", true, true, "github", "draft", "squash", []string{"ci/test"}, ErrPolicyMerge},
		{"policy without allowMerge rejected", "policy", false, true, "github", "draft", "squash", []string{"ci/test"}, ErrPolicyMergeNotAllowed},
		{"policy without allowPublication rejected", "policy", true, false, "github", "draft", "squash", []string{"ci/test"}, ErrPolicyMergeNotAllowed},
		{"policy with non-github provider rejected", "policy", true, true, "gitlab", "draft", "squash", []string{"ci/test"}, ErrPolicyMergeProvider},
		{"policy with non-draft mode rejected", "policy", true, true, "github", "ready", "squash", []string{"ci/test"}, ErrPolicyMergeProvider},
		{"policy with missing mergeMethod rejected", "policy", true, true, "github", "draft", "", []string{"ci/test"}, ErrPolicyMergeMethod},
		{"policy with invalid mergeMethod rejected", "policy", true, true, "github", "draft", "fast-forward", []string{"ci/test"}, ErrPolicyMergeMethod},
		{"policy with empty requiredChecks rejected", "policy", true, true, "github", "draft", "squash", nil, ErrPolicyMergeChecks},
		{"policy with duplicate requiredChecks rejected", "policy", true, true, "github", "draft", "squash", []string{"ci/test", "ci/test"}, ErrPolicyMergeChecks},
		{"policy merge admitted", "policy", true, true, "github", "draft", "squash", []string{"ci/test"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			fixture.Effective.AllowMerge = test.allowMerge
			fixture.Effective.AllowPublication = test.allowPub
			task := policyMergeTask()
			task.Publication.MergePolicy = test.mergePolicy
			task.Publication.Provider = test.provider
			task.Publication.Mode = test.mode
			task.Publication.MergeMethod = test.mergeMethod
			task.Publication.RequiredChecks = test.checks
			policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
			if test.wantErr != "" {
				assertError(t, err, test.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ValidatePolicy() error = %v, want nil", err)
			}
			if policy.AllowPublication != fixture.Effective.AllowPublication {
				t.Fatalf("AllowPublication = %v, want %v", policy.AllowPublication, fixture.Effective.AllowPublication)
			}
		})
	}
}

func TestValidatePolicyAdapters(t *testing.T) {
	validator := newValidator(t)

	t.Run("empty allowlist fails closed", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = []string{}
		assertPolicyError(t, sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator, ErrPolicyNoAdapters)
	})

	t.Run("no overlap fails closed", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = []string{"adapter-x"}
		assertPolicyError(t, sealPolicyFixture(t, fixture), defaultTask(), "run-1", validator, ErrPolicyNoCandidates)
	})

	t.Run("empty preferred adapter fails closed", func(t *testing.T) {
		task := defaultTask()
		task.Worker.PreferredAdapter = ""
		assertPolicyError(t, sealPolicyFixture(t, defaultFixture()), task, "run-1", validator, ErrPolicyPreferredEmpty)
	})

	t.Run("fallback denied keeps only preferred", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowFallbackWorkers = false
		task := defaultTask()
		task.Worker.FallbackAdapters = []string{"adapter-b", "adapter-c"}
		policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if policy.AllowFallbackWorkers {
			t.Fatal("AllowFallbackWorkers = true, want false")
		}
		if policy.PreferredAdapter != "adapter-a" {
			t.Fatalf("PreferredAdapter = %q, want %q", policy.PreferredAdapter, "adapter-a")
		}
		if len(policy.FallbackAdapters) != 0 {
			t.Fatalf("FallbackAdapters = %v, want empty", policy.FallbackAdapters)
		}
	})

	t.Run("fallback denied with preferred not allowed fails closed", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowFallbackWorkers = false
		fixture.Effective.AllowedAdapters = []string{"adapter-b"}
		task := defaultTask()
		task.Worker.FallbackAdapters = []string{"adapter-b"}
		assertPolicyError(t, sealPolicyFixture(t, fixture), task, "run-1", validator, ErrPolicyNoCandidates)
	})

	t.Run("fallback allowed preserves taskspec order", func(t *testing.T) {
		task := defaultTask()
		task.Worker.PreferredAdapter = "adapter-b"
		task.Worker.FallbackAdapters = []string{"adapter-c", "adapter-a", "adapter-b"}
		policy, err := ValidatePolicy(sealPolicyFixture(t, defaultFixture()), task, "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if policy.PreferredAdapter != "adapter-b" {
			t.Fatalf("PreferredAdapter = %q, want %q", policy.PreferredAdapter, "adapter-b")
		}
		if !slices.Equal(policy.FallbackAdapters, []string{"adapter-c", "adapter-a"}) {
			t.Fatalf("FallbackAdapters = %v, want %v", policy.FallbackAdapters, []string{"adapter-c", "adapter-a"})
		}
	})

	t.Run("only allowed fallback survives as overlap", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = []string{"adapter-c"}
		task := defaultTask()
		task.Worker.FallbackAdapters = []string{"adapter-c"}
		policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
		if err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
		if !slices.Equal(policy.FallbackAdapters, []string{"adapter-c"}) {
			t.Fatalf("FallbackAdapters = %v, want %v", policy.FallbackAdapters, []string{"adapter-c"})
		}
	})
}

func TestValidatePolicySuccess(t *testing.T) {
	validator := newValidator(t)
	task := defaultTask()
	task.Worker.ExecutionProfile = "hardened"
	task.Worker.FallbackAdapters = []string{"adapter-c", "adapter-b"}
	task.Publication.Required = true
	fixture := defaultFixture()
	fixture.Effective.MinimumExecutionProfile = "workspace-write"

	policy, err := ValidatePolicy(sealPolicyFixture(t, fixture), task, "run-1", validator)
	if err != nil {
		t.Fatalf("ValidatePolicy() error = %v, want nil", err)
	}
	if policy.TaskID != "task-1" || policy.RunID != "run-1" {
		t.Fatalf("identity = (%q, %q), want (task-1, run-1)", policy.TaskID, policy.RunID)
	}
	if policy.ExecutionProfile != "hardened" {
		t.Fatalf("ExecutionProfile = %q, want hardened", policy.ExecutionProfile)
	}
	if !slices.Equal(policy.AllowedAdapters, []string{"adapter-a", "adapter-b", "adapter-c"}) {
		t.Fatalf("AllowedAdapters = %v", policy.AllowedAdapters)
	}
	if !policy.AllowFallbackWorkers {
		t.Fatal("AllowFallbackWorkers = false, want true")
	}
	if !slices.Equal(policy.EnvironmentAllowlist, []string{"PATH", "LANG"}) {
		t.Fatalf("EnvironmentAllowlist = %v", policy.EnvironmentAllowlist)
	}
	if policy.RetentionDays != 30 {
		t.Fatalf("RetentionDays = %d, want 30", policy.RetentionDays)
	}
	if !policy.AllowPublication {
		t.Fatal("AllowPublication = false, want true")
	}
	if policy.NetworkPolicy != "unenforced" {
		t.Fatalf("NetworkPolicy = %q, want unenforced", policy.NetworkPolicy)
	}
	// TaskSpec order is preserved and no adapter is added implicitly.
	if policy.PreferredAdapter != "adapter-a" {
		t.Fatalf("PreferredAdapter = %q, want adapter-a", policy.PreferredAdapter)
	}
	if !slices.Equal(policy.FallbackAdapters, []string{"adapter-c", "adapter-b"}) {
		t.Fatalf("FallbackAdapters = %v, want [adapter-c adapter-b]", policy.FallbackAdapters)
	}

	request := policy.SelectionRequest()
	if request.PreferredAdapter != "adapter-a" {
		t.Fatalf("SelectionRequest.PreferredAdapter = %q, want adapter-a", request.PreferredAdapter)
	}
	if !slices.Equal(request.FallbackAdapters, []string{"adapter-c", "adapter-b"}) {
		t.Fatalf("SelectionRequest.FallbackAdapters = %v", request.FallbackAdapters)
	}
	if !slices.Equal(request.AllowedAdapters, []string{"adapter-a", "adapter-b", "adapter-c"}) {
		t.Fatalf("SelectionRequest.AllowedAdapters = %v", request.AllowedAdapters)
	}
}

func TestValidatePolicyDoesNotLeakSnapshotContent(t *testing.T) {
	validator := newValidator(t)
	// A snapshot with distinctive source paths, digests, and environment
	// values must never surface them in any gate error.
	fixture := defaultFixture()
	fixture.Sources[0]["path"] = "/secret/policy/source.yaml"
	fixture.Effective.EnvironmentAllowlist = []string{"PATH", "LEAKY_VARIABLE"}
	sealed := sealPolicyFixture(t, fixture)

	task := defaultTask()
	task.Publication.MergePolicy = "manual"
	_, err := ValidatePolicy(sealed, task, "run-1", validator)
	if err == nil {
		t.Fatal("ValidatePolicy() error = nil, want merge denial")
	}
	var resealed fixtureSnapshot
	if err := json.Unmarshal(sealed, &resealed); err != nil {
		t.Fatalf("unmarshal sealed fixture: %v", err)
	}
	for _, leaked := range []string{
		"/secret/policy/source.yaml",
		"LEAKY_VARIABLE",
		resealed.PolicyDigest,
		fixture.Sources[0]["digest"].(string),
	} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error %q leaks snapshot content %q", err.Error(), leaked)
		}
	}
	if err.Error() != ErrPolicyMerge {
		t.Fatalf("error = %q, want fixed category %q", err.Error(), ErrPolicyMerge)
	}
}

func assertPolicyError(t *testing.T, raw []byte, task domain.TaskSpec, runID string, validator *contract.Validator, want string) {
	t.Helper()
	_, err := ValidatePolicy(raw, task, runID, validator)
	assertError(t, err, want)
}

func assertError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ValidatePolicy() error = nil, want %q", want)
	}
	if err.Error() != want {
		t.Fatalf("ValidatePolicy() error = %q, want %q", err.Error(), want)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("ValidatePolicy() error %q is not permanent", err.Error())
	}
}
