//go:build darwin && arm64

package resultingress

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// TerminatePreparedExecution consumes an already durable terminalization
// barrier. It cannot cancel eligibility itself, select a PID, or signal an
// unowned process. Exact pending receipts recover without a second signal.
func (s *DurableStore) TerminatePreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity) (PreparedExecutionTerminalObservation, error) {
	if s == nil || ctx == nil || verifier == nil || acquisition.Validate() != nil || acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionUnavailable
	}
	var result PreparedExecutionTerminalObservation
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			state, owner, directory, closeDirectory, err := s.preparedTerminalState(projection, acquisition, identity, nil, profile.fixedMarshalPath)
			if err != nil {
				return err
			}
			if closeDirectory {
				defer directory.Close()
			}
			result, err = s.observeTerminalPreparedExecutionV2Locked(ctx, projection, state, owner, identity, directory, profile.fixedMarshalPath,
				productionContinuationTransportV2, processsupervisor.ObservePreparedCommandV2, processsupervisor.CommandTerminate)
			return err
		})
	})
	return result, err
}
