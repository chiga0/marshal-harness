package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Expired outcomes are fixed strings so operators and tests can compare them
// without parsing free text.
const (
	OutcomeExpired              = "expired"
	OutcomeRemoved              = "removed"
	OutcomeRemovedWorktreeKept  = "removed-worktree-retained"
	OutcomeSkippedActiveLease   = "skipped-active-lease"
	OutcomeSkippedActiveSession = "skipped-active-session"
	OutcomeSkippedNotExpired    = "skipped-not-expired"
	OutcomeFailedArchive        = "failed-archive"
	OutcomeFailedRemoveRunDir   = "failed-remove-run-directory"
	OutcomeFailedRemoveWorktree = "failed-remove-worktree"
)

// ErrActorRequired rejects any mutating cleanup that carries no operator ID.
var ErrActorRequired = errors.New("cleanup requires an operator actor")

type ExpiredInput struct {
	StateRoot, RepositoryRoot string
	Apply                     bool
	Actor                     string
	Now                       time.Time
}

// ExpiredRun is one terminal Run past its retention. In preview mode Targets
// describe exactly what --apply would remove; nothing is written.
type ExpiredRun struct {
	RunID         string       `json:"runId"`
	TaskID        string       `json:"taskId"`
	State         domain.State `json:"state"`
	UpdatedAt     time.Time    `json:"updatedAt"`
	RetentionDays int          `json:"retentionDays"`
	Outcome       string       `json:"outcome"`
	ArchivePath   string       `json:"archivePath,omitempty"`
	Targets       []Target     `json:"targets"`
}

type ExpiredResult struct {
	Applied bool         `json:"applied"`
	Actor   string       `json:"actor,omitempty"`
	Runs    []ExpiredRun `json:"runs"`
}

// ExecuteExpired lists terminal Runs whose updatedAt is older than their own
// policy retentionDays. Preview performs no writes at all; --apply first
// archives each run directory as a verified tar and only then removes the run
// directory and its clean or archive-proven worktree.
func ExecuteExpired(ctx context.Context, input ExpiredInput) (ExpiredResult, error) {
	if ctx == nil || input.StateRoot == "" || input.RepositoryRoot == "" {
		return ExpiredResult{}, ErrTargetIdentity
	}
	if err := ctx.Err(); err != nil {
		return ExpiredResult{}, err
	}
	if input.Apply {
		actor := strings.TrimSpace(input.Actor)
		if actor == "" || len(actor) > 512 {
			return ExpiredResult{}, ErrActorRequired
		}
		input.Actor = actor
	}
	now := cleanupTime(input.Now)
	store := runstore.New(input.StateRoot)
	runIDs, err := listRunDirectories(input.StateRoot)
	if err != nil {
		return ExpiredResult{}, err
	}
	result := ExpiredResult{Applied: input.Apply, Actor: input.Actor, Runs: []ExpiredRun{}}
	for _, runID := range runIDs {
		if err := ctx.Err(); err != nil {
			return ExpiredResult{}, err
		}
		state, err := store.Inspect(runID)
		if err != nil || state.RunID != runID || !state.State.Terminal() {
			continue
		}
		runDir := filepath.Join(input.StateRoot, "runs", runID)
		retentionDays := retentionDaysFor(runDir)
		if !runExpired(now, state.UpdatedAt, retentionDays) {
			continue
		}
		candidate := ExpiredRun{
			RunID: runID, TaskID: state.TaskID, State: state.State, UpdatedAt: state.UpdatedAt,
			RetentionDays: retentionDays, Outcome: OutcomeExpired,
			Targets: []Target{{Kind: "run-directory", Path: runDir, Action: "remove-run-directory"}},
		}
		if !input.Apply {
			candidate.Targets = append(candidate.Targets, previewExpiredWorktreeTarget(ctx, input, state)...)
			result.Runs = append(result.Runs, candidate)
			continue
		}
		result.Runs = append(result.Runs, applyExpiredRun(ctx, input, store, candidate))
	}
	return result, nil
}

