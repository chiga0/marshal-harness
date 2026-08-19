package authorityprovider

import (
	"encoding/json"
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
	digest     string
	peerDigest string
	peerRole   Principal
	response   []byte
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

func (f *FakeProvider) HandleControl(raw []byte, peer PeerIdentity, now time.Time, fds []FDRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, err := DecodeControlRequest(raw, peer, now, fds)
	if err != nil {
		return nil, err
	}
	if replay, ok := f.controlReplay[request.CommandID]; ok {
		if replay.digest != request.RequestEnvelopeDigest || replay.peerDigest != peer.PrincipalDigest || replay.peerRole != peer.Role {
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
	payload, err := fakeControlPayload(request)
	if err != nil {
		return nil, err
	}
	response := APAPResponseEnvelopeV1{SchemaVersion: ResponseSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: f.sequence, SafeCode: CodeOK, SafeMessage: SafeMessageFor(CodeOK), Payload: payload}
	encoded, err := SealControlResponse(response)
	if err != nil {
		return nil, err
	}
	f.controlReplay[request.CommandID] = replayEntry{digest: request.RequestEnvelopeDigest, peerDigest: peer.PrincipalDigest, peerRole: peer.Role, response: append([]byte(nil), encoded...)}
	if f.fault(request.Operation, request.CommandID) == FaultDropResponse {
		return nil, ErrResponseLost
	}
	return encoded, nil
}

func (f *FakeProvider) HandleCredentialIngress(raw []byte, peer PeerIdentity, now time.Time, fds []FDRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, err := DecodeCredentialIngressRequest(raw, peer, now, fds)
	if err != nil {
		return nil, err
	}
	if replay, ok := f.ingressReplay[request.CommandID]; ok {
		if replay.digest != request.RequestDigest || replay.peerDigest != peer.PrincipalDigest || replay.peerRole != peer.Role {
			return nil, protocolError(CodeIdentityMismatch, "replay-conflict")
		}
		return append([]byte(nil), replay.response...), nil
	}
	switch f.fault(OperationAttachProbeCredential, request.CommandID) {
	case FaultReject:
		return nil, protocolError(CodeInternalFailClosed, "fault-injected")
	}
	payload, err := marshalCanonical(CredentialIngressSuccessPayload{DeliveryReceiptDigest: stableDigest("delivery", request.RequestDigest), InstallReceiptDigest: stableDigest("install", request.RequestDigest)})
	if err != nil {
		return nil, err
	}
	response := CredentialIngressResponseV1{SchemaVersion: IngressResponseSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion, Audience: IngressAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, SafeCode: CodeOK, SafeMessage: SafeMessageFor(CodeOK), Payload: payload}
	encoded, err := SealCredentialIngressResponse(response)
	if err != nil {
		return nil, err
	}
	f.ingressReplay[request.CommandID] = replayEntry{digest: request.RequestDigest, peerDigest: peer.PrincipalDigest, peerRole: peer.Role, response: append([]byte(nil), encoded...)}
	if f.fault(OperationAttachProbeCredential, request.CommandID) == FaultDropResponse {
		return nil, ErrResponseLost
	}
	return encoded, nil
}

func fakeControlPayload(request APAPRequestEnvelopeV1) ([]byte, error) {
	switch request.Operation {
	case OperationDescribe:
		return marshalCanonical(DescribeSuccessPayload{ProviderBuildDigest: stableDigest("fake-build"), Platform: "linux", Profiles: []AuthorityProfile{ProfileCodex, ProfileQoder}})
	case OperationBeginProbe:
		return marshalCanonical(BeginProbeSuccessPayload{ProbeSessionID: "probe-" + request.CommandID, TargetIsolationIdentityDigest: stableDigest("target", request.RequestEnvelopeDigest), CredentialIngressEndpointIdentityDigest: stableDigest("endpoint", request.RequestEnvelopeDigest), ExpiresAt: request.ExpiresAt})
	case OperationStageBundleLeafBatch:
		var staged StageBundleLeafBatchPayload
		if err := decodeClosed(request.Payload, &staged); err != nil {
			return nil, err
		}
		digests := make([]string, len(staged.OrderedLeafDescriptors))
		for i := range staged.OrderedLeafDescriptors {
			digests[i] = staged.OrderedLeafDescriptors[i].Digest
		}
		return marshalCanonical(StageBundleLeafBatchSuccessPayload{BundleTransactionID: staged.BundleTransactionID, StagedLeafDigests: digests, StagingReceiptDigest: stableDigest("staging", request.RequestEnvelopeDigest)})
	case OperationPrepareLaunch:
		var launch PrepareLaunchPayload
		if err := decodeClosed(request.Payload, &launch); err != nil {
			return nil, err
		}
		return marshalCanonical(PrepareLaunchSuccessPayload{
			LaunchTransactionID:     "launch-" + request.CommandID,
			APAPLaunchRequestDigest: launch.APAPLaunchRequestDigest,
			ProfileRequestDigest:    launch.ProfileRequestDigest,
			LaunchReceiptDigest:     stableDigest("launch-receipt", request.RequestEnvelopeDigest),
			LaunchReceipt:           json.RawMessage(`{"status":"prepared"}`),
			ReleaseIdentity:         stableDigest("release-identity", request.RequestEnvelopeDigest),
			Deadline:                request.ExpiresAt,
		})
	case OperationCommitLaunch:
		return marshalCanonical(CommitLaunchSuccessPayload{Status: "released", ReleaseReceiptDigest: stableDigest("release-receipt", request.RequestEnvelopeDigest), ReleaseReceipt: json.RawMessage(`{"status":"released"}`)})
	case OperationAbortLaunch:
		return marshalCanonical(AbortLaunchSuccessPayload{Status: "aborted", AbortReceiptDigest: stableDigest("abort-receipt", request.RequestEnvelopeDigest), AbortReceipt: json.RawMessage(`{"status":"aborted"}`)})
	case OperationInspectLaunch:
		return marshalCanonical(InspectLaunchSuccessPayload{Status: "unknown"})
	default:
		return nil, protocolError(CodeIdentityMismatch, "operation-unsupported")
	}
}
