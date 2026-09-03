//go:build darwin && arm64

package fixedcontrolplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"golang.org/x/sys/unix"
)

type httpApplicationStub struct {
	mu             sync.Mutex
	statusCalls    int
	startCalls     int
	inspectCalls   int
	reconcileCalls int
	status         application.StatusProjection
	started        application.RunStartProjection
	run            application.RunProjection
	startErr       error
}

func (stub *httpApplicationStub) Status(context.Context, application.StatusRequest) (application.StatusProjection, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.statusCalls++
	return stub.status, nil
}

func (stub *httpApplicationStub) StartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.startCalls++
	return stub.started, stub.startErr
}

func (stub *httpApplicationStub) ReconcileStartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.reconcileCalls++
	return stub.started, true, nil
}

func (stub *httpApplicationStub) InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.inspectCalls++
	return stub.run, nil
}

type httpDeliveryStub struct {
	mu             sync.Mutex
	beginCalls     int
	reconcileCalls int
	applyAt        int
	pending        productionruntime.FixedDeliveryPending
	receipt        productionruntime.FixedDeliveryReceipt
	wrongPending   bool
}

func (stub *httpDeliveryStub) BeginStartRunBound(_ context.Context, _ string, _ application.StartRunRequest, _ time.Time, binding productionruntime.FixedStartRunDeliveryBinding) (productionruntime.FixedDeliveryPending, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.beginCalls++
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	stub.pending = productionruntime.FixedDeliveryPending{
		SchemaVersion: "fixed-delivery-record/v2", ProtocolRevision: "darwin-fixed-delivery/v2", RecordType: "pending", Operation: "start-run",
		OwnerAcquisitionDigest: digest("owner-acquisition"), OwnerFactDigest: digest("owner-fact"), RepositoryDigest: digest("repository"), NamespaceDigest: digest("namespace"), AuthorityRootDigest: digest("authority-root"),
		RequestKeyDigest: binding.RequestKeyDigest, RequestDigest: binding.RequestDigest, ApplicationIntentDigest: binding.ApplicationIntentDigest, Deadline: binding.Deadline,
	}
	if err := sealHTTPPending(&stub.pending); err != nil {
		return productionruntime.FixedDeliveryPending{}, false, err
	}
	stub.receipt.SchemaVersion = "fixed-delivery-record/v2"
	stub.receipt.ProtocolRevision = "darwin-fixed-delivery/v2"
	stub.receipt.RecordType = "receipt-ref"
	stub.receipt.Operation = "start-run"
	stub.receipt.PendingDigest = stub.pending.Digest
	if err := sealHTTPReceipt(&stub.receipt); err != nil {
		return productionruntime.FixedDeliveryPending{}, false, err
	}
	return stub.pending, false, nil
}

func (stub *httpDeliveryStub) ReconcileStartRunDelivery(context.Context, productionruntime.FixedDeliveryPending, application.StartRunRequest, productionruntime.FixedStartRunReconciler) (productionruntime.FixedDeliveryReceipt, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.reconcileCalls++
	if stub.applyAt == 0 || stub.reconcileCalls < stub.applyAt {
		return productionruntime.FixedDeliveryReceipt{}, false, nil
	}
	receipt := stub.receipt
	if stub.wrongPending {
		receipt.PendingDigest = canonical.DigestBytes([]byte("wrong-pending"))
		if err := sealHTTPReceipt(&receipt); err != nil {
			return productionruntime.FixedDeliveryReceipt{}, false, err
		}
	}
	return receipt, true, nil
}

func sealHTTPPending(pending *productionruntime.FixedDeliveryPending) error {
	pending.Digest = ""
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	pending.Digest, err = canonical.DigestJSON(raw)
	return err
}

func sealHTTPReceipt(receipt *productionruntime.FixedDeliveryReceipt) error {
	receipt.Digest = ""
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt.Digest, err = canonical.DigestJSON(raw)
	return err
}

