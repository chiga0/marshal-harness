package productionruntime

import (
	"context"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// adoptCompletedProjectionMutation closes the release half of the same
// derived-projection mutation already admitted after PrepareRunStart. Release
// atomically swaps existing-worktree-bindings, changing runtime-v1 timestamps.
// Do not turn off root validation: join the current terminal ledger and exact
// projection bytes before updating only that directory's observation.
func (l *CompositionLedger) adoptCompletedProjectionMutation(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, terminal resultingress.AttemptAuthorityState) error {
	if l.sessionBorrow == nil {
		return nil // Standalone composition has no resident fixed-server root.
	}
	conflict := func() error {
		return application.NewError("adopt-completed-worktree-projection", application.ReasonAuthorityConflict)
	}
	session := l.sessionBorrow.session
	if ctx == nil || verifier == nil || session == nil || session.ingress != l.ingress || session.acquisition != acquisition ||
		!l.existingWorktreeEnabled || terminal.CommittedResultFactDigest == "" || terminal.BarrierDigest == "" ||
		terminal.ProcessTerminalDigest == "" || terminal.AllocationTerminalDigest == "" || terminal.SupervisorClosedDigest == "" ||
		terminal.CleanupReleasedDigest == "" || terminal.ExistingWorktreeReleaseReceiptDigest == "" {
		return conflict()
	}
	return verifier.WithCurrentOwnerLock(ctx, acquisition, func() error {
		owner, found, err := l.ingress.OpenOwner(acquisition.Scope)
		if err != nil || !found || owner.Acquisition != acquisition || owner.FactDigest != session.ownerState.FactDigest {
			return conflict()
		}
		current, found, err := l.ingress.AttemptState(terminal.Identity)
		if err != nil || !found || !reflect.DeepEqual(current, terminal) {
			return conflict()
		}
		projection, err := l.runs.ReadCurrentRunProjectionUnderLease(l.runLease)
		if err != nil || projection != read.Run || projection.RunID != terminal.Identity.RunID || projection.AttemptID != terminal.Identity.AttemptID {
			return conflict()
		}
		_, receipt, found, complete, err := l.ingress.CurrentExistingWorktreeRelease(terminal.Identity)
		if err != nil || !found || !complete || receipt.ReceiptDigest != terminal.ExistingWorktreeReleaseReceiptDigest {
			return conflict()
		}
		snapshot, err := l.ingress.CurrentExistingWorktreeSnapshot(terminal.Identity)
		if err != nil || snapshot.CurrentAttemptHeadDigest != terminal.HeadDigest || snapshot.CurrentAttemptRevision != terminal.Revision {
			return conflict()
		}
		if allocationcontrol.VerifyExistingWorktreeProjectionFromGraph(l.existingWorktreeGraph, snapshot) != nil || adoptFixedServerRuntimeMutation(&session.fixedRoot) != nil {
			return conflict()
		}
		after, found, err := l.ingress.OpenOwner(acquisition.Scope)
		if err != nil || !found || after != owner {
			return conflict()
		}
		return nil
	})
}
