package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// FaultKind is the closed enumeration of deterministic fault injections the
// fake provider supports.
type FaultKind string

// Closed members of FaultKind.
const (
	// FaultReject refuses the matched operation with ErrFaultInjected.
	FaultReject FaultKind = "reject"
	// FaultDelay advances the fake's logical clock by exactly one tick
	// before serving the matched operation; no wall clock participates.
	FaultDelay FaultKind = "delay"
	// FaultDropResponse drops the matched response with ErrResponseLost.
	FaultDropResponse FaultKind = "drop-response"
	// FaultEchoDeclaredDigest turns Stage into a digest-echoing operation:
	// the declared digest is echoed into the receipt without recomputation,
	// which the conformance suite must detect and judge as a failure.
	FaultEchoDeclaredDigest FaultKind = "echo-declared-digest"
	// FaultTamperStageBytes tampers with the staged bytes between the
	// pre-consumption and the post-consumption recomputation.
	FaultTamperStageBytes FaultKind = "tamper-stage-bytes"
	// FaultSelfSignConformance makes Probe carry a self-signed conformance
	// pass claim, which the suite must ignore and which must never turn a
	// failing observation into a pass.
	FaultSelfSignConformance FaultKind = "self-sign-conformance"
	// FaultDisableContainment disables the sandbox containment simulation
	// for Exec, letting the adversarial probe commands surface as observed
	// boundary violations and as deterministic observation log entries
	// through Inspect.
	FaultDisableContainment FaultKind = "disable-containment"
)

// ErrFaultInjected is the fixed sentinel returned by an injected rejection.
var ErrFaultInjected = errors.New("sandbox: fake provider rejected the operation through an injected fault")

// FaultSpec configures one deterministic fault injection, scoped by
// operation name and optionally by commandId; an empty CommandId matches
// every command. The first matching spec wins.
type FaultSpec struct {
	Operation string
	CommandId string
	Fault     FaultKind
}

func (spec FaultSpec) matches(operation, commandId string) bool {
	if spec.Operation != operation {
		return false
	}
	return spec.CommandId == "" || spec.CommandId == commandId
}

// FakeConfig configures the scripted fake provider.
type FakeConfig struct {
	// ConformanceEvidenceRef is the valid conformance evidence digest the
	// fake provider holds; empty means it holds none, so every hardened
	// request must be refused fail closed.
	ConformanceEvidenceRef string
}

// fakeAllocation is the in-memory state of one allocation. The ADR 0055
// envelope declarations are recorded exactly as granted at Provision time;
// the fake provider simulates the symlink-resolved working-root comparison
// with cleaned absolute paths, since no filesystem participates.
type fakeAllocation struct {
	meta             SandboxAllocation
	staged           map[string][]byte
	violations       []BoundaryViolation
	spawnCount       int64
	log              []string
	exitCode         int
	checkpoints      int64
	workDirAllowlist []string
	envAllowlist     []string
}

// maxLogLines bounds the per-allocation observation log returned by
// Inspect, keeping the adjudication input bounded.
const maxLogLines = 32

// fakeTimeoutCapSeconds is the deterministic provider cap the fake provider
// clamps the ADR 0055 §4 per-op timeout against; no wall clock ever
// participates, the clamp is recorded in the observation log.
const fakeTimeoutCapSeconds int64 = 3600

// FakeProvider is a scripted, deterministic implementation of
// SandboxProvider in the style of internal/adapter/fake: all behavior is
// derived from the construction-time configuration, the injected fault
// specs and the request script, with no random source, no real processes
// and no wall-clock read.
type FakeProvider struct {
	config       FakeConfig
	faults       []FaultSpec
	allocations  map[string]*fakeAllocation
	store        map[string][]byte
	logicalTicks int64
}

// NewFakeProvider constructs a scripted fake provider.
func NewFakeProvider(config FakeConfig) *FakeProvider {
	return &FakeProvider{
		config:      config,
		allocations: map[string]*fakeAllocation{},
		store:       map[string][]byte{},
	}
}

