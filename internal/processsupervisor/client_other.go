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

func Reconnect(context.Context, ReconnectOptions) (*Client, error) {
	return nil, ErrUnavailable
}