func runExpired(now, updatedAt time.Time, retentionDays int) bool {
	if updatedAt.IsZero() || retentionDays <= 0 {
		return false
	}
	return now.After(updatedAt.UTC().AddDate(0, 0, retentionDays))
}

func listRunDirectories(stateRoot string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(stateRoot, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if domain.ValidateID(entry.Name()) != nil {
			continue
		}
		runIDs = append(runIDs, entry.Name())
	}
	sort.Strings(runIDs)
	return runIDs, nil
}

// previewExpiredWorktreeTarget classifies the worktree read-only and without
// Marshal locks: previews never write, so the classification is an advisory
// snapshot that --apply re-proves under the real locks.
func previewExpiredWorktreeTarget(ctx context.Context, input ExpiredInput, state domain.RunState) []Target {
	if state.WorktreePath == "" {
		return nil
	}
	target := Target{Kind: "managed-worktree", Path: state.WorktreePath}
	if !validTargetPath(input.StateRoot, state.WorktreePath) {
		target.Action = "retained-unproven"
		return []Target{target}
	}
	if _, err := os.Lstat(state.WorktreePath); err != nil {
		return nil
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		target.Action = "retained-unproven"
		return []Target{target}
	}
	clean, err := repository.CleanSnapshot(state.WorktreePath)
	if err != nil {
		target.Action = "retained-unproven"
		return []Target{target}
	}
	if clean {
		target.Action = "git-worktree-remove-clean"
		return []Target{target}
	}
	target.Action = "git-worktree-remove-archived"
	if !worktreePatchCurrent(ctx, state, worktreePatchDigest(input.StateRoot, state)) {
		target.Action = "retained-dirty-unarchived"
	}
	return []Target{target}
}

func applyExpiredRun(ctx context.Context, input ExpiredInput, store *runstore.Store, candidate ExpiredRun) ExpiredRun {
	runDir := filepath.Join(input.StateRoot, "runs", candidate.RunID)
	lease, err := store.Acquire(candidate.RunID)
	if err != nil {
		candidate.Outcome = OutcomeSkippedActiveLease
		return candidate
	}
	defer lease.Release()
	state, err := store.Inspect(candidate.RunID)
	if err != nil || state.RunID != candidate.RunID || !state.State.Terminal() ||
		!runExpired(cleanupTime(input.Now), state.UpdatedAt, candidate.RetentionDays) {
		candidate.Outcome = OutcomeSkippedNotExpired
		return candidate
	}
	if err := rejectActiveSessions(runDir); err != nil {
		candidate.Outcome = OutcomeSkippedActiveSession
		return candidate
	}
	archivedDigest := worktreePatchDigest(input.StateRoot, state)
	archivePath, err := writeRunArchiveTar(input.StateRoot, candidate.RunID, runDir)
	if err != nil {
		candidate.Outcome = OutcomeFailedArchive
		return candidate
	}
	candidate.ArchivePath = archivePath
	if err := writeRunEvidenceRecord(input.StateRoot, candidate, archivePath, input); err != nil {
		candidate.Outcome = OutcomeFailedArchive
		return candidate
	}
	if err := removeRunDirectory(input.StateRoot, candidate.RunID); err != nil {
		candidate.Outcome = OutcomeFailedRemoveRunDir
		return candidate
	}
	worktreeTargets, outcome := applyExpiredWorktree(ctx, input, state, archivedDigest)
	candidate.Targets = append(candidate.Targets, worktreeTargets...)
	if outcome != "" {
		candidate.Outcome = outcome
		return candidate
	}
	candidate.Outcome = OutcomeRemoved
	for _, target := range candidate.Targets {
		if target.Action == "retained-dirty-unarchived" || target.Action == "retained-unproven" {
			candidate.Outcome = OutcomeRemovedWorktreeKept
		}
	}
	return candidate
}

