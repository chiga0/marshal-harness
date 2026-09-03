//go:build !darwin || !arm64

package productionruntime

import (
	"context"
	"os"
)

func (session *RepositorySession) OpenFixedEndpointAuthority(context.Context) (*FixedEndpointAuthority, error) {
	return nil, ErrFixedDeliveryConflict
}

func (authority *FixedEndpointAuthority) withCurrent(context.Context, bool, func(*os.File) error) error {
	return ErrFixedDeliveryConflict
}
