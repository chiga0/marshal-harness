package authorityprovider

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// CredentialIngressRequest is the only server-side projection that can carry
// a credential capability. The capability remains a held fd owned by the
// handler for one invocation and never enters APAP control transport.
type CredentialIngressRequest struct {
	Envelope   CredentialIngressRequestV1
	Capability *os.File
	Peer       PeerIdentity
}

// CredentialIngressHandler belongs to the independently provisioned secret
// provider/isolation authority. It must return a typed receipt reference and
// never return credential bytes or another capability fd.
type CredentialIngressHandler func(context.Context, CredentialIngressRequest) (CredentialIngressResponseV1, error)

// ServeCredentialIngress serves a session-scoped ingress endpoint. It shares
// only bounded framing with APAP control; protocol decoding enforces the
// secret-provider principal and exactly one credentialCapability fd.
func ServeCredentialIngress(ctx context.Context, listener *ControlListener, authenticate PeerAuthenticator, handler CredentialIngressHandler) error {
	if ctx == nil || listener == nil || listener.listener == nil || authenticate == nil || handler == nil {
		return errors.New("credential ingress server is unavailable")
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
			return errors.New("accept credential ingress connection")
		}
		serveCredentialIngressConnection(ctx, connection, listener.stream, authenticate, handler)
	}
}

func serveCredentialIngressConnection(ctx context.Context, connection *net.UnixConn, stream bool, authenticate PeerAuthenticator, handler CredentialIngressHandler) {
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
	if err != nil || len(files) != 1 {
		_ = closeFiles(files)
		return
	}
	request, err := DecodeCredentialIngressRequest(payload, peer, time.Now().UTC(), []FDRef{{Role: FDCredentialCapability, Index: 0}})
	if err != nil {
		_ = closeFiles(files)
		return
	}
	response, err := handler(ctx, CredentialIngressRequest{Envelope: request, Capability: files[0], Peer: peer})
	_ = closeFiles(files)
	if err != nil {
		return
	}
	encoded, err := SealCredentialIngressResponse(response)
	if err != nil {
		return
	}
	if _, err := DecodeCredentialIngressResponse(encoded, request); err != nil {
		return
	}
	_ = writeControlFrame(connection, stream, encoded, nil)
}
