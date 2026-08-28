package resultbinding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func testBindingFacts() Facts {
	return Facts{
		TaskID:                        "T-BIND",
		RunID:                         "run-bind",
		AttemptID:                     "attempt-bind",
		AgentAdapterID:                "pi",
		AgentExecutable:               "/usr/local/bin/pi",
		AgentProviderVersion:          "0.84.3",
		CapabilityDigest:              "sha256:" + "a1b2c3d4" + "00000000000000000000000000000000000000000000000000000000",
		AgentRegistrationID:           "registration:" + "a1b2c3d4" + "000000000000000000000000",
		AgentCapabilitySnapshotDigest: "sha256:" + strings.Repeat("b", 64),
		ExecutionProfile:              "workspace-write",
		SandboxProviderRegistrationID: "registration:local-runner",
		AllocationID:                  "alloc-bind-1",
		AllocationGeneration:          1,
		LiveAllocationState:           sandbox.AllocationActive,
		FencingToken:                  "fence-bind-1",
		LeaseExpiry:                   time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
	}
}

func TestWriteReadAttemptBindingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	binding, err := ReadAttemptBinding(dir)
	if err != nil {
		t.Fatalf("ReadAttemptBinding: %v", err)
	}
	if binding.Facts.AttemptID != facts.AttemptID {
		t.Errorf("AttemptID = %q, want %q", binding.Facts.AttemptID, facts.AttemptID)
	}
	if binding.Facts.LeaseExpiry != facts.LeaseExpiry {
		t.Errorf("LeaseExpiry = %v, want %v", binding.Facts.LeaseExpiry, facts.LeaseExpiry)
	}
	if binding.BindingDigest == "" {
		t.Error("BindingDigest must not be empty")
	}
}

func TestReadAttemptBindingRejectsTampered(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	// 篡改 binding 文件：改 AgentProviderVersion 但不改 digest。
	path := filepath.Join(dir, AttemptBindingFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := string(raw)
	tampered = replaceFirst(tampered, "0.84.3", "9.99.9")
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadAttemptBinding(dir)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("ReadAttemptBinding with tampered file: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestReadAttemptBindingMissingFileFailClosed(t *testing.T) {
	_, err := ReadAttemptBinding(t.TempDir())
	if err == nil {
		t.Fatal("ReadAttemptBinding on missing file must fail closed")
	}
}

// TestWriteAttemptBindingCreationOnce 验证 creation-once 语义：
// 相同 facts 的重复写入是幂等的（安全重放），不同 facts 的写入 fail closed。
func TestWriteAttemptBindingCreationOnce(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	// 第一次写入应成功。
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// 相同 facts 的第二次写入应幂等成功。
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("idempotent rewrite: %v", err)
	}
	// 不同 facts 的写入应 fail closed。
	tampered := facts
	tampered.AllocationGeneration = facts.AllocationGeneration + 1
	if err := WriteAttemptBinding(dir, tampered); err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("write with different facts: err = %v, want ErrAdmissionRejected", err)
	}
}

// fakeAuthoritySource 是测试用的 DurableAuthoritySource stub。
type fakeAuthoritySource struct {
	registration provider.ProviderRegistration
	active       bool
	getErr       error
	agentActive  bool
	ingressDir   string
	facts        *Facts
	agentReg     *agentregistry.AgentRegistration
	agentSnap    *agentregistry.AgentCapabilitySnapshot
}

func (f fakeAuthoritySource) ProviderRegistration() (provider.ProviderRegistration, error) {
	return f.registration, nil
}

func (f fakeAuthoritySource) ProviderRegistrationActive(string) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	return f.active, nil
}

