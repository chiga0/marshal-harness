package application

import (
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPreparedRunStartRequiresExactReadyHead(t *testing.T) {
	valid := PreparedRunStart{ProtocolRevision: ProtocolRevision, TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", State: domain.StateReady, Sequence: 3, AuthorityHead: testDigest, PreparationDigest: testDigest}
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
	status := StatusProjection{ProtocolRevision: ProtocolRevision, Availability: AvailabilityReady, PlatformProfileID: "darwin-local-dogfood", AgentProvider: "pi", AgentVersion: "0.84.3", AgentClosureProfile: "pi/0.84.3/darwin-arm64/v1", AgentIdentityDigest: testDigest}
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