// WithFaults appends fault injection specs and returns the provider for
// chaining.
func (f *FakeProvider) WithFaults(specs ...FaultSpec) *FakeProvider {
	f.faults = append(f.faults, specs...)
	return f
}

// SeedStore places deterministic content behind one locator of the fake's
// artifact store so staged locators resolve.
func (f *FakeProvider) SeedStore(storeId, sha256 string, content []byte) {
	f.store[storeId+"\x00"+sha256] = append([]byte(nil), content...)
}

// LogicalTicks exposes the deterministic logical clock advanced by injected
// delays.
func (f *FakeProvider) LogicalTicks() int64 {
	return f.logicalTicks
}

func (f *FakeProvider) faultFor(operation, commandId string) (FaultKind, bool) {
	for _, spec := range f.faults {
		if spec.matches(operation, commandId) {
			return spec.Fault, true
		}
	}
	return "", false
}

// enterOperation validates the operation identity fail closed first, then
// applies the reject, delay and drop-response faults. Behavioral faults are
// applied by the operations themselves.
func (f *FakeProvider) enterOperation(operation string, identity OperationIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	fault, active := f.faultFor(operation, identity.CommandId)
	if !active {
		return nil
	}
	switch fault {
	case FaultReject:
		return fmt.Errorf("%w: %s", ErrFaultInjected, operation)
	case FaultDelay:
		f.logicalTicks++
		return nil
	case FaultDropResponse:
		return ErrResponseLost
	default:
		return nil
	}
}

// activeAllocation resolves the addressed allocation and enforces the
// dispatch-bound bindings fail closed: the identity must bind the same
// allocation locator and carry the allocation's current generation, so a
// stale handle presented after a restore is rejected.
func (f *FakeProvider) activeAllocation(identity OperationIdentity, allocationId string) (*fakeAllocation, error) {
	if strings.TrimSpace(allocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", ErrInvalidRequest)
	}
	if identity.AllocationId != allocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", ErrInvalidRequest, identity.AllocationId, allocationId)
	}
	allocation, ok := f.allocations[allocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAllocationNotFound, allocationId)
	}
	if allocation.meta.State != AllocationActive {
		return nil, fmt.Errorf("%w: %q is %s", ErrAllocationNotActive, allocationId, string(allocation.meta.State))
	}
	if identity.Generation != allocation.meta.Generation {
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", ErrStaleAllocationGeneration, identity.Generation, allocation.meta.Generation)
	}
	return allocation, nil
}

func (f *FakeProvider) allocationsFor(runId, attemptId string) []SandboxAllocation {
	var result []SandboxAllocation
	for _, allocation := range f.allocations {
		if allocation.meta.RunId == runId && allocation.meta.AttemptId == attemptId {
			result = append(result, allocation.meta)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AllocationId < result[j].AllocationId
	})
	return result
}

func (f *FakeProvider) appendLog(allocation *fakeAllocation, line string) {
	allocation.log = append(allocation.log, line)
	if len(allocation.log) > maxLogLines {
		allocation.log = allocation.log[len(allocation.log)-maxLogLines:]
	}
}

// Probe implements SandboxProvider.
func (f *FakeProvider) Probe(ctx context.Context, request ProbeRequest) (*ProbeReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationProbe, request.Identity); err != nil {
		return nil, err
	}
	if err := validateRequirements(request.Requirements); err != nil {
		return nil, err
	}
	report := &ProbeReport{
		Supported:              true,
		Reason:                 "the fake provider serves every closed access mode and assurance level combination",
		ConformanceEvidenceRef: f.config.ConformanceEvidenceRef,
	}
	if fault, active := f.faultFor(OperationProbe, request.Identity.CommandId); active && fault == FaultSelfSignConformance {
		report.SelfSignedConformanceClaim = true
	}
	return report, nil
}

