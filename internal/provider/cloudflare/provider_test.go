package cloudflare

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// TestProviderFullTenOperationHappyPath drives the full ten-operation
// chain — Probe, Provision, Stage, Exec, Inspect, Signal, Checkpoint,
// Restore, Terminate, Reconcile — through the Bridge provider against the
// fake Bridge fixture.
func TestProviderFullTenOperationHappyPath(t *testing.T) {
	name := "chain"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	id := func(allocationId, commandId string, generation int64) sandbox.OperationIdentity {
		return scenarioIdentity(name, allocationId, commandId, generation)
	}

	probeReport, err := provider.Probe(ctx, sandbox.ProbeRequest{
		Identity:     id(alloc, "cmd-probe", 1),
		Requirements: workspaceRequirements(t),
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !probeReport.Supported || probeReport.ConformanceEvidenceRef != "" || probeReport.SelfSignedConformanceClaim {
		t.Fatalf("unexpected probe report: %+v", probeReport)
	}

	provisionReceipt, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        id(alloc, "cmd-provision", 1),
		Requirements:    workspaceRequirements(t),
		AllowedStoreIds: []string{"store-" + name},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if provisionReceipt.Allocation.AllocationId != alloc || provisionReceipt.Allocation.Generation != 1 || provisionReceipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("unexpected provision receipt: %+v", provisionReceipt.Allocation)
	}

	inlineContent := []byte("inline" + "-content-" + name)
	storeContent := []byte("store" + "-content-" + name)
	storeDigest := sandbox.RecomputeSHA256(storeContent)
	fb.SeedStore("store-"+name, storeDigest, storeContent)
	stageReport, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     id(alloc, "cmd-stage", 1),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{
			{
				InputId:        "payload",
				DeclaredSHA256: sandbox.RecomputeSHA256(inlineContent),
				Inline:         append([]byte(nil), inlineContent...),
			},
			{
				InputId:        "ref",
				DeclaredSHA256: storeDigest,
				Locator:        &sandbox.Locator{StoreId: "store-" + name, SHA256: storeDigest, SizeBytes: int64(len(storeContent))},
			},
		},
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(stageReport.Receipts) != 2 {
		t.Fatalf("one receipt per staged input was expected, got %d", len(stageReport.Receipts))
	}
	for _, receipt := range stageReport.Receipts {
		if receipt.RecomputedSHA256 != receipt.PostConsumptionSHA256 {
			t.Fatalf("the recomputed digests must agree for input %q: %+v", receipt.InputId, receipt)
		}
	}
	if stageReport.Receipts[0].RecomputedSHA256 != sandbox.RecomputeSHA256(inlineContent) {
		t.Fatalf("the inline receipt must carry the recomputed digest: %+v", stageReport.Receipts[0])
	}
	if stageReport.Receipts[1].RecomputedSHA256 != storeDigest || stageReport.Receipts[1].SizeBytes != int64(len(storeContent)) {
		t.Fatalf("the locator receipt must carry the recomputed digest and size: %+v", stageReport.Receipts[1])
	}

	execReceipt, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     id(alloc, "cmd-exec", 1),
		AllocationId: alloc,
		Command:      []string{"echo", "bridge"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	expectedStdout := "exec stdout\x00" + "echo" + "\x00" + "bridge"
	if execReceipt.Status != sandbox.ExecutionCompleted || execReceipt.ExitCode != 0 {
		t.Fatalf("the exec receipt must observe a clean completion, got %+v", execReceipt)
	}
	if execReceipt.StdoutSHA256 != sandbox.RecomputeSHA256([]byte(expectedStdout)) {
		t.Fatalf("the stdout digest must be the recomputation of the streamed bytes: %+v", execReceipt)
	}

	inspectReport, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     id(alloc, "cmd-inspect", 1),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspectReport.State != sandbox.AllocationActive || len(inspectReport.Violations) != 0 || len(inspectReport.LogLines) == 0 {
		t.Fatalf("the observation must reflect the contained execution, got %+v", inspectReport)
	}

	fb.SetLiveSessions(t, alloc, 1)
	signalReceipt, err := provider.Signal(ctx, sandbox.SignalRequest{
		Identity:     id(alloc, "cmd-signal", 1),
		AllocationId: alloc,
		Signal:       sandbox.SignalTerm,
	})
	if err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if !signalReceipt.Delivered {
		t.Fatal("the signal must be delivered to the live session")
	}

	checkpointReceipt, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     id(alloc, "cmd-checkpoint", 1),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	snapshot, ok := fb.CheckpointBytes(checkpointReceipt.CheckpointId)
	if !ok {
		t.Fatalf("the fixture bridge must hold the persisted snapshot %q", checkpointReceipt.CheckpointId)
	}
	if checkpointReceipt.SHA256 != sandbox.RecomputeSHA256(snapshot) {
		t.Fatal("the checkpoint receipt must carry the recomputed snapshot digest, never an echo")
	}

	restoreReceipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             id(next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restoreReceipt.Allocation.AllocationId != next || restoreReceipt.Allocation.Generation != 2 || restoreReceipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the restore receipt must observe the active replacement at generation 2, got %+v", restoreReceipt.Allocation)
	}

	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     id(next, "cmd-exec-next", 2),
		AllocationId: next,
		Command:      []string{"echo", "restored"},
	}); err != nil {
		t.Fatalf("Exec on the restored allocation: %v", err)
	}
	if _, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     id(next, "cmd-inspect-next", 2),
		AllocationId: next,
	}); err != nil {
		t.Fatalf("Inspect on the restored allocation: %v", err)
	}

	terminateReceipt, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     id(next, "cmd-terminate-next", 2),
		AllocationId: next,
	})
	if err != nil || terminateReceipt.State != sandbox.AllocationTerminated {
		t.Fatalf("Terminate of the restored allocation: state=%q err=%v", string(terminateReceipt.State), err)
	}
	previousTerminate, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     id(alloc, "cmd-terminate-previous", 1),
		AllocationId: alloc,
	})
	if err != nil || previousTerminate.State != sandbox.AllocationReplaced {
		t.Fatalf("Terminate of the replaced allocation must observe the replaced state, got %+v err=%v", previousTerminate, err)
	}

	reconcileReport, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  id(next, "cmd-reconcile", 2),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil || reconcileReport.DriftDetected || len(reconcileReport.ActiveAllocationIds) != 0 {
		t.Fatalf("the final reconcile must be clean, got %+v err=%v", reconcileReport, err)
	}
}

