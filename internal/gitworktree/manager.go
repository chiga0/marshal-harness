package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/gofrs/flock"
)

type Repository struct {
	Root      string
	CommonDir string
}

type Worktree struct {
	Path     string
	Branch   string
	BaseSHA  string
	repo     Repository
	repoLock *flock.Flock
	taskLock *flock.Flock
}

type Detached struct {
	Path      string
	repo      Repository
	stateRoot string
}

var branchUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func Open(root string) (Repository, error) {
	return OpenContext(context.Background(), root)
}

func OpenContext(ctx context.Context, root string) (Repository, error) {
	canonicalRoot, err := canonical(root)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize repository: %w", err)
	}
	topLevel, err := gitOutputContext(ctx, canonicalRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, err
	}
	canonicalRoot, err = canonical(topLevel)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize repository top level: %w", err)
	}
	common, err := gitOutputContext(ctx, canonicalRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(canonicalRoot, common)
	}
	canonicalCommon, err := canonical(common)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	return Repository{Root: canonicalRoot, CommonDir: canonicalCommon}, nil
}

func (r Repository) ResolveBase(ref string) (string, error) {
	return r.ResolveBaseContext(context.Background(), ref)
}

func (r Repository) ResolveBaseContext(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" || strings.HasPrefix(ref, "-") {
		return "", errors.New("invalid base ref")
	}
	sha, err := gitOutputContext(ctx, r.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve base ref %q: %w", ref, err)
	}
	return sha, nil
}

func (r Repository) Create(stateRoot, taskID, baseSHA string) (*Worktree, error) {
	if err := domain.ValidateID(taskID); err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(baseSHA) {
		return nil, errors.New("base SHA must be a full hexadecimal object ID")
	}
	if _, err := r.ResolveBase(baseSHA); err != nil {
		return nil, err
	}
	locks := filepath.Join(stateRoot, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, err
	}
	repositoryLock := flock.New(filepath.Join(locks, "repository.lock"))
	taskLock := flock.New(filepath.Join(locks, "task-"+safeName(taskID)+".lock"))
	if locked, err := repositoryLock.TryLock(); err != nil || !locked {
		return nil, fmt.Errorf("acquire repository lock: %w", lockError(err))
	}
	if locked, err := taskLock.TryLock(); err != nil || !locked {
		_ = repositoryLock.Unlock()
		return nil, fmt.Errorf("acquire task lock: %w", lockError(err))
	}
	worktreePath := filepath.Join(stateRoot, "worktrees", safeName(taskID))
	branch := "marshal/" + safeName(taskID)
	if _, err := os.Lstat(worktreePath); !errors.Is(err, os.ErrNotExist) {
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, fmt.Errorf("worktree path already exists: %s", worktreePath)
	}
	if _, err := gitOutput(r.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, fmt.Errorf("worktree branch already exists: %s", branch)
	}
	if err := gitRun(r.Root, "worktree", "add", "-b", branch, worktreePath, baseSHA); err != nil {
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, err
	}
	if err := gitRun(r.Root, "worktree", "lock", "--reason", "managed by Marshal", worktreePath); err != nil {
		_ = gitRun(r.Root, "worktree", "remove", "--force", worktreePath)
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, err
	}
	if err := repositoryLock.Unlock(); err != nil {
		_ = gitRun(r.Root, "worktree", "unlock", worktreePath)
		_ = gitRun(r.Root, "worktree", "remove", "--force", worktreePath)
		_ = taskLock.Unlock()
		return nil, err
	}
	worktree := &Worktree{Path: worktreePath, Branch: branch, BaseSHA: baseSHA, repo: r, taskLock: taskLock}
	if err := worktree.Validate(); err != nil {
		_ = worktree.Release()
		return nil, err
	}
	return worktree, nil
}