// Provision implements SandboxProvider.
func (f *FakeProvider) Provision(ctx context.Context, request ProvisionRequest) (*ProvisionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationProvision, request.Identity); err != nil {
		return nil, err
	}
	if err := CheckAssuranceGate(request.Requirements, f.config.ConformanceEvidenceRef); err != nil {
		return nil, err
	}
	for _, storeId := range request.AllowedStoreIds {
		if err := validateStoreAliasShape(storeId); err != nil {
			return nil, fmt.Errorf("sandbox: provision: %w", err)
		}
	}
	// ADR 0055 §1/§2: the optional envelope declarations are registered
	// fail closed; credential-semantic environment keys never register.
	if err := ValidateWorkDirAllowlist(request.WorkDirAllowlist); err != nil {
		return nil, err
	}
	if err := ValidateEnvironmentAllowlist(request.EnvironmentAllowlist); err != nil {
		return nil, err
	}
	workDirAllowlist := make([]string, 0, len(request.WorkDirAllowlist))
	for _, declared := range request.WorkDirAllowlist {
		workDirAllowlist = append(workDirAllowlist, filepath.Clean(declared))
	}
	candidate := SandboxAllocation{
		AllocationId:           request.Identity.AllocationId,
		RunId:                  request.Identity.RunId,
		AttemptId:              request.Identity.AttemptId,
		Generation:             request.Identity.Generation,
		State:                  AllocationActive,
		AccessMode:             request.Requirements.AccessMode,
		AssuranceLevel:         request.Requirements.MinimumAssuranceLevel,
		ConformanceEvidenceRef: f.config.ConformanceEvidenceRef,
		AllowedStoreIds:        append([]string(nil), request.AllowedStoreIds...),
		WorkDirAllowlist:       append([]string(nil), request.WorkDirAllowlist...),
		EnvironmentAllowlist:   append([]string(nil), request.EnvironmentAllowlist...),
	}
	existing := f.allocationsFor(candidate.RunId, candidate.AttemptId)
	if err := CheckSingleActive(existing, candidate); err != nil {
		return nil, err
	}
	f.allocations[candidate.AllocationId] = &fakeAllocation{
		meta:             candidate,
		staged:           map[string][]byte{},
		workDirAllowlist: workDirAllowlist,
		envAllowlist:     append([]string(nil), request.EnvironmentAllowlist...),
	}
	return &ProvisionReceipt{Allocation: candidate}, nil
}

// Stage implements SandboxProvider. The honest path recomputes the sha256
// digest before consumption and fails the attempt closed with
// ErrStageInputMismatch on any mismatch, then recomputes after consumption;
// the receipt always carries the recomputed digests, never an echo.
func (f *FakeProvider) Stage(ctx context.Context, request StageRequest) (*StageReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationStage, request.Identity); err != nil {
		return nil, err
	}
	allocation, err := f.activeAllocation(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	if err := ValidateStageRequest(request.Inputs, allocation.meta.AllowedStoreIds); err != nil {
		return nil, err
	}
	fault, active := f.faultFor(OperationStage, request.Identity.CommandId)
	echoed := active && fault == FaultEchoDeclaredDigest
	tampered := active && fault == FaultTamperStageBytes
	report := &StageReport{Receipts: make([]StageReceipt, 0, len(request.Inputs))}
	for _, input := range request.Inputs {
		content, resolveErr := f.resolveContent(input)
		if resolveErr != nil {
			return nil, resolveErr
		}
		var pre, post string
		if echoed {
			// Fault mode: echo the declared digest without recomputation.
			pre = input.DeclaredSHA256
			post = input.DeclaredSHA256
		} else {
			pre = RecomputeSHA256(content)
			if pre != input.DeclaredSHA256 {
				allocation.meta.State = AllocationFailed
				return nil, fmt.Errorf("%w: input %q", ErrStageInputMismatch, input.InputId)
			}
		}
		stored := append([]byte(nil), content...)
		if tampered {
			stored = append(stored, []byte("|fake-tamper-marker")...)
		}
		if !echoed {
			post = RecomputeSHA256(stored)
		}
		allocation.staged[input.InputId] = stored
		report.Receipts = append(report.Receipts, StageReceipt{
			InputId:               input.InputId,
			RecomputedSHA256:      pre,
			PostConsumptionSHA256: post,
			SizeBytes:             int64(len(content)),
		})
	}
	return report, nil
}