// TestProviderInvalidIdentityFailsClosedBeforeBridge freezes that every
// operation rejects a malformed identity before any Bridge call.
func TestProviderInvalidIdentityFailsClosedBeforeBridge(t *testing.T) {
	name := "invalid-identity"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	broken := func(commandId string) sandbox.OperationIdentity {
		identity := scenarioIdentity(name, alloc, commandId, 1)
		identity.FencingToken = ""
		return identity
	}
	for _, tc := range []struct {
		operation string
		call      func() error
	}{
		{"probe", func() error {
			_, err := provider.Probe(ctx, sandbox.ProbeRequest{Identity: broken("cmd-probe"), Requirements: workspaceRequirements(t)})
			return err
		}},
		{"provision", func() error {
			_, err := provider.Provision(ctx, sandbox.ProvisionRequest{Identity: broken("cmd-provision"), Requirements: workspaceRequirements(t)})
			return err
		}},
		{"stage", func() error {
			_, err := provider.Stage(ctx, sandbox.StageRequest{Identity: broken("cmd-stage"), AllocationId: alloc})
			return err
		}},
		{"exec", func() error {
			_, err := provider.Exec(ctx, sandbox.ExecRequest{Identity: broken("cmd-exec"), AllocationId: alloc, Command: []string{"echo"}})
			return err
		}},
		{"inspect", func() error {
			_, err := provider.Inspect(ctx, sandbox.InspectRequest{Identity: broken("cmd-inspect"), AllocationId: alloc})
			return err
		}},
		{"signal", func() error {
			_, err := provider.Signal(ctx, sandbox.SignalRequest{Identity: broken("cmd-signal"), AllocationId: alloc, Signal: sandbox.SignalTerm})
			return err
		}},
		{"checkpoint", func() error {
			_, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{Identity: broken("cmd-checkpoint"), AllocationId: alloc})
			return err
		}},
		{"restore", func() error {
			_, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{Identity: broken("cmd-restore"), PreviousAllocationId: alloc})
			return err
		}},
		{"terminate", func() error {
			_, err := provider.Terminate(ctx, sandbox.TerminateRequest{Identity: broken("cmd-terminate"), AllocationId: alloc})
			return err
		}},
		{"reconcile", func() error {
			_, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{Identity: broken("cmd-reconcile"), RunId: "run-" + name, AttemptId: "attempt-" + name})
			return err
		}},
	} {
		if err := tc.call(); !errors.Is(err, sandbox.ErrInvalidOperationIdentity) {
			t.Fatalf("%s must fail closed with ErrInvalidOperationIdentity, got %v", tc.operation, err)
		}
	}
	if total := fb.TotalRequests(); total != 0 {
		t.Fatalf("a rejected identity must never reach the bridge, got %d requests", total)
	}
}

