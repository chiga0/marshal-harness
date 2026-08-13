package verification

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestToolAllowlistFromTaskExtractsDeclaredTools(t *testing.T) {
	var task domain.TaskSpec
	task.Worker.Tools = []string{"read", "edit", "write"}
	got := ToolAllowlistFromTask(task)
	if len(got) != 3 || got[0] != "read" || got[1] != "edit" || got[2] != "write" {
		t.Fatalf("ToolAllowlistFromTask = %v, want [read edit write]", got)
	}
	// The returned slice must never alias the TaskSpec storage.
	got[0] = "tampered"
	if task.Worker.Tools[0] != "read" {
		t.Fatal("ToolAllowlistFromTask exposed TaskSpec storage aliasing")
	}
}

func TestToolAllowlistFromTaskReturnsEmptyWithoutDeclaration(t *testing.T) {
	var task domain.TaskSpec
	if got := ToolAllowlistFromTask(task); len(got) != 0 {
		t.Fatalf("ToolAllowlistFromTask = %v, want empty for undeclared tasks", got)
	}
}
