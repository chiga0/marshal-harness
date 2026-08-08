package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func (f cleanupFixture) exportInput(actor string) Input {
	input := f.input(false)
	input.ExportPatch = true
	input.Actor = actor
	return input
}

func makeWorktreeDirty(t *testing.T, fixture cleanupFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("base\nmodified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupExportPatchThenApplyArchived(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateBlocked)
	makeWorktreeDirty(t, fixture)

	if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("unarchived dirty apply = %v", err)
	}
	if _, err := Execute(context.Background(), fixture.exportInput("")); !errors.Is(err, ErrActorRequired) {
		t.Fatalf("export without actor = %v", err)
	}

	exported, err := Execute(context.Background(), fixture.exportInput("op:1"))
	if err != nil || !exported.Exported || exported.Applied {
		t.Fatalf("export=%+v error=%v", exported, err)
	}
	patchPath := filepath.Join(fixture.stateRoot, "archive", fixture.runID+".patch")
	if exported.ArchivePath != patchPath {
		t.Fatalf("archive path = %q, want %q", exported.ArchivePath, patchPath)
	}
	patchInfo, err := os.Lstat(patchPath)
	if err != nil || patchInfo.Mode().Perm() != 0o600 {
		t.Fatalf("patch file = %+v err=%v", patchInfo, err)
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "unarchived.txt") || !strings.Contains(string(patch), "+modified") {
		t.Fatalf("patch does not carry untracked and tracked changes:\n%s", patch)
	}
	recordData, err := os.ReadFile(filepath.Join(fixture.stateRoot, "archive", fixture.runID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record ArchiveRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	if record.RunID != fixture.runID || record.TaskID != fixture.taskID || record.Kind != archivePatchKind ||
		record.Digest == "" || record.Actor != "op:1" || record.ExportedAt.IsZero() || record.ArchivePath != patchPath {
		t.Fatalf("archive record = %+v", record)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktreePath, "unarchived.txt")); err != nil {
		t.Fatalf("export modified the worktree: %v", err)
	}

	preview, err := Execute(context.Background(), fixture.input(false))
	if err != nil || preview.Applied || len(preview.Targets) != 1 || preview.Targets[0].Action != "git-worktree-remove-archived" {
		t.Fatalf("archived preview=%+v error=%v", preview, err)
	}
	applied, err := Execute(context.Background(), fixture.input(true))
	if err != nil || !applied.Applied || len(applied.Targets) != 1 || applied.Targets[0].Action != "git-worktree-remove-archived" {
		t.Fatalf("archived apply=%+v error=%v", applied, err)
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

func TestCleanupApplyRejectsDirtyDriftAfterExport(t *testing.T) {
	fixture := newCleanupFixture(t, domain.StateBlocked)
	makeWorktreeDirty(t, fixture)
	if _, err := Execute(context.Background(), fixture.exportInput("op:1")); err != nil {
		t.Fatalf("export=%v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "unarchived.txt"), []byte("keep\nmore\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), fixture.input(true)); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("drifted apply = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.worktreePath, "unarchived.txt")); err != nil {
		t.Fatalf("drifted apply removed evidence: %v", err)
	}
	if _, err := Execute(context.Background(), fixture.exportInput("op:1")); err != nil {
		t.Fatalf("re-export=%v", err)
	}
	applied, err := Execute(context.Background(), fixture.input(true))
	if err != nil || !applied.Applied {
		t.Fatalf("apply after re-export=%+v error=%v", applied, err)
	}
}

func TestCleanupExportRefusesCleanAndNonTerminal(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateAccepted)
		if _, err := Execute(context.Background(), fixture.exportInput("op:1")); !errors.Is(err, ErrExportClean) {
			t.Fatalf("clean export = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.stateRoot, "archive", fixture.runID+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("clean export wrote an archive record: %v", err)
		}
	})
	t.Run("non terminal", func(t *testing.T) {
		fixture := newCleanupFixture(t, domain.StateAccepted)
		rewriteSnapshotState(t, fixture, domain.StateRunning)
		if _, err := Execute(context.Background(), fixture.exportInput("op:1")); !errors.Is(err, ErrNonTerminal) {
			t.Fatalf("non-terminal export = %v", err)
		}
	})
}

func TestVerifyArchiveTarRejectsCorruption(t *testing.T) {
	directory := t.TempDir()
	corrupt := filepath.Join(directory, "corrupt.tar.gz")
	if err := os.WriteFile(corrupt, []byte("not a gzip stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveTar(corrupt); err == nil {
		t.Fatal("verifyArchiveTar accepted garbage")
	}
	source := filepath.Join(directory, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "state.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(directory, "valid.tar.gz")
	archive, err := os.Create(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildRunTar(archive, source); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveTar(valid); err != nil {
		t.Fatalf("verifyArchiveTar rejected a valid archive: %v", err)
	}
	truncated := filepath.Join(directory, "truncated.tar.gz")
	data, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveTar(truncated); err == nil {
		t.Fatal("verifyArchiveTar accepted a truncated archive")
	}
}

func TestRetentionDaysFallsBackToDefault(t *testing.T) {
	directory := t.TempDir()
	if got := retentionDaysFor(directory); got != DefaultRetentionDays {
		t.Fatalf("missing policy retention = %d", got)
	}
	for name, content := range map[string]string{
		"malformed": "{",
		"zero":      `{"effective":{"retentionDays":0}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, "policy-snapshot.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := retentionDaysFor(directory); got != DefaultRetentionDays {
				t.Fatalf("retention = %d", got)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(directory, "policy-snapshot.json"), []byte(`{"effective":{"retentionDays":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := retentionDaysFor(directory); got != 7 {
		t.Fatalf("retention = %d", got)
	}
}

func TestRunExpiredHonorsRetentionBoundary(t *testing.T) {
	updatedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if runExpired(updatedAt.AddDate(0, 0, 30), updatedAt, 30) {
		t.Fatal("boundary instant must not be expired")
	}
	if !runExpired(updatedAt.AddDate(0, 0, 31), updatedAt, 30) {
		t.Fatal("day after retention must be expired")
	}
	if runExpired(updatedAt.AddDate(0, 0, 365), time.Time{}, 30) {
		t.Fatal("zero updatedAt must fail closed")
	}
}
