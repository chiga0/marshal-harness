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

func TestCancelNeverConfusesPendingCleanupWithSuccessOrTooLate(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reason           application.ReasonCode
		partial, tooLate bool
	}{
		{"admission-first", application.ReasonStopTooLate, false, true},
		{"cleanup-incomplete", application.ReasonAuthorityConflict, false, false},
		{"partial-too-late", application.ReasonStopTooLate, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newEndpointFixture(t)
			endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = endpoint.Close() })
			port, delivery := testHTTPApplication()
			port.cancelErr = application.NewError("cancel-run", tc.reason)
			if tc.partial {
				port.stopped.Run.RunID = port.run.RunID
			}
			router, err := NewHTTPRouter(port, delivery)
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan error, 1)
			go func() {
				connection, err := endpoint.Accept(context.Background())
				if err != nil {
					served <- err
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
			request := application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: port.run.RunID, AttemptID: port.run.AttemptID, ExpectedSequence: port.run.Sequence, ExpectedAuthorityHead: port.run.AuthorityHead}, RequestID: "cancel-1"}
			result, err := CallCancelRun(context.Background(), authority, "cancel:request", request, time.Now().UTC().Add(time.Minute))
			if result != (CancelRunClientResult{}) || err == nil || application.HasReason(err, application.ReasonStopTooLate) != tc.tooLate {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			select {
			case serveErr := <-served:
				if tc.tooLate {
					if !application.HasReason(serveErr, application.ReasonStopTooLate) {
						t.Fatalf("server: %v", serveErr)
					}
				} else if !errors.Is(serveErr, errHTTPPending) {
					t.Fatalf("server: %v", serveErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("serve timeout")
			}
			if port.cancelCalls != 1 || delivery.lifecycleBeginCalls != 1 || delivery.lifecycleCommitCalls != 0 {
				t.Fatalf("cancel=%d pending=%d receipt=%d", port.cancelCalls, delivery.lifecycleBeginCalls, delivery.lifecycleCommitCalls)
			}
		})
	}
}

func TestCollectStoppedIsTerminalNotSuccessOrLivePending(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified-stop", true: "partial-projection"}[partial], func(t *testing.T) {
			fixture := newEndpointFixture(t)
			endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = endpoint.Close() })
			port, delivery := testHTTPApplication()
			port.collected = application.CollectedRunProjection{}
			if partial {
				port.collected.Run.RunID = port.run.RunID
			}
			port.collectErr = application.NewError("collect-run-result", application.ReasonRunStopped)
			router, err := NewHTTPRouter(port, delivery)
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan error, 1)
			go func() {
				connection, err := endpoint.Accept(context.Background())
				if err != nil {
					served <- err
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
			result, err := CallCollectRunResult(context.Background(), authority, "collect:stopped", request, time.Now().UTC().Add(time.Minute))
			if err == nil || application.HasReason(err, application.ReasonRunStopped) == partial || result.Projection != (application.CollectedRunProjection{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			select {
			case serveErr := <-served:
				if partial && !errors.Is(serveErr, errHTTPPending) || !partial && !application.HasReason(serveErr, application.ReasonRunStopped) {
					t.Fatalf("server: %v", serveErr)
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
