//go:build darwin

package processcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/sandboxlaunch"
	"golang.org/x/sys/unix"
)

const (
	processObserver         = "darwin-kqueue-v1"
	postKillObservationTime = 5 * time.Second
	darwinProcessZombie     = int8(5)
)

type darwinSystem interface {
	validateFixed(string) (ObjectObservation, error)
	start(context.Context, string, ObjectObservation, LaunchRequest) (darwinUnit, error)
	reconcile(ProcessObservation) (ProcessState, error)
}

type darwinUnit interface {
	awaitReady(context.Context) error
	release() error
	abort()
	inspect() (ProcessState, error)
	signal(syscall.Signal) (ProcessState, error)
	result() (int, string, error)
	close() error
	observation() ProcessObservation
	observedAt() time.Time
}

type darwinCoordinator struct {
	authority        AttemptAuthority
	fixedMarshalPath string
	fixedMarshal     ObjectObservation
	system           darwinSystem
}

func newPlatformCoordinator(authority AttemptAuthority, fixedMarshalPath string) (platformCoordinator, error) {
	return newDarwinCoordinator(authority, fixedMarshalPath, realDarwinSystem{})
}

func newDarwinCoordinator(authority AttemptAuthority, fixedMarshalPath string, system darwinSystem) (*darwinCoordinator, error) {
	if authority == nil || system == nil || !absoluteClean(fixedMarshalPath) {
		return nil, ErrAuthority
	}
	fixedMarshal, err := system.validateFixed(fixedMarshalPath)
	if err != nil {
		return nil, ErrIdentityConflict
	}
	return &darwinCoordinator{authority: authority, fixedMarshalPath: fixedMarshalPath, fixedMarshal: fixedMarshal, system: system}, nil
}

