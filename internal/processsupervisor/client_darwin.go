//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const handshakeTimeout = 30 * time.Second

// ObserveCurrentCore returns the kernel-adjacent identity that must be bound by
// the caller's current durable control-owner-acquired fact. fixedMarshalPath is
// exact: aliases, PATH lookup and a different Marshal image are rejected.
func ObserveCurrentCore(fixedMarshalPath string) (CoreIdentity, error) {
	if !absoluteClean(fixedMarshalPath) {
		return CoreIdentity{}, ErrInvalid
	}
	identity, err := observeSelfIdentity()
	if err != nil || identity.Binary.CanonicalPath != fixedMarshalPath {
		return CoreIdentity{}, ErrConflict
	}
	return identity, nil
}

// ObserveHeldControlDirectory returns the exact descriptor and pathname
// identity used in authority. Callers retain ownership of file.
func ObserveHeldControlDirectory(file *os.File) (ControlDirectoryIdentity, error) {
	_, identity, err := observeControlDirectory(file)
	return identity, err
}

// ObserveHeldControlSocket observes the rendezvous descriptor-relatively. It
// does not search the filesystem or adopt an unknown socket.
func ObserveHeldControlSocket(directory *os.File) (ControlSocketIdentity, error) {
	return observeControlSocket(directory)
}

// Start launches only the current fixed Marshal image with inherited bootstrap
// and control-directory descriptors and an empty environment. The returned
// handshake is fully bound before any caller may persist supervisor authority.

func Start(ctx context.Context, options StartOptions) (*Client, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrIntervention
	}
	if options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.Bootstrap.validate() != nil {
		return nil, ErrInvalid
	}
	core, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil || !sameCoreIdentity(options.Bootstrap.Core, core) {
		return nil, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || directory != options.Bootstrap.ControlDirectoryIdentity || revalidateInitialControlDirectory(options.ControlDirectory, directory) != nil {
		return nil, ErrConflict
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	parentFile := os.NewFile(uintptr(fds[0]), "marshal-supervisor-bootstrap-core")
	childFile := os.NewFile(uintptr(fds[1]), "marshal-supervisor-bootstrap-child")
	if parentFile == nil || childFile == nil {
		closeFiles(parentFile, childFile)
		return nil, ErrUnavailable
	}
	parentConnection, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = childFile.Close()
		return nil, ErrUnavailable
	}
	connection, ok := parentConnection.(*net.UnixConn)
	if !ok {
		_ = parentConnection.Close()
		_ = childFile.Close()
		return nil, ErrUnavailable
	}
	command := exec.Command(options.FixedMarshalPath, "internal", "process-supervisor")
	command.Env = []string{}
	command.ExtraFiles = []*os.File{childFile, options.ControlDirectory}
	if err := command.Start(); err != nil {
		_ = connection.Close()
		_ = childFile.Close()
		return nil, ErrUnavailable
	}
	_ = childFile.Close()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = connection.Close()
			abortStartedSupervisor(command)
		}
	}()
	codec, err := NewProtocolCodec(connection)
	if err != nil {
		return nil, err
	}
	var handshake HandshakeResponse
	var anchor HandshakeAnchor
	var peer CoreIdentity
	var finalDirectory ControlDirectoryIdentity
	err = runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
		if err := codec.Write(options.Bootstrap); err != nil {
			return ErrIntervention
		}
		if err := codec.Read(&handshake); err != nil {
			return ErrIntervention
		}
		if handshake.ControlFiles.validate() != nil {
			return ErrConflict
		}
		heldFiles, err := openHeldSessionControlFiles(options.ControlDirectory, handshake.ControlFiles)
		if err != nil {
			return ErrConflict
		}
		defer heldFiles.close()
		nonce, err := readSessionNonce(heldFiles, canonical.DigestBytes([]byte(options.Bootstrap.SessionNonce)))
		if err != nil || nonce != options.Bootstrap.SessionNonce {
			return ErrConflict
		}
		socket, err := ObserveHeldControlSocket(options.ControlDirectory)
		if err != nil || revalidateControlDirectoryEntries(options.ControlDirectory, directory, false, controlDirectoryRuntimeBase) != nil || observeControlSocketExact(options.ControlDirectory, socket) != nil {
			return ErrConflict
		}
		peer, err = ObserveFixedMarshalPeer(connection)
		if err != nil || command.Process == nil || peer.Process.PID != command.Process.Pid {
			return ErrConflict
		}
		journalHead, err := initialJournalHead(options.Bootstrap)
		if err != nil {
			return ErrIntervention
		}
		anchor = HandshakeAnchor{
			SessionID: options.Bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(options.Bootstrap.SessionNonce)), Authority: options.Bootstrap.Authority,
			OwnerEpoch: options.Bootstrap.OwnerEpoch, CurrentAuthorityHead: options.Bootstrap.CurrentAuthorityHead,
			CommandSequence: 0, CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: journalHead,
			UID: core.UID, GID: core.GID, FixedBinary: core.Binary, ControlSocket: socket, ControlFiles: handshake.ControlFiles,
		}
		if ValidateHandshakeBinding(handshake, anchor, peer) != nil || revalidateHeldSessionControlFiles(options.ControlDirectory, heldFiles, handshake.ControlFiles) != nil {
			return ErrConflict
		}
		finalDirectory, err = ObserveHeldControlDirectory(options.ControlDirectory)
		if err != nil || !sameControlDirectoryObject(finalDirectory, directory) || revalidateControlDirectoryEntries(options.ControlDirectory, finalDirectory, false, controlDirectoryRuntimeBase) != nil {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	evidence := ConnectionEvidence{Core: core, ControlDirectory: finalDirectory, Handshake: handshake, Anchor: anchor}
	client, err := newClient(connection, evidence, peer)
	if err != nil {
		return nil, err
	}
	succeeded = true
	// Retain and reap the exact child without coupling its lifetime to the Core
	// connection. Disconnect closes only connection; this goroutine never kills.
	go func() { _ = command.Wait() }()
	return client, nil
}

