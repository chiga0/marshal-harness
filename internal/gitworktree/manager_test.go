package gitworktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

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

func TestRemoveArchivedForcesDirtyWorktreeAndRetainsBranch(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	manager, _ := Open(repository)
	stateRoot := filepath.Join(repository, ".marshal")
	worktree, err := manager.Create(stateRoot, "task:archived", base)
	if err != nil {
		t.Fatal(err)
	}
	path, branch := worktree.Path, worktree.Branch
	if err := os.WriteFile(filepath.Join(path, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := worktree.RemoveClean(); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("RemoveClean(dirty) = %v", err)
	}
	if err := worktree.RemoveArchived(); err != nil {
		t.Fatalf("RemoveArchived = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived worktree still exists: %v", err)
	}
	if output := gitCommand(t, repository, "show-ref", "--verify", "refs/heads/"+branch); strings.TrimSpace(output) == "" {
		t.Fatal("archived removal unexpectedly removed the local branch")
	}
	_ = gitCommand(t, repository, "branch", "-D", branch)
}

func TestCleanSnapshotIsAdvisoryAndReadOnly(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	manager, _ := Open(repository)
	stateRoot := filepath.Join(repository, ".marshal")
	worktree, err := manager.Create(stateRoot, "task:snapshot", base)
	if err != nil {
		t.Fatal(err)
	}
	path := worktree.Path
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", path)
		_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
	})
	clean, err := manager.CleanSnapshot(path)
	if err != nil || !clean {
		t.Fatalf("CleanSnapshot(clean) = %v, %v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(path, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean, err = manager.CleanSnapshot(path)
	if err != nil || clean {
		t.Fatalf("CleanSnapshot(dirty) = %v, %v", clean, err)
	}
	if _, err := manager.CleanSnapshot(repository); err == nil {
		t.Fatal("CleanSnapshot accepted the main checkout")
	}
	if _, err := manager.CleanSnapshot(filepath.Join(stateRoot, "worktrees", "task:missing")); err == nil {
		t.Fatal("CleanSnapshot accepted an unregistered path")
	}
}

func TestCreateRetriesShortLivedRepositoryLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	holder, release := holdRepositoryLock(t, stateRoot)
	go func() {
		time.Sleep(900 * time.Millisecond)
		_ = holder.Unlock()
	}()
	started := time.Now()
	worktree, err := manager.Create(stateRoot, "task:retry", base)
	if err != nil {
		t.Fatalf("Create gave up on short-lived repository lock contention: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
		t.Fatalf("Create returned in %s without backing off for the repository lock", elapsed)
	}
	release()
	_ = worktree.Release()
	_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
	_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
}

func TestCreateFailsWhenRepositoryLockOutlivesRetryWindow(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	hold := repositoryLockRetryWindow() + 500*time.Millisecond
	holder, release := holdRepositoryLock(t, stateRoot)
	stop := make(chan struct{})
	go func() {
		select {
		case <-time.After(hold):
		case <-stop:
		}
		_ = holder.Unlock()
	}()
	started := time.Now()
	_, err = manager.Create(stateRoot, "task:window", base)
	elapsed := time.Since(started)
	close(stop)
	if err == nil {
		t.Fatal("Create succeeded while the repository lock stayed held beyond the retry window")
	}
	if !strings.Contains(err.Error(), "repository lock") {
		t.Fatalf("error = %v, want repository lock failure", err)
	}
	if elapsed < repositoryLockRetryWindow()-200*time.Millisecond || elapsed > hold+time.Second {
		t.Fatalf("Create backed off for %s, want roughly the %s retry window", elapsed, repositoryLockRetryWindow())
	}
	if _, statErr := os.Lstat(filepath.Join(stateRoot, "worktrees", "task-window")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed Create left a worktree behind: %v", statErr)
	}
	release()
}

func TestCreateDoesNotRetryTaskLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Create(stateRoot, "task:one", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = first.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", first.Path)
		_ = gitCommand(t, repository, "branch", "-D", first.Branch)
	}()
	started := time.Now()
	second, err := manager.Create(stateRoot, "task:one", base)
	elapsed := time.Since(started)
	if err == nil {
		_ = second.Release()
		t.Fatal("second writer for the same task acquired a worktree")
	}
	if !strings.Contains(err.Error(), "task lock") {
		t.Fatalf("error = %v, want task lock failure", err)
	}
	if elapsed >= repositoryLockBackoff {
		t.Fatalf("task lock contention backed off for %s, want immediate failure", elapsed)
	}
}

func repositoryLockRetryWindow() time.Duration {
	return time.Duration(repositoryLockRetries) * repositoryLockBackoff
}

func holdRepositoryLock(t *testing.T, stateRoot string) (*flock.Flock, func()) {
	t.Helper()
	locks := filepath.Join(stateRoot, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatal(err)
	}
	holder := flock.New(filepath.Join(locks, "repository.lock"))
	locked, err := holder.TryLock()
	if err != nil || !locked {
		t.Fatalf("fixture could not take repository lock: %v", err)
	}
	release := func() { _ = holder.Unlock() }
	t.Cleanup(release)
	return holder, release
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