func (coordinator *darwinCoordinator) launch(ctx context.Context, request LaunchRequest) (platformProcess, error) {
	if err := validateLaunchRequest(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	launchAuthorization, err := coordinator.authority.AuthorizeLaunch(ctx, LaunchAuthorityRequest{
		Authority:        request.Authority,
		ExpectedRevision: request.ExpectedRevision,
		ExpectedHead:     request.ExpectedHead,
		LaunchID:         request.LaunchID,
		Closure:          request.Closure,
	})
	if err != nil {
		return nil, launchUncertain(fmt.Errorf("%w: launch authorization", ErrAuthority))
	}
	if !launchAuthorization.Appended {
		return nil, ErrLaunchUncertain
	}
	if err := validateFreshAppend(launchAuthorization, request.ExpectedRevision); err != nil {
		return nil, launchUncertain(fmt.Errorf("%w: incomplete launch transition", ErrAuthority))
	}
	if err := ctx.Err(); err != nil {
		return nil, launchUncertain(err)
	}

	unit, err := coordinator.system.start(ctx, coordinator.fixedMarshalPath, coordinator.fixedMarshal, request)
	if err != nil {
		return nil, launchUncertain(err)
	}
	released := false
	defer func() {
		if !released {
			unit.abort()
		}
	}()
	if err := unit.awaitReady(ctx); err != nil {
		return nil, launchUncertain(err)
	}
	observation := unit.observation()
	observedAt := unit.observedAt().UTC()
	if err := validateObservedAt(observation, observedAt); err != nil {
		return nil, launchUncertain(err)
	}
	started, err := coordinator.authority.RecordProcessStarted(ctx, ProcessStartedAuthorityRequest{
		Authority:             request.Authority,
		ExpectedRevision:      launchAuthorization.Revision,
		ExpectedHead:          launchAuthorization.HeadDigest,
		LaunchTransition:      launchAuthorization.TransitionDigest,
		CommandID:             request.CommandID,
		ObservedAt:            observedAt.Format(time.RFC3339Nano),
		Observation:           observation,
		LaunchMaterialsDigest: request.Closure.LaunchMaterialsDigest,
		AgentLaunchSpecDigest: request.Closure.AgentLaunchSpecDigest,
	})
	if err != nil || !started.Appended {
		return nil, launchUncertain(fmt.Errorf("%w: process-started transition", ErrAuthority))
	}
	if err := validateFreshAppend(started, launchAuthorization.Revision); err != nil {
		return nil, launchUncertain(fmt.Errorf("%w: incomplete process-started transition", ErrAuthority))
	}
	if err := unit.release(); err != nil {
		return nil, launchUncertain(err)
	}
	released = true
	return &darwinProcess{authority: coordinator.authority, ref: request.Authority, unit: unit, observed: observation}, nil
}

func launchUncertain(cause error) error {
	if cause == nil {
		return ErrLaunchUncertain
	}
	return errors.Join(ErrLaunchUncertain, cause)
}

func (coordinator *darwinCoordinator) reconcile(ctx context.Context, ref AuthorityRef, observation ProcessObservation) (Inspection, error) {
	if err := ref.validate(); err != nil {
		return Inspection{}, err
	}
	sealed, err := observation.sealed()
	if err != nil || sealed.ObservationDigest != observation.ObservationDigest || observation.ObserverIdentity != processObserver {
		return Inspection{State: ProcessIdentityConflict, Observation: observation}, ErrIdentityConflict
	}
	var state ProcessState
	var inspectErr error
	if err := coordinator.withAuthority(ctx, ref, OperationReconcile, observation.ObservationDigest, func() error {
		state, inspectErr = coordinator.system.reconcile(observation)
		return inspectErr
	}); err != nil {
		return Inspection{}, err
	}
	if inspectErr != nil {
		return Inspection{State: ProcessIdentityConflict, Observation: observation}, inspectErr
	}
	if state != ProcessAbsent {
		if state == ProcessLive {
			state = ProcessLaunchUncertain
		}
		return Inspection{State: state, Observation: observation}, stateError(state)
	}
	if err := coordinator.withAuthority(ctx, ref, OperationTerminalFact, observation.ObservationDigest, func() error {
		state, inspectErr = coordinator.system.reconcile(observation)
		return inspectErr
	}); err != nil {
		return Inspection{}, err
	}
	if inspectErr != nil || state != ProcessAbsent {
		return Inspection{State: ProcessIdentityConflict, Observation: observation}, ErrIdentityConflict
	}
	return Inspection{State: ProcessAbsent, Observation: observation}, nil
}

func (coordinator *darwinCoordinator) withAuthority(ctx context.Context, ref AuthorityRef, operation ControlOperation, digest string, effect func() error) error {
	return guardedAuthorityEffect(operation, func(callback func() error) error {
		return coordinator.authority.WithCurrentAuthority(ctx, ControlAuthorization{Authority: ref, Operation: operation, ObservationDigest: digest}, callback)
	}, effect)
}

type darwinProcess struct {
	authority AttemptAuthority
	ref       AuthorityRef
	unit      darwinUnit
	observed  ProcessObservation
	closed    bool
	terminal  bool
	mu        sync.Mutex
}

func (process *darwinProcess) observation() ProcessObservation { return process.observed }

func (process *darwinProcess) inspect(ctx context.Context) (Inspection, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.inspectLocked(ctx, true)
}

func (process *darwinProcess) inspectLocked(ctx context.Context, terminalFact bool) (Inspection, error) {
	if process.closed {
		return Inspection{}, ErrClosed
	}
	var state ProcessState
	var inspectErr error
	if err := process.withAuthority(ctx, OperationInspect, func() error {
		state, inspectErr = process.unit.inspect()
		return inspectErr
	}); err != nil {
		return Inspection{}, err
	}
	if inspectErr != nil {
		return Inspection{State: ProcessIdentityConflict, Observation: process.observed}, inspectErr
	}
	if state == ProcessAbsent && terminalFact {
		if err := process.withAuthority(ctx, OperationTerminalFact, func() error {
			state, inspectErr = process.unit.inspect()
			return inspectErr
		}); err != nil {
			return Inspection{}, err
		}
		if inspectErr != nil || state != ProcessAbsent {
			return Inspection{State: ProcessIdentityConflict, Observation: process.observed}, ErrIdentityConflict
		}
		process.terminal = true
		exitCode, signal, resultErr := process.unit.result()
		if resultErr != nil {
			return Inspection{State: ProcessIdentityConflict, Observation: process.observed}, resultErr
		}
		return Inspection{State: state, Observation: process.observed, ExitKnown: true, ExitCode: exitCode, Signal: signal}, nil
	} else if state == ProcessLive {
		process.terminal = false
	}
	return Inspection{State: state, Observation: process.observed}, stateError(state)
}

func (process *darwinProcess) wait(ctx context.Context) (Inspection, error) {
	for {
		inspection, err := process.inspect(ctx)
		if inspection.State != ProcessLive || err != nil {
			return inspection, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Inspection{State: ProcessLive, Observation: process.observed}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (process *darwinProcess) terminate(ctx context.Context, grace time.Duration) (Inspection, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	inspection, err := process.inspectLocked(ctx, true)
	if inspection.State != ProcessLive || err != nil {
		return inspection, err
	}
	var state ProcessState
	var signalErr error
	if err := process.withAuthority(ctx, OperationSignalTERM, func() error {
		state, signalErr = process.unit.signal(syscall.SIGTERM)
		return signalErr
	}); err != nil {
		return Inspection{State: state, Observation: process.observed}, err
	}
	if signalErr != nil {
		return Inspection{State: state, Observation: process.observed}, signalErr
	}
	if inspection, done, waitErr := process.waitBoundedLocked(ctx, grace); done {
		if waitErr != nil {
			return inspection, waitErr
		}
		return inspection, stateError(inspection.State)
	}
	if err := process.withAuthority(ctx, OperationSignalKILL, func() error {
		state, signalErr = process.unit.signal(syscall.SIGKILL)
		return signalErr
	}); err != nil {
		return Inspection{State: state, Observation: process.observed}, err
	}
	if signalErr != nil {
		return Inspection{State: state, Observation: process.observed}, signalErr
	}
	inspection, done, waitErr := process.waitBoundedLocked(ctx, postKillObservationTime)
	if waitErr != nil {
		return inspection, waitErr
	}
	if !done {
		return Inspection{State: ProcessLive, Observation: process.observed}, ErrStillRunning
	}
	return inspection, stateError(inspection.State)
}

func (process *darwinProcess) waitBoundedLocked(ctx context.Context, duration time.Duration) (Inspection, bool, error) {
	deadline := time.Now().Add(duration)
	for {
		inspection, err := process.inspectLocked(ctx, true)
		if inspection.State != ProcessLive || err != nil {
			return inspection, true, err
		}
		if !time.Now().Before(deadline) {
			return inspection, false, nil
		}
		remaining := time.Until(deadline)
		if remaining > 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Inspection{State: ProcessLive, Observation: process.observed}, true, ctx.Err()
		case <-timer.C:
		}
	}
}

func (process *darwinProcess) close() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return ErrClosed
	}
	if !process.terminal {
		return ErrStillRunning
	}
	if err := process.unit.close(); err != nil {
		return err
	}
	process.closed = true
	return nil
}

func (process *darwinProcess) withAuthority(ctx context.Context, operation ControlOperation, effect func() error) error {
	return guardedAuthorityEffect(operation, func(callback func() error) error {
		return process.authority.WithCurrentAuthority(ctx, ControlAuthorization{Authority: process.ref, Operation: operation, ObservationDigest: process.observed.ObservationDigest}, callback)
	}, effect)
}

type authorizedEffectError struct{ err error }

func (failure *authorizedEffectError) Error() string { return failure.err.Error() }
func (failure *authorizedEffectError) Unwrap() error { return failure.err }

func guardedAuthorityEffect(operation ControlOperation, invoke func(func() error) error, effect func() error) error {
	var mu sync.Mutex
	calls := 0
	closed := false
	completed := false
	violated := false
	var effectErr error
	var carrier *authorizedEffectError
	callback := func() error {
		mu.Lock()
		if closed || calls != 0 {
			violated = true
			mu.Unlock()
			return ErrAuthority
		}
		calls++
		mu.Unlock()

		result := effect()
		mu.Lock()
		effectErr = result
		completed = true
		if result != nil {
			carrier = &authorizedEffectError{err: result}
		}
		currentCarrier := carrier
		mu.Unlock()
		if currentCarrier != nil {
			return currentCarrier
		}
		return nil
	}

	verifierErr := invoke(callback)
	mu.Lock()
	closed = true
	callCount, callbackCompleted, callbackViolated := calls, completed, violated
	resultErr, resultCarrier := effectErr, carrier
	mu.Unlock()
	// Only the exact private carrier returned by callback is an effect failure.
	// A verifier that replaces or combines it with its own error has denied the
	// authority check, which always takes precedence.
	propagatedEffect := verifierErr != nil && resultCarrier != nil && verifierErr == resultCarrier
	if verifierErr != nil && !propagatedEffect {
		return fmt.Errorf("%w: %s verifier", ErrAuthority, operation)
	}
	if callCount != 1 || !callbackCompleted || callbackViolated {
		return fmt.Errorf("%w: %s callback contract", ErrAuthority, operation)
	}
	if propagatedEffect {
		return resultErr
	}
	return resultErr
}

func validateLaunchRequest(request LaunchRequest) error {
	if err := request.Authority.validate(); err != nil {
		return err
	}
	if request.ExpectedRevision == 0 || !validSHA256(request.ExpectedHead) || request.LaunchID == "" || request.CommandID == "" || len(request.Arguments) == 0 {
		return ErrAuthority
	}
	if !absoluteClean(request.WorkingDirectory) || !absoluteClean(request.ExecutablePath) || request.Arguments[0] != request.ExecutablePath || !validSHA256(request.ExpectedExecutableSHA256) {
		return ErrIdentityConflict
	}
	if len(request.Materials) != 0 {
		return ErrUnsupported
	}
	if err := request.Closure.Validate(); err != nil || request.ExecutablePath != request.Closure.RuntimeExecutable.CanonicalPath || request.ExpectedExecutableSHA256 != request.Closure.RuntimeExecutable.RawSHA256 {
		return ErrUnsupported
	}
	digest, err := launchidentity.DigestSpec(launchidentity.SpecInput{RuntimeExecutable: request.Closure.RuntimeExecutable, ClosureProfileID: request.Closure.ClosureProfileID, MaterialRoots: request.Closure.MaterialRoots, LaunchMaterials: request.Closure.LaunchMaterials, Arguments: request.Arguments, Environment: request.Environment, WorkingDirectory: request.WorkingDirectory})
	if err != nil || digest != request.Closure.AgentLaunchSpecDigest {
		return ErrIdentityConflict
	}
	if err := sandboxlaunch.ValidatePayload(request.Arguments, request.Environment); err != nil {
		return ErrIdentityConflict
	}
	if err := sandboxlaunch.ValidateLaunchID(request.LaunchID); err != nil {
		return ErrAuthority
	}
	return nil
}

func stateError(state ProcessState) error {
	switch state {
	case ProcessAbsent, ProcessLive:
		return nil
	case ProcessLaunchUncertain:
		return ErrLaunchUncertain
	default:
		return ErrIdentityConflict
	}
}

type realDarwinSystem struct{}

func (realDarwinSystem) validateFixed(path string) (ObjectObservation, error) {
	file, observation, err := openObserved(path, unix.S_IFREG, true)
	if err != nil {
		return ObjectObservation{}, err
	}
	if err := file.Close(); err != nil {
		return ObjectObservation{}, err
	}
	return observation, nil
}

func (realDarwinSystem) start(ctx context.Context, fixedMarshalPath string, frozenMarshal ObjectObservation, request LaunchRequest) (darwinUnit, error) {
	_ = ctx
	heldClosure, err := launchidentity.Reopen(request.Closure)
	if err != nil {
		return nil, ErrIdentityConflict
	}
	closureOwned := false
	defer func() {
		if !closureOwned {
			heldClosure.Close()
		}
	}()
	workingDirectory, workingObservation, err := openObserved(request.WorkingDirectory, unix.S_IFDIR, false)
	if err != nil {
		return nil, ErrIdentityConflict
	}
	executable, executableObservation, err := openObserved(request.ExecutablePath, unix.S_IFREG, true)
	if err != nil {
		_ = workingDirectory.Close()
		return nil, ErrIdentityConflict
	}
	if executableObservation.SHA256 != request.ExpectedExecutableSHA256 {
		closeFiles(workingDirectory, executable)
		return nil, ErrIdentityConflict
	}
	marshalImage, marshalObservation, err := openObserved(fixedMarshalPath, unix.S_IFREG, true)
	if err != nil {
		_ = workingDirectory.Close()
		_ = executable.Close()
		return nil, ErrIdentityConflict
	}
	if !sameObservedObject(marshalObservation, frozenMarshal, true) {
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, ErrIdentityConflict
	}
	watches := []vnodeWatch{
		vnodeWatch{file: workingDirectory, contentSensitive: false},
		vnodeWatch{file: executable, contentSensitive: true},
		vnodeWatch{file: marshalImage, contentSensitive: true},
	}
	watches = append(watches, vnodeWatch{file: heldClosure.Runtime, contentSensitive: true})
	for _, file := range heldClosure.Roots {
		// Directory entry changes alter the closed profile even when every
		// already-enumerated material remains unchanged.
		watches = append(watches, vnodeWatch{file: file, contentSensitive: true})
	}
	for _, file := range heldClosure.Materials {
		watches = append(watches, vnodeWatch{file: file, contentSensitive: true})
	}
	guard, err := newVnodeGuard(watches...)
	if err != nil {
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, ErrIdentityConflict
	}

	specRead, specWrite, err := os.Pipe()
	if err != nil {
		guard.close()
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, err
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		closeFiles(specRead, specWrite)
		guard.close()
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, err
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		closeFiles(specRead, specWrite, readyRead, readyWrite)
		guard.close()
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, err
	}

	rootBindings := make([]sandboxlaunch.RootBinding, 0, len(request.Closure.MaterialRoots))
	materialBindings := make([]sandboxlaunch.MaterialBinding, 0, len(request.Closure.LaunchMaterials))
	nextFD := sandboxlaunch.MaterialFDBase
	for _, root := range request.Closure.MaterialRoots {
		rootBindings = append(rootBindings, sandboxlaunch.RootBinding{Name: root.Name, Path: root.CanonicalPath, FD: nextFD, Object: identityBinding(root.Object)})
		nextFD++
	}
	for _, material := range request.Closure.LaunchMaterials {
		materialBindings = append(materialBindings, sandboxlaunch.MaterialBinding{Role: material.Role, Path: material.Object.CanonicalPath, FD: nextFD, Object: identityBinding(material.Object)})
		nextFD++
	}
	spec := sandboxlaunch.Spec{
		ProtocolRevision: sandboxlaunch.ProtocolRevision,
		LaunchID:         request.LaunchID,
		ParentPID:        os.Getpid(),
		Arguments:        append([]string(nil), request.Arguments...),
		Environment:      append([]string(nil), request.Environment...),
		ExecutablePath:   request.ExecutablePath,
		Roots:            rootBindings,
		Materials:        materialBindings,
		SpecPipe:         pipeBinding(specRead),
		ReadyPipe:        pipeBinding(readyWrite),
		ReleasePipe:      pipeBinding(releaseRead),
		WorkingDirectory: launchBinding(workingObservation),
		Executable:       launchBinding(executableObservation),
		Marshal:          launchBinding(marshalObservation),
	}
	rawSpec, err := spec.Canonical()
	if err != nil {
		closeFiles(specRead, specWrite, readyRead, readyWrite, releaseRead, releaseWrite)
		guard.close()
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, ErrIdentityConflict
	}

	// Cancellation must never invoke os.Process.Kill outside current Attempt
	// authority. Process lifetime is controlled exclusively by darwinProcess.
	command := exec.Command(fixedMarshalPath, "__sandbox-launch")
	command.Env = []string{}
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	command.ExtraFiles = []*os.File{specRead, readyWrite, releaseRead, workingDirectory, executable, marshalImage}
	command.ExtraFiles = append(command.ExtraFiles, heldClosure.Roots...)
	command.ExtraFiles = append(command.ExtraFiles, heldClosure.Materials...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		closeFiles(specRead, specWrite, readyRead, readyWrite, releaseRead, releaseWrite)
		guard.close()
		closeFiles(workingDirectory, executable, marshalImage)
		return nil, err
	}
	closeFiles(specRead, readyWrite, releaseRead)
	waitForPreWorkloadExit := func(processQueue int) bool {
		_ = specWrite.Close()
		_ = releaseWrite.Close()
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		cleanup := func() {
			if processQueue >= 0 {
				_ = unix.Close(processQueue)
			}
			closeFiles(readyRead, workingDirectory, executable, marshalImage)
			guard.close()
		}
		return boundedOwnedWait(done, time.After(time.Second), cleanup)
	}

	pid := command.Process.Pid
	processQueue, err := newProcessQueue(pid)
	if err != nil {
		if !waitForPreWorkloadExit(-1) {
			return nil, ErrLaunchUncertain
		}
		return nil, ErrIdentityConflict
	}
	observation, sid, observedAt, err := observeProcess(pid, workingObservation, executableObservation)
	if err != nil {
		if !waitForPreWorkloadExit(processQueue) {
			return nil, ErrLaunchUncertain
		}
		return nil, ErrIdentityConflict
	}

	unit := &realDarwinUnit{
		command:               command,
		readyRead:             readyRead,
		releaseWrite:          releaseWrite,
		workingDirectory:      workingDirectory,
		executable:            executable,
		marshalImage:          marshalImage,
		workingObservation:    workingObservation,
		executableObservation: executableObservation,
		marshalObservation:    marshalObservation,
		guard:                 guard,
		processQueue:          processQueue,
		observed:              observation,
		sid:                   sid,
		processObservedAt:     observedAt,
		heldClosure:           heldClosure,
	}
	closureOwned = true
	if err := writeAll(specWrite, rawSpec); err != nil {
		_ = specWrite.Close()
		unit.abort()
		return nil, err
	}
	if err := specWrite.Close(); err != nil {
		unit.abort()
		return nil, err
	}
	return unit, nil
}

func (realDarwinSystem) reconcile(observation ProcessObservation) (ProcessState, error) {
	if err := revalidatePersistedObservation(observation); err != nil {
		return ProcessIdentityConflict, err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", observation.PID)
	var groupErr error
	if errors.Is(err, unix.ESRCH) || info == nil {
		groupErr = unix.Kill(-observation.PGID, 0)
	}
	return classifyProcess(observation, info, 0, 0, err, groupErr)
}

func revalidatePersistedObservation(observation ProcessObservation) error {
	working, currentWorking, err := openObserved(observation.WorkingDirectory, unix.S_IFDIR, false)
	if err != nil {
		return ErrIdentityConflict
	}
	_ = working.Close()
	if !samePersistedWorkingDirectory(currentWorking, observation) {
		return ErrIdentityConflict
	}
	executable, currentExecutable, err := openObserved(observation.ExecutablePath, unix.S_IFREG, true)
	if err != nil {
		return ErrIdentityConflict
	}
	_ = executable.Close()
	if !samePersistedExecutable(currentExecutable, observation) {
		return ErrIdentityConflict
	}
	return nil
}

func samePersistedWorkingDirectory(current ObjectObservation, observation ProcessObservation) bool {
	return current.Path == observation.WorkingDirectory && current.Device == observation.WorkingDirectoryDevice && current.Inode == observation.WorkingDirectoryInode &&
		current.Mode&unix.S_IFMT == observation.WorkingDirectoryType && current.UID == observation.WorkingDirectoryOwner && current.Mode == observation.WorkingDirectoryMode
}

func samePersistedExecutable(current ObjectObservation, observation ProcessObservation) bool {
	return current.Path == observation.ExecutablePath && current.Device == observation.ExecutableDevice && current.Inode == observation.ExecutableInode && current.Size == observation.ExecutableSize &&
		current.Mode&unix.S_IFMT == observation.ExecutableType && current.UID == observation.ExecutableOwner && current.GID == observation.ExecutableGroup && current.Mode == observation.ExecutableMode &&
		current.Nlink == observation.ExecutableLinkCount && current.SHA256 == observation.ExecutableSHA256
}

type realDarwinUnit struct {
	command               *exec.Cmd
	readyRead             *os.File
	releaseWrite          *os.File
	workingDirectory      *os.File
	executable            *os.File
	marshalImage          *os.File
	workingObservation    ObjectObservation
	executableObservation ObjectObservation
	marshalObservation    ObjectObservation
	guard                 *vnodeGuard
	processQueue          int
	observed              ProcessObservation
	sid                   int
	processObservedAt     time.Time
	heldClosure           *launchidentity.HeldClosure
	trackedDescendants    map[int]descendantObservation
	released              bool
	leaderExited          bool
	waited                bool
	closed                bool
	mu                    sync.Mutex
}

func (unit *realDarwinUnit) observation() ProcessObservation { return unit.observed }
func (unit *realDarwinUnit) observedAt() time.Time           { return unit.processObservedAt }

func (unit *realDarwinUnit) awaitReady(ctx context.Context) error {
	result := make(chan error, 1)
	go func() {
		var ready [2]byte
		count, err := io.ReadFull(unit.readyRead, ready[:1])
		if err != nil || count != 1 || ready[0] != sandboxlaunch.ReadyByte {
			result <- ErrIdentityConflict
			return
		}
		count, err = unit.readyRead.Read(ready[1:])
		if err != io.EOF || count != 0 {
			result <- ErrIdentityConflict
			return
		}
		result <- nil
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if err != nil {
			return err
		}
	}
	if err := unit.revalidate(); err != nil {
		return err
	}
	count, err := unit.releaseWrite.Write([]byte{sandboxlaunch.ReleaseByte})
	if count != 1 || err != nil {
		return ErrIdentityConflict
	}
	_ = unit.releaseWrite.Close()
	var status syscall.WaitStatus
	waited := make(chan error, 1)
	go func() {
		_, waitErr := syscall.Wait4(unit.command.Process.Pid, &status, syscall.WUNTRACED, nil)
		waited <- waitErr
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case waitErr := <-waited:
		if waitErr != nil || !status.Stopped() || status.StopSignal() != syscall.SIGTRAP {
			return ErrIdentityConflict
		}
	}
	path, err := processExecutablePath(unit.command.Process.Pid)
	if err != nil || path != unit.executableObservation.Path {
		return ErrIdentityConflict
	}
	observation, sid, observedAt, err := observeProcess(unit.command.Process.Pid, unit.workingObservation, unit.executableObservation)
	if err != nil || sid != unit.sid {
		return ErrIdentityConflict
	}
	unit.observed, unit.processObservedAt = observation, observedAt
	return unit.revalidate()
}

func (unit *realDarwinUnit) release() error {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed || unit.released {
		return ErrClosed
	}
	if err := unit.revalidate(); err != nil {
		return err
	}
	if err := syscall.PtraceDetach(unit.observed.PID); err != nil {
		return ErrIdentityConflict
	}
	unit.released = true
	return nil
}

func (unit *realDarwinUnit) abort() {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed {
		return
	}
	// Before process-started CAS the exact child is either blocked on G or held
	// at the kernel exec-stop. The live coordinator owns its Process handle, so
	// it may kill+wait without inventing recovery authority.
	_ = unit.releaseWrite.Close()
	_ = unix.Kill(unit.command.Process.Pid, unix.SIGKILL)
	done := make(chan struct{})
	go func() {
		_ = unit.command.Wait()
		close(done)
	}()
	select {
	case <-done:
		unit.waited = true
		_ = unit.closeLocked()
	case <-time.After(time.Second):
		// Retain the wait right and held guards in the waiting goroutine rather
		// than inventing kill authority. The fixed helper has no workload release
		// and RB3 durable recovery owns any later uncertain-launch reconciliation.
		go func() {
			<-done
			unit.mu.Lock()
			unit.waited = true
			_ = unit.closeLocked()
			unit.mu.Unlock()
		}()
	}
}

func (unit *realDarwinUnit) inspect() (ProcessState, error) {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed {
		return ProcessIdentityConflict, ErrClosed
	}
	return unit.inspectLocked()
}

func (unit *realDarwinUnit) inspectLocked() (ProcessState, error) {
	if err := unit.revalidate(); err != nil {
		return ProcessIdentityConflict, err
	}
	if err := unit.refreshDescendantsLocked(); err != nil {
		return ProcessIdentityConflict, err
	}
	exited, err := unit.processExitedLocked()
	if err != nil {
		return ProcessIdentityConflict, err
	}
	if exited {
		return groupDescendantState(unit.observed, unit.sid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", unit.observed.PID)
	observedSID := 0
	if err == nil && info != nil {
		observedSID, err = unix.Getsid(unit.observed.PID)
		if err != nil {
			return ProcessIdentityConflict, err
		}
	}
	var groupErr error
	if errors.Is(err, unix.ESRCH) || info == nil {
		groupErr = unix.Kill(-unit.observed.PGID, 0)
	}
	return classifyProcess(unit.observed, info, unit.sid, observedSID, err, groupErr)
}

func classifyProcess(observation ProcessObservation, info *unix.KinfoProc, expectedSID, observedSID int, processErr, groupErr error) (ProcessState, error) {
	if processErr == nil && info != nil {
		if int(info.Proc.P_pid) != observation.PID || int(info.Eproc.Pgid) != observation.PGID ||
			(expectedSID != 0 && observedSID != expectedSID) || info.Proc.P_starttime.Sec != observation.BirthSeconds || int64(info.Proc.P_starttime.Usec) != observation.BirthMicroseconds {
			return ProcessIdentityConflict, ErrIdentityConflict
		}
		return ProcessLive, nil
	}
	if processErr != nil && !errors.Is(processErr, unix.ESRCH) {
		return ProcessIdentityConflict, processErr
	}
	switch {
	case groupErr == nil || errors.Is(groupErr, unix.EPERM):
		// Without the exact unreaped leader anchor, a surviving same-number
		// group cannot be distinguished from PGID reuse and is never signaled.
		return ProcessIdentityConflict, ErrIdentityConflict
	case errors.Is(groupErr, unix.ESRCH):
		return ProcessAbsent, nil
	default:
		return ProcessIdentityConflict, groupErr
	}
}

func (unit *realDarwinUnit) signal(signal syscall.Signal) (ProcessState, error) {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed {
		return ProcessIdentityConflict, ErrClosed
	}
	state, err := unit.inspectLocked()
	if err != nil || state != ProcessLive {
		return state, err
	}
	if err := unix.Kill(-unit.observed.PGID, signal); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return unit.inspectLocked()
		}
		return ProcessIdentityConflict, err
	}
	return ProcessLive, nil
}

func (unit *realDarwinUnit) close() error {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed {
		return ErrClosed
	}
	return unit.closeLocked()
}

func (unit *realDarwinUnit) result() (int, string, error) {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if unit.closed || !unit.leaderExited || unit.command == nil {
		return 0, "", ErrStillRunning
	}
	if !unit.waited {
		// A non-zero exit is the workload result, not a process-control error.
		_ = unit.command.Wait()
		unit.waited = true
	}
	if unit.command.ProcessState == nil {
		return 0, "", ErrIdentityConflict
	}
	status, ok := unit.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, "", ErrIdentityConflict
	}
	if status.Signaled() {
		return -1, status.Signal().String(), nil
	}
	if !status.Exited() {
		return 0, "", ErrIdentityConflict
	}
	return status.ExitStatus(), "", nil
}

func (unit *realDarwinUnit) closeLocked() error {
	if unit.closed {
		return nil
	}
	unit.closed = true
	if !unit.waited && unit.command != nil {
		if err := unit.command.Wait(); err != nil {
			// A workload's non-zero exit is not a process-control identity error.
			// Cmd.Wait has still fulfilled the exact reaping obligation.
		}
		unit.waited = true
	}
	closeFiles(unit.readyRead, unit.releaseWrite, unit.workingDirectory, unit.executable, unit.marshalImage)
	if unit.heldClosure != nil {
		unit.heldClosure.Close()
		unit.heldClosure = nil
	}
	unit.guard.close()
	if unit.processQueue >= 0 {
		_ = unix.Close(unit.processQueue)
		unit.processQueue = -1
	}
	return nil
}

func (unit *realDarwinUnit) processExitedLocked() (bool, error) {
	if unit.leaderExited {
		return true, nil
	}
	events := make([]unix.Kevent_t, 1)
	timeout := unix.Timespec{}
	count, err := unix.Kevent(unit.processQueue, nil, events, &timeout)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if events[0].Flags&unix.EV_ERROR != 0 {
		if events[0].Data == 0 {
			return false, ErrIdentityConflict
		}
		return false, unix.Errno(events[0].Data)
	}
	exited, err := classifyProcessEvent(events[0].Fflags)
	if err != nil {
		return false, err
	}
	unit.leaderExited = exited
	return exited, nil
}

func classifyProcessEvent(flags uint32) (bool, error) {
	if flags&unix.NOTE_EXIT != 0 {
		return true, nil
	}
	if flags&unix.NOTE_FORK != 0 {
		// A normal cooperative workload may fork tools. Darwin removed reliable
		// NOTE_TRACK/NOTE_CHILD support, so NOTE_FORK is only a refresh hint; it
		// is never itself an identity conflict.
		return false, nil
	}
	if flags == 0 {
		return false, ErrIdentityConflict
	}
	return false, ErrIdentityConflict
}

type descendantObservation struct {
	PID               int
	ParentPID         int
	BirthSeconds      int64
	BirthMicroseconds int64
}

func (unit *realDarwinUnit) refreshDescendantsLocked() error {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return err
	}
	tracked, err := reconcileDescendantObservations(unit.observed, unit.sid, processes, unit.trackedDescendants, unix.Getsid)
	if err != nil {
		return err
	}
	unit.trackedDescendants = tracked
	return nil
}

