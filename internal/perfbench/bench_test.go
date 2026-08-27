package perfbench

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/attemptgate"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/effectsink"
	"github.com/chiga0/marshal-harness/internal/jitgate"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

// ── 共享夹具 ────────────────────────────────────────────────────────────────
//
// 全部夹具为内存内构造（与被测包 *_test.go 同一风格）：无 I/O、无进程、
// 时钟一律注入定值。每个 harness 构造函数返回 once 闭包：第 i 次单调用。
// 基准与门禁测试共用同一批闭包，保证测量的是同一条路径。

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const (
	perfAgentRegID   = "registration:perfbench-agent"
	perfSandboxRegID = "registration:perfbench-sandbox"
	perfAllocID      = "alloc-perfbench"
	perfAttemptID    = "attempt-perfbench"
)

// poolSize 是会改变内部状态的调用（Ingress.Admit / ExecuteIfAdmitted）
// 使用的预构造输入池大小：必须大于门禁测试采样数 N=200，使门禁测试
// 测量的全部是"首次接纳"路径（绕过幂等重放稳态）；基准迭代超过池长后
// 回绕进入重放/AlreadyExecuted 稳态，该稳态仍完整执行全部 recheck
// 判定，仅跳过账本写入。
const poolSize = 512

func perfRegistry(tb testing.TB, evidenceDigests []string) *agentregistry.Registry {
	tb.Helper()
	reg := agentregistry.NewRegistry()
	registration := agentregistry.AgentRegistration{
		RegistrationID:       perfAgentRegID,
		AuthorityNamespaceID: "authority:perfbench",
		SecurityDomainID:     "domain:execution:perfbench",
		Principal:            "principal:perfbench",
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         "fake-agent",
		ProviderVersion:      "0.1.0",
		ProtocolVersion:      "acp/v1",
		Scope:                "scope:perfbench",
		IdempotencyKey:       "idem-perfbench-1",
		RequestDigest:        digestOf("registration-request", "perfbench"),
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            time.Unix(1000, 0).UTC(),
		UpdatedAt:            time.Unix(1000, 0).UTC(),
	}
	if _, err := reg.Register(registration); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             digestOf("agent-snapshot", "perfbench-v1"),
		RegistrationID:             perfAgentRegID,
		ProtocolVersion:            "acp/v1",
		ProviderName:               "fake-agent",
		ProviderVersion:            "0.1.0",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: evidenceDigests,
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if _, err := reg.AddSnapshot(snap); err != nil {
		tb.Fatalf("AddSnapshot: %v", err)
	}
	return reg
}

func perfLedger(tb testing.TB) *bindingcheck.SandboxLedger {
	tb.Helper()
	ledger := bindingcheck.NewSandboxLedger()
	if _, err := ledger.PutAllocation(perfAllocID, perfSandboxRegID, 1); err != nil {
		tb.Fatalf("PutAllocation: %v", err)
	}
	return ledger
}

func perfProfile(tb testing.TB) runtimeprofile.WorkerRuntimeProfile {
	tb.Helper()
	agent, err := runtimeprofile.NewAgentBinding(perfAgentRegID, digestOf("agent-snapshot", "perfbench-v1"), "fake-agent", "0.1.0", "acp/v1")
	if err != nil {
		tb.Fatalf("NewAgentBinding: %v", err)
	}
	sandbox, err := runtimeprofile.NewSandboxBinding(perfSandboxRegID, perfAllocID, 1)
	if err != nil {
		tb.Fatalf("NewSandboxBinding: %v", err)
	}
	profile, err := runtimeprofile.NewProfile(agent, sandbox, digestOf("compat", "perfbench"))
	if err != nil {
		tb.Fatalf("NewProfile: %v", err)
	}
	return profile
}

// newRecheckOnce 构造 bindingcheck.Checker.Recheck 单调用闭包。
func newRecheckOnce(tb testing.TB) func(i int) {
	tb.Helper()
	reg := perfRegistry(tb, nil)
	checker, err := bindingcheck.NewChecker(reg, perfLedger(tb))
	if err != nil {
		tb.Fatalf("NewChecker: %v", err)
	}
	profile := perfProfile(tb)
	return func(int) {
		result, err := checker.Recheck(profile)
		if err != nil {
			tb.Fatalf("Recheck: %v", err)
		}
		if !result.Accepted() {
			tb.Fatalf("Recheck must accept happy-path fixture, got %+v", result)
		}
	}
}