// TestProviderHardenedAssuranceGate freezes the fail-closed assurance gate:
// a hardened request against a provider without evidence is refused
// outright and never downgraded.
func TestProviderHardenedAssuranceGate(t *testing.T) {
	name := "hardened-gate"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()

	probeReport, err := provider.Probe(ctx, sandbox.ProbeRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-probe", 1),
		Requirements: hardenedRequirements(t),
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probeReport.Supported || probeReport.ConformanceEvidenceRef != "" || probeReport.SelfSignedConformanceClaim {
		t.Fatalf("a hardened request against an unevidenced provider must be reported unsupported: %+v", probeReport)
	}

	_, err = provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: hardenedRequirements(t),
	})
	if !errors.Is(err, sandbox.ErrAssuranceNotMet) {
		t.Fatalf("a hardened request without evidence must fail closed with ErrAssuranceNotMet, got %v", err)
	}
	if total := fb.TotalRequests(); total != 1 {
		t.Fatalf("only the probe health read may reach the bridge before the gate, got %d requests", total)
	}
}

// TestProviderHardenedWithEvidenceServes freezes that a hardened request
// served on valid evidence grants exactly the hardened combination and
// carries the evidence reference.
func TestProviderHardenedWithEvidenceServes(t *testing.T) {
	name := "hardened-evidence"
	alloc := "alloc-" + name
	evidence := fixtureDigest("conformance-evidence" + "-" + name)
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, evidence)
	ctx := context.Background()
	receipt, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: hardenedRequirements(t),
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if receipt.Allocation.AssuranceLevel != "hardened" {
		t.Fatalf("the hardened request must grant the hardened assurance, got %q", string(receipt.Allocation.AssuranceLevel))
	}
	if receipt.Allocation.ConformanceEvidenceRef != evidence {
		t.Fatalf("the allocation must carry the evidence reference")
	}
	if err := receipt.Allocation.Validate(); err != nil {
		t.Fatalf("the granted allocation must validate: %v", err)
	}
}

// TestProviderDuplicateActiveAllocationRejected freezes the single-active
// invariant at the provider bookkeeping level.
func TestProviderDuplicateActiveAllocationRejected(t *testing.T) {
	name := "duplicate"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, "alloc-"+name, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	_, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, "alloc-"+name+"-second", "cmd-provision-second", 1),
		Requirements: workspaceRequirements(t),
	})
	if !errors.Is(err, sandbox.ErrDuplicateActiveAllocation) {
		t.Fatalf("a second concurrent allocation must be rejected, got %v", err)
	}
	if got := fb.RequestCount("POST", sandboxesPath); got != 1 {
		t.Fatalf("the rejected duplicate must never reach the bridge, got %d creates", got)
	}
}

// TestProviderCapacityExhaustionFailsClosed freezes that a Bridge capacity
// refusal fails closed and leaves no local bookkeeping behind.
func TestProviderCapacityExhaustionFailsClosed(t *testing.T) {
	name := "capacity"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	fb.CapacityExhaustNext()
	_, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	})
	if !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("capacity exhaustion must fail closed, got %v", err)
	}
	// The failed provision must leave no bookkeeping: the identical retry
	// succeeds once the capacity clears.
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision-retry", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("the retry after cleared capacity must succeed, got %v", err)
	}
}

// TestProviderStageDigestMismatchFailsClosed freezes that a mismatched
// declared digest fails the attempt with the fixed sentinel, produces no
// receipt and fails the allocation.
func TestProviderStageDigestMismatchFailsClosed(t *testing.T) {
	name := "stage-mismatch"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("mismatch" + "-content")
	mismatchedDeclared := sandbox.RecomputeSHA256(append(append([]byte(nil), content...), []byte("declared-mismatch")...))
	report, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: mismatchedDeclared,
			Inline:         content,
		}},
	})
	if !errors.Is(err, sandbox.ErrStageInputMismatch) {
		t.Fatalf("the mismatch must fail closed with ErrStageInputMismatch, got %v", err)
	}
	if report != nil {
		t.Fatal("a failed stage must produce no receipt")
	}
	if _, ok := fb.SandboxFile(alloc, "staged/payload"); ok {
		t.Fatal("the mismatched bytes must never be consumed by the container")
	}
	// The attempt is failed: later exec refuses the allocation.
	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec"),
		AllocationId: alloc,
		Command:      []string{"echo"},
	}); !errors.Is(err, sandbox.ErrAllocationNotActive) {
		t.Fatalf("exec after a failed stage must refuse the inactive allocation, got %v", err)
	}
	if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("terminate must still recover the failed allocation, got %v", err)
	}
}

