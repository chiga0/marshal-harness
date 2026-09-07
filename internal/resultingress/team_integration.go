package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

type TeamAcceptedSource struct {
	NodeID             string `json:"nodeId"`
	RunID              string `json:"runId"`
	AttemptID          string `json:"attemptId"`
	AuthorityHead      string `json:"authorityHead"`
	CreationFactDigest string `json:"creationFactDigest"`
	CandidateDigest    string `json:"candidateDigest"`
	PatchDigest        string `json:"patchDigest"`
	DecisionDigest     string `json:"decisionDigest"`
	PacketDigest       string `json:"packetDigest"`
	OutcomeDigest      string `json:"outcomeDigest"`
}

// Data binding only. CurrentAcceptedTeamVerifier, not this serializable
// structure, proves that the original two inputs remain accepted.
type TeamIntegrationInputs struct {
	GoalID         string               `json:"goalId"`
	NodeID         string               `json:"nodeId"`
	PlanFactDigest string               `json:"planFactDigest"`
	BaseSHA        string               `json:"baseSha"`
	Sources        []TeamAcceptedSource `json:"sources"`
}

type TeamIntegrationBase struct {
	Inputs       TeamIntegrationInputs `json:"inputs"`
	InputsDigest string                `json:"inputsDigest"`
	TreeSHA      string                `json:"treeSha"`
	CommitSHA    string                `json:"commitSha"`
}

func (in TeamIntegrationInputs) Digest() (string, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

type CurrentAcceptedTeamVerifier interface {
	// Hold current owner and both real upstream Run leases while re-reading
	// exact committed accepted inputs, then execute fn under those guards.
	WithCurrentAcceptedTeam(context.Context, ControlOwnerAcquisition, TeamPlanApproval, TeamIntegrationInputs, func() error) error
}

type acceptedTeamApprovalVerifier struct {
	verifier CurrentAcceptedTeamVerifier
	inputs   TeamIntegrationInputs
}

func (v acceptedTeamApprovalVerifier) WithCurrentApprovedTeam(ctx context.Context, owner ControlOwnerAcquisition, approval TeamPlanApproval, fn func() error) error {
	return v.verifier.WithCurrentAcceptedTeam(ctx, owner, approval, v.inputs, fn)
}

func (s *DurableStore) FreezeIntegrationTeamRun(ctx context.Context, verifier CurrentAcceptedTeamVerifier, owner ControlOwnerAcquisition, approval TeamPlanApproval, goalID, nodeID, planFactDigest string, raw []byte, integration TeamIntegrationBase) (TeamRunCreationState, error) {
	if verifier == nil {
		return TeamRunCreationState{}, ErrTeamRunCreationConflict
	}
	encoded, err := json.Marshal(integration)
	if err != nil {
		return TeamRunCreationState{}, ErrTeamRunCreationConflict
	}
	// decodeTeamRecord deliberately requires canonical wire bytes; Go struct
	// field order is not JCS key order. Canonicalize before the defensive clone.
	encoded, err = canonical.JSON(encoded)
	var frozen TeamIntegrationBase
	if err != nil || decodeTeamRecord(encoded, &frozen) != nil {
		return TeamRunCreationState{}, ErrTeamRunCreationConflict
	}
	return s.freezeTeamRun(ctx, acceptedTeamApprovalVerifier{verifier, frozen.Inputs}, owner, approval, goalID, nodeID, planFactDigest, raw, &frozen)
}

var teamIntegrationObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// DeriveTeamIntegrationTask changes exactly one field in the approved raw
// document. It does not marshal the partial domain.TaskSpec back to JSON.
func DeriveTeamIntegrationTask(template []byte, originalBase, derivedBase string) ([]byte, error) {
	if !teamIntegrationObjectID.MatchString(originalBase) || !teamIntegrationObjectID.MatchString(derivedBase) {
		return nil, ErrTeamRunCreationConflict
	}
	var task, repo map[string]json.RawMessage
	if json.Unmarshal(template, &task) != nil || json.Unmarshal(task["repository"], &repo) != nil {
		return nil, ErrTeamRunCreationConflict
	}
	var base string
	if json.Unmarshal(repo["baseRef"], &base) != nil || base != originalBase {
		return nil, ErrTeamRunCreationConflict
	}
	repo["baseRef"], _ = json.Marshal(derivedBase)
	task["repository"], _ = json.Marshal(repo)
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(raw)
}

func validateTeamIntegration(plan TeamPlanState, input goal.TeamInputs, creation TeamRunCreationState) error {
	i := creation.Integration
	if i == nil || i.Inputs.GoalID != creation.GoalID || i.Inputs.NodeID != creation.NodeID || i.Inputs.PlanFactDigest != plan.FactDigest || i.Inputs.BaseSHA != input.BaseSHA ||
		!teamIntegrationObjectID.MatchString(i.TreeSHA) || !teamIntegrationObjectID.MatchString(i.CommitSHA) || len(i.Inputs.Sources) != 2 {
		return ErrTeamRunCreationConflict
	}
	digest, err := i.Inputs.Digest()
	if err != nil || digest != i.InputsDigest {
		return ErrTeamRunCreationConflict
	}
	roles := map[string]string{}
	for _, node := range input.Nodes {
		roles[node.NodeID] = node.Role
	}
	if roles[creation.NodeID] != "integrate" {
		return ErrTeamRunCreationConflict
	}
	var expected []string
	for _, edge := range input.Proposal.Edges {
		if edge.To == creation.NodeID {
			expected = append(expected, edge.From)
		}
	}
	sort.Strings(expected)
	if len(expected) != 2 || expected[0] == expected[1] {
		return ErrTeamRunCreationConflict
	}
	for index, source := range i.Inputs.Sources {
		_, runID, err := goal.TeamNodeIDs(input.Proposal, source.NodeID)
		if err != nil || source.NodeID != expected[index] || roles[source.NodeID] != "implement" || source.RunID != runID || domain.ValidateID(source.AttemptID) != nil {
			return ErrTeamRunCreationConflict
		}
		for _, digest := range []string{source.AuthorityHead, source.CreationFactDigest, source.CandidateDigest, source.PatchDigest, source.DecisionDigest, source.PacketDigest, source.OutcomeDigest} {
			if requireDigest("accepted team source", digest) != nil {
				return ErrTeamRunCreationConflict
			}
		}
	}
	return nil
}

func validateTeamIntegrationDependencies(in *Ingress, scope ControlOwnerScope, creation TeamRunCreationState) error {
	if creation.Integration == nil {
		return nil
	}
	for _, source := range creation.Integration.Inputs.Sources {
		prior, found := in.teamRunCreations[teamRunCreationKey(scope, creation.GoalID, source.NodeID)]
		if !found || prior.RunID != source.RunID || prior.PlanFactDigest != creation.PlanFactDigest || prior.FactDigest != source.CreationFactDigest || prior.Integration != nil {
			return ErrTeamRunCreationConflict
		}
	}
	return nil
}

func equalTeamIntegration(a, b *TeamIntegrationBase) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}
