package qoder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

type qoderAPAPFixture struct {
	authority       QoderAPAPAuthority
	now             time.Time
	responsePrivate ed25519.PrivateKey
	receiptPrivate  ed25519.PrivateKey
	fencePrivate    ed25519.PrivateKey
}

func qoderAPAPHeldFixture() QoderAPAPHeldProbeBinding {
	return QoderAPAPHeldProbeBinding{
		ScratchRootIdentities: []CandidateRootIdentity{
			candidateRootIdentity(CandidateObjectIdentity{Device: 10, Inode: 10}),
			candidateRootIdentity(CandidateObjectIdentity{Device: 11, Inode: 11}),
			candidateRootIdentity(CandidateObjectIdentity{Device: 12, Inode: 12}),
			candidateRootIdentity(CandidateObjectIdentity{Device: 13, Inode: 13}),
		},
		CredentialRootIdentity:        candidateRootIdentity(CandidateObjectIdentity{Device: 20, Inode: 20}),
		BusinessRootIdentities:        []CandidateRootIdentity{candidateRootIdentity(CandidateObjectIdentity{Device: 30, Inode: 30})},
		VariantTopologyDigests:        []string{digest("1"), digest("2"), digest("3"), digest("4")},
		TargetIsolationIdentityDigest: digest("d"), CredentialIngressEndpointIdentityDigest: digest("f"),
	}
}

func newQoderAPAPFixture(t *testing.T) qoderAPAPFixture {
	t.Helper()
	osFixture := newExactLedgerFixture(t)
	osFixture.appendActivate(t, "credential-capability-provider", "credential-0", 0, "operator-0")
	probe := newExactProbeTrustFixtureWithOperator(t, osFixture.now, QoderOSTrustKeyIdentity{Role: "trust-ledger-operator", KeyID: "operator-0", PublicKeyDigest: digestBytes(osFixture.keys["operator-0"].Public().(ed25519.PublicKey)), PublicKey: osFixture.keys["operator-0"].Public().(ed25519.PublicKey)}, osFixture.keys["operator-0"])
	trust, err := ReplayQoderProbeTrustLedger(probe.records, []QoderOSTrustKeyIdentity{probe.operator}, osFixture.now)
	if err != nil {
		t.Fatal(err)
	}
	osState, err := ReplayQoderOSTrustRootLedger(osFixture.records, osFixture.receipts, "os-anchor", "anchor-key", 0, osFixture.providerPublic, osFixture.now)
	if err != nil {
		t.Fatal(err)
	}
	host := exactRootedHostIdentity(t, osFixture.now, "host-1", 1, osFixture.keys["host-1"])
	executable, evidenceRoot := exactHeldObjects(t)
	evidence := QoderConformanceEvidenceExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderConformanceEvidence", SchemaVersion: 1, ObservationDigest: digest("a"), ProbeRunID: "run-1", RunnerID: "runner-1", RunnerVersion: "1", ObservedAt: candidateExactTimestamp(osFixture.now.Add(-time.Minute)), ValidUntil: candidateExactTimestamp(osFixture.now.Add(time.Hour)), AdapterVersion: adapterVersion, CandidateExecutableIdentity: candidateExecutableReceiptIdentity(executable, supportedBinary), HostIdentity: host, AuthorityGeneration: 1, SuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("c"), ProbeRunChallengeDigest: digest("e"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProfileDigest: expectedProbeProfileDigest(), VariantInvocationManifests: exactEvidenceManifests(t, osFixture.now, osFixture.keys["credential-0"]), ToolPolicyDigest: expectedProbeToolPolicyDigest(), EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TranscriptDigest: digest("3"), ReceiptDigests: []string{digest("4"), digest("5"), digest("6"), digest("7")}, AggregateReceiptDigest: digest("8"), CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, EvidenceAuthorityKeyID: "evidence-0", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding}
	resignExactEvidence(&evidence, probe.keys["evidence-0"])
	config := QoderAuthorityConfigExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderAuthorityConfig", SchemaVersion: 1, RepositoryIdentity: "repo-1", AuthorityGeneration: 1, HostIdentityDigest: host.RecordDigest, EvidenceRootIdentity: candidateRootIdentity(evidenceRoot.Identity), CurrentEvidenceDigest: evidence.EvidenceDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest, ProbeRunChallengeDigest: evidence.ProbeRunChallengeDigest, RevokedEvidenceDigests: []string{}, TrustPolicyDigest: digest("a"), ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, ConsumerFenceProviderIdentity: "fence-provider"}
	config.ConfigDigest = digestRecordWithoutFields(config, "configDigest")
	fenceRequest := ConsumerFenceAdvanceRequest{ConsumerInstanceID: "consumer-1", RepositoryIdentity: config.RepositoryIdentity, TransactionID: "transaction-1", PreparedRecordDigest: digest("9"), AuthorityGeneration: config.AuthorityGeneration, ConfigDigest: config.ConfigDigest}
	current := QoderExactAuthorityCurrent{OSTrustRecords: osFixture.records, OSTrustReceipts: osFixture.receipts, OSAnchorProviderIdentity: "os-anchor", OSAnchorProviderKeyID: "anchor-key", OSAnchorProviderPublicKey: osFixture.providerPublic, ProbeTrustRecords: probe.records, HostIdentity: host, FenceRequest: fenceRequest, FenceReceipt: exactFenceAdvanceReceipt(t, osFixture.now, fenceRequest, "fence-0", 0, osFixture.keys["fence-0"]), CredentialProviderIdentity: "credential-provider", Executable: executable, ExecutableVersion: supportedBinary, EvidenceRoot: evidenceRoot}
	responsePublic, responsePrivate, _ := ed25519.GenerateKey(rand.Reader)
	peerDigest := digest("a")
	authority := QoderAPAPAuthority{ProviderInstanceID: "provider-1", ProviderSequence: 7, Peer: authorityprovider.PeerIdentity{PrincipalDigest: peerDigest, Role: authorityprovider.PrincipalVerifierController}, ResponseKeys: authorityprovider.StaticKeyring{"response-key:0": {PublicKey: responsePublic, Usage: qoderAPAPResponseUsage}}, Config: config, Evidence: evidence, Current: current}
	return qoderAPAPFixture{authority: authority, now: osFixture.now, responsePrivate: responsePrivate, receiptPrivate: probe.keys["receipt-0"], fencePrivate: osFixture.keys["fence-0"]}
}

