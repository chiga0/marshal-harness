//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

func TestRunMutationLanesCancelWithoutReleasingOwnerAndReclaimEntries(t *testing.T) {
	var lanes runMutationLanes
	release, err := lanes.acquire(context.Background(), "run-one")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	waiting := &mutationWaitContext{Context: ctx, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		unlock, err := lanes.acquire(waiting, "run-one")
		if unlock != nil {
			unlock()
		}
		done <- err
	}()
	select {
	case <-waiting.entered:
	case <-time.After(time.Second):
		t.Fatal("waiter not queued")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	other, err := lanes.acquire(context.Background(), "run-two")
	if err != nil {
		t.Fatal(err)
	}
	other()
	lanes.mu.Lock()
	if len(lanes.entries) != 1 || lanes.entries["run-one"].refs != 1 {
		t.Error("cancelled waiter changed owner or leaked entry")
	}
	lanes.mu.Unlock()
	release()
	release() // deferred/explicit cleanup is idempotent.
	lanes.mu.Lock()
	defer lanes.mu.Unlock()
	if len(lanes.entries) != 0 {
		t.Fatal("terminal Run entry retained")
	}
}

// This proves router scheduling only. The application fixture does not prove
// a real verifier or a stopped Run; production coverage is separately required.
func TestVerificationYieldsGlobalWriterButKeepsRunThroughReceipt(t *testing.T) {
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	port, delivery := testHTTPApplication()
	port.run = port.collected.Run
	probe := &mutationProbeDelivery{StartRunDelivery: delivery}
	router, err := NewHTTPRouter(port, probe)
	if err != nil {
		t.Fatal(err)
	}
	phases := 0
	probe.probe = func(phase string) {
		phases++
		called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error { t.Error("writer overlap in " + phase); return nil })
		if called || err != nil {
			t.Fatalf("phase=%s called=%v err=%v", phase, called, err)
		}
		router.runMutations.mu.Lock()
		defer router.runMutations.mu.Unlock()
		if lane := router.runMutations.entries[port.run.RunID]; lane == nil || len(lane.gate) != 1 {
			t.Error("Run unprotected in " + phase)
		}
	}
	current := application.CurrentRunRequest{RunID: port.run.RunID, AttemptID: port.run.AttemptID, ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead}
	input := application.VerifyRunRequest(current)
	deadline := time.Now().UTC().Add(time.Minute)
	body := canonicalBody(t, input)
	binding, err := clientRequestBinding("request:verify", body, productionruntime.FixedLifecycleVerifyOperation, input, deadline)
	if err != nil {
		t.Fatal(err)
	}
	request := httpRequest{operation: productionruntime.FixedLifecycleVerifyOperation, requestKey: "request:verify", body: body}
	response, code, err := router.lifecycleOperation(context.Background(), binding, request, deadline, input, current, func(ctx context.Context) (any, error) {
		called, err := router.TryBackgroundMutation(ctx, func(context.Context) error { return nil })
		if !called || err != nil {
			t.Fatalf("verification monopolizes writer: %v", err)
		}
		other, err := router.runMutations.acquire(ctx, "run-other")
		if err != nil {
			t.Fatal(err)
		}
		other()
		run := port.run
		run.State, run.Sequence, run.AuthorityHead = domain.StateReviewPending, run.Sequence+1, digest("verified")
		return application.VerificationProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: run, Status: "pass", ReportDigest: digest("report"), ArtifactManifestDigest: digest("manifest")}, nil
	})
	if err != nil || code != 200 || response.LifecycleReceipt == nil || phases != 2 {
		t.Fatalf("code=%d phases=%d err=%v", code, phases, err)
	}
	if len(router.runMutations.entries) != 0 {
		t.Fatal("Run scheduling entry leaked")
	}
}

func TestVerificationReceiptWaitUsesOriginalDeadlineAndPreservesPending(t *testing.T) {
	port, delivery := testHTTPApplication()
	port.run = port.collected.Run
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	current := application.CurrentRunRequest{RunID: port.run.RunID, AttemptID: port.run.AttemptID, ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead}
	input := application.VerifyRunRequest(current)
	deadline := time.Now().UTC().Add(time.Second)
	body := canonicalBody(t, input)
	binding, err := clientRequestBinding("request:verify-timeout", body, productionruntime.FixedLifecycleVerifyOperation, input, deadline)
	if err != nil {
		t.Fatal(err)
	}
	request := httpRequest{operation: productionruntime.FixedLifecycleVerifyOperation, requestKey: "request:verify-timeout", body: body}
	otherWriter := false
	defer func() {
		if otherWriter {
			router.releaseMutation()
		}
	}()
	_, code, err := router.lifecycleOperation(context.Background(), binding, request, deadline, input, current, func(ctx context.Context) (any, error) {
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
			t.Fatal("verification deadline changed")
		}
		if err := router.acquireMutation(ctx); err != nil {
			t.Fatal(err)
		}
		otherWriter = true
		// Simulate another Run owning the short commit lane when execution
		// finishes. No new deadline, receipt or second verifier is allowed.
		return application.VerificationProjection{}, nil
	})
	if code != 503 || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if delivery.lifecycleBeginCalls != 1 || delivery.lifecycleCommitCalls != 0 {
		t.Fatal("timed-out commit manufactured receipt or redelivered")
	}
	if len(router.runMutations.entries) != 0 || len(router.mutation) != 1 {
		t.Fatal("released another writer or leaked Run lane")
	}
}
