package productionruntime

import (
	"io"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

type ownerLockIdentity struct {
	Device uint64
	Inode  uint64
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
