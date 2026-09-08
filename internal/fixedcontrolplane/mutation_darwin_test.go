//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

type mutationProbeDelivery struct {
	StartRunDelivery
	probe func(string)
}

type mutationWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *mutationWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func (probe *mutationProbeDelivery) BeginStartRunBound(ctx context.Context, key string, input application.StartRunRequest, deadline time.Time, binding productionruntime.FixedStartRunDeliveryBinding) (productionruntime.FixedDeliveryPending, bool, error) {
	probe.probe("begin-start")
	return probe.StartRunDelivery.BeginStartRunBound(ctx, key, input, deadline, binding)
}

func (probe *mutationProbeDelivery) ReconcileStartRunDelivery(ctx context.Context, pending productionruntime.FixedDeliveryPending, input application.StartRunRequest, reconcile productionruntime.FixedStartRunReconciler) (productionruntime.FixedDeliveryReceipt, bool, error) {
	probe.probe("reconcile-start")
	return probe.StartRunDelivery.ReconcileStartRunDelivery(ctx, pending, input, reconcile)
}

func (probe *mutationProbeDelivery) BeginLifecycleBound(ctx context.Context, key, operation string, input any, current application.CurrentRunRequest, deadline time.Time, binding productionruntime.FixedLifecycleDeliveryBinding) (productionruntime.FixedLifecyclePending, bool, error) {
	probe.probe("begin-lifecycle")
	return probe.StartRunDelivery.BeginLifecycleBound(ctx, key, operation, input, current, deadline, binding)
}

func (probe *mutationProbeDelivery) CommitLifecycleDelivery(ctx context.Context, pending productionruntime.FixedLifecyclePending, operation string, input any, current application.CurrentRunRequest, result productionruntime.FixedLifecycleResult) (productionruntime.FixedLifecycleReceipt, error) {
	probe.probe("commit-lifecycle")
	return probe.StartRunDelivery.CommitLifecycleDelivery(ctx, pending, operation, input, current, result)
}

func TestResidentWriterLaneCoversEntireDeliveryAndLeavesQueriesAvailable(t *testing.T) {
	for _, operation := range []string{"start-run", "replay-start", productionruntime.FixedLifecycleCollectOperation} {
		t.Run(operation, func(t *testing.T) {
			port, delivery := testHTTPApplication()
			delivery.beginReplay = operation == "replay-start"
			probe := &mutationProbeDelivery{StartRunDelivery: delivery}
			router, err := NewHTTPRouter(port, probe)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().UTC().Add(time.Minute)
			phases := map[string]int{}
			probe.probe = func(phase string) {
				phases[phase]++
				called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error {
					t.Error("background writer entered delivery transaction")
					return nil
				})
				if called || err != nil {
					t.Fatalf("phase=%s called=%v err=%v", phase, called, err)
				}
				// Read routes must remain usable even before application entry and
				// during receipt commit, not merely between timer ticks.
				for _, query := range []struct {
					operation string
					input     any
				}{
					{"status", application.StatusRequest{}},
					{"inspect-run", application.InspectRunRequest{RunID: port.run.RunID}},
				} {
					body := canonicalBody(t, query.input)
					binding := readBinding("request:query", body, query.operation, query.input, deadline)
					response, code, err := router.dispatch(context.Background(), binding, httpRequest{operation: query.operation, requestKey: "request:query", body: body}, deadline)
					if err != nil || code != 200 || response.Disposition != "success" {
						t.Fatalf("query=%s code=%d err=%v", query.operation, code, err)
					}
				}
			}
			var input any
			wireOperation := "start-run"
			if operation == productionruntime.FixedLifecycleCollectOperation {
				wireOperation = operation
				input = application.CollectRunResultRequest{RunID: port.run.RunID, AttemptID: port.run.AttemptID, ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead}
			} else {
				input = application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
			}
			body := canonicalBody(t, input)
			binding, err := clientRequestBinding("request:mutation", body, wireOperation, input, deadline)
			if err != nil {
				t.Fatal(err)
			}
			request := httpRequest{operation: wireOperation, requestKey: "request:mutation", body: body}
			// A timer holding the lane must exclude a public Begin, including
			// exact Start replay. Cancellation while queued must not create intent.
			called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				waiting := &mutationWaitContext{Context: ctx, entered: make(chan struct{})}
				done := make(chan error, 1)
				go func() {
					_, code, err := router.dispatch(waiting, binding, request, deadline)
					if code != 503 {
						done <- errors.New("queued mutation did not reject cancellation")
						return
					}
					done <- err
				}()
				select {
				case <-waiting.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("public mutation never waited for the active background writer")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, ErrUnavailable) {
						t.Fatalf("queued mutation: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("queued mutation ignored cancellation")
				}
				return nil
			})
			if !called || err != nil || len(phases) != 0 {
				t.Fatalf("called=%v err=%v phases=%v", called, err, phases)
			}
			response, code, err := router.dispatch(context.Background(), binding, request, deadline)
			if err != nil || code != 200 || response.Disposition != "success" {
				t.Fatalf("code=%d response=%+v err=%v", code, response, err)
			}
			if len(phases) != 2 {
				t.Fatalf("delivery phases not covered: %v", phases)
			}
			if wireOperation == "start-run" {
				wantStarts := 1
				if delivery.beginReplay {
					wantStarts = 0
				}
				if port.startCalls != wantStarts || response.DeliveryReceipt == nil {
					t.Fatalf("starts=%d receipt=%v", port.startCalls, response.DeliveryReceipt)
				}
			} else if port.collectCalls != 1 || response.LifecycleReceipt == nil {
				t.Fatalf("collect=%d receipt=%v", port.collectCalls, response.LifecycleReceipt)
			}
			// The lane must also be released on successful completion.
			if called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error { return nil }); !called || err != nil {
				t.Fatalf("lane leaked: called=%v err=%v", called, err)
			}
		})
	}
}

