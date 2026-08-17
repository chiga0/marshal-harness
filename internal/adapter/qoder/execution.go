package qoder

import (
	"bytes"
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

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"golang.org/x/sys/unix"
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
func (a *Adapter) runLocalAttempt(runCtx context.Context, executable string, arguments []string, prompt []byte, workingDirectory string, environment []string, outputLimit int64) (attemptObservation, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = workingDirectory
	command.Env = environment
	command.Stdin = bytes.NewReader(prompt)
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
	kill := func() {
		killOnce.Do(func() {
			terminateGroup(command)
			_ = stdout.Close()
			_ = stderr.Close()
		})
	}
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
		"--output-format", "stream-json",
		"--permission-mode", "accept_edits",
		"--no-session-persistence",
		"--config-dir", configDir,
		"--setting-sources", "",
	}
}

// buildArgs produces the exact hardened argv for a non-interactive qoder
// attempt. The prompt is deliberately absent and travels only through stdin;
// Marshal never invokes qoder through a shell.
func buildArgs(model, configDir, worktree string, disableAllTools bool) []string {
	args := append([]string{}, hardeningFlags(configDir)...)
	args = append(args, "--cwd", worktree)
	if disableAllTools {
		args = append(args, "--tools", "")
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
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
func probeEnvironment(probeRoot string) []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "TMPDIR" || key == "LANG" {
			result = append(result, entry)
		}
	}
	return append(result,
		"HOME="+probeRoot,
		"XDG_CONFIG_HOME="+filepath.Join(probeRoot, "xdg-config"),
		"XDG_CACHE_HOME="+filepath.Join(probeRoot, "xdg-cache"),
		"XDG_DATA_HOME="+filepath.Join(probeRoot, "xdg-data"),
		"XDG_STATE_HOME="+filepath.Join(probeRoot, "xdg-state"),
	)
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
			if !info.IsDir() {
				return fmt.Errorf("qoder config path component is not a directory: %s", component)
			}
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("qoder config path component has non-private permissions: %s", component)
			}
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat qoder config dir component: %w", statErr)
		}
	}
	return nil
}

type taskProjection struct {
	model           string
	disableAllTools bool
}

func readTaskProjection(controlRoot, relative string) (taskProjection, error) {
	path, err := existingPathWithin(controlRoot, relative)
	if err != nil {
		return taskProjection{}, fmt.Errorf("resolve TaskSpec: %w", err)
	}
	data, err := readBounded(path, maxResultBytes)
	if err != nil {
		return taskProjection{}, fmt.Errorf("read TaskSpec: %w", err)
	}
	var task struct {
		Worker struct {
			Model string `json:"model"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return taskProjection{}, fmt.Errorf("decode TaskSpec: %w", err)
	}
	tools, err := denials.ParseDeclaredWorkerTools(data)
	if err != nil {
		return taskProjection{}, fmt.Errorf("worker tools: %w", err)
	}
	if len(tools) > 0 {
		return taskProjection{}, fmt.Errorf("%w: named worker.tools cannot be mapped to a verified Qoder built-in tool identifier", ErrUnsupportedWorkerTools)
	}
	return taskProjection{model: task.Worker.Model, disableAllTools: tools != nil}, nil
}

// preparePrivateDirectory creates each missing directory component one at a
// time and rejects every existing symlink, non-directory, or non-private
// component below root. The final realpath must remain contained in root.
func preparePrivateDirectory(root, target string) (string, error) {
	if !pathWithin(root, target) {
		return "", errors.New("output directory escapes control root")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", fmt.Errorf("create private output directory: %w", err)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect output directory component: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("output directory must not traverse a symlink: %s", component)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("output path component is not a directory: %s", component)
		}
		if info.Mode().Perm() != 0o700 {
			return "", fmt.Errorf("output directory component has non-private permissions: %s", component)
		}
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve output directory: %w", err)
	}
	if !pathWithin(root, real) {
		return "", errors.New("output directory escapes control root")
	}
	return real, nil
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

func digestBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// snapshotExecutable copies the inspected executable into a private immutable
// launch object, then revalidates both digest and version against the inspected
// identity. Run executes only this object, closing inspect-to-Start replacement
// races on the configured path.
func snapshotExecutable(ctx context.Context, identity executableIdentity) (string, func(), error) {
	root, err := os.MkdirTemp("", "marshal-qoder-exec-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	source, err := os.Open(identity.path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer source.Close()
	path := filepath.Join(root, "qodercli")
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, copyErr := io.Copy(target, source)
	syncErr := target.Sync()
	closeErr := target.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		cleanup()
		return "", nil, errors.Join(copyErr, syncErr, closeErr)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		cleanup()
		return "", nil, err
	}
	digest, err := digestFile(path)
	if err != nil || digest != identity.digest {
		cleanup()
		return "", nil, fmt.Errorf("%w: executable changed before immutable launch snapshot", ErrIdentityDrift)
	}
	version, err := readBinaryVersion(ctx, path)
	if err != nil || version != identity.version {
		cleanup()
		return "", nil, fmt.Errorf("%w: launch snapshot version does not match inspected identity", ErrIdentityDrift)
	}
	return path, cleanup, nil
}

type trustedOutputDirectory struct {
	path string
	dir  *os.File
	info os.FileInfo
}

type claimedLeaf struct {
	name string
	file *os.File
	info os.FileInfo
}

func openTrustedOutputDirectory(path string) (*trustedOutputDirectory, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := dir.Stat()
	linkInfo, linkErr := os.Lstat(path)
	if err != nil || linkErr != nil || !info.IsDir() || linkInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, linkInfo) {
		dir.Close()
		return nil, errors.New("output directory changed while acquiring trusted handle")
	}
	return &trustedOutputDirectory{path: path, dir: dir, info: info}, nil
}

func (directory *trustedOutputDirectory) close() error { return directory.dir.Close() }

func (directory *trustedOutputDirectory) verifyPathBinding() error {
	info, err := os.Lstat(directory.path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(info, directory.info) {
		return errors.New("output directory path no longer names the trusted directory")
	}
	return nil
}

func (directory *trustedOutputDirectory) claim(name, kind string) (*claimedLeaf, error) {
	if name == "" || name == "." || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid attempt %s leaf name", kind)
	}
	fd, err := unix.Openat(int(directory.dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("attempt %s leaf unexpectedly pre-exists", kind)
		}
		return nil, fmt.Errorf("claim attempt %s leaf: %w", kind, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("stat claimed attempt %s leaf: %w", kind, err)
	}
	return &claimedLeaf{name: name, file: file, info: info}, nil
}

func (leaf *claimedLeaf) close() error { return leaf.file.Close() }

func (leaf *claimedLeaf) write(data []byte) error {
	info, err := leaf.file.Stat()
	if err != nil || !os.SameFile(info, leaf.info) {
		return errors.New("claimed output leaf identity changed")
	}
	if err := leaf.file.Truncate(0); err != nil {
		return err
	}
	if _, err := leaf.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := leaf.file.Write(data); err != nil {
		return err
	}
	return leaf.file.Sync()
}

func (leaf *claimedLeaf) readBounded(limit int64) ([]byte, error) {
	info, err := leaf.file.Stat()
	if err != nil || !os.SameFile(info, leaf.info) {
		return nil, errors.New("claimed output leaf identity changed")
	}
	if _, err := leaf.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(leaf.file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds byte limit")
	}
	return data, nil
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
