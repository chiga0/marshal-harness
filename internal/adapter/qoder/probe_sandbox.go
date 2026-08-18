package qoder

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const candidateIsolationProfile = "qoder-probe-isolation-v1"
const candidateOSAuditSigningDomain = "marshal-qoder-os-audit-v1\x00"

// CandidateOSAuditSession is created by the OS audit provider before launch.
// Its opaque capability is consumable by the isolation transport, but only
// the provider can validate it or produce the corresponding audit receipt.
type CandidateOSAuditSession struct {
	ProviderIdentity string
	SessionID        string
	LaunchCapability []byte
	providerSeal     []byte
}

type CandidateHeldHandleProof struct {
	Executable               CandidateObjectIdentity
	ExecutableDigest         string
	ScratchRoot              CandidateObjectIdentity
	WorkingDirectory         CandidateObjectIdentity
	CredentialRoot           CandidateObjectIdentity
	BusinessRoots            []CandidateObjectIdentity
	InvocationManifestDigest string
	TopologyDigest           string
}

type CandidateOSAuditStartRequest struct {
	ProbeRunID       string
	ReceiptSequence  int
	VariantID        string
	Held             CandidateHeldHandleProof
	Executable       *CandidateBoundObject
	ScratchRoot      *CandidateBoundObject
	WorkingDirectory *CandidateBoundObject
	CredentialRoot   *CandidateBoundObject
	BusinessRoots    []CandidateBoundObject
	Invocation       CandidateProbeInvocation
}

type CandidateOSAuditFinishRequest struct {
	Session                 CandidateOSAuditSession
	Held                    CandidateHeldHandleProof
	Invocation              CandidateProbeInvocation
	TranscriptDigest        string
	MarkerDigest            string
	ExecutionTopologyDigest string
}

// CandidateOSAuditAttestation is produced only after the provider has checked
// its opaque launch session against the same held handles at exit. It contains
// no credential material or caller-supplied verdicts.
type CandidateOSAuditAttestation struct {
	AuditProviderIdentity      string
	AuditSessionID             string
	PrincipalHandleDigest      string
	LaunchAuditDigest          string
	DenialAuditDigest          string
	ExitAuditDigest            string
	AncestorChainDigest        string
	BusinessRootDenialDigests  []string
	CredentialReadOnlyEnforced bool
	BusinessRootsDenied        bool
	ScratchOnlyWriteEnforced   bool
	NetworkPolicyEnforced      bool
	AmbientStateDenied         bool
	HostIdentityDigest         string
	ProviderReceiptDigest      string
	ProviderKeyID              string
	ProviderKeyEpoch           uint64
	SignatureAlgorithm         string
	SignatureEncoding          string
	Signature                  string
	CredentialCapability       CandidateCredentialCapabilityIdentity
	InvocationManifest         CandidateVariantInvocationManifest
}

type CandidateOSAuditTrustBinding struct {
	ProviderIdentity string
	ProviderKeyID    string
	ProviderKeyEpoch uint64
	PublicKey        ed25519.PublicKey
}

// CandidateOSAuditProvider owns the unforgeable audit session and OS
// principal attestation. The transport cannot return either authority value.
type CandidateOSAuditProvider interface {
	BeginSession(context.Context, CandidateOSAuditStartRequest) (CandidateOSAuditSession, error)
	VerifySession(context.Context, CandidateOSAuditFinishRequest) (CandidateOSAuditAttestation, error)
}

type CandidateIsolationRequest struct {
	LaunchCapability []byte
	Invocation       CandidateProbeInvocation
}

// CandidateIsolationResult deliberately has no principal or audit fields;
// those are queried independently from CandidateOSAuditProvider.
type CandidateIsolationResult struct {
	Transcript              []byte
	ExecutionTopologyDigest string
}

// CandidateIsolationTransport is the only execution seam. There is no
// ordinary host subprocess fallback in this package.
type CandidateIsolationTransport interface {
	RunIsolated(context.Context, CandidateIsolationRequest) (CandidateIsolationResult, error)
}

