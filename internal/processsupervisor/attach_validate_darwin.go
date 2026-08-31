//go:build darwin

package processsupervisor

import "time"

// validateAttachJournalAnchor verifies that a held journal snapshot exactly
// matches the previous Supervisor anchor: journal sequence/head, current owner
// epoch/authority head, no pending command, and the reconstructed command
// sequence/head. It is pure read-only validation used by both sides of Attach.
func validateAttachJournalAnchor(snapshot JournalSnapshot, anchor HandshakeAnchor) error {
	if snapshot.Sequence != anchor.JournalSequence || snapshot.Head != anchor.JournalHead || snapshot.currentOwnerEpoch != anchor.OwnerEpoch || snapshot.currentAuthorityHead != anchor.CurrentAuthorityHead || snapshot.pending != nil {
		return ErrConflict
	}
	sequence, head := uint64(0), CommandGenesisDigest
	for _, command := range snapshot.commands {
		if command.Projection.Sequence > sequence {
			sequence, head = command.Projection.Sequence, command.Response.CommandHead
		}
	}
	if sequence != anchor.CommandSequence || head != anchor.CommandHead {
		return ErrConflict
	}
	return nil
}

func (response attachResponse) validate(request attachRequest, observed CoreIdentity) error {
	observedAt, err := time.Parse(time.RFC3339Nano, response.ObservedAt)
	if response.SchemaVersion != AttachObservationSchema || response.ProtocolRevision != ProtocolRevision || response.Status != "ok" ||
		response.ReasonCode != "process-supervisor-attached" || response.RequestDigest != request.RequestDigest ||
		ValidateHandshakeBinding(response.Handshake, request.Authority.PreviousSupervisor, observed) != nil ||
		response.Handshake.SupervisorProcess != request.Authority.Supervisor ||
		response.CurrentAcquisition != request.Authority.CurrentAcquisition || response.CurrentOwnerBoundFact != request.Authority.CurrentOwnerBoundFact ||
		response.Child != request.Authority.Child || response.ChildObservationDigest != request.Authority.ChildObservationDigest ||
		response.ObserverIdentity != attachObserverIdentityV1 || err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != response.ObservedAt ||
		!validDigest(response.ResponseDigest) {
		return ErrConflict
	}
	digest, digestErr := response.detachedDigest()
	if digestErr != nil || digest != response.ResponseDigest {
		return ErrConflict
	}
	return nil
}
