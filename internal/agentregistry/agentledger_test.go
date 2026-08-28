package agentregistry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestAgentLedgerCurrentAuthorityRefreshesAcrossOpenInstances(t *testing.T) {
	dir := t.TempDir()
	first, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := first.Register(validReg())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddSnapshot(validSnap(reg.RegistrationID, validDigest)); err != nil {
		t.Fatal(err)
	}
	if _, snap, err := second.CurrentAuthority(reg.RegistrationID); err != nil || snap.SnapshotDigest != validDigest {
		t.Fatalf("second instance did not refresh first snapshot: snap=%+v err=%v", snap, err)
	}
	if _, err := first.AddSnapshot(validSnap(reg.RegistrationID, validDigest2)); err != nil {
		t.Fatal(err)
	}
	if gotReg, snap, err := second.CurrentAuthority(reg.RegistrationID); err != nil || gotReg.LifecycleState != LifecycleStateActive || snap.SnapshotDigest != validDigest2 {
		t.Fatalf("second instance admitted stale authority after supersede: reg=%+v snap=%+v err=%v", gotReg, snap, err)
	}
	if _, err := first.Transition(reg.RegistrationID, LifecycleStateRevoked); err != nil {
		t.Fatal(err)
	}
	gotReg, _, err := second.CurrentAuthority(reg.RegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	if gotReg.LifecycleState != LifecycleStateRevoked {
		t.Fatalf("second instance retained stale lifecycle %q", gotReg.LifecycleState)
	}
}

func TestAgentLedgerSnapshotABARejectsHistoricalReactivation(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ledger.Register(validReg())
	if err != nil {
		t.Fatal(err)
	}
	a := validSnap(reg.RegistrationID, validDigest)
	b := validSnap(reg.RegistrationID, validDigest2)
	for _, snap := range []AgentCapabilitySnapshot{a, b} {
		if _, err := ledger.AddSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ledger.AddSnapshot(a); err == nil || !strings.Contains(err.Error(), "cannot be reactivated") {
		t.Fatalf("A→B→A must fail closed so an epoch-1 Attempt cannot revive, got %v", err)
	}
	_, current, err := ledger.CurrentAuthority(reg.RegistrationID)
	if err != nil || current.SnapshotDigest != b.SnapshotDigest {
		t.Fatalf("rejected A reactivation changed current authority: snap=%+v err=%v", current, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, agentLedgerFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"factType":"agent-capability-snapshot-activated"`) {
		t.Fatalf("rejected A reactivation appended an activation fact: %s", raw)
	}
}

func TestAgentLedgerLegacyActivationFactFailsClosedOnRecovery(t *testing.T) {
	dir := t.TempDir()
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ledger.Register(validReg())
	if err != nil {
		t.Fatal(err)
	}
	a := validSnap(reg.RegistrationID, validDigest)
	b := validSnap(reg.RegistrationID, validDigest2)
	if _, err := ledger.AddSnapshot(a); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.AddSnapshot(b); err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		FactType               string `json:"factType"`
		Sequence               int64  `json:"sequence"`
		RegistrationID         string `json:"registrationId"`
		SnapshotDigest         string `json:"snapshotDigest"`
		PreviousSnapshotDigest string `json:"previousSnapshotDigest"`
		Digest                 string `json:"digest"`
	}{
		FactType:               agentFactTypeActivated,
		Sequence:               4,
		RegistrationID:         reg.RegistrationID,
		SnapshotDigest:         a.SnapshotDigest,
		PreviousSnapshotDigest: b.SnapshotDigest,
	}
	if err := ledger.appendLine(&legacy,
		func() string { return legacy.Digest },
		func(d string) error { legacy.Digest = d; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentLedger(dir); err == nil || !strings.Contains(err.Error(), "can revive stale attempt authority") {
		t.Fatalf("legacy activation fact must fail closed during recovery, got %v", err)
	}
}

func TestAgentLedgerConcurrentWritersHaveStrictSequence(t *testing.T) {
	dir := t.TempDir()
	left, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ledgers := []*AgentLedger{left, right}
	const writers = 12
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := validReg()
			reg.RegistrationID = fmt.Sprintf("registration:%08x", i+10)
			reg.IdempotencyKey = fmt.Sprintf("register-%d", i)
			reg.RequestDigest = fmt.Sprintf("sha256:%064x", i+10)
			_, err := ledgers[i%len(ledgers)].Register(reg)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent register: %v", err)
		}
	}
	restarted, err := NewAgentLedger(dir)
	if err != nil {
		t.Fatalf("strict replay after concurrent writers: %v", err)
	}
	for i := 0; i < writers; i++ {
		id := fmt.Sprintf("registration:%08x", i+10)
		if _, err := restarted.Lookup(id); err != nil {
			t.Fatalf("missing concurrent registration %s: %v", id, err)
		}
	}
}
