package attemptgate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const (
	testAgentRegistrationID   = "registration:aaaa1111"
	testSandboxRegistrationID = "registration:bbbb2222"
	testAllocationID          = "alloc-0001"
	testAttemptID             = "attempt-0001"
)

func testEvidenceDigest() string     { return digestOf("agent-evidence", "verifier-1") }
func testSnapshotDigest() string     { return digestOf("agent-snapshot", "v1") }
func testCompatDigest() string       { return digestOf("compat") }
func testRegistrationDigest() string { return digestOf("registration-request") }

func mustRegistry(t *testing.T, evidenceDigests []string) *agentregistry.Registry {
	t.Helper()
	reg := agentregistry.NewRegistry()
	registration := agentregistry.AgentRegistration{
		RegistrationID:       testAgentRegistrationID,
		AuthorityNamespaceID: "authority:test",
		SecurityDomainID:     "domain:execution:test",
		Principal:            "principal:agent-1",
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         "fake-agent",
		ProviderVersion:      "0.1.0",
		ProtocolVersion:      "acp/v1",
		Scope:                "scope:test",
		IdempotencyKey:       "idem-1",
		RequestDigest:        testRegistrationDigest(),
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            time.Unix(1000, 0).UTC(),
		UpdatedAt:            time.Unix(1000, 0).UTC(),
	}
	if _, err := reg.Register(registration); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             testSnapshotDigest(),
		RegistrationID:             testAgentRegistrationID,
		ProtocolVersion:            "acp/v1",
		ProviderName:               "fake-agent",
		ProviderVersion:            "0.1.0",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: evidenceDigests,
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if _, err := reg.AddSnapshot(snap); err != nil {
		t.Fatalf("AddSnapshot: %v", err)
	}
	return reg
}

func mustLedger(t *testing.T) *bindingcheck.SandboxLedger {
	t.Helper()
	ledger := bindingcheck.NewSandboxLedger()
	if _, err := ledger.PutAllocation(testAllocationID, testSandboxRegistrationID, 1); err != nil {
		t.Fatalf("PutAllocation: %v", err)
	}
	return ledger
}

func mustProfile(t *testing.T) runtimeprofile.WorkerRuntimeProfile {
	t.Helper()
	return mustProfileWith(t, testAgentRegistrationID, testAllocationID, 1, testSnapshotDigest())
}

func mustProfileWith(t *testing.T, agentRegID, allocationID string, generation int64, snapshotDigest string) runtimeprofile.WorkerRuntimeProfile {
	t.Helper()
	agent, err := runtimeprofile.NewAgentBinding(agentRegID, snapshotDigest, "fake-agent", "0.1.0", "acp/v1")
	if err != nil {
		t.Fatalf("NewAgentBinding: %v", err)
	}
	sandbox, err := runtimeprofile.NewSandboxBinding(testSandboxRegistrationID, allocationID, generation)
	if err != nil {
		t.Fatalf("NewSandboxBinding: %v", err)
	}
	profile, err := runtimeprofile.NewProfile(agent, sandbox, testCompatDigest())
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	return profile
}

func mustGate(t *testing.T, reg *agentregistry.Registry, ledger *bindingcheck.SandboxLedger) (*Gate, *AttemptProfileStore) {
	t.Helper()
	checker, err := bindingcheck.NewChecker(reg, ledger)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	store := NewAttemptProfileStore()
	gate, err := NewGate(store, checker, reg)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	return gate, store
}

// ── AttemptProfileStore ─────────────────────────────────────────────────────

