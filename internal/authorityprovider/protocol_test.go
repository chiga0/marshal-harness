package authorityprovider

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)

func testDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func peer(role Principal, label string) PeerIdentity {
	return PeerIdentity{PrincipalDigest: testDigest(label), Role: role}
}
func defaultPeer(role Principal) PeerIdentity { return peer(role, string(role)) }
func sequence(value uint64) *uint64           { return &value }
func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func validRequest(operation Operation, p PeerIdentity) APAPRequestEnvelopeV1 {
	expected := sequence(7)
	if operation.readOnly() {
		expected = nil
	}
	payload := json.RawMessage(`{}`)
	switch operation {
	case OperationBeginProbe:
		payload = mustJSON(BeginProbePayload{CandidateIdentityDigest: testDigest("candidate"), SuiteDigest: testDigest("suite"), ProbeArtifactDigest: testDigest("artifact"), PolicyDigest: testDigest("policy"), ChallengeDigest: testDigest("challenge"), Deadline: fixtureNow.Add(time.Minute)})
	case OperationStageBundleLeafBatch:
		kind := UpdateEvidence
		if p.Role == PrincipalRotation {
			kind = UpdateRotation
		}
		if p.Role == PrincipalRevocation {
			kind = UpdateRevocation
		}
		payload = mustJSON(StageBundleLeafBatchPayload{BundleTransactionID: "txn-1", UpdateKind: kind, OrderedLeafDescriptors: []BundleLeafDescriptor{{LeafKind: "a", Digest: testDigest("leaf-a"), Size: 10, MediaType: "application/json"}}})
	}
	return APAPRequestEnvelopeV1{SchemaVersion: RequestSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: "request-1", CommandID: "command-1", CallerPrincipalDigest: p.PrincipalDigest, ProviderInstanceID: "provider-1", AuthorityProfile: ProfileQoder, Operation: operation, IssuedAt: fixtureNow.Add(-time.Second), ExpiresAt: fixtureNow.Add(time.Minute), Nonce: "nonce-0001", ExpectedProviderSequence: expected, Payload: payload}
}

