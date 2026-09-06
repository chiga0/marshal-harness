package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// ApproveInitialTeam is a privileged application seam. It does not authenticate
// a transport on its own. The authenticated team route's input
// adapter must supply the authenticated operator request, not Worker output.
// Approval commits obligations only; Run creation is a later reconciliation.
func (session *RepositorySession) ApproveInitialTeam(ctx context.Context, request application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, error) {
	const operation = "approve-initial-team"
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonInvalidRequest)
	}
	frozen, requestDigest, err := request.Frozen()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	deadline, _ := time.Parse(time.RFC3339Nano, frozen.Deadline)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	borrow, err := session.borrow()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	defer borrow.Close()
	if session.teamInputPreflight == nil {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	// Full schema/Policy validation is outside the repository owner lock, but
	// inside the session lifetime. Give the injected pure validator a copy so
	// even an erroneous validator cannot replace approved bytes in the commit.
	validationCopy := bytes.Clone(frozen.Inputs)
	if session.teamInputPreflight(validationCopy) != nil || canonical.DigestBytes(validationCopy) != frozen.InputsDigest {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonInvalidRequest)
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: requestDigest, ExpectedHead: frozen.ExpectedHead}
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	plan, err := session.ingress.AcceptInitialTeamPlan(ctx, verifier, session.acquisition, approval, frozen.Inputs)
	if err != nil {
		if errors.Is(err, resultingress.ErrTeamPlanConflict) {
			return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonAuthorityConflict)
		}
		return application.InitialTeamApprovalProjection{}, err
	}
	return teamApprovalProjection(plan), nil
}

func teamApprovalProjection(plan resultingress.TeamPlanState) application.InitialTeamApprovalProjection {
	return application.InitialTeamApprovalProjection{
		GoalID: plan.Revision.GoalId, PlanRevision: plan.Revision.PlanRevision,
		InputsDigest: plan.Approval.InputsDigest, RequestDigest: plan.Approval.RequestDigest,
		FactDigest: plan.FactDigest, ObligationCount: len(plan.Materializations),
	}
}

// ReconcileInitialTeamApproval never appends or refreshes a deadline. A query
// after the original approval deadline may recover the exact committed fact.
func (session *RepositorySession) ReconcileInitialTeamApproval(ctx context.Context, request application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, bool, error) {
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonInvalidRequest)
	}
	frozen, digest, err := request.Frozen()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, false, err
	}
	var inputs goal.TeamInputs
	if json.Unmarshal(frozen.Inputs, &inputs) != nil || inputs.Spec.Validate() != nil {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, false, err
	}
	defer borrow.Close()
	if !inputs.Spec.AuthorityNamespaceId.Equal(session.acquisition.Scope.AuthorityNamespaceID) {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonAuthorityConflict)
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: digest, ExpectedHead: frozen.ExpectedHead}
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	var result application.InitialTeamApprovalProjection
	var found bool
	err = verifier.WithCurrentApprovedTeam(ctx, session.acquisition, approval, func() error {
		plan, exists, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, inputs.Spec.GoalId)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if plan.Approval != approval || !bytes.Equal(plan.Inputs, frozen.Inputs) {
			return application.NewError("reconcile-team-approval", application.ReasonAuthorityConflict)
		}
		result, found = teamApprovalProjection(plan), true
		return result.Validate()
	})
	return result, found, err
}

// This verifier cannot be constructed by an input adapter or deserialized from
// a request. It holds the real claimed owner and rechecks both physical root
// and RB1 owner before invoking the ledger transaction, which rechecks again.
type repositoryApprovedTeamVerifier struct {
	session  *RepositorySession
	approval resultingress.TeamPlanApproval
}

func (v repositoryApprovedTeamVerifier) WithCurrentApprovedTeam(ctx context.Context, owner resultingress.ControlOwnerAcquisition, approval resultingress.TeamPlanApproval, fn func() error) error {
	if ctx == nil || v.session == nil || fn == nil || owner != v.session.acquisition || approval != v.approval {
		return application.NewError("approve-initial-team", application.ReasonInvalidRequest)
	}
	return v.session.owner.WithCurrentOwnerLock(ctx, owner, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateFixedServerRoot(v.session.fixedRoot, 5); err != nil {
			return err
		}
		current, found, err := v.session.ingress.OpenOwner(owner.Scope)
		if err != nil {
			return err
		}
		if !found || current.Acquisition != owner || current.FactDigest != v.session.ownerState.FactDigest {
			return application.NewError("approve-initial-team", application.ReasonOwnerNotCurrent)
		}
		return fn()
	})
}