func TestStore_BindAndResolve(t *testing.T) {
	store := NewAttemptProfileStore()
	profile := mustProfile(t)

	if err := store.Bind(testAttemptID, profile); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	resolved, err := store.Resolve(testAttemptID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ProfileDigest != profile.ProfileDigest {
		t.Errorf("resolved profile digest = %q, want %q", resolved.ProfileDigest, profile.ProfileDigest)
	}
}

func TestStore_BindIdempotentSameProfile(t *testing.T) {
	store := NewAttemptProfileStore()
	profile := mustProfile(t)
	if err := store.Bind(testAttemptID, profile); err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	if err := store.Bind(testAttemptID, profile); err != nil {
		t.Errorf("idempotent re-bind must succeed, got %v", err)
	}
}

func TestStore_BindConflict(t *testing.T) {
	store := NewAttemptProfileStore()
	profile := mustProfile(t)
	other := mustProfileWith(t, testAgentRegistrationID, "alloc-other", 1, testSnapshotDigest())

	if err := store.Bind(testAttemptID, profile); err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	err := store.Bind(testAttemptID, other)
	if !errors.Is(err, ErrProfileConflict) {
		t.Errorf("expected ErrProfileConflict, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "attemptgate: ") {
		t.Errorf("error %q missing attemptgate: prefix", err.Error())
	}
}

func TestStore_ResolveUnknown(t *testing.T) {
	store := NewAttemptProfileStore()
	_, err := store.Resolve("never-bound")
	if !errors.Is(err, ErrUnknownAttempt) {
		t.Errorf("expected ErrUnknownAttempt, got %v", err)
	}
}

func TestStore_InvalidInputs(t *testing.T) {
	store := NewAttemptProfileStore()
	profile := mustProfile(t)

	if err := store.Bind("", profile); !errors.Is(err, ErrInvalidAttempt) {
		t.Errorf("empty attemptID: expected ErrInvalidAttempt, got %v", err)
	}
	if _, err := store.Resolve(""); !errors.Is(err, ErrInvalidAttempt) {
		t.Errorf("empty resolve: expected ErrInvalidAttempt, got %v", err)
	}

	var zero runtimeprofile.WorkerRuntimeProfile
	if err := store.Bind("attempt-x", zero); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("zero profile: expected ErrInvalidProfile, got %v", err)
	}
}

// ── Gate：双侧独立 recheck ──────────────────────────────────────────────────

func TestGate_AdmitPositive(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)

	profile := mustProfile(t)
	if err := store.Bind(testAttemptID, profile); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if !decision.Accepted {
		t.Errorf("expected Accepted, got %+v", decision)
	}
	if !decision.Agent.OK || !decision.Sandbox.OK || !decision.EvidenceOK {
		t.Errorf("expected all sides OK, got %+v", decision)
	}
	if decision.ProfileDigest != profile.ProfileDigest {
		t.Errorf("decision profile digest = %q, want %q", decision.ProfileDigest, profile.ProfileDigest)
	}
}

func TestGate_UnknownAttemptFailsClosed(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, _ := mustGate(t, reg, ledger)

	_, err := gate.AdmitAttemptResult("never-bound", testEvidenceDigest())
	if !errors.Is(err, ErrUnknownAttempt) {
		t.Errorf("expected ErrUnknownAttempt, got %v", err)
	}
}

func TestGate_MalformedInputs(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, _ := mustGate(t, reg, ledger)

	if _, err := gate.AdmitAttemptResult("", testEvidenceDigest()); !errors.Is(err, ErrInvalidAttempt) {
		t.Errorf("empty attempt: expected ErrInvalidAttempt, got %v", err)
	}
	if _, err := gate.AdmitAttemptResult(testAttemptID, "sha256:short"); !errors.Is(err, ErrMalformedEvidenceDigest) {
		t.Errorf("malformed evidence: expected ErrMalformedEvidenceDigest, got %v", err)
	}
	if _, err := gate.AdmitAttemptResult(testAttemptID, ""); !errors.Is(err, ErrMalformedEvidenceDigest) {
		t.Errorf("empty evidence: expected ErrMalformedEvidenceDigest, got %v", err)
	}
}

