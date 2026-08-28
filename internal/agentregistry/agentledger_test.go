package agentregistry

import (
	"errors"
	"testing"
)

// TestAgentLedgerRegisterRecoversAcrossReopen 锁定 durable agent registry：
// 注册 + 生命周期迁移落账后，全新 AgentLedger 在同一目录确定性重放，
// 恢复同一 registration（RegistrationID/IdempotencyKey/LifecycleState 逐字相等）。
func TestAgentLedgerRegisterRecoversAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatalf("NewAgentLedger: %v", err)
	}
	reg, err := ledger.Register(validReg())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := ledger.Transition(reg.RegistrationID, LifecycleStateRevoked); err != nil {
		t.Fatalf("Transition revoked: %v", err)
	}

	restarted, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatalf("NewAgentLedger reopen: %v", err)
	}
	recovered, err := restarted.Lookup(reg.RegistrationID)
	if err != nil {
		t.Fatalf("Lookup after reopen: %v", err)
	}
	if recovered.RegistrationID != reg.RegistrationID ||
		recovered.IdempotencyKey != reg.IdempotencyKey ||
		recovered.LifecycleState != LifecycleStateRevoked {
		t.Fatalf("recovered registration diverges: got %+v, want revoked identity of %+v", recovered, reg)
	}
}

func TestAgentLedgerSnapshotSupersedeRecoversAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ledger.Register(validReg())
	if err != nil {
		t.Fatal(err)
	}
	first := validSnap(reg.RegistrationID, validDigest)
	second := validSnap(reg.RegistrationID, validDigest2)
	if _, err := ledger.AddSnapshot(first); err != nil {
		t.Fatalf("AddSnapshot first: %v", err)
	}
	if _, err := ledger.AddSnapshot(second); err != nil {
		t.Fatalf("AddSnapshot second: %v", err)
	}

	restarted, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatalf("NewAgentLedger reopen: %v", err)
	}
	gotReg, gotSnap, err := restarted.CurrentAuthority(reg.RegistrationID)
	if err != nil {
		t.Fatalf("CurrentAuthority after reopen: %v", err)
	}
	if gotReg.RegistrationID != reg.RegistrationID || gotSnap.SnapshotDigest != second.SnapshotDigest {
		t.Fatalf("current authority did not recover the latest snapshot: reg=%+v snap=%+v", gotReg, gotSnap)
	}
	if gotSnap.SnapshotDigest == first.SnapshotDigest {
		t.Fatal("the superseded snapshot must not remain current after restart")
	}
}

// TestAgentLedgerIdempotentReplayAcrossReopen 锁定幂等：同一 (key,digest)
// 跨进程重复注册不追加重复事实、也不与落账冲突。
func TestAgentLedgerIdempotentReplayAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Register(validReg()); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Register(validReg()); err != nil {
		t.Fatalf("in-process idempotent replay must succeed: %v", err)
	}

	restarted, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Register(validReg())
	if err != nil {
		t.Fatalf("cross-process idempotent replay must succeed: %v", err)
	}
	if got.RegistrationID != validReg().RegistrationID {
		t.Fatalf("idempotent replay must return the existing record, got %q", got.RegistrationID)
	}
}

// TestAgentLedgerConflictFailsClosed 锁定 fail closed：同 IdempotencyKey 但
// 不同 RequestDigest 的注册必须冲突（恢复进程内一样拒绝）。
func TestAgentLedgerConflictFailsClosed(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Register(validReg()); err != nil {
		t.Fatal(err)
	}
	conflict := validReg()
	conflict.RequestDigest = validDigest + "x"
	if _, err := ledger.Register(conflict); err == nil {
		t.Fatal("same idempotency key with different digest must conflict fail closed")
	}

	restarted, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Register(conflict); err == nil {
		t.Fatal("conflict must still fail closed after reopen")
	}
}

// TestAgentLedgerMemoryOnlyFailsClosed 锁定 memory-only 拒绝：空白目录的账本
// 读写一律 fail closed。
func TestAgentLedgerMemoryOnlyFailsClosed(t *testing.T) {
	l, err := NewAgentLedger("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Register(validReg()); !errors.Is(err, ErrMemoryOnlyAgentLedger) {
		t.Fatalf("Register on memory-only ledger must fail closed, got %v", err)
	}
	if _, err := l.Transition("registration:0001", LifecycleStateRevoked); !errors.Is(err, ErrMemoryOnlyAgentLedger) {
		t.Fatalf("Transition on memory-only ledger must fail closed, got %v", err)
	}
	if _, err := l.Lookup("registration:0001"); !errors.Is(err, ErrMemoryOnlyAgentLedger) {
		t.Fatalf("Lookup on memory-only ledger must fail closed, got %v", err)
	}
}
