package taskgen

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
)

func TestGenerateDefaultsAndPreservesExplicitOrder(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	draft := taskDraft(t)
	worker := draft["worker"].(map[string]any)
	delete(worker, "preferredAdapter")
	delete(worker, "fallbackAdapters")
	generated, err := Generate(marshalDraft(t, draft), nil, validator)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerOrder(t, generated, "qoder", []string{"codex", "qwen", "pi"})

	draft = taskDraft(t)
	worker = draft["worker"].(map[string]any)
	worker["preferredAdapter"] = "pi"
	worker["fallbackAdapters"] = []any{"qwen", "codex", "qoder"}
	generated, err = Generate(marshalDraft(t, draft), nil, validator)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerOrder(t, generated, "pi", []string{"qwen", "codex", "qoder"})

	override := &Selection{Preferred: "codex", Fallback: []string{"pi", "qwen", "qoder"}}
	overrideDraft := taskDraft(t)
	delete(overrideDraft["worker"].(map[string]any), "preferredAdapter")
	delete(overrideDraft["worker"].(map[string]any), "fallbackAdapters")
	generated, err = Generate(marshalDraft(t, overrideDraft), override, validator)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerOrder(t, generated, "codex", []string{"pi", "qwen", "qoder"})
	if !reflect.DeepEqual(override.Fallback, []string{"pi", "qwen", "qoder"}) {
		t.Fatalf("Generate mutated override: %+v", override)
	}
}

func TestGenerateRejectsOpenCodeAndIncompleteOrders(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []Selection{
		{Preferred: "opencode", Fallback: []string{"qwen"}},
		{Preferred: "qoder", Fallback: []string{"codex", "opencode", "pi"}},
		{Preferred: "OpenCode", Fallback: []string{}},
	} {
		if _, err := Generate(marshalDraft(t, taskDraft(t)), &selection, validator); !errors.Is(err, ErrOpenCodeIneligible) {
			t.Fatalf("selection %+v error = %v, want ErrOpenCodeIneligible", selection, err)
		}
	}
	draftWithOpenCode := taskDraft(t)
	draftWithOpenCode["worker"].(map[string]any)["preferredAdapter"] = "opencode"
	safeOverride := &Selection{Preferred: "qoder", Fallback: []string{"codex", "qwen", "pi"}}
	if _, err := Generate(marshalDraft(t, draftWithOpenCode), safeOverride, validator); !errors.Is(err, ErrOpenCodeIneligible) {
		t.Fatalf("safe override of OpenCode draft error = %v, want ErrOpenCodeIneligible", err)
	}

	for _, missing := range []string{"preferredAdapter", "fallbackAdapters"} {
		draft := taskDraft(t)
		delete(draft["worker"].(map[string]any), missing)
		if _, err := Generate(marshalDraft(t, draft), nil, validator); !errors.Is(err, ErrIncompleteOrder) {
			t.Fatalf("missing %s error = %v, want ErrIncompleteOrder", missing, err)
		}
	}
}

func TestGenerateRejectsDuplicateJSONMembers(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate([]byte(`{"worker":{},"worker":{}}`), nil, validator); !errors.Is(err, ErrInvalidDraft) {
		t.Fatalf("duplicate-member error = %v, want ErrInvalidDraft", err)
	}
}

func taskDraft(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../schemas/examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var draft map[string]any
	if err := json.Unmarshal(data, &draft); err != nil {
		t.Fatal(err)
	}
	return draft
}

func marshalDraft(t *testing.T, draft map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertWorkerOrder(t *testing.T, generated []byte, preferred string, fallbacks []string) {
	t.Helper()
	var task struct {
		Worker struct {
			Preferred string   `json:"preferredAdapter"`
			Fallback  []string `json:"fallbackAdapters"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(generated, &task); err != nil {
		t.Fatal(err)
	}
	if task.Worker.Preferred != preferred || !reflect.DeepEqual(task.Worker.Fallback, fallbacks) {
		t.Fatalf("worker order = %q -> %v, want %q -> %v", task.Worker.Preferred, task.Worker.Fallback, preferred, fallbacks)
	}
}