func testHTTPApplication() (*httpApplicationStub, *httpDeliveryStub) {
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	run := application.RunProjection{TaskID: "task:test", RunID: "run:test", AttemptID: "attempt:1", State: domain.StateRunning, Sequence: 4, AuthorityHead: digest("running-head")}
	started := application.RunStartProjection{
		Prepared: application.PreparedRunStart{
			ProtocolRevision: application.PreparedRunStartProtocolRevision, TaskID: run.TaskID, RunID: run.RunID, AttemptID: run.AttemptID,
			ReservationFactDigest: digest("reservation"), AttemptOpenedFactDigest: digest("attempt-opened"), AttemptOrdinal: 1, AttemptsUsedBefore: 0, MaxAttempts: 1,
			State: domain.StateReady, Sequence: 3, AuthorityHead: digest("ready-head"), PreparationDigest: digest("preparation"),
		},
		Run: run,
	}
	applicationStub := &httpApplicationStub{
		status: application.StatusProjection{
			ProtocolRevision: application.ProtocolRevision, Availability: application.AvailabilityReady, PlatformProfileID: "darwin-local-dogfood",
			AgentProvider: "pi", AgentVersion: "0.84.4", AgentClosureProfile: "pi-test/v1", AgentIdentityDigest: digest("pi"), OwnerEpoch: 1, OwnerFactDigest: digest("owner"),
		},
		started: started,
		run:     run,
	}
	delivery := &httpDeliveryStub{
		applyAt: 2,
		receipt: productionruntime.FixedDeliveryReceipt{
			PreparationDigest: started.Prepared.PreparationDigest, ApplicationReceiptFactDigest: run.AuthorityHead,
			RunID: run.RunID, AttemptID: run.AttemptID, PostRevision: run.Sequence, PostAuthorityHead: run.AuthorityHead,
		},
	}
	return applicationStub, delivery
}

func unixConnectionPair(t *testing.T, binding RequestBinding) (*AuthenticatedConnection, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	serverFile := os.NewFile(uintptr(fds[0]), "fixed-http-server")
	clientFile := os.NewFile(uintptr(fds[1]), "fixed-http-client")
	serverRaw, serverErr := net.FileConn(serverFile)
	clientRaw, clientErr := net.FileConn(clientFile)
	_ = serverFile.Close()
	_ = clientFile.Close()
	if serverErr != nil || clientErr != nil {
		t.Fatalf("file conn: server=%v client=%v", serverErr, clientErr)
	}
	server, serverOK := serverRaw.(*net.UnixConn)
	client, clientOK := clientRaw.(*net.UnixConn)
	if !serverOK || !clientOK {
		t.Fatal("socketpair did not produce Unix connections")
	}
	authenticated := &AuthenticatedConnection{UnixConn: server, Binding: binding, recheck: func(context.Context) error { return nil }, release: func() {}}
	t.Cleanup(func() {
		_ = authenticated.Close()
		_ = client.Close()
	})
	return authenticated, client
}

