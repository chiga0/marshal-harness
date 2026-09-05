//go:build !darwin

package processsupervisor

import (
	"context"
	"os"
)

func ObserveCurrentCore(string) (CoreIdentity, error) {
	return CoreIdentity{}, ErrUnavailable
}

func ObserveHeldControlDirectory(*os.File) (ControlDirectoryIdentity, error) {
	return ControlDirectoryIdentity{}, ErrUnavailable
}

func ObserveHeldControlSocket(*os.File) (ControlSocketIdentity, error) {
	return ControlSocketIdentity{}, ErrUnavailable
}

func Start(context.Context, StartOptions) (*Client, error) {
	return nil, ErrUnavailable
}

func StartV2(context.Context, StartOptionsV2) (*ClientV2, error) {
	return nil, ErrUnavailable
}

func Reconnect(context.Context, ReconnectOptions) (*Client, error) {
	return nil, ErrUnavailable
}

// WithAttached fails closed on non-Darwin: the read-only Attach primitive is a
// Darwin ordinary-user contract only and must never provide authority on
// another platform or hardened profile.
func WithAttached(context.Context, AttachOptions, func(*AttachedSession) error) error {
	return ErrUnavailable
}
