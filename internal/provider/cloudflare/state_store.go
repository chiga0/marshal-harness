package cloudflare

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file implements the failure-atomic durable side-effect store of the
// Bridge provider. The official Bridge exposes no remote listing endpoint,
// so the provider must persist the side-effecting facts it needs to recover
// a create/restore after a crash: the durable intent written before a
// side-effecting create, the Bridge locator persisted immediately after the
// remote create succeeds, and the committed outcome that atomically installs
// the active allocation.
//
// Failure atomicity: every mutation runs on a staged deep copy and the live
// state is replaced only after the staged copy has been durably persisted. A
// failed write therefore never mutates the in-memory state, so a caller can
// retry or fail closed without half-applied bookkeeping.

var (
	// ErrStateStoreInconsistent rejects a committed outcome whose Bridge
	// locator disagrees with the locator previously persisted for the same
	// allocation, or an allocation without a persisted locator.
	ErrStateStoreInconsistent = errors.New("cloudflare state store: inconsistent outcome or allocation locator")
	// ErrStateStoreInvalid rejects a malformed record.
	ErrStateStoreInvalid = errors.New("cloudflare state store: invalid record")
)

// CreateIntent is the durable intent recorded before a side-effecting create
// (Provision or a replacement Restore). It is keyed by the idempotency key
// the create call carries, so a replay after a lost response re-issues the
// identical remote create.
type CreateIntent struct {
	ReplayKey    string `json:"replayKey"`
	AllocationId string `json:"allocationId"`
	RunId        string `json:"runId"`
	AttemptId    string `json:"attemptId"`
	Generation   int64  `json:"generation"`
}

// validate fails closed on any missing field.
func (intent CreateIntent) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"replayKey", intent.ReplayKey},
		{"allocationId", intent.AllocationId},
		{"runId", intent.RunId},
		{"attemptId", intent.AttemptId},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: intent.%s must be a non-empty string", ErrStateStoreInvalid, field.name)
		}
	}
	if intent.Generation < 1 {
		return fmt.Errorf("%w: intent.generation must be a positive integer", ErrStateStoreInvalid)
	}
	return nil
}

// CreateOutcome is the durable outcome committed once a create completes. It
// carries the allocation record to install as the active allocation and the
// Bridge locator the remote create returned.
type CreateOutcome struct {
	ReplayKey     string                    `json:"replayKey"`
	AllocationId  string                    `json:"allocationId"`
	BridgeLocator string                    `json:"bridgeLocator"`
	Meta          sandbox.SandboxAllocation `json:"meta"`
	Role          sandbox.WorkloadRole      `json:"role"`
}

// AllocationRecord is the durable view of one allocation.
type AllocationRecord struct {
	Meta          sandbox.SandboxAllocation `json:"meta"`
	Role          sandbox.WorkloadRole      `json:"role"`
	BridgeLocator string                    `json:"bridgeLocator"`
	SessionId     string                    `json:"sessionId,omitempty"`
}

// clone returns a deep copy so the staged mutation never aliases the live
// slice-backed fields of an allocation record.
func (record AllocationRecord) clone() AllocationRecord {
	record.Meta.AllowedStoreIds = append([]string(nil), record.Meta.AllowedStoreIds...)
	return record
}

// storeState is the serializable state of one store.
type storeState struct {
	Intents     map[string]CreateIntent     `json:"intents"`
	Locators    map[string]string           `json:"locators"`
	Allocations map[string]AllocationRecord `json:"allocations"`
}

func newStoreState() *storeState {
	return &storeState{
		Intents:     map[string]CreateIntent{},
		Locators:    map[string]string{},
		Allocations: map[string]AllocationRecord{},
	}
}

// clone returns a deep copy of the state; mutation runs on the copy only.
func (state *storeState) clone() *storeState {
	out := newStoreState()
	for key, intent := range state.Intents {
		out.Intents[key] = intent
	}
	for key, locator := range state.Locators {
		out.Locators[key] = locator
	}
	for key, record := range state.Allocations {
		out.Allocations[key] = record.clone()
	}
	return out
}

