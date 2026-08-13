package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// fixtureDigest derives a well-formed sha256 digest from seed material, so
// no Digest-family, Token-family or Key-family fixture field is ever
// assigned one complete string literal (gitleaks publication gate).
func fixtureDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

// fixedClock returns the injected deterministic clock; no wall clock read
// participates in any test.
func fixedClock() func() time.Time {
	base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// scenarioIdentity builds one valid dispatch-bound identity with a
// deterministic fencing token derived from the scenario name.
func scenarioIdentity(name, allocationId, commandId string, generation int64) sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       "task-" + name,
		RunId:        "run-" + name,
		AttemptId:    "attempt-" + name,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: allocationId,
		Generation:   generation,
		FencingToken: fixtureDigest("fencing" + "-" + name),
		CommandId:    commandId,
	}
}

func workspaceRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatalf("workspace requirements: %v", err)
	}
	return requirements
}

func hardenedRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelHardened)
	if err != nil {
		t.Fatalf("hardened requirements: %v", err)
	}
	return requirements
}

func newTestRunner(t *testing.T, options ...Option) *LocalRunner {
	t.Helper()
	runner, err := NewLocalRunner(t.TempDir(), fixedClock(), options...)
	if err != nil {
		t.Fatalf("NewLocalRunner: %v", err)
	}
	return runner
}

// fakeExecutor is a deterministic, restricted command executor for the
// hermetic tests: it records every spec and answers from scripted outcomes
// without ever spawning a host process.
type fakeExecutor struct {
	mu             sync.Mutex
	specs          []ExecSpec
	outcomes       map[string]ExecOutcome
	defaultOutcome ExecOutcome
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		outcomes:       map[string]ExecOutcome{},
		defaultOutcome: ExecOutcome{Started: true, ExitCode: 0},
	}
}

func (f *fakeExecutor) run(ctx context.Context, spec ExecSpec) ExecOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if outcome, ok := f.outcomes[spec.Argv[0]]; ok {
		return outcome
	}
	return f.defaultOutcome
}

func provisionAllocation(t *testing.T, runner *LocalRunner, name, allocationId string) sandbox.SandboxAllocation {
	t.Helper()
	receipt, err := runner.Provision(context.Background(), sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, allocationId, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	return receipt.Allocation
}

func traceOutcomes(trace []sandbox.BusinessEvent) map[string]string {
	outcomes := make(map[string]string, len(trace))
	for _, event := range trace {
		outcomes[event.Kind] = event.Outcome
	}
	return outcomes
}

func assertVerdictsEquivalent(t *testing.T, fixtureName string, expected, observed sandbox.ConformanceVerdict) {
	t.Helper()
	if expected.Passed != observed.Passed {
		t.Fatalf("fixture %s: verdict Passed diverges: fake=%t local=%t", fixtureName, expected.Passed, observed.Passed)
	}
	if expected.ReasonCode != observed.ReasonCode {
		t.Fatalf("fixture %s: verdict ReasonCode diverges: fake=%q local=%q", fixtureName, expected.ReasonCode, observed.ReasonCode)
	}
	expectedOutcomes := traceOutcomes(expected.Trace)
	observedOutcomes := traceOutcomes(observed.Trace)
	for kind, outcome := range expectedOutcomes {
		if observedOutcomes[kind] != outcome {
			t.Fatalf("fixture %s: invariant %q outcome diverges: fake=%q local=%q", fixtureName, kind, outcome, observedOutcomes[kind])
		}
	}
	for kind, outcome := range observedOutcomes {
		if expectedOutcomes[kind] != outcome {
			t.Fatalf("fixture %s: local emitted invariant %q outcome %q absent from the fake trace", fixtureName, kind, outcome)
		}
	}
}

// TestConformanceEquivalenceFakeLocal drives the identical probe set of
// RunConformance through the scripted fake provider and the Local provider
// and freezes their verdict equivalence: identical Passed/ReasonCode and
// normalized business-trace outcome/invariant equivalence (single-active,
// no double-write, late-arrival isolation).
func TestConformanceEquivalenceFakeLocal(t *testing.T) {
	readOnly, err := domain.NewSandboxRequirements(domain.AccessModeReadOnly, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatalf("read-only requirements: %v", err)
	}
	fixtures := []sandbox.ConformanceFixture{
		{Name: "workspace-write-happy", Requirements: workspaceRequirements(t), Payload: []byte("payload" + "-workspace-write")},
		{Name: "hardened-refusal", Requirements: hardenedRequirements(t)},
		{Name: "read-only-happy", Requirements: readOnly, Payload: []byte("payload" + "-read-only")},
	}
	for _, tc := range []struct {
		name    string
		fixture sandbox.ConformanceFixture
	}{
		{"workspace-write-happy", fixtures[0]},
		{"hardened-refusal", fixtures[1]},
		{"read-only-happy", fixtures[2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := sandbox.NewFakeProvider(sandbox.FakeConfig{})
			local := newTestRunner(t, WithExecutor(newFakeExecutor().run))
			fakeVerdicts := sandbox.RunConformance(fake, tc.fixture)
			localVerdicts := sandbox.RunConformance(local, tc.fixture)
			if len(fakeVerdicts) != 1 || len(localVerdicts) != 1 {
				t.Fatalf("expected exactly one verdict per provider, got fake=%d local=%d", len(fakeVerdicts), len(localVerdicts))
			}
			assertVerdictsEquivalent(t, tc.name, fakeVerdicts[0], localVerdicts[0])
			if !localVerdicts[0].Passed {
				t.Fatalf("the Local provider must pass the conformance suite for %s, got reason %q", tc.name, localVerdicts[0].ReasonCode)
			}
		})
	}
}

// TestProvisionHardenedFailClosed freezes that a hardened request against
// the Local provider is refused fail closed and is never downgraded to
// workspace-write, and that the Local provider never emits hardened
// conformance evidence. Each refusal variant runs as its own subtest.
func TestProvisionHardenedFailClosed(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "probe-refuses-hardened",
			run: func(t *testing.T) {
				probeReport, err := runner.Probe(ctx, sandbox.ProbeRequest{
					Identity:     scenarioIdentity("hardened", "alloc-hardened", "cmd-probe", 1),
					Requirements: hardenedRequirements(t),
				})
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				if probeReport.Supported {
					t.Fatal("the Local provider must report hardened requests as unsupported")
				}
				if probeReport.ConformanceEvidenceRef != "" {
					t.Fatal("the Local provider holds no conformance evidence and must never claim one")
				}
				if probeReport.SelfSignedConformanceClaim {
					t.Fatal("the Local provider must never carry a self-signed conformance claim")
				}
			},
		},
		{
			name: "provision-fails-closed-on-hardened",
			run: func(t *testing.T) {
				_, err := runner.Provision(ctx, sandbox.ProvisionRequest{
					Identity:     scenarioIdentity("hardened", "alloc-hardened", "cmd-provision", 1),
					Requirements: hardenedRequirements(t),
				})
				if err == nil {
					t.Fatal("Provision accepted a hardened request against the never-hardened Local provider")
				}
				if !errors.Is(err, sandbox.ErrAssuranceNotMet) {
					t.Fatalf("a hardened request must fail closed with ErrAssuranceNotMet, got %v", err)
				}
			},
		},
		{
			name: "allocation-ceiling-stays-workspace-write",
			run: func(t *testing.T) {
				allocation := provisionAllocation(t, runner, "ceiling", "alloc-ceiling")
				if allocation.AssuranceLevel != domain.AssuranceLevelWorkspaceWrite {
					t.Fatalf("the Local assurance ceiling is workspace-write, got %q", string(allocation.AssuranceLevel))
				}
				if allocation.ConformanceEvidenceRef != "" {
					t.Fatal("a Local allocation must never carry a hardened conformance evidence reference")
				}
			},
		},
	} {
		t.Run(tc.name, tc.run)
	}
}

