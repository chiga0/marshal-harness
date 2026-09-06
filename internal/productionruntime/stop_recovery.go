package productionruntime

import (
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// ReconcileStoppedRun repairs only the exact already-committed terminal
// response/Outcome. It cannot create a stop intent, signal a process, or
// construct a Run runtime. A missing transport receipt is not missing intent.
func (session *RepositorySession) ReconcileStoppedRun(ctx context.Context, request application.CancelRunRequest) (result application.CancelRunProjection, found bool, resultErr error) {
	if ctx == nil || request.Validate() != nil {
		return result, false, application.NewError("reconcile-stopped-run", application.ReasonInvalidRequest)
	}
	return session.reconcileStoppedRun(ctx, request)
}

// ReconcileStoppedCurrentRun is the Collect response-loss path. The caller
// supplies the original Run authority, not a fabricated cancellation request.
// The stored stop intent supplies its own request ID after current-ledger
// selection; the shared terminal verifier still checks the original head.
func (session *RepositorySession) ReconcileStoppedCurrentRun(ctx context.Context, request application.CurrentRunRequest) (application.CancelRunProjection, bool, error) {
	if ctx == nil || application.CollectRunResultRequest(request).Validate() != nil {
		return application.CancelRunProjection{}, false, application.NewError("reconcile-stopped-run", application.ReasonInvalidRequest)
	}
	return session.reconcileStoppedRun(ctx, application.CancelRunRequest{CurrentRunRequest: request})
}

func (session *RepositorySession) reconcileStoppedRun(ctx context.Context, request application.CancelRunRequest) (result application.CancelRunProjection, found bool, resultErr error) {
	borrow, err := session.borrow()
	if err != nil {
		return result, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, borrow.Close()) }()
	lease, err := session.runs.AcquireExisting(request.RunID)
	if err != nil {
		return result, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	err = session.owner.WithCurrentOwnerLock(ctx, session.acquisition, func() error {
		owner, ok, err := session.ingress.OpenOwner(session.acquisition.Scope)
		if err != nil || !ok || owner.Acquisition != session.acquisition || owner.FactDigest != session.ownerState.FactDigest {
			return application.NewError("reconcile-stopped-run", application.ReasonOwnerNotCurrent)
		}
		read, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
		if err != nil {
			return err
		}
		if read.Run.State != domain.StateBlocked {
			return nil
		}
		if read.Run.RunID != request.RunID || read.Run.AttemptID != request.AttemptID {
			return application.NewError("reconcile-stopped-run", application.ReasonAuthorityConflict)
		}
		states, err := session.ingress.AttemptStates()
		if err != nil {
			return err
		}
		var terminal resultingress.AttemptAuthorityState
		matches := 0
		for _, state := range states {
			id := state.Identity
			if id.AuthorityNamespaceID == session.acquisition.Scope.AuthorityNamespaceID && id.TaskID == read.Run.TaskID && id.RunID == request.RunID && id.AttemptID == request.AttemptID {
				terminal, matches = state, matches+1
			}
		}
		if matches != 1 {
			return application.NewError("reconcile-stopped-run", application.ReasonAuthorityConflict)
		}
		if request.RequestID == "" {
			request.RequestID = terminal.StopIntent.RequestID
		}
		result, err = rehydrateStoppedRunUnderLease(ctx, session.runs, lease, session.ingress, request, terminal)
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return application.CancelRunProjection{}, false, err
	}
	return result, found, nil
}
