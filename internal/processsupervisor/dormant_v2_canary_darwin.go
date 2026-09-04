//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

// DormantV2CanaryResult is emitted only by the fixed-image S2 canary. It
// contains no path, argv, environment, nonce, PID or raw transcript material.
type DormantV2CanaryResult struct {
	ProtocolRevision    string `json:"protocolRevision"`
	MechanicsIdentity   string `json:"mechanicsIdentity"`
	ObserverIdentity    string `json:"observerIdentity"`
	NaturalExitCode     int    `json:"naturalExitCode"`
	StoppedCleanupState string `json:"stoppedCleanupState"`
	TimeoutRejected     bool   `json:"timeoutRejected"`
	HostilePathRejected bool   `json:"hostilePathRejected"`
}

type dormantV2CanaryError string

func (err dormantV2CanaryError) Error() string { return string(err) }

// DormantV2CanaryReason returns a closed, input-independent stage code.
func DormantV2CanaryReason(err error) string {
	var stage dormantV2CanaryError
	if errors.As(err, &stage) {
		return string(stage)
	}
	return "supervisor-v2-canary-rejected"
}

func dormantV2Reject(stage string) error {
	return dormantV2CanaryError("supervisor-v2-canary-" + stage + "-rejected")
}

// RunDormantV2FixedCanary exercises the real fixed Marshal parent, inherited
// child FD protocol and runLaunchChild SETEXEC caller. It creates data files
// only and uses the installed, resolved Node runtime already required by the
// Darwin Pi profile. The v2 production selector remains disabled.
func RunDormantV2FixedCanary(ctx context.Context) (DormantV2CanaryResult, error) {
	if ctx == nil {
		return DormantV2CanaryResult{}, ErrInvalid
	}
	runtimePath, err := dormantV2CanaryRuntime()
	if err != nil {
		return DormantV2CanaryResult{}, err
	}
	natural, err := runDormantV2Case(ctx, runtimePath, []string{runtimePath, "-e", "process.exit(1)"}, false)
	if err != nil {
		return DormantV2CanaryResult{}, err
	}
	if natural.State != "terminal" || natural.ExitCode != 1 || natural.Signal != "" {
		return DormantV2CanaryResult{}, dormantV2Reject("natural-result")
	}
	cleanup, err := runDormantV2Case(ctx, runtimePath, []string{runtimePath, "-e", "setTimeout(()=>{},60000)"}, true)
	if err != nil {
		return DormantV2CanaryResult{}, err
	}
	if cleanup.State != "terminal" || cleanup.Signal == "" {
		return DormantV2CanaryResult{}, dormantV2Reject("cleanup-result")
	}
	timeoutRejected, err := runDormantV2TimeoutCase(ctx, runtimePath)
	if err != nil {
		return DormantV2CanaryResult{}, err
	}
	if !timeoutRejected {
		return DormantV2CanaryResult{}, dormantV2Reject("timeout-result")
	}
	hostileRejected, err := runDormantV2HostilePathCase(ctx, runtimePath)
	if err != nil {
		return DormantV2CanaryResult{}, err
	}
	if !hostileRejected {
		return DormantV2CanaryResult{}, dormantV2Reject("hostile-path-result")
	}
	return DormantV2CanaryResult{
		ProtocolRevision: protocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2, ObserverIdentity: observerIdentityV2,
		NaturalExitCode: natural.ExitCode, StoppedCleanupState: cleanup.State, TimeoutRejected: true, HostilePathRejected: true,
	}, nil
}

