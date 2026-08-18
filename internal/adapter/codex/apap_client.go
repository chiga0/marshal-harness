package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

type codexAPAPReplay struct {
	requestDigest  string
	responseDigest string
}

type codexAPAPLaunchReplay struct {
	nonce         string
	receiptDigest string
}

// CodexAPAPProfileBridge consumes only the shared Describe/BeginProbe core
// and produces a receipt-bound prerequisite for the separately implemented
// ADR 0037 launch barrier. It is not an Adapter constructor or registry seam.
type CodexAPAPProfileBridge struct {
	authority  CodexAPAPAuthority
	now        func() time.Time
	nonceFence *HostAttestationNonceFence

	mu       sync.Mutex
	requests map[string]codexAPAPCurrent
	replay   map[string]codexAPAPReplay
	sessions map[string]string
	launches map[string]codexAPAPLaunchReplay
}

func NewCodexAPAPProfileBridge(authority CodexAPAPAuthority) (*CodexAPAPProfileBridge, error) {
	return newCodexAPAPProfileBridge(authority, func() time.Time { return time.Now().UTC() })
}

func newCodexAPAPProfileBridge(authority CodexAPAPAuthority, now func() time.Time) (*CodexAPAPProfileBridge, error) {
	if now == nil || authority.Source == nil || authority.ResponseKeys == nil || authority.LaunchKeys == nil || authority.CandidateExecutable.Version != codexAPAPVersion || authority.CandidateExecutable.Validate() != nil || authority.ProviderSequence > maxSafeGeneration || !validID(authority.ProviderInstanceID) || authority.Peer.Role != authorityprovider.PrincipalVerifierController || !validDigest(authority.Peer.PrincipalDigest) {
		return nil, errors.New("codex APAP bridge authority is invalid")
	}
	return &CodexAPAPProfileBridge{authority: authority, now: now, nonceFence: NewHostAttestationNonceFence(), requests: map[string]codexAPAPCurrent{}, replay: map[string]codexAPAPReplay{}, sessions: map[string]string{}, launches: map[string]codexAPAPLaunchReplay{}}, nil
}

func (bridge *CodexAPAPProfileBridge) DescribeRequest(ctx context.Context, requestID, commandID, nonce string, issuedAt, expiresAt time.Time) (authorityprovider.APAPRequestEnvelopeV1, []byte, error) {
	return bridge.newRequest(ctx, authorityprovider.OperationDescribe, requestID, commandID, nonce, issuedAt, expiresAt, nil)
}

func (bridge *CodexAPAPProfileBridge) BeginProbeRequest(ctx context.Context, input CodexAPAPBeginInput) (authorityprovider.APAPRequestEnvelopeV1, []byte, []authorityprovider.FDRef, error) {
	request, raw, err := bridge.newRequest(ctx, authorityprovider.OperationBeginProbe, input.RequestID, input.CommandID, input.Nonce, input.IssuedAt, input.ExpiresAt, &input)
	if err != nil {
		return request, nil, nil, err
	}
	refs := []authorityprovider.FDRef{{Role: authorityprovider.FDCandidateExecutable}, {Role: authorityprovider.FDScratchRoot}, {Role: authorityprovider.FDBusinessDenyRoot, Index: 0}}
	return request, raw, refs, nil
}

