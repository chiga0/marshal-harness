package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

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
	NodeID    string
	Run       application.RunProjection
	Candidate review.AcceptedCandidate
}

// ReadAcceptedTeamInputs reads both upstreams from the original owner ledger
// and holds both Run leases until all captured inputs have been verified.
// It accepts selectors only, never client packets or claimed ACCEPTED states.
// Not-ready and a legitimately occupied Run are non-mutating observations.
func (session *RepositorySession) ReadAcceptedTeamInputs(ctx context.Context, goalID, integrationID, planFactDigest string) (result []AcceptedTeamInput, ready bool, resultErr error) {
	const operation = "read-accepted-team-inputs"
	fail := func() error { return application.NewError(operation, application.ReasonAuthorityConflict) }
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
	resultErr = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() (readErr error) {
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
			if err := ctx.Err(); err != nil {
				return err
			}
			authority, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
			if err != nil {
				return err
			}
			if authority.Run.State != domain.StateAccepted {
				return nil
			}
			state, err := runstore.InspectUnderLease(lease)
			if err != nil || state.BaseSHA != inputs.BaseSHA || state.RunID != creations[index].RunID {
				return fail()
			}
			var frozen struct {
				Task json.RawMessage `json:"task"`
			}
			if json.Unmarshal(creations[index].Inputs, &frozen) != nil {
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
			candidate, err := review.ReadAcceptedCandidate(state, events[len(events)-1], namespace, validator,
				func(limit int64, parts ...string) ([]byte, error) {
					return runstore.ReadFileUnderLease(lease, limit, parts...)
				})
			if err != nil {
				return fail()
			}
			result = append(result, AcceptedTeamInput{NodeID: upstreams[index], Run: authority.Run, Candidate: candidate})
		}
		ready = true
		return nil
	})
	if resultErr != nil || !ready {
		return nil, false, resultErr
	}
	return result, true, nil
}
