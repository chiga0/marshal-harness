//go:build darwin

package processsupervisor

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

// StartV2 uses the same fixed command and inherited descriptors as Start.
// Only the exact v2 wire, journal and evidence are admitted. This is not a
// production selector or permission to adopt an active v1 session.
func StartV2(ctx context.Context, options StartOptionsV2) (*ClientV2, error) {
	if ctx == nil || ctx.Err() != nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.Bootstrap.validate() != nil {
		return nil, ErrInvalid
	}
	core, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil || !sameCoreIdentity(core, options.Bootstrap.Core) {
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
	parent, child := os.NewFile(uintptr(fds[0]), "marshal-v2-bootstrap-core"), os.NewFile(uintptr(fds[1]), "marshal-v2-bootstrap-child")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, ErrUnavailable
	}
	connection, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		_ = child.Close()
		return nil, ErrUnavailable
	}
	command := newSupervisorCommand(options.FixedMarshalPath, child, options.ControlDirectory)
	if err := command.Start(); err != nil {
		_ = connection.Close()
		_ = child.Close()
		return nil, ErrUnavailable
	}
	_ = child.Close()
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
	var handshake HandshakeResponseV2
	var anchor SessionAnchorV2
	var peer CoreIdentity
	err = runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
		if codec.Write(options.Bootstrap) != nil || codec.Read(&handshake) != nil {
			return ErrIntervention
		}
		held, err := openHeldSessionControlFilesForLeaf(options.ControlDirectory, handshake.ControlFiles, journalFileNameV2)
		if err != nil {
			return ErrConflict
		}
		defer held.close()
		nonceDigest := canonical.DigestBytes([]byte(options.Bootstrap.SessionNonce))
		nonce, err := readSessionNonce(held, nonceDigest)
		if err != nil || nonce != options.Bootstrap.SessionNonce {
			return ErrConflict
		}
		peer, err = ObserveFixedMarshalPeer(connection)
		if err != nil || command.Process == nil || peer.Process.PID != command.Process.Pid {
			return ErrConflict
		}
		socket, err := ObserveHeldControlSocket(options.ControlDirectory)
		if err != nil {
			return ErrConflict
		}
		finalDirectory, err := ObserveHeldControlDirectory(options.ControlDirectory)
		if err != nil || !sameControlDirectoryObject(finalDirectory, directory) {
			return ErrConflict
		}
		state, err := readHeldJournalStateV2(held.journal)
		if err != nil || state.sequence != 1 || state.pending != nil {
			return ErrConflict
		}
		created := journalRecordV2{SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
			JournalSequence: 1, Kind: journalSessionCreated, SessionID: options.Bootstrap.SessionID, SessionNonceDigest: nonceDigest, Authority: options.Bootstrap.Authority,
			OwnerEpoch: options.Bootstrap.OwnerEpoch, CurrentAuthorityHead: options.Bootstrap.CurrentAuthorityHead, PreviousRecordDigest: journalGenesisDigestV2}
		expectedHead, err := created.detachedDigest()
		if err != nil || state.head != expectedHead {
			return ErrConflict
		}
		anchor = SessionAnchorV2{Generation: DormantV2ProtocolContract(), ControlDirectory: finalDirectory, Binding: HandshakeAnchor{
			SessionID: created.SessionID, SessionNonceDigest: nonceDigest, Authority: created.Authority, OwnerEpoch: created.OwnerEpoch, CurrentAuthorityHead: created.CurrentAuthorityHead,
			CommandHead: commandGenesisDigestV2, JournalSequence: 1, JournalHead: expectedHead, UID: core.UID, GID: core.GID, FixedBinary: core.Binary, ControlSocket: socket, ControlFiles: handshake.ControlFiles}}
		boundary := sessionControlBoundary{directory: options.ControlDirectory, directoryIdentity: finalDirectory, socket: socket, heldFiles: held, controlFiles: handshake.ControlFiles}
		if handshake.Reconciliation != "" || ValidateHandshakeBindingV2(handshake, anchor, peer) != nil || boundary.revalidateV2(state) != nil {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	client, err := newClientV2(connection, handshake, anchor, peer)
	if err != nil {
		return nil, err
	}
	client.codec = codec // Preserve any buffered bytes; never discard extra frames.
	succeeded = true
	go func() { _ = command.Wait() }()
	return client, nil
}

// A read-only exact transaction for Core's current held journal observation.
// Unlike the writer's startup repair, no incomplete tail is ever changed.
func readHeldJournalStateV2(file *os.File) (journalStateV2, error) {
	before, size, err := observeControlFile(file)
	if err != nil || size <= 0 || size > MaxJournalFileBytes {
		return journalStateV2{}, ErrConflict
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, size))
	if err != nil || int64(len(data)) != size {
		return journalStateV2{}, ErrIntervention
	}
	after, afterSize, err := observeControlFile(file)
	if err != nil || after != before || size != afterSize {
		return journalStateV2{}, ErrConflict
	}
	records, consumed, partial, err := parseJournalV2(data)
	if err != nil || partial || consumed != len(data) {
		return journalStateV2{}, ErrIntervention
	}
	state := newJournalStateV2()
	for _, record := range records {
		if state.validateNext(record) != nil {
			return journalStateV2{}, ErrIntervention
		}
		state.accept(record)
	}
	return state, nil
}
