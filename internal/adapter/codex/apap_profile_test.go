package codex

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

type codexAPAPTestSource struct {
	material authorityProbeMaterial
	err      error
}

func (source *codexAPAPTestSource) LoadFreshAuthority(context.Context) (authorityProbeMaterial, error) {
	if source == nil || source.err != nil {
		return authorityProbeMaterial{}, source.err
	}
	return source.material, nil
}

type codexAPAPFixture struct {
	authority       CodexAPAPAuthority
	now             time.Time
	responsePrivate ed25519.PrivateKey
	launchPrivate   ed25519.PrivateKey
	bundle          testAuthorityBundle
}

func newCodexAPAPFixture(t *testing.T) codexAPAPFixture {
	t.Helper()
	contract, err := compiledCodexContractBinding()
	if err != nil {
		t.Fatal(err)
	}
	binary := ExecutableIdentityV1{CanonicalRealpath: "/opt/codex/codex", DeviceMajor: 8, DeviceMinor: 1, Inode: 42, MountIDUnique: 99, Size: 4096, Mode: 0o755, SHA256: testDigest("codex-01450"), Version: codexAPAPVersion, VersionOutputDigest: testDigest("codex-version")}
	bundle := newTestAuthorityBundleFor(t, binary, contract)
	responsePublic, responsePrivate, _ := ed25519.GenerateKey(rand.Reader)
	launchPublic, launchPrivate, _ := ed25519.GenerateKey(rand.Reader)
	authority := CodexAPAPAuthority{ProviderInstanceID: "provider-codex", ProviderSequence: 7, Peer: authorityprovider.PeerIdentity{PrincipalDigest: testDigest("verifier-peer"), Role: authorityprovider.PrincipalVerifierController}, ResponseKeys: authorityprovider.StaticKeyring{"response-key:0": {PublicKey: responsePublic, Usage: codexAPAPResponseUsage}}, LaunchKeys: authorityprovider.StaticKeyring{"launch-key:0": {PublicKey: launchPublic, Usage: codexLaunchUsage}}, Source: &codexAPAPTestSource{material: authorityMaterialFromFixture(t, bundle)}, CandidateExecutable: binary}
	return codexAPAPFixture{authority: authority, now: bundle.now, responsePrivate: responsePrivate, launchPrivate: launchPrivate, bundle: bundle}
}

