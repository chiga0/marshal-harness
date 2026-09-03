package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

type Runner struct {
	Environment []string
}

func (r Runner) Run(ctx context.Context, worktree string, spec CommandSpec) CommandResult {
	started := time.Now().UTC()
	executableHint := "unresolved"
	if len(spec.Argv) > 0 {
		executableHint = "unresolved:" + spec.Argv[0]
	}
	result := CommandResult{StartedAt: started, Status: "error", Record: CommandRecord{Argv: append([]string(nil), spec.Argv...), CWD: spec.CWD, Executable: executableHint, StartedAt: started, BaselineStatus: "not-run"}}
	if len(spec.Argv) == 0 || strings.HasPrefix(spec.Argv[0], "-") {
		return finishCommand(result, nil, errors.New("command argv is empty or invalid"), started)
	}
	if strings.HasPrefix(spec.Argv[0], verificationbuiltin.ReservedPrefix) {
		result.Record.Executable = verificationbuiltin.ReservedPrefix + "denied"
		return finishCommand(result, nil, errors.New(verificationbuiltin.ReasonDenied), started)
	}
	if spec.Timeout <= 0 {
		return finishCommand(result, nil, errors.New("command timeout must be positive"), started)
	}
	cwd, err := secureDirectory(worktree, spec.CWD)
	if err != nil {
		return finishCommand(result, nil, err, started)
	}
	environment := verifierEnvironment(r.Environment)
	executable, err := lookPath(spec.Argv[0], cwd, worktree, environment)
	if err != nil {
		return finishCommand(result, nil, err, started)
	}
	command := exec.Command(executable, spec.Argv[1:]...)
	command.Args = append([]string(nil), spec.Argv...)
	command.Dir = cwd
	command.Env = environment
	result.Record.Executable = executable
	result.Record.StartedAt = started
	configureProcess(command)
	limit := spec.MaxLogBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	stdout := newTailCapture(limit)
	stderr := newTailCapture(limit)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		result.Stdout, result.Stderr = stdout.Bytes(), stderr.Bytes()
		return finishCommand(result, command, err, started)
	}
	runContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err = <-wait:
	case <-runContext.Done():
		terminateProcess(command)
		err = <-wait
		result.Status = "cancelled"
	}
	result.Stdout, result.Stderr = stdout.Bytes(), stderr.Bytes()
	result.Record.Truncated = stdout.Truncated() || stderr.Truncated()
	result.StdoutTruncated, result.StderrTruncated = stdout.Truncated(), stderr.Truncated()
	result = finishCommand(result, command, err, started)
	if runContext.Err() != nil {
		result.Status = "cancelled"
	}
	return result
}

func finishCommand(result CommandResult, command *exec.Cmd, runErr error, started time.Time) CommandResult {
	result.EndedAt = time.Now().UTC()
	result.Record.CompletedAt = result.EndedAt
	result.Record.DurationMilliseconds = result.EndedAt.Sub(started).Milliseconds()
	if command == nil || command.ProcessState == nil {
		result.Status = "error"
		return result
	}
	exitCode := command.ProcessState.ExitCode()
	result.Record.ExitCode = &exitCode
	if waitStatus, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
		signal := waitStatus.Signal().String()
		result.Record.Signal = &signal
	}
	if result.Status == "cancelled" {
		return result
	}
	if runErr == nil && exitCode == 0 {
		result.Status = "pass"
	} else {
		result.Status = "fail"
	}
	return result
}

func secureDirectory(root, relative string) (string, error) {
	if relative == "." {
		relative = ""
	}
	if relative != "" {
		if err := validateRelativePath(relative); err != nil {
			return "", err
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(canonicalRoot, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !within(canonicalRoot, resolved) {
		return "", errors.New("command cwd escapes worktree")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("command cwd is not a directory")
	}
	return resolved, nil
}

func lookPath(name, cwd, worktree string, environment []string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		candidate := name
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(cwd, candidate)
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("executable is not runnable: %s", name)
		}
		if !filepath.IsAbs(name) {
			canonicalRoot, rootErr := filepath.EvalSymlinks(worktree)
			if rootErr != nil || !within(canonicalRoot, resolved) {
				return "", fmt.Errorf("relative executable escapes worktree: %s", name)
			}
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("executable is not runnable: %s", name)
		}
		return resolved, nil
	}
	pathValue := ""
	for _, item := range environment {
		if strings.HasPrefix(item, "PATH=") {
			pathValue = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	for _, directory := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("executable %q not found in filtered PATH", name)
}

func verifierEnvironment(additional []string) []string {
	values := map[string]string{"LC_ALL": "C", "LANG": "C", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/dev/null"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			values[name] = value
		}
	}
	for _, item := range additional {
		name, value, found := strings.Cut(item, "=")
		if found && name != "" && !strings.HasPrefix(name, "GIT_") && !sensitiveEnvironmentName(name) {
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slicesSort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func sensitiveEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH", "SSH_", "AWS_", "AZURE_", "GOOGLE_", "GITHUB_", "GITLAB_"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func slicesSort(values []string) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}

type tailCapture struct {
	mu        sync.Mutex
	limit     int64
	total     int64
	head      bytes.Buffer
	tail      []byte
	tailStart int
}

func newTailCapture(limit int64) *tailCapture { return &tailCapture{limit: limit} }

func (w *tailCapture) Write(data []byte) (int, error) {
	originalLength := len(data)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(data))
	headLimit := w.limit / 2
	if int64(w.head.Len()) < headLimit {
		count := int(headLimit - int64(w.head.Len()))
		if count > len(data) {
			count = len(data)
		}
		_, _ = w.head.Write(data[:count])
		data = data[count:]
	}
	tailLimit := int(w.limit - headLimit)
	if tailLimit > 0 {
		if w.tail == nil {
			w.tail = make([]byte, tailLimit)
		}
		for _, value := range data {
			w.tail[w.tailStart%tailLimit] = value
			w.tailStart++
		}
	}
	return originalLength, nil
}

func (w *tailCapture) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.total <= w.limit {
		tailCount := int(w.total) - w.head.Len()
		return append(bytes.Clone(w.head.Bytes()), w.tail[:tailCount]...)
	}
	tailLimit := len(w.tail)
	result := append(bytes.Clone(w.head.Bytes()), []byte("\n... output truncated ...\n")...)
	start := w.tailStart % tailLimit
	result = append(result, w.tail[start:]...)
	result = append(result, w.tail[:start]...)
	return result
}

func (w *tailCapture) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total > w.limit
}
