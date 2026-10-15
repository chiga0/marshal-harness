package qoder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// attemptObservation is the bounded outcome of executing one qoder attempt:
// the decoded JSONL aggregate, bounded stderr, exit/signal status, and the
// attempt wall-clock bounds. It never carries provider free text beyond the
// bounded evidence files.
type attemptObservation struct {
	capture       captureResult
	stderr        streamCapture
	exitCode      int
	signal        string
	processFailed bool
	startedAt     time.Time
	completedAt   time.Time
}

// runLocalAttempt executes one qoder attempt as a supervised host child
// process: process group, cancellation/timeout kill, and bounded stdout/stderr
// capture. It owns local process semantics only and never interprets the
// qoder protocol payload.
func (a *Adapter) runLocalAttempt(runCtx context.Context, executable string, arguments []string, workingDirectory string, environment []string, outputLimit int64) (attemptObservation, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = workingDirectory
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return attemptObservation{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return attemptObservation{}, err
	}
	started := a.now().UTC()
	if err := command.Start(); err != nil {
		return attemptObservation{}, fmt.Errorf("start qoder: %w", err)
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { terminateGroup(command) }) }
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() { stdoutDone <- captureJSONL(stdout, outputLimit, kill) }()
	go func() { stderrDone <- captureStream(stderr, stderrLimit) }()
	processFinished := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			kill()
		case <-processFinished:
		}
	}()
	capture := <-stdoutDone
	stderrCapture := <-stderrDone
	waitErr := command.Wait()
	close(processFinished)
	exitCode, signal := processOutcome(command)
	return attemptObservation{
		capture:       capture,
		stderr:        stderrCapture,
		exitCode:      exitCode,
		signal:        signal,
		processFailed: waitErr != nil,
		startedAt:     started,
		completedAt:   a.now().UTC(),
	}, nil
}

// hardeningFlags returns the frozen, ordered non-interactive hardening surface
// for the real Qoder CLI. The previous `run --json --non-interactive --sandbox
// workspace-write` construct does not exist in the real help; the real CLI
// exposes --print as the non-interactive entry, --output-format for structured
// output, --permission-mode for permissions, and --no-session-persistence to
// keep the attempt ephemeral.
//
// The real 1.1.23 help lists the permission choices default, accept_edits,
// bypass_permissions, dont_ask, and auto; workspace-write is not a legal
// permission mode. Marshal freezes accept_edits because it stays
// non-interactive while still routing file edits through the provider's
// permission system; bypass_permissions is forbidden because it would remove
// that gate. The same help lists setting-sources user, project, and local
// only; managed is not legal. Marshal therefore passes an empty
// --setting-sources set so user/project/local settings never influence the
// attempt, and rebinds HOME/XDG to the managed config dir as the only
// remaining config source. --output-format and the JSONL event schema are
// frozen pending live conformance and never authorize a run on their own.
func hardeningFlags(configDir string) []string {
	return []string{
		"--print",
		"--output-format", "jsonl",
		"--permission-mode", "accept_edits",
		"--no-session-persistence",
		"--config-dir", configDir,
		"--setting-sources", "",
	}
}

// buildArgs produces the exact hardened argv for a non-interactive qoder
// attempt. The prompt is always the final positional argument; Marshal never
// invokes qoder through a shell.
func buildArgs(model, configDir, prompt string) []string {
	args := append([]string{}, hardeningFlags(configDir)...)
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

// processFailureError reports a failed qoder process using only fixed
// classification and exit/signal information. Provider stderr is persisted
// separately as a bounded evidence file (qoder-stderr.log) but is never
// concatenated into the returned error, so tokens, secrets, or user content
// cannot reach Events, CLI output, or Outcome.
func processFailureError(exitCode int, signal string) error {
	if signal != "" {
		return fmt.Errorf("%w: exit=%d signal=%s", ErrProcessFailed, exitCode, signal)
	}
	return fmt.Errorf("%w: exit=%d", ErrProcessFailed, exitCode)
}

func processOutcome(command *exec.Cmd) (int, string) {
	if command.ProcessState == nil {
		return -1, ""
	}
	exitCode := command.ProcessState.ExitCode()
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return exitCode, status.Signal().String()
	}
	return exitCode, ""
}

func contextError(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return ""
}

