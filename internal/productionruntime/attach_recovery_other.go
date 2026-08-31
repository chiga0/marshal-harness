//go:build !darwin || !arm64

package productionruntime

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func (l *CompositionLedger) recoverRunningAttempt(ctx context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, owner resultingress.ControlOwnerState) error {
	_, attempt, running, err := l.currentRunningAttempt(ctx)
	if err != nil || !running || runningAttemptBoundToOwner(attempt, owner) {
		return err
	}
	return application.NewError("recover-running-attempt", application.ReasonRecoveryRequired)
}
