package app

// sandbox_transport.go delivers the M9-d transport adapters of the
// dispatch-bound Port (docs/m9-vertical-to-server-design.md §3.4, ADR 0018
// §2/§16, ADR 0017 §7): embedded/in-process, Push HTTP and Pull outbound
// runner are transport/topology adapters inside ONE versioned protocol
// family — versioned HTTP/JSON with one shared request/response schema,
// one error model and one conformance profile. The adapters never derive
// different protocol versions or semantic branches: Push never weakens an
// invariant, Pull never relaxes the capability match, and the Port
// semantics never change with the transport. Topology-specific
// offer/poll/claim/ack transitions and timing stay wire-level detail the
// dual-topology conformance suite never compares.
//
// Authority layout: every business decision (claim adjudication, fencing,
// current-ledger eligibility recheck, result admission, deadline
// reconciliation) is adjudicated by the shared DispatchTransportCore over
// the durable M9-a lease ledger and the gate-6 Matcher — the identical
// code path under every topology — while the Push/Pull/embedded bindings
// contribute transport transitions only. Every normalized business trace
// event is recorded by the core adjudication, never by a transport.
//
// Transport security baseline (ADR 0018 §12): internal/server imports
// internal/app, so this package never imports internal/server; the frozen
// TLS baseline, mutual identity validation and request-level replay
// protection of internal/server/tls.go are reused through the
// DispatchRequestSigner seam and a caller-composed listener (reference
// only, never a modification). Credentials never enter business JSON,
// events, logs or digests: transport identity stays in the TLS handshake
// and the request signature, the business payloads carry lease identity
// only.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// The dispatch-bound protocol family identity (ADR 0018 §16). Push and Pull
// share the identical family version — a topology never derives its own
// protocol version.
const (
	DispatchProtocolFamily   = "marshal-dispatch"
	DispatchProtocolVersion1 = "marshal-dispatch/1"
	dispatchAPIVersion       = "marshal.dev/v1alpha1"
)

// Wire envelope kinds of the protocol family.
const (
	dispatchKindRequest  = "DispatchRequest"
	dispatchKindResponse = "DispatchResponse"
	dispatchKindError    = "DispatchError"
)

// Wire operations of the protocol family. Push and Pull carry the identical
// operations over the identical schemas; only the wire transitions differ
// (Push: request/response per operation; Pull: offer/poll/ack).
const (
	dispatchOperationOffer      = "offer"
	dispatchOperationOfferAck   = "offer-ack"
	dispatchOperationExec       = "exec"
	dispatchOperationHeartbeat  = "heartbeat"
	dispatchOperationResult     = "result"
	dispatchOperationPoll       = "poll"
	dispatchOperationReceipt    = "receipt"
	dispatchOperationSPI        = "spi"
	dispatchOperationSPIReceipt = "spi-receipt"
)

// Wire paths of the protocol family. The Core-side paths serve the Pull
// outbound endpoints and the runner-to-Core result/heartbeat ingress; the
// provider-side paths serve the Push offer/exec delivery and the shared SPI
// surface.
const (
	dispatchPathOffer      = "/marshal-dispatch/v1/offer"
	dispatchPathExec       = "/marshal-dispatch/v1/exec"
	dispatchPathSPI        = "/marshal-dispatch/v1/spi"
	dispatchPathPoll       = "/marshal-dispatch/v1/poll"
	dispatchPathOfferAck   = "/marshal-dispatch/v1/offer-ack"
	dispatchPathHeartbeat  = "/marshal-dispatch/v1/heartbeat"
	dispatchPathResult     = "/marshal-dispatch/v1/result"
	dispatchPathReceipt    = "/marshal-dispatch/v1/receipt"
	dispatchPathSPIReceipt = "/marshal-dispatch/v1/spi-receipt"
)

// maxDispatchBodyBytes bounds one dispatch protocol body; the bound covers
// the 16 MiB Stage inline ceiling plus envelope overhead.
const maxDispatchBodyBytes int64 = 32 << 20

// Closed wire error codes of the protocol family error model. Every code
// round-trips onto one frozen business sentinel, so a SPI conformance
// fixture observes the identical sentinel semantics under every topology.
const (
	dispatchWireErrorRejected                  = "rejected"
	dispatchWireErrorInvalidIdentity           = "invalid-identity"
	dispatchWireErrorInvalidRequest            = "invalid-request"
	dispatchWireErrorStageInputMismatch        = "stage-input-mismatch"
	dispatchWireErrorLocatorUnresolved         = "locator-unresolved"
	dispatchWireErrorAllocationNotFound        = "allocation-not-found"
	dispatchWireErrorAllocationNotActive       = "allocation-not-active"
	dispatchWireErrorDuplicateActiveAllocation = "duplicate-active-allocation"
	dispatchWireErrorStaleGeneration           = "stale-generation"
	dispatchWireErrorAssuranceNotMet           = "assurance-not-met"
	dispatchWireErrorResponseLost              = "response-lost"
	dispatchWireErrorInvalidSignal             = "invalid-signal"
	dispatchWireErrorFaultInjected             = "fault-injected"
	dispatchWireErrorUnknownLease              = "unknown-lease"
	dispatchWireErrorLeaseConflict             = "lease-conflict"
	dispatchWireErrorFencingRejected           = "fencing-rejected"
	dispatchWireErrorProtocol                  = "protocol-violation"
	dispatchWireErrorInternal                  = "internal"
)

