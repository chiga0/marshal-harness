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

// TestToolAllowlistFromTaskFeedsToolAuditGate pins the Input plumbing
// contract: the extraction result feeds Input.ToolAllowlist verbatim, a
// zero-value declaration yields nil which keeps the tool-audit gate
// skipped, and declared tools propagate into the gate reconciliation.
func TestToolAllowlistFromTaskFeedsToolAuditGate(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		tools      []string
		wantNil    bool
		wantStatus string
	}{
		{name: "zero-value-spec-yields-nil-and-skips", tools: nil, wantNil: true, wantStatus: "skipped"},
		{name: "declared-tools-propagate-and-pass", tools: []string{"read", "edit"}, wantNil: false, wantStatus: "pass"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var task domain.TaskSpec
			task.Worker.Tools = testCase.tools
			allowlist := ToolAllowlistFromTask(task)
			if testCase.wantNil && allowlist != nil {
				t.Fatalf("ToolAllowlistFromTask = %#v, want nil for undeclared worker.tools", allowlist)
			}
			if !testCase.wantNil && len(allowlist) != len(testCase.tools) {
				t.Fatalf("ToolAllowlistFromTask = %v, want %v", allowlist, testCase.tools)
			}
			attemptDir := stageToolAuditAttempt(t, t.TempDir(), "attempt-1", 1, []string{"read"})
			gate := assessToolAudit(Input{ToolAllowlist: allowlist}, attemptDir)
			if gate.Status != testCase.wantStatus {
				t.Fatalf("tool-audit status = %q, want %q", gate.Status, testCase.wantStatus)
			}
		})
	}
}