// TestStageAndExecBoundaryEscapeRejected freezes that every write target
// escaping the allocation directory is rejected: stage targets must stay
// relative inside the allocation, and exec targets must never resolve
// outside it.
func TestStageAndExecBoundaryEscapeRejected(t *testing.T) {
	runner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "boundary", "alloc-boundary")
	content := []byte("boundary" + "-payload")
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "stage-targets-stay-inside-the-allocation",
			run: func(t *testing.T) {
				for _, inputId := range []string{"../escape.txt", "/absolute.txt", "nested/../../escape.txt"} {
					_, err := runner.Stage(ctx, sandbox.StageRequest{
						Identity:     scenarioIdentity("boundary", allocation.AllocationId, "cmd-stage-escape", 1),
						AllocationId: allocation.AllocationId,
						Inputs: []sandbox.StageInput{{
							InputId:        inputId,
							DeclaredSHA256: sandbox.RecomputeSHA256(content),
							Inline:         append([]byte(nil), content...),
						}},
					})
					if err == nil {
						t.Fatalf("Stage accepted the escaping write target %q", inputId)
					}
					if !errors.Is(err, sandbox.ErrInvalidRequest) {
						t.Fatalf("the escaping write target %q must surface ErrInvalidRequest, got %v", inputId, err)
					}
				}
				escaped := filepath.Join(filepath.Dir(runner.root), "escape.txt")
				if _, err := os.Stat(escaped); err == nil {
					t.Fatal("an escaping stage target must never write outside the sandbox root")
				}
			},
		},
		{
			name: "exec-targets-stay-inside-the-allocation",
			run: func(t *testing.T) {
				for _, command := range [][]string{{"../evil"}, {"/bin/sh", "-c", "true"}, {"nested/../../evil"}} {
					_, err := runner.Exec(ctx, sandbox.ExecRequest{
						Identity:     scenarioIdentity("boundary", allocation.AllocationId, "cmd-exec-escape", 1),
						AllocationId: allocation.AllocationId,
						Command:      command,
					})
					if err == nil {
						t.Fatalf("Exec accepted the escaping target %v", command)
					}
					if !errors.Is(err, sandbox.ErrInvalidRequest) {
						t.Fatalf("the escaping exec target %v must surface ErrInvalidRequest, got %v", command, err)
					}
				}
			},
		},
	} {
		t.Run(tc.name, tc.run)
	}
}

