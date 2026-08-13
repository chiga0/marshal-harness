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

func TestSameTaskDifferentRunsGetIndependentWorktrees(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, _ := Open(repository)
	first, err := manager.CreateForRun(stateRoot, "task:per-run", "run-one", base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.CreateForRun(stateRoot, "task:per-run", "run-two", base)
	if err != nil {
		t.Fatalf("second run of the same task could not create its own worktree: %v", err)
	}
	for _, worktree := range []*Worktree{first, second} {
		worktree := worktree
		t.Cleanup(func() {
			_ = worktree.Release()
			_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
			_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
		})
	}
	if first.Path == second.Path || first.Branch == second.Branch {
		t.Fatalf("runs share identity: %s/%s vs %s/%s", first.Path, first.Branch, second.Path, second.Branch)
	}
	if want := filepath.Join(stateRoot, "worktrees", "task-per-run-run-one"); first.Path != want {
		t.Fatalf("first worktree path = %q, want %q", first.Path, want)
	}
	if want := "marshal/task-per-run-run-two"; second.Branch != want {
		t.Fatalf("second branch = %q, want %q", second.Branch, want)
	}
	if _, err := os.Stat(filepath.Join(second.Path, "README.md")); err != nil {
		t.Fatalf("second worktree is not a checkout: %v", err)
	}
}

func TestRunScopedWorktreeAdmitsOneWriterPerRun(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, _ := Open(repository)
	first, err := manager.CreateForRun(stateRoot, "task:locked", "run-one", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = first.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", first.Path)
		_ = gitCommand(t, repository, "branch", "-D", first.Branch)
	}()
	if _, err := manager.CreateForRun(stateRoot, "task:locked", "run-one", base); err == nil {
		t.Fatal("second writer for the same (task, run) acquired a worktree")
	}
	if _, err := manager.Acquire(stateRoot, "task:locked", first.Path, base); err == nil {
		t.Fatal("acquire admitted a second writer for the same run-scoped worktree")
	}
	second, err := manager.CreateForRun(stateRoot, "task:locked", "run-two", base)
	if err != nil {
		t.Fatalf("run-scoped lock leaked across runs of the same task: %v", err)
	}
	_ = second.Release()
	_ = gitCommand(t, repository, "worktree", "remove", "--force", second.Path)
	_ = gitCommand(t, repository, "branch", "-D", second.Branch)
}

func TestAcquireResolvesLegacyAndRunScopedNames(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, _ := Open(repository)
	legacy, err := manager.Create(stateRoot, "task:acquire", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = legacy.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", legacy.Path)
		_ = gitCommand(t, repository, "branch", "-D", legacy.Branch)
	})
	reacquired, err := manager.Acquire(stateRoot, "task:acquire", legacy.Path, base)
	if err != nil {
		t.Fatalf("legacy task-keyed worktree did not resolve: %v", err)
	}
	if reacquired.Branch != "marshal/task-acquire" {
		t.Fatalf("legacy branch = %q, want %q", reacquired.Branch, "marshal/task-acquire")
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
	runScoped, err := manager.CreateForRun(stateRoot, "task:acquire", "run-x", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := runScoped.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runScoped.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", runScoped.Path)
		_ = gitCommand(t, repository, "branch", "-D", runScoped.Branch)
	})
	reacquired, err = manager.Acquire(stateRoot, "task:acquire", runScoped.Path, base)
	if err != nil {
		t.Fatalf("run-scoped worktree did not resolve for its task: %v", err)
	}
	if reacquired.Branch != "marshal/task-acquire-run-x" {
		t.Fatalf("run-scoped branch = %q, want %q", reacquired.Branch, "marshal/task-acquire-run-x")
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(stateRoot, "task:stranger", runScoped.Path, base); err == nil {
		t.Fatal("acquire resolved a run-scoped worktree for the wrong task")
	}
	if _, err := manager.Acquire(stateRoot, "task:acquire", legacy.Path+"-missing", base); err == nil {
		t.Fatal("acquire resolved a worktree path that does not exist")
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

func TestAcquireRetriesShortLivedRepositoryLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	shortenRepositoryLockBackoff(t)
	worktree, err := manager.CreateForRun(stateRoot, "task:acquire-retry", "run-one", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
		_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
	})
	holder, release := holdRepositoryLock(t, stateRoot)
	go func() {
		time.Sleep(3 * repositoryLockBackoff)
		_ = holder.Unlock()
	}()
	started := time.Now()
	reacquired, err := manager.Acquire(stateRoot, "task:acquire-retry", worktree.Path, base)
	if err != nil {
		t.Fatalf("Acquire gave up on short-lived repository lock contention: %v", err)
	}
	if elapsed := time.Since(started); elapsed < repositoryLockBackoff {
		t.Fatalf("Acquire returned in %s without backing off for the repository lock", elapsed)
	}
	release()
	_ = reacquired.Release()
}

func TestAcquireOutlivedRepositoryLockDoesNotLeakTaskLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	shortenRepositoryLockBackoff(t)
	worktree, err := manager.CreateForRun(stateRoot, "task:window", "run-one", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
		_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
	})
	_, release := holdRepositoryLock(t, stateRoot)
	started := time.Now()
	_, err = manager.Acquire(stateRoot, "task:window", worktree.Path, base)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Acquire succeeded while the repository lock stayed held beyond the retry window")
	}
	if !strings.Contains(err.Error(), "repository lock") {
		t.Fatalf("error = %v, want repository lock failure", err)
	}
	if elapsed < repositoryLockRetryWindow()-100*time.Millisecond || elapsed > repositoryLockRetryWindow()+2*time.Second {
		t.Fatalf("Acquire backed off for %s, want roughly the %s retry window", elapsed, repositoryLockRetryWindow())
	}
	release()
	reacquired, err := manager.Acquire(stateRoot, "task:window", worktree.Path, base)
	if err != nil {
		t.Fatalf("Acquire after the repository lock was released failed, task lock leaked: %v", err)
	}
	_ = reacquired.Release()
}

func TestAcquireDoesNotRetryTaskLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	shortenRepositoryLockBackoff(t)
	first, err := manager.CreateForRun(stateRoot, "task:single-writer", "run-one", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = first.Release()
		_ = gitCommand(t, repository, "worktree", "remove", "--force", first.Path)
		_ = gitCommand(t, repository, "branch", "-D", first.Branch)
	}()
	started := time.Now()
	second, err := manager.Acquire(stateRoot, "task:single-writer", first.Path, base)
	elapsed := time.Since(started)
	if err == nil {
		_ = second.Release()
		t.Fatal("second writer for the same worktree acquired a lease")
	}
	if !strings.Contains(err.Error(), "task lock") {
		t.Fatalf("error = %v, want task lock failure", err)
	}
	if elapsed >= repositoryLockBackoff {
		t.Fatalf("task lock contention backed off for %s, want immediate failure", elapsed)
	}
}

func TestConcurrentAcquireOfDistinctRunWorktrees(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	shortenRepositoryLockBackoff(t)
	var worktrees []*Worktree
	for _, runID := range []string{"run-one", "run-two"} {
		worktree, err := manager.CreateForRun(stateRoot, "task:concurrent", runID, base)
		if err != nil {
			t.Fatal(err)
		}
		if err := worktree.Release(); err != nil {
			t.Fatal(err)
		}
		worktrees = append(worktrees, worktree)
		t.Cleanup(func() {
			_ = worktree.Release()
			_ = gitCommand(t, repository, "worktree", "remove", "--force", worktree.Path)
			_ = gitCommand(t, repository, "branch", "-D", worktree.Branch)
		})
	}
	type acquisition struct {
		worktree *Worktree
		err      error
	}
	results := make(chan acquisition, len(worktrees))
	for _, worktree := range worktrees {
		worktree := worktree
		go func() {
			acquired, err := manager.Acquire(stateRoot, "task:concurrent", worktree.Path, base)
			results <- acquisition{worktree: acquired, err: err}
		}()
	}
	for range worktrees {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Acquire of a distinct run worktree failed: %v", result.err)
		}
		acquired := result.worktree
		t.Cleanup(func() { _ = acquired.Release() })
	}
}

func TestRemovalRetriesShortLivedRepositoryLock(t *testing.T) {
	repository, base := fixtureRepository(t)
	initializeMarshalState(t, repository)
	stateRoot := filepath.Join(repository, ".marshal")
	manager, err := Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	shortenRepositoryLockBackoff(t)

	cleanWorktree, err := manager.Create(stateRoot, "task:remove-clean", base)
	if err != nil {
		t.Fatal(err)
	}
	holder, release := holdRepositoryLock(t, stateRoot)
	go func() {
		time.Sleep(3 * repositoryLockBackoff)
		_ = holder.Unlock()
	}()
	started := time.Now()
	if err := cleanWorktree.RemoveClean(); err != nil {
		t.Fatalf("RemoveClean gave up on short-lived repository lock contention: %v", err)
	}
	if elapsed := time.Since(started); elapsed < repositoryLockBackoff {
		t.Fatalf("RemoveClean returned in %s without backing off for the repository lock", elapsed)
	}
	release()

	archivedWorktree, err := manager.Create(stateRoot, "task:remove-archived", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archivedWorktree.Path, "unarchived.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, release = holdRepositoryLock(t, stateRoot)
	go func() {
		time.Sleep(3 * repositoryLockBackoff)
		_ = holder.Unlock()
	}()
	started = time.Now()
	if err := archivedWorktree.RemoveArchived(); err != nil {
		t.Fatalf("RemoveArchived gave up on short-lived repository lock contention: %v", err)
	}
	if elapsed := time.Since(started); elapsed < repositoryLockBackoff {
		t.Fatalf("RemoveArchived returned in %s without backing off for the repository lock", elapsed)
	}
	release()
}

func shortenRepositoryLockBackoff(t *testing.T) {
	t.Helper()
	previous := repositoryLockBackoff
	repositoryLockBackoff = 50 * time.Millisecond
	t.Cleanup(func() { repositoryLockBackoff = previous })
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
