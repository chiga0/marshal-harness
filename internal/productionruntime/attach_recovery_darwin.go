//go:build darwin && arm64

package productionruntime

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// recoverRunningAttempt is the sole production entry to ADR 0067 Attach and
// owner-successor rebind. NewCompositionLedger calls it while the exact phase-B
// repository owner remains held and before the Runtime can serve operations.
func (l *CompositionLedger) recoverRunningAttempt(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, owner resultingress.ControlOwnerState) error {
	_, attempt, running, err := l.currentRunningAttempt(ctx)
	if err != nil || !running {
		return err
	}
	// Once the supervisor is durably closed no further Attach/rebind is legal
	// or necessary. Cleanup successors and the Run successor are pure ledger
	// replays under current Run authority.
	if attempt.SupervisorClosedDigest != "" || attempt.CleanupReleasedDigest != "" {
		return nil
	}
	if runningAttemptBoundToOwner(attempt, owner) {
		return nil
	}
	rebound, err := l.ingress.RebindOwnerSuccessorForAttachedRecovery(ctx, verifier, acquisition, attempt.Identity)
	if err != nil {
		return application.NewError("recover-running-attempt", application.ReasonRecoveryRequired)
	}
	if !runningAttemptBoundToOwner(rebound, owner) && !runningAttemptReadyForCloseRecovery(rebound, owner) {
		return application.NewError("recover-running-attempt", application.ReasonAuthorityConflict)
	}
	return nil
}