func reconcileDescendantObservations(observation ProcessObservation, expectedSID int, processes []unix.KinfoProc, previous map[int]descendantObservation, sessionOf func(int) (int, error)) (map[int]descendantObservation, error) {
	// The ordinary-user profile repeatedly snapshots visible ancestry and keeps
	// exact birth/parent observations for children it has seen. This detects a
	// surviving child's visible reparent, PGID migration, or new session without
	// treating normal fork/exec as containment failure. A malicious descendant
	// that escapes and disappears between snapshots remains outside the
	// cooperative, non-hardened guarantee frozen by ADR 0056.
	byPID := make(map[int]*unix.KinfoProc, len(processes))
	for index := range processes {
		process := &processes[index]
		byPID[int(process.Proc.P_pid)] = process
	}
	next := make(map[int]descendantObservation, len(previous))
	for pid, tracked := range previous {
		process := byPID[pid]
		if process == nil {
			continue
		}
		if !sameDescendantBirth(process, tracked) || int(process.Eproc.Ppid) != tracked.ParentPID || int(process.Eproc.Pgid) != observation.PGID {
			return nil, ErrIdentityConflict
		}
		sid, err := sessionOf(pid)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil || sid != expectedSID {
			return nil, ErrIdentityConflict
		}
		next[pid] = tracked
	}
	for pid, process := range byPID {
		if pid == observation.PID || !descendsFrom(pid, observation.PID, byPID) {
			continue
		}
		if int(process.Eproc.Pgid) != observation.PGID {
			return nil, ErrIdentityConflict
		}
		sid, err := sessionOf(pid)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil || sid != expectedSID {
			return nil, ErrIdentityConflict
		}
		if _, alreadyTracked := next[pid]; !alreadyTracked {
			next[pid] = descendantObservation{
				PID: pid, ParentPID: int(process.Eproc.Ppid), BirthSeconds: process.Proc.P_starttime.Sec, BirthMicroseconds: int64(process.Proc.P_starttime.Usec),
			}
		}
	}
	return next, nil
}

