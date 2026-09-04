package application

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPreparedRunStartRequiresExactReadyHead(t *testing.T) {
	valid := PreparedRunStart{ProtocolRevision: PreparedRunStartProtocolRevision, TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", ReservationFactDigest: testDigest, AttemptOpenedFactDigest: testDigest, AttemptOrdinal: 1, MaxAttempts: 3, State: domain.StateReady, Sequence: 3, AuthorityHead: testDigest, PreparationDigest: testDigest}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid prepared start: %v", err)
	}
	invalid := valid
	invalid.State = domain.StateRunning
	if err := invalid.Validate(); !HasReason(err, ReasonInvalidRequest) {
		t.Fatalf("state drift reason = %v", err)
	}
}

func TestStatusRejectsReadyWithoutCurrentOwner(t *testing.T) {
	status := StatusProjection{ProtocolRevision: ProtocolRevision, Availability: AvailabilityReady, PlatformProfileID: "darwin-local-dogfood", AgentProvider: "pi", AgentVersion: "0.84.4", AgentClosureProfile: "pi/0.84.4/darwin-arm64/v1", AgentIdentityDigest: testDigest}
	if err := status.Validate(); !HasReason(err, ReasonAuthorityConflict) {
		t.Fatalf("missing owner reason = %v", err)
	}
	status.OwnerEpoch = 1
	status.OwnerFactDigest = testDigest
	if err := status.Validate(); err != nil {
		t.Fatalf("valid status: %v", err)
	}
}

func TestErrorDoesNotEchoInput(t *testing.T) {
	err := NewError("prepare-run-start", ReasonInvalidRequest)
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonInvalidRequest {
		t.Fatalf("typed error = %#v", err)
	}
	if got := err.Error(); got != "application: invalid-request" {
		t.Fatalf("error = %q", got)
	}
}

func TestT2RequestsRequireExactAttemptHead(t *testing.T) {
	valid := CurrentRunRequest{RunID: "run-1", AttemptID: "attempt-1", ExpectedSequence: 4, ExpectedAuthorityHead: testDigest}
	for name, validate := range map[string]func() error{
		"collect": func() error { return CollectRunResultRequest(valid).Validate() },
		"verify":  func() error { return VerifyRunRequest(valid).Validate() },
		"review":  func() error { return BuildReviewPacketRequest(valid).Validate() },
	} {
		if err := validate(); err != nil {
			t.Fatalf("%s valid request: %v", name, err)
		}
	}
	invalid := CollectRunResultRequest(valid)
	invalid.AttemptID = ""
	if err := invalid.Validate(); !HasReason(err, ReasonInvalidRequest) {
		t.Fatalf("missing attempt reason = %v", err)
	}
}

func TestApplyReviewDecisionRequiresBoundedCanonicalInput(t *testing.T) {
	valid := ApplyReviewDecisionRequest{RunID: "run-1", AttemptID: "attempt-1", ExpectedSequence: 7, ExpectedAuthorityHead: testDigest, Decision: json.RawMessage(`{"verdict":"accept"}`), DecisionDigest: testDigest}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid decision request: %v", err)
	}
	invalid := valid
	invalid.Decision = json.RawMessage(`{"verdict":`)
	if err := invalid.Validate(); !HasReason(err, ReasonInvalidRequest) {
		t.Fatalf("invalid json reason = %v", err)
	}
}
