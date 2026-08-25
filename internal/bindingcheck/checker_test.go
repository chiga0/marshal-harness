package bindingcheck

import (
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

// ── shared fixtures ───────────────────────────────────────────────────────────

const (
	snapDigest1  = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	snapDigest2  = "sha256:0000000000000000000000000000000000000000000000000000000000000002"
	snapDigest3  = "sha256:0000000000000000000000000000000000000000000000000000000000000003"
	reqDigest    = "sha256:0000000000000000000000000000000000000000000000000000000000000099"
	compatDigest = "sha256:00000000000000000000000000000000000000000000000000000000000000cc"

	regID1 = "registration:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	regID2 = "registration:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	spRegID  = "registration:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	allocID1 = "alloc-1"
)

var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func makeReg(registrationID, idempKey string) agentregistry.AgentRegistration {
	return agentregistry.AgentRegistration{
		RegistrationID:       registrationID,
		AuthorityNamespaceID: "ns-1",
		SecurityDomainID:     "sd-1",
		Principal:            "principal-1",
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         "test-agent",
		ProviderVersion:      "1.0.0",
		ProtocolVersion:      "v1",
		Scope:                "task",
		IdempotencyKey:       idempKey,
		RequestDigest:        reqDigest,
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            baseTime,
		UpdatedAt:            baseTime,
	}
}

func makeSnap(registrationID, snapDigest string) agentregistry.AgentCapabilitySnapshot {
	return agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             snapDigest,
		RegistrationID:             registrationID,
		ProtocolVersion:            "v1",
		ProviderName:               "test-agent",
		ProviderVersion:            "1.0.0",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
}

// mustAgentBinding constructs a valid AgentBinding or panics.
func mustAgentBinding(registrationID, snapDigest string) runtimeprofile.AgentBinding {
	b, err := runtimeprofile.NewAgentBinding(registrationID, snapDigest, "test-agent", "1.0.0", "v1")
	if err != nil {
		panic(err)
	}
	return b
}

// mustSandboxBinding constructs a valid SandboxBinding or panics.
func mustSandboxBinding(allocationID string, generation int64) runtimeprofile.SandboxBinding {
	b, err := runtimeprofile.NewSandboxBinding(spRegID, allocationID, generation)
	if err != nil {
		panic(err)
	}
	return b
}

// mustProfile constructs a valid WorkerRuntimeProfile or panics.
func mustProfile(agent runtimeprofile.AgentBinding, sandbox runtimeprofile.SandboxBinding) runtimeprofile.WorkerRuntimeProfile {
	p, err := runtimeprofile.NewProfile(agent, sandbox, compatDigest)
	if err != nil {
		panic(err)
	}
	return p
}

// setupHappyPath builds a registry+ledger pair with one active agent and one
// active sandbox allocation, and returns the matching profile.
func setupHappyPath(t *testing.T) (*agentregistry.Registry, *SandboxLedger, runtimeprofile.WorkerRuntimeProfile) {
	t.Helper()
	reg := agentregistry.NewRegistry()
	if _, err := reg.Register(makeReg(regID1, "key-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.AddSnapshot(makeSnap(regID1, snapDigest1)); err != nil {
		t.Fatalf("add snapshot: %v", err)
	}

	ledger := NewSandboxLedger()
	if _, err := ledger.PutAllocation(allocID1, spRegID, 1); err != nil {
		t.Fatalf("put allocation: %v", err)
	}

	agent := mustAgentBinding(regID1, snapDigest1)
	sandbox := mustSandboxBinding(allocID1, 1)
	profile := mustProfile(agent, sandbox)
	return reg, ledger, profile
}

// ── NewChecker ────────────────────────────────────────────────────────────────

func TestNewChecker_NilRegistry(t *testing.T) {
	_, err := NewChecker(nil, NewSandboxLedger())
	if err == nil {
		t.Fatal("expected error for nil registry")
	}
	if !strings.HasPrefix(err.Error(), "bindingcheck:") {
		t.Errorf("error must have bindingcheck: prefix, got: %v", err)
	}
}

func TestNewChecker_NilLedger(t *testing.T) {
	_, err := NewChecker(agentregistry.NewRegistry(), nil)
	if err == nil {
		t.Fatal("expected error for nil ledger")
	}
	if !strings.HasPrefix(err.Error(), "bindingcheck:") {
		t.Errorf("error must have bindingcheck: prefix, got: %v", err)
	}
}

// ── Happy path ────────────────────────────────────────────────────────────────

func TestRecheck_BothActive_Accepted(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	checker, err := NewChecker(reg, ledger)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted() {
		t.Errorf("expected accepted, agent=%+v sandbox=%+v", result.Agent, result.Sandbox)
	}
}

// ── Agent-side individual reason tests ───────────────────────────────────────

func TestRecheck_AgentUnknownRegistration(t *testing.T) {
	reg := agentregistry.NewRegistry()
	ledger := NewSandboxLedger()
	if _, err := ledger.PutAllocation(allocID1, spRegID, 1); err != nil {
		t.Fatalf("setup: %v", err)
	}

	agent := mustAgentBinding(regID1, snapDigest1)
	sandbox := mustSandboxBinding(allocID1, 1)
	profile := mustProfile(agent, sandbox)

	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent.OK {
		t.Error("agent side should fail")
	}
	if !containsReason(result.Agent.Reasons, RejectionReasonAgentUnknownRegistration) {
		t.Errorf("expected agent-unknown-registration, got %v", result.Agent.Reasons)
	}
	if !result.Sandbox.OK {
		t.Errorf("sandbox side must pass independently, got %v", result.Sandbox.Reasons)
	}
}

func TestRecheck_AgentRegistrationInactive_Suspended(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if _, err := reg.Suspend(regID1); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent.OK {
		t.Error("agent side should fail after suspension")
	}
	if !containsReason(result.Agent.Reasons, RejectionReasonAgentRegistrationInactive) {
		t.Errorf("expected agent-registration-inactive, got %v", result.Agent.Reasons)
	}
	if !result.Sandbox.OK {
		t.Errorf("sandbox must pass independently, got %v", result.Sandbox.Reasons)
	}
}

func TestRecheck_AgentRegistrationInactive_Revoked(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if _, err := reg.Revoke(regID1); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, _ := checker.Recheck(profile)
	if result.Agent.OK {
		t.Error("agent side should fail after revocation")
	}
	if !containsReason(result.Agent.Reasons, RejectionReasonAgentRegistrationInactive) {
		t.Errorf("expected agent-registration-inactive, got %v", result.Agent.Reasons)
	}
}

func TestRecheck_AgentSnapshotMismatch(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	// Add a different snapshot as active (replace replaces activeSnapshot entry)
	snap2 := makeSnap(regID1, snapDigest2)
	if _, err := reg.AddSnapshot(snap2); err != nil {
		t.Fatalf("add snapshot2: %v", err)
	}
	// profile still carries snapDigest1 but active snapshot is now snapDigest2
	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Agent.OK {
		t.Error("agent side should fail on snapshot mismatch")
	}
	if !containsReason(result.Agent.Reasons, RejectionReasonAgentSnapshotMismatch) {
		t.Errorf("expected agent-snapshot-mismatch, got %v", result.Agent.Reasons)
	}
	if !result.Sandbox.OK {
		t.Errorf("sandbox must pass independently, got %v", result.Sandbox.Reasons)
	}
}

// ── Sandbox-side individual reason tests ─────────────────────────────────────

func TestRecheck_SandboxUnknownAllocation(t *testing.T) {
	reg := agentregistry.NewRegistry()
	if _, err := reg.Register(makeReg(regID1, "key-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.AddSnapshot(makeSnap(regID1, snapDigest1)); err != nil {
		t.Fatalf("add snapshot: %v", err)
	}

	ledger := NewSandboxLedger() // empty ledger

	agent := mustAgentBinding(regID1, snapDigest1)
	sandbox := mustSandboxBinding(allocID1, 1)
	profile := mustProfile(agent, sandbox)

	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sandbox.OK {
		t.Error("sandbox side should fail on unknown allocation")
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxUnknownAllocation) {
		t.Errorf("expected sandbox-unknown-allocation, got %v", result.Sandbox.Reasons)
	}
	if !result.Agent.OK {
		t.Errorf("agent must pass independently, got %v", result.Agent.Reasons)
	}
}

func TestRecheck_SandboxAllocationInactive_Revoked(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if err := ledger.Revoke(allocID1); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sandbox.OK {
		t.Error("sandbox side should fail after revocation")
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxAllocationInactive) {
		t.Errorf("expected sandbox-allocation-inactive, got %v", result.Sandbox.Reasons)
	}
	if !result.Agent.OK {
		t.Errorf("agent must pass independently, got %v", result.Agent.Reasons)
	}
}

func TestRecheck_SandboxAllocationInactive_Expired(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if err := ledger.Expire(allocID1); err != nil {
		t.Fatalf("expire: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, _ := checker.Recheck(profile)
	if result.Sandbox.OK {
		t.Error("sandbox side should fail after expiry")
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxAllocationInactive) {
		t.Errorf("expected sandbox-allocation-inactive, got %v", result.Sandbox.Reasons)
	}
}

func TestRecheck_SandboxGenerationMismatch_Ahead(t *testing.T) {
	reg, ledger, _ := setupHappyPath(t)
	// profile carries generation 1, but ledger has been replaced to generation 2
	if _, err := ledger.Replace(allocID1); err != nil {
		t.Fatalf("replace: %v", err)
	}
	// profile still has gen 1
	agent := mustAgentBinding(regID1, snapDigest1)
	sandbox := mustSandboxBinding(allocID1, 1)
	profile := mustProfile(agent, sandbox)

	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sandbox.OK {
		t.Error("sandbox side should fail: ledger gen 2, binding gen 1")
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxGenerationMismatch) {
		t.Errorf("expected sandbox-generation-mismatch, got %v", result.Sandbox.Reasons)
	}
}

func TestRecheck_SandboxGenerationMismatch_Behind(t *testing.T) {
	reg, ledger, _ := setupHappyPath(t)
	// ledger stays at generation 1, but binding claims generation 3
	agent := mustAgentBinding(regID1, snapDigest1)
	sandbox := mustSandboxBinding(allocID1, 3) // ahead of ledger
	profile := mustProfile(agent, sandbox)

	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sandbox.OK {
		t.Error("sandbox side should fail: binding gen 3, ledger gen 1")
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxGenerationMismatch) {
		t.Errorf("expected sandbox-generation-mismatch, got %v", result.Sandbox.Reasons)
	}
}

// ── Dual failure: both sides fail simultaneously ──────────────────────────────

func TestRecheck_BothSidesFail(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if _, err := reg.Revoke(regID1); err != nil {
		t.Fatalf("revoke agent: %v", err)
	}
	if err := ledger.Revoke(allocID1); err != nil {
		t.Fatalf("revoke sandbox: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Accepted() {
		t.Error("result must not be accepted when both sides fail")
	}
	if result.Agent.OK {
		t.Error("agent side should fail")
	}
	if result.Sandbox.OK {
		t.Error("sandbox side should fail")
	}
	if !containsReason(result.Agent.Reasons, RejectionReasonAgentRegistrationInactive) {
		t.Errorf("expected agent-registration-inactive, got %v", result.Agent.Reasons)
	}
	if !containsReason(result.Sandbox.Reasons, RejectionReasonSandboxAllocationInactive) {
		t.Errorf("expected sandbox-allocation-inactive, got %v", result.Sandbox.Reasons)
	}
}

// ── Independence assertions ───────────────────────────────────────────────────

func TestRecheck_AgentRevokedSandboxStillOK(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if _, err := reg.Revoke(regID1); err != nil {
		t.Fatalf("revoke agent: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, _ := checker.Recheck(profile)
	if result.Sandbox.OK != true {
		t.Errorf("sandbox must be independently OK after agent revocation, reasons=%v", result.Sandbox.Reasons)
	}
	if result.Agent.OK {
		t.Error("agent side must fail after revocation")
	}
}

func TestRecheck_SandboxRevokedAgentStillOK(t *testing.T) {
	reg, ledger, profile := setupHappyPath(t)
	if err := ledger.Revoke(allocID1); err != nil {
		t.Fatalf("revoke sandbox: %v", err)
	}
	checker, _ := NewChecker(reg, ledger)
	result, _ := checker.Recheck(profile)
	if result.Agent.OK != true {
		t.Errorf("agent must be independently OK after sandbox revocation, reasons=%v", result.Agent.Reasons)
	}
	if result.Sandbox.OK {
		t.Error("sandbox side must fail after revocation")
	}
}

// ── Replace semantics ─────────────────────────────────────────────────────────

func TestRecheck_AgentReplace_OldBindingFails_NewBindingPasses(t *testing.T) {
	reg, ledger, oldProfile := setupHappyPath(t)

	// Register new agent registration and snapshot
	newReg := makeReg(regID2, "key-2")
	if _, err := reg.Register(newReg); err != nil {
		t.Fatalf("register new: %v", err)
	}
	if _, err := reg.AddSnapshot(makeSnap(regID2, snapDigest2)); err != nil {
		t.Fatalf("add new snapshot: %v", err)
	}
	// Replace old registration (marks as replaced)
	if _, err := reg.Replace(regID1); err != nil {
		t.Fatalf("replace old reg: %v", err)
	}

	checker, _ := NewChecker(reg, ledger)

	// Old binding must fail
	oldResult, err := checker.Recheck(oldProfile)
	if err != nil {
		t.Fatalf("unexpected error old profile: %v", err)
	}
	if oldResult.Agent.OK {
		t.Error("old agent binding must fail after replace")
	}

	// New binding via ReplaceAgentBinding must pass
	newAgentBinding := mustAgentBinding(regID2, snapDigest2)
	newProfile, err := runtimeprofile.ReplaceAgentBinding(oldProfile, newAgentBinding)
	if err != nil {
		t.Fatalf("ReplaceAgentBinding: %v", err)
	}
	newResult, err := checker.Recheck(newProfile)
	if err != nil {
		t.Fatalf("unexpected error new profile: %v", err)
	}
	if !newResult.Agent.OK {
		t.Errorf("new agent binding must pass, reasons=%v", newResult.Agent.Reasons)
	}
	if !newResult.Sandbox.OK {
		t.Errorf("sandbox must still pass, reasons=%v", newResult.Sandbox.Reasons)
	}
}

func TestRecheck_SandboxReplace_OldGenerationFails_NewGenerationPasses(t *testing.T) {
	reg, ledger, oldProfile := setupHappyPath(t)

	// Replace sandbox: ledger gen 1 → gen 2
	if _, err := ledger.Replace(allocID1); err != nil {
		t.Fatalf("replace allocation: %v", err)
	}

	checker, _ := NewChecker(reg, ledger)

	// Old binding (gen 1) must fail
	oldResult, _ := checker.Recheck(oldProfile)
	if oldResult.Sandbox.OK {
		t.Error("old sandbox binding (gen 1) must fail after replace to gen 2")
	}

	// New binding via ReplaceSandboxBinding (gen 2) must pass
	newSandboxBinding := mustSandboxBinding(allocID1, 2)
	newProfile, err := runtimeprofile.ReplaceSandboxBinding(oldProfile, newSandboxBinding)
	if err != nil {
		t.Fatalf("ReplaceSandboxBinding: %v", err)
	}
	newResult, err := checker.Recheck(newProfile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !newResult.Sandbox.OK {
		t.Errorf("new sandbox binding must pass, reasons=%v", newResult.Sandbox.Reasons)
	}
	if !newResult.Agent.OK {
		t.Errorf("agent must still pass, reasons=%v", newResult.Agent.Reasons)
	}
}

func TestRecheck_BothReplace_NewCombinationPasses(t *testing.T) {
	reg, ledger, _ := setupHappyPath(t)

	// Register new agent
	if _, err := reg.Register(makeReg(regID2, "key-2")); err != nil {
		t.Fatalf("register new: %v", err)
	}
	if _, err := reg.AddSnapshot(makeSnap(regID2, snapDigest2)); err != nil {
		t.Fatalf("add snapshot: %v", err)
	}
	if _, err := reg.Replace(regID1); err != nil {
		t.Fatalf("replace old reg: %v", err)
	}

	// Replace sandbox
	if _, err := ledger.Replace(allocID1); err != nil {
		t.Fatalf("replace sandbox: %v", err)
	}

	newAgent := mustAgentBinding(regID2, snapDigest2)
	newSandbox := mustSandboxBinding(allocID1, 2)
	newProfile := mustProfile(newAgent, newSandbox)

	checker, _ := NewChecker(reg, ledger)
	result, err := checker.Recheck(newProfile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted() {
		t.Errorf("new combination must be accepted, agent=%+v sandbox=%+v", result.Agent, result.Sandbox)
	}
}

// ── Malformed input: fail closed with bindingcheck: prefix ───────────────────

func TestRecheck_ZeroValueProfile_FailClosed(t *testing.T) {
	reg := agentregistry.NewRegistry()
	checker, _ := NewChecker(reg, NewSandboxLedger())
	_, err := checker.Recheck(runtimeprofile.WorkerRuntimeProfile{})
	if err == nil {
		t.Fatal("expected error for zero-value profile")
	}
	if !strings.HasPrefix(err.Error(), "bindingcheck:") {
		t.Errorf("error must have bindingcheck: prefix, got: %v", err)
	}
}

func TestRecheck_EmptyAllocationID_FailClosed(t *testing.T) {
	reg := agentregistry.NewRegistry()
	if _, err := reg.Register(makeReg(regID1, "key-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.AddSnapshot(makeSnap(regID1, snapDigest1)); err != nil {
		t.Fatalf("add snapshot: %v", err)
	}
	agent := mustAgentBinding(regID1, snapDigest1)
	// SandboxBinding with empty AllocationID won't pass NewSandboxBinding validation,
	// so create it directly and embed in profile without a valid SandboxBindingDigest.
	// The profile Validate() call in Recheck will catch the malformed sandbox binding.
	_, err := runtimeprofile.NewSandboxBinding(spRegID, "", 1)
	if err == nil {
		t.Fatal("expected NewSandboxBinding to fail on empty AllocationID")
	}
	// Directly confirm a zero-value SandboxBinding causes fail closed.
	badProfile := runtimeprofile.WorkerRuntimeProfile{
		Agent:   agent,
		Sandbox: runtimeprofile.SandboxBinding{},
	}
	checker, _ := NewChecker(reg, NewSandboxLedger())
	_, recheckErr := checker.Recheck(badProfile)
	if recheckErr == nil {
		t.Fatal("expected error for profile with zero-value SandboxBinding")
	}
	if !strings.HasPrefix(recheckErr.Error(), "bindingcheck:") {
		t.Errorf("error must have bindingcheck: prefix, got: %v", recheckErr)
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

func containsReason(reasons []RejectionReason, want RejectionReason) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
