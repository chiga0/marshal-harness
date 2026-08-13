package sandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func validAllocation(allocationId string, generation int64, state AllocationState) SandboxAllocation {
	return SandboxAllocation{
		AllocationId:    allocationId,
		RunId:           "run-" + "1",
		AttemptId:       "attempt-" + "1",
		Generation:      generation,
		State:           state,
		AccessMode:      domain.AccessModeWorkspaceWrite,
		AssuranceLevel:  domain.AssuranceLevelWorkspaceWrite,
		AllowedStoreIds: []string{"store-" + "a"},
	}
}

// TestAllocationStateClosedEnumeration freezes the closed allocation state
// enumeration and the terminal set.
func TestAllocationStateClosedEnumeration(t *testing.T) {
	for _, state := range []AllocationState{AllocationProvisioning, AllocationActive, AllocationTerminated, AllocationReplaced, AllocationFailed} {
		if err := state.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed allocation state %q: %v", string(state), err)
		}
	}
	for _, state := range []AllocationState{"", "Active", "draining", "active "} {
		if err := state.Validate(); err == nil {
			t.Fatalf("Validate accepted the allocation state %q outside the closed enumeration", string(state))
		}
	}
	if !AllocationTerminated.IsTerminal() || !AllocationReplaced.IsTerminal() || !AllocationFailed.IsTerminal() {
		t.Fatal("terminated, replaced and failed must be terminal")
	}
	if AllocationActive.IsTerminal() || AllocationProvisioning.IsTerminal() {
		t.Fatal("active and provisioning must not be terminal")
	}
}

