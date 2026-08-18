package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type candidateOSAuditProviderFixture struct {
	t                       *testing.T
	sessions                map[string]CandidateOSAuditStartRequest
	executed                map[string]candidateExecutedProof
	forgeProviderIdentity   bool
	forgeCapabilityIdentity bool
	forgeAuditBoolean       bool
	forgeCredentialKey      bool
	forgeAuditKey           bool
	credentialKey           ed25519.PrivateKey
	auditKey                ed25519.PrivateKey
}

type candidateExecutedProof struct {
	identity       CandidateObjectIdentity
	manifestDigest string
	topologyDigest string
	topologyError  bool
}

func (provider *candidateOSAuditProviderFixture) BeginSession(_ context.Context, request CandidateOSAuditStartRequest) (CandidateOSAuditSession, error) {
	provider.t.Helper()
	if request.Executable == nil || request.Executable.File == nil || request.Executable.Identity != request.Held.Executable || request.Executable.Digest != request.Held.ExecutableDigest || request.WorkingDirectory == nil || request.WorkingDirectory.File == nil || request.WorkingDirectory.Identity != request.Held.WorkingDirectory || request.CredentialRoot == nil || request.CredentialRoot.File == nil || request.CredentialRoot.Identity != request.Held.CredentialRoot || len(request.BusinessRoots) != len(request.Held.BusinessRoots) || request.Held.InvocationManifestDigest != digestCandidateInvocationManifest(request.Invocation) {
		return CandidateOSAuditSession{}, errors.New("held handle proof mismatch")
	}
	sessionID := fmt.Sprintf("audit-session-%d", request.ReceiptSequence)
	provider.sessions[sessionID] = request
	identity := "os-audit-provider"
	if provider.forgeProviderIdentity {
		identity = "transport-forged-principal"
	}
	return CandidateOSAuditSession{ProviderIdentity: identity, SessionID: sessionID, LaunchCapability: []byte("launch-" + sessionID), providerSeal: []byte("provider-seal-" + sessionID)}, nil
}

func (provider *candidateOSAuditProviderFixture) recordExecution(capability []byte, invocation CandidateProbeInvocation, identity CandidateObjectIdentity) {
	handles := &candidateProbeHandles{executable: invocation.Executable, credential: invocation.CredentialConfigRoot, scratchRoot: invocation.ScratchRoot, business: invocation.BusinessRepositoryRoots}
	topology, err := validateCandidateTopology(handles, invocation.WorkingDirectory)
	provider.executed[string(capability)] = candidateExecutedProof{identity: identity, manifestDigest: digestCandidateInvocationManifest(invocation), topologyDigest: topology, topologyError: err != nil}
}

