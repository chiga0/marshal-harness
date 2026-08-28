//go:build darwin

package processsupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type processReport struct {
	State               string          `json:"state"`
	ObserverIdentity    string          `json:"observerIdentity"`
	ObservedAt          string          `json:"observedAt"`
	Process             ProcessIdentity `json:"process"`
	RuntimeObjectDigest string          `json:"runtimeObjectDigest"`
	WorkingObjectDigest string          `json:"workingObjectDigest"`
	ExitCode            int             `json:"exitCode,omitempty"`
	Signal              string          `json:"signal,omitempty"`
	StdoutDigest        string          `json:"stdoutDigest,omitempty"`
	StderrDigest        string          `json:"stderrDigest,omitempty"`
	StdoutBytes         uint64          `json:"stdoutBytes,omitempty"`
	StderrBytes         uint64          `json:"stderrBytes,omitempty"`
	TranscriptTruncated bool            `json:"transcriptTruncated,omitempty"`
}

type waitOutcome struct {
	exitCode int
	signal   string
}

type boundedCapture struct {
	mu        sync.Mutex
	hash      hashWriter
	data      []byte
	total     uint64
	limit     int
	truncated bool
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func newBoundedCapture(limit int) *boundedCapture {
	return &boundedCapture{hash: sha256.New(), limit: limit}
}

func (capture *boundedCapture) Write(input []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	_, _ = capture.hash.Write(input)
	if capture.total > uint64(MaxTranscriptBytes)-minUint64(uint64(len(input)), MaxTranscriptBytes) {
		capture.total = uint64(MaxTranscriptBytes) + 1
	} else {
		capture.total += uint64(len(input))
	}
	remaining := capture.limit - len(capture.data)
	if remaining > 0 {
		if remaining > len(input) {
			remaining = len(input)
		}
		capture.data = append(capture.data, input[:remaining]...)
	}
	if len(input) > remaining {
		capture.truncated = true
	}
	return len(input), nil
}

func (capture *boundedCapture) snapshot() ([]byte, string, uint64, bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]byte(nil), capture.data...), "sha256:" + hex.EncodeToString(capture.hash.Sum(nil)), capture.total, capture.truncated
}

type vnodeGuard struct {
	queue   int
	tainted bool
}

func newVnodeGuard(files []*os.File, specs []HeldObjectSpec) (*vnodeGuard, error) {
	if len(files) != len(specs) {
		return nil, ErrInvalid
	}
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	changes := make([]unix.Kevent_t, 0, len(files))
	for index, file := range files {
		if file == nil {
			_ = unix.Close(queue)
			return nil, ErrInvalid
		}
		flags := uint32(unix.NOTE_DELETE | unix.NOTE_ATTRIB | unix.NOTE_RENAME | unix.NOTE_REVOKE)
		if specs[index].Role != "working-directory" {
			flags |= unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_LINK
		}
		changes = append(changes, unix.Kevent_t{Ident: uint64(file.Fd()), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: flags})
	}
	if _, err := unix.Kevent(queue, changes, nil, nil); err != nil {
		_ = unix.Close(queue)
		return nil, err
	}
	return &vnodeGuard{queue: queue}, nil
}

func (guard *vnodeGuard) clean() bool {
	if guard == nil || guard.tainted || guard.queue < 0 {
		return false
	}
	events := make([]unix.Kevent_t, 8)
	timeout := unix.Timespec{}
	count, err := unix.Kevent(guard.queue, nil, events, &timeout)
	if err != nil || count != 0 {
		guard.tainted = true
	}
	return !guard.tainted
}

func (guard *vnodeGuard) close() {
	if guard != nil && guard.queue >= 0 {
		_ = unix.Close(guard.queue)
		guard.queue = -1
	}
}

type darwinMechanics struct {
	mu sync.Mutex

	controlDirectory *os.File
	marshalFile      *os.File
	marshalSpec      HeldObjectSpec

	command        *exec.Cmd
	process        ProcessIdentity
	runtimeSpec    HeldObjectSpec
	workingSpec    HeldObjectSpec
	heldFiles      []*os.File
	heldSpecs      []HeldObjectSpec
	guard          *vnodeGuard
	stdout         *boundedCapture
	stderr         *boundedCapture
	waited         chan waitOutcome
	waitResult     *waitOutcome
	stopped        bool
	resumed        bool
	terminal       bool
	collected      bool
	closed         bool
	lastReport     processReport
	transcriptHash string
}

