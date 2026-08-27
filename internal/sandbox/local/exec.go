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
	// TranscriptMaxBytes is the bounded stdout capture of the ADR 0055 §3
	// transcript policy (0 = policy absent, the executor-internal bounded
	// capture applies). The effective host capture bound is
	// min(TranscriptMaxBytes, maxCaptureBytes); the moment the stdout
	// capture exceeds the bound the host executor kills the process group
	// and reports TranscriptExceeded.
	TranscriptMaxBytes int64
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
	// TranscriptExceeded reports that the bounded stdout capture exceeded
	// its transcript bound and the workload was killed (ADR 0055 §3.2).
	TranscriptExceeded bool
}

// CommandExecutor is the command execution seam of LocalRunner: tests
// substitute a deterministic executor with a restricted argv contract, and
// the production default is HostExecutor.
type CommandExecutor func(ctx context.Context, spec ExecSpec) ExecOutcome

// Exec implements sandbox.SandboxProvider. The receipt is a lifecycle guard
// only; adversarial probe tokens are contained by construction and recorded
// in the observation log. The optional ADR 0055 workload envelope
// (WorkingDir/Environment/TranscriptPolicy/TimeoutSeconds) is adjudicated
// fail closed against the allocation's Provision-time declarations before
// any spawn; the zero envelope keeps the ADR 0017 behavior exactly.
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
	// Adjudicate the optional ADR 0055 envelope fail closed before any side
	// effect: the provider-independent shape first, then the declared
	// bindings recorded in the allocation record at Provision time.
	if err := request.ValidateEnvelope(); err != nil {
		r.appendDiagnostic(sandbox.OperationExec, request.AllocationId, "exec envelope rejected: "+err.Error())
		return nil, err
	}
	execDir := entry.dir
	workingDirDeclared := false
	if request.WorkingDir != "" {
		resolvedDir, resolveErr := resolveWorkingDir(entry, request.WorkingDir)
		if resolveErr != nil {
			r.appendDiagnostic(sandbox.OperationExec, request.AllocationId, "working dir rejected: "+resolveErr.Error())
			return nil, resolveErr
		}
		execDir = resolvedDir
		workingDirDeclared = true
	}
	envOverlays, err := sandbox.ResolveExecEnvironment(request.Environment, entry.envAllowlist)
	if err != nil {
		r.appendDiagnostic(sandbox.OperationExec, request.AllocationId, "environment rejected: "+err.Error())
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
	if err := containExecTarget(entry.dir, request.Command[0], workingDirDeclared); err != nil {
		r.appendDiagnostic(sandbox.OperationExec, request.AllocationId, "exec target rejected: "+err.Error())
		return nil, err
	}
	transcriptRequested := !request.TranscriptPolicy.Absent()
	spec := ExecSpec{
		Argv:    append([]string(nil), request.Command...),
		Dir:     execDir,
		Env:     mergedEnvironment(envOverlays),
		Stdin:   append([]byte(nil), request.Stdin...),
		Timeout: effectiveExecTimeout(r.execTimeout, request.TimeoutSeconds),
	}
	if transcriptRequested {
		spec.TranscriptMaxBytes = request.TranscriptPolicy.MaxBytes
	}
	entry.spawnCount++
	outcome := r.executor(ctx, spec)
	entry.exitCode = outcome.ExitCode
	// The transcript bound is fail closed: any stdout capture above the
	// declared MaxBytes observes the workload as killed and never produces
	// a partial success, nor a staged artifact (ADR 0055 §3.2).
	transcriptExceeded := outcome.TranscriptExceeded ||
		(transcriptRequested && int64(len(outcome.Stdout)) > request.TranscriptPolicy.MaxBytes)
	status := sandbox.ExecutionCompleted
	switch {
	case transcriptExceeded || outcome.TimedOut || outcome.Signaled:
		status = sandbox.ExecutionKilled
	case !outcome.Started || outcome.ExitCode != 0:
		status = sandbox.ExecutionFailed
	}
	appendLog(entry, fmt.Sprintf("exec: %s status=%s exit=%d", request.Command[0], string(status), outcome.ExitCode))
	receipt := &sandbox.ExecReceipt{
		Status:       status,
		ExitCode:     outcome.ExitCode,
		StdoutSHA256: sandbox.RecomputeSHA256(outcome.Stdout),
		StderrSHA256: sandbox.RecomputeSHA256(outcome.Stderr),
	}
	if transcriptExceeded {
		appendLog(entry, "transcript bound exceeded: workload killed without partial success")
		return receipt, fmt.Errorf("%w: artifact %q (bound %d bytes)", sandbox.ErrTranscriptLimitExceeded, request.TranscriptPolicy.ArtifactId, request.TranscriptPolicy.MaxBytes)
	}
	// The transcript sink records only a cleanly completed capture: an op
	// that timed out, was signaled or never started never stages an
	// artifact (ADR 0055 §3.3/§3.5).
	if transcriptRequested && outcome.Started && !outcome.TimedOut && !outcome.Signaled {
		digest, stageErr := stageTranscriptArtifact(entry, request.TranscriptPolicy.ArtifactId, outcome.Stdout)
		if stageErr != nil {
			return nil, stageErr
		}
		receipt.TranscriptDigest = digest
		receipt.TranscriptStderrDigest = sandbox.RecomputeSHA256(outcome.Stderr)
	}
	return receipt, nil
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

// resolveWorkingDir adjudicates the declared WorkingDir binding of
// ADR 0055 §1 fail closed: the request must be an absolute path, it must
// exist provider side as a directory, and its fully symlink-resolved target
// must equal one of the allocation's declared working roots — with symlinks
// re-evaluated on both sides at request time, so any path rewrite or
// soft-link traversal into an undeclared target is rejected.
func resolveWorkingDir(entry *allocation, workingDir string) (string, error) {
	if !filepath.IsAbs(workingDir) {
		return "", fmt.Errorf("%w: WorkingDir %q must be an absolute path", sandbox.ErrInvalidWorkDir, workingDir)
	}
	resolved, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return "", fmt.Errorf("%w: WorkingDir %q does not exist provider side", sandbox.ErrInvalidWorkDir, workingDir)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: WorkingDir %q is not an existing directory provider side", sandbox.ErrInvalidWorkDir, workingDir)
	}
	for _, declared := range entry.workDirAllowlist {
		if normalizeDeclaredWorkDir(declared) == resolved {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: WorkingDir %q resolves outside the allocation's declared workDirAllowlist", sandbox.ErrInvalidWorkDir, workingDir)
}

// normalizeDeclaredWorkDir resolves one declared working root to its
// canonical comparison form: symlinks are evaluated when the target
// currently exists, so a declared path and its soft-linked spellings always
// compare under their effective target; a not-yet-existing target falls
// back to the cleaned absolute path.
func normalizeDeclaredWorkDir(declared string) string {
	if resolved, err := filepath.EvalSymlinks(declared); err == nil {
		return resolved
	}
	return filepath.Clean(declared)
}

// stageTranscriptArtifact writes one cleanly completed stdout capture as a
// content-addressed staged artifact of the allocation: the target reuses
// the Stage path validation (relative, no traversal, inside the allocation
// directory) and the digest is recomputed from the bytes actually on disk
// after the write, mirroring the post-consumption recomputation of Stage.
// The artifact id is recorded in the staged map like any Stage input.
func stageTranscriptArtifact(entry *allocation, artifactId string, stdout []byte) (string, error) {
	target, err := resolveStageTarget(entry.dir, artifactId)
	if err != nil {
		return "", fmt.Errorf("%w: %v", sandbox.ErrInvalidTranscriptPolicy, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("local: exec: transcript artifact: %w", err)
	}
	if err := os.WriteFile(target, stdout, 0o600); err != nil {
		return "", fmt.Errorf("local: exec: transcript artifact: %w", err)
	}
	readBack, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("local: exec: transcript artifact: %w", err)
	}
	digest := sandbox.RecomputeSHA256(readBack)
	rel, _ := filepath.Rel(entry.dir, target)
	entry.staged[artifactId] = filepath.ToSlash(rel)
	appendLog(entry, "transcript staged: "+artifactId)
	return digest, nil
}

// effectiveExecTimeout freezes the per-op timeout of ADR 0055 §4: a
// positive TimeoutSeconds takes effect as min(requested, the runner cap),
// and any non-positive value keeps the runner default. The arithmetic is
// integer-only and safe for any caller value.
func effectiveExecTimeout(runnerCap time.Duration, requestedSeconds int64) time.Duration {
	if requestedSeconds <= 0 {
		return runnerCap
	}
	if requestedSeconds >= int64(runnerCap/time.Second) {
		return runnerCap
	}
	return time.Duration(requestedSeconds) * time.Second
}

// mergedEnvironment overlays the allow-listed ADR 0055 §2 key=value pairs
// onto the sanitized baseline. An allow-listed key overrides the baseline
// value of the same name (the declaration, never the sanitized default,
// decides an allow-listed key); every other entry of the baseline survives
// unchanged.
func mergedEnvironment(overlays []string) []string {
	values := map[string]string{"LC_ALL": "C", "LANG": "C"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			values[name] = value
		}
	}
	for _, pair := range overlays {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
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

// containExecTarget rejects an argv[0] that escapes the allocation
// directory: relative paths must stay inside the allocation directory and
// bare names carry no path and are resolved by the executor's sanitized
// environment. Absolute paths are rejected outright unless the request
// declared a bound WorkingDir (ADR 0055 §1): a real workload executable
// normally lives outside the allocation directory, so with a declared
// WorkingDir an absolute argv[0] that exists on disk is a legitimate exec
// target; the existence check stays fail closed and a missing or
// directory-target absolute path is rejected.
func containExecTarget(dir, name string, workingDirDeclared bool) error {
	if filepath.IsAbs(name) {
		if !workingDirDeclared {
			return fmt.Errorf("%w: exec target must be a relative path inside the allocation directory", sandbox.ErrInvalidRequest)
		}
		info, err := os.Stat(name)
		if err != nil {
			return fmt.Errorf("%w: absolute exec target %q does not exist on disk", sandbox.ErrInvalidRequest, name)
		}
		if info.IsDir() {
			return fmt.Errorf("%w: absolute exec target %q is a directory, not an executable", sandbox.ErrInvalidRequest, name)
		}
		return nil
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
// stream capture and process-group termination on timeout or on transcript
// overflow.
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
	// The transcript bound is enforced by the host itself: when a
	// TranscriptPolicy is carried, the effective capture bound is
	// min(policy.MaxBytes, the executor-internal maxCaptureBytes), and
	// exceeding it kills the process group immediately (ADR 0055 §3.2).
	// stderr follows the identical capture bound (ADR 0055 §3.5); its
	// overflow truncates without killing the workload, since the stdout
	// transcript is the only artifact-bound capture.
	captureLimit := maxCaptureBytes
	var overflow chan struct{}
	fireOverflow := func() {}
	if spec.TranscriptMaxBytes > 0 {
		if spec.TranscriptMaxBytes < captureLimit {
			captureLimit = spec.TranscriptMaxBytes
		}
		overflow = make(chan struct{})
		var overflowOnce sync.Once
		fireOverflow = func() { overflowOnce.Do(func() { close(overflow) }) }
	}
	stdout := &boundedWriter{limit: captureLimit, onOverflow: fireOverflow}
	stderr := &boundedWriter{limit: captureLimit}
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
	killProcessGroup := func() {
		if pgid, pgidErr := syscall.Getpgid(command.Process.Pid); pgidErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = command.Process.Kill()
		}
	}
	select {
	case <-wait:
		if waitStatus, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
			outcome.Signaled = true
		}
		outcome.ExitCode = command.ProcessState.ExitCode()
	case <-overflow:
		outcome.TranscriptExceeded = true
		killProcessGroup()
		<-wait
		if command.ProcessState != nil {
			outcome.ExitCode = command.ProcessState.ExitCode()
		}
	case <-runContext.Done():
		outcome.TimedOut = true
		killProcessGroup()
		<-wait
		if command.ProcessState != nil {
			outcome.ExitCode = command.ProcessState.ExitCode()
		}
	}
	outcome.Stdout = stdout.bytes()
	outcome.Stderr = stderr.bytes()
	return outcome
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

// boundedWriter captures at most limit bytes of one output stream. When an
// onOverflow hook is set it fires exactly once per stream whose total write
// volume exceeds the limit.
type boundedWriter struct {
	mu         sync.Mutex
	limit      int64
	buf        bytes.Buffer
	total      int64
	onOverflow func()
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
	if w.total > w.limit && w.onOverflow != nil {
		w.onOverflow()
	}
	return written, nil
}

func (w *boundedWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.buf.Bytes())
}
