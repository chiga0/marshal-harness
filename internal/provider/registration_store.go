package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ledgerFileName is the append-only registration ledger file kept inside the
// store directory: one canonical JSON fact per line, never rewritten.
const ledgerFileName = "registrations.jsonl"

const (
	// factTypeRegistration marks the ledger fact admitting a registration.
	factTypeRegistration = "registration"
	// factTypeLifecycle marks the ledger fact of a lifecycle transition.
	factTypeLifecycle = "lifecycle-transition"
)

// RegistrationStore is the durable authority ledger of ProviderRegistration
// facts (ADR 0018 §5). Every admission and every lifecycle transition is an
// append-only canonical JSON fact in a local file ledger; the in-memory index
// is a rebuildable projection reconstructed from the ledger at construction,
// so a restart recovers every admitted registration, its terminal lifecycle
// state and its replay protection. A store that is not bound to a durable
// ledger directory never accepts a registration: memory-only registrations
// are prohibited. The store assumes single goroutine use; within that
// discipline every accepted mutation is appended to the ledger before the
// index changes, so the ledger is never left behind the projection.
type RegistrationStore struct {
	dir           string
	ledgerPath    string
	ledger        *os.File
	byId          map[string]ProviderRegistration
	byIdempotency map[string]string
	lifecycle     map[string]LifecycleState
}

// registrationFact is one append-only admission fact in the ledger.
type registrationFact struct {
	FactType     string               `json:"factType"`
	Registration ProviderRegistration `json:"registration"`
}

// lifecycleFact is one append-only lifecycle transition fact in the ledger;
// it overlays the admitted registration and never rewrites the original
// registration fact.
type lifecycleFact struct {
	FactType       string         `json:"factType"`
	RegistrationId string         `json:"registrationId"`
	From           LifecycleState `json:"from"`
	To             LifecycleState `json:"to"`
	At             string         `json:"at"`
}

// NewRegistrationStore opens (creating when absent) the append-only
// registration ledger rooted at dir and rebuilds the in-memory index by
// replaying every durable fact, so a restart recovers all admitted
// registrations, their terminal lifecycle states and their replay
// protection. A blank directory fails closed, and any malformed, unknown or
// conflicting ledger fact fails closed during recovery.
func NewRegistrationStore(dir string) (*RegistrationStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("provider: memory-only registration not allowed: a durable ledger directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("provider: ledger directory: %w", err)
	}
	ledgerPath := filepath.Join(dir, ledgerFileName)
	ledger, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("provider: ledger open: %w", err)
	}
	store := &RegistrationStore{
		dir:           dir,
		ledgerPath:    ledgerPath,
		ledger:        ledger,
		byId:          make(map[string]ProviderRegistration),
		byIdempotency: make(map[string]string),
		lifecycle:     make(map[string]LifecycleState),
	}
	if err := store.recover(); err != nil {
		ledger.Close()
		return nil, err
	}
	return store, nil
}

// Put admits reg into the durable ledger under the ADR 0018 §5 replay rules.
// The record must validate fail closed, initial admission only accepts the
// create or active lifecycleState, the identical idempotency identity and
// registration digest merges idempotently without a second ledger fact, the
// same identity with a different digest conflicts fail closed, a reused
// registrationId under a different idempotency identity conflicts fail
// closed, and a revoked or expired registration is never resurrected by an
// ordinary replay.
func (s *RegistrationStore) Put(reg ProviderRegistration) (ProviderRegistration, error) {
	if err := s.bound(); err != nil {
		return ProviderRegistration{}, err
	}
	if err := reg.Validate(); err != nil {
		return ProviderRegistration{}, err
	}
	if reg.LifecycleState != LifecycleStateCreate && reg.LifecycleState != LifecycleStateActive {
		return ProviderRegistration{}, fmt.Errorf("provider: admission requires lifecycleState create or active, got %q", string(reg.LifecycleState))
	}
	idempotencyDigest, err := reg.IdempotencyDigest()
	if err != nil {
		return ProviderRegistration{}, err
	}
	if existingId, exists := s.byIdempotency[idempotencyDigest]; exists {
		existing := s.byId[existingId]
		current, _ := s.currentState(existingId)
		replayed := existing
		replayed.LifecycleState = current
		if err := replayed.ValidateReplay(reg); err != nil {
			return ProviderRegistration{}, err
		}
		if existing.RegistrationDigest != reg.RegistrationDigest {
			return ProviderRegistration{}, fmt.Errorf("provider: idempotency conflict: identical idempotency identity with a different registrationDigest")
		}
		merged := existing
		merged.LifecycleState = current
		return merged, nil
	}
	if _, exists := s.byId[reg.RegistrationId]; exists {
		return ProviderRegistration{}, fmt.Errorf("provider: idempotency conflict: registrationId %q is already bound to a different idempotency identity", reg.RegistrationId)
	}
	if err := s.appendFact(registrationFact{FactType: factTypeRegistration, Registration: reg}); err != nil {
		return ProviderRegistration{}, err
	}
	s.byId[reg.RegistrationId] = reg
	s.byIdempotency[idempotencyDigest] = reg.RegistrationId
	return reg, nil
}