// Reconnect uses only caller-supplied durable anchors and the held directory.
// The socket pathname is a locator; descriptor identities, peer credentials,
// nonce, binary and protocol observations provide authority binding.
func Reconnect(ctx context.Context, options ReconnectOptions) (*Client, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrIntervention
	}
	plan, previous := options.Plan, options.Anchor
	if options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.ControlDirectoryIdentity.validate() != nil || previous.ControlFiles.validate() != nil || plan.PreviousOwnerEpoch != previous.OwnerEpoch || plan.OwnerEpoch <= plan.PreviousOwnerEpoch || plan.OwnerEpoch > maxSafeJSONInteger || plan.PreviousAuthorityHead != previous.CurrentAuthorityHead || !validDigest(plan.CurrentAuthorityHead) || plan.CurrentAuthorityHead == plan.PreviousAuthorityHead || !validDigest(plan.ControlOwnerAcquired) {
		return nil, ErrInvalid
	}
	core, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil || core.UID != previous.UID || core.GID != previous.GID || !sameBinaryObject(core.Binary, previous.FixedBinary) {
		return nil, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || !sameControlDirectoryObject(directory, options.ControlDirectoryIdentity) || observeControlSocketExact(options.ControlDirectory, previous.ControlSocket) != nil {
		return nil, ErrConflict
	}
	heldFiles, err := openHeldSessionControlFiles(options.ControlDirectory, previous.ControlFiles)
	if err != nil {
		return nil, ErrConflict
	}
	defer heldFiles.close()
	if revalidateHeldRuntimeControlBoundary(options.ControlDirectory, directory, heldFiles, previous) != nil {
		return nil, ErrConflict
	}
	nonce, err := readSessionNonce(heldFiles, previous.SessionNonceDigest)
	if err != nil {
		return nil, ErrConflict
	}
	request := reconnectRequest{SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: previous.SessionID, SessionNonce: nonce, PreviousOwnerEpoch: plan.PreviousOwnerEpoch, OwnerEpoch: plan.OwnerEpoch, PreviousAuthorityHead: plan.PreviousAuthorityHead, CurrentAuthorityHead: plan.CurrentAuthorityHead, ControlOwnerAcquired: plan.ControlOwnerAcquired, Core: core, LastOwnerEpoch: previous.OwnerEpoch, LastAuthorityHead: previous.CurrentAuthorityHead, LastCommandSequence: previous.CommandSequence, LastCommandHead: previous.CommandHead, LastJournalSequence: previous.JournalSequence, LastJournalHead: previous.JournalHead}
	var pendingProjection *PendingReplayEvidence
	if options.Pending != nil {
		prepared := *options.Pending
		if prepared.evidence.Validate() != nil || prepared.evidence.PreCommand != previous {
			return nil, ErrConflict
		}
		pending := prepared.request
		request.PendingRequest = &pending
		evidence := pendingEvidenceForPrepared(prepared)
		pendingProjection = &evidence
	}
	wire := reconnectWireOptions{ControlDirectory: options.ControlDirectory, ControlDirectoryIdentity: options.ControlDirectoryIdentity, Request: request, Anchor: previous, PendingEvidence: pendingProjection}
	if err := validateReconnectWireOptions(wire); err != nil {
		return nil, err
	}
	address := filepath.Join(directory.CanonicalPath, controlSocket)
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return nil, ErrIntervention
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = connection.Close()
		}
	}()
	codec, err := NewProtocolCodec(connection)
	if err != nil {
		return nil, err
	}
	var handshake HandshakeResponse
	var replay *Response
	var current HandshakeAnchor
	var peer CoreIdentity
	err = runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
		if revalidateHeldRuntimeControlBoundary(options.ControlDirectory, directory, heldFiles, previous) != nil {
			return ErrConflict
		}
		peer, err = ObserveFixedMarshalPeer(connection)
		if err != nil || !sameBinaryObject(peer.Binary, previous.FixedBinary) {
			return ErrConflict
		}
		if err := codec.Write(request); err != nil {
			return ErrIntervention
		}
		if err := codec.Read(&handshake); err != nil {
			return ErrIntervention
		}
		replay, current, err = validateReconnectHandshake(handshake, wire, peer)
		if err != nil || revalidateHeldRuntimeControlBoundary(options.ControlDirectory, directory, heldFiles, wire.Anchor) != nil {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var replayedOutcome *VerifiedCommandOutcome
	if replay != nil {
		if request.PendingRequest == nil {
			return nil, ErrConflict
		}
		postCommand, err := commandPostAnchor(wire.Anchor, *request.PendingRequest, *replay)
		if err != nil {
			return nil, ErrConflict
		}
		outcome, err := verifiedCommandOutcome(*request.PendingRequest, *replay, CommandRecoveryEvidence{Reconciliation: handshake.Reconciliation, Replayed: true, PreCommand: wire.Anchor, PostCommand: postCommand})
		if err != nil {
			return nil, ErrConflict
		}
		replayedOutcome = &outcome
	}
	var pending *PendingReplayEvidence
	if pendingProjection != nil {
		copy := *pendingProjection
		pending = &copy
	}
	recovery := &SessionRecoveryEvidence{Reconciliation: handshake.Reconciliation, Previous: wire.Anchor, Current: current, Pending: pending, MechanicsLocked: handshake.Reconciliation == ReconciliationIntentPending}
	evidence := ConnectionEvidence{Core: core, ControlDirectory: directory, Handshake: handshake, Anchor: current, ReplayedOutcome: replayedOutcome, Recovery: recovery}
	client, err := newClient(connection, evidence, peer)
	if err != nil {
		return nil, err
	}
	if request.PendingRequest != nil && replay == nil {
		evidence := pendingEvidence(*request.PendingRequest)
		client.pending = &evidence
	}
	succeeded = true
	return client, nil
}