func NewPlatformMechanics(controlDirectory *os.File) (Mechanics, error) {
	if controlDirectory == nil {
		return nil, ErrInvalid
	}
	path, err := os.Executable()
	if err != nil || !absoluteClean(path) {
		return nil, ErrConflict
	}
	marshalFile, spec, err := openObservedSpec("marshal", path, "regular")
	if err != nil || spec.validate("marshal", "regular") != nil {
		if marshalFile != nil {
			_ = marshalFile.Close()
		}
		return nil, ErrConflict
	}
	return &darwinMechanics{controlDirectory: controlDirectory, marshalFile: marshalFile, marshalSpec: spec}, nil
}

func (mechanics *darwinMechanics) Spawn(ctx context.Context, payload SpawnPayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if mechanics.command != nil || mechanics.closed || validateSpawnPayload(payload) != nil {
		return MechanicsResult{}, ErrConflict
	}
	for _, object := range spawnObjects(payload) {
		if object.Device == mechanics.marshalSpec.Device && object.Inode == mechanics.marshalSpec.Inode {
			return MechanicsResult{}, ErrConflict
		}
	}
	var descriptorLimit unix.Rlimit
	requiredHeld := uint64(1 + len(payload.MaterialRoots) + len(payload.LaunchMaterials))
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &descriptorLimit) != nil || requiredHeld+32 >= descriptorLimit.Cur {
		return MechanicsResult{}, ErrMechanicsOpen
	}
	held, err := openSpawnObjects(payload)
	if err != nil {
		return MechanicsResult{}, err
	}
	owned := false
	defer func() {
		if !owned {
			closeFiles(held...)
		}
	}()
	allGuarded := append(append([]*os.File(nil), held...), mechanics.marshalFile)
	allSpecs := append(spawnObjects(payload), mechanics.marshalSpec)
	guard, err := newVnodeGuard(allGuarded, allSpecs)
	if err != nil {
		return MechanicsResult{}, ErrConflict
	}
	guardOwned := false
	defer func() {
		if !guardOwned {
			guard.close()
		}
	}()
	specRead, specWrite, err := os.Pipe()
	if err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		closeFiles(specRead, specWrite)
		return MechanicsResult{}, ErrIntervention
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		closeFiles(specRead, specWrite, readyRead, readyWrite)
		return MechanicsResult{}, ErrIntervention
	}
	defer closeFiles(specRead, specWrite, readyRead, readyWrite, releaseRead, releaseWrite)
	child, err := buildChildSpec(payload, mechanics.marshalSpec)
	if err != nil {
		return MechanicsResult{}, err
	}
	rawSpec, err := child.canonical()
	if err != nil {
		return MechanicsResult{}, err
	}
	stdout := newBoundedCapture(MaxStdoutBytes)
	stderr := newBoundedCapture(MaxStderrBytes)
	command := exec.Command(mechanics.marshalSpec.CanonicalPath, "internal", "process-supervisor")
	command.Env = []string{}
	command.Stdin = bytes.NewReader(payload.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.ExtraFiles = append([]*os.File{specRead, readyWrite, releaseRead}, held...)
	command.ExtraFiles = append(command.ExtraFiles[:5], append([]*os.File{mechanics.marshalFile}, command.ExtraFiles[5:]...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	spawnCommitted := false
	var launchedProcess ProcessIdentity
	defer func() {
		if !spawnCommitted {
			abortStartedChild(command, launchedProcess, releaseWrite)
		}
	}()
	closeFiles(specRead, readyWrite, releaseRead)
	process, err := observeProcessIdentity(command.Process.Pid)
	if err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	launchedProcess = process
	if err := writeAll(specWrite, rawSpec); err != nil || specWrite.Close() != nil {
		return MechanicsResult{}, ErrIntervention
	}
	for index := range rawSpec {
		rawSpec[index] = 0
	}
	if err := waitReady(ctx, readyRead); err != nil || !guard.clean() || revalidateHeldSet(held, payload) != nil || verifyHeldObject(mechanics.marshalFile, mechanics.marshalSpec) != nil {
		return MechanicsResult{}, ErrIntervention
	}
	if count, err := releaseWrite.Write([]byte{childReleaseByte}); err != nil || count != 1 || releaseWrite.Close() != nil {
		return MechanicsResult{}, ErrIntervention
	}
	var status syscall.WaitStatus
	for {
		pid, waitErr := syscall.Wait4(command.Process.Pid, &status, syscall.WUNTRACED|syscall.WNOHANG, nil)
		if waitErr != nil {
			return MechanicsResult{}, ErrIntervention
		}
		if pid == command.Process.Pid {
			if !status.Stopped() || status.StopSignal() != syscall.SIGTRAP {
				return MechanicsResult{}, ErrIntervention
			}
			break
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return MechanicsResult{}, ErrIntervention
		case <-timer.C:
		}
	}
	path, err := processExecutablePath(command.Process.Pid)
	if err != nil || path != payload.Runtime.CanonicalPath {
		return MechanicsResult{}, ErrConflict
	}
	process, err = observeProcessIdentity(command.Process.Pid)
	if err != nil || !sameProcessBirth(process, launchedProcess) || !guard.clean() || revalidateHeldSet(held, payload) != nil {
		return MechanicsResult{}, ErrConflict
	}
	mechanics.command = command
	mechanics.process = process
	mechanics.runtimeSpec = payload.Runtime
	mechanics.workingSpec = payload.WorkingDirectory
	mechanics.heldFiles = held
	mechanics.heldSpecs = spawnObjects(payload)
	mechanics.guard = guard
	mechanics.stdout = stdout
	mechanics.stderr = stderr
	mechanics.stopped = true
	mechanics.lastReport = mechanics.report("exec-stopped", nil)
	owned, guardOwned = true, true
	spawnCommitted = true
	command.Args = nil
	command.Env = nil
	command.Stdin = nil
	return resultForReport("process-exec-stopped", mechanics.lastReport), nil
}

func (mechanics *darwinMechanics) Resume(ctx context.Context, payload ResumePayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	if mechanics.command == nil || !mechanics.stopped || mechanics.resumed || mechanics.terminal || mechanics.revalidateLocked() != nil || !validDigest(payload.ProcessStartedFactDigest) {
		return MechanicsResult{}, ErrConflict
	}
	if err := syscall.PtraceDetach(mechanics.process.PID); err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	mechanics.stopped = false
	mechanics.resumed = true
	mechanics.startWaitLocked()
	mechanics.lastReport = mechanics.report("running", nil)
	return resultForReport("process-resumed", mechanics.lastReport), nil
}

func (mechanics *darwinMechanics) Inspect(ctx context.Context, payload CleanupPayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if ctx.Err() != nil || mechanics.command == nil || validateCleanupPayload(payload) != nil {
		return MechanicsResult{}, ErrConflict
	}
	mechanics.refreshWaitLocked()
	if !mechanics.terminal && mechanics.revalidateLocked() != nil {
		if !mechanics.awaitWaitLocked(ctx, 50*time.Millisecond) {
			return MechanicsResult{}, ErrConflict
		}
	}
	state := "running"
	if mechanics.stopped {
		state = "exec-stopped"
	}
	if mechanics.terminal {
		state = "terminal"
	}
	mechanics.lastReport = mechanics.report(state, mechanics.waitResult)
	return resultForReport("process-inspected", mechanics.lastReport), nil
}

func (mechanics *darwinMechanics) Terminate(ctx context.Context, payload CleanupPayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if ctx.Err() != nil || mechanics.command == nil || validateCleanupPayload(payload) != nil {
		return MechanicsResult{}, ErrConflict
	}
	mechanics.refreshWaitLocked()
	if mechanics.terminal {
		mechanics.lastReport = mechanics.report("terminal", mechanics.waitResult)
		return resultForReport("process-already-terminal", mechanics.lastReport), nil
	}
	if mechanics.revalidateLocked() != nil {
		if !mechanics.awaitWaitLocked(ctx, 50*time.Millisecond) {
			return MechanicsResult{}, ErrConflict
		}
		mechanics.lastReport = mechanics.report("terminal", mechanics.waitResult)
		return resultForReport("process-already-terminal", mechanics.lastReport), nil
	}
	if mechanics.stopped {
		// A pre-resume exec-stopped runtime must never be detached: detach would
		// release the provider entrypoint before cleanup. Exact group SIGKILL is
		// the only supported abort at this barrier.
		if err := unix.Kill(-mechanics.process.ProcessGroupID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			return MechanicsResult{}, ErrIntervention
		}
		mechanics.stopped = false
		mechanics.startWaitLocked()
		for !mechanics.awaitWaitLocked(ctx, 20*time.Millisecond) {
			if ctx.Err() != nil {
				return MechanicsResult{}, ErrIntervention
			}
		}
		if err := groupAbsent(mechanics.process.ProcessGroupID); err != nil {
			return MechanicsResult{}, ErrIntervention
		}
		mechanics.lastReport = mechanics.report("terminal", mechanics.waitResult)
		return resultForReport("process-terminal", mechanics.lastReport), nil
	}
	if err := unix.Kill(-mechanics.process.ProcessGroupID, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
		return MechanicsResult{}, ErrIntervention
	}
	grace := time.NewTimer(2 * time.Second)
	defer grace.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !mechanics.terminal {
		mechanics.refreshWaitLocked()
		if mechanics.terminal {
			break
		}
		select {
		case <-ctx.Done():
			return MechanicsResult{}, ErrIntervention
		case <-ticker.C:
		case <-grace.C:
			if mechanics.revalidateLocked() != nil {
				if mechanics.awaitWaitLocked(ctx, 50*time.Millisecond) {
					break
				}
				return MechanicsResult{}, ErrIntervention
			}
			if err := unix.Kill(-mechanics.process.ProcessGroupID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
				return MechanicsResult{}, ErrIntervention
			}
			grace.Reset(2 * time.Second)
		}
	}
	if err := groupAbsent(mechanics.process.ProcessGroupID); err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	mechanics.lastReport = mechanics.report("terminal", mechanics.waitResult)
	return resultForReport("process-terminal", mechanics.lastReport), nil
}

func (mechanics *darwinMechanics) Collect(ctx context.Context, payload CollectPayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if ctx.Err() != nil || !validDigest(payload.ProcessStartedFactDigest) || !validDigest(payload.LastObservationDigest) || mechanics.command == nil || mechanics.collected {
		return MechanicsResult{}, ErrConflict
	}
	mechanics.refreshWaitLocked()
	if !mechanics.terminal {
		return MechanicsResult{}, ErrConflict
	}
	stdoutData, stdoutDigest, stdoutBytes, stdoutTruncated := mechanics.stdout.snapshot()
	stderrData, stderrDigest, stderrBytes, stderrTruncated := mechanics.stderr.snapshot()
	if stdoutBytes+stderrBytes > MaxTranscriptBytes {
		stdoutTruncated, stderrTruncated = true, true
	}
	if err := writeOwnerObject(mechanics.controlDirectory, "stdout.bin", stdoutData); err != nil || writeOwnerObject(mechanics.controlDirectory, "stderr.bin", stderrData) != nil {
		return MechanicsResult{}, ErrIntervention
	}
	report := mechanics.report("terminal", mechanics.waitResult)
	report.StdoutDigest, report.StderrDigest = stdoutDigest, stderrDigest
	report.StdoutBytes, report.StderrBytes = minUint64(stdoutBytes, MaxStdoutBytes), minUint64(stderrBytes, MaxStderrBytes)
	report.TranscriptTruncated = stdoutTruncated || stderrTruncated
	manifest := mustCanonical(report)
	if err := writeOwnerObject(mechanics.controlDirectory, "transcript.jcs", manifest); err != nil {
		return MechanicsResult{}, ErrIntervention
	}
	mechanics.transcriptHash = canonical.DigestBytes(manifest)
	mechanics.collected = true
	mechanics.lastReport = report
	result := resultForReport("transcript-collected", report)
	result.TranscriptDigest = mechanics.transcriptHash
	result.StdoutBytes, result.StderrBytes, result.Truncated = report.StdoutBytes, report.StderrBytes, report.TranscriptTruncated
	return result, nil
}

func (mechanics *darwinMechanics) Close(ctx context.Context, payload ClosePayload) (MechanicsResult, error) {
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if ctx.Err() != nil || !validDigest(payload.ProcessTerminalFactDigest) || !validDigest(payload.AllocationTerminatedDigest) || !validDigest(payload.CleanupBindingDigest) || !mechanics.terminal || !mechanics.collected || mechanics.closed {
		return MechanicsResult{}, ErrConflict
	}
	report := mechanics.lastReport
	closeFiles(mechanics.heldFiles...)
	mechanics.heldFiles = nil
	mechanics.heldSpecs = nil
	mechanics.guard.close()
	_ = mechanics.marshalFile.Close()
	mechanics.marshalFile = nil
	mechanics.closed = true
	return resultForReport("mechanics-closed", report), nil
}

func (mechanics *darwinMechanics) refreshWaitLocked() {
	if mechanics.terminal || mechanics.waited == nil {
		return
	}
	select {
	case result := <-mechanics.waited:
		mechanics.waitResult = &result
		mechanics.terminal = true
	default:
	}
}

func (mechanics *darwinMechanics) startWaitLocked() {
	if mechanics.waited != nil {
		return
	}
	mechanics.waited = make(chan waitOutcome, 1)
	go func(command *exec.Cmd, result chan<- waitOutcome) {
		_ = command.Wait()
		outcome := waitOutcome{}
		if state := command.ProcessState; state != nil {
			outcome.exitCode = state.ExitCode()
			if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				outcome.signal = status.Signal().String()
			}
		}
		result <- outcome
	}(mechanics.command, mechanics.waited)
}

func (mechanics *darwinMechanics) awaitWaitLocked(ctx context.Context, limit time.Duration) bool {
	mechanics.refreshWaitLocked()
	if mechanics.terminal {
		return true
	}
	if mechanics.waited == nil {
		return false
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case result := <-mechanics.waited:
		mechanics.waitResult = &result
		mechanics.terminal = true
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (mechanics *darwinMechanics) revalidateLocked() error {
	if mechanics.guard == nil || !mechanics.guard.clean() || verifyHeldObject(mechanics.marshalFile, mechanics.marshalSpec) != nil {
		return ErrConflict
	}
	if len(mechanics.heldFiles) != len(mechanics.heldSpecs) {
		return ErrConflict
	}
	for index, file := range mechanics.heldFiles {
		if file == nil || verifyLiveObject(file, mechanics.heldSpecs[index]) != nil {
			return ErrConflict
		}
	}
	observed, err := observeProcessIdentity(mechanics.process.PID)
	if err != nil || !sameProcessBirth(observed, mechanics.process) {
		return ErrConflict
	}
	path, err := processExecutablePath(mechanics.process.PID)
	if err != nil || path != mechanics.runtimeSpec.CanonicalPath {
		return ErrConflict
	}
	return nil
}

func (mechanics *darwinMechanics) report(state string, outcome *waitOutcome) processReport {
	runtimeDigest, _ := digestHeldSpec(mechanics.runtimeSpec)
	workingDigest, _ := digestHeldSpec(mechanics.workingSpec)
	report := processReport{State: state, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Process: mechanics.process, RuntimeObjectDigest: runtimeDigest, WorkingObjectDigest: workingDigest}
	if outcome != nil {
		report.ExitCode, report.Signal = outcome.exitCode, outcome.signal
	}
	return report
}

func resultForReport(reason string, report processReport) MechanicsResult {
	payload := mustCanonical(report)
	return MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: canonical.DigestBytes(payload), Payload: payload}
}

func buildChildSpec(payload SpawnPayload, marshal HeldObjectSpec) (childSpec, error) {
	spec := childSpec{ProtocolRevision: ProtocolRevision, ParentPID: os.Getpid(), Runtime: childObject{FD: int(childRuntimeFD), Object: payload.Runtime}, WorkingDirectory: childObject{FD: int(childCwdFD), Object: payload.WorkingDirectory}, Marshal: childObject{FD: int(childMarshalFD), Object: marshal}, Argv: append([]string(nil), payload.Argv...), Environment: append([]string(nil), payload.Environment...)}
	next := childClosureFD
	for _, object := range payload.MaterialRoots {
		spec.MaterialRoots = append(spec.MaterialRoots, childObject{FD: next, Object: object})
		next++
	}
	for _, object := range payload.LaunchMaterials {
		spec.LaunchMaterials = append(spec.LaunchMaterials, childObject{FD: next, Object: object})
		next++
	}
	return spec, spec.validate()
}

func openSpawnObjects(payload SpawnPayload) ([]*os.File, error) {
	objects := spawnObjects(payload)
	files := make([]*os.File, 0, len(objects))
	for _, object := range objects {
		file, err := openHeldObject(object)
		if err != nil {
			closeFiles(files...)
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func revalidateHeldSet(files []*os.File, payload SpawnPayload) error {
	objects := spawnObjects(payload)
	if len(files) != len(objects) {
		return ErrConflict
	}
	for index, file := range files {
		if verifyHeldObject(file, objects[index]) != nil || verifyPathObject(objects[index]) != nil {
			return ErrConflict
		}
	}
	return nil
}

func spawnObjects(payload SpawnPayload) []HeldObjectSpec {
	objects := append([]HeldObjectSpec{payload.WorkingDirectory, payload.Runtime}, payload.MaterialRoots...)
	return append(objects, payload.LaunchMaterials...)
}

func openObservedSpec(role, path, kind string) (*os.File, HeldObjectSpec, error) {
	if !absoluteClean(path) {
		return nil, HeldObjectSpec{}, ErrConflict
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW_ANY
	if kind == "directory" {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, HeldObjectSpec{}, ErrConflict
	}
	file := os.NewFile(uintptr(fd), "marshal-held-object")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		_ = file.Close()
		return nil, HeldObjectSpec{}, ErrConflict
	}
	spec := HeldObjectSpec{Role: role, CanonicalPath: path, Device: uint64(stat.Dev), Inode: stat.Ino, FileType: kind, UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink), Size: stat.Size}
	if kind == "regular" {
		spec.RawSHA256, err = digestOpenFile(file)
		var after unix.Stat_t
		if err != nil || unix.Fstat(fd, &after) != nil || !sameStableFileStat(stat, after) {
			_ = file.Close()
			return nil, HeldObjectSpec{}, ErrConflict
		}
	}
	return file, spec, nil
}

func openHeldObject(spec HeldObjectSpec) (*os.File, error) {
	file, observed, err := openObservedSpec(spec.Role, spec.CanonicalPath, spec.FileType)
	if err != nil || observed != spec {
		if file != nil {
			_ = file.Close()
		}
		return nil, ErrConflict
	}
	return file, nil
}

func verifyHeldObject(file *os.File, spec HeldObjectSpec) error {
	if file == nil {
		return ErrConflict
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || uint64(stat.Dev) != spec.Device || stat.Ino != spec.Inode || uint32(stat.Mode) != spec.Mode || stat.Uid != spec.UID || stat.Gid != spec.GID || uint64(stat.Nlink) != spec.LinkCount || stat.Size != spec.Size {
		return ErrConflict
	}
	if spec.FileType == "regular" {
		digest, err := digestOpenFile(file)
		var after unix.Stat_t
		if err != nil || unix.Fstat(int(file.Fd()), &after) != nil || !sameStableFileStat(stat, after) || digest != spec.RawSHA256 {
			return ErrConflict
		}
	}
	return nil
}

func sameStableFileStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func verifyPathObject(spec HeldObjectSpec) error {
	file, err := openHeldObject(spec)
	if err != nil {
		return err
	}
	return file.Close()
}

func verifyLiveObject(file *os.File, spec HeldObjectSpec) error {
	if spec.Role != "working-directory" {
		if verifyHeldObject(file, spec) != nil {
			return ErrConflict
		}
		return verifyPathObject(spec)
	}
	if verifyWorkingDirectory(file, spec) != nil {
		return ErrConflict
	}
	pathFile, observed, err := openObservedSpec(spec.Role, spec.CanonicalPath, spec.FileType)
	if err != nil {
		return ErrConflict
	}
	defer pathFile.Close()
	if observed.CanonicalPath != spec.CanonicalPath || observed.Device != spec.Device || observed.Inode != spec.Inode || observed.FileType != spec.FileType || observed.UID != spec.UID || observed.GID != spec.GID || observed.Mode != spec.Mode {
		return ErrConflict
	}
	return nil
}

func verifyWorkingDirectory(file *os.File, spec HeldObjectSpec) error {
	if file == nil || spec.Role != "working-directory" || spec.FileType != "directory" {
		return ErrConflict
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || uint64(stat.Dev) != spec.Device || stat.Ino != spec.Inode || uint32(stat.Mode) != spec.Mode || stat.Uid != spec.UID || stat.Gid != spec.GID {
		return ErrConflict
	}
	return nil
}

func digestOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func digestHeldSpec(spec HeldObjectSpec) (string, error) {
	copy := spec
	copy.CanonicalPath = ""
	return digestValue(copy)
}

func observeProcessIdentity(pid int) (ProcessIdentity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || int(info.Eproc.Pgid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return ProcessIdentity{}, ErrConflict
	}
	sid, err := unix.Getsid(pid)
	if err != nil || sid != mustSessionID() {
		return ProcessIdentity{}, ErrConflict
	}
	return ProcessIdentity{PID: pid, BirthSeconds: info.Proc.P_starttime.Sec, BirthMicroseconds: int64(info.Proc.P_starttime.Usec), SessionID: sid, ProcessGroupID: int(info.Eproc.Pgid)}, nil
}

func sameProcessBirth(left, right ProcessIdentity) bool { return left == right }

func mustSessionID() int {
	sid, _ := unix.Getsid(0)
	return sid
}

func processExecutablePath(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 5 {
		return "", ErrConflict
	}
	_ = binary.LittleEndian.Uint32(raw[:4])
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", ErrConflict
	}
	path := string(rest[:end])
	if !absoluteClean(path) {
		return "", ErrConflict
	}
	return path, nil
}

func waitReady(ctx context.Context, file *os.File) error {
	result := make(chan error, 1)
	go func() {
		var ready [2]byte
		count, err := io.ReadFull(file, ready[:1])
		if err != nil || count != 1 || ready[0] != childReadyByte {
			result <- ErrConflict
			return
		}
		count, err = file.Read(ready[1:])
		if err != io.EOF || count != 0 {
			result <- ErrConflict
			return
		}
		result <- nil
	}()
	select {
	case <-ctx.Done():
		return ErrIntervention
	case err := <-result:
		return err
	}
}

func writeOwnerObject(directory *os.File, name string, data []byte) error {
	if directory == nil || !validID(name) || len(data) > MaxTranscriptBytes {
		return ErrInvalid
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return ErrIntervention
	}
	file := os.NewFile(uintptr(fd), "marshal-supervisor-output")
	defer file.Close()
	if validateJournalFile(file) != nil || writeAll(file, data) != nil || file.Sync() != nil || validateJournalFile(file) != nil {
		return ErrIntervention
	}
	return directory.Sync()
}

func groupAbsent(pgid int) error {
	err := unix.Kill(-pgid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return ErrIntervention
}

func abortStartedChild(command *exec.Cmd, process ProcessIdentity, release *os.File) {
	if release != nil {
		_ = release.Close()
	}
	if command == nil || command.Process == nil {
		return
	}
	if process.PID > 1 {
		if observed, err := observeProcessIdentity(process.PID); err == nil && sameProcessBirth(observed, process) {
			_ = unix.Kill(-process.ProcessGroupID, unix.SIGKILL)
		}
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func minUint64(value uint64, limit int) uint64 {
	if value > uint64(limit) {
		return uint64(limit)
	}
	return value
}

func absoluteClean(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
