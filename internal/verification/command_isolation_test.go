package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssue138VerifierCommandsUseFreshIsolates(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "source.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"source.txt"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{
		{ID: "pycache", Argv: []string{"sh", "-c", "mkdir -p __pycache__; printf cache > __pycache__/module.pyc"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
		{ID: "tracked-overwrite", Argv: []string{"sh", "-c", "printf changed > README.md"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
		{ID: "delete-rename", Argv: []string{"sh", "-c", "rm README.md; mv source.txt renamed.txt"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
		{ID: "symlink", Argv: []string{"sh", "-c", "ln -s source.txt generated-link"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
		{ID: "fail-after-write", Argv: []string{"sh", "-c", "printf dirty > failed.txt; exit 7"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
		{ID: "fresh-after-mutation", Argv: []string{"sh", "-c", "test ! -e __pycache__ && test ! -e renamed.txt && test -f README.md && test -f source.txt"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096},
	}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pycache", "tracked-overwrite", "delete-rename", "symlink", "fail-after-write"} {
		gate := commandGateByID(t, result.Report.Gates, "command:"+id)
		if gate.Status != "fail" || !strings.Contains(gate.Summary, verifierWorktreeMutatedReason) || !strings.Contains(gate.Summary, "before=sha256:") || !strings.Contains(gate.Summary, "after=sha256:") {
			t.Fatalf("mutation gate %s = %+v", id, gate)
		}
	}
	failure := commandGateByID(t, result.Report.Gates, "command:fail-after-write")
	if failure.Command == nil || failure.Command.ExitCode == nil || *failure.Command.ExitCode != 7 {
		t.Fatalf("mutation hid the original command outcome: %+v", failure.Command)
	}
	if gate := commandGateByID(t, result.Report.Gates, "command:fresh-after-mutation"); gate.Status != "pass" {
		t.Fatalf("later command inherited an earlier isolate mutation: %+v", gate)
	}
	after, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !sameObservationIdentity(before, after) {
		t.Fatalf("managed candidate changed: before=%+v after=%+v", before, after)
	}
	for _, path := range []string{"__pycache__", "renamed.txt", "generated-link", "failed.txt"} {
		if _, err := os.Lstat(filepath.Join(fixture.worktree.Path, path)); !os.IsNotExist(err) {
			t.Fatalf("verifier mutation escaped into managed worktree: %s", path)
		}
	}
}

func TestIssue138PythonBytecodeUsesExternalIsolatedCache(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "module.py"), []byte("VALUE = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"module.py"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "python-import", Argv: []string{"python3", "-c", "import module; assert module.VALUE == 1"}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gate := commandGateByID(t, result.Report.Gates, "command:python-import"); gate.Status != "pass" {
		t.Fatalf("bytecode cache was not redirected outside the isolate: %+v", gate)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worktree.Path, "__pycache__")); !os.IsNotExist(err) {
		t.Fatal("python bytecode cache reached the managed worktree")
	}
}

func TestIssue138BaselineMutationFailsClosed(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.Remove(filepath.Join(fixture.worktree.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "baseline-dirty", Argv: []string{"sh", "-c", "if test -f README.md; then printf dirty > baseline-output.txt; fi"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "always"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:baseline-dirty")
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "baseline-"+verifierWorktreeMutatedReason) || !strings.Contains(gate.Summary, "before=sha256:") || !strings.Contains(gate.Summary, "after=sha256:") || gate.Command == nil || gate.Command.BaselineStatus != "error" {
		t.Fatalf("baseline mutation gate = %+v", gate)
	}
	if _, err := os.Lstat(filepath.Join(fixture.repository, "baseline-output.txt")); !os.IsNotExist(err) {
		t.Fatal("baseline command modified the baseline repository")
	}
}

func TestIssue138CancellationKeepsManagedCandidate(t *testing.T) {
	fixture := newVerificationFixture(t)
	expected, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := runCommandIsolated(ctx, Runner{}, fixture.worktree.Path, fixture.baseSHA, expected, CommandSpec{
		ID: "cancelled", Argv: []string{"sh", "-c", "sleep 30"}, CWD: ".", Timeout: 30 * time.Second, Required: true, MaxLogBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Command.Status != "cancelled" || result.Mutated {
		t.Fatalf("cancelled isolated command = %+v", result)
	}
	after, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !sameObservationIdentity(expected, after) {
		t.Fatal("cancellation changed the managed candidate")
	}
}

func TestIssue138RejectsDirectManagedCandidateReference(t *testing.T) {
	fixture := newVerificationFixture(t)
	expected, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runCommandIsolated(context.Background(), Runner{}, fixture.worktree.Path, fixture.baseSHA, expected, CommandSpec{
		ID: "absolute", Argv: []string{"sh", "-c", "printf dirty > " + filepath.Join(fixture.worktree.Path, "dirty.txt")}, CWD: ".", Timeout: 5 * time.Second, Required: true,
	})
	if err == nil || !strings.Contains(err.Error(), "references the managed candidate") {
		t.Fatalf("direct managed-candidate reference was not rejected: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.worktree.Path, "dirty.txt")); !os.IsNotExist(statErr) {
		t.Fatal("rejected command still changed the managed candidate")
	}
}

func TestIssue138EscapingSymlinkRefusesCommand(t *testing.T) {
	fixture := newVerificationFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.worktree.Path, "escape")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"escape"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "unstarted", Argv: []string{"sh", "-c", "printf started"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:unstarted")
	if gate.Status != "error" || !strings.Contains(gate.Summary, "refuses an unsafe symlink") || gate.Command == nil || gate.Command.ExitCode != nil {
		t.Fatalf("unsafe input did not fail before command start: %+v", gate)
	}
}
