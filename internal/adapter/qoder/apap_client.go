package qoder

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

type QoderAPAPBeginInput struct {
	RequestID string
	CommandID string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Held      QoderAPAPHeldProbeBinding
}

type qoderAPAPReplay struct {
	requestDigest  string
	responseDigest string
}

type qoderAPAPIssuedRequest struct {
	requestDigest string
	identity      string
	operation     authorityprovider.Operation
}

type qoderAPAPReceiptReplay struct {
	sessionDigest string
	lastSequence  uint64
	lastDigest    string
	digests       map[uint64]string
	sessionIDs    map[string]struct{}
	capabilityIDs map[string]struct{}
	modelDigest   string
	lastCompleted time.Time
}

// QoderAPAPProfileBridge validates and maps APAP control objects into the
// already-frozen ADR 0034 Qoder object model. It has no registry or launch
// authority and cannot enable NewFromAuthorityConfig.
type QoderAPAPProfileBridge struct {
	authority QoderAPAPAuthority
	identity  CandidateExecutableReceiptIdentity
	now       func() time.Time

	mu            sync.Mutex
	replay        map[string]qoderAPAPReplay
	issued        map[string]qoderAPAPIssuedRequest
	beginBindings map[string]QoderAPAPHeldProbeBinding
	sessions      map[string]string
	receiptReplay map[string]qoderAPAPReceiptReplay
}

func NewQoderAPAPProfileBridge(authority QoderAPAPAuthority) (*QoderAPAPProfileBridge, error) {
	bridge, err := newQoderAPAPProfileBridge(authority, time.Now().UTC())
	if bridge != nil {
		bridge.now = func() time.Time { return time.Now().UTC() }
	}
	return bridge, err
}

func newQoderAPAPProfileBridge(authority QoderAPAPAuthority, now time.Time) (*QoderAPAPProfileBridge, error) {
	identity, trust, err := validateQoderAPAPAuthority(authority, now.UTC())
	if err != nil {
		return nil, err
	}
	_ = trust
	return &QoderAPAPProfileBridge{authority: authority, identity: identity, now: func() time.Time { return now.UTC() }, replay: map[string]qoderAPAPReplay{}, issued: map[string]qoderAPAPIssuedRequest{}, beginBindings: map[string]QoderAPAPHeldProbeBinding{}, sessions: map[string]string{}, receiptReplay: map[string]qoderAPAPReceiptReplay{}}, nil
}

func (bridge *QoderAPAPProfileBridge) DescribeRequest(requestID, commandID, nonce string, issuedAt, expiresAt time.Time) (authorityprovider.APAPRequestEnvelopeV1, []byte, error) {
	if bridge == nil || bridge.validateCurrentAuthority() != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, errors.New("qoder APAP bridge is unavailable")
	}
	request := bridge.request(authorityprovider.OperationDescribe, requestID, commandID, nonce, issuedAt, expiresAt, nil, json.RawMessage(`{}`))
	decoded, raw, err := sealQoderAPAPRequest(request)
	if err != nil {
		return decoded, nil, err
	}
	if err := bridge.registerIssuedRequest(decoded); err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, err
	}
	return decoded, raw, nil
}

