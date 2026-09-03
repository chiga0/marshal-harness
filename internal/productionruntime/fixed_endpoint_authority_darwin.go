//go:build darwin && arm64

package productionruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"golang.org/x/sys/unix"
)

type fixedEndpointClientState struct {
	root    fixedServerRoot
	ingress *resultingress.CurrentOwnerReadView
	scope   resultingress.ControlOwnerScope
}

func (state *fixedEndpointClientState) close() error {
	if state == nil {
		return nil
	}
	var result error
	if state.ingress != nil {
		result = state.ingress.Close()
		state.ingress = nil
	}
	result = errors.Join(result, state.root.close())
	return result
}

// OpenFixedEndpointClientAuthority opens a read-only view of the current
// resident owner and fixed endpoint. It never acquires the repository owner
// lock and never creates missing state, so a separate fixed Marshal client can
// authenticate to the one server without becoming a second writer.
func OpenFixedEndpointClientAuthority(ctx context.Context, repositoryPath string) (_ *FixedEndpointAuthority, err error) {
	if ctx == nil {
		return nil, ErrFixedDeliveryConflict
	}
	repository, err := OpenCanonicalRepositoryRoot(repositoryPath)
	if err != nil {
		return nil, err
	}
	defer repository.Close()
	root, err := openExistingFixedServerRoot(repository)
	if err != nil {
		return nil, err
	}
	client := &fixedEndpointClientState{root: root}
	fail := func(cause error) (*FixedEndpointAuthority, error) {
		_ = client.close()
		return nil, cause
	}
	ingressFD, err := unix.Openat(int(root.nodes[1].file.Fd()), ResultIngressDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return fail(ErrFixedDeliveryConflict)
	}
	ingressDirectory := os.NewFile(uintptr(ingressFD), "marshal-fixed-endpoint-client-ingress")
	if ingressDirectory == nil {
		_ = unix.Close(ingressFD)
		return fail(ErrFixedDeliveryConflict)
	}
	client.ingress, err = resultingress.OpenDarwinCurrentOwnerReadView(ingressDirectory)
	_ = ingressDirectory.Close()
	if err != nil {
		return fail(err)
	}
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: repositoryPath}
	repositoryDigest, err := namespace.Digest()
	if err != nil {
		return fail(err)
	}
	client.scope = resultingress.ControlOwnerScope{AuthorityNamespaceID: namespace, RepositoryIdentityDigest: repositoryDigest}
	current, found, err := client.ingress.OpenOwner(client.scope)
	if err != nil || !found || current.Acquisition.Validate() != nil {
		return fail(ErrFixedDeliveryConflict)
	}
	ownerDigest, err := resultingress.ControlOwnerAcquisitionDigest(current.Acquisition)
	if err != nil {
		return fail(err)
	}
	rootDigest, err := root.digest()
	if err != nil {
		return fail(err)
	}
	controlFD, err := unix.Dup(int(root.nodes[3].file.Fd()))
	if err != nil {
		return fail(ErrFixedDeliveryConflict)
	}
	unix.CloseOnExec(controlFD)
	control := os.NewFile(uintptr(controlFD), "marshal-fixed-endpoint-client-control")
	if control == nil {
		_ = unix.Close(controlFD)
		return fail(ErrFixedDeliveryConflict)
	}
	controlPath, err := descriptorPath(controlFD)
	if err != nil || controlPath != filepath.Join(repositoryPath, fixedServerRootComponents[0], fixedServerRootComponents[1], fixedServerRootComponents[2]) {
		_ = control.Close()
		return fail(ErrFixedDeliveryConflict)
	}
	endpointAuthority := &FixedEndpointAuthority{client: client, control: control, snapshot: FixedEndpointSnapshot{
		Acquisition: current.Acquisition, OwnerFactDigest: current.FactDigest,
		OwnerAcquisitionDigest: ownerDigest, RepositoryDigest: repositoryDigest,
		AuthorityRootDigest: rootDigest, ControlPath: controlPath,
		FixedMarshalPath: current.Acquisition.OwnerBinary.CanonicalPath,
	}}
	if err := endpointAuthority.Recheck(ctx); err != nil {
		_ = endpointAuthority.Close()
		return nil, err
	}
	return endpointAuthority, nil
}

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
	if authority.closed || authority.control == nil {
		return ErrFixedDeliveryConflict
	}
	if authority.client != nil {
		if mutate || fn != nil || authority.client.ingress == nil {
			return ErrFixedDeliveryConflict
		}
		client := authority.client
		if validateFixedServerRoot(client.root, len(client.root.nodes)) != nil {
			return ErrFixedDeliveryConflict
		}
		current, found, err := client.ingress.OpenOwner(client.scope)
		if err != nil || !found || current.Acquisition != authority.snapshot.Acquisition || current.FactDigest != authority.snapshot.OwnerFactDigest {
			return ErrFixedDeliveryConflict
		}
		rootDigest, err := client.root.digest()
		if err != nil || rootDigest != authority.snapshot.AuthorityRootDigest {
			return ErrFixedDeliveryConflict
		}
		current, found, err = client.ingress.OpenOwner(client.scope)
		if err != nil || !found || current.Acquisition != authority.snapshot.Acquisition || current.FactDigest != authority.snapshot.OwnerFactDigest {
			return ErrFixedDeliveryConflict
		}
		return nil
	}
	if authority.borrow == nil || authority.borrow.session == nil {
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
