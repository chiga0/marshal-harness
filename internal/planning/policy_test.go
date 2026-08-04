package planning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
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
	APIVersion   string           `json:"apiVersion"`
	Kind         string           `json:"kind"`
	TaskID       string           `json:"taskId"`
	RunID        string           `json:"runId"`
	Sources      []map[string]any `json:"sources"`
	Effective    fixtureEffective `json:"effective"`
	PolicyDigest string           `json:"policyDigest"`
	GeneratedAt  string           `json:"generatedAt"`
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
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return data
}

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatalf("contract.NewValidator(): %v", err)
	}
	return validator
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
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicyNoAdapters)
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
			policy, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
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
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicyPublication)
	})

	t.Run("required and allowed", func(t *testing.T) {
		task := defaultTask()
		task.Publication.Required = true
		policy, err := ValidatePolicy(mustMarshal(t, defaultFixture()), task, "run-1", validator)
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
		if _, err := ValidatePolicy(mustMarshal(t, fixture), defaultTask(), "run-1", validator); err != nil {
			t.Fatalf("ValidatePolicy() error = %v, want nil", err)
		}
	})
}

func TestValidatePolicyMerge(t *testing.T) {
	validator := newValidator(t)
	tests := []struct {
		name        string
		mergePolicy string
		allowMerge  bool
		wantErr     string
	}{
		{"never and denied is accepted", "never", false, ""},
		{"manual merge rejected", "manual", false, ErrPolicyMerge},
		{"policy merge rejected", "policy", false, ErrPolicyMerge},
		{"allowMerge cannot relax the mvp", "never", true, ErrPolicyMerge},
		{"both conditions rejected", "manual", true, ErrPolicyMerge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := defaultFixture()
			fixture.Effective.AllowMerge = test.allowMerge
			task := defaultTask()
			task.Publication.MergePolicy = test.mergePolicy
			policy, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
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
		assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicyNoAdapters)
	})

	t.Run("no overlap fails closed", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowedAdapters = []string{"adapter-x"}
		assertPolicyError(t, mustMarshal(t, fixture), defaultTask(), "run-1", validator, ErrPolicyNoCandidates)
	})

	t.Run("empty preferred adapter fails closed", func(t *testing.T) {
		task := defaultTask()
		task.Worker.PreferredAdapter = ""
		assertPolicyError(t, mustMarshal(t, defaultFixture()), task, "run-1", validator, ErrPolicyPreferredEmpty)
	})

	t.Run("fallback denied keeps only preferred", func(t *testing.T) {
		fixture := defaultFixture()
		fixture.Effective.AllowFallbackWorkers = false
		task := defaultTask()
		task.Worker.FallbackAdapters = []string{"adapter-b", "adapter-c"}
		policy, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
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
		assertPolicyError(t, mustMarshal(t, fixture), task, "run-1", validator, ErrPolicyNoCandidates)
	})

	t.Run("fallback allowed preserves taskspec order", func(t *testing.T) {
		task := defaultTask()
		task.Worker.PreferredAdapter = "adapter-b"
		task.Worker.FallbackAdapters = []string{"adapter-c", "adapter-a", "adapter-b"}
		policy, err := ValidatePolicy(mustMarshal(t, defaultFixture()), task, "run-1", validator)
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
		policy, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
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

	policy, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
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

	task := defaultTask()
	task.Publication.MergePolicy = "manual"
	_, err := ValidatePolicy(mustMarshal(t, fixture), task, "run-1", validator)
	if err == nil {
		t.Fatal("ValidatePolicy() error = nil, want merge denial")
	}
	for _, leaked := range []string{
		"/secret/policy/source.yaml",
		"LEAKY_VARIABLE",
		fixture.PolicyDigest,
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
