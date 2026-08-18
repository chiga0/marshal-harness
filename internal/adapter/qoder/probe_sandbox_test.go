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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type candidateOSAuditProviderFixture struct {
	t                             *testing.T
	sessions                      map[string]CandidateOSAuditStartRequest
	executed                      map[string]candidateExecutedProof
	forgeProviderIdentity         bool
	forgeCapabilityIdentity       bool
	forgeAuditBoolean             bool
	forgeCredentialKey            bool
	forgeAuditKey                 bool
	credentialKey                 ed25519.PrivateKey
	auditKey                      ed25519.PrivateKey
	credentialMutate              func(*CandidateCredentialCapabilityIdentity)
	credentialSignatureTextMutate func(string) string
	auditSignatureTextMutate      func(string) string
	lastCredentialCapability      CandidateCredentialCapabilityIdentity
	lastAuditAttestation          CandidateOSAuditAttestation
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
	capability := CandidateCredentialCapabilityIdentity{APIVersion: candidateReceiptAPIVersion, Kind: "QoderCredentialCapabilityIdentity", SchemaVersion: 1, ProviderIdentity: "os-credential-provider", CapabilityID: capabilityID, ProbeRunID: started.ProbeRunID, VariantID: started.VariantID, CapabilityClass: "qoder-live-probe", PolicyScopeDigest: digest("a"), IssuedAt: candidateExactTimestamp(now.Add(-time.Minute)), ExpiresAt: candidateExactTimestamp(now.Add(time.Minute)), ProviderKeyID: "credential-provider-key", ProviderKeyEpoch: 3, SignatureAlgorithm: candidateSignatureAlgorithm, SignatureEncoding: candidateSignatureEncoding}
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
	credentialChanged := false
	if provider.credentialMutate != nil {
		provider.credentialMutate(&capability)
		capability.RecordDigest = capability.digest()
		capabilityMessage, _ = capability.signingBytes()
		capability.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(credentialKey, capabilityMessage))
		credentialChanged = true
	}
	if provider.credentialSignatureTextMutate != nil {
		capability.Signature = provider.credentialSignatureTextMutate(capability.Signature)
		credentialChanged = true
	}
	if credentialChanged {
		for index := range manifest.EnvironmentManifest.Entries {
			if manifest.EnvironmentManifest.Entries[index].Source == "credential-capability" {
				copy := capability
				manifest.EnvironmentManifest.Entries[index].CapabilityIdentity = &copy
			}
		}
		manifest.EnvironmentManifest.ManifestDigest = digestRecordWithoutFields(manifest.EnvironmentManifest, "manifestDigest")
		manifest.ManifestDigest = digestRecordWithoutFields(manifest, "manifestDigest")
	}
	provider.lastCredentialCapability = capability
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
	if provider.auditSignatureTextMutate != nil {
		attestation.Signature = provider.auditSignatureTextMutate(attestation.Signature)
	}
	provider.lastAuditAttestation = attestation
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
	t                   *testing.T
	identity            CandidateReceiptAuthorityIdentity
	key                 ed25519.PrivateKey
	lastCompleted       time.Time
	mutate              func(*CandidateExecutionReceipt)
	signatureTextMutate func(string) string
	documentMutate      func([]byte) []byte
	auditTrust          CandidateOSAuditTrustBinding
	lastReceipt         CandidateExecutionReceipt
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
		SessionID:      request.SessionID, ModelID: request.ObservedModel, ProtocolVersion: request.ProtocolVersion, PermissionMode: request.PermissionMode, EventContract: conformanceEventContract, TranscriptDigest: request.TranscriptDigest, MarkerDigest: request.MarkerDigest, StartedAt: candidateExactTimestamp(startedAt), CompletedAt: candidateExactTimestamp(completedAt), ReceiptAuthorityKeyID: authority.identity.KeyID, ReceiptAuthorityKeyEpoch: authority.identity.KeyEpoch, SignatureAlgorithm: candidateSignatureAlgorithm, SignatureEncoding: candidateSignatureEncoding,
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
	if authority.signatureTextMutate != nil {
		receipt.Signature = authority.signatureTextMutate(receipt.Signature)
	}
	authority.lastReceipt = receipt
	document, _ := json.Marshal(receipt)
	document, _ = canonical.JSON(document)
	if authority.documentMutate != nil {
		document = authority.documentMutate(document)
	}
	return document, nil
}

func candidateExactTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func resignCandidateManifest(manifest *CandidateVariantInvocationManifest) {
	manifest.ArgvManifest.ManifestDigest = digestRecordWithoutFields(manifest.ArgvManifest, "manifestDigest")
	manifest.EnvironmentManifest.ManifestDigest = digestRecordWithoutFields(manifest.EnvironmentManifest, "manifestDigest")
	manifest.ManifestDigest = digestRecordWithoutFields(*manifest, "manifestDigest")
}

func assertTrustedInvalidReceiptWasSigned(t *testing.T, authority *candidateReceiptAuthorityFixture) {
	t.Helper()
	receipt := authority.lastReceipt
	message, err := receipt.signingBytes()
	if err != nil {
		t.Fatalf("invalid receipt did not retain a canonical signing message: %v", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(receipt.Signature)
	if err != nil || receipt.RecordDigest != receipt.digestForTest(t) || !ed25519.Verify(authority.key.Public().(ed25519.PublicKey), message, signature) {
		t.Fatal("invalid receipt was not signed by the trusted receipt key")
	}
}

func (receipt CandidateExecutionReceipt) digestForTest(t *testing.T) string {
	t.Helper()
	digest, err := receipt.digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertTrustedInvalidCapabilityWasSigned(t *testing.T, provider *candidateOSAuditProviderFixture) {
	t.Helper()
	capability := provider.lastCredentialCapability
	message, err := capability.signingBytes()
	if err != nil {
		t.Fatalf("invalid capability did not retain a canonical signing message: %v", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(capability.Signature)
	if err != nil || capability.RecordDigest != capability.digest() || !ed25519.Verify(provider.credentialKey.Public().(ed25519.PublicKey), message, signature) {
		t.Fatal("invalid capability was not signed by the trusted credential provider key")
	}
}

func assertTrustedInvalidAuditWasSigned(t *testing.T, provider *candidateOSAuditProviderFixture) {
	t.Helper()
	attestation := provider.lastAuditAttestation
	signature, err := base64.RawURLEncoding.DecodeString(attestation.Signature)
	if err != nil || attestation.ProviderReceiptDigest != candidateOSProviderReceiptDigest(attestation) || !ed25519.Verify(provider.auditKey.Public().(ed25519.PublicKey), []byte(candidateOSAuditSigningDomain+attestation.ProviderReceiptDigest), signature) {
		t.Fatal("invalid OS audit attestation was not signed by the trusted audit key")
	}
}

func nonCanonicalRawURLSameBytes(t *testing.T, canonicalText string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(canonicalText)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, replacement := range alphabet {
		if byte(replacement) == canonicalText[len(canonicalText)-1] {
			continue
		}
		candidate := canonicalText[:len(canonicalText)-1] + string(replacement)
		candidateDecoded, candidateErr := base64.RawURLEncoding.DecodeString(candidate)
		if candidateErr != nil || !bytes.Equal(candidateDecoded, decoded) {
			continue
		}
		if _, strictErr := base64.RawURLEncoding.Strict().DecodeString(candidate); strictErr != nil {
			return candidate
		}
	}
	t.Fatal("could not construct non-canonical raw-url text for the same bytes")
	return ""
}

func sortCandidateBusinessRootsByReceiptIdentity(t *testing.T, paths []string) {
	t.Helper()
	type orderedRoot struct {
		path      string
		canonical []byte
	}
	roots := make([]orderedRoot, 0, len(paths))
	for _, path := range paths {
		object, err := openCandidateObject(path, true)
		if err != nil {
			t.Fatal(err)
		}
		identity := candidateRootIdentity(object.Identity)
		canonical, err := canonical.JSON(mustCandidateJSON(identity))
		_ = object.File.Close()
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, orderedRoot{path: path, canonical: canonical})
	}
	sort.Slice(roots, func(left, right int) bool { return bytes.Compare(roots[left].canonical, roots[right].canonical) < 0 })
	for index := range roots {
		paths[index] = roots[index].path
	}
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

func TestProductionProbeRejectsTrustedSignedInvalidReceiptScalars(t *testing.T) {
	tests := []struct {
		name     string
		twoRoots bool
		mutate   func(*CandidateExecutionReceipt)
	}{
		{name: "NUL id", mutate: func(receipt *CandidateExecutionReceipt) { receipt.SessionID = "session\x00id" }},
		{name: "non ASCII id", mutate: func(receipt *CandidateExecutionReceipt) { receipt.ModelID = "modèle" }},
		{name: "overlong enum", mutate: func(receipt *CandidateExecutionReceipt) { receipt.ProtocolVersion = strings.Repeat("p", 257) }},
		{name: "epoch above JSON integer bound", mutate: func(receipt *CandidateExecutionReceipt) { receipt.ReceiptAuthorityKeyEpoch = 1 << 63 }},
		{name: "offset timestamp", mutate: func(receipt *CandidateExecutionReceipt) { receipt.StartedAt = "2026-08-18T00:00:00+00:00" }},
		{name: "short fractional timestamp", mutate: func(receipt *CandidateExecutionReceipt) { receipt.CompletedAt = "2026-08-18T00:00:00.123Z" }},
		{name: "unsorted digest array", twoRoots: true, mutate: func(receipt *CandidateExecutionReceipt) {
			receipt.IsolationAudit.BusinessRootDenialDigests = []string{digest("b"), digest("a")}
		}},
		{name: "duplicate digest array", twoRoots: true, mutate: func(receipt *CandidateExecutionReceipt) {
			receipt.IsolationAudit.BusinessRootDenialDigests = []string{digest("a"), digest("a")}
		}},
		{name: "non NFC argv literal", mutate: func(receipt *CandidateExecutionReceipt) {
			literal := "e\u0301"
			receipt.InvocationManifest.ArgvManifest.Entries[0].LiteralValue = &literal
			receipt.InvocationManifest.ArgvManifest.Entries[0].ValueDigest = digestBytes([]byte(literal))
			resignCandidateManifest(&receipt.InvocationManifest)
		}},
		{name: "NUL argv literal", mutate: func(receipt *CandidateExecutionReceipt) {
			literal := "bad\x00literal"
			receipt.InvocationManifest.ArgvManifest.Entries[0].LiteralValue = &literal
			receipt.InvocationManifest.ArgvManifest.Entries[0].ValueDigest = digestBytes([]byte(literal))
			resignCandidateManifest(&receipt.InvocationManifest)
		}},
		{name: "overlong argv literal", mutate: func(receipt *CandidateExecutionReceipt) {
			literal := strings.Repeat("x", 4097)
			receipt.InvocationManifest.ArgvManifest.Entries[0].LiteralValue = &literal
			receipt.InvocationManifest.ArgvManifest.Entries[0].ValueDigest = digestBytes([]byte(literal))
			resignCandidateManifest(&receipt.InvocationManifest)
		}},
		{name: "source representation mismatch", mutate: func(receipt *CandidateExecutionReceipt) {
			receipt.InvocationManifest.ArgvManifest.Entries[0].Source = "model-id"
			resignCandidateManifest(&receipt.InvocationManifest)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
			if test.twoRoots {
				request.BusinessRepositoryRoots = append(request.BusinessRepositoryRoots, realPrivateTempDir(t))
				sortCandidateBusinessRootsByReceiptIdentity(t, request.BusinessRepositoryRoots)
			}
			authority.mutate = test.mutate
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("trusted signed receipt with invalid exact contract was accepted")
			}
			assertTrustedInvalidReceiptWasSigned(t, authority)
		})
	}
}

func TestProductionProbeRejectsTrustedSignedInvalidCredentialCapabilityScalars(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CandidateCredentialCapabilityIdentity)
	}{
		{name: "NUL provider id", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.ProviderIdentity = "provider\x00id" }},
		{name: "non NFC probe run id", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.ProbeRunID = "e\u0301" }},
		{name: "overlong class", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.CapabilityClass = strings.Repeat("c", 257) }},
		{name: "epoch above JSON integer bound", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.ProviderKeyEpoch = 1 << 63 }},
		{name: "short fractional issued time", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.IssuedAt = "2026-08-18T00:00:00.123Z" }},
		{name: "offset expiry time", mutate: func(value *CandidateCredentialCapabilityIdentity) { value.ExpiresAt = "2026-08-18T01:00:00+00:00" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier, request, _, _, provider := productionCandidateVerifierFixture(t)
			provider.credentialMutate = test.mutate
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("trusted signed credential capability with invalid exact contract was accepted")
			}
			assertTrustedInvalidCapabilityWasSigned(t, provider)
		})
	}
}

