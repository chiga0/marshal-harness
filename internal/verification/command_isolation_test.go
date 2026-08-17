package verification

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestIssue138RejectsLexicalAndSymlinkCandidateAliases(t *testing.T) {
	fixture := newVerificationFixture(t)
	expected, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "candidate-alias")
	if err := os.Symlink(fixture.worktree.Path, alias); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"lexical-dotdot": fixture.worktree.Path + string(filepath.Separator) + "subdir" + string(filepath.Separator) + ".." + string(filepath.Separator) + "dirty.txt",
		"symlink-alias":  filepath.Join(alias, "dirty.txt"),
	} {
		t.Run(name, func(t *testing.T) {
			_, runErr := runCommandIsolated(context.Background(), Runner{}, fixture.worktree.Path, fixture.baseSHA, expected, CommandSpec{
				ID: name, Argv: []string{"sh", "-c", "printf dirty > " + target}, CWD: ".", Timeout: 5 * time.Second, Required: true,
			})
			if runErr == nil || !strings.Contains(runErr.Error(), "references the managed candidate") {
				t.Fatalf("candidate alias was not rejected: %v", runErr)
			}
		})
	}
}

func TestIssue138RejectsSymlinkThenDotDotIntoCandidate(t *testing.T) {
	fixture := newVerificationFixture(t)
	subdirectory := filepath.Join(fixture.worktree.Path, "candidate-subdirectory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	expected, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "subdirectory-alias")
	if err := os.Symlink(subdirectory, alias); err != nil {
		t.Fatal(err)
	}
	// Kernel traversal expands alias to candidate/candidate-subdirectory
	// before applying "..", so the absent output is candidate/output.txt.
	// A lexical Clean before symlink expansion would incorrectly turn this
	// into a sibling of alias and miss the protected-root reference.
	target := alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "output.txt"
	_, err = runCommandIsolated(context.Background(), Runner{}, fixture.worktree.Path, fixture.baseSHA, expected, CommandSpec{
		ID: "symlink-dotdot", Argv: []string{"sh", "-c", "printf dirty > " + target}, CWD: ".", Timeout: 5 * time.Second, Required: true,
	})
	if err == nil || !strings.Contains(err.Error(), "references the managed candidate") {
		t.Fatalf("symlink followed by dot-dot escaped protected-reference detection: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.worktree.Path, "output.txt")); !os.IsNotExist(statErr) {
		t.Fatal("rejected symlink/dot-dot reference still changed the candidate")
	}
}

func TestIssue138CandidateCommandAlsoProtectsBaselineRoot(t *testing.T) {
	fixture := newVerificationFixture(t)
	candidate, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := Observe(fixture.repository, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "baseline-alias")
	if err := os.Symlink(fixture.repository, alias); err != nil {
		t.Fatal(err)
	}
	_, err = runCommandIsolated(context.Background(), Runner{}, fixture.worktree.Path, fixture.baseSHA, candidate, CommandSpec{
		ID: "cross-root", Argv: []string{"sh", "-c", "printf dirty > " + filepath.Join(alias, "dirty.txt")}, CWD: ".", Timeout: 5 * time.Second, Required: true,
	}, commandProtectedSource{Path: fixture.repository, BaseSHA: fixture.baseSHA, Expected: baseline})
	if err == nil || !strings.Contains(err.Error(), "references the managed candidate") {
		t.Fatalf("candidate command could reference baseline alias: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.repository, "dirty.txt")); !os.IsNotExist(statErr) {
		t.Fatal("rejected cross-root command modified baseline")
	}
}

func TestIssue138RejectsCandidateAliasInPATH(t *testing.T) {
	fixture := newVerificationFixture(t)
	bin := filepath.Join(fixture.worktree.Path, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "candidate-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf changed > ../source.txt\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "candidate-bin")
	if err := os.Symlink(bin, alias); err != nil {
		t.Fatal(err)
	}
	_, err = runCommandIsolated(context.Background(), Runner{Environment: []string{"PATH=" + alias}}, fixture.worktree.Path, fixture.baseSHA, expected, CommandSpec{
		ID: "path-alias", Argv: []string{"candidate-tool"}, CWD: ".", Timeout: 5 * time.Second, Required: true,
	})
	if err == nil || !strings.Contains(err.Error(), "environment references the managed candidate") {
		t.Fatalf("PATH alias into candidate was not rejected: %v", err)
	}
}

func TestIssue138PostRunSnapshotErrorPreservesCommandOutcome(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	input.Commands = []CommandSpec{{
		ID: "fifo-after-failure", Argv: []string{"sh", "-c", "printf preserved-output; mkfifo unsupported-entry; exit 7"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096,
	}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:fifo-after-failure")
	if gate.Status != "error" || !strings.Contains(gate.Summary, "verifier-command-isolation-error") {
		t.Fatalf("post-run isolation error was not typed: %+v", gate)
	}
	if gate.Command == nil || gate.Command.ExitCode == nil || *gate.Command.ExitCode != 7 {
		t.Fatalf("post-run isolation error discarded real exit: %+v", gate.Command)
	}
	stdoutPath := filepath.Join(input.RunDirectory, "logs", "fifo-after-failure.stdout.log")
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "preserved-output" {
		t.Fatalf("post-run isolation error discarded stdout: %q", stdout)
	}
}

func TestIssue138CandidateCopyRejectsLstatToFIFORaceWithoutBlocking(t *testing.T) {
	fixture := newVerificationFixture(t)
	destination := t.TempDir()
	var hookErr error
	replaced := false
	hooks := commandIsolationHooks{afterLstat: func(path string) {
		if replaced || filepath.Base(path) != "README.md" {
			return
		}
		replaced = true
		if err := os.Remove(path); err != nil {
			hookErr = err
			return
		}
		hookErr = syscall.Mkfifo(path, 0o600)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := copyCandidateFilesWithHooks(ctx, fixture.worktree.Path, destination, hooks)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !replaced {
		t.Fatal("copy race hook did not replace the candidate file")
	}
	if err == nil || time.Since(started) >= time.Second {
		t.Fatalf("candidate copy did not fail closed before its deadline: err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestIssue138SnapshotRejectsLstatToFIFORaceWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "regular.txt")
	if err := os.WriteFile(path, []byte("regular\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var hookErr error
	replaced := false
	hooks := commandIsolationHooks{afterLstat: func(current string) {
		if replaced || current != path {
			return
		}
		replaced = true
		if err := os.Remove(current); err != nil {
			hookErr = err
			return
		}
		hookErr = syscall.Mkfifo(current, 0o600)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := snapshotCommandTreeWithHooks(ctx, root, hooks)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !replaced {
		t.Fatal("snapshot race hook did not replace the isolate file")
	}
	if err == nil || time.Since(started) >= time.Second {
		t.Fatalf("snapshot did not fail closed before its deadline: err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestIssue138SlowReadStopsAtContextDeadline(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = copyStreamContext(ctx, io.Discard, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow verifier read returned %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("slow verifier read exceeded its bounded deadline: %s", elapsed)
	}
}

func TestIssue138IsolationEnvironmentCoversCachesWithExactModes(t *testing.T) {
	root := t.TempDir()
	environment, err := commandIsolationEnvironment(root, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	for _, name := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "PYTHONPYCACHEPREFIX", "PIP_CACHE_DIR", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "GOPATH", "npm_config_cache", "CARGO_HOME", "RUSTUP_HOME"} {
		path := values[name]
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("cache %s is not an exact 0700 directory: path=%q info=%v err=%v", name, path, info, statErr)
		}
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
