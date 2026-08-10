package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func TestCleanupPreviewAndApplyRetainEvidenceAndBranch(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateAccepted)
	preview, err := Execute(context.Background(), fixture.input(false))
	if err != nil || preview.Applied || len(preview.Targets) != 1 {
		t.Fatalf("preview=%+v error=%v", preview, err)
	}
	if _, err := os.Stat(fixture.worktreePath); err != nil {
		t.Fatalf("preview changed worktree: %v", err)
	}
	applied, err := Execute(context.Background(), fixture.input(true))
	if err != nil || !applied.Applied || len(applied.Targets) != 1 {
		t.Fatalf("apply=%+v error=%v", applied, err)
	}
	if _, err := os.Lstat(fixture.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}
	for _, name := range []string{"state.json", "outcome.json", "result.md"} {
		if _, err := os.Stat(filepath.Join(fixture.runDir, name)); err != nil {
			t.Fatalf("retained evidence %s missing: %v", name, err)
		}
	}
	if output := fixture.git("show-ref", "--verify", "refs/heads/"+fixture.branch); strings.TrimSpace(output) == "" {
		t.Fatal("local branch was removed")
	}
	again, err := Execute(context.Background(), fixture.input(true))
	if err != nil || len(again.Targets) != 0 {
		t.Fatalf("idempotent apply=%+v error=%v", again, err)
	}
}

func TestCleanupRemovesEachRunWorktreeOfSameTaskIndependently(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateAccepted)
	const retryRunID = "run-cleanup-retry"
	manager, err := gitworktree.Open(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := manager.CreateForRun(fixture.stateRoot, fixture.taskID, retryRunID, fixture.baseSHA)
	if err != nil {
		t.Fatalf("retry run of the same task could not create its own worktree: %v", err)
	}
	retryPath, retryBranch := retry.Path, retry.Branch
	if err := retry.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = gitFixtureCommand(nil, fixture.repository, "worktree", "remove", "--force", retryPath)
		_ = gitFixtureCommand(nil, fixture.repository, "branch", "-D", retryBranch)
	})
	if want := fixture.taskID + "-" + retryRunID; filepath.Base(retryPath) != want {
		t.Fatalf("retry worktree name = %q, want %q", filepath.Base(retryPath), want)
	}
	writeCleanupRunState(t, fixture.stateRoot, fixture.taskID, retryRunID, domain.StateAccepted, fixture.baseSHA, retryPath)

	first, err := Execute(context.Background(), fixture.input(true))
	if err != nil || !first.Applied {
		t.Fatalf("first run cleanup=%+v error=%v", first, err)
	}
	if _, err := os.Lstat(fixture.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first run worktree still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(retryPath, "README.md")); err != nil {
		t.Fatalf("retry worktree was touched by the first run's cleanup: %v", err)
	}

	retryInput := fixture.input(true)
	retryInput.RunID = retryRunID
	second, err := Execute(context.Background(), retryInput)
	if err != nil || !second.Applied {
		t.Fatalf("retry run cleanup=%+v error=%v", second, err)
	}
	if _, err := os.Lstat(retryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry worktree still exists: %v", err)
	}
}

