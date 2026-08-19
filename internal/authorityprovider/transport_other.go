//go:build !darwin

package authorityprovider

import (
	"context"
	"errors"
	"net"
)

func dialControl(ctx context.Context, endpoint string) (controlConnection, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unixpacket", endpoint)
	if err != nil {
		return controlConnection{}, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return controlConnection{}, errors.New("APAP transport is not a Unix packet connection")
	}
	return controlConnection{conn: unixConnection}, nil
}
