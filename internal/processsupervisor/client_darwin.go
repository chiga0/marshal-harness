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
	if err != nil || directory != options.Bootstrap.ControlDirectoryIdentity || revalidateControlDirectory(options.ControlDirectory, directory) != nil {
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
	err = runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
		if err := codec.Write(options.Bootstrap); err != nil {
			return ErrIntervention
		}
		if err := codec.Read(&handshake); err != nil {
			return ErrIntervention
		}
		socket, err := ObserveHeldControlSocket(options.ControlDirectory)
		if err != nil || revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, socket) != nil {
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
			UID: core.UID, GID: core.GID, FixedBinary: core.Binary, ControlSocket: socket,
		}
		return ValidateHandshakeBinding(handshake, anchor, peer)
	})
	if err != nil {
		return nil, err
	}
	evidence := ConnectionEvidence{Core: core, ControlDirectory: directory, Handshake: handshake, Anchor: anchor}
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
	if err := validateReconnectOptions(options); err != nil {
		return nil, err
	}
	core, err := observeSelfIdentity()
	if err != nil || !sameCoreIdentity(options.Request.Core, core) || core.UID != options.Anchor.UID || core.GID != options.Anchor.GID || !sameBinaryObject(core.Binary, options.Anchor.FixedBinary) {
		return nil, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || directory != options.ControlDirectoryIdentity || revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, options.Anchor.ControlSocket) != nil {
		return nil, ErrConflict
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
	var anchor HandshakeAnchor
	var peer CoreIdentity
	err = runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
		if revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, options.Anchor.ControlSocket) != nil {
			return ErrConflict
		}
		peer, err = ObserveFixedMarshalPeer(connection)
		if err != nil || !sameBinaryObject(peer.Binary, options.Anchor.FixedBinary) {
			return ErrConflict
		}
		if err := codec.Write(options.Request); err != nil {
			return ErrIntervention
		}
		if err := codec.Read(&handshake); err != nil {
			return ErrIntervention
		}
		replay, anchor, err = validateReconnectHandshake(handshake, options, peer)
		if err != nil || revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, options.Anchor.ControlSocket) != nil {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var replayedOutcome *VerifiedCommandOutcome
	if replay != nil {
		if options.Request.PendingRequest == nil {
			return nil, ErrConflict
		}
		postCommand, err := commandPostAnchor(options.Anchor, *options.Request.PendingRequest, *replay)
		if err != nil {
			return nil, ErrConflict
		}
		outcome, err := verifiedCommandOutcome(*options.Request.PendingRequest, *replay, CommandRecoveryEvidence{Reconciliation: handshake.Reconciliation, Replayed: true, PreCommand: options.Anchor, PostCommand: postCommand})
		if err != nil {
			return nil, ErrConflict
		}
		replayedOutcome = &outcome
	}
	var pending *PendingReplayEvidence
	if options.PendingEvidence != nil {
		copy := *options.PendingEvidence
		pending = &copy
	}
	recovery := &SessionRecoveryEvidence{Reconciliation: handshake.Reconciliation, Previous: options.Anchor, Current: anchor, Pending: pending, MechanicsLocked: handshake.Reconciliation == ReconciliationIntentPending}
	evidence := ConnectionEvidence{Core: core, ControlDirectory: directory, Handshake: handshake, Anchor: anchor, ReplayedOutcome: replayedOutcome, Recovery: recovery}
	client, err := newClient(connection, evidence, peer)
	if err != nil {
		return nil, err
	}
	if options.Request.PendingRequest != nil && replay == nil {
		evidence := pendingEvidence(*options.Request.PendingRequest)
		client.pending = &evidence
	}
	succeeded = true
	return client, nil
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
