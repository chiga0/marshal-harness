package contract

import (
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
