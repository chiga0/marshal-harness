package gitworktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
)

func TestNestedLinkedWorktreeDoesNotModifyMainCheckout(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(stateRoot, "task:fixture", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
		_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
	})
	if err := os.WriteFile(filepath.Join(worktree.Path, "change.txt"), []byte("task change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository, "change.txt")); !os.IsNotExist(err) {
		t.Fatalf("task change leaked into main checkout: %v", err)
	}
	status := gitCommand(t, repository, "status", "--short", "--untracked-files=all")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("main checkout became dirty: %q", status)
	}
	opened, err := Open(worktree.Path)
	if err != nil || opened.CommonDir != manager.CommonDir {
		t.Fatalf("worktree identity = %+v, %v", opened, err)
	}
	if runtime.GOOS == "darwin" && strings.HasPrefix(worktree.Path, "/private/var/") {
		alias := strings.TrimPrefix(worktree.Path, "/private")
		aliasOpened, err := Open(alias)
		if err != nil || aliasOpened.Root != opened.Root || aliasOpened.CommonDir != opened.CommonDir {
			t.Fatalf("macOS path alias identity = %+v, %v", aliasOpened, err)
		}
	}
}

func TestTaskLockIsExclusive(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	manager, _ := Open(repository)
	first, err := manager.Create(filepath.Join(repository, ".marshal"), "task:one", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = first.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", first.Path)
		_ = gitCommand(t, repository, "branch", "-D", first.Branch)
	}()
	if _, err := manager.Acquire(filepath.Join(repository, ".marshal"), "task:one", first.Path, base); err == nil {
		t.Fatal("second task writer lock was acquired")
	}
}

func TestDifferentTasksCanHaveIndependentWorktrees(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	manager, _ := Open(repository)
	first, err := manager.Create(filepath.Join(repository, ".marshal"), "task:one", base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(filepath.Join(repository, ".marshal"), "task:two", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, worktree := range []*Worktree{first, second} {
		worktree := worktree
		t.Cleanup(func() {
			_ = worktree.Release()
			_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
			_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
		})
	}
}

func TestRemoveCleanRejectsDirtyAndRetainsBranch(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	manager, _ := Open(repository)
	stateRoot := filepath.Join(repository, ".marshal")

	dirty, err := manager.Create(stateRoot, "task:dirty", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Path, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dirty.RemoveClean(); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("RemoveClean(dirty) = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirty.Path, "unarchived.txt")); err != nil {
		t.Fatalf("dirty file was removed: %v", err)
	}
	_ = dirty.Release()
	_ = gitCommand(t, repository, "worktree", "remove", "--force", dirty.Path)
	_ = gitCommand(t, repository, "branch", "-D", dirty.Branch)

	clean, err := manager.Create(stateRoot, "task:clean", base)
	if err != nil {
		t.Fatal(err)
	}
	path, branch := clean.Path, clean.Branch
	if err := clean.RemoveClean(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", err)
	}
	if output := gitCommand(t, repository, "show-ref", "--verify", "refs/heads/"+branch); strings.TrimSpace(output) == "" {
		t.Fatal("cleanup unexpectedly removed the local branch")
	}
	_ = gitCommand(t, repository, "branch", "-D", branch)
}

func initializeMarshalState(t *testing.T, root string) {
	t.Helper()
	state, err := marshalRepository.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
}

func fixtureRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	gitCommand(t, repository, "init", "-q")
	gitCommand(t, repository, "config", "user.name", "Marshal Test")
	gitCommand(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "README.md")
	gitCommand(t, repository, "commit", "-q", "-m", "base")
	return repository, strings.TrimSpace(gitCommand(t, repository, "rev-parse", "HEAD"))
}

func gitCommand(t testing.TB, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
