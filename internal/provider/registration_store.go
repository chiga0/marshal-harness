package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ledgerFileName is the single append-only registration ledger kept inside
// the store directory; every line carries exactly one canonical JSON fact.
const ledgerFileName = "registrations.jsonl"

const (
	factTypeRegistration = "registration"
	factTypeLifecycle    = "lifecycle"
)

// ErrMemoryOnlyRegistration is returned whenever a store that is not bound
// to a durable ledger directory is asked to read or write registrations:
// ADR 0018 §5 forbids memory-only registrations fail closed.
var ErrMemoryOnlyRegistration = errors.New("provider: memory-only registration not allowed: the registration store is not bound to a durable ledger directory")

// ErrRegistrationConflict is the fail-closed rejection of any registration
// that collides with an existing ledger record without being identical: the
// identical idempotency identity with a different registrationDigest, or the
// identical registrationId under a different idempotency identity.
var ErrRegistrationConflict = errors.New("provider: registration conflict")

// ErrUnknownRegistration is returned for reads and lifecycle transitions
// that reference a registrationId the ledger has never accepted.
var ErrUnknownRegistration = errors.New("provider: unknown registrationId")

// RegistrationStore is the durable authority ledger store for
// ProviderRegistration records (ADR 0018 §5): an append-only file ledger of
// canonical JSON facts inside one directory. Registration facts record
// accepted registrations and lifecycle facts record revoke/expire
// transitions; existing lines are never rewritten or deleted. The in-memory
// indexes are a rebuildable projection of the ledger: NewRegistrationStore
// replays every fact, so each restart recovers the accepted registrations,
// their current lifecycle state and the replay protection.
type RegistrationStore struct {
	dir                 string
	byRegistrationId    map[string]ProviderRegistration
	byIdempotencyDigest map[string]string
}

// registrationFact is the append-only ledger fact recording one accepted
// ProviderRegistration (ADR 0018 §5).
type registrationFact struct {
	FactType     string               `json:"factType"`
	Registration ProviderRegistration `json:"registration"`
}

// lifecycleFact is the append-only ledger fact recording one lifecycle
// transition of an already accepted registration; the original registration
// line is never rewritten.
type lifecycleFact struct {
	FactType       string         `json:"factType"`
	RegistrationId string         `json:"registrationId"`
	From           LifecycleState `json:"from"`
	To             LifecycleState `json:"to"`
	TransitionedAt string         `json:"transitionedAt"`
}

// NewRegistrationStore opens (creating it if absent) the durable ledger
// directory and rebuilds the in-memory indexes by replaying every ledger
// fact, so all accepted registrations, their current lifecycle state and the
// replay protection survive restarts. A corrupt, conflicting or non
// canonical ledger fails closed.
func NewRegistrationStore(dir string) (*RegistrationStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("provider: registration store directory must be a non-empty path")
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("provider: create registration ledger directory: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("provider: registration ledger directory: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("provider: registration ledger path is not a directory")
	}
	store := &RegistrationStore{
		dir:                 dir,
		byRegistrationId:    map[string]ProviderRegistration{},
		byIdempotencyDigest: map[string]string{},
	}
	if err := store.recover(); err != nil {
		return nil, err
	}
	return store, nil
}

// Put accepts reg into the durable ledger. The record is validated fail
// closed, only create or active may be the initial lifecycleState, and the
// idempotency digest is the identity key. Replaying the identical record
// merges idempotently without appending a second fact and returns the
// existing record; the identical identity with a different
// registrationDigest and a repeated registrationId under a different
// identity both fail closed as conflicts; revoked and expired records are
// terminal and never resurrected by an ordinary replay. A newly accepted
// registration is appended as a ledger fact before it becomes visible.
func (s *RegistrationStore) Put(reg ProviderRegistration) (ProviderRegistration, error) {
	if err := s.requireBound(); err != nil {
		return ProviderRegistration{}, err
	}
	if err := reg.Validate(); err != nil {
		return ProviderRegistration{}, err
	}
	if reg.LifecycleState != LifecycleStateCreate && reg.LifecycleState != LifecycleStateActive {
		return ProviderRegistration{}, fmt.Errorf("provider: initial registration rejected: lifecycleState %q is terminal and can never be the initial state of an accepted registration", string(reg.LifecycleState))
	}
	idempotencyDigest, err := reg.IdempotencyDigest()
	if err != nil {
		return ProviderRegistration{}, err
	}
	if existingId, ok := s.byIdempotencyDigest[idempotencyDigest]; ok {
		existing := s.byRegistrationId[existingId]
		if err := existing.ValidateReplay(reg); err != nil {
			return ProviderRegistration{}, err
		}
		if existing.RegistrationDigest != reg.RegistrationDigest {
			return ProviderRegistration{}, fmt.Errorf("%w: the identical idempotency identity already exists with a different registrationDigest; refusing to merge or overwrite", ErrRegistrationConflict)
		}
		return existing, nil
	}
	if _, ok := s.byRegistrationId[reg.RegistrationId]; ok {
		return ProviderRegistration{}, fmt.Errorf("%w: registrationId %q is already registered under a different idempotency identity", ErrRegistrationConflict, reg.RegistrationId)
	}
	fact := registrationFact{
		FactType:     factTypeRegistration,
		Registration: reg,
	}
	if err := s.appendFact(fact); err != nil {
		return ProviderRegistration{}, err
	}
	if err := s.indexRegistration(reg); err != nil {
		return ProviderRegistration{}, err
	}
	return reg, nil
}