func (provider *candidateOSAuditProviderFixture) VerifySession(_ context.Context, request CandidateOSAuditFinishRequest) (CandidateOSAuditAttestation, error) {
	provider.t.Helper()
	started, ok := provider.sessions[request.Session.SessionID]
	if !ok || request.Session.ProviderIdentity != "os-audit-provider" || !bytes.Equal(request.Session.providerSeal, []byte("provider-seal-"+request.Session.SessionID)) || !candidateHeldProofEqual(started.Held, request.Held) {
		return CandidateOSAuditAttestation{}, errors.New("opaque audit session is not trusted")
	}
	executed, ok := provider.executed[string(request.Session.LaunchCapability)]
	if !ok || executed.identity != request.Held.Executable || executed.manifestDigest != request.Held.InvocationManifestDigest || executed.topologyError || executed.topologyDigest != request.ExecutionTopologyDigest || started.Executable == nil || verifyBoundObjectIdentity(*started.Executable) != nil {
		return CandidateOSAuditAttestation{}, errors.New("executed object is not the held executable")
	}
	capabilityID := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%016d", started.ReceiptSequence)))
	if provider.forgeCapabilityIdentity {
		capabilityID = base64.RawURLEncoding.EncodeToString([]byte("replayed-cap-001"))
	}
	now := time.Now().UTC()
	capability := CandidateCredentialCapabilityIdentity{APIVersion: candidateReceiptAPIVersion, Kind: "QoderCredentialCapabilityIdentity", SchemaVersion: 1, ProviderIdentity: "os-credential-provider", CapabilityID: capabilityID, ProbeRunID: started.ProbeRunID, VariantID: started.VariantID, CapabilityClass: "qoder-live-probe", PolicyScopeDigest: digest("a"), IssuedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), ProviderKeyID: "credential-provider-key", ProviderKeyEpoch: 3, SignatureAlgorithm: candidateSignatureAlgorithm, SignatureEncoding: candidateSignatureEncoding}
	capability.RecordDigest = capability.digest()
	capabilityMessage, _ := capability.signingBytes()
	credentialKey := provider.credentialKey
	if provider.forgeCredentialKey {
		_, credentialKey, _ = ed25519.GenerateKey(rand.Reader)
	}
	capability.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(credentialKey, capabilityMessage))
	manifest, err := candidateInvocationManifest(started.Invocation, capability)
	if err != nil {
		return CandidateOSAuditAttestation{}, err
	}
	businessAudit := make([]string, len(started.Held.BusinessRoots))
	for index := range businessAudit {
		businessAudit[index] = digest(fmt.Sprintf("%x", index+1))
	}
	attestation := CandidateOSAuditAttestation{AuditProviderIdentity: "os-audit-provider", AuditSessionID: request.Session.SessionID, PrincipalHandleDigest: digest("d"), DenialAuditDigest: digest("2"), ExitAuditDigest: digest("3"), AncestorChainDigest: request.ExecutionTopologyDigest, BusinessRootDenialDigests: businessAudit, CredentialReadOnlyEnforced: true, BusinessRootsDenied: true, ScratchOnlyWriteEnforced: true, NetworkPolicyEnforced: true, AmbientStateDenied: true, HostIdentityDigest: digest("4"), CredentialCapability: capability, InvocationManifest: manifest}
	if provider.forgeAuditBoolean {
		attestation.ScratchOnlyWriteEnforced = false
	}
	attestation.LaunchAuditDigest = candidateOSLaunchAuditDigest(attestation.AuditProviderIdentity, attestation.AuditSessionID, attestation.PrincipalHandleDigest, request.Held)
	attestation.ProviderKeyID = "os-audit-key"
	attestation.ProviderKeyEpoch = 5
	attestation.SignatureAlgorithm = candidateSignatureAlgorithm
	attestation.SignatureEncoding = candidateSignatureEncoding
	attestation.ProviderReceiptDigest = candidateOSProviderReceiptDigest(attestation)
	auditKey := provider.auditKey
	if provider.forgeAuditKey {
		_, auditKey, _ = ed25519.GenerateKey(rand.Reader)
	}
	attestation.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(auditKey, []byte(candidateOSAuditSigningDomain+attestation.ProviderReceiptDigest)))
	return attestation, nil
}

func candidateHeldProofEqual(left, right CandidateHeldHandleProof) bool {
	if left.Executable != right.Executable || left.ExecutableDigest != right.ExecutableDigest || left.ScratchRoot != right.ScratchRoot || left.WorkingDirectory != right.WorkingDirectory || left.CredentialRoot != right.CredentialRoot || left.InvocationManifestDigest != right.InvocationManifestDigest || left.TopologyDigest != right.TopologyDigest || len(left.BusinessRoots) != len(right.BusinessRoots) {
		return false
	}
	return equalIdentities(left.BusinessRoots, right.BusinessRoots)
}

type candidateIsolationTransportFixture struct {
	t                       *testing.T
	provider                *candidateOSAuditProviderFixture
	calls                   []CandidateProbeInvocation
	executedOverride        *CandidateObjectIdentity
	topologyOverride        string
	argvSubstitution        bool
	environmentSubstitution bool
	scratchRootPath         string
	markerKind              string
	transcriptChallenge     string
	transcriptModel         string
	replaySession           bool
	beforeRecord            func(CandidateProbeInvocation)
	afterRecord             func(CandidateProbeInvocation)
}

