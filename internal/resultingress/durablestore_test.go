package resultingress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDurableIngressIndependentStoresSerializeReplayCheckAndAppend(t *testing.T) {
	dir := t.TempDir()
	const writers = 8
	start := make(chan struct{})
	facts := make(chan AdmissionFact, writers)
	errs := make(chan error, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	for n := 0; n < writers; n++ {
		go func(n int) {
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				ready.Done()
				errs <- err
				return
			}
			ingress, err := NewDurableIngress(validBinding(), store)
			if err != nil {
				ready.Done()
				errs <- err
				return
			}
			ready.Done()
			<-start
			digest := fixedDigest(fmt.Sprintf("parallel-%d", n))
			drc := validDRC(digest)
			drc.IdempotencyKey = fmt.Sprintf("parallel-key-%d", n)
			drc.Nonce = fmt.Sprintf("parallel-nonce-%d", n)
			fact, err := ingress.Admit(context.Background(), drc, validEnvelope(digest, 1))
			if err != nil {
				errs <- err
				return
			}
			facts <- fact
		}(n)
	}
	ready.Wait()
	close(start)
	sequences := make([]int, 0, writers)
	for n := 0; n < writers; n++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case fact := <-facts:
			sequences = append(sequences, int(fact.LedgerSequence))
		}
	}
	sort.Ints(sequences)
	for n, sequence := range sequences {
		if sequence != n+1 {
			t.Fatalf("serialized ledger sequences = %v", sequences)
		}
	}
}

func TestDurableIngressAppendFailureDoesNotAdvanceMemorySequence(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := NewDurableIngress(validBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dir, resultIngressStoreFileName)
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := fixedDigest("append-failure")
	if _, err := ingress.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1)); err == nil {
		t.Fatal("ledger append failure was accepted")
	}
	if ingress.ledgerSequence != 0 {
		t.Fatalf("append failure advanced in-memory ledger sequence to %d", ingress.ledgerSequence)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	fact, err := ingress.Admit(context.Background(), validDRC(digest), validEnvelope(digest, 1))
	if err != nil || fact.LedgerSequence != 1 {
		t.Fatalf("first durable retry = %+v, %v", fact, err)
	}
}

func TestDurableStoreSeparatesSemanticReplayConflictFromPhysicalReadFailure(t *testing.T) {
	scope := attemptTestOwnerScope(attemptTestIdentity())
	for name, ledger := range map[string][]byte{
		"unknown-fact":   []byte("{}\n"),
		"truncated-tail": []byte("{}"),
		"non-canonical":  []byte("{ }\n"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), ledger, 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.OpenOwner(scope); !errors.Is(err, ErrDurableReplayConflict) {
				t.Fatalf("semantic replay error = %v, want ErrDurableReplayConflict", err)
			}
		})
	}

	t.Run("physical-read", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, resultIngressStoreFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := OpenResultIngressStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.OpenOwner(scope)
		if err == nil || errors.Is(err, ErrDurableReplayConflict) {
			t.Fatalf("physical read error = %v, must not be a semantic replay conflict", err)
		}
	})
}

func TestDurableIngressReplayBindsEnvelopeKindAndSequence(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	ingress, err := NewDurableIngress(validBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixedDigest("exact-envelope")
	drc := validDRC(digest)
	if _, err := ingress.Admit(context.Background(), drc, validEnvelope(digest, 1)); err != nil {
		t.Fatal(err)
	}
	for _, drift := range []ResultEnvelope{
		{Kind: KindWorkerResult, ResultDigest: digest, Sequence: 2},
		{Kind: KindAssessment, ResultDigest: digest, Sequence: 1},
	} {
		reopenedStore, _ := OpenResultIngressStore(dir)
		reopened, openErr := NewDurableIngress(validBinding(), reopenedStore)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, found, replayErr := reopened.ReplayCommitted(drc, drift); found || !errors.Is(replayErr, ErrDigestMismatch) {
			t.Fatalf("drifted envelope %+v replay = found=%v err=%v", drift, found, replayErr)
		}
	}
}

func TestDurableIngressLegacyFixtureBlocksReplayAndPreservesMonotonicHistory(t *testing.T) {
	const legacyLine = `{"digest":"sha256:fd38539eb89424d7d92c0fef2b0693ea8f4a52faf499988e8424d244d1bed08c","envelopeDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","envelopeFactDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","factType":"result-admitted","idempotencyKey":"legacy-key","ledgerSequence":1,"sequence":1}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), []byte(legacyLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := OpenResultIngressStore(dir)
	ingress, err := NewDurableIngress(validBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	legacyDRC := validDRC("sha256:" + strings.Repeat("a", 64))
	legacyDRC.IdempotencyKey = "legacy-key"
	if _, found, replayErr := ingress.ReplayCommitted(legacyDRC, validEnvelope(legacyDRC.RequestDigest, 1)); found || !errors.Is(replayErr, ErrDigestMismatch) {
		t.Fatalf("legacy replay must remain blocked: found=%v err=%v", found, replayErr)
	}
	newDigest := fixedDigest("post-legacy")
	newDRC := validDRC(newDigest)
	newDRC.IdempotencyKey = "post-legacy-key"
	newDRC.Nonce = "post-legacy-nonce"
	fact, err := ingress.Admit(context.Background(), newDRC, validEnvelope(newDigest, 1))
	if err != nil || fact.LedgerSequence != 2 {
		t.Fatalf("post-legacy admission = %+v, %v", fact, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, resultIngressStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if lines[0] != legacyLine || !strings.Contains(lines[len(lines)-1], `"protocolRevision":"result-ingress/v2"`) {
		t.Fatalf("legacy history was rewritten or v2 was not versioned: %s", raw)
	}
}

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
