package productionruntime

import (
	"bytes"
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// ApproveInitialTeam is a privileged application seam. It does not authenticate
// a transport on its own and is not registered as an HTTP route yet. The input
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
	return application.InitialTeamApprovalProjection{
		GoalID: plan.Revision.GoalId, PlanRevision: plan.Revision.PlanRevision,
		InputsDigest: plan.Approval.InputsDigest, RequestDigest: plan.Approval.RequestDigest,
		FactDigest: plan.FactDigest, ObligationCount: len(plan.Materializations),
	}, nil
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
