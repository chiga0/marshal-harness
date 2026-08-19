package authorityprovider

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// ControlListener is an externally provisioned APAP endpoint. The listener
// never removes or replaces its path; launchd/system provisioning owns its
// owner, mode and lifecycle.
type ControlListener struct {
	listener *net.UnixListener
	stream   bool
}

// Close closes the listener without unlinking its endpoint path. Provisioning
// owns path cleanup and service restart semantics.
func (listener *ControlListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	return listener.listener.Close()
}

// ControlRequest is the authenticated transport input delivered to an APAP
// provider. The provider owns request descriptors only for the duration of
// Handle and must not persist or reopen them by pathname.
type ControlRequest struct {
	Payload []byte
	FDs     []*os.File
	Peer    PeerIdentity
}

// ControlHandler is the provider-owned authority boundary. A nil handler or
// authenticator is rejected; transport never invents a peer identity.
type ControlHandler func(context.Context, ControlRequest) (ControlResponse, error)

// PeerAuthenticator binds a kernel-observed connection to a provider
// principal. Implementations are service/platform specific and must return an
// exact PeerIdentity or fail closed.
type PeerAuthenticator func(*net.UnixConn) (PeerIdentity, error)

// ServeControl accepts one bounded APAP control frame per connection. It does
// not mint signatures, read credentials, or change Marshal lifecycle state.
func ServeControl(ctx context.Context, listener *ControlListener, authenticate PeerAuthenticator, handler ControlHandler) error {
	if ctx == nil || listener == nil || listener.listener == nil || authenticate == nil || handler == nil {
		return errors.New("APAP control server is unavailable")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = listener.listener.SetDeadline(time.Now().Add(250 * time.Millisecond))
		connection, err := listener.listener.AcceptUnix()
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("accept APAP control connection")
		}
		serveControlConnection(ctx, connection, listener.stream, authenticate, handler)
	}
}

func serveControlConnection(ctx context.Context, connection *net.UnixConn, stream bool, authenticate PeerAuthenticator, handler ControlHandler) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	peer, err := authenticate(connection)
	if err != nil {
		return
	}
	payload, oob, flags, err := readControlFrame(connection, stream)
	if err != nil || len(payload) == 0 || len(payload) > maxControlPacketBytes || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return
	}
	files, err := receivedFiles(oob)
	if err != nil {
		return
	}
	response, err := handler(ctx, ControlRequest{Payload: payload, FDs: files, Peer: peer})
	_ = closeFiles(files)
	if err != nil || len(response.Payload) == 0 || len(response.Payload) > maxControlPacketBytes || len(response.FDs) > maxControlPacketFDs {
		_ = response.Close()
		return
	}
	fdNumbers := make([]int, 0, len(response.FDs))
	for _, file := range response.FDs {
		if file == nil {
			_ = response.Close()
			return
		}
		fdNumbers = append(fdNumbers, int(file.Fd()))
	}
	if err := writeControlFrame(connection, stream, response.Payload, unix.UnixRights(fdNumbers...)); err != nil {
		_ = response.Close()
		return
	}
	_ = response.Close()
}

func readControlFrame(connection *net.UnixConn, stream bool) ([]byte, []byte, int, error) {
	if stream {
		return readStreamFrame(connection)
	}
	buffer := make([]byte, maxControlPacketBytes+1)
	oobBuffer := make([]byte, unix.CmsgSpace(maxControlPacketFDs*4))
	length, oobLength, flags, _, err := connection.ReadMsgUnix(buffer, oobBuffer)
	return buffer[:length], oobBuffer[:oobLength], flags, err
}

func writeControlFrame(connection *net.UnixConn, stream bool, payload, oob []byte) error {
	if stream {
		return writeStreamFrame(connection, payload, oob)
	}
	written, _, err := connection.WriteMsgUnix(payload, oob, nil)
	if err != nil || written != len(payload) {
		return err
	}
	return nil
}

func receivedFiles(oob []byte) ([]*os.File, error) {
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, errors.New("parse APAP control descriptors")
	}
	files := make([]*os.File, 0, maxControlPacketFDs)
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil || len(fds) > maxControlPacketFDs-len(files) {
			_ = closeFiles(files)
			return nil, errors.New("APAP control descriptors are invalid")
		}
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "apap-request-fd"))
		}
	}
	return files, nil
}

func closeFiles(files []*os.File) error {
	var first error
	for _, file := range files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