type CandidateReceiptAuthorityIdentity struct {
	Issuer   string
	KeyID    string
	KeyEpoch uint64
}

type CandidateReceiptRequest struct {
	Authority               CandidateReceiptAuthorityIdentity
	AuditProvider           CandidateOSAuditProvider
	AuditSession            CandidateOSAuditSession
	AuditFinish             CandidateOSAuditFinishRequest
	Invocation              CandidateProbeInvocation
	TranscriptDigest        string
	SessionID               string
	ObservedModel           string
	BinaryVersion           string
	ProtocolVersion         string
	PermissionMode          string
	MarkerDigest            string
	ExecutionTopologyDigest string
}

// CandidateReceiptAuthority runs outside the Qoder principal. A conforming
// implementation calls AuditProvider.VerifySession itself and signs only the
// returned attestation plus the held invocation. No signing key is accepted by
// this constructor or exposed to the verifier.
type CandidateReceiptAuthority interface {
	Identity(context.Context) (CandidateReceiptAuthorityIdentity, error)
	IssueExecutionReceipt(context.Context, CandidateReceiptRequest) ([]byte, error)
}

type candidateProductionProbeSandbox struct {
	transport     CandidateIsolationTransport
	auditProvider CandidateOSAuditProvider
	authority     CandidateReceiptAuthority
}

func newCandidateProductionProbeSandbox(transport CandidateIsolationTransport, auditProvider CandidateOSAuditProvider, authority CandidateReceiptAuthority) (CandidateProbeSandbox, error) {
	if transport == nil || auditProvider == nil || authority == nil {
		return nil, errors.New("qoder production probe isolation authority is unavailable")
	}
	return &candidateProductionProbeSandbox{transport: transport, auditProvider: auditProvider, authority: authority}, nil
}