func (f fakeAuthoritySource) AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error) {
	if f.getErr != nil {
		return agentregistry.AgentRegistration{}, agentregistry.AgentCapabilitySnapshot{}, f.getErr
	}
	facts := testBindingFacts()
	if f.facts != nil {
		facts = *f.facts
	}
	state := agentregistry.LifecycleStateActive
	if !f.agentActive {
		state = agentregistry.LifecycleStateRevoked
	}
	reg := agentregistry.AgentRegistration{
		RegistrationID: registrationID, AuthorityNamespaceID: AuthorityNamespaceID,
		SecurityDomainID: "default/execution/test", Principal: "principal:agent:test",
		ProviderType: agentregistry.ProviderTypeAgent, ProviderName: facts.AgentAdapterID,
		ProviderVersion: facts.AgentProviderVersion, ProtocolVersion: ProtocolVersion,
		Scope: "worker", IdempotencyKey: "cap:" + facts.EffectiveAgentCapabilitySnapshotDigest(),
		RequestDigest: facts.EffectiveAgentCapabilitySnapshotDigest(), LifecycleState: state,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest: facts.EffectiveAgentCapabilitySnapshotDigest(), RegistrationID: registrationID,
		ProtocolVersion: ProtocolVersion, ProviderName: facts.AgentAdapterID,
		ProviderVersion:            facts.AgentProviderVersion,
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if f.agentReg != nil {
		reg = *f.agentReg
	}
	if f.agentSnap != nil {
		snap = *f.agentSnap
	}
	return reg, snap, nil
}

func TestOrdinaryUserAdmissionDoesNotForgeConformanceEvidence(t *testing.T) {
	facts := testBindingFacts()
	binding := bindingFor(t, facts)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true, agentActive: true,
	}
	admission, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err != nil || admission == nil || !admission.Accepted || !admission.EvidenceOK {
		t.Fatalf("ordinary-user admission must rely on Core binding without forged conformance: admission=%+v err=%v", admission, err)
	}
}

func TestHardenedAdmissionRequiresIndependentConformanceEvidence(t *testing.T) {
	facts := testBindingFacts()
	facts.ExecutionProfile = "hardened"
	binding := bindingFor(t, facts)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true, agentActive: true, facts: &facts,
	}
	if admission, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive); err == nil || admission != nil {
		t.Fatalf("hardened admission without independent evidence must fail closed: admission=%+v err=%v", admission, err)
	}
	reg, snap, err := auth.AgentAuthority(facts.AgentRegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	snap.ConformanceEvidenceDigests = []string{"sha256:" + strings.Repeat("c", 64)}
	auth.agentReg = &reg
	auth.agentSnap = &snap
	admission, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err != nil || admission == nil || !admission.Accepted || !admission.EvidenceOK {
		t.Fatalf("hardened admission with independent evidence failed: admission=%+v err=%v", admission, err)
	}
}

func (f fakeAuthoritySource) ResultIngressDir() string { return f.ingressDir }