func signQoderAPAPResponse(t *testing.T, request authorityprovider.APAPRequestEnvelopeV1, response []byte, private ed25519.PrivateKey) QoderAPAPSignedResponse {
	t.Helper()
	document, err := SealQoderAPAPResponseBinding(request.RequestEnvelopeDigest, response)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := authorityprovider.SignObjectForFake(document, qoderAPAPResponseDomain, "response-key", 0, private)
	if err != nil {
		t.Fatal(err)
	}
	return QoderAPAPSignedResponse{Document: document, Signature: signature}
}

func TestQoderAPAPAuthorityPinsVersionPeerEvidenceAndGeneration(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	if _, err := NewQoderAPAPProfileBridge(fixture.authority); err != nil {
		t.Fatalf("real-clock constructor: %v", err)
	}
	if _, err := newQoderAPAPProfileBridge(fixture.authority, fixture.now); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*QoderAPAPAuthority){
		"wrong Qoder version": func(v *QoderAPAPAuthority) { v.Current.ExecutableVersion = "1.1.24" },
		"provider":            func(v *QoderAPAPAuthority) { v.ProviderInstanceID = "" },
		"peer role":           func(v *QoderAPAPAuthority) { v.Peer.Role = authorityprovider.PrincipalConsumer },
		"peer digest":         func(v *QoderAPAPAuthority) { v.Peer.PrincipalDigest = "sha256:bad" },
		"foreign generation":  func(v *QoderAPAPAuthority) { v.Config.AuthorityGeneration++ },
		"held substitution": func(v *QoderAPAPAuthority) {
			replacement, _ := exactHeldObjects(t)
			v.Current.Executable = replacement
		},
		"no authority": func(v *QoderAPAPAuthority) { v.ResponseKeys = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := fixture.authority
			mutate(&candidate)
			if _, err := newQoderAPAPProfileBridge(candidate, fixture.now); err == nil {
				t.Fatal("invalid APAP authority accepted")
			}
		})
	}
}

