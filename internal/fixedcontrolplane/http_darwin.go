//go:build darwin && arm64

package fixedcontrolplane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

const (
	httpProtocolRevision  = "darwin-fixed-control-http/v1"
	maxHTTPHeaderCount    = 32
	maxHTTPHeaderBytes    = 32 << 10
	maxHTTPSingleHeader   = 8 << 10
	maxHTTPBodyBytes      = 1 << 20
	maxHTTPResponseBytes  = 1 << 20
	readHeaderTimeout     = 5 * time.Second
	readBodyTimeout       = 15 * time.Second
	writeTimeout          = 15 * time.Second
	maxRepositoryInflight = 32
	maxRepositoryQueue    = 32
)

var (
	errHTTPOverloaded  = errors.New("fixedcontrolplane: overloaded")
	errHTTPPending     = errors.New("fixedcontrolplane: pending")
	errHTTPUnsupported = errors.New("fixedcontrolplane: unsupported")
)

// StartRunDelivery is the exact immutable delivery capability consumed by
// the T1 HTTP adapter. FixedDeliveryStore is its production implementation.
type StartRunDelivery interface {
	BeginStartRunBound(context.Context, string, application.StartRunRequest, time.Time, productionruntime.FixedStartRunDeliveryBinding) (productionruntime.FixedDeliveryPending, bool, error)
	ReconcileStartRunDelivery(context.Context, productionruntime.FixedDeliveryPending, application.StartRunRequest, productionruntime.FixedStartRunReconciler) (productionruntime.FixedDeliveryReceipt, bool, error)
}

// HTTPRouter is the bounded T1 application adapter. It owns no business
// authority: mutation is delegated to PublicApplicationPort and its only
// durable transport state is the injected immutable delivery store.
type HTTPRouter struct {
	application application.PublicApplicationPort
	delivery    StartRunDelivery
	inflight    chan struct{}
	queue       chan struct{}
}

func NewHTTPRouter(port application.PublicApplicationPort, delivery StartRunDelivery) (*HTTPRouter, error) {
	if port == nil || delivery == nil {
		return nil, ErrInvalid
	}
	return &HTTPRouter{application: port, delivery: delivery, inflight: make(chan struct{}, maxRepositoryInflight), queue: make(chan struct{}, maxRepositoryQueue)}, nil
}

type httpRequest struct {
	operation  string
	requestKey string
	body       []byte
}

type httpResponse struct {
	SchemaVersion    string                                  `json:"schemaVersion"`
	ProtocolRevision string                                  `json:"protocolRevision"`
	Operation        string                                  `json:"operation,omitempty"`
	Disposition      string                                  `json:"disposition"`
	ReasonCode       string                                  `json:"reasonCode,omitempty"`
	Status           *application.StatusProjection           `json:"status,omitempty"`
	Run              *application.RunProjection              `json:"run,omitempty"`
	Started          *application.RunStartProjection         `json:"started,omitempty"`
	DeliveryReceipt  *productionruntime.FixedDeliveryReceipt `json:"deliveryReceipt,omitempty"`
}

type httpIntent struct {
	ProtocolRevision string `json:"protocolRevision"`
	Operation        string `json:"operation"`
	Request          any    `json:"request"`
}