// TestProviderStageLocatorPath freezes the locator staging path: bound and
// seeded locators stage, unbound aliases are rejected by validation and
// unresolvable locators fail closed with the fixed sentinel.
func TestProviderStageLocatorPath(t *testing.T) {
	name := "stage-locator"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        identity("cmd-provision"),
		Requirements:    workspaceRequirements(t),
		AllowedStoreIds: []string{"store-" + name},
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	content := []byte("locator" + "-content-" + name)
	digest := sandbox.RecomputeSHA256(content)

	_, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage-unbound"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "unbound",
			DeclaredSHA256: digest,
			Locator:        &sandbox.Locator{StoreId: "store-unbound", SHA256: digest, SizeBytes: int64(len(content))},
		}},
	})
	if !errors.Is(err, sandbox.ErrInvalidLocator) {
		t.Fatalf("an unbound store alias must be rejected with ErrInvalidLocator, got %v", err)
	}

	_, err = provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage-unresolved"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "unresolved",
			DeclaredSHA256: digest,
			Locator:        &sandbox.Locator{StoreId: "store-" + name, SHA256: digest, SizeBytes: int64(len(content))},
		}},
	})
	if !errors.Is(err, sandbox.ErrLocatorUnresolved) {
		t.Fatalf("an unresolved locator must fail closed with ErrLocatorUnresolved, got %v", err)
	}

	fb.SeedStore("store-"+name, digest, content)
	report, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage-ok"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "resolved",
			DeclaredSHA256: digest,
			Locator:        &sandbox.Locator{StoreId: "store-" + name, SHA256: digest, SizeBytes: int64(len(content))},
		}},
	})
	if err != nil {
		t.Fatalf("Stage of the seeded locator: %v", err)
	}
	if len(report.Receipts) != 1 || report.Receipts[0].RecomputedSHA256 != digest || report.Receipts[0].PostConsumptionSHA256 != digest {
		t.Fatalf("the locator receipt must carry the recomputed digests, got %+v", report.Receipts)
	}
	if staged, ok := fb.SandboxFile(alloc, "staged/resolved"); !ok || sandbox.RecomputeSHA256(staged) != digest {
		t.Fatal("the staged locator content must match the store object byte for byte")
	}
}

// TestProviderStagePostConsumptionTamperFailsClosed freezes that tampering
// between the pre-consumption check and the read-back recomputation fails
// the attempt closed.
func TestProviderStagePostConsumptionTamperFailsClosed(t *testing.T) {
	name := "stage-tamper"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fb.TamperAfterWrite(true)
	content := []byte("tamper" + "-content")
	report, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         content,
		}},
	})
	if err == nil {
		t.Fatal("a post-consumption tamper must fail the attempt closed")
	}
	if errors.Is(err, sandbox.ErrStageInputMismatch) {
		t.Fatalf("the tamper is a post-consumption failure, not the pre-consumption sentinel: %v", err)
	}
	if report != nil {
		t.Fatal("a tampered stage must produce no receipt")
	}
}

// TestProviderExecStatusMapping freezes the exec receipt status mapping and
// the empty-command refusal.
func TestProviderExecStatusMapping(t *testing.T) {
	name := "exec-status"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	completed, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec-ok"),
		AllocationId: alloc,
		Command:      []string{"echo"},
	})
	if err != nil || completed.Status != sandbox.ExecutionCompleted || completed.ExitCode != 0 {
		t.Fatalf("the clean exec must complete, got %+v err=%v", completed, err)
	}

	fb.SetExecOutcome("failing-cmd", 3, false)
	failed, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec-failed"),
		AllocationId: alloc,
		Command:      []string{"failing-cmd"},
	})
	if err != nil || failed.Status != sandbox.ExecutionFailed || failed.ExitCode != 3 {
		t.Fatalf("the scripted non-zero exit must map to failed, got %+v err=%v", failed, err)
	}

	fb.SetExecOutcome("doomed-cmd", 137, true)
	killed, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec-killed"),
		AllocationId: alloc,
		Command:      []string{"doomed-cmd"},
	})
	if err != nil || killed.Status != sandbox.ExecutionKilled || killed.ExitCode != 137 {
		t.Fatalf("the signaled exit must map to killed, got %+v err=%v", killed, err)
	}

	execCalls := fb.RequestCount("POST", sandboxesPath+"/"+alloc+"/exec")
	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec-empty"),
		AllocationId: alloc,
		Command:      nil,
	}); !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("an empty command must be rejected with ErrInvalidRequest, got %v", err)
	}
	if got := fb.RequestCount("POST", sandboxesPath+"/"+alloc+"/exec"); got != execCalls {
		t.Fatalf("the empty command must never reach the bridge, got %d exec calls", got)
	}
}