// Get returns the registration recorded under registrationId with its
// current lifecycleState. Unknown registrationIds and memory-only stores
// fail closed.
func (s *RegistrationStore) Get(registrationId string) (ProviderRegistration, error) {
	if err := s.requireBound(); err != nil {
		return ProviderRegistration{}, err
	}
	registration, ok := s.byRegistrationId[registrationId]
	if !ok {
		return ProviderRegistration{}, fmt.Errorf("%w: %q", ErrUnknownRegistration, registrationId)
	}
	return registration, nil
}

// Revoke transitions the registration to the terminal revoked state by
// appending a lifecycle fact; the original registration line is never
// rewritten. Unknown or already terminal registrations fail closed.
func (s *RegistrationStore) Revoke(registrationId string) error {
	return s.transition(registrationId, LifecycleStateRevoked)
}

// Expire transitions the registration to the terminal expired state by
// appending a lifecycle fact; the original registration line is never
// rewritten. Unknown or already terminal registrations fail closed.
func (s *RegistrationStore) Expire(registrationId string) error {
	return s.transition(registrationId, LifecycleStateExpired)
}

// requireBound fails closed on any store that is not bound to a durable
// ledger directory, including the zero value and nil receivers: memory-only
// registrations are never accepted (ADR 0018 §5).
func (s *RegistrationStore) requireBound() error {
	if s == nil || s.dir == "" {
		return ErrMemoryOnlyRegistration
	}
	return nil
}

// ledgerPath returns the path of the append-only ledger file.
func (s *RegistrationStore) ledgerPath() string {
	return filepath.Join(s.dir, ledgerFileName)
}

