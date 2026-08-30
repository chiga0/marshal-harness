package resultingress

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
)

func TestPrepareMacRunStartProjectsCurrentPreparedExecution(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	start, err := fixture.store.PrepareMacRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, fixture.prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("PrepareMacRunStart: %v", err)
	}
	if err := start.Validate(); err != nil {
		t.Fatalf("prepared Run-start is invalid: %v", err)
	}
	if start.TaskID != fixture.prepared.AttemptIdentity.TaskID || start.RunID != fixture.prepared.AttemptIdentity.RunID || start.AttemptID != fixture.prepared.AttemptIdentity.AttemptID || start.Sequence != fixture.prepared.ExpectedRunSequence || start.AuthorityHead != fixture.prepared.ExpectedRunAuthorityHead || start.PreparationDigest != fixture.prepared.PreparationDigest {
		t.Fatalf("prepared Run-start lost durable identity: %+v", start)
	}
	if start.ReservationFactDigest != fixture.prepared.ReservationFactDigest || start.AttemptOpenedFactDigest != fixture.prepared.AttemptOpenedFactDigest || start.AttemptOrdinal != fixture.prepared.AttemptOrdinal || start.AttemptsUsedBefore != fixture.prepared.AttemptsUsedBefore || start.MaxAttempts != fixture.prepared.MaxAttempts {
		t.Fatalf("prepared Run-start lost durable attempt authority: %+v", start)
	}
}

func TestCommitMacRunStartRejectsCallerChosenAuthorityBeforeStart(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	start, err := fixture.store.PrepareMacRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, fixture.prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("PrepareMacRunStart: %v", err)
	}
	tampered := start
	tampered.AttemptID = "attempt:caller-chosen"
	projector := &claimCaptureProjector{}
	if err := fixture.store.CommitMacRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, tampered, projector); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("caller-chosen Attempt identity accepted: %v", err)
	}
	if projector.claim != (CommittedRunStartClaim{}) {
		t.Fatalf("rejected caller identity reached proof projector: %+v", projector.claim)
	}
}

func TestPreparedRunStartProtocolRevisionIsClosed(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	start, err := fixture.store.PrepareMacRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, fixture.prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("PrepareMacRunStart: %v", err)
	}
	start.ProtocolRevision = application.ProtocolRevision
	if err := start.Validate(); err == nil {
		t.Fatal("application protocol revision was accepted as Run-start revision")
	}
}
