package bindingcheck

import (
	"errors"
	"fmt"
)

// AllocationState is the closed lifecycle enum for a sandbox allocation entry.
type AllocationState int

const (
	AllocationStateActive   AllocationState = iota // active: can transition to revoked/expired/replaced
	AllocationStateRevoked                         // terminal
	AllocationStateExpired                         // terminal
	AllocationStateReplaced                        // terminal
)

func (s AllocationState) isTerminal() bool {
	return s == AllocationStateRevoked || s == AllocationStateExpired || s == AllocationStateReplaced
}

// allocationEntry holds the ledger record for one allocationID.
type allocationEntry struct {
	allocationID                  string
	sandboxProviderRegistrationID string
	generation                    int
	state                         AllocationState
}

// SandboxLedger is a deterministic in-memory ledger of sandbox allocations.
type SandboxLedger struct {
	entries map[string]*allocationEntry
}

// NewSandboxLedger returns an empty SandboxLedger.
func NewSandboxLedger() *SandboxLedger {
	return &SandboxLedger{entries: make(map[string]*allocationEntry)}
}

// PutAllocation records an active allocation. Identical repeat calls are
// idempotent. Same allocationID with different content returns a conflict error.
func (l *SandboxLedger) PutAllocation(allocationID, sandboxProviderRegistrationID string, generation int) (*allocationEntry, error) {
	if allocationID == "" {
		return nil, errors.New("bindingcheck: allocationID must not be empty")
	}
	if sandboxProviderRegistrationID == "" {
		return nil, errors.New("bindingcheck: sandboxProviderRegistrationID must not be empty")
	}
	if generation <= 0 {
		return nil, fmt.Errorf("bindingcheck: generation must be positive, got %d", generation)
	}

	if existing, ok := l.entries[allocationID]; ok {
		// idempotent if same content
		if existing.sandboxProviderRegistrationID == sandboxProviderRegistrationID && existing.generation == generation {
			return existing, nil
		}
		return nil, fmt.Errorf("bindingcheck: allocationID %q already exists with different content (conflict)", allocationID)
	}

	e := &allocationEntry{
		allocationID:                  allocationID,
		sandboxProviderRegistrationID: sandboxProviderRegistrationID,
		generation:                    generation,
		state:                         AllocationStateActive,
	}
	l.entries[allocationID] = e
	return e, nil
}

// Revoke transitions an active allocation to the revoked terminal state.
func (l *SandboxLedger) Revoke(allocationID string) error {
	return l.transitionTerminal(allocationID, AllocationStateRevoked)
}

// Expire transitions an active allocation to the expired terminal state.
func (l *SandboxLedger) Expire(allocationID string) error {
	return l.transitionTerminal(allocationID, AllocationStateExpired)
}

// Replace transitions an active allocation to the replaced terminal state and
// records a new active entry with an incremented generation under the same
// allocationID key (generation = old + 1, same sandboxProviderRegistrationID).
func (l *SandboxLedger) Replace(allocationID string) (*allocationEntry, error) {
	if allocationID == "" {
		return nil, errors.New("bindingcheck: allocationID must not be empty")
	}
	e, ok := l.entries[allocationID]
	if !ok {
		return nil, fmt.Errorf("bindingcheck: allocationID %q not found", allocationID)
	}
	if e.state.isTerminal() {
		return nil, fmt.Errorf("bindingcheck: allocationID %q is in terminal state %v, cannot replace", allocationID, e.state)
	}

	// archive old entry under a synthetic key so Lookup still finds new
	archiveKey := fmt.Sprintf("%s#gen%d", allocationID, e.generation)
	archived := &allocationEntry{
		allocationID:                  e.allocationID,
		sandboxProviderRegistrationID: e.sandboxProviderRegistrationID,
		generation:                    e.generation,
		state:                         AllocationStateReplaced,
	}
	l.entries[archiveKey] = archived

	newEntry := &allocationEntry{
		allocationID:                  allocationID,
		sandboxProviderRegistrationID: e.sandboxProviderRegistrationID,
		generation:                    e.generation + 1,
		state:                         AllocationStateActive,
	}
	l.entries[allocationID] = newEntry
	return newEntry, nil
}

// Lookup returns the current entry for an allocationID, or nil if not found.
func (l *SandboxLedger) Lookup(allocationID string) *allocationEntry {
	return l.entries[allocationID]
}

func (l *SandboxLedger) transitionTerminal(allocationID string, target AllocationState) error {
	if allocationID == "" {
		return errors.New("bindingcheck: allocationID must not be empty")
	}
	e, ok := l.entries[allocationID]
	if !ok {
		return fmt.Errorf("bindingcheck: allocationID %q not found", allocationID)
	}
	if e.state.isTerminal() {
		return fmt.Errorf("bindingcheck: allocationID %q is in terminal state %v, cannot transition to %v", allocationID, e.state, target)
	}
	e.state = target
	return nil
}