func mustSeal(t *testing.T, request APAPRequestEnvelopeV1) []byte {
	t.Helper()
	raw, err := SealControlRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func beginFDs() []FDRef {
	return []FDRef{{Role: FDCandidateExecutable}, {Role: FDScratchRoot}, {Role: FDBusinessDenyRoot, Index: 0}}
}
func validFDs(operation Operation) []FDRef {
	if operation == OperationBeginProbe {
		return beginFDs()
	}
	if operation == OperationStageBundleLeafBatch {
		return []FDRef{{Role: FDBundleLeaf}}
	}
	return nil
}

func TestRegisteredOperationAuthorizationAndUnsupportedSurface(t *testing.T) {
	for operation, principals := range operationPrincipals {
		for role := range principals {
			p := defaultPeer(role)
			if _, err := DecodeControlRequest(mustSeal(t, validRequest(operation, p)), p, fixtureNow, validFDs(operation)); err != nil {
				t.Fatalf("%s/%s: %v", operation, role, err)
			}
		}
	}
	for _, operation := range []Operation{OperationPrepareLaunch, OperationCommitBundleUpdate, OperationWatchEpoch, OperationRunProbeVariant} {
		p := defaultPeer(PrincipalConsumer)
		request := validRequest(OperationDescribe, p)
		request.Operation = operation
		request.ExpectedProviderSequence = sequence(7)
		if _, err := DecodeControlRequest(mustSeal(t, request), p, fixtureNow, nil); err == nil {
			t.Fatalf("unimplemented %s registered", operation)
		}
	}
}

func TestFDRoleExactValidation(t *testing.T) {
	launch := []FDRef{{Role: FDCandidateExecutable}, {Role: FDAuthorityRoot}, {Role: FDFenceRoot}, {Role: FDWorktree}, {Role: FDControlRoot}, {Role: FDControlInput}, {Role: FDControlOutput}, {Role: FDMountNamespace}}
	if err := ValidateControlFDRoles(OperationPrepareLaunch, launch); err != nil {
		t.Fatal(err)
	}
	if err := ValidateControlFDRoles(OperationPrepareLaunch, launch[:7]); err == nil {
		t.Fatal("short launch fd table accepted")
	}
	if err := ValidateControlFDRoles(OperationBeginProbe, []FDRef{{Role: FDCandidateExecutable}, {Role: FDScratchRoot}, {Role: FDBusinessDenyRoot, Index: 1}}); err == nil {
		t.Fatal("business deny index gap accepted")
	}
	leaves := make([]FDRef, 25)
	for i := range leaves {
		leaves[i] = FDRef{Role: FDBundleLeaf, Index: i}
	}
	if err := ValidateControlFDRoles(OperationStageBundleLeafBatch, leaves); err == nil {
		t.Fatal("oversized leaf table accepted")
	}
	if err := ValidateControlFDRoles(OperationDescribe, []FDRef{{Role: FDCredentialCapability}}); err == nil || ErrorCode(err) != CodeSecretBoundaryViolation {
		t.Fatal("credential fd crossed control boundary")
	}
}

func TestControlRequestTypedClosedNegativeVectors(t *testing.T) {
	p := defaultPeer(PrincipalVerifierController)
	base := validRequest(OperationBeginProbe, p)
	tests := []struct {
		name   string
		mutate func(*APAPRequestEnvelopeV1)
		raw    func([]byte) []byte
		peer   PeerIdentity
		fds    []FDRef
	}{
		{name: "peer digest mismatch", peer: peer(PrincipalVerifierController, "other"), fds: beginFDs()},
		{name: "peer digest malformed", peer: PeerIdentity{Role: PrincipalVerifierController, PrincipalDigest: "sha256:no"}, fds: beginFDs()},
		{name: "wrong role", peer: PeerIdentity{Role: PrincipalConsumer, PrincipalDigest: p.PrincipalDigest}, fds: beginFDs()},
		{name: "digest shape", peer: p, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) {
			var v BeginProbePayload
			_ = json.Unmarshal(r.Payload, &v)
			v.SuiteDigest = "sha256:bad"
			r.Payload = mustJSON(v)
		}},
		{name: "deadline cross bind", peer: p, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) {
			var v BeginProbePayload
			_ = json.Unmarshal(r.Payload, &v)
			v.Deadline = r.ExpiresAt.Add(time.Second)
			r.Payload = mustJSON(v)
		}},
		{name: "payload null", peer: p, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.Payload = json.RawMessage(`null`) }},
		{name: "wrong json type", peer: p, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.Payload = json.RawMessage(`{"candidateIdentityDigest":7}`) }},
		{name: "unknown payload field", peer: p, fds: beginFDs(), raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"suiteDigest":"`), []byte(`"alien":true,"suiteDigest":"`), 1)
		}},
		{name: "duplicate payload field", peer: p, fds: beginFDs(), raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"suiteDigest":"`), []byte(`"suiteDigest":"x","suiteDigest":"`), 1)
		}},
		{name: "fd missing", peer: p, fds: beginFDs()[:2]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Payload = append(json.RawMessage(nil), base.Payload...)
			if test.mutate != nil {
				test.mutate(&request)
			}
			raw := mustSeal(t, request)
			if test.raw != nil {
				raw = test.raw(raw)
			}
			if _, err := DecodeControlRequest(raw, test.peer, fixtureNow, test.fds); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestDescribeRejectsNullPayload(t *testing.T) {
	p := defaultPeer(PrincipalConsumer)
	request := validRequest(OperationDescribe, p)
	request.Payload = json.RawMessage(`null`)
	if _, err := DecodeControlRequest(mustSeal(t, request), p, fixtureNow, nil); err == nil {
		t.Fatal("null Describe payload accepted")
	}
}

func TestStagePayloadCardinalityOrderAndRoleBinding(t *testing.T) {
	p := defaultPeer(PrincipalEvidenceConfig)
	base := validRequest(OperationStageBundleLeafBatch, p)
	mutations := map[string]func(*StageBundleLeafBatchPayload){
		"null array":      func(v *StageBundleLeafBatchPayload) { v.OrderedLeafDescriptors = nil },
		"wrong role kind": func(v *StageBundleLeafBatchPayload) { v.UpdateKind = UpdateRotation },
		"bad size":        func(v *StageBundleLeafBatchPayload) { v.OrderedLeafDescriptors[0].Size = 0 },
		"bad media":       func(v *StageBundleLeafBatchPayload) { v.OrderedLeafDescriptors[0].MediaType = "text/plain" },
		"bad digest":      func(v *StageBundleLeafBatchPayload) { v.OrderedLeafDescriptors[0].Digest = "sha256:BAD" },
		"unordered": func(v *StageBundleLeafBatchPayload) {
			v.OrderedLeafDescriptors = append([]BundleLeafDescriptor{{LeafKind: "z", Digest: testDigest("z"), Size: 1, MediaType: "application/json"}}, v.OrderedLeafDescriptors...)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request := base
			var payload StageBundleLeafBatchPayload
			_ = json.Unmarshal(base.Payload, &payload)
			mutate(&payload)
			request.Payload = mustJSON(payload)
			fds := make([]FDRef, len(payload.OrderedLeafDescriptors))
			for i := range fds {
				fds[i] = FDRef{Role: FDBundleLeaf, Index: i}
			}
			if _, err := DecodeControlRequest(mustSeal(t, request), p, fixtureNow, fds); err == nil {
				t.Fatal("invalid stage accepted")
			}
		})
	}
	request := base
	var payload StageBundleLeafBatchPayload
	_ = json.Unmarshal(base.Payload, &payload)
	payload.OrderedLeafDescriptors = append(payload.OrderedLeafDescriptors, BundleLeafDescriptor{LeafKind: "b", Digest: testDigest("leaf-b"), Size: 1, MediaType: "application/json"})
	request.Payload = mustJSON(payload)
	if _, err := DecodeControlRequest(mustSeal(t, request), p, fixtureNow, []FDRef{{Role: FDBundleLeaf}}); err == nil {
		t.Fatal("descriptor/fd cardinality mismatch accepted")
	}
}

