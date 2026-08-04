// Package cleanup previews and applies narrowly scoped local Run cleanup.
package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var (
	ErrNonTerminal    = errors.New("cleanup requires a terminal run")
	ErrOutcomeMissing = errors.New("cleanup requires a valid retained outcome")
	ErrActiveSession  = errors.New("cleanup refuses an active terminal session")
	ErrDirtyWorktree  = errors.New("cleanup refuses an unarchived dirty worktree")
	ErrTargetIdentity = errors.New("cleanup target identity is not provable")
)

type Input struct {
	StateRoot, RepositoryRoot, RunID string
	Apply                            bool
	Now                              time.Time
	Validator                        *contract.Validator
}

type Target struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Action string `json:"action"`
}

type Result struct {
	RunID   string   `json:"runId"`
	Applied bool     `json:"applied"`
	Targets []Target `json:"targets"`
}

type tombstone struct {
	RunID     string    `json:"runId"`
	TaskID    string    `json:"taskId"`
	Kind      string    `json:"kind"`
	Path      string    `json:"path"`
	Phase     string    `json:"phase"`
	CreatedAt time.Time `json:"createdAt"`
}

// Execute returns an exact preview unless Apply is true. It never deletes Run
// journals, outcomes, publication records, local branches or remote objects.
func Execute(ctx context.Context, input Input) (Result, error) {
	if ctx == nil || input.Validator == nil || input.StateRoot == "" || input.RepositoryRoot == "" || domain.ValidateID(input.RunID) != nil {
		return Result{}, ErrTargetIdentity
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return Result{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return Result{}, err
	}
	if !state.State.Terminal() {
		return Result{}, ErrNonTerminal
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	if err := validateOutcome(runDir, state, input.Validator); err != nil {
		return Result{}, err
	}
	if err := rejectActiveSessions(runDir); err != nil {
		return Result{}, err
	}

	completed, planned, err := readTombstones(runDir, state)
	if err != nil {
		return Result{}, err
	}
	path := state.WorktreePath
	if !validTargetPath(input.StateRoot, path) {
		return Result{}, ErrTargetIdentity
	}
	target := Target{Kind: "managed-worktree", Path: path, Action: "git-worktree-remove-clean"}
	if completed[path] {
		return Result{RunID: state.RunID, Applied: input.Apply, Targets: []Target{}}, nil
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) && planned[path] && input.Apply {
			now := cleanupTime(input.Now)
			if err := appendTombstone(runDir, tombstone{RunID: state.RunID, TaskID: state.TaskID, Kind: target.Kind, Path: path, Phase: "completed", CreatedAt: now}); err != nil {
				return Result{}, err
			}
			return Result{RunID: state.RunID, Applied: true, Targets: []Target{target}}, nil
		}
		return Result{}, ErrTargetIdentity
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		return Result{}, err
	}
	worktree, err := repository.Acquire(input.StateRoot, state.TaskID, path, state.BaseSHA)
	if err != nil {
		return Result{}, err
	}
	defer worktree.Release()
	clean, err := worktree.Clean()
	if err != nil {
		return Result{}, err
	}
	if !clean {
		return Result{}, ErrDirtyWorktree
	}
	result := Result{RunID: state.RunID, Applied: input.Apply, Targets: []Target{target}}
	if !input.Apply {
		return result, nil
	}
	now := cleanupTime(input.Now)
	if !planned[path] {
		if err := appendTombstone(runDir, tombstone{RunID: state.RunID, TaskID: state.TaskID, Kind: target.Kind, Path: path, Phase: "planned", CreatedAt: now}); err != nil {
			return Result{}, err
		}
	}
	if err := worktree.RemoveClean(); err != nil {
		if errors.Is(err, gitworktree.ErrDirtyWorktree) {
			return Result{}, ErrDirtyWorktree
		}
		return Result{}, err
	}
	if err := appendTombstone(runDir, tombstone{RunID: state.RunID, TaskID: state.TaskID, Kind: target.Kind, Path: path, Phase: "completed", CreatedAt: now}); err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateOutcome(runDir string, state domain.RunState, validator *contract.Validator) error {
	path := filepath.Join(runDir, "outcome.json")
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrOutcomeMissing
	}
	data, err := os.ReadFile(path)
	if err != nil || validator.Validate(domain.KindOutcome, data) != nil {
		return ErrOutcomeMissing
	}
	var outcome domain.OutcomeBundle
	if json.Unmarshal(data, &outcome) != nil || outcome.RunID != state.RunID || outcome.TaskID != state.TaskID || outcome.TerminalState != state.State {
		return ErrOutcomeMissing
	}
	if info, err := os.Lstat(filepath.Join(runDir, "result.md")); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrOutcomeMissing
	}
	return nil
}