func (sandbox *candidateProductionProbeSandbox) RunProbe(ctx context.Context, invocation CandidateProbeInvocation) (CandidateProbeResult, error) {
	if sandbox == nil || sandbox.transport == nil || sandbox.auditProvider == nil || sandbox.authority == nil || ctx == nil || ctx.Err() != nil {
		return CandidateProbeResult{}, errors.New("qoder production probe sandbox is unavailable")
	}
	if err := validateCandidateIsolationInvocation(invocation); err != nil {
		return CandidateProbeResult{}, err
	}
	handles := &candidateProbeHandles{executable: invocation.Executable, credential: invocation.CredentialConfigRoot, scratchRoot: invocation.ScratchRoot, business: invocation.BusinessRepositoryRoots}
	preTopology, err := validateCandidateTopology(handles, invocation.WorkingDirectory)
	if err != nil || preTopology != invocation.ExpectedTopologyDigest {
		return CandidateProbeResult{}, errors.New("qoder production probe pre-execution topology is invalid")
	}
	held := candidateHeldHandleProof(invocation, preTopology)
	start := CandidateOSAuditStartRequest{
		ProbeRunID: invocation.ProbeRunID, ReceiptSequence: invocation.ReceiptSequence, VariantID: candidateVariantID(invocation.VariantIndex), Held: held,
		Executable: &invocation.Executable, ScratchRoot: &invocation.ScratchRoot, WorkingDirectory: &invocation.WorkingDirectory, CredentialRoot: &invocation.CredentialConfigRoot, BusinessRoots: append([]CandidateBoundObject(nil), invocation.BusinessRepositoryRoots...),
		Invocation: cloneCandidateProbeInvocation(invocation),
	}
	auditSession, err := sandbox.auditProvider.BeginSession(ctx, start)
	if err != nil || strings.TrimSpace(auditSession.ProviderIdentity) == "" || strings.TrimSpace(auditSession.SessionID) == "" || len(auditSession.LaunchCapability) == 0 || len(auditSession.providerSeal) == 0 {
		return CandidateProbeResult{}, errors.New("qoder production probe OS audit session is unavailable")
	}
	result, err := sandbox.transport.RunIsolated(ctx, CandidateIsolationRequest{LaunchCapability: append([]byte(nil), auditSession.LaunchCapability...), Invocation: cloneCandidateProbeInvocationForTransport(invocation)})
	if err != nil {
		return CandidateProbeResult{}, errors.New("qoder production probe isolated execution failed")
	}
	postTopology, topologyErr := validateCandidateTopology(handles, invocation.WorkingDirectory)
	if topologyErr != nil || postTopology != preTopology || result.ExecutionTopologyDigest != preTopology {
		return CandidateProbeResult{}, errors.New("qoder production probe execution topology is not continuous")
	}
	if len(result.Transcript) == 0 || len(result.Transcript) > maxResultBytes {
		return CandidateProbeResult{}, errors.New("qoder production probe transcript is empty or oversized")
	}
	capture := decodeTranscript(result.Transcript)
	if capture.err != nil || !capture.terminal.seen || !capture.terminal.success {
		return CandidateProbeResult{}, errors.New("qoder production probe transcript contract failed")
	}
	markerDigest, err := readCandidateMarker(ctx, int(invocation.WorkingDirectory.File.Fd()), invocation.ChallengeDigest)
	if err != nil {
		return CandidateProbeResult{}, err
	}
	postMarkerTopology, topologyErr := validateCandidateTopology(handles, invocation.WorkingDirectory)
	if topologyErr != nil || postMarkerTopology != preTopology {
		return CandidateProbeResult{}, errors.New("qoder production probe topology changed during marker verification")
	}
	authority, err := sandbox.authority.Identity(ctx)
	if err != nil || strings.TrimSpace(authority.Issuer) == "" || strings.TrimSpace(authority.KeyID) == "" {
		return CandidateProbeResult{}, errors.New("qoder production probe receipt authority is unavailable")
	}
	finish := CandidateOSAuditFinishRequest{Session: cloneCandidateOSAuditSession(auditSession), Held: held, Invocation: cloneCandidateProbeInvocation(invocation), TranscriptDigest: digestBytes(result.Transcript), MarkerDigest: markerDigest, ExecutionTopologyDigest: result.ExecutionTopologyDigest}
	receiptRequest := CandidateReceiptRequest{
		Authority: authority, AuditProvider: sandbox.auditProvider, AuditSession: cloneCandidateOSAuditSession(auditSession), AuditFinish: finish, Invocation: cloneCandidateProbeInvocation(invocation), TranscriptDigest: finish.TranscriptDigest, SessionID: capture.sessionID, ObservedModel: capture.model, BinaryVersion: capture.cliVersion, ProtocolVersion: capture.protocolVersion, PermissionMode: capture.permissionMode, MarkerDigest: markerDigest, ExecutionTopologyDigest: result.ExecutionTopologyDigest,
	}
	document, err := sandbox.authority.IssueExecutionReceipt(ctx, receiptRequest)
	if err != nil || len(document) == 0 || len(document) > candidateReceiptLimit {
		return CandidateProbeResult{}, errors.New("qoder production probe receipt authority failed")
	}
	return CandidateProbeResult{Transcript: append([]byte(nil), result.Transcript...), ExecutionReceipt: append([]byte(nil), document...), AuthorityBacked: true}, nil
}

func candidateHeldHandleProof(invocation CandidateProbeInvocation, topology string) CandidateHeldHandleProof {
	return CandidateHeldHandleProof{Executable: invocation.Executable.Identity, ExecutableDigest: invocation.Executable.Digest, ScratchRoot: invocation.ScratchRoot.Identity, WorkingDirectory: invocation.WorkingDirectory.Identity, CredentialRoot: invocation.CredentialConfigRoot.Identity, BusinessRoots: objectIdentities(invocation.BusinessRepositoryRoots), InvocationManifestDigest: invocation.InvocationManifestDigest, TopologyDigest: topology}
}

