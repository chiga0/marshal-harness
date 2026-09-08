package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// AcceptedTeamInput is a read-only snapshot of an upstream, not a bearer
// capability. Integration freezing must re-read current owner/plan/Run facts
// and compare these exact bytes before granting any creation obligation.
type AcceptedTeamInput struct {
	NodeID             string
	CreationFactDigest string
	Run                application.RunProjection
	Candidate          review.AcceptedCandidate
	AttemptsUsed       uint
	AcceptedAt         time.Time
}

// ReadAcceptedTeamInputs reads both upstreams from the original owner ledger
// and holds both Run leases until all captured inputs have been verified.
// It accepts selectors only, never client packets or claimed ACCEPTED states.
// Not-ready and a legitimately occupied Run are non-mutating observations.
func (session *RepositorySession) ReadAcceptedTeamInputs(ctx context.Context, goalID, integrationID, planFactDigest string) (result []AcceptedTeamInput, ready bool, resultErr error) {
	const operation = "read-accepted-team-inputs"
	if ctx == nil || domain.ValidateID(goalID) != nil || domain.ValidateID(integrationID) != nil || planFactDigest == "" {
		return nil, false, application.NewError(operation, application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return nil, false, err
	}
	defer borrow.Close()
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, false, err
	}
	reader := repositoryApprovedTeamVerifier{session: session}
	resultErr = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var err error
		ready, err = session.withAcceptedTeamInputsUnderOwner(ctx, goalID, integrationID, planFactDigest, validator, func(inputs []AcceptedTeamInput) error { result = inputs; return nil })
		return err
	})
	if resultErr != nil || !ready {
		return nil, false, resultErr
	}
	return result, true, nil
}

// Caller holds the session lifetime and current repository owner. The callback
// runs before either upstream lease is released, including a creation append.
func (session *RepositorySession) withAcceptedTeamInputsUnderOwner(ctx context.Context, goalID, integrationID, planFactDigest string, validator *contract.Validator, consume func([]AcceptedTeamInput) error) (ready bool, resultErr error) {
	const operation = "read-accepted-team-inputs"
	fail := func() error { return application.NewError(operation, application.ReasonAuthorityConflict) }
	var result []AcceptedTeamInput
	resultErr = func() (readErr error) {
		if err := session.ingress.RequireTaskNotStopped(session.acquisition.Scope, goalID); err != nil {
			return taskError(err)
		}
		plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, goalID)
		if err != nil {
			return err
		}
		if !found || plan.FactDigest != planFactDigest {
			return fail()
		}
		if _, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, goalID); err != nil {
			return err
		} else if halted {
			return application.NewError(operation, application.ReasonRecoveryRequired)
		}
		var inputs goal.TeamInputs
		if json.Unmarshal(plan.Inputs, &inputs) != nil {
			return fail()
		}
		roles := map[string]string{}
		for _, node := range inputs.Nodes {
			roles[node.NodeID] = node.Role
		}
		if roles[integrationID] != "integrate" {
			return fail()
		}
		var upstreams []string
		for _, edge := range inputs.Proposal.Edges {
			if edge.To == integrationID {
				if roles[edge.From] != "implement" {
					return fail()
				}
				upstreams = append(upstreams, edge.From)
			}
		}
		if len(upstreams) != 2 || upstreams[0] == upstreams[1] {
			return fail()
		}
		sort.Strings(upstreams)
		leases := make([]*runstore.Lease, 0, 2)
		creations := make([]resultingress.TeamRunCreationState, 0, 2)
		defer func() {
			for index := len(leases) - 1; index >= 0; index-- {
				readErr = errors.Join(readErr, leases[index].Release())
			}
		}()
		for _, nodeID := range upstreams {
			creation, found, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, goalID, nodeID)
			if err != nil || !found {
				return err
			}
			_, runID, err := goal.TeamNodeIDs(inputs.Proposal, nodeID)
			if err != nil || creation.RunID != runID || creation.PlanFactDigest != planFactDigest {
				return fail()
			}
			lease, err := session.runs.AcquireExisting(runID)
			if errors.Is(err, runstore.ErrLeaseHeld) {
				return nil
			}
			if err != nil {
				return err
			}
			leases = append(leases, lease)
			creations = append(creations, creation)
		}
		namespace, err := session.acquisition.Scope.AuthorityNamespaceID.Digest()
		if err != nil {
			return err
		}
		for index, lease := range leases {
			value, accepted, err := session.readAcceptedTeamRunUnderLease(ctx, lease, creations[index], inputs.BaseSHA, namespace, validator)
			if err != nil || !accepted {
				return err
			}
			result = append(result, value)
		}
		if err := session.ingress.RequireTaskNotStopped(session.acquisition.Scope, goalID); err != nil {
			return taskError(err)
		}
		ready = true
		return consume(result)
	}()
	return ready && resultErr == nil, resultErr
}