func (bridge *QoderAPAPProfileBridge) BeginProbeRequest(input QoderAPAPBeginInput) (authorityprovider.APAPRequestEnvelopeV1, []byte, []authorityprovider.FDRef, error) {
	if bridge == nil || bridge.validateCurrentAuthority() != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, errors.New("qoder APAP bridge is unavailable")
	}
	if err := validateQoderAPAPHeldProbeBinding(input.Held); err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, err
	}
	expected := bridge.authority.ProviderSequence
	payload := authorityprovider.BeginProbePayload{
		CandidateIdentityDigest: qoderAPAPCandidateIdentityDigest(bridge.identity),
		SuiteDigest:             expectedProbeSuiteDigest(),
		ProbeArtifactDigest:     bridge.authority.Evidence.ProbeArtifactDigest,
		PolicyDigest:            bridge.authority.Config.TrustPolicyDigest,
		ChallengeDigest:         bridge.authority.Evidence.ProbeRunChallengeDigest,
		Deadline:                input.ExpiresAt.UTC(),
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, err
	}
	request := bridge.request(authorityprovider.OperationBeginProbe, input.RequestID, input.CommandID, input.Nonce, input.IssuedAt, input.ExpiresAt, &expected, payloadRaw)
	decoded, raw, err := sealQoderAPAPRequest(request)
	if err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, err
	}
	if err := bridge.registerIssuedRequest(decoded); err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, err
	}
	bridge.mu.Lock()
	if prior, exists := bridge.beginBindings[decoded.RequestEnvelopeDigest]; exists && digestRecordWithoutFields(prior) != digestRecordWithoutFields(input.Held) {
		bridge.mu.Unlock()
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, nil, errors.New("qoder APAP request held identity conflicts")
	}
	bridge.beginBindings[decoded.RequestEnvelopeDigest] = cloneQoderAPAPHeldProbeBinding(input.Held)
	bridge.mu.Unlock()
	refs := []authorityprovider.FDRef{{Role: authorityprovider.FDCandidateExecutable}, {Role: authorityprovider.FDScratchRoot}, {Role: authorityprovider.FDBusinessDenyRoot, Index: 0}}
	return decoded, raw, refs, nil
}

func (bridge *QoderAPAPProfileBridge) ValidateDescribe(request authorityprovider.APAPRequestEnvelopeV1, signed QoderAPAPSignedResponse) (authorityprovider.DescribeSuccessPayload, error) {
	var result authorityprovider.DescribeSuccessPayload
	if bridge == nil {
		return result, errors.New("qoder APAP bridge is unavailable")
	}
	response, err := bridge.validateResponse(request, signed, bridge.authority.ProviderSequence)
	if err != nil || request.Operation != authorityprovider.OperationDescribe || json.Unmarshal(response.Payload, &result) != nil {
		return result, errors.New("qoder APAP Describe response is invalid")
	}
	if len(result.Profiles) != 1 || result.Profiles[0] != authorityprovider.ProfileQoder {
		return result, errors.New("qoder APAP provider does not expose the exact Qoder-only profile")
	}
	return result, nil
}