func (transport *candidateIsolationTransportFixture) RunIsolated(_ context.Context, request CandidateIsolationRequest) (CandidateIsolationResult, error) {
	transport.t.Helper()
	invocation := request.Invocation
	if transport.argvSubstitution {
		invocation.Arguments = append(invocation.Arguments, "--substitute")
	}
	if transport.environmentSubstitution {
		invocation.Environment = append(invocation.Environment, "INHERITED_SECRET=forbidden")
	}
	transport.calls = append(transport.calls, cloneCandidateProbeInvocation(invocation))
	executed := invocation.Executable.Identity
	if transport.executedOverride != nil {
		executed = *transport.executedOverride
	}
	if transport.beforeRecord != nil {
		transport.beforeRecord(invocation)
	}
	transport.provider.recordExecution(request.LaunchCapability, invocation, executed)
	if transport.afterRecord != nil {
		transport.afterRecord(invocation)
	}
	marker := []byte(invocation.ChallengeDigest + "\n")
	switch transport.markerKind {
	case "symlink":
		if err := unix.Symlinkat("target", int(invocation.WorkingDirectory.File.Fd()), ".marshal-qoder-probe-challenge"); err != nil {
			transport.t.Fatal(err)
		}
	case "fifo":
		path := filepath.Join(transport.scratchRootPath, invocation.WorkingDirectory.File.Name(), ".marshal-qoder-probe-challenge")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			transport.t.Fatal(err)
		}
	case "oversize":
		writeMarkerAt(transport.t, invocation.WorkingDirectory.File, []byte(strings.Repeat("a", candidateMarkerLimit+1)))
	case "hardlink":
		writeMarkerNamedAt(transport.t, invocation.WorkingDirectory.File, "marker-source", marker)
		if err := unix.Linkat(int(invocation.WorkingDirectory.File.Fd()), "marker-source", int(invocation.WorkingDirectory.File.Fd()), ".marshal-qoder-probe-challenge", 0); err != nil {
			transport.t.Fatal(err)
		}
	default:
		writeMarkerAt(transport.t, invocation.WorkingDirectory.File, marker)
	}
	model := invocation.ExpectedModel
	if model == "" {
		model = "provider/default"
	}
	if transport.transcriptModel != "" {
		model = transport.transcriptModel
	}
	topology := invocation.ExpectedTopologyDigest
	if transport.topologyOverride != "" {
		topology = transport.topologyOverride
	}
	challenge := invocation.ChallengeDigest
	if transport.transcriptChallenge != "" {
		challenge = transport.transcriptChallenge
	}
	session := fmt.Sprintf("session-%d", invocation.VariantIndex)
	if transport.replaySession {
		session = "replayed-session"
	}
	return CandidateIsolationResult{Transcript: candidateTranscriptValues(session, model, challenge), ExecutionTopologyDigest: topology}, nil
}

type candidateReceiptAuthorityFixture struct {
	t              *testing.T
	identity       CandidateReceiptAuthorityIdentity
	key            ed25519.PrivateKey
	lastCompleted  time.Time
	mutate         func(*CandidateExecutionReceipt)
	documentMutate func([]byte) []byte
	auditTrust     CandidateOSAuditTrustBinding
}

func (authority *candidateReceiptAuthorityFixture) Identity(context.Context) (CandidateReceiptAuthorityIdentity, error) {
	return authority.identity, nil
}