func TestPeerBoundReplayAndSequence(t *testing.T) {
	p1, p2 := peer(PrincipalVerifierController, "peer-1"), peer(PrincipalVerifierController, "peer-2")
	request := validRequest(OperationBeginProbe, p1)
	raw := mustSeal(t, request)
	fake := NewFakeProvider(7)
	first, err := fake.HandleControl(raw, p1, fixtureNow, beginFDs())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fake.HandleControl(raw, p1, fixtureNow, beginFDs())
	if err != nil || !bytes.Equal(first, replay) {
		t.Fatalf("exact replay: %v", err)
	}
	if _, err := fake.HandleControl(raw, p2, fixtureNow, beginFDs()); err == nil {
		t.Fatal("cross-peer raw replay accepted")
	}
	other := request
	other.CallerPrincipalDigest = p2.PrincipalDigest
	other.Nonce = "nonce-0002"
	if _, err := fake.HandleControl(mustSeal(t, other), p2, fixtureNow, beginFDs()); err == nil {
		t.Fatal("same-role different-peer command replay accepted")
	}
	stale := validRequest(OperationBeginProbe, p1)
	stale.CommandID = "stale-command"
	stale.ExpectedProviderSequence = sequence(6)
	if _, err := fake.HandleControl(mustSeal(t, stale), p1, fixtureNow, beginFDs()); err == nil {
		t.Fatal("stale sequence accepted")
	}
}