type attachControlSnapshot struct {
	Directory     ControlDirectoryIdentity
	Socket        ControlSocketIdentity
	Files         SessionControlFiles
	NonceSize     int64
	NonceDigest   string
	JournalSize   int64
	JournalDigest string
}

// WithAttached authenticates an already-live Supervisor without invoking
// reconnect reconciliation, reissuing authority, rebuilding the child/pipe, or
// resending any command. The connection and the borrowed AttachedSession
// exist only inside fn, while OwnerVerifier keeps the exact repository owner
// acquisition and owner-bound Attempt successor held and current. Attach
// appends no mechanics journal and changes no owner epoch, authority head,
// command head, pending request, nonce, socket, or control entry; on failure
// every control object is byte-for-byte unchanged. Non-Darwin builds fail
// closed via client_other.go.
func WithAttached(ctx context.Context, options AttachOptions, fn func(*AttachedSession) error) error {
	if ctx == nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) ||
		options.ControlDirectoryIdentity.validate() != nil || options.Authority.validate() != nil || options.OwnerVerifier == nil || fn == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ErrIntervention
	}
	deadline := time.Now().Add(handshakeTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return withAttachOwner(ctx, options.OwnerVerifier, options.Authority, func() error {
		core, err := ObserveCurrentCore(options.FixedMarshalPath)
		if err != nil || core.UID != options.Authority.CurrentAcquisition.OwnerUID || core.GID != options.Authority.CurrentAcquisition.OwnerGID ||
			core.Process != options.Authority.CurrentAcquisition.OwnerProcess || core.Binary != options.Authority.CurrentAcquisition.OwnerBinary ||
			!sameBinaryObject(core.Binary, options.Authority.PreviousSupervisor.FixedBinary) {
			return ErrConflict
		}
		directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
		if err != nil || !sameControlDirectoryObject(directory, options.ControlDirectoryIdentity) {
			return ErrConflict
		}
		held, err := openHeldSessionControlFiles(options.ControlDirectory, options.Authority.PreviousSupervisor.ControlFiles)
		if err != nil {
			return ErrConflict
		}
		defer held.close()
		before, journal, err := captureAttachControlSnapshot(options.ControlDirectory, directory, held)
		if err != nil || validateAttachJournalAnchor(journal, options.Authority.PreviousSupervisor) != nil {
			return ErrConflict
		}
		nonce, err := readSessionNonce(held, options.Authority.PreviousSupervisor.SessionNonceDigest)
		if err != nil {
			return ErrConflict
		}
		request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: nonce, Core: core, ControlDirectoryIdentity: directory, Authority: options.Authority}
		request.RequestDigest, err = request.detachedDigest()
		if err != nil || request.validate() != nil {
			return ErrConflict
		}
		address := filepath.Join(directory.CanonicalPath, controlSocket)
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
		if err != nil {
			return ErrIntervention
		}
		defer connection.Close()
		codec, err := NewProtocolCodec(connection)
		if err != nil {
			return err
		}
		var response attachResponse
		var peer CoreIdentity
		err = runBoundedTransport(ctx, connection, deadline, func() error {
			current, currentJournal, err := captureAttachControlSnapshot(options.ControlDirectory, directory, held)
			if err != nil || current != before || validateAttachJournalAnchor(currentJournal, options.Authority.PreviousSupervisor) != nil {
				return ErrConflict
			}
			peer, err = ObserveFixedMarshalPeer(connection)
			if err != nil || !sameBinaryObject(peer.Binary, options.Authority.PreviousSupervisor.FixedBinary) {
				return ErrConflict
			}
			if err := codec.Write(request); err != nil {
				return ErrIntervention
			}
			if err := codec.Read(&response); err != nil || response.validate(request, peer) != nil {
				return ErrConflict
			}
			after, afterJournal, err := captureAttachControlSnapshot(options.ControlDirectory, directory, held)
			if err != nil || after != before || validateAttachJournalAnchor(afterJournal, options.Authority.PreviousSupervisor) != nil {
				return ErrConflict
			}
			return nil
		})
		if err != nil {
			return err
		}
		observation := AttachObservation{
			SchemaVersion: AttachObservationSchema, ProtocolRevision: ProtocolRevision, RequestDigest: request.RequestDigest, ResponseDigest: response.ResponseDigest,
			PreviousSupervisor: options.Authority.PreviousSupervisor, Handshake: response.Handshake, Supervisor: options.Authority.Supervisor, CurrentAcquisition: options.Authority.CurrentAcquisition, CurrentOwnerBoundFact: options.Authority.CurrentOwnerBoundFact,
			Child: options.Authority.Child, ChildObservationDigest: options.Authority.ChildObservationDigest, ControlDirectory: directory, Peer: peer, ObservedAt: response.ObservedAt,
		}
		if observation.validate() != nil {
			return ErrConflict
		}
		session := newRebindAttachedSession(observation, connection, codec, options.Authority.PreviousSupervisor)
		borrowErr := callAttachedBorrower(session, fn)
		afterCallback, callbackJournal, snapshotErr := captureAttachControlSnapshot(options.ControlDirectory, directory, held)
		if snapshotErr != nil {
			return ErrConflict
		}
		session.guard.mu.Lock()
		commandExecuted := session.guard.commandExecuted
		postCommand := session.guard.postCommand
		session.guard.mu.Unlock()
		if commandExecuted {
			// One bind-authority(owner-successor) rebind advanced the mechanics
			// journal by exactly one intent + one receipt and moved the session
			// authority head. The control directory, socket, nonce and held files
			// must be unchanged; the journal must now match the post-command anchor.
			if validateAttachJournalAnchor(callbackJournal, postCommand) != nil {
				return ErrConflict
			}
			if afterCallback.Directory != before.Directory || afterCallback.Socket != before.Socket || afterCallback.NonceSize != before.NonceSize || afterCallback.NonceDigest != before.NonceDigest || afterCallback.Files != before.Files {
				return ErrConflict
			}
		} else {
			if afterCallback != before || validateAttachJournalAnchor(callbackJournal, options.Authority.PreviousSupervisor) != nil {
				return ErrConflict
			}
		}
		if borrowErr != nil {
			return borrowErr
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return ErrIntervention
		}
		// Half-close the write side and require the server to close without any
		// further frame. After a rebind the server has already consumed its one
		// bind-authority command and is waiting for this EOF; after a read-only
		// Attach EOF is the only accepted follow-up.
		if err := connection.SetDeadline(deadline); err != nil || connection.CloseWrite() != nil {
			return ErrIntervention
		}
		var unexpected [1]byte
		if count, readErr := connection.Read(unexpected[:]); count != 0 || !errors.Is(readErr, io.EOF) {
			return ErrConflict
		}
		return nil
	})
}

