package verification

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// CandidateStore is the admission seam for immutable Candidate records
// (ADR 0027). The local MVP implementation is append-only filesystem
// storage under <runDir>/candidates; a remote authority ledger is a
// future backend with identical put-if-absent semantics.
type CandidateStore interface {
	// Admit runs digest-verified put-if-absent. Identical records coalesce
	// idempotently; conflicting bytes are quarantined and an error is
	// returned (fail closed).
	Admit(candidate domain.Candidate, payload []byte) (domain.Candidate, error)
	// ByDigest resolves an admitted record by its candidateDigest.
	ByDigest(candidateDigest string) (domain.Candidate, error)
	// Head returns the latest admitted candidate of the attempt chain.
	Head() (domain.Candidate, bool, error)
}

// Frozen producer identities of the dual-record chain (design §2.1
// convention): the worker Candidate records the Worker's raw observed
// patch bytes (adapter detail already lives in the WorkerResult), and the
// normalizer Candidate records the deterministic gofmt product.
const (
	candidateProducerWorker     = "worker"
	candidateProducerNormalizer = "verifier:format-normalize"
)

// candidateDigestPattern pins the sha256 digest wire form used for record
// identities and content addresses; it doubles as the filename guard so a
// hostile digest can never escape the candidates directory.
var candidateDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// candidateBaseShaPattern requires the locked base to be a full SHA object
// id before any Candidate record may be sealed against it.
var candidateBaseShaPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// localCandidateStore is the append-only filesystem CandidateStore:
// candidates/<candidateDigest>.json, filename = record digest so repeated
// admission is naturally idempotent, plus candidates/quarantine/ for
// conflicting bytes that are isolated forever and never re-admitted
// (ADR 0018 §13).
type localCandidateStore struct {
	runDirectory string
}

// Compile-time proof that the local implementation satisfies the seam.
var _ CandidateStore = (*localCandidateStore)(nil)

// newLocalCandidateStore binds the store to the run directory without
// creating candidates/: the directory must come into existence only with the
// first successfully admitted record (atomicWrite below). Eager creation
// would give legacy no-Attempt runs a candidates/ directory they never had
// (§5 zero regression), and a rejected admission must leave no trace behind;
// every read path therefore treats a missing directory as an empty store.
func newLocalCandidateStore(runDirectory string) *localCandidateStore {
	return &localCandidateStore{runDirectory: runDirectory}
}

func (s *localCandidateStore) candidatesDir() string {
	return filepath.Join(s.runDirectory, "candidates")
}

func (s *localCandidateStore) quarantineDir() string {
	return filepath.Join(s.candidatesDir(), "quarantine")
}

func (s *localCandidateStore) recordPath(candidateDigest string) string {
	return filepath.Join(s.candidatesDir(), candidateDigest+".json")
}

// Admit implements the digest-verified put-if-absent algorithm (design §3.3):
// payload digest verification, fail-closed structural validation, detached
// candidateDigest recompute, normalizer predecessor chain checks, and the
// identity-slot lookup that coalesces identical records, quarantines
// conflicting bytes, or atomically appends the new record.
func (s *localCandidateStore) Admit(candidate domain.Candidate, payload []byte) (domain.Candidate, error) {
	// Step 1: contentDigest is content-addressed; the payload must hash to it.
	if recomputed := canonical.DigestBytes(payload); recomputed != candidate.ContentDigest {
		return domain.Candidate{}, errors.New("candidate admission rejected: payload bytes do not match contentDigest")
	}
	// Step 2: fail-closed structural validation (identity fields, closed
	// producerKind enumeration, chain-shape conditions).
	if err := candidate.Validate(); err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate admission rejected: %w", err)
	}
	// Step 3: detached record digest recompute; any tampered field breaks it.
	detached := candidate
	detached.CandidateDigest = ""
	recomputedRecord, err := detached.Digest()
	if err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate admission rejected: detached digest recompute: %w", err)
	}
	if recomputedRecord != candidate.CandidateDigest {
		return domain.Candidate{}, errors.New("candidate admission rejected: candidateDigest does not match the recomputed detached digest")
	}
	// Step 4: normalizer records must extend an already-admitted predecessor
	// with an identical base and attempt identity.
	if candidate.ProducerKind == domain.ProducerKindNormalizer {
		predecessor, err := s.ByDigest(candidate.PredecessorCandidateDigest)
		if err != nil {
			return domain.Candidate{}, fmt.Errorf("candidate admission rejected: predecessor unavailable: %w", err)
		}
		if predecessor.BaseSHA != candidate.BaseSHA || predecessor.TaskID != candidate.TaskID || predecessor.RunID != candidate.RunID || predecessor.AttemptID != candidate.AttemptID {
			return domain.Candidate{}, errors.New("candidate admission rejected: predecessor identity diverges from the normalizer record")
		}
	}
	// Step 5: identity-slot put-if-absent.
	existing, err := s.findBySlot(candidate)
	if err != nil {
		return domain.Candidate{}, err
	}
	if existing != nil {
		if existing.CandidateDigest == candidate.CandidateDigest {
			// Idempotent coalescing: the first-admitted record (and its
			// createdAt) remains authoritative.
			return *existing, nil
		}
		if err := s.quarantine(candidate); err != nil {
			return domain.Candidate{}, fmt.Errorf("candidate admission rejected: quarantine conflicting bytes: %w", err)
		}
		return domain.Candidate{}, errors.New("candidate admission rejected: identity slot conflict, conflicting bytes quarantined")
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate admission rejected: encode record: %w", err)
	}
	if err := atomicWrite(s.recordPath(candidate.CandidateDigest), append(data, '\n')); err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate admission rejected: persist record: %w", err)
	}
	return candidate, nil
}