func TestCleanupGuardsPreserveWorktree(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateBlocked)
		if err := os.WriteFile(filepath.Join(fixture.worktreePath, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrDirtyWorktree) {
			t.Fatalf("dirty cleanup = %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.worktreePath, "unarchived.txt")); err != nil {
			t.Fatalf("dirty evidence removed: %v", err)
		}
	})
	t.Run("active session", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateAborted)
		directory := filepath.Join(fixture.runDir, "attempts", "attempt-01")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "terminal-session.json"), []byte(`{"state":"paused"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrActiveSession) {
			t.Fatalf("active-session cleanup = %v", err)
		}
	})
	t.Run("non terminal", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateAccepted)
		store := runstore.New(fixture.stateRoot)
		lease, _ := store.Acquire(fixture.runID)
		state, _ := store.ReadSnapshot(fixture.runID)
		state.State = domain.StateRunning
		if err := store.WriteSnapshot(lease, state); err != nil {
			t.Fatal(err)
		}
		_ = lease.Release()
		if _, err := Execute(context.Background(), fixture.input(false)); !errors.Is(err, ErrNonTerminal) {
			t.Fatalf("non-terminal cleanup = %v", err)
		}
	})
	t.Run("active lease", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateAccepted)
		lease, _ := runstore.New(fixture.stateRoot).Acquire(fixture.runID)
		defer lease.Release()
		if _, err := Execute(context.Background(), fixture.input(false)); !errors.Is(err, runstore.ErrLeaseHeld) {
			t.Fatalf("active-lease cleanup = %v", err)
		}
	})
}

func TestCleanupRecoversAfterRemovalBeforeCompletedTombstone(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateAccepted)
	state, _ := runstore.New(fixture.stateRoot).Inspect(fixture.runID)
	when := time.Unix(200, 0).UTC()
	if err := appendTombstone(fixture.runDir, tombstone{RunID: state.RunID, TaskID: state.TaskID, Kind: "managed-worktree", Path: fixture.worktreePath, Phase: "planned", CreatedAt: when}); err != nil {
		t.Fatal(err)
	}
	repository, _ := gitworktree.Open(fixture.repository)
	worktree, err := repository.Acquire(fixture.stateRoot, fixture.taskID, fixture.worktreePath, fixture.baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.RemoveClean(); err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), fixture.input(true))
	if err != nil || len(result.Targets) != 1 {
		t.Fatalf("recovery=%+v error=%v", result, err)
	}
	completed, _, err := readTombstones(fixture.runDir, state)
	if err != nil || !completed[fixture.worktreePath] {
		t.Fatalf("completed tombstone=%v error=%v", completed, err)
	}
}

func TestCleanupRejectsUntrustedTombstoneJournal(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, cleanupFixture)
	}{
		{name: "truncated", prepare: func(t *testing.T, fixture cleanupFixture) {
			directory := filepath.Join(fixture.runDir, "cleanup")
			_ = os.MkdirAll(directory, 0o700)
			if err := os.WriteFile(filepath.Join(directory, "tombstones.jsonl"), []byte(`{"runId":"run-cleanup"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", prepare: func(t *testing.T, fixture cleanupFixture) {
			directory := filepath.Join(fixture.runDir, "cleanup")
			_ = os.MkdirAll(directory, 0o700)
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, "tombstones.jsonl")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture(t, domain.StateAccepted)
			test.prepare(t, fixture)
			if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrTargetIdentity) {
				t.Fatalf("untrusted journal cleanup = %v", err)
			}
			if _, err := os.Stat(fixture.worktreePath); err != nil {
				t.Fatalf("worktree changed: %v", err)
			}
		})
	}
}

type cleanupFixture struct {
	repository, stateRoot, runDir, worktreePath, branch, baseSHA string
	taskID, runID                                                string
	validator                                                    *contract.Validator
}

func newCleanupFixture(t *testing.T, terminal domain.State) cleanupFixture {
	t.Helper()
	repository := t.TempDir()
	gitFixtureCommand(t, repository, "init", "-q")
	gitFixtureCommand(t, repository, "config", "user.name", "Marshal Test")
	gitFixtureCommand(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixtureCommand(t, repository, "add", "README.md")
	gitFixtureCommand(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitFixtureCommand(t, repository, "rev-parse", "HEAD"))
	location, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatalf("discover state: %v", err)
	}
	if err := location.Init(); err != nil {
		t.Fatalf("init state: %v", err)
	}
	manager, _ := gitworktree.Open(repository)
	worktree, err := manager.Create(location.StateRoot, "task-cleanup", base)
	if err != nil {
		t.Fatal(err)
	}
	path, branch := worktree.Path, worktree.Branch
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	validator, _ := contract.NewValidator()
	state := writeCleanupRunState(t, location.StateRoot, "task-cleanup", "run-cleanup", terminal, base, path)
	fixture := cleanupFixture{repository: repository, stateRoot: location.StateRoot, runDir: filepath.Join(location.StateRoot, "runs", state.RunID), worktreePath: path, branch: branch, baseSHA: base, taskID: state.TaskID, runID: state.RunID, validator: validator}
	t.Cleanup(func() {
		_ = gitFixtureCommand(nil, repository, "worktree", "remove", "--force", path)
		_ = gitFixtureCommand(nil, repository, "branch", "-D", branch)
	})
	return fixture
}

