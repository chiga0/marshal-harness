package contract

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const draft202012Identifier = "https://json-schema.org/draft/2020-12/schema"

// TestEmbeddedSchemasAreValidDraft202012Documents pins the AGENTS.md schema
// gate: every embedded schema parses as JSON, declares Draft 2020-12, and
// compiles against the Draft 2020-12 metaschema. Compilation fails closed
// on any metaschema violation, so a malformed schema change cannot ship.
func TestEmbeddedSchemasAreValidDraft202012Documents(t *testing.T) {
	t.Parallel()
	for _, descriptor := range descriptors {
		descriptor := descriptor
		t.Run(descriptor.Name, func(t *testing.T) {
			t.Parallel()
			data := readFixture(t, descriptor.SchemaPath)
			if !json.Valid(data) {
				t.Fatalf("schema %s is not valid JSON", descriptor.SchemaPath)
			}
			document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("decode schema %s: %v", descriptor.SchemaPath, err)
			}
			object, ok := document.(map[string]any)
			if !ok {
				t.Fatalf("schema %s is not a JSON object", descriptor.SchemaPath)
			}
			if object["$schema"] != draft202012Identifier {
				t.Fatalf("schema %s does not declare Draft 2020-12: %v", descriptor.SchemaPath, object["$schema"])
			}
			identifier, ok := object["$id"].(string)
			if !ok || identifier == "" {
				t.Fatalf("schema %s has no $id", descriptor.SchemaPath)
			}
			// Mirror the production validator configuration exactly, including
			// the ECMA regexp engine, so metaschema compilation sees the same
			// pattern semantics Marshal enforces at runtime.
			compiler := jsonschema.NewCompiler()
			compiler.DefaultDraft(jsonschema.Draft2020)
			compiler.AssertFormat()
			compiler.UseRegexpEngine(compileECMARegexp)
			if err := compiler.AddResource(identifier, document); err != nil {
				t.Fatalf("register schema %s: %v", descriptor.SchemaPath, err)
			}
			if _, err := compiler.Compile(identifier); err != nil {
				t.Fatalf("schema %s fails the Draft 2020-12 metaschema: %v", descriptor.SchemaPath, err)
			}
		})
	}
}

// TestTaskSpecSchemaWorkerToolsClosedVocabulary pins the worker.tools field
// introduced for issue #37: optional, uniqueItems, closed vocabulary, and
// mechanically enforced by the schema.
func TestTaskSpecSchemaWorkerToolsClosedVocabulary(t *testing.T) {
	t.Parallel()
	validator := mustValidator(t)
	tests := []struct {
		name  string
		tools any
		valid bool
	}{
		{name: "absent-stays-valid", tools: nil, valid: true},
		{name: "full-vocabulary", tools: []string{"read", "edit", "write", "grep", "find", "ls", "bash"}, valid: true},
		{name: "subset", tools: []string{"read", "edit"}, valid: true},
		{name: "outside-vocabulary", tools: []string{"read", "shell"}},
		{name: "case-mangled", tools: []string{"Read"}},
		{name: "duplicated", tools: []string{"read", "read"}},
		{name: "non-array", tools: "read,edit"},
		{name: "non-string-item", tools: []any{"read", 5}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
				worker := document["worker"].(map[string]any)
				if test.tools == nil {
					delete(worker, "tools")
					return
				}
				worker["tools"] = test.tools
			})
			err := validator.Validate(domain.KindTask, data)
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want valid TaskSpec", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid worker.tools declaration")
			}
		})
	}
}

// TestTaskSpecSchemaWorkerToolsEnumMatchesVocabulary pins the schema enum to
// the exact frozen vocabulary in schema order, so a vocabulary drift is a
// visible schema change rather than a silent table edit.
func TestTaskSpecSchemaWorkerToolsEnumMatchesVocabulary(t *testing.T) {
	t.Parallel()
	data := readFixture(t, "task-spec.schema.json")
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	worker, ok := schema.Defs["worker"]
	if !ok {
		t.Fatal("task-spec schema lost $defs/worker")
	}
	tools, ok := worker.Properties["tools"]
	if !ok || len(tools.Items.Enum) == 0 {
		t.Fatal("$defs/worker lost the tools closed enum")
	}
	want := []string{"bash", "edit", "find", "grep", "ls", "read", "write"}
	got := append([]string{}, tools.Items.Enum...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("worker.tools enum = %v, want %v", tools.Items.Enum, want)
	}
}

