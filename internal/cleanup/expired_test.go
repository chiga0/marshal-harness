package cleanup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type expiredFixture struct {
	repository string
	stateRoot  string
	baseSHA    string
	runs       int
}

var expiredBaseTime = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func newExpiredFixture(t *testing.T) expiredFixture {
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
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	return expiredFixture{repository: repository, stateRoot: location.StateRoot, baseSHA: base}
}

type expiredRunOptions struct {
	state         domain.State
	updatedAt     time.Time
	retentionDays int
	dirty         bool
	sessionState  string
}

type expiredRun struct {
	runID, taskID, worktreePath, branch string
}

func (f *expiredFixture) addRun(t *testing.T, options expiredRunOptions) expiredRun {
	t.Helper()
	f.runs++
	runID := "run-expired-" + string(rune('a'+f.runs-1))
	taskID := "task-expired-" + string(rune('a'+f.runs-1))
	manager, err := gitworktree.Open(f.repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(f.stateRoot, taskID, f.baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	path, branch := worktree.Path, worktree.Branch
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = gitFixtureCommand(nil, f.repository, "worktree", "remove", "--force", path)
		_ = gitFixtureCommand(nil, f.repository, "branch", "-D", branch)
	})
	if options.dirty {
		if err := os.WriteFile(filepath.Join(path, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, options.updatedAt)
	state.State, state.BaseSHA, state.WorktreePath = options.state, f.baseSHA, path
	state.UpdatedAt = options.updatedAt
	store := runstore.New(f.stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	_ = lease.Release()
	runDir := filepath.Join(f.stateRoot, "runs", runID)
	if options.retentionDays > 0 {
		policy := `{"effective":{"retentionDays":` + itoa(options.retentionDays) + `}}`
		if err := os.WriteFile(filepath.Join(runDir, "policy-snapshot.json"), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if options.sessionState != "" {
		directory := filepath.Join(runDir, "attempts", "attempt-01")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		session := `{"state":"` + options.sessionState + `"}`
		if err := os.WriteFile(filepath.Join(directory, "terminal-session.json"), []byte(session), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return expiredRun{runID: runID, taskID: taskID, worktreePath: path, branch: branch}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func (f expiredFixture) input(apply bool, now time.Time) ExpiredInput {
	return ExpiredInput{StateRoot: f.stateRoot, RepositoryRoot: f.repository, Apply: apply, Actor: "op:1", Now: now}
}

func findRun(result ExpiredResult, runID string) *ExpiredRun {
	for index := range result.Runs {
		if result.Runs[index].RunID == runID {
			return &result.Runs[index]
		}
	}
	return nil
}

func TestExpiredPreviewListsOnlyTerminalExpiredRuns(t *testing.T) {
	fixture := newExpiredFixture(t)
	now := expiredBaseTime.AddDate(0, 0, 40)
	expired := fixture.addRun(t, expiredRunOptions{state: domain.StateAccepted, updatedAt: expiredBaseTime, retentionDays: 1})
	fresh := fixture.addRun(t, expiredRunOptions{state: domain.StateAccepted, updatedAt: expiredBaseTime, retentionDays: 90})
	running := fixture.addRun(t, expiredRunOptions{state: domain.StateRunning, updatedAt: expiredBaseTime, retentionDays: 1})
	defaulted := fixture.addRun(t, expiredRunOptions{state: domain.StateBlocked, updatedAt: now.AddDate(0, 0, -31)})
	barely := fixture.addRun(t, expiredRunOptions{state: domain.StateRejected, updatedAt: now.AddDate(0, 0, -29)})

	before := snapshotDirectory(t, fixture.stateRoot)
	result, err := ExecuteExpired(context.Background(), fixture.input(false, now))
	if err != nil || result.Applied {
		t.Fatalf("preview=%+v error=%v", result, err)
	}
	if len(result.Runs) != 2 {
		t.Fatalf("preview runs = %+v", result.Runs)
	}
	first, second := result.Runs[0], result.Runs[1]
	if first.RunID != expired.runID || second.RunID != defaulted.runID {
		t.Fatalf("preview runs = %s, %s", first.RunID, second.RunID)
	}
	if first.RetentionDays != 1 || second.RetentionDays != DefaultRetentionDays {
		t.Fatalf("retention = %d, %d", first.RetentionDays, second.RetentionDays)
	}
	for _, run := range []ExpiredRun{first, second} {
		if run.Outcome != OutcomeExpired || len(run.Targets) != 2 ||
			run.Targets[0].Action != "remove-run-directory" || run.Targets[1].Action != "git-worktree-remove-clean" {
			t.Fatalf("preview run = %+v", run)
		}
	}
	if findRun(result, fresh.runID) != nil || findRun(result, running.runID) != nil || findRun(result, barely.runID) != nil {
		t.Fatalf("preview included a non-expired or non-terminal run: %+v", result.Runs)
	}
	if after := snapshotDirectory(t, fixture.stateRoot); after != before {
		t.Fatalf("preview wrote state:\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := os.Lstat(filepath.Join(fixture.stateRoot, "archive")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview wrote archive state: %v", err)
	}
}

// snapshotDirectory records every entry below root as "path:mode:size" so
// tests can prove a preview performed zero writes.
func snapshotDirectory(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, path+":"+info.Mode().String()+":"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	return strings.Join(entries, "\n")
}

func TestExpiredApplyArchivesThenRemovesCleanRun(t *testing.T) {
	fixture := newExpiredFixture(t)
	now := expiredBaseTime.AddDate(0, 0, 40)
	run := fixture.addRun(t, expiredRunOptions{state: domain.StateAccepted, updatedAt: expiredBaseTime, retentionDays: 1})

	noActor := fixture.input(true, now)
	noActor.Actor = ""
	if _, err := ExecuteExpired(context.Background(), noActor); !errors.Is(err, ErrActorRequired) {
		t.Fatalf("apply without actor = %v", err)
	}

	result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
	if err != nil {
		t.Fatal(err)
	}
	applied := findRun(result, run.runID)
	if applied == nil || applied.Outcome != OutcomeRemoved || len(applied.Targets) != 2 {
		t.Fatalf("apply result = %+v", result)
	}
	if _, err := os.Lstat(filepath.Join(fixture.stateRoot, "runs", run.runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run directory still exists: %v", err)
	}
	if _, err := os.Lstat(run.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if output := gitFixtureCommand(nil, fixture.repository, "show-ref", "--verify", "refs/heads/"+run.branch); strings.TrimSpace(output) == "" {
		t.Fatal("local branch was removed")
	}
	tarPath := filepath.Join(fixture.stateRoot, "archive", run.runID+".tar.gz")
	if applied.ArchivePath != tarPath {
		t.Fatalf("archive path = %q", applied.ArchivePath)
	}
	names := listTarEntries(t, tarPath)
	for _, required := range []string{"state.json", "policy-snapshot.json"} {
		if !names[required] {
			t.Fatalf("archive missing %s: %v", required, names)
		}
	}
	recordData, err := os.ReadFile(filepath.Join(fixture.stateRoot, "archive", run.runID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record ArchiveRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	if record.RunID != run.runID || record.TaskID != run.taskID || record.Kind != archiveRunKind ||
		record.ArchivePath != tarPath || record.Actor != "op:1" || !validArchiveDigest(record.Digest) {
		t.Fatalf("run evidence record = %+v", record)
	}
	repeat, err := ExecuteExpired(context.Background(), fixture.input(true, now))
	if err != nil || len(repeat.Runs) != 0 {
		t.Fatalf("idempotent apply=%+v error=%v", repeat, err)
	}
}

func TestExpiredApplyKeepsDirtyUnarchivedWorktree(t *testing.T) {
	fixture := newExpiredFixture(t)
	now := expiredBaseTime.AddDate(0, 0, 40)
	run := fixture.addRun(t, expiredRunOptions{state: domain.StateBlocked, updatedAt: expiredBaseTime, retentionDays: 1, dirty: true})
	result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
	if err != nil {
		t.Fatal(err)
	}
	applied := findRun(result, run.runID)
	if applied == nil || applied.Outcome != OutcomeRemovedWorktreeKept {
		t.Fatalf("apply result = %+v", result)
	}
	if applied.Targets[1].Action != "retained-dirty-unarchived" {
		t.Fatalf("worktree target = %+v", applied.Targets)
	}
	if _, err := os.Lstat(filepath.Join(fixture.stateRoot, "runs", run.runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.worktreePath, "unarchived.txt")); err != nil {
		t.Fatalf("dirty worktree evidence removed: %v", err)
	}
}

func TestExpiredApplyRemovesDirtyArchivedWorktree(t *testing.T) {
	fixture := newExpiredFixture(t)
	now := expiredBaseTime.AddDate(0, 0, 40)
	run := fixture.addRun(t, expiredRunOptions{state: domain.StateBlocked, updatedAt: expiredBaseTime, retentionDays: 1, dirty: true})
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureOutcome(t, fixture.stateRoot, run.runID, run.taskID, domain.StateBlocked)
	export, err := Execute(context.Background(), Input{
		StateRoot: fixture.stateRoot, RepositoryRoot: fixture.repository, RunID: run.runID,
		ExportPatch: true, Actor: "op:1", Now: now, Validator: validator,
	})
	if err != nil || !export.Exported {
		t.Fatalf("export=%+v error=%v", export, err)
	}
	result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
	if err != nil {
		t.Fatal(err)
	}
	applied := findRun(result, run.runID)
	if applied == nil || applied.Outcome != OutcomeRemoved || applied.Targets[1].Action != "git-worktree-remove-archived" {
		t.Fatalf("apply result = %+v", result)
	}
	if _, err := os.Lstat(run.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived dirty worktree still exists: %v", err)
	}
}

func TestExpiredApplyFailsClosedAndRollsBack(t *testing.T) {
	t.Run("active lease", func(t *testing.T) {
		fixture := newExpiredFixture(t)
		now := expiredBaseTime.AddDate(0, 0, 40)
		run := fixture.addRun(t, expiredRunOptions{state: domain.StateAccepted, updatedAt: expiredBaseTime, retentionDays: 1})
		lease, err := runstore.New(fixture.stateRoot).Acquire(run.runID)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
		if err != nil {
			t.Fatal(err)
		}
		applied := findRun(result, run.runID)
		if applied == nil || applied.Outcome != OutcomeSkippedActiveLease {
			t.Fatalf("apply result = %+v", result)
		}
		if _, err := os.Stat(filepath.Join(fixture.stateRoot, "runs", run.runID, "state.json")); err != nil {
			t.Fatalf("skipped run directory removed: %v", err)
		}
	})
	t.Run("active session", func(t *testing.T) {
		fixture := newExpiredFixture(t)
		now := expiredBaseTime.AddDate(0, 0, 40)
		run := fixture.addRun(t, expiredRunOptions{state: domain.StateAborted, updatedAt: expiredBaseTime, retentionDays: 1, sessionState: "running"})
		result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
		if err != nil {
			t.Fatal(err)
		}
		applied := findRun(result, run.runID)
		if applied == nil || applied.Outcome != OutcomeSkippedActiveSession {
			t.Fatalf("apply result = %+v", result)
		}
		if _, err := os.Stat(filepath.Join(fixture.stateRoot, "runs", run.runID, "state.json")); err != nil {
			t.Fatalf("skipped run directory removed: %v", err)
		}
	})
	t.Run("archive failure keeps run directory", func(t *testing.T) {
		fixture := newExpiredFixture(t)
		now := expiredBaseTime.AddDate(0, 0, 40)
		run := fixture.addRun(t, expiredRunOptions{state: domain.StateAccepted, updatedAt: expiredBaseTime, retentionDays: 1})
		archiveDir := filepath.Join(fixture.stateRoot, "archive")
		if err := os.MkdirAll(filepath.Join(archiveDir, run.runID+".tar.gz"), 0o700); err != nil {
			t.Fatal(err)
		}
		result, err := ExecuteExpired(context.Background(), fixture.input(true, now))
		if err != nil {
			t.Fatal(err)
		}
		applied := findRun(result, run.runID)
		if applied == nil || applied.Outcome != OutcomeFailedArchive {
			t.Fatalf("apply result = %+v", result)
		}
		if _, err := os.Stat(filepath.Join(fixture.stateRoot, "runs", run.runID, "state.json")); err != nil {
			t.Fatalf("failed run directory removed: %v", err)
		}
		if _, err := os.Stat(run.worktreePath); err != nil {
			t.Fatalf("failed run worktree removed: %v", err)
		}
	})
}

func listTarEntries(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("corrupt evidence archive: %v", err)
	}
	defer gz.Close()
	names := map[string]bool{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("read evidence archive: %v", err)
		}
		names[header.Name] = true
		if _, err := io.Copy(io.Discard, reader); err != nil {
			t.Fatal(err)
		}
	}
}

func rewriteSnapshotState(t *testing.T, fixture cleanupFixture, state domain.State) {
	t.Helper()
	store := runstore.New(fixture.stateRoot)
	lease, err := store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	snapshot, err := store.ReadSnapshot(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State = state
	if err := store.WriteSnapshot(lease, snapshot); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureOutcome(t *testing.T, stateRoot, runID, taskID string, terminal domain.State) {
	t.Helper()
	runDir := filepath.Join(stateRoot, "runs", runID)
	outcome := domain.OutcomeBundle{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome, TaskID: taskID, RunID: runID,
		TerminalState: terminal, Verdict: verdictFor(terminal), FinalReviewRound: 1,
		FinalReviewDigest:   "sha256:" + strings.Repeat("a", 64),
		FinalEvidenceDigest: "sha256:" + strings.Repeat("b", 64), Summary: "expired fixture", FindingCount: 0,
		RetentionPolicy: "retain", GeneratedAt: expiredBaseTime,
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "outcome.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "result.md"), []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