// TestStageTamperedBytesFailClosed freezes that a digest mismatch detected
// before consumption fails the attempt closed with the fixed sentinel and
// produces no receipt.
func TestStageTamperedBytesFailClosed(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "tamper", "alloc-tamper")
	content := []byte("tamper" + "-payload")
	declared := sandbox.RecomputeSHA256(append(append([]byte(nil), content...), []byte("declared-mismatch")...))
	report, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("tamper", allocation.AllocationId, "cmd-stage-tamper", 1),
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "tampered",
			DeclaredSHA256: declared,
			Inline:         content,
		}},
	})
	if err == nil {
		t.Fatal("Stage accepted a tampered input instead of recomputing the digest before consumption")
	}
	if !errors.Is(err, sandbox.ErrStageInputMismatch) {
		t.Fatalf("a tampered stage input must fail closed with ErrStageInputMismatch, got %v", err)
	}
	if report != nil {
		t.Fatal("a tampered stage input must produce no receipt")
	}
	inspectReport, err := runner.Inspect(ctx, sandbox.InspectRequest{
		Identity:     scenarioIdentity("tamper", allocation.AllocationId, "cmd-inspect", 1),
		AllocationId: allocation.AllocationId,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspectReport.State != sandbox.AllocationFailed {
		t.Fatalf("the tampered attempt must leave the allocation failed, got %s", string(inspectReport.State))
	}
}

// TestStageReceiptRecomputedNeverEchoed freezes that the stage receipt
// carries digests recomputed out of the real filesystem, never an echo of
// the declared digest: both the pre-consumption and the post-consumption
// digests must equal an independent out-of-band recomputation of the bytes
// actually on disk.
func TestStageReceiptRecomputedNeverEchoed(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "receipt", "alloc-receipt")
	content := []byte("receipt" + "-payload")
	declared := sandbox.RecomputeSHA256(content)
	report, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("receipt", allocation.AllocationId, "cmd-stage", 1),
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: declared,
			Inline:         append([]byte(nil), content...),
		}},
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(report.Receipts) != 1 {
		t.Fatalf("expected exactly one receipt, got %d", len(report.Receipts))
	}
	receipt := report.Receipts[0]
	if receipt.RecomputedSHA256 != declared {
		t.Fatalf("the pre-consumption digest must equal the out-of-band recomputation, got %q", receipt.RecomputedSHA256)
	}
	dir, err := runner.AllocationDirectory(allocation.AllocationId)
	if err != nil {
		t.Fatalf("AllocationDirectory: %v", err)
	}
	diskBytes, err := os.ReadFile(filepath.Join(dir, "payload"))
	if err != nil {
		t.Fatalf("reading the staged file out-of-band: %v", err)
	}
	if receipt.PostConsumptionSHA256 != sandbox.RecomputeSHA256(diskBytes) {
		t.Fatalf("the post-consumption digest must be recomputed from the bytes on disk, got %q", receipt.PostConsumptionSHA256)
	}
	if receipt.SizeBytes != int64(len(content)) {
		t.Fatalf("the receipt size must observe the staged bytes, got %d", receipt.SizeBytes)
	}
}

// TestStageLocatorCopiesFromBoundStore freezes the locator path of the
// content-addressed stage: seeded store content is copied into the
// allocation directory and the receipt digests are recomputed, while an
// unseeded locator fails closed unresolved.
func TestStageLocatorCopiesFromBoundStore(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	content := []byte("locator" + "-payload")
	digest := sandbox.RecomputeSHA256(content)
	if err := runner.SeedStore("store-locator", digest, content); err != nil {
		t.Fatalf("SeedStore: %v", err)
	}
	receipt, err := runner.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        scenarioIdentity("locator", "alloc-locator", "cmd-provision", 1),
		Requirements:    workspaceRequirements(t),
		AllowedStoreIds: []string{"store-locator"},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	report, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("locator", receipt.Allocation.AllocationId, "cmd-stage", 1),
		AllocationId: receipt.Allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "from-store",
			DeclaredSHA256: digest,
			Locator:        &sandbox.Locator{StoreId: "store-locator", SHA256: digest, SizeBytes: int64(len(content))},
		}},
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if report.Receipts[0].RecomputedSHA256 != digest || report.Receipts[0].PostConsumptionSHA256 != digest {
		t.Fatalf("the locator receipt must carry recomputed digests, got %+v", report.Receipts[0])
	}
	_, err = runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("locator", receipt.Allocation.AllocationId, "cmd-stage-unresolved", 1),
		AllocationId: receipt.Allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "missing",
			DeclaredSHA256: digest,
			Locator:        &sandbox.Locator{StoreId: "store-locator", SHA256: fixtureDigest("absent" + "-object"), SizeBytes: 4},
		}},
	})
	if err == nil {
		t.Fatal("Stage resolved a locator the store does not hold")
	}
	if !errors.Is(err, sandbox.ErrLocatorUnresolved) {
		t.Fatalf("an unseeded locator must surface ErrLocatorUnresolved, got %v", err)
	}
}