// TestProviderStaleGenerationRejected freezes that a stale handle presented
// after a restore is rejected fail closed.
func TestProviderStaleGenerationRejected(t *testing.T) {
	name := "stale-generation"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity(name, next, "cmd-exec-stale", 1),
		AllocationId: next,
		Command:      []string{"echo"},
	}); !errors.Is(err, sandbox.ErrStaleAllocationGeneration) {
		t.Fatalf("the stale generation must be rejected after the restore, got %v", err)
	}
}

// TestProviderSignalClosedEnumeration freezes the closed signal
// enumeration and the delivery observation.
func TestProviderSignalClosedEnumeration(t *testing.T) {
	name := "signal"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := provider.Signal(ctx, sandbox.SignalRequest{
		Identity:     identity("cmd-signal-invalid"),
		AllocationId: alloc,
		Signal:       sandbox.SignalName("sigterm"),
	}); !errors.Is(err, sandbox.ErrInvalidSignal) {
		t.Fatalf("a signal outside the closed enumeration must be rejected, got %v", err)
	}
	if got := fb.RequestCount("POST", sandboxesPath+"/"+alloc+"/signal"); got != 0 {
		t.Fatalf("an invalid signal must never reach the bridge, got %d calls", got)
	}

	notDelivered, err := provider.Signal(ctx, sandbox.SignalRequest{
		Identity:     identity("cmd-signal-absent"),
		AllocationId: alloc,
		Signal:       sandbox.SignalTerm,
	})
	if err != nil || notDelivered.Delivered {
		t.Fatalf("a signal without a live session must observe non-delivery, got %+v err=%v", notDelivered, err)
	}
	fb.SetLiveSessions(t, alloc, 1)
	delivered, err := provider.Signal(ctx, sandbox.SignalRequest{
		Identity:     identity("cmd-signal-live"),
		AllocationId: alloc,
		Signal:       sandbox.SignalKill,
	})
	if err != nil || !delivered.Delivered {
		t.Fatalf("a signal to the live session must observe delivery, got %+v err=%v", delivered, err)
	}
}

// TestProviderCheckpointDigestRecomputed freezes that the checkpoint
// receipt digest is the out-of-band recomputation of the persisted snapshot
// bytes, never an echo.
func TestProviderCheckpointDigestRecomputed(t *testing.T) {
	name := "checkpoint-honesty"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("checkpoint" + "-content")
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         content,
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	first, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint-1"),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	snapshot, ok := fb.CheckpointBytes(first.CheckpointId)
	if !ok {
		t.Fatalf("the fixture bridge must hold the persisted snapshot %q", first.CheckpointId)
	}
	if first.SHA256 != sandbox.RecomputeSHA256(snapshot) || first.SizeBytes != int64(len(snapshot)) {
		t.Fatalf("the checkpoint receipt must carry the recomputed snapshot digest and size: %+v", first)
	}
	second, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint-2"),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Checkpoint again: %v", err)
	}
	if second.CheckpointId == first.CheckpointId {
		t.Fatal("each checkpoint must carry its own deterministic identifier")
	}
	if second.SHA256 != first.SHA256 {
		t.Fatal("identical staged content must yield the identical checkpoint digest")
	}
}

// TestProviderRestoreReplacementSemantics freezes the replacement restore
// chain on top of the implicit persist: create + hydrate + destroy, the
// previous allocation becomes replaced and terminate observes it idempotently.
func TestProviderRestoreReplacementSemantics(t *testing.T) {
	name := "restore-replacement"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("restore" + "-content")
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-stage", 1),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         content,
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	receipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if receipt.Allocation.AllocationId != next || receipt.Allocation.Generation != 2 || receipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the restore must activate the replacement at generation 2, got %+v", receipt.Allocation)
	}
	if staged, ok := fb.SandboxFile(next, "staged/payload"); !ok || sandbox.RecomputeSHA256(staged) != sandbox.RecomputeSHA256(content) {
		t.Fatal("the hydrate must restore the staged content byte for byte")
	}
	if got := fb.RequestCount("DELETE", sandboxesPath+"/"+alloc); got != 1 {
		t.Fatalf("the restore must destroy the previous sandbox exactly once, got %d", got)
	}
	previousTerminate, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-terminate-previous", 1),
		AllocationId: alloc,
	})
	if err != nil || previousTerminate.State != sandbox.AllocationReplaced {
		t.Fatalf("terminate of the replaced allocation must observe replaced, got %+v err=%v", previousTerminate, err)
	}
	if got := fb.RequestCount("DELETE", sandboxesPath+"/"+alloc); got != 1 {
		t.Fatalf("terminating a terminal allocation must not call the bridge again, got %d", got)
	}
}