func sameDescendantBirth(process *unix.KinfoProc, tracked descendantObservation) bool {
	return process != nil && int(process.Proc.P_pid) == tracked.PID && process.Proc.P_starttime.Sec == tracked.BirthSeconds && int64(process.Proc.P_starttime.Usec) == tracked.BirthMicroseconds
}

func descendsFrom(pid, root int, processes map[int]*unix.KinfoProc) bool {
	seen := make(map[int]struct{})
	for pid > 1 && pid != root {
		if _, duplicate := seen[pid]; duplicate {
			return false
		}
		seen[pid] = struct{}{}
		process := processes[pid]
		if process == nil {
			return false
		}
		pid = int(process.Eproc.Ppid)
	}
	return pid == root
}

func groupDescendantState(observation ProcessObservation, expectedSID int) (ProcessState, error) {
	leader, leaderErr := unix.SysctlKinfoProc("kern.proc.pid", observation.PID)
	leaderSID := 0
	if leaderErr == nil && leader != nil {
		var sidErr error
		leaderSID, sidErr = unix.Getsid(observation.PID)
		if sidErr != nil {
			return ProcessIdentityConflict, sidErr
		}
	}
	leaderAnchored, err := classifyExitedLeader(observation, leader, expectedSID, leaderSID, leaderErr)
	if err != nil {
		return ProcessIdentityConflict, err
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", observation.PGID)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ProcessAbsent, nil
		}
		return ProcessIdentityConflict, err
	}
	return classifyGroupMembers(observation, processes, expectedSID, leaderAnchored, unix.Getsid)
}