func (bridge *QoderAPAPProfileBridge) ValidateBeginProbe(request authorityprovider.APAPRequestEnvelopeV1, signed QoderAPAPSignedResponse) (QoderAPAPProbeSession, error) {
	if bridge == nil || request.Operation != authorityprovider.OperationBeginProbe || request.ExpectedProviderSequence == nil || *request.ExpectedProviderSequence != bridge.authority.ProviderSequence {
		return QoderAPAPProbeSession{}, errors.New("qoder APAP BeginProbe request is invalid")
	}
	response, err := bridge.validateResponse(request, signed, bridge.authority.ProviderSequence)
	if err != nil {
		return QoderAPAPProbeSession{}, err
	}
	var payload authorityprovider.BeginProbeSuccessPayload
	if err := json.Unmarshal(response.Payload, &payload); err != nil || payload.ExpiresAt.IsZero() || !payload.ExpiresAt.Equal(request.ExpiresAt) || !payload.ExpiresAt.After(bridge.now()) {
		return QoderAPAPProbeSession{}, errors.New("qoder APAP BeginProbe session is invalid")
	}
	bridge.mu.Lock()
	held, heldOK := bridge.beginBindings[request.RequestEnvelopeDigest]
	bridge.mu.Unlock()
	if !heldOK || payload.TargetIsolationIdentityDigest != held.TargetIsolationIdentityDigest || payload.CredentialIngressEndpointIdentityDigest != held.CredentialIngressEndpointIdentityDigest {
		return QoderAPAPProbeSession{}, errors.New("qoder APAP BeginProbe held identity binding is invalid")
	}
	var begin authorityprovider.BeginProbePayload
	if err := json.Unmarshal(request.Payload, &begin); err != nil || begin.CandidateIdentityDigest != qoderAPAPCandidateIdentityDigest(bridge.identity) || begin.SuiteDigest != expectedProbeSuiteDigest() || begin.ProbeArtifactDigest != bridge.authority.Evidence.ProbeArtifactDigest || begin.PolicyDigest != bridge.authority.Config.TrustPolicyDigest || begin.ChallengeDigest != bridge.authority.Evidence.ProbeRunChallengeDigest || !begin.Deadline.Equal(request.ExpiresAt) {
		return QoderAPAPProbeSession{}, errors.New("qoder APAP BeginProbe profile mapping is invalid")
	}
	session := QoderAPAPProbeSession{
		ProviderInstanceID: bridge.authority.ProviderInstanceID, ProviderSequence: response.ObservedProviderSequence,
		PeerPrincipalDigest: bridge.authority.Peer.PrincipalDigest, AuthorityProfile: authorityprovider.ProfileQoder,
		ProbeSessionID: payload.ProbeSessionID, TargetIsolationIdentityDigest: payload.TargetIsolationIdentityDigest,
		CredentialIngressEndpointIdentityDigest: payload.CredentialIngressEndpointIdentityDigest,
		ScratchRootIdentities:                   append([]CandidateRootIdentity(nil), held.ScratchRootIdentities...), CredentialRootIdentity: held.CredentialRootIdentity,
		BusinessRootIdentities: append([]CandidateRootIdentity(nil), held.BusinessRootIdentities...), VariantTopologyDigests: append([]string(nil), held.VariantTopologyDigests...),
		CandidateIdentity: bridge.identity, CandidateIdentityDigest: begin.CandidateIdentityDigest,
		EvidenceDigest: bridge.authority.Evidence.EvidenceDigest, AuthorityGeneration: bridge.authority.Config.AuthorityGeneration,
		HostIdentityDigest:  bridge.authority.Evidence.HostIdentity.RecordDigest,
		ProbeArtifactDigest: begin.ProbeArtifactDigest, ChallengeDigest: begin.ChallengeDigest,
		RequestEnvelopeDigest: request.RequestEnvelopeDigest, ResponseEnvelopeDigest: response.ResponseEnvelopeDigest,
		IssuedAt: request.IssuedAt, ExpiresAt: payload.ExpiresAt,
	}
	bridge.mu.Lock()
	sessionDigest := digestRecordWithoutFields(session)
	if prior, ok := bridge.sessions[session.ProbeSessionID]; ok && prior != sessionDigest {
		bridge.mu.Unlock()
		return QoderAPAPProbeSession{}, errors.New("qoder APAP signed session replay conflicts")
	}
	bridge.sessions[session.ProbeSessionID] = sessionDigest
	bridge.mu.Unlock()
	return session, nil
}

