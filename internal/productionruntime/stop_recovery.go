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

// ReconcileStoppedCurrentRun joins either an original Collect replay or a new
// Collect bound to the current stopped Run. It does not manufacture a cancel:
// the stored intent supplies the original request for terminal verification.
func (session *RepositorySession) ReconcileStoppedCurrentRun(ctx context.Context, request application.CurrentRunRequest) (application.CancelRunProjection, bool, error) {
	if ctx == nil || application.CollectRunResultRequest(request).Validate() != nil {
		return application.CancelRunProjection{}, false, application.NewError("reconcile-stopped-run", application.ReasonInvalidRequest)
	}
	return session.reconcileStoppedRun(ctx, application.CancelRunRequest{CurrentRunRequest: request})
}

// RecoverStoppedRun is resident recovery, not a new cancel request. Both the
// selected Attempt and its original request come from current stored facts.
func (session *RepositorySession) RecoverStoppedRun(ctx context.Context, runID string) (application.CancelRunProjection, bool, error) {
	if ctx == nil || (application.InspectRunRequest{RunID: runID}).Validate() != nil {
		return application.CancelRunProjection{}, false, application.NewError("recover-stopped-run", application.ReasonInvalidRequest)
	}
	return session.reconcileStoppedRun(ctx, application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: runID}})
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
		deriveStoredRequest := request.AttemptID == ""
		if deriveStoredRequest {
			request.AttemptID = read.Run.AttemptID
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
		if deriveStoredRequest && (matches == 0 || matches == 1 && terminal.StopIntent == (resultingress.AttemptStopIntent{})) {
			return nil
		}
		if matches != 1 {
			return application.NewError("reconcile-stopped-run", application.ReasonAuthorityConflict)
		}
		if deriveStoredRequest {
			request.ExpectedSequence = terminal.StopIntent.ExpectedSequence
			request.ExpectedAuthorityHead = terminal.StopIntent.ExpectedAuthorityHead
		}
		request = bindStoppedReadToOriginalIntent(read.Run, request, terminal.StopIntent)
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

// Only an exact current terminal read can be translated to the stored intent.
// Explicit cancel requests and original pending replays keep their own heads.
// The caller still runs rehydrateStoppedRunUnderLease's complete durable checks.
func bindStoppedReadToOriginalIntent(current application.RunProjection, request application.CancelRunRequest, intent resultingress.AttemptStopIntent) application.CancelRunRequest {
	if request.RequestID == "" && current.State == domain.StateBlocked && currentMatchesDelivery(current, request.CurrentRunRequest) {
		request.ExpectedSequence = intent.ExpectedSequence
		request.ExpectedAuthorityHead = intent.ExpectedAuthorityHead
	}
	return request
}
