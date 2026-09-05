//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// WithAttachedV2 never launches a process or adopts a predecessor's wait
// rights. The physical owner is held through observation, durable preparation
// in the callback, one optional continuation, and final boundary validation.
func WithAttachedV2(ctx context.Context, options AttachOptionsV2, fn func(*AttachedSessionV2) error) error {
	return withAttachedV2(ctx, options, fn, ObserveCurrentCore, ObserveFixedMarshalPeer, controlSocketAddress)
}

func withAttachedV2(ctx context.Context, options AttachOptionsV2, fn func(*AttachedSessionV2) error,
	observeCore func(string) (CoreIdentity, error), observePeer func(*net.UnixConn) (CoreIdentity, error),
	socketAddress func(ControlDirectoryIdentity) (string, error)) error {
	if ctx == nil || ctx.Err() != nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.Authority.Validate() != nil || fn == nil {
		return ErrInvalid
	}
	return withAttachOwnerV2(ctx, options.OwnerVerifier, options.Authority, func() error {
		anchor := options.Authority.PreviousSupervisor
		core, err := observeCore(options.FixedMarshalPath)
		a := options.Authority.CurrentAcquisition
		if err != nil || core != (CoreIdentity{UID: a.OwnerUID, GID: a.OwnerGID, Process: a.OwnerProcess, Binary: a.OwnerBinary}) {
			return ErrConflict
		}
		directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
		if err != nil || !sameControlDirectoryObject(directory, anchor.ControlDirectory) {
			return ErrConflict
		}
		held, err := openHeldSessionControlFilesForLeaf(options.ControlDirectory, anchor.Binding.ControlFiles, journalFileNameV2)
		if err != nil {
			return ErrConflict
		}
		defer held.close()
		boundary := sessionControlBoundary{directory: options.ControlDirectory, directoryIdentity: directory, socket: anchor.Binding.ControlSocket, controlFiles: anchor.Binding.ControlFiles, heldFiles: held}
		before, err := captureAttachControlSnapshotV2(boundary, anchor)
		if err != nil {
			return err
		}
		nonce, err := readSessionNonce(held, anchor.Binding.SessionNonceDigest)
		if err != nil {
			return ErrConflict
		}
		request := attachRequestV2{SchemaVersion: AttachSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
			MechanicsIdentity: mechanicsIdentityV2, SessionNonce: nonce, Core: core, Authority: options.Authority}
		request.RequestDigest, err = request.detachedDigest()
		if err != nil || request.validate() != nil {
			return ErrConflict
		}
		address, err := socketAddress(directory)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(handshakeTimeout)
		dialer := net.Dialer{Deadline: deadline}
		conn, err := dialer.DialContext(ctx, "unix", address)
		if err != nil {
			return ErrIntervention
		}
		defer conn.Close()
		connection, ok := conn.(*net.UnixConn)
		if !ok {
			return ErrConflict
		}
		codec, err := NewProtocolCodec(connection)
		if err != nil {
			return err
		}
		var response attachResponseV2
		var peer CoreIdentity
		err = runBoundedTransport(ctx, connection, deadline, func() error {
			current, err := captureAttachControlSnapshotV2(boundary, anchor)
			if err != nil || current != before {
				return ErrConflict
			}
			peer, err = observePeer(connection)
			if err != nil || peer.Process != options.Authority.Supervisor || !sameBinaryObject(peer.Binary, anchor.Binding.FixedBinary) {
				return ErrConflict
			}
			if codec.Write(request) != nil || codec.Read(&response) != nil {
				return ErrIntervention
			}
			if response.validate(request, peer) != nil {
				return ErrConflict
			}
			after, err := captureAttachControlSnapshotV2(boundary, anchor)
			if err != nil || after != before {
				return ErrConflict
			}
			return nil
		})
		if err != nil {
			return err
		}
		client, err := newClientV2(connection, response.Handshake, anchor, peer)
		if err != nil {
			return err
		}
		// Keep the exact decoder, including buffered unsolicited frames.
		client.codec = codec
		session := &AttachedSessionV2{observation: AttachObservationV2{Response: response, Peer: peer}, client: client, scope: ctx}
		borrowErr := callAttachedBorrowerV2(session, fn)
		session.mu.Lock()
		attempted, executed, command, post := session.attempted, session.executed, session.command, session.post
		session.mu.Unlock()
		if attempted && !executed {
			// The intent may have committed even when a reply was lost. Do not
			// demand an unchanged journal or turn uncertainty into no-effect.
			return ErrIntervention
		}
		expected := anchor
		if executed {
			expected = post
		}
		checkAfter := func() error {
			after, err := captureAttachControlSnapshotV2(boundary, expected)
			if err != nil {
				return err
			}
			if executed {
				if !sameAttachPostCommandBoundary(command, after, before) {
					return ErrConflict
				}
			} else if after != before {
				return ErrConflict
			}
			return nil
		}
		if err := checkAfter(); err != nil {
			return ErrIntervention
		}
		if borrowErr != nil {
			return borrowErr
		}
		return runBoundedTransport(ctx, connection, time.Now().Add(handshakeTimeout), func() error {
			if connection.CloseWrite() != nil {
				return ErrIntervention
			}
			var unexpected responseV2
			if err := codec.Read(&unexpected); !errors.Is(err, io.EOF) {
				return ErrConflict
			}
			return checkAfter()
		})
	})
}

func captureAttachControlSnapshotV2(boundary sessionControlBoundary, anchor SessionAnchorV2) (attachControlSnapshot, error) {
	state, err := readHeldJournalStateV2(boundary.heldFiles.journal)
	if err != nil || validateReconnectJournalV2(state, anchor, nil) != nil || state.ownerEpoch != anchor.Binding.OwnerEpoch ||
		state.authorityHead != anchor.Binding.CurrentAuthorityHead || boundary.revalidateV2(state) != nil {
		return attachControlSnapshot{}, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(boundary.directory)
	if err != nil || !sameControlDirectoryObject(directory, anchor.ControlDirectory) {
		return attachControlSnapshot{}, ErrConflict
	}
	nonce, nonceSize, nonceDigest, err := digestHeldControlFile(boundary.heldFiles.nonce, nonceBytes)
	if err != nil {
		return attachControlSnapshot{}, err
	}
	journal, journalSize, journalDigest, err := digestHeldControlFile(boundary.heldFiles.journal, MaxJournalFileBytes)
	if err != nil || nonce != anchor.Binding.ControlFiles.Nonce || journal != anchor.Binding.ControlFiles.Journal || nonceDigest != anchor.Binding.SessionNonceDigest {
		return attachControlSnapshot{}, ErrConflict
	}
	return attachControlSnapshot{Directory: directory, Socket: boundary.socket, Files: boundary.controlFiles,
		NonceSize: nonceSize, NonceDigest: nonceDigest, JournalSize: journalSize, JournalDigest: journalDigest}, nil
}
