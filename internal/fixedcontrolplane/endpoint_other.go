//go:build !darwin || !arm64

package fixedcontrolplane

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

type Endpoint struct{}
type AuthenticatedConnection struct{}

func OpenEndpoint(context.Context, *productionruntime.FixedEndpointAuthority) (*Endpoint, error) {
	return nil, ErrUnavailable
}

func (endpoint *Endpoint) Accept(context.Context) (*AuthenticatedConnection, error) {
	return nil, ErrUnavailable
}

func (endpoint *Endpoint) StopAccept() error { return nil }

func (endpoint *Endpoint) Close() error { return nil }

func Dial(context.Context, *productionruntime.FixedEndpointAuthority, RequestBinding) (*AuthenticatedConnection, error) {
	return nil, ErrUnavailable
}