// workerEnvironment fully replaces the ambient environment with a fixed
// benign allowlist plus Marshal isolation variables. Credentials, ambient
// HOME, user identity, and XDG config directories never reach the worker
// process. HOME is explicitly rebound to the Marshal-managed config dir so
// Node/Qoder cannot fall back to the system account home (os.homedir()) when
// HOME is unset, and user/project/local settings are never read.
func workerEnvironment(worktree, configDir string) []string {
	allowed := map[string]bool{
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"PATH": true, "TERM": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	}
	environment := make([]string, 0, len(allowed)+11)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment,
		"CI=1",
		"HOME="+configDir,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_CACHE_HOME="+filepath.Join(configDir, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(configDir, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(configDir, ".local", "state"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes",
		"PWD="+worktree,
	)
	return environment
}

// probeEnvironment mirrors the sanitized probe environment and deliberately
// rebinds HOME to a benign isolated directory so the `--version` probe never
// falls back to the system account home or reads user configuration.
func probeEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "TMPDIR" || key == "LANG" {
			result = append(result, entry)
		}
	}
	return append(result, "HOME="+os.TempDir())
}

// managedConfigDir binds the Marshal-managed, isolated Qoder config root. The
// control root and the target are both EvalSymlinks-resolved before any
// containment comparison, so a symlinked control root (e.g. macOS /var ->
// /private/var) is not misjudged as an escape. Every path component strictly
// below the control root is Lstat-checked so an uncontrolled parent symlink is
// rejected before MkdirAll could follow it, and the resulting directory is
// verified to be a real directory with private 0o700 permissions. A symlink,
// an escape, or an abnormal mode fails closed before any worker launches.
func managedConfigDir(controlRoot string) (string, error) {
	root, err := filepath.EvalSymlinks(controlRoot)
	if err != nil {
		return "", fmt.Errorf("resolve control root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", errors.New("control root must be an existing directory")
	}
	path := filepath.Join(root, "config", "qoder")
	// Reject any symlink along the managed path (the target or an uncontrolled
	// parent) before MkdirAll/Chmod could follow it and act outside the
	// control root.
	if err := rejectSymlinkedComponents(root, path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("create qoder config dir: %w", err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve qoder config dir: %w", err)
	}
	if !pathWithin(root, real) {
		return "", errors.New("qoder config dir escapes control root")
	}
	// Normalize to private permissions: a pre-existing group/world-accessible
	// directory could otherwise leak configuration or be influenced by another
	// principal.
	if err := os.Chmod(real, 0o700); err != nil {
		return "", fmt.Errorf("lock qoder config dir permissions: %w", err)
	}
	info, err = os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat resolved qoder config dir: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("qoder config dir is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return "", errors.New("qoder config dir has non-private permissions")
	}
	return real, nil
}

// rejectSymlinkedComponents Lstat-checks every path component strictly below
// the control root and rejects any component that is a symlink. This closes
// the gap where MkdirAll would silently follow an uncontrolled parent symlink
// and create the config dir outside the control root before the final
// containment check could run.
func rejectSymlinkedComponents(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("qoder config dir is outside the control root")
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("qoder config dir must not traverse a symlink: %s", component)
			}
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat qoder config dir component: %w", statErr)
		}
	}
	return nil
}

func readModel(controlRoot, relative string) string {
	path, err := existingPathWithin(controlRoot, relative)
	if err != nil {
		return ""
	}
	data, err := readBounded(path, maxResultBytes)
	if err != nil {
		return ""
	}
	var task struct {
		Worker struct {
			Model string `json:"model"`
		} `json:"worker"`
	}
	if json.Unmarshal(data, &task) != nil {
		return ""
	}
	return task.Worker.Model
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func lexicalPathWithin(root, relative string) (string, error) {
	path := filepath.Join(root, relative)
	if !pathWithin(root, path) {
		return "", errors.New("path escapes control root")
	}
	return path, nil
}

func existingPathWithin(root, relative string) (string, error) {
	path, err := lexicalPathWithin(root, relative)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !pathWithin(root, real) {
		return "", errors.New("symlink escapes control root")
	}
	return real, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds byte limit")
	}
	return data, nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".qoder-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func terminateGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	group, err := syscall.Getpgid(command.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = command.Process.Kill()
	}
}