// ByDigest resolves and re-validates an admitted record. Every stored byte
// must still reproduce its record identity; tampered files fail closed.
func (s *localCandidateStore) ByDigest(candidateDigest string) (domain.Candidate, error) {
	if !candidateDigestPattern.MatchString(candidateDigest) {
		return domain.Candidate{}, fmt.Errorf("candidate digest %q is not a sha256 digest", candidateDigest)
	}
	data, err := os.ReadFile(s.recordPath(candidateDigest))
	if err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate %s is not admitted: %w", candidateDigest, err)
	}
	var record domain.Candidate
	if err := json.Unmarshal(data, &record); err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate %s record is unreadable: %w", candidateDigest, err)
	}
	if record.CandidateDigest != candidateDigest {
		return domain.Candidate{}, fmt.Errorf("candidate %s record identity mismatch", candidateDigest)
	}
	if err := record.Validate(); err != nil {
		return domain.Candidate{}, fmt.Errorf("candidate %s failed re-validation: %w", candidateDigest, err)
	}
	return record, nil
}

// Head returns the chain tip: the unique admitted record that no other
// admitted record references as predecessor. An empty store reports
// (zero, false, nil); cycles or ambiguous tips fail closed.
func (s *localCandidateStore) Head() (domain.Candidate, bool, error) {
	records, err := s.records()
	if err != nil {
		return domain.Candidate{}, false, err
	}
	if len(records) == 0 {
		return domain.Candidate{}, false, nil
	}
	referenced := make(map[string]bool, len(records))
	for _, record := range records {
		if record.PredecessorCandidateDigest != "" {
			referenced[record.PredecessorCandidateDigest] = true
		}
	}
	var head *domain.Candidate
	for index := range records {
		if referenced[records[index].CandidateDigest] {
			continue
		}
		if head != nil {
			return domain.Candidate{}, false, errors.New("candidate chain is ambiguous: multiple records are not referenced by any successor")
		}
		head = &records[index]
	}
	if head == nil {
		return domain.Candidate{}, false, errors.New("candidate chain is cyclic: no chain tip exists")
	}
	return *head, true, nil
}

// findAdmittedByContent returns the already-admitted record of the attempt
// whose contentDigest equals the supplied value, irrespective of
// producerKind: contentDigest is the content address (ADR 0027), so a
// repeated Verify observing identical bytes must coalesce onto the existing
// fact instead of inflating the chain ("no drift, no new fact"). Multiple
// distinct records claiming identical content fail closed.
func (s *localCandidateStore) findAdmittedByContent(attemptID, contentDigest string) (domain.Candidate, bool, error) {
	if err := domain.ValidateID(attemptID); err != nil {
		return domain.Candidate{}, false, fmt.Errorf("candidate content lookup rejected: %w", err)
	}
	if !candidateDigestPattern.MatchString(contentDigest) {
		return domain.Candidate{}, false, fmt.Errorf("candidate content lookup rejected: contentDigest %q is not a sha256 digest", contentDigest)
	}
	records, err := s.records()
	if err != nil {
		return domain.Candidate{}, false, err
	}
	var match *domain.Candidate
	for index := range records {
		if records[index].AttemptID != attemptID || records[index].ContentDigest != contentDigest {
			continue
		}
		if match != nil && match.CandidateDigest != records[index].CandidateDigest {
			return domain.Candidate{}, false, errors.New("candidate content lookup rejected: conflicting records claim identical content")
		}
		match = &records[index]
	}
	if match == nil {
		return domain.Candidate{}, false, nil
	}
	return *match, true, nil
}

