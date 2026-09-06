package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const teamRunCreationFactType = "team-run-inputs-frozen"
const teamRunCreationProtocol = "bounded-team-run-creation/v1"
const maxTeamRunCreationBytes = 256 << 10

var ErrTeamRunCreationConflict = errors.New("resultingress: team Run creation conflict")

// TeamRunCreationState is the creating obligation, not READY or a Start fact.
// Inputs retain the exact PreparedInputs chosen before any Run side effects.
type TeamRunCreationState struct {
	GoalID         string          `json:"goalId"`
	NodeID         string          `json:"nodeId"`
	RunID          string          `json:"runId"`
	PlanFactDigest string          `json:"planFactDigest"`
	Inputs         json.RawMessage `json:"inputs"`
	InputsDigest   string          `json:"inputsDigest"`
	FactDigest     string          `json:"factDigest"`
}

type teamRunCreationFact struct {
	ProtocolRevision string               `json:"protocolRevision"`
	FactType         string               `json:"factType"`
	Sequence         int64                `json:"sequence"`
	Scope            ControlOwnerScope    `json:"scope"`
	OwnerFactDigest  string               `json:"ownerFactDigest"`
	Creation         TeamRunCreationState `json:"creation"`
	Digest           string               `json:"digest"`
}

// This ledger read shape deliberately has no planning dependency (planning
// already reaches RB1 through runstore). Raw Task/Policy/Capability documents
// stay lossless. The trusted Prepare producer owns their full schema/probe
// checks; replay independently enforces the approved template and identity.
type teamPreparedInputs struct {
	RunID             string          `json:"runId"`
	RepositoryRoot    string          `json:"repositoryRoot"`
	BaseSHA           string          `json:"baseSha"`
	PreparedAt        time.Time       `json:"preparedAt"`
	Task              json.RawMessage `json:"task"`
	Policy            json.RawMessage `json:"policy"`
	Capability        json.RawMessage `json:"capability"`
	SelectionAttempts []struct {
		AdapterID string
		Outcome   string
	} `json:"selectionAttempts"`
}

// FreezeInitialTeamRun binds a process-local validated Prepare result to an
// already approved, dependency-free implement node. The production caller
// must supply its immutable composition's actual Prepare result, never
// unvalidated client/Worker bytes. The current-owner verifier holds the same
// owner while RB1 replays and commits. No Run/worktree/Attempt is created here.
func (s *DurableStore) FreezeInitialTeamRun(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, approval TeamPlanApproval, goalID, nodeID, planFactDigest string, raw []byte) (TeamRunCreationState, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || domain.ValidateID(goalID) != nil ||
		domain.ValidateID(nodeID) != nil || requireDigest("team plan", planFactDigest) != nil ||
		len(raw) == 0 || len(raw) > maxTeamRunCreationBytes {
		return TeamRunCreationState{}, ErrTeamRunCreationConflict
	}
	frozen, err := canonical.JSON(raw)
	if err != nil {
		return TeamRunCreationState{}, ErrTeamRunCreationConflict
	}
	var result TeamRunCreationState
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier, approval}, owner, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, ok := projection.controlOwners[ownerKey]
			if !ok || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			plan, ok := projection.teamPlans[teamPlanKey(owner.Scope, goalID)]
			if !ok || plan.Approval != approval {
				return ErrTeamRunCreationConflict
			}
			candidate := TeamRunCreationState{GoalID: goalID, NodeID: nodeID, PlanFactDigest: planFactDigest, Inputs: frozen, InputsDigest: canonical.DigestBytes(frozen)}
			key, runID, err := validateTeamRunCreation(owner.Scope, plan, candidate)
			if err != nil {
				return err
			}
			candidate.RunID = runID
			if previous, ok := projection.teamRunCreations[key]; ok {
				if previous.PlanFactDigest != candidate.PlanFactDigest || !bytes.Equal(previous.Inputs, candidate.Inputs) {
					return ErrTeamRunCreationConflict
				}
				result = previous
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &teamRunCreationFact{ProtocolRevision: teamRunCreationProtocol, FactType: teamRunCreationFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Creation: candidate}
			encoded, err := json.Marshal(fact)
			if err != nil || len(encoded)+100 > maxTeamRunCreationBytes+4096 {
				return ErrTeamRunCreationConflict
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(digest string) { fact.Digest = digest }); err != nil {
				return err
			}
			s.nextSequence++
			candidate.FactDigest = fact.Digest
			projection.teamRunCreations[key] = candidate
			result = candidate
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) ReadTeamRunCreation(scope ControlOwnerScope, goalID, nodeID string) (TeamRunCreationState, bool, error) {
	if scope.Validate() != nil || domain.ValidateID(goalID) != nil || domain.ValidateID(nodeID) != nil {
		return TeamRunCreationState{}, false, ErrTeamRunCreationConflict
	}
	projection := newAuthorityProjection()
	var result TeamRunCreationState
	var found bool
	err := s.transact(projection, func() error {
		result, found = projection.teamRunCreations[teamRunCreationKey(scope, goalID, nodeID)]
		return nil
	})
	return result, found, err
}

// TeamCreationObligation joins existing committed facts, not directory names
// or a newly supplied approval. It grants neither Start nor a new reservation.
type TeamCreationObligation struct {
	Plan     TeamPlanState
	Creation TeamRunCreationState
}