// ServeAuthenticated serves exactly one HTTP/1.1 request. One request per
// authenticated connection is intentionally stricter than the protocol cap
// of sixteen and removes keep-alive/pipelining ambiguity from the first T1
// slice.
func (router *HTTPRouter) ServeAuthenticated(ctx context.Context, connection *AuthenticatedConnection) error {
	if router == nil || router.application == nil || router.delivery == nil || ctx == nil || connection == nil {
		return ErrInvalid
	}
	request, err := readHTTPRequest(connection)
	if err != nil {
		_ = writeHTTPResponse(connection, transportHTTPStatus(err), errorHTTPResponse("", err))
		return err
	}
	deadline, err := time.Parse(time.RFC3339Nano, connection.Binding.Deadline)
	requestNow := time.Now().UTC()
	if err != nil || deadline.Location() != time.UTC || !deadline.After(requestNow) || deadline.After(requestNow.Add(maxApplicationTime)) {
		disconnected := watchClientDisconnect(connection, func() {})
		return errors.Join(ErrConflict, writeHTTPResponseAndAwaitClient(connection, 409, errorHTTPResponse(request.operation, ErrConflict), disconnected))
	}
	applicationContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	disconnected := watchClientDisconnect(connection, cancel)
	if err := router.admit(applicationContext); err != nil {
		return errors.Join(err, writeHTTPResponseAndAwaitClient(connection, 503, errorHTTPResponse(request.operation, err), disconnected))
	}
	defer router.release()
	if err := connection.Recheck(applicationContext); err != nil {
		return errors.Join(err, writeHTTPResponseAndAwaitClient(connection, 409, errorHTTPResponse(request.operation, err), disconnected))
	}

	// A client close cancels a running application operation. After reading the
	// complete response, a conforming client half-closes its write side only
	// after its post-response authority/peer recheck. Keeping this watcher alive
	// through the response therefore prevents the server from racing that
	// required recheck by closing its end first.
	response, statusCode, operationErr := router.dispatch(applicationContext, connection.Binding, request, deadline)
	recheckContext, recheckCancel := context.WithTimeout(context.Background(), handshakeTimeout)
	recheckErr := connection.Recheck(recheckContext)
	recheckCancel()
	if recheckErr != nil {
		operationErr = errors.Join(operationErr, recheckErr)
		statusCode = 409
		response = errorHTTPResponse(request.operation, ErrConflict)
	}
	if operationErr != nil && response.Disposition == "" {
		response = errorHTTPResponse(request.operation, operationErr)
	}
	return errors.Join(operationErr, writeHTTPResponseAndAwaitClient(connection, statusCode, response, disconnected))
}

func watchClientDisconnect(connection *AuthenticatedConnection, cancel context.CancelFunc) <-chan struct{} {
	disconnected := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = connection.Read(extra[:])
		cancel()
		close(disconnected)
	}()
	return disconnected
}

func writeHTTPResponseAndAwaitClient(connection *AuthenticatedConnection, statusCode int, response httpResponse, disconnected <-chan struct{}) error {
	if err := writeHTTPResponse(connection, statusCode, response); err != nil {
		_ = connection.CloseRead()
		return err
	}
	select {
	case <-disconnected:
		return nil
	case <-time.After(time.Second):
		_ = connection.CloseRead()
		return ErrUnavailable
	}
}

func (router *HTTPRouter) admit(ctx context.Context) error {
	select {
	case router.inflight <- struct{}{}:
		return nil
	default:
	}
	select {
	case router.queue <- struct{}{}:
		defer func() { <-router.queue }()
	default:
		return errHTTPOverloaded
	}
	select {
	case router.inflight <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrUnavailable
	}
}

func (router *HTTPRouter) release() {
	<-router.inflight
}

func (router *HTTPRouter) dispatch(ctx context.Context, authenticated RequestBinding, request httpRequest, deadline time.Time) (httpResponse, int, error) {
	switch request.operation {
	case "status":
		var input application.StatusRequest
		if decodeHTTPBody(request.body, &input) != nil {
			return httpResponse{}, 400, ErrInvalid
		}
		if readBinding(request.requestKey, request.body, request.operation, input, deadline) != authenticated {
			return httpResponse{}, 409, ErrConflict
		}
		projection, err := router.application.Status(ctx, input)
		if err != nil {
			return httpResponse{}, applicationHTTPStatus(err), err
		}
		if projection.Validate() != nil {
			return httpResponse{}, 409, ErrConflict
		}
		return successHTTPResponse(request.operation, &projection, nil, nil, nil), 200, nil
	case "inspect-run":
		var input application.InspectRunRequest
		if decodeHTTPBody(request.body, &input) != nil || input.Validate() != nil {
			return httpResponse{}, 400, ErrInvalid
		}
		if readBinding(request.requestKey, request.body, request.operation, input, deadline) != authenticated {
			return httpResponse{}, 409, ErrConflict
		}
		projection, err := router.application.InspectRun(ctx, input)
		if err != nil {
			return httpResponse{}, applicationHTTPStatus(err), err
		}
		if projection.Validate() != nil || projection.RunID != input.RunID {
			return httpResponse{}, 409, ErrConflict
		}
		return successHTTPResponse(request.operation, nil, &projection, nil, nil), 200, nil
	case "start-run":
		return router.startRun(ctx, authenticated, request, deadline)
	default:
		return httpResponse{}, 404, errHTTPUnsupported
	}
}