func validateCandidateIsolationInvocation(invocation CandidateProbeInvocation) error {
	if strings.TrimSpace(invocation.ProbeRunID) == "" || invocation.ReceiptSequence < 1 || invocation.ReceiptSequence > 4 || invocation.VariantIndex != invocation.ReceiptSequence-1 || !validSHA256Digest(invocation.InvocationDigest) || !validSHA256Digest(invocation.InvocationManifestDigest) || !validSHA256Digest(invocation.ChallengeDigest) || !validSHA256Digest(invocation.ExpectedTopologyDigest) {
		return errors.New("qoder production probe invocation identity is invalid")
	}
	if invocation.ReceiptSequence == 1 {
		if invocation.PreviousReceiptDigest != "" {
			return errors.New("qoder production probe receipt chain does not start at genesis")
		}
	} else if !validSHA256Digest(invocation.PreviousReceiptDigest) {
		return errors.New("qoder production probe receipt chain is incomplete")
	}
	if invocation.InvocationManifestDigest != digestCandidateInvocationManifest(invocation) || invocation.InvocationDigest != digestCandidateInvocation(invocation) {
		return errors.New("qoder production probe invocation manifest is substituted")
	}
	objects := append([]CandidateBoundObject{invocation.Executable, invocation.WorkingDirectory, invocation.ScratchRoot, invocation.CredentialConfigRoot}, invocation.BusinessRepositoryRoots...)
	for _, object := range objects {
		if object.File == nil || verifyBoundObjectIdentity(object) != nil {
			return errors.New("qoder production probe requires held object handles")
		}
	}
	if len(invocation.WritableRoots) != 1 || invocation.WritableRoots[0].Identity != invocation.WorkingDirectory.Identity || len(invocation.BusinessRepositoryRoots) == 0 {
		return errors.New("qoder production probe write and deny roots are invalid")
	}
	if !equalStrings(invocation.Arguments, buildArgs(invocation.ExpectedModel, candidateBoundCredentialToken, candidateBoundScratchToken, invocation.VariantIndex >= 2)) || !equalStrings(invocation.Environment, candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken)) {
		return errors.New("qoder production probe argv or replacement environment is not exact")
	}
	return nil
}

func cloneCandidateProbeInvocation(value CandidateProbeInvocation) CandidateProbeInvocation {
	value.Arguments = append([]string(nil), value.Arguments...)
	value.Environment = append([]string(nil), value.Environment...)
	value.WritableRoots = append([]CandidateBoundObject(nil), value.WritableRoots...)
	value.BusinessRepositoryRoots = append([]CandidateBoundObject(nil), value.BusinessRepositoryRoots...)
	value.Prompt = append([]byte(nil), value.Prompt...)
	return value
}

func cloneCandidateProbeInvocationForTransport(value CandidateProbeInvocation) CandidateProbeInvocation {
	value = cloneCandidateProbeInvocation(value)
	value.Executable.CanonicalPath = ""
	value.WorkingDirectory.CanonicalPath = ""
	value.ScratchRoot.CanonicalPath = ""
	value.CredentialConfigRoot.CanonicalPath = ""
	for index := range value.WritableRoots {
		value.WritableRoots[index].CanonicalPath = ""
	}
	for index := range value.BusinessRepositoryRoots {
		value.BusinessRepositoryRoots[index].CanonicalPath = ""
	}
	return value
}

func cloneCandidateOSAuditSession(value CandidateOSAuditSession) CandidateOSAuditSession {
	value.LaunchCapability = append([]byte(nil), value.LaunchCapability...)
	value.providerSeal = append([]byte(nil), value.providerSeal...)
	return value
}