func dormantV2CanaryRuntime() (string, error) {
	path, err := exec.LookPath("node")
	if err != nil {
		return "", dormantV2Reject("runtime-unavailable")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !absoluteClean(path) {
		return "", dormantV2Reject("runtime-identity")
	}
	return path, nil
}

func runDormantV2Case(ctx context.Context, runtimePath string, argv []string, terminateStopped bool) (ProcessReport, error) {
	mechanics, payload, cleanup, err := newDormantV2CanaryMechanics(runtimePath, argv)
	if err != nil {
		return ProcessReport{}, err
	}
	defer cleanup()
	spawned, err := mechanics.Spawn(ctx, payload)
	if err != nil {
		if stage := mechanicsFailureStage(err); stage != "" {
			return ProcessReport{}, dormantV2Reject("spawn-" + stage)
		}
		if errors.Is(err, ErrConflict) {
			return ProcessReport{}, dormantV2Reject("spawn-conflict")
		}
		if errors.Is(err, ErrMechanicsOpen) {
			return ProcessReport{}, dormantV2Reject("spawn-capacity")
		}
		return ProcessReport{}, dormantV2Reject("spawn")
	}
	if report, err := dormantV2ResultReport(spawned); err != nil || report.State != "exec-stopped" {
		return ProcessReport{}, dormantV2Reject("spawn-report")
	}
	barrier := dormantV2CleanupPayload()
	if terminateStopped {
		result, err := mechanics.Terminate(ctx, barrier)
		if err != nil {
			return ProcessReport{}, dormantV2Reject("terminate")
		}
		report, err := dormantV2ResultReport(result)
		if err != nil || report.State != "terminal" {
			return ProcessReport{}, dormantV2Reject("terminate-report")
		}
		return finishDormantV2Canary(ctx, mechanics, report)
	}
	resumed, err := mechanics.Resume(ctx, ResumePayload{ProcessStartedFactDigest: canonical.DigestBytes([]byte("marshal/v2-canary/process-started"))})
	if err != nil {
		return ProcessReport{}, dormantV2Reject("resume")
	}
	if report, err := dormantV2ResultReport(resumed); err != nil || report.State != "running" {
		return ProcessReport{}, dormantV2Reject("resume-report")
	}
	report, err := awaitDormantV2Terminal(ctx, mechanics, barrier)
	if err != nil {
		return ProcessReport{}, dormantV2Reject("inspect")
	}
	return finishDormantV2Canary(ctx, mechanics, report)
}

func finishDormantV2Canary(ctx context.Context, mechanics *darwinMechanics, terminal ProcessReport) (ProcessReport, error) {
	collected, err := mechanics.Collect(ctx, CollectPayload{ProcessStartedFactDigest: canonical.DigestBytes([]byte("marshal/v2-canary/process-started")), LastObservationDigest: canonical.DigestBytes([]byte("marshal/v2-canary/terminal"))})
	if err != nil {
		return ProcessReport{}, err
	}
	report, err := dormantV2ResultReport(collected)
	if err != nil || report.State != "terminal" || report.Process != terminal.Process {
		return ProcessReport{}, ErrIntervention
	}
	closed, err := mechanics.Close(ctx, ClosePayload{
		ProcessTerminalFactDigest:  canonical.DigestBytes([]byte("marshal/v2-canary/process-terminal")),
		AllocationTerminatedDigest: canonical.DigestBytes([]byte("marshal/v2-canary/allocation-terminal")),
		CleanupBindingDigest:       canonical.DigestBytes([]byte("marshal/v2-canary/cleanup")),
	})
	if err != nil {
		return ProcessReport{}, err
	}
	if _, err := dormantV2ResultReport(closed); err != nil {
		return ProcessReport{}, err
	}
	return report, nil
}

func awaitDormantV2Terminal(ctx context.Context, mechanics *darwinMechanics, barrier CleanupPayload) (ProcessReport, error) {
	for {
		result, err := mechanics.Inspect(ctx, barrier)
		if err != nil {
			return ProcessReport{}, err
		}
		report, err := dormantV2ResultReport(result)
		if err != nil {
			return ProcessReport{}, err
		}
		if report.State == "terminal" {
			return report, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ProcessReport{}, ErrIntervention
		case <-timer.C:
		}
	}
}

func runDormantV2TimeoutCase(ctx context.Context, runtimePath string) (bool, error) {
	mechanics, payload, cleanup, err := newDormantV2CanaryMechanics(runtimePath, []string{runtimePath, "-e", "process.exit(0)"})
	if err != nil {
		return false, err
	}
	defer cleanup()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = mechanics.Spawn(cancelled, payload)
	return errors.Is(err, ErrIntervention), nil
}

func runDormantV2HostilePathCase(ctx context.Context, runtimePath string) (bool, error) {
	mechanics, payload, cleanup, err := newDormantV2CanaryMechanics(runtimePath, []string{runtimePath, "-e", "process.exit(0)"})
	if err != nil {
		return false, err
	}
	defer cleanup()
	symlink := filepath.Join(payload.WorkingDirectory.CanonicalPath, "runtime-link")
	if err := os.Symlink(payload.Runtime.CanonicalPath, symlink); err != nil {
		return false, err
	}
	payload.Runtime.CanonicalPath = symlink
	payload.Argv[0] = symlink
	if err := resealDormantV2CanaryPayload(&payload); err != nil {
		return false, err
	}
	_, err = mechanics.Spawn(ctx, payload)
	return errors.Is(err, ErrConflict), nil
}

func newDormantV2CanaryMechanics(runtimePath string, arguments []string) (*darwinMechanics, SpawnPayload, func(), error) {
	// /var is a symlink to /private/var on macOS. O_NOFOLLOW_ANY deliberately
	// rejects that alias, so use the stable physical temporary root. Only
	// owner-only data/control files are created here; no executable is emitted.
	root, err := os.MkdirTemp("/private/tmp", "marshal-v2-canary-data-")
	if err != nil {
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-directory")
	}
	if os.Chmod(root, 0o700) != nil {
		_ = os.RemoveAll(root)
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-directory-mode")
	}
	cleanupRoot := func() { _ = os.RemoveAll(root) }
	directory, err := os.Open(root)
	if err != nil {
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-directory-open")
	}
	mechanics, err := newDarwinMechanics(directory, protocolRevisionV2)
	if err != nil {
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-mechanics")
	}
	runtimeFile, runtimeSpec, err := openObservedSpec("runtime", runtimePath, "regular")
	if err != nil {
		_ = mechanics.marshalFile.Close()
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-runtime")
	}
	_ = runtimeFile.Close()
	workingFile, workingSpec, err := openObservedSpec("working-directory", root, "directory")
	if err != nil {
		_ = mechanics.marshalFile.Close()
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-working-directory")
	}
	_ = workingFile.Close()
	argv := append([]string(nil), arguments...)
	if len(argv) == 0 || argv[0] != runtimePath {
		_ = mechanics.marshalFile.Close()
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-arguments")
	}
	environment := []string{}
	closure, err := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: launchObject(runtimeSpec), ClosureProfileID: launchidentity.NativeProfile,
		MaterialRoots: []launchidentity.MaterialRootV1{}, LaunchMaterials: []launchidentity.LaunchMaterialV1{}, Arguments: argv, Environment: environment, WorkingDirectory: root,
	})
	if err != nil {
		_ = mechanics.marshalFile.Close()
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-closure")
	}
	payload := SpawnPayload{
		LaunchAuthorizedFactDigest: canonical.DigestBytes([]byte("marshal/v2-canary/launch")), SupervisorStartedFactDigest: canonical.DigestBytes([]byte("marshal/v2-canary/supervisor")),
		Runtime: runtimeSpec, WorkingDirectory: workingSpec,
		AllocationLiveIdentity: &AllocationLiveIdentity{Device: workingSpec.Device, Inode: workingSpec.Inode, FileType: workingSpec.FileType, UID: workingSpec.UID, GID: workingSpec.GID, Mode: workingSpec.Mode, LinkCount: workingSpec.LinkCount, Size: workingSpec.Size},
		SourceGateRevision:     SourceGateRevisionV1, ClosureProfileID: closure.ClosureProfileID, MaterialRoots: closure.MaterialRoots, LaunchMaterials: closure.LaunchMaterials,
		LaunchMaterialsDigest: closure.LaunchMaterialsDigest, AgentLaunchSpecDigest: closure.AgentLaunchSpecDigest,
		ArgvDigest: mustDigestValueForCanary(argv), EnvironmentDigest: mustDigestValueForCanary(environment), StdinDigest: canonical.DigestBytes(nil),
		EnvironmentKeys: []string{}, Argv: argv, Environment: environment, Stdin: []byte{},
	}
	if validateSpawnPayload(payload) != nil {
		_ = mechanics.marshalFile.Close()
		_ = directory.Close()
		cleanupRoot()
		return nil, SpawnPayload{}, func() {}, dormantV2Reject("setup-payload")
	}
	cleanup := func() {
		if mechanics.marshalFile != nil {
			_ = mechanics.marshalFile.Close()
		}
		_ = directory.Close()
		cleanupRoot()
	}
	return mechanics, payload, cleanup, nil
}

