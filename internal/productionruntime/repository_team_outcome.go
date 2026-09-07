package productionruntime

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type repositoryCompletedTeamVerifier struct{ session *RepositorySession }

func projectTeamOutcome(value resultingress.TeamDeliveryOutcome) (application.InitialTeamOutcomeProjection, error) {
	projection := application.InitialTeamOutcomeProjection{Outcome: value.Outcome, PlanFactDigest: value.PlanFactDigest, FactDigest: value.FactDigest,
		IntegrationRunID: value.Integration.RunID, CandidateDigest: value.Integration.CandidateDigest, PatchDigest: value.Integration.PatchDigest,
		IntegrationBaseSHA: value.IntegrationBaseSHA, AttemptsUsed: value.AttemptsUsed, Measurement: value.Measurement}
	return projection, projection.Validate()
}

func (v repositoryCompletedTeamVerifier) WithCurrentCompletedTeam(ctx context.Context, owner resultingress.ControlOwnerAcquisition, approval resultingress.TeamPlanApproval, goalID, planFact string, consume func(resultingress.TeamDeliveryOutcome) error) error {
	session := v.session
	reader := repositoryApprovedTeamVerifier{session: session, approval: approval}
	return reader.WithCurrentApprovedTeam(ctx, owner, approval, func() error {
		fail := func() error {
			return application.NewError("complete-initial-team", application.ReasonAuthorityConflict)
		}
		plan, found, err := session.ingress.ReadTeamPlan(owner.Scope, goalID)
		if err != nil {
			return err
		}
		if !found || plan.FactDigest != planFact || plan.Approval != approval {
			return fail()
		}
		// Find the frozen integrate creation, never a caller-selected Run.
		var integration resultingress.TeamRunCreationState
		for _, obligation := range plan.Materializations {
			creation, found, err := session.ingress.ReadTeamRunCreation(owner.Scope, goalID, obligation.NodeID)
			if err != nil {
				return err
			}
			if found && creation.Integration != nil {
				if integration.RunID != "" {
					return fail()
				}
				integration = creation
			}
		}
		if integration.RunID == "" {
			return resultingress.ErrTeamOutcomeNotReady
		}
		validator, err := contract.NewValidator()
		if err != nil {
			return err
		}
		ready, err := session.withAcceptedTeamInputsUnderOwner(ctx, goalID, integration.NodeID, planFact, validator, func(upstreams []AcceptedTeamInput) (callbackErr error) {
			bound := bindAcceptedTeamInputs(goalID, integration.NodeID, planFact, upstreams)
			digest, err := bound.Digest()
			if err != nil || digest != integration.Integration.InputsDigest {
				return fail()
			}
			lease, err := session.runs.AcquireExisting(integration.RunID)
			if errors.Is(err, runstore.ErrLeaseHeld) {
				return resultingress.ErrTeamOutcomeNotReady
			}
			if err != nil {
				return err
			}
			defer func() { callbackErr = errors.Join(callbackErr, lease.Release()) }()
			namespace, err := owner.Scope.AuthorityNamespaceID.Digest()
			if err != nil {
				return err
			}
			final, accepted, err := session.readAcceptedTeamRunUnderLease(ctx, lease, integration, integration.Integration.CommitSHA, namespace, validator)
			if err != nil {
				return err
			}
			if !accepted {
				return resultingress.ErrTeamOutcomeNotReady
			}
			attempts := int64(final.AttemptsUsed)
			if final.AttemptsUsed != 1 {
				return fail()
			}
			for _, upstream := range upstreams {
				if upstream.AttemptsUsed != 1 || upstream.AcceptedAt.After(final.AcceptedAt) {
					return fail()
				}
				attempts += int64(upstream.AttemptsUsed)
			}
			revision, err := plan.Revision.Digest()
			if err != nil {
				return err
			}
			finalBinding := bindAcceptedTeamInputs(goalID, integration.NodeID, planFact, []AcceptedTeamInput{final})
			return consume(resultingress.TeamDeliveryOutcome{
				Outcome: goal.GoalOutcome{AuthorityNamespaceId: owner.Scope.AuthorityNamespaceID, GoalId: goalID,
					State: goal.OutcomeStateCompleted, Reason: "verified-team-delivery", FinalPlanDigest: revision,
					BudgetDigest: plan.Revision.BudgetSnapshotDigest, FinalizedAt: final.AcceptedAt.UTC().Format(time.RFC3339Nano)},
				PlanFactDigest: planFact, Upstreams: bound.Sources, Integration: finalBinding.Sources[0],
				IntegrationBaseSHA: integration.Integration.CommitSHA, AttemptsUsed: attempts, Measurement: "attempt-counts-only",
			})
		})
		if err != nil {
			return err
		}
		if !ready {
			return resultingress.ErrTeamOutcomeNotReady
		}
		return nil
	})
}

// FinalizeReadyInitialTeams is called by the existing resident writer lane.
// It resumes only a missing completion append. It cannot start/retry Workers,
// run verification, alter a Decision, refund a budget or extend a deadline.
func (session *RepositorySession) FinalizeReadyInitialTeams(ctx context.Context) error {
	if ctx == nil {
		return application.NewError("complete-initial-teams", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	plans, err := session.ingress.ListTeamPlans(session.acquisition.Scope)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, done, err := session.ingress.ReadTeamOutcome(session.acquisition.Scope, plan.Revision.GoalId)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		_, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, plan.Revision.GoalId)
		if err != nil {
			return err
		}
		if halted {
			continue
		}
		_, err = session.ingress.CompleteTeam(ctx, repositoryCompletedTeamVerifier{session}, session.acquisition, plan.Approval, plan.Revision.GoalId, plan.FactDigest)
		if errors.Is(err, resultingress.ErrTeamOutcomeNotReady) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Read-only. In particular, polling this method is not an alternate finalizer.
func (session *RepositorySession) ReadInitialTeamOutcome(ctx context.Context, request application.ApproveInitialTeamRequest) (result application.InitialTeamOutcomeProjection, found bool, err error) {
	approval, approved, err := session.ReconcileInitialTeamApproval(ctx, request)
	if err != nil || !approved {
		return result, false, err
	}
	borrow, err := session.borrow()
	if err != nil {
		return result, false, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: session}
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		value, present, err := session.ingress.ReadTeamOutcome(session.acquisition.Scope, approval.GoalID)
		if err != nil || !present {
			return err
		}
		if value.PlanFactDigest != approval.FactDigest {
			return application.NewError("read-team-outcome", application.ReasonAuthorityConflict)
		}
		result, err = projectTeamOutcome(value)
		found = err == nil
		return err
	})
	return result, found && err == nil, err
}
