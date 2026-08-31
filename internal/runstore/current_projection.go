package runstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// ReconcileSnapshotUnderLease deterministically repairs a snapshot that
// trails an already-fsynced Run journal. The exact held Run lease and the
// per-lease mutation lock exclude concurrent writers while inspectAt replays
// the journal tail and writeSnapshotAt persists that projection. Repeating the
// operation is byte-stable and does not append another authority event.
func (s *Store) ReconcileSnapshotUnderLease(ctx context.Context, lease *Lease) (domain.RunState, error) {
	if s == nil || ctx == nil || !leaseOwnerMatches(lease) || lease.root != s.root {
		return domain.RunState{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return domain.RunState{}, err
	}
	lease.guard.mu.RLock()
	defer lease.guard.mu.RUnlock()
	if !leaseHeldBySelfLocked(lease) || lease.guard.preparedBorrowed.Load() {
		return domain.RunState{}, ErrConflict
	}
	lease.guard.mutation.Lock()
	defer lease.guard.mutation.Unlock()
	authority, err := openRunAuthorityLocked(lease)
	if err != nil {
		return domain.RunState{}, err
	}
	defer authority.Close()
	state, err := inspectAt(int(authority.Fd()))
	if err != nil || state.RunID != lease.runID {
		return domain.RunState{}, fmt.Errorf("%w: reconcile Run snapshot", ErrConflict)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return domain.RunState{}, err
	}
	if lease.beforeMutation != nil {
		if err := lease.beforeMutation(); err != nil {
			return domain.RunState{}, fmt.Errorf("snapshot reconcile mutation hook: %w", err)
		}
	}
	if err := writeSnapshotAt(int(authority.Fd()), append(data, '\n')); err != nil {
		return domain.RunState{}, err
	}
	return state, nil
}

// ReadCurrentRunProjectionUnderLease returns the exact current journal head
// for any Run state while the caller holds the canonical Run lease.
func (s *Store) ReadCurrentRunProjectionUnderLease(lease *Lease) (application.RunProjection, error) {
	if s == nil || !leaseOwnerMatches(lease) || lease.root != s.root {
		return application.RunProjection{}, ErrConflict
	}
	lease.guard.mu.RLock()
	defer lease.guard.mu.RUnlock()
	if !leaseHeldBySelfLocked(lease) || lease.guard.preparedBorrowed.Load() {
		return application.RunProjection{}, ErrConflict
	}
	records, err := strictRunJournalAt(int(lease.runDir.Fd()))
	if err != nil || len(records) == 0 {
		return application.RunProjection{}, ErrConflict
	}
	state, err := inspectAt(int(lease.runDir.Fd()))
	if err != nil || state.RunID != lease.runID || state.Sequence != uint64(len(records)) {
		return application.RunProjection{}, ErrConflict
	}
	projection := application.RunProjection{
		TaskID: state.TaskID, RunID: state.RunID, AttemptID: state.CurrentAttemptID,
		State: state.State, Sequence: state.Sequence, AuthorityHead: records[len(records)-1].digest,
	}
	if projection.Validate() != nil {
		return application.RunProjection{}, ErrConflict
	}
	return projection, nil
}
