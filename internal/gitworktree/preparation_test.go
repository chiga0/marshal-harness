package gitworktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverPreparationReusesCleanWorktreeAndOrphanedManagedLock(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "orphaned-lock"}[locked], func(t *testing.T) {
			root, base := fixtureRepository(t)
			initializeMarshalState(t, root)
			manager, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			stateRoot := filepath.Join(root, ".marshal")
			first, err := manager.CreateForRun(stateRoot, "task-recover", "run-recover", base)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.RecoverPreparationForRun(stateRoot, "task-recover", "run-recover", base); err == nil {
				t.Fatal("adopted a worktree with an active writer")
			}
			before, err := os.Stat(first.Path)
			if err != nil {
				t.Fatal(err)
			}
			if locked {
				// Explicit crash fixture: drop the process flock, leave Git's
				// administrative lock. No real worker process is killed.
				if err := first.taskLock.Unlock(); err != nil {
					t.Fatal(err)
				}
				first.taskLock = nil
			} else if err := first.Release(); err != nil {
				t.Fatal(err)
			}
			recovered, err := manager.RecoverPreparationForRun(stateRoot, "task-recover", "run-recover", base)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Release()
			after, err := os.Stat(recovered.Path)
			if err != nil || !os.SameFile(before, after) || recovered.Path != first.Path || recovered.Branch != first.Branch {
				t.Fatal("recovery replaced the worktree")
			}
		})
	}
}

func TestRecoverPreparationContinuesBranchOnlyCrash(t *testing.T) {
	root, base := fixtureRepository(t)
	initializeMarshalState(t, root)
	manager, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	gitCommand(t, root, "branch", "marshal/task-recover-run-recover", base)
	worktree, err := manager.RecoverPreparationForRun(filepath.Join(root, ".marshal"), "task-recover", "run-recover", base)
	if err != nil {
		t.Fatal(err)
	}
	defer worktree.Release()
	if got := gitCommand(t, worktree.Path, "rev-parse", "HEAD"); got != base {
		t.Fatal("base changed")
	}
}

func TestRecoverPreparationPreservesConflictingWorktree(t *testing.T) {
	for _, mode := range []string{"dirty", "head", "branch", "foreign-lock", "symlink", "branch-only-drift"} {
		t.Run(mode, func(t *testing.T) {
			root, base := fixtureRepository(t)
			initializeMarshalState(t, root)
			manager, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			stateRoot := filepath.Join(root, ".marshal")
			if mode == "branch-only-drift" {
				gitCommand(t, root, "commit", "--allow-empty", "-m", "advance")
				gitCommand(t, root, "branch", "marshal/task-recover-run-recover", "HEAD")
			} else if mode == "symlink" {
				parent := filepath.Join(stateRoot, "worktrees")
				if err := os.MkdirAll(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, filepath.Join(parent, "task-recover-run-recover")); err != nil {
					t.Fatal(err)
				}
			} else {
				first, err := manager.CreateForRun(stateRoot, "task-recover", "run-recover", base)
				if err != nil {
					t.Fatal(err)
				}
				if err := first.Release(); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "dirty":
					if err := os.WriteFile(filepath.Join(first.Path, "user-work.txt"), []byte("preserve"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "head":
					gitCommand(t, first.Path, "commit", "--allow-empty", "-m", "different")
				case "branch":
					gitCommand(t, first.Path, "checkout", "--detach", base)
				case "foreign-lock":
					gitCommand(t, root, "worktree", "lock", "--reason", "user operation", first.Path)
				}
			}
			before := gitCommand(t, root, "worktree", "list", "--porcelain")
			if _, err := manager.RecoverPreparationForRun(stateRoot, "task-recover", "run-recover", base); err == nil {
				t.Fatal("conflicting recovery accepted")
			}
			if after := gitCommand(t, root, "worktree", "list", "--porcelain"); before != after {
				t.Fatal("recovery changed conflict evidence")
			}
			if mode == "dirty" {
				data, err := os.ReadFile(filepath.Join(stateRoot, "worktrees", "task-recover-run-recover", "user-work.txt"))
				if err != nil || string(data) != "preserve" {
					t.Fatal("user content was modified")
				}
			}
		})
	}
}

func TestRecoverPreparationRejectsRedirectedContainers(t *testing.T) {
	for _, name := range []string{"locks", "worktrees"} {
		t.Run(name, func(t *testing.T) {
			root, base := fixtureRepository(t)
			manager, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			stateRoot := filepath.Join(root, ".marshal")
			if err := os.Mkdir(stateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(stateRoot, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.RecoverPreparationForRun(stateRoot, "task-recover", "run-recover", base); err == nil {
				t.Fatal("redirected container accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("recovery wrote through a container symlink")
			}
		})
	}
}
