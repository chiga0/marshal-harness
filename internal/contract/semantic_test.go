package contract

import (
	"encoding/json"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// The TaskSpec Schema rejects unknown vocabulary words and duplicates before
// the semantic layer runs; these tests pin the readable semantic violations
// validateTask produces for direct callers, matching the schema's closed
// worker.tools semantics.

func TestValidateTaskRejectsWorkerToolsOutsideVocabulary(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["worker"].(map[string]any)["tools"] = []string{"read", "shell"}
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, violation := range violations {
		if violation.Path == "/worker/tools/1" && violation.Code == "unknown-worker-tool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want unknown-worker-tool at /worker/tools/1", violations)
	}
}

func TestValidateTaskRejectsDuplicateWorkerTools(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["worker"].(map[string]any)["tools"] = []string{"read", "read"}
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, violation := range violations {
		if violation.Path == "/worker/tools/1" && violation.Code == "duplicate-id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want duplicate-id at /worker/tools/1", violations)
	}
}

func TestValidateTaskAcceptsDeclaredWorkerTools(t *testing.T) {
	declared := []string{"read", "edit", "write", "grep", "find", "ls", "bash"}
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["worker"].(map[string]any)["tools"] = declared
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Fatalf("declared worker.tools produced a semantic violation: %+v", violation)
	}
	// The full validator stack (schema + semantics) accepts the declaration.
	if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
		t.Fatalf("full validation rejected a valid declaration: %v", err)
	}
}

// The issue #23 admission fields are optional; these tests pin the readable
// semantic violations validateTask produces for direct callers, matching the
// schema's closed admission.status, dependsOn and preconditions semantics.

func TestValidateTaskAdmissionStatusClosedVocabulary(t *testing.T) {
	for _, status := range []string{domain.AdmissionStatusPrepared, domain.AdmissionStatusExecutable} {
		data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
			document["admission"] = map[string]any{"status": status}
		})
		violations, err := validateTask(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			t.Fatalf("admission.status %q produced a semantic violation: %+v", status, violation)
		}
		if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
			t.Fatalf("full validation rejected admission.status %q: %v", status, err)
		}
	}

	// An absent admission declaration stays valid (backward compatibility).
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		delete(document, "admission")
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Fatalf("absent admission produced a semantic violation: %+v", violation)
	}

	data = mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["admission"] = map[string]any{"status": "bogus"}
	})
	violations, err = validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, violation := range violations {
		if violation.Path == "/admission/status" && violation.Code == "unknown-admission-status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want unknown-admission-status at /admission/status", violations)
	}
}

func TestValidateTaskDependsOnSemantics(t *testing.T) {
	validRun := map[string]any{"kind": "run", "runId": "run-1", "requiredState": "ACCEPTED"}
	validTask := map[string]any{"kind": "task", "taskId": "task-1", "requiredState": "NO_CHANGE"}

	// Distinct run- and task-scoped declarations produce no violations.
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["dependsOn"] = []any{validRun, validTask}
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Fatalf("valid dependsOn produced a semantic violation: %+v", violation)
	}
	if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
		t.Fatalf("full validation rejected a valid dependsOn declaration: %v", err)
	}

	tests := []struct {
		name  string
		entry map[string]any
		path  string
		code  string
	}{
		{
			name:  "duplicate kind and reference",
			entry: nil, // handled specially: two identical entries
			path:  "/dependsOn/1",
			code:  "duplicate-id",
		},
		{
			name:  "run dependency without runId",
			entry: map[string]any{"kind": "run", "requiredState": "ACCEPTED"},
			path:  "/dependsOn/0/runId",
			code:  "missing-dependency-reference",
		},
		{
			name:  "task dependency without taskId",
			entry: map[string]any{"kind": "task", "requiredState": "ACCEPTED"},
			path:  "/dependsOn/0/taskId",
			code:  "missing-dependency-reference",
		},
		{
			name:  "unknown kind",
			entry: map[string]any{"kind": "branch", "runId": "run-1", "requiredState": "ACCEPTED"},
			path:  "/dependsOn/0/kind",
			code:  "unknown-dependency-kind",
		},
		{
			name:  "non-terminal requiredState",
			entry: map[string]any{"kind": "run", "runId": "run-1", "requiredState": "READY"},
			path:  "/dependsOn/0/requiredState",
			code:  "non-terminal-dependency-state",
		},
		{
			name:  "unknown requiredState",
			entry: map[string]any{"kind": "run", "runId": "run-1", "requiredState": "EXPLODED"},
			path:  "/dependsOn/0/requiredState",
			code:  "unknown-dependency-state",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
				if test.entry == nil {
					document["dependsOn"] = []any{validRun, validRun}
					return
				}
				document["dependsOn"] = []any{test.entry}
			})
			violations, err := validateTask(data)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, violation := range violations {
				if violation.Path == test.path && violation.Code == test.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("violations = %+v, want %s at %s", violations, test.code, test.path)
			}
		})
	}
}

