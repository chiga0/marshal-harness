//go:build darwin && arm64

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
)

// existingWorktreeCompositionHandles owns every descriptor the fixed CLI
// opens to build the path B descriptor graph plus the held target worktree.
// All handles cover ComposeRuntime/Prepare/Start and are closed in reverse
// order on exit. No pathname is reopened after construction; the graph and
// target descriptor are the only authority for the bind.
type existingWorktreeCompositionHandles struct {
	filesystemRoot   *os.File
	repositoryParent *os.File
	repositoryRoot   *os.File
	repositoryCommon *os.File
	repositoryDotGit *os.File
	commonGitParent  *os.File
	target           *os.File
	graph            allocationcontrol.ExistingWorktreeDescriptorGraphV1
}

func (handles *existingWorktreeCompositionHandles) Close() error {
	if handles == nil {
		return nil
	}
	var errs []error
	// Reverse order: target first, then graph descriptors.
	if handles.target != nil {
		errs = append(errs, handles.target.Close())
		handles.target = nil
	}
	if handles.commonGitParent != nil {
		errs = append(errs, handles.commonGitParent.Close())
		handles.commonGitParent = nil
	}
	if handles.repositoryDotGit != nil {
		errs = append(errs, handles.repositoryDotGit.Close())
		handles.repositoryDotGit = nil
	}
	if handles.repositoryCommon != nil {
		errs = append(errs, handles.repositoryCommon.Close())
		handles.repositoryCommon = nil
	}
	if handles.repositoryRoot != nil {
		errs = append(errs, handles.repositoryRoot.Close())
		handles.repositoryRoot = nil
	}
	if handles.repositoryParent != nil {
		errs = append(errs, handles.repositoryParent.Close())
		handles.repositoryParent = nil
	}
	if handles.filesystemRoot != nil {
		errs = append(errs, handles.filesystemRoot.Close())
		handles.filesystemRoot = nil
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// gitCommonDirLocator uses the fixed /usr/bin/git solely as a locator for the
// repository's common Git directory. The returned path is never trusted as
// authority: the held descriptors and the descriptor-graph constructors
// perform the final validation.
func gitCommonDirLocator(repositoryRoot string) (string, error) {
	command := exec.Command("/usr/bin/git", "-C", repositoryRoot, "rev-parse", "--git-common-dir")
	command.Env = []string{"HOME=/dev/null", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git common-dir locator: %w", err)
	}
	commonDir := strings.TrimSpace(string(output))
	if commonDir == "" {
		return "", fmt.Errorf("git common-dir locator: empty output")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repositoryRoot, commonDir)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(commonDir))
	if err != nil {
		return "", fmt.Errorf("git common-dir locator: resolve: %w", err)
	}
	return resolved, nil
}

// openExistingWorktreeComposition builds and holds the path B descriptor graph
// plus the target worktree descriptor. It supports a repository whose `.git`
// is a directory (main worktree) or a linked-worktree `.git` file. The fixed
// /usr/bin/git is used only as a locator for the common Git directory; the
// final authority is NewExistingWorktreeDescriptorGraph /
// NewLinkedExistingWorktreeDescriptorGraph over the held descriptors.
func openExistingWorktreeComposition(repositoryRoot, worktreePath string) (*existingWorktreeCompositionHandles, error) {
	canonicalRepo, err := filepath.EvalSymlinks(filepath.Clean(repositoryRoot))
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: repository root: %w", err)
	}
	// The Git admin gitdir (a linked worktree's .git file) is byte-bound to
	// the exact WorktreePath string. Silently EvalSymlinks-ing it to a
	// different string (e.g. /tmp -> /private/tmp on Darwin) would break that
	// binding, so fail closed unless the clean absolute path is already
	// canonical. The real request keeps using the clean canonical path.
	cleanWorktree := filepath.Clean(worktreePath)
	if !filepath.IsAbs(cleanWorktree) {
		return nil, fmt.Errorf("existing-worktree composition: worktree path not absolute: %q", worktreePath)
	}
	resolvedWorktree, err := filepath.EvalSymlinks(cleanWorktree)
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: worktree path not resolvable: %w", err)
	}
	if resolvedWorktree != cleanWorktree {
		return nil, fmt.Errorf("existing-worktree composition: worktree path %q resolves to %q; a symlinked path breaks the byte-bound Git admin gitdir binding", cleanWorktree, resolvedWorktree)
	}
	canonicalWorktree := cleanWorktree
	handles := &existingWorktreeCompositionHandles{}
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			_ = handles.Close()
		}
	}()
	filesystemRoot, err := os.Open("/")
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: filesystem root: %w", err)
	}
	handles.filesystemRoot = filesystemRoot
	repositoryParent, err := os.Open(filepath.Dir(canonicalRepo))
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: repository parent: %w", err)
	}
	handles.repositoryParent = repositoryParent
	repositoryRootFile, err := os.Open(canonicalRepo)
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: repository root: %w", err)
	}
	handles.repositoryRoot = repositoryRootFile
	repositoryName := filepath.Base(canonicalRepo)
	dotGitPath := filepath.Join(canonicalRepo, ".git")
	dotGitInfo, err := os.Lstat(dotGitPath)
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: stat .git: %w", err)
	}
	if dotGitInfo.IsDir() {
		commonDir, err := os.Open(dotGitPath)
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: open common dir: %w", err)
		}
		handles.repositoryCommon = commonDir
		graph, err := allocationcontrol.NewExistingWorktreeDescriptorGraph(handles.filesystemRoot, handles.repositoryParent, handles.repositoryRoot, handles.repositoryCommon, repositoryName)
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: descriptor graph: %w", err)
		}
		handles.graph = graph
	} else {
		dotGitFile, err := os.Open(dotGitPath)
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: open .git file: %w", err)
		}
		handles.repositoryDotGit = dotGitFile
		commonDirPath, err := gitCommonDirLocator(canonicalRepo)
		if err != nil {
			return nil, err
		}
		commonDir, err := os.Open(commonDirPath)
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: open common dir: %w", err)
		}
		handles.repositoryCommon = commonDir
		commonParent, err := os.Open(filepath.Dir(commonDirPath))
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: open common parent: %w", err)
		}
		handles.commonGitParent = commonParent
		graph, err := allocationcontrol.NewLinkedExistingWorktreeDescriptorGraph(handles.filesystemRoot, handles.repositoryParent, handles.repositoryRoot, handles.repositoryDotGit, handles.commonGitParent, handles.repositoryCommon, repositoryName, filepath.Base(commonDirPath))
		if err != nil {
			return nil, fmt.Errorf("existing-worktree composition: linked descriptor graph: %w", err)
		}
		handles.graph = graph
	}
	target, err := os.Open(canonicalWorktree)
	if err != nil {
		return nil, fmt.Errorf("existing-worktree composition: open target: %w", err)
	}
	handles.target = target
	closeOnFailure = false
	return handles, nil
}