// TestProviderRestoreInPlaceSemantics freezes the confirmed in-place
// restore: the same locator is re-activated at the bumped generation after
// the Bridge observation confirms no live exec session.
func TestProviderRestoreInPlaceSemantics(t *testing.T) {
	name := "restore-in-place"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("in-place" + "-content")
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-stage", 1),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         content,
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	// A live exec session blocks the in-place restore re-verification.
	fb.SetLiveSessions(t, alloc, 1)
	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, alloc, "cmd-restore-blocked", 2),
		PreviousAllocationId: alloc,
		InPlaceConfirmed:     true,
	}); !errors.Is(err, sandbox.ErrRestoreRejected) {
		t.Fatalf("an in-place restore against a live session must be rejected, got %v", err)
	}

	fb.SetLiveSessions(t, alloc, 0)
	receipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, alloc, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		InPlaceConfirmed:     true,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if receipt.Allocation.AllocationId != alloc || receipt.Allocation.Generation != 2 || receipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the in-place restore must re-activate the same locator at generation 2, got %+v", receipt.Allocation)
	}
	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-exec-stale", 1),
		AllocationId: alloc,
		Command:      []string{"echo"},
	}); !errors.Is(err, sandbox.ErrStaleAllocationGeneration) {
		t.Fatalf("the stale generation must be rejected after the in-place restore, got %v", err)
	}
}

// TestProviderRestoreRejectionRules freezes the restore decision rules of
// PlanRestore at the provider boundary, with no Bridge side effect.
func TestProviderRestoreRejectionRules(t *testing.T) {
	name := "restore-reject"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	creates := fb.RequestCount("POST", sandboxesPath)
	persists := fb.RequestCount("POST", sandboxesPath+"/"+alloc+"/persist")

	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, alloc, "cmd-restore-unconfirmed", 2),
		PreviousAllocationId: alloc,
	}); !errors.Is(err, sandbox.ErrRestoreRejected) {
		t.Fatalf("a replacement restore without a next allocationId must be rejected, got %v", err)
	}
	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, alloc, "cmd-restore-reuse", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     alloc,
	}); !errors.Is(err, sandbox.ErrRestoreRejected) {
		t.Fatalf("a replacement restore reusing the previous allocationId must be rejected, got %v", err)
	}
	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, alloc, "cmd-restore-stale", 1),
		PreviousAllocationId: alloc,
		InPlaceConfirmed:     true,
	}); !errors.Is(err, sandbox.ErrStaleAllocationGeneration) {
		t.Fatalf("a restore identity must carry the post-restore generation, got %v", err)
	}
	if _, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, "alloc-unknown", "cmd-restore-unknown", 2),
		PreviousAllocationId: "alloc-unknown",
		NextAllocationId:     "alloc-unknown-next",
	}); !errors.Is(err, sandbox.ErrAllocationNotFound) {
		t.Fatalf("a restore of an unknown allocation must fail closed, got %v", err)
	}

	if got := fb.RequestCount("POST", sandboxesPath); got != creates {
		t.Fatalf("rejected restores must never create a sandbox, got %d creates", got)
	}
	if got := fb.RequestCount("POST", sandboxesPath+"/"+alloc+"/persist"); got != persists {
		t.Fatalf("rejected restores must never persist, got %d persists", got)
	}
}

// TestProviderTerminateIdempotent freezes that terminating an already
// terminal allocation is idempotent and performs no extra Bridge call.
func TestProviderTerminateIdempotent(t *testing.T) {
	name := "terminate-idempotent"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	first, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate-1"),
		AllocationId: alloc,
	})
	if err != nil || first.State != sandbox.AllocationTerminated {
		t.Fatalf("Terminate: state=%q err=%v", string(first.State), err)
	}
	if got := fb.RequestCount("DELETE", sandboxesPath+"/"+alloc); got != 1 {
		t.Fatalf("exactly one destroy call was expected, got %d", got)
	}
	second, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate-2"),
		AllocationId: alloc,
	})
	if err != nil || second.State != sandbox.AllocationTerminated {
		t.Fatalf("Terminate again: state=%q err=%v", string(second.State), err)
	}
	if got := fb.RequestCount("DELETE", sandboxesPath+"/"+alloc); got != 1 {
		t.Fatalf("the idempotent terminate must not call the bridge again, got %d", got)
	}
}

