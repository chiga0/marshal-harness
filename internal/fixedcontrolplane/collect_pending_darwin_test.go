//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

func TestCollectPendingClassifiesOnlyAuthenticatedLiveObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		live    bool
		partial bool
	}{
		{"running", application.NewError("collect-run-result", application.ReasonAttemptStillRunning), true, false},
		{"conflict", application.NewError("collect-run-result", application.ReasonAuthorityConflict), false, false},
		{"unknown", errors.New("private diagnostic must not reach client"), false, false},
		{"partial-result", application.NewError("collect-run-result", application.ReasonAttemptStillRunning), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newEndpointFixture(t)
			endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = endpoint.Close() })
			port, delivery := testHTTPApplication()
			port.collected = application.CollectedRunProjection{}
			if tc.partial {
				port.collected.Run.RunID = port.run.RunID
			}
			port.collectErr = tc.err
			router, err := NewHTTPRouter(port, delivery)
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan error, 1)
			go func() {
				connection, acceptErr := endpoint.Accept(context.Background())
				if acceptErr != nil {
					served <- acceptErr
					return
				}
				defer connection.Close()
				served <- router.ServeAuthenticated(context.Background(), connection)
			}()
			authority, err := productionruntime.OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			request := application.CollectRunResultRequest{RunID: port.run.RunID, AttemptID: port.run.AttemptID, ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead}
			result, err := CallCollectRunResult(context.Background(), authority, "collect:pending", request, time.Now().UTC().Add(time.Minute))
			if result != (CollectRunClientResult{}) || err == nil || errors.Is(err, ErrAttemptStillRunning) != tc.live {
				t.Fatalf("result=%+v err=%v live=%v", result, err, tc.live)
			}
			select {
			case serveErr := <-served:
				if !errors.Is(serveErr, errHTTPPending) {
					t.Fatalf("serve: %v", serveErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("serve timeout")
			}
			if port.collectCalls != 1 || delivery.lifecycleBeginCalls != 1 || delivery.lifecycleCommitCalls != 0 {
				t.Fatalf("collect=%d pending=%d receipt=%d", port.collectCalls, delivery.lifecycleBeginCalls, delivery.lifecycleCommitCalls)
			}
		})
	}
}

func TestRunningPendingRejectsWrongOperationAndSuccessPayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*httpResponse)
		want   error
	}{
		{"exact", func(*httpResponse) {}, ErrAttemptStillRunning},
		{"start", func(r *httpResponse) { r.Operation = "start-run" }, ErrInvalid},
		{"receipt", func(r *httpResponse) { r.LifecycleReceipt = &productionruntime.FixedLifecycleReceipt{} }, ErrInvalid},
		{"result", func(r *httpResponse) { r.Collected = &application.CollectedRunProjection{} }, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, rawClient := unixConnectionPair(t, testBinding())
			client := &AuthenticatedConnection{UnixConn: rawClient, recheck: func(context.Context) error { return nil }, release: func() {}}
			response := errorHTTPResponse(productionruntime.FixedLifecycleCollectOperation, errHTTPPending)
			response.ReasonCode = string(application.ReasonAttemptStillRunning)
			tc.change(&response)
			written := make(chan error, 1)
			go func() { written <- writeHTTPResponse(server, 202, response) }()
			_, err := readClientHTTPResponse(client)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got=%v want=%v", err, tc.want)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
		})
	}
}
