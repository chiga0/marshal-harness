package authorityprovider

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// LaunchStatus is the externally observable state of one launch transaction.
// A production provider must persist the corresponding record; this in-memory
// reducer never treats a missing record as success and is not restart durable.
type LaunchStatus string

const (
	LaunchPending  LaunchStatus = "pending"
	LaunchReleased LaunchStatus = "released"
	LaunchAborted  LaunchStatus = "aborted"
	LaunchExited   LaunchStatus = "exited"
)

// LaunchTransaction contains only non-secret identity and digest material.
// The coordinator deliberately does not retain received files or credentials.
type LaunchTransaction struct {
	LaunchTransactionID               string       `json:"launchTransactionId"`
	TaskID                            string       `json:"taskId"`
	RunID                             string       `json:"runId"`
	AttemptID                         string       `json:"attemptId"`
	AuthorityNamespaceID              string       `json:"authorityNamespaceId"`
	LaunchNonce                       string       `json:"launchNonce"`
	APAPLaunchRequestDigest           string       `json:"apapLaunchRequestDigest"`
	ProfileRequestDigest              string       `json:"profileRequestDigest"`
	LaunchReceiptDigest               string       `json:"launchReceiptDigest"`
	ReleaseIdentity                   string       `json:"releaseIdentity"`
	CandidateExecutableIdentityDigest string       `json:"candidateExecutableIdentityDigest"`
	AuthorityRootIdentityDigest       string       `json:"authorityRootIdentityDigest"`
	FenceRootIdentityDigest           string       `json:"fenceRootIdentityDigest"`
	WorktreeIdentityDigest            string       `json:"worktreeIdentityDigest"`
	ControlRootIdentityDigest         string       `json:"controlRootIdentityDigest"`
	ControlInputIdentityDigest        string       `json:"controlInputIdentityDigest"`
	ControlOutputIdentityDigest       string       `json:"controlOutputIdentityDigest"`
	MountNamespaceIdentityDigest      string       `json:"mountNamespaceIdentityDigest"`
	Status                            LaunchStatus `json:"status"`
}

// LaunchEffects is the only side-effect boundary. Implementations must be
// outside the consumer and provide the OS-enforced stopped-child, release,
// kill/wait and independent receipt authority. The coordinator itself never
// executes a pathname, reads a credential, or signs a receipt.
type LaunchEffects interface {
	PrepareLaunch(context.Context, PrepareLaunchPayload, []FDRef) (PrepareLaunchSuccessPayload, error)
	CommitLaunch(context.Context, CommitLaunchPayload, LaunchTransaction) (CommitLaunchSuccessPayload, error)
	AbortLaunch(context.Context, AbortLaunchPayload, LaunchTransaction) (AbortLaunchSuccessPayload, error)
	InspectLaunch(context.Context, InspectLaunchPayload, *LaunchTransaction) (InspectLaunchSuccessPayload, error)
}

// LaunchEffectError lets an external authority return a typed safe code while
// keeping implementation details out of the APAP response.
type LaunchEffectError struct {
	Code SafeCode
	Err  error
}

func (e *LaunchEffectError) Error() string {
	if e == nil || e.Err == nil {
		return "authorityprovider: launch effect failed"
	}
	return e.Err.Error()
}