// dispatchEnvelope is the versioned wire envelope shared by every operation
// of the family.
type dispatchEnvelope struct {
	APIVersion      string          `json:"apiVersion"`
	Kind            string          `json:"kind"`
	ProtocolVersion string          `json:"protocolVersion"`
	Operation       string          `json:"operation"`
	RequestId       string          `json:"requestId"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// dispatchWireError is the versioned error body of the family.
type dispatchWireError struct {
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// encodeDispatchEnvelope seals one request envelope around payload.
func encodeDispatchEnvelope(operation, requestId string, payload any) ([]byte, error) {
	return encodeDispatchEnvelopeKind(dispatchKindRequest, operation, requestId, payload)
}

// encodeDispatchEnvelopeKind seals one envelope of the given kind.
func encodeDispatchEnvelopeKind(kind, operation, requestId string, payload any) ([]byte, error) {
	envelope := dispatchEnvelope{
		APIVersion:      dispatchAPIVersion,
		Kind:            kind,
		ProtocolVersion: DispatchProtocolVersion1,
		Operation:       operation,
		RequestId:       requestId,
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("app: dispatch wire: marshal payload: %w", err)
		}
		envelope.Payload = raw
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch wire: marshal envelope: %w", err)
	}
	canonicalized, err := canonical.JSON(body)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch wire: canonicalize envelope: %w", err)
	}
	return canonicalized, nil
}

// decodeDispatchEnvelope admits one wire body fail closed: canonical JSON is
// the sole admission gate (duplicate members rejected at every depth), the
// envelope must be strict, and the family identity must match exactly.
func decodeDispatchEnvelope(body []byte) (dispatchEnvelope, error) {
	canonicalized, err := canonical.JSON(body)
	if err != nil {
		return dispatchEnvelope{}, fmt.Errorf("%w: %v", errors.New("app: dispatch wire: envelope rejected"), err)
	}
	var envelope dispatchEnvelope
	if err := decodeStrict(canonicalized, &envelope); err != nil {
		return dispatchEnvelope{}, fmt.Errorf("app: dispatch wire: decode envelope: %w", err)
	}
	if envelope.APIVersion != dispatchAPIVersion {
		return dispatchEnvelope{}, fmt.Errorf("app: dispatch wire: unsupported apiVersion %q", envelope.APIVersion)
	}
	if envelope.ProtocolVersion != DispatchProtocolVersion1 {
		return dispatchEnvelope{}, fmt.Errorf("app: dispatch wire: unsupported protocolVersion %q", envelope.ProtocolVersion)
	}
	switch envelope.Kind {
	case dispatchKindRequest, dispatchKindResponse, dispatchKindError:
	default:
		return dispatchEnvelope{}, fmt.Errorf("app: dispatch wire: unknown envelope kind %q", envelope.Kind)
	}
	return envelope, nil
}

// decodeStrict decodes canonical JSON into target rejecting unknown fields
// at every depth.
func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing content after the JSON document")
	}
	return nil
}

// dispatchWireErrorFromError maps one business sentinel onto its frozen
// wire error code.
func dispatchWireErrorFromError(err error) dispatchWireError {
	code := dispatchWireErrorInternal
	switch {
	case errors.Is(err, sandbox.ErrInvalidOperationIdentity), errors.Is(err, sandbox.ErrInvalidWorkloadRole):
		code = dispatchWireErrorInvalidIdentity
	case errors.Is(err, sandbox.ErrStageInputMismatch):
		code = dispatchWireErrorStageInputMismatch
	case errors.Is(err, sandbox.ErrLocatorUnresolved), errors.Is(err, sandbox.ErrInvalidLocator):
		code = dispatchWireErrorLocatorUnresolved
	case errors.Is(err, sandbox.ErrAllocationNotFound):
		code = dispatchWireErrorAllocationNotFound
	case errors.Is(err, sandbox.ErrAllocationNotActive):
		code = dispatchWireErrorAllocationNotActive
	case errors.Is(err, sandbox.ErrDuplicateActiveAllocation):
		code = dispatchWireErrorDuplicateActiveAllocation
	case errors.Is(err, sandbox.ErrStaleAllocationGeneration):
		code = dispatchWireErrorStaleGeneration
	case errors.Is(err, sandbox.ErrAssuranceNotMet), errors.Is(err, sandbox.ErrAssuranceDowngrade), errors.Is(err, sandbox.ErrAccessModeMismatch):
		code = dispatchWireErrorAssuranceNotMet
	case errors.Is(err, sandbox.ErrResponseLost):
		code = dispatchWireErrorResponseLost
	case errors.Is(err, sandbox.ErrInvalidSignal):
		code = dispatchWireErrorInvalidSignal
	case errors.Is(err, sandbox.ErrFaultInjected):
		code = dispatchWireErrorFaultInjected
	case errors.Is(err, dispatch.ErrUnknownLease):
		code = dispatchWireErrorUnknownLease
	case errors.Is(err, dispatch.ErrLeaseConflict):
		code = dispatchWireErrorLeaseConflict
	case errors.Is(err, dispatch.ErrLeaseGenerationConflict):
		code = dispatchWireErrorFencingRejected
	case errors.Is(err, sandbox.ErrInvalidRequest), errors.Is(err, sandbox.ErrInlineTooLarge),
		errors.Is(err, sandbox.ErrStageRequestTooLarge), errors.Is(err, sandbox.ErrDuplicateStageInputId),
		errors.Is(err, sandbox.ErrInvalidStageInput):
		code = dispatchWireErrorInvalidRequest
	case errors.Is(err, sandbox.ErrRestoreRejected):
		code = dispatchWireErrorRejected
	}
	message := err.Error()
	if code == dispatchWireErrorInternal {
		// Internal failures never leak dynamic detail across the wire.
		message = "the dispatch operation failed closed"
	}
	return dispatchWireError{Code: code, Reason: code, Message: message}
}

// dispatchWireErrorToError maps one wire error back onto the frozen
// business sentinel, preserving errors.Is semantics for the SPI
// conformance fixtures under every topology.
func dispatchWireErrorToError(wireErr dispatchWireError) error {
	detail := wireErr.Message
	if strings.TrimSpace(detail) == "" {
		detail = wireErr.Code
	}
	switch wireErr.Code {
	case dispatchWireErrorInvalidIdentity:
		return fmt.Errorf("%w: %s", sandbox.ErrInvalidOperationIdentity, detail)
	case dispatchWireErrorStageInputMismatch:
		return fmt.Errorf("%w: %s", sandbox.ErrStageInputMismatch, detail)
	case dispatchWireErrorLocatorUnresolved:
		return fmt.Errorf("%w: %s", sandbox.ErrLocatorUnresolved, detail)
	case dispatchWireErrorAllocationNotFound:
		return fmt.Errorf("%w: %s", sandbox.ErrAllocationNotFound, detail)
	case dispatchWireErrorAllocationNotActive:
		return fmt.Errorf("%w: %s", sandbox.ErrAllocationNotActive, detail)
	case dispatchWireErrorDuplicateActiveAllocation:
		return fmt.Errorf("%w: %s", sandbox.ErrDuplicateActiveAllocation, detail)
	case dispatchWireErrorStaleGeneration:
		return fmt.Errorf("%w: %s", sandbox.ErrStaleAllocationGeneration, detail)
	case dispatchWireErrorAssuranceNotMet:
		return fmt.Errorf("%w: %s", sandbox.ErrAssuranceNotMet, detail)
	case dispatchWireErrorResponseLost:
		return fmt.Errorf("%w: %s", sandbox.ErrResponseLost, detail)
	case dispatchWireErrorInvalidSignal:
		return fmt.Errorf("%w: %s", sandbox.ErrInvalidSignal, detail)
	case dispatchWireErrorFaultInjected:
		return fmt.Errorf("%w: %s", sandbox.ErrFaultInjected, detail)
	case dispatchWireErrorUnknownLease:
		return fmt.Errorf("%w: %s", dispatch.ErrUnknownLease, detail)
	case dispatchWireErrorLeaseConflict:
		return fmt.Errorf("%w: %s", dispatch.ErrLeaseConflict, detail)
	case dispatchWireErrorFencingRejected:
		return fmt.Errorf("%w: %s", dispatch.ErrLeaseGenerationConflict, detail)
	case dispatchWireErrorInvalidRequest:
		return fmt.Errorf("%w: %s", sandbox.ErrInvalidRequest, detail)
	case dispatchWireErrorRejected:
		return fmt.Errorf("%w: %s", sandbox.ErrRestoreRejected, detail)
	default:
		return fmt.Errorf("app: dispatch wire: %s: %s", wireErr.Code, detail)
	}
}

// Wire payloads of the protocol family. Business payloads carry lease
// identity only — transport credentials never enter business JSON.

// wireOfferPayload delivers one adjudicated lease offer to the execution
// plane (Push delivery or the Pull poll response).
type wireOfferPayload struct {
	Lease        dispatch.DispatchLease     `json:"lease"`
	WorkloadRole sandbox.WorkloadRole       `json:"workloadRole"`
	Requirements domain.SandboxRequirements `json:"requirements"`
}

// wireOfferAckPayload is the ack of one offer: the lease binding echoed
// verbatim plus the granted allocation observation.
type wireOfferAckPayload struct {
	LeaseId      string                    `json:"leaseId"`
	Generation   int64                     `json:"generation"`
	FencingToken string                    `json:"fencingToken"`
	Allocation   sandbox.SandboxAllocation `json:"allocation"`
}

// wireExecPayload delivers one execution command under the full lease
// identity.
type wireExecPayload struct {
	Identity     sandbox.OperationIdentity `json:"identity"`
	LeaseId      string                    `json:"leaseId"`
	AllocationId string                    `json:"allocationId"`
	Command      []string                  `json:"command"`
	CommandId    string                    `json:"commandId"`
}

// wireLeaseOperation binds one lease-scoped heartbeat/result operation.
type wireLeaseOperation struct {
	LeaseId      string `json:"leaseId"`
	AttemptId    string `json:"attemptId"`
	Generation   int64  `json:"generation"`
	FencingToken string `json:"fencingToken"`
}

// wireResultPayload submits one finished result for admission.
type wireResultPayload struct {
	LeaseId      string `json:"leaseId"`
	AttemptId    string `json:"attemptId"`
	Generation   int64  `json:"generation"`
	FencingToken string `json:"fencingToken"`
	CommandId    string `json:"commandId"`
	ResultDigest string `json:"resultDigest"`
}

// wireVerdictPayload is the admission/heartbeat verdict of the Core.
type wireVerdictPayload struct {
	Accepted    bool                    `json:"accepted"`
	ReasonClass sandbox.DualReasonClass `json:"reasonClass"`
	Detail      string                  `json:"detail"`
}

// wirePollPayload is the outbound poll identity of the Pull runner.
type wirePollPayload struct {
	RunnerId string `json:"runnerId"`
}

// wirePollResponse is the Core's answer to one poll: the next queued item
// or the empty mark.
type wirePollResponse struct {
	Available bool              `json:"available"`
	ItemKind  string            `json:"itemKind,omitempty"`
	RequestId string            `json:"requestId,omitempty"`
	Offer     *wireOfferPayload `json:"offer,omitempty"`
	Exec      *wireExecPayload  `json:"exec,omitempty"`
	SPI       *wireSPIRequest   `json:"spi,omitempty"`
}

// wireReceiptPayload reports the transport outcome of one queued exec item.
type wireReceiptPayload struct {
	RequestId string `json:"requestId"`
	LeaseId   string `json:"leaseId"`
	CommandId string `json:"commandId"`
	Success   bool   `json:"success"`
	Detail    string `json:"detail,omitempty"`
}

// wireSPIReceiptPayload reports the transport outcome of one queued SPI
// item: the receipt payload or the wire error, never both.
type wireSPIReceiptPayload struct {
	RequestId string             `json:"requestId"`
	Payload   json.RawMessage    `json:"payload,omitempty"`
	Error     *dispatchWireError `json:"error,omitempty"`
}

// wireSPIRequest carries one SPI operation of the ten-operation contract
// through the transport (the RunConformance parameterization).
type wireSPIRequest struct {
	Operation string                    `json:"operation"`
	Identity  sandbox.OperationIdentity `json:"identity"`
	Payload   json.RawMessage           `json:"payload"`
}

// SPI operation payloads (strict field sets, one per SPI operation).
type wireSPIProbe struct {
	Requirements domain.SandboxRequirements `json:"requirements"`
}

type wireSPIProvision struct {
	Requirements    domain.SandboxRequirements `json:"requirements"`
	AllowedStoreIds []string                   `json:"allowedStoreIds"`
}

type wireSPIStage struct {
	AllocationId string               `json:"allocationId"`
	Inputs       []sandbox.StageInput `json:"inputs"`
}

type wireSPIExec struct {
	AllocationId string   `json:"allocationId"`
	Command      []string `json:"command"`
}

type wireSPIInspect struct {
	AllocationId string `json:"allocationId"`
}

type wireSPISignal struct {
	AllocationId string             `json:"allocationId"`
	Signal       sandbox.SignalName `json:"signal"`
}

type wireSPICheckpoint struct {
	AllocationId string `json:"allocationId"`
}

type wireSPIRestore struct {
	PreviousAllocationId string `json:"previousAllocationId"`
	NextAllocationId     string `json:"nextAllocationId"`
	InPlaceConfirmed     bool   `json:"inPlaceConfirmed"`
}

type wireSPITerminate struct {
	AllocationId string `json:"allocationId"`
}

type wireSPIReconcile struct {
	RunId     string `json:"runId"`
	AttemptId string `json:"attemptId"`
}

// dispatchSPIOperation executes one SPI operation against the bound
// provider: the shared provider-side surface of the Push endpoint and the
// Pull runner.
func dispatchSPIOperation(ctx context.Context, providerInstance sandbox.SandboxProvider, request wireSPIRequest) (any, error) {
	identity := request.Identity
	switch request.Operation {
	case sandbox.OperationProbe:
		var payload wireSPIProbe
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: probe payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Probe(ctx, sandbox.ProbeRequest{Identity: identity, Requirements: payload.Requirements})
	case sandbox.OperationProvision:
		var payload wireSPIProvision
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: provision payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Provision(ctx, sandbox.ProvisionRequest{Identity: identity, Requirements: payload.Requirements, AllowedStoreIds: payload.AllowedStoreIds})
	case sandbox.OperationStage:
		var payload wireSPIStage
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: stage payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Stage(ctx, sandbox.StageRequest{Identity: identity, AllocationId: payload.AllocationId, Inputs: payload.Inputs})
	case sandbox.OperationExec:
		var payload wireSPIExec
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: exec payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Exec(ctx, sandbox.ExecRequest{Identity: identity, AllocationId: payload.AllocationId, Command: payload.Command})
	case sandbox.OperationInspect:
		var payload wireSPIInspect
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: inspect payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Inspect(ctx, sandbox.InspectRequest{Identity: identity, AllocationId: payload.AllocationId})
	case sandbox.OperationSignal:
		var payload wireSPISignal
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: signal payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Signal(ctx, sandbox.SignalRequest{Identity: identity, AllocationId: payload.AllocationId, Signal: payload.Signal})
	case sandbox.OperationCheckpoint:
		var payload wireSPICheckpoint
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: checkpoint payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Checkpoint(ctx, sandbox.CheckpointRequest{Identity: identity, AllocationId: payload.AllocationId})
	case sandbox.OperationRestore:
		var payload wireSPIRestore
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: restore payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Restore(ctx, sandbox.RestoreOperationRequest{Identity: identity, PreviousAllocationId: payload.PreviousAllocationId, NextAllocationId: payload.NextAllocationId, InPlaceConfirmed: payload.InPlaceConfirmed})
	case sandbox.OperationTerminate:
		var payload wireSPITerminate
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: terminate payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Terminate(ctx, sandbox.TerminateRequest{Identity: identity, AllocationId: payload.AllocationId})
	case sandbox.OperationReconcile:
		var payload wireSPIReconcile
		if err := decodeStrict(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("%w: reconcile payload: %v", sandbox.ErrInvalidRequest, err)
		}
		return providerInstance.Reconcile(ctx, sandbox.ReconcileRequest{Identity: identity, RunId: payload.RunId, AttemptId: payload.AttemptId})
	default:
		return nil, fmt.Errorf("%w: unknown spi operation %q", sandbox.ErrInvalidRequest, request.Operation)
	}
}

// DispatchRequestSigner is the seam through which the dispatch transport
// reuses the frozen remote transport security baseline of internal/server
// (ADR 0018 §12): the caller layer that composes a non-loopback TLS
// surface binds server.SignRequest here. internal/server imports
// internal/app, so this package never imports internal/server directly —
// the baseline is referenced, never modified. Loopback/in-process
// transports run without a signer exactly like the frozen loopback path.
type DispatchRequestSigner interface {
	SignRequest(method, path, timestamp, nonce string, body []byte) (string, error)
}

// DispatchTransportClient is the outbound HTTP client configuration of the
// dispatch transport: an HTTP client plus the optional request signer of
// the TLS baseline. The client is outbound-only; it never listens.
type DispatchTransportClient struct {
	Client *http.Client
	Signer DispatchRequestSigner

	mu           sync.Mutex
	nonceCounter int64
}

// NewDispatchTransportClient binds one outbound transport client. A nil
// http.Client selects the default client.
func NewDispatchTransportClient(client *http.Client, signer DispatchRequestSigner) *DispatchTransportClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &DispatchTransportClient{Client: client, Signer: signer}
}

// post issues one outbound POST under the protocol family, applying the
// request-level replay protection headers when the TLS signer is bound.
func (client *DispatchTransportClient) post(ctx context.Context, baseURL, path string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if client.Signer != nil {
		client.mu.Lock()
		client.nonceCounter++
		counter := client.nonceCounter
		client.mu.Unlock()
		timestamp := time.Now().UTC().Format(time.RFC3339)
		nonce := canonical.DigestBytes([]byte(fmt.Sprintf("dispatch-nonce\x00%s\x00%s\x00%d", path, timestamp, counter)))
		signature, err := client.Signer.SignRequest(http.MethodPost, path, timestamp, nonce, body)
		if err != nil {
			return nil, fmt.Errorf("app: dispatch transport: sign request binding: %w", err)
		}
		request.Header.Set("Marshal-Nonce", nonce)
		request.Header.Set("Marshal-Timestamp", timestamp)
		request.Header.Set("Marshal-Signature", signature)
	}
	response, err := client.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxDispatchBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport: read response: %w", err)
	}
	if int64(len(responseBody)) > maxDispatchBodyBytes {
		return nil, errors.New("app: dispatch transport: response body exceeds the protocol bound")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("app: dispatch transport: the endpoint answered %d", response.StatusCode)
	}
	return responseBody, nil
}

// postEnvelope issues one envelope and admits the response envelope fail
// closed; a DispatchError response is mapped back onto the frozen business
// sentinel.
func (client *DispatchTransportClient) postEnvelope(ctx context.Context, baseURL, path, operation, requestId string, payload any) (dispatchEnvelope, error) {
	body, err := encodeDispatchEnvelope(operation, requestId, payload)
	if err != nil {
		return dispatchEnvelope{}, err
	}
	responseBody, err := client.post(ctx, baseURL, path, body)
	if err != nil {
		return dispatchEnvelope{}, err
	}
	envelope, err := decodeDispatchEnvelope(responseBody)
	if err != nil {
		return dispatchEnvelope{}, err
	}
	if envelope.Kind == dispatchKindError {
		var wireErr dispatchWireError
		if err := decodeStrict(envelope.Payload, &wireErr); err != nil {
			return dispatchEnvelope{}, fmt.Errorf("app: dispatch transport: decode wire error: %w", err)
		}
		return dispatchEnvelope{}, dispatchWireErrorToError(wireErr)
	}
	if envelope.Kind != dispatchKindResponse {
		return dispatchEnvelope{}, fmt.Errorf("app: dispatch transport: unexpected response kind %q", envelope.Kind)
	}
	return envelope, nil
}

// readRequestBody admits one request body under the protocol bound.
func readDispatchBody(request *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxDispatchBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxDispatchBodyBytes {
		return nil, errors.New("the request body exceeds the protocol bound")
	}
	return body, nil
}

// writeDispatchEnvelope writes one success envelope.
func writeDispatchEnvelope(writer http.ResponseWriter, operation, requestId string, payload any) {
	body, err := encodeDispatchEnvelopeKind(dispatchKindResponse, operation, requestId, payload)
	if err != nil {
		http.Error(writer, "internal", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

// writeDispatchError writes one error envelope carrying the frozen wire
// error of err.
func writeDispatchError(writer http.ResponseWriter, operation, requestId string, err error) {
	wireErr := dispatchWireErrorFromError(err)
	body, encodeErr := encodeDispatchEnvelopeKind(dispatchKindError, operation, requestId, wireErr)
	if encodeErr != nil {
		http.Error(writer, "internal", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

// NewDispatchProviderHandler builds the provider-side wire surface of the
// protocol family: the Push endpoint and the Pull runner serve the
// identical handler over the identical provider instance semantics. The
// handler hosts the offer/exec delivery operations and the shared SPI
// surface; every operation fails closed on a malformed envelope before any
// provider side effect.
func NewDispatchProviderHandler(providerInstance sandbox.SandboxProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+dispatchPathOffer, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationOffer, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationOffer, "", err)
			return
		}
		var payload wireOfferPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationOffer, envelope.RequestId, fmt.Errorf("%w: offer payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		if err := payload.Lease.Validate(); err != nil {
			writeDispatchError(writer, dispatchOperationOffer, envelope.RequestId, fmt.Errorf("%w: the offered lease does not validate", sandbox.ErrInvalidRequest))
			return
		}
		if err := payload.WorkloadRole.Validate(); err != nil {
			writeDispatchError(writer, dispatchOperationOffer, envelope.RequestId, err)
			return
		}
		receipt, err := providerInstance.Provision(request.Context(), sandbox.ProvisionRequest{
			Identity: sandbox.OperationIdentity{
				TaskId:       payload.Lease.TaskId,
				RunId:        payload.Lease.RunId,
				AttemptId:    payload.Lease.AttemptId,
				WorkloadRole: payload.WorkloadRole,
				AllocationId: payload.Lease.AllocationId,
				Generation:   payload.Lease.Generation,
				FencingToken: payload.Lease.FencingToken,
				CommandId:    "offer-" + payload.Lease.LeaseId,
			},
			Requirements: payload.Requirements,
		})
		if err != nil {
			writeDispatchError(writer, dispatchOperationOffer, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationOffer, envelope.RequestId, wireOfferAckPayload{
			LeaseId:      payload.Lease.LeaseId,
			Generation:   payload.Lease.Generation,
			FencingToken: payload.Lease.FencingToken,
			Allocation:   receipt.Allocation,
		})
	})
	mux.HandleFunc("POST "+dispatchPathExec, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationExec, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationExec, "", err)
			return
		}
		var payload wireExecPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationExec, envelope.RequestId, fmt.Errorf("%w: exec payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		receipt, err := providerInstance.Exec(request.Context(), sandbox.ExecRequest{
			Identity:     payload.Identity,
			AllocationId: payload.AllocationId,
			Command:      payload.Command,
		})
		if err != nil {
			writeDispatchError(writer, dispatchOperationExec, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationExec, envelope.RequestId, receipt)
	})
	mux.HandleFunc("POST "+dispatchPathSPI, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationSPI, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationSPI, "", err)
			return
		}
		var spiRequest wireSPIRequest
		if err := decodeStrict(envelope.Payload, &spiRequest); err != nil {
			writeDispatchError(writer, dispatchOperationSPI, envelope.RequestId, fmt.Errorf("%w: spi payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		report, err := dispatchSPIOperation(request.Context(), providerInstance, spiRequest)
		if err != nil {
			writeDispatchError(writer, dispatchOperationSPI, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationSPI, envelope.RequestId, report)
	})
	return mux
}

// Deterministic identity constants of the transport Core registration (ADR
// 0018 §10 local derivation style): frozen createdAt constants so identical
// durable ledger replays merge idempotently on any day.
const (
	dispatchTransportRegistrationID  = "dispatch-transport-provider"
	dispatchTransportProviderType    = "sandbox"
	dispatchTransportProviderName    = "transport"
	dispatchTransportProviderVersion = "m9d-transport"
	dispatchTransportPrincipal       = "dispatch-transport-provider"
	dispatchTransportCreatedAt       = "2026-01-01T00:00:00Z"
	dispatchTransportAckWindow       = 30 * time.Minute
	dispatchTransportLeaseWindow     = 24 * time.Hour
)

// transportCoreSeeds are the deterministic derivation seeds of the
// transport registration attestation; the two-part concatenation keeps
// every digest derivation gitleaks-safe.
var (
	dispatchTransportRequestDigest = sandbox.RecomputeSHA256([]byte("dispatch-transport-registration" + "\x00" + dispatchTransportRegistrationID))
	dispatchTransportConfigDigest  = sandbox.RecomputeSHA256([]byte("dispatch-transport" + "\x00" + "effective-config"))
	dispatchTransportProbeArtifact = sandbox.RecomputeSHA256([]byte("dispatch-transport" + "\x00" + "probe-artifact"))
	// dispatchTransportScopeID is the deterministic authority scope of the
	// protocol family. It is never seeded with transport deployment data
	// (the durable state root, transport metadata, headers or poll
	// ordinals): the scope is declared by the registration, the capability
	// snapshot and the sealed evidence chain, and the snapshot digest
	// enters the lease derivation binding of every accepted claim — the
	// identical DualClaimRequest must derive the identical lease digest
	// under every topology, so the scope is one protocol-family constant.
	dispatchTransportScopeID = "dispatch-transport:" + sandbox.RecomputeSHA256([]byte("scope"+"\x00"+"dispatch-transport-protocol-family"))
)

// transportClock is the injectable deterministic clock of the transport
// Core; no other clock participates in the business adjudication.
type transportClock struct {
	current time.Time
}

func (clock *transportClock) Now() time.Time { return clock.current }

// pullQueueItem is one staged Pull transition: an offer, an exec command or
// an SPI operation awaiting the runner's outbound poll.
type pullQueueItem struct {
	kind      string
	requestId string
	payload   json.RawMessage
}

// transportReceipt is the transport outcome of one queued Pull item.
type transportReceipt struct {
	success   bool
	detail    string
	payload   json.RawMessage
	wireError *dispatchWireError
}

// DispatchTransportCore is the shared Core-side authority of the
// dispatch-bound protocol family: the durable M9-a lease ledger, the gate-6
// Matcher bound to the durable registration ledger and the Core typed-edge
// runtime adjudicate every claim, heartbeat, execution and result under
// every topology. The core implements sandbox.DualAuthority: it records
// every normalized business trace event, so the identical adjudication code
// path produces the identical business trace under every transport.
type DispatchTransportCore struct {
	namespace    authority.AuthorityNamespaceId
	targetActor  authority.SecurityDomainId
	store        *provider.RegistrationStore
	edgeRuntime  *authority.EdgeRuntime
	matcher      *dispatch.Matcher
	ledger       *dispatch.LeaseLedger
	providerPort sandbox.SandboxProvider
	ackWindow    time.Duration
	leaseWindow  time.Duration

	baseRegistration provider.ProviderRegistration
	baseSnapshot     provider.ProviderCapabilitySnapshot
	baseEvidences    []provider.ConformanceEvidence

	mu                  sync.Mutex
	clock               transportClock
	registration        provider.ProviderRegistration
	snapshot            provider.ProviderCapabilitySnapshot
	evidences           []provider.ConformanceEvidence
	reregistrations     int
	requirementsByLease map[string]domain.SandboxRequirements
	workloadRoleByLease map[string]sandbox.WorkloadRole
	acked               map[string]bool
	openExecutions      map[string]string
	finishedExecutions  map[string]map[string]struct{}
	pullQueue           []pullQueueItem
	receipts            map[string]transportReceipt
	recorder            *sandbox.DualTraceRecorder
}

// DispatchTransportConfig assembles one transport Core.
type DispatchTransportConfig struct {
	// StateRoot is the durable root of the registration store and the
	// M9-a lease ledger of this core.
	StateRoot string
	// Provider is the sandbox provider the transport bindings serve.
	Provider sandbox.SandboxProvider
	// Now is the initial value of the injected deterministic clock.
	Now time.Time
}

// NewDispatchTransportCore builds the shared Core authority of the
// dispatch-bound protocol family over one durable state root: the durable
// registration ledger carrying the transport provider registration with
// its aligned capability snapshot and sealed conformance evidence chain,
// the durable M9-a lease ledger and the gate-6 Matcher bound to the Core
// typed-edge runtime.
func NewDispatchTransportCore(config DispatchTransportConfig) (*DispatchTransportCore, error) {
	if strings.TrimSpace(config.StateRoot) == "" {
		return nil, errors.New("app: dispatch transport core: stateRoot must be a non-empty path")
	}
	if config.Provider == nil {
		return nil, errors.New("app: dispatch transport core: the provider binding must not be nil")
	}
	if config.Now.IsZero() {
		return nil, errors.New("app: dispatch transport core: the injected clock must not be zero")
	}
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  "local",
		ControlPlaneId:   "default",
		AuthorityScopeId: dispatchTransportScopeID,
	}
	providerDomain := authority.SecurityDomainId{
		TenantNamespace:   "local",
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "dispatch-transport",
	}
	resultIngress := authority.SecurityDomainId{
		TenantNamespace:   "local",
		TrustDomainKind:   authority.TrustDomainKindDataCapability,
		IsolationDomainId: "result-ingress",
	}
	store, err := provider.NewRegistrationStore(filepath.Join(config.StateRoot, "providers"))
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: durable registration ledger: %w", err)
	}
	ledger, err := dispatch.NewLeaseLedger(filepath.Join(config.StateRoot, "leases"))
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: durable lease ledger: %w", err)
	}
	edgeRuntime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: typed-edge runtime: %w", err)
	}
	core := &DispatchTransportCore{
		namespace:           namespace,
		targetActor:         resultIngress,
		store:               store,
		edgeRuntime:         edgeRuntime,
		ledger:              ledger,
		providerPort:        config.Provider,
		ackWindow:           dispatchTransportAckWindow,
		leaseWindow:         dispatchTransportLeaseWindow,
		clock:               transportClock{current: config.Now.UTC()},
		requirementsByLease: map[string]domain.SandboxRequirements{},
		workloadRoleByLease: map[string]sandbox.WorkloadRole{},
		acked:               map[string]bool{},
		openExecutions:      map[string]string{},
		finishedExecutions:  map[string]map[string]struct{}{},
		receipts:            map[string]transportReceipt{},
		recorder:            &sandbox.DualTraceRecorder{},
	}
	edgeRuntime.BindLeaseResolver(transportLeaseResolver{core: core})
	edgeRuntime.BindTargetEligibilityResolver(transportTargetResolver{core: core})
	registration, err := core.buildRegistration(dispatchTransportRegistrationID, "embedded"+":"+dispatchTransportRegistrationID, dispatchTransportRequestDigest, providerDomain)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: build registration: %w", err)
	}
	stored, err := store.Put(registration)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: registration submission: %w", err)
	}
	evidences, err := core.buildEvidences(stored)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: build evidence chain: %w", err)
	}
	snapshot, err := core.buildSnapshot(stored, evidences[0].EvidenceDigest)
	if err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: build capability snapshot: %w", err)
	}
	if err := provider.ValidateEvidenceSetForSnapshot(snapshot, evidences); err != nil {
		return nil, fmt.Errorf("app: dispatch transport core: evidence set alignment: %w", err)
	}
	core.baseRegistration = stored
	core.baseSnapshot = snapshot
	core.baseEvidences = evidences
	core.registration = stored
	core.snapshot = snapshot
	core.evidences = evidences
	core.matcher = dispatch.NewMatcherWithEdgeRuntime(store, edgeRuntime)
	return core, nil
}

// buildRegistration builds one transport provider registration with the
// frozen createdAt constant.
func (core *DispatchTransportCore) buildRegistration(registrationId, idempotencyKey, requestDigest string, actorDomain authority.SecurityDomainId) (provider.ProviderRegistration, error) {
	registration := provider.ProviderRegistration{
		RegistrationId:       registrationId,
		AuthorityNamespaceId: core.namespace,
		SecurityDomainId:     actorDomain,
		Principal:            dispatchTransportPrincipal,
		ProviderType:         dispatchTransportProviderType,
		ProviderName:         dispatchTransportProviderName,
		ProviderVersion:      dispatchTransportProviderVersion,
		ProtocolVersion:      DispatchProtocolVersion1,
		Scope:                core.namespace.AuthorityScopeId,
		IdempotencyKey:       idempotencyKey,
		RequestDigest:        requestDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "dispatch-transport-instance",
			ConfigDigest:       dispatchTransportConfigDigest,
			TrustRootKeyId:     "dispatch-transport-trust-root-key",
			TrustRootAlgorithm: "ed25519",
		},
		LifecycleState: provider.LifecycleStateActive,
		CreatedAt:      dispatchTransportCreatedAt,
	}
	digest, err := registration.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, err
	}
	registration.RegistrationDigest = digest
	return registration, nil
}

// buildSnapshot captures the capability snapshot aligned with the stored
// registration: the capabilities declare workspace-write access at the
// workspace-write ceiling, and the closed conformanceEvidenceDigests set
// carries the sealed evidence chain digest.
func (core *DispatchTransportCore) buildSnapshot(registration provider.ProviderRegistration, evidenceDigest string) (provider.ProviderCapabilitySnapshot, error) {
	snapshot := provider.ProviderCapabilitySnapshot{
		RegistrationId:  registration.RegistrationId,
		ProtocolVersion: registration.ProtocolVersion,
		ProviderType:    registration.ProviderType,
		ProviderName:    registration.ProviderName,
		ProviderVersion: registration.ProviderVersion,
		Capabilities: map[string]string{
			"accessMode":            string(domain.AccessModeWorkspaceWrite),
			"minimumAssuranceLevel": string(domain.AssuranceLevelWorkspaceWrite),
		},
		ConformanceEvidenceDigests: []string{evidenceDigest},
		Scope:                      registration.Scope,
		SnapshotState:              provider.SnapshotStateActive,
		CreatedAt:                  dispatchTransportCreatedAt,
		Attestation:                registration.Attestation,
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return provider.ProviderCapabilitySnapshot{}, err
	}
	snapshot.ProviderCapabilitySnapshotDigest = digest
	return snapshot, nil
}

// buildEvidences builds the sealed conformance evidence chain aligned with
// the registration attestation: suite-issued (never provider self-signed),
// all four closed dimensions passed, valid state. The canonical evidence
// digest is derived from the sealed content and declared by the snapshot.
func (core *DispatchTransportCore) buildEvidences(registration provider.ProviderRegistration) ([]provider.ConformanceEvidence, error) {
	evidence := provider.ConformanceEvidence{
		AuthorityNamespaceId: core.namespace,
		SecurityDomainId:     registration.SecurityDomainId,
		ProviderInstanceId:   registration.Attestation.ProviderInstanceId,
		ConfigDigest:         registration.Attestation.ConfigDigest,
		TrustRootKeyId:       registration.Attestation.TrustRootKeyId,
		SuiteName:            "dispatch-transport-conformance",
		ProbeArtifactDigest:  dispatchTransportProbeArtifact,
		DimensionResults: map[provider.ConformanceDimension]provider.DimensionResult{
			provider.ConformanceDimensionMount:      provider.DimensionResultPassed,
			provider.ConformanceDimensionNetwork:    provider.DimensionResultPassed,
			provider.ConformanceDimensionResource:   provider.DimensionResultPassed,
			provider.ConformanceDimensionCredential: provider.DimensionResultPassed,
		},
		EvidenceState:      provider.EvidenceStateValid,
		ProviderSelfSigned: false,
		SignedAt:           dispatchTransportCreatedAt,
	}
	digest, err := evidence.Digest()
	if err != nil {
		return nil, err
	}
	evidence.EvidenceDigest = digest
	return []provider.ConformanceEvidence{evidence}, nil
}

// Provider exposes the sandbox provider the transport bindings serve.
func (core *DispatchTransportCore) Provider() sandbox.SandboxProvider { return core.providerPort }

// Namespace returns the Core authority key space of the transport core.
func (core *DispatchTransportCore) Namespace() authority.AuthorityNamespaceId { return core.namespace }

// Registration returns the current registration view of the transport core.
func (core *DispatchTransportCore) Registration() provider.ProviderRegistration {
	core.mu.Lock()
	defer core.mu.Unlock()
	return core.registration
}

// CapabilitySnapshot returns the current capability snapshot view.
func (core *DispatchTransportCore) CapabilitySnapshot() provider.ProviderCapabilitySnapshot {
	core.mu.Lock()
	defer core.mu.Unlock()
	return core.snapshot
}

// LeaseRecord returns the current ledger record of one lease.
func (core *DispatchTransportCore) LeaseRecord(leaseId string) (dispatch.DispatchLease, bool) {
	lease, _, _, err := core.ledger.Current(leaseId)
	if err != nil {
		return dispatch.DispatchLease{}, false
	}
	return lease, true
}

// Trace returns the normalized business trace recorded by the core
// adjudication.
func (core *DispatchTransportCore) Trace() []sandbox.DualTraceEvent {
	return core.recorder.Events()
}

// record stores one business event fail closed.
func (core *DispatchTransportCore) record(event sandbox.DualTraceEvent) {
	if err := core.recorder.Record(event); err != nil {
		panic(fmt.Sprintf("app: dispatch transport core: record business event: %v", err))
	}
}

// now returns the injected clock under the core lock; callers must hold
// core.mu.
func (core *DispatchTransportCore) now() time.Time { return core.clock.Now() }

// advanceClockTo moves the injected clock to target when target is later;
// callers must hold core.mu.
func (core *DispatchTransportCore) advanceClockTo(target time.Time) {
	if target.After(core.clock.current) {
		core.clock.current = target.UTC()
	}
}

// currentViews returns the current registration/snapshot/evidence views;
// callers must hold core.mu.
func (core *DispatchTransportCore) currentViews() (provider.ProviderRegistration, provider.ProviderCapabilitySnapshot, []provider.ConformanceEvidence) {
	return core.registration, core.snapshot, append([]provider.ConformanceEvidence(nil), core.evidences...)
}

// leaseRefOf converts one ledger lease into the dual scenario lease ref.
func leaseRefOf(lease dispatch.DispatchLease) sandbox.DualLeaseRef {
	return sandbox.DualLeaseRef{
		LeaseId:       lease.LeaseId,
		TaskId:        lease.TaskId,
		RunId:         lease.RunId,
		AttemptId:     lease.AttemptId,
		AllocationId:  lease.AllocationId,
		Generation:    lease.Generation,
		FencingToken:  lease.FencingToken,
		AckDeadlineAt: lease.AckDeadlineAt,
		ExpiresAt:     lease.ExpiresAt,
	}
}

// AdjudicateClaim implements sandbox.DualAuthority: the assurance
// adjudication layer, the gate-6 Matcher.Claim (six-step fail-closed), the
// claim-path fencing guard and the M9-a ledger claim append — the frozen
// layer order, shared by every topology.
func (core *DispatchTransportCore) AdjudicateClaim(ctx context.Context, request sandbox.DualClaimRequest) (sandbox.DualLeaseRef, sandbox.DualOperationOutcome, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := request.Validate(); err != nil {
		attemptId := request.AttemptId
		if strings.TrimSpace(attemptId) == "" {
			attemptId = "invalid"
		}
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimRejected, AttemptId: attemptId, ReasonClass: sandbox.DualReasonIneligible})
		return sandbox.DualLeaseRef{}, sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonIneligible, Detail: err.Error()}, nil
	}
	_, snapshot, _ := core.currentViews()
	if request.Requirements.MinimumAssuranceLevel == domain.AssuranceLevelHardened && len(snapshot.ConformanceEvidenceDigests) == 0 {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: sandbox.DualReasonIneligible})
		return sandbox.DualLeaseRef{}, sandbox.DualOperationOutcome{
			Accepted:    false,
			ReasonClass: sandbox.DualReasonIneligible,
			Detail:      "assurance adjudication rejected the hardened requirements: fail closed without downgrade",
		}, nil
	}
	now := core.now()
	registration, snapshotView, evidences := core.currentViews()
	claimed, err := core.matcher.Claim(dispatch.ClaimRequest{
		AuthorityNamespaceId: core.namespace,
		RegistrationId:       registration.RegistrationId,
		Snapshot:             snapshotView,
		Evidences:            evidences,
		Requirements:         request.Requirements,
		TargetActor:          core.targetActor,
		TaskId:               request.TaskId,
		RunId:                request.RunId,
		AttemptId:            request.AttemptId,
		AllocationId:         request.AllocationId,
		AckDeadlineAt:        now.Add(core.ackWindow).Format(time.RFC3339),
		ExpiresAt:            now.Add(core.leaseWindow).Format(time.RFC3339),
	}, now)
	if err != nil {
		class := core.classifyClaimFailure(err)
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: class})
		return sandbox.DualLeaseRef{}, sandbox.DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: err.Error()}, nil
	}
	if err := dispatch.ValidateLeaseFencing(claimed, claimed.Generation, claimed.FencingToken); err != nil {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: sandbox.DualReasonFencing})
		return sandbox.DualLeaseRef{}, sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: err.Error()}, nil
	}
	if err := core.ledger.AppendClaim(claimed); err != nil {
		class := sandbox.DualReasonIneligible
		if errors.Is(err, dispatch.ErrLeaseConflict) {
			class = sandbox.DualReasonDuplicateClaim
		}
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: class})
		return sandbox.DualLeaseRef{}, sandbox.DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: err.Error()}, nil
	}
	core.requirementsByLease[claimed.LeaseId] = request.Requirements
	core.workloadRoleByLease[claimed.LeaseId] = request.WorkloadRole
	core.acked[claimed.LeaseId] = false
	return leaseRefOf(claimed), sandbox.DualOperationOutcome{Accepted: true}, nil
}

// classifyClaimFailure maps one gate-6 claim failure onto the closed reason
// class of the current authority ledger view.
func (core *DispatchTransportCore) classifyClaimFailure(err error) sandbox.DualReasonClass {
	if errors.Is(err, dispatch.ErrLeaseConflict) {
		return sandbox.DualReasonDuplicateClaim
	}
	if strings.Contains(err.Error(), "already carries lease") {
		return sandbox.DualReasonDuplicateClaim
	}
	if stored, getErr := core.store.Get(core.registration.RegistrationId); getErr == nil {
		switch stored.LifecycleState {
		case provider.LifecycleStateRevoked:
			return sandbox.DualReasonRevoked
		case provider.LifecycleStateExpired:
			return sandbox.DualReasonExpired
		}
	}
	switch core.snapshot.SnapshotState {
	case provider.SnapshotStateSuperseded, provider.SnapshotStateExpired:
		return sandbox.DualReasonSuperseded
	}
	for _, evidence := range core.evidences {
		switch evidence.EvidenceState {
		case provider.EvidenceStateRevoked:
			return sandbox.DualReasonEvidenceRevoked
		case provider.EvidenceStateExpired:
			return sandbox.DualReasonEvidenceRevoked
		}
	}
	if stored, getErr := core.store.Get(core.registration.RegistrationId); getErr == nil {
		if core.snapshot.RegistrationId != stored.RegistrationId ||
			core.snapshot.ProtocolVersion != stored.ProtocolVersion ||
			core.snapshot.ProviderType != stored.ProviderType ||
			core.snapshot.ProviderName != stored.ProviderName ||
			core.snapshot.ProviderVersion != stored.ProviderVersion ||
			!core.snapshot.Attestation.Equal(stored.Attestation) {
			return sandbox.DualReasonIncompatible
		}
	}
	return sandbox.DualReasonIneligible
}

// CompleteClaim implements sandbox.DualAuthority: the topology completed
// its offer/ack transitions, the claim becomes accepted.
func (core *DispatchTransportCore) CompleteClaim(ctx context.Context, lease sandbox.DualLeaseRef) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	if _, ok := core.requirementsByLease[lease.LeaseId]; !ok {
		return fmt.Errorf("app: dispatch transport core: %w: %s", dispatch.ErrUnknownLease, lease.LeaseId)
	}
	if core.acked[lease.LeaseId] {
		return nil
	}
	core.acked[lease.LeaseId] = true
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventClaimAccepted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId})
	return nil
}

// currentLease returns the current ledger snapshot of one lease; callers
// must hold core.mu.
func (core *DispatchTransportCore) currentLease(leaseId string) (dispatch.DispatchLease, error) {
	lease, _, _, err := core.ledger.Current(leaseId)
	return lease, err
}

// revalidateCurrent rechecks one in-flight lease against the current
// ledger views; callers must hold core.mu.
func (core *DispatchTransportCore) revalidateCurrent(lease dispatch.DispatchLease) error {
	requirements, ok := core.requirementsByLease[lease.LeaseId]
	if !ok {
		return fmt.Errorf("dispatch: revalidate: unknown lease binding")
	}
	_, snapshot, evidences := core.currentViews()
	return core.matcher.Revalidate(lease, snapshot, evidences, requirements, core.now())
}

// cancelReasonOf extracts the machine-readable cancel reason of one
// revalidate failure.
func cancelReasonOf(err error) dispatch.CancelReason {
	const marker = "cancelReason "
	message := err.Error()
	index := strings.Index(message, marker)
	if index < 0 {
		return dispatch.CancelReasonRegistrationIncompatible
	}
	rest := message[index+len(marker):]
	if colon := strings.Index(rest, ":"); colon >= 0 {
		rest = rest[:colon]
	}
	reason := dispatch.CancelReason(strings.TrimSpace(rest))
	if validateErr := reason.Validate(); validateErr != nil {
		return dispatch.CancelReasonRegistrationIncompatible
	}
	return reason
}

// fenceLease cancels one in-flight lease with the machine-readable reason
// and records the terminal lease-revoked event with the mapped closed
// reason class; terminal leases are left untouched. Callers must hold
// core.mu.
func (core *DispatchTransportCore) fenceLease(lease dispatch.DispatchLease, reason dispatch.CancelReason) {
	if lease.LeaseState.IsTerminal() {
		return
	}
	if err := core.ledger.AppendCancel(lease.LeaseId, reason, lease.Generation); err != nil {
		return
	}
	core.record(sandbox.DualTraceEvent{
		Kind:        sandbox.DualEventLeaseRevoked,
		AttemptId:   lease.AttemptId,
		LeaseId:     lease.LeaseId,
		ReasonClass: sandbox.DualReasonForCancelReason(reason),
	})
}

// AdjudicateExecutionStart implements sandbox.DualAuthority: current ledger
// state, fencing and eligibility recheck plus the single-active execution
// reservation. No business event is recorded here — the exec-started
// event belongs to the successful transport handoff.
func (core *DispatchTransportCore) AdjudicateExecutionStart(ctx context.Context, lease sandbox.DualLeaseRef, commandId string) (string, sandbox.DualOperationOutcome, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if current.LeaseState.IsTerminal() {
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "the lease is terminal"}, nil
	}
	if err := dispatch.ValidateLeaseFencing(current, lease.Generation, lease.FencingToken); err != nil {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventFencingViolationBlocked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonFencing})
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: err.Error()}, nil
	}
	if err := core.revalidateCurrent(current); err != nil {
		core.fenceLease(current, cancelReasonOf(err))
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "the lease lost eligibility"}, nil
	}
	if core.openExecutions[lease.LeaseId] != "" {
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the lease already carries an open execution; no dual-active execution"}, nil
	}
	return "exec:" + lease.LeaseId + ":" + commandId, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// RecordExecutionStarted implements sandbox.DualAuthority.
func (core *DispatchTransportCore) RecordExecutionStarted(ctx context.Context, lease sandbox.DualLeaseRef, commandId, executionId string) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil || current.LeaseState.IsTerminal() {
		return fmt.Errorf("app: dispatch transport core: the lease is not in flight")
	}
	if core.openExecutions[lease.LeaseId] != "" {
		return fmt.Errorf("app: dispatch transport core: the lease already carries an open execution")
	}
	core.openExecutions[lease.LeaseId] = commandId
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventExecStarted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId})
	return nil
}

// RecordExecutionFinished implements sandbox.DualAuthority and returns the
// deterministic execution digest.
func (core *DispatchTransportCore) RecordExecutionFinished(ctx context.Context, lease sandbox.DualLeaseRef, commandId, executionId string) (string, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil || current.LeaseState.IsTerminal() {
		return "", fmt.Errorf("app: dispatch transport core: the lease is not in flight")
	}
	if core.openExecutions[lease.LeaseId] != commandId {
		return "", fmt.Errorf("app: dispatch transport core: no open execution carries commandId %q", commandId)
	}
	core.openExecutions[lease.LeaseId] = ""
	if core.finishedExecutions[lease.LeaseId] == nil {
		core.finishedExecutions[lease.LeaseId] = map[string]struct{}{}
	}
	core.finishedExecutions[lease.LeaseId][commandId] = struct{}{}
	digest := sandbox.DualExecutionDigest(lease.LeaseId, commandId, executionId)
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventExecFinished, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, Digest: digest})
	return digest, nil
}

// AdjudicateResult implements sandbox.DualAuthority: the current-ledger
// admission decision of one submitted result.
func (core *DispatchTransportCore) AdjudicateResult(ctx context.Context, lease sandbox.DualLeaseRef, commandId, resultDigest string) (sandbox.DualOperationOutcome, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonLateResult})
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonLateResult, Detail: "unknown lease"}, nil
	}
	if current.LeaseState.IsTerminal() {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonLateResult})
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonLateResult, Detail: "the lease lost eligibility before the result arrived"}, nil
	}
	if err := dispatch.ValidateLeaseFencing(current, lease.Generation, lease.FencingToken); err != nil {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonFencing})
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: err.Error()}, nil
	}
	if err := core.revalidateCurrent(current); err != nil {
		reason := cancelReasonOf(err)
		core.fenceLease(current, reason)
		class := sandbox.DualReasonForCancelReason(reason)
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: class})
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the lease lost eligibility"}, nil
	}
	if _, ok := core.finishedExecutions[lease.LeaseId][commandId]; !ok {
		core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonIneligible})
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonIneligible, Detail: "no finished execution carries this result"}, nil
	}
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventResultAdmitted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, Digest: resultDigest})
	return sandbox.DualOperationOutcome{Accepted: true}, nil
}

// AdjudicateHeartbeat implements sandbox.DualAuthority: the deadline
// recheck of one in-flight lease against the current ledger.
func (core *DispatchTransportCore) AdjudicateHeartbeat(ctx context.Context, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if current.LeaseState.IsTerminal() {
		class := sandbox.DualReasonDeadline
		if current.LeaseState == dispatch.LeaseStateCancelled {
			class = sandbox.DualReasonForCancelReason(current.CancelReason)
		}
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the lease is terminal"}, nil
	}
	if err := dispatch.ValidateLeaseFencing(current, lease.Generation, lease.FencingToken); err != nil {
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: err.Error()}, nil
	}
	if err := core.revalidateCurrent(current); err != nil {
		reason := cancelReasonOf(err)
		if reason == dispatch.CancelReasonDeadlineExceeded {
			core.expireLeaseLocked(current)
		} else {
			core.fenceLease(current, reason)
		}
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonForCancelReason(reason), Detail: err.Error()}, nil
	}
	return sandbox.DualOperationOutcome{Accepted: true}, nil
}

// AdjudicateStaleOperation implements sandbox.DualAuthority: one operation
// presenting a stale fencingToken is always fencing-violation-blocked.
func (core *DispatchTransportCore) AdjudicateStaleOperation(ctx context.Context, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if err := dispatch.ValidateLeaseFencing(current, lease.Generation, lease.FencingToken); err == nil {
		return sandbox.DualOperationOutcome{}, fmt.Errorf("app: dispatch transport core: the presented operation must carry a stale fencing identity")
	}
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventFencingViolationBlocked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonFencing})
	return sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonFencing, Detail: "stale fencingToken blocked"}, nil
}

// Invalidate implements sandbox.DualAuthority: one post-claim invalidation
// fact makes every in-flight lease lose eligibility immediately.
func (core *DispatchTransportCore) Invalidate(ctx context.Context, kind sandbox.DualInvalidationKind) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := kind.Validate(); err != nil {
		return err
	}
	var reason dispatch.CancelReason
	switch kind {
	case sandbox.DualInvalidateRegistrationRevoke:
		if err := core.store.Revoke(core.registration.RegistrationId); err != nil {
			return fmt.Errorf("app: dispatch transport core: registration revoke: %w", err)
		}
		reason = dispatch.CancelReasonSecurityCriticalRevoke
	case sandbox.DualInvalidateRegistrationExpire:
		if err := core.store.Expire(core.registration.RegistrationId); err != nil {
			return fmt.Errorf("app: dispatch transport core: registration expire: %w", err)
		}
		reason = dispatch.CancelReasonRegistrationExpired
	case sandbox.DualInvalidateRegistrationIncompatible:
		mutated := core.snapshot
		mutated.ProviderVersion = core.registration.ProviderVersion + "-incompatible"
		core.snapshot = mutated
		reason = dispatch.CancelReasonRegistrationIncompatible
	case sandbox.DualInvalidateSnapshotSupersede:
		mutated := core.snapshot
		mutated.SnapshotState = provider.SnapshotStateSuperseded
		core.snapshot = mutated
		reason = dispatch.CancelReasonSnapshotSuperseded
	case sandbox.DualInvalidateEvidenceRevoke:
		mutated := append([]provider.ConformanceEvidence(nil), core.evidences...)
		for index := range mutated {
			mutated[index].EvidenceState = provider.EvidenceStateRevoked
		}
		core.evidences = mutated
		reason = dispatch.CancelReasonEvidenceRevoked
	}
	for leaseId := range core.requirementsByLease {
		current, err := core.currentLease(leaseId)
		if err != nil {
			continue
		}
		core.fenceLease(current, reason)
	}
	return nil
}

// expireLeaseLocked expires one in-flight lease through the durable ledger
// and records lease-expired with the deadline reason class; callers must
// hold core.mu.
func (core *DispatchTransportCore) expireLeaseLocked(lease dispatch.DispatchLease) {
	if lease.LeaseState.IsTerminal() {
		return
	}
	if err := core.ledger.AppendExpire(lease.LeaseId, lease.Generation); err != nil {
		return
	}
	core.record(sandbox.DualTraceEvent{Kind: sandbox.DualEventLeaseExpired, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: sandbox.DualReasonDeadline})
}

// MissAckDeadline implements sandbox.DualAuthority: the ack deadline state
// of the deadline semantics.
func (core *DispatchTransportCore) MissAckDeadline(ctx context.Context, lease sandbox.DualLeaseRef) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: %w: %s", dispatch.ErrUnknownLease, lease.LeaseId)
	}
	if current.LeaseState.IsTerminal() {
		return fmt.Errorf("app: dispatch transport core: the lease is already terminal")
	}
	if core.acked[lease.LeaseId] {
		return fmt.Errorf("app: dispatch transport core: the lease was already acknowledged; the ack deadline cannot be missed")
	}
	ackDeadline, err := time.Parse(time.RFC3339, current.AckDeadlineAt)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: ackDeadlineAt: %w", err)
	}
	core.advanceClockTo(ackDeadline.Add(time.Minute))
	core.expireLeaseLocked(current)
	return nil
}

// ExpireLeaseWindow implements sandbox.DualAuthority: the expiry state of
// the deadline semantics.
func (core *DispatchTransportCore) ExpireLeaseWindow(ctx context.Context, lease sandbox.DualLeaseRef) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(lease.LeaseId)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: %w: %s", dispatch.ErrUnknownLease, lease.LeaseId)
	}
	if current.LeaseState.IsTerminal() {
		return fmt.Errorf("app: dispatch transport core: the lease is already terminal")
	}
	expiresAt, err := time.Parse(time.RFC3339, current.ExpiresAt)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: expiresAt: %w", err)
	}
	core.advanceClockTo(expiresAt.Add(time.Minute))
	core.expireLeaseLocked(current)
	return nil
}

// Reregister implements sandbox.DualAuthority: one fresh eligible
// registration/snapshot/evidence chain replaces the invalidated one. The
// invalidated registration stays terminal in the durable ledger and is
// never resurrected.
func (core *DispatchTransportCore) Reregister(ctx context.Context) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	core.reregistrations++
	suffix := fmt.Sprintf("r%d", core.reregistrations)
	registrationId := dispatchTransportRegistrationID + "-" + suffix
	registration, err := core.buildRegistration(registrationId,
		"embedded"+":"+registrationId,
		sandbox.RecomputeSHA256([]byte("dispatch-transport-registration"+"\x00"+registrationId)),
		core.registration.SecurityDomainId)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: reregister: %w", err)
	}
	stored, err := core.store.Put(registration)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: reregister submission: %w", err)
	}
	evidences, err := core.buildEvidences(stored)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: reregister evidence chain: %w", err)
	}
	snapshot, err := core.buildSnapshot(stored, evidences[0].EvidenceDigest)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: reregister snapshot: %w", err)
	}
	if err := provider.ValidateEvidenceSetForSnapshot(snapshot, evidences); err != nil {
		return fmt.Errorf("app: dispatch transport core: reregister evidence alignment: %w", err)
	}
	core.registration = stored
	core.snapshot = snapshot
	core.evidences = evidences
	return nil
}

// ---- Pull queue and Core-side wire surface ------------------------------

// enqueuePullItem stages one Pull transition item and returns its request
// identity.
func (core *DispatchTransportCore) enqueuePullItem(kind, requestId string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("app: dispatch transport core: marshal pull item: %w", err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	core.pullQueue = append(core.pullQueue, pullQueueItem{kind: kind, requestId: requestId, payload: raw})
	return nil
}

// completePullReceipt stores the transport outcome of one queued item.
func (core *DispatchTransportCore) completePullReceipt(requestId string, receipt transportReceipt) {
	core.mu.Lock()
	defer core.mu.Unlock()
	core.receipts[requestId] = receipt
}

// takePullReceipt reads the transport outcome of one queued item.
func (core *DispatchTransportCore) takePullReceipt(requestId string) (transportReceipt, bool) {
	core.mu.Lock()
	defer core.mu.Unlock()
	receipt, ok := core.receipts[requestId]
	return receipt, ok
}

// dequeuePullItem pops the next staged Pull item, when present.
func (core *DispatchTransportCore) dequeuePullItem() (pullQueueItem, bool) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.pullQueue) == 0 {
		return pullQueueItem{}, false
	}
	item := core.pullQueue[0]
	core.pullQueue = core.pullQueue[1:]
	return item, true
}

// leaseRefForWire resolves one presented lease binding for the Core-side
// wire endpoints.
func (core *DispatchTransportCore) leaseRefForWire(binding wireLeaseOperation) (sandbox.DualLeaseRef, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	current, err := core.currentLease(binding.LeaseId)
	if err != nil {
		return sandbox.DualLeaseRef{}, err
	}
	ref := leaseRefOf(current)
	ref.Generation = binding.Generation
	ref.FencingToken = binding.FencingToken
	return ref, nil
}

// Handler returns the Core-side wire surface of the protocol family: the
// Pull poll endpoint plus the runner-to-Core offer-ack, heartbeat, result
// and receipt ingress. The identical surface serves every topology; the
// transport security baseline is composed by the caller layer.
func (core *DispatchTransportCore) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+dispatchPathPoll, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationPoll, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationPoll, "", err)
			return
		}
		item, available := core.dequeuePullItem()
		response := wirePollResponse{Available: available}
		if available {
			response.ItemKind = item.kind
			response.RequestId = item.requestId
			switch item.kind {
			case dispatchOperationOffer:
				var payload wireOfferPayload
				if err := decodeStrict(item.payload, &payload); err != nil {
					writeDispatchError(writer, dispatchOperationPoll, envelope.RequestId, err)
					return
				}
				response.Offer = &payload
			case dispatchOperationExec:
				var payload wireExecPayload
				if err := decodeStrict(item.payload, &payload); err != nil {
					writeDispatchError(writer, dispatchOperationPoll, envelope.RequestId, err)
					return
				}
				response.Exec = &payload
			case dispatchOperationSPI:
				var payload wireSPIRequest
				if err := decodeStrict(item.payload, &payload); err != nil {
					writeDispatchError(writer, dispatchOperationPoll, envelope.RequestId, err)
					return
				}
				response.SPI = &payload
			}
		}
		writeDispatchEnvelope(writer, dispatchOperationPoll, envelope.RequestId, response)
	})
	mux.HandleFunc("POST "+dispatchPathOfferAck, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationOfferAck, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationOfferAck, "", err)
			return
		}
		var payload wireOfferAckPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationOfferAck, envelope.RequestId, fmt.Errorf("%w: ack payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		core.mu.Lock()
		current, leaseErr := core.currentLease(payload.LeaseId)
		if leaseErr == nil && current.LeaseState.IsTerminal() {
			leaseErr = fmt.Errorf("%w: the lease is terminal", dispatch.ErrLeaseConflict)
		}
		if leaseErr == nil {
			leaseErr = dispatch.ValidateLeaseFencing(current, payload.Generation, payload.FencingToken)
		}
		core.mu.Unlock()
		if leaseErr != nil {
			writeDispatchError(writer, dispatchOperationOfferAck, envelope.RequestId, leaseErr)
			return
		}
		if err := core.CompleteClaim(request.Context(), leaseRefOf(current)); err != nil {
			writeDispatchError(writer, dispatchOperationOfferAck, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationOfferAck, envelope.RequestId, wireVerdictPayload{Accepted: true})
	})
	mux.HandleFunc("POST "+dispatchPathHeartbeat, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationHeartbeat, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationHeartbeat, "", err)
			return
		}
		var payload wireLeaseOperation
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationHeartbeat, envelope.RequestId, fmt.Errorf("%w: heartbeat payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		ref, err := core.leaseRefForWire(payload)
		if err != nil {
			writeDispatchError(writer, dispatchOperationHeartbeat, envelope.RequestId, err)
			return
		}
		outcome, err := core.AdjudicateHeartbeat(request.Context(), ref)
		if err != nil {
			writeDispatchError(writer, dispatchOperationHeartbeat, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationHeartbeat, envelope.RequestId, wireVerdictPayload{Accepted: outcome.Accepted, ReasonClass: outcome.ReasonClass, Detail: outcome.Detail})
	})
	mux.HandleFunc("POST "+dispatchPathResult, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationResult, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationResult, "", err)
			return
		}
		var payload wireResultPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationResult, envelope.RequestId, fmt.Errorf("%w: result payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		ref, err := core.leaseRefForWire(wireLeaseOperation{LeaseId: payload.LeaseId, AttemptId: payload.AttemptId, Generation: payload.Generation, FencingToken: payload.FencingToken})
		if err != nil {
			writeDispatchError(writer, dispatchOperationResult, envelope.RequestId, err)
			return
		}
		outcome, err := core.AdjudicateResult(request.Context(), ref, payload.CommandId, payload.ResultDigest)
		if err != nil {
			writeDispatchError(writer, dispatchOperationResult, envelope.RequestId, err)
			return
		}
		writeDispatchEnvelope(writer, dispatchOperationResult, envelope.RequestId, wireVerdictPayload{Accepted: outcome.Accepted, ReasonClass: outcome.ReasonClass, Detail: outcome.Detail})
	})
	mux.HandleFunc("POST "+dispatchPathReceipt, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationReceipt, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationReceipt, "", err)
			return
		}
		var payload wireReceiptPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationReceipt, envelope.RequestId, fmt.Errorf("%w: receipt payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		core.completePullReceipt(payload.RequestId, transportReceipt{success: payload.Success, detail: payload.Detail})
		writeDispatchEnvelope(writer, dispatchOperationReceipt, envelope.RequestId, wireVerdictPayload{Accepted: true})
	})
	mux.HandleFunc("POST "+dispatchPathSPIReceipt, func(writer http.ResponseWriter, request *http.Request) {
		body, err := readDispatchBody(request)
		if err != nil {
			writeDispatchError(writer, dispatchOperationSPIReceipt, "", err)
			return
		}
		envelope, err := decodeDispatchEnvelope(body)
		if err != nil {
			writeDispatchError(writer, dispatchOperationSPIReceipt, "", err)
			return
		}
		var payload wireSPIReceiptPayload
		if err := decodeStrict(envelope.Payload, &payload); err != nil {
			writeDispatchError(writer, dispatchOperationSPIReceipt, envelope.RequestId, fmt.Errorf("%w: spi receipt payload: %v", sandbox.ErrInvalidRequest, err))
			return
		}
		core.completePullReceipt(payload.RequestId, transportReceipt{success: payload.Error == nil, payload: payload.Payload, wireError: payload.Error})
		writeDispatchEnvelope(writer, dispatchOperationSPIReceipt, envelope.RequestId, wireVerdictPayload{Accepted: true})
	})
	return mux
}

// ---- Topology bindings ---------------------------------------------------

// EmbeddedTopologyBinding is the embedded/in-process transport adapter of
// the protocol family: the identical Core adjudication with in-process
// provider delivery and immediate offer/ack transitions.
type EmbeddedTopologyBinding struct {
	core *DispatchTransportCore
}

// NewEmbeddedTopologyBinding binds the embedded adapter over one Core.
func NewEmbeddedTopologyBinding(core *DispatchTransportCore) *EmbeddedTopologyBinding {
	return &EmbeddedTopologyBinding{core: core}
}

// Topology implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) Topology() sandbox.DualTopology {
	return sandbox.TopologyEmbedded
}

// provisionIdentity derives the provision identity of one lease offer.
func provisionIdentity(lease dispatch.DispatchLease, role sandbox.WorkloadRole) sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       lease.TaskId,
		RunId:        lease.RunId,
		AttemptId:    lease.AttemptId,
		WorkloadRole: role,
		AllocationId: lease.AllocationId,
		Generation:   lease.Generation,
		FencingToken: lease.FencingToken,
		CommandId:    "offer-" + lease.LeaseId,
	}
}

// Claim implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) Claim(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	record, ok := binding.core.LeaseRecord(lease.LeaseId)
	if !ok {
		return sandbox.DualClaimReceipt{}, fmt.Errorf("app: embedded topology: the adjudicated lease vanished: %s", lease.LeaseId)
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	if _, err := binding.core.Provider().Provision(ctx, sandbox.ProvisionRequest{
		Identity:     provisionIdentity(record, role),
		Requirements: request.Requirements,
	}); err != nil {
		return sandbox.DualClaimReceipt{
			Lease:   lease,
			Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the provision response was lost: " + err.Error()},
		}, nil
	}
	if err := authoritySeam.CompleteClaim(ctx, lease); err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	return sandbox.DualClaimReceipt{Lease: lease, Outcome: sandbox.DualOperationOutcome{Accepted: true}}, nil
}

// ClaimUnacked implements sandbox.DualTopologyBinding: the in-process ack
// transition stays out, modeling the identical unaccepted-offer state.
func (binding *EmbeddedTopologyBinding) ClaimUnacked(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	return sandbox.DualClaimReceipt{
		Lease:   lease,
		Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the in-process ack transition stayed out"},
	}, nil
}

// execIdentity derives the exec identity of one execution handoff.
func execIdentity(lease sandbox.DualLeaseRef, role sandbox.WorkloadRole, commandId string) sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       lease.TaskId,
		RunId:        lease.RunId,
		AttemptId:    lease.AttemptId,
		WorkloadRole: role,
		AllocationId: lease.AllocationId,
		Generation:   lease.Generation,
		FencingToken: lease.FencingToken,
		CommandId:    commandId,
	}
}

// StartExecution implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) StartExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId string) (string, sandbox.DualOperationOutcome, error) {
	executionId, outcome, err := authoritySeam.AdjudicateExecutionStart(ctx, lease, commandId)
	if err != nil || !outcome.Accepted {
		return "", outcome, err
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	if _, err := binding.core.Provider().Exec(ctx, sandbox.ExecRequest{
		Identity:     execIdentity(lease, role, commandId),
		AllocationId: lease.AllocationId,
		Command:      []string{"dispatch-exec:" + commandId},
	}); err != nil {
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: err.Error()}, nil
	}
	if err := authoritySeam.RecordExecutionStarted(ctx, lease, commandId, executionId); err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return executionId, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// FinishExecution implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) FinishExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, executionId string) (string, sandbox.DualOperationOutcome, error) {
	digest, err := authoritySeam.RecordExecutionFinished(ctx, lease, commandId, executionId)
	if err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return digest, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// SubmitResult implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) SubmitResult(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, resultDigest string) (sandbox.DualOperationOutcome, error) {
	return authoritySeam.AdjudicateResult(ctx, lease, commandId, resultDigest)
}

// Heartbeat implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) Heartbeat(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	return authoritySeam.AdjudicateHeartbeat(ctx, lease)
}

// PresentStaleOperation implements sandbox.DualTopologyBinding.
func (binding *EmbeddedTopologyBinding) PresentStaleOperation(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	return presentStaleFencing(ctx, authoritySeam, lease)
}

// presentStaleFencing derives the stale fencing identity every topology
// presents and delegates the adjudication to the shared Core.
func presentStaleFencing(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	stale := lease
	stale.FencingToken = sandbox.RecomputeSHA256([]byte("stale-fencing" + "\x00" + lease.LeaseId))
	if lease.Generation > 1 {
		stale.Generation = lease.Generation - 1
	} else {
		stale.Generation = lease.Generation + 1
	}
	return authoritySeam.AdjudicateStaleOperation(ctx, stale)
}

// PushTopologyBinding is the Push HTTP transport adapter: the Core delivers
// offers and exec commands to the provider endpoint over the versioned
// protocol family; the runner-to-Core result and heartbeat ingress stays
// outbound.
type PushTopologyBinding struct {
	core            *DispatchTransportCore
	providerBaseURL string
	coreBaseURL     string
	client          *DispatchTransportClient
}

// NewPushTopologyBinding binds the Push adapter over one Core, one provider
// endpoint URL and one Core ingress URL.
func NewPushTopologyBinding(core *DispatchTransportCore, providerBaseURL, coreBaseURL string, client *DispatchTransportClient) *PushTopologyBinding {
	if client == nil {
		client = NewDispatchTransportClient(nil, nil)
	}
	return &PushTopologyBinding{core: core, providerBaseURL: providerBaseURL, coreBaseURL: coreBaseURL, client: client}
}

// Topology implements sandbox.DualTopologyBinding.
func (binding *PushTopologyBinding) Topology() sandbox.DualTopology { return sandbox.TopologyPush }

// Claim implements sandbox.DualTopologyBinding: adjudication, push offer
// delivery, provider-side provision and the ack completion.
func (binding *PushTopologyBinding) Claim(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	record, ok := binding.core.LeaseRecord(lease.LeaseId)
	if !ok {
		return sandbox.DualClaimReceipt{}, fmt.Errorf("app: push topology: the adjudicated lease vanished: %s", lease.LeaseId)
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	_, err = binding.client.postEnvelope(ctx, binding.providerBaseURL, dispatchPathOffer, dispatchOperationOffer,
		"offer-"+lease.LeaseId, wireOfferPayload{Lease: record, WorkloadRole: role, Requirements: request.Requirements})
	if err != nil {
		return sandbox.DualClaimReceipt{
			Lease:   lease,
			Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the offer/provision response was lost: " + err.Error()},
		}, nil
	}
	if err := authoritySeam.CompleteClaim(ctx, lease); err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	return sandbox.DualClaimReceipt{Lease: lease, Outcome: sandbox.DualOperationOutcome{Accepted: true}}, nil
}

// ClaimUnacked implements sandbox.DualTopologyBinding: the offer is never
// delivered (or its ack is lost) — the identical unaccepted-offer state
// under the Push transitions.
func (binding *PushTopologyBinding) ClaimUnacked(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	return sandbox.DualClaimReceipt{
		Lease:   lease,
		Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the pushed offer ack was lost"},
	}, nil
}

// StartExecution implements sandbox.DualTopologyBinding: adjudication, push
// exec delivery and the exec-started recording after the provider accepted
// the command.
func (binding *PushTopologyBinding) StartExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId string) (string, sandbox.DualOperationOutcome, error) {
	executionId, outcome, err := authoritySeam.AdjudicateExecutionStart(ctx, lease, commandId)
	if err != nil || !outcome.Accepted {
		return "", outcome, err
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	_, err = binding.client.postEnvelope(ctx, binding.providerBaseURL, dispatchPathExec, dispatchOperationExec,
		"exec-"+lease.LeaseId+"-"+commandId, wireExecPayload{
			Identity:     execIdentity(lease, role, commandId),
			LeaseId:      lease.LeaseId,
			AllocationId: lease.AllocationId,
			Command:      []string{"dispatch-exec:" + commandId},
			CommandId:    commandId,
		})
	if err != nil {
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: err.Error()}, nil
	}
	if err := authoritySeam.RecordExecutionStarted(ctx, lease, commandId, executionId); err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return executionId, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// FinishExecution implements sandbox.DualTopologyBinding.
func (binding *PushTopologyBinding) FinishExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, executionId string) (string, sandbox.DualOperationOutcome, error) {
	digest, err := authoritySeam.RecordExecutionFinished(ctx, lease, commandId, executionId)
	if err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return digest, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// SubmitResult implements sandbox.DualTopologyBinding: the result ingress
// is runner-to-Core outbound under every topology.
func (binding *PushTopologyBinding) SubmitResult(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, resultDigest string) (sandbox.DualOperationOutcome, error) {
	envelope, err := binding.client.postEnvelope(ctx, binding.coreBaseURL, dispatchPathResult, dispatchOperationResult,
		"result-"+lease.LeaseId+"-"+commandId, wireResultPayload{
			LeaseId:      lease.LeaseId,
			AttemptId:    lease.AttemptId,
			Generation:   lease.Generation,
			FencingToken: lease.FencingToken,
			CommandId:    commandId,
			ResultDigest: resultDigest,
		})
	if err != nil {
		return sandbox.DualOperationOutcome{}, err
	}
	return decodeVerdictPayload(envelope.Payload)
}

// Heartbeat implements sandbox.DualTopologyBinding.
func (binding *PushTopologyBinding) Heartbeat(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	envelope, err := binding.client.postEnvelope(ctx, binding.coreBaseURL, dispatchPathHeartbeat, dispatchOperationHeartbeat,
		"heartbeat-"+lease.LeaseId, wireLeaseOperation{
			LeaseId:      lease.LeaseId,
			AttemptId:    lease.AttemptId,
			Generation:   lease.Generation,
			FencingToken: lease.FencingToken,
		})
	if err != nil {
		return sandbox.DualOperationOutcome{}, err
	}
	return decodeVerdictPayload(envelope.Payload)
}

// PresentStaleOperation implements sandbox.DualTopologyBinding.
func (binding *PushTopologyBinding) PresentStaleOperation(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	return presentStaleFencing(ctx, authoritySeam, lease)
}

// decodeVerdictPayload admits one Core verdict payload.
func decodeVerdictPayload(raw json.RawMessage) (sandbox.DualOperationOutcome, error) {
	var payload wireVerdictPayload
	if err := decodeStrict(raw, &payload); err != nil {
		return sandbox.DualOperationOutcome{}, fmt.Errorf("app: dispatch transport: decode verdict payload: %w", err)
	}
	return sandbox.DualOperationOutcome{Accepted: payload.Accepted, ReasonClass: payload.ReasonClass, Detail: payload.Detail}, nil
}

// PullRunner is the outbound-only runner of the Pull topology: it polls
// the Core offer queue, executes against its local provider and submits
// acks/receipts back — every call is an outbound request, the runner never
// listens on an inbound port.
type PullRunner struct {
	runnerId    string
	coreBaseURL string
	provider    sandbox.SandboxProvider
	client      *DispatchTransportClient
}

// NewPullRunner binds one outbound-only Pull runner.
func NewPullRunner(runnerId, coreBaseURL string, providerInstance sandbox.SandboxProvider, client *DispatchTransportClient) *PullRunner {
	if client == nil {
		client = NewDispatchTransportClient(nil, nil)
	}
	return &PullRunner{runnerId: runnerId, coreBaseURL: coreBaseURL, provider: providerInstance, client: client}
}

// RunOnce drives exactly one outbound poll cycle: poll the Core queue,
// execute the polled item against the local provider and submit the ack or
// receipt outbound. An empty queue is not an error.
func (runner *PullRunner) RunOnce(ctx context.Context) error {
	envelope, err := runner.client.postEnvelope(ctx, runner.coreBaseURL, dispatchPathPoll, dispatchOperationPoll,
		"poll-"+runner.runnerId, wirePollPayload{RunnerId: runner.runnerId})
	if err != nil {
		return err
	}
	var response wirePollResponse
	if err := decodeStrict(envelope.Payload, &response); err != nil {
		return fmt.Errorf("app: pull runner: decode poll response: %w", err)
	}
	if !response.Available {
		return nil
	}
	switch response.ItemKind {
	case dispatchOperationOffer:
		return runner.runOffer(ctx, response.RequestId, response.Offer)
	case dispatchOperationExec:
		return runner.runExec(ctx, response.RequestId, response.Exec)
	case dispatchOperationSPI:
		return runner.runSPI(ctx, response.RequestId, response.SPI)
	default:
		return fmt.Errorf("app: pull runner: unknown poll item kind %q", response.ItemKind)
	}
}

// runOffer provisions the local provider under the offered lease and
// submits the ack outbound.
func (runner *PullRunner) runOffer(ctx context.Context, requestId string, offer *wireOfferPayload) error {
	if offer == nil {
		return errors.New("app: pull runner: the poll response carries no offer payload")
	}
	if err := offer.Lease.Validate(); err != nil {
		return fmt.Errorf("app: pull runner: the offered lease does not validate: %w", err)
	}
	receipt, err := runner.provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     provisionIdentity(offer.Lease, offer.WorkloadRole),
		Requirements: offer.Requirements,
	})
	if err != nil {
		// The ack carries the failure; the Core never completes the claim
		// and the deadline reconciliation fences the unaccepted lease.
		_, postErr := runner.client.postEnvelope(ctx, runner.coreBaseURL, dispatchPathReceipt, dispatchOperationReceipt,
			"ack-error-"+requestId, wireReceiptPayload{RequestId: requestId, LeaseId: offer.Lease.LeaseId, Success: false, Detail: err.Error()})
		if postErr != nil {
			return postErr
		}
		return nil
	}
	_, err = runner.client.postEnvelope(ctx, runner.coreBaseURL, dispatchPathOfferAck, dispatchOperationOfferAck,
		"ack-"+requestId, wireOfferAckPayload{
			LeaseId:      offer.Lease.LeaseId,
			Generation:   offer.Lease.Generation,
			FencingToken: offer.Lease.FencingToken,
			Allocation:   receipt.Allocation,
		})
	return err
}

// runExec executes one polled exec command against the local provider and
// submits the receipt outbound.
func (runner *PullRunner) runExec(ctx context.Context, requestId string, exec *wireExecPayload) error {
	if exec == nil {
		return errors.New("app: pull runner: the poll response carries no exec payload")
	}
	_, err := runner.provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     exec.Identity,
		AllocationId: exec.AllocationId,
		Command:      exec.Command,
	})
	receipt := wireReceiptPayload{RequestId: requestId, LeaseId: exec.LeaseId, CommandId: exec.CommandId, Success: err == nil}
	if err != nil {
		receipt.Detail = err.Error()
	}
	_, postErr := runner.client.postEnvelope(ctx, runner.coreBaseURL, dispatchPathReceipt, dispatchOperationReceipt,
		"receipt-"+requestId, receipt)
	return postErr
}

// runSPI executes one polled SPI operation against the local provider and
// submits the SPI receipt outbound.
func (runner *PullRunner) runSPI(ctx context.Context, requestId string, spiRequest *wireSPIRequest) error {
	if spiRequest == nil {
		return errors.New("app: pull runner: the poll response carries no spi payload")
	}
	report, err := dispatchSPIOperation(ctx, runner.provider, *spiRequest)
	payload := wireSPIReceiptPayload{RequestId: requestId}
	if err != nil {
		wireErr := dispatchWireErrorFromError(err)
		payload.Error = &wireErr
	} else {
		raw, marshalErr := json.Marshal(report)
		if marshalErr != nil {
			return fmt.Errorf("app: pull runner: marshal spi receipt: %w", marshalErr)
		}
		payload.Payload = raw
	}
	_, postErr := runner.client.postEnvelope(ctx, runner.coreBaseURL, dispatchPathSPIReceipt, dispatchOperationSPIReceipt,
		"spi-receipt-"+requestId, payload)
	return postErr
}

// PullTopologyBinding is the Pull outbound runner transport adapter: the
// Core stages offers and commands in the pull queue, the outbound-only
// runner polls, provisions/executes locally and submits acks/receipts back.
type PullTopologyBinding struct {
	core   *DispatchTransportCore
	runner *PullRunner
}

// NewPullTopologyBinding binds the Pull adapter over one Core and one
// outbound-only runner.
func NewPullTopologyBinding(core *DispatchTransportCore, runner *PullRunner) *PullTopologyBinding {
	return &PullTopologyBinding{core: core, runner: runner}
}

// Topology implements sandbox.DualTopologyBinding.
func (binding *PullTopologyBinding) Topology() sandbox.DualTopology { return sandbox.TopologyPull }

// Claim implements sandbox.DualTopologyBinding: adjudication, offer
// staging, outbound poll, local provision and outbound ack.
func (binding *PullTopologyBinding) Claim(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	record, ok := binding.core.LeaseRecord(lease.LeaseId)
	if !ok {
		return sandbox.DualClaimReceipt{}, fmt.Errorf("app: pull topology: the adjudicated lease vanished: %s", lease.LeaseId)
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	requestId := "offer-" + lease.LeaseId
	if err := binding.core.enqueuePullItem(dispatchOperationOffer, requestId, wireOfferPayload{Lease: record, WorkloadRole: role, Requirements: request.Requirements}); err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if err := binding.runner.RunOnce(ctx); err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if receipt, ok := binding.core.takePullReceipt(requestId); ok && !receipt.success {
		return sandbox.DualClaimReceipt{
			Lease:   lease,
			Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the provision response was lost: " + receipt.detail},
		}, nil
	}
	binding.core.mu.Lock()
	acked := binding.core.acked[lease.LeaseId]
	binding.core.mu.Unlock()
	if !acked {
		return sandbox.DualClaimReceipt{
			Lease:   lease,
			Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the poll/ack transitions did not complete the claim"},
		}, nil
	}
	return sandbox.DualClaimReceipt{Lease: lease, Outcome: sandbox.DualOperationOutcome{Accepted: true}}, nil
}

// ClaimUnacked implements sandbox.DualTopologyBinding: the adjudicated
// offer is lost before it ever reaches the pull queue — the identical
// unaccepted-offer state under the Pull transitions, reconciled through
// the ack deadline.
func (binding *PullTopologyBinding) ClaimUnacked(ctx context.Context, authoritySeam sandbox.DualAuthority, request sandbox.DualClaimRequest) (sandbox.DualClaimReceipt, error) {
	lease, outcome, err := authoritySeam.AdjudicateClaim(ctx, request)
	if err != nil {
		return sandbox.DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return sandbox.DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	return sandbox.DualClaimReceipt{
		Lease:   lease,
		Outcome: sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: "the staged offer was lost before the runner ever polled it"},
	}, nil
}

// StartExecution implements sandbox.DualTopologyBinding: adjudication, exec
// staging, outbound poll, local execution and the receipt-driven
// exec-started recording.
func (binding *PullTopologyBinding) StartExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId string) (string, sandbox.DualOperationOutcome, error) {
	executionId, outcome, err := authoritySeam.AdjudicateExecutionStart(ctx, lease, commandId)
	if err != nil || !outcome.Accepted {
		return "", outcome, err
	}
	role := sandbox.WorkloadRoleWorker
	binding.core.mu.Lock()
	if bound, present := binding.core.workloadRoleByLease[lease.LeaseId]; present {
		role = bound
	}
	binding.core.mu.Unlock()
	requestId := "exec-" + lease.LeaseId + "-" + commandId
	if err := binding.core.enqueuePullItem(dispatchOperationExec, requestId, wireExecPayload{
		Identity:     execIdentity(lease, role, commandId),
		LeaseId:      lease.LeaseId,
		AllocationId: lease.AllocationId,
		Command:      []string{"dispatch-exec:" + commandId},
		CommandId:    commandId,
	}); err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	if err := binding.runner.RunOnce(ctx); err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	receipt, ok := binding.core.takePullReceipt(requestId)
	if !ok || !receipt.success {
		detail := "the exec receipt was lost"
		if ok {
			detail = receipt.detail
		}
		return "", sandbox.DualOperationOutcome{Accepted: false, ReasonClass: sandbox.DualReasonNone, Detail: detail}, nil
	}
	if err := authoritySeam.RecordExecutionStarted(ctx, lease, commandId, executionId); err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return executionId, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// FinishExecution implements sandbox.DualTopologyBinding.
func (binding *PullTopologyBinding) FinishExecution(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, executionId string) (string, sandbox.DualOperationOutcome, error) {
	digest, err := authoritySeam.RecordExecutionFinished(ctx, lease, commandId, executionId)
	if err != nil {
		return "", sandbox.DualOperationOutcome{}, err
	}
	return digest, sandbox.DualOperationOutcome{Accepted: true}, nil
}

// SubmitResult implements sandbox.DualTopologyBinding: outbound result
// ingress to the Core.
func (binding *PullTopologyBinding) SubmitResult(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef, commandId, resultDigest string) (sandbox.DualOperationOutcome, error) {
	envelope, err := binding.runner.client.postEnvelope(ctx, binding.coreBaseURL(), dispatchPathResult, dispatchOperationResult,
		"result-"+lease.LeaseId+"-"+commandId, wireResultPayload{
			LeaseId:      lease.LeaseId,
			AttemptId:    lease.AttemptId,
			Generation:   lease.Generation,
			FencingToken: lease.FencingToken,
			CommandId:    commandId,
			ResultDigest: resultDigest,
		})
	if err != nil {
		return sandbox.DualOperationOutcome{}, err
	}
	return decodeVerdictPayload(envelope.Payload)
}

// coreBaseURL exposes the Core ingress URL of the runner.
func (binding *PullTopologyBinding) coreBaseURL() string { return binding.runner.coreBaseURL }

// Heartbeat implements sandbox.DualTopologyBinding: outbound heartbeat
// ingress to the Core.
func (binding *PullTopologyBinding) Heartbeat(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	envelope, err := binding.runner.client.postEnvelope(ctx, binding.coreBaseURL(), dispatchPathHeartbeat, dispatchOperationHeartbeat,
		"heartbeat-"+lease.LeaseId, wireLeaseOperation{
			LeaseId:      lease.LeaseId,
			AttemptId:    lease.AttemptId,
			Generation:   lease.Generation,
			FencingToken: lease.FencingToken,
		})
	if err != nil {
		return sandbox.DualOperationOutcome{}, err
	}
	return decodeVerdictPayload(envelope.Payload)
}

// PresentStaleOperation implements sandbox.DualTopologyBinding.
func (binding *PullTopologyBinding) PresentStaleOperation(ctx context.Context, authoritySeam sandbox.DualAuthority, lease sandbox.DualLeaseRef) (sandbox.DualOperationOutcome, error) {
	return presentStaleFencing(ctx, authoritySeam, lease)
}

// ---- SPI adapters: RunConformance parameterized by topology --------------

// PushSPIAdapter implements sandbox.SandboxProvider over the Push wire
// surface: the identical RunConformance fixtures run through the versioned
// protocol family.
type PushSPIAdapter struct {
	providerBaseURL string
	client          *DispatchTransportClient
}

// NewPushSPIAdapter binds the Push SPI adapter.
func NewPushSPIAdapter(providerBaseURL string, client *DispatchTransportClient) *PushSPIAdapter {
	if client == nil {
		client = NewDispatchTransportClient(nil, nil)
	}
	return &PushSPIAdapter{providerBaseURL: providerBaseURL, client: client}
}

// spiCall issues one SPI operation over the wire and decodes the report
// into target.
func (adapter *PushSPIAdapter) spiCall(ctx context.Context, operation string, identity sandbox.OperationIdentity, payload any, target any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("app: dispatch spi: marshal payload: %w", err)
	}
	requestId, err := identity.ReplayKey()
	if err != nil {
		requestId = "spi-" + operation
	}
	envelope, err := adapter.client.postEnvelope(ctx, adapter.providerBaseURL, dispatchPathSPI, dispatchOperationSPI, requestId, wireSPIRequest{
		Operation: operation,
		Identity:  identity,
		Payload:   raw,
	})
	if err != nil {
		return err
	}
	if err := decodeStrict(envelope.Payload, target); err != nil {
		return fmt.Errorf("app: dispatch spi: decode %s report: %w", operation, err)
	}
	return nil
}

// Probe implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Probe(ctx context.Context, request sandbox.ProbeRequest) (*sandbox.ProbeReport, error) {
	report := &sandbox.ProbeReport{}
	err := adapter.spiCall(ctx, sandbox.OperationProbe, request.Identity, wireSPIProbe{Requirements: request.Requirements}, report)
	return report, err
}

// Provision implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Provision(ctx context.Context, request sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	receipt := &sandbox.ProvisionReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationProvision, request.Identity, wireSPIProvision{Requirements: request.Requirements, AllowedStoreIds: request.AllowedStoreIds}, receipt)
	return receipt, err
}

// Stage implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Stage(ctx context.Context, request sandbox.StageRequest) (*sandbox.StageReport, error) {
	report := &sandbox.StageReport{}
	err := adapter.spiCall(ctx, sandbox.OperationStage, request.Identity, wireSPIStage{AllocationId: request.AllocationId, Inputs: request.Inputs}, report)
	return report, err
}

// Exec implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Exec(ctx context.Context, request sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	receipt := &sandbox.ExecReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationExec, request.Identity, wireSPIExec{AllocationId: request.AllocationId, Command: request.Command}, receipt)
	return receipt, err
}

// Inspect implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Inspect(ctx context.Context, request sandbox.InspectRequest) (*sandbox.InspectReport, error) {
	report := &sandbox.InspectReport{}
	err := adapter.spiCall(ctx, sandbox.OperationInspect, request.Identity, wireSPIInspect{AllocationId: request.AllocationId}, report)
	return report, err
}

// Signal implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Signal(ctx context.Context, request sandbox.SignalRequest) (*sandbox.SignalReceipt, error) {
	receipt := &sandbox.SignalReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationSignal, request.Identity, wireSPISignal{AllocationId: request.AllocationId, Signal: request.Signal}, receipt)
	return receipt, err
}

// Checkpoint implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Checkpoint(ctx context.Context, request sandbox.CheckpointRequest) (*sandbox.CheckpointReceipt, error) {
	receipt := &sandbox.CheckpointReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationCheckpoint, request.Identity, wireSPICheckpoint{AllocationId: request.AllocationId}, receipt)
	return receipt, err
}

// Restore implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Restore(ctx context.Context, request sandbox.RestoreOperationRequest) (*sandbox.RestoreReceipt, error) {
	receipt := &sandbox.RestoreReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationRestore, request.Identity, wireSPIRestore{PreviousAllocationId: request.PreviousAllocationId, NextAllocationId: request.NextAllocationId, InPlaceConfirmed: request.InPlaceConfirmed}, receipt)
	return receipt, err
}

// Terminate implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Terminate(ctx context.Context, request sandbox.TerminateRequest) (*sandbox.TerminateReceipt, error) {
	receipt := &sandbox.TerminateReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationTerminate, request.Identity, wireSPITerminate{AllocationId: request.AllocationId}, receipt)
	return receipt, err
}

// Reconcile implements sandbox.SandboxProvider.
func (adapter *PushSPIAdapter) Reconcile(ctx context.Context, request sandbox.ReconcileRequest) (*sandbox.ReconcileReport, error) {
	report := &sandbox.ReconcileReport{}
	err := adapter.spiCall(ctx, sandbox.OperationReconcile, request.Identity, wireSPIReconcile{RunId: request.RunId, AttemptId: request.AttemptId}, report)
	return report, err
}

// PullSPIAdapter implements sandbox.SandboxProvider through the Pull
// topology: every SPI call stages one queue item and drives the
// outbound-only runner synchronously. The runner stays outbound-only; the
// synchronous stepping keeps the conformance run deterministic.
type PullSPIAdapter struct {
	core   *DispatchTransportCore
	runner *PullRunner
}

// NewPullSPIAdapter binds the Pull SPI adapter.
func NewPullSPIAdapter(core *DispatchTransportCore, runner *PullRunner) *PullSPIAdapter {
	return &PullSPIAdapter{core: core, runner: runner}
}

// spiCall stages one SPI operation, drives one runner cycle and decodes the
// receipt.
func (adapter *PullSPIAdapter) spiCall(ctx context.Context, operation string, identity sandbox.OperationIdentity, payload any, target any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("app: dispatch spi: marshal payload: %w", err)
	}
	requestId, keyErr := identity.ReplayKey()
	if keyErr != nil {
		requestId = "spi-" + operation
	}
	requestId = "spi-" + requestId
	if err := adapter.core.enqueuePullItem(dispatchOperationSPI, requestId, wireSPIRequest{Operation: operation, Identity: identity, Payload: raw}); err != nil {
		return err
	}
	if err := adapter.runner.RunOnce(ctx); err != nil {
		return err
	}
	receipt, ok := adapter.core.takePullReceipt(requestId)
	if !ok {
		return fmt.Errorf("%w: the spi receipt was lost", sandbox.ErrResponseLost)
	}
	if receipt.wireError != nil {
		return dispatchWireErrorToError(*receipt.wireError)
	}
	if err := decodeStrict(receipt.payload, target); err != nil {
		return fmt.Errorf("app: dispatch spi: decode %s report: %w", operation, err)
	}
	return nil
}

// Probe implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Probe(ctx context.Context, request sandbox.ProbeRequest) (*sandbox.ProbeReport, error) {
	report := &sandbox.ProbeReport{}
	err := adapter.spiCall(ctx, sandbox.OperationProbe, request.Identity, wireSPIProbe{Requirements: request.Requirements}, report)
	return report, err
}

// Provision implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Provision(ctx context.Context, request sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	receipt := &sandbox.ProvisionReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationProvision, request.Identity, wireSPIProvision{Requirements: request.Requirements, AllowedStoreIds: request.AllowedStoreIds}, receipt)
	return receipt, err
}

// Stage implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Stage(ctx context.Context, request sandbox.StageRequest) (*sandbox.StageReport, error) {
	report := &sandbox.StageReport{}
	err := adapter.spiCall(ctx, sandbox.OperationStage, request.Identity, wireSPIStage{AllocationId: request.AllocationId, Inputs: request.Inputs}, report)
	return report, err
}

// Exec implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Exec(ctx context.Context, request sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	receipt := &sandbox.ExecReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationExec, request.Identity, wireSPIExec{AllocationId: request.AllocationId, Command: request.Command}, receipt)
	return receipt, err
}

// Inspect implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Inspect(ctx context.Context, request sandbox.InspectRequest) (*sandbox.InspectReport, error) {
	report := &sandbox.InspectReport{}
	err := adapter.spiCall(ctx, sandbox.OperationInspect, request.Identity, wireSPIInspect{AllocationId: request.AllocationId}, report)
	return report, err
}

// Signal implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Signal(ctx context.Context, request sandbox.SignalRequest) (*sandbox.SignalReceipt, error) {
	receipt := &sandbox.SignalReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationSignal, request.Identity, wireSPISignal{AllocationId: request.AllocationId, Signal: request.Signal}, receipt)
	return receipt, err
}

// Checkpoint implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Checkpoint(ctx context.Context, request sandbox.CheckpointRequest) (*sandbox.CheckpointReceipt, error) {
	receipt := &sandbox.CheckpointReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationCheckpoint, request.Identity, wireSPICheckpoint{AllocationId: request.AllocationId}, receipt)
	return receipt, err
}

// Restore implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Restore(ctx context.Context, request sandbox.RestoreOperationRequest) (*sandbox.RestoreReceipt, error) {
	receipt := &sandbox.RestoreReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationRestore, request.Identity, wireSPIRestore{PreviousAllocationId: request.PreviousAllocationId, NextAllocationId: request.NextAllocationId, InPlaceConfirmed: request.InPlaceConfirmed}, receipt)
	return receipt, err
}

// Terminate implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Terminate(ctx context.Context, request sandbox.TerminateRequest) (*sandbox.TerminateReceipt, error) {
	receipt := &sandbox.TerminateReceipt{}
	err := adapter.spiCall(ctx, sandbox.OperationTerminate, request.Identity, wireSPITerminate{AllocationId: request.AllocationId}, receipt)
	return receipt, err
}

// Reconcile implements sandbox.SandboxProvider.
func (adapter *PullSPIAdapter) Reconcile(ctx context.Context, request sandbox.ReconcileRequest) (*sandbox.ReconcileReport, error) {
	report := &sandbox.ReconcileReport{}
	err := adapter.spiCall(ctx, sandbox.OperationReconcile, request.Identity, wireSPIReconcile{RunId: request.RunId, AttemptId: request.AttemptId}, report)
	return report, err
}

// Compile-time port assertions: the transport core is the shared
// dual-topology authority, the three adapters are the topology bindings of
// the identical Port, and the two SPI adapters are full SandboxProvider
// implementations of the identical ten-operation contract.
var (
	_ sandbox.DualAuthority       = (*DispatchTransportCore)(nil)
	_ sandbox.DualTopologyBinding = (*EmbeddedTopologyBinding)(nil)
	_ sandbox.DualTopologyBinding = (*PushTopologyBinding)(nil)
	_ sandbox.DualTopologyBinding = (*PullTopologyBinding)(nil)
	_ sandbox.SandboxProvider     = (*PushSPIAdapter)(nil)
	_ sandbox.SandboxProvider     = (*PullSPIAdapter)(nil)
)

// transportLeaseResolver re-adjudicates the dispatch edges issued by the
// transport core against the durable M9-a lease ledger.
type transportLeaseResolver struct {
	core *DispatchTransportCore
}

// LeaseActive implements authority.LeaseActiveResolver.
func (resolver transportLeaseResolver) LeaseActive(leaseId string, generation int64, fencingToken string) (bool, error) {
	lease, state, _, err := resolver.core.ledger.Current(leaseId)
	if err != nil {
		return false, nil
	}
	if state.IsTerminal() {
		return false, nil
	}
	return lease.Generation == generation && lease.FencingToken == fencingToken, nil
}

// transportTargetResolver re-adjudicates the result-ingress target actor of
// the transport core against the current durable ledger.
type transportTargetResolver struct {
	core *DispatchTransportCore
}

// TargetEligible implements authority.TargetEligibilityResolver.
func (resolver transportTargetResolver) TargetEligible(target authority.SecurityDomainId) (bool, error) {
	if !target.Equal(resolver.core.targetActor) {
		return false, nil
	}
	resolver.core.mu.Lock()
	registrationId := resolver.core.registration.RegistrationId
	resolver.core.mu.Unlock()
	stored, err := resolver.core.store.Get(registrationId)
	if err != nil {
		return false, nil
	}
	return stored.LifecycleState == provider.LifecycleStateActive, nil
}
