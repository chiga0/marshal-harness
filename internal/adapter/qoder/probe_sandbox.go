package qoder

import (
	"context"
	"errors"
	"strings"
)

const candidateIsolationProfile = "qoder-probe-isolation-v1"

// CandidateIsolationPrincipal describes the OS-enforced principal used to run
// Qoder. ProcessIdentity must identify a principal distinct from the receipt
// authority; a normal child process of the verifier is not sufficient.
type CandidateIsolationPrincipal struct {
	ProviderIdentity string
	ProcessIdentity  string
	Profile          string
}

// CandidateReceiptAuthorityIdentity identifies the out-of-sandbox principal
// that turns provider audit evidence into a signed receipt. No private key or
// signing callback is accepted by the verifier or sandbox constructor.
type CandidateReceiptAuthorityIdentity struct {
	ProviderIdentity string
	ProcessIdentity  string
	Issuer           string
	KeyID            string
}

// CandidateIsolationAudit is non-sensitive evidence produced by the OS
// isolation provider. The receipt authority, not Qoder, is responsible for
// these verdicts and digests.
type CandidateIsolationAudit struct {
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
}

// CandidateIsolationRequest contains only held objects and the exact
// replacement invocation. Implementations must consume the handles and must
// not reopen CandidateBoundObject.CanonicalPath.
type CandidateIsolationRequest struct {
	Principal  CandidateIsolationPrincipal
	Invocation CandidateProbeInvocation
}

type CandidateIsolationResult struct {
	Transcript              []byte
	ExecutionTopologyDigest string
	Audit                   CandidateIsolationAudit
}

// CandidateIsolationTransport is the only production execution seam. There
// is deliberately no os/exec fallback in this package.
type CandidateIsolationTransport interface {
	Principal(context.Context) (CandidateIsolationPrincipal, error)
	RunIsolated(context.Context, CandidateIsolationRequest) (CandidateIsolationResult, error)
}

type CandidateReceiptRequest struct {
	Authority               CandidateReceiptAuthorityIdentity
	Principal               CandidateIsolationPrincipal
	Invocation              CandidateProbeInvocation
	TranscriptDigest        string
	SessionID               string
	ObservedModel           string
	BinaryVersion           string
	ProtocolVersion         string
	PermissionMode          string
	MarkerDigest            string
	ExecutionTopologyDigest string
	Audit                   CandidateIsolationAudit
}

// CandidateReceiptAuthority runs outside the Qoder principal. Implementations
// retain their signing key and return only a closed signed receipt document.
type CandidateReceiptAuthority interface {
	Identity(context.Context) (CandidateReceiptAuthorityIdentity, error)
	IssueExecutionReceipt(context.Context, CandidateReceiptRequest) ([]byte, error)
}

type candidateProductionProbeSandbox struct {
	transport CandidateIsolationTransport
	authority CandidateReceiptAuthority
}

// newCandidateProductionProbeSandbox is package-private while production
// Qoder admission remains hard-disabled. It cannot accept a receipt key and
// cannot degrade to an ordinary host subprocess.
func newCandidateProductionProbeSandbox(transport CandidateIsolationTransport, authority CandidateReceiptAuthority) (CandidateProbeSandbox, error) {
	if transport == nil || authority == nil {
		return nil, errors.New("qoder production probe isolation provider is unavailable")
	}
	return &candidateProductionProbeSandbox{transport: transport, authority: authority}, nil
}

