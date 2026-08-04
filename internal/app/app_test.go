package app

import (
	"testing"

	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestParseTaskSpecUsesValidatedContract(t *testing.T) {
	t.Parallel()
	data, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	application, err := New()
	if err != nil {
		t.Fatal(err)
	}
	task, err := application.ParseTaskSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	if task.Metadata.ID != "ENG-123" || len(task.Acceptance.Commands) != 1 || len(task.Deliverables) != 3 {
		t.Fatalf("parsed TaskSpec = %+v", task)
	}
}