// recover replays the ledger file (when present) and rebuilds the in-memory
// indexes. Any malformed, non canonical, conflicting or orphan line fails
// closed.
func (s *RegistrationStore) recover() error {
	file, err := os.Open(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("provider: open registration ledger: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := s.applyLedgerLine(line); err != nil {
			return fmt.Errorf("provider: registration ledger recovery failed at line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("provider: read registration ledger: %w", err)
	}
	return nil
}

// applyLedgerLine validates one ledger line as canonical JSON and applies
// the fact it carries to the in-memory indexes.
func (s *RegistrationStore) applyLedgerLine(line []byte) error {
	canonicalized, err := canonical.JSON(line)
	if err != nil {
		return fmt.Errorf("ledger line rejected: %w", err)
	}
	if !bytes.Equal(canonicalized, line) {
		return fmt.Errorf("ledger line is not in canonical form")
	}
	var envelope struct {
		FactType string `json:"factType"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("decode ledger fact envelope: %w", err)
	}
	switch envelope.FactType {
	case factTypeRegistration:
		return s.applyRegistrationFact(line)
	case factTypeLifecycle:
		return s.applyLifecycleFact(line)
	default:
		return fmt.Errorf("unknown ledger factType %q", envelope.FactType)
	}
}

// applyRegistrationFact validates and indexes one accepted registration
// fact.
func (s *RegistrationStore) applyRegistrationFact(line []byte) error {
	var fact registrationFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("decode registration fact: %w", err)
	}
	registration := fact.Registration
	if err := registration.Validate(); err != nil {
		return fmt.Errorf("registration fact failed validation: %w", err)
	}
	if registration.LifecycleState.IsTerminal() {
		return fmt.Errorf("registration fact starts in terminal lifecycleState %q", string(registration.LifecycleState))
	}
	return s.indexRegistration(registration)
}

// applyLifecycleFact validates and applies one lifecycle transition fact.
func (s *RegistrationStore) applyLifecycleFact(line []byte) error {
	var fact lifecycleFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("decode lifecycle fact: %w", err)
	}
	if err := requireText("lifecycle fact registrationId", fact.RegistrationId); err != nil {
		return err
	}
	if err := fact.From.Validate(); err != nil {
		return fmt.Errorf("lifecycle fact from state: %w", err)
	}
	if err := fact.To.Validate(); err != nil {
		return fmt.Errorf("lifecycle fact to state: %w", err)
	}
	if !fact.To.IsTerminal() {
		return fmt.Errorf("lifecycle fact to state %q is not terminal: the durable ledger never transitions back to create or active", string(fact.To))
	}
	if err := requireRFC3339("lifecycle fact transitionedAt", fact.TransitionedAt); err != nil {
		return err
	}
	return s.applyTransition(fact.RegistrationId, fact.From, fact.To)
}

// indexRegistration records one validated registration in the in-memory
// indexes. The identical idempotency identity with the identical
// registrationDigest merges idempotently; any other collision fails closed.
func (s *RegistrationStore) indexRegistration(registration ProviderRegistration) error {
	idempotencyDigest, err := registration.IdempotencyDigest()
	if err != nil {
		return err
	}
	if existingId, ok := s.byIdempotencyDigest[idempotencyDigest]; ok {
		existing := s.byRegistrationId[existingId]
		if existing.RegistrationDigest == registration.RegistrationDigest {
			return nil
		}
		return fmt.Errorf("%w: the identical idempotency identity already exists with a different registrationDigest", ErrRegistrationConflict)
	}
	if _, ok := s.byRegistrationId[registration.RegistrationId]; ok {
		return fmt.Errorf("%w: registrationId %q already exists under a different idempotency identity", ErrRegistrationConflict, registration.RegistrationId)
	}
	s.byRegistrationId[registration.RegistrationId] = registration
	s.byIdempotencyDigest[idempotencyDigest] = registration.RegistrationId
	return nil
}

// applyTransition applies one lifecycle transition to the in-memory record,
// verifying that the referenced registration exists, currently carries the
// from state and that the from state is not already terminal.
func (s *RegistrationStore) applyTransition(registrationId string, from, to LifecycleState) error {
	registration, ok := s.byRegistrationId[registrationId]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRegistration, registrationId)
	}
	if registration.LifecycleState != from {
		return fmt.Errorf("registration %q carries lifecycleState %q, not the transition source %q", registrationId, string(registration.LifecycleState), string(from))
	}
	if from.IsTerminal() {
		return fmt.Errorf("registration %q is already in terminal lifecycleState %q", registrationId, string(from))
	}
	registration.LifecycleState = to
	s.byRegistrationId[registrationId] = registration
	return nil
}

// transition appends one lifecycle fact and then applies it. The fact is
// durably appended before the in-memory state changes, so a failed append
// never corrupts the ledger view.
func (s *RegistrationStore) transition(registrationId string, to LifecycleState) error {
	if err := s.requireBound(); err != nil {
		return err
	}
	registration, ok := s.byRegistrationId[registrationId]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRegistration, registrationId)
	}
	if registration.LifecycleState.IsTerminal() {
		return fmt.Errorf("registration %q is already in terminal lifecycleState %q and cannot transition to %q", registrationId, string(registration.LifecycleState), string(to))
	}
	fact := lifecycleFact{
		FactType:       factTypeLifecycle,
		RegistrationId: registrationId,
		From:           registration.LifecycleState,
		To:             to,
		TransitionedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.appendFact(fact); err != nil {
		return err
	}
	registration.LifecycleState = to
	s.byRegistrationId[registrationId] = registration
	return nil
}

// appendFact canonicalizes fact under RFC 8785 JCS and appends it as one
// line to the ledger, syncing before returning so the fact is durable.
// Existing lines are never rewritten.
func (s *RegistrationStore) appendFact(fact any) error {
	raw, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("provider: marshal ledger fact: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return fmt.Errorf("provider: canonicalize ledger fact: %w", err)
	}
	line := append(canonicalized, '\n')
	file, err := os.OpenFile(s.ledgerPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("provider: open registration ledger: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		file.Close()
		return fmt.Errorf("provider: append registration ledger fact: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("provider: sync registration ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("provider: close registration ledger: %w", err)
	}
	return nil
}