func classifyExitedLeader(observation ProcessObservation, leader *unix.KinfoProc, expectedSID, observedSID int, processErr error) (bool, error) {
	if processErr != nil {
		if errors.Is(processErr, unix.ESRCH) {
			return false, nil
		}
		return false, processErr
	}
	if leader == nil || int(leader.Proc.P_pid) != observation.PID || int(leader.Eproc.Pgid) != observation.PGID || observedSID != expectedSID ||
		leader.Proc.P_starttime.Sec != observation.BirthSeconds || int64(leader.Proc.P_starttime.Usec) != observation.BirthMicroseconds || leader.Proc.P_stat != darwinProcessZombie {
		return false, ErrIdentityConflict
	}
	return true, nil
}

func classifyGroupMembers(observation ProcessObservation, processes []unix.KinfoProc, expectedSID int, leaderAnchored bool, sessionOf func(int) (int, error)) (ProcessState, error) {
	leaderSeen := false
	descendantSeen := false
	for index := range processes {
		process := &processes[index]
		pid := int(process.Proc.P_pid)
		sid, err := sessionOf(pid)
		if err != nil || sid != expectedSID || int(process.Eproc.Pgid) != observation.PGID {
			return ProcessIdentityConflict, ErrIdentityConflict
		}
		if pid == observation.PID {
			if leaderSeen || process.Proc.P_starttime.Sec != observation.BirthSeconds || int64(process.Proc.P_starttime.Usec) != observation.BirthMicroseconds || process.Proc.P_stat != darwinProcessZombie {
				return ProcessIdentityConflict, ErrIdentityConflict
			}
			leaderSeen = true
			continue
		}
		descendantSeen = true
	}
	if descendantSeen && !leaderSeen && !leaderAnchored {
		// Once the exact unreaped leader anchor disappears, a same-number PGID
		// can be reuse; no member remains signal-authorized.
		return ProcessIdentityConflict, ErrIdentityConflict
	}
	if descendantSeen {
		return ProcessLive, nil
	}
	return ProcessAbsent, nil
}

