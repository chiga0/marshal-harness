package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// workspaceWriteRequirements builds the read-write baseline requirements.
func workspaceWriteRequirements() domain.SandboxRequirements {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		panic(err)
	}
	return requirements
}

// hardenedRequirements builds hardened requirements in the workspace-write
// access mode.
func hardenedRequirements() domain.SandboxRequirements {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelHardened)
	if err != nil {
		panic(err)
	}
	return requirements
}

// testIdentity derives a valid identity bound to one allocation locator.
func testIdentity(allocationId, commandId string) OperationIdentity {
	id := validIdentity()
	id.AllocationId = allocationId
	id.CommandId = commandId
	return id
}

// testIdentityWithGeneration additionally overrides the lease generation.
func testIdentityWithGeneration(allocationId, commandId string, generation int64) OperationIdentity {
	id := testIdentity(allocationId, commandId)
	id.Generation = generation
	return id
}

// validEvidenceRef derives a valid conformance evidence digest through the
// fixture helper.
func validEvidenceRef() string {
	return fixtureDigest("conformance-evidence" + "-provider")
}

func provisionTestAllocation(t *testing.T, fake *FakeProvider, allocationId string) SandboxAllocation {
	t.Helper()
	receipt, err := fake.Provision(context.Background(), ProvisionRequest{
		Identity:        testIdentity(allocationId, "command-provision"),
		Requirements:    workspaceWriteRequirements(),
		AllowedStoreIds: []string{"store-" + "a"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	return receipt.Allocation
}

// TestFakeFaultReject freezes the reject fault injection.
func TestFakeFaultReject(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationExec, Fault: FaultReject})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"reject")
	_, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-exec"),
		AllocationId: allocation.AllocationId,
		Command:      []string{"echo-" + "1"},
	})
	if !errors.Is(err, ErrFaultInjected) {
		t.Fatalf("the reject fault must surface ErrFaultInjected, got %v", err)
	}
}

// TestFakeFaultDelayIsDeterministic freezes that an injected delay advances
// the logical clock by exactly one tick per operation, with no wall clock.
func TestFakeFaultDelayIsDeterministic(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationProbe, Fault: FaultDelay})
	before := fake.LogicalTicks()
	for index := 0; index < 3; index++ {
		if _, err := fake.Probe(ctx, ProbeRequest{Identity: testIdentity("allocation-"+"delay", "command-probe"), Requirements: workspaceWriteRequirements()}); err != nil {
			t.Fatalf("probe %d: %v", index, err)
		}
	}
	if fake.LogicalTicks() != before+3 {
		t.Fatalf("each injected delay must advance the logical clock by exactly one tick, got %d", fake.LogicalTicks())
	}
}

// TestFakeFaultDropResponse freezes the drop-response fault, scoped by
// operation and commandId.
func TestFakeFaultDropResponse(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationStage, CommandId: "command-stage-drop", Fault: FaultDropResponse})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"drop")
	_, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage-drop"),
		AllocationId: allocation.AllocationId,
		Inputs:       []StageInput{validInlineInput("input-"+"1", []byte("payload"))},
	})
	if !errors.Is(err, ErrResponseLost) {
		t.Fatalf("the drop-response fault must surface ErrResponseLost, got %v", err)
	}
	report, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage-kept"),
		AllocationId: allocation.AllocationId,
		Inputs:       []StageInput{validInlineInput("input-"+"1", []byte("payload"))},
	})
	if err != nil {
		t.Fatalf("a non-matching commandId must not drop the response: %v", err)
	}
	if len(report.Receipts) != 1 {
		t.Fatalf("one receipt expected, got %d", len(report.Receipts))
	}
}

// TestFakeFaultTamperStageBytes freezes the tamper fault: the
// post-consumption recomputation observes the tampered bytes and differs
// from the pre-consumption digest.
func TestFakeFaultTamperStageBytes(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationStage, Fault: FaultTamperStageBytes})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"tamper")
	report, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage"),
		AllocationId: allocation.AllocationId,
		Inputs:       []StageInput{validInlineInput("input-"+"1", []byte("payload"))},
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	receipt := report.Receipts[0]
	if receipt.RecomputedSHA256 != RecomputeSHA256([]byte("payload")) {
		t.Fatal("the pre-consumption digest must match the honest recomputation")
	}
	if receipt.PostConsumptionSHA256 == receipt.RecomputedSHA256 {
		t.Fatal("the tamper fault must make the post-consumption digest diverge")
	}
}

// TestFakeFaultEchoDeclaredDigest freezes the digest-echo fault: the
// provider echoes the declared digest without recomputation and therefore
// accepts a mismatched input — exactly the behavior the conformance suite
// must judge as a failure.
func TestFakeFaultEchoDeclaredDigest(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationStage, Fault: FaultEchoDeclaredDigest})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"echo")
	mismatched := StageInput{
		InputId:        "input-" + "1",
		DeclaredSHA256: RecomputeSHA256([]byte("other-" + "content")),
		Inline:         []byte("actual-" + "content"),
	}
	report, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage"),
		AllocationId: allocation.AllocationId,
		Inputs:       []StageInput{mismatched},
	})
	if err != nil {
		t.Fatalf("the echo fault must suppress the mismatch sentinel: %v", err)
	}
	if report.Receipts[0].RecomputedSHA256 != mismatched.DeclaredSHA256 {
		t.Fatal("the echo fault must return the declared digest unmodified")
	}
}