func captureAttachControlSnapshot(directory *os.File, identity ControlDirectoryIdentity, held *heldSessionControlFiles) (attachControlSnapshot, JournalSnapshot, error) {
	if directory == nil || held == nil || identity.validate() != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrInvalid
	}
	observed, err := ObserveHeldControlDirectory(directory)
	if err != nil || !sameControlDirectoryObject(observed, identity) || revalidateHeldSessionControlFiles(directory, held, held.identity) != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	socket, err := ObserveHeldControlSocket(directory)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	nonceIdentity, nonceSize, nonceDigest, err := digestHeldControlFile(held.nonce, nonceBytes)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, err
	}
	journalIdentity, journalSize, journalDigest, err := digestHeldControlFile(held.journal, MaxJournalFileBytes)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, err
	}
	if nonceIdentity != held.identity.Nonce || journalIdentity != held.identity.Journal {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	journal, err := readHeldJournalSnapshot(held.journal)
	if err != nil || revalidateControlDirectoryForSnapshot(directory, identity, journal) != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	return attachControlSnapshot{Directory: observed, Socket: socket, Files: held.identity, NonceSize: nonceSize, NonceDigest: nonceDigest, JournalSize: journalSize, JournalDigest: journalDigest}, journal, nil
}

func digestHeldControlFile(file *os.File, limit int) (ControlFileIdentity, int64, string, error) {
	identity, size, err := observeControlFile(file)
	if err != nil || size <= 0 || size > int64(limit) {
		return ControlFileIdentity{}, 0, "", ErrConflict
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, size))
	if err != nil || int64(len(data)) != size {
		return ControlFileIdentity{}, 0, "", ErrIntervention
	}
	after, afterSize, err := observeControlFile(file)
	if err != nil || after != identity || afterSize != size {
		return ControlFileIdentity{}, 0, "", ErrConflict
	}
	return identity, size, canonical.DigestBytes(data), nil
}

