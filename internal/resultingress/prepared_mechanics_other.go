//go:build !darwin || !arm64

package resultingress

import "context"

func (s *DurableStore) reconcilePreparedExecutionLocked(context.Context, *Ingress, PreparedExecutionV1, AttemptAuthorityState) (AttemptAuthorityState, error) {
	return AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
}