// TestSandboxAllocationValidate freezes the fail-closed allocation record
// validation.
func TestSandboxAllocationValidate(t *testing.T) {
	if err := validAllocation("allocation-"+"1", 1, AllocationActive).Validate(); err != nil {
		t.Fatalf("Validate rejected a valid allocation: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*SandboxAllocation)
	}{
		{"empty allocationId", func(a *SandboxAllocation) { a.AllocationId = "" }},
		{"empty runId", func(a *SandboxAllocation) { a.RunId = "" }},
		{"empty attemptId", func(a *SandboxAllocation) { a.AttemptId = "" }},
		{"zero generation", func(a *SandboxAllocation) { a.Generation = 0 }},
		{"unknown state", func(a *SandboxAllocation) { a.State = "draining" }},
		{"unknown access mode", func(a *SandboxAllocation) { a.AccessMode = "full-access" }},
		{"unknown assurance level", func(a *SandboxAllocation) { a.AssuranceLevel = "none" }},
		{"malformed evidence ref", func(a *SandboxAllocation) { a.ConformanceEvidenceRef = "sha256:" + "zz" }},
		{"url-shaped store alias", func(a *SandboxAllocation) { a.AllowedStoreIds = []string{"https://example.com"} }},
	}
	for _, tc := range cases {
		allocation := validAllocation("allocation-"+"1", 1, AllocationActive)
		tc.mutate(&allocation)
		if err := allocation.Validate(); err == nil {
			t.Fatalf("Validate accepted the allocation with %s", tc.name)
		}
	}
}

// TestCheckSingleActiveRejectsDualActive freezes the single-active
// invariant: within one (runId, attemptId) a second concurrent allocation at
// the current generation is rejected, stale generations are rejected, scopes
// must match, and a generation bump is admitted.
func TestCheckSingleActiveRejectsDualActive(t *testing.T) {
	current := validAllocation("allocation-"+"1", 1, AllocationActive)
	if err := CheckSingleActive(nil, current); err != nil {
		t.Fatalf("the first allocation must pass the single-active check: %v", err)
	}
	dual := validAllocation("allocation-"+"2", 1, AllocationActive)
	err := CheckSingleActive([]SandboxAllocation{current}, dual)
	if !errors.Is(err, ErrDuplicateActiveAllocation) {
		t.Fatalf("a concurrent dual-active allocation must be rejected with ErrDuplicateActiveAllocation, got %v", err)
	}
	gen2 := validAllocation("allocation-"+"1", 2, AllocationActive)
	stale := validAllocation("allocation-"+"2", 1, AllocationActive)
	err = CheckSingleActive([]SandboxAllocation{gen2}, stale)
	if !errors.Is(err, ErrStaleAllocationGeneration) {
		t.Fatalf("a stale generation must be rejected with ErrStaleAllocationGeneration, got %v", err)
	}
	next := validAllocation("allocation-"+"2", 3, AllocationActive)
	if err := CheckSingleActive([]SandboxAllocation{gen2}, next); err != nil {
		t.Fatalf("a replacement generation bump must be admitted: %v", err)
	}
	otherScope := validAllocation("allocation-"+"3", 3, AllocationActive)
	otherScope.RunId = "run-" + "2"
	err = CheckSingleActive([]SandboxAllocation{gen2}, otherScope)
	if !errors.Is(err, ErrAllocationScopeMismatch) {
		t.Fatalf("a scope mismatch must be rejected with ErrAllocationScopeMismatch, got %v", err)
	}
	if err := CheckSingleActive([]SandboxAllocation{gen2}, gen2); err != nil {
		t.Fatalf("re-admitting the identical allocation must stay idempotent: %v", err)
	}
	replaced := validAllocation("allocation-"+"1", 1, AllocationReplaced)
	if err := CheckSingleActive([]SandboxAllocation{replaced}, validAllocation("allocation-"+"2", 1, AllocationActive)); err != nil {
		t.Fatalf("a terminal previous allocation must not block the current generation: %v", err)
	}
}

// TestPlanRestoreReplacementIsDefault freezes the default replacement
// restore: a fresh allocationId, a monotonically increasing generation and
// the provisioning state.
func TestPlanRestoreReplacementIsDefault(t *testing.T) {
	previous := validAllocation("allocation-"+"1", 3, AllocationActive)
	next, err := PlanRestore(RestoreRequest{Previous: previous, NextAllocationId: "allocation-" + "2"})
	if err != nil {
		t.Fatalf("PlanRestore rejected a valid replacement restore: %v", err)
	}
	if next.AllocationId != "allocation-"+"2" {
		t.Fatalf("the replacement must carry the fresh allocationId, got %q", next.AllocationId)
	}
	if next.Generation != 4 {
		t.Fatalf("restore must bump the generation monotonically, got %d", next.Generation)
	}
	if next.State != AllocationProvisioning {
		t.Fatalf("the restored allocation must start provisioning, got %q", string(next.State))
	}
	if _, err := PlanRestore(RestoreRequest{Previous: previous}); err == nil {
		t.Fatal("a replacement restore without a next allocationId must be rejected")
	} else if !errors.Is(err, ErrRestoreRejected) {
		t.Fatalf("the rejection must surface ErrRestoreRejected, got %v", err)
	}
	if _, err := PlanRestore(RestoreRequest{Previous: previous, NextAllocationId: previous.AllocationId}); err == nil {
		t.Fatal("a replacement restore must not reuse the previous allocationId")
	}
}

// TestPlanRestoreInPlaceRequiresConfirmation freezes that an in-place
// restore is permitted only with the caller's confirmation flag and keeps
// the allocationId.
func TestPlanRestoreInPlaceRequiresConfirmation(t *testing.T) {
	previous := validAllocation("allocation-"+"1", 3, AllocationActive)
	next, err := PlanRestore(RestoreRequest{Previous: previous, InPlaceConfirmed: true})
	if err != nil {
		t.Fatalf("PlanRestore rejected a confirmed in-place restore: %v", err)
	}
	if next.AllocationId != previous.AllocationId {
		t.Fatal("an in-place restore must keep the allocationId")
	}
	if next.Generation != 4 {
		t.Fatalf("an in-place restore must bump the generation, got %d", next.Generation)
	}
	if _, err := PlanRestore(RestoreRequest{Previous: previous, InPlaceConfirmed: true, NextAllocationId: "allocation-" + "2"}); err == nil {
		t.Fatal("an in-place restore must not carry a replacement allocationId")
	}
}

// TestStaleHandleRejectedAfterRestore freezes that a stale handle presented
// after a restore (old generation against the replaced allocation) is
// rejected fail closed.
func TestStaleHandleRejectedAfterRestore(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocationId := "allocation-" + "restore"
	provisioned, err := fake.Provision(ctx, ProvisionRequest{
		Identity:        testIdentity(allocationId, "command-provision"),
		Requirements:    workspaceWriteRequirements(),
		AllowedStoreIds: []string{"store-" + "a"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	restored, err := fake.Restore(ctx, RestoreOperationRequest{
		Identity:             testIdentityWithGeneration(allocationId, "command-restore", 2),
		PreviousAllocationId: provisioned.Allocation.AllocationId,
		NextAllocationId:     "allocation-" + "restored",
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Allocation.Generation != 2 {
		t.Fatalf("the restored allocation must carry generation 2, got %d", restored.Allocation.Generation)
	}
	stale := testIdentity(provisioned.Allocation.AllocationId, "command-stale-write")
	stale.Generation = 1
	_, err = fake.Exec(ctx, ExecRequest{
		Identity:     stale,
		AllocationId: provisioned.Allocation.AllocationId,
		Command:      []string{"write-" + "1"},
	})
	if err == nil {
		t.Fatal("a stale-generation handle must be rejected after a restore")
	}
	staleOnNext := testIdentityWithGeneration(restored.Allocation.AllocationId, "command-stale-next", 1)
	_, err = fake.Exec(ctx, ExecRequest{
		Identity:     staleOnNext,
		AllocationId: restored.Allocation.AllocationId,
		Command:      []string{"write-" + "2"},
	})
	if err == nil {
		t.Fatal("an old-generation identity must be rejected against the restored allocation")
	}
	current := testIdentityWithGeneration(restored.Allocation.AllocationId, "command-current", 2)
	if _, err := fake.Exec(ctx, ExecRequest{
		Identity:     current,
		AllocationId: restored.Allocation.AllocationId,
		Command:      []string{"write-" + "3"},
	}); err != nil {
		t.Fatalf("the current-generation handle must be admitted after a restore: %v", err)
	}
}