func (fixture codexAPAPFixture) bridge(t *testing.T) *CodexAPAPProfileBridge {
	t.Helper()
	bridge, err := newCodexAPAPProfileBridge(fixture.authority, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}

func signCodexAPAP(t *testing.T, document []byte, domain, keyID string, private ed25519.PrivateKey) authorityprovider.SignedObjectEnvelopeV1 {
	t.Helper()
	signature, err := authorityprovider.SignObjectForFake(document, domain, keyID, 0, private)
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

func codexCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCodexAPAPAuthorityPinsAtomicIdentityAndRevocation(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	if _, err := NewCodexAPAPProfileBridge(fixture.authority); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*CodexAPAPAuthority){
		"exact version": func(value *CodexAPAPAuthority) { value.CandidateExecutable.Version = "0.145.1" },
		"provider":      func(value *CodexAPAPAuthority) { value.ProviderInstanceID = "" },
		"peer":          func(value *CodexAPAPAuthority) { value.Peer.Role = authorityprovider.PrincipalConsumer },
		"peer digest":   func(value *CodexAPAPAuthority) { value.Peer.PrincipalDigest = "sha256:bad" },
		"response key":  func(value *CodexAPAPAuthority) { value.ResponseKeys = nil },
		"launch key":    func(value *CodexAPAPAuthority) { value.LaunchKeys = nil },
		"source":        func(value *CodexAPAPAuthority) { value.Source = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := fixture.authority
			mutate(&candidate)
			if _, err := newCodexAPAPProfileBridge(candidate, func() time.Time { return fixture.now }); err == nil {
				t.Fatal("invalid static authority accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*authorityProbeMaterial){
		"held substitution": func(material *authorityProbeMaterial) {
			material.ObservationEnvelope.PayloadDigest = testDigest("substitution")
		},
		"generation": func(material *authorityProbeMaterial) { material.State.Fence.AuthorityGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := fixture.authority
			material := authorityMaterialFromFixture(t, fixture.bundle)
			mutate(&material)
			candidate.Source = &codexAPAPTestSource{material: material}
			bridge, err := newCodexAPAPProfileBridge(candidate, func() time.Time { return fixture.now })
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := bridge.DescribeRequest(context.Background(), "request-1", "command-1", "nonce-0001", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute)); err == nil {
				t.Fatal("invalid atomic authority accepted")
			}
		})
	}

	var config CodexAuthorityConfigV1
	if err := json.Unmarshal(fixture.bundle.config.Payload, &config); err != nil {
		t.Fatal(err)
	}
	config.RevokedEvidenceDigests = []string{fixture.bundle.evidence.PayloadDigest}
	config.RevocationSetDigest, _ = RevocationSetDigest(config)
	configEnvelope := buildTestSignedEnvelope(t, config, authorityConfigSchema, map[string]ed25519.PrivateKey{"config-key": fixture.bundle.configPrivate})
	material := authorityMaterialFromFixture(t, fixture.bundle)
	material.ConfigEnvelope = configEnvelope
	material.State.Fence.ConfigDigest = configEnvelope.PayloadDigest
	material.State.Fence.RevocationSetDigest = config.RevocationSetDigest
	candidate := fixture.authority
	candidate.Source = &codexAPAPTestSource{material: material}
	bridge, _ := newCodexAPAPProfileBridge(candidate, func() time.Time { return fixture.now })
	if _, _, err := bridge.DescribeRequest(context.Background(), "request-2", "command-2", "nonce-0002", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("revoked current evidence accepted")
	}
}

func TestCodexAPAPLaunchReceiptExactBindingAndReplay(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	bridge := fixture.bridge(t)
	session := codexAPAPSession(t, fixture, bridge)
	request := CodexLaunchRequestV1{TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", LaunchNonce: testNonce(21), AuthorityGeneration: session.AuthorityGeneration, TrustRootGeneration: session.TrustRootGeneration, ArgvDigest: testDigest("argv"), ConfigDigest: session.ConfigDigest, ControlRootIdentityDigest: testDigest("control-root"), EnvironmentDigest: testDigest("environment"), EvidenceDigest: session.EvidenceDigest, FenceDigest: session.FenceDigest, WorktreeIdentityDigest: testDigest("worktree")}
	receipt := codexLaunchReceipt(t, fixture, session, request)
	document := codexCanonical(t, receipt)
	signature := signCodexAPAP(t, document, codexLaunchDomain, "launch-key", fixture.launchPrivate)
	binding, err := bridge.BindLaunchReceipt(context.Background(), session, request, document, signature)
	if err != nil || binding.LaunchReceiptDigest != signature.ObjectDigest {
		t.Fatalf("valid launch receipt rejected: %+v, %v", binding, err)
	}
	if _, err := bridge.BindLaunchReceipt(context.Background(), session, request, document, signature); err != nil {
		t.Fatalf("identical replay rejected: %v", err)
	}

	for name, mutate := range map[string]func(*CodexWorkerLaunchReceiptV1){
		"provider generation": func(value *CodexWorkerLaunchReceiptV1) { value.AuthorityGeneration++ },
		"evidence":            func(value *CodexWorkerLaunchReceiptV1) { value.EvidenceDigest = testDigest("foreign-evidence") },
		"executable": func(value *CodexWorkerLaunchReceiptV1) {
			value.SourceExecutableIdentityDigest = testDigest("foreign-executable")
		},
		"request":     func(value *CodexWorkerLaunchReceiptV1) { value.RequestDigest = testDigest("foreign-request") },
		"child":       func(value *CodexWorkerLaunchReceiptV1) { value.Child.ProcExeInode++ },
		"phase order": func(value *CodexWorkerLaunchReceiptV1) { value.PhaseDigests[0] = "sha256:bad" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			changed.PhaseDigests = append([]string(nil), receipt.PhaseDigests...)
			mutate(&changed)
			raw := codexCanonical(t, changed)
			signed := signCodexAPAP(t, raw, codexLaunchDomain, "launch-key", fixture.launchPrivate)
			if _, err := bridge.BindLaunchReceipt(context.Background(), session, request, raw, signed); err == nil {
				t.Fatal("re-signed launch substitution accepted")
			}
		})
	}
	_, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := bridge.BindLaunchReceipt(context.Background(), session, request, document, signCodexAPAP(t, document, codexLaunchDomain, "launch-key", foreignPrivate)); err == nil {
		t.Fatal("foreign launch signer accepted")
	}

	request2 := request
	request2.LaunchNonce = testNonce(22)
	receipt2 := codexLaunchReceipt(t, fixture, session, request2)
	document2 := codexCanonical(t, receipt2)
	if _, err := bridge.BindLaunchReceipt(context.Background(), session, request2, document2, signCodexAPAP(t, document2, codexLaunchDomain, "launch-key", fixture.launchPrivate)); err == nil {
		t.Fatal("same Attempt different nonce replay accepted")
	}

	tampered := session
	tampered.AuthorityProfile = authorityprovider.ProfileQoder
	if _, err := bridge.BindLaunchReceipt(context.Background(), tampered, request, document, signature); err == nil {
		t.Fatal("wrong Qoder profile accepted")
	}
	tampered = session
	tampered.ProviderSequence++
	if _, err := bridge.BindLaunchReceipt(context.Background(), tampered, request, document, signature); err == nil {
		t.Fatal("foreign provider sequence accepted")
	}

	bridge.now = func() time.Time { return session.ExpiresAt.Add(time.Second) }
	if _, err := bridge.BindLaunchReceipt(context.Background(), session, request, document, signature); err == nil {
		t.Fatal("expired APAP session accepted")
	}
}

func TestCodexAPAPFreshRecheckRejectsRevocationBeforeLaunchBinding(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	bridge := fixture.bridge(t)
	session := codexAPAPSession(t, fixture, bridge)
	request := CodexLaunchRequestV1{TaskID: "task-fresh", RunID: "run-fresh", AttemptID: "attempt-fresh", LaunchNonce: testNonce(23), AuthorityGeneration: session.AuthorityGeneration, TrustRootGeneration: session.TrustRootGeneration, ArgvDigest: testDigest("argv"), ConfigDigest: session.ConfigDigest, ControlRootIdentityDigest: testDigest("control-root"), EnvironmentDigest: testDigest("environment"), EvidenceDigest: session.EvidenceDigest, FenceDigest: session.FenceDigest, WorktreeIdentityDigest: testDigest("worktree")}
	receipt := codexLaunchReceipt(t, fixture, session, request)
	document := codexCanonical(t, receipt)
	signature := signCodexAPAP(t, document, codexLaunchDomain, "launch-key", fixture.launchPrivate)
	fixture.authority.Source.(*codexAPAPTestSource).material = revokedCodexAPAPMaterial(t, fixture)
	if _, err := bridge.BindLaunchReceipt(context.Background(), session, request, document, signature); err == nil {
		t.Fatal("authority revoked between response and launch binding was accepted")
	}
}

func codexLaunchReceipt(t *testing.T, fixture codexAPAPFixture, session CodexAPAPProbeSession, request CodexLaunchRequestV1) CodexWorkerLaunchReceiptV1 {
	t.Helper()
	requestDigest, err := codexLaunchRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	contract, _ := compiledCodexContractBinding()
	requested := fixture.now.Add(-2 * time.Second)
	return CodexWorkerLaunchReceiptV1{SchemaVersion: codexLaunchSchema, AuthorityNamespace: session.AuthorityNamespace, AuthorityGeneration: session.AuthorityGeneration, TrustRootGeneration: session.TrustRootGeneration, TaskID: request.TaskID, RunID: request.RunID, AttemptID: request.AttemptID, LaunchNonce: request.LaunchNonce, RequestDigest: requestDigest, LauncherBuildDigest: contract.LauncherBuildDigest, LaunchKeyID: "launch-key", ConfigDigest: session.ConfigDigest, EvidenceDigest: session.EvidenceDigest, FenceDigest: session.FenceDigest, HostIdentityDigest: session.HostIdentityDigest, SourceExecutableIdentityDigest: session.CandidateExecutableIdentityDigest, SealedMemfd: SealedMemfdIdentityV1{DeviceMajor: 1, DeviceMinor: 2, Inode: 3, MountIDUnique: 4, Size: session.CandidateExecutable.Size, SHA256: session.CandidateExecutable.SHA256, Seals: codexRequiredMemfdSeals}, Child: ChildExecIdentityV1{PID: 123, StartTimeTicks: 456, PidfdInode: 789, ProcExeDeviceMajor: 1, ProcExeDeviceMinor: 2, ProcExeInode: 3, ProcExeMountIDUnique: 4, ProcExeSize: session.CandidateExecutable.Size, ProcExeSHA256: session.CandidateExecutable.SHA256}, ArgvDigest: request.ArgvDigest, EnvironmentDigest: request.EnvironmentDigest, PhaseDigests: []string{testDigest("t0"), testDigest("t1"), testDigest("t2"), testDigest("t3")}, RequestedAt: formatAuthorityTime(requested), ExecObservedAt: formatAuthorityTime(requested.Add(time.Second)), IssuedAt: formatAuthorityTime(fixture.now)}
}

func revokedCodexAPAPMaterial(t *testing.T, fixture codexAPAPFixture) authorityProbeMaterial {
	t.Helper()
	var config CodexAuthorityConfigV1
	if err := json.Unmarshal(fixture.bundle.config.Payload, &config); err != nil {
		t.Fatal(err)
	}
	config.RevokedEvidenceDigests = []string{fixture.bundle.evidence.PayloadDigest}
	config.RevocationSetDigest, _ = RevocationSetDigest(config)
	configEnvelope := buildTestSignedEnvelope(t, config, authorityConfigSchema, map[string]ed25519.PrivateKey{"config-key": fixture.bundle.configPrivate})
	material := authorityMaterialFromFixture(t, fixture.bundle)
	material.ConfigEnvelope = configEnvelope
	material.State.Fence.ConfigDigest = configEnvelope.PayloadDigest
	material.State.Fence.RevocationSetDigest = config.RevocationSetDigest
	return material
}

func TestCodexAPAPSourceFailureIsFailClosed(t *testing.T) {
	fixture := newCodexAPAPFixture(t)
	fixture.authority.Source = &codexAPAPTestSource{err: errors.New("unavailable")}
	bridge := fixture.bridge(t)
	if _, _, err := bridge.DescribeRequest(context.Background(), "request-3", "command-3", "nonce-0003", fixture.now.Add(-time.Second), fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("source failure accepted")
	}
}
