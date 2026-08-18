package qoder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type candidateIsolationTransportFixture struct {
	t             *testing.T
	principal     CandidateIsolationPrincipal
	calls         []CandidateProbeInvocation
	auditMutate   func(*CandidateIsolationAudit)
	topology      string
	mutateRequest func(*CandidateProbeInvocation)
}

func (fixture *candidateIsolationTransportFixture) Principal(context.Context) (CandidateIsolationPrincipal, error) {
	return fixture.principal, nil
}

func (fixture *candidateIsolationTransportFixture) RunIsolated(_ context.Context, request CandidateIsolationRequest) (CandidateIsolationResult, error) {
	fixture.t.Helper()
	invocation := request.Invocation
	fixture.calls = append(fixture.calls, cloneCandidateProbeInvocation(invocation))
	if fixture.mutateRequest != nil {
		fixture.mutateRequest(&request.Invocation)
	}
	model := invocation.ExpectedModel
	if model == "" {
		model = "provider/default"
	}
	transcript := []byte(fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"production-session-%d\",\"model\":%q,\"qodercli_version\":\"1.1.23\",\"protocol_version\":\"1.2.0\",\"permissionMode\":\"acceptEdits\"}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"challengeDigest\":%q}]}}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"terminal_reason\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n", invocation.VariantIndex, model, invocation.ChallengeDigest))
	writeMarkerAt(fixture.t, invocation.WorkingDirectory.File, []byte(invocation.ChallengeDigest+"\n"))
	audit := CandidateIsolationAudit{
		LaunchAuditDigest: digest("a"), DenialAuditDigest: digest("b"), ExitAuditDigest: digest("c"), AncestorChainDigest: invocation.ExpectedTopologyDigest,
		CredentialReadOnlyEnforced: true, BusinessRootsDenied: true, ScratchOnlyWriteEnforced: true, NetworkPolicyEnforced: true, AmbientStateDenied: true,
	}
	for index := range invocation.BusinessRepositoryRoots {
		audit.BusinessRootDenialDigests = append(audit.BusinessRootDenialDigests, digest(fmt.Sprintf("%x", index+1)))
	}
	if fixture.auditMutate != nil {
		fixture.auditMutate(&audit)
	}
	topology := invocation.ExpectedTopologyDigest
	if fixture.topology != "" {
		topology = fixture.topology
	}
	return CandidateIsolationResult{Transcript: transcript, ExecutionTopologyDigest: topology, Audit: audit}, nil
}

type candidateReceiptAuthorityFixture struct {
	t             *testing.T
	identity      CandidateReceiptAuthorityIdentity
	key           ed25519.PrivateKey
	mutate        func(*CandidateExecutionReceipt)
	lastCompleted time.Time
}

func (fixture *candidateReceiptAuthorityFixture) Identity(context.Context) (CandidateReceiptAuthorityIdentity, error) {
	return fixture.identity, nil
}

func (fixture *candidateReceiptAuthorityFixture) IssueExecutionReceipt(_ context.Context, request CandidateReceiptRequest) ([]byte, error) {
	fixture.t.Helper()
	invocation := request.Invocation
	scratchPath := fmt.Sprintf("/sandbox/objects/scratch-%d-%d", invocation.WorkingDirectory.Identity.Device, invocation.WorkingDirectory.Identity.Inode)
	credentialPath := fmt.Sprintf("/sandbox/objects/credential-%d-%d", invocation.CredentialConfigRoot.Identity.Device, invocation.CredentialConfigRoot.Identity.Inode)
	startedAt := time.Now().UTC().Add(-time.Second)
	if !fixture.lastCompleted.IsZero() {
		startedAt = fixture.lastCompleted.Add(time.Nanosecond)
	}
	completedAt := startedAt.Add(time.Millisecond)
	fixture.lastCompleted = completedAt
	receipt := CandidateExecutionReceipt{
		Kind: candidateReceiptKind, EvidenceClass: candidateEvidenceClassLive, SandboxID: fixture.identity.Issuer, SandboxVersion: "1", ReceiptAuthorityKeyID: fixture.identity.KeyID,
		InvocationDigest: invocation.InvocationDigest, ProbeRunID: invocation.ProbeRunID, ReceiptSequence: invocation.ReceiptSequence, PreviousReceiptDigest: invocation.PreviousReceiptDigest, InvocationManifestDigest: invocation.InvocationManifestDigest, VariantIndex: invocation.VariantIndex,
		Executable: invocation.Executable.Identity, ExecutableDigest: invocation.Executable.Digest, Arguments: substituteBoundPaths(invocation.Arguments, scratchPath, credentialPath), Environment: substituteBoundPaths(invocation.Environment, scratchPath, credentialPath), ScratchArgumentPath: scratchPath, CredentialArgumentPath: credentialPath,
		WorkingDirectory: invocation.WorkingDirectory.Identity, CredentialConfigRoot: invocation.CredentialConfigRoot.Identity, WritableRoots: objectIdentities(invocation.WritableRoots), BusinessRepositoryRoots: objectIdentities(invocation.BusinessRepositoryRoots),
		ChallengeDigest: invocation.ChallengeDigest, TranscriptDigest: request.TranscriptDigest, SessionID: request.SessionID, ObservedModel: request.ObservedModel, BinaryVersion: request.BinaryVersion, ProtocolVersion: request.ProtocolVersion, PermissionMode: request.PermissionMode,
		MarkerDigest: request.MarkerDigest, StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano), TopologyDigest: request.ExecutionTopologyDigest,
		IsolationProfile: request.Principal.Profile, IsolationProviderID: request.Principal.ProviderIdentity, IsolationProcessID: request.Principal.ProcessIdentity, ReceiptAuthorityProviderID: fixture.identity.ProviderIdentity, ReceiptAuthorityProcessID: fixture.identity.ProcessIdentity, IsolationAudit: request.Audit,
	}
	if fixture.mutate != nil {
		fixture.mutate(&receipt)
	}
	return signCandidateReceipt(fixture.t, receipt, fixture.key), nil
}