func (f *FakeProvider) resolveContent(input StageInput) ([]byte, error) {
	if input.Locator == nil {
		return append([]byte(nil), input.Inline...), nil
	}
	content, ok := f.store[input.Locator.StoreId+"\x00"+input.Locator.SHA256]
	if !ok {
		return nil, fmt.Errorf("%w: store %q does not hold object %q", ErrLocatorUnresolved, input.Locator.StoreId, input.Locator.SHA256)
	}
	return append([]byte(nil), content...), nil
}

// Exec implements SandboxProvider. The adversarial probe command tokens are
// contained by default; the disable-containment fault lets them surface as
// observed violations and deterministic observation log entries through
// Inspect, so the out-of-band observation records every escaped probe.
func (f *FakeProvider) Exec(ctx context.Context, request ExecRequest) (*ExecReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationExec, request.Identity); err != nil {
		return nil, err
	}
	if len(request.Command) == 0 {
		return nil, fmt.Errorf("%w: exec requires a non-empty command", ErrInvalidRequest)
	}
	allocation, err := f.activeAllocation(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	// Adjudicate the optional ADR 0055 envelope fail closed before any
	// scripted outcome: the provider-independent shape first, then the
	// declared bindings recorded on the allocation at Provision time. The
	// fake performs no filesystem reads: a WorkingDir binding is
	// adjudicated on cleaned absolute paths, simulating the Local
	// provider's symlink-resolved comparison.
	if err := request.ValidateEnvelope(); err != nil {
		return nil, err
	}
	if request.WorkingDir != "" {
		resolved := filepath.Clean(request.WorkingDir)
		declared := false
		for _, root := range allocation.workDirAllowlist {
			if root == resolved {
				declared = true
				break
			}
		}
		if !declared {
			return nil, fmt.Errorf("%w: WorkingDir %q is not declared in the allocation's workDirAllowlist", ErrInvalidWorkDir, request.WorkingDir)
		}
		f.appendLog(allocation, "exec cwd: "+resolved)
	}
	envOverlays, err := ResolveExecEnvironment(request.Environment, allocation.envAllowlist)
	if err != nil {
		return nil, err
	}
	if len(envOverlays) > 0 {
		f.appendLog(allocation, "exec env overlay: "+strings.Join(envOverlays, " "))
	}
	if request.TimeoutSeconds > 0 {
		f.appendLog(allocation, fmt.Sprintf("exec timeout effective: %ds", EffectiveTimeoutSeconds(request.TimeoutSeconds, fakeTimeoutCapSeconds)))
	}
	contained := true
	if fault, active := f.faultFor(OperationExec, request.Identity.CommandId); active && fault == FaultDisableContainment {
		contained = false
	}
	for _, token := range request.Command {
		switch token {
		case ProbeCommandBoundaryWrite:
			if contained {
				f.appendLog(allocation, "probe blocked: out-of-bounds write attempt contained")
			} else {
				allocation.violations = append(allocation.violations, BoundaryViolation{Kind: ViolationOutOfBoundsWrite, Detail: "the probe wrote outside the allocation boundary"})
				f.appendLog(allocation, "observed violation: out-of-bounds write escaped containment")
			}
		case ProbeCommandSensitiveEnvRead:
			if contained {
				f.appendLog(allocation, "probe blocked: sensitive environment read denied")
			} else {
				allocation.violations = append(allocation.violations, BoundaryViolation{Kind: ViolationSensitiveEnvRead, Detail: "the probe read sensitive environment entries"})
				f.appendLog(allocation, "observed violation: sensitive environment read escaped containment")
			}
		case ProbeCommandSpawnFlood:
			if contained {
				f.appendLog(allocation, "probe blocked: spawn flood capped at the limit")
			} else {
				allocation.spawnCount += 8
				allocation.violations = append(allocation.violations, BoundaryViolation{Kind: ViolationSpawnLimitExceeded, Detail: "the probe exceeded the spawn limit"})
				f.appendLog(allocation, "observed violation: spawn flood escaped containment")
			}
		default:
			f.appendLog(allocation, "exec: "+token)
		}
	}
	allocation.exitCode = 0
	joined := strings.Join(request.Command, "\x00")
	stdout := []byte("stdout:" + joined)
	stderr := []byte("stderr:" + joined)
	receipt := &ExecReceipt{
		Status:       ExecutionCompleted,
		ExitCode:     0,
		StdoutSHA256: RecomputeSHA256(stdout),
		StderrSHA256: RecomputeSHA256(stderr),
	}
	// The deterministic transcript sink of ADR 0055 §3: an overflowing
	// capture kills the workload fail closed without any staged artifact or
	// partial success; a cleanly completing capture is staged in memory and
	// its digest is recomputed and echoed in the receipt.
	if !request.TranscriptPolicy.Absent() {
		if int64(len(stdout)) > request.TranscriptPolicy.MaxBytes {
			allocation.exitCode = -1
			receipt.Status = ExecutionKilled
			receipt.ExitCode = -1
			f.appendLog(allocation, "transcript bound exceeded: workload killed without partial success")
			return receipt, fmt.Errorf("%w: artifact %q (bound %d bytes)", ErrTranscriptLimitExceeded, request.TranscriptPolicy.ArtifactId, request.TranscriptPolicy.MaxBytes)
		}
		allocation.staged[request.TranscriptPolicy.ArtifactId] = append([]byte(nil), stdout...)
		receipt.TranscriptDigest = RecomputeSHA256(allocation.staged[request.TranscriptPolicy.ArtifactId])
		receipt.TranscriptStderrDigest = RecomputeSHA256(stderr)
		f.appendLog(allocation, "transcript staged: "+request.TranscriptPolicy.ArtifactId)
	}
	return receipt, nil
}

