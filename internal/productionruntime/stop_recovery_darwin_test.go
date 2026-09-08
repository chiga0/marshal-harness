//go:build darwin && arm64

package productionruntime

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
)

func TestReconcileStoppedRunDoesNotInventStopBeforeExecution(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	request := application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: fixture.request.RunID, AttemptID: "attempt-not-started", ExpectedSequence: fixture.request.ExpectedSequence, ExpectedAuthorityHead: fixture.request.ExpectedAuthorityHead}, RequestID: "cancel-not-started"}
	before, err := fixture.session.ingress.AttemptStates()
	if err != nil {
		t.Fatal(err)
	}
	projectionBefore, err := fixture.session.InspectRun(context.Background(), application.InspectRunRequest{RunID: request.RunID})
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := fixture.session.ReconcileStoppedRun(context.Background(), request)
	if err != nil || found || result != (application.CancelRunProjection{}) {
		t.Fatalf("invented stop: found=%v result=%+v err=%v", found, result, err)
	}
	result, found, err = fixture.session.ReconcileStoppedCurrentRun(context.Background(), request.CurrentRunRequest)
	if err != nil || found || result != (application.CancelRunProjection{}) {
		t.Fatalf("invented Collect stop: found=%v result=%+v err=%v", found, result, err)
	}
	after, err := fixture.session.ingress.AttemptStates()
	if err != nil || len(after) != len(before) {
		t.Fatalf("unexpected Attempt mutation: %v", err)
	}
	projectionAfter, err := fixture.session.InspectRun(context.Background(), application.InspectRunRequest{RunID: request.RunID})
	if err != nil || projectionBefore != projectionAfter {
		t.Fatalf("unexpected Run mutation: %v", err)
	}
}

func TestReconcileStoppedRunRejectsInvalidInputAndClosedSession(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	request := application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: fixture.request.RunID, AttemptID: "attempt-1", ExpectedSequence: fixture.request.ExpectedSequence, ExpectedAuthorityHead: fixture.request.ExpectedAuthorityHead}, RequestID: "cancel-1"}
	for name, ctx := range map[string]context.Context{"nil": nil, "cancelled": func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := fixture.session.ReconcileStoppedRun(ctx, request); err == nil || found {
				t.Fatalf("invalid context accepted: %v", err)
			}
		})
	}
	invalid := request
	invalid.RequestID = ""
	if _, found, err := fixture.session.ReconcileStoppedRun(context.Background(), invalid); err == nil || found {
		t.Fatalf("invalid input accepted: %v", err)
	}
	// The delivery store deliberately borrows the session for its lifetime.
	// Release it before closing the owner; Close correctly waits for borrows.
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.session.ReconcileStoppedRun(context.Background(), request); err == nil || found {
		t.Fatalf("closed session accepted: %v", err)
	}
}