// writeRunEvidenceRecord persists the run-evidence record binding the tar to
// the run, its digest and the operator. It replaces any earlier worktree-patch
// record of the same run, which applyExpiredRun already snapshotted before.
func writeRunEvidenceRecord(stateRoot string, candidate ExpiredRun, archivePath string, input ExpiredInput) error {
	digest, err := digestArchiveFile(archivePath)
	if err != nil {
		return err
	}
	record := ArchiveRecord{
		RunID: candidate.RunID, TaskID: candidate.TaskID, Kind: archiveRunKind, ArchivePath: archivePath,
		Digest: digest, ExportedAt: cleanupTime(input.Now), Actor: input.Actor,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeArchiveFile(archiveRecordPath(stateRoot, candidate.RunID), append(data, '\n'))
}

func applyExpiredWorktree(ctx context.Context, input ExpiredInput, state domain.RunState, archivedDigest string) ([]Target, string) {
	target := Target{Kind: "managed-worktree", Path: state.WorktreePath}
	if !validTargetPath(input.StateRoot, state.WorktreePath) {
		target.Action = "retained-unproven"
		return []Target{target}, ""
	}
	if _, err := os.Lstat(state.WorktreePath); err != nil {
		return nil, ""
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		target.Action = "retained-unproven"
		return []Target{target}, ""
	}
	worktree, err := repository.Acquire(input.StateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		target.Action = "retained-unproven"
		return []Target{target}, ""
	}
	defer worktree.Release()
	clean, err := worktree.Clean()
	if err != nil {
		target.Action = "retained-unproven"
		return []Target{target}, ""
	}
	if clean {
		target.Action = "git-worktree-remove-clean"
		if err := worktree.RemoveClean(); err != nil {
			target.Action = "retained-unproven"
			return []Target{target}, OutcomeFailedRemoveWorktree
		}
		return []Target{target}, ""
	}
	if !worktreePatchCurrent(ctx, state, archivedDigest) {
		target.Action = "retained-dirty-unarchived"
		return []Target{target}, ""
	}
	target.Action = "git-worktree-remove-archived"
	if err := worktree.RemoveArchived(); err != nil {
		target.Action = "retained-dirty-unarchived"
		return []Target{target}, OutcomeFailedRemoveWorktree
	}
	return []Target{target}, ""
}

// worktreePatchDigest returns the digest of a valid worktree-patch archive
// record bound to this run, or "" when no archive authorizes the worktree.
func worktreePatchDigest(stateRoot string, state domain.RunState) string {
	record, err := readArchiveRecord(stateRoot, state.RunID, archivePatchKind)
	if err != nil || record.TaskID != state.TaskID {
		return ""
	}
	return record.Digest
}

// worktreePatchCurrent verifies that the archived patch digest still matches
// the current worktree diff byte for byte. Any drift since export fails
// closed.
func worktreePatchCurrent(ctx context.Context, state domain.RunState, archivedDigest string) bool {
	if archivedDigest == "" {
		return false
	}
	observation, err := observeWorktree(ctx, state)
	if err != nil {
		return false
	}
	return observation.DiffDigest == archivedDigest
}

func removeRunDirectory(stateRoot, runID string) error {
	if domain.ValidateID(runID) != nil {
		return ErrTargetIdentity
	}
	runDir := filepath.Join(stateRoot, "runs", runID)
	info, err := os.Lstat(runDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrTargetIdentity
	}
	expectedParent, err := filepath.EvalSymlinks(filepath.Join(stateRoot, "runs"))
	if err != nil {
		return ErrTargetIdentity
	}
	actualParent, err := filepath.EvalSymlinks(filepath.Dir(runDir))
	if err != nil || actualParent != expectedParent {
		return ErrTargetIdentity
	}
	return os.RemoveAll(runDir)
}
