package authorityprovider

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxControlPacketBytes = 64 << 10
	maxControlPacketFDs   = 32
)

// ControlClient is the narrow transport boundary to an externally deployed
// APAP service. It carries opaque signed envelopes and held descriptors only;
// it does not mint authority, interpret credentials, or provide a fallback
// when the service is absent.
type ControlClient struct {
	endpoint string
	dial     func(context.Context, string) (*net.UnixConn, error)
}

func NewControlClient(endpoint string) (*ControlClient, error) {
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint || endpoint == string(filepath.Separator) {
		return nil, errors.New("APAP endpoint must be an absolute clean path")
	}
	return &ControlClient{endpoint: endpoint, dial: dialUnixPacket}, nil
}

// ControlResponse is the raw APAP response plus descriptors returned by the
// authority service. The caller owns and must close every returned descriptor.
type ControlResponse struct {
	Payload []byte
	FDs     []*os.File
}

func (response ControlResponse) Close() error {
	var first error
	for _, file := range response.FDs {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RoundTrip sends one already-sealed canonical APAP envelope and receives one
// bounded response packet. The peer remains the authority for identity,
// sequence, signatures and operation authorization; transport success alone
// never implies a supported adapter.
func (client *ControlClient) RoundTrip(ctx context.Context, payload []byte, files ...*os.File) (ControlResponse, error) {
	if client == nil || client.dial == nil {
		return ControlResponse{}, errors.New("APAP control client is unavailable")
	}
	if len(payload) == 0 || len(payload) > maxControlPacketBytes || len(files) > maxControlPacketFDs {
		return ControlResponse{}, errors.New("APAP control packet exceeds bounds")
	}
	if ctx == nil {
		return ControlResponse{}, errors.New("APAP control context is nil")
	}
	conn, err := client.dial(ctx, client.endpoint)
	if err != nil {
		return ControlResponse{}, errors.New("connect APAP control socket")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	fdNumbers := make([]int, 0, len(files))
	for _, file := range files {
		if file == nil {
			return ControlResponse{}, errors.New("APAP control fd is nil")
		}
		fdNumbers = append(fdNumbers, int(file.Fd()))
	}
	oob := unix.UnixRights(fdNumbers...)
	written, _, err := conn.WriteMsgUnix(payload, oob, nil)
	if err != nil || written != len(payload) {
		return ControlResponse{}, errors.New("send APAP control packet")
	}
	buffer := make([]byte, maxControlPacketBytes+1)
	oobBuffer := make([]byte, unix.CmsgSpace(maxControlPacketFDs*4))
	length, oobLength, flags, _, err := conn.ReadMsgUnix(buffer, oobBuffer)
	if err != nil {
		return ControlResponse{}, errors.New("receive APAP control packet")
	}
	if length == 0 || length > maxControlPacketBytes || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return ControlResponse{}, errors.New("APAP control response exceeds bounds")
	}
	response := ControlResponse{Payload: append([]byte(nil), buffer[:length]...)}
	messages, err := unix.ParseSocketControlMessage(oobBuffer[:oobLength])
	if err != nil {
		return ControlResponse{}, errors.New("parse APAP control descriptors")
	}
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil || len(fds) > maxControlPacketFDs-len(response.FDs) {
			_ = response.Close()
			return ControlResponse{}, errors.New("APAP control descriptors are invalid")
		}
		for _, fd := range fds {
			response.FDs = append(response.FDs, os.NewFile(uintptr(fd), "apap-response-fd"))
		}
	}
	return response, nil
}

func dialUnixPacket(ctx context.Context, endpoint string) (*net.UnixConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unixpacket", endpoint)
	if err != nil {
		return nil, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("APAP transport is not a Unix packet connection")
	}
	return unixConnection, nil
}
