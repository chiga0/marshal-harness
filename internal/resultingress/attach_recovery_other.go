//go:build !darwin || !arm64

package resultingress

import "context"

// RebindOwnerSuccessorForAttachedRecovery is unavailable on non-darwin/arm64
// platforms: the read-only Attach primitive and the fixed Darwin process
// supervisor are darwin/arm64-only. Calls fail closed with
// ErrPreparedExecutionConflict rather than attempting a cross-platform stub.
func (s *DurableStore) RebindOwnerSuccessorForAttachedRecovery(_ context.Context, _ CurrentOwnerLockVerifier, _ ControlOwnerAcquisition, _ AttemptIdentity) (AttemptAuthorityState, error) {
	return AttemptAuthorityState{}, ErrPreparedExecutionConflict
}
