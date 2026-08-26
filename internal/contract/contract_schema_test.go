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

func TestLocalApplicabilityProjectionIsClosedAcrossEvidenceAndOutcome(t *testing.T) {
	validator := mustValidator(t)
	tests := []struct {
		name       string
		kind       domain.Kind
		path       string
		bindingKey string
	}{
		{"verification", domain.KindVerificationReport, "examples/local-dogfood/verification-report.json", "localSelfIdentityBinding"},
		{"manifest", domain.KindArtifactManifest, "examples/local-dogfood/artifact-manifest.json", "localSelfIdentityBinding"},
		{"packet", domain.KindReviewPacket, "examples/local-dogfood/review-packet.json", "localSelfIdentityBinding"},
		{"outcome", domain.KindOutcome, "examples/local-dogfood/outcome.json", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, mutation := range []struct {
				name  string
				apply func(map[string]any)
			}{
				{"missing", func(value map[string]any) { delete(value, "applicability") }},
				{"production", func(value map[string]any) { value["applicability"].(map[string]any)["production"] = true }},
				{"publication", func(value map[string]any) { value["applicability"].(map[string]any)["publication"] = "remote" }},
				{"unknown", func(value map[string]any) { value["applicability"].(map[string]any)["extra"] = "forged" }},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					data := mutateFixture(t, test.path, func(document map[string]any) {
						target := document
						if test.bindingKey != "" {
							target = document[test.bindingKey].(map[string]any)
						}
						mutation.apply(target)
					})
					if err := validator.Validate(test.kind, data); err == nil {
						t.Fatal("invalid local applicability projection was accepted")
					}
				})
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

func TestADR0028OptionalDeadlineFieldsStayClosedAndBackwardCompatible(t *testing.T) {
	t.Parallel()
	validator := mustValidator(t)

	t.Run("task budget bounds and legacy absence", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			valid bool
		}{
			{name: "absent", value: nil, valid: true},
			{name: "minimum", value: float64(1), valid: true},
			{name: "maximum", value: float64(604800), valid: true},
			{name: "zero", value: float64(0)},
			{name: "over maximum", value: float64(604801)},
		}
		for _, test := range tests {
			data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
				budgets := document["budgets"].(map[string]any)
				if test.value == nil {
					delete(budgets, "ciObserveTimeoutSeconds")
				} else {
					budgets["ciObserveTimeoutSeconds"] = test.value
				}
			})
			err := validator.Validate(domain.KindTask, data)
			if test.valid && err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("%s: invalid budget accepted", test.name)
			}
		}
	})

	t.Run("publication deadline optional date-time", func(t *testing.T) {
		legacy := mutateFixture(t, "examples/happy-path/publication-record.json", func(document map[string]any) {
			delete(document, "ciDeadline")
		})
		if err := validator.Validate(domain.KindPublicationRecord, legacy); err != nil {
			t.Fatalf("legacy PublicationRecord rejected: %v", err)
		}
		invalid := mutateFixture(t, "examples/happy-path/publication-record.json", func(document map[string]any) {
			document["ciDeadline"] = "not-a-time"
		})
		if err := validator.Validate(domain.KindPublicationRecord, invalid); err == nil {
			t.Fatal("malformed ciDeadline accepted")
		}
	})

	t.Run("remote completion optional date-time", func(t *testing.T) {
		legacy := mutateFixture(t, "examples/happy-path/remote-check-record.json", func(document map[string]any) {
			delete(document["checks"].([]any)[0].(map[string]any), "completedAt")
		})
		if err := validator.Validate(domain.KindRemoteCheckRecord, legacy); err != nil {
			t.Fatalf("legacy RemoteCheckRecord rejected: %v", err)
		}
		invalid := mutateFixture(t, "examples/happy-path/remote-check-record.json", func(document map[string]any) {
			document["checks"].([]any)[0].(map[string]any)["completedAt"] = "not-a-time"
		})
		if err := validator.Validate(domain.KindRemoteCheckRecord, invalid); err == nil {
			t.Fatal("malformed completedAt accepted")
		}
	})
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

