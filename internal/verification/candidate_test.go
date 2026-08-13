package verification

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// Gitleaks-safe fixture constructors: every digest/SHA literal is assembled
// from repetition of a single hex seed, never written as a whole literal.

func candidateFixtureDigest(seed string) string {
	return "sha256:" + strings.Repeat(seed, 64)[:64]
}

func candidateFixtureSHA(seed string) string {
	return strings.Repeat(seed, 40)[:40]
}

func candidateFixturePayload(name string) []byte {
	return []byte(name + " observed patch bytes\n")
}

// candidateRecord seals a Candidate against the payload: contentDigest is the
// content address of the payload bytes and candidateDigest is back-filled
// from the detached canonical digest, exactly as buildCandidate does.
func candidateRecord(t *testing.T, payload []byte, producerKind domain.ProducerKind, producer, predecessor string, createdAt time.Time) domain.Candidate {
	t.Helper()
	record := domain.Candidate{
		APIVersion:                 domain.APIVersionV1Alpha1,
		Kind:                       domain.KindCandidate,
		TaskID:                     "task:candidate",
		RunID:                      "run:candidate",
		AttemptID:                  "attempt:candidate",
		AuthorityNamespaceID:       candidateFixtureDigest("a"),
		BaseSHA:                    candidateFixtureSHA("0"),
		ContentDigest:              canonical.DigestBytes(payload),
		ProducerKind:               producerKind,
		Producer:                   producer,
		PredecessorCandidateDigest: predecessor,
		CreatedAt:                  createdAt.UTC(),
	}
	digest, err := record.Digest()
	if err != nil {
		t.Fatal(err)
	}
	record.CandidateDigest = digest
	return record
}