func (bridge *QoderAPAPProfileBridge) BindReceipt(session QoderAPAPProbeSession, document []byte) (QoderAPAPReceiptBinding, error) {
	held := QoderAPAPHeldProbeBinding{ScratchRootIdentities: session.ScratchRootIdentities, CredentialRootIdentity: session.CredentialRootIdentity, BusinessRootIdentities: session.BusinessRootIdentities, VariantTopologyDigests: session.VariantTopologyDigests, TargetIsolationIdentityDigest: session.TargetIsolationIdentityDigest, CredentialIngressEndpointIdentityDigest: session.CredentialIngressEndpointIdentityDigest}
	if err := validateQoderAPAPHeldProbeBinding(held); err != nil {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP session held identity binding is invalid")
	}
	if bridge == nil || session.ProviderInstanceID != bridge.authority.ProviderInstanceID || session.ProviderSequence != bridge.authority.ProviderSequence || session.PeerPrincipalDigest != bridge.authority.Peer.PrincipalDigest || session.AuthorityProfile != authorityprovider.ProfileQoder || session.CandidateIdentity != bridge.identity || session.CandidateIdentityDigest != qoderAPAPCandidateIdentityDigest(bridge.identity) || session.EvidenceDigest != bridge.authority.Evidence.EvidenceDigest || session.AuthorityGeneration != bridge.authority.Config.AuthorityGeneration || session.HostIdentityDigest != bridge.authority.Evidence.HostIdentity.RecordDigest || session.ProbeArtifactDigest != bridge.authority.Evidence.ProbeArtifactDigest || session.ChallengeDigest != bridge.authority.Evidence.ProbeRunChallengeDigest || !validCandidateASCII(session.ProbeSessionID) || !validSHA256Digest(session.TargetIsolationIdentityDigest) || !validSHA256Digest(session.CredentialIngressEndpointIdentityDigest) || !validSHA256Digest(session.RequestEnvelopeDigest) || !validSHA256Digest(session.ResponseEnvelopeDigest) || session.IssuedAt.IsZero() || !session.ExpiresAt.After(session.IssuedAt) || containsDigest(bridge.authority.Config.RevokedEvidenceDigests, session.EvidenceDigest) || !session.ExpiresAt.After(bridge.now()) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP session authority binding is invalid")
	}
	bridge.mu.Lock()
	registeredSession, ok := bridge.sessions[session.ProbeSessionID]
	bridge.mu.Unlock()
	if !ok || registeredSession != digestRecordWithoutFields(session) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP session is not derived from a signed response")
	}
	_, trust, err := validateQoderAPAPAuthority(bridge.authority, bridge.now())
	if err != nil {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP current authority is invalid")
	}
	binding, err := bindQoderAPAPReceipt(session, document, trust, bridge.now())
	if err != nil {
		return QoderAPAPReceiptBinding{}, err
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	sessionDigest := digestRecordWithoutFields(session)
	state, ok := bridge.receiptReplay[session.ProbeSessionID]
	if !ok {
		state = qoderAPAPReceiptReplay{sessionDigest: sessionDigest, digests: map[uint64]string{}, sessionIDs: map[string]struct{}{}, capabilityIDs: map[string]struct{}{}}
	}
	sequence := binding.Receipt.ReceiptSequence
	if state.sessionDigest != sessionDigest {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt session replay conflicts")
	}
	if prior, exists := state.digests[sequence]; exists {
		if prior != binding.ReceiptDigest {
			return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt replay conflicts")
		}
		return binding, nil
	}
	if sequence != state.lastSequence+1 || (sequence == 1 && binding.Receipt.PreviousReceiptDigest != nil) || (sequence > 1 && (binding.Receipt.PreviousReceiptDigest == nil || *binding.Receipt.PreviousReceiptDigest != state.lastDigest)) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt chain is not contiguous")
	}
	nextModelDigest, completed, capabilityID, err := bridge.validateReceiptContract(session, binding.Receipt, state)
	if err != nil {
		return QoderAPAPReceiptBinding{}, err
	}
	state.sessionIDs[binding.Receipt.SessionID] = struct{}{}
	state.capabilityIDs[capabilityID] = struct{}{}
	state.modelDigest = nextModelDigest
	state.lastCompleted = completed
	state.lastSequence, state.lastDigest = sequence, binding.ReceiptDigest
	state.digests[sequence] = binding.ReceiptDigest
	bridge.receiptReplay[session.ProbeSessionID] = state
	return binding, nil
}