// 仅 Agent 侧 revoke：Agent 拒绝、Sandbox 不受影响。
func TestGate_AgentSideRevoke_SandboxSideUntouched(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if _, err := reg.Revoke(testAgentRegistrationID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted {
		t.Errorf("revoked agent must not be accepted")
	}
	if decision.Agent.OK {
		t.Errorf("agent side must fail after revoke")
	}
	assertHasReason(t, decision.Agent.Reasons, bindingcheck.RejectionReasonAgentRegistrationInactive)
	if !decision.Sandbox.OK {
		t.Errorf("sandbox side must stay OK when only agent side fails, got %v", decision.Sandbox.Reasons)
	}
}

// 仅 Sandbox 侧 revoke：Sandbox 拒绝、Agent 不受影响。
func TestGate_SandboxSideRevoke_AgentSideUntouched(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if err := ledger.Revoke(testAllocationID); err != nil {
		t.Fatalf("ledger.Revoke: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted {
		t.Errorf("revoked sandbox must not be accepted")
	}
	if decision.Sandbox.OK {
		t.Errorf("sandbox side must fail after revoke")
	}
	assertHasReason(t, decision.Sandbox.Reasons, bindingcheck.RejectionReasonSandboxAllocationInactive)
	if !decision.Agent.OK {
		t.Errorf("agent side must stay OK when only sandbox side fails, got %v", decision.Agent.Reasons)
	}
}

// 仅 Sandbox 侧 expire：与 revoke 对称的失效路径。
func TestGate_SandboxSideExpire(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if err := ledger.Expire(testAllocationID); err != nil {
		t.Fatalf("ledger.Expire: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.Sandbox.OK {
		t.Errorf("expired sandbox allocation must fail, got %+v", decision)
	}
	assertHasReason(t, decision.Sandbox.Reasons, bindingcheck.RejectionReasonSandboxAllocationInactive)
	if !decision.Agent.OK {
		t.Errorf("agent side must stay OK, got %v", decision.Agent.Reasons)
	}
}

// Sandbox replace：generation bump 使旧 profile 的 generation 绑定失效。
func TestGate_SandboxReplace_GenerationMismatch(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if _, err := ledger.Replace(testAllocationID); err != nil {
		t.Fatalf("ledger.Replace: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.Sandbox.OK {
		t.Errorf("replaced sandbox allocation must fail, got %+v", decision)
	}
	assertHasReason(t, decision.Sandbox.Reasons, bindingcheck.RejectionReasonSandboxGenerationMismatch)
	if !decision.Agent.OK {
		t.Errorf("agent side must stay OK, got %v", decision.Agent.Reasons)
	}
}

// Agent replace：registration 进入 replaced 终态，旧 profile 不可接纳。
func TestGate_AgentReplace_RegistrationInactive(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if _, err := reg.Replace(testAgentRegistrationID); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.Agent.OK {
		t.Errorf("replaced agent registration must fail, got %+v", decision)
	}
	assertHasReason(t, decision.Agent.Reasons, bindingcheck.RejectionReasonAgentRegistrationInactive)
	if !decision.Sandbox.OK {
		t.Errorf("sandbox side must stay OK, got %v", decision.Sandbox.Reasons)
	}
}

// Agent snapshot supersede：active snapshot digest 变化使旧绑定快照失效。
func TestGate_AgentSnapshotSuperseded(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	next := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             digestOf("agent-snapshot", "v2"),
		RegistrationID:             testAgentRegistrationID,
		ProtocolVersion:            "acp/v1",
		ProviderName:               "fake-agent",
		ProviderVersion:            "0.1.1",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{testEvidenceDigest()},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if _, err := reg.AddSnapshot(next); err != nil {
		t.Fatalf("AddSnapshot v2: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.Agent.OK {
		t.Errorf("superseded snapshot binding must fail, got %+v", decision)
	}
	assertHasReason(t, decision.Agent.Reasons, bindingcheck.RejectionReasonAgentSnapshotMismatch)
	if !decision.Sandbox.OK {
		t.Errorf("sandbox side must stay OK, got %v", decision.Sandbox.Reasons)
	}
}

func TestNewGate_NilDependencies(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	checker, _ := bindingcheck.NewChecker(reg, ledger)
	store := NewAttemptProfileStore()

	if _, err := NewGate(nil, checker, reg); err == nil {
		t.Errorf("nil store must fail")
	}
	if _, err := NewGate(store, nil, reg); !errors.Is(err, ErrNilDependency) {
		t.Errorf("nil checker: expected ErrNilDependency, got %v", err)
	}
	if _, err := NewGate(store, checker, nil); !errors.Is(err, ErrNilDependency) {
		t.Errorf("nil registry: expected ErrNilDependency, got %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func assertHasReason(t *testing.T, reasons []bindingcheck.RejectionReason, want bindingcheck.RejectionReason) {
	t.Helper()
	for _, r := range reasons {
		if r == want {
			return
		}
	}
	t.Errorf("expected reason %q in %v", want, reasons)
}