// candidateFixtureChain admits the dual-record chain (worker root +
// normalizer successor) into a fresh store and returns both records.
func candidateFixtureChain(t *testing.T) (*localCandidateStore, domain.Candidate, domain.Candidate, []byte, []byte) {
	t.Helper()
	store := newLocalCandidateStore(t.TempDir())
	workerPayload := candidateFixturePayload("worker")
	worker := candidateRecord(t, workerPayload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	if _, err := store.Admit(worker, workerPayload); err != nil {
		t.Fatal(err)
	}
	normalizerPayload := candidateFixturePayload("normalizer")
	normalizer := candidateRecord(t, normalizerPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, worker.CandidateDigest, time.Unix(1001, 0))
	if _, err := store.Admit(normalizer, normalizerPayload); err != nil {
		t.Fatal(err)
	}
	return store, worker, normalizer, workerPayload, normalizerPayload
}

// candidateRecordFiles lists admitted record files. The candidates directory
// legitimately does not exist until the first successful persistence
// (rejected admissions and never-admitted stores must leave no trace), so
// absence reads as zero files exactly like records() treats it.
func candidateRecordFiles(t *testing.T, store *localCandidateStore) []string {
	t.Helper()
	entries, err := os.ReadDir(store.candidatesDir())
	if errors.Is(err, os.ErrNotExist) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

func TestCandidateStoreAdmitsDualRecordChain(t *testing.T) {
	store, worker, normalizer, workerPayload, normalizerPayload := candidateFixtureChain(t)

	// Both records are on disk under their record digests.
	if files := candidateRecordFiles(t, store); len(files) != 2 {
		t.Fatalf("candidate store files = %v", files)
	}

	storedWorker, err := store.ByDigest(worker.CandidateDigest)
	if err != nil {
		t.Fatal(err)
	}
	storedNormalizer, err := store.ByDigest(normalizer.CandidateDigest)
	if err != nil {
		t.Fatal(err)
	}

	// Chain shape: the normalizer predecessor points at the worker record
	// identity, and the chain agrees field by field on base and attempt
	// identity (§7.3 positive case).
	if storedNormalizer.PredecessorCandidateDigest != storedWorker.CandidateDigest {
		t.Fatalf("predecessor = %s, worker digest = %s", storedNormalizer.PredecessorCandidateDigest, storedWorker.CandidateDigest)
	}
	if storedWorker.PredecessorCandidateDigest != "" {
		t.Fatalf("worker record is not a chain root: %+v", storedWorker)
	}
	if storedWorker.BaseSHA != storedNormalizer.BaseSHA || storedWorker.TaskID != storedNormalizer.TaskID ||
		storedWorker.RunID != storedNormalizer.RunID || storedWorker.AttemptID != storedNormalizer.AttemptID {
		t.Fatalf("chain identity diverges: worker=%+v normalizer=%+v", storedWorker, storedNormalizer)
	}
	// Content addressing: contentDigest equals the payload digest, and the
	// record digest recomputes from the stored bytes.
	if storedWorker.ContentDigest != canonical.DigestBytes(workerPayload) || storedNormalizer.ContentDigest != canonical.DigestBytes(normalizerPayload) {
		t.Fatalf("content digests diverge from payloads: %+v / %+v", storedWorker, storedNormalizer)
	}
	for _, record := range []domain.Candidate{storedWorker, storedNormalizer} {
		recomputed, err := record.Digest()
		if err != nil || recomputed != record.CandidateDigest {
			t.Fatalf("detached digest recompute = %q err = %v, stored = %q", recomputed, err, record.CandidateDigest)
		}
	}
	// Producer identities follow the frozen convention.
	if storedWorker.ProducerKind != domain.ProducerKindWorker || storedWorker.Producer != "worker" {
		t.Fatalf("worker producer identity = %+v", storedWorker)
	}
	if storedNormalizer.ProducerKind != domain.ProducerKindNormalizer || storedNormalizer.Producer != candidateProducerNormalizer {
		t.Fatalf("normalizer producer identity = %+v", storedNormalizer)
	}

	head, ok, err := store.Head()
	if err != nil || !ok || head.CandidateDigest != normalizer.CandidateDigest {
		t.Fatalf("head = %+v ok = %v err = %v", head, ok, err)
	}
}

func TestCandidateStoreIdempotentAdmissionCoalesces(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("repeat")
	record := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	first, err := store.Admit(record, payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Admit(record, payload)
	if err != nil {
		t.Fatalf("repeated admission must coalesce idempotently: %v", err)
	}
	if !first.Equal(second) || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("coalesced record diverged: first=%+v second=%+v", first, second)
	}
	if files := candidateRecordFiles(t, store); len(files) != 1 {
		t.Fatalf("store must hold exactly one copy: %v", files)
	}
}

func TestCandidateStoreQuarantinesIdentitySlotConflict(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("conflict")
	first := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	if _, err := store.Admit(first, payload); err != nil {
		t.Fatal(err)
	}
	// Same identity slot (identical content address and chain position) but
	// divergent metadata: a conflicting record that must be quarantined,
	// never overwrite the admitted object.
	conflict := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(2000, 0))
	if conflict.CandidateDigest == first.CandidateDigest {
		t.Fatal("fixture bug: conflicting record must carry a distinct record digest")
	}
	if _, err := store.Admit(conflict, payload); err == nil {
		t.Fatal("identity slot conflict must fail closed")
	}
	quarantined, err := os.ReadDir(store.quarantineDir())
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("quarantine directory = %v err = %v", quarantined, err)
	}
	quarantineData, err := os.ReadFile(filepath.Join(store.quarantineDir(), quarantined[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(quarantineData), strings.TrimPrefix(conflict.CandidateDigest, "sha256:")) {
		t.Fatalf("quarantined bytes are not the conflicting record: %s", quarantineData)
	}
	// The current object is untouched: single record, first createdAt kept.
	if files := candidateRecordFiles(t, store); len(files) != 1 {
		t.Fatalf("conflict must not add a record: %v", files)
	}
	kept, err := store.ByDigest(first.CandidateDigest)
	if err != nil || !kept.Equal(first) {
		t.Fatalf("admitted record was disturbed: %+v err = %v", kept, err)
	}
	head, ok, err := store.Head()
	if err != nil || !ok || head.CandidateDigest != first.CandidateDigest {
		t.Fatalf("head after conflict = %+v ok = %v err = %v", head, ok, err)
	}
}

func TestCandidateStoreRejectsPayloadDigestMismatch(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("genuine")
	record := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	if _, err := store.Admit(record, candidateFixturePayload("forged")); err == nil {
		t.Fatal("payload bytes diverging from contentDigest must be rejected")
	}
	// Rejection happens before any write: the candidates directory must never
	// come into existence, and the store must still read as empty.
	if _, statErr := os.Stat(store.candidatesDir()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected admission must not create the candidates directory: %v", statErr)
	}
	if files := candidateRecordFiles(t, store); len(files) != 0 {
		t.Fatalf("rejected admission must not persist: %v", files)
	}
}

// TestCandidateStoreToleratesAbsentCandidatesDirectory pins the regression
// behind the t2-candidates-dir finding: a store whose candidates directory
// was never created (nothing ever admitted, or every admission rejected)
// must read as empty and fail closed on lookups instead of erroring on the
// missing directory.
func TestCandidateStoreToleratesAbsentCandidatesDirectory(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	if _, statErr := os.Stat(store.candidatesDir()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a fresh store must not create the candidates directory eagerly: %v", statErr)
	}
	if records, err := store.records(); err != nil || len(records) != 0 {
		t.Fatalf("absent directory must read as zero records: %+v err = %v", records, err)
	}
	if _, ok, err := store.Head(); err != nil || ok {
		t.Fatalf("absent directory must report head absence: ok = %v err = %v", ok, err)
	}
	if _, ok, err := store.findAdmittedByContent("attempt:candidate", candidateFixtureDigest("f")); err != nil || ok {
		t.Fatalf("absent directory must report content absence: ok = %v err = %v", ok, err)
	}
	if _, err := store.ByDigest(candidateFixtureDigest("a")); err == nil {
		t.Fatal("ByDigest must fail closed when nothing was ever admitted")
	}
	if files := candidateRecordFiles(t, store); len(files) != 0 {
		t.Fatalf("absent directory must list zero files: %v", files)
	}
}

func TestCandidateStoreRejectsTamperedRecordDigest(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("tampered")
	record := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	record.CandidateDigest = candidateFixtureDigest("e")
	if _, err := store.Admit(record, payload); err == nil {
		t.Fatal("stored candidateDigest diverging from the recomputed detached digest must be rejected")
	}
}

func TestCandidateStorePredecessorChainFailsClosed(t *testing.T) {
	_, worker, _, workerPayload, _ := candidateFixtureChain(t)
	newPayload := candidateFixturePayload("successor")

	t.Run("worker carrying predecessor is rejected", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		record := candidateRecord(t, newPayload, domain.ProducerKindWorker, candidateProducerWorker, worker.CandidateDigest, time.Unix(1100, 0))
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("worker records are chain roots; predecessor must be rejected")
		}
	})
	t.Run("normalizer without predecessor is rejected", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		record := candidateRecord(t, newPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, "", time.Unix(1100, 0))
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("normalizer records require a predecessor")
		}
	})
	t.Run("predecessor digest unknown to the store is rejected", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		record := candidateRecord(t, newPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, candidateFixtureDigest("f"), time.Unix(1100, 0))
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("predecessor must already be admitted")
		}
	})
	t.Run("predecessor baseSha divergence is rejected", func(t *testing.T) {
		store, predecessor := candidateFixtureRoot(t, candidateFixtureSHA("1"))
		record := candidateRecord(t, newPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, predecessor.CandidateDigest, time.Unix(1100, 0))
		record.BaseSHA = candidateFixtureSHA("2")
		digest, err := record.Digest()
		if err != nil {
			t.Fatal(err)
		}
		record.CandidateDigest = digest
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("predecessor and successor baseSha must agree")
		}
	})
	t.Run("predecessor attempt identity divergence is rejected", func(t *testing.T) {
		store, predecessor := candidateFixtureRoot(t, candidateFixtureSHA("1"))
		record := candidateRecord(t, newPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, predecessor.CandidateDigest, time.Unix(1100, 0))
		record.AttemptID = "attempt:other"
		digest, err := record.Digest()
		if err != nil {
			t.Fatal(err)
		}
		record.CandidateDigest = digest
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("predecessor and successor attempt identity must agree")
		}
	})
	t.Run("tampered predecessor record is rejected", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		predecessor := candidateRecord(t, workerPayload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
		if _, err := store.Admit(predecessor, workerPayload); err != nil {
			t.Fatal(err)
		}
		path := store.recordPath(predecessor.CandidateDigest)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var tampered domain.Candidate
		if err := json.Unmarshal(data, &tampered); err != nil {
			t.Fatal(err)
		}
		tampered.Producer = "evil"
		tamperedData, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(tamperedData, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		record := candidateRecord(t, newPayload, domain.ProducerKindNormalizer, candidateProducerNormalizer, predecessor.CandidateDigest, time.Unix(1100, 0))
		if _, err := store.Admit(record, newPayload); err == nil {
			t.Fatal("a tampered predecessor must break admission fail-closed")
		}
	})
}