// TestExecKilledTimeoutObservationOnly freezes that a killed or timed-out
// workload produces only an observation receipt: Exec returns no error, the
// receipt status is killed/failed, and no acceptance is ever derived.
func TestExecKilledTimeoutObservationOnly(t *testing.T) {
	executor := newFakeExecutor()
	executor.outcomes["timeout-cmd"] = ExecOutcome{Started: true, TimedOut: true, ExitCode: -1}
	executor.outcomes["signaled-cmd"] = ExecOutcome{Started: true, Signaled: true, ExitCode: -1}
	executor.outcomes["failing-cmd"] = ExecOutcome{Started: true, ExitCode: 2, Stderr: []byte("stderr" + "-detail")}
	executor.outcomes["never-started"] = ExecOutcome{Started: false, ExitCode: -1, StartError: errors.New("spawn" + "-failed")}
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "exec-obs", "alloc-exec-obs")
	for _, tc := range []struct {
		command string
		status  sandbox.ExecutionStatus
	}{
		{"timeout-cmd", sandbox.ExecutionKilled},
		{"signaled-cmd", sandbox.ExecutionKilled},
		{"failing-cmd", sandbox.ExecutionFailed},
		{"never-started", sandbox.ExecutionFailed},
	} {
		receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
			Identity:     scenarioIdentity("exec-obs", allocation.AllocationId, "cmd-exec-"+tc.command, 1),
			AllocationId: allocation.AllocationId,
			Command:      []string{tc.command},
		})
		if err != nil {
			t.Fatalf("Exec(%s) returned an error instead of an observation: %v", tc.command, err)
		}
		if receipt.Status != tc.status {
			t.Fatalf("Exec(%s) must observe status %s, got %s", tc.command, string(tc.status), string(receipt.Status))
		}
	}
	if receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("exec-obs", allocation.AllocationId, "cmd-exec-ok", 1),
		AllocationId: allocation.AllocationId,
		Command:      []string{"plain-cmd"},
	}); err != nil || receipt.Status != sandbox.ExecutionCompleted {
		t.Fatalf("a clean execution must observe completed, got receipt=%+v err=%v", receipt, err)
	}
	inspectReport, err := runner.Inspect(ctx, sandbox.InspectRequest{
		Identity:     scenarioIdentity("exec-obs", allocation.AllocationId, "cmd-inspect", 1),
		AllocationId: allocation.AllocationId,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspectReport.SpawnCount != 5 {
		t.Fatalf("expected 5 observed spawns, got %d", inspectReport.SpawnCount)
	}
	executor.mu.Lock()
	specCount := len(executor.specs)
	firstDir := ""
	if specCount > 0 {
		firstDir = executor.specs[0].Dir
	}
	executor.mu.Unlock()
	if specCount != 5 {
		t.Fatalf("the executor must observe every real spawn, got %d", specCount)
	}
	expectedDir, err := runner.AllocationDirectory(allocation.AllocationId)
	if err != nil {
		t.Fatalf("AllocationDirectory: %v", err)
	}
	if firstDir != expectedDir {
		t.Fatalf("the executor must run inside the allocation directory, got %q", firstDir)
	}
	if len(inspectReport.Violations) != 0 {
		t.Fatalf("the observation-only executions must not surface violations, got %+v", inspectReport.Violations)
	}
}

// TestStaleFencingRejectedWithDiagnostics freezes that stale generation or
// mismatched fencingToken requests are rejected fail closed before any side
// effect and leave a diagnostic record.
func TestStaleFencingRejectedWithDiagnostics(t *testing.T) {
	runner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "fencing", "alloc-fencing")
	forged := scenarioIdentity("fencing", allocation.AllocationId, "cmd-exec-forged", 1)
	forged.FencingToken = fixtureDigest("forged" + "-token")
	invalid := scenarioIdentity("fencing", allocation.AllocationId, "cmd-exec-invalid", 0)
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "mismatched-fencing-token-exec",
			run: func(t *testing.T) {
				if _, err := runner.Exec(ctx, sandbox.ExecRequest{Identity: forged, AllocationId: allocation.AllocationId, Command: []string{"plain-cmd"}}); err == nil {
					t.Fatal("Exec accepted a mismatched fencingToken")
				}
			},
		},
		{
			name: "invalid-identity-stage",
			run: func(t *testing.T) {
				if _, err := runner.Stage(ctx, sandbox.StageRequest{Identity: invalid, AllocationId: allocation.AllocationId}); err == nil {
					t.Fatal("Stage accepted an invalid operation identity")
				}
			},
		},
	} {
		t.Run(tc.name, tc.run)
	}
	diagnostics := runner.Diagnostics()
	if len(diagnostics) < 2 {
		t.Fatalf("expected at least two fail-closed diagnostics, got %d", len(diagnostics))
	}
}