// Get returns the ledger record of registrationId carrying its current
// lifecycle state. Unknown registrationIds fail closed.
func (s *RegistrationStore) Get(registrationId string) (ProviderRegistration, error) {
	if err := s.bound(); err != nil {
		return ProviderRegistration{}, err
	}
	registration, ok := s.byId[registrationId]
	if !ok {
		return ProviderRegistration{}, fmt.Errorf("provider: unknown registrationId %q", registrationId)
	}
	if current, ok := s.lifecycle[registrationId]; ok {
		registration.LifecycleState = current
	}
	return registration, nil
}

// Revoke appends the terminal revoked lifecycle transition fact for
// registrationId; the original registration fact is never rewritten.
func (s *RegistrationStore) Revoke(registrationId string) error {
	return s.transition(registrationId, LifecycleStateRevoked)
}

// Expire appends the terminal expired lifecycle transition fact for
// registrationId; the original registration fact is never rewritten.
func (s *RegistrationStore) Expire(registrationId string) error {
	return s.transition(registrationId, LifecycleStateExpired)
}

// bound fails closed whenever the store is not attached to a durable ledger
// directory; memory-only registrations are never admitted (ADR 0018 §5).
func (s *RegistrationStore) bound() error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("provider: memory-only registration not allowed: the store is not bound to a durable ledger directory")
	}
	return nil
}

// currentState returns the effective lifecycle state of registrationId: the
// latest transition fact when present, otherwise the admitted state.
func (s *RegistrationStore) currentState(registrationId string) (LifecycleState, bool) {
	if state, ok := s.lifecycle[registrationId]; ok {
		return state, true
	}
	registration, ok := s.byId[registrationId]
	if !ok {
		return "", false
	}
	return registration.LifecycleState, true
}

// transition appends one terminal lifecycle transition fact after failing
// closed on unknown registrationIds and on any transition out of an already
// terminal state. Repeating the identical terminal transition is an
// idempotent no-op that appends nothing.
func (s *RegistrationStore) transition(registrationId string, target LifecycleState) error {
	if err := s.bound(); err != nil {
		return err
	}
	current, ok := s.currentState(registrationId)
	if !ok {
		return fmt.Errorf("provider: unknown registrationId %q", registrationId)
	}
	if current == target {
		return nil
	}
	if current.IsTerminal() {
		return fmt.Errorf("provider: registration %q is already %s and cannot transition to %s", registrationId, string(current), string(target))
	}
	fact := lifecycleFact{
		FactType:       factTypeLifecycle,
		RegistrationId: registrationId,
		From:           current,
		To:             target,
		At:             time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.appendFact(fact); err != nil {
		return err
	}
	s.lifecycle[registrationId] = target
	return nil
}

// recover replays the append-only ledger in order and rebuilds the index.
// Any malformed, unknown or conflicting fact fails closed.
func (s *RegistrationStore) recover() error {
	data, err := os.ReadFile(s.ledgerPath)
	if err != nil {
		return fmt.Errorf("provider: ledger read: %w", err)
	}
	for index, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		canonicalized, err := canonical.JSON(trimmed)
		if err != nil {
			return fmt.Errorf("provider: ledger line %d rejected: %w", index+1, err)
		}
		var envelope struct {
			FactType string `json:"factType"`
		}
		if err := json.Unmarshal(canonicalized, &envelope); err != nil {
			return fmt.Errorf("provider: ledger line %d decode: %w", index+1, err)
		}
		switch envelope.FactType {
		case factTypeRegistration:
			var fact registrationFact
			if err := json.Unmarshal(canonicalized, &fact); err != nil {
				return fmt.Errorf("provider: ledger line %d decode: %w", index+1, err)
			}
			if err := s.applyRegistration(fact.Registration); err != nil {
				return fmt.Errorf("provider: ledger line %d: %w", index+1, err)
			}
		case factTypeLifecycle:
			var fact lifecycleFact
			if err := json.Unmarshal(canonicalized, &fact); err != nil {
				return fmt.Errorf("provider: ledger line %d decode: %w", index+1, err)
			}
			if err := s.applyLifecycle(fact); err != nil {
				return fmt.Errorf("provider: ledger line %d: %w", index+1, err)
			}
		default:
			return fmt.Errorf("provider: ledger line %d carries unknown factType %q", index+1, envelope.FactType)
		}
	}
	return nil
}