// TestCandidateRecordSchemaProducerKindMatchesFrozenEnumeration pins the ADR
// 0027 closed producerKind enumeration to its exact frozen vocabulary, so a
// vocabulary drift is a visible schema change instead of a silent edit.
func TestCandidateRecordSchemaProducerKindMatchesFrozenEnumeration(t *testing.T) {
	t.Parallel()
	data := readFixture(t, "candidate-record.schema.json")
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	producerKind, ok := schema.Properties["producerKind"]
	if !ok || len(producerKind.Enum) == 0 {
		t.Fatal("candidate-record schema lost the producerKind closed enum")
	}
	got := append([]string{}, producerKind.Enum...)
	slices.Sort(got)
	if want := []string{"normalizer", "worker"}; !slices.Equal(got, want) {
		t.Fatalf("producerKind enum = %v, want %v", producerKind.Enum, want)
	}
}

// TestCandidateRecordSchemaConditionalPredecessor pins the ADR 0027 T1
// fail-closed conditions expressed via allOf + if/then: worker Candidates
// are chain roots and must not carry predecessorCandidateDigest, normalizer
// Candidates must carry it, producerKind stays inside the closed
// enumeration, and contentDigest keeps its sha256 content-addressed form.
func TestCandidateRecordSchemaConditionalPredecessor(t *testing.T) {
	t.Parallel()

	predecessor := "sha256:" + strings.Repeat("f", 64)
	validator := mustValidator(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{name: "worker chain root stays valid", valid: true, mutate: func(map[string]any) {}},
		{
			name:  "normalizer successor with predecessor",
			valid: true,
			mutate: func(document map[string]any) {
				document["producerKind"] = "normalizer"
				document["producer"] = "verifier:format-normalize"
				document["predecessorCandidateDigest"] = predecessor
			},
		},
		{
			name:  "remote dispatch reserved fields accepted",
			valid: true,
			mutate: func(document map[string]any) {
				document["allocationId"] = "allocation-01"
				document["generation"] = 3
			},
		},
		{name: "producerKind outside enumeration", mutate: func(document map[string]any) { document["producerKind"] = "publisher" }},
		{name: "producerKind case-mangled", mutate: func(document map[string]any) { document["producerKind"] = "Worker" }},
		{name: "worker carrying predecessor", mutate: func(document map[string]any) { document["predecessorCandidateDigest"] = predecessor }},
		{
			name: "normalizer missing predecessor",
			mutate: func(document map[string]any) {
				document["producerKind"] = "normalizer"
				document["producer"] = "verifier:format-normalize"
			},
		},
		{
			name: "normalizer predecessor without prefix",
			mutate: func(document map[string]any) {
				document["producerKind"] = "normalizer"
				document["producer"] = "verifier:format-normalize"
				document["predecessorCandidateDigest"] = strings.Repeat("f", 64)
			},
		},
		{name: "contentDigest invalid prefix", mutate: func(document map[string]any) { document["contentDigest"] = "md5:" + strings.Repeat("d", 64) }},
		{name: "contentDigest non-hex", mutate: func(document map[string]any) { document["contentDigest"] = "sha256:" + strings.Repeat("w", 64) }},
		{name: "candidateDigest invalid", mutate: func(document map[string]any) { document["candidateDigest"] = "sha256:" + strings.Repeat("e", 63) }},
		{name: "baseSha too short", mutate: func(document map[string]any) { document["baseSha"] = "abc" }},
		{name: "createdAt not date-time", mutate: func(document map[string]any) { document["createdAt"] = "not-a-date-time" }},
		{name: "negative generation", mutate: func(document map[string]any) { document["generation"] = -1 }},
		{name: "unknown extra field", mutate: func(document map[string]any) { document["extra"] = 1 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateFixture(t, "examples/happy-path/candidate-record.json", test.mutate)
			err := validator.Validate(domain.KindCandidate, data)
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want valid Candidate", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid Candidate")
			}
		})
	}
}
