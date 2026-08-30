//go:build darwin

package processsupervisor

import (
	"context"
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
		handshakeDebug("start: writing bootstrap")
		if err := codec.Write(options.Bootstrap); err != nil {
			handshakeDebug("start: bootstrap write failed: %v", err)
			return ErrIntervention
		}
		handshakeDebug("start: reading handshake")
		if err := codec.Read(&handshake); err != nil {
			handshakeDebug("start: handshake read failed: %v", err)
			return ErrIntervention
		}
		handshakeDebug("start: handshake read ok")
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