// ListTeamCreationObligations replays once and returns only this exact owner
// scope's frozen creations. The caller must still hold/recheck current owner
// and both facts before performing each recovery mutation.
func (s *DurableStore) ListTeamCreationObligations(scope ControlOwnerScope) ([]TeamCreationObligation, error) {
	if scope.Validate() != nil {
		return nil, ErrTeamRunCreationConflict
	}
	projection := newAuthorityProjection()
	var result []TeamCreationObligation
	err := s.transact(projection, func() error {
		for key, creation := range projection.teamRunCreations {
			if key != teamRunCreationKey(scope, creation.GoalID, creation.NodeID) {
				continue
			}
			plan, found := projection.teamPlans[teamPlanKey(scope, creation.GoalID)]
			if !found || plan.FactDigest != creation.PlanFactDigest {
				return ErrTeamRunCreationConflict
			}
			result = append(result, TeamCreationObligation{Plan: plan, Creation: creation})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(a, b TeamCreationObligation) int {
		if n := strings.Compare(a.Creation.GoalID, b.Creation.GoalID); n != 0 {
			return n
		}
		return strings.Compare(a.Creation.NodeID, b.Creation.NodeID)
	})
	return result, nil
}

func teamRunCreationKey(scope ControlOwnerScope, goalID, nodeID string) string {
	return canonicalDigestOrEmpty(struct {
		Scope          ControlOwnerScope
		GoalID, NodeID string
	}{scope, goalID, nodeID})
}

func validateTeamRunCreation(scope ControlOwnerScope, plan TeamPlanState, creation TeamRunCreationState) (string, string, error) {
	fail := func() (string, string, error) { return "", "", ErrTeamRunCreationConflict }
	if scope.Validate() != nil || plan.FactDigest == "" || creation.PlanFactDigest != plan.FactDigest || creation.GoalID != plan.Revision.GoalId ||
		domain.ValidateID(creation.NodeID) != nil || len(creation.Inputs) == 0 || len(creation.Inputs) > maxTeamRunCreationBytes ||
		creation.InputsDigest != canonical.DigestBytes(creation.Inputs) {
		return fail()
	}
	var inputs goal.TeamInputs
	var prepared teamPreparedInputs
	if decodeTeamRecord(plan.Inputs, &inputs) != nil || decodeTeamRecord(creation.Inputs, &prepared) != nil ||
		prepared.RepositoryRoot != inputs.Spec.Repository || prepared.BaseSHA != inputs.BaseSHA || prepared.PreparedAt.IsZero() ||
		len(prepared.Task)+len(prepared.Policy) > goal.MaxTeamNodeBytes || len(prepared.Capability) == 0 || len(prepared.Capability) > 64<<10 ||
		len(prepared.SelectionAttempts) != 1 || prepared.SelectionAttempts[0].AdapterID != "pi" || prepared.SelectionAttempts[0].Outcome != "selected" {
		return fail()
	}
	// PreparedAt has one UTC encoding. A changed time on retry is new input,
	// never permission to refresh the original preparation implicitly.
	encodedTime, err := prepared.PreparedAt.MarshalJSON()
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(creation.Inputs, &fields) != nil || !bytes.Equal(encodedTime, fields["preparedAt"]) || prepared.PreparedAt.Location() != time.UTC {
		return fail()
	}
	for _, edge := range inputs.Proposal.Edges {
		if edge.To == creation.NodeID {
			return fail()
		}
	}
	for _, node := range inputs.Nodes {
		if node.NodeID != creation.NodeID {
			continue
		}
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil || node.Role != "implement" || prepared.RunID != runID || (creation.RunID != "" && creation.RunID != runID) ||
			!bytes.Equal(node.Task, prepared.Task) || !bytes.Equal(node.Policy, prepared.Policy) {
			return fail()
		}
		var task domain.TaskSpec
		var policy struct {
			TaskID string `json:"taskId"`
			RunID  string `json:"runId"`
		}
		var capability struct {
			AdapterID   string `json:"adapterId"`
			ProbeStatus string `json:"probeStatus"`
		}
		if json.Unmarshal(prepared.Task, &task) != nil || task.Metadata.ID != taskID || task.Repository.Path != prepared.RepositoryRoot || task.Repository.BaseRef != prepared.BaseSHA ||
			json.Unmarshal(prepared.Policy, &policy) != nil || policy.TaskID != taskID || policy.RunID != runID ||
			json.Unmarshal(prepared.Capability, &capability) != nil || capability.AdapterID != "pi" || capability.ProbeStatus != "supported" {
			return fail()
		}
		return teamRunCreationKey(scope, creation.GoalID, creation.NodeID), runID, nil
	}
	return fail()
}

func applyTeamRunCreationLine(line []byte, in *Ingress, sequence int64) error {
	if len(line) > maxTeamRunCreationBytes+4096 {
		return ErrTeamRunCreationConflict
	}
	var fact teamRunCreationFact
	if decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != teamRunCreationProtocol || fact.FactType != teamRunCreationFactType ||
		fact.Sequence != sequence || fact.Creation.FactDigest != "" || fact.Creation.RunID == "" {
		return ErrTeamRunCreationConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("creation fact", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamRunCreationConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, ok := in.controlOwners[ownerKey]
	if !ok || owner.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	plan, ok := in.teamPlans[teamPlanKey(fact.Scope, fact.Creation.GoalID)]
	if !ok {
		return ErrTeamRunCreationConflict
	}
	key, _, err := validateTeamRunCreation(fact.Scope, plan, fact.Creation)
	if err != nil {
		return err
	}
	if _, exists := in.teamRunCreations[key]; exists {
		return ErrTeamRunCreationConflict
	}
	fact.Creation.FactDigest = digest
	in.teamRunCreations[key] = fact.Creation
	return nil
}