func TestProductionProbeRejectsNonCanonicalReceiptBase64URL(t *testing.T) {
	t.Run("receipt id nonzero trailing bits", func(t *testing.T) {
		verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
		authority.mutate = func(receipt *CandidateExecutionReceipt) {
			receipt.ReceiptID = nonCanonicalRawURLSameBytes(t, receipt.ReceiptID)
		}
		if _, _, err := verifier.Verify(context.Background(), request); err == nil {
			t.Fatal("trusted signed receipt with non-canonical receipt id was accepted")
		}
		assertTrustedInvalidReceiptWasSigned(t, authority)
	})
	t.Run("realpath bytes line break", func(t *testing.T) {
		verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
		authority.mutate = func(receipt *CandidateExecutionReceipt) {
			receipt.CandidateExecutableIdentity.RealpathBytes.Bytes = receipt.CandidateExecutableIdentity.RealpathBytes.Bytes[:4] + "\r\n" + receipt.CandidateExecutableIdentity.RealpathBytes.Bytes[4:]
		}
		if _, _, err := verifier.Verify(context.Background(), request); err == nil {
			t.Fatal("trusted signed receipt with line-broken realpath bytes was accepted")
		}
		assertTrustedInvalidReceiptWasSigned(t, authority)
	})
	for name, mutate := range map[string]func(*testing.T, string) string{
		"signature line break":            func(_ *testing.T, value string) string { return value[:4] + "\r\n" + value[4:] },
		"signature nonzero trailing bits": nonCanonicalRawURLSameBytes,
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
			authority.signatureTextMutate = func(value string) string { return mutate(t, value) }
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("trusted receipt with non-canonical signature text was accepted")
			}
			assertTrustedInvalidReceiptWasSigned(t, authority)
		})
	}
}

