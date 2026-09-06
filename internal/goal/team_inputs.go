package goal

import (
	"encoding/json"
	"errors"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	TeamInputsVersion  = "bounded-team-inputs/v1"
	MaxTeamInputsBytes = 512 << 10
	MaxTeamNodeBytes   = 128 << 10
)

// TeamInputs is the complete initial proposal, not approval or current
// authority. Keeping these data in the Goal layer lets planning and RB1 share
// the same bytes without making the durable ledger depend on an input adapter.
type TeamInputs struct {
	SchemaVersion   string           `json:"schemaVersion"`
	Spec            GoalSpecRevision `json:"spec"`
	Proposal        GoalPlanProposal `json:"proposal"`
	Limits          Guardrails       `json:"limits"`
	AdmissionPolicy AdmissionPolicy  `json:"admissionPolicy"`
	BaseSHA         string           `json:"baseSha"`
	Nodes           []TeamNodeInputs `json:"nodes"`
}

type TeamNodeInputs struct {
	NodeID string          `json:"nodeId"`
	Role   string          `json:"role"`
	Task   json.RawMessage `json:"task"`
	Policy json.RawMessage `json:"policy"`
}

// TeamNodeIDs derives identity before input digests exist. A changed bundle
// under these same IDs must conflict at durable admission, not create more Runs.
func TeamNodeIDs(proposal GoalPlanProposal, nodeID string) (taskID, runID string, err error) {
	invalid := errors.New("goal: invalid team node identity")
	if proposal.Validate() != nil || domain.ValidateID(nodeID) != nil {
		return "", "", invalid
	}
	identity, err := json.Marshal(struct {
		Version    string                         `json:"version"`
		Namespace  authority.AuthorityNamespaceId `json:"namespace"`
		GoalID     string                         `json:"goalId"`
		ProposalID string                         `json:"proposalId"`
		NodeID     string                         `json:"nodeId"`
	}{TeamInputsVersion, proposal.AuthorityNamespaceId, proposal.GoalId, proposal.ProposalId, nodeID})
	if err != nil {
		return "", "", invalid
	}
	identity, err = canonical.JSON(identity)
	if err != nil {
		return "", "", invalid
	}
	digest := canonical.DigestBytes(identity)[len("sha256:"):]
	return "team-task-" + digest, "team-run-" + digest, nil
}
