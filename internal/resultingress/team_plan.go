package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const teamPlanFactType = "team-plan-accepted"
const teamPlanProtocol = "bounded-team-plan/v1"

var ErrTeamPlanConflict = errors.New("resultingress: team plan conflict")

// TeamPlanApproval binds an authenticated operator's exact request. Its
// digests alone are NOT approval; the production verifier must check the
// authenticated caller, full planning preview, repository and current owner.
type TeamPlanApproval struct {
	InputsDigest  string `json:"inputsDigest"`
	RequestDigest string `json:"requestDigest"`
	ExpectedHead  string `json:"expectedHead"`
}

type CurrentApprovedTeamVerifier interface {
	// Hold the current repository owner lock for the entire callback. Validate
	// the complete frozen Task/Policy bundle through planning before entering.
	// Worker text, actor strings and caller-supplied digests cannot satisfy this.
	WithCurrentApprovedTeam(context.Context, ControlOwnerAcquisition, TeamPlanApproval, func() error) error
}

type teamApprovalOwnerVerifier struct {
	verifier CurrentApprovedTeamVerifier
	approval TeamPlanApproval
}

func (v teamApprovalOwnerVerifier) WithCurrentOwnerLock(ctx context.Context, owner ControlOwnerAcquisition, fn func() error) error {
	return v.verifier.WithCurrentApprovedTeam(ctx, owner, v.approval, fn)
}

type TeamMaterialization struct {
	NodeID               string                  `json:"nodeId"`
	TaskID               string                  `json:"taskId"`
	RunID                string                  `json:"runId"`
	Reservation          goal.ReservationRequest `json:"reservation"`
	TaskTemplateDigest   string                  `json:"taskTemplateDigest"`
	PolicyTemplateDigest string                  `json:"policyTemplateDigest"`
}

// One append commits the complete approved input, accepted revision, budget
// reservations and creation obligations. It does not itself create any Run.
type TeamPlanState struct {
	Inputs           json.RawMessage               `json:"inputs"`
	Approval         TeamPlanApproval              `json:"approval"`
	Revision         goal.AcceptedGoalPlanRevision `json:"revision"`
	Materializations []TeamMaterialization         `json:"materializations"`
	FactDigest       string                        `json:"factDigest"`
}

type teamPlanFact struct {
	ProtocolRevision string            `json:"protocolRevision"`
	FactType         string            `json:"factType"`
	Sequence         int64             `json:"sequence"`
	Scope            ControlOwnerScope `json:"scope"`
	OwnerFactDigest  string            `json:"ownerFactDigest"`
	Plan             TeamPlanState     `json:"plan"`
	Digest           string            `json:"digest"`
}

// AcceptInitialTeamPlan rejects every existing non-identical Goal rather than
// resetting its budget. Replan/settlement need their own current-ledger
// transitions; this entry point never pretends an existing Goal is new.
func (s *DurableStore) AcceptInitialTeamPlan(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, approval TeamPlanApproval, raw []byte) (TeamPlanState, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || len(raw) == 0 || len(raw) > goal.MaxTeamInputsBytes || approval.ExpectedHead != "" || requireDigest("approval request", approval.RequestDigest) != nil {
		return TeamPlanState{}, ErrTeamPlanConflict
	}
	frozen, err := canonical.JSON(raw)
	if err != nil || canonical.DigestBytes(frozen) != approval.InputsDigest {
		return TeamPlanState{}, ErrTeamPlanConflict
	}
	var inputs goal.TeamInputs
	if decodeTeamRecord(frozen, &inputs) != nil {
		return TeamPlanState{}, ErrTeamPlanConflict
	}
	key := teamPlanKey(owner.Scope, inputs.Spec.GoalId)
	var result TeamPlanState
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier, approval}, owner, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, ok := projection.controlOwners[ownerKey]
			if !ok || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			if existing, ok := projection.teamPlans[key]; ok {
				if existing.Approval != approval || !bytes.Equal(existing.Inputs, frozen) {
					return ErrTeamPlanConflict
				}
				result = existing
				return nil
			}
			_, candidate, err := deriveInitialTeam(owner.Scope, approval, frozen)
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fact := &teamPlanFact{ProtocolRevision: teamPlanProtocol, FactType: teamPlanFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Plan: candidate}
			encoded, err := json.Marshal(fact)
			// Reserve room for the detached digest before any append. Never write
			// a fact that this same reader would reject on its size boundary.
			if err != nil || len(encoded)+100 > goal.MaxTeamInputsBytes+(128<<10) {
				return ErrTeamPlanConflict
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(digest string) { fact.Digest = digest }); err != nil {
				return err
			}
			s.nextSequence++
			candidate.FactDigest = fact.Digest
			projection.teamPlans[key] = candidate
			result = candidate
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) ReadTeamPlan(scope ControlOwnerScope, goalID string) (TeamPlanState, bool, error) {
	if scope.Validate() != nil || domain.ValidateID(goalID) != nil {
		return TeamPlanState{}, false, ErrTeamPlanConflict
	}
	key := teamPlanKey(scope, goalID)
	projection := newAuthorityProjection()
	var result TeamPlanState
	var found bool
	err := s.transact(projection, func() error { result, found = projection.teamPlans[key]; return nil })
	return result, found, err
}

func teamPlanKey(scope ControlOwnerScope, goalID string) string {
	return canonicalDigestOrEmpty(struct {
		Scope  ControlOwnerScope
		GoalID string
	}{scope, goalID})
}

