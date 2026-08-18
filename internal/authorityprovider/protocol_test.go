package authorityprovider

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)

func sequence(value uint64) *uint64 { return &value }

func validRequest(operation Operation, principal Principal) APAPRequestEnvelopeV1 {
	expected := sequence(7)
	if operation.readOnly() {
		expected = nil
	}
	payload := json.RawMessage(`{}`)
	if operation == OperationDescribe {
		payload = json.RawMessage(`{}`)
	}
	if operation == OperationBeginProbe {
		payload = mustJSON(BeginProbePayload{CandidateIdentityDigest: "sha256:candidate", SuiteDigest: "sha256:suite", ProbeArtifactDigest: "sha256:artifact", PolicyDigest: "sha256:policy", ChallengeDigest: "sha256:challenge", Deadline: fixtureNow.Add(time.Minute)})
	} else if fields, ok := controlPayloadFields[operation]; ok {
		object := make(map[string]any, len(fields))
		for _, field := range fields {
			object[field] = "fixture"
		}
		payload = mustJSON(object)
	}
	return APAPRequestEnvelopeV1{SchemaVersion: RequestSchema, ProtocolFamily: ControlFamily, ProtocolVersion: ProtocolVersion, Audience: ControlAudience, RequestID: "request-1", CommandID: "command-1", CallerPrincipalDigest: string(principal), ProviderInstanceID: "provider-1", AuthorityProfile: ProfileQoder, Operation: operation, IssuedAt: fixtureNow.Add(-time.Second), ExpiresAt: fixtureNow.Add(time.Minute), Nonce: "nonce-0001", ExpectedProviderSequence: expected, Payload: payload}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
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
	switch operation {
	case OperationBeginProbe:
		return beginFDs()
	case OperationStageBundleLeafBatch:
		return []FDRef{{Role: FDBundleLeaf}}
	case OperationPrepareLaunch:
		return []FDRef{{Role: FDCandidateExecutable}, {Role: FDAuthorityRoot}, {Role: FDFenceRoot}, {Role: FDWorktree}, {Role: FDControlRoot}, {Role: FDControlInput}, {Role: FDControlOutput}, {Role: FDMountNamespace}}
	default:
		return nil
	}
}

