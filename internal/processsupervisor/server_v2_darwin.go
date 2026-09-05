//go:build darwin

package processsupervisor

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// The inherited fixed-image entry routes only an exact v2 bootstrap here.
// Core's production producer remains v1 until the complete S3 cutover gate;
// this path never imports, appends to or adopts an existing v1 session.
func runSupervisorLoopV2(ctx context.Context, connection *net.UnixConn, reader *bufio.Reader, directory *os.File, raw []byte, options supervisorLoopOptions) error {
	var bootstrap bootstrapRequestV2
	if strictCanonicalDecode(raw, &bootstrap) != nil || bootstrap.validate() != nil {
		return ErrInvalid
	}
	peerObserver, selfObserver := options.observePeer, options.observeSelf
	if peerObserver == nil {
		peerObserver = observePeer
	}
	if selfObserver == nil {
		selfObserver = observeSelfIdentity
	}
	peer, err := peerObserver(connection)
	if err != nil || !sameCoreIdentity(peer, bootstrap.Core) {
		return ErrConflict
	}
	_, initial, err := observeControlDirectory(directory)
	if err != nil || initial != bootstrap.ControlDirectoryIdentity || revalidateInitialControlDirectory(directory, initial) != nil {
		return ErrConflict
	}
	nonce, err := writeHeldOpenatExclusive(directory, nonceFileName, []byte(bootstrap.SessionNonce), 0600)
	if err != nil {
		return err
	}
	defer nonce.Close()
	journalFile, err := openatExclusive(directory, journalFileNameV2, 0600)
	if err != nil {
		return err
	}
	defer journalFile.Close()
	if directory.Sync() != nil {
		return ErrIntervention
	}
	nonceIdentity, nonceSize, err := observeControlFile(nonce)
	if err != nil || nonceSize != nonceBytes {
		return ErrConflict
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	if err != nil {
		return err
	}
	files := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	held := &heldSessionControlFiles{nonce: nonce, journal: journalFile, identity: files}
	if revalidateHeldSessionControlFilesForLeaf(directory, held, files, journalFileNameV2) != nil {
		return ErrConflict
	}
	journal, err := openJournalWriterV2(journalFile)
	if err != nil {
		return err
	}
	defer journal.close()
	mechanics := options.mechanics
	if mechanics == nil {
		mechanics, err = newDarwinMechanics(directory, protocolRevisionV2)
		if err != nil {
			return err
		}
	}
	session, err := newSessionV2(bootstrap, journal, mechanics, nil)
	if err != nil {
		return err
	}
	if options.configureSessionV2 != nil {
		options.configureSessionV2(session)
	}
	if revalidateControlDirectoryEntriesForLeaf(directory, initial, false, journalFileNameV2, controlDirectorySetupFiles) != nil {
		return ErrConflict
	}
	listener, err := listenUnixAt(directory, controlSocket)
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if unix.Fchmodat(int(directory.Fd()), controlSocket, 0600, 0) != nil || directory.Sync() != nil {
		return ErrIntervention
	}
	socket, err := observeControlSocket(directory)
	if err != nil {
		return err
	}
	_, finalDirectory, err := observeControlDirectory(directory)
	if err != nil || !sameControlDirectoryObject(finalDirectory, initial) {
		return ErrConflict
	}
	boundary := sessionControlBoundary{directory: directory, directoryIdentity: finalDirectory, socket: socket, heldFiles: held, controlFiles: files}
	self, err := selfObserver()
	if err != nil || boundary.revalidateV2(journal.recoverySnapshot("")) != nil {
		return ErrConflict
	}
	var active atomic.Bool
	active.Store(true)
	// v2's closed handshake schema has no rejected variant. Busy or invalid
	// peers get a bounded close, never a synthesized v1 or invalid v2 message.
	incoming, acceptErrors := acceptConnectionsWithRejection(ctx, listener, &active, nil)
	terminal, serveErr := serveAuthenticatedV2(ctx, connection, reader, session, boundary, self, reconnectResolutionV2{})
	releaseActiveConnection(&active, connection)
	if terminal || errors.Is(serveErr, ErrConflict) || errors.Is(serveErr, ErrIntervention) {
		return serveErr
	}
	if options.reconnectReady != nil {
		options.reconnectReady()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-acceptErrors:
			return ErrIntervention
		case next := <-incoming:
			if next == nil {
				return ErrIntervention
			}
			terminal, err := serveReconnectV2(ctx, next, session, boundary, self, peerObserver)
			releaseActiveConnection(&active, next)
			if terminal || errors.Is(err, ErrIntervention) {
				return err
			}
		}
	}
}