// TestTaskSpecSchemaAdmissionDependsOnPreconditions pins the issue #23
// admission fields: all three are optional for backward compatibility, the
// admission.status vocabulary is closed, dependsOn enforces the run/task
// mutual exclusion and the five terminal states, and preconditions reuse the
// command subset shape with a required-always semantic.
func TestTaskSpecSchemaAdmissionDependsOnPreconditions(t *testing.T) {
	t.Parallel()
	validator := mustValidator(t)
	tests := []struct {
		name  string
		apply func(map[string]any)
		valid bool
	}{
		{
			name:  "absent-stays-valid",
			apply: func(document map[string]any) {},
			valid: true,
		},
		{
			name:  "admission-prepared",
			apply: func(document map[string]any) { document["admission"] = map[string]any{"status": "prepared"} },
			valid: true,
		},
		{
			name:  "admission-executable",
			apply: func(document map[string]any) { document["admission"] = map[string]any{"status": "executable"} },
			valid: true,
		},
		{
			name:  "admission-status-outside-enum",
			apply: func(document map[string]any) { document["admission"] = map[string]any{"status": "bogus"} },
		},
		{
			name:  "admission-missing-status",
			apply: func(document map[string]any) { document["admission"] = map[string]any{} },
		},
		{
			name: "admission-unknown-extra-field",
			apply: func(document map[string]any) {
				document["admission"] = map[string]any{"status": "prepared", "extra": 1}
			},
		},
		{
			name: "dependsOn-run-entry",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "run", "runId": "run-1", "requiredState": "ACCEPTED"}}
			},
			valid: true,
		},
		{
			name: "dependsOn-task-entry",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "task", "taskId": "task-1", "requiredState": "NO_CHANGE"}}
			},
			valid: true,
		},
		{
			name: "dependsOn-pinned-bindings",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{
					"kind": "run", "runId": "run:1", "requiredState": "BLOCKED",
					"baseSha": strings.Repeat("a", 40), "specDigest": "sha256:" + strings.Repeat("b", 64),
				}}
			},
			valid: true,
		},
		{
			name: "dependsOn-kind-outside-enum",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "branch", "runId": "run-1", "requiredState": "ACCEPTED"}}
			},
		},
		{
			name: "dependsOn-non-terminal-state",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "run", "runId": "run-1", "requiredState": "READY"}}
			},
		},
		{
			name: "dependsOn-run-without-runId",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "run", "requiredState": "ACCEPTED"}}
			},
		},
		{
			name: "dependsOn-task-without-taskId",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "task", "requiredState": "ACCEPTED"}}
			},
		},
		{
			name: "dependsOn-run-with-taskId-violates-exclusion",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "run", "runId": "run-1", "taskId": "task-1", "requiredState": "ACCEPTED"}}
			},
		},
		{
			name: "dependsOn-both-references-violates-exclusion",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "task", "runId": "run-1", "taskId": "task-1", "requiredState": "ACCEPTED"}}
			},
		},
		{
			name: "dependsOn-missing-requiredState",
			apply: func(document map[string]any) {
				document["dependsOn"] = []any{map[string]any{"kind": "run", "runId": "run-1"}}
			},
		},
		{
			name: "preconditions-minimal",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check", "argv": []any{"make", "check"}}}
			},
			valid: true,
		},
		{
			name: "preconditions-full",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{
					"id": "check", "argv": []any{"make", "check"}, "cwd": "sub/dir", "timeoutSeconds": 60,
				}}
			},
			valid: true,
		},
		{
			name: "preconditions-missing-argv",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check"}}
			},
		},
		{
			name: "preconditions-empty-argv",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check", "argv": []any{}}}
			},
		},
		{
			name: "preconditions-zero-timeout",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check", "argv": []any{"true"}, "timeoutSeconds": 0}}
			},
		},
		{
			name: "preconditions-absolute-cwd",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check", "argv": []any{"true"}, "cwd": "/etc"}}
			},
		},
		{
			name: "preconditions-escaping-cwd",
			apply: func(document map[string]any) {
				document["preconditions"] = []any{map[string]any{"id": "check", "argv": []any{"true"}, "cwd": "../up"}}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
				delete(document, "admission")
				delete(document, "dependsOn")
				delete(document, "preconditions")
				test.apply(document)
			})
			err := validator.Validate(domain.KindTask, data)
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want valid TaskSpec", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid admission declaration")
			}
		})
	}
}

// TestTaskSpecSchemaAdmissionEnumsMatchFrozenVocabulary pins the issue #23
// closed enums to their exact frozen vocabularies, so a vocabulary drift is
// a visible schema change instead of a silent table edit.
func TestTaskSpecSchemaAdmissionEnumsMatchFrozenVocabulary(t *testing.T) {
	t.Parallel()
	data := readFixture(t, "task-spec.schema.json")
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	admission, ok := schema.Defs["admission"]
	if !ok {
		t.Fatal("task-spec schema lost $defs/admission")
	}
	status, ok := admission.Properties["status"]
	if !ok || len(status.Enum) == 0 {
		t.Fatal("$defs/admission lost the status closed enum")
	}
	got := append([]string{}, status.Enum...)
	slices.Sort(got)
	if want := []string{"executable", "prepared"}; !slices.Equal(got, want) {
		t.Fatalf("admission.status enum = %v, want %v", status.Enum, want)
	}

	dependency, ok := schema.Defs["dependency"]
	if !ok {
		t.Fatal("task-spec schema lost $defs/dependency")
	}
	kind, ok := dependency.Properties["kind"]
	if !ok || len(kind.Enum) == 0 {
		t.Fatal("$defs/dependency lost the kind closed enum")
	}
	got = append([]string{}, kind.Enum...)
	slices.Sort(got)
	if want := []string{"run", "task"}; !slices.Equal(got, want) {
		t.Fatalf("dependsOn kind enum = %v, want %v", kind.Enum, want)
	}
	requiredState, ok := dependency.Properties["requiredState"]
	if !ok || len(requiredState.Enum) == 0 {
		t.Fatal("$defs/dependency lost the requiredState closed enum")
	}
	got = append([]string{}, requiredState.Enum...)
	slices.Sort(got)
	if want := []string{"ABORTED", "ACCEPTED", "BLOCKED", "NO_CHANGE", "REJECTED"}; !slices.Equal(got, want) {
		t.Fatalf("dependsOn requiredState enum = %v, want the five terminal states %v", requiredState.Enum, want)
	}
}