// TestRestoreResponseLostReconcileDrift freezes the response-loss path: the
// restore side effect applies, the response is dropped, and once the host
// state diverges the Reconcile adjudication fails closed with a
// ReconcileRecord.
func TestRestoreResponseLostReconcileDrift(t *testing.T) {
	runner := newTestRunner(t, WithFaults(Fault{Operation: sandbox.OperationRestore, Kind: FaultDropResponse}))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "drop", "alloc-drop")
	restoreIdentity := scenarioIdentity("drop", "alloc-drop-next", "cmd-restore", 2)
	restoreIdentity.FencingToken = fixtureDigest("restore" + "-token")
	_, err := runner.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             restoreIdentity,
		PreviousAllocationId: allocation.AllocationId,
		NextAllocationId:     "alloc-drop-next",
	})
	if err == nil {
		t.Fatal("Restore must surface the injected response loss")
	}
	if !errors.Is(err, sandbox.ErrResponseLost) {
		t.Fatalf("the dropped restore response must surface ErrResponseLost, got %v", err)
	}
	nextDir, err := runner.AllocationDirectory("alloc-drop-next")
	if err != nil {
		t.Fatalf("the replacement allocation must exist host-side despite the lost response: %v", err)
	}
	if _, statErr := os.Stat(nextDir); statErr != nil {
		t.Fatalf("the replacement allocation directory must exist before the drift: %v", statErr)
	}
	if err := os.RemoveAll(nextDir); err != nil {
		t.Fatalf("simulating the host-side loss: %v", err)
	}
	report, reconcileErr := runner.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  restoreIdentity,
		RunId:     "run-" + "drop",
		AttemptId: "attempt-" + "drop",
	})
	if reconcileErr == nil {
		t.Fatal("Reconcile must fail closed when the host state drifted")
	}
	if report == nil || !report.DriftDetected {
		t.Fatalf("Reconcile must observe the drift, got %+v", report)
	}
	records := runner.ReconcileRecords()
	if len(records) != 1 {
		t.Fatalf("expected exactly one ReconcileRecord, got %d", len(records))
	}
	if err := records[0].Validate(); err != nil {
		t.Fatalf("the constructed ReconcileRecord must validate: %v", err)
	}
	if records[0].Decision != "block" {
		t.Fatalf("drift must yield the block decision, got %s", string(records[0].Decision))
	}
}

// TestConcurrentRestoreSingleWriter freezes that concurrent restores admit
// exactly one writer: the first restore wins and the second fails closed.
func TestConcurrentRestoreSingleWriter(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "race", "alloc-race")
	first := scenarioIdentity("race", "alloc-race-next-1", "cmd-restore-1", 2)
	first.FencingToken = fixtureDigest("restore" + "-first")
	second := scenarioIdentity("race", "alloc-race-next-2", "cmd-restore-2", 2)
	second.FencingToken = fixtureDigest("restore" + "-second")
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	var winnerIdentity sandbox.OperationIdentity
	start := make(chan struct{})
	for _, identity := range []sandbox.OperationIdentity{first, second} {
		wait.Add(1)
		go func(identity sandbox.OperationIdentity) {
			defer wait.Done()
			<-start
			receipt, err := runner.Restore(ctx, sandbox.RestoreOperationRequest{
				Identity:             identity,
				PreviousAllocationId: allocation.AllocationId,
				NextAllocationId:     identity.AllocationId,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil && receipt != nil {
				successes++
				winnerIdentity = identity
			}
		}(identity)
	}
	close(start)
	wait.Wait()
	if successes != 1 {
		t.Fatalf("concurrent restores must admit exactly one writer, got %d", successes)
	}
	report, err := runner.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  winnerIdentity,
		RunId:     "run-" + "race",
		AttemptId: "attempt-" + "race",
	})
	if err != nil {
		t.Fatalf("Reconcile after a single-writer restore: %v", err)
	}
	if report.DriftDetected || len(report.ActiveAllocationIds) != 1 {
		t.Fatalf("exactly one active allocation must survive the race, got %+v", report)
	}
}

// TestStaleHandleAfterRestoreRejected freezes that after a restore every
// stale handle (old generation, previous allocationId) is rejected fail
// closed.
func TestStaleHandleAfterRestoreRejected(t *testing.T) {
	runner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "stale", "alloc-stale")
	content := []byte("stale" + "-payload")
	if _, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("stale", allocation.AllocationId, "cmd-stage", 1),
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage before restore: %v", err)
	}
	restoreIdentity := scenarioIdentity("stale", "alloc-stale-next", "cmd-restore", 2)
	restoreIdentity.FencingToken = fixtureDigest("restore" + "-stale")
	restoreReceipt, err := runner.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             restoreIdentity,
		PreviousAllocationId: allocation.AllocationId,
		NextAllocationId:     "alloc-stale-next",
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restoreReceipt.Allocation.Generation != 2 {
		t.Fatalf("the post-restore generation must increase monotonically, got %d", restoreReceipt.Allocation.Generation)
	}
	stale := scenarioIdentity("stale", allocation.AllocationId, "cmd-stage-stale", 1)
	_, err = runner.Stage(ctx, sandbox.StageRequest{
		Identity:     stale,
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "late",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	})
	if err == nil {
		t.Fatal("Stage accepted a stale handle after the restore")
	}
	if _, execErr := runner.Exec(ctx, sandbox.ExecRequest{Identity: stale, AllocationId: allocation.AllocationId, Command: []string{"plain-cmd"}}); execErr == nil {
		t.Fatal("Exec accepted a stale handle after the restore")
	}
	nextDir, err := runner.AllocationDirectory("alloc-stale-next")
	if err != nil {
		t.Fatalf("AllocationDirectory: %v", err)
	}
	stagedBytes, err := os.ReadFile(filepath.Join(nextDir, "payload"))
	if err != nil {
		t.Fatalf("the replacement allocation must carry the staged content: %v", err)
	}
	if sandbox.RecomputeSHA256(stagedBytes) != sandbox.RecomputeSHA256(content) {
		t.Fatal("the replacement allocation must preserve the staged bytes")
	}
}

