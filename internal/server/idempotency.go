package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/gofrs/flock"
)

// ErrIdempotencyConflict is the fail-closed rejection of a submission whose
// (authorityNamespaceId, scope, idempotencyKey) identity already exists with
// a different requestDigest (ADR 0018 §3). A conflict never merges, never
// overwrites and never executes the business operation.
var ErrIdempotencyConflict = errors.New("server: idempotency conflict: identical identity and idempotency key with a different request digest")

// idempotencyRecordKind is the frozen kind of the public-api idempotent
// submission authority records. The records are authority objects owned by
// the authorityNamespaceId (ADR 0018 §1/§10); they are not contract records
// and never enter the Run store.
const idempotencyRecordKind = "IdempotencyRecord"

const (
	idempotencyPhasePending   = "pending"
	idempotencyPhaseCompleted = "completed"
)

// Identity is the idempotent submission identity of one public-api request:
// the authority key space that owns the record, the authority scope the
// request operates in, the authenticated route operation/resource, and the
// client-chosen idempotency key. requestDigest travels alongside but is not
// part of the lookup key.
type Identity struct {
	Namespace authority.AuthorityNamespaceId
	Scope     string
	Operation string
	Resource  string
	Key       string
}

// Record is one durable idempotency authority record. AuthorityNamespaceId,
// Scope, Operation, Resource, IdempotencyKey and RequestDigest carry the
// route-bound canonical submission identity; the record is an authority object owned
// by AuthorityNamespaceId, never by an actor-side securityDomainId. Result is
// the frozen response document of the first accepted submission and Status
// its frozen HTTP status; replays return both verbatim and never re-execute
// the business operation, so a replay can never produce a second business
// object.
type Record struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string                         `json:"scope"`
	Operation            string                         `json:"operation"`
	Resource             string                         `json:"resource"`
	IdempotencyKey       string                         `json:"idempotencyKey"`
	RequestDigest        string                         `json:"requestDigest"`
	Phase                string                         `json:"phase"`
	Intent               json.RawMessage                `json:"intent,omitempty"`
	Status               int                            `json:"status"`
	Result               json.RawMessage                `json:"result,omitempty"`
	CreatedAt            time.Time                      `json:"createdAt"`
	CompletedAt          *time.Time                     `json:"completedAt,omitempty"`
	LastFailureCode      string                         `json:"lastFailureCode,omitempty"`
	LastFailureReason    string                         `json:"lastFailureReason,omitempty"`
	LastFailureAt        *time.Time                     `json:"lastFailureAt,omitempty"`
}

// Store is the durable idempotency authority record store: one atomic JSON
// file per identity under the state root, guarded by a file lock so two
// processes on the same state root serialize per key, and by an in-process
// keyed mutex so one server serializes only the identical operation/resource
// identity while unrelated mutations proceed concurrently.
type Store struct {
	root string
	now  func() time.Time

	mu    sync.Mutex
	locks map[string]*identityMutex
}

type identityMutex struct {
	mu   sync.Mutex
	refs int
}

// NewIdempotencyStore binds one store to its durable directory. The
// directory is created lazily on first write.
func NewIdempotencyStore(root string, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{root: root, now: now, locks: make(map[string]*identityMutex)}
}

// Outcome reports one accepted submission: the frozen result document, its
// frozen status and whether the returned result is a merged replay of an
// existing record.
type Outcome struct {
	Result   json.RawMessage
	Status   int
	Replayed bool
}

// Submit decides one idempotent submission. When no record exists for the
// identity, execute runs exactly once and its result becomes the durable
// record; when the identical identity and requestDigest already exist, the
// stored result merges without executing; the identical identity with a
// different requestDigest returns ErrIdempotencyConflict fail closed. A
// failed execute keeps a pending intent with stable failure identity, so the
// identical request remains retryable and restart recovery stays observable.
func (s *Store) Submit(identity Identity, requestDigest string, execute func() (json.RawMessage, int, error)) (Outcome, error) {
	return s.SubmitCommand(identity, requestDigest, nil, func(bool, json.RawMessage) (json.RawMessage, int, error) {
		return execute()
	})
}