func (bridge *QoderAPAPProfileBridge) validateReceiptContract(session QoderAPAPProbeSession, receipt CandidateExecutionReceipt, state qoderAPAPReceiptReplay) (string, time.Time, string, error) {
	index := int(receipt.ReceiptSequence) - 1
	capability, err := credentialCapabilityFromManifest(receipt.InvocationManifest.EnvironmentManifest)
	expected, nextModelDigest, manifestErr := exactExpectedInvocationManifest(index, session.ProbeSessionID, receipt.InvocationManifest, capability, state.modelDigest)
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if err != nil || manifestErr != nil || !candidateManifestsEqual(receipt.InvocationManifest, expected) || startErr != nil || completeErr != nil || (!state.lastCompleted.IsZero() && started.Before(state.lastCompleted)) {
		return "", time.Time{}, "", errors.New("qoder APAP receipt invocation chain is invalid")
	}
	if _, duplicate := state.sessionIDs[receipt.SessionID]; duplicate {
		return "", time.Time{}, "", errors.New("qoder APAP receipt execution session was replayed")
	}
	if _, duplicate := state.capabilityIDs[capability.CapabilityID]; duplicate {
		return "", time.Time{}, "", errors.New("qoder APAP receipt credential capability was replayed")
	}
	osTrust, trustErr := ReplayQoderOSTrustRootLedger(bridge.authority.Current.OSTrustRecords, bridge.authority.Current.OSTrustReceipts, bridge.authority.Current.OSAnchorProviderIdentity, bridge.authority.Current.OSAnchorProviderKeyID, bridge.authority.Current.OSAnchorProviderKeyEpoch, bridge.authority.Current.OSAnchorProviderPublicKey, bridge.now())
	credentialKey, keyOK := findTrustKey(osTrust.ActiveKeys["credential-capability-provider"], capability.ProviderKeyID, capability.ProviderKeyEpoch)
	issuedAt, issuedErr := time.Parse(time.RFC3339Nano, capability.IssuedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, capability.ExpiresAt)
	policy := candidateAuthorityPolicy{credentialProviderKeyID: credentialKey.KeyID, credentialProviderKeyEpoch: credentialKey.KeyEpoch, credentialProviderPublicKey: credentialKey.PublicKey}
	if trustErr != nil || !keyOK || validateCandidateCredentialCapability(capability, session.ProbeSessionID, receipt.VariantID) != nil || verifyCandidateCredentialCapability(capability, policy) != nil || capability.ProviderIdentity != bridge.authority.Current.CredentialProviderIdentity || capability.CapabilityClass != "qoder-live-probe" || capability.PolicyScopeDigest != bridge.authority.Config.TrustPolicyDigest || issuedErr != nil || expiresErr != nil || issuedAt.After(started) || !completed.Before(expiresAt) {
		return "", time.Time{}, "", errors.New("qoder APAP receipt credential authority is invalid")
	}
	if index == 1 || index == 3 {
		if receipt.ModelID == "" || nextModelDigest != digestBytes([]byte(receipt.ModelID)) {
			return "", time.Time{}, "", errors.New("qoder APAP receipt model identity is invalid")
		}
	}
	return nextModelDigest, completed, capability.CapabilityID, nil
}

func (bridge *QoderAPAPProfileBridge) request(operation authorityprovider.Operation, requestID, commandID, nonce string, issuedAt, expiresAt time.Time, expected *uint64, payload json.RawMessage) authorityprovider.APAPRequestEnvelopeV1 {
	return authorityprovider.APAPRequestEnvelopeV1{SchemaVersion: authorityprovider.RequestSchema, ProtocolFamily: authorityprovider.ControlFamily, ProtocolVersion: authorityprovider.ProtocolVersion, Audience: authorityprovider.ControlAudience, RequestID: requestID, CommandID: commandID, CallerPrincipalDigest: bridge.authority.Peer.PrincipalDigest, ProviderInstanceID: bridge.authority.ProviderInstanceID, AuthorityProfile: authorityprovider.ProfileQoder, Operation: operation, IssuedAt: issuedAt.UTC(), ExpiresAt: expiresAt.UTC(), Nonce: nonce, ExpectedProviderSequence: expected, Payload: payload}
}

func sealQoderAPAPRequest(request authorityprovider.APAPRequestEnvelopeV1) (authorityprovider.APAPRequestEnvelopeV1, []byte, error) {
	raw, err := authorityprovider.SealControlRequest(request)
	if err != nil {
		return request, nil, err
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, nil, err
	}
	return request, raw, nil
}