// FileStateStore is a file-durable, failure-atomic side-effect store. The
// zero value is not usable; construct it with NewFileStateStore (production)
// or newMemoryStateStore (tests).
type FileStateStore struct {
	mu    sync.Mutex
	path  string
	live  *storeState
	write func([]byte) error
}

// NewFileStateStore opens (or creates) the durable state file at path and
// loads any previously persisted state. This is the production constructor:
// every mutation is durably persisted through an atomic temp-file write plus
// fsync plus rename, and a failed write leaves the in-memory state untouched.
func NewFileStateStore(path string) (*FileStateStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: the state store path must be a non-empty string", ErrStateStoreInvalid)
	}
	store := &FileStateStore{path: path, live: newStoreState()}
	store.write = func(data []byte) error { return atomicWriteFile(path, data) }
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// newMemoryStateStore returns an ephemeral in-memory store for tests. It
// shares the identical staged-copy mutation discipline but never touches the
// file system, so it is never durable and must not be described as such.
func newMemoryStateStore() *FileStateStore {
	store := &FileStateStore{live: newStoreState()}
	store.write = func([]byte) error { return nil }
	return store
}

// load reads the persisted state from disk, if any. A missing file is an
// empty store; a malformed file fails closed.
func (store *FileStateStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrStateStoreInvalid, err)
	}
	if len(data) == 0 {
		return nil
	}
	var state storeState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrStateStoreInvalid, err)
	}
	if state.Intents == nil {
		state.Intents = map[string]CreateIntent{}
	}
	if state.Locators == nil {
		state.Locators = map[string]string{}
	}
	if state.Allocations == nil {
		state.Allocations = map[string]AllocationRecord{}
	}
	store.live = &state
	return nil
}

// mutate applies one change to a staged deep copy and swaps it in only after
// the staged copy is durably persisted. Any failure — validation, encoding or
// persistence — leaves the live state untouched.
func (store *FileStateStore) mutate(change func(*storeState) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	staged := store.live.clone()
	if err := change(staged); err != nil {
		return err
	}
	data, err := json.Marshal(staged)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrStateStoreInvalid, err)
	}
	if err := store.write(data); err != nil {
		return err
	}
	store.live = staged
	return nil
}

// RecordIntent persists the durable create intent. Recording the identical
// replay key again is idempotent.
func (store *FileStateStore) RecordIntent(intent CreateIntent) error {
	if err := intent.validate(); err != nil {
		return err
	}
	return store.mutate(func(state *storeState) error {
		if existing, ok := state.Intents[intent.ReplayKey]; ok {
			if existing != intent {
				return fmt.Errorf("%w: replay key %q already carries a different intent", ErrStateStoreInconsistent, intent.ReplayKey)
			}
			return nil
		}
		state.Intents[intent.ReplayKey] = intent
		return nil
	})
}

// ClearIntent removes a durable intent after a definitive (non-ambiguous)
// resolution, such as a refused create.
func (store *FileStateStore) ClearIntent(replayKey string) error {
	return store.mutate(func(state *storeState) error {
		delete(state.Intents, replayKey)
		return nil
	})
}

// Intent returns the durable intent for one replay key, if recorded.
func (store *FileStateStore) Intent(replayKey string) (CreateIntent, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	intent, ok := store.live.Intents[replayKey]
	return intent, ok
}

// PendingIntents returns every intent whose outcome has not been committed.
// A pending intent is the durable trace of a create whose response was lost
// or whose outcome never completed: the remote side effect may or may not
// exist, so reconcile must treat it as ambiguity and never report clean.
func (store *FileStateStore) PendingIntents() []CreateIntent {
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]CreateIntent, 0, len(store.live.Intents))
	for _, intent := range store.live.Intents {
		out = append(out, intent)
	}
	return out
}

// RecordBridgeLocator persists the Bridge locator returned by a successful
// remote create, keyed by the Marshal allocation id. A different locator for
// the same allocation is an inconsistency and is rejected.
func (store *FileStateStore) RecordBridgeLocator(allocationId, bridgeLocator string) error {
	if strings.TrimSpace(allocationId) == "" || strings.TrimSpace(bridgeLocator) == "" {
		return fmt.Errorf("%w: the allocation id and bridge locator must be non-empty strings", ErrStateStoreInvalid)
	}
	return store.mutate(func(state *storeState) error {
		if existing, ok := state.Locators[allocationId]; ok && existing != bridgeLocator {
			return fmt.Errorf("%w: allocation %q already carries bridge locator %q", ErrStateStoreInconsistent, allocationId, existing)
		}
		state.Locators[allocationId] = bridgeLocator
		return nil
	})
}

