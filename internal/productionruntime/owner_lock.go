package productionruntime

import (
	"context"
	"io"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

type ownerLockIdentity struct {
	Device uint64
	Inode  uint64
}

// repositoryOwnerScopeLock is the Phase A repository lock. It deliberately
// does not implement resultingress.CurrentOwnerLockVerifier: no durable owner
// acquisition is current yet. acquireAndBind keeps its provisional verifier
// private to the one direct DurableStore.AcquireOwner call and only yields the
// Phase B type after exact durable replay in the same physical critical
// section.
type repositoryOwnerScopeLock interface {
	io.Closer
	scope() resultingress.ControlOwnerScope
	identity() ownerLockIdentity
	acquireAndBind(context.Context, *resultingress.DurableStore, resultingress.ControlOwnerAcquisition) (repositoryOwnerLock, resultingress.ControlOwnerState, resultingress.ControlOwnerAcquisition, error)
}

// repositoryOwnerLock is both the process-lifetime lock and the exact verifier
// consumed by ResultIngress. It is not an authority fact or bearer capability.
type repositoryOwnerLock interface {
	resultingress.CurrentOwnerLockVerifier
	io.Closer
	acquisition() resultingress.ControlOwnerAcquisition
	identity() ownerLockIdentity
	claimRuntime() error
	claimed() bool
}