func (router *HTTPRouter) startRun(ctx context.Context, authenticated RequestBinding, request httpRequest, deadline time.Time) (httpResponse, int, error) {
	var input application.StartRunRequest
	if decodeHTTPBody(request.body, &input) != nil || input.Validate() != nil {
		return httpResponse{}, 400, ErrInvalid
	}
	binding, err := productionruntime.NewFixedStartRunDeliveryBinding(request.requestKey, input, deadline)
	if err != nil || authenticated != (RequestBinding{RequestKeyDigest: binding.RequestKeyDigest, RequestDigest: binding.RequestDigest, IntentDigest: binding.ApplicationIntentDigest, Deadline: binding.Deadline}) {
		return httpResponse{}, 409, ErrConflict
	}
	pending, replay, err := router.delivery.BeginStartRunBound(ctx, request.requestKey, input, deadline, binding)
	if err != nil {
		return httpResponse{}, applicationHTTPStatus(err), err
	}
	if pending.RequestKeyDigest != binding.RequestKeyDigest || pending.RequestDigest != binding.RequestDigest || pending.ApplicationIntentDigest != binding.ApplicationIntentDigest || pending.Deadline != binding.Deadline {
		return httpResponse{}, 409, ErrConflict
	}
	// A newly-created pending intent has no predecessor delivery to recover.
	// Reconcile before mutation only when BeginStartRunBound reports a durable
	// replay; concurrent identical requests remain safe because StartRun itself
	// rehydrates an exact successor and every path still reconciles afterward.
	// This also avoids turning over the fresh path's held Run graph immediately
	// before StartRun prepares and launches.
	if replay {
		if receipt, applied, reconcileErr := router.delivery.ReconcileStartRunDelivery(ctx, pending, input, router.application); reconcileErr != nil {
			return httpResponse{}, applicationHTTPStatus(reconcileErr), reconcileErr
		} else if applied {
			return router.replayedStart(ctx, input, pending, receipt)
		}
	}
	// Once pending is durable, even a typed Port error cannot prove that no
	// mutation committed before response loss. Only the current RB1 reconcile
	// below may distinguish success from an unresolved delivery.
	_, startErr := router.application.StartRun(ctx, input)
	receipt, applied, err := router.delivery.ReconcileStartRunDelivery(ctx, pending, input, router.application)
	if err != nil {
		return httpResponse{}, applicationHTTPStatus(err), err
	}
	if !applied {
		// The client-facing result must remain an unresolved delivery even when
		// the application returned a typed failure: once pending is durable, that
		// failure cannot prove that no mutation committed before response loss.
		// Preserve it in the internal error chain so the resident server can emit
		// a stable reasonCode instead of discarding the only root-cause signal.
		return errorHTTPResponse(request.operation, errHTTPPending), 202, errors.Join(errHTTPPending, startErr)
	}
	return router.replayedStart(ctx, input, pending, receipt)
}

func (router *HTTPRouter) replayedStart(ctx context.Context, request application.StartRunRequest, pending productionruntime.FixedDeliveryPending, receipt productionruntime.FixedDeliveryReceipt) (httpResponse, int, error) {
	projection, found, err := router.application.ReconcileStartRun(ctx, request)
	if err != nil || !found || productionruntime.ValidateFixedStartRunDeliveryReceipt(pending, receipt) != nil || projection.Validate() != nil || receipt.RunID != projection.Run.RunID || receipt.AttemptID != projection.Run.AttemptID || receipt.PostRevision != projection.Run.Sequence || receipt.PostAuthorityHead != projection.Run.AuthorityHead || receipt.ApplicationReceiptFactDigest != projection.Run.AuthorityHead || receipt.PreparationDigest != projection.Prepared.PreparationDigest {
		return httpResponse{}, 409, ErrConflict
	}
	return successHTTPResponse("start-run", nil, nil, &projection, &receipt), 200, nil
}