// newAdmissionOnce 构造 attemptgate.Gate.AdmitAttemptResult 单调用闭包。
func newAdmissionOnce(tb testing.TB) func(i int) {
	tb.Helper()
	evidenceDigest := digestOf("agent-evidence", "perfbench")
	reg := perfRegistry(tb, []string{evidenceDigest})
	checker, err := bindingcheck.NewChecker(reg, perfLedger(tb))
	if err != nil {
		tb.Fatalf("NewChecker: %v", err)
	}
	store := attemptgate.NewAttemptProfileStore()
	gate, err := attemptgate.NewGate(store, checker, reg)
	if err != nil {
		tb.Fatalf("NewGate: %v", err)
	}
	if err := store.Bind(perfAttemptID, perfProfile(tb)); err != nil {
		tb.Fatalf("Bind: %v", err)
	}
	return func(int) {
		decision, err := gate.AdmitAttemptResult(perfAttemptID, evidenceDigest)
		if err != nil {
			tb.Fatalf("AdmitAttemptResult: %v", err)
		}
		if !decision.Accepted {
			tb.Fatalf("AdmitAttemptResult must accept happy-path fixture, got %+v", decision)
		}
	}
}

// newJitOnce 构造 jitgate.VerifyBeforeProvision 单调用闭包。
func newJitOnce(tb testing.TB) func(i int) {
	tb.Helper()
	token, err := jitgate.NewAdmissionToken(
		"decision-perfbench",
		perfAgentRegID,
		digestOf("policy-snapshot", "perfbench-v1"),
		digestOf("policy", "perfbench-v1"),
		6000,
	)
	if err != nil {
		tb.Fatalf("NewAdmissionToken: %v", err)
	}
	view := jitgate.LedgerView{
		RegistrationActive:   true,
		ActiveSnapshotDigest: token.SnapshotDigest,
		CurrentPolicyDigest:  token.PolicyDigest,
		PolicyActive:         true,
	}
	now := time.Unix(5000, 0).UTC()
	return func(int) {
		verdict, err := jitgate.VerifyBeforeProvision(token, view, now)
		if err != nil {
			tb.Fatalf("VerifyBeforeProvision: %v", err)
		}
		if verdict == nil || !verdict.OK {
			tb.Fatalf("VerifyBeforeProvision must accept happy-path fixture, got %+v", verdict)
		}
	}
}

// newIngressOnce 构造 resultingress.Ingress.Admit（cold worker-result 路径）
// 单调用闭包。DRC 池按幂等键区分：i < poolSize 时均为首次接纳。
func newIngressOnce(tb testing.TB) func(i int) {
	tb.Helper()
	binding := resultingress.LedgerBinding{
		LeaseID:        "lease-perfbench",
		Generation:     1,
		FencingToken:   "fence-perfbench",
		AttemptID:      perfAttemptID,
		AllocationID:   perfAllocID,
		Expiry:         time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		Revoked:        false,
		RegistrationID: perfAgentRegID,
		SnapshotDigest: digestOf("agent-snapshot", "perfbench-v1"),
		EvidenceDigest: digestOf("agent-evidence", "perfbench"),
	}
	ing, err := resultingress.NewIngress(binding)
	if err != nil {
		tb.Fatalf("NewIngress: %v", err)
	}
	resultDigest := digestOf("worker-result", "perfbench")
	envelope := resultingress.ResultEnvelope{
		Kind:         resultingress.KindWorkerResult,
		ResultDigest: resultDigest,
		Sequence:     1,
	}
	drcs := make([]resultingress.DRC, poolSize)
	for i := range drcs {
		drcs[i] = resultingress.DRC{
			AuthorityNamespaceID: "ns-perfbench",
			TaskID:               "task-perfbench",
			RunID:                "run-perfbench",
			AttemptID:            perfAttemptID,
			AllocationID:         perfAllocID,
			LeaseID:              "lease-perfbench",
			Generation:           1,
			FencingToken:         "fence-perfbench",
			CommandID:            "cmd-perfbench",
			IdempotencyKey:       fmt.Sprintf("idem-perfbench-%d", i),
			RequestDigest:        resultDigest,
			Nonce:                "nonce-perfbench",
			Expiry:               time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
			Operation:            resultingress.OpResult,
			RegistrationID:       perfAgentRegID,
			SnapshotDigest:       digestOf("agent-snapshot", "perfbench-v1"),
			EvidenceDigest:       digestOf("agent-evidence", "perfbench"),
		}
	}
	ctx := context.Background()
	return func(i int) {
		fact, err := ing.Admit(ctx, drcs[i%poolSize], envelope)
		if err != nil {
			tb.Fatalf("Admit: %v", err)
		}
		if fact.FactDigest == "" {
			tb.Fatal("Admit must return a fact digest")
		}
	}
}

