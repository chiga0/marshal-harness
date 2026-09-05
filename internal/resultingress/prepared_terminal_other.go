//go:build !darwin || !arm64

package resultingress

import "context"

func (s *DurableStore) TerminatePreparedExecution(context.Context, CurrentOwnerLockVerifier, ControlOwnerAcquisition, AttemptIdentity) (PreparedExecutionTerminalObservation, error) {
	return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionUnavailable
}

func (s *DurableStore) InspectPreparedExecution(context.Context, CurrentOwnerLockVerifier, ControlOwnerAcquisition, AttemptIdentity) (PreparedExecutionTerminalObservation, error) {
	return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionUnavailable
}

func (s *DurableStore) ClosePreparedExecution(context.Context, CurrentOwnerLockVerifier, ControlOwnerAcquisition, AttemptIdentity) (PreparedExecutionClose, error) {
	return PreparedExecutionClose{}, ErrPreparedExecutionUnavailable
}