func readBinding(requestKey string, body []byte, operation string, request any, deadline time.Time) RequestBinding {
	intentRaw, err := json.Marshal(httpIntent{ProtocolRevision: httpProtocolRevision, Operation: operation, Request: request})
	if err != nil {
		return RequestBinding{}
	}
	intentDigest, err := canonical.DigestJSON(intentRaw)
	if err != nil {
		return RequestBinding{}
	}
	return RequestBinding{RequestKeyDigest: canonical.DigestBytes([]byte(requestKey)), RequestDigest: canonical.DigestBytes(body), IntentDigest: intentDigest, Deadline: deadline.Format(time.RFC3339Nano)}
}

func readHTTPRequest(connection *AuthenticatedConnection) (httpRequest, error) {
	if err := connection.SetReadDeadline(time.Now().Add(readHeaderTimeout)); err != nil {
		return httpRequest{}, ErrUnavailable
	}
	reader := bufio.NewReaderSize(connection, maxHTTPSingleHeader+1)
	requestLine, err := readHTTPLine(reader, maxHTTPSingleHeader)
	if err != nil {
		return httpRequest{}, err
	}
	parts := strings.Split(requestLine, " ")
	if len(parts) != 3 || parts[0] != "POST" || parts[2] != "HTTP/1.1" {
		return httpRequest{}, ErrInvalid
	}
	operation := ""
	switch parts[1] {
	case "/v1/status":
		operation = "status"
	case "/v1/runs/inspect":
		operation = "inspect-run"
	case "/v1/runs/start":
		operation = "start-run"
	default:
		// Consume and validate the complete request before returning an
		// unsupported-operation response. This lets the authenticated peer use
		// the same post-response half-close protocol as every other request.
		operation = "unsupported"
	}
	headers := make(map[string]string)
	total := len(requestLine) + 2
	headerCount := 0
	for {
		line, lineErr := readHTTPLine(reader, maxHTTPSingleHeader)
		if lineErr != nil {
			return httpRequest{}, lineErr
		}
		total += len(line) + 2
		if total > maxHTTPHeaderBytes {
			return httpRequest{}, ErrInvalid
		}
		if line == "" {
			break
		}
		headerCount++
		if headerCount > maxHTTPHeaderCount {
			return httpRequest{}, ErrInvalid
		}
		if line[0] == ' ' || line[0] == '\t' {
			return httpRequest{}, ErrInvalid
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			return httpRequest{}, ErrInvalid
		}
		name := strings.ToLower(line[:colon])
		if !validHTTPHeaderName(name) {
			return httpRequest{}, ErrInvalid
		}
		if _, exists := headers[name]; exists {
			return httpRequest{}, ErrInvalid
		}
		value := strings.TrimSpace(line[colon+1:])
		if strings.ContainsAny(value, "\r\n\x00") {
			return httpRequest{}, ErrInvalid
		}
		headers[name] = value
	}
	for name := range headers {
		switch name {
		case "host", "content-type", "content-length", "marshal-request-key", "connection":
		default:
			return httpRequest{}, ErrInvalid
		}
	}
	if headers["host"] != "marshal.local" || headers["content-type"] != "application/json" || headers["connection"] != "close" || !validHTTPRequestKey(headers["marshal-request-key"]) {
		return httpRequest{}, ErrInvalid
	}
	length, err := strconv.ParseUint(headers["content-length"], 10, 21)
	if err != nil || strconv.FormatUint(length, 10) != headers["content-length"] || length == 0 || length > maxHTTPBodyBytes {
		return httpRequest{}, ErrInvalid
	}
	if err := connection.SetReadDeadline(time.Now().Add(readBodyTimeout)); err != nil {
		return httpRequest{}, ErrUnavailable
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil || reader.Buffered() != 0 {
		return httpRequest{}, ErrInvalid
	}
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return httpRequest{}, ErrUnavailable
	}
	return httpRequest{operation: operation, requestKey: headers["marshal-request-key"], body: body}, nil
}