func (bridge *CodexAPAPProfileBridge) newRequest(ctx context.Context, operation authorityprovider.Operation, requestID, commandID, nonce string, issuedAt, expiresAt time.Time, begin *CodexAPAPBeginInput) (authorityprovider.APAPRequestEnvelopeV1, []byte, error) {
	if bridge == nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, errors.New("codex APAP bridge is unavailable")
	}
	current, err := loadCodexAPAPCurrent(ctx, bridge.authority, bridge.now().UTC(), bridge.nonceFence)
	if err != nil {
		return authorityprovider.APAPRequestEnvelopeV1{}, nil, err
	}
	payload := json.RawMessage(`{}`)
	var expected *uint64
	if operation == authorityprovider.OperationBeginProbe {
		if begin == nil {
			return authorityprovider.APAPRequestEnvelopeV1{}, nil, errors.New("codex APAP BeginProbe input is absent")
		}
		value := bridge.authority.ProviderSequence
		expected = &value
		payload, err = json.Marshal(authorityprovider.BeginProbePayload{CandidateIdentityDigest: codexAPAPIdentityDigest(bridge.authority.CandidateExecutable), SuiteDigest: current.bundle.Evidence.SuiteDigest, ProbeArtifactDigest: current.bundle.Evidence.ProbeArtifactDigest, PolicyDigest: current.bundle.Config.ContractDigest, ChallengeDigest: current.bundle.Evidence.AggregateChallengeDigest, Deadline: expiresAt.UTC()})
		if err != nil {
			return authorityprovider.APAPRequestEnvelopeV1{}, nil, err
		}
	}
	request := authorityprovider.APAPRequestEnvelopeV1{SchemaVersion: authorityprovider.RequestSchema, ProtocolFamily: authorityprovider.ControlFamily, ProtocolVersion: authorityprovider.ProtocolVersion, Audience: authorityprovider.ControlAudience, RequestID: requestID, CommandID: commandID, CallerPrincipalDigest: bridge.authority.Peer.PrincipalDigest, ProviderInstanceID: bridge.authority.ProviderInstanceID, AuthorityProfile: authorityprovider.ProfileCodex, Operation: operation, IssuedAt: issuedAt.UTC(), ExpiresAt: expiresAt.UTC(), Nonce: nonce, ExpectedProviderSequence: expected, Payload: payload}
	raw, err := authorityprovider.SealControlRequest(request)
	if err != nil || json.Unmarshal(raw, &request) != nil {
		return request, nil, errors.New("codex APAP request cannot be sealed")
	}
	var refs []authorityprovider.FDRef
	if operation == authorityprovider.OperationBeginProbe {
		refs = []authorityprovider.FDRef{{Role: authorityprovider.FDCandidateExecutable}, {Role: authorityprovider.FDScratchRoot}, {Role: authorityprovider.FDBusinessDenyRoot, Index: 0}}
	}
	if _, err := authorityprovider.DecodeControlRequest(raw, bridge.authority.Peer, bridge.now().UTC(), refs); err != nil {
		return request, nil, errors.New("codex APAP request is not admitted by the shared core")
	}
	bridge.mu.Lock()
	bridge.requests[request.RequestEnvelopeDigest] = current
	bridge.mu.Unlock()
	return request, raw, nil
}

func (bridge *CodexAPAPProfileBridge) ValidateDescribe(request authorityprovider.APAPRequestEnvelopeV1, signed CodexAPAPSignedResponse) (authorityprovider.DescribeSuccessPayload, error) {
	var payload authorityprovider.DescribeSuccessPayload
	response, _, err := bridge.validateResponse(request, signed)
	if err != nil || request.Operation != authorityprovider.OperationDescribe || json.Unmarshal(response.Payload, &payload) != nil || len(payload.Profiles) != 1 || payload.Profiles[0] != authorityprovider.ProfileCodex {
		return payload, errors.New("codex APAP Describe response is invalid")
	}
	return payload, nil
}