func TestQoderAPAPAuthorityRejectsRevokedEvidence(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	candidate := fixture.authority
	candidate.Config.RevokedEvidenceDigests = []string{candidate.Evidence.EvidenceDigest}
	candidate.Config.ConfigDigest = digestRecordWithoutFields(candidate.Config, "configDigest")
	candidate.Current.FenceRequest.ConfigDigest = candidate.Config.ConfigDigest
	candidate.Current.FenceReceipt = exactFenceAdvanceReceipt(t, fixture.now, candidate.Current.FenceRequest, "fence-0", 0, fixture.fencePrivate)
	if _, err := newQoderAPAPProfileBridge(candidate, fixture.now); err == nil {
		t.Fatal("revoked evidence accepted")
	}
}

func TestQoderAPAPReceiptMapsExactADR0034Object(t *testing.T) {
	fixture := newQoderAPAPFixture(t)
	bridge, err := newQoderAPAPProfileBridge(fixture.authority, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	verifier, request, _, receiptAuthority, _ := productionCandidateVerifierFixture(t)
	if _, _, err := verifier.Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// Generate a structurally exact receipt through the existing external
	// authority fixture, then bind it to this APAP session and current receipt
	// trust key. No credential bytes enter this bridge test.
	receipt := receiptAuthority.lastReceipt
	held := qoderAPAPHeldFixture()
	held.ScratchRootIdentities[0] = receipt.ScratchRootIdentity
	held.CredentialRootIdentity = receipt.CredentialRootIdentity
	held.BusinessRootIdentities = append([]CandidateRootIdentity(nil), receipt.BusinessRootIdentities...)
	held.VariantTopologyDigests[0] = receipt.TopologyDigest
	begin := QoderAPAPBeginInput{RequestID: "receipt-begin", CommandID: "receipt-command", Nonce: "nonce-0001", IssuedAt: fixture.now.Add(-time.Second), ExpiresAt: fixture.now.Add(time.Minute), Held: held}
	beginRequest, beginRaw, refs, err := bridge.BeginProbeRequest(begin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorityprovider.DecodeControlRequest(beginRaw, fixture.authority.Peer, fixture.now, refs); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(authorityprovider.BeginProbeSuccessPayload{ProbeSessionID: fixture.authority.Evidence.ProbeRunID, TargetIsolationIdentityDigest: held.TargetIsolationIdentityDigest, CredentialIngressEndpointIdentityDigest: held.CredentialIngressEndpointIdentityDigest, ExpiresAt: begin.ExpiresAt})
	responseRaw, err := authorityprovider.SealControlResponse(authorityprovider.APAPResponseEnvelopeV1{SchemaVersion: authorityprovider.ResponseSchema, ProtocolFamily: authorityprovider.ControlFamily, ProtocolVersion: authorityprovider.ProtocolVersion, Audience: authorityprovider.ControlAudience, RequestID: beginRequest.RequestID, CommandID: beginRequest.CommandID, ProviderInstanceID: beginRequest.ProviderInstanceID, AuthorityProfile: authorityprovider.ProfileQoder, Operation: authorityprovider.OperationBeginProbe, ObservedProviderSequence: fixture.authority.ProviderSequence, SafeCode: authorityprovider.CodeOK, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	session, err := bridge.ValidateBeginProbe(beginRequest, signQoderAPAPResponse(t, beginRequest, responseRaw, fixture.responsePrivate))
	if err != nil {
		t.Fatal(err)
	}
	receipt.ProbeRunID = session.ProbeSessionID
	receipt.ReceiptSequence, receipt.VariantID, receipt.PreviousReceiptDigest = 1, candidateVariantID(0), nil
	receipt.InvocationManifest = fixture.authority.Evidence.VariantInvocationManifests[0]
	receipt.CandidateExecutableIdentity = bridge.identity
	receipt.ProbeRunChallengeDigest = session.ChallengeDigest
	receipt.VariantChallengeDigest = candidateVariantChallenge(session.ChallengeDigest, 0)
	receipt.HostIdentityDigest = session.HostIdentityDigest
	receipt.ReceiptAuthorityKeyID, receipt.ReceiptAuthorityKeyEpoch = "receipt-0", 0
	receipt.RecordDigest, _ = receipt.digest()
	message, _ := receipt.signingBytes()
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.receiptPrivate, message))
	document, _ := json.Marshal(receipt)
	document, _ = canonical.JSON(document)
	bridge.now = func() time.Time { return time.Now().UTC().Add(time.Second) }
	if _, err := bridge.BindReceipt(session, document); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.BindReceipt(session, document); err != nil {
		t.Fatalf("exact receipt replay failed: %v", err)
	}
	for name, mutate := range map[string]func(*CandidateExecutionReceipt){
		"variant order":     func(v *CandidateExecutionReceipt) { v.VariantID = candidateVariantID(1) },
		"variant challenge": func(v *CandidateExecutionReceipt) { v.VariantChallengeDigest = digest("b") },
		"isolation profile": func(v *CandidateExecutionReceipt) { v.IsolationProfileDigest = digest("b") },
		"topology": func(v *CandidateExecutionReceipt) {
			v.TopologyDigest = digest("b")
			v.IsolationAudit.AncestorChainDigest = digest("b")
		},
		"protocol":       func(v *CandidateExecutionReceipt) { v.ProtocolVersion = "substitute" },
		"permission":     func(v *CandidateExecutionReceipt) { v.PermissionMode = "substitute" },
		"event contract": func(v *CandidateExecutionReceipt) { v.EventContract = "substitute" },
		"scratch root": func(v *CandidateExecutionReceipt) {
			v.ScratchRootIdentity = candidateRootIdentity(CandidateObjectIdentity{Device: 90, Inode: 90})
		},
		"credential root": func(v *CandidateExecutionReceipt) {
			v.CredentialRootIdentity = candidateRootIdentity(CandidateObjectIdentity{Device: 91, Inode: 91})
		},
		"business root": func(v *CandidateExecutionReceipt) {
			v.BusinessRootIdentities = []CandidateRootIdentity{candidateRootIdentity(CandidateObjectIdentity{Device: 92, Inode: 92})}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			changed.RecordDigest, _ = changed.digest()
			message, _ := changed.signingBytes()
			changed.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.receiptPrivate, message))
			changedDocument, _ := json.Marshal(changed)
			changedDocument, _ = canonical.JSON(changedDocument)
			if _, err := bridge.BindReceipt(session, changedDocument); err == nil {
				t.Fatal("trusted re-signing widened the frozen receipt contract")
			}
		})
	}
	aliasedEndpoint := session
	aliasedEndpoint.CredentialIngressEndpointIdentityDigest = session.CredentialRootIdentity.IdentityDigest
	if _, err := bridge.BindReceipt(aliasedEndpoint, document); err == nil {
		t.Fatal("credential ingress endpoint identity aliased to credential root identity")
	}
	tamperedSession := session
	tamperedSession.AuthorityGeneration++
	if _, err := bridge.BindReceipt(tamperedSession, document); err == nil {
		t.Fatal("session detached from signed response accepted")
	}
	foreignAuthority := receipt
	foreignAuthority.ReceiptAuthorityKeyID = "foreign-receipt-key"
	foreignAuthority.RecordDigest, _ = foreignAuthority.digest()
	message, _ = foreignAuthority.signingBytes()
	foreignAuthority.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.receiptPrivate, message))
	foreignDocument, _ := json.Marshal(foreignAuthority)
	foreignDocument, _ = canonical.JSON(foreignDocument)
	if _, err := bridge.BindReceipt(session, foreignDocument); err == nil {
		t.Fatal("foreign receipt authority accepted")
	}
	conflict := receipt
	conflict.ReceiptID = base64.RawURLEncoding.EncodeToString([]byte("receipt-conflict"))
	conflict.RecordDigest, _ = conflict.digest()
	message, _ = conflict.signingBytes()
	conflict.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.receiptPrivate, message))
	conflictDocument, _ := json.Marshal(conflict)
	conflictDocument, _ = canonical.JSON(conflictDocument)
	if _, err := bridge.BindReceipt(session, conflictDocument); err == nil {
		t.Fatal("same-sequence receipt replay conflict accepted")
	}
	receipt.CandidateExecutableIdentity.BinaryVersion = "1.1.24"
	receipt.RecordDigest, _ = receipt.digest()
	message, _ = receipt.signingBytes()
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.receiptPrivate, message))
	document, _ = json.Marshal(receipt)
	document, _ = canonical.JSON(document)
	if _, err := bridge.BindReceipt(session, document); err == nil {
		t.Fatal("foreign Qoder receipt version accepted")
	}
}
