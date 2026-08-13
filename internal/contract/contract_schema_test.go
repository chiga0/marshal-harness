package contract

import (
	"bytes"
	"encoding/json"
	"slices"
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