func (unit *realDarwinUnit) revalidate() error {
	if unit.guard.pollTainted() {
		return ErrIdentityConflict
	}
	for _, check := range []struct {
		file        *os.File
		observation ObjectObservation
		kind        uint32
		content     bool
	}{
		{unit.workingDirectory, unit.workingObservation, unix.S_IFDIR, false},
		{unit.executable, unit.executableObservation, unix.S_IFREG, true},
		{unit.marshalImage, unit.marshalObservation, unix.S_IFREG, true},
	} {
		if err := revalidateObserved(check.file, check.observation, check.kind, check.content); err != nil {
			return ErrIdentityConflict
		}
	}
	return nil
}

type vnodeGuard struct {
	queue   int
	tainted bool
}

type vnodeWatch struct {
	file             *os.File
	contentSensitive bool
}

func newVnodeGuard(watches ...vnodeWatch) (*vnodeGuard, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	changes := make([]unix.Kevent_t, 0, len(watches))
	for _, watch := range watches {
		changes = append(changes, unix.Kevent_t{
			Ident:  uint64(watch.file.Fd()),
			Filter: unix.EVFILT_VNODE,
			Flags:  unix.EV_ADD | unix.EV_CLEAR,
			Fflags: vnodeFlags(watch.contentSensitive),
		})
	}
	if _, err := unix.Kevent(queue, changes, nil, nil); err != nil {
		_ = unix.Close(queue)
		return nil, err
	}
	return &vnodeGuard{queue: queue}, nil
}