func (sandbox *candidateProductionProbeSandbox) RunProbe(ctx context.Context, invocation CandidateProbeInvocation) (CandidateProbeResult, error) {
	if sandbox == nil || sandbox.transport == nil || sandbox.authority == nil || ctx == nil || ctx.Err() != nil {
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
	principal, err := sandbox.transport.Principal(ctx)
	if err != nil || strings.TrimSpace(principal.ProviderIdentity) == "" || strings.TrimSpace(principal.ProcessIdentity) == "" || principal.Profile != candidateIsolationProfile {
		return CandidateProbeResult{}, errors.New("qoder production probe isolation principal is not trusted")
	}
	authority, err := sandbox.authority.Identity(ctx)
	if err != nil || strings.TrimSpace(authority.ProviderIdentity) == "" || strings.TrimSpace(authority.ProcessIdentity) == "" || strings.TrimSpace(authority.Issuer) == "" || strings.TrimSpace(authority.KeyID) == "" || authority.ProcessIdentity == principal.ProcessIdentity {
		return CandidateProbeResult{}, errors.New("qoder production probe receipt authority is not independent")
	}
	requestInvocation := cloneCandidateProbeInvocation(invocation)
	result, err := sandbox.transport.RunIsolated(ctx, CandidateIsolationRequest{Principal: principal, Invocation: requestInvocation})
	if err != nil {
		return CandidateProbeResult{}, errors.New("qoder production probe isolated execution failed")
	}
	postTopology, topologyErr := validateCandidateTopology(handles, invocation.WorkingDirectory)
	if topologyErr != nil || postTopology != preTopology || result.ExecutionTopologyDigest != preTopology {
		return CandidateProbeResult{}, errors.New("qoder production probe execution topology is not continuous")
	}
	if result.Audit.AncestorChainDigest != result.ExecutionTopologyDigest {
		return CandidateProbeResult{}, errors.New("qoder production probe audit topology is substituted")
	}
	if err := validateCandidateIsolationAudit(result.Audit, len(invocation.BusinessRepositoryRoots)); err != nil {
		return CandidateProbeResult{}, err
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
	receiptRequest := CandidateReceiptRequest{
		Authority: authority, Principal: principal, Invocation: cloneCandidateProbeInvocation(invocation), TranscriptDigest: digestBytes(result.Transcript), SessionID: capture.sessionID, ObservedModel: capture.model, BinaryVersion: capture.cliVersion, ProtocolVersion: capture.protocolVersion, PermissionMode: capture.permissionMode, MarkerDigest: markerDigest, ExecutionTopologyDigest: result.ExecutionTopologyDigest, Audit: cloneCandidateIsolationAudit(result.Audit),
	}
	document, err := sandbox.authority.IssueExecutionReceipt(ctx, receiptRequest)
	if err != nil || len(document) == 0 || len(document) > candidateReceiptLimit {
		return CandidateProbeResult{}, errors.New("qoder production probe receipt authority failed")
	}
	return CandidateProbeResult{Transcript: append([]byte(nil), result.Transcript...), ExecutionReceipt: append([]byte(nil), document...), IsolationPrincipal: principal, ReceiptAuthorityIdentity: authority, IsolationAudit: cloneCandidateIsolationAudit(result.Audit)}, nil
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
	seenEnvironment := map[string]struct{}{}
	for _, entry := range invocation.Environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return errors.New("qoder production probe replacement environment is malformed")
		}
		if _, duplicate := seenEnvironment[name]; duplicate {
			return errors.New("qoder production probe replacement environment has duplicate names")
		}
		seenEnvironment[name] = struct{}{}
	}
	return nil
}

func validateCandidateIsolationAudit(audit CandidateIsolationAudit, businessRootCount int) error {
	digests := append([]string{audit.LaunchAuditDigest, audit.DenialAuditDigest, audit.ExitAuditDigest, audit.AncestorChainDigest}, audit.BusinessRootDenialDigests...)
	for _, value := range digests {
		if !validSHA256Digest(value) {
			return errors.New("qoder production probe isolation audit digest is invalid")
		}
	}
	if len(audit.BusinessRootDenialDigests) != businessRootCount || !audit.CredentialReadOnlyEnforced || !audit.BusinessRootsDenied || !audit.ScratchOnlyWriteEnforced || !audit.NetworkPolicyEnforced || !audit.AmbientStateDenied {
		return errors.New("qoder production probe isolation audit is incomplete")
	}
	seen := map[string]struct{}{}
	for _, value := range audit.BusinessRootDenialDigests {
		if _, duplicate := seen[value]; duplicate {
			return errors.New("qoder production probe business-root audit is duplicated")
		}
		seen[value] = struct{}{}
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

func cloneCandidateIsolationAudit(value CandidateIsolationAudit) CandidateIsolationAudit {
	value.BusinessRootDenialDigests = append([]string(nil), value.BusinessRootDenialDigests...)
	return value
}

func equalCandidateIsolationAudit(left, right CandidateIsolationAudit) bool {
	if left.LaunchAuditDigest != right.LaunchAuditDigest || left.DenialAuditDigest != right.DenialAuditDigest || left.ExitAuditDigest != right.ExitAuditDigest || left.AncestorChainDigest != right.AncestorChainDigest || left.CredentialReadOnlyEnforced != right.CredentialReadOnlyEnforced || left.BusinessRootsDenied != right.BusinessRootsDenied || left.ScratchOnlyWriteEnforced != right.ScratchOnlyWriteEnforced || left.NetworkPolicyEnforced != right.NetworkPolicyEnforced || left.AmbientStateDenied != right.AmbientStateDenied {
		return false
	}
	return equalStrings(left.BusinessRootDenialDigests, right.BusinessRootDenialDigests)
}