func (authority *candidateReceiptAuthorityFixture) IssueExecutionReceipt(ctx context.Context, request CandidateReceiptRequest) ([]byte, error) {
	authority.t.Helper()
	audit, err := request.AuditProvider.VerifySession(ctx, request.AuditFinish)
	if err != nil {
		return nil, err
	}
	if err := validateCandidateOSAuditAttestation(audit, request.AuditFinish, len(request.Invocation.BusinessRepositoryRoots), authority.auditTrust); err != nil {
		return nil, err
	}
	if err := validateCandidateReceiptHeldHandles(request.Invocation, request.ExecutionTopologyDigest); err != nil {
		return nil, err
	}
	manifest := audit.InvocationManifest
	startedAt := time.Now().UTC().Add(-time.Second)
	if !authority.lastCompleted.IsZero() {
		startedAt = authority.lastCompleted.Add(time.Nanosecond)
	}
	completedAt := startedAt.Add(time.Millisecond)
	authority.lastCompleted = completedAt
	var previous *string
	if request.Invocation.ReceiptSequence > 1 {
		value := request.Invocation.PreviousReceiptDigest
		previous = &value
	}
	receiptID := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("receipt-id-%05d", request.Invocation.ReceiptSequence)))
	receipt := CandidateExecutionReceipt{
		APIVersion: candidateReceiptAPIVersion, Kind: candidateReceiptKind, SchemaVersion: candidateReceiptSchemaVersion, ReceiptID: receiptID,
		ProbeRunID: request.Invocation.ProbeRunID, ReceiptSequence: uint64(request.Invocation.ReceiptSequence), VariantID: candidateVariantID(request.Invocation.VariantIndex), ProbeRunChallengeDigest: request.Invocation.ProbeRunChallengeDigest, VariantChallengeDigest: request.Invocation.ChallengeDigest, PreviousReceiptDigest: previous,
		CandidateExecutableIdentity: candidateExecutableReceiptIdentity(request.Invocation.Executable, request.BinaryVersion), InvocationManifest: manifest, ScratchRootIdentity: candidateRootIdentity(request.Invocation.WorkingDirectory.Identity), CredentialRootIdentity: candidateRootIdentity(request.Invocation.CredentialConfigRoot.Identity), BusinessRootIdentities: candidateRootIdentities(request.Invocation.BusinessRepositoryRoots),
		IsolationProfileDigest: candidateObservedProfileDigest(), TopologyDigest: request.ExecutionTopologyDigest, HostIdentityDigest: audit.HostIdentityDigest,
		IsolationAudit: CandidateReceiptIsolationAudit{AuditProviderIdentity: audit.AuditProviderIdentity, AuditSessionID: audit.AuditSessionID, LaunchAuditDigest: audit.LaunchAuditDigest, DenialAuditDigest: audit.DenialAuditDigest, ExitAuditDigest: audit.ExitAuditDigest, AncestorChainDigest: audit.AncestorChainDigest, BusinessRootDenialDigests: audit.BusinessRootDenialDigests, CredentialReadOnlyEnforced: audit.CredentialReadOnlyEnforced, BusinessRootsDenied: audit.BusinessRootsDenied, ScratchOnlyWriteEnforced: audit.ScratchOnlyWriteEnforced, NetworkPolicyEnforced: audit.NetworkPolicyEnforced, AmbientStateDenied: audit.AmbientStateDenied},
		SessionID:      request.SessionID, ModelID: request.ObservedModel, ProtocolVersion: request.ProtocolVersion, PermissionMode: request.PermissionMode, EventContract: conformanceEventContract, TranscriptDigest: request.TranscriptDigest, MarkerDigest: request.MarkerDigest, StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano), ReceiptAuthorityKeyID: authority.identity.KeyID, ReceiptAuthorityKeyEpoch: authority.identity.KeyEpoch, SignatureAlgorithm: candidateSignatureAlgorithm, SignatureEncoding: candidateSignatureEncoding,
	}
	if authority.mutate != nil {
		authority.mutate(&receipt)
	}
	receipt.RecordDigest, err = receipt.digest()
	if err != nil {
		return nil, err
	}
	message, _ := receipt.signingBytes()
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(authority.key, message))
	document, _ := json.Marshal(receipt)
	document, _ = canonical.JSON(document)
	if authority.documentMutate != nil {
		document = authority.documentMutate(document)
	}
	return document, nil
}

func productionCandidateVerifierFixture(t *testing.T) (*CandidateLiveVerifier, CandidateLiveProbeRequest, *candidateIsolationTransportFixture, *candidateReceiptAuthorityFixture, *candidateOSAuditProviderFixture) {
	t.Helper()
	receiptPublic, receiptPrivate, _ := ed25519.GenerateKey(rand.Reader)
	credentialPublic, credentialPrivate, _ := ed25519.GenerateKey(rand.Reader)
	auditPublic, auditPrivate, _ := ed25519.GenerateKey(rand.Reader)
	verifierPublic, verifierPrivate, _ := ed25519.GenerateKey(rand.Reader)
	provider := &candidateOSAuditProviderFixture{t: t, sessions: map[string]CandidateOSAuditStartRequest{}, executed: map[string]candidateExecutedProof{}, credentialKey: credentialPrivate, auditKey: auditPrivate}
	transport := &candidateIsolationTransportFixture{t: t, provider: provider}
	authority := &candidateReceiptAuthorityFixture{t: t, identity: CandidateReceiptAuthorityIdentity{Issuer: "receipt-authority", KeyID: "receipt-key", KeyEpoch: 7}, key: receiptPrivate, auditTrust: CandidateOSAuditTrustBinding{ProviderIdentity: "os-audit-provider", ProviderKeyID: "os-audit-key", ProviderKeyEpoch: 5, PublicKey: auditPublic}}
	sandbox, err := newCandidateProductionProbeSandbox(transport, provider, authority)
	if err != nil {
		t.Fatal(err)
	}
	policy := candidateAuthorityPolicy{receiptRole: "receipt", receiptIssuer: authority.identity.Issuer, receiptKeyID: authority.identity.KeyID, receiptKeyEpoch: authority.identity.KeyEpoch, receiptPublicKey: receiptPublic, receiptLedgerTailDigest: digest("9"), credentialProviderKeyID: "credential-provider-key", credentialProviderKeyEpoch: 3, credentialProviderPublicKey: credentialPublic, verifierKeyID: "verifier-key", verifierPublicKey: verifierPublic}
	verifier, err := newCandidateLiveVerifier(sandbox, policy, verifierPrivate)
	if err != nil {
		t.Fatal(err)
	}
	request := candidateProbeRequestFixture(t)
	transport.scratchRootPath = request.ScratchRoot
	return verifier, request, transport, authority, provider
}