// Inspect implements SandboxProvider and returns the out-of-band
// observation: recorded violations, bounded log lines, spawn count and the
// last exit code. It never mutates state.
func (f *FakeProvider) Inspect(ctx context.Context, request InspectRequest) (*InspectReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationInspect, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	allocation, ok := f.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.Generation != allocation.meta.Generation {
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", ErrStaleAllocationGeneration, request.Identity.Generation, allocation.meta.Generation)
	}
	return &InspectReport{
		State:      allocation.meta.State,
		ExitCode:   allocation.exitCode,
		Violations: append([]BoundaryViolation(nil), allocation.violations...),
		SpawnCount: allocation.spawnCount,
		LogLines:   append([]string(nil), allocation.log...),
	}, nil
}

// Signal implements SandboxProvider.
func (f *FakeProvider) Signal(ctx context.Context, request SignalRequest) (*SignalReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationSignal, request.Identity); err != nil {
		return nil, err
	}
	if err := request.Signal.Validate(); err != nil {
		return nil, err
	}
	allocation, err := f.activeAllocation(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	f.appendLog(allocation, "signal delivered: "+string(request.Signal))
	return &SignalReceipt{Delivered: true}, nil
}

// Checkpoint implements SandboxProvider with a deterministic snapshot over
// the staged content.
func (f *FakeProvider) Checkpoint(ctx context.Context, request CheckpointRequest) (*CheckpointReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationCheckpoint, request.Identity); err != nil {
		return nil, err
	}
	allocation, err := f.activeAllocation(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	allocation.checkpoints++
	inputIds := make([]string, 0, len(allocation.staged))
	for inputId := range allocation.staged {
		inputIds = append(inputIds, inputId)
	}
	sort.Strings(inputIds)
	var payload []byte
	for _, inputId := range inputIds {
		payload = append(payload, []byte(inputId)...)
		payload = append(payload, allocation.staged[inputId]...)
	}
	receipt := &CheckpointReceipt{
		CheckpointId: fmt.Sprintf("ckpt:%s:%d", allocation.meta.AllocationId, allocation.checkpoints),
		SHA256:       RecomputeSHA256(payload),
		SizeBytes:    int64(len(payload)),
	}
	f.appendLog(allocation, "checkpoint: "+receipt.CheckpointId)
	return receipt, nil
}

