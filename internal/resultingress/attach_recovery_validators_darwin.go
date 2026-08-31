//go:build darwin && arm64

package resultingress

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

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
