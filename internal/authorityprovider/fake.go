package authorityprovider

import (
	"errors"
	"sync"
	"time"
)

var ErrResponseLost = errors.New("authorityprovider: response lost")

type FaultKind string

const (
	FaultReject          FaultKind = "reject"
	FaultDropResponse    FaultKind = "drop-response"
	FaultAdvanceSequence FaultKind = "advance-sequence"
)

type FaultSpec struct {
	Operation Operation
	CommandID string
	Fault     FaultKind
}

func (s FaultSpec) matches(operation Operation, commandID string) bool {
	return s.Operation == operation && (s.CommandID == "" || s.CommandID == commandID)
}

type replayEntry struct {
	digest   string
	response []byte
}

type FakeProvider struct {
	mu            sync.Mutex
	sequence      uint64
	faults        []FaultSpec
	controlReplay map[string]replayEntry
	ingressReplay map[string]replayEntry
}

func NewFakeProvider(initialSequence uint64) *FakeProvider {
	return &FakeProvider{sequence: initialSequence, controlReplay: map[string]replayEntry{}, ingressReplay: map[string]replayEntry{}}
}
func (f *FakeProvider) WithFaults(faults ...FaultSpec) *FakeProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, faults...)
	return f
}
func (f *FakeProvider) ProviderSequence() uint64 { f.mu.Lock(); defer f.mu.Unlock(); return f.sequence }
func (f *FakeProvider) fault(operation Operation, commandID string) FaultKind {
	for _, spec := range f.faults {
		if spec.matches(operation, commandID) {
			return spec.Fault
		}
	}
	return ""
}

func (f *FakeProvider) HandleControl(raw []byte, peer Principal, now time.Time, fds []FDRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, err := DecodeControlRequest(raw, peer, now, fds)
	if err != nil {
		return nil, err
	}
	if replay, ok := f.controlReplay[request.CommandID]; ok {
		if replay.digest != request.RequestEnvelopeDigest {
			return nil, protocolError(CodeIdentityMismatch, "replay-conflict")
		}
		return append([]byte(nil), replay.response...), nil
	}
	switch f.fault(request.Operation, request.CommandID) {
	case FaultReject:
		return nil, protocolError(CodeInternalFailClosed, "fault-injected")
	case FaultAdvanceSequence:
		f.sequence++
	}
	if err := ValidateExpectedSequence(request, f.sequence); err != nil {
		return nil, err
	}
	if request.Operation == OperationCommitBundleUpdate {
		f.sequence++
	}
	response := APAPResponseEnvelopeV1{SchemaVersion: ResponseSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: f.sequence, SafeCode: CodeOK, SafeMessage: "", Payload: fixedPayload()}
	encoded, err := SealControlResponse(response)
	if err != nil {
		return nil, err
	}
	f.controlReplay[request.CommandID] = replayEntry{digest: request.RequestEnvelopeDigest, response: append([]byte(nil), encoded...)}
	if f.fault(request.Operation, request.CommandID) == FaultDropResponse {
		return nil, ErrResponseLost
	}
	return encoded, nil
}

func (f *FakeProvider) HandleCredentialIngress(raw []byte, peer Principal, now time.Time, fds []FDRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, err := DecodeCredentialIngressRequest(raw, peer, now, fds)
	if err != nil {
		return nil, err
	}
	if replay, ok := f.ingressReplay[request.CommandID]; ok {
		if replay.digest != request.RequestDigest {
			return nil, protocolError(CodeIdentityMismatch, "replay-conflict")
		}
		return append([]byte(nil), replay.response...), nil
	}
	switch f.fault(OperationAttachProbeCredential, request.CommandID) {
	case FaultReject:
		return nil, protocolError(CodeInternalFailClosed, "fault-injected")
	}
	response := CredentialIngressResponseV1{SchemaVersion: IngressResponseSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion, Audience: IngressAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, SafeCode: CodeOK, SafeMessage: "", Payload: fixedPayload()}
	encoded, err := SealCredentialIngressResponse(response)
	if err != nil {
		return nil, err
	}
	f.ingressReplay[request.CommandID] = replayEntry{digest: request.RequestDigest, response: append([]byte(nil), encoded...)}
	if f.fault(OperationAttachProbeCredential, request.CommandID) == FaultDropResponse {
		return nil, ErrResponseLost
	}
	return encoded, nil
}
