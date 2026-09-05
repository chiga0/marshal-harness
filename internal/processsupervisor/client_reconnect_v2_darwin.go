//go:build darwin

package processsupervisor

import (
	"context"
	"net"
	"time"
)

// ReconnectV2 borrows the exact existing control directory. It never starts,
// signals or adopts a process and never translates a v1 journal or command.
func ReconnectV2(ctx context.Context, options ReconnectOptionsV2) (*ClientV2, error) {
	if ctx == nil || ctx.Err() != nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.Anchor.Validate() != nil {
		return nil, ErrInvalid
	}
	core, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil {
		return nil, err
	}
	anchor := options.Anchor
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || !sameControlDirectoryObject(directory, anchor.ControlDirectory) {
		return nil, ErrConflict
	}
	held, err := openHeldSessionControlFilesForLeaf(options.ControlDirectory, anchor.Binding.ControlFiles, journalFileNameV2)
	if err != nil {
		return nil, ErrConflict
	}
	defer held.close()
	nonce, err := readSessionNonce(held, anchor.Binding.SessionNonceDigest)
	if err != nil {
		return nil, ErrConflict
	}
	request, err := prepareReconnectRequestV2(anchor, options.Plan, options.Pending, nonce, core)
	if err != nil {
		return nil, err
	}
	boundary := sessionControlBoundary{directory: options.ControlDirectory, directoryIdentity: directory, socket: anchor.Binding.ControlSocket, heldFiles: held, controlFiles: anchor.Binding.ControlFiles}
	checkBefore := func() error {
		state, err := readHeldJournalStateV2(held.journal)
		if err != nil || boundary.revalidateV2(state) != nil || validateReconnectJournalV2(state, anchor, options.Pending) != nil {
			return ErrConflict
		}
		return nil
	}
	if err := checkBefore(); err != nil {
		return nil, err
	}
	address, err := controlSocketAddress(directory)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(handshakeTimeout)
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.DialContext(ctx, "unix", address)
	if err != nil {
		return nil, ErrIntervention
	}
	connection, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, ErrConflict
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
	var response HandshakeResponseV2
	var peer CoreIdentity
	err = runBoundedTransport(ctx, connection, deadline, func() error {
		if err := checkBefore(); err != nil {
			return err
		}
		peer, err = ObserveFixedMarshalPeer(connection)
		if err != nil || peer.UID != anchor.Binding.UID || peer.GID != anchor.Binding.GID || !sameBinaryObject(peer.Binary, anchor.Binding.FixedBinary) {
			return ErrConflict
		}
		if codec.Write(request) != nil || codec.Read(&response) != nil {
			return ErrIntervention
		}
		recovery, err := validateReconnectHandshakeV2(response, anchor, options.Plan, options.Pending, peer)
		if err != nil {
			return err
		}
		state, err := readHeldJournalStateV2(held.journal)
		b := recovery.Current.Binding
		if err != nil || boundary.revalidateV2(state) != nil || state.sequence != b.JournalSequence || state.head != b.JournalHead || state.commandSeq != b.CommandSequence || state.commandHead != b.CommandHead {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	client, err := newReconnectedClientV2(connection, codec, response, anchor, options.Plan, options.Pending, peer)
	if err != nil {
		return nil, err
	}
	succeeded = true
	return client, nil
}