func revalidateHeldRuntimeControlBoundary(directory *os.File, identity ControlDirectoryIdentity, held *heldSessionControlFiles, anchor HandshakeAnchor) error {
	if held == nil || revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles) != nil || observeControlSocketExact(directory, anchor.ControlSocket) != nil {
		return ErrConflict
	}
	snapshot, err := readHeldJournalSnapshot(held.journal)
	if err != nil || revalidateControlDirectoryForSnapshot(directory, identity, snapshot) != nil || revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles) != nil {
		return ErrConflict
	}
	return nil
}

func pendingEvidenceForPrepared(prepared PreparedCommand) PendingReplayEvidence {
	return pendingEvidence(prepared.request)
}

func initialJournalHead(bootstrap BootstrapRequest) (string, error) {
	record := journalRecord{
		SchemaVersion: JournalSchema, JournalSequence: 1, Kind: journalSessionCreated,
		SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)),
		Authority: bootstrap.Authority, OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead,
		PreviousRecordDigest: JournalGenesisDigest,
	}
	digest, err := record.detachedDigest()
	if err != nil {
		return "", err
	}
	record.RecordDigest = digest
	if record.validate(JournalGenesisDigest, 1) != nil {
		return "", ErrIntervention
	}
	return digest, nil
}

func abortStartedSupervisor(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