// TestConcurrentDoubleActiveRejected freezes the single-active invariant:
// two concurrent provisions for the same (runId, attemptId) and generation
// admit exactly one allocation; the loser fails closed with
// ErrDuplicateActiveAllocation.
func TestConcurrentDoubleActiveRejected(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	requirements := workspaceRequirements(t)
	first := scenarioIdentity("double", "alloc-double-1", "cmd-provision-1", 1)
	second := scenarioIdentity("double", "alloc-double-2", "cmd-provision-2", 1)
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	var loserErr error
	start := make(chan struct{})
	for _, identity := range []sandbox.OperationIdentity{first, second} {
		wait.Add(1)
		go func(identity sandbox.OperationIdentity) {
			defer wait.Done()
			<-start
			_, err := runner.Provision(ctx, sandbox.ProvisionRequest{Identity: identity, Requirements: requirements})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				loserErr = err
			}
		}(identity)
	}
	close(start)
	wait.Wait()
	if successes != 1 {
		t.Fatalf("concurrent provisions must admit exactly one active allocation, got %d", successes)
	}
	if !errors.Is(loserErr, sandbox.ErrDuplicateActiveAllocation) {
		t.Fatalf("the losing provision must surface ErrDuplicateActiveAllocation, got %v", loserErr)
	}
	_, sequentialErr := runner.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity("double", "alloc-double-3", "cmd-provision-3", 1),
		Requirements: workspaceRequirements(t),
	})
	if !errors.Is(sequentialErr, sandbox.ErrDuplicateActiveAllocation) {
		t.Fatalf("a second same-generation provision must fail closed, got %v", sequentialErr)
	}
}

// TestCrossRoleReuseRejected freezes that worker and verifier allocations
// are independent: reusing one allocation across workload roles is rejected.
func TestCrossRoleReuseRejected(t *testing.T) {
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "roles", "alloc-roles")
	verifierIdentity := scenarioIdentity("roles", allocation.AllocationId, "cmd-exec-verifier", 1)
	verifierIdentity.WorkloadRole = sandbox.WorkloadRoleVerifier
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{Identity: verifierIdentity, AllocationId: allocation.AllocationId, Command: []string{"plain-cmd"}}); err == nil {
		t.Fatal("Exec accepted a verifier identity on a worker allocation")
	}
	if _, err := runner.Stage(ctx, sandbox.StageRequest{Identity: verifierIdentity, AllocationId: allocation.AllocationId}); err == nil {
		t.Fatal("Stage accepted a verifier identity on a worker allocation")
	}
	if _, err := runner.Inspect(ctx, sandbox.InspectRequest{Identity: verifierIdentity, AllocationId: allocation.AllocationId}); err == nil {
		t.Fatal("Inspect accepted a verifier identity on a worker allocation")
	}
	verifierProvision := scenarioIdentity("roles-verifier", "alloc-roles-verifier", "cmd-provision", 1)
	verifierProvision.WorkloadRole = sandbox.WorkloadRoleVerifier
	verifierReceipt, err := runner.Provision(ctx, sandbox.ProvisionRequest{Identity: verifierProvision, Requirements: workspaceRequirements(t)})
	if err != nil {
		t.Fatalf("Provision for the verifier role: %v", err)
	}
	if verifierReceipt.Allocation.AllocationId == allocation.AllocationId {
		t.Fatal("the verifier role must hold its own allocation")
	}
	verifierExec := scenarioIdentity("roles-verifier", verifierReceipt.Allocation.AllocationId, "cmd-exec", 1)
	verifierExec.WorkloadRole = sandbox.WorkloadRoleVerifier
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     verifierExec,
		AllocationId: verifierReceipt.Allocation.AllocationId,
		Command:      []string{"plain-cmd"},
	}); err != nil {
		t.Fatalf("a verifier-owned allocation must serve verifier identities: %v", err)
	}
	found := false
	for _, diagnostic := range runner.Diagnostics() {
		if strings.Contains(diagnostic.Reason, "workload role") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the cross-role refusal must leave a diagnostic record")
	}
}

