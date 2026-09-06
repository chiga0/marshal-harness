package runstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// RunBusinessBudgetProjection is read-only: the frozen TaskSpec and first
// planning event are its sources, never a fresh timer or snapshot alone.
type RunBusinessBudgetProjection struct {
	Run                   application.RunProjection
	SpecDigest            string
	CreationEventDigest   string
	CreatedAt             time.Time
	RunTimeoutSeconds     int64
	AttemptTimeoutSeconds int64
}

// ReadBusinessBudgetUnderLease reads all Run-side deadline sources under the
// same held guard. It does not choose an Attempt or grant stop authority.
func (s *Store) ReadBusinessBudgetUnderLease(ctx context.Context, lease *Lease) (RunBusinessBudgetProjection, error) {
	if s == nil || ctx == nil || !leaseOwnerMatches(lease) || ctx.Err() != nil || lease.guard.preparedBorrowed.Load() {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	lease.guard.mu.RLock()
	defer lease.guard.mu.RUnlock()
	if !leaseHeldBySelfLocked(lease) || lease.root != s.root || lease.guard.preparedBorrowed.Load() || ctx.Err() != nil {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	authority, err := openRunAuthorityLocked(lease)
	if err != nil {
		return RunBusinessBudgetProjection{}, err
	}
	defer authority.Close()
	projection, err := s.readRunStartAuthorityLocked(lease)
	if err != nil || projection.Run.State != domain.StateReady && projection.Run.State != domain.StateRunning {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	fd := int(authority.Fd())
	records, err := strictRunJournalAt(fd)
	if err != nil || len(records) == 0 {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	state, err := inspectAt(fd)
	if err != nil {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	first := records[0]
	spec, ok := first.event.Payload["specDigest"].(string)
	if first.event.Sequence != 1 || first.event.RunID != projection.Run.RunID || first.event.Type != "planning.spec-accepted" || first.event.StateFrom != domain.StateCreated || first.event.StateTo != domain.StatePlanned || first.event.Timestamp.IsZero() || !state.CreatedAt.Equal(first.event.Timestamp) || !ok || spec != projection.SpecDigest {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	raw, err := readRegularAt(fd, "task-spec.json", 2<<20)
	if err != nil {
		return RunBusinessBudgetProjection{}, err
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil || digest != projection.SpecDigest {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	var task domain.TaskSpec
	if json.Unmarshal(raw, &task) != nil || task.Kind != domain.KindTask || task.Metadata.ID != projection.Run.TaskID || task.Budgets.RunTimeoutSeconds <= 0 || task.Budgets.AttemptTimeoutSeconds <= 0 {
		return RunBusinessBudgetProjection{}, ErrConflict
	}
	return RunBusinessBudgetProjection{
		Run: projection.Run, SpecDigest: digest, CreationEventDigest: first.digest,
		CreatedAt: first.event.Timestamp.UTC(), RunTimeoutSeconds: task.Budgets.RunTimeoutSeconds, AttemptTimeoutSeconds: task.Budgets.AttemptTimeoutSeconds,
	}, nil
}