func (bridge *QoderAPAPProfileBridge) validateResponse(request authorityprovider.APAPRequestEnvelopeV1, signed QoderAPAPSignedResponse, sequence uint64) (authorityprovider.APAPResponseEnvelopeV1, error) {
	if bridge == nil || bridge.validateCurrentAuthority() != nil || request.ProviderInstanceID != bridge.authority.ProviderInstanceID || request.AuthorityProfile != authorityprovider.ProfileQoder || request.CallerPrincipalDigest != bridge.authority.Peer.PrincipalDigest {
		return authorityprovider.APAPResponseEnvelopeV1{}, errors.New("qoder APAP request authority is invalid")
	}
	if err := bridge.validateRequest(request); err != nil {
		return authorityprovider.APAPResponseEnvelopeV1{}, err
	}
	if err := authorityprovider.ValidateSignedObject(signed.Document, signed.Signature, qoderAPAPResponseDomain, qoderAPAPResponseUsage, bridge.authority.ResponseKeys); err != nil {
		return authorityprovider.APAPResponseEnvelopeV1{}, errors.New("qoder APAP response signature is invalid")
	}
	binding, err := decodeQoderAPAPResponseBinding(signed.Document)
	if err != nil || binding.RequestEnvelopeDigest != request.RequestEnvelopeDigest {
		return authorityprovider.APAPResponseEnvelopeV1{}, errors.New("qoder APAP response request binding is invalid")
	}
	response, err := authorityprovider.DecodeControlResponse(binding.Response, request, sequence)
	if err != nil {
		return response, errors.New("qoder APAP response envelope is invalid")
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if prior, ok := bridge.replay[request.CommandID]; ok && (prior.requestDigest != request.RequestEnvelopeDigest || prior.responseDigest != response.ResponseEnvelopeDigest) {
		return response, errors.New("qoder APAP command replay conflicts with prior identity")
	}
	bridge.replay[request.CommandID] = qoderAPAPReplay{requestDigest: request.RequestEnvelopeDigest, responseDigest: response.ResponseEnvelopeDigest}
	return response, nil
}

func (bridge *QoderAPAPProfileBridge) validateRequest(request authorityprovider.APAPRequestEnvelopeV1) error {
	bridge.mu.Lock()
	issued, ok := bridge.issued[request.CommandID]
	bridge.mu.Unlock()
	if !ok || issued.requestDigest != request.RequestEnvelopeDigest || issued.operation != request.Operation || issued.identity != digestRecordWithoutFields(request) {
		return errors.New("qoder APAP request was not issued by this bridge")
	}
	raw, err := authorityprovider.SealControlRequest(request)
	if err != nil {
		return errors.New("qoder APAP request cannot be sealed")
	}
	var sealed authorityprovider.APAPRequestEnvelopeV1
	if json.Unmarshal(raw, &sealed) != nil || sealed.RequestEnvelopeDigest != request.RequestEnvelopeDigest {
		return errors.New("qoder APAP request digest is invalid")
	}
	var refs []authorityprovider.FDRef
	if request.Operation == authorityprovider.OperationBeginProbe {
		refs = []authorityprovider.FDRef{{Role: authorityprovider.FDCandidateExecutable}, {Role: authorityprovider.FDScratchRoot}, {Role: authorityprovider.FDBusinessDenyRoot, Index: 0}}
	}
	if _, err := authorityprovider.DecodeControlRequest(raw, bridge.authority.Peer, bridge.now(), refs); err != nil {
		return errors.New("qoder APAP request is not admitted by the shared core")
	}
	return nil
}

func (bridge *QoderAPAPProfileBridge) registerIssuedRequest(request authorityprovider.APAPRequestEnvelopeV1) error {
	issued := qoderAPAPIssuedRequest{requestDigest: request.RequestEnvelopeDigest, identity: digestRecordWithoutFields(request), operation: request.Operation}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if prior, ok := bridge.issued[request.CommandID]; ok {
		if prior != issued {
			return errors.New("qoder APAP command was already issued with another identity")
		}
		return nil
	}
	bridge.issued[request.CommandID] = issued
	return nil
}

func (bridge *QoderAPAPProfileBridge) validateCurrentAuthority() error {
	identity, _, err := validateQoderAPAPAuthority(bridge.authority, bridge.now())
	if err != nil || identity != bridge.identity {
		return errors.New("qoder APAP current authority changed")
	}
	return nil
}