func vnodeFlags(contentSensitive bool) uint32 {
	flags := uint32(unix.NOTE_DELETE | unix.NOTE_ATTRIB | unix.NOTE_RENAME | unix.NOTE_REVOKE)
	if contentSensitive {
		flags |= unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_LINK
	}
	return flags
}

func (guard *vnodeGuard) pollTainted() bool {
	if guard == nil || guard.tainted {
		return true
	}
	events := make([]unix.Kevent_t, 3)
	timeout := unix.Timespec{}
	count, err := unix.Kevent(guard.queue, nil, events, &timeout)
	if err != nil || count > 0 {
		guard.tainted = true
	}
	return guard.tainted
}

func (guard *vnodeGuard) close() {
	if guard != nil && guard.queue >= 0 {
		_ = unix.Close(guard.queue)
		guard.queue = -1
	}
}

func newProcessQueue(pid int) (int, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return -1, err
	}
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_EXIT | unix.NOTE_FORK}
	if _, err := unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(queue)
		return -1, err
	}
	return queue, nil
}

func observeProcess(pid int, working, executable ObjectObservation) (ProcessObservation, int, time.Time, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || int(info.Eproc.Pgid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return ProcessObservation{}, 0, time.Time{}, ErrIdentityConflict
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return ProcessObservation{}, 0, time.Time{}, ErrIdentityConflict
	}
	parentSID, err := unix.Getsid(0)
	if err != nil || sid != parentSID {
		// v1's ordinary-user production profile is cooperative: the launched
		// leader must remain in the coordinator session. A leader-side setsid or
		// PGID drift is observable and always fails closed; unobservable escaped
		// descendants are not claimed as contained.
		return ProcessObservation{}, 0, time.Time{}, ErrIdentityConflict
	}
	observation, err := (ProcessObservation{
		PID:                    pid,
		PGID:                   pid,
		BirthSeconds:           info.Proc.P_starttime.Sec,
		BirthMicroseconds:      int64(info.Proc.P_starttime.Usec),
		WorkingDirectory:       working.Path,
		WorkingDirectoryDevice: working.Device,
		WorkingDirectoryInode:  working.Inode,
		WorkingDirectoryType:   working.Mode & unix.S_IFMT,
		WorkingDirectoryOwner:  working.UID,
		WorkingDirectoryMode:   working.Mode,
		ExecutablePath:         executable.Path,
		ExecutableDevice:       executable.Device,
		ExecutableInode:        executable.Inode,
		ExecutableSize:         executable.Size,
		ExecutableType:         executable.Mode & unix.S_IFMT,
		ExecutableOwner:        executable.UID,
		ExecutableGroup:        executable.GID,
		ExecutableMode:         executable.Mode,
		ExecutableLinkCount:    executable.Nlink,
		ExecutableSHA256:       executable.SHA256,
		ObserverIdentity:       processObserver,
	}).sealed()
	if err != nil {
		return ProcessObservation{}, 0, time.Time{}, err
	}
	observedAt := time.Now().UTC()
	if err := validateObservedAt(observation, observedAt); err != nil {
		return ProcessObservation{}, 0, time.Time{}, err
	}
	return observation, sid, observedAt, nil
}