// SubmitCommand durably records a pending command intent before execution.
// The per-identity lock remains held while execute runs, so identical
// concurrent submissions serialize without blocking unrelated mutations.
// If the process dies after the intent or business state commits, the next
// caller receives recovery=true and the frozen intent to reconcile from the
// authoritative business store instead of blindly repeating side effects.
func (s *Store) SubmitCommand(identity Identity, requestDigest string, prepareIntent func() (json.RawMessage, error), execute func(recovery bool, intent json.RawMessage) (json.RawMessage, int, error)) (Outcome, error) {
	if err := validateIdentity(identity, requestDigest); err != nil {
		return Outcome{}, err
	}
	path, lockPath := s.recordPaths(identity)

	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Outcome{}, fmt.Errorf("server: create idempotency directory: %w", err)
	}
	unlockIdentity := s.lockIdentity(path)
	defer unlockIdentity()
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return Outcome{}, fmt.Errorf("server: idempotency lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	existing, found, err := s.readRecord(path)
	if err != nil {
		return Outcome{}, err
	}
	if found {
		if err := matchRecord(existing, identity); err != nil {
			return Outcome{}, err
		}
		if existing.RequestDigest != requestDigest {
			return Outcome{}, ErrIdempotencyConflict
		}
		if existing.Phase == idempotencyPhaseCompleted {
			return Outcome{Result: existing.Result, Status: existing.Status, Replayed: true}, nil
		}
	} else {
		var intent json.RawMessage
		if prepareIntent != nil {
			intent, err = prepareIntent()
			if err != nil {
				return Outcome{}, err
			}
		}
		pending := Record{
			APIVersion:           domain.APIVersionV1Alpha1,
			Kind:                 idempotencyRecordKind,
			AuthorityNamespaceId: identity.Namespace,
			Scope:                identity.Scope,
			Operation:            identity.Operation,
			Resource:             identity.Resource,
			IdempotencyKey:       identity.Key,
			RequestDigest:        requestDigest,
			Phase:                idempotencyPhasePending,
			Intent:               intent,
			CreatedAt:            s.now().UTC(),
		}
		if err := s.writeRecord(path, pending); err != nil {
			return Outcome{}, err
		}
		existing = pending
	}

	result, status, err := execute(found, existing.Intent)
	if err != nil {
		// Preserve the command intent and only stable, non-sensitive failure
		// identity. The same request may retry against this pending record; a
		// crash and a returned rejection therefore share one recovery model.
		failedAt := s.now().UTC()
		existing.LastFailureCode, existing.LastFailureReason, existing.LastFailureAt = "INTERNAL", "executor-failed", &failedAt
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			existing.LastFailureCode, existing.LastFailureReason = string(apiErr.Code), apiErr.Reason
		}
		if writeErr := s.writeRecord(path, existing); writeErr != nil {
			return Outcome{}, errors.Join(err, writeErr)
		}
		return Outcome{}, err
	}
	if len(result) == 0 {
		return Outcome{}, errors.New("server: idempotency executor returned no result")
	}
	if status < 200 || status > 299 {
		return Outcome{}, fmt.Errorf("server: idempotency executor returned non-success status %d", status)
	}
	completedAt := s.now().UTC()
	record := existing
	record.Phase = idempotencyPhaseCompleted
	record.Status = status
	record.Result = result
	record.CompletedAt = &completedAt
	record.LastFailureCode, record.LastFailureReason, record.LastFailureAt = "", "", nil
	if err := s.writeRecord(path, record); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: result, Status: status}, nil
}