// BridgeLocator returns the persisted Bridge locator of one allocation.
func (store *FileStateStore) BridgeLocator(allocationId string) (string, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	locator, ok := store.live.Locators[allocationId]
	return locator, ok
}

// CommitCreateOutcome atomically installs the active allocation carried by
// the outcome. It rejects an outcome whose allocation carries no persisted
// Bridge locator or whose Bridge locator disagrees with the persisted one,
// and resolves any matching intent. The committed record and the intent
// cleanup are one atomic mutation.
func (store *FileStateStore) CommitCreateOutcome(outcome CreateOutcome) error {
	if strings.TrimSpace(outcome.ReplayKey) == "" || strings.TrimSpace(outcome.AllocationId) == "" || strings.TrimSpace(outcome.BridgeLocator) == "" {
		return fmt.Errorf("%w: the outcome replay key, allocation id and bridge locator must be non-empty strings", ErrStateStoreInvalid)
	}
	if err := outcome.Meta.Validate(); err != nil {
		return fmt.Errorf("%w: outcome allocation: %v", ErrStateStoreInvalid, err)
	}
	if err := outcome.Role.Validate(); err != nil {
		return fmt.Errorf("%w: outcome role: %v", ErrStateStoreInvalid, err)
	}
	return store.mutate(func(state *storeState) error {
		locator, ok := state.Locators[outcome.AllocationId]
		if !ok {
			return fmt.Errorf("%w: allocation %q has no persisted bridge locator", ErrStateStoreInconsistent, outcome.AllocationId)
		}
		if locator != outcome.BridgeLocator {
			return fmt.Errorf("%w: outcome bridge locator %q disagrees with the persisted locator %q for allocation %q", ErrStateStoreInconsistent, outcome.BridgeLocator, locator, outcome.AllocationId)
		}
		if intent, ok := state.Intents[outcome.ReplayKey]; ok && intent.AllocationId != outcome.AllocationId {
			return fmt.Errorf("%w: outcome allocation %q disagrees with the intent allocation %q", ErrStateStoreInconsistent, outcome.AllocationId, intent.AllocationId)
		}
		delete(state.Intents, outcome.ReplayKey)
		record := AllocationRecord{
			Meta:          outcome.Meta,
			Role:          outcome.Role,
			BridgeLocator: outcome.BridgeLocator,
		}
		state.Allocations[outcome.AllocationId] = record.clone()
		return nil
	})
}

// UpdateAllocation durably upserts one allocation record without the locator
// consistency check, for non-create state transitions (terminate, an in-place
// restore generation bump).
func (store *FileStateStore) UpdateAllocation(record AllocationRecord) error {
	if strings.TrimSpace(record.Meta.AllocationId) == "" {
		return fmt.Errorf("%w: the allocation record must carry a non-empty allocation id", ErrStateStoreInvalid)
	}
	if err := record.Meta.Validate(); err != nil {
		return fmt.Errorf("%w: allocation record: %v", ErrStateStoreInvalid, err)
	}
	if err := record.Role.Validate(); err != nil {
		return fmt.Errorf("%w: allocation record role: %v", ErrStateStoreInvalid, err)
	}
	return store.mutate(func(state *storeState) error {
		state.Allocations[record.Meta.AllocationId] = record.clone()
		return nil
	})
}

// Allocation returns the durable record of one allocation.
func (store *FileStateStore) Allocation(allocationId string) (AllocationRecord, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.live.Allocations[allocationId]
	return record.clone(), ok
}

// Allocations returns every committed allocation record.
func (store *FileStateStore) Allocations() []AllocationRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]AllocationRecord, 0, len(store.live.Allocations))
	for _, record := range store.live.Allocations {
		out = append(out, record.clone())
	}
	return out
}

// atomicWriteFile durably replaces the file at path with data: write to a
// temp file in the same directory, fsync, then rename over the target. A
// failed write never leaves a partially-written target behind.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".marshal-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