// newEffectOnce 构造 effectsink.ExecuteIfAdmitted 单调用闭包。Intent 池
// 按幂等键区分（IntentDigest 随之改变）：i < poolSize 时均为首次执行。
func newEffectOnce(tb testing.TB) func(i int) {
	tb.Helper()
	ledger := effectsink.NewEffectLedger()
	intents := make([]effectsink.EffectIntent, poolSize)
	for i := range intents {
		intent, err := effectsink.NewEffectIntent(
			fmt.Sprintf("intent-perfbench-%d", i),
			effectsink.SinkKindSCMMutation,
			"repo:marshal-harness/branch:perfbench",
			fmt.Sprintf("idem-effect-perfbench-%d", i),
			7,
			"fence-token-perfbench",
			digestOf("publication-authorization", "perfbench-v1"),
			digestOf("target-state", "perfbench-v1"),
		)
		if err != nil {
			tb.Fatalf("NewEffectIntent: %v", err)
		}
		intents[i] = intent
	}
	view := effectsink.CurrentView{
		CurrentGeneration:          intents[0].Generation,
		CurrentFencingToken:        intents[0].FencingToken,
		AuthorizationRevoked:       false,
		CurrentAuthorizationDigest: intents[0].AuthorizationDigest,
		CurrentTargetDigest:        intents[0].TargetDigest,
	}
	return func(i int) {
		verdict, err := effectsink.ExecuteIfAdmitted(ledger, intents[i%poolSize], view)
		if err != nil {
			tb.Fatalf("ExecuteIfAdmitted: %v", err)
		}
		if verdict == nil || !verdict.OK {
			tb.Fatalf("ExecuteIfAdmitted must admit happy-path fixture, got %+v", verdict)
		}
	}
}

// ── 基准：单调用延迟，夹具在计时前构造 ─────────────────────────────────────

func BenchmarkBindingCheckRecheck(b *testing.B) {
	once := newRecheckOnce(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		once(i)
	}
}

func BenchmarkAttemptGateAdmit(b *testing.B) {
	once := newAdmissionOnce(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		once(i)
	}
}

func BenchmarkJitGateVerify(b *testing.B) {
	once := newJitOnce(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		once(i)
	}
}

func BenchmarkResultIngressAdmit(b *testing.B) {
	once := newIngressOnce(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		once(i)
	}
}

func BenchmarkEffectSinkExecute(b *testing.B) {
	once := newEffectOnce(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		once(i)
	}
}

// ── 门禁测试：N=200 采样 → EstimateP99 → CheckThresholds ──────────────────

// TestBaselineConformance 以普通 Test 复用五条基准的单调用闭包，各自采样
// N=200 次单调用延迟，计算 p99 并对照 DefaultThresholds 断言。刻意不经
// test(-1)bench 驱动，保证 CI 执行面确定。
func TestBaselineConformance(t *testing.T) {
	const n = 200
	suites := []struct {
		name string
		once func(i int)
	}{
		{MetricRecheckP99, newRecheckOnce(t)},
		{MetricAdmissionP99, newAdmissionOnce(t)},
		{MetricJitP99, newJitOnce(t)},
		{MetricIngressP99, newIngressOnce(t)},
		{MetricEffectP99, newEffectOnce(t)},
	}

	observations := make([]Observation, 0, len(suites))
	for _, s := range suites {
		samples := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			start := time.Now()
			s.once(i)
			samples = append(samples, time.Since(start))
		}
		p99, ok := EstimateP99(samples)
		if !ok {
			t.Fatalf("EstimateP99(%s): empty samples", s.name)
		}
		t.Logf("%s: p99 = %d µs over %d samples", s.name, p99, n)
		observations = append(observations, Observation{Name: s.name, P99Micros: p99})
	}

	violations := CheckThresholds(DefaultThresholds(), observations)
	for _, v := range violations {
		t.Errorf("SLO violation: %s p99 = %d µs, threshold %d µs", v.Name, v.Got, v.Want)
	}
}