func (e *LaunchEffectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewLaunchEffectError(code SafeCode, err error) error {
	if _, ok := code.Class(); !ok || code == CodeOK {
		code = CodeInternalFailClosed
	}
	return &LaunchEffectError{Code: code, Err: err}
}

// LaunchCoordinator is the deterministic APAP launch transaction reducer.
// It serializes prepare/commit/abort/inspect, binds command replay to the
// request digest and advances the provider sequence only at CommitLaunch's
// release linearization point. It is not itself a production authority.
type LaunchCoordinator struct {
	mu       sync.Mutex
	effects  LaunchEffects
	sequence uint64
	records  map[string]LaunchTransaction
	replay   map[string]launchReplay
}

type launchReplay struct {
	requestDigest string
	peerDigest    string
	peerRole      Principal
	response      APAPResponseEnvelopeV1
}

func NewLaunchCoordinator(effects LaunchEffects, initialSequence uint64) (*LaunchCoordinator, error) {
	if effects == nil {
		return nil, errors.New("launch coordinator effects are unavailable")
	}
	return &LaunchCoordinator{effects: effects, sequence: initialSequence, records: map[string]LaunchTransaction{}, replay: map[string]launchReplay{}}, nil
}

func (coordinator *LaunchCoordinator) ProviderSequence() uint64 {
	if coordinator == nil {
		return 0
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.sequence
}

// HandleControl validates the sealed request, reduces the launch transaction,
// and returns a sealed response. It is suitable for use as the handler behind
// ServeControl after the platform authenticator has supplied the PeerIdentity.
func (coordinator *LaunchCoordinator) HandleControl(ctx context.Context, raw []byte, peer PeerIdentity, now time.Time, fds []FDRef) ([]byte, error) {
	if coordinator == nil || coordinator.effects == nil {
		return nil, errors.New("launch coordinator is unavailable")
	}
	request, err := DecodeControlRequest(raw, peer, now, fds)
	if err != nil {
		return nil, err
	}
	response, err := coordinator.apply(ctx, request, peer, now, fds)
	if err != nil {
		return nil, err
	}
	return SealControlResponse(response)
}

func (coordinator *LaunchCoordinator) apply(ctx context.Context, request APAPRequestEnvelopeV1, peer PeerIdentity, now time.Time, fds []FDRef) (APAPResponseEnvelopeV1, error) {
	if request.Operation != OperationPrepareLaunch && request.Operation != OperationCommitLaunch && request.Operation != OperationAbortLaunch && request.Operation != OperationInspectLaunch {
		return APAPResponseEnvelopeV1{}, protocolError(CodeIdentityMismatch, "launch-operation-required")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return coordinator.failure(request, CodeInternalFailClosed)
	}
	if replay, ok := coordinator.replay[request.CommandID]; ok {
		if replay.requestDigest != request.RequestEnvelopeDigest || replay.peerDigest != peer.PrincipalDigest || replay.peerRole != peer.Role {
			return coordinator.failure(request, CodeIdentityMismatch)
		}
		return replay.response, nil
	}
	if request.ExpectedProviderSequence != nil && *request.ExpectedProviderSequence != coordinator.sequence {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	switch request.Operation {
	case OperationPrepareLaunch:
		return coordinator.prepareLocked(ctx, request, peer, fds)
	case OperationCommitLaunch:
		return coordinator.commitLocked(ctx, request, peer)
	case OperationAbortLaunch:
		return coordinator.abortLocked(ctx, request, peer)
	case OperationInspectLaunch:
		return coordinator.inspectLocked(ctx, request, peer)
	default:
		return APAPResponseEnvelopeV1{}, protocolError(CodeIdentityMismatch, "launch-operation-required")
	}
}

func (coordinator *LaunchCoordinator) prepareLocked(ctx context.Context, request APAPRequestEnvelopeV1, peer PeerIdentity, fds []FDRef) (APAPResponseEnvelopeV1, error) {
	var payload PrepareLaunchPayload
	if err := decodeClosed(request.Payload, &payload); err != nil || !validPrepareLaunchPayload(payload, request) {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	key := launchAttemptKey(payload.AttemptID, payload.LaunchNonce)
	if _, exists := coordinator.records[key]; exists {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	result, err := coordinator.effects.PrepareLaunch(ctx, payload, append([]FDRef(nil), fds...))
	if err != nil {
		return coordinator.cacheFailureLocked(request, peer, launchEffectCode(err, CodeIsolationUnavailable))
	}
	response := coordinator.successResponse(request, coordinator.sequence, result)
	if !validateControlResponsePayload(response, request) {
		return coordinator.cacheFailureLocked(request, peer, CodeLaunchReceiptInvalid)
	}
	for _, prior := range coordinator.records {
		if prior.LaunchTransactionID == result.LaunchTransactionID {
			return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
		}
	}
	coordinator.records[key] = LaunchTransaction{LaunchTransactionID: result.LaunchTransactionID, TaskID: payload.TaskID, RunID: payload.RunID, AttemptID: payload.AttemptID, AuthorityNamespaceID: payload.AuthorityNamespaceID, LaunchNonce: payload.LaunchNonce, APAPLaunchRequestDigest: payload.APAPLaunchRequestDigest, ProfileRequestDigest: payload.ProfileRequestDigest, LaunchReceiptDigest: result.LaunchReceiptDigest, ReleaseIdentity: result.ReleaseIdentity, CandidateExecutableIdentityDigest: payload.CandidateExecutableIdentityDigest, AuthorityRootIdentityDigest: payload.AuthorityRootIdentityDigest, FenceRootIdentityDigest: payload.FenceRootIdentityDigest, WorktreeIdentityDigest: payload.WorktreeIdentityDigest, ControlRootIdentityDigest: payload.ControlRootIdentityDigest, ControlInputIdentityDigest: payload.ControlInputIdentityDigest, ControlOutputIdentityDigest: payload.ControlOutputIdentityDigest, MountNamespaceIdentityDigest: payload.MountNamespaceIdentityDigest, Status: LaunchPending}
	return coordinator.cacheLocked(request, peer, response)
}

func (coordinator *LaunchCoordinator) commitLocked(ctx context.Context, request APAPRequestEnvelopeV1, peer PeerIdentity) (APAPResponseEnvelopeV1, error) {
	var payload CommitLaunchPayload
	if err := decodeClosed(request.Payload, &payload); err != nil || !validID(payload.LaunchTransactionID) || !validDigest(payload.LaunchReceiptDigest) || !validDigest(payload.ReleaseIdentity) || !validDigest(payload.DurableAcceptDigest) {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	transaction, key, ok := coordinator.findTransaction(payload.LaunchTransactionID)
	if !ok || transaction.Status != LaunchPending || transaction.LaunchReceiptDigest != payload.LaunchReceiptDigest || transaction.ReleaseIdentity != payload.ReleaseIdentity {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	result, err := coordinator.effects.CommitLaunch(ctx, payload, transaction)
	if err != nil {
		return coordinator.cacheFailureLocked(request, peer, launchEffectCode(err, CodeLaunchOutcomeAmbiguous))
	}
	if coordinator.sequence == ^uint64(0) {
		return coordinator.cacheFailureLocked(request, peer, CodeInternalFailClosed)
	}
	response := coordinator.successResponse(request, coordinator.sequence+1, result)
	if !validateControlResponsePayload(response, request) {
		return coordinator.cacheFailureLocked(request, peer, CodeLaunchReceiptInvalid)
	}
	transaction.Status = LaunchReleased
	coordinator.records[key] = transaction
	coordinator.sequence++
	return coordinator.cacheLocked(request, peer, response)
}

func (coordinator *LaunchCoordinator) abortLocked(ctx context.Context, request APAPRequestEnvelopeV1, peer PeerIdentity) (APAPResponseEnvelopeV1, error) {
	var payload AbortLaunchPayload
	if err := decodeClosed(request.Payload, &payload); err != nil || !validID(payload.LaunchTransactionID) || !validID(payload.ReasonCode) {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	transaction, key, ok := coordinator.findTransaction(payload.LaunchTransactionID)
	if !ok || transaction.Status != LaunchPending {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	result, err := coordinator.effects.AbortLaunch(ctx, payload, transaction)
	if err != nil {
		return coordinator.cacheFailureLocked(request, peer, launchEffectCode(err, CodeLaunchOutcomeAmbiguous))
	}
	response := coordinator.successResponse(request, coordinator.sequence, result)
	if !validateControlResponsePayload(response, request) {
		return coordinator.cacheFailureLocked(request, peer, CodeLaunchReceiptInvalid)
	}
	transaction.Status = LaunchStatus(result.Status)
	coordinator.records[key] = transaction
	return coordinator.cacheLocked(request, peer, response)
}

func (coordinator *LaunchCoordinator) inspectLocked(ctx context.Context, request APAPRequestEnvelopeV1, peer PeerIdentity) (APAPResponseEnvelopeV1, error) {
	var payload InspectLaunchPayload
	if err := decodeClosed(request.Payload, &payload); err != nil || !validID(payload.AttemptID) || !validNonce(payload.LaunchNonce) || !validDigest(payload.APAPLaunchRequestDigest) || !validDigest(payload.ProfileRequestDigest) {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	transaction, ok := coordinator.records[launchAttemptKey(payload.AttemptID, payload.LaunchNonce)]
	if ok && (transaction.APAPLaunchRequestDigest != payload.APAPLaunchRequestDigest || transaction.ProfileRequestDigest != payload.ProfileRequestDigest) {
		return coordinator.cacheFailureLocked(request, peer, CodeIdentityMismatch)
	}
	var observed *LaunchTransaction
	if ok {
		copy := transaction
		observed = &copy
	}
	result, err := coordinator.effects.InspectLaunch(ctx, payload, observed)
	if err != nil {
		return coordinator.cacheFailureLocked(request, peer, launchEffectCode(err, CodeLaunchOutcomeAmbiguous))
	}
	response := coordinator.successResponse(request, coordinator.sequence, result)
	if !validateControlResponsePayload(response, request) || (ok && result.Status != string(transaction.Status) && result.Status != "released" && result.Status != "aborted" && result.Status != "exited") {
		return coordinator.cacheFailureLocked(request, peer, CodeLaunchReceiptInvalid)
	}
	if ok && result.Status != string(transaction.Status) {
		transaction.Status = LaunchStatus(result.Status)
		coordinator.records[launchAttemptKey(payload.AttemptID, payload.LaunchNonce)] = transaction
	}
	return coordinator.cacheLocked(request, peer, response)
}

func (coordinator *LaunchCoordinator) findTransaction(id string) (LaunchTransaction, string, bool) {
	for key, transaction := range coordinator.records {
		if transaction.LaunchTransactionID == id {
			return transaction, key, true
		}
	}
	return LaunchTransaction{}, "", false
}

func launchAttemptKey(attempt, nonce string) string { return attempt + "\x00" + nonce }

func launchEffectCode(err error, fallback SafeCode) SafeCode {
	var typed *LaunchEffectError
	if errors.As(err, &typed) {
		if _, ok := typed.Code.Class(); ok && typed.Code != CodeOK {
			return typed.Code
		}
	}
	return fallback
}

func (coordinator *LaunchCoordinator) successResponse(request APAPRequestEnvelopeV1, sequence uint64, payload any) APAPResponseEnvelopeV1 {
	raw, _ := marshalCanonical(payload)
	return APAPResponseEnvelopeV1{SchemaVersion: ResponseSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: sequence, SafeCode: CodeOK, SafeMessage: SafeMessageFor(CodeOK), Payload: raw}
}

func (coordinator *LaunchCoordinator) failure(request APAPRequestEnvelopeV1, code SafeCode) (APAPResponseEnvelopeV1, error) {
	if _, ok := code.Class(); !ok || code == CodeOK {
		code = CodeInternalFailClosed
	}
	return APAPResponseEnvelopeV1{SchemaVersion: ResponseSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: coordinator.sequence, SafeCode: code, SafeMessage: SafeMessageFor(code), Payload: json.RawMessage(`null`)}, nil
}

func (coordinator *LaunchCoordinator) cacheLocked(request APAPRequestEnvelopeV1, peer PeerIdentity, response APAPResponseEnvelopeV1) (APAPResponseEnvelopeV1, error) {
	sealed, err := SealControlResponse(response)
	if err != nil {
		return APAPResponseEnvelopeV1{}, err
	}
	var decoded APAPResponseEnvelopeV1
	if err := json.Unmarshal(sealed, &decoded); err != nil {
		return APAPResponseEnvelopeV1{}, err
	}
	coordinator.replay[request.CommandID] = launchReplay{requestDigest: request.RequestEnvelopeDigest, peerDigest: peer.PrincipalDigest, peerRole: peer.Role, response: decoded}
	return decoded, nil
}

func (coordinator *LaunchCoordinator) cacheFailureLocked(request APAPRequestEnvelopeV1, peer PeerIdentity, code SafeCode) (APAPResponseEnvelopeV1, error) {
	response, err := coordinator.failure(request, code)
	if err != nil {
		return response, err
	}
	return coordinator.cacheLocked(request, peer, response)
}