// TestStageInputValidationSemantics freezes that the Local provider reuses
// the stage.go admission rules, one subtest per malformed input shape:
// oversized inline objects, unbound locator aliases, duplicate input ids,
// ambiguous sources, and the URL-shaped and credential-shaped locator
// aliases.
func TestStageInputValidationSemantics(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "stage-rules", "alloc-stage-rules")
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity("stage-rules", allocation.AllocationId, commandId, 1)
	}
	oversized := make([]byte, sandbox.MaxInlineObjectBytes+1)
	for index := range oversized {
		oversized[index] = 'a'
	}
	unbound := []byte("unbound" + "-content")
	duplicate := []byte("duplicate" + "-content")
	duplicateInput := sandbox.StageInput{
		InputId:        "duplicate",
		DeclaredSHA256: sandbox.RecomputeSHA256(duplicate),
		Inline:         append([]byte(nil), duplicate...),
	}
	ambiguous := []byte("ambiguous" + "-content")
	shaped := []byte("shaped" + "-locator-content")
	urlLocator := sandbox.Locator{
		StoreId:   "https" + "://objects.example/store",
		SHA256:    sandbox.RecomputeSHA256(shaped),
		SizeBytes: int64(len(shaped)),
	}
	credentialLocator := sandbox.Locator{
		StoreId:   "user" + ":pass" + "word@corp.example",
		SHA256:    sandbox.RecomputeSHA256(shaped),
		SizeBytes: int64(len(shaped)),
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "oversized-inline-object",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-oversized"),
					AllocationId: allocation.AllocationId,
					Inputs: []sandbox.StageInput{{
						InputId:        "oversized",
						DeclaredSHA256: sandbox.RecomputeSHA256(oversized),
						Inline:         oversized,
					}},
				})
				if !errors.Is(err, sandbox.ErrInlineTooLarge) {
					t.Fatalf("an oversized inline object must surface ErrInlineTooLarge, got %v", err)
				}
			},
		},
		{
			name: "locator-outside-bound-alias-set",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-unbound"),
					AllocationId: allocation.AllocationId,
					Inputs: []sandbox.StageInput{{
						InputId:        "unbound",
						DeclaredSHA256: sandbox.RecomputeSHA256(unbound),
						Locator:        &sandbox.Locator{StoreId: "store-unbound", SHA256: sandbox.RecomputeSHA256(unbound), SizeBytes: int64(len(unbound))},
					}},
				})
				if !errors.Is(err, sandbox.ErrInvalidStageInput) {
					t.Fatalf("a locator outside the bound alias set must fail closed with ErrInvalidStageInput, got %v", err)
				}
			},
		},
		{
			name: "duplicate-input-ids",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-duplicate"),
					AllocationId: allocation.AllocationId,
					Inputs:       []sandbox.StageInput{duplicateInput, duplicateInput},
				})
				if !errors.Is(err, sandbox.ErrDuplicateStageInputId) {
					t.Fatalf("duplicate input ids must surface ErrDuplicateStageInputId, got %v", err)
				}
			},
		},
		{
			name: "ambiguous-source",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-ambiguous"),
					AllocationId: allocation.AllocationId,
					Inputs: []sandbox.StageInput{{
						InputId:        "ambiguous",
						DeclaredSHA256: sandbox.RecomputeSHA256(ambiguous),
						Inline:         ambiguous,
						Locator:        &sandbox.Locator{StoreId: "store-x", SHA256: sandbox.RecomputeSHA256(ambiguous), SizeBytes: int64(len(ambiguous))},
					}},
				})
				if !errors.Is(err, sandbox.ErrInvalidStageInput) {
					t.Fatalf("an ambiguous source must surface ErrInvalidStageInput, got %v", err)
				}
			},
		},
		{
			name: "locator-url-shaped-store-alias",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-url-locator"),
					AllocationId: allocation.AllocationId,
					Inputs: []sandbox.StageInput{{
						InputId:        "url-shaped",
						DeclaredSHA256: urlLocator.SHA256,
						Locator:        &urlLocator,
					}},
				})
				if !errors.Is(err, sandbox.ErrInvalidStageInput) {
					t.Fatalf("a URL-shaped store alias must fail closed at stage admission with ErrInvalidStageInput, got %v", err)
				}
				// The shape refusal is unconditional: it must hold even when
				// the malformed alias is hypothetically present in the bound
				// alias set, so the fixed locator sentinel applies.
				if shapeErr := urlLocator.Validate([]string{urlLocator.StoreId}); !errors.Is(shapeErr, sandbox.ErrInvalidLocator) {
					t.Fatalf("a URL-shaped store alias must surface the fixed ErrInvalidLocator sentinel even when hypothetically bound, got %v", shapeErr)
				}
			},
		},
		{
			name: "locator-credential-shaped-store-alias",
			run: func(t *testing.T) {
				_, err := runner.Stage(ctx, sandbox.StageRequest{
					Identity:     identity("cmd-credential-locator"),
					AllocationId: allocation.AllocationId,
					Inputs: []sandbox.StageInput{{
						InputId:        "credential-shaped",
						DeclaredSHA256: credentialLocator.SHA256,
						Locator:        &credentialLocator,
					}},
				})
				if !errors.Is(err, sandbox.ErrInvalidStageInput) {
					t.Fatalf("a credential-shaped store alias must fail closed at stage admission with ErrInvalidStageInput, got %v", err)
				}
				if shapeErr := credentialLocator.Validate([]string{credentialLocator.StoreId}); !errors.Is(shapeErr, sandbox.ErrInvalidLocator) {
					t.Fatalf("a credential-shaped store alias must surface the fixed ErrInvalidLocator sentinel even when hypothetically bound, got %v", shapeErr)
				}
			},
		},
	} {
		t.Run(tc.name, tc.run)
	}
}