// TestFakeFaultSelfSignConformance freezes the self-sign fault on Probe.
func TestFakeFaultSelfSignConformance(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationProbe, Fault: FaultSelfSignConformance})
	report, err := fake.Probe(ctx, ProbeRequest{Identity: testIdentity("allocation-"+"selfsign", "command-probe"), Requirements: workspaceWriteRequirements()})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !report.SelfSignedConformanceClaim {
		t.Fatal("the self-sign fault must carry the self-signed pass claim")
	}
}

// TestFakeFaultDisableContainment freezes that the disable-containment fault
// lets the adversarial probes surface as observed violations through
// Inspect, while the honest fake contains them.
func TestFakeFaultDisableContainment(t *testing.T) {
	ctx := context.Background()
	for _, contained := range []bool{true, false} {
		fake := NewFakeProvider(FakeConfig{})
		if !contained {
			fake.WithFaults(FaultSpec{Operation: OperationExec, Fault: FaultDisableContainment})
		}
		allocation := provisionTestAllocation(t, fake, fmt.Sprintf("allocation-contain-%v", contained))
		for _, commandId := range []struct{ id, token string }{
			{"command-boundary", ProbeCommandBoundaryWrite},
			{"command-env", ProbeCommandSensitiveEnvRead},
			{"command-spawn", ProbeCommandSpawnFlood},
		} {
			_, err := fake.Exec(ctx, ExecRequest{
				Identity:     testIdentity(allocation.AllocationId, commandId.id),
				AllocationId: allocation.AllocationId,
				Command:      []string{commandId.token},
			})
			if err != nil {
				t.Fatalf("exec %s: %v", commandId.id, err)
			}
		}
		report, err := fake.Inspect(ctx, InspectRequest{
			Identity:     testIdentity(allocation.AllocationId, "command-inspect"),
			AllocationId: allocation.AllocationId,
		})
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if contained && len(report.Violations) != 0 {
			t.Fatalf("the honest fake must contain every probe, got %d violations", len(report.Violations))
		}
		if !contained && len(report.Violations) != 3 {
			t.Fatalf("the permissive fake must surface three violations, got %d", len(report.Violations))
		}
		if !contained && report.SpawnCount == 0 {
			t.Fatal("the spawn flood must be counted when containment is disabled")
		}
		if len(report.LogLines) == 0 || len(report.LogLines) > maxLogLines {
			t.Fatalf("the observation log must stay non-empty and bounded, got %d lines", len(report.LogLines))
		}
	}
}

// TestFakeHardenedGate freezes the assurance gate on the provider side: a
// hardened request without evidence fails closed and is never downgraded,
// while a provider holding valid evidence serves the hardened request.
func TestFakeHardenedGate(t *testing.T) {
	ctx := context.Background()
	noEvidence := NewFakeProvider(FakeConfig{})
	_, err := noEvidence.Provision(ctx, ProvisionRequest{
		Identity:     testIdentity("allocation-"+"hardened", "command-provision"),
		Requirements: hardenedRequirements(),
	})
	if !errors.Is(err, ErrAssuranceNotMet) {
		t.Fatalf("a hardened request without evidence must fail closed with ErrAssuranceNotMet, got %v", err)
	}
	withEvidence := NewFakeProvider(FakeConfig{ConformanceEvidenceRef: validEvidenceRef()})
	receipt, err := withEvidence.Provision(ctx, ProvisionRequest{
		Identity:     testIdentity("allocation-"+"hardened-ok", "command-provision"),
		Requirements: hardenedRequirements(),
	})
	if err != nil {
		t.Fatalf("a hardened request with valid evidence must be served: %v", err)
	}
	if receipt.Allocation.AssuranceLevel != domain.AssuranceLevelHardened {
		t.Fatal("valid evidence must never downgrade the granted assurance level")
	}
}

// TestFakeProvisionRejectsDualActive freezes the provider-side single-active
// rejection for one (runId, attemptId).
func TestFakeProvisionRejectsDualActive(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	provisionTestAllocation(t, fake, "allocation-"+"first")
	_, err := fake.Provision(ctx, ProvisionRequest{
		Identity:        testIdentity("allocation-"+"second", "command-provision"),
		Requirements:    workspaceWriteRequirements(),
		AllowedStoreIds: []string{"store-" + "a"},
	})
	if !errors.Is(err, ErrDuplicateActiveAllocation) {
		t.Fatalf("a second concurrent allocation must be rejected with ErrDuplicateActiveAllocation, got %v", err)
	}
}

