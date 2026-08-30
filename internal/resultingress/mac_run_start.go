package resultingress

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// PrepareMacRunStart projects an already-created ResultIngress
// PreparedExecution into the narrow Runstore-facing start contract. It does
// not create an Attempt, acquire an owner, provision an allocation, or mint a
// proof. Those facts must already exist in this same durable ledger and are
// re-resolved under the current owner lock before the projection is returned.
//
// The returned application value is therefore a sealed, secret-safe handle;
// callers must use CommitMacRunStart for the matching proof-producing commit.
// In particular, callers cannot construct a PreparedRunStart from identities
// or digests supplied outside ResultIngress.
func (s *DurableStore) PrepareMacRunStart(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, preparationDigest string) (application.PreparedRunStart, error) {
	prepared, err := s.ResolvePreparedExecution(ctx, verifier, acquisition, preparationDigest)
	if err != nil {
		return application.PreparedRunStart{}, err
	}
	start := application.PreparedRunStart{
		ProtocolRevision:        application.PreparedRunStartProtocolRevision,
		TaskID:                  prepared.AttemptIdentity.TaskID,
		RunID:                   prepared.AttemptIdentity.RunID,
		AttemptID:               prepared.AttemptIdentity.AttemptID,
		ReservationFactDigest:   prepared.ReservationFactDigest,
		AttemptOpenedFactDigest: prepared.AttemptOpenedFactDigest,
		AttemptOrdinal:          prepared.AttemptOrdinal,
		AttemptsUsedBefore:      prepared.AttemptsUsedBefore,
		MaxAttempts:             prepared.MaxAttempts,
		State:                   domain.StateReady,
		Sequence:                prepared.ExpectedRunSequence,
		AuthorityHead:           prepared.ExpectedRunAuthorityHead,
		PreparationDigest:       prepared.PreparationDigest,
	}
	if err := start.Validate(); err != nil {
		return application.PreparedRunStart{}, ErrPreparedExecutionConflict
	}
	return start, nil
}

// CommitMacRunStart is the matching proof-producing continuation for
// PrepareMacRunStart. It resolves the preparation again under the current
// owner lock, compares every field that crosses the Runstore boundary, and
// delegates proof minting to StartPreparedExecution. The proof constructor
// remains private to this package, so callers cannot forge process-started or
// resume provenance.
func (s *DurableStore) CommitMacRunStart(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, prepared application.PreparedRunStart, projector RunStartProjector) error {
	if ctx == nil || projector == nil || prepared.Validate() != nil {
		return ErrPreparedExecutionConflict
	}
	resolved, err := s.ResolvePreparedExecution(ctx, verifier, acquisition, prepared.PreparationDigest)
	if err != nil {
		return err
	}
	if resolved.AttemptIdentity.TaskID != prepared.TaskID || resolved.AttemptIdentity.RunID != prepared.RunID || resolved.AttemptIdentity.AttemptID != prepared.AttemptID ||
		resolved.ReservationFactDigest != prepared.ReservationFactDigest || resolved.AttemptOpenedFactDigest != prepared.AttemptOpenedFactDigest ||
		resolved.AttemptOrdinal != prepared.AttemptOrdinal || resolved.AttemptsUsedBefore != prepared.AttemptsUsedBefore || resolved.MaxAttempts != prepared.MaxAttempts ||
		resolved.ExpectedRunSequence != prepared.Sequence || resolved.ExpectedRunAuthorityHead != prepared.AuthorityHead || resolved.PreparationDigest != prepared.PreparationDigest {
		return ErrPreparedExecutionConflict
	}
	return s.StartPreparedExecution(ctx, verifier, acquisition, resolved.AttemptIdentity, resolved.PreparationDigest, projector)
}