func handshakeV2(session *sessionV2, self CoreIdentity, boundary sessionControlBoundary, resolution reconnectResolutionV2) handshakeResponseV2 {
	session.core.mu.Lock()
	defer session.core.mu.Unlock()
	sequence, head, _ := session.journal.checkpoint()
	core := &session.core
	return handshakeResponseV2{SchemaVersion: handshakeSchemaV2, ProtocolRevision: protocolRevisionV2,
		LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: core.sessionID, SessionNonceDigest: core.nonceDigest,
		OwnerEpoch: core.ownerEpoch, CurrentAuthorityHead: core.authorityHead, CommandSequence: core.commandSequence, CommandHead: core.commandHead,
		JournalSequence: sequence, JournalHead: head, ObserverIdentity: observerIdentityV2, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		SupervisorProcess: self.Process, SupervisorBinary: self.Binary, ControlSocket: boundary.socket, ControlFiles: boundary.controlFiles,
		Reconciliation: resolution.State, ReplayedResponse: resolution.Response}
}

func serveReconnectV2(ctx context.Context, connection *net.UnixConn, session *sessionV2, boundary sessionControlBoundary, self CoreIdentity, observe func(*net.UnixConn) (CoreIdentity, error)) (bool, error) {
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if connection.SetDeadline(time.Now().Add(30*time.Second)) != nil {
		return false, ErrInvalid
	}
	peer, peerErr := observe(connection)
	reader := bufio.NewReaderSize(connection, MaxWireFrameBytes+frameHeaderBytes+1)
	raw, readErr := readFrame(reader, MaxWireFrameBytes)
	if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil {
		session.core.intervene()
		return false, ErrIntervention
	}
	var request reconnectRequestV2
	if peerErr != nil || readErr != nil || strictCanonicalDecode(raw, &request) != nil {
		return false, ErrInvalid
	}
	attempt := session.reconnectAttempt(request, peer)
	// Recheck even a rejected attempt. A malicious peer cannot mask a
	// concurrent boundary drift, and a post-effect failure cannot be retried.
	if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil || attempt.disposition == reconnectFailedAfterMechanics ||
		attempt.disposition != reconnectRejectedBeforeMechanics && attempt.err != nil {
		session.core.intervene()
		return false, ErrIntervention
	}
	if attempt.err != nil {
		return false, ErrInvalid
	}
	if attempt.disposition != reconnectResolvedWithoutMechanics && attempt.disposition != reconnectResolvedAfterMechanics {
		session.core.intervene()
		return false, ErrIntervention
	}
	return serveAuthenticatedV2(ctx, connection, reader, session, boundary, self, attempt.resolution)
}

func serveAuthenticatedV2(ctx context.Context, connection net.Conn, reader *bufio.Reader, session *sessionV2, boundary sessionControlBoundary, self CoreIdentity, resolution reconnectResolutionV2) (bool, error) {
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil {
		return false, ErrIntervention
	}
	handshake := handshakeV2(session, self, boundary, resolution)
	if handshake.validate() != nil {
		return false, ErrIntervention
	}
	if connection.SetDeadline(time.Now().Add(30*time.Second)) != nil {
		return false, ErrInvalid
	}
	if err := writeFrame(connection, handshake, MaxWireFrameBytes); err != nil {
		return false, err
	}
	for {
		state := session.core.State()
		if state == string(sessionClosed) || state == string(sessionAborted) {
			return true, nil
		}
		// This bounds a peer that leaves a partial frame. It is only a
		// transport idle limit; timeout never signals or cancels the workload.
		if connection.SetReadDeadline(time.Now().Add(30*time.Second)) != nil {
			return false, ErrInvalid
		}
		raw, err := readFrame(reader, MaxWireFrameBytes)
		if err != nil {
			return false, err
		}
		if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil {
			session.core.intervene()
			return false, ErrIntervention
		}
		response, commandErr := session.handle(raw)
		if boundary.revalidateV2(session.journal.recoverySnapshot("")) != nil || session.core.State() == string(sessionIntervention) {
			session.core.intervene()
			return false, ErrIntervention
		}
		if commandErr != nil {
			return false, ErrInvalid
		}
		if connection.SetWriteDeadline(time.Now().Add(30*time.Second)) != nil {
			return false, ErrInvalid
		}
		if err := writeFrame(connection, response, MaxWireFrameBytes); err != nil {
			return false, err
		}
	}
}