func TestOperationAuthorizationMatrix(t *testing.T) {
	for operation, principals := range operationPrincipals {
		for principal := range principals {
			name := string(operation) + "/" + string(principal)
			t.Run(name, func(t *testing.T) {
				request := validRequest(operation, principal)
				raw := mustSeal(t, request)
				if _, err := DecodeControlRequest(raw, principal, fixtureNow, validFDs(operation)); err != nil {
					t.Fatal(err)
				}
			})
		}
		t.Run(string(operation)+"/worker-rejected", func(t *testing.T) {
			request := validRequest(operation, PrincipalWorker)
			raw := mustSeal(t, request)
			if _, err := DecodeControlRequest(raw, PrincipalWorker, fixtureNow, validFDs(operation)); err == nil || ErrorCode(err) != CodePrincipalUnauthorized {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestFDRoleExactValidation(t *testing.T) {
	launch := validFDs(OperationPrepareLaunch)
	leaves := make([]FDRef, 24)
	for i := range leaves {
		leaves[i] = FDRef{Role: FDBundleLeaf, Index: i}
	}
	tests := []struct {
		name      string
		operation Operation
		refs      []FDRef
		ok        bool
	}{
		{name: "launch exact", operation: OperationPrepareLaunch, refs: launch, ok: true},
		{name: "launch missing", operation: OperationPrepareLaunch, refs: launch[:7]},
		{name: "launch duplicate", operation: OperationPrepareLaunch, refs: append(append([]FDRef(nil), launch[:7]...), FDRef{Role: FDControlOutput})},
		{name: "leaf 24", operation: OperationStageBundleLeafBatch, refs: leaves, ok: true},
		{name: "leaf 25", operation: OperationStageBundleLeafBatch, refs: append(leaves, FDRef{Role: FDBundleLeaf, Index: 24})},
		{name: "leaf index gap", operation: OperationStageBundleLeafBatch, refs: []FDRef{{Role: FDBundleLeaf, Index: 1}}},
		{name: "unexpected on describe", operation: OperationDescribe, refs: []FDRef{{Role: FDCandidateExecutable}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateControlFDRoles(test.operation, test.refs)
			if (err == nil) != test.ok {
				t.Fatalf("error=%v ok=%v", err, test.ok)
			}
		})
	}
}

func TestDecodeControlRequestNegativeConformance(t *testing.T) {
	base := validRequest(OperationBeginProbe, PrincipalVerifierController)
	tests := []struct {
		name   string
		mutate func(*APAPRequestEnvelopeV1)
		peer   Principal
		fds    []FDRef
		raw    func([]byte) []byte
		code   SafeCode
	}{
		{name: "wrong principal", peer: PrincipalConsumer, fds: beginFDs(), code: CodeIdentityMismatch},
		{name: "unauthorized operation", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) {
			r.CallerPrincipalDigest = string(PrincipalVerifierController)
			r.Operation = OperationPrepareLaunch
		}, code: CodePrincipalUnauthorized},
		{name: "unknown profile", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.AuthorityProfile = "unknown" }, code: CodeProfileUnsupported},
		{name: "wrong audience", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.Audience = "wrong" }, code: CodeIdentityMismatch},
		{name: "attach forbidden on control", peer: PrincipalVerifierController, fds: nil, mutate: func(r *APAPRequestEnvelopeV1) { r.Operation = OperationAttachProbeCredential }, code: CodeIdentityMismatch},
		{name: "nonce invalid", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.Nonce = "short" }, code: CodeIdentityMismatch},
		{name: "expired", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) { r.ExpiresAt = fixtureNow }, code: CodeIdentityMismatch},
		{name: "fd missing", peer: PrincipalVerifierController, fds: beginFDs()[:2], code: CodeIdentityMismatch},
		{name: "fd order", peer: PrincipalVerifierController, fds: []FDRef{{Role: FDScratchRoot}, {Role: FDCandidateExecutable}, {Role: FDBusinessDenyRoot}}, code: CodeIdentityMismatch},
		{name: "credential root on APAP", peer: PrincipalVerifierController, fds: []FDRef{{Role: FDCandidateExecutable}, {Role: FDScratchRoot}, {Role: FDCredentialRoot}}, code: CodeSecretBoundaryViolation},
		{name: "credential capability on APAP", peer: PrincipalVerifierController, fds: []FDRef{{Role: FDCandidateExecutable}, {Role: FDScratchRoot}, {Role: FDCredentialCapability}}, code: CodeSecretBoundaryViolation},
		{name: "unknown semantic field", peer: PrincipalVerifierController, fds: beginFDs(), mutate: func(r *APAPRequestEnvelopeV1) {
			r.Payload = json.RawMessage(`{"candidateIdentityDigest":"sha256:candidate","suiteDigest":"sha256:suite","probeArtifactDigest":"sha256:artifact","policyDigest":"sha256:policy","challengeDigest":"sha256:challenge","deadline":"2026-08-18T08:01:00Z","credentialRoot":"forbidden"}`)
		}, code: CodeIdentityMismatch},
		{name: "duplicate envelope field", peer: PrincipalVerifierController, fds: beginFDs(), raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"requestId":"request-1"`), []byte(`"requestId":"request-1","requestId":"request-2"`), 1)
		}, code: CodeIdentityMismatch},
		{name: "duplicate semantic field", peer: PrincipalVerifierController, fds: beginFDs(), raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"suiteDigest":"sha256:suite"`), []byte(`"suiteDigest":"sha256:suite","suiteDigest":"sha256:other"`), 1)
		}, code: CodeIdentityMismatch},
		{name: "unknown envelope field", peer: PrincipalVerifierController, fds: beginFDs(), raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"audience"`), []byte(`{"alien":true,"audience"`), 1)
		}, code: CodeIdentityMismatch},
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
			_, err := DecodeControlRequest(raw, test.peer, fixtureNow, test.fds)
			if err == nil || ErrorCode(err) != test.code {
				t.Fatalf("error=%v code=%s want=%s", err, ErrorCode(err), test.code)
			}
		})
	}
}

func TestExpectedSequenceAndReplay(t *testing.T) {
	request := validRequest(OperationCommitBundleUpdate, PrincipalEvidenceConfig)
	raw := mustSeal(t, request)
	fake := NewFakeProvider(7)
	first, err := fake.HandleControl(raw, PrincipalEvidenceConfig, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.ProviderSequence() != 8 {
		t.Fatalf("sequence=%d", fake.ProviderSequence())
	}
	replay, err := fake.HandleControl(raw, PrincipalEvidenceConfig, fixtureNow, nil)
	if err != nil {
		t.Fatalf("exact replay failed after sequence advance: %v", err)
	}
	if !bytes.Equal(first, replay) {
		t.Fatal("replay response changed")
	}
	conflict := request
	conflict.Nonce = "nonce-0002"
	conflictRaw := mustSeal(t, conflict)
	if _, err := fake.HandleControl(conflictRaw, PrincipalEvidenceConfig, fixtureNow, nil); err == nil || ErrorCode(err) != CodeIdentityMismatch {
		t.Fatalf("conflict=%v", err)
	}
	stale := validRequest(OperationBeginProbe, PrincipalVerifierController)
	stale.ExpectedProviderSequence = sequence(6)
	if _, err := fake.HandleControl(mustSeal(t, stale), PrincipalVerifierController, fixtureNow, beginFDs()); err == nil || ErrorCode(err) != CodeIdentityMismatch {
		t.Fatalf("stale CAS=%v", err)
	}
	readWithCAS := validRequest(OperationDescribe, PrincipalConsumer)
	readWithCAS.ExpectedProviderSequence = sequence(7)
	if _, err := DecodeControlRequest(mustSeal(t, readWithCAS), PrincipalConsumer, fixtureNow, nil); err == nil {
		t.Fatal("read-only request with CAS accepted")
	}
}

func TestControlResponseEnvelopeClosed(t *testing.T) {
	request := validRequest(OperationBeginProbe, PrincipalVerifierController)
	raw := mustSeal(t, request)
	decodedRequest, err := DecodeControlRequest(raw, PrincipalVerifierController, fixtureNow, beginFDs())
	if err != nil {
		t.Fatal(err)
	}
	responseRaw, err := NewFakeProvider(7).HandleControl(raw, PrincipalVerifierController, fixtureNow, beginFDs())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlResponse(responseRaw, decodedRequest); err != nil {
		t.Fatal(err)
	}
	var response APAPResponseEnvelopeV1
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*APAPResponseEnvelopeV1)
		raw    func([]byte) []byte
	}{
		{name: "wrong operation", mutate: func(r *APAPResponseEnvelopeV1) { r.Operation = OperationDescribe }},
		{name: "unknown safe code", mutate: func(r *APAPResponseEnvelopeV1) { r.SafeCode = "unknown" }},
		{name: "unknown field", raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"audience"`), []byte(`{"alien":true,"audience"`), 1)
		}},
		{name: "duplicate field", raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"requestId":"request-1"`), []byte(`"requestId":"request-1","requestId":"other"`), 1)
		}},
		{name: "missing required field", raw: func(raw []byte) []byte { return bytes.Replace(raw, []byte(`,"safeMessage":""`), nil, 1) }},
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
			if _, err := DecodeControlResponse(encoded, decodedRequest); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func validIngressRequest() CredentialIngressRequestV1 {
	payload := AttachProbeCredentialPayload{ProbeSessionID: "probe-1", CapabilityIdentityDigest: "sha256:cap", CapabilityPolicyDigest: "sha256:policy", ServiceIdentityDigest: "sha256:service", CapabilityExpiresAt: fixtureNow.Add(time.Minute), DeliveryNonce: "delivery-0001", TargetIsolationIdentityDigest: "sha256:target"}
	return CredentialIngressRequestV1{SchemaVersion: IngressRequestSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion, Audience: IngressAudience, RequestID: "request-1", CommandID: "command-1", SecretProviderPrincipalDigest: string(PrincipalSecretProvider), ProviderInstanceID: "provider-1", AuthorityProfile: ProfileQoder, ProbeSessionID: "probe-1", TargetIsolationIdentityDigest: "sha256:target", CredentialIngressEndpointIdentityDigest: "sha256:endpoint", CredentialIngressTicketDigest: "sha256:ticket", IssuedAt: fixtureNow.Add(-time.Second), ExpiresAt: fixtureNow.Add(time.Minute), Nonce: "nonce-0001", Payload: mustJSON(payload)}
}

func TestCredentialIngressIsSeparateAndClosed(t *testing.T) {
	request := validIngressRequest()
	raw, err := SealCredentialIngressRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	credentialFD := []FDRef{{Role: FDCredentialCapability}}
	if _, err := DecodeCredentialIngressRequest(raw, PrincipalSecretProvider, fixtureNow, credentialFD); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeProvider(7)
	responseRaw, err := fake.HandleCredentialIngress(raw, PrincipalSecretProvider, fixtureNow, credentialFD)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCredentialIngressResponse(responseRaw, request); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.Nonce = "nonce-0002"
	conflictRaw, _ := SealCredentialIngressRequest(conflict)
	if _, err := fake.HandleCredentialIngress(conflictRaw, PrincipalSecretProvider, fixtureNow, credentialFD); err == nil {
		t.Fatal("ingress replay conflict accepted")
	}
	tests := []struct {
		name   string
		peer   Principal
		fds    []FDRef
		mutate func(*CredentialIngressRequestV1)
		code   SafeCode
	}{
		{name: "wrong principal", peer: PrincipalVerifierController, fds: credentialFD, code: CodePrincipalUnauthorized},
		{name: "missing capability", peer: PrincipalSecretProvider, code: CodeSecretBoundaryViolation},
		{name: "wrong fd", peer: PrincipalSecretProvider, fds: []FDRef{{Role: FDCandidateExecutable}}, code: CodeSecretBoundaryViolation},
		{name: "wrong audience", peer: PrincipalSecretProvider, fds: credentialFD, mutate: func(r *CredentialIngressRequestV1) { r.Audience = ControlAudience }, code: CodeIdentityMismatch},
		{name: "target substitution", peer: PrincipalSecretProvider, fds: credentialFD, mutate: func(r *CredentialIngressRequestV1) {
			var p AttachProbeCredentialPayload
			_ = json.Unmarshal(r.Payload, &p)
			p.TargetIsolationIdentityDigest = "sha256:other"
			r.Payload = mustJSON(p)
		}, code: CodeSecretBoundaryViolation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			candidate.Payload = append(json.RawMessage(nil), request.Payload...)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			encoded, sealErr := SealCredentialIngressRequest(candidate)
			if sealErr != nil {
				t.Fatal(sealErr)
			}
			_, decodeErr := DecodeCredentialIngressRequest(encoded, test.peer, fixtureNow, test.fds)
			if decodeErr == nil || ErrorCode(decodeErr) != test.code {
				t.Fatalf("error=%v", decodeErr)
			}
		})
	}
}

func TestSafeCodeClassificationClosed(t *testing.T) {
	tests := map[SafeCode]SafeClass{CodeOK: ClassOK, CodeBundleInvalid: ClassPermanent, CodeProviderBusy: ClassTransient, CodeBundleCommitAmbiguous: ClassReconcile, CodeInternalFailClosed: ClassPermanent}
	for code, want := range tests {
		got, ok := code.Class()
		if !ok || got != want {
			t.Fatalf("%s class=%s", code, got)
		}
	}
	if _, ok := SafeCode("unknown").Class(); ok {
		t.Fatal("unknown safe code accepted")
	}
}

func TestSignedObjectEnvelopeValidation(t *testing.T) {
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	unsigned := []byte(`{"authorityProfile":"qoder-cli-adr0034-v1","value":1}`)
	domain := "marshal-test-v1\x00"
	usage := "test-usage"
	envelope, err := SignObjectForFake(unsigned, domain, "key-1", 3, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyring := StaticKeyring{"key-1:3": {PublicKey: publicKey, Usage: usage}}
	if err := ValidateSignedObject(unsigned, envelope, domain, usage, keyring); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSignedObject(unsigned, envelope, domain, usage, nil); err == nil {
		t.Fatal("nil key resolver accepted")
	}
	tests := []struct {
		name      string
		mutate    func(*SignedObjectEnvelopeV1)
		wantUsage string
	}{
		{name: "domain", mutate: func(e *SignedObjectEnvelopeV1) { e.SignatureDomain = "wrong" }},
		{name: "key usage", wantUsage: "wrong"},
		{name: "key epoch", mutate: func(e *SignedObjectEnvelopeV1) { e.KeyEpoch = 4 }},
		{name: "algorithm", mutate: func(e *SignedObjectEnvelopeV1) { e.SignatureAlgorithm = "unknown" }},
		{name: "encoding", mutate: func(e *SignedObjectEnvelopeV1) { e.SignatureEncoding = "base64" }},
		{name: "signature", mutate: func(e *SignedObjectEnvelopeV1) { e.Signature = strings.Repeat("A", 86) }},
		{name: "digest", mutate: func(e *SignedObjectEnvelopeV1) { e.ObjectDigest = "sha256:" + strings.Repeat("0", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := envelope
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			expectedUsage := usage
			if test.wantUsage != "" {
				expectedUsage = test.wantUsage
			}
			if err := ValidateSignedObject(unsigned, candidate, domain, expectedUsage, keyring); err == nil {
				t.Fatal("invalid signature envelope accepted")
			}
		})
	}
	if err := ValidateSignedObject([]byte(`{"value":1,"value":2}`), envelope, domain, usage, keyring); err == nil {
		t.Fatal("duplicate unsigned member accepted")
	}
}

func TestFakeProviderFaults(t *testing.T) {
	request := validRequest(OperationBeginProbe, PrincipalVerifierController)
	raw := mustSeal(t, request)
	dropped := NewFakeProvider(7).WithFaults(FaultSpec{Operation: OperationBeginProbe, Fault: FaultDropResponse})
	if _, err := dropped.HandleControl(raw, PrincipalVerifierController, fixtureNow, beginFDs()); !errors.Is(err, ErrResponseLost) {
		t.Fatalf("drop=%v", err)
	}
	if _, err := dropped.HandleControl(raw, PrincipalVerifierController, fixtureNow, beginFDs()); err != nil {
		t.Fatalf("lost response replay=%v", err)
	}
	advanced := NewFakeProvider(7).WithFaults(FaultSpec{Operation: OperationBeginProbe, Fault: FaultAdvanceSequence})
	if _, err := advanced.HandleControl(raw, PrincipalVerifierController, fixtureNow, beginFDs()); err == nil {
		t.Fatal("advance fault did not force CAS conflict")
	}
	rejected := NewFakeProvider(7).WithFaults(FaultSpec{Operation: OperationBeginProbe, Fault: FaultReject})
	if _, err := rejected.HandleControl(raw, PrincipalVerifierController, fixtureNow, beginFDs()); err == nil || ErrorCode(err) != CodeInternalFailClosed {
		t.Fatalf("reject=%v", err)
	}
}