func (bridge *CodexAPAPProfileBridge) ValidateBeginProbe(ctx context.Context, request authorityprovider.APAPRequestEnvelopeV1, signed CodexAPAPSignedResponse) (CodexAPAPProbeSession, error) {
	if bridge == nil || request.Operation != authorityprovider.OperationBeginProbe || request.ExpectedProviderSequence == nil || *request.ExpectedProviderSequence != bridge.authority.ProviderSequence {
		return CodexAPAPProbeSession{}, errors.New("codex APAP BeginProbe request is invalid")
	}
	response, current, err := bridge.validateResponse(request, signed)
	if err != nil {
		return CodexAPAPProbeSession{}, err
	}
	fresh, err := loadCodexAPAPCurrent(ctx, bridge.authority, bridge.now().UTC(), NewHostAttestationNonceFence())
	if err != nil || !equalCodexAPAPCurrent(current, fresh) {
		return CodexAPAPProbeSession{}, errors.New("codex APAP current authority changed before response admission")
	}
	var begin authorityprovider.BeginProbePayload
	var payload authorityprovider.BeginProbeSuccessPayload
	if json.Unmarshal(request.Payload, &begin) != nil || json.Unmarshal(response.Payload, &payload) != nil || begin.CandidateIdentityDigest != codexAPAPIdentityDigest(bridge.authority.CandidateExecutable) || begin.SuiteDigest != current.bundle.Evidence.SuiteDigest || begin.ProbeArtifactDigest != current.bundle.Evidence.ProbeArtifactDigest || begin.PolicyDigest != current.bundle.Config.ContractDigest || begin.ChallengeDigest != current.bundle.Evidence.AggregateChallengeDigest || !begin.Deadline.Equal(request.ExpiresAt) || !payload.ExpiresAt.Equal(request.ExpiresAt) || !payload.ExpiresAt.After(bridge.now().UTC()) {
		return CodexAPAPProbeSession{}, errors.New("codex APAP BeginProbe profile mapping is invalid")
	}
	session := CodexAPAPProbeSession{ProviderInstanceID: bridge.authority.ProviderInstanceID, ProviderSequence: response.ObservedProviderSequence, PeerPrincipalDigest: bridge.authority.Peer.PrincipalDigest, AuthorityProfile: authorityprovider.ProfileCodex, ProbeSessionID: payload.ProbeSessionID, TargetIsolationIdentityDigest: payload.TargetIsolationIdentityDigest, CredentialIngressEndpointIdentityDigest: payload.CredentialIngressEndpointIdentityDigest, CandidateExecutable: bridge.authority.CandidateExecutable, CandidateExecutableIdentityDigest: begin.CandidateIdentityDigest, AuthorityNamespace: current.bundle.Config.AuthorityNamespace, AuthorityGeneration: current.bundle.Config.AuthorityGeneration, TrustRootGeneration: current.bundle.Config.TrustRootGeneration, ConfigDigest: current.material.ConfigEnvelope.PayloadDigest, EvidenceDigest: current.material.EvidenceEnvelope.PayloadDigest, FenceDigest: current.fence, HostIdentityDigest: current.bundle.Evidence.HostIdentityDigest, SuiteDigest: begin.SuiteDigest, ProbeArtifactDigest: begin.ProbeArtifactDigest, ChallengeDigest: begin.ChallengeDigest, ContractDigest: current.bundle.Config.ContractDigest, RequestEnvelopeDigest: request.RequestEnvelopeDigest, ResponseEnvelopeDigest: response.ResponseEnvelopeDigest, IssuedAt: request.IssuedAt, ExpiresAt: payload.ExpiresAt}
	digest, err := canonicalDigest(session)
	if err != nil {
		return CodexAPAPProbeSession{}, err
	}
	bridge.mu.Lock()
	if prior, ok := bridge.sessions[session.ProbeSessionID]; ok && prior != digest {
		bridge.mu.Unlock()
		return CodexAPAPProbeSession{}, errors.New("codex APAP signed session replay conflicts")
	}
	bridge.sessions[session.ProbeSessionID] = digest
	bridge.mu.Unlock()
	return session, nil
}