func rejectActiveSessions(runDir string) error {
	attempts, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if !attempt.IsDir() || attempt.Type()&os.ModeSymlink != 0 {
			return ErrTargetIdentity
		}
		path := filepath.Join(runDir, "attempts", attempt.Name(), "terminal-session.json")
		if info, statErr := os.Lstat(path); statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return ErrTargetIdentity
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		data, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		var record struct {
			State string `json:"state"`
		}
		if json.Unmarshal(data, &record) != nil || (record.State != "running" && record.State != "paused" && record.State != "terminated") {
			return ErrTargetIdentity
		}
		if record.State != "terminated" {
			return ErrActiveSession
		}
	}
	return nil
}

func validTargetPath(stateRoot, target string) bool {
	if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(target) || target != filepath.Clean(target) {
		return false
	}
	expectedParent, err := filepath.EvalSymlinks(filepath.Join(stateRoot, "worktrees"))
	if err != nil {
		return false
	}
	actualParent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil || actualParent != expectedParent || filepath.Base(target) == "." || filepath.Base(target) == string(filepath.Separator) {
		return false
	}
	info, err := os.Lstat(target)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 || errors.Is(err, os.ErrNotExist)
}

func cleanupTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func appendTombstone(runDir string, record tombstone) error {
	directory := filepath.Join(runDir, "cleanup")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrTargetIdentity
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, "tombstones.jsonl")
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size()+int64(len(data)+1) > 1<<20) {
		return ErrTargetIdentity
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func readTombstones(runDir string, state domain.RunState) (map[string]bool, map[string]bool, error) {
	completed, planned := map[string]bool{}, map[string]bool{}
	directory := filepath.Join(runDir, "cleanup")
	if info, err := os.Lstat(directory); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, nil, ErrTargetIdentity
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	path := filepath.Join(directory, "tombstones.jsonl")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return completed, planned, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return nil, nil, ErrTargetIdentity
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, nil, fmt.Errorf("%w: truncated cleanup tombstone journal", ErrTargetIdentity)
	}
	for _, line := range strings.Split(string(data[:len(data)-1]), "\n") {
		var record tombstone
		if json.Unmarshal([]byte(line), &record) != nil || record.RunID != state.RunID || record.TaskID != state.TaskID || record.Kind != "managed-worktree" ||
			(record.Phase != "planned" && record.Phase != "completed") || strings.TrimSpace(record.Path) == "" || record.CreatedAt.IsZero() {
			return nil, nil, fmt.Errorf("%w: invalid cleanup tombstone", ErrTargetIdentity)
		}
		if record.Phase == "planned" {
			if planned[record.Path] || completed[record.Path] {
				return nil, nil, fmt.Errorf("%w: duplicate cleanup plan", ErrTargetIdentity)
			}
			planned[record.Path] = true
		} else {
			if !planned[record.Path] || completed[record.Path] {
				return nil, nil, fmt.Errorf("%w: cleanup completion without unique plan", ErrTargetIdentity)
			}
			completed[record.Path] = true
		}
	}
	return completed, planned, nil
}