// TestFakeStoreSeedingAndLocatorResolution freezes locator staging against
// the seeded store.
func TestFakeStoreSeedingAndLocatorResolution(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"locator")
	content := []byte("locator-" + "content")
	objectDigest := RecomputeSHA256(content)
	fake.SeedStore("store-"+"a", objectDigest, content)
	report, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage"),
		AllocationId: allocation.AllocationId,
		Inputs: []StageInput{{
			InputId:        "input-locator",
			DeclaredSHA256: objectDigest,
			Locator:        &Locator{StoreId: "store-" + "a", SHA256: objectDigest, SizeBytes: int64(len(content))},
		}},
	})
	if err != nil {
		t.Fatalf("stage with a seeded locator: %v", err)
	}
	if report.Receipts[0].RecomputedSHA256 != objectDigest {
		t.Fatal("the seeded locator content must recompute to its digest")
	}
	unseededDigest := fixtureDigest("unseeded" + "-object")
	_, err = fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-stage-unseeded"),
		AllocationId: allocation.AllocationId,
		Inputs: []StageInput{{
			InputId:        "input-unseeded",
			DeclaredSHA256: unseededDigest,
			Locator:        &Locator{StoreId: "store-" + "a", SHA256: unseededDigest, SizeBytes: 16},
		}},
	})
	if !errors.Is(err, ErrLocatorUnresolved) {
		t.Fatalf("an unseeded locator must fail with ErrLocatorUnresolved, got %v", err)
	}
}

// fakeScenarioFingerprint replays one fixed script against a fresh fake
// provider and reduces every receipt to one deterministic fingerprint.
func fakeScenarioFingerprint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocationId := "allocation-" + "det"
	allocation := provisionTestAllocation(t, fake, allocationId)
	stageReport, err := fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocationId, "command-stage"),
		AllocationId: allocationId,
		Inputs:       []StageInput{validInlineInput("input-"+"1", []byte("deterministic-"+"payload"))},
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	execReceipt, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocationId, "command-exec"),
		AllocationId: allocationId,
		Command:      []string{ProbeCommandBoundaryWrite, "workload-step"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	inspectReport, err := fake.Inspect(ctx, InspectRequest{Identity: testIdentity(allocationId, "command-inspect"), AllocationId: allocationId})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	checkpoint, err := fake.Checkpoint(ctx, CheckpointRequest{Identity: testIdentity(allocationId, "command-checkpoint"), AllocationId: allocationId})
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	terminateReceipt, err := fake.Terminate(ctx, TerminateRequest{Identity: testIdentity(allocationId, "command-terminate"), AllocationId: allocationId})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	reconcileReport, err := fake.Reconcile(ctx, ReconcileRequest{Identity: testIdentity(allocationId, "command-reconcile"), RunId: "run-" + "1", AttemptId: "attempt-" + "1"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	parts := []string{
		allocation.AllocationId,
		stageReport.Receipts[0].RecomputedSHA256,
		stageReport.Receipts[0].PostConsumptionSHA256,
		execReceipt.StdoutSHA256,
		execReceipt.StderrSHA256,
		fmt.Sprintf("violations=%d spawn=%d", len(inspectReport.Violations), inspectReport.SpawnCount),
		checkpoint.CheckpointId,
		checkpoint.SHA256,
		fmt.Sprintf("size=%d", checkpoint.SizeBytes),
		string(terminateReceipt.State),
		fmt.Sprintf("active=%s drift=%v", strings.Join(reconcileReport.ActiveAllocationIds, ","), reconcileReport.DriftDetected),
	}
	return strings.Join(parts, "|")
}

// TestFakeDeterministicReplay freezes that two fresh fake providers replaying
// the identical script produce identical receipts: no random source and no
// clock read participate.
func TestFakeDeterministicReplay(t *testing.T) {
	first := fakeScenarioFingerprint(t)
	second := fakeScenarioFingerprint(t)
	if first != second {
		t.Fatalf("two fresh fake providers must replay the identical script deterministically:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestFakeCheckpointDeterministic freezes that the checkpoint digest is a
// pure function of the staged content.
func TestFakeCheckpointDeterministic(t *testing.T) {
	ctx := context.Background()
	digests := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		fake := NewFakeProvider(FakeConfig{})
		allocationId := fmt.Sprintf("allocation-ckpt-%d", index)
		provisionTestAllocation(t, fake, allocationId)
		if _, err := fake.Stage(ctx, StageRequest{
			Identity:     testIdentity(allocationId, "command-stage"),
			AllocationId: allocationId,
			Inputs:       []StageInput{validInlineInput("input-"+"1", []byte("checkpoint-"+"payload"))},
		}); err != nil {
			t.Fatalf("stage: %v", err)
		}
		checkpoint, err := fake.Checkpoint(ctx, CheckpointRequest{Identity: testIdentity(allocationId, "command-checkpoint"), AllocationId: allocationId})
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		digests = append(digests, checkpoint.SHA256)
	}
	if digests[0] != digests[1] {
		t.Fatal("identical staged content must derive the identical checkpoint digest")
	}
}
