package verification

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
)

func TestVerifierEndToEndPassesWithUntrackedDeliverable(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.worktree.Path, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "pkg", "code.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "source", Kind: "code", Required: true, PathGlob: "pkg/*.go", MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "exists", Argv: []string{"sh", "-c", "test -f pkg/code.go"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" || !result.Report.Observed.HasUntrackedFiles {
		t.Fatalf("report = %+v", result.Report)
	}
	for _, name := range []string{"observed.patch", "verification-report.json", "artifact-manifest.json"} {
		if _, err := os.Stat(filepath.Join(input.RunDirectory, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestRenameRequiresBothPathsInScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.worktree.Path, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.worktree.Path, "mv", "README.md", "docs/README.md")
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"docs/**"}, MaxDiffBytes: 1 << 20, MaxChangedFiles: 10})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "README.md") {
		t.Fatalf("scope gate = %+v", gate)
	}
}

func TestWorkerDeclarationCannotOverrideScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "forbidden.txt"), []byte("claimed allowed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"src/**"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.WorkerDeclaredPaths = []string{"forbidden.txt"}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "scope:changed-paths") != "fail" {
		t.Fatalf("worker declaration changed scope result: %+v", result.Report)
	}
}

func TestOversizedDiffFailsScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "large.txt"), []byte(strings.Repeat("x", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 10, MaxDiffBytes: 64})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Diff 字节数") {
		t.Fatalf("scope gate = %+v", gate)
	}
}

func TestDefaultPatchCaptureLimitAllowsOrdinaryDiff(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "ordinary.txt"), []byte("ordinary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.PatchCaptureBytes = 0
	input.Scope = ScopePolicy{AllowPaths: []string{"ordinary.txt"}, MaxChangedFiles: 5}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" || result.Report.Observed.DiffTruncated {
		t.Fatalf("default capture report = %+v", result.Report)
	}
}

func TestArtifactTraversalAndSymlinkEscapeFail(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	_, gates := CollectArtifacts(root, []Deliverable{{ID: "escape", Kind: "report", Required: true, PathGlob: "escape.txt", MinimumCount: 1}, {ID: "traversal", Kind: "report", Required: true, PathGlob: "../*", MinimumCount: 1}}, time.Now())
	if gateStatus(gates, "artifact:escape") != "fail" || gateStatus(gates, "artifact:traversal") != "error" {
		t.Fatalf("artifact gates = %+v", gates)
	}
}

func TestInvalidSymlinkArtifactStillPersistsEvidence(t *testing.T) {
	fixture := newVerificationFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.worktree.Path, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"escape.txt"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "escape", Kind: "report", Required: true, PathGlob: "escape.txt", MinimumCount: 1}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "scope:changed-paths") != "fail" || gateStatus(result.Report.Gates, "artifact:escape") != "fail" {
		t.Fatalf("symlink escape report = %+v", result.Report)
	}
	for _, name := range []string{"verification-report.json", "artifact-manifest.json"} {
		if _, err := os.Stat(filepath.Join(input.RunDirectory, name)); err != nil {
			t.Fatalf("missing failure evidence %s: %v", name, err)
		}
	}
}

func TestRunnerResolvesRelativeExecutableFromCommandCWD(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "tools")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(bin, "check.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalScript, err := filepath.EvalSymlinks(script)
	if err != nil {
		t.Fatal(err)
	}
	result := (Runner{}).Run(context.Background(), root, CommandSpec{ID: "relative", Argv: []string{"./check.sh"}, CWD: "tools", Timeout: time.Second, Required: true})
	if result.Status != "pass" || result.Record.Executable != canonicalScript {
		t.Fatalf("relative executable result = %+v", result)
	}
	outsideDirectory := t.TempDir()
	outsideScript := filepath.Join(outsideDirectory, "outside.sh")
	if err := os.WriteFile(outsideScript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	escapingPath, err := filepath.Rel(bin, outsideScript)
	if err != nil {
		t.Fatal(err)
	}
	result = (Runner{}).Run(context.Background(), root, CommandSpec{ID: "escape", Argv: []string{escapingPath}, CWD: "tools", Timeout: time.Second, Required: true})
	if result.Status != "error" {
		t.Fatalf("escaping executable result = %+v", result)
	}
}

func TestObserveHonorsCancelledContext(t *testing.T) {
	fixture := newVerificationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveContext(ctx, fixture.worktree.Path, fixture.baseSHA, 1<<20); err == nil {
		t.Fatal("ObserveContext accepted a cancelled context")
	}
}

func TestRepositoryIntegrityHonorsCancelledContext(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gate := verifyRepository(ctx, input)
	if gate.Status != "error" {
		t.Fatalf("cancelled repository gate = %+v", gate)
	}
}

func TestScopeRejectsSymlinkThroughEscapingIntermediateComponent(t *testing.T) {
	fixture := newVerificationFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.worktree.Path, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("redirect/target.txt", filepath.Join(fixture.worktree.Path, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Symlink") {
		t.Fatalf("intermediate symlink gate = %+v, observation = %+v", gate, observation)
	}
}

func TestSubmoduleMutationFailsWhenNotAllowed(t *testing.T) {
	submodule := t.TempDir()
	gitTest(t, submodule, "init", "-q")
	gitTest(t, submodule, "config", "user.name", "Marshal Test")
	gitTest(t, submodule, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(submodule, "value.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, submodule, "add", "value.txt")
	gitTest(t, submodule, "commit", "-q", "-m", "one")
	firstSHA := strings.TrimSpace(gitTest(t, submodule, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(submodule, "value.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, submodule, "commit", "-q", "-am", "two")
	secondSHA := strings.TrimSpace(gitTest(t, submodule, "rev-parse", "HEAD"))

	parent := t.TempDir()
	gitTest(t, parent, "init", "-q")
	gitTest(t, parent, "config", "user.name", "Marshal Test")
	gitTest(t, parent, "config", "user.email", "marshal@example.invalid")
	gitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", submodule, "modules/dep")
	gitTest(t, filepath.Join(parent, "modules", "dep"), "checkout", "-q", firstSHA)
	gitTest(t, parent, "add", ".gitmodules", "modules/dep")
	gitTest(t, parent, "commit", "-q", "-m", "base with submodule")
	base := strings.TrimSpace(gitTest(t, parent, "rev-parse", "HEAD"))
	state, err := marshalRepository.Discover(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(state.StateRoot, "task:submodule", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", parent, "branch", "-D", worktree.Branch).Run()
	})
	gitTest(t, worktree.Path, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q")
	gitTest(t, filepath.Join(worktree.Path, "modules", "dep"), "checkout", "-q", secondSHA)
	observation, err := Observe(worktree.Path, base, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"modules/**"}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Submodule") {
		t.Fatalf("submodule gate = %+v, observation = %+v", gate, observation)
	}
}

func TestVerifierDetectsDirtyCommandOutput(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"source.txt"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "dirty", Argv: []string{"sh", "-c", "echo generated > verifier-output.txt"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "command:dirty") != "fail" {
		t.Fatalf("dirty verifier report = %+v", result.Report)
	}
}

func TestOnFailureBaselineClassifiesWithoutWaivingFailure(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.Remove(filepath.Join(fixture.worktree.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "readme", Argv: []string{"sh", "-c", "test -f README.md"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "on-failure"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" {
		t.Fatalf("baseline waived changed failure: %+v", result.Report)
	}
	for _, gate := range result.Report.Gates {
		if gate.ID == "command:readme" && (gate.Command == nil || gate.Command.BaselineStatus != "pass") {
			t.Fatalf("baseline status = %+v", gate.Command)
		}
	}
}

func TestRunnerCancellationTerminatesProcessGroup(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-group test is Unix-specific")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan CommandResult, 1)
	go func() {
		resultChannel <- (Runner{}).Run(ctx, root, CommandSpec{ID: "cancel", Argv: []string{"sh", "-c", "sleep 30 & echo $! > child.pid; wait"}, CWD: ".", Timeout: 30 * time.Second, Required: true, MaxLogBytes: 4096})
	}()
	pidPath := filepath.Join(root, "child.pid")
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child process PID was not recorded")
	}
	cancel()
	result := <-resultChannel
	if result.Status != "cancelled" {
		t.Fatalf("command status = %s", result.Status)
	}
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process %d survived cancellation: %v", pid, err)
	}
}

func TestRunnerBoundsLogs(t *testing.T) {
	result := (Runner{}).Run(context.Background(), t.TempDir(), CommandSpec{ID: "logs", Argv: []string{"sh", "-c", "yes x | head -c 10000"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 1024})
	if result.Status != "pass" || !result.Record.Truncated || len(result.Stdout) > 1100 {
		t.Fatalf("bounded result status=%s truncated=%v bytes=%d", result.Status, result.Record.Truncated, len(result.Stdout))
	}
}

type verificationFixture struct {
	t            *testing.T
	repository   string
	baseSHA      string
	worktree     *gitworktree.Worktree
	commonDir    string
	runDirectory string
}

func newVerificationFixture(t *testing.T) verificationFixture {
	t.Helper()
	repository := t.TempDir()
	gitTest(t, repository, "init", "-q")
	gitTest(t, repository, "config", "user.name", "Marshal Test")
	gitTest(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "README.md")
	gitTest(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repository, "rev-parse", "HEAD"))
	state, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(state.StateRoot, "task:"+strings.ReplaceAll(t.Name(), "/", "-"), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		command := exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path)
		_ = command.Run()
		command = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch)
		_ = command.Run()
	})
	return verificationFixture{t: t, repository: repository, baseSHA: base, worktree: worktree, commonDir: manager.CommonDir, runDirectory: filepath.Join(t.TempDir(), "run")}
}

func (f verificationFixture) input() Input {
	return Input{TaskID: "task:fixture", RunID: "run:fixture", SpecDigest: "sha256:" + strings.Repeat("a", 64), BaseSHA: f.baseSHA, Worktree: f.worktree.Path, ExpectedCommonDir: f.commonDir, RunDirectory: f.runDirectory, PatchCaptureBytes: 1 << 20}
}

func gateStatus(gates []Gate, id string) string {
	for _, gate := range gates {
		if gate.ID == id {
			return gate.Status
		}
	}
	return "missing"
}

func gitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