func writeCleanupRunState(t *testing.T, stateRoot, taskID, runID string, terminal domain.State, baseSHA, worktreePath string) domain.RunState {
	t.Helper()
	state := domain.NewRunState(taskID, runID, time.Unix(100, 0).UTC())
	state.State, state.BaseSHA, state.WorktreePath = terminal, baseSHA, worktreePath
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	_ = lease.Release()
	runDir := filepath.Join(stateRoot, "runs", runID)
	outcome := domain.OutcomeBundle{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome, TaskID: state.TaskID, RunID: state.RunID,
		TerminalState: terminal, Verdict: verdictFor(terminal), FinalReviewRound: 1,
		FinalReviewDigest:   "sha256:" + strings.Repeat("a", 64),
		FinalEvidenceDigest: "sha256:" + strings.Repeat("b", 64), Summary: "cleanup fixture", FindingCount: 0,
		RetentionPolicy: "retain", GeneratedAt: time.Unix(101, 0).UTC(),
	}
	data, _ := json.Marshal(outcome)
	if err := os.WriteFile(filepath.Join(runDir, "outcome.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "result.md"), []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state
}

func (f cleanupFixture) input(apply bool) Input {
	return Input{StateRoot: f.stateRoot, RepositoryRoot: f.repository, RunID: f.runID, Apply: apply, Now: time.Unix(200, 0), Validator: f.validator}
}

func (f cleanupFixture) git(args ...string) string {
	return gitFixtureCommand(nil, f.repository, args...)
}

func verdictFor(state domain.State) string {
	switch state {
	case domain.StateAccepted:
		return "accept"
	case domain.StateBlocked:
		return "blocked"
	case domain.StateAborted:
		return "abort"
	default:
		return "reject"
	}
}

func gitFixtureCommand(t testing.TB, directory string, args ...string) string {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil && t != nil {
		t.Helper()
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestRecordLegacyOutcomeReconstructsThenAllowsCleanup(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateBlocked)
	if err := os.Remove(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.runDir, "result.md")); err != nil {
		t.Fatal(err)
	}
	// Without an outcome, cleanup must refuse.
	if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrOutcomeMissing) {
		t.Fatalf("cleanup without outcome err = %v, want ErrOutcomeMissing", err)
	}
	in := fixture.input(false)
	in.Actor = "op:1"
	if _, err := RecordLegacyOutcome(context.Background(), in); err != nil {
		t.Fatalf("RecordLegacyOutcome = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.runDir, "outcome.json"))
	if err != nil {
		t.Fatalf("reconstructed outcome missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, data); err != nil {
		t.Fatalf("reconstructed outcome invalid: %v", err)
	}
	// A second record must refuse to overwrite.
	if _, err := RecordLegacyOutcome(context.Background(), in); !errors.Is(err, ErrOutcomeExists) {
		t.Fatalf("second record err = %v, want ErrOutcomeExists", err)
	}
	// Now cleanup can proceed (worktree is clean).
	if _, err := Execute(context.Background(), fixture.input(true)); err != nil {
		t.Fatalf("cleanup after migration = %v", err)
	}
}

func TestRecordLegacyOutcomeRequiresActorAndTerminal(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateBlocked)
	if err := os.Remove(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
		t.Fatal(err)
	}
	noActor := fixture.input(false)
	if _, err := RecordLegacyOutcome(context.Background(), noActor); !errors.Is(err, ErrActorRequired) {
		t.Fatalf("no actor err = %v, want ErrActorRequired", err)
	}
}