// candidateFixtureRoot admits a single worker root with the supplied baseSha.
func candidateFixtureRoot(t *testing.T, baseSHA string) (*localCandidateStore, domain.Candidate) {
	t.Helper()
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("root")
	record := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	record.BaseSHA = baseSHA
	digest, err := record.Digest()
	if err != nil {
		t.Fatal(err)
	}
	record.CandidateDigest = digest
	if _, err := store.Admit(record, payload); err != nil {
		t.Fatal(err)
	}
	return store, record
}

func TestCandidateStoreByDigestFailsClosed(t *testing.T) {
	store, worker, _, _, _ := candidateFixtureChain(t)
	if _, err := store.ByDigest("not-a-digest"); err == nil {
		t.Fatal("malformed digest form must be rejected")
	}
	if _, err := store.ByDigest(candidateFixtureDigest("f")); err == nil {
		t.Fatal("unknown digest must be rejected")
	}
	// Tamper with the stored bytes: the recomputed detached digest must no
	// longer match and resolution fails closed.
	path := store.recordPath(worker.CandidateDigest)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record domain.Candidate
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.Producer = "evil"
	tampered, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(tampered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ByDigest(worker.CandidateDigest); err == nil {
		t.Fatal("tampered record bytes must fail re-validation")
	}
}

func TestCandidateStoreHeadSemantics(t *testing.T) {
	t.Run("empty store reports absence", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		if _, ok, err := store.Head(); err != nil || ok {
			t.Fatalf("empty head = ok=%v err=%v", ok, err)
		}
	})
	t.Run("ambiguous roots fail closed", func(t *testing.T) {
		store := newLocalCandidateStore(t.TempDir())
		firstPayload := candidateFixturePayload("first-root")
		first := candidateRecord(t, firstPayload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
		if _, err := store.Admit(first, firstPayload); err != nil {
			t.Fatal(err)
		}
		secondPayload := candidateFixturePayload("second-root")
		second := candidateRecord(t, secondPayload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1001, 0))
		second.AttemptID = "attempt:other"
		digest, err := second.Digest()
		if err != nil {
			t.Fatal(err)
		}
		second.CandidateDigest = digest
		if _, err := store.Admit(second, secondPayload); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Head(); err == nil {
			t.Fatal("two unreferenced roots must fail closed instead of guessing a head")
		}
	})
}