// Restore implements SandboxProvider on top of the frozen PlanRestore
// decision logic. The identity must carry the post-restore generation.
func (f *FakeProvider) Restore(ctx context.Context, request RestoreOperationRequest) (*RestoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationRestore, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.PreviousAllocationId) == "" {
		return nil, fmt.Errorf("%w: previousAllocationId must be a non-empty string", ErrInvalidRequest)
	}
	previous, ok := f.allocations[request.PreviousAllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAllocationNotFound, request.PreviousAllocationId)
	}
	if previous.meta.State.IsTerminal() {
		return nil, fmt.Errorf("%w: %q is %s", ErrAllocationNotActive, request.PreviousAllocationId, string(previous.meta.State))
	}
	if request.Identity.AllocationId != request.PreviousAllocationId && request.Identity.AllocationId != request.NextAllocationId {
		return nil, fmt.Errorf("%w: the identity must bind the previous or the next allocation", ErrInvalidRequest)
	}
	next, err := PlanRestore(RestoreRequest{
		Previous:         previous.meta,
		NextAllocationId: request.NextAllocationId,
		InPlaceConfirmed: request.InPlaceConfirmed,
	})
	if err != nil {
		return nil, err
	}
	if request.Identity.Generation != next.Generation {
		return nil, fmt.Errorf("%w: the restore identity must carry the post-restore generation %d", ErrStaleAllocationGeneration, next.Generation)
	}
	next.State = AllocationActive
	existing := f.allocationsFor(next.RunId, next.AttemptId)
	if err := CheckSingleActive(existing, next); err != nil {
		return nil, err
	}
	if !request.InPlaceConfirmed {
		previous.meta.State = AllocationReplaced
	}
	restored := &fakeAllocation{
		meta:   next,
		staged: map[string][]byte{},
	}
	for inputId, content := range previous.staged {
		restored.staged[inputId] = append([]byte(nil), content...)
	}
	f.allocations[next.AllocationId] = restored
	return &RestoreReceipt{Allocation: next}, nil
}

// Terminate implements SandboxProvider and is idempotent on allocations
// that are already terminal.
func (f *FakeProvider) Terminate(ctx context.Context, request TerminateRequest) (*TerminateReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationTerminate, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	allocation, ok := f.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.Generation != allocation.meta.Generation {
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", ErrStaleAllocationGeneration, request.Identity.Generation, allocation.meta.Generation)
	}
	if allocation.meta.State != AllocationTerminated && allocation.meta.State != AllocationReplaced {
		allocation.meta.State = AllocationTerminated
		f.appendLog(allocation, "terminated")
	}
	return &TerminateReceipt{State: allocation.meta.State}, nil
}

// Reconcile implements SandboxProvider and reports the allocation
// bookkeeping of one (runId, attemptId) scope deterministically.
func (f *FakeProvider) Reconcile(ctx context.Context, request ReconcileRequest) (*ReconcileReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.enterOperation(OperationReconcile, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.RunId) == "" || strings.TrimSpace(request.AttemptId) == "" {
		return nil, fmt.Errorf("%w: runId and attemptId must be non-empty strings", ErrInvalidRequest)
	}
	if request.Identity.RunId != request.RunId || request.Identity.AttemptId != request.AttemptId {
		return nil, fmt.Errorf("%w: the identity does not bind the requested scope", ErrInvalidRequest)
	}
	metas := f.allocationsFor(request.RunId, request.AttemptId)
	var currentGeneration int64
	for _, meta := range metas {
		if meta.Generation > currentGeneration {
			currentGeneration = meta.Generation
		}
	}
	report := &ReconcileReport{
		ActiveAllocationIds: []string{},
		OrphanAllocationIds: []string{},
	}
	for _, meta := range metas {
		if meta.State != AllocationActive {
			continue
		}
		if meta.Generation == currentGeneration {
			report.ActiveAllocationIds = append(report.ActiveAllocationIds, meta.AllocationId)
		} else {
			report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
		}
	}
	sort.Strings(report.ActiveAllocationIds)
	sort.Strings(report.OrphanAllocationIds)
	report.DriftDetected = len(report.ActiveAllocationIds) > 1 || len(report.OrphanAllocationIds) > 0
	return report, nil
}