func TestControlResponseClosedTypedAndSequenceBound(t *testing.T) {
	p := defaultPeer(PrincipalVerifierController)
	request := validRequest(OperationBeginProbe, p)
	raw := mustSeal(t, request)
	decoded, err := DecodeControlRequest(raw, p, fixtureNow, beginFDs())
	if err != nil {
		t.Fatal(err)
	}
	responseRaw, err := NewFakeProvider(7).HandleControl(raw, p, fixtureNow, beginFDs())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlResponse(responseRaw, decoded, 7); err != nil {
		t.Fatal(err)
	}
	var response APAPResponseEnvelopeV1
	_ = json.Unmarshal(responseRaw, &response)
	tests := []struct {
		name   string
		mutate func(*APAPResponseEnvelopeV1)
		raw    func([]byte) []byte
	}{
		{name: "wrong sequence", mutate: func(r *APAPResponseEnvelopeV1) { r.ObservedProviderSequence++ }},
		{name: "sequence wrong type", raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"observedProviderSequence":7`), []byte(`"observedProviderSequence":"7"`), 1)
		}},
		{name: "wrong operation", mutate: func(r *APAPResponseEnvelopeV1) { r.Operation = OperationDescribe }},
		{name: "unknown safe code", mutate: func(r *APAPResponseEnvelopeV1) { r.SafeCode = "unknown" }},
		{name: "success null", mutate: func(r *APAPResponseEnvelopeV1) { r.Payload = json.RawMessage(`null`) }},
		{name: "payload wrong type", mutate: func(r *APAPResponseEnvelopeV1) { r.Payload = json.RawMessage(`{"probeSessionId":7}`) }},
		{name: "payload unknown", mutate: func(r *APAPResponseEnvelopeV1) {
			r.Payload = json.RawMessage(`{"probeSessionId":"x","targetIsolationIdentityDigest":"x","credentialIngressEndpointIdentityDigest":"x","expiresAt":"2026-08-18T08:01:00Z","alien":true}`)
		}},
		{name: "failure success payload", mutate: func(r *APAPResponseEnvelopeV1) {
			r.SafeCode = CodeProviderBusy
			r.SafeMessage = SafeMessageFor(r.SafeCode)
		}},
		{name: "failure raw error", mutate: func(r *APAPResponseEnvelopeV1) {
			r.SafeCode = CodeProviderBusy
			r.SafeMessage = "dial tcp: secret path"
			r.Payload = json.RawMessage(`null`)
		}},
		{name: "duplicate", raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"safeCode":"ok"`), []byte(`"safeCode":"ok","safeCode":"ok"`), 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := response
			encoded := responseRaw
			if test.mutate != nil {
				test.mutate(&candidate)
				encoded, _ = SealControlResponse(candidate)
			}
			if test.raw != nil {
				encoded = test.raw(encoded)
			}
			if _, err := DecodeControlResponse(encoded, decoded, 7); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	failure := response
	failure.SafeCode = CodeProviderBusy
	failure.SafeMessage = SafeMessageFor(failure.SafeCode)
	failure.Payload = json.RawMessage(`null`)
	encoded, _ := SealControlResponse(failure)
	if _, err := DecodeControlResponse(encoded, decoded, 7); err != nil {
		t.Fatalf("closed failure rejected: %v", err)
	}
}

func validIngressRequest(p PeerIdentity) CredentialIngressRequestV1 {
	payload := AttachProbeCredentialPayload{ProbeSessionID: "probe-1", CapabilityIdentityDigest: testDigest("cap"), CapabilityPolicyDigest: testDigest("policy"), ServiceIdentityDigest: testDigest("service"), CapabilityExpiresAt: fixtureNow.Add(time.Minute), DeliveryNonce: "delivery-0001", TargetIsolationIdentityDigest: testDigest("target")}
	return CredentialIngressRequestV1{SchemaVersion: IngressRequestSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion, Audience: IngressAudience, RequestID: "request-1", CommandID: "command-1", SecretProviderPrincipalDigest: p.PrincipalDigest, ProviderInstanceID: "provider-1", AuthorityProfile: ProfileQoder, ProbeSessionID: "probe-1", TargetIsolationIdentityDigest: testDigest("target"), CredentialIngressEndpointIdentityDigest: testDigest("endpoint"), CredentialIngressTicketDigest: testDigest("ticket"), IssuedAt: fixtureNow.Add(-time.Second), ExpiresAt: fixtureNow.Add(time.Minute), Nonce: "nonce-0001", Payload: mustJSON(payload)}
}

func TestCredentialIngressPeerReplayAndClosedResponse(t *testing.T) {
	p1, p2 := peer(PrincipalSecretProvider, "secret-1"), peer(PrincipalSecretProvider, "secret-2")
	request := validIngressRequest(p1)
	raw, _ := SealCredentialIngressRequest(request)
	fd := []FDRef{{Role: FDCredentialCapability}}
	if _, err := DecodeCredentialIngressRequest(raw, p1, fixtureNow, fd); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeProvider(7)
	responseRaw, err := fake.HandleCredentialIngress(raw, p1, fixtureNow, fd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCredentialIngressResponse(responseRaw, request); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.HandleCredentialIngress(raw, p2, fixtureNow, fd); err == nil {
		t.Fatal("cross-peer ingress replay accepted")
	}
	bad := request
	bad.SecretProviderPrincipalDigest = p2.PrincipalDigest
	bad.Nonce = "nonce-0002"
	badRaw, _ := SealCredentialIngressRequest(bad)
	if _, err := fake.HandleCredentialIngress(badRaw, p2, fixtureNow, fd); err == nil {
		t.Fatal("same-role ingress command replay accepted")
	}
	var response CredentialIngressResponseV1
	_ = json.Unmarshal(responseRaw, &response)
	response.SafeCode = CodeProviderBusy
	response.SafeMessage = SafeMessageFor(response.SafeCode)
	encoded, _ := SealCredentialIngressResponse(response)
	if _, err := DecodeCredentialIngressResponse(encoded, request); err == nil {
		t.Fatal("failure with success payload accepted")
	}
	response.Payload = json.RawMessage(`null`)
	encoded, _ = SealCredentialIngressResponse(response)
	if _, err := DecodeCredentialIngressResponse(encoded, request); err != nil {
		t.Fatal(err)
	}
}

func TestSignedObjectStrictBase64URL(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	unsigned := []byte(`{"value":1}`)
	domain, usage := "marshal-test-v1\x00", "test-usage"
	envelope, err := SignObjectForFake(unsigned, domain, "key-1", 3, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyring := StaticKeyring{"key-1:3": {PublicKey: publicKey, Usage: usage}}
	if err := ValidateSignedObject(unsigned, envelope, domain, usage, keyring); err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	index := strings.IndexByte(alphabet, envelope.Signature[len(envelope.Signature)-1])
	malleated := envelope
	malleated.Signature = envelope.Signature[:len(envelope.Signature)-1] + string(alphabet[(index&0x30)|1])
	if _, err := base64.RawURLEncoding.DecodeString(malleated.Signature); err != nil {
		t.Fatalf("vector is not permissively decodable: %v", err)
	}
	if err := ValidateSignedObject(unsigned, malleated, domain, usage, keyring); err == nil {
		t.Fatal("non-canonical base64url accepted")
	}
	bad := envelope
	bad.Signature = strings.Repeat("A", 86)
	if err := ValidateSignedObject(unsigned, bad, domain, usage, keyring); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func TestFakeProviderFaults(t *testing.T) {
	p := defaultPeer(PrincipalVerifierController)
	raw := mustSeal(t, validRequest(OperationBeginProbe, p))
	dropped := NewFakeProvider(7).WithFaults(FaultSpec{Operation: OperationBeginProbe, Fault: FaultDropResponse})
	if _, err := dropped.HandleControl(raw, p, fixtureNow, beginFDs()); !errors.Is(err, ErrResponseLost) {
		t.Fatalf("drop=%v", err)
	}
	if _, err := dropped.HandleControl(raw, p, fixtureNow, beginFDs()); err != nil {
		t.Fatalf("replay=%v", err)
	}
	if _, err := NewFakeProvider(7).WithFaults(FaultSpec{Operation: OperationBeginProbe, Fault: FaultAdvanceSequence}).HandleControl(raw, p, fixtureNow, beginFDs()); err == nil {
		t.Fatal("advance fault accepted")
	}
}
