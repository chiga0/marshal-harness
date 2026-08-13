package sandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// The fake provider must satisfy the frozen SPI.
var _ SandboxProvider = (*FakeProvider)(nil)

// TestEveryOperationFailsClosedOnInvalidIdentity freezes that all ten
// operations reject an invalid identity before any side effect.
func TestEveryOperationFailsClosedOnInvalidIdentity(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	requirements := workspaceWriteRequirements()
	invalid := OperationIdentity{}
	if _, err := fake.Probe(ctx, ProbeRequest{Identity: invalid, Requirements: requirements}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("probe: %v", err)
	}
	if _, err := fake.Provision(ctx, ProvisionRequest{Identity: invalid, Requirements: requirements}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("provision: %v", err)
	}
	if _, err := fake.Stage(ctx, StageRequest{Identity: invalid}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("stage: %v", err)
	}
	if _, err := fake.Exec(ctx, ExecRequest{Identity: invalid, Command: []string{"echo-" + "1"}}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("exec: %v", err)
	}
	if _, err := fake.Inspect(ctx, InspectRequest{Identity: invalid}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := fake.Signal(ctx, SignalRequest{Identity: invalid, Signal: SignalTerm}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("signal: %v", err)
	}
	if _, err := fake.Checkpoint(ctx, CheckpointRequest{Identity: invalid}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := fake.Restore(ctx, RestoreOperationRequest{Identity: invalid}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("restore: %v", err)
	}
	if _, err := fake.Terminate(ctx, TerminateRequest{Identity: invalid}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("terminate: %v", err)
	}
	if _, err := fake.Reconcile(ctx, ReconcileRequest{Identity: invalid, RunId: "run-" + "1", AttemptId: "attempt-" + "1"}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("reconcile: %v", err)
	}
}

// TestPublisherRoleRejectedAtTheSPI freezes that publisher never validates
// as a workload role at the SPI boundary.
func TestPublisherRoleRejectedAtTheSPI(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	id := testIdentity("allocation-"+"publisher", "command-probe")
	id.WorkloadRole = "publisher"
	if _, err := fake.Probe(ctx, ProbeRequest{Identity: id, Requirements: workspaceWriteRequirements()}); !errors.Is(err, ErrInvalidOperationIdentity) {
		t.Fatalf("publisher must never validate as a workload role, got %v", err)
	}
	id.WorkloadRole = WorkloadRoleVerifier
	if _, err := fake.Probe(ctx, ProbeRequest{Identity: id, Requirements: workspaceWriteRequirements()}); err != nil {
		t.Fatalf("verifier is a closed workload role and must validate: %v", err)
	}
}

// TestProvisionReceiptObservationSemantics freezes that the provision
// receipt observes the granted two-dimensional combination exactly: it
// validates as a record and honors the requirements without downgrade, but
// it is never treated as an authority grant.
func TestProvisionReceiptObservationSemantics(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{ConformanceEvidenceRef: validEvidenceRef()})
	requirements := hardenedRequirements()
	receipt, err := fake.Provision(ctx, ProvisionRequest{
		Identity:        testIdentity("allocation-"+"observation", "command-provision"),
		Requirements:    requirements,
		AllowedStoreIds: []string{"store-" + "observation"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := receipt.Allocation.Validate(); err != nil {
		t.Fatalf("the observed allocation must validate: %v", err)
	}
	if err := ValidateAllocationRequirements(receipt.Allocation, requirements); err != nil {
		t.Fatalf("the granted combination must honor the requirements without downgrade: %v", err)
	}
	if receipt.Allocation.AccessMode != domain.AccessModeWorkspaceWrite {
		t.Fatalf("the access mode must be granted unchanged, got %q", string(receipt.Allocation.AccessMode))
	}
	if receipt.Allocation.AssuranceLevel != domain.AssuranceLevelHardened {
		t.Fatalf("the assurance level must never be downgraded, got %q", string(receipt.Allocation.AssuranceLevel))
	}
	if receipt.Allocation.ConformanceEvidenceRef != validEvidenceRef() {
		t.Fatal("the allocation must carry the evidence reference it was granted with")
	}
}

// TestAssuranceGateHelpers freezes the gate helpers against downgrade and
// access-mode mismatch.
func TestAssuranceGateHelpers(t *testing.T) {
	if err := CheckAssuranceGate(hardenedRequirements(), ""); !errors.Is(err, ErrAssuranceNotMet) {
		t.Fatalf("hardened without evidence must fail closed, got %v", err)
	}
	if err := CheckAssuranceGate(hardenedRequirements(), validEvidenceRef()); err != nil {
		t.Fatalf("hardened with valid evidence must pass the gate: %v", err)
	}
	if err := CheckAssuranceGate(workspaceWriteRequirements(), ""); err != nil {
		t.Fatalf("workspace-write must not require evidence: %v", err)
	}
	allocation := validAllocation("allocation-"+"gate", 1, AllocationActive)
	err := ValidateAllocationRequirements(allocation, hardenedRequirements())
	if !errors.Is(err, ErrAssuranceDowngrade) {
		t.Fatalf("a workspace-write allocation against a hardened request must fail with ErrAssuranceDowngrade, got %v", err)
	}
	mismatched := allocation
	mismatched.AccessMode = domain.AccessModeReadOnly
	err = ValidateAllocationRequirements(mismatched, workspaceWriteRequirements())
	if !errors.Is(err, ErrAccessModeMismatch) {
		t.Fatalf("an access mode mismatch must fail with ErrAccessModeMismatch, got %v", err)
	}
}

// TestSignalNameClosedEnumeration freezes the closed signal enumeration.
func TestSignalNameClosedEnumeration(t *testing.T) {
	for _, signal := range []SignalName{SignalTerm, SignalKill, SignalInterrupt} {
		if err := signal.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed signal %q: %v", string(signal), err)
		}
	}
	for _, signal := range []SignalName{"", "SIGTERM", "term "} {
		if err := signal.Validate(); err == nil {
			t.Fatalf("Validate accepted the signal %q outside the closed enumeration", string(signal))
		}
	}
}

// TestExecutionStatusClosedEnumeration freezes the closed execution status
// enumeration.
func TestExecutionStatusClosedEnumeration(t *testing.T) {
	for _, status := range []ExecutionStatus{ExecutionCompleted, ExecutionFailed, ExecutionKilled} {
		if err := status.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed status %q: %v", string(status), err)
		}
	}
	for _, status := range []ExecutionStatus{"", "done", "COMPLETED"} {
		if err := status.Validate(); err == nil {
			t.Fatalf("Validate accepted the status %q outside the closed enumeration", string(status))
		}
	}
}

// TestSignalAndTerminateLifecycle exercises the remaining lifecycle guards
// of the fake provider: signal delivery, idempotent termination and the
// stale-generation rejection on terminate.
func TestSignalAndTerminateLifecycle(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"lifecycle")
	receipt, err := fake.Signal(ctx, SignalRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-signal"),
		AllocationId: allocation.AllocationId,
		Signal:       SignalTerm,
	})
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !receipt.Delivered {
		t.Fatal("the signal must be delivered to an active allocation")
	}
	terminate, err := fake.Terminate(ctx, TerminateRequest{Identity: testIdentity(allocation.AllocationId, "command-terminate"), AllocationId: allocation.AllocationId})
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if terminate.State != AllocationTerminated {
		t.Fatalf("terminate must reach the terminated state, got %q", string(terminate.State))
	}
	again, err := fake.Terminate(ctx, TerminateRequest{Identity: testIdentity(allocation.AllocationId, "command-terminate-again"), AllocationId: allocation.AllocationId})
	if err != nil {
		t.Fatalf("terminate must stay idempotent on a terminal allocation: %v", err)
	}
	if again.State != AllocationTerminated {
		t.Fatalf("an idempotent terminate must keep the terminated state, got %q", string(again.State))
	}
	stale := testIdentityWithGeneration(allocation.AllocationId, "command-stale-terminate", 4)
	if _, err := fake.Terminate(ctx, TerminateRequest{Identity: stale, AllocationId: allocation.AllocationId}); err == nil {
		t.Fatal("a stale-generation terminate must be rejected")
	}
	if _, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-after-terminate"),
		AllocationId: allocation.AllocationId,
		Command:      []string{"echo-" + "2"},
	}); err == nil {
		t.Fatal("exec must reject a terminated allocation")
	}
}
