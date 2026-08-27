package attemptgate

import (
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// 证据边界：Sandbox conformance evidence（由 independent verifier 经
// internal/sandbox 签发）不得冒充 Agent evidence。Sandbox 侧签发的 digest
// 结构性与 Agent EvidenceRecord 分属不同 Port 的类型系统，即使出示方把它
// 放进结果接纳路径，也只能落到同一 fail-closed 集合归属检查。
func TestBoundary_SandboxEvidenceCannotImpersonateAgentEvidence(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	verdict := sandbox.ConformanceVerdict{
		Passed:     true,
		ReasonCode: "pass",
		Trace:      []sandbox.BusinessEvent{{Kind: "alloc", Outcome: "pass", Detail: "fixture"}},
	}
	_, sandboxEvidenceDigest, err := sandbox.IssueConformanceEvidence("verifier-sandbox", "fixture-x", verdict)
	if err != nil {
		t.Fatalf("IssueConformanceEvidence: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, sandboxEvidenceDigest)
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.EvidenceOK {
		t.Errorf("sandbox-issued evidence must not be accepted on the agent side, got %+v", decision)
	}
	if decision.EvidenceReason != EvidenceRejectReasonNotBound {
		t.Errorf("expected evidence-not-bound, got %q", decision.EvidenceReason)
	}
	// binding 侧未受影响：拒绝归因只在证据侧
	if !decision.Agent.OK || !decision.Sandbox.OK {
		t.Errorf("binding sides must be untouched by evidence impersonation, got %+v", decision)
	}
}

// 反向边界：Agent evidence digest 也不在其它 Agent registration 的封闭
// 集合内——跨 registration 借用证据同样 fail closed。
func TestBoundary_CrossRegistrationEvidenceBorrowRejected(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// 第二个 Agent registration，携带自己的 active snapshot 与证据 E2；
	// E2 对注册者有效，但不属于本 Attempt 所绑定 registration 的封闭集合。
	otherRegID := "registration:cccc3333"
	otherEvidence := digestOf("other-agent-evidence")
	other := agentregistry.AgentRegistration{
		RegistrationID:       otherRegID,
		AuthorityNamespaceID: "authority:test",
		SecurityDomainID:     "domain:execution:test",
		Principal:            "principal:agent-2",
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         "fake-agent",
		ProviderVersion:      "0.2.0",
		ProtocolVersion:      "acp/v1",
		Scope:                "scope:test",
		IdempotencyKey:       "idem-2",
		RequestDigest:        digestOf("other-registration-request"),
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            time.Unix(1000, 0).UTC(),
		UpdatedAt:            time.Unix(1000, 0).UTC(),
	}
	if _, err := reg.Register(other); err != nil {
		t.Fatalf("Register other: %v", err)
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             digestOf("other-agent-snapshot"),
		RegistrationID:             otherRegID,
		ProtocolVersion:            "acp/v1",
		ProviderName:               "fake-agent",
		ProviderVersion:            "0.2.0",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{otherEvidence},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if _, err := reg.AddSnapshot(snap); err != nil {
		t.Fatalf("AddSnapshot other: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, otherEvidence)
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.EvidenceOK || decision.Accepted {
		t.Errorf("borrowed evidence from another registration must fail, got %+v", decision)
	}
	if decision.EvidenceReason != EvidenceRejectReasonNotBound {
		t.Errorf("expected evidence-not-bound, got %q", decision.EvidenceReason)
	}
	if !decision.Agent.OK || !decision.Sandbox.OK {
		t.Errorf("bindings must stay OK; only evidence side rejects, got %+v", decision)
	}
}

// 类型级边界：Agent EvidenceRecord 的 ProviderType 封闭于 agent，任何把
// "sandbox" 写进 Agent 证据类型的尝试在 Validate 即 fail closed。
func TestBoundary_AgentEvidenceRecordRejectsSandboxProviderType(t *testing.T) {
	record := agentregistry.EvidenceRecord{
		EvidenceDigest: testEvidenceDigest(),
		EvidenceKind:   agentregistry.EvidenceKindConformance,
		ProviderType:   agentregistry.ProviderType("sandbox"),
		RegistrationID: testAgentRegistrationID,
	}
	if err := record.Validate(); err == nil {
		t.Errorf("EvidenceRecord with ProviderType=sandbox must fail closed")
	}
}

// 跨 Port binding 混淆：Agent 侧 registration id 出现在 SandboxBinding
// 的 allocation 位置（或反之）时，current-ledger recheck 必须 fail
// closed，且另一侧不受影响。
func TestBoundary_CrossPortBindingConfusionFailsClosed(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)

	// AgentBinding 携带 sandbox provider 的 registration id：Agent ledger 查无此人。
	confusedAgent, err := runtimeprofile.NewAgentBinding(testSandboxRegistrationID, testSnapshotDigest(), "fake-agent", "0.1.0", "acp/v1")
	if err != nil {
		t.Fatalf("NewAgentBinding: %v", err)
	}
	sandboxBinding, err := runtimeprofile.NewSandboxBinding(testSandboxRegistrationID, testAllocationID, 1)
	if err != nil {
		t.Fatalf("NewSandboxBinding: %v", err)
	}
	confused, err := runtimeprofile.NewProfile(confusedAgent, sandboxBinding, testCompatDigest())
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	if err := store.Bind(testAttemptID, confused); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	decision, err := gate.AdmitAttemptResult(testAttemptID, testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.Agent.OK {
		t.Errorf("cross-port agent binding must fail, got %+v", decision)
	}
	assertHasReason(t, decision.Agent.Reasons, bindingcheck.RejectionReasonAgentUnknownRegistration)
	if !decision.Sandbox.OK {
		t.Errorf("sandbox side must stay OK, got %v", decision.Sandbox.Reasons)
	}

	// 反向：SandboxBinding 的 allocation 位置填 agent registration id。
	confusedSandbox, err := runtimeprofile.NewSandboxBinding(testSandboxRegistrationID, testAgentRegistrationID, 1)
	if err != nil {
		t.Fatalf("NewSandboxBinding reverse: %v", err)
	}
	validAgent, err := runtimeprofile.NewAgentBinding(testAgentRegistrationID, testSnapshotDigest(), "fake-agent", "0.1.0", "acp/v1")
	if err != nil {
		t.Fatalf("NewAgentBinding valid: %v", err)
	}
	reverse, err := runtimeprofile.NewProfile(validAgent, confusedSandbox, testCompatDigest())
	if err != nil {
		t.Fatalf("NewProfile reverse: %v", err)
	}
	if err := store.Bind("attempt-reverse", reverse); err != nil {
		t.Fatalf("Bind reverse: %v", err)
	}

	reverseDecision, err := gate.AdmitAttemptResult("attempt-reverse", testEvidenceDigest())
	if err != nil {
		t.Fatalf("AdmitAttemptResult reverse: %v", err)
	}
	if reverseDecision.Accepted || reverseDecision.Sandbox.OK {
		t.Errorf("cross-port sandbox binding must fail, got %+v", reverseDecision)
	}
	assertHasReason(t, reverseDecision.Sandbox.Reasons, bindingcheck.RejectionReasonSandboxUnknownAllocation)
	if !reverseDecision.Agent.OK {
		t.Errorf("agent side must stay OK, got %v", reverseDecision.Agent.Reasons)
	}
}

// 伪造 evidence digest：形态合法但从未签发的 digest 在双侧 binding 全部
// 有效时仍被证据侧拒绝。
func TestBoundary_ForgedEvidenceDigestRejected(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, store := mustGate(t, reg, ledger)
	if err := store.Bind(testAttemptID, mustProfile(t)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	forged := digestOf("forged-evidence")
	decision, err := gate.AdmitAttemptResult(testAttemptID, forged)
	if err != nil {
		t.Fatalf("AdmitAttemptResult: %v", err)
	}
	if decision.Accepted || decision.EvidenceOK {
		t.Errorf("forged evidence must fail, got %+v", decision)
	}
	if decision.EvidenceReason != EvidenceRejectReasonNotBound {
		t.Errorf("expected evidence-not-bound, got %q", decision.EvidenceReason)
	}
	if !decision.Agent.OK || !decision.Sandbox.OK {
		t.Errorf("bindings must stay OK; only evidence side rejects, got %+v", decision)
	}
}

// 形态错误的证据 digest 在门禁入口即结构性拒绝。
func TestBoundary_MalformedEvidenceDigestRejected(t *testing.T) {
	reg := mustRegistry(t, []string{testEvidenceDigest()})
	ledger := mustLedger(t)
	gate, _ := mustGate(t, reg, ledger)

	_, err := gate.AdmitAttemptResult(testAttemptID, "not-a-digest")
	if !errors.Is(err, ErrMalformedEvidenceDigest) {
		t.Errorf("expected ErrMalformedEvidenceDigest, got %v", err)
	}
}
