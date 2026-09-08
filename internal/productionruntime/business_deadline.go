package productionruntime

import (
	"context"
	"math"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// admitBusinessStart checks the original Run budget before reservation and
// again before launch. Expired READY tasks are pre-start recovery cases, not
// fake stopped Attempts; this gate appends no stop intent or terminal event.
func (l *CompositionLedger) admitBusinessStart(ctx context.Context, run application.RunProjection) error {
	budget, err := l.runs.ReadBusinessBudgetUnderLease(ctx, l.runLease)
	if err != nil || budget.Run != run || run.State != domain.StateReady || run.AttemptID != "" ||
		budget.CreatedAt.IsZero() || budget.RunTimeoutSeconds <= 0 || budget.RunTimeoutSeconds > math.MaxInt64/int64(time.Second) {
		return application.NewError("admit-business-start", application.ReasonAuthorityConflict)
	}
	expires := budget.CreatedAt.Add(time.Duration(budget.RunTimeoutSeconds) * time.Second)
	if !l.now().UTC().Before(expires) {
		return resultingress.ErrBusinessDeadlineExceeded
	}
	return nil
}

// ReconcileBusinessStop consumes only immutable sources under this Run's
// current owner and lease. The resident scheduler supplies no deadline/PID.
func (l *CompositionLedger) ReconcileBusinessStop(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, runID string) error {
	if l == nil || ctx == nil || ctx.Err() != nil || runID == "" {
		return application.NewError("reconcile-business-stop", application.ReasonInvalidRequest)
	}
	if _, err := l.CurrentOwner(ctx, verifier, acquisition); err != nil {
		return err
	}
	read, attempt, running, err := l.currentRunningAttempt(ctx)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	if read.Run.RunID != runID {
		return application.NewError("reconcile-business-stop", application.ReasonAuthorityConflict)
	}
	_, _, err = l.stopDueAttempt(ctx, verifier, acquisition, read, attempt)
	return err
}

func (l *CompositionLedger) currentBusinessDeadline(ctx context.Context, read runstore.RunStartAuthorityProjection, attempt resultingress.AttemptAuthorityState) (resultingress.BusinessDeadlineWitness, error) {
	budget, err := l.runs.ReadBusinessBudgetUnderLease(ctx, l.runLease)
	if err != nil || budget.Run != read.Run || attempt.Identity.RunID != read.Run.RunID || attempt.Identity.AttemptID != read.Run.AttemptID || attempt.ProcessStartedDigest == "" {
		return resultingress.BusinessDeadlineWitness{}, resultingress.ErrAttemptAuthorityConflict
	}
	return resultingress.SealBusinessDeadline(resultingress.BusinessDeadlineWitness{SpecDigest: budget.SpecDigest, CreationEventDigest: budget.CreationEventDigest, ProcessStartedFactDigest: attempt.ProcessStartedDigest, RunCreatedAt: budget.CreatedAt.UTC().Format(time.RFC3339Nano), ProcessStartedAt: attempt.ObservedAt, RunTimeoutSeconds: budget.RunTimeoutSeconds, AttemptTimeoutSeconds: budget.AttemptTimeoutSeconds})
}

// stopDueAttempt uses no caller-supplied deadline and never extends a budget
// during recovery. A committed result wins; an existing stop keeps its intent.
func (l *CompositionLedger) stopDueAttempt(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, read runstore.RunStartAuthorityProjection, attempt resultingress.AttemptAuthorityState) (application.CancelRunProjection, bool, error) {
	current, found, err := l.ingress.AttemptState(attempt.Identity)
	if err != nil || !found {
		return application.CancelRunProjection{}, false, resultingress.ErrAttemptAuthorityConflict
	}
	if current.CommittedResultFactDigest != "" {
		return application.CancelRunProjection{}, false, nil
	}
	if current.StopIntent != (resultingress.AttemptStopIntent{}) {
		result, err := l.finishStoppedAttempt(ctx, verifier, acquisition, read, current)
		return result, err == nil, err
	}
	witness, err := l.currentBusinessDeadline(ctx, read, current)
	if err != nil {
		return application.CancelRunProjection{}, false, err
	}
	expires, category, err := witness.Effective()
	if err != nil {
		return application.CancelRunProjection{}, false, err
	}
	now := l.now().UTC()
	if now.Before(expires) {
		return application.CancelRunProjection{}, false, nil
	}
	intent, err := resultingress.SealAttemptStopIntent(current.Identity, resultingress.AttemptStopIntent{RequestID: "deadline:" + canonical.DigestBytes([]byte(current.Identity.AttemptID))[7:], ExpectedSequence: read.Run.Sequence, ExpectedAuthorityHead: read.Run.AuthorityHead, OperatorUID: acquisition.OwnerUID, ObservedAt: now.Format(time.RFC3339Nano), Category: category, Deadline: witness})
	if err != nil {
		return application.CancelRunProjection{}, false, err
	}
	barrier, err := l.ingress.CompareAndAppendBarrier(ctx, l, current.Revision, current.HeadDigest, resultingress.BarrierAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: runAuthorityForAttempt(current.Identity)}, resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionTerminalizationBarrier, Identity: current.Identity, TerminalizationID: intent.IntentDigest, EligibilityTerminal: intent.Eligibility(), StopIntent: intent})
	if err != nil {
		return application.CancelRunProjection{}, false, err
	}
	result, err := l.finishStoppedAttempt(ctx, verifier, acquisition, read, barrier.State)
	return result, err == nil, err
}