// TestProviderInspectObservedState freezes that Inspect reflects the Bridge
// observation channel: escaped probes surface as violations, a terminated
// sandbox is observed terminal, and a silently reclaimed sandbox fails
// closed.
func TestProviderInspectObservedState(t *testing.T) {
	name := "inspect-observed"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	fb.DisableContainment(true)
	if _, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec-probe"),
		AllocationId: alloc,
		Command:      []string{sandbox.ProbeCommandBoundaryWrite},
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	observed, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     identity("cmd-inspect-violations"),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(observed.Violations) != 1 || observed.Violations[0].Kind != sandbox.ViolationOutOfBoundsWrite {
		t.Fatalf("the escaped probe must surface as an observed violation, got %+v", observed.Violations)
	}

	if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	terminal, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     identity("cmd-inspect-terminal"),
		AllocationId: alloc,
	})
	if err != nil || terminal.State != sandbox.AllocationTerminated {
		t.Fatalf("Inspect of the terminated allocation must observe the terminal state, got %+v err=%v", terminal, err)
	}
}

// TestProviderInspectSilentReclaimFailsClosed freezes that a locally active
// allocation the Bridge no longer knows fails closed.
func TestProviderInspectSilentReclaimFailsClosed(t *testing.T) {
	name := "inspect-reclaim"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fb.ForgetSandbox(t, alloc)
	if _, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     scenarioIdentity(name, alloc, "cmd-inspect", 1),
		AllocationId: alloc,
	}); !errors.Is(err, sandbox.ErrAllocationNotFound) {
		t.Fatalf("a silently reclaimed sandbox must fail closed, got %v", err)
	}
}

// TestProviderReconcileDriftFailsClosed freezes that reconcile reports
// drift fail closed for Bridge-side orphans and for locally active
// allocations missing from the Bridge running list.
func TestProviderReconcileDriftFailsClosed(t *testing.T) {
	t.Run("bridge-orphan", func(t *testing.T) {
		name := "reconcile-orphan"
		alloc := "alloc-" + name
		fb := newFakeBridge(t, testBridgeToken(name))
		provider := newTestProvider(t, fb, "")
		ctx := context.Background()
		if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
			Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
			Requirements: workspaceRequirements(t),
		}); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		// A sandbox exists on the Bridge that the bookkeeping does not know.
		if _, err := provider.client.CreateSandbox(ctx, CreateSandboxRequest{
			SandboxId:  "alloc-ghost",
			RunId:      "run-" + name,
			AttemptId:  "attempt-" + name,
			Generation: 1,
		}, "key-ghost"); err != nil {
			t.Fatalf("direct bridge create: %v", err)
		}
		report, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
			Identity:  scenarioIdentity(name, alloc, "cmd-reconcile", 1),
			RunId:     "run-" + name,
			AttemptId: "attempt-" + name,
		})
		if err == nil {
			t.Fatal("reconcile must fail closed on a bridge-side orphan")
		}
		if report == nil || !report.DriftDetected {
			t.Fatalf("reconcile must report drift, got %+v", report)
		}
		orphaned := false
		for _, orphan := range report.OrphanAllocationIds {
			if orphan == "alloc-ghost" {
				orphaned = true
			}
		}
		if !orphaned {
			t.Fatalf("the ghost sandbox must be reported as an orphan, got %+v", report)
		}
	})

	t.Run("silent-reclaim", func(t *testing.T) {
		name := "reconcile-reclaim"
		alloc := "alloc-" + name
		fb := newFakeBridge(t, testBridgeToken(name))
		provider := newTestProvider(t, fb, "")
		ctx := context.Background()
		if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
			Identity:     scenarioIdentity(name, alloc, "cmd-provision", 1),
			Requirements: workspaceRequirements(t),
		}); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		fb.ForgetSandbox(t, alloc)
		report, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
			Identity:  scenarioIdentity(name, alloc, "cmd-reconcile", 1),
			RunId:     "run-" + name,
			AttemptId: "attempt-" + name,
		})
		if err == nil {
			t.Fatal("reconcile must fail closed on a silent reclaim")
		}
		if report == nil || !report.DriftDetected {
			t.Fatalf("reconcile must report drift, got %+v", report)
		}
		if len(report.ActiveAllocationIds) != 0 {
			t.Fatalf("a reclaimed sandbox must not be reported active, got %+v", report)
		}
	})
}

