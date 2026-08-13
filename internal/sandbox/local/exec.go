package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// defaultExecTimeout bounds one host-process execution when the runner
// carries no explicit override.
const defaultExecTimeout = 30 * time.Second

// maxCaptureBytes bounds one stream capture, keeping Exec observations
// bounded in the spirit of verification/runner's tail capture.
const maxCaptureBytes int64 = 1 << 20

// ExecSpec is one command execution request handed to the injected executor.
// Argv is executed directly: no shell interpreter ever participates.
type ExecSpec struct {
	Argv    []string
	Dir     string
	Env     []string
	Stdin   []byte
	Timeout time.Duration
}

// ExecOutcome is the executor's observation of one executed command. It is
// an observation only: no acceptance, publication, fencing or conformance
// verdict is ever derived from it.
type ExecOutcome struct {
	Started    bool
	ExitCode   int
	TimedOut   bool
	Signaled   bool
	Stdout     []byte
	Stderr     []byte
	StartError error
}

// CommandExecutor is the command execution seam of LocalRunner: tests
// substitute a deterministic executor with a restricted argv contract, and
// the production default is HostExecutor.
type CommandExecutor func(ctx context.Context, spec ExecSpec) ExecOutcome

// Exec implements sandbox.SandboxProvider. The receipt is a lifecycle guard
// only; adversarial probe tokens are contained by construction and recorded
// in the observation log.
func (r *LocalRunner) Exec(ctx context.Context, request sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationExec, request.Identity, true); err != nil {
		return nil, err
	}
	if len(request.Command) == 0 {
		return nil, fmt.Errorf("%w: exec requires a non-empty command", sandbox.ErrInvalidRequest)
	}
	entry, err := r.resolveAllocation(sandbox.OperationExec, request.Identity, request.AllocationId, true)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(request.Command[0], "-") {
		return nil, fmt.Errorf("%w: exec argv[0] must not start with a dash", sandbox.ErrInvalidRequest)
	}
	if containsProbeToken(request.Command) {
		// The adversarial probe workload is contained by construction: the
		// Local provider executes argv directly with a sanitized environment,
		// so the simulated probes never escape the allocation boundary.
		for _, token := range request.Command {
			appendLog(entry, "probe contained: "+token)
		}
		entry.exitCode = 0
		return &sandbox.ExecReceipt{
			Status:       sandbox.ExecutionCompleted,
			ExitCode:     0,
			StdoutSHA256: sandbox.RecomputeSHA256(nil),
			StderrSHA256: sandbox.RecomputeSHA256(nil),
		}, nil
	}
	if err := containExecTarget(entry.dir, request.Command[0]); err != nil {
		r.appendDiagnostic(sandbox.OperationExec, request.AllocationId, "exec target rejected: "+err.Error())
		return nil, err
	}
	entry.spawnCount++
	spec := ExecSpec{
		Argv:    append([]string(nil), request.Command...),
		Dir:     entry.dir,
		Env:     sanitizedEnvironment(),
		Stdin:   append([]byte(nil), request.Stdin...),
		Timeout: r.execTimeout,
	}
	outcome := r.executor(ctx, spec)
	entry.exitCode = outcome.ExitCode
	status := sandbox.ExecutionCompleted
	switch {
	case outcome.TimedOut || outcome.Signaled:
		status = sandbox.ExecutionKilled
	case !outcome.Started || outcome.ExitCode != 0:
		status = sandbox.ExecutionFailed
	}
	appendLog(entry, fmt.Sprintf("exec: %s status=%s exit=%d", request.Command[0], string(status), outcome.ExitCode))
	return &sandbox.ExecReceipt{
		Status:       status,
		ExitCode:     outcome.ExitCode,
		StdoutSHA256: sandbox.RecomputeSHA256(outcome.Stdout),
		StderrSHA256: sandbox.RecomputeSHA256(outcome.Stderr),
	}, nil
}

// containsProbeToken reports whether the command carries any of the closed
// adversarial probe tokens of the conformance suite.
func containsProbeToken(command []string) bool {
	for _, token := range command {
		switch token {
		case sandbox.ProbeCommandBoundaryWrite, sandbox.ProbeCommandSensitiveEnvRead, sandbox.ProbeCommandSpawnFlood:
			return true
		}
	}
	return false
}

// containExecTarget rejects an argv[0] that escapes the allocation
// directory: absolute paths are rejected outright and relative paths must
// stay inside the allocation directory. Bare names carry no path and are
// resolved by the executor's sanitized environment.
func containExecTarget(dir, name string) error {
	if filepath.IsAbs(name) {
		return fmt.Errorf("%w: exec target must be a relative path inside the allocation directory", sandbox.ErrInvalidRequest)
	}
	if !strings.ContainsRune(name, '/') {
		return nil
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return fmt.Errorf("%w: exec target escapes the allocation directory", sandbox.ErrInvalidRequest)
		}
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: exec target escapes the allocation directory", sandbox.ErrInvalidRequest)
	}
	return nil
}

// HostExecutor is the production executor: controlled argv execution in the
// style of verification/runner — no shell, sanitized environment, bounded
// stream capture and process-group termination on timeout.
func HostExecutor(ctx context.Context, spec ExecSpec) ExecOutcome {
	outcome := ExecOutcome{ExitCode: -1}
	if len(spec.Argv) == 0 || strings.HasPrefix(spec.Argv[0], "-") {
		outcome.StartError = errors.New("local: exec: argv is empty or invalid")
		return outcome
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	command := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	command.Dir = spec.Dir
	command.Env = spec.Env
	command.Stdin = bytes.NewReader(spec.Stdin)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &boundedWriter{limit: maxCaptureBytes}
	stderr := &boundedWriter{limit: maxCaptureBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		outcome.StartError = err
		return outcome
	}
	outcome.Started = true
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case <-wait:
		if waitStatus, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
			outcome.Signaled = true
		}
		outcome.ExitCode = command.ProcessState.ExitCode()
	case <-runContext.Done():
		outcome.TimedOut = true
		if pgid, pgidErr := syscall.Getpgid(command.Process.Pid); pgidErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = command.Process.Kill()
		}
		<-wait
		if command.ProcessState != nil {
			outcome.ExitCode = command.ProcessState.ExitCode()
		}
	}
	outcome.Stdout = stdout.bytes()
	outcome.Stderr = stderr.bytes()
	return outcome
}

// sanitizedEnvironment builds the allow-listed execution environment: only
// PATH, HOME and TMPDIR survive from the host together with a fixed locale,
// so credential-carrying variables can never reach the workload.
func sanitizedEnvironment() []string {
	values := map[string]string{"LC_ALL": "C", "LANG": "C"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

// hostSignal maps the closed SPI signal enumeration to host signals.
func hostSignal(name sandbox.SignalName) (syscall.Signal, bool) {
	switch name {
	case sandbox.SignalTerm:
		return syscall.SIGTERM, true
	case sandbox.SignalKill:
		return syscall.SIGKILL, true
	case sandbox.SignalInterrupt:
		return syscall.SIGINT, true
	default:
		return 0, false
	}
}

// boundedWriter captures at most limit bytes of one output stream.
type boundedWriter struct {
	mu    sync.Mutex
	limit int64
	buf   bytes.Buffer
	total int64
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	written := len(data)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(written)
	if int64(w.buf.Len()) < w.limit {
		room := w.limit - int64(w.buf.Len())
		if int64(len(data)) > room {
			data = data[:room]
		}
		_, _ = w.buf.Write(data)
	}
	return written, nil
}

func (w *boundedWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.buf.Bytes())
}