// Get returns the stored record of one identity when present.
func (s *Store) Get(identity Identity) (Record, bool, error) {
	if err := validateIdentity(identity, digestPrefix+strings.Repeat("0", 64)); err != nil {
		return Record{}, false, err
	}
	path, _ := s.recordPaths(identity)
	unlockIdentity := s.lockIdentity(path)
	defer unlockIdentity()
	record, found, err := s.readRecord(path)
	if err != nil || !found {
		return Record{}, found, err
	}
	if err := matchRecord(record, identity); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

// matchRecord fails closed when a durable record's frozen identity no longer
// matches the requested submission identity. Record paths are derived from
// the canonical digest of the complete operation/resource-bound lookup
// binding, so a mismatch can only arise from a hash collision or tampering;
// such a record never merges and never executes.
func matchRecord(record Record, identity Identity) error {
	if !record.AuthorityNamespaceId.Equal(identity.Namespace) ||
		record.Scope != identity.Scope ||
		record.Operation != identity.Operation ||
		record.Resource != identity.Resource ||
		record.IdempotencyKey != identity.Key {
		return errors.New("server: idempotency record does not match the submission identity")
	}
	return nil
}

func validateIdentity(identity Identity, requestDigest string) error {
	if err := identity.Namespace.Validate(); err != nil {
		return fmt.Errorf("server: idempotency identity: %w", err)
	}
	if strings.TrimSpace(identity.Scope) == "" {
		return errors.New("server: idempotency identity: scope must be a non-empty string")
	}
	if identity.Scope != identity.Namespace.AuthorityScopeId {
		return errors.New("server: idempotency identity: scope does not match the authority namespace")
	}
	if strings.TrimSpace(identity.Key) == "" {
		return errors.New("server: idempotency identity: idempotencyKey must be a non-empty string")
	}
	if len(identity.Key) > maxIdempotencyKeyBytes {
		return errors.New("server: idempotency identity: idempotencyKey exceeds its size limit")
	}
	if strings.TrimSpace(identity.Operation) == "" || len(identity.Operation) > 128 {
		return errors.New("server: idempotency identity: operation must be a bounded non-empty string")
	}
	if strings.TrimSpace(identity.Resource) == "" || len(identity.Resource) > 512 {
		return errors.New("server: idempotency identity: resource must be a bounded non-empty string")
	}
	if !strings.HasPrefix(requestDigest, digestPrefix) || len(requestDigest) != len(digestPrefix)+64 {
		return errors.New("server: idempotency identity: requestDigest must be a sha256 hex digest")
	}
	return nil
}

// recordPaths derives the durable file pair of one identity from the
// canonical digest of its operation- and resource-bound identity
// binding, so path construction is deterministic and collision-resistant.
func (s *Store) recordPaths(identity Identity) (recordPath, lockPath string) {
	binding := struct {
		AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
		Scope                string                         `json:"scope"`
		Operation            string                         `json:"operation"`
		Resource             string                         `json:"resource"`
		IdempotencyKey       string                         `json:"idempotencyKey"`
	}{identity.Namespace, identity.Scope, identity.Operation, identity.Resource, identity.Key}
	// The binding fields are validated by validateIdentity before any path
	// derivation; canonical marshal of a validated value cannot fail.
	data, err := json.Marshal(binding)
	if err != nil {
		data = []byte(fmt.Sprintf("%v", binding))
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])
	return filepath.Join(s.root, name+".json"), filepath.Join(s.root, name+".lock")
}

func (s *Store) readRecord(path string) (Record, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("server: read idempotency record: %w", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, false, fmt.Errorf("server: idempotency record unreadable: %w", err)
	}
	if record.Kind != idempotencyRecordKind || record.APIVersion != domain.APIVersionV1Alpha1 ||
		(record.Phase != idempotencyPhasePending && record.Phase != idempotencyPhaseCompleted) ||
		(record.Phase == idempotencyPhaseCompleted && (len(record.Result) == 0 || record.Status < 200 || record.Status > 299)) {
		return Record{}, false, errors.New("server: idempotency record unreadable: unsupported record")
	}
	return record, true, nil
}

func (s *Store) lockIdentity(key string) func() {
	s.mu.Lock()
	entry := s.locks[key]
	if entry == nil {
		entry = &identityMutex{}
		s.locks[key] = entry
	}
	entry.refs++
	s.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, key)
		}
		s.mu.Unlock()
	}
}

// writeRecord persists one record atomically: same-directory temp file,
// fsync, rename, directory fsync, owner-only permissions.
func (s *Store) writeRecord(path string, record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("server: encode idempotency record: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("server: create idempotency directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".idempotency-*.tmp")
	if err != nil {
		return fmt.Errorf("server: stage idempotency record: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("server: secure idempotency record: %w", err)
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("server: write idempotency record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("server: sync idempotency record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("server: close idempotency record: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("server: commit idempotency record: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("server: open idempotency directory: %w", err)
	}
	err = directoryHandle.Sync()
	_ = directoryHandle.Close()
	if err != nil {
		return fmt.Errorf("server: sync idempotency directory: %w", err)
	}
	return nil
}
