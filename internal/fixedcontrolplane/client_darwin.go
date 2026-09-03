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

type StartRunClientResult struct {
	Projection application.RunStartProjection
	Receipt    productionruntime.FixedDeliveryReceipt
}

func CallStatus(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, deadline time.Time) (application.StatusProjection, error) {
	response, err := call(ctx, authority, "status", "/v1/status", requestKey, application.StatusRequest{}, deadline)
	if err != nil || response.Status == nil || response.Status.Validate() != nil {
		return application.StatusProjection{}, errors.Join(ErrConflict, err)
	}
	return *response.Status, nil
}

func CallInspectRun(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.InspectRunRequest, deadline time.Time) (application.RunProjection, error) {
	if request.Validate() != nil {
		return application.RunProjection{}, ErrInvalid
	}
	response, err := call(ctx, authority, "inspect-run", "/v1/runs/inspect", requestKey, request, deadline)
	if err != nil || response.Run == nil || response.Run.Validate() != nil || response.Run.RunID != request.RunID {
		return application.RunProjection{}, errors.Join(ErrConflict, err)
	}
	return *response.Run, nil
}

func CallStartRun(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.StartRunRequest, deadline time.Time) (StartRunClientResult, error) {
	if request.Validate() != nil {
		return StartRunClientResult{}, ErrInvalid
	}
	response, err := call(ctx, authority, "start-run", "/v1/runs/start", requestKey, request, deadline)
	if err != nil || response.Started == nil || response.DeliveryReceipt == nil || response.Started.Validate() != nil || response.Started.Run.RunID != request.RunID {
		return StartRunClientResult{}, errors.Join(ErrConflict, err)
	}
	return StartRunClientResult{Projection: *response.Started, Receipt: *response.DeliveryReceipt}, nil
}

func call(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, operation, path, requestKey string, request any, deadline time.Time) (httpResponse, error) {
	if ctx == nil || authority == nil || !validHTTPRequestKey(requestKey) || !deadline.After(time.Now().UTC()) {
		return httpResponse{}, ErrInvalid
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return httpResponse{}, ErrInvalid
	}
	body, err := canonical.JSON(raw)
	if err != nil || len(body) == 0 || len(body) > maxHTTPBodyBytes {
		return httpResponse{}, ErrInvalid
	}
	binding, err := clientRequestBinding(requestKey, body, operation, request, deadline.UTC())
	if err != nil {
		return httpResponse{}, ErrInvalid
	}
	if binding.Validate(time.Now().UTC()) != nil {
		return httpResponse{}, ErrInvalid
	}
	connection, err := Dial(ctx, authority, binding)
	if err != nil {
		return httpResponse{}, err
	}
	defer connection.Close()
	header := "POST " + path + " HTTP/1.1\r\nHost: marshal.local\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\nMarshal-Request-Key: " + requestKey + "\r\nConnection: close\r\n\r\n"
	if connection.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil || writeFull(connection, []byte(header)) != nil || writeFull(connection, body) != nil {
		return httpResponse{}, ErrUnavailable
	}
	response, err := readClientHTTPResponse(connection)
	if err != nil {
		return httpResponse{}, err
	}
	if response.Operation != operation || connection.Recheck(ctx) != nil {
		return httpResponse{}, ErrConflict
	}
	if connection.CloseWrite() != nil {
		return httpResponse{}, ErrUnavailable
	}
	return response, nil
}