// validateCandidateOSAuditAttestation is the receipt-authority side of the
// contract. It binds the provider's principal attestation to the opaque
// session and exact held-handle proof instead of accepting transport verdicts.
func validateCandidateOSAuditAttestation(value CandidateOSAuditAttestation, finish CandidateOSAuditFinishRequest, businessRootCount int, trust CandidateOSAuditTrustBinding) error {
	if value.AuditProviderIdentity != finish.Session.ProviderIdentity || value.AuditSessionID != finish.Session.SessionID || !validSHA256Digest(value.PrincipalHandleDigest) || !validSHA256Digest(value.HostIdentityDigest) || value.AncestorChainDigest != finish.ExecutionTopologyDigest || len(value.BusinessRootDenialDigests) != businessRootCount || !value.CredentialReadOnlyEnforced || !value.BusinessRootsDenied || !value.ScratchOnlyWriteEnforced || !value.NetworkPolicyEnforced || !value.AmbientStateDenied {
		return errors.New("qoder OS audit attestation identity is invalid")
	}
	expectedLaunch := candidateOSLaunchAuditDigest(value.AuditProviderIdentity, value.AuditSessionID, value.PrincipalHandleDigest, finish.Held)
	if value.LaunchAuditDigest != expectedLaunch {
		return errors.New("qoder OS audit principal is not bound to held handles")
	}
	for _, digest := range append([]string{value.LaunchAuditDigest, value.DenialAuditDigest, value.ExitAuditDigest, value.AncestorChainDigest}, value.BusinessRootDenialDigests...) {
		if !validSHA256Digest(digest) {
			return errors.New("qoder OS audit digest is invalid")
		}
	}
	expectedReceipt := candidateOSProviderReceiptDigest(value)
	if value.ProviderReceiptDigest != expectedReceipt {
		return errors.New("qoder OS audit provider receipt is invalid")
	}
	signature, signatureErr := base64.RawURLEncoding.DecodeString(value.Signature)
	if value.AuditProviderIdentity != trust.ProviderIdentity || value.ProviderKeyID != trust.ProviderKeyID || value.ProviderKeyEpoch != trust.ProviderKeyEpoch || value.SignatureAlgorithm != candidateSignatureAlgorithm || value.SignatureEncoding != candidateSignatureEncoding || len(trust.PublicKey) != ed25519.PublicKeySize || signatureErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(trust.PublicKey, []byte(candidateOSAuditSigningDomain+value.ProviderReceiptDigest), signature) {
		return errors.New("qoder OS audit provider signature is not trusted")
	}
	expectedManifest, err := candidateInvocationManifest(finish.Invocation, value.CredentialCapability)
	if err != nil || !candidateManifestsEqual(value.InvocationManifest, expectedManifest) {
		return errors.New("qoder OS audit invocation manifest is invalid")
	}
	return nil
}

func validateCandidateReceiptHeldHandles(invocation CandidateProbeInvocation, executionTopologyDigest string) error {
	if err := validateCandidateIsolationInvocation(invocation); err != nil {
		return err
	}
	handles := &candidateProbeHandles{executable: invocation.Executable, credential: invocation.CredentialConfigRoot, scratchRoot: invocation.ScratchRoot, business: invocation.BusinessRepositoryRoots}
	topology, err := validateCandidateTopology(handles, invocation.WorkingDirectory)
	if err != nil || topology != executionTopologyDigest || topology != invocation.ExpectedTopologyDigest {
		return errors.New("qoder receipt authority held-handle topology is invalid")
	}
	return nil
}

func candidateOSLaunchAuditDigest(providerIdentity, sessionID, principalHandleDigest string, held CandidateHeldHandleProof) string {
	data, _ := json.Marshal(map[string]any{"providerIdentity": providerIdentity, "sessionId": sessionID, "principalHandleDigest": principalHandleDigest, "held": held})
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData)
}

func candidateOSProviderReceiptDigest(value CandidateOSAuditAttestation) string {
	value.ProviderReceiptDigest = ""
	value.Signature = ""
	data, _ := json.Marshal(value)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	delete(object, "ProviderReceiptDigest")
	delete(object, "providerReceiptDigest")
	delete(object, "Signature")
	delete(object, "signature")
	data, _ = json.Marshal(object)
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData)
}
