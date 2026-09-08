//go:build darwin && arm64

package productionruntime

import (
	"context"
	"errors"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// CancelTaskReadyReservation uses the original zero-side-effect producer and
// physical dispatch ledger. It creates no Attempt and preserves READY.
func (s *RepositorySession) CancelTaskReadyReservation(ctx context.Context, runID string, dispatchLedger *dispatch.LeaseLedger) (err error) {
	borrow, err := s.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	if !errors.Is(s.ingress.RequireTaskRunNotStopped(s.acquisition.Scope.AuthorityNamespaceID, runID), resultingress.ErrTaskStopped) {
		return application.NewError("cancel-task-ready", application.ReasonAuthorityConflict)
	}
	lease, err := s.runs.AcquireExisting(runID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	reservations, attempts, err := s.ingress.TaskRunExecution(s.acquisition.Scope.AuthorityNamespaceID, runID)
	if err != nil {
		return err
	}
	if len(attempts) != 0 {
		return application.NewError("cancel-task-ready", application.ReasonRecoveryRequired)
	}
	if len(reservations) == 0 {
		return nil
	}
	if len(reservations) != 1 {
		return application.NewError("cancel-task-ready", application.ReasonRecoveryRequired)
	}
	r := reservations[0]
	verifier, err := runstore.NewAttemptRunAuthorityVerifier(s.runs, lease, s.acquisition.Scope.AuthorityNamespaceID, r.Reservation.Ready.OrchestratorID)
	if err != nil {
		return err
	}
	zero, err := newZeroAttemptSideEffectVerifier(s.owner, s.acquisition, verifier, dispatchLedger, s.ingress)
	if err != nil {
		return err
	}
	_, err = s.ingress.CancelAttemptReservation(ctx, zero, r.ReservationFactDigest)
	return err
}
