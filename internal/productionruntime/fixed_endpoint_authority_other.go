//go:build !darwin || !arm64

package productionruntime

import (
	"context"
	"os"

	"github.com/chiga0/marshal-harness/internal/application"
)

type fixedEndpointClientState struct{}

func (*fixedEndpointClientState) close() error { return nil }

func (session *RepositorySession) OpenFixedEndpointAuthority(context.Context) (*FixedEndpointAuthority, error) {
	return nil, ErrFixedDeliveryConflict
}

func OpenFixedEndpointClientAuthority(context.Context, string) (*FixedEndpointAuthority, error) {
	return nil, ErrFixedDeliveryConflict
}

func (*FixedEndpointAuthority) AdoptAuthenticatedClientStartRun(context.Context, application.RunStartProjection, FixedDeliveryReceipt) error {
	return ErrFixedDeliveryConflict
}

func (authority *FixedEndpointAuthority) withCurrent(context.Context, bool, func(*os.File) error) error {
	return ErrFixedDeliveryConflict
}