func deriveInitialTeam(scope ControlOwnerScope, approval TeamPlanApproval, raw []byte) (string, TeamPlanState, error) {
	fail := func() (string, TeamPlanState, error) { return "", TeamPlanState{}, ErrTeamPlanConflict }
	if scope.Validate() != nil || len(raw) == 0 || len(raw) > goal.MaxTeamInputsBytes || canonical.DigestBytes(raw) != approval.InputsDigest || approval.ExpectedHead != "" || requireDigest("approval request", approval.RequestDigest) != nil {
		return fail()
	}
	var inputs goal.TeamInputs
	if decodeTeamRecord(raw, &inputs) != nil || inputs.SchemaVersion != goal.TeamInputsVersion || inputs.Spec.Validate() != nil || !inputs.Spec.AuthorityNamespaceId.Equal(scope.AuthorityNamespaceID) || inputs.Proposal.BasedOnPlanRevision != 0 || len(inputs.Nodes) != 3 || len(inputs.Proposal.Nodes) != 3 || len(inputs.Proposal.Edges) != 2 || inputs.Limits.MaxConcurrentNodes > 3 || len(inputs.BaseSHA) != 40 || !reservationGitObjectPattern.MatchString(inputs.BaseSHA) {
		return fail()
	}
	spec := inputs.Spec
	// Only called after proving this Goal key absent in the current replay.
	// This is the creation transaction, not an empty replacement for history.
	state := goal.AuthorityState{AuthorityNamespaceId: spec.AuthorityNamespaceId, GoalId: spec.GoalId, ProjectId: spec.ProjectId, Repository: spec.Repository, SpecRevision: &spec, Budget: goal.GoalBudgetLedger{AuthorityNamespaceId: spec.AuthorityNamespaceId, GoalId: spec.GoalId, Limits: inputs.Limits}}
	proposal, err := inputs.Proposal.Canonical()
	if err != nil {
		return fail()
	}
	decision := goal.Evaluate(proposal, state, inputs.AdmissionPolicy)
	if !decision.Accepted || decision.Revision == nil || len(decision.ReservationPlan) != 3 {
		return fail()
	}
	byNode := make(map[string]goal.TeamNodeInputs, 3)
	integration := ""
	for _, input := range inputs.Nodes {
		if _, exists := byNode[input.NodeID]; exists {
			return fail()
		}
		if len(input.Task) == 0 || len(input.Policy) == 0 || len(input.Task)+len(input.Policy) > goal.MaxTeamNodeBytes {
			return fail()
		}
		if input.Role != "implement" && input.Role != "integrate" {
			return fail()
		}
		if input.Role == "integrate" {
			if integration != "" {
				return fail()
			}
			integration = input.NodeID
		}
		byNode[input.NodeID] = input
	}
	if integration == "" {
		return fail()
	}
	for _, edge := range inputs.Proposal.Edges {
		if edge.To != integration || byNode[edge.From].Role != "implement" {
			return fail()
		}
	}
	result := TeamPlanState{Inputs: bytes.Clone(raw), Approval: approval, Revision: *decision.Revision}
	for _, reservation := range decision.ReservationPlan {
		input, exists := byNode[reservation.NodeId]
		if !exists {
			return fail()
		}
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, input.NodeID)
		if err != nil {
			return fail()
		}
		task, err := canonical.JSON(input.Task)
		if err != nil {
			return fail()
		}
		policy, err := canonical.JSON(input.Policy)
		if err != nil {
			return fail()
		}
		result.Materializations = append(result.Materializations, TeamMaterialization{NodeID: input.NodeID, TaskID: taskID, RunID: runID, Reservation: reservation, TaskTemplateDigest: canonical.DigestBytes(task), PolicyTemplateDigest: canonical.DigestBytes(policy)})
	}
	slices.SortFunc(result.Materializations, func(a, b TeamMaterialization) int {
		if a.NodeID < b.NodeID {
			return -1
		}
		if a.NodeID > b.NodeID {
			return 1
		}
		return 0
	})
	return teamPlanKey(scope, spec.GoalId), result, nil
}

func decodeTeamRecord(raw []byte, target any) error {
	canon, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canon) {
		return ErrTeamPlanConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrTeamPlanConflict
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return ErrTeamPlanConflict
	}
	return nil
}

func applyTeamPlanLine(line []byte, in *Ingress, sequence int64) error {
	if len(line) > goal.MaxTeamInputsBytes+(128<<10) {
		return ErrTeamPlanConflict
	}
	var fact teamPlanFact
	if decodeTeamRecord(line, &fact) != nil || fact.Sequence != sequence || fact.ProtocolRevision != teamPlanProtocol || fact.FactType != teamPlanFactType || fact.Plan.FactDigest != "" {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("team fact", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, ok := in.controlOwners[ownerKey]
	if !ok || owner.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	key, derived, err := deriveInitialTeam(fact.Scope, fact.Plan.Approval, fact.Plan.Inputs)
	if err != nil || canonicalDigestOrEmpty(derived) != canonicalDigestOrEmpty(fact.Plan) {
		return ErrTeamPlanConflict
	}
	if _, exists := in.teamPlans[key]; exists {
		return ErrTeamPlanConflict
	}
	derived.FactDigest = digest
	in.teamPlans[key] = derived
	return nil
}
