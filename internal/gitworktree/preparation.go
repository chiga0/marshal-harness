package gitworktree

import (
	"errors"
	"strings"
)

// recoverPreparationLock runs with both repository metadata and task flock
// held. A Git administrative lock is not a process lock: an exact surviving
// Marshal lock may be adopted only after the caller proved an unstarted Run.
func (w *Worktree) recoverPreparationLock() error {
	if err := w.Validate(); err != nil {
		return err
	}
	head, err := gitOutput(w.Path, "rev-parse", "--verify", "HEAD")
	if err != nil || head != w.BaseSHA {
		return errors.New("preparation worktree base changed")
	}
	branch, err := gitOutput(w.Path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || branch != "refs/heads/"+w.Branch {
		return errors.New("preparation worktree branch changed")
	}
	clean, err := w.Clean()
	if err != nil {
		return err
	}
	if !clean {
		return ErrDirtyWorktree
	}
	listing, err := gitOutput(w.repo.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	actual, err := canonical(w.Path)
	if err != nil {
		return err
	}
	found, active, locked := false, false, false
	for _, field := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			path, pathErr := canonical(strings.TrimPrefix(field, "worktree "))
			active = pathErr == nil && path == actual
			if active {
				if found {
					return errors.New("duplicate preparation worktree registration")
				}
				found = true
			}
		} else if active && (field == "locked" || strings.HasPrefix(field, "locked ")) {
			if field != "locked managed by Marshal" {
				return errors.New("preparation worktree has a foreign lock")
			}
			locked = true
		} else if field == "" {
			active = false
		}
	}
	if !found {
		return errors.New("preparation worktree registration is missing")
	}
	// A crash after git worktree add may precede the original chmod step.
	if err := setPrivateModeOnWorktreeTargets(w.Path); err != nil {
		return err
	}
	if !locked {
		return gitRun(w.repo.Root, "worktree", "lock", "--reason", "managed by Marshal", w.Path)
	}
	return nil
}
