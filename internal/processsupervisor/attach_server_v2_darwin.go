//go:build darwin

package processsupervisor

import (
	"bufio"
	"errors"
	"io"
	"net"
	"time"
)

// This admission is read-only. It must never call reconnectAttempt, whose
// owner/head changes are not the ADR 0067 Attach -> durable bind sequence.
func validateAttachServerV2(session *sessionV2, boundary sessionControlBoundary, self CoreIdentity, request attachRequestV2) error {
	a := request.Authority
	p := a.PreviousSupervisor.Binding
	if request.validate() != nil || session == nil || !sameControlDirectoryObject(a.PreviousSupervisor.ControlDirectory, boundary.directoryIdentity) ||
		p.ControlSocket != boundary.socket || p.ControlFiles != boundary.controlFiles || a.Supervisor != self.Process ||
		!sameBinaryObject(p.FixedBinary, self.Binary) || self.UID != request.Core.UID || self.GID != request.Core.GID {
		return ErrConflict
	}
	session.core.mu.Lock()
	defer session.core.mu.Unlock()
	c := &session.core
	j := session.journal.recoverySnapshot("")
	if c.state != sessionBound || c.sessionID != p.SessionID || c.nonceDigest != p.SessionNonceDigest || c.authority != p.Authority ||
		c.ownerEpoch != p.OwnerEpoch || c.authorityHead != p.CurrentAuthorityHead || c.commandSequence != p.CommandSequence || c.commandHead != p.CommandHead ||
		c.lastObservation != a.ChildObservationDigest || j.sequence != p.JournalSequence || j.head != p.JournalHead || j.pending != nil ||
		j.commandSeq != p.CommandSequence || j.commandHead != p.CommandHead || j.ownerEpoch != p.OwnerEpoch || j.authorityHead != p.CurrentAuthorityHead {
		return ErrConflict
	}
	observer, ok := c.mechanics.(attachChildObserver)
	if !ok {
		return ErrConflict
	}
	child, err := observer.attachChildIdentity()
	if err != nil || child != a.Child {
		return ErrConflict
	}
	return nil
}

func serveReadOnlyAttachV2(connection *net.UnixConn, reader *bufio.Reader, session *sessionV2, boundary sessionControlBoundary, self, peer CoreIdentity, raw []byte) error {
	var request attachRequestV2
	if strictCanonicalDecode(raw, &request) != nil || !sameCoreIdentity(request.Core, peer) || validateAttachServerV2(session, boundary, self, request) != nil {
		return ErrConflict
	}
	response := attachResponseV2{SchemaVersion: AttachObservationSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, Status: "ok", ReasonCode: "process-supervisor-attached", RequestDigest: request.RequestDigest,
		Handshake: handshakeV2(session, self, boundary, reconnectResolutionV2{}), Authority: request.Authority, ObserverIdentity: observerIdentityV2,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	var err error
	response.ResponseDigest, err = response.detachedDigest()
	if err != nil || response.validate(request, self) != nil {
		return ErrConflict
	}
	if writeFrame(connection, response, MaxWireFrameBytes) != nil {
		return ErrConflict
	}
	// Only the read-only exchange is admitted at this integration stage.
	// A continuation needs the callback-scoped exact prepared-command gate;
	// it must not fall through to the generic authenticated command loop.
	_, err = readFrame(reader, MaxWireFrameBytes)
	if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil {
		return ErrIntervention
	}
	if validateAttachServerV2(session, boundary, self, request) != nil {
		return ErrConflict
	}
	if !errors.Is(err, io.EOF) {
		return ErrConflict
	}
	return nil
}
