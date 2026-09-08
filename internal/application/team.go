package application

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const InitialTeamApprovalProtocol = "initial-team-approval/v1"
const MaxInitialTeamInputsBytes = 512 << 10

// Approval is an operator operation, not a bearer capability. Input adapters
// must authenticate the caller before invoking it. The server validates the
// full frozen bundle independently; neither RequestID nor InputsDigest grants
// authority. Only initial creation (empty ExpectedHead) is supported here.
type ApproveInitialTeamRequest struct {
	ProtocolRevision string          `json:"protocolRevision"`
	RequestID        string          `json:"requestId"`
	InputsDigest     string          `json:"inputsDigest"`
	ExpectedHead     string          `json:"expectedHead"`
	Deadline         string          `json:"deadline"`
	Inputs           json.RawMessage `json:"inputs"`
}

func (request ApproveInitialTeamRequest) Frozen() (ApproveInitialTeamRequest, string, error) {
	fail := func() (ApproveInitialTeamRequest, string, error) {
		return ApproveInitialTeamRequest{}, "", NewError("approve-initial-team", ReasonInvalidRequest)
	}
	if request.ProtocolRevision != InitialTeamApprovalProtocol || domain.ValidateID(request.RequestID) != nil || !validDigest(request.InputsDigest) || request.ExpectedHead != "" || len(request.Inputs) == 0 || len(request.Inputs) > MaxInitialTeamInputsBytes {
		return fail()
	}
	deadline, err := time.Parse(time.RFC3339Nano, request.Deadline)
	if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != request.Deadline {
		return fail()
	}
	frozen, err := canonical.JSON(request.Inputs)
	if err != nil || canonical.DigestBytes(frozen) != request.InputsDigest {
		return fail()
	}
	request.Inputs = frozen
	raw, err := json.Marshal(request)
	if err != nil {
		return fail()
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		return fail()
	}
	return request, canonical.DigestBytes(raw), nil
}

func (projection InitialTeamApprovalProjection) Validate() error {
	if !validID(projection.GoalID) || projection.PlanRevision != 1 || !validDigest(projection.InputsDigest) || !validDigest(projection.RequestDigest) || !validDigest(projection.FactDigest) || projection.ObligationCount != 3 {
		return NewError("initial-team-approval", ReasonAuthorityConflict)
	}
	return nil
}

// The projection reports durable plan approval, never business acceptance or
// completed Run materialization. It deliberately excludes Task/Policy bodies.
type InitialTeamApprovalProjection struct {
	GoalID          string `json:"goalId"`
	PlanRevision    int64  `json:"planRevision"`
	InputsDigest    string `json:"inputsDigest"`
	RequestDigest   string `json:"requestDigest"`
	FactDigest      string `json:"factDigest"`
	ObligationCount int    `json:"obligationCount"`
}

// Compact read projection of a durable team completion, not permission to
// publish. The exact fact binds both upstreams as well as this final candidate.
type InitialTeamOutcomeProjection struct {
	Outcome            goal.GoalOutcome `json:"outcome"`
	PlanFactDigest     string           `json:"planFactDigest"`
	FactDigest         string           `json:"factDigest"`
	IntegrationRunID   string           `json:"integrationRunId"`
	CandidateDigest    string           `json:"candidateDigest"`
	PatchDigest        string           `json:"patchDigest"`
	IntegrationBaseSHA string           `json:"integrationBaseSha"`
	AttemptsUsed       int64            `json:"attemptsUsed"`
	Measurement        string           `json:"measurement"`
}

func (p InitialTeamOutcomeProjection) Validate() error {
	_, objectErr := hex.DecodeString(p.IntegrationBaseSHA)
	if p.Outcome.Validate() != nil || p.Outcome.State != goal.OutcomeStateCompleted || p.Outcome.Reason != "verified-team-delivery" ||
		!validDigest(p.PlanFactDigest) || !validDigest(p.FactDigest) || !validDigest(p.CandidateDigest) || !validDigest(p.PatchDigest) ||
		!validID(p.IntegrationRunID) || objectErr != nil || (len(p.IntegrationBaseSHA) != 40 && len(p.IntegrationBaseSHA) != 64) || strings.ToLower(p.IntegrationBaseSHA) != p.IntegrationBaseSHA || p.AttemptsUsed != 3 || p.Measurement != "attempt-counts-only" {
		return NewError("team-outcome", ReasonAuthorityConflict)
	}
	return nil
}