func TestProductionProbeUsesOpaqueOSAuditForOneChainedFourVariantRun(t *testing.T) {
	verifier, request, transport, _, _ := productionCandidateVerifierFixture(t)
	if _, _, err := verifier.Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(transport.calls))
	}
	runID := transport.calls[0].ProbeRunID
	for index, invocation := range transport.calls {
		if invocation.ProbeRunID != runID || invocation.ReceiptSequence != index+1 || invocation.VariantIndex != index || invocation.ProbeRunChallengeDigest != request.ChallengeDigest {
			t.Fatalf("variant escaped chained run: %+v", invocation)
		}
		if invocation.Executable.CanonicalPath != "" || invocation.CredentialConfigRoot.CanonicalPath != "" || invocation.ScratchRoot.CanonicalPath != "" || invocation.BusinessRepositoryRoots[0].CanonicalPath != "" {
			t.Fatal("transport received caller pathname instead of held-only objects")
		}
	}
}

func TestProductionProbeRejectsForgedPrincipalAndNonHeldExecutable(t *testing.T) {
	for name, configure := range map[string]func(*candidateIsolationTransportFixture, *candidateOSAuditProviderFixture){
		"forged principal string": func(_ *candidateIsolationTransportFixture, provider *candidateOSAuditProviderFixture) {
			provider.forgeProviderIdentity = true
		},
		"forged audit boolean": func(_ *candidateIsolationTransportFixture, provider *candidateOSAuditProviderFixture) {
			provider.forgeAuditBoolean = true
		},
		"forged opaque audit receipt": func(_ *candidateIsolationTransportFixture, provider *candidateOSAuditProviderFixture) {
			provider.forgeAuditKey = true
		},
		"non-held executable": func(transport *candidateIsolationTransportFixture, _ *candidateOSAuditProviderFixture) {
			identity := CandidateObjectIdentity{Device: 99, Inode: 100}
			transport.executedOverride = &identity
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, transport, _, provider := productionCandidateVerifierFixture(t)
			configure(transport, provider)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("forged OS authority was accepted")
			}
		})
	}
}

func TestProductionProbeRejectsReceiptSubstitutionUnknownKeyAndEpoch(t *testing.T) {
	for name, configure := range map[string]func(*candidateReceiptAuthorityFixture){
		"record field": func(authority *candidateReceiptAuthorityFixture) {
			authority.documentMutate = func(document []byte) []byte {
				var value map[string]any
				_ = json.Unmarshal(document, &value)
				value["hostIdentityDigest"] = digest("f")
				changed, _ := json.Marshal(value)
				changed, _ = canonical.JSON(changed)
				return changed
			}
		},
		"wrong key": func(authority *candidateReceiptAuthorityFixture) {
			_, authority.key, _ = ed25519.GenerateKey(rand.Reader)
		},
		"wrong epoch": func(authority *candidateReceiptAuthorityFixture) { authority.identity.KeyEpoch++ },
		"manifest": func(authority *candidateReceiptAuthorityFixture) {
			authority.mutate = func(receipt *CandidateExecutionReceipt) { receipt.InvocationManifest.VariantID = "substitute" }
		},
		"receipt chain": func(authority *candidateReceiptAuthorityFixture) {
			authority.mutate = func(receipt *CandidateExecutionReceipt) {
				value := digest("e")
				receipt.PreviousReceiptDigest = &value
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
			configure(authority)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("substituted receipt was accepted")
			}
		})
	}
}