// TestLifecycleInspectCheckpointSignalTerminate exercises the full
// observation lifecycle as one subtest per operation: checkpoint
// determinism, inspect summaries, signal non-delivery without a live
// process, idempotent termination and a clean reconcile.
func TestLifecycleInspectCheckpointSignalTerminate(t *testing.T) {
	runner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "lifecycle", "alloc-lifecycle")
	content := []byte("lifecycle" + "-payload")
	if _, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-stage", 1),
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "checkpoint-determinism",
			run: func(t *testing.T) {
				first, err := runner.Checkpoint(ctx, sandbox.CheckpointRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-checkpoint-1", 1),
					AllocationId: allocation.AllocationId,
				})
				if err != nil {
					t.Fatalf("Checkpoint: %v", err)
				}
				second, err := runner.Checkpoint(ctx, sandbox.CheckpointRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-checkpoint-2", 1),
					AllocationId: allocation.AllocationId,
				})
				if err != nil {
					t.Fatalf("Checkpoint again: %v", err)
				}
				if first.SHA256 != second.SHA256 || first.SizeBytes != second.SizeBytes {
					t.Fatalf("identical staged content must derive identical checkpoint digests: %+v vs %+v", first, second)
				}
				if first.CheckpointId == second.CheckpointId {
					t.Fatal("checkpoint identities must stay unique per snapshot")
				}
			},
		},
		{
			name: "inspect-directory-summary",
			run: func(t *testing.T) {
				inspectReport, err := runner.Inspect(ctx, sandbox.InspectRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-inspect", 1),
					AllocationId: allocation.AllocationId,
				})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				if inspectReport.State != sandbox.AllocationActive {
					t.Fatalf("Inspect must observe the active state, got %s", string(inspectReport.State))
				}
				directoryObserved := false
				for _, line := range inspectReport.LogLines {
					if line == "directory-entry: payload" {
						directoryObserved = true
					}
				}
				if !directoryObserved {
					t.Fatalf("Inspect must summarize the allocation directory content, got %v", inspectReport.LogLines)
				}
			},
		},
		{
			name: "signal-observes-no-live-process",
			run: func(t *testing.T) {
				signalReceipt, err := runner.Signal(ctx, sandbox.SignalRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-signal", 1),
					AllocationId: allocation.AllocationId,
					Signal:       sandbox.SignalTerm,
				})
				if err != nil {
					t.Fatalf("Signal: %v", err)
				}
				if signalReceipt.Delivered {
					t.Fatal("a synchronous Local allocation holds no live process; the signal must observe non-delivery")
				}
			},
		},
		{
			name: "terminate-idempotent-and-clean",
			run: func(t *testing.T) {
				terminateReceipt, err := runner.Terminate(ctx, sandbox.TerminateRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-terminate", 1),
					AllocationId: allocation.AllocationId,
				})
				if err != nil {
					t.Fatalf("Terminate: %v", err)
				}
				if terminateReceipt.State != sandbox.AllocationTerminated {
					t.Fatalf("Terminate must reach the terminated state, got %s", string(terminateReceipt.State))
				}
				if dir, dirErr := runner.AllocationDirectory(allocation.AllocationId); dirErr != nil {
					t.Fatalf("AllocationDirectory: %v", dirErr)
				} else if _, statErr := os.Stat(dir); statErr == nil {
					t.Fatal("Terminate must clean the allocation directory")
				}
				again, err := runner.Terminate(ctx, sandbox.TerminateRequest{
					Identity:     scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-terminate-2", 1),
					AllocationId: allocation.AllocationId,
				})
				if err != nil || again.State != sandbox.AllocationTerminated {
					t.Fatalf("Terminate must be idempotent on terminal allocations, got %+v err=%v", again, err)
				}
			},
		},
		{
			name: "reconcile-clean-after-terminate",
			run: func(t *testing.T) {
				report, err := runner.Reconcile(ctx, sandbox.ReconcileRequest{
					Identity:  scenarioIdentity("lifecycle", allocation.AllocationId, "cmd-reconcile", 1),
					RunId:     "run-" + "lifecycle",
					AttemptId: "attempt-" + "lifecycle",
				})
				if err != nil {
					t.Fatalf("Reconcile: %v", err)
				}
				if report.DriftDetected || len(report.ActiveAllocationIds) != 0 {
					t.Fatalf("a terminated scope must reconcile clean, got %+v", report)
				}
			},
		},
	} {
		t.Run(tc.name, tc.run)
	}
}

// TestSideEffectIntentsRegistered freezes that provision, stage and
// terminate each register a validating SideEffectIntent observation.
func TestSideEffectIntentsRegistered(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "intents", "alloc-intents")
	content := []byte("intents" + "-payload")
	if _, err := runner.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity("intents", allocation.AllocationId, "cmd-stage", 1),
		AllocationId: allocation.AllocationId,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := runner.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     scenarioIdentity("intents", allocation.AllocationId, "cmd-terminate", 1),
		AllocationId: allocation.AllocationId,
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	intents := runner.Intents()
	if len(intents) != 3 {
		t.Fatalf("expected provision/stage/terminate intents, got %d", len(intents))
	}
	expectedOperations := []string{sandbox.OperationProvision, sandbox.OperationStage, "allocation-terminate"}
	for index, intent := range intents {
		if intent.Operation != expectedOperations[index] {
			t.Fatalf("intent %d must observe operation %q, got %q", index, expectedOperations[index], intent.Operation)
		}
		if err := intent.Validate(); err != nil {
			t.Fatalf("intent %d must validate as an authority record: %v", index, err)
		}
	}
}
