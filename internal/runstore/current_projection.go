package runstore

import (
	"github.com/chiga0/marshal-harness/internal/application"
)

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