// TestProviderCredentialDiscipline freezes the credential discipline end to
// end: the Bridge Bearer token travels only as the Authorization header and
// never surfaces in any provider observable — errors, receipts, reports,
// observation logs or diagnostics.
func TestProviderCredentialDiscipline(t *testing.T) {
	token := testBridgeToken("discipline")
	name := "discipline"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, token)
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}

	var observables []string
	collect := func(err error) {
		if err != nil {
			observables = append(observables, err.Error())
		}
	}

	probeReport, err := provider.Probe(ctx, sandbox.ProbeRequest{Identity: identity("cmd-probe"), Requirements: workspaceRequirements(t)})
	collect(err)
	if probeReport != nil {
		observables = append(observables, probeReport.Reason, probeReport.ConformanceEvidenceRef)
	}
	provisionReceipt, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        identity("cmd-provision"),
		Requirements:    workspaceRequirements(t),
		AllowedStoreIds: []string{"store-" + name},
	})
	collect(err)
	if provisionReceipt != nil {
		observables = append(observables, provisionReceipt.Allocation.AllocationId, provisionReceipt.Allocation.RunId, provisionReceipt.Allocation.ConformanceEvidenceRef)
	}
	content := []byte("discipline" + "-content")
	stageReport, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         content,
		}},
	})
	collect(err)
	if stageReport != nil {
		for _, receipt := range stageReport.Receipts {
			observables = append(observables, receipt.InputId, receipt.RecomputedSHA256, receipt.PostConsumptionSHA256)
		}
	}
	execReceipt, err := provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     identity("cmd-exec"),
		AllocationId: alloc,
		Command:      []string{"echo"},
	})
	collect(err)
	if execReceipt != nil {
		observables = append(observables, execReceipt.StdoutSHA256, execReceipt.StderrSHA256, string(execReceipt.Status))
	}
	inspectReport, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     identity("cmd-inspect"),
		AllocationId: alloc,
	})
	collect(err)
	if inspectReport != nil {
		observables = append(observables, inspectReport.LogLines...)
	}
	checkpointReceipt, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint"),
		AllocationId: alloc,
	})
	collect(err)
	if checkpointReceipt != nil {
		observables = append(observables, checkpointReceipt.CheckpointId, checkpointReceipt.SHA256)
	}
	restoreReceipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	collect(err)
	if restoreReceipt != nil {
		observables = append(observables, restoreReceipt.Allocation.AllocationId)
	}
	_, terminateErr := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     scenarioIdentity(name, next, "cmd-terminate", 2),
		AllocationId: next,
	})
	collect(terminateErr)
	reconcileReport, reconcileErr := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  scenarioIdentity(name, next, "cmd-reconcile", 2),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	collect(reconcileErr)
	if reconcileReport != nil {
		observables = append(observables, reconcileReport.ActiveAllocationIds...)
		observables = append(observables, reconcileReport.OrphanAllocationIds...)
	}

	// Error paths through the credential, exercised in a second scope so
	// the fail-closed stage mismatch cannot corrupt the happy chain above.
	mismatchName := name + "-mismatch"
	mismatchAlloc := "alloc-" + mismatchName
	mismatchIdentity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(mismatchName, mismatchAlloc, commandId, 1)
	}
	_, mismatchProvisionErr := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     mismatchIdentity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	})
	collect(mismatchProvisionErr)
	mismatchedDeclared := sandbox.RecomputeSHA256(append(append([]byte(nil), content...), []byte("declared-mismatch")...))
	_, mismatchErr := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     mismatchIdentity("cmd-stage-mismatch"),
		AllocationId: mismatchAlloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "mismatch",
			DeclaredSHA256: mismatchedDeclared,
			Inline:         content,
		}},
	})
	collect(mismatchErr)
	if mismatchErr == nil {
		t.Fatal("the mismatched stage must fail closed")
	}
	_, hardenedErr := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     mismatchIdentity("cmd-provision-hardened"),
		Requirements: hardenedRequirements(t),
	})
	collect(hardenedErr)

	for _, diagnostic := range provider.Diagnostics() {
		observables = append(observables, diagnostic.Operation, diagnostic.AllocationId, diagnostic.Reason)
	}

	assertNoCredential(t, token, observables...)

	headers := fb.AuthHeaders()
	if len(headers) == 0 {
		t.Fatal("the fixture bridge must have observed authenticated requests")
	}
	for _, header := range headers {
		if header != "Bearer "+token {
			t.Fatalf("the credential must travel only as the Authorization header, got %q", header)
		}
	}
}

// TestProviderMissingCredentialFailsClosed freezes that provider
// construction refuses a missing credential and malformed configuration.
func TestProviderMissingCredentialFailsClosed(t *testing.T) {
	if _, err := NewProvider(ProviderConfig{
		BridgeBaseURL: "https://bridge" + ".example",
		BridgeToken:   "",
	}); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("a missing credential must fail closed at construction, got %v", err)
	}
	if _, err := NewProvider(ProviderConfig{
		BridgeBaseURL: "",
		BridgeToken:   "fixture" + "-token",
	}); !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("a missing base URL must fail closed at construction, got %v", err)
	}
	if _, err := NewProvider(ProviderConfig{
		BridgeBaseURL:          "https://bridge" + ".example",
		BridgeToken:            "fixture" + "-token",
		ConformanceEvidenceRef: "sha256:zz",
	}); !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("a malformed evidence reference must fail closed at construction, got %v", err)
	}
}