// clientRequestBinding keeps the authenticated transport binding identical to
// the durable delivery binding for start-run. Read-only requests retain the
// HTTP protocol intent; start-run deliberately uses the delivery protocol
// intent because BeginStartRunBound rejects any independently derived digest.
func clientRequestBinding(requestKey string, body []byte, operation string, request any, deadline time.Time) (RequestBinding, error) {
	if operation != "start-run" {
		return readBinding(requestKey, body, operation, request, deadline), nil
	}
	input, ok := request.(application.StartRunRequest)
	if !ok {
		return RequestBinding{}, ErrInvalid
	}
	binding, err := productionruntime.NewFixedStartRunDeliveryBinding(requestKey, input, deadline)
	if err != nil {
		return RequestBinding{}, ErrInvalid
	}
	return RequestBinding{
		RequestKeyDigest: binding.RequestKeyDigest,
		RequestDigest:    binding.RequestDigest,
		IntentDigest:     binding.ApplicationIntentDigest,
		Deadline:         binding.Deadline,
	}, nil
}

func readClientHTTPResponse(connection *AuthenticatedConnection) (httpResponse, error) {
	if connection == nil || connection.SetReadDeadline(time.Now().Add(writeTimeout)) != nil {
		return httpResponse{}, ErrUnavailable
	}
	reader := bufio.NewReaderSize(connection, maxHTTPSingleHeader+1)
	statusLine, err := readHTTPLine(reader, maxHTTPSingleHeader)
	parts := strings.Split(statusLine, " ")
	if err != nil || len(parts) != 3 || parts[0] != "HTTP/1.1" {
		return httpResponse{}, ErrInvalid
	}
	statusCode, err := strconv.Atoi(parts[1])
	if err != nil || parts[2] != responseReason(statusCode) {
		return httpResponse{}, ErrInvalid
	}
	headers := make(map[string]string)
	total, count := len(statusLine)+2, 0
	for {
		line, lineErr := readHTTPLine(reader, maxHTTPSingleHeader)
		if lineErr != nil {
			return httpResponse{}, lineErr
		}
		total += len(line) + 2
		if total > maxHTTPHeaderBytes {
			return httpResponse{}, ErrInvalid
		}
		if line == "" {
			break
		}
		count++
		colon := strings.IndexByte(line, ':')
		if count > maxHTTPHeaderCount || colon <= 0 {
			return httpResponse{}, ErrInvalid
		}
		name := strings.ToLower(line[:colon])
		if !validHTTPHeaderName(name) {
			return httpResponse{}, ErrInvalid
		}
		if _, exists := headers[name]; exists {
			return httpResponse{}, ErrInvalid
		}
		value := strings.TrimSpace(line[colon+1:])
		if strings.ContainsAny(value, "\r\n\x00") {
			return httpResponse{}, ErrInvalid
		}
		headers[name] = value
	}
	if len(headers) != 3 || headers["content-type"] != "application/json" || headers["connection"] != "close" {
		return httpResponse{}, ErrInvalid
	}
	length, err := strconv.ParseUint(headers["content-length"], 10, 21)
	if err != nil || length == 0 || length > maxHTTPResponseBytes || strconv.FormatUint(length, 10) != headers["content-length"] {
		return httpResponse{}, ErrInvalid
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil || reader.Buffered() != 0 {
		return httpResponse{}, ErrInvalid
	}
	canonicalBody, err := canonical.JSON(body)
	if err != nil || !bytes.Equal(body, canonicalBody) {
		return httpResponse{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response httpResponse
	if decoder.Decode(&response) != nil {
		return httpResponse{}, ErrInvalid
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) == nil || response.SchemaVersion != "fixed-control-http-response/v1" || response.ProtocolRevision != httpProtocolRevision {
		return httpResponse{}, ErrInvalid
	}
	if statusCode != 200 || response.Disposition != "success" {
		if statusCode == 202 && response.Disposition == "pending" {
			return httpResponse{}, errHTTPPending
		}
		if statusCode == 409 {
			return httpResponse{}, ErrConflict
		}
		return httpResponse{}, ErrUnavailable
	}
	return response, nil
}

func responseReason(statusCode int) string {
	switch statusCode {
	case 200:
		return "OK"
	case 202:
		return "Accepted"
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 409:
		return "Conflict"
	case 503:
		return "Service Unavailable"
	default:
		return ""
	}
}