func productionCandidateVerifierFixture(t *testing.T) (*CandidateLiveVerifier, CandidateLiveProbeRequest, *candidateIsolationTransportFixture, *candidateReceiptAuthorityFixture) {
	t.Helper()
	receiptPublic, receiptPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifierPublic, verifierPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	transport := &candidateIsolationTransportFixture{t: t, principal: CandidateIsolationPrincipal{ProviderIdentity: "os-isolation-provider", ProcessIdentity: "qoder-principal", Profile: candidateIsolationProfile}}
	authority := &candidateReceiptAuthorityFixture{t: t, identity: CandidateReceiptAuthorityIdentity{ProviderIdentity: "os-receipt-provider", ProcessIdentity: "receipt-principal", Issuer: "external-live-sandbox", KeyID: "receipt-root-1"}, key: receiptPrivate}
	sandbox, err := newCandidateProductionProbeSandbox(transport, authority)
	if err != nil {
		t.Fatal(err)
	}
	policy := candidateAuthorityPolicy{receiptIssuer: authority.identity.Issuer, receiptKeyID: authority.identity.KeyID, receiptPublicKey: receiptPublic, verifierKeyID: "verifier-root-1", verifierPublicKey: verifierPublic}
	verifier, err := newCandidateLiveVerifier(sandbox, policy, verifierPrivate)
	if err != nil {
		t.Fatal(err)
	}
	request := CandidateLiveProbeRequest{RunnerID: "external-qoder-probe", RunnerVersion: "1", Executable: fakeExecutable(t, "1.1.23", "exit 0"), CredentialConfigRoot: realPrivateTempDir(t), ScratchRoot: realPrivateTempDir(t), BusinessRepositoryRoots: []string{realPrivateTempDir(t)}, Model: "provider/model", AuthorityGeneration: 1, ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), TrustRootKeyID: "root", Validity: time.Hour}
	request.Executable, err = filepath.EvalSymlinks(request.Executable)
	if err != nil {
		t.Fatal(err)
	}
	return verifier, request, transport, authority
}

func TestProductionProbeSandboxRunsOneChainedFourVariantProbe(t *testing.T) {
	verifier, request, transport, _ := productionCandidateVerifierFixture(t)
	if _, _, err := verifier.Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 4 {
		t.Fatalf("isolated calls = %d, want 4", len(transport.calls))
	}
	runID := transport.calls[0].ProbeRunID
	for index, invocation := range transport.calls {
		if invocation.ProbeRunID != runID || invocation.ReceiptSequence != index+1 || invocation.VariantIndex != index {
			t.Fatalf("variant %d escaped one ordered run: %+v", index, invocation)
		}
		if index == 0 && invocation.PreviousReceiptDigest != "" {
			t.Fatal("first receipt has a predecessor")
		}
		if index > 0 && !validSHA256Digest(invocation.PreviousReceiptDigest) {
			t.Fatalf("variant %d lacks predecessor digest", index)
		}
	}
}

func TestProductionProbeSandboxFailsClosedWithoutIndependentPrincipals(t *testing.T) {
	verifier, request, transport, authority := productionCandidateVerifierFixture(t)
	authority.identity.ProcessIdentity = transport.principal.ProcessIdentity
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("same isolation and receipt principal was accepted")
	}
	if _, err := newCandidateProductionProbeSandbox(nil, authority); err == nil {
		t.Fatal("production sandbox admitted an absent isolation transport")
	}
}

func TestProductionProbeSandboxRejectsIsolationAndReceiptSubstitution(t *testing.T) {
	for name, configure := range map[string]func(*candidateIsolationTransportFixture, *candidateReceiptAuthorityFixture){
		"execution topology": func(transport *candidateIsolationTransportFixture, _ *candidateReceiptAuthorityFixture) {
			transport.topology = digest("substitute")
		},
		"scratch-only write": func(transport *candidateIsolationTransportFixture, _ *candidateReceiptAuthorityFixture) {
			transport.auditMutate = func(audit *CandidateIsolationAudit) { audit.ScratchOnlyWriteEnforced = false }
		},
		"business deny audit": func(transport *candidateIsolationTransportFixture, _ *candidateReceiptAuthorityFixture) {
			transport.auditMutate = func(audit *CandidateIsolationAudit) { audit.BusinessRootDenialDigests = nil }
		},
		"receipt chain": func(_ *candidateIsolationTransportFixture, authority *candidateReceiptAuthorityFixture) {
			authority.mutate = func(receipt *CandidateExecutionReceipt) { receipt.PreviousReceiptDigest = digest("substitute") }
		},
		"unknown receipt key": func(_ *candidateIsolationTransportFixture, authority *candidateReceiptAuthorityFixture) {
			_, authority.key, _ = ed25519.GenerateKey(rand.Reader)
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, transport, authority := productionCandidateVerifierFixture(t)
			configure(transport, authority)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("substituted production probe result was accepted")
			}
		})
	}
}
