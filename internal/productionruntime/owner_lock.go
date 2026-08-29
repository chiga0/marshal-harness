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
// acquisition is current yet. acquireOwner keeps its provisional verifier
// private to the one direct DurableStore.AcquireOwner call, and
// bindAcquisition only yields the Phase B type after exact durable replay.
type repositoryOwnerScopeLock interface {
	io.Closer
	scope() resultingress.ControlOwnerScope
	identity() ownerLockIdentity
	acquireOwner(context.Context, *resultingress.DurableStore, uint64, string, resultingress.ControlOwnerAcquisition) (resultingress.ControlOwnerAppendResult, error)
	bindAcquisition(*resultingress.DurableStore) (repositoryOwnerLock, error)
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
