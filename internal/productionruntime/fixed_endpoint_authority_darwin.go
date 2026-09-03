//go:build darwin && arm64

package productionruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/resultingress"
	"golang.org/x/sys/unix"
)

func (session *RepositorySession) OpenFixedEndpointAuthority(ctx context.Context) (*FixedEndpointAuthority, error) {
	borrow, err := session.borrow()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*FixedEndpointAuthority, error) {
		_ = borrow.Close()
		return nil, err
	}
	if ctx == nil || session.fixedRoot.nodes[3].file == nil || session.fixedPath == "" {
		return fail(ErrFixedDeliveryConflict)
	}
	fd, err := unix.Dup(int(session.fixedRoot.nodes[3].file.Fd()))
	if err != nil {
		return fail(ErrFixedDeliveryConflict)
	}
	unix.CloseOnExec(fd)
	control := os.NewFile(uintptr(fd), "marshal-fixed-endpoint-control")
	if control == nil {
		_ = unix.Close(fd)
		return fail(ErrFixedDeliveryConflict)
	}
	authority := &FixedEndpointAuthority{borrow: borrow, control: control}
	if err := authority.initialize(ctx); err != nil {
		_ = authority.Close()
		return nil, err
	}
	return authority, nil
}

func (authority *FixedEndpointAuthority) initialize(ctx context.Context) error {
	if authority == nil || authority.borrow == nil || authority.borrow.session == nil || authority.control == nil || ctx == nil {
		return ErrFixedDeliveryConflict
	}
	session := authority.borrow.session
	ownerDigest, err := resultingress.ControlOwnerAcquisitionDigest(session.acquisition)
	if err != nil {
		return err
	}
	rootDigest, err := session.fixedRoot.digest()
	if err != nil {
		return err
	}
	controlPath, err := descriptorPath(int(authority.control.Fd()))
	expectedPath := filepath.Join(session.fixedRoot.repositoryPath, fixedServerRootComponents[0], fixedServerRootComponents[1], fixedServerRootComponents[2])
	if err != nil || controlPath != expectedPath {
		return ErrFixedDeliveryConflict
	}
	authority.snapshot = FixedEndpointSnapshot{
		Acquisition: session.acquisition, OwnerFactDigest: session.ownerState.FactDigest,
		OwnerAcquisitionDigest: ownerDigest, RepositoryDigest: session.acquisition.Scope.RepositoryIdentityDigest,
		AuthorityRootDigest: rootDigest, ControlPath: controlPath, FixedMarshalPath: session.fixedPath,
	}
	return authority.Recheck(ctx)
}

func (authority *FixedEndpointAuthority) withCurrent(ctx context.Context, mutate bool, fn func(*os.File) error) error {
	if authority == nil || ctx == nil {
		return ErrFixedDeliveryConflict
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	if authority.closed || authority.borrow == nil || authority.borrow.session == nil || authority.control == nil {
		return ErrFixedDeliveryConflict
	}
	session := authority.borrow.session
	return session.owner.WithCurrentOwnerLock(ctx, session.acquisition, func() error {
		current, found, err := session.ingress.OpenOwner(session.acquisition.Scope)
		if err != nil || !found || current.Acquisition != session.acquisition || current.FactDigest != session.ownerState.FactDigest {
			return ErrFixedDeliveryConflict
		}
		if validateFixedServerRoot(session.fixedRoot, len(session.fixedRoot.nodes)) != nil {
			return ErrFixedDeliveryConflict
		}
		var callbackErr error
		if fn != nil {
			callbackErr = fn(authority.control)
		}
		if mutate {
			if err := adoptFixedServerControlMutation(&session.fixedRoot); err != nil {
				return errors.Join(callbackErr, err)
			}
		}
		rootDigest, err := session.fixedRoot.digest()
		if err != nil || rootDigest != authority.snapshot.AuthorityRootDigest {
			return ErrFixedDeliveryConflict
		}
		current, found, err = session.ingress.OpenOwner(session.acquisition.Scope)
		if err != nil || !found || current.Acquisition != session.acquisition || current.FactDigest != session.ownerState.FactDigest {
			return ErrFixedDeliveryConflict
		}
		return callbackErr
	})
}