func (bridge *CodexAPAPProfileBridge) validateResponse(request authorityprovider.APAPRequestEnvelopeV1, signed CodexAPAPSignedResponse) (authorityprovider.APAPResponseEnvelopeV1, codexAPAPCurrent, error) {
	if bridge == nil || request.ProviderInstanceID != bridge.authority.ProviderInstanceID || request.AuthorityProfile != authorityprovider.ProfileCodex || request.CallerPrincipalDigest != bridge.authority.Peer.PrincipalDigest {
		return authorityprovider.APAPResponseEnvelopeV1{}, codexAPAPCurrent{}, errors.New("codex APAP request authority is invalid")
	}
	bridge.mu.Lock()
	current, ok := bridge.requests[request.RequestEnvelopeDigest]
	bridge.mu.Unlock()
	if !ok {
		return authorityprovider.APAPResponseEnvelopeV1{}, current, errors.New("codex APAP request was not produced by this bridge")
	}
	sealed, err := authorityprovider.SealControlRequest(request)
	var admitted authorityprovider.APAPRequestEnvelopeV1
	if err != nil || json.Unmarshal(sealed, &admitted) != nil || admitted.RequestEnvelopeDigest != request.RequestEnvelopeDigest {
		return authorityprovider.APAPResponseEnvelopeV1{}, current, errors.New("codex APAP request digest is invalid")
	}
	if err := authorityprovider.ValidateSignedObject(signed.Document, signed.Signature, codexAPAPResponseDomain, codexAPAPResponseUsage, bridge.authority.ResponseKeys); err != nil {
		return authorityprovider.APAPResponseEnvelopeV1{}, current, errors.New("codex APAP response signature is invalid")
	}
	response, err := authorityprovider.DecodeControlResponse(signed.Document, request, bridge.authority.ProviderSequence)
	if err != nil {
		return response, current, errors.New("codex APAP response envelope is invalid")
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if prior, exists := bridge.replay[request.CommandID]; exists && (prior.requestDigest != request.RequestEnvelopeDigest || prior.responseDigest != response.ResponseEnvelopeDigest) {
		return response, current, errors.New("codex APAP command replay conflicts")
	}
	bridge.replay[request.CommandID] = codexAPAPReplay{request.RequestEnvelopeDigest, response.ResponseEnvelopeDigest}
	return response, current, nil
}

func (bridge *CodexAPAPProfileBridge) BindLaunchReceipt(ctx context.Context, session CodexAPAPProbeSession, request CodexLaunchRequestV1, document []byte, signature authorityprovider.SignedObjectEnvelopeV1) (CodexAPAPLaunchBinding, error) {
	if bridge == nil || session.ProviderInstanceID != bridge.authority.ProviderInstanceID || session.ProviderSequence != bridge.authority.ProviderSequence || session.PeerPrincipalDigest != bridge.authority.Peer.PrincipalDigest || session.AuthorityProfile != authorityprovider.ProfileCodex || session.CandidateExecutable != bridge.authority.CandidateExecutable || session.CandidateExecutable.Version != codexAPAPVersion || !session.ExpiresAt.After(bridge.now().UTC()) {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch session identity is invalid")
	}
	sessionDigest, err := canonicalDigest(session)
	bridge.mu.Lock()
	registered := bridge.sessions[session.ProbeSessionID]
	bridge.mu.Unlock()
	if err != nil || registered != sessionDigest {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch session is not signed-response derived")
	}
	bridge.mu.Lock()
	original, ok := bridge.requests[session.RequestEnvelopeDigest]
	bridge.mu.Unlock()
	if !ok {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch request snapshot is unavailable")
	}
	fresh, err := loadCodexAPAPCurrent(ctx, bridge.authority, bridge.now().UTC(), NewHostAttestationNonceFence())
	if err != nil || !equalCodexAPAPCurrent(original, fresh) || fresh.material.ConfigEnvelope.PayloadDigest != session.ConfigDigest || fresh.material.EvidenceEnvelope.PayloadDigest != session.EvidenceDigest || fresh.fence != session.FenceDigest || fresh.bundle.Config.AuthorityGeneration != session.AuthorityGeneration || fresh.bundle.Config.TrustRootGeneration != session.TrustRootGeneration {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP current authority changed before launch binding")
	}
	requestDigest, err := codexLaunchRequestDigest(request)
	if err != nil || request.AuthorityGeneration != session.AuthorityGeneration || request.TrustRootGeneration != session.TrustRootGeneration || request.ConfigDigest != session.ConfigDigest || request.EvidenceDigest != session.EvidenceDigest || request.FenceDigest != session.FenceDigest {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch request authority differs")
	}
	if err := authorityprovider.ValidateSignedObject(document, signature, codexLaunchDomain, codexLaunchUsage, bridge.authority.LaunchKeys); err != nil {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch receipt signature is invalid")
	}
	receipt, err := decodeCodexLaunchReceipt(document)
	if err != nil {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch receipt is invalid")
	}
	contract, _ := compiledCodexContractBinding()
	if receipt.SchemaVersion != codexLaunchSchema || receipt.AuthorityNamespace != session.AuthorityNamespace || receipt.AuthorityGeneration != session.AuthorityGeneration || receipt.TrustRootGeneration != session.TrustRootGeneration || receipt.TaskID != request.TaskID || receipt.RunID != request.RunID || receipt.AttemptID != request.AttemptID || receipt.LaunchNonce != request.LaunchNonce || receipt.RequestDigest != requestDigest || receipt.LauncherBuildDigest != contract.LauncherBuildDigest || receipt.LaunchKeyID != signature.KeyID || receipt.ConfigDigest != session.ConfigDigest || receipt.EvidenceDigest != session.EvidenceDigest || receipt.FenceDigest != session.FenceDigest || receipt.HostIdentityDigest != session.HostIdentityDigest || receipt.SourceExecutableIdentityDigest != session.CandidateExecutableIdentityDigest || receipt.ArgvDigest != request.ArgvDigest || receipt.EnvironmentDigest != request.EnvironmentDigest || receipt.SealedMemfd.Seals != codexRequiredMemfdSeals || receipt.SealedMemfd.SHA256 != session.CandidateExecutable.SHA256 || receipt.Child.ProcExeSHA256 != session.CandidateExecutable.SHA256 || receipt.SealedMemfd.DeviceMajor != receipt.Child.ProcExeDeviceMajor || receipt.SealedMemfd.DeviceMinor != receipt.Child.ProcExeDeviceMinor || receipt.SealedMemfd.Inode != receipt.Child.ProcExeInode || receipt.SealedMemfd.MountIDUnique != receipt.Child.ProcExeMountIDUnique || receipt.SealedMemfd.Size != receipt.Child.ProcExeSize || receipt.Child.PID == 0 || receipt.Child.PID > 1<<31-1 || receipt.Child.StartTimeTicks == 0 || receipt.Child.PidfdInode == 0 || len(receipt.PhaseDigests) != 4 {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch receipt binding differs")
	}
	for _, digest := range receipt.PhaseDigests {
		if !validDigest(digest) {
			return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch phase digest is invalid")
		}
	}
	requested, err1 := parseAuthorityTime(receipt.RequestedAt)
	execObserved, err2 := parseAuthorityTime(receipt.ExecObservedAt)
	issued, err3 := parseAuthorityTime(receipt.IssuedAt)
	now := bridge.now().UTC()
	if err1 != nil || err2 != nil || err3 != nil || requested.After(execObserved) || execObserved.After(issued) || issued.After(now.Add(time.Second)) || issued.Sub(requested) > 5*time.Second || now.Sub(issued) > 5*time.Second {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch receipt freshness is invalid")
	}
	key := session.AuthorityNamespace + "\x00" + request.AttemptID
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if prior, ok := bridge.launches[key]; ok && (prior.nonce != request.LaunchNonce || prior.receiptDigest != signature.ObjectDigest) {
		return CodexAPAPLaunchBinding{}, errors.New("codex APAP launch nonce replay conflicts")
	}
	bridge.launches[key] = codexAPAPLaunchReplay{request.LaunchNonce, signature.ObjectDigest}
	return CodexAPAPLaunchBinding{Session: session, Request: request, Receipt: receipt, LaunchReceiptDigest: signature.ObjectDigest}, nil
}