func canonicalBody(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func callHTTPRouter(t *testing.T, router *HTTPRouter, binding RequestBinding, path, requestKey string, body []byte) (int, httpResponse, error) {
	t.Helper()
	server, client := unixConnectionPair(t, binding)
	served := make(chan error, 1)
	go func() {
		served <- router.ServeAuthenticated(context.Background(), server)
		_ = server.Close()
	}()
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := "POST " + path + " HTTP/1.1\r\nHost: marshal.local\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\nMarshal-Request-Key: " + requestKey + "\r\nConnection: close\r\n\r\n"
	if _, err := client.Write(append([]byte(request), body...)); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err != nil || len(responseBody) > maxHTTPResponseBytes {
		t.Fatalf("read response: size=%d err=%v", len(responseBody), err)
	}
	var decoded httpResponse
	if decodeHTTPBody(responseBody, &decoded) != nil {
		t.Fatalf("invalid response: %s", responseBody)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case serveErr := <-served:
		return response.StatusCode, decoded, serveErr
	case <-time.After(10 * time.Second):
		t.Fatal("serve timeout")
		return 0, httpResponse{}, nil
	}
}

func TestFixedClientViewCallsResidentStatus(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	port, delivery := testHTTPApplication()
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
	clientAuthority, err := productionruntime.OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	defer clientAuthority.Close()
	projection, err := CallStatus(context.Background(), clientAuthority, "status:independent-client", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("CallStatus: %v", err)
	}
	if projection != port.status {
		t.Fatalf("projection=%+v want=%+v", projection, port.status)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve timeout")
	}
}

func TestFixedClientStartRunUsesDurableDeliveryBinding(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	port, delivery := testHTTPApplication()
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
	clientAuthority, err := productionruntime.OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	defer clientAuthority.Close()
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	result, err := CallStartRun(context.Background(), clientAuthority, "start:independent-client", request, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("CallStartRun: %v", err)
	}
	if result.Projection != port.started || result.Receipt != delivery.receipt || delivery.beginCalls != 1 || port.startCalls != 1 {
		t.Fatalf("result=%+v begin=%d start=%d", result, delivery.beginCalls, port.startCalls)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve timeout")
	}
}

func TestHTTPRouterStatusUsesBoundCanonicalRequest(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	body := canonicalBody(t, application.StatusRequest{})
	deadline := time.Now().UTC().Add(time.Minute)
	binding := readBinding("request:status", body, "status", application.StatusRequest{}, deadline)
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/status", "request:status", body)
	if serveErr != nil || code != 200 || response.Disposition != "success" || response.Status == nil || port.statusCalls != 1 || delivery.beginCalls != 0 {
		t.Fatalf("code=%d response=%+v err=%v statusCalls=%d beginCalls=%d", code, response, serveErr, port.statusCalls, delivery.beginCalls)
	}
}

func TestHTTPRouterStartRunPublishesPendingBeforeSingleMutationAndExactReceipt(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	body := canonicalBody(t, request)
	deadline := time.Now().UTC().Add(time.Minute)
	deliveryBinding, err := productionruntime.NewFixedStartRunDeliveryBinding("request:start", request, deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding := RequestBinding{RequestKeyDigest: deliveryBinding.RequestKeyDigest, RequestDigest: deliveryBinding.RequestDigest, IntentDigest: deliveryBinding.ApplicationIntentDigest, Deadline: deliveryBinding.Deadline}
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/runs/start", "request:start", body)
	if serveErr != nil || code != 200 || response.Disposition != "success" || response.Started == nil || response.DeliveryReceipt == nil || delivery.beginCalls != 1 || delivery.reconcileCalls != 2 || port.startCalls != 1 || port.reconcileCalls != 1 {
		t.Fatalf("code=%d response=%+v err=%v begin=%d deliveryReconcile=%d start=%d appReconcile=%d", code, response, serveErr, delivery.beginCalls, delivery.reconcileCalls, port.startCalls, port.reconcileCalls)
	}
}

func TestHTTPRouterRejectsBindingMismatchBeforeDeliveryOrApplication(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	body := canonicalBody(t, application.StatusRequest{})
	deadline := time.Now().UTC().Add(time.Minute)
	binding := readBinding("request:status", body, "status", application.StatusRequest{}, deadline)
	binding.RequestDigest = canonical.DigestBytes([]byte("forged"))
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/status", "request:status", body)
	if !errors.Is(serveErr, ErrConflict) || code != 409 || response.ReasonCode != "authority-conflict" || port.statusCalls != 0 || delivery.beginCalls != 0 {
		t.Fatalf("code=%d response=%+v err=%v statusCalls=%d beginCalls=%d", code, response, serveErr, port.statusCalls, delivery.beginCalls)
	}
}

func TestHTTPRouterReturnsPendingWithoutClaimingApplicationSuccess(t *testing.T) {
	port, delivery := testHTTPApplication()
	delivery.applyAt = 0
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	body := canonicalBody(t, request)
	deadline := time.Now().UTC().Add(time.Minute)
	deliveryBinding, err := productionruntime.NewFixedStartRunDeliveryBinding("request:pending", request, deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding := RequestBinding{RequestKeyDigest: deliveryBinding.RequestKeyDigest, RequestDigest: deliveryBinding.RequestDigest, IntentDigest: deliveryBinding.ApplicationIntentDigest, Deadline: deliveryBinding.Deadline}
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/runs/start", "request:pending", body)
	if !errors.Is(serveErr, errHTTPPending) || code != 202 || response.Disposition != "pending" || response.ReasonCode != "delivery-pending" || response.Started != nil || response.DeliveryReceipt != nil || delivery.beginCalls != 1 || port.startCalls != 1 {
		t.Fatalf("code=%d response=%+v err=%v begin=%d start=%d", code, response, serveErr, delivery.beginCalls, port.startCalls)
	}
}

func TestHTTPRouterStartRunErrorStillRequiresCurrentLedgerReconcile(t *testing.T) {
	port, delivery := testHTTPApplication()
	port.startErr = application.NewError("start-run", application.ReasonAuthorityConflict)
	delivery.applyAt = 0
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	body := canonicalBody(t, request)
	deadline := time.Now().UTC().Add(time.Minute)
	deliveryBinding, err := productionruntime.NewFixedStartRunDeliveryBinding("request:start-error", request, deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding := RequestBinding{RequestKeyDigest: deliveryBinding.RequestKeyDigest, RequestDigest: deliveryBinding.RequestDigest, IntentDigest: deliveryBinding.ApplicationIntentDigest, Deadline: deliveryBinding.Deadline}
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/runs/start", "request:start-error", body)
	if !errors.Is(serveErr, errHTTPPending) || code != 202 || response.Disposition != "pending" || response.ReasonCode != "delivery-pending" || delivery.reconcileCalls != 2 || port.startCalls != 1 {
		t.Fatalf("code=%d response=%+v err=%v deliveryReconcile=%d start=%d", code, response, serveErr, delivery.reconcileCalls, port.startCalls)
	}
}

func TestHTTPRouterReplaysReceiptWithoutSecondMutation(t *testing.T) {
	port, delivery := testHTTPApplication()
	delivery.applyAt = 1
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	body := canonicalBody(t, request)
	deadline := time.Now().UTC().Add(time.Minute)
	deliveryBinding, err := productionruntime.NewFixedStartRunDeliveryBinding("request:replay", request, deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding := RequestBinding{RequestKeyDigest: deliveryBinding.RequestKeyDigest, RequestDigest: deliveryBinding.RequestDigest, IntentDigest: deliveryBinding.ApplicationIntentDigest, Deadline: deliveryBinding.Deadline}
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/runs/start", "request:replay", body)
	if serveErr != nil || code != 200 || response.Disposition != "success" || response.DeliveryReceipt == nil || delivery.reconcileCalls != 1 || port.startCalls != 0 || port.reconcileCalls != 1 {
		t.Fatalf("code=%d response=%+v err=%v deliveryReconcile=%d start=%d appReconcile=%d", code, response, serveErr, delivery.reconcileCalls, port.startCalls, port.reconcileCalls)
	}
}

func TestHTTPRouterRejectsReceiptForDifferentPending(t *testing.T) {
	port, delivery := testHTTPApplication()
	delivery.applyAt = 1
	delivery.wrongPending = true
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	request := application.StartRunRequest{RunID: port.run.RunID, ExpectedSequence: port.started.Prepared.Sequence, ExpectedAuthorityHead: port.started.Prepared.AuthorityHead}
	body := canonicalBody(t, request)
	deadline := time.Now().UTC().Add(time.Minute)
	deliveryBinding, err := productionruntime.NewFixedStartRunDeliveryBinding("request:wrong-receipt", request, deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding := RequestBinding{RequestKeyDigest: deliveryBinding.RequestKeyDigest, RequestDigest: deliveryBinding.RequestDigest, IntentDigest: deliveryBinding.ApplicationIntentDigest, Deadline: deliveryBinding.Deadline}
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/runs/start", "request:wrong-receipt", body)
	if !errors.Is(serveErr, ErrConflict) || code != 409 || response.ReasonCode != "authority-conflict" || response.Disposition != "error" || port.startCalls != 0 {
		t.Fatalf("code=%d response=%+v err=%v start=%d", code, response, serveErr, port.startCalls)
	}
}

func TestHTTPRouterRejectsNonCanonicalUnknownBodyAndDeadlineBeforePort(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	unknownBody := []byte(`{"unknown":true}`)
	deadline := time.Now().UTC().Add(time.Minute)
	binding := readBinding("request:unknown", unknownBody, "status", application.StatusRequest{}, deadline)
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/status", "request:unknown", unknownBody)
	if !errors.Is(serveErr, ErrInvalid) || code != 400 || response.ReasonCode != "invalid-request" || port.statusCalls != 0 || delivery.beginCalls != 0 {
		t.Fatalf("unknown code=%d response=%+v err=%v statusCalls=%d beginCalls=%d", code, response, serveErr, port.statusCalls, delivery.beginCalls)
	}

	body := canonicalBody(t, application.StatusRequest{})
	deadline = time.Now().UTC().Add(maxApplicationTime + time.Minute)
	binding = readBinding("request:long-deadline", body, "status", application.StatusRequest{}, deadline)
	code, response, serveErr = callHTTPRouter(t, router, binding, "/v1/status", "request:long-deadline", body)
	if !errors.Is(serveErr, ErrConflict) || code != 409 || response.ReasonCode != "authority-conflict" || port.statusCalls != 0 || delivery.beginCalls != 0 {
		t.Fatalf("deadline code=%d response=%+v err=%v statusCalls=%d beginCalls=%d", code, response, serveErr, port.statusCalls, delivery.beginCalls)
	}
}

func TestValidHTTPRequestKeyRejectsWhitespaceAndControlBytes(t *testing.T) {
	for _, value := range []string{"", " request", "request ", "request key", "request\x00key", "request\x1fkey", "request\x7fkey"} {
		if validHTTPRequestKey(value) {
			t.Fatalf("invalid request key accepted")
		}
	}
	if !validHTTPRequestKey("request:key-1") {
		t.Fatal("canonical request key rejected")
	}
}

func TestHTTPRouterRejectsUnsupportedAndOverloadedRequestsBeforePort(t *testing.T) {
	port, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(port, delivery)
	if err != nil {
		t.Fatal(err)
	}
	body := canonicalBody(t, application.StatusRequest{})
	deadline := time.Now().UTC().Add(time.Minute)
	binding := readBinding("request:unsupported", body, "status", application.StatusRequest{}, deadline)
	code, response, serveErr := callHTTPRouter(t, router, binding, "/v1/unsupported", "request:unsupported", body)
	if !errors.Is(serveErr, errHTTPUnsupported) || code != 404 || response.ReasonCode != "unsupported-operation" || port.statusCalls != 0 || delivery.beginCalls != 0 {
		t.Fatalf("unsupported code=%d response=%+v err=%v", code, response, serveErr)
	}

	for index := 0; index < maxRepositoryInflight; index++ {
		router.inflight <- struct{}{}
	}
	for index := 0; index < maxRepositoryQueue; index++ {
		router.queue <- struct{}{}
	}
	binding = readBinding("request:overloaded", body, "status", application.StatusRequest{}, deadline)
	code, response, serveErr = callHTTPRouter(t, router, binding, "/v1/status", "request:overloaded", body)
	if !errors.Is(serveErr, errHTTPOverloaded) || code != 503 || response.ReasonCode != "control-plane-overloaded" || port.statusCalls != 0 || delivery.beginCalls != 0 {
		t.Fatalf("overloaded code=%d response=%+v err=%v", code, response, serveErr)
	}
}