// A queued operator cancellation must remain bounded by its own request, even
// if an unrelated verification owns the writer lane. This is a routing/lane
// regression, not proof that an already-running verifier is preemptible.
func TestQueuedCancelExpiresWithoutIntentAndQueriesBypassWriter(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer router.releaseMutation()
	deadline := time.Now().UTC().Add(time.Minute)
	input := application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{
		RunID: port.run.RunID, AttemptID: port.run.AttemptID,
		ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead,
	}, RequestID: "cancel-queued"}
	body := canonicalBody(t, input)
	binding, err := clientRequestBinding("request:queued-cancel", body, productionruntime.FixedLifecycleCancelOperation, input, deadline)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	waiting := &mutationWaitContext{Context: ctx, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, code, err := router.dispatch(waiting, binding, httpRequest{
			operation:  productionruntime.FixedLifecycleCancelOperation,
			requestKey: "request:queued-cancel", body: body,
		}, deadline)
		if code != 503 || !errors.Is(err, ErrUnavailable) {
			done <- errors.New("queued cancel did not expire as unavailable")
			return
		}
		done <- nil
	}()
	select {
	case <-waiting.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not enter bounded writer wait")
	}
	query := application.InspectRunRequest{RunID: port.run.RunID}
	queryBody := canonicalBody(t, query)
	queryBinding := readBinding("request:inspect-during-cancel", queryBody, "inspect-run", query, deadline)
	queried := make(chan error, 1)
	go func() {
		response, code, err := router.dispatch(context.Background(), queryBinding, httpRequest{
			operation: "inspect-run", requestKey: "request:inspect-during-cancel", body: queryBody,
		}, deadline)
		if err != nil || code != 200 || response.Disposition != "success" {
			queried <- errors.New("query failed behind writer")
			return
		}
		queried <- nil
	}()
	select {
	case err := <-queried:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query waited behind writer")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued cancel ignored its request deadline")
	}
	if port.cancelCalls != 0 || delivery.lifecycleBeginCalls != 0 || delivery.lifecycleCommitCalls != 0 {
		t.Fatal("expired queued cancel created a pending intent, mutation or receipt")
	}
	if called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error {
		t.Fatal("cancel released another writer's lane")
		return nil
	}); called || err != nil {
		t.Fatalf("writer ownership changed: called=%v err=%v", called, err)
	}
}

func TestResidentWriterLaneRejectsInvalidAndReleasesOnCallbackError(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("background failure")
	if called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error { return want }); !called || !errors.Is(err, want) {
		t.Fatalf("called=%v err=%v", called, err)
	}
	if err := router.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	router.releaseMutation()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if called, err := router.TryBackgroundMutation(ctx, func(context.Context) error { t.Error("cancelled callback invoked"); return nil }); called || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("called=%v err=%v", called, err)
	}
	if _, err := router.TryBackgroundMutation(context.Background(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := (*HTTPRouter)(nil).acquireMutation(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
