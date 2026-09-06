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

type responsePhaseApplication struct {
	*httpApplicationStub
	statusFn func(context.Context) (application.StatusProjection, error)
}

func (app *responsePhaseApplication) Status(ctx context.Context, _ application.StatusRequest) (application.StatusProjection, error) {
	return app.statusFn(ctx)
}

// Real endpoint, authenticated client and HTTP router; only the business
// application is injected. This is not evidence of a real Pi delivery.
func responsePhaseClient(t *testing.T, fn func(context.Context, application.StatusProjection) (application.StatusProjection, error)) (*productionruntime.FixedEndpointAuthority, <-chan error) {
	t.Helper()
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	port, delivery := testHTTPApplication()
	app := &responsePhaseApplication{httpApplicationStub: port, statusFn: func(ctx context.Context) (application.StatusProjection, error) {
		return fn(ctx, port.status)
	}}
	router, err := NewHTTPRouter(app, delivery)
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
	client, err := productionruntime.OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, served
}

func TestResponsePhaseApplicationCanExceedTransferTimeout(t *testing.T) {
	client, served := responsePhaseClient(t, func(ctx context.Context, result application.StatusProjection) (application.StatusProjection, error) {
		select {
		case <-time.After(writeTimeout + time.Second):
			return result, nil
		case <-ctx.Done():
			return application.StatusProjection{}, ctx.Err()
		}
	})
	result, err := CallStatus(context.Background(), client, "phase:long-application", time.Now().UTC().Add(time.Minute))
	if err != nil || result.Validate() != nil {
		t.Fatalf("application wait incorrectly used byte-transfer timeout: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("post-response protocol did not finish")
	}
}

func TestResponsePhaseParentCancellationInterruptsSocketAndApplication(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	client, served := responsePhaseClient(t, func(ctx context.Context, _ application.StatusProjection) (application.StatusProjection, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return application.StatusProjection{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := CallStatus(ctx, client, "phase:cancel", time.Now().UTC().Add(time.Minute))
		returned <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("application never entered")
	}
	cancel()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("cancelled call reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent cancellation did not interrupt response wait")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel application")
	}
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled connection leaked")
	}
}

func TestResponsePhaseOriginalDeadlineBoundsMissingAndPartialEnvelope(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "partial"}[partial], func(t *testing.T) {
			server, raw := unixConnectionPair(t, testBinding())
			client := &AuthenticatedConnection{UnixConn: raw, release: func() {}}
			if partial {
				if _, err := server.Write([]byte("H")); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			_, err := readClientHTTPResponseUntil(client, started.Add(100*time.Millisecond))
			if err == nil || time.Since(started) > 2*time.Second {
				t.Fatalf("original deadline was extended: %v", err)
			}
		})
	}
}

func TestResponsePhaseServerAllowsBoundedPostResponseRecheck(t *testing.T) {
	server, raw := unixConnectionPair(t, testBinding())
	client := &AuthenticatedConnection{UnixConn: raw, release: func() {}}
	disconnected := watchClientDisconnect(server, func() {})
	served := make(chan error, 1)
	response := errorHTTPResponse("start-run", errHTTPPending)
	go func() { served <- writeHTTPResponseAndAwaitClient(server, 202, response, disconnected) }()
	if _, err := readClientHTTPResponse(client); !errors.Is(err, errHTTPPending) {
		t.Fatal(err)
	}
	// This simulates time spent in the mandatory client recheck. The old one
	// second close raced it; no authority check is bypassed by this fixture.
	select {
	case err := <-served:
		t.Fatalf("server closed before client completed recheck: %v", err)
	case <-time.After(1250 * time.Millisecond):
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("half-close did not finish response")
	}
}

func TestResponsePhaseServerDoesNotExtendRequestForMissingHalfClose(t *testing.T) {
	binding := testBinding()
	binding.Deadline = time.Now().UTC().Add(200 * time.Millisecond).Format(time.RFC3339Nano)
	server, raw := unixConnectionPair(t, binding)
	client := &AuthenticatedConnection{UnixConn: raw, release: func() {}}
	disconnected := watchClientDisconnect(server, func() {})
	served := make(chan error, 1)
	go func() {
		served <- writeHTTPResponseAndAwaitClient(server, 202, errorHTTPResponse("start-run", errHTTPPending), disconnected)
	}()
	if _, err := readClientHTTPResponse(client); !errors.Is(err, errHTTPPending) {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing half-close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server extended original request deadline")
	}
}
