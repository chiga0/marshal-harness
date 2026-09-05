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

func ReconnectV2(context.Context, ReconnectOptionsV2) (*ClientV2, error) {
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

func WithAttachedV2(context.Context, AttachOptionsV2, func(*AttachedSessionV2) error) error {
	return ErrUnavailable
}

func ObservePreparedCommandV2(context.Context, PreparedJournalOptionsV2) (PreparedJournalObservationV2,error) {
	return PreparedJournalObservationV2{},ErrUnavailable
}
