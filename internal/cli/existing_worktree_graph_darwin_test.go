//go:build darwin && arm64

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func existingWorktreeGraphTestGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func existingWorktreeGraphTestRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	existingWorktreeGraphTestGit(t, repository, "init", "-q")
	existingWorktreeGraphTestGit(t, repository, "config", "user.email", "marshal@example.invalid")
	existingWorktreeGraphTestGit(t, repository, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	existingWorktreeGraphTestGit(t, repository, "add", "tracked.txt")
	existingWorktreeGraphTestGit(t, repository, "commit", "-q", "-m", "base")
	linkedRepository := filepath.Join(root, "linked-repository")
	target := filepath.Join(root, "target")
	existingWorktreeGraphTestGit(t, repository, "worktree", "add", "-q", "--detach", linkedRepository, "HEAD")
	existingWorktreeGraphTestGit(t, repository, "worktree", "add", "-q", "--detach", target, "HEAD")
	return repository, linkedRepository, target
}

func TestOpenExistingWorktreeCompositionSupportsLinkedRepository(t *testing.T) {
	_, linkedRepository, target := existingWorktreeGraphTestRepository(t)
	handles, err := openExistingWorktreeComposition(linkedRepository, target)
	if err != nil {
		t.Fatalf("open linked composition: %v", err)
	}
	defer func() { _ = handles.Close() }()
	if handles.repositoryDotGit == nil || handles.commonGitParent == nil || handles.repositoryCommon == nil {
		t.Fatal("linked repository graph did not retain .git file and common-directory descriptors")
	}
	if handles.graph.RepositoryDotGitFile == nil || handles.graph.RepositoryCommonGitParent == nil {
		t.Fatal("linked repository graph selected the main-worktree shape")
	}
}

func TestOpenExistingWorktreeCompositionRejectsSymlinkedTargetPath(t *testing.T) {
	repository, _, target := existingWorktreeGraphTestRepository(t)
	alias := filepath.Join(filepath.Dir(target), "target-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if handles, err := openExistingWorktreeComposition(repository, alias); err == nil {
		_ = handles.Close()
		t.Fatal("symlinked target path was accepted")
	}
}
