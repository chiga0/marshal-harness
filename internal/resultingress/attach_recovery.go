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

// rebindTransport performs the read-only Attach and, within the same borrowed
// callback, the same-connection bind-authority(owner-successor) execute. The
// transport must keep the repository owner held for the complete callback and
// must not reopen a second connection, rebuild the child, or escape the session
// across callbacks. On darwin/arm64 the production implementation calls
// processsupervisor.WithAttached; tests substitute a fake via the unexported
// rebindOwnerSuccessorForAttachedRecoveryWithTransport seam.
type rebindTransport func(ctx context.Context, options processsupervisor.AttachOptions, rebind func(AttachedRebindSession) error) error

// validateRebindObservation proves the authenticated Attach observation binds
// the exact durable authority the orchestrator built: the previous Supervisor
// anchor, the live supervisor, the current acquisition, the control-owner-bound
// successor, the child and its observation. The full cryptographic validation
// (request/response digests, peer identity, handshake binding) is the Attach
// transport's responsibility; the orchestrator only rechecks that the returned
// observation matches the durable authority it constructed. A live Supervisor
// that drifted from durable authority fails closed before any bind intent is
// persisted.
func validateRebindObservation(observation processsupervisor.AttachObservation, authority processsupervisor.AttachAuthority) error {
	if observation.PreviousSupervisor != authority.PreviousSupervisor || observation.Supervisor != authority.Supervisor ||
		observation.CurrentAcquisition != authority.CurrentAcquisition || observation.CurrentOwnerBoundFact != authority.CurrentOwnerBoundFact ||
		observation.Child != authority.Child || observation.ChildObservationDigest != authority.ChildObservationDigest {
		return ErrPreparedExecutionConflict
	}
	return nil
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

// validateRebindPendingIntentForReplay checks that a durable pending
// bind-authority(owner-successor) intent belongs to the exact same owner and
// pre-anchor that the current recovery holds, so a same-owner transport-loss
// replay (ADR 0067 §5 row 2) can safely re-execute the identical prepared
// command and append the outcome. A pending intent from a different owner,
// different pre-anchor, different command ID, or different request digest is a
// conflict, not a replay.
func validateRebindPendingIntentForReplay(state AttemptAuthorityState, acquisition ControlOwnerAcquisition, preAnchor processsupervisor.HandshakeAnchor) error {
	intent := state.SupervisorPendingIntent
	if intent.Command != processsupervisor.CommandBindAuthority || intent.SessionID != state.SupervisorStarted.Handshake.SessionID ||
		intent.PreCommand != state.SupervisorMechanicsAnchor || supervisorHandshakeAnchor(intent.PreCommand) != preAnchor ||
		acquisition.OwnerEpoch != state.Owner.OwnerEpoch || intent.Rebuild.OwnerEpoch != intent.PreCommand.OwnerEpoch ||
		intent.Rebuild.AuthorityHead != state.HeadDigest || intent.Rebuild.AuthorityHead == intent.PreCommand.CurrentAuthorityHead {
		return ErrAttemptAuthorityConflict
	}
	return nil
}