func (r Repository) Acquire(stateRoot, taskID, worktreePath, baseSHA string) (*Worktree, error) {
	if err := domain.ValidateID(taskID); err != nil {
		return nil, err
	}
	expected, err := filepath.Abs(filepath.Join(stateRoot, "worktrees", safeName(taskID)))
	if err != nil {
		return nil, err
	}
	actual, err := canonical(worktreePath)
	if err != nil {
		return nil, err
	}
	expected, err = canonical(expected)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, fmt.Errorf("worktree path %q does not match managed task path %q", actual, expected)
	}
	locks := filepath.Join(stateRoot, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, err
	}
	repositoryLock := flock.New(filepath.Join(locks, "repository.lock"))
	taskLock := flock.New(filepath.Join(locks, "task-"+safeName(taskID)+".lock"))
	if locked, err := repositoryLock.TryLock(); err != nil || !locked {
		return nil, fmt.Errorf("acquire repository lock: %w", lockError(err))
	}
	if locked, err := taskLock.TryLock(); err != nil || !locked {
		_ = repositoryLock.Unlock()
		return nil, fmt.Errorf("acquire task lock: %w", lockError(err))
	}
	worktree := &Worktree{Path: actual, Branch: "marshal/" + safeName(taskID), BaseSHA: baseSHA, repo: r, taskLock: taskLock}
	if err := worktree.Validate(); err != nil {
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, err
	}
	if err := gitRun(r.Root, "worktree", "lock", "--reason", "managed by Marshal", actual); err != nil {
		_ = taskLock.Unlock()
		_ = repositoryLock.Unlock()
		return nil, err
	}
	if err := repositoryLock.Unlock(); err != nil {
		_ = gitRun(r.Root, "worktree", "unlock", actual)
		_ = taskLock.Unlock()
		return nil, err
	}
	return worktree, nil
}

func (r Repository) CreateDetached(stateRoot, path, baseSHA string) (*Detached, error) {
	if _, err := r.ResolveBase(baseSHA); err != nil {
		return nil, err
	}
	repositoryLock, err := acquireRepositoryLock(stateRoot)
	if err != nil {
		return nil, err
	}
	defer repositoryLock.Unlock()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("detached worktree path already exists: %s", absolute)
	}
	if err := gitRun(r.Root, "worktree", "add", "--detach", absolute, baseSHA); err != nil {
		return nil, err
	}
	if err := gitRun(r.Root, "worktree", "lock", "--reason", "Marshal baseline diagnostic", absolute); err != nil {
		_ = gitRun(r.Root, "worktree", "remove", "--force", absolute)
		return nil, err
	}
	detached := &Detached{Path: absolute, repo: r, stateRoot: stateRoot}
	opened, err := Open(absolute)
	if err != nil || opened.CommonDir != r.CommonDir {
		_ = gitRun(r.Root, "worktree", "unlock", absolute)
		_ = gitRun(r.Root, "worktree", "remove", "--force", absolute)
		return nil, errors.New("detached worktree identity mismatch")
	}
	return detached, nil
}

func (d *Detached) Remove() error {
	if d == nil || d.Path == "" {
		return nil
	}
	repositoryLock, lockErr := acquireRepositoryLock(d.stateRoot)
	if lockErr != nil {
		return lockErr
	}
	defer repositoryLock.Unlock()
	_ = gitRun(d.repo.Root, "worktree", "unlock", d.Path)
	err := gitRun(d.repo.Root, "worktree", "remove", "--force", d.Path)
	d.Path = ""
	return err
}

func acquireRepositoryLock(stateRoot string) (*flock.Flock, error) {
	locks := filepath.Join(stateRoot, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, err
	}
	repositoryLock := flock.New(filepath.Join(locks, "repository.lock"))
	if locked, err := repositoryLock.TryLock(); err != nil || !locked {
		return nil, fmt.Errorf("acquire repository lock: %w", lockError(err))
	}
	return repositoryLock, nil
}

func (w *Worktree) Validate() error {
	opened, err := Open(w.Path)
	if err != nil {
		return err
	}
	canonicalPath, err := canonical(w.Path)
	if err != nil {
		return err
	}
	if canonicalPath == w.repo.Root {
		return errors.New("task worktree resolved to the main checkout")
	}
	if opened.Root != canonicalPath || opened.CommonDir != w.repo.CommonDir {
		return errors.New("worktree root or Git common directory identity mismatch")
	}
	return nil
}

func (w *Worktree) Release() error {
	if w == nil {
		return nil
	}
	var result error
	if err := gitRun(w.repo.Root, "worktree", "unlock", w.Path); err != nil {
		result = errors.Join(result, err)
	}
	if w.taskLock != nil {
		result = errors.Join(result, w.taskLock.Unlock())
		w.taskLock = nil
	}
	if w.repoLock != nil {
		result = errors.Join(result, w.repoLock.Unlock())
		w.repoLock = nil
	}
	return result
}

func safeName(value string) string {
	value = branchUnsafe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return "task"
	}
	return value
}

func canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func gitOutput(directory string, args ...string) (string, error) {
	return gitOutputContext(context.Background(), directory, args...)
}

func gitOutputContext(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Env = gitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitRun(directory string, args ...string) error {
	_, err := gitOutput(directory, args...)
	return err
}

func gitEnvironment() []string {
	environment := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func lockError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("already locked")
}
