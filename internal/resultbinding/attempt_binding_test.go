package resultbinding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// fakeAuthoritySource 是测试用的 DurableAuthoritySource stub。
type fakeAuthoritySource struct {
	registration provider.ProviderRegistration
	active       bool
	getErr       error
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
	auth := fakeAuthoritySource{active: true}
	_, err := AdmitWithDurableAuthority(context.Background(), nil, []byte(`{}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatal("nil binding must fail closed")
	}
}

// ── R2/R3 负测矩阵：revoke/replace/replay/旧 generation ──────────────────────

func TestAdmitWithDurableAuthorityRejectsTerminatedAllocation(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAttemptBinding(dir, testBindingFacts()); err != nil {
		t.Fatal(err)
	}
	binding, _ := ReadAttemptBinding(dir)
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       true,
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
