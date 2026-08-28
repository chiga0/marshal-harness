//go:build !darwin

package processsupervisor

import (
	"context"
	"net"
)

// RunInherited truthfully refuses the unprofiled platform before reading any
// inherited authority material.
func RunInherited(context.Context) error { return ErrUnavailable }

func ObserveFixedMarshalPeer(*net.UnixConn) (CoreIdentity, error) {
	return CoreIdentity{}, ErrUnavailable
}
