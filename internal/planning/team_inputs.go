package planning

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const (
	TeamInputsVersion  = "bounded-team-inputs/v1"
	MaxTeamInputsBytes = 512 << 10
	MaxTeamNodeBytes   = 128 << 10
)

var (
	ErrTeamInputs = errors.New("planning: invalid bounded team inputs")
	teamBaseSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// TeamInputs is a complete initial proposal, not approval or current authority.
// Raw messages retain all schema-governed Task/Policy fields. No file reference,
// environment lookup, executable probe or mutable branch resolves in preview.
type TeamInputs struct {
	SchemaVersion string                `json:"schemaVersion"`
	Spec          goal.GoalSpecRevision `json:"spec"`
	Proposal      goal.GoalPlanProposal `json:"proposal"`
	BaseSHA       string                `json:"baseSha"`
	Nodes         []TeamNodeInputs      `json:"nodes"`
}

type TeamNodeInputs struct {
	NodeID string          `json:"nodeId"`
	Role   string          `json:"role"`
	Task   json.RawMessage `json:"task"`
	Policy json.RawMessage `json:"policy"`
}

// TeamInputsPreview is deliberately not an accepted plan. The fixed server
// must still evaluate current-ledger budget/scope/CAS and authenticate operator
// approval of these exact canonical bytes before committing any obligation.
type TeamInputsPreview struct {
	Canonical []byte
	Digest    string
	Inputs    TeamInputs
}

// TeamNodeIDs are derived before input digests exist, avoiding the cycle
// accepted-fact -> RunID -> Policy digest -> input digest -> accepted-fact.
// A changed bundle with the same identities is a ledger conflict, not permission
// to create another Run. The proposal ID must change for a new proposal.
func TeamNodeIDs(proposal goal.GoalPlanProposal, nodeID string) (taskID, runID string, err error) {
	if proposal.Validate() != nil || domain.ValidateID(nodeID) != nil {
		return "", "", ErrTeamInputs
	}
	identity, err := json.Marshal(struct {
		Version    string `json:"version"`
		Namespace  any    `json:"namespace"`
		GoalID     string `json:"goalId"`
		ProposalID string `json:"proposalId"`
		NodeID     string `json:"nodeId"`
	}{TeamInputsVersion, proposal.AuthorityNamespaceId, proposal.GoalId, proposal.ProposalId, nodeID})
	if err != nil {
		return "", "", ErrTeamInputs
	}
	identity, err = canonical.JSON(identity)
	if err != nil {
		return "", "", ErrTeamInputs
	}
	digest := canonical.DigestBytes(identity)[len("sha256:"):]
	return "team-task-" + digest, "team-run-" + digest, nil
}

// PreviewTeamInputs validates only the executable-input binding for the first
// two-implementer/one-integrator profile. It creates no state, grants no approval
// and does not replace goal.Evaluate or the runtime's actual environment check.
// Replanning is intentionally not accepted by this initial-plan parser.
func PreviewTeamInputs(raw []byte, validator *contract.Validator) (TeamInputsPreview, error) {
	fail := func() (TeamInputsPreview, error) { return TeamInputsPreview{}, ErrTeamInputs }
	if validator == nil || len(raw) == 0 || len(raw) > MaxTeamInputsBytes {
		return fail()
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil {
		return fail()
	}
	var inputs TeamInputs
	decoder := json.NewDecoder(bytes.NewReader(canonicalRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&inputs) != nil || inputs.SchemaVersion != TeamInputsVersion || !teamBaseSHA.MatchString(inputs.BaseSHA) || inputs.Spec.Validate() != nil || inputs.Proposal.Validate() != nil {
		return fail()
	}
	spec, proposal := inputs.Spec, inputs.Proposal
	specDigest, err := spec.Digest()
	if err != nil || proposal.GoalSpecDigest != specDigest || proposal.GoalSpecRevision != spec.Revision || proposal.GoalId != spec.GoalId || proposal.ProjectId != spec.ProjectId || proposal.Repository != spec.Repository || !proposal.AuthorityNamespaceId.Equal(spec.AuthorityNamespaceId) || proposal.BasedOnPlanRevision != 0 || len(proposal.Supersessions) != 0 {
		return fail()
	}
	if len(inputs.Nodes) != 3 || len(proposal.Nodes) != 3 || len(proposal.Edges) != 2 {
		return fail()
	}
	nodes := make(map[string]goal.GoalNode, 3)
	for _, node := range proposal.Nodes {
		if node.Validate() != nil || node.ExecutorKind != goal.ExecutorKindImplement || node.Repository != spec.Repository || len(node.Paths) == 0 {
			return fail()
		}
		if _, exists := nodes[node.NodeId]; exists {
			return fail()
		}
		nodes[node.NodeId] = node
	}
	roles := make(map[string]string, 3)
	integration := ""
	for _, input := range inputs.Nodes {
		node, exists := nodes[input.NodeID]
		if !exists || roles[input.NodeID] != "" || len(input.Task)+len(input.Policy) > MaxTeamNodeBytes {
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
		roles[input.NodeID] = input.Role
		taskID, runID, idErr := TeamNodeIDs(proposal, input.NodeID)
		if idErr != nil || validator.Validate(domain.KindTask, input.Task) != nil {
			return fail()
		}
		var task domain.TaskSpec
		if json.Unmarshal(input.Task, &task) != nil {
			return fail()
		}
		policy, policyErr := ValidatePolicy(input.Policy, task, runID, validator)
		if policyErr != nil || task.Metadata.ID != taskID || task.Repository.Path != spec.Repository || task.Repository.BaseRef != inputs.BaseSHA || !slices.Equal(task.Scope.AllowPaths, node.Paths) {
			return fail()
		}
		// Dependencies and the integration base are derived from accepted exact
		// upstream candidates later, never from a mutable "latest Task" lookup.
		if len(task.DependsOn) != 0 || task.Admission.Status != domain.AdmissionStatusExecutable || task.Publication.Provider != "none" || task.Publication.Mode != "none" || task.Publication.Required || policy.AllowPublication {
			return fail()
		}
		if task.Worker.PreferredAdapter != "pi" || task.Worker.Model == "" || len(task.Worker.FallbackAdapters) != 0 || policy.AllowFallbackWorkers || !slices.Equal(policy.AllowedAdapters, []string{"pi"}) {
			return fail()
		}
		var snapshot struct {
			Effective struct {
				AllowWorkerSubagents bool `json:"allowWorkerSubagents"`
			} `json:"effective"`
		}
		if json.Unmarshal(input.Policy, &snapshot) != nil || snapshot.Effective.AllowWorkerSubagents {
			return fail()
		}
		if node.Estimate.Runs != 1 || node.Estimate.Attempts < int64(task.Budgets.MaxAttempts) || node.Estimate.WallTimeSeconds < task.Budgets.RunTimeoutSeconds || node.Estimate.ArtifactBytes < task.Budgets.MaxOutputBytes {
			return fail()
		}
	}
	if integration == "" {
		return fail()
	}
	producers := make(map[string]bool, 2)
	for _, edge := range proposal.Edges {
		if edge.Validate() != nil || edge.To != integration || roles[edge.From] != "implement" || producers[edge.From] {
			return fail()
		}
		producers[edge.From] = true
	}
	return TeamInputsPreview{Canonical: canonicalRaw, Digest: canonical.DigestBytes(canonicalRaw), Inputs: inputs}, nil
}
