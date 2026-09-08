package productionruntime

import (
	"bytes"
	"context"
	"errors"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// Only returned by the initial read before Git, Prepare or Run writes. Later
// loss of authority is not retryable through this sentinel.
var ErrTeamIntegrationWaiting = errors.New("team integration upstreams not ready")

func bindAcceptedTeamInputs(goalID, nodeID, planFact string, values []AcceptedTeamInput) resultingress.TeamIntegrationInputs {
	input := resultingress.TeamIntegrationInputs{GoalID: goalID, NodeID: nodeID, PlanFactDigest: planFact}
	for _, value := range values {
		input.BaseSHA = value.Candidate.Candidate.BaseSHA
		input.Sources = append(input.Sources, resultingress.TeamAcceptedSource{
			NodeID: value.NodeID, RunID: value.Run.RunID, AttemptID: value.Run.AttemptID, AuthorityHead: value.Run.AuthorityHead,
			CreationFactDigest: value.CreationFactDigest, CandidateDigest: value.Candidate.Candidate.CandidateDigest,
			PatchDigest: value.Candidate.Candidate.ContentDigest, DecisionDigest: value.Candidate.DecisionDigest,
			PacketDigest: value.Candidate.PacketDigest, OutcomeDigest: value.Candidate.OutcomeDigest,
		})
	}
	return input
}

// Called with the session lifetime held. Reuses original creation first, so
// cold recovery never refreshes capability, time, IDs or accepted sources.
func (session *RepositorySession) prepareIntegrationTeamRun(ctx context.Context, raw []byte, inputs goal.TeamInputs, approval resultingress.TeamPlanApproval, node goal.TeamNodeInputs) (resultingress.TeamRunCreationState, error) {
	fail := func() error {
		return application.NewError("prepare-team-integration", application.ReasonAuthorityConflict)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	reader := repositoryApprovedTeamVerifier{session: session, approval: approval}
	var plan resultingress.TeamPlanState
	var existing resultingress.TeamRunCreationState
	var sources []AcceptedTeamInput
	var found bool
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, approval, func() error {
		var present bool
		var err error
		plan, present, err = session.ingress.ReadTeamPlan(session.acquisition.Scope, inputs.Spec.GoalId)
		if err != nil {
			return err
		}
		if !present || plan.Approval != approval || !bytes.Equal(plan.Inputs, raw) {
			return fail()
		}
		ready, err := session.withAcceptedTeamInputsUnderOwner(ctx, inputs.Spec.GoalId, node.NodeID, plan.FactDigest, validator, func(values []AcceptedTeamInput) error { sources = values; return nil })
		if err != nil {
			return err
		}
		if !ready {
			return ErrTeamIntegrationWaiting
		}
		existing, found, err = session.ingress.ReadTeamRunCreation(session.acquisition.Scope, inputs.Spec.GoalId, node.NodeID)
		return err
	})
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	bound := bindAcceptedTeamInputs(inputs.Spec.GoalId, node.NodeID, plan.FactDigest, sources)
	digest, err := bound.Digest()
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	if found {
		if existing.Integration == nil || existing.Integration.InputsDigest != digest || existing.PlanFactDigest != plan.FactDigest {
			return resultingress.TeamRunCreationState{}, fail()
		}
		return existing, nil
	}
	if session.teamIntegrationBuilder == nil {
		return resultingress.TeamRunCreationState{}, application.NewError("prepare-team-integration", application.ReasonCompositionIncomplete)
	}
	patches := make([][]byte, len(sources))
	for index, source := range sources {
		patches[index] = bytes.Clone(source.Candidate.Patch)
	}
	// No owner or Run lease is held while bounded Git/Prepare commands run.
	tree, commit, err := session.teamIntegrationBuilder(ctx, inputs.BaseSHA, digest, patches)
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	task, err := resultingress.DeriveTeamIntegrationTask(node.Task, inputs.BaseSHA, commit)
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	_, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	prepared, err := session.teamRunPreparer(ctx, task, bytes.Clone(node.Policy), runID)
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	integration := resultingress.TeamIntegrationBase{Inputs: bound, InputsDigest: digest, TreeSHA: tree, CommitSHA: commit}
	verifier := repositoryAcceptedTeamVerifier{session: session, approval: approval}
	return session.ingress.FreezeIntegrationTeamRun(ctx, verifier, session.acquisition, approval, inputs.Spec.GoalId, node.NodeID, plan.FactDigest, prepared, integration)
}

type repositoryAcceptedTeamVerifier struct {
	session  *RepositorySession
	approval resultingress.TeamPlanApproval
}

func (v repositoryAcceptedTeamVerifier) WithCurrentAcceptedTeam(ctx context.Context, owner resultingress.ControlOwnerAcquisition, approval resultingress.TeamPlanApproval, input resultingress.TeamIntegrationInputs, fn func() error) error {
	validator, err := contract.NewValidator()
	if err != nil {
		return err
	}
	reader := repositoryApprovedTeamVerifier(v)
	return reader.WithCurrentApprovedTeam(ctx, owner, approval, func() error {
		return v.session.requireAcceptedTeamInputsUnderOwner(ctx, input, validator, fn)
	})
}

func (session *RepositorySession) requireAcceptedTeamInputsUnderOwner(ctx context.Context, expected resultingress.TeamIntegrationInputs, validator *contract.Validator, fn func() error) error {
	fail := func() error {
		return application.NewError("current-team-integration-inputs", application.ReasonAuthorityConflict)
	}
	ready, err := session.withAcceptedTeamInputsUnderOwner(ctx, expected.GoalID, expected.NodeID, expected.PlanFactDigest, validator, func(values []AcceptedTeamInput) error {
		actual := bindAcceptedTeamInputs(expected.GoalID, expected.NodeID, expected.PlanFactDigest, values)
		want, e1 := expected.Digest()
		got, e2 := actual.Digest()
		if e1 != nil || e2 != nil || got != want {
			return fail()
		}
		return fn()
	})
	if err != nil {
		return err
	}
	if !ready {
		return fail()
	}
	return nil
}

func (session *RepositorySession) withCurrentCreationInputsUnderOwner(ctx context.Context, creation resultingress.TeamRunCreationState, fn func() error) error {
	if err := session.ingress.RequireTaskNotStopped(session.acquisition.Scope, creation.GoalID); err != nil {
		return taskError(err)
	}
	if creation.Integration == nil {
		return fn()
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return err
	}
	return session.requireAcceptedTeamInputsUnderOwner(ctx, creation.Integration.Inputs, validator, fn)
}