func validHTTPRequestKey(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

func readHTTPLine(reader *bufio.Reader, limit int) (string, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) < 2 || len(line) > limit+2 || line[len(line)-2] != '\r' {
		return "", ErrInvalid
	}
	return string(line[:len(line)-2]), nil
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func decodeHTTPBody(raw []byte, target any) error {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrInvalid
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) == nil {
		return ErrInvalid
	}
	return nil
}

func successHTTPResponse(operation string, status *application.StatusProjection, run *application.RunProjection, started *application.RunStartProjection, receipt *productionruntime.FixedDeliveryReceipt) httpResponse {
	return httpResponse{SchemaVersion: "fixed-control-http-response/v1", ProtocolRevision: httpProtocolRevision, Operation: operation, Disposition: "success", Status: status, Run: run, Started: started, DeliveryReceipt: receipt}
}

func errorHTTPResponse(operation string, err error) httpResponse {
	reason := "unavailable"
	switch {
	case errors.Is(err, errHTTPOverloaded):
		reason = "control-plane-overloaded"
	case errors.Is(err, errHTTPPending), errors.Is(err, productionruntime.ErrFixedDeliveryUnknown):
		reason = "delivery-pending"
	case errors.Is(err, errHTTPUnsupported):
		reason = "unsupported-operation"
	case errors.Is(err, ErrInvalid):
		reason = "invalid-request"
	case errors.Is(err, ErrConflict), errors.Is(err, productionruntime.ErrFixedDeliveryConflict):
		reason = "authority-conflict"
	default:
		var applicationError *application.Error
		if errors.As(err, &applicationError) {
			reason = string(applicationError.Reason)
		}
	}
	disposition := "error"
	if reason == "delivery-pending" {
		disposition = "pending"
	}
	return httpResponse{SchemaVersion: "fixed-control-http-response/v1", ProtocolRevision: httpProtocolRevision, Operation: operation, Disposition: disposition, ReasonCode: reason}
}

func applicationHTTPStatus(err error) int {
	switch {
	case err == nil:
		return 409
	case application.HasReason(err, application.ReasonInvalidRequest):
		return 400
	case application.HasReason(err, application.ReasonAuthorityConflict), application.HasReason(err, application.ReasonOwnerNotCurrent):
		return 409
	case errors.Is(err, ErrInvalid):
		return 400
	case errors.Is(err, ErrConflict), errors.Is(err, productionruntime.ErrFixedDeliveryConflict):
		return 409
	case errors.Is(err, errHTTPPending), errors.Is(err, productionruntime.ErrFixedDeliveryUnknown):
		return 202
	default:
		return 503
	}
}

func transportHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errHTTPUnsupported):
		return 404
	case errors.Is(err, ErrConflict):
		return 409
	case errors.Is(err, ErrUnavailable), errors.Is(err, errHTTPOverloaded):
		return 503
	default:
		return 400
	}
}

func writeHTTPResponse(connection *AuthenticatedConnection, statusCode int, response httpResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return ErrUnavailable
	}
	body, err = canonical.JSON(body)
	if err != nil || len(body) == 0 || len(body) > maxHTTPResponseBytes {
		return ErrUnavailable
	}
	reason := responseReason(statusCode)
	if reason == "" {
		return ErrUnavailable
	}
	header := "HTTP/1.1 " + strconv.Itoa(statusCode) + " " + reason + "\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n"
	if err := connection.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return ErrUnavailable
	}
	if writeFull(connection, []byte(header)) != nil || writeFull(connection, body) != nil {
		return ErrUnavailable
	}
	return nil
}