func TestProductionProbeRejectsTopologyCredentialKeyAndCapabilityReplay(t *testing.T) {
	for name, configure := range map[string]func(*candidateIsolationTransportFixture, *candidateOSAuditProviderFixture){
		"execution topology": func(transport *candidateIsolationTransportFixture, _ *candidateOSAuditProviderFixture) {
			transport.topologyOverride = digest("f")
		},
		"credential provider key": func(_ *candidateIsolationTransportFixture, provider *candidateOSAuditProviderFixture) {
			provider.forgeCredentialKey = true
		},
		"credential capability replay": func(_ *candidateIsolationTransportFixture, provider *candidateOSAuditProviderFixture) {
			provider.forgeCapabilityIdentity = true
		},
		"argv substitution": func(transport *candidateIsolationTransportFixture, _ *candidateOSAuditProviderFixture) {
			transport.argvSubstitution = true
		},
		"ambient environment": func(transport *candidateIsolationTransportFixture, _ *candidateOSAuditProviderFixture) {
			transport.environmentSubstitution = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, transport, _, provider := productionCandidateVerifierFixture(t)
			configure(transport, provider)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("substituted execution authority was accepted")
			}
		})
	}
}

func TestProductionProbeRejectsAbnormalMarkersTranscriptAndSessionReplay(t *testing.T) {
	for name, configure := range map[string]func(*candidateIsolationTransportFixture){
		"marker symlink":       func(transport *candidateIsolationTransportFixture) { transport.markerKind = "symlink" },
		"marker fifo":          func(transport *candidateIsolationTransportFixture) { transport.markerKind = "fifo" },
		"marker oversize":      func(transport *candidateIsolationTransportFixture) { transport.markerKind = "oversize" },
		"marker hardlink":      func(transport *candidateIsolationTransportFixture) { transport.markerKind = "hardlink" },
		"transcript challenge": func(transport *candidateIsolationTransportFixture) { transport.transcriptChallenge = digest("f") },
		"transcript model":     func(transport *candidateIsolationTransportFixture) { transport.transcriptModel = "provider/substitute" },
		"session replay":       func(transport *candidateIsolationTransportFixture) { transport.replaySession = true },
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, transport, _, _ := productionCandidateVerifierFixture(t)
			configure(transport)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("abnormal isolated result was accepted")
			}
		})
	}
}

func TestProductionProbeRejectsProtectedRootNestedDuringExecutionAndSwappedBack(t *testing.T) {
	for _, target := range []string{"credential", "business"} {
		t.Run(target, func(t *testing.T) {
			verifier, request, transport, _, _ := productionCandidateVerifierFixture(t)
			var original, nested string
			transport.beforeRecord = func(invocation CandidateProbeInvocation) {
				if target == "credential" {
					original = request.CredentialConfigRoot
				} else {
					original = request.BusinessRepositoryRoots[0]
				}
				nested = filepath.Join(request.ScratchRoot, invocation.WorkingDirectory.File.Name(), "nested-"+target)
				if err := os.Rename(original, nested); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(original, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			transport.afterRecord = func(CandidateProbeInvocation) {
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(nested, original); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("execution-time protected-root nesting was accepted")
			}
		})
	}
}

func candidateTranscript(invocation CandidateProbeInvocation, model string) []byte {
	return candidateTranscriptValues(fmt.Sprintf("session-%d", invocation.VariantIndex), model, invocation.ChallengeDigest)
}

func candidateTranscriptValues(session, model, challenge string) []byte {
	return []byte(fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q,\"model\":%q,\"qodercli_version\":\"1.1.23\",\"protocol_version\":\"1.2.0\",\"permissionMode\":\"acceptEdits\"}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"challengeDigest\":%q}]}}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"terminal_reason\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n", session, model, challenge))
}

func writeMarkerAt(t *testing.T, directory *os.File, data []byte) {
	t.Helper()
	writeMarkerNamedAt(t, directory, ".marshal-qoder-probe-challenge", data)
}

func writeMarkerNamedAt(t *testing.T, directory *os.File, name string, data []byte) {
	t.Helper()
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "marker")
	_, _ = file.Write(data)
	_ = file.Close()
}
