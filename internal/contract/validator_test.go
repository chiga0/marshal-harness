package contract

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestCatalogMatchesDomainKinds(t *testing.T) {
	t.Parallel()

	want := domain.Kinds()
	got := make([]domain.Kind, 0, len(descriptors))
	for _, descriptor := range Descriptors() {
		got = append(got, descriptor.Kind)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("catalog kinds = %v, want %v", got, want)
	}
}

func TestEmbeddedContractFixtures(t *testing.T) {
	t.Parallel()

	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	for _, descriptor := range Descriptors() {
		descriptor := descriptor
		t.Run(descriptor.Name, func(t *testing.T) {
			t.Parallel()
			happy := readFixture(t, descriptor.HappyPath)
			if err := validator.Validate(descriptor.Kind, happy); err != nil {
				t.Fatalf("happy fixture failed: %v", err)
			}
			invalid := readFixture(t, descriptor.InvalidPath)
			if err := validator.Validate(descriptor.Kind, invalid); err == nil {
				t.Fatal("invalid fixture unexpectedly passed")
			}
		})
	}
}

func TestFormatAssertionsAreEnabled(t *testing.T) {
	t.Parallel()

	validator := mustValidator(t)
	data := mutateFixture(t, "examples/happy-path/review-decision.json", func(document map[string]any) {
		document["decidedAt"] = "not-a-date-time"
	})
	err := validator.Validate(domain.KindReviewDecision, data)
	if err == nil || !strings.Contains(err.Error(), "date-time") {
		t.Fatalf("Validate() error = %v, want date-time format failure", err)
	}
}

func TestExplicitKindRejectsMismatchedRecord(t *testing.T) {
	t.Parallel()

	validator := mustValidator(t)
	data := readFixture(t, "examples/happy-path/run-state.json")
	if err := validator.Validate(domain.KindTask, data); err == nil {
		t.Fatal("RunState unexpectedly validated as Task")
	}
}

func TestRelativePathSchemaRejectsBackslash(t *testing.T) {
	t.Parallel()

	validator := mustValidator(t)
	data := mutateFixture(t, "examples/happy-path/review-packet.json", func(document map[string]any) {
		document["inputs"].(map[string]any)["taskSpec"] = `..\secret.json`
	})
	if err := validator.Validate(domain.KindReviewPacket, data); err == nil {
		t.Fatal("backslash relative path unexpectedly passed")
	}
}

func TestSemanticValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fixture  string
		kind     domain.Kind
		mutate   func(map[string]any)
		wantCode string
	}{
		{
			name:    "attempt budget overcommitted",
			fixture: "examples/happy-path/task-spec.json",
			kind:    domain.KindTask,
			mutate: func(document map[string]any) {
				document["budgets"].(map[string]any)["maxAttempts"] = float64(3)
			},
			wantCode: "attempt-budget-overcommitted",
		},
		{
			name:    "passing report contains failed required gate",
			fixture: "examples/happy-path/verification-report.json",
			kind:    domain.KindVerificationReport,
			mutate: func(document map[string]any) {
				document["gates"].([]any)[0].(map[string]any)["status"] = "fail"
			},
			wantCode: "verification-status-inconsistent",
		},
		{
			name:    "blocked review has no owner",
			fixture: "examples/happy-path/review-decision.json",
			kind:    domain.KindReviewDecision,
			mutate: func(document map[string]any) {
				document["verdict"] = "blocked"
			},
			wantCode: "blocked-without-owner",
		},
		{
			name:    "marshal state exposed as source artifact",
			fixture: "examples/happy-path/artifact-manifest.json",
			kind:    domain.KindArtifactManifest,
			mutate: func(document map[string]any) {
				artifact := document["artifacts"].([]any)[0].(map[string]any)
				artifact["pathRoot"] = "repository"
				artifact["relativePath"] = ".marshal/runs/run-01/observed.patch"
			},
			wantCode: "marshal-state-as-source",
		},
	}

	validator := mustValidator(t)
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateFixture(t, test.fixture, test.mutate)
			err := validator.Validate(test.kind, data)
			var semanticError *SemanticError
			if !errors.As(err, &semanticError) {
				t.Fatalf("Validate() error = %v, want SemanticError", err)
			}
			if !strings.Contains(err.Error(), test.wantCode) {
				t.Fatalf("Validate() error = %v, want code %q", err, test.wantCode)
			}
		})
	}
}

func TestRunStateConstantsMatchSchema(t *testing.T) {
	t.Parallel()

	data := readFixture(t, "run-state.schema.json")
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode run-state schema: %v", err)
	}
	want := findStringEnumContaining(t, schema, string(domain.StateCreated))
	gotStates := domain.States()
	got := make([]string, len(gotStates))
	for index, state := range gotStates {
		got[index] = string(state)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("domain states = %v, schema states = %v", got, want)
	}
}

func mustValidator(t *testing.T) *Validator {
	t.Helper()
	validator, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	return validator
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := marshalSchemas.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mutateFixture(t *testing.T, name string, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(readFixture(t, name), &document); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	mutate(document)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode mutated fixture %s: %v", name, err)
	}
	return data
}

func findStringEnumContaining(t *testing.T, value any, needle string) []string {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if enum, ok := typed["enum"].([]any); ok {
			values := make([]string, 0, len(enum))
			contains := false
			for _, item := range enum {
				text, ok := item.(string)
				if !ok {
					values = nil
					break
				}
				values = append(values, text)
				contains = contains || text == needle
			}
			if contains {
				return values
			}
		}
		for _, child := range typed {
			if result := findStringEnum(child, needle); result != nil {
				return result
			}
		}
	case []any:
		for _, child := range typed {
			if result := findStringEnum(child, needle); result != nil {
				return result
			}
		}
	}
	t.Fatalf("enum containing %q not found", needle)
	return nil
}

func findStringEnum(value any, needle string) []string {
	switch typed := value.(type) {
	case map[string]any:
		if enum, ok := typed["enum"].([]any); ok {
			values := make([]string, 0, len(enum))
			contains := false
			for _, item := range enum {
				text, ok := item.(string)
				if !ok {
					values = nil
					break
				}
				values = append(values, text)
				contains = contains || text == needle
			}
			if contains {
				return values
			}
		}
		for _, child := range typed {
			if result := findStringEnum(child, needle); result != nil {
				return result
			}
		}
	case []any:
		for _, child := range typed {
			if result := findStringEnum(child, needle); result != nil {
				return result
			}
		}
	}
	return nil
}
