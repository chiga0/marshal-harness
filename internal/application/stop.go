package application

import "github.com/chiga0/marshal-harness/internal/domain"

const StopRunProtocolRevision = "run-stop/v1"

// CancelRunRequest supplies no PID, actor, deadline or reason override.
// Identity and the fixed operator-request category come from the server.
type CancelRunRequest struct {
	CurrentRunRequest
	RequestID string `json:"requestId"`
}

func (r CancelRunRequest) Validate() error {
	if r.CurrentRunRequest.validate("cancel-run") != nil || !validID(r.RequestID) || len(r.RequestID) > 128 {
		return NewError("cancel-run", ReasonInvalidRequest)
	}
	return nil
}

// CancelRunProjection is a completed result, never an acknowledgement that
// a signal was merely sent. In-progress stop recovery stays a typed error.
type CancelRunProjection struct {
	ProtocolRevision string        `json:"protocolRevision"`
	Run              RunProjection `json:"run"`
	RequestDigest    string        `json:"requestDigest"`
	StopIntentDigest string        `json:"stopIntentDigest"`
	TerminalReason   string        `json:"terminalReason"`
	OutcomeDigest    string        `json:"outcomeDigest"`
}

func (p CancelRunProjection) Validate() error {
	if p.ProtocolRevision != StopRunProtocolRevision || p.Run.Validate() != nil || p.Run.State != domain.StateBlocked || !validDigest(p.RequestDigest) || !validDigest(p.StopIntentDigest) || !validDigest(p.OutcomeDigest) {
		return NewError("cancel-run", ReasonAuthorityConflict)
	}
	switch p.TerminalReason {
	case "aborted-by-operator", "attempt-deadline-exceeded", "run-deadline-exceeded":
		return nil
	}
	return NewError("cancel-run", ReasonAuthorityConflict)
}