func TestProductionProbeRejectsNonCanonicalCapabilityBase64URL(t *testing.T) {
	t.Run("capability id nonzero trailing bits", func(t *testing.T) {
		verifier, request, _, _, provider := productionCandidateVerifierFixture(t)
		provider.credentialMutate = func(capability *CandidateCredentialCapabilityIdentity) {
			capability.CapabilityID = nonCanonicalRawURLSameBytes(t, capability.CapabilityID)
		}
		if _, _, err := verifier.Verify(context.Background(), request); err == nil {
			t.Fatal("trusted capability with non-canonical id was accepted")
		}
		assertTrustedInvalidCapabilityWasSigned(t, provider)
	})
	for name, mutate := range map[string]func(*testing.T, string) string{
		"signature line break":            func(_ *testing.T, value string) string { return value[:4] + "\r\n" + value[4:] },
		"signature nonzero trailing bits": nonCanonicalRawURLSameBytes,
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, _, _, provider := productionCandidateVerifierFixture(t)
			provider.credentialSignatureTextMutate = func(value string) string { return mutate(t, value) }
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("trusted capability with non-canonical signature text was accepted")
			}
			assertTrustedInvalidCapabilityWasSigned(t, provider)
		})
	}
}

func TestProductionProbeRejectsNonCanonicalOSAuditSignature(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string) string{
		"line break":            func(_ *testing.T, value string) string { return value[:4] + "\r\n" + value[4:] },
		"nonzero trailing bits": nonCanonicalRawURLSameBytes,
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, _, _, provider := productionCandidateVerifierFixture(t)
			provider.auditSignatureTextMutate = func(value string) string { return mutate(t, value) }
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("trusted OS audit attestation with non-canonical signature text was accepted")
			}
			assertTrustedInvalidAuditWasSigned(t, provider)
		})
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