func resealDormantV2CanaryPayload(payload *SpawnPayload) error {
	if payload == nil {
		return ErrInvalid
	}
	closure, err := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: launchObject(payload.Runtime), ClosureProfileID: launchidentity.NativeProfile,
		MaterialRoots: payload.MaterialRoots, LaunchMaterials: payload.LaunchMaterials,
		Arguments: payload.Argv, Environment: payload.Environment, WorkingDirectory: payload.WorkingDirectory.CanonicalPath,
	})
	if err != nil {
		return ErrInvalid
	}
	payload.LaunchMaterialsDigest = closure.LaunchMaterialsDigest
	payload.AgentLaunchSpecDigest = closure.AgentLaunchSpecDigest
	payload.ArgvDigest = mustDigestValueForCanary(payload.Argv)
	payload.EnvironmentDigest = mustDigestValueForCanary(payload.Environment)
	if payload.ArgvDigest == "" || payload.EnvironmentDigest == "" {
		return ErrInvalid
	}
	return nil
}

func dormantV2CleanupPayload() CleanupPayload {
	return CleanupPayload{
		ProcessStartedFactDigest:     canonical.DigestBytes([]byte("marshal/v2-canary/process-started")),
		TerminalizationBarrierDigest: canonical.DigestBytes([]byte("marshal/v2-canary/barrier")), TerminalizationID: "v2-canary-terminalization", TerminalGeneration: 1,
		CleanupBindingDigest: canonical.DigestBytes([]byte("marshal/v2-canary/cleanup")), LastObservationDigest: canonical.DigestBytes([]byte("marshal/v2-canary/observation")),
	}
}

func dormantV2ResultReport(result MechanicsResult) (ProcessReport, error) {
	var report ProcessReport
	if strictCanonicalDecode(result.Payload, &report) != nil || ValidateDormantV2ProcessReport(report) != nil {
		return ProcessReport{}, ErrIntervention
	}
	digest, err := digestValue(report)
	if err != nil || result.ObservationDigest != digest {
		return ProcessReport{}, ErrIntervention
	}
	return report, nil
}

func mustDigestValueForCanary(value any) string {
	digest, _ := digestValue(value)
	return digest
}
