//go:build !darwin || !arm64

package productionruntime

import (
	"context"
	"os"
)

type fixedEndpointClientState struct{}

func (*fixedEndpointClientState) close() error { return nil }

func (session *RepositorySession) OpenFixedEndpointAuthority(context.Context) (*FixedEndpointAuthority, error) {
	return nil, ErrFixedDeliveryConflict
}

func OpenFixedEndpointClientAuthority(context.Context, string) (*FixedEndpointAuthority, error) {
	return nil, ErrFixedDeliveryConflict
}

func (authority *FixedEndpointAuthority) withCurrent(context.Context, bool, func(*os.File) error) error {
	return ErrFixedDeliveryConflict
}