// findBySlot locates the record occupying the identity slot
// (taskId, runId, attemptId, producerKind, predecessorCandidateDigest,
// contentDigest). Two distinct records in one slot mean the store was
// tampered with; that fails closed.
func (s *localCandidateStore) findBySlot(candidate domain.Candidate) (*domain.Candidate, error) {
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	var match *domain.Candidate
	for index := range records {
		record := &records[index]
		if record.TaskID != candidate.TaskID || record.RunID != candidate.RunID || record.AttemptID != candidate.AttemptID ||
			record.ProducerKind != candidate.ProducerKind || record.PredecessorCandidateDigest != candidate.PredecessorCandidateDigest ||
			record.ContentDigest != candidate.ContentDigest {
			continue
		}
		if match != nil && match.CandidateDigest != record.CandidateDigest {
			return nil, errors.New("candidate store corrupted: identity slot holds conflicting records")
		}
		match = record
	}
	return match, nil
}

// records loads every admitted record (candidates/*.json, never the
// quarantine directory) in deterministic order and re-validates each one;
// any invalid file is a tampering signal and fails closed.
func (s *localCandidateStore) records() ([]domain.Candidate, error) {
	entries, err := os.ReadDir(s.candidatesDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read candidate store: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	records := make([]domain.Candidate, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(s.candidatesDir(), name))
		if err != nil {
			return nil, fmt.Errorf("read candidate record %s: %w", name, err)
		}
		var record domain.Candidate
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("candidate record %s is unreadable: %w", name, err)
		}
		if err := record.Validate(); err != nil {
			return nil, fmt.Errorf("candidate record %s failed re-validation: %w", name, err)
		}
		if record.CandidateDigest+".json" != name {
			return nil, fmt.Errorf("candidate record %s is stored under a foreign filename", name)
		}
		records = append(records, record)
	}
	return records, nil
}

// quarantine isolates conflicting bytes under candidates/quarantine/ with a
// timestamped, collision-safe name. Quarantined bytes never flow back into
// the store (ADR 0018 §13).
func (s *localCandidateStore) quarantine(candidate domain.Candidate) error {
	data, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	base := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + strings.ReplaceAll(candidate.ContentDigest, ":", "-")
	name := base + ".json"
	for suffix := 1; ; suffix++ {
		_, statErr := os.Stat(filepath.Join(s.quarantineDir(), name))
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return statErr
		}
		name = fmt.Sprintf("%s-%d.json", base, suffix)
	}
	return atomicWrite(filepath.Join(s.quarantineDir(), name), append(data, '\n'))
}

// buildCandidate constructs a sealed Candidate record: the detached
// candidateDigest is computed over the candidateDigest-stripped document and
// back-filled, so the record validates and the store can recompute identity.
func buildCandidate(input Input, producerKind domain.ProducerKind, producer, contentDigest, predecessorCandidateDigest string, createdAt time.Time) (domain.Candidate, error) {
	record := domain.Candidate{
		APIVersion:                 domain.APIVersionV1Alpha1,
		Kind:                       domain.KindCandidate,
		TaskID:                     input.TaskID,
		RunID:                      input.RunID,
		AttemptID:                  input.AttemptID,
		AuthorityNamespaceID:       input.AuthorityNamespaceID,
		BaseSHA:                    input.BaseSHA,
		ContentDigest:              contentDigest,
		ProducerKind:               producerKind,
		Producer:                   producer,
		PredecessorCandidateDigest: predecessorCandidateDigest,
		CreatedAt:                  createdAt.UTC(),
	}
	digest, err := record.Digest()
	if err != nil {
		return domain.Candidate{}, fmt.Errorf("compute candidate detached digest: %w", err)
	}
	record.CandidateDigest = digest
	return record, nil
}
