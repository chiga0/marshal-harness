package resultingress

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDurableIngressReplayDetectedAcrossReopen 锁定 R2 纵切的 durable
// ResultIngress replay：一次成功接纳落账后，全新 store + 全新 Ingress 在同一
// scope 确定性重放——同一 idempotencyKey 与同一 delivery digest 的重复送达
// 被判定为 IdempotentReplay=true 且不推进 ledgerSequence，同一 idempotencyKey
// 但不同 digest 的投递被判定为伪造 fail closed。
func TestDurableIngressReplayDetectedAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatalf("OpenResultIngressStore: %v", err)
	}
	in, err := NewDurableIngress(validBinding(), store)
	if err != nil {
		t.Fatalf("NewDurableIngress: %v", err)
	}
	digest := fixedDigest("payload-1")
	fact, err := in.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1))
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if fact.IdempotentReplay {
		t.Fatal("first admission must not be flagged replay")
	}
	quarantineLen := len(in.Quarantine())

	// 崩溃/重启：全新 store 与全新 Ingress 恢复 admitted 状态。
	reopenedStore, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableIngress(validBinding(), reopenedStore)
	if err != nil {
		t.Fatal(err)
	}
	// 同一 idempotencyKey、同一 digest 重复送达 → 幂等重放，不推进 sequence。
	replayFact, err := reopened.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1))
	if err != nil {
		t.Fatalf("replay Admit must be idempotent, not rejected: %v", err)
	}
	if !replayFact.IdempotentReplay {
		t.Fatal("duplicate delivery of identical (idempotencyKey,digest) must be flagged IdempotentReplay")
	}
	if replayFact.LedgerSequence != fact.LedgerSequence {
		t.Fatalf("idempotent replay must not advance ledgerSequence: got %d want %d", replayFact.LedgerSequence, fact.LedgerSequence)
	}
	// 同一 idempotencyKey、不同 digest → 伪造 fail closed 且落 quarantine。
	forged := "sha256:" + strings.Repeat("b", 64)
	if _, err := reopened.Admit(context.Background(), validDRC(forged), validEnvelope(forged, 1)); err == nil {
		t.Fatal("same idempotencyKey with different digest must be rejected as forgery")
	}
	if len(reopened.Quarantine()) <= quarantineLen {
		t.Fatal("forgery attempt must append a quarantine record")
	}
}

// TestDurableIngressCommittedReplaySurvivesLeaseExpiry locks the recovery
// rule needed by the admission outbox: an already committed, byte-identical
// delivery remains replayable after restart even when recovery happens after
// lease expiry. A different DRC cannot claim that effect.
func TestDurableIngressCommittedReplaySurvivesLeaseExpiry(t *testing.T) {
	dir := t.TempDir()
	binding := validBinding()
	now := time.Unix(1_900_000_000, 0).UTC()
	binding.Expiry = now.Add(time.Minute)
	digest := fixedDigest("committed-before-crash")
	drc := validDRC(digest)
	drc.Expiry = binding.Expiry

	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := NewDurableIngress(binding, store)
	if err != nil {
		t.Fatal(err)
	}
	ingress.clock = func() time.Time { return now }
	first, err := ingress.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableIngress(binding, reopenedStore)
	if err != nil {
		t.Fatal(err)
	}
	reopened.clock = func() time.Time { return binding.Expiry.Add(time.Hour) }
	replay, err := reopened.Admit(context.Background(), drc, validEnvelope(digest, 1))
	if err != nil || !replay.IdempotentReplay || replay.FactDigest != first.FactDigest {
		t.Fatalf("committed replay after expiry = %+v, %v", replay, err)
	}

	forged := drc
	forged.Nonce = "different-binding"
	if _, err := reopened.Admit(context.Background(), forged, validEnvelope(digest, 1)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("different DRC must not claim committed effect, got %v", err)
	}
}

// TestDurableIngressQuarantinePersistsAcrossReopen 锁定 quarantine 跨进程
// 恢复：拒绝投递的机械审计记录也能在崩溃/重启后恢复。
func TestDurableIngressQuarantinePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	in, err := NewDurableIngress(validBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	// 显式 DigestMismatch（DRC.RequestDigest != envelope.ResultDigest）→
	// quaration 拒绝并落账。
	drcD := fixedDigest("mismatched-drc")
	envD := fixedDigest("mismatched-envelope")
	if _, err := in.Admit(context.Background(), validDRC(drcD), validEnvelope(envD, 1)); err == nil {
		t.Fatal("mismatched DRC/envelope digest must be rejected")
	}

	reopenedStore, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDurableIngress(validBinding(), reopenedStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Quarantine()) == 0 {
		t.Fatal("quarantine records must be recovered across reopen")
	}
}

// TestResultIngressMemoryOnlyFailsClosed 锁定 memory-only 拒绝：空白目录的
// ResultIngress 账本读写一律 fail closed。
func TestResultIngressMemoryOnlyFailsClosed(t *testing.T) {
	s, err := OpenResultIngressStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAdmitted("k", "sha256:"+strings.Repeat("a", 64), validEnvelope("sha256:"+strings.Repeat("a", 64), 1), "sha256:"+strings.Repeat("a", 64), 1); !errors.Is(err, ErrMemoryOnlyResultIngress) {
		t.Fatalf("RecordAdmitted on memory-only store must fail closed, got %v", err)
	}
	if err := s.RecordQuarantined(ReasonMalformed, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64), futureExpiry); !errors.Is(err, ErrMemoryOnlyResultIngress) {
		t.Fatalf("RecordQuarantined on memory-only store must fail closed, got %v", err)
	}
	if _, err := NewDurableIngress(validBinding(), s); err == nil {
		t.Fatal("NewDurableIngress on memory-only store must fail closed")
	}
}
