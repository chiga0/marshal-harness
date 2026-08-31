package resultingress

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// AttachedRebindSession is the callback-scoped view of one authenticated
// read-only Attach transport extended with the single same-connection
// bind-authority(owner-successor) execute capability. *processsupervisor.
// AttachedSession satisfies this interface. The session is usable only inside
// the rebind callback; saving, repeating, or crossing goroutines fails closed.
type AttachedRebindSession interface {
	Observation() (processsupervisor.AttachObservation, error)
	ExecutePreparedBindAuthority(ctx context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error)
	ExecutePreparedCollect(ctx context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error)
	ExecutePreparedInspect(ctx context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error)
	ExecutePreparedClose(ctx context.Context, prepared processsupervisor.PreparedCommand) (processsupervisor.VerifiedCommandOutcome, error)
}

// validateRebindSupervisorIntentAgainstState is the narrower admission for the
// bind-authority(owner-successor) rebind intent. Unlike the initial-bind
// validator (which requires AuthorityHead==SupervisorStartedDigest and an empty
// SupervisorBoundAuthorityHead), the rebind requires the initial bind to be
// done, the PreCommand to be the current mechanics anchor, and the new
// AuthorityHead to be the exact control-owner-bound successor Attempt head.
func validateRebindSupervisorIntentAgainstState(state AttemptAuthorityState, intent SupervisorCommandIntent) error {
	if intent.Validate() != nil || intent.Command != processsupervisor.CommandBindAuthority || intent.SessionID != state.SupervisorStarted.Handshake.SessionID ||
		intent.Sequence != state.SupervisorCommandSequence+1 || intent.PreviousCommandHead != state.SupervisorCommandHead {
		return ErrAttemptAuthorityOrder
	}
	pre, prior := intent.PreCommand, state.SupervisorMechanicsAnchor
	if prior.Validate() != nil || pre != prior || pre.CurrentAuthorityHead != state.SupervisorMechanicsAuthorityHead {
		return ErrAttemptAuthorityConflict
	}
	if intent.CurrentAuthorityHead != pre.CurrentAuthorityHead {
		return ErrAttemptAuthorityConflict
	}
	rebuild := intent.Rebuild
	if rebuild.OwnerEpoch != pre.OwnerEpoch || rebuild.PreviousAuthorityHead != pre.CurrentAuthorityHead ||
		rebuild.AuthorityHead != state.HeadDigest || rebuild.AuthorityHead == pre.CurrentAuthorityHead ||
		rebuild.SupervisorStartedFactDigest != state.SupervisorStartedDigest {
		return ErrAttemptAuthorityConflict
	}
	for _, commandID := range state.SupervisorCommandIDs {
		if commandID == intent.CommandID {
			return ErrAttemptAuthorityConflict
		}
	}
	return nil
}