// Shared by integration preparation and final delivery. The caller owns the
// repository owner lock and this exact Run lease for the full operation.
func (session *RepositorySession) readAcceptedTeamRunUnderLease(ctx context.Context, lease *runstore.Lease, creation resultingress.TeamRunCreationState, base, namespace string, validator *contract.Validator) (AcceptedTeamInput, bool, error) {
	fail := func() (AcceptedTeamInput, bool, error) {
		return AcceptedTeamInput{}, false, application.NewError("read-accepted-team-run", application.ReasonAuthorityConflict)
	}
	if err := ctx.Err(); err != nil {
		return AcceptedTeamInput{}, false, err
	}
	authority, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return AcceptedTeamInput{}, false, err
	}
	if authority.Run.State != domain.StateAccepted {
		return AcceptedTeamInput{}, false, nil
	}
	state, err := runstore.InspectUnderLease(lease)
	if err != nil || state.BaseSHA != base || state.RunID != creation.RunID || state.Sequence != authority.Run.Sequence || state.CurrentAttemptID != authority.Run.AttemptID {
		return fail()
	}
	var frozen struct {
		Task json.RawMessage `json:"task"`
	}
	if json.Unmarshal(creation.Inputs, &frozen) != nil {
		return fail()
	}
	task, err := runstore.ReadFileUnderLease(lease, int64(len(frozen.Task)+1), "task-spec.json")
	if err != nil || !bytes.Equal(task, frozen.Task) {
		return fail()
	}
	events, truncated, err := runstore.ReadEventsUnderLease(lease)
	if err != nil || truncated || len(events) == 0 {
		return fail()
	}
	terminal := events[len(events)-1]
	read := func(limit int64, parts ...string) ([]byte, error) {
		return runstore.ReadFileUnderLease(lease, limit, parts...)
	}
	objective, err := session.taskObjectiveUnderOwner(ctx, creation)
	if err != nil {
		return fail()
	}
	if objective != nil {
		if len(events) < 2 {
			return fail()
		}
		verified := events[len(events)-2]
		if verified.Type != "verification.completed" || verified.Actor == nil || verified.Actor.Type != "system" || verified.Actor.ID != "marshal-verifier" || verified.RunID != state.RunID || verified.AttemptID != state.CurrentAttemptID || verified.Sequence+1 != state.Sequence || verified.StateFrom != domain.StateVerifying || verified.StateTo != domain.StateReviewPending {
			return fail()
		}
		objective.VerificationDigest, _ = verified.Payload["reportDigest"].(string)
		objective.ArtifactManifestDigest, _ = verified.Payload["artifactManifestDigest"].(string)
		objective.ReadEvidence = read
	}
	candidate, err := review.ReadAcceptedCandidateWithObjective(state, terminal, namespace, validator, read, objective)
	if err != nil {
		return fail()
	}
	return AcceptedTeamInput{NodeID: creation.NodeID, CreationFactDigest: creation.FactDigest, Run: authority.Run,
		Candidate: candidate, AttemptsUsed: state.AttemptsUsed, AcceptedAt: terminal.Timestamp}, true, nil
}
