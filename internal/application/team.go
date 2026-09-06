package application

import (
	"encoding/json"

	"github.com/chiga0/marshal-harness/internal/canonical"
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
	Inputs           json.RawMessage `json:"inputs"`
}

func (request ApproveInitialTeamRequest) Frozen() (ApproveInitialTeamRequest, string, error) {
	fail := func() (ApproveInitialTeamRequest, string, error) {
		return ApproveInitialTeamRequest{}, "", NewError("approve-initial-team", ReasonInvalidRequest)
	}
	if request.ProtocolRevision != InitialTeamApprovalProtocol || !validID(request.RequestID) || !validDigest(request.InputsDigest) || request.ExpectedHead != "" || len(request.Inputs) == 0 || len(request.Inputs) > MaxInitialTeamInputsBytes {
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