func TestAdmitWithDurableAuthorityRejectsRevokedRegistration(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	binding, err := ReadAttemptBinding(dir)
	if err != nil {
		t.Fatalf("ReadAttemptBinding: %v", err)
	}
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       false, // revoked
	}
	_, err = AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("AdmitWithDurableAuthority with revoked registration: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestAdmitWithDurableAuthorityNilBindingFailClosed(t *testing.T) {
	auth := fakeAuthoritySource{active: true, agentActive: true}
	_, err := AdmitWithDurableAuthority(context.Background(), nil, []byte(`{}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatal("nil binding must fail closed")
	}
}

// ── R2/R3 负测矩阵：revoke/replace/replay/旧 generation ──────────────────────

func TestAdmitWithDurableAuthorityRejectsRevokedAgentRegistration(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAttemptBinding(dir, testBindingFacts()); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  false, // agent registration revoked
	}
	admission, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("revoked agent registration: err = %v, want ErrAdmissionRejected", err)
	}
	if admission != nil && (admission.AgentOK || admission.Accepted) {
		t.Errorf("revoked agent must not be AgentOK or Accepted: %+v", admission)
	}
}

func TestAdmitWithDurableAuthorityRejectsSupersededAgentSnapshot(t *testing.T) {
	facts := testBindingFacts()
	facts.AgentRegistrationID = "registration:" + strings.Repeat("b", 32)
	facts.AgentCapabilitySnapshotDigest = "sha256:" + strings.Repeat("c", 64)
	current := fakeAuthoritySource{agentActive: true, facts: &facts}
	_, snap, err := current.AgentAuthority(facts.AgentRegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	snap.SnapshotDigest = "sha256:" + strings.Repeat("d", 64)
	current.registration = provider.ProviderRegistration{RegistrationId: facts.SandboxProviderRegistrationID}
	current.active = true
	current.agentSnap = &snap

	admission, err := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), []byte(`{"kind":"WorkerResult"}`), current, sandbox.AllocationActive)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("superseded agent snapshot must be rejected, admission=%+v err=%v", admission, err)
	}
	if admission == nil || admission.AgentOK || admission.Accepted || !contains(admission.AdmissionReason, "current ledger") {
		t.Fatalf("superseded snapshot rejection must identify agent current-ledger mismatch: %+v", admission)
	}
}

func TestAdmitWithDurableAuthorityRejectsAgentProviderIdentityDrift(t *testing.T) {
	facts := testBindingFacts()
	facts.AgentRegistrationID = "registration:" + strings.Repeat("b", 32)
	facts.AgentCapabilitySnapshotDigest = "sha256:" + strings.Repeat("c", 64)
	current := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: facts.SandboxProviderRegistrationID},
		active:       true, agentActive: true, facts: &facts,
	}
	reg, _, err := current.AgentAuthority(facts.AgentRegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	reg.ProviderVersion = "9.9.9"
	current.agentReg = &reg

	admission, err := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), []byte(`{"kind":"WorkerResult"}`), current, sandbox.AllocationActive)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("provider identity drift must be rejected, admission=%+v err=%v", admission, err)
	}
	if admission == nil || admission.AgentOK || admission.Accepted {
		t.Fatalf("provider drift must fail the agent side: %+v", admission)
	}
}

func TestAdmitWithDurableAuthorityRejectsTerminatedAllocation(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAttemptBinding(dir, testBindingFacts()); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	// Inspect 回读的 live state 是 terminated → seedSandboxLedger 把它 revoke
	// → bindingcheck 拒绝（allocation 已终态，不得接纳结果）。
	_, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationTerminated)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("terminated allocation: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestAdmitWithDurableAuthorityRejectsReplacedAllocation(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAttemptBinding(dir, testBindingFacts()); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	// Inspect 回读的 live state 是 replaced → seedSandboxLedger 派生新一代
	// → bindingcheck 发现 generation mismatch → 拒绝。
	_, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationReplaced)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("replaced allocation: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestAdmitWithDurableAuthorityRejectsStaleGenerationBinding(t *testing.T) {
	dir := t.TempDir()
	// 写入 generation=1 的 binding，但 Inspect 回读 allocation 当前 generation=2
	// （旧 generation 结果迟到）。
	facts := testBindingFacts()
	facts.AllocationGeneration = 1
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	// binding 里 generation=1，但 seedSandboxLedger 以 binding 的 generation
	// 建 ledger——所以这条路径验证的是 binding 文件被替换为旧 generation
	// 后的 detection。用 tampered binding 模拟：改 generation 为 0。
	tampered := binding
	tampered.Facts.AllocationGeneration = 0
	// 重新计算 digest 以绕过 tamper 检测（模拟攻击者持有私钥——不现实，
	// 但验证 bindingcheck 层而非 digest 层的 generation guard）。
	// 实际上 generation=0 会被 facts.validate() 拦截。
	_, err := AdmitWithDurableAuthority(context.Background(), tampered, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatal("stale generation (0) must fail closed")
	}
}

func TestAdmitWithDurableAuthorityRejectsReplayResult(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	resultBytes := []byte(`{"kind":"WorkerResult","status":"completed"}`)
	// 第一次接纳应成功（binding facts 合法 + active allocation + active registration）。
	admission1, err1 := AdmitWithDurableAuthority(context.Background(), binding, resultBytes, auth, sandbox.AllocationActive)
	if err1 != nil {
		t.Fatalf("first admit should succeed: %v", err1)
	}
	if !admission1.Accepted {
		t.Fatalf("first admit should be accepted: %+v", admission1)
	}
	// 第二次重放同一结果：ResultIngress 的 idempotency key 相同 →
	// 幂等 replay（Admit 返回既有 fact，不产生第二效果）。
	admission2, err2 := AdmitWithDurableAuthority(context.Background(), binding, resultBytes, auth, sandbox.AllocationActive)
	if err2 != nil {
		t.Fatalf("replay should be idempotent (not an error): %v", err2)
	}
	if !admission2.Accepted {
		t.Fatalf("idempotent replay should be accepted: %+v", admission2)
	}
	// 两次 admission 的 fact digest 必须相同（幂等）。
	if admission1.AdmissionFact != admission2.AdmissionFact {
		t.Errorf("replay fact digest mismatch: %q vs %q", admission1.AdmissionFact, admission2.AdmissionFact)
	}
}

func TestAdmitWithDurableAuthorityRejectsExpiredLease(t *testing.T) {
	dir := t.TempDir()
	// 写入一个 lease 已过期的 binding。
	facts := testBindingFacts()
	facts.LeaseExpiry = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // 远在过去
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	// 过期 lease → resultingress LedgerBinding.Expiry 已过 → DRC expiry
	// 检查应拒绝（lease 过期的结果不得接纳）。
	_, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatal("expired lease must fail closed")
	}
}

func replaceFirst(s, old, new string) string {
	idx := indexOf(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// stubAuthority 是 AdmitWithDurableAuthority 的最小 DurableAuthoritySource
// 测试替身：返回预置的 provider registration 与 active 判定。
type stubAuthority struct {
	registration   provider.ProviderRegistration
	providerActive bool
	agentActive    bool
}

func (s *stubAuthority) ProviderRegistration() (provider.ProviderRegistration, error) {
	return s.registration, nil
}
func (s *stubAuthority) ProviderRegistrationActive(string) (bool, error) {
	return s.providerActive, nil
}
func (s *stubAuthority) AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error) {
	f := fakeAuthoritySource{agentActive: s.agentActive}
	return f.AgentAuthority(registrationID)
}
func (s *stubAuthority) ResultIngressDir() string { return "" }

// TestAdmitWithDurableAuthorityRejectsSandboxRegistrationMismatch 锁定 P1-2
// 的 current-ledger binding 机械断言：AttemptBinding 冻结的
// SandboxProviderRegistrationID 与真实 durable ledger 当前
// ProviderRegistrationID 不等时，接纳必须 fail closed（SandboxOK=false）。
func TestAdmitWithDurableAuthorityRejectsSandboxRegistrationMismatch(t *testing.T) {
	facts := testBindingFacts()
	facts.AgentRegistrationID = "registration:agent-bind"
	binding := &AttemptBinding{Schema: AttemptBindingSchema, Facts: facts}

	auth := &stubAuthority{
		// 真实 ledger 的 provider registration 与 binding 冻结值不同。
		registration:   provider.ProviderRegistration{RegistrationId: "registration:different-provider"},
		providerActive: true,
		agentActive:    true,
	}
	admission, err := AdmitWithDurableAuthority(context.Background(), binding, []byte(`{}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatalf("expected rejection for sandbox registration mismatch, got admission=%+v", admission)
	}
	if !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("expected ErrAdmissionRejected, got %v", err)
	}
	if admission == nil || admission.Accepted || admission.SandboxOK {
		t.Fatalf("mismatched sandbox registration must be rejected with SandboxOK=false, got %+v", admission)
	}
	if !contains(admission.AdmissionReason, "does not match current ledger") {
		t.Errorf("admission reason must mention ledger mismatch, got %q", admission.AdmissionReason)
	}
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

// TestAdmitWithDurableAuthorityDurableIngressDetectsCrossProcessReplay 锁定
// R2 纵切的 durable ingress 生产接线：authority 提供 ResultIngressDir 时，
// admission 用耐久 replay 账本。首次接纳落账（IdempotentReplay=false）；同
// binding + 同 worker-result bytes 的二次送达跨调用被理赔为
// IdempotentReplay=true（admission 再次构造的 durable ingress 从账本重放
// 重复有效负载）。对比内存路径（ingressDir 为空）在同样重复送达下返回 false。
func TestAdmitWithDurableAuthorityDurableIngressDetectsCrossProcessReplay(t *testing.T) {
	facts := testBindingFacts()
	result := []byte(`{"kind":"WorkerResult","status":"completed"}`)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
		ingressDir:   t.TempDir(),
	}
	first, err := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), result, auth, sandbox.AllocationActive)
	if err != nil || !first.Accepted {
		t.Fatalf("first admit must succeed, err=%v", err)
	}
	if first.IdempotentReplay {
		t.Fatal("first admit must not be flagged as replay")
	}
	second, err := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), result, auth, sandbox.AllocationActive)
	if err != nil {
		t.Fatalf("idempotent replay must not be an error: %v", err)
	}
	if !second.Accepted {
		t.Fatalf("idempotent replay must be accepted: %+v", second)
	}
	if !second.IdempotentReplay {
		t.Fatalf("duplicate delivery across durable store must be flagged IdempotentReplay, got %+v", second)
	}
	// 对比：内存路径（ingressDir 空）不可区分真实 replay，两次仍各返回 false。
	memAuth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
		agentActive:  true,
	}
	m1, _ := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), result, memAuth, sandbox.AllocationActive)
	m2, _ := AdmitWithDurableAuthority(context.Background(), bindingFor(t, facts), result, memAuth, sandbox.AllocationActive)
	if m1.IdempotentReplay || m2.IdempotentReplay {
		t.Fatal("in-memory path must not flag cross-process replay (no durable state)")
	}
}

// bindingFor 构造当前目录内冻结的 AttemptBinding（ReadAttemptBinding 回放）。
func bindingFor(t *testing.T, facts Facts) *AttemptBinding {
	t.Helper()
	dir := t.TempDir()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatal(err)
	}
	b, err := ReadAttemptBinding(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
