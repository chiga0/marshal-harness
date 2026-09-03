//go:build linux

package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

func TestTaskSpecBuiltinFailsClosedOnLinux(t *testing.T) {
	fixture := newVerificationFixture(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.worktree.Path, "task.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"task.json"}, MaxChangedFiles: 1, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "task-spec", Kind: "documentation", Required: true, PathGlob: "task.json", MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "validate", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "none"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "command:validate") != "fail" {
		t.Fatalf("linux reserved builtin did not fail closed: %+v", result.Report)
	}
	stderr, err := os.ReadFile(filepath.Join(input.RunDirectory, "logs", "validate.stderr.log"))
	if err != nil || string(stderr) != "contract-builtin-denied\n" {
		t.Fatalf("stderr = %q err=%v", stderr, err)
	}
}
