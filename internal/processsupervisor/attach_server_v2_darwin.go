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
	if !session.matchesAttachCheckpointLocked(a) {
		return ErrConflict
	}
	observer, ok := session.core.mechanics.(attachChildObserver)
	if !ok {
		return ErrConflict
	}
	child, err := observer.attachChildIdentity()
	if err != nil || child != a.Child {
		return ErrConflict
	}
	return nil
}

func serveAttachV2(connection *net.UnixConn, reader *bufio.Reader, session *sessionV2, boundary sessionControlBoundary, self, peer CoreIdentity, raw []byte) error {
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
	// EOF is a read-only Attach. Otherwise the same authenticated connection
	// admits at most one command in the closed continuation set, never the
	// generic command loop. Core must persist its preparation before sending.
	if connection.SetReadDeadline(time.Now().Add(handshakeTimeout)) != nil {
		return ErrConflict
	}
	frame, err := readFrame(reader, MaxWireFrameBytes)
	if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil {
		return ErrIntervention
	}
	if validateAttachServerV2(session, boundary, self, request) != nil {
		return ErrConflict
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return ErrConflict
	}
	result, err := session.handleAttachContinuation(frame, request.Authority)
	if err != nil {
		return err
	}
	post := session.journal.recoverySnapshot("")
	if post.pending != nil || post.sequence != request.Authority.PreviousSupervisor.Binding.JournalSequence+2 ||
		post.commandSeq != result.Sequence || post.commandHead != result.CommandHead || boundary.revalidateV2(post) != nil {
		return ErrIntervention
	}
	if connection.SetDeadline(time.Now().Add(handshakeTimeout)) != nil {
		return ErrConflict
	}
	if writeFrame(connection, result, MaxWireFrameBytes) != nil {
		return ErrConflict
	}
	var unexpected [1]byte
	if count, readErr := reader.Read(unexpected[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		return ErrConflict
	}
	final := session.journal.recoverySnapshot("")
	if final.pending != nil || final.sequence != post.sequence || final.head != post.head || boundary.revalidateV2(final) != nil {
		return ErrIntervention
	}
	return nil
}