func TestCandidateStoreRejectsForeignFilename(t *testing.T) {
	store := newLocalCandidateStore(t.TempDir())
	payload := candidateFixturePayload("misnamed")
	record := candidateRecord(t, payload, domain.ProducerKindWorker, candidateProducerWorker, "", time.Unix(1000, 0))
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(store.candidatesDir(), candidateFixtureDigest("b")+".json"), append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Head(); err == nil {
		t.Fatal("a record stored under a foreign filename must fail closed")
	}
	if _, err := store.ByDigest(candidateFixtureDigest("b")); err == nil {
		t.Fatal("ByDigest must reject a record whose identity does not match the filename")
	}
}

func TestCandidateStoreFindAdmittedByContent(t *testing.T) {
	store, worker, normalizer, workerPayload, normalizerPayload := candidateFixtureChain(t)
	byContent, ok, err := store.findAdmittedByContent(worker.AttemptID, canonical.DigestBytes(normalizerPayload))
	if err != nil || !ok || byContent.CandidateDigest != normalizer.CandidateDigest {
		t.Fatalf("content lookup = %+v ok = %v err = %v", byContent, ok, err)
	}
	byContent, ok, err = store.findAdmittedByContent(worker.AttemptID, canonical.DigestBytes(workerPayload))
	if err != nil || !ok || byContent.CandidateDigest != worker.CandidateDigest {
		t.Fatalf("worker content lookup = %+v ok = %v err = %v", byContent, ok, err)
	}
	_, ok, err = store.findAdmittedByContent(worker.AttemptID, candidateFixtureDigest("f"))
	if err != nil || ok {
		t.Fatalf("unknown content must report absence: ok = %v err = %v", ok, err)
	}
	// Content is attempt-scoped: another attempt sees nothing.
	_, ok, err = store.findAdmittedByContent("attempt:other", canonical.DigestBytes(workerPayload))
	if err != nil || ok {
		t.Fatalf("cross-attempt content lookup must not leak: ok = %v err = %v", ok, err)
	}
	if _, _, err := store.findAdmittedByContent("not an id", candidateFixtureDigest("f")); err == nil {
		t.Fatal("malformed attempt identity must fail closed")
	}
	if _, _, err := store.findAdmittedByContent(worker.AttemptID, "not-a-digest"); err == nil {
		t.Fatal("malformed content digest must fail closed")
	}
}
