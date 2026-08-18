package qoder

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

func qoderDescribeResponse(t *testing.T, request authorityprovider.APAPRequestEnvelopeV1, sequence uint64, profiles []authorityprovider.AuthorityProfile) []byte {
	t.Helper()
	payload, err := json.Marshal(authorityprovider.DescribeSuccessPayload{ProviderBuildDigest: digest("b"), Platform: "linux", Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	response := authorityprovider.APAPResponseEnvelopeV1{SchemaVersion: authorityprovider.ResponseSchema, ProtocolFamily: authorityprovider.ControlFamily, ProtocolVersion: authorityprovider.ProtocolVersion, Audience: authorityprovider.ControlAudience, RequestID: request.RequestID, CommandID: request.CommandID, ProviderInstanceID: request.ProviderInstanceID, AuthorityProfile: request.AuthorityProfile, Operation: request.Operation, ObservedProviderSequence: sequence, SafeCode: authorityprovider.CodeOK, Payload: payload}
	raw, err := authorityprovider.SealControlResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestQoderAPAPDescribeRequiresSignedQoderOnlyProfile(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	bridge, err := newQoderAPAPProfileBridge(fixture.authority, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := bridge.DescribeRequest("describe-1", "describe-command", "nonce-0001", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	valid := qoderDescribeResponse(t, request, fixture.authority.ProviderSequence, []authorityprovider.AuthorityProfile{authorityprovider.ProfileQoder})
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, request, valid, fixture.responsePrivate)); err != nil {
		t.Fatal(err)
	}

	foreign := qoderDescribeResponse(t, request, fixture.authority.ProviderSequence, []authorityprovider.AuthorityProfile{authorityprovider.ProfileCodex})
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, request, foreign, fixture.responsePrivate)); err == nil {
		t.Fatal("foreign Codex profile accepted")
	}

	var response authorityprovider.APAPResponseEnvelopeV1
	_ = json.Unmarshal(valid, &response)
	response.ProviderInstanceID = "foreign-provider"
	foreignProvider, _ := authorityprovider.SealControlResponse(response)
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, request, foreignProvider, fixture.responsePrivate)); err == nil {
		t.Fatal("foreign provider accepted")
	}

	response.ProviderInstanceID = request.ProviderInstanceID
	response.ObservedProviderSequence++
	wrongSequence, _ := authorityprovider.SealControlResponse(response)
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, request, wrongSequence, fixture.responsePrivate)); err == nil {
		t.Fatal("wrong provider sequence accepted")
	}

	_, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, request, valid, foreignPrivate)); err == nil {
		t.Fatal("foreign response signer accepted")
	}
	tamperedRequest := request
	tamperedRequest.Nonce = "nonce-0002"
	if _, err := bridge.ValidateDescribe(tamperedRequest, signQoderAPAPResponse(t, request, valid, fixture.responsePrivate)); err == nil {
		t.Fatal("request mutation without digest refresh accepted")
	}
	for name, mutate := range map[string]func(*authorityprovider.APAPRequestEnvelopeV1){
		"nonce with refreshed digest":     func(v *authorityprovider.APAPRequestEnvelopeV1) { v.Nonce = "nonce-0003" },
		"issuedAt with refreshed digest":  func(v *authorityprovider.APAPRequestEnvelopeV1) { v.IssuedAt = v.IssuedAt.Add(time.Millisecond) },
		"expiresAt with refreshed digest": func(v *authorityprovider.APAPRequestEnvelopeV1) { v.ExpiresAt = v.ExpiresAt.Add(time.Millisecond) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			raw, err := authorityprovider.SealControlRequest(changed)
			if err != nil || json.Unmarshal(raw, &changed) != nil {
				t.Fatal("reseal request")
			}
			if _, err := bridge.ValidateDescribe(changed, signQoderAPAPResponse(t, request, valid, fixture.responsePrivate)); err == nil {
				t.Fatal("caller-resealed request accepted")
			}
		})
	}
	other, _, err := bridge.DescribeRequest("describe-2", "describe-command-2", "nonce-0004", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ValidateDescribe(request, signQoderAPAPResponse(t, other, valid, fixture.responsePrivate)); err == nil {
		t.Fatal("trusted response rebound to another issued request accepted")
	}
}

func TestQoderAPAPBeginMapsHeldIdentityEvidenceAndRejectsReplay(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	bridge, err := newQoderAPAPProfileBridge(fixture.authority, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	input := QoderAPAPBeginInput{RequestID: "begin-1", CommandID: "begin-command", Nonce: "nonce-0001", IssuedAt: fixture.now.Add(-time.Second), ExpiresAt: fixture.now.Add(time.Minute)}
	request, raw, refs, err := bridge.BeginProbeRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	provider := authorityprovider.NewFakeProvider(fixture.authority.ProviderSequence)
	responseRaw, err := provider.HandleControl(raw, fixture.authority.Peer, fixture.now, refs)
	if err != nil {
		t.Fatal(err)
	}
	signed := signQoderAPAPResponse(t, request, responseRaw, fixture.responsePrivate)
	session, err := bridge.ValidateBeginProbe(request, signed)
	if err != nil {
		t.Fatal(err)
	}
	if session.AuthorityProfile != authorityprovider.ProfileQoder || session.ProviderInstanceID != fixture.authority.ProviderInstanceID || session.PeerPrincipalDigest != fixture.authority.Peer.PrincipalDigest || session.CandidateIdentity.BinaryVersion != supportedBinary || session.CandidateIdentity != fixture.authority.Evidence.CandidateExecutableIdentity || session.EvidenceDigest != fixture.authority.Config.CurrentEvidenceDigest || session.AuthorityGeneration != fixture.authority.Config.AuthorityGeneration {
		t.Fatalf("session mapping drifted: %+v", session)
	}

	conflict := request
	conflict.Nonce = "nonce-0002"
	conflictRaw, err := authorityprovider.SealControlRequest(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(conflictRaw, &conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ValidateBeginProbe(conflict, signed); err == nil {
		t.Fatal("same-command different-request replay accepted")
	}

	wrongPeer := request
	wrongPeer.CallerPrincipalDigest = digest("c")
	wrongPeerRaw, _ := authorityprovider.SealControlRequest(wrongPeer)
	_ = json.Unmarshal(wrongPeerRaw, &wrongPeer)
	if _, err := bridge.ValidateBeginProbe(wrongPeer, signed); err == nil {
		t.Fatal("foreign peer accepted")
	}

	bridge.now = func() time.Time { return input.ExpiresAt.Add(time.Second) }
	if _, err := bridge.ValidateBeginProbe(request, signed); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestQoderAPAPUnregisteredOperationsRemainFailClosed(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	bridge, err := newQoderAPAPProfileBridge(fixture.authority, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := bridge.DescribeRequest("unsupported-1", "unsupported-command", "nonce-0001", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	request.Operation = authorityprovider.OperationStageBundleLeafBatch
	expected := fixture.authority.ProviderSequence
	request.ExpectedProviderSequence = &expected
	request.Payload = json.RawMessage(`{"bundleTransactionId":"txn","updateKind":"evidence-update","orderedLeafDescriptors":[]}`)
	raw, _ := authorityprovider.SealControlRequest(request)
	if _, err := authorityprovider.DecodeControlRequest(raw, fixture.authority.Peer, fixture.now, nil); err == nil {
		t.Fatal("unregistered APAP operation was widened by Qoder bridge")
	}
}
