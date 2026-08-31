package productionruntime

import (
	"context"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// releaseExistingWorktree closes the path-B allocation without touching task
// bytes. The durable RB1 release union records disposition=preserved and the
// standard Attempt allocation-terminal fact is appended by the caller only
// after this receipt has fsynced.
func (l *CompositionLedger) releaseExistingWorktree(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, attempt resultingress.AttemptAuthorityState) (allocationcontrol.ExistingWorktreeReleaseReceiptV1, error) {
	if !l.existingWorktreeEnabled || l.existingWorktreeTarget == nil || attempt.ProcessTerminalDigest == "" || attempt.AllocationTerminalDigest != "" {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, application.NewError("release-existing-worktree", application.ReasonAuthorityConflict)
	}
	bindReceipt, err := l.ingress.CurrentExistingWorktreeBindReceipt(attempt.Identity)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	replayRequest, replayReceipt, replayFound, replayComplete, err := l.ingress.CurrentExistingWorktreeRelease(attempt.Identity)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	if replayComplete {
		return replayReceipt, nil
	}
	runAuthority, err := runstore.DupRunDirectory(l.runLease)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	defer runAuthority.Close()
	run, err := allocationcontrol.NewDescriptorBoundRunV1(attempt.Identity.RunID, runAuthority.File, read.Run.AuthorityHead)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	request := replayRequest
	if !replayFound {
		request = allocationcontrol.ExistingWorktreeReleaseRequestV1{
			Binding: bindReceipt.Binding, BindingReceiptDigest: bindReceipt.ReceiptDigest,
			TerminalizationID: attempt.TerminalizationID, CleanupBindingDigest: attempt.CleanupBindingDigest,
			ProcessTerminalFactDigest: attempt.ProcessTerminalDigest, CleanupDisposition: "preserved",
			RunAuthorityHeadDigest: read.Run.AuthorityHead, AttemptAuthorityHeadDigest: attempt.HeadDigest,
		}
		if err := request.Seal(); err != nil {
			return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
		}
	} else if request.BindingReceiptDigest != bindReceipt.ReceiptDigest || request.TerminalizationID != attempt.TerminalizationID || request.CleanupBindingDigest != attempt.CleanupBindingDigest || request.ProcessTerminalFactDigest != attempt.ProcessTerminalDigest || request.RunAuthorityHeadDigest != read.Run.AuthorityHead {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, application.NewError("release-existing-worktree", application.ReasonAuthorityConflict)
	}
	verifier := &existingWorktreeCurrentVerifier{ledger: l, scope: acquisition.Scope, graph: l.existingWorktreeGraph}
	authority, err := resultingress.NewExistingWorktreeAuthority(l.ingress, verifier)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	controller, err := allocationcontrol.NewExistingWorktreeController(authority)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, err
	}
	receipt, err := controller.Release(ctx, run, request)
	if err != nil {
		return allocationcontrol.ExistingWorktreeReleaseReceiptV1{}, fmt.Errorf("composition: existing-worktree release: %w", err)
	}
	return receipt, nil
}
