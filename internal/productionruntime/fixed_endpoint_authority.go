package productionruntime

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// FixedEndpointSnapshot is the secret-free current authority tuple consumed
// by the local control-plane transport. Locator strings remain host-local and
// are deliberately absent from durable business authority.
type FixedEndpointSnapshot struct {
	Acquisition            resultingress.ControlOwnerAcquisition
	OwnerFactDigest        string
	OwnerAcquisitionDigest string
	RepositoryDigest       string
	AuthorityRootDigest    string
	ControlPath            string
	FixedMarshalPath       string
}

// FixedEndpointAuthority is a session borrow over the one production owner
// and held control root. It does not create a second repository/ledger root.
type FixedEndpointAuthority struct {
	mu       sync.RWMutex
	borrow   *repositorySessionBorrow
	control  *os.File
	snapshot FixedEndpointSnapshot
	closed   bool
}

var _ io.Closer = (*FixedEndpointAuthority)(nil)

func (authority *FixedEndpointAuthority) Snapshot() FixedEndpointSnapshot {
	if authority == nil {
		return FixedEndpointSnapshot{}
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	if authority.closed {
		return FixedEndpointSnapshot{}
	}
	return authority.snapshot
}

func (authority *FixedEndpointAuthority) ControlDirectory() *os.File {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	if authority.closed {
		return nil
	}
	return authority.control
}

// Recheck proves the exact current owner, held root and fixed server process
// before or after every transport boundary.
func (authority *FixedEndpointAuthority) Recheck(ctx context.Context) error {
	return authority.withCurrent(ctx, false, nil)
}

// WithControlMutation authorizes one endpoint setup/teardown callback and then
// adopts only the resulting control-directory mutation observation. The
// callback remains responsible for exact socket/token object checks.
func (authority *FixedEndpointAuthority) WithControlMutation(ctx context.Context, fn func(*os.File) error) error {
	if fn == nil {
		return ErrFixedDeliveryConflict
	}
	return authority.withCurrent(ctx, true, fn)
}

func (authority *FixedEndpointAuthority) Close() error {
	if authority == nil {
		return nil
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return nil
	}
	authority.closed = true
	var result error
	if authority.control != nil {
		result = authority.control.Close()
		authority.control = nil
	}
	if authority.borrow != nil {
		if err := authority.borrow.Close(); result == nil {
			result = err
		}
		authority.borrow = nil
	}
	return result
}
