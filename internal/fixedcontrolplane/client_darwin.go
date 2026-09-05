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

type CollectRunClientResult struct {
	Projection application.CollectedRunProjection
	Receipt    productionruntime.FixedLifecycleReceipt
}

type VerifyRunClientResult struct {
	Projection application.VerificationProjection
	Receipt    productionruntime.FixedLifecycleReceipt
}

type ReviewPacketClientResult struct {
	Projection application.ReviewPacketProjection
	Receipt    productionruntime.FixedLifecycleReceipt
}

type ReviewDecisionClientResult struct {
	Projection application.ReviewDecisionProjection
	Receipt    productionruntime.FixedLifecycleReceipt
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

func CallCollectRunResult(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.CollectRunResultRequest, deadline time.Time) (CollectRunClientResult, error) {
	if request.Validate() != nil {
		return CollectRunClientResult{}, ErrInvalid
	}
	response, err := call(ctx, authority, productionruntime.FixedLifecycleCollectOperation, "/v1/runs/collect", requestKey, request, deadline)
	if err != nil {
		return CollectRunClientResult{}, err
	}
	if response.Collected == nil || response.LifecycleReceipt == nil || response.Collected.Validate() != nil {
		return CollectRunClientResult{}, ErrConflict
	}
	return CollectRunClientResult{Projection: *response.Collected, Receipt: *response.LifecycleReceipt}, nil
}

func CallVerifyRun(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.VerifyRunRequest, deadline time.Time) (VerifyRunClientResult, error) {
	if request.Validate() != nil {
		return VerifyRunClientResult{}, ErrInvalid
	}
	response, err := call(ctx, authority, productionruntime.FixedLifecycleVerifyOperation, "/v1/runs/verify", requestKey, request, deadline)
	if err != nil || response.Verification == nil || response.LifecycleReceipt == nil || response.Verification.Validate() != nil {
		return VerifyRunClientResult{}, errors.Join(ErrConflict, err)
	}
	return VerifyRunClientResult{Projection: *response.Verification, Receipt: *response.LifecycleReceipt}, nil
}

func CallBuildReviewPacket(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.BuildReviewPacketRequest, deadline time.Time) (ReviewPacketClientResult, error) {
	if request.Validate() != nil {
		return ReviewPacketClientResult{}, ErrInvalid
	}
	response, err := call(ctx, authority, productionruntime.FixedLifecycleReviewOperation, "/v1/runs/review-packet", requestKey, request, deadline)
	if err != nil || response.ReviewPacket == nil || response.LifecycleReceipt == nil || response.ReviewPacket.Validate() != nil {
		return ReviewPacketClientResult{}, errors.Join(ErrConflict, err)
	}
	return ReviewPacketClientResult{Projection: *response.ReviewPacket, Receipt: *response.LifecycleReceipt}, nil
}

func CallApplyReviewDecision(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.ApplyReviewDecisionRequest, deadline time.Time) (ReviewDecisionClientResult, error) {
	if request.Validate() != nil {
		return ReviewDecisionClientResult{}, ErrInvalid
	}
	response, err := call(ctx, authority, productionruntime.FixedLifecycleDecisionOperation, "/v1/runs/decision", requestKey, request, deadline)
	if err != nil || response.Decision == nil || response.LifecycleReceipt == nil || response.Decision.Validate() != nil {
		return ReviewDecisionClientResult{}, errors.Join(ErrConflict, err)
	}
	return ReviewDecisionClientResult{Projection: *response.Decision, Receipt: *response.LifecycleReceipt}, nil
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
	response, responseErr := readClientHTTPResponse(connection)
	// A syntactically valid non-success response still completes the
	// authenticated request protocol. Recheck the peer and half-close only
	// after consuming that exact response; otherwise 202/409/503 returns can
	// make the server mistake an application outcome for a transport failure.
	if response.SchemaVersion == "" {
		return httpResponse{}, responseErr
	}
	if response.Operation != operation {
		return httpResponse{}, ErrConflict
	}
	var recheckErr error
	if operation == "start-run" && responseErr == nil && response.Started != nil && response.DeliveryReceipt != nil {
		startRequest, ok := request.(application.StartRunRequest)
		if !ok || response.Started.Prepared.RunID != startRequest.RunID || response.Started.Prepared.Sequence != startRequest.ExpectedSequence || response.Started.Prepared.AuthorityHead != startRequest.ExpectedAuthorityHead {
			return httpResponse{}, ErrConflict
		}
		recheckErr = connection.RecheckStartRun(ctx, *response.Started, *response.DeliveryReceipt)
	} else if isLifecycleOperation(operation) && responseErr == nil && response.LifecycleReceipt != nil {
		projection, projectionErr := lifecycleResponseProjection(response, operation)
		result, resultErr := fixedLifecycleResult(operation, projection)
		current, currentErr := lifecycleCurrentRequest(request)
		if projectionErr != nil || resultErr != nil || currentErr != nil || validateLifecycleClientResult(current, result) != nil {
			return httpResponse{}, ErrConflict
		}
		recheckErr = connection.RecheckLifecycle(ctx, result, *response.LifecycleReceipt)
	} else {
		recheckErr = connection.Recheck(ctx)
	}
	if recheckErr != nil {
		return httpResponse{}, ErrConflict
	}
	if connection.CloseWrite() != nil {
		return httpResponse{}, ErrUnavailable
	}
	if responseErr != nil {
		return response, responseErr
	}
	return response, nil
}

// clientRequestBinding keeps the authenticated transport binding identical to
// the durable delivery binding for start-run. Read-only requests retain the
// HTTP protocol intent; start-run deliberately uses the delivery protocol
// intent because BeginStartRunBound rejects any independently derived digest.
func clientRequestBinding(requestKey string, body []byte, operation string, request any, deadline time.Time) (RequestBinding, error) {
	switch {
	case operation == "start-run":
		input, ok := request.(application.StartRunRequest)
		if !ok {
			return RequestBinding{}, ErrInvalid
		}
		binding, err := productionruntime.NewFixedStartRunDeliveryBinding(requestKey, input, deadline)
		if err != nil {
			return RequestBinding{}, ErrInvalid
		}
		return RequestBinding{RequestKeyDigest: binding.RequestKeyDigest, RequestDigest: binding.RequestDigest, IntentDigest: binding.ApplicationIntentDigest, Deadline: binding.Deadline}, nil
	case isLifecycleOperation(operation):
		binding, err := productionruntime.NewFixedLifecycleDeliveryBinding(requestKey, operation, request, deadline)
		if err != nil {
			return RequestBinding{}, ErrInvalid
		}
		return RequestBinding{RequestKeyDigest: binding.RequestKeyDigest, RequestDigest: binding.RequestDigest, IntentDigest: binding.ApplicationIntentDigest, Deadline: binding.Deadline}, nil
	default:
		return readBinding(requestKey, body, operation, request, deadline), nil
	}
}

func isLifecycleOperation(operation string) bool {
	switch operation {
	case productionruntime.FixedLifecycleCollectOperation, productionruntime.FixedLifecycleVerifyOperation, productionruntime.FixedLifecycleReviewOperation, productionruntime.FixedLifecycleDecisionOperation:
		return true
	default:
		return false
	}
}

func lifecycleResponseProjection(response httpResponse, operation string) (any, error) {
	switch operation {
	case productionruntime.FixedLifecycleCollectOperation:
		if response.Collected != nil {
			return *response.Collected, nil
		}
	case productionruntime.FixedLifecycleVerifyOperation:
		if response.Verification != nil {
			return *response.Verification, nil
		}
	case productionruntime.FixedLifecycleReviewOperation:
		if response.ReviewPacket != nil {
			return *response.ReviewPacket, nil
		}
	case productionruntime.FixedLifecycleDecisionOperation:
		if response.Decision != nil {
			return *response.Decision, nil
		}
	}
	return nil, ErrConflict
}

func lifecycleCurrentRequest(request any) (application.CurrentRunRequest, error) {
	switch value := request.(type) {
	case application.CollectRunResultRequest:
		return application.CurrentRunRequest(value), nil
	case application.VerifyRunRequest:
		return application.CurrentRunRequest(value), nil
	case application.BuildReviewPacketRequest:
		return application.CurrentRunRequest(value), nil
	case application.ApplyReviewDecisionRequest:
		return application.CurrentRunRequest{RunID: value.RunID, AttemptID: value.AttemptID, ExpectedSequence: value.ExpectedSequence, ExpectedAuthorityHead: value.ExpectedAuthorityHead}, nil
	default:
		return application.CurrentRunRequest{}, ErrInvalid
	}
}

func validateLifecycleClientResult(current application.CurrentRunRequest, result productionruntime.FixedLifecycleResult) error {
	if result.Run.RunID != current.RunID || result.Run.AttemptID != current.AttemptID {
		return ErrConflict
	}
	if result.Operation == productionruntime.FixedLifecycleReviewOperation {
		if result.Run.Sequence != current.ExpectedSequence || result.Run.AuthorityHead != current.ExpectedAuthorityHead {
			return ErrConflict
		}
		return nil
	}
	if result.Run.Sequence != current.ExpectedSequence+1 || result.Run.AuthorityHead == current.ExpectedAuthorityHead {
		return ErrConflict
	}
	return nil
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
	if decoder.Decode(&extra) == nil || response.SchemaVersion != httpResponseSchema || response.ProtocolRevision != httpProtocolRevision {
		return httpResponse{}, ErrInvalid
	}
	if statusCode != 200 || response.Disposition != "success" {
		if statusCode == 202 && response.Disposition == "pending" {
			if response.ReasonCode == string(application.ReasonAttemptStillRunning) {
				if response.Operation != productionruntime.FixedLifecycleCollectOperation || response.Status != nil || response.Run != nil || response.Started != nil || response.DeliveryReceipt != nil || response.Collected != nil || response.Verification != nil || response.ReviewPacket != nil || response.Decision != nil || response.LifecycleReceipt != nil {
					return httpResponse{}, ErrInvalid
				}
				return response, ErrAttemptStillRunning
			}
			return response, errHTTPPending
		}
		if statusCode == 409 {
			return response, ErrConflict
		}
		return response, ErrUnavailable
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