func processExecutablePath(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 5 {
		return "", ErrIdentityConflict
	}
	_ = binary.LittleEndian.Uint32(raw[:4])
	rest := raw[4:]
	end := 0
	for end < len(rest) && rest[end] != 0 {
		end++
	}
	if end == 0 || end == len(rest) {
		return "", ErrIdentityConflict
	}
	path := string(rest[:end])
	if !absoluteClean(path) {
		return "", ErrIdentityConflict
	}
	return path, nil
}

func openObserved(path string, kind uint32, hash bool) (*os.File, ObjectObservation, error) {
	if !absoluteClean(path) {
		return nil, ObjectObservation{}, ErrIdentityConflict
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW_ANY
	if kind == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, ObjectObservation{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	observation, err := observeFile(file, path, kind, hash)
	if err != nil {
		_ = file.Close()
		return nil, ObjectObservation{}, err
	}
	return file, observation, nil
}

func observeFile(file *os.File, path string, kind uint32, hash bool) (ObjectObservation, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || uint32(stat.Mode)&unix.S_IFMT != kind {
		return ObjectObservation{}, ErrIdentityConflict
	}
	observation := ObjectObservation{
		Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid,
		Size: stat.Size, Nlink: uint64(stat.Nlink),
	}
	if kind == unix.S_IFREG && (stat.Mode&0o111 == 0 || stat.Nlink != 1) {
		return ObjectObservation{}, ErrIdentityConflict
	}
	if hash {
		digest, err := digestOpenFile(file)
		if err != nil {
			return ObjectObservation{}, err
		}
		observation.SHA256 = digest
	}
	return observation, nil
}

func revalidateObserved(held *os.File, expected ObjectObservation, kind uint32, contentSensitive bool) error {
	heldObservation, err := observeFile(held, expected.Path, kind, contentSensitive)
	if err != nil || !sameObservedObject(heldObservation, expected, contentSensitive) {
		return ErrIdentityConflict
	}
	opened, openedObservation, err := openObserved(expected.Path, kind, contentSensitive)
	if err != nil {
		return ErrIdentityConflict
	}
	_ = opened.Close()
	if !sameObservedObject(openedObservation, expected, contentSensitive) {
		return ErrIdentityConflict
	}
	return nil
}

func sameObservedObject(actual, expected ObjectObservation, contentSensitive bool) bool {
	if actual.Path != expected.Path || actual.Device != expected.Device || actual.Inode != expected.Inode || actual.Mode != expected.Mode || actual.UID != expected.UID || actual.GID != expected.GID {
		return false
	}
	if !contentSensitive {
		return true
	}
	return actual.Size == expected.Size && actual.Nlink == expected.Nlink && actual.SHA256 == expected.SHA256
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

func pipeBinding(file *os.File) sandboxlaunch.ObjectBinding {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return sandboxlaunch.ObjectBinding{}
	}
	return sandboxlaunch.ObjectBinding{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid, Size: stat.Size, Nlink: uint64(stat.Nlink)}
}

func launchBinding(observation ObjectObservation) sandboxlaunch.ObjectBinding {
	return sandboxlaunch.ObjectBinding{Device: observation.Device, Inode: observation.Inode, Mode: observation.Mode, UID: observation.UID, GID: observation.GID, Size: observation.Size, Nlink: observation.Nlink, SHA256: observation.SHA256}
}

func identityBinding(object launchidentity.ObjectV1) sandboxlaunch.ObjectBinding {
	return sandboxlaunch.ObjectBinding{Device: object.Device, Inode: object.Inode, Mode: object.Mode, UID: object.UID, GID: object.GID, Size: object.Size, Nlink: object.LinkCount, SHA256: object.RawSHA256}
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		count, err := writer.Write(content)
		if count > 0 {
			content = content[count:]
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func boundedOwnedWait(done <-chan struct{}, timeout <-chan time.Time, cleanup func()) bool {
	select {
	case <-done:
		cleanup()
		return true
	case <-timeout:
		// Keep the wait right, held FDs, and vnode guard strongly owned until
		// the fixed helper actually exits. Never synthesize kill authority.
		go func() {
			<-done
			cleanup()
		}()
		return false
	}
}
