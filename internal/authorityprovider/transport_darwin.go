//go:build darwin

package authorityprovider

import (
	"context"
	"errors"
	"net"
)

// Darwin does not provide a usable SOCK_SEQPACKET Unix-domain transport on
// the supported macOS hosts. Use a length-framed SOCK_STREAM while retaining
// SCM_RIGHTS and the same bounded, fail-closed packet contract.
func dialControl(ctx context.Context, endpoint string) (controlConnection, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
	if err != nil {
		return controlConnection{}, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return controlConnection{}, errors.New("APAP transport is not a Unix connection")
	}
	return controlConnection{conn: unixConnection, stream: true}, nil
}