// applyRegistration indexes one admitted registration fact. An exact
// duplicate fact (same registrationId and same canonical digest) is an
// idempotent replay of the ledger itself; any other collision fails closed.
func (s *RegistrationStore) applyRegistration(registration ProviderRegistration) error {
	if err := registration.Validate(); err != nil {
		return err
	}
	if registration.LifecycleState != LifecycleStateCreate && registration.LifecycleState != LifecycleStateActive {
		return fmt.Errorf("provider: admission requires lifecycleState create or active, got %q", string(registration.LifecycleState))
	}
	idempotencyDigest, err := registration.IdempotencyDigest()
	if err != nil {
		return err
	}
	if existingId, exists := s.byIdempotency[idempotencyDigest]; exists {
		existing := s.byId[existingId]
		if existingId == registration.RegistrationId && existing.RegistrationDigest == registration.RegistrationDigest {
			return nil
		}
		return fmt.Errorf("provider: idempotency conflict: identical idempotency identity bound to a different registration fact")
	}
	if _, exists := s.byId[registration.RegistrationId]; exists {
		return fmt.Errorf("provider: idempotency conflict: registrationId %q is already bound to a different idempotency identity", registration.RegistrationId)
	}
	s.byId[registration.RegistrationId] = registration
	s.byIdempotency[idempotencyDigest] = registration.RegistrationId
	return nil
}

// applyLifecycle applies one lifecycle transition fact; a transition must
// reference a known registration, start from its current state and end in a
// terminal state.
func (s *RegistrationStore) applyLifecycle(fact lifecycleFact) error {
	if err := fact.From.Validate(); err != nil {
		return err
	}
	if err := fact.To.Validate(); err != nil {
		return err
	}
	if !fact.To.IsTerminal() {
		return fmt.Errorf("provider: lifecycle transitions must end in revoked or expired, got %q", string(fact.To))
	}
	current, ok := s.currentState(fact.RegistrationId)
	if !ok {
		return fmt.Errorf("provider: lifecycle transition references unknown registrationId %q", fact.RegistrationId)
	}
	if current != fact.From {
		return fmt.Errorf("provider: lifecycle transition from %q does not match the current state %q", string(fact.From), string(current))
	}
	s.lifecycle[fact.RegistrationId] = fact.To
	return nil
}

// appendFact canonicalizes fact under RFC 8785 JCS and appends it as one
// ledger line; existing lines are never rewritten or removed. The ledger is
// appended before the in-memory index changes, so the durable facts always
// lead the projection.
func (s *RegistrationStore) appendFact(fact any) error {
	raw, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("provider: ledger marshal: %w", err)
	}
	line, err := canonical.JSON(raw)
	if err != nil {
		return fmt.Errorf("provider: ledger canonicalization: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.ledger.Write(line); err != nil {
		return fmt.Errorf("provider: ledger append: %w", err)
	}
	return nil
}
