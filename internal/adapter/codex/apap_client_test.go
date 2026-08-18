package codex

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

func codexAPAPResponse(t *testing.T, request authorityprovider.APAPRequestEnvelopeV1, sequence uint64, payload any) []byte {
	t.Helper()
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	response := authorityprovider.APAPResponseEnvelopeV1{SchemaVersion: authorityprovider.ResponseSchema, ProtocolFamily: authorityprovider.ControlFamily, ProtocolVersion: authorityprovider.ProtocolVersion, Audience: authorityprovider.ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: sequence, SafeCode: authorityprovider.CodeOK, Payload: payloadRaw}
	raw, err := authorityprovider.SealControlResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func signedCodexResponse(t *testing.T, fixture codexAPAPFixture, raw []byte) CodexAPAPSignedResponse {
	t.Helper()
	return CodexAPAPSignedResponse{Document: raw, Signature: signCodexAPAP(t, raw, codexAPAPResponseDomain, "response-key", fixture.responsePrivate)}
}

func TestCodexAPAPDescribeRequiresSignedCodexOnlyProfile(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	bridge := fixture.bridge(t)
	request, _, err := bridge.DescribeRequest(context.Background(), "describe-1", "describe-command", "nonce-0001", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	valid := codexAPAPResponse(t, request, fixture.authority.ProviderSequence, authorityprovider.DescribeSuccessPayload{ProviderBuildDigest: testDigest("provider-build"), Platform: "linux", Profiles: []authorityprovider.AuthorityProfile{authorityprovider.ProfileCodex}})
	if _, err := bridge.ValidateDescribe(request, signedCodexResponse(t, fixture, valid)); err != nil {
		t.Fatal(err)
	}
	foreignProfile := codexAPAPResponse(t, request, fixture.authority.ProviderSequence, authorityprovider.DescribeSuccessPayload{ProviderBuildDigest: testDigest("provider-build"), Platform: "linux", Profiles: []authorityprovider.AuthorityProfile{authorityprovider.ProfileQoder}})
	if _, err := bridge.ValidateDescribe(request, signedCodexResponse(t, fixture, foreignProfile)); err == nil {
		t.Fatal("wrong Qoder profile accepted")
	}

	var response authorityprovider.APAPResponseEnvelopeV1
	_ = json.Unmarshal(valid, &response)
	response.ProviderInstanceID = "foreign-provider"
	foreignProvider, _ := authorityprovider.SealControlResponse(response)
	if _, err := bridge.ValidateDescribe(request, signedCodexResponse(t, fixture, foreignProvider)); err == nil {
		t.Fatal("foreign provider accepted")
	}
	response.ProviderInstanceID = request.ProviderInstanceID
	response.ObservedProviderSequence++
	wrongSequence, _ := authorityprovider.SealControlResponse(response)
	if _, err := bridge.ValidateDescribe(request, signedCodexResponse(t, fixture, wrongSequence)); err == nil {
		t.Fatal("wrong provider sequence accepted")
	}
	_, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
	foreignSignature := CodexAPAPSignedResponse{Document: valid, Signature: signCodexAPAP(t, valid, codexAPAPResponseDomain, "response-key", foreignPrivate)}
	if _, err := bridge.ValidateDescribe(request, foreignSignature); err == nil {
		t.Fatal("foreign response signer accepted")
	}
	tampered := request
	tampered.Nonce = "nonce-0002"
	if _, err := bridge.ValidateDescribe(tampered, signedCodexResponse(t, fixture, valid)); err == nil {
		t.Fatal("request mutation without digest refresh accepted")
	}
}

func TestCodexAPAPBeginProbeMapsAtomicAuthorityAndRejectsReplay(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	bridge := fixture.bridge(t)
	input := CodexAPAPBeginInput{RequestID: "begin-1", CommandID: "begin-command", Nonce: "nonce-0001", IssuedAt: fixture.now.Add(-time.Second), ExpiresAt: fixture.now.Add(time.Minute)}
	request, raw, refs, err := bridge.BeginProbeRequest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorityprovider.DecodeControlRequest(raw, fixture.authority.Peer, fixture.now, refs); err != nil {
		t.Fatal(err)
	}
	response := codexAPAPResponse(t, request, fixture.authority.ProviderSequence, authorityprovider.BeginProbeSuccessPayload{ProbeSessionID: "probe-session", TargetIsolationIdentityDigest: testDigest("target"), CredentialIngressEndpointIdentityDigest: testDigest("ingress"), ExpiresAt: input.ExpiresAt})
	session, err := bridge.ValidateBeginProbe(request, signedCodexResponse(t, fixture, response))
	if err != nil {
		t.Fatal(err)
	}
	if session.AuthorityProfile != authorityprovider.ProfileCodex || session.ProviderInstanceID != fixture.authority.ProviderInstanceID || session.ProviderSequence != fixture.authority.ProviderSequence || session.PeerPrincipalDigest != fixture.authority.Peer.PrincipalDigest || session.CandidateExecutable.Version != codexAPAPVersion || session.CandidateExecutable != fixture.authority.CandidateExecutable || session.EvidenceDigest == "" || session.ConfigDigest == "" || session.FenceDigest == "" || session.AuthorityGeneration == 0 || session.TrustRootGeneration == 0 {
		t.Fatalf("session mapping drifted: %+v", session)
	}

	conflict := request
	conflict.Nonce = "nonce-0002"
	conflictRaw, _ := authorityprovider.SealControlRequest(conflict)
	_ = json.Unmarshal(conflictRaw, &conflict)
	if _, err := bridge.ValidateBeginProbe(conflict, signedCodexResponse(t, fixture, response)); err == nil {
		t.Fatal("same-command different-request replay accepted")
	}
	wrongPeer := request
	wrongPeer.CallerPrincipalDigest = testDigest("wrong-peer")
	wrongPeerRaw, _ := authorityprovider.SealControlRequest(wrongPeer)
	_ = json.Unmarshal(wrongPeerRaw, &wrongPeer)
	if _, err := bridge.ValidateBeginProbe(wrongPeer, signedCodexResponse(t, fixture, response)); err == nil {
		t.Fatal("foreign peer accepted")
	}
	wrongProfile := request
	wrongProfile.AuthorityProfile = authorityprovider.ProfileQoder
	wrongProfileRaw, _ := authorityprovider.SealControlRequest(wrongProfile)
	_ = json.Unmarshal(wrongProfileRaw, &wrongProfile)
	if _, err := bridge.ValidateBeginProbe(wrongProfile, signedCodexResponse(t, fixture, response)); err == nil {
		t.Fatal("wrong Qoder profile request accepted")
	}
	bridge.now = func() time.Time { return input.ExpiresAt.Add(time.Second) }
	if _, err := bridge.ValidateBeginProbe(request, signedCodexResponse(t, fixture, response)); err == nil {
		t.Fatal("expired probe response accepted")
	}
}

func TestCodexAPAPUnregisteredOperationsRemainFailClosed(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	bridge := fixture.bridge(t)
	request, _, err := bridge.DescribeRequest(context.Background(), "unsupported-1", "unsupported-command", "nonce-0001", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	request.Operation = authorityprovider.OperationPrepareLaunch
	expected := fixture.authority.ProviderSequence
	request.ExpectedProviderSequence = &expected
	raw, _ := authorityprovider.SealControlRequest(request)
	if _, err := authorityprovider.DecodeControlRequest(raw, fixture.authority.Peer, fixture.now, nil); err == nil {
		t.Fatal("unregistered APAP launch operation was widened")
	}
}

func codexAPAPSession(t *testing.T, fixture codexAPAPFixture, bridge *CodexAPAPProfileBridge) CodexAPAPProbeSession {
	t.Helper()
	input := CodexAPAPBeginInput{RequestID: "session-request", CommandID: "session-command", Nonce: "nonce-0001", IssuedAt: fixture.now.Add(-time.Second), ExpiresAt: fixture.now.Add(time.Minute)}
	request, _, _, err := bridge.BeginProbeRequest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	response := codexAPAPResponse(t, request, fixture.authority.ProviderSequence, authorityprovider.BeginProbeSuccessPayload{ProbeSessionID: "session-1", TargetIsolationIdentityDigest: testDigest("target"), CredentialIngressEndpointIdentityDigest: testDigest("ingress"), ExpiresAt: input.ExpiresAt})
	session, err := bridge.ValidateBeginProbe(request, signedCodexResponse(t, fixture, response))
	if err != nil {
		t.Fatal(err)
	}
	return session
}