// Issue #87 acceptance floor: the durable TaskSpec Schema intentionally
// carries no acceptance floor, and neither does validateTask, so archived
// TaskSpecs planned before the floor existed re-validate unchanged forever.
// The floor lives in the semantic layer as a plan-entry gate for new
// TaskSpecs: commands non-empty, every argv non-empty, and at least one
// required:true command, fail closed.

// decodeTaskSpec decodes a fixture document into the canonical TaskSpec
// shape the plan entry hands to the floor gate.
func decodeTaskSpec(t *testing.T, data []byte) domain.TaskSpec {
	t.Helper()
	var task domain.TaskSpec
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatalf("decode TaskSpec fixture: %v", err)
	}
	return task
}

// TestTaskSpecSchemaAcceptanceCommandsRemainFloorless pins the issue #87
// rework decision on the durable schema: acceptance.commands carries no
// minItems floor, so archived legacy TaskSpecs remain schema-valid forever
// and the floor stays a semantic plan-entry concern.
func TestTaskSpecSchemaAcceptanceCommandsRemainFloorless(t *testing.T) {
	t.Parallel()
	data := readFixture(t, "task-spec.schema.json")
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				MinItems *int `json:"minItems"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	acceptance, ok := schema.Defs["acceptance"]
	if !ok {
		t.Fatal("task-spec schema lost $defs/acceptance")
	}
	commands, ok := acceptance.Properties["commands"]
	if !ok {
		t.Fatal("task-spec schema lost $defs/acceptance/commands")
	}
	if commands.MinItems != nil {
		t.Fatalf("acceptance.commands minItems = %v, want no floor in the durable schema", *commands.MinItems)
	}
}

func acceptanceCommandFixture(id string, argv []any, required bool) map[string]any {
	return map[string]any{"id": id, "argv": argv, "cwd": ".", "timeoutSeconds": 60, "required": required}
}

func TestTaskSpecAcceptanceFloorRejectsEmptyCommands(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["acceptance"].(map[string]any)["commands"] = []any{}
	})
	violations := TaskSpecAcceptanceFloorViolations(decodeTaskSpec(t, data))
	found := false
	for _, violation := range violations {
		if violation.Path == "/acceptance/commands" && violation.Code == "acceptance-commands-empty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want acceptance-commands-empty at /acceptance/commands", violations)
	}
	if err := ValidateTaskSpecAcceptanceFloor(decodeTaskSpec(t, data)); err == nil {
		t.Fatal("ValidateTaskSpecAcceptanceFloor() unexpectedly accepted an empty acceptance.commands list")
	}
}

func TestTaskSpecAcceptanceFloorRejectsEmptyArgv(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["acceptance"].(map[string]any)["commands"] = []any{acceptanceCommandFixture("empty-argv", []any{}, true)}
	})
	violations := TaskSpecAcceptanceFloorViolations(decodeTaskSpec(t, data))
	found := false
	for _, violation := range violations {
		if violation.Path == "/acceptance/commands/0/argv" && violation.Code == "empty-argv" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want empty-argv at /acceptance/commands/0/argv", violations)
	}
	if err := ValidateTaskSpecAcceptanceFloor(decodeTaskSpec(t, data)); err == nil {
		t.Fatal("ValidateTaskSpecAcceptanceFloor() unexpectedly accepted an acceptance command with empty argv")
	}
}

func TestTaskSpecAcceptanceFloorRejectsWithoutRequiredCommand(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["acceptance"].(map[string]any)["commands"] = []any{
			acceptanceCommandFixture("optional-a", []any{"true"}, false),
			acceptanceCommandFixture("optional-b", []any{"true"}, false),
		}
	})
	violations := TaskSpecAcceptanceFloorViolations(decodeTaskSpec(t, data))
	found := false
	for _, violation := range violations {
		if violation.Path == "/acceptance/commands" && violation.Code == "acceptance-required-command-missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want acceptance-required-command-missing at /acceptance/commands", violations)
	}
	if err := ValidateTaskSpecAcceptanceFloor(decodeTaskSpec(t, data)); err == nil {
		t.Fatal("ValidateTaskSpecAcceptanceFloor() unexpectedly accepted acceptance commands without any required:true entry")
	}
}

func TestTaskSpecAcceptanceFloorAcceptsFloorSatisfiedCommands(t *testing.T) {
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["acceptance"].(map[string]any)["commands"] = []any{
			acceptanceCommandFixture("optional-check", []any{"true"}, false),
			acceptanceCommandFixture("required-check", []any{"true"}, true),
		}
	})
	task := decodeTaskSpec(t, data)
	for _, violation := range TaskSpecAcceptanceFloorViolations(task) {
		t.Fatalf("floor-satisfied acceptance commands produced a floor violation: %+v", violation)
	}
	if err := ValidateTaskSpecAcceptanceFloor(task); err != nil {
		t.Fatalf("ValidateTaskSpecAcceptanceFloor() rejected floor-satisfied acceptance commands: %v", err)
	}
}

// TestValidateTaskKeepsLegacyAcceptanceShapesRevalidatable pins the issue
// #87 rework scope: the floor never rides the archived re-validation path.
// Legacy TaskSpec shapes that predate the floor — an empty commands list or
// optional-only commands — stay schema-valid and semantically clean under
// the regular validator, exactly as archived packets, reconciliation and
// every frozen-record check re-validate them.
func TestValidateTaskKeepsLegacyAcceptanceShapesRevalidatable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "empty commands",
			mutate: func(document map[string]any) {
				document["acceptance"].(map[string]any)["commands"] = []any{}
			},
		},
		{
			name: "optional-only commands",
			mutate: func(document map[string]any) {
				document["acceptance"].(map[string]any)["commands"] = []any{
					acceptanceCommandFixture("optional-a", []any{"true"}, false),
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := mutateFixture(t, "examples/happy-path/task-spec.json", test.mutate)
			violations, err := validateTask(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range violations {
				t.Fatalf("legacy acceptance shape produced a semantic violation: %+v", violation)
			}
			if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
				t.Fatalf("regular validator rejected a legacy acceptance shape: %v", err)
			}
		})
	}
}

func TestValidateTaskPreconditionsSemantics(t *testing.T) {
	// A fully declared precondition produces no violations.
	data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
		document["preconditions"] = []any{map[string]any{
			"id": "build", "argv": []any{"make", "check"}, "cwd": "sub/dir", "timeoutSeconds": 60,
		}}
	})
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Fatalf("valid precondition produced a semantic violation: %+v", violation)
	}
	if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
		t.Fatalf("full validation rejected a valid precondition declaration: %v", err)
	}

	tests := []struct {
		name  string
		entry map[string]any
		path  string
		code  string
	}{
		{
			name:  "duplicate precondition id",
			entry: nil, // handled specially: two entries sharing one id
			path:  "/preconditions/1",
			code:  "duplicate-id",
		},
		{
			name:  "empty argv",
			entry: map[string]any{"id": "empty", "argv": []any{}},
			path:  "/preconditions/0/argv",
			code:  "empty-argv",
		},
		{
			name:  "absolute cwd",
			entry: map[string]any{"id": "abs", "argv": []any{"true"}, "cwd": "/etc"},
			path:  "/preconditions/0/cwd",
			code:  "invalid-relative-path",
		},
		{
			name:  "escaping cwd",
			entry: map[string]any{"id": "escape", "argv": []any{"true"}, "cwd": "../up"},
			path:  "/preconditions/0/cwd",
			code:  "invalid-relative-path",
		},
		{
			name:  "negative timeoutSeconds",
			entry: map[string]any{"id": "slow", "argv": []any{"true"}, "timeoutSeconds": -5},
			path:  "/preconditions/0/timeoutSeconds",
			code:  "invalid-timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
				if test.entry == nil {
					entry := map[string]any{"id": "dup", "argv": []any{"true"}}
					document["preconditions"] = []any{entry, entry}
					return
				}
				document["preconditions"] = []any{test.entry}
			})
			violations, err := validateTask(data)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, violation := range violations {
				if violation.Path == test.path && violation.Code == test.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("violations = %+v, want %s at %s", violations, test.code, test.path)
			}
		})
	}
}
