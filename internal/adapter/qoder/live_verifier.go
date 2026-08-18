package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
)

const (
	candidateEvidenceClassLive     = "credentialed-live"
	candidateEvidenceClassHermetic = "hermetic-fixture"
	candidateReceiptKind           = "qoder-probe-execution-receipt-v1"
	candidateReceiptLimit          = 64 << 10
	candidateMarkerLimit           = 256
	candidateBoundScratchToken     = "$BOUND_SCRATCH_DIR"
	candidateBoundCredentialToken  = "$BOUND_CREDENTIAL_DIR"
	candidateReceiptMaxExecution   = 30 * time.Minute
)

type CandidateObjectIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type CandidateBoundObject struct {
	File          *os.File
	CanonicalPath string
	Identity      CandidateObjectIdentity
	Digest        string
}

// CandidateProbeSandbox is an independently attested execution boundary.
// It must consume the held handles (never reopen CanonicalPath), execute the
// held executable identity without a shell, replace the environment exactly,
// and enforce the write/deny handle policy. It signs an actual receipt with a
// key unavailable to both this verifier and the evidence signer.
type CandidateProbeSandbox interface {
	RunProbe(context.Context, CandidateProbeInvocation) (CandidateProbeResult, error)
}

type CandidateProbeInvocation struct {
	InvocationDigest         string
	ProbeRunID               string
	ReceiptSequence          int
	PreviousReceiptDigest    string
	InvocationManifestDigest string
	VariantIndex             int
	Executable               CandidateBoundObject
	Arguments                []string
	Environment              []string
	WorkingDirectory         CandidateBoundObject
	ScratchRoot              CandidateBoundObject
	CredentialConfigRoot     CandidateBoundObject
	WritableRoots            []CandidateBoundObject
	BusinessRepositoryRoots  []CandidateBoundObject
	ChallengeDigest          string
	ExpectedModel            string
	ExpectedTopologyDigest   string
	Prompt                   []byte
}

type CandidateProbeResult struct {
	Transcript               []byte
	ExecutionReceipt         []byte
	IsolationPrincipal       CandidateIsolationPrincipal
	ReceiptAuthorityIdentity CandidateReceiptAuthorityIdentity
	IsolationAudit           CandidateIsolationAudit
}

type CandidateExecutionReceipt struct {
	Kind                       string                    `json:"kind"`
	EvidenceClass              string                    `json:"evidenceClass"`
	SandboxID                  string                    `json:"sandboxId"`
	SandboxVersion             string                    `json:"sandboxVersion"`
	ReceiptAuthorityKeyID      string                    `json:"receiptAuthorityKeyId"`
	InvocationDigest           string                    `json:"invocationDigest"`
	ProbeRunID                 string                    `json:"probeRunId"`
	ReceiptSequence            int                       `json:"receiptSequence"`
	PreviousReceiptDigest      string                    `json:"previousReceiptDigest"`
	InvocationManifestDigest   string                    `json:"invocationManifestDigest"`
	VariantIndex               int                       `json:"variantIndex"`
	Executable                 CandidateObjectIdentity   `json:"executable"`
	ExecutableDigest           string                    `json:"executableDigest"`
	Arguments                  []string                  `json:"arguments"`
	Environment                []string                  `json:"environment"`
	ScratchArgumentPath        string                    `json:"scratchArgumentPath"`
	CredentialArgumentPath     string                    `json:"credentialArgumentPath"`
	WorkingDirectory           CandidateObjectIdentity   `json:"workingDirectory"`
	CredentialConfigRoot       CandidateObjectIdentity   `json:"credentialConfigRoot"`
	WritableRoots              []CandidateObjectIdentity `json:"writableRoots"`
	BusinessRepositoryRoots    []CandidateObjectIdentity `json:"businessRepositoryRoots"`
	ChallengeDigest            string                    `json:"challengeDigest"`
	TranscriptDigest           string                    `json:"transcriptDigest"`
	SessionID                  string                    `json:"sessionId"`
	ObservedModel              string                    `json:"observedModel"`
	BinaryVersion              string                    `json:"binaryVersion"`
	ProtocolVersion            string                    `json:"protocolVersion"`
	PermissionMode             string                    `json:"permissionMode"`
	MarkerDigest               string                    `json:"markerDigest"`
	StartedAt                  string                    `json:"startedAt"`
	CompletedAt                string                    `json:"completedAt"`
	TopologyDigest             string                    `json:"topologyDigest"`
	IsolationProfile           string                    `json:"isolationProfile"`
	IsolationProviderID        string                    `json:"isolationProviderId"`
	IsolationProcessID         string                    `json:"isolationProcessId"`
	ReceiptAuthorityProviderID string                    `json:"receiptAuthorityProviderId"`
	ReceiptAuthorityProcessID  string                    `json:"receiptAuthorityProcessId"`
	IsolationAudit             CandidateIsolationAudit   `json:"isolationAudit"`
	Signature                  string                    `json:"signature"`
}

func (receipt CandidateExecutionReceipt) signingBytes() ([]byte, error) {
	unsigned := receipt
	unsigned.Signature = ""
	data, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(data)
}

func (receipt CandidateExecutionReceipt) digest() (string, error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	data, err = canonical.JSON(data)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

type CandidateLiveProbeRequest struct {
	RunnerID                string
	RunnerVersion           string
	Executable              string
	CredentialConfigRoot    string
	ScratchRoot             string
	BusinessRepositoryRoots []string
	Model                   string
	AuthorityGeneration     uint64
	ProbeArtifactDigest     string
	ChallengeDigest         string
	TrustRootKeyID          string
	Validity                time.Duration
}

type CandidateLiveVerifier struct {
	sandbox            CandidateProbeSandbox
	policy             candidateAuthorityPolicy
	verifierPrivateKey ed25519.PrivateKey
	now                func() time.Time
}

type candidateAuthorityPolicy struct {
	receiptIssuer     string
	receiptKeyID      string
	receiptPublicKey  ed25519.PublicKey
	verifierKeyID     string
	verifierPublicKey ed25519.PublicKey
}

// NewCandidateLiveVerifier remains an exported hard-disabled boundary while
// ADR 0034 is Proposed. A caller cannot establish its own receipt trust root.
func NewCandidateLiveVerifier(sandbox CandidateProbeSandbox, receiptIssuer string, receiptKey ed25519.PublicKey) (*CandidateLiveVerifier, error) {
	return nil, port.Permanent(ErrConformancePending)
}

func newCandidateLiveVerifier(sandbox CandidateProbeSandbox, policy candidateAuthorityPolicy, verifierPrivateKey ed25519.PrivateKey) (*CandidateLiveVerifier, error) {
	if sandbox == nil || strings.TrimSpace(policy.receiptIssuer) == "" || strings.TrimSpace(policy.receiptKeyID) == "" || len(policy.receiptPublicKey) != ed25519.PublicKeySize || strings.TrimSpace(policy.verifierKeyID) == "" || len(policy.verifierPublicKey) != ed25519.PublicKeySize || len(verifierPrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(verifierPrivateKey.Public().(ed25519.PublicKey), policy.verifierPublicKey) {
		return nil, errors.New("qoder candidate authority policy is invalid")
	}
	policy.receiptPublicKey = append(ed25519.PublicKey(nil), policy.receiptPublicKey...)
	policy.verifierPublicKey = append(ed25519.PublicKey(nil), policy.verifierPublicKey...)
	return &CandidateLiveVerifier{sandbox: sandbox, policy: policy, verifierPrivateKey: append(ed25519.PrivateKey(nil), verifierPrivateKey...), now: time.Now}, nil
}

type candidateProbeHandles struct {
	executable  CandidateBoundObject
	credential  CandidateBoundObject
	scratchRoot CandidateBoundObject
	business    []CandidateBoundObject
}

func (handles *candidateProbeHandles) close() {
	if handles == nil {
		return
	}
	for _, object := range append([]CandidateBoundObject{handles.executable, handles.credential, handles.scratchRoot}, handles.business...) {
		if object.File != nil {
			_ = object.File.Close()
		}
	}
}

func (verifier *CandidateLiveVerifier) Verify(ctx context.Context, request CandidateLiveProbeRequest) ([]byte, string, error) {
	if verifier == nil || verifier.sandbox == nil || ctx == nil || ctx.Err() != nil {
		return nil, "", errors.New("qoder candidate live verifier is unavailable")
	}
	if err := validateCandidateLiveProbeRequest(request); err != nil {
		return nil, "", err
	}
	handles, err := openCandidateProbeHandles(request)
	if err != nil {
		return nil, "", err
	}
	defer handles.close()
	hostFingerprint, err := currentHostFingerprint()
	if err != nil {
		return nil, "", err
	}
	started := verifier.now().UTC()
	probeRunID, err := newCandidateProbeRunID()
	if err != nil {
		return nil, "", err
	}
	previousReceiptDigest := ""
	var binaryVersion string
	sessions := map[string]struct{}{}
	transcriptDigests := make([]string, 0, 4)
	receiptDigests := make([]string, 0, 4)
	receiptDocuments := make([]json.RawMessage, 0, 4)
	observedArgv := make([][]string, 0, 4)
	var observedEnvironmentDigest string
	var previousCompletedAt time.Time
	for index, variant := range candidateProbeVariants(request.Model) {
		variantDirectory, cleanup, err := createCandidateScratchDirectory(int(handles.scratchRoot.File.Fd()))
		if err != nil {
			return nil, "", err
		}
		result, probeErr := verifier.runVariant(ctx, request, handles, variantDirectory, probeRunID, previousReceiptDigest, index, variant)
		cleanup()
		if probeErr != nil {
			return nil, "", probeErr
		}
		if binaryVersion != "" && result.binaryVersion != binaryVersion {
			return nil, "", errors.New("qoder candidate live probe binary version changed between variants")
		}
		binaryVersion = result.binaryVersion
		if !previousCompletedAt.IsZero() && result.startedAt.Before(previousCompletedAt) {
			return nil, "", errors.New("qoder candidate live probe receipts overlap")
		}
		previousCompletedAt = result.completedAt
		if _, replay := sessions[result.sessionID]; replay {
			return nil, "", errors.New("qoder candidate live probe replayed a session across variants")
		}
		sessions[result.sessionID] = struct{}{}
		transcriptDigests = append(transcriptDigests, result.transcriptDigest)
		receiptDigests = append(receiptDigests, result.receiptDigest)
		previousReceiptDigest = result.receiptDigest
		receiptDocuments = append(receiptDocuments, append(json.RawMessage(nil), result.receiptDocument...))
		observedArgv = append(observedArgv, result.normalizedArguments)
		environmentDigest := result.normalizedEnvironmentDigest
		if observedEnvironmentDigest != "" && observedEnvironmentDigest != environmentDigest {
			return nil, "", errors.New("qoder candidate live probe environment changed between variants")
		}
		observedEnvironmentDigest = environmentDigest
	}
	if !isSupportedBinaryVersion(binaryVersion) {
		return nil, "", fmt.Errorf("%w: candidate live probe binary is outside %s", ErrUnsupportedVersion, supportedBinaryRange)
	}
	if err := verifyBoundObjectIdentity(handles.executable); err != nil {
		return nil, "", fmt.Errorf("%w: candidate executable identity changed", ErrIdentityDrift)
	}
	for _, object := range append([]CandidateBoundObject{handles.credential, handles.scratchRoot}, handles.business...) {
		if err := verifyBoundObjectIdentity(object); err != nil {
			return nil, "", errors.New("qoder candidate live probe bound root identity changed")
		}
	}
	argvData, _ := json.Marshal(observedArgv)
	observedArgvDigest := digestBytes(argvData)
	if observedArgvDigest != expectedProbeArgvDigest() || observedEnvironmentDigest != expectedProbeEnvironmentDigest() || candidateObservedProfileDigest() != expectedProbeProfileDigest() || candidateObservedToolPolicyDigest() != expectedProbeToolPolicyDigest() {
		return nil, "", errors.New("qoder candidate live probe did not exercise the frozen contract")
	}
	transcriptSet, _ := json.Marshal(map[string]any{"variantTranscriptDigests": transcriptDigests})
	receiptSet, _ := json.Marshal(map[string]any{"variantExecutionReceiptDigests": receiptDigests})
	completed := verifier.now().UTC()
	observation := LiveConformanceObservation{
		RunnerID: request.RunnerID, RunnerVersion: request.RunnerVersion, ObservedAt: started, ValidUntil: completed.Add(request.Validity),
		AdapterVersion: adapterVersion, Executable: handles.executable.CanonicalPath, ExecutableDigest: candidateExecutableDigest(handles.executable), BinaryVersion: binaryVersion, QoderCLIVersion: binaryVersion,
		HostOS: runtime.GOOS, HostArch: runtime.GOARCH, HostFingerprint: hostFingerprint, AuthorityGeneration: request.AuthorityGeneration,
		ProbeSuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: request.ProbeArtifactDigest, ChallengeDigest: request.ChallengeDigest,
		CapabilitiesDigest: digestObservedCandidateCapabilities(), ProbeProfileDigest: candidateObservedProfileDigest(), ArgvDigest: observedArgvDigest, EnvironmentDigest: observedEnvironmentDigest, ToolPolicyDigest: candidateObservedToolPolicyDigest(),
		TranscriptDigest: digestBytes(transcriptSet), ExecutionReceiptDigest: digestBytes(receiptSet), EvidenceClass: candidateEvidenceClassLive,
		ExecutionReceiptDigests: append([]string(nil), receiptDigests...), ExecutionReceipts: receiptDocuments,
		ReceiptAuthorityKeyID: verifier.policy.receiptKeyID, ReceiptAuthorityPublicKeyDigest: digestBytes(verifier.policy.receiptPublicKey), VerifierKeyID: verifier.policy.verifierKeyID, VerifierPublicKeyDigest: digestBytes(verifier.policy.verifierPublicKey),
		CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TrustRootKeyID: request.TrustRootKeyID,
	}
	signingBytes, err := liveObservationSigningBytes(observation)
	if err != nil {
		return nil, "", err
	}
	observation.VerifierSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(verifier.verifierPrivateKey, signingBytes))
	return EncodeLiveConformanceObservation(observation)
}

type candidateProbeVariant struct {
	model           string
	disableAllTools bool
}

func candidateProbeVariants(model string) []candidateProbeVariant {
	return []candidateProbeVariant{{}, {model: model}, {disableAllTools: true}, {model: model, disableAllTools: true}}
}

type candidateVariantResult struct {
	transcriptDigest            string
	receiptDigest               string
	sessionID                   string
	binaryVersion               string
	normalizedArguments         []string
	normalizedEnvironmentDigest string
	receiptDocument             []byte
	startedAt                   time.Time
	completedAt                 time.Time
}

func (verifier *CandidateLiveVerifier) runVariant(ctx context.Context, request CandidateLiveProbeRequest, handles *candidateProbeHandles, scratch CandidateBoundObject, probeRunID, previousReceiptDigest string, index int, variant candidateProbeVariant) (candidateVariantResult, error) {
	challenge := candidateVariantChallenge(request.ChallengeDigest, index)
	arguments := buildArgs(variant.model, candidateBoundCredentialToken, candidateBoundScratchToken, variant.disableAllTools)
	environment := candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken)
	invocation := CandidateProbeInvocation{
		ProbeRunID: probeRunID, ReceiptSequence: index + 1, PreviousReceiptDigest: previousReceiptDigest,
		VariantIndex: index, Executable: handles.executable, Arguments: arguments, Environment: environment,
		WorkingDirectory: scratch, ScratchRoot: handles.scratchRoot, CredentialConfigRoot: handles.credential, WritableRoots: []CandidateBoundObject{scratch}, BusinessRepositoryRoots: handles.business,
		ChallengeDigest: challenge, ExpectedModel: variant.model,
		Prompt: candidateProbePrompt(challenge),
	}
	invocation.InvocationManifestDigest = digestCandidateInvocationManifest(invocation)
	topologyDigest, err := validateCandidateTopology(handles, scratch)
	if err != nil {
		return candidateVariantResult{}, err
	}
	invocation.ExpectedTopologyDigest = topologyDigest
	invocation.InvocationDigest = digestCandidateInvocation(invocation)
	result, err := verifier.sandbox.RunProbe(ctx, invocation)
	if err != nil {
		return candidateVariantResult{}, errors.New("qoder candidate live probe sandbox execution failed")
	}
	if len(result.Transcript) == 0 || len(result.Transcript) > maxResultBytes {
		return candidateVariantResult{}, errors.New("qoder candidate live probe transcript is empty or oversized")
	}
	postTopology, err := validateCandidateTopology(handles, scratch)
	if err != nil || postTopology != invocation.ExpectedTopologyDigest {
		return candidateVariantResult{}, errors.New("qoder candidate live probe topology changed across execution")
	}
	capture := decodeTranscript(result.Transcript)
	if capture.err != nil || !capture.terminal.seen || !capture.terminal.success || capture.cliVersion == "" || capture.protocolVersion != qoderProtocolVersion || capture.permissionMode != qoderPermissionMode || (variant.model != "" && capture.model != variant.model) || !transcriptBindsCandidateChallenge(result.Transcript, challenge) {
		return candidateVariantResult{}, errors.New("qoder candidate live probe protocol contract failed")
	}
	markerDigest, err := readCandidateMarker(ctx, int(scratch.File.Fd()), challenge)
	if err != nil {
		return candidateVariantResult{}, err
	}
	postMarkerTopology, err := validateCandidateTopology(handles, scratch)
	if err != nil || postMarkerTopology != invocation.ExpectedTopologyDigest {
		return candidateVariantResult{}, errors.New("qoder candidate live probe topology changed during marker verification")
	}
	receipt, receiptDigest, err := verifier.verifyExecutionReceipt(result.ExecutionReceipt, invocation, result, capture, markerDigest)
	if err != nil {
		return candidateVariantResult{}, err
	}
	normalizedArguments, normalizedEnvironmentDigest, err := normalizeActualReceiptProfile(receipt, invocation)
	if err != nil {
		return candidateVariantResult{}, err
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completedAt, _ := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	return candidateVariantResult{transcriptDigest: receipt.TranscriptDigest, receiptDigest: receiptDigest, receiptDocument: append([]byte(nil), result.ExecutionReceipt...), sessionID: receipt.SessionID, binaryVersion: receipt.BinaryVersion, normalizedArguments: normalizedArguments, normalizedEnvironmentDigest: normalizedEnvironmentDigest, startedAt: startedAt, completedAt: completedAt}, nil
}

func transcriptBindsCandidateChallenge(transcript []byte, challenge string) bool {
	decoder := json.NewDecoder(bytes.NewReader(transcript))
	matches := 0
	for {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					ChallengeDigest string `json:"challengeDigest"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return false
		}
		if event.Type != "assistant" {
			continue
		}
		for _, content := range event.Message.Content {
			if content.ChallengeDigest != "" {
				if content.ChallengeDigest != challenge {
					return false
				}
				matches++
			}
		}
	}
	return matches == 1
}

func (verifier *CandidateLiveVerifier) verifyExecutionReceipt(document []byte, invocation CandidateProbeInvocation, result CandidateProbeResult, capture captureResult, markerDigest string) (CandidateExecutionReceipt, string, error) {
	receipt, err := decodeCandidateExecutionReceipt(document)
	if err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	if receipt.EvidenceClass != candidateEvidenceClassLive {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt is not credentialed-live")
	}
	if receipt.SandboxID != verifier.policy.receiptIssuer || receipt.ReceiptAuthorityKeyID != verifier.policy.receiptKeyID || receipt.Kind != candidateReceiptKind || receipt.SandboxVersion == "" {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt provenance is invalid")
	}
	message, err := receipt.signingBytes()
	if err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	signature, err := base64.StdEncoding.DecodeString(receipt.Signature)
	if err != nil || !ed25519.Verify(verifier.policy.receiptPublicKey, message, signature) {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt signature is not trusted")
	}
	writable := objectIdentities(invocation.WritableRoots)
	denied := objectIdentities(invocation.BusinessRepositoryRoots)
	if receipt.TopologyDigest != invocation.ExpectedTopologyDigest {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt topology differs from verifier topology")
	}
	if receipt.InvocationDigest != invocation.InvocationDigest || receipt.ProbeRunID != invocation.ProbeRunID || receipt.ReceiptSequence != invocation.ReceiptSequence || receipt.PreviousReceiptDigest != invocation.PreviousReceiptDigest || receipt.InvocationManifestDigest != invocation.InvocationManifestDigest || receipt.VariantIndex != invocation.VariantIndex || receipt.Executable != invocation.Executable.Identity || receipt.ExecutableDigest != candidateExecutableDigest(invocation.Executable) || receipt.WorkingDirectory != invocation.WorkingDirectory.Identity || receipt.CredentialConfigRoot != invocation.CredentialConfigRoot.Identity || !equalIdentities(receipt.WritableRoots, writable) || !equalIdentities(receipt.BusinessRepositoryRoots, denied) || receipt.ChallengeDigest != invocation.ChallengeDigest || receipt.TranscriptDigest != digestBytes(result.Transcript) || receipt.SessionID != capture.sessionID || receipt.ObservedModel != capture.model || receipt.BinaryVersion != capture.cliVersion || receipt.ProtocolVersion != capture.protocolVersion || receipt.PermissionMode != capture.permissionMode || receipt.MarkerDigest != markerDigest || receipt.IsolationProfile != result.IsolationPrincipal.Profile || receipt.IsolationProviderID != result.IsolationPrincipal.ProviderIdentity || receipt.IsolationProcessID != result.IsolationPrincipal.ProcessIdentity || receipt.ReceiptAuthorityProviderID != result.ReceiptAuthorityIdentity.ProviderIdentity || receipt.ReceiptAuthorityProcessID != result.ReceiptAuthorityIdentity.ProcessIdentity || !equalCandidateIsolationAudit(receipt.IsolationAudit, result.IsolationAudit) {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt does not bind actual execution")
	}
	if receipt.IsolationProfile != candidateIsolationProfile || receipt.IsolationProcessID == "" || receipt.ReceiptAuthorityProcessID == "" || receipt.IsolationProcessID == receipt.ReceiptAuthorityProcessID || validateCandidateIsolationAudit(receipt.IsolationAudit, len(denied)) != nil {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt isolation authority is invalid")
	}
	if _, _, err := normalizeActualReceiptProfile(receipt, invocation); err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	now := verifier.now().UTC()
	if startErr != nil || completeErr != nil || completed.Before(started) || completed.After(now) || completed.Sub(started) > candidateReceiptMaxExecution || now.Sub(started) > maxConformanceValidity {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt time bounds are invalid")
	}
	digest, err := receipt.digest()
	return receipt, digest, err
}

func decodeCandidateExecutionReceipt(document []byte) (CandidateExecutionReceipt, error) {
	if len(document) == 0 || len(document) > candidateReceiptLimit {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt is empty or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var receipt CandidateExecutionReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt is invalid")
	}
	encoded, _ := json.Marshal(receipt)
	canonicalData, err := canonical.JSON(encoded)
	if err != nil || !bytes.Equal(document, canonicalData) {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt is not canonical")
	}
	return receipt, nil
}

func verifyCandidateObservationAuthorityChain(observation LiveConformanceObservation, policy candidateAuthorityPolicy, now time.Time) error {
	if observation.ReceiptAuthorityKeyID != policy.receiptKeyID || observation.ReceiptAuthorityPublicKeyDigest != digestBytes(policy.receiptPublicKey) || observation.VerifierKeyID != policy.verifierKeyID || observation.VerifierPublicKeyDigest != digestBytes(policy.verifierPublicKey) {
		return errors.New("qoder candidate observation authority identity is not trusted")
	}
	verifierSignature, err := base64.StdEncoding.DecodeString(observation.VerifierSignature)
	if err != nil || len(verifierSignature) != ed25519.SignatureSize {
		return errors.New("qoder candidate observation verifier signature is invalid")
	}
	verifierMessage, err := liveObservationSigningBytes(observation)
	if err != nil || !ed25519.Verify(policy.verifierPublicKey, verifierMessage, verifierSignature) {
		return errors.New("qoder candidate observation verifier signature is not trusted")
	}
	if len(observation.ExecutionReceipts) != 4 || len(observation.ExecutionReceiptDigests) != 4 {
		return errors.New("qoder candidate observation receipt chain is incomplete")
	}
	receiptDigests := make([]string, 0, 4)
	transcriptDigests := make([]string, 0, 4)
	normalizedArgv := make([][]string, 0, 4)
	seenVariants, seenSessions := map[int]struct{}{}, map[string]struct{}{}
	var environmentDigest string
	var executableIdentity CandidateObjectIdentity
	var probeRunID, previousReceiptDigest string
	var previousCompletedAt time.Time
	for index, document := range observation.ExecutionReceipts {
		receipt, err := decodeCandidateExecutionReceipt(document)
		if err != nil {
			return err
		}
		if receipt.Kind != candidateReceiptKind || receipt.EvidenceClass != candidateEvidenceClassLive || receipt.SandboxID != policy.receiptIssuer || receipt.ReceiptAuthorityKeyID != policy.receiptKeyID || receipt.SandboxVersion == "" || receipt.VariantIndex < 0 || receipt.VariantIndex > 3 || receipt.ReceiptSequence != index+1 || !validSHA256Digest(receipt.TopologyDigest) || !validSHA256Digest(receipt.InvocationManifestDigest) || receipt.IsolationProfile != candidateIsolationProfile || receipt.IsolationProcessID == "" || receipt.ReceiptAuthorityProcessID == "" || receipt.IsolationProcessID == receipt.ReceiptAuthorityProcessID || validateCandidateIsolationAudit(receipt.IsolationAudit, len(receipt.BusinessRepositoryRoots)) != nil {
			return errors.New("qoder candidate signer rejected receipt provenance")
		}
		if _, duplicate := seenVariants[receipt.VariantIndex]; duplicate || receipt.VariantIndex != index {
			return errors.New("qoder candidate signer rejected receipt variant replay")
		}
		seenVariants[receipt.VariantIndex] = struct{}{}
		if index == 0 {
			if strings.TrimSpace(receipt.ProbeRunID) == "" || receipt.PreviousReceiptDigest != "" {
				return errors.New("qoder candidate signer rejected receipt run chain")
			}
			probeRunID = receipt.ProbeRunID
		} else if receipt.ProbeRunID != probeRunID || receipt.PreviousReceiptDigest != previousReceiptDigest {
			return errors.New("qoder candidate signer rejected receipt run chain")
		}
		if receipt.SessionID == "" {
			return errors.New("qoder candidate signer rejected empty receipt session")
		}
		if _, duplicate := seenSessions[receipt.SessionID]; duplicate {
			return errors.New("qoder candidate signer rejected receipt session replay")
		}
		seenSessions[receipt.SessionID] = struct{}{}
		message, err := receipt.signingBytes()
		if err != nil {
			return err
		}
		signature, err := base64.StdEncoding.DecodeString(receipt.Signature)
		if err != nil || !ed25519.Verify(policy.receiptPublicKey, message, signature) {
			return errors.New("qoder candidate signer rejected receipt signature")
		}
		digest, err := receipt.digest()
		if err != nil || digest != observation.ExecutionReceiptDigests[index] {
			return errors.New("qoder candidate signer rejected receipt digest")
		}
		receiptDigests = append(receiptDigests, digest)
		previousReceiptDigest = digest
		transcriptDigests = append(transcriptDigests, receipt.TranscriptDigest)
		if receipt.ExecutableDigest != observation.ExecutableDigest || receipt.BinaryVersion != observation.BinaryVersion || receipt.ProtocolVersion != observation.ProtocolVersion || receipt.PermissionMode != observation.PermissionMode || receipt.ChallengeDigest != candidateVariantChallenge(observation.ChallengeDigest, receipt.VariantIndex) || !equalIdentities(receipt.WritableRoots, []CandidateObjectIdentity{receipt.WorkingDirectory}) || !validSHA256Digest(receipt.MarkerDigest) || !validSHA256Digest(receipt.TranscriptDigest) {
			return errors.New("qoder candidate signer rejected receipt candidate identity")
		}
		if index == 0 {
			executableIdentity = receipt.Executable
		} else if receipt.Executable != executableIdentity {
			return errors.New("qoder candidate signer rejected executable identity drift")
		}
		expectedModel := ""
		if receipt.VariantIndex == 1 || receipt.VariantIndex == 3 {
			expectedModel = receipt.ObservedModel
		}
		invocation := CandidateProbeInvocation{
			ProbeRunID: receipt.ProbeRunID, ReceiptSequence: receipt.ReceiptSequence, PreviousReceiptDigest: receipt.PreviousReceiptDigest,
			VariantIndex: receipt.VariantIndex, Executable: CandidateBoundObject{Identity: receipt.Executable, Digest: receipt.ExecutableDigest},
			Arguments: buildArgs(expectedModel, candidateBoundCredentialToken, candidateBoundScratchToken, receipt.VariantIndex >= 2), Environment: candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken),
			WorkingDirectory: CandidateBoundObject{Identity: receipt.WorkingDirectory}, CredentialConfigRoot: CandidateBoundObject{Identity: receipt.CredentialConfigRoot},
			WritableRoots: []CandidateBoundObject{{Identity: receipt.WorkingDirectory}}, ChallengeDigest: receipt.ChallengeDigest, ExpectedModel: expectedModel, ExpectedTopologyDigest: receipt.TopologyDigest, Prompt: candidateProbePrompt(receipt.ChallengeDigest),
		}
		invocation.InvocationManifestDigest = digestCandidateInvocationManifest(invocation)
		if invocation.InvocationManifestDigest != receipt.InvocationManifestDigest {
			return errors.New("qoder candidate signer rejected invocation manifest")
		}
		for _, identity := range receipt.BusinessRepositoryRoots {
			invocation.BusinessRepositoryRoots = append(invocation.BusinessRepositoryRoots, CandidateBoundObject{Identity: identity})
		}
		invocation.InvocationDigest = digestCandidateInvocation(invocation)
		if invocation.InvocationDigest != receipt.InvocationDigest {
			return errors.New("qoder candidate signer rejected receipt invocation digest")
		}
		normalized, envDigest, err := normalizeActualReceiptProfile(receipt, invocation)
		if err != nil {
			return err
		}
		normalizedArgv = append(normalizedArgv, normalized)
		if environmentDigest != "" && environmentDigest != envDigest {
			return errors.New("qoder candidate signer rejected environment drift")
		}
		environmentDigest = envDigest
		started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
		completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
		if startErr != nil || completeErr != nil || completed.Before(started) || completed.Sub(started) > candidateReceiptMaxExecution || completed.After(now) || started.Before(observation.ObservedAt.Add(-time.Minute)) || completed.After(observation.ValidUntil) || (!previousCompletedAt.IsZero() && started.Before(previousCompletedAt)) {
			return errors.New("qoder candidate signer rejected receipt time bounds")
		}
		previousCompletedAt = completed
	}
	receiptSet, _ := json.Marshal(map[string]any{"variantExecutionReceiptDigests": receiptDigests})
	transcriptSet, _ := json.Marshal(map[string]any{"variantTranscriptDigests": transcriptDigests})
	argvData, _ := json.Marshal(normalizedArgv)
	if digestBytes(receiptSet) != observation.ExecutionReceiptDigest || digestBytes(transcriptSet) != observation.TranscriptDigest || digestBytes(argvData) != observation.ArgvDigest || environmentDigest != observation.EnvironmentDigest {
		return errors.New("qoder candidate signer rejected observation aggregate digests")
	}
	return nil
}

func validateCandidateLiveProbeRequest(request CandidateLiveProbeRequest) error {
	if request.RunnerID == "" || request.RunnerID == adapterID || request.RunnerVersion == "" || request.AuthorityGeneration == 0 || request.TrustRootKeyID == "" || !validSHA256Digest(request.ProbeArtifactDigest) || !validSHA256Digest(request.ChallengeDigest) || request.Validity <= 0 || request.Validity > maxConformanceValidity || strings.TrimSpace(request.Model) == "" || len(request.BusinessRepositoryRoots) == 0 {
		return errors.New("qoder candidate live probe request is incomplete")
	}
	for _, path := range append([]string{request.Executable, request.CredentialConfigRoot, request.ScratchRoot}, request.BusinessRepositoryRoots...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("qoder candidate live probe paths must be absolute and clean")
		}
	}
	return nil
}

func openCandidateProbeHandles(request CandidateLiveProbeRequest) (*candidateProbeHandles, error) {
	executable, err := openCandidateObject(request.Executable, false)
	if err != nil {
		return nil, fmt.Errorf("open qoder candidate executable identity: %w", err)
	}
	handles := &candidateProbeHandles{executable: executable}
	fail := func(err error) (*candidateProbeHandles, error) { handles.close(); return nil, err }
	handles.credential, err = openCandidateObject(request.CredentialConfigRoot, true)
	if err != nil {
		return fail(errors.New("open qoder candidate credential root identity"))
	}
	handles.scratchRoot, err = openCandidateObject(request.ScratchRoot, true)
	if err != nil {
		return fail(errors.New("open qoder candidate scratch root identity"))
	}
	for _, path := range request.BusinessRepositoryRoots {
		object, openErr := openCandidateObject(path, true)
		if openErr != nil {
			return fail(errors.New("open qoder candidate business root identity"))
		}
		handles.business = append(handles.business, object)
	}
	sort.Slice(handles.business, func(left, right int) bool {
		if handles.business[left].Identity.Device != handles.business[right].Identity.Device {
			return handles.business[left].Identity.Device < handles.business[right].Identity.Device
		}
		return handles.business[left].Identity.Inode < handles.business[right].Identity.Inode
	})
	directories := append([]CandidateBoundObject{handles.scratchRoot, handles.credential}, handles.business...)
	for left := range directories {
		for right := left + 1; right < len(directories); right++ {
			overlap, overlapErr := authorityDirectoriesOverlap(int(directories[left].File.Fd()), int(directories[right].File.Fd()))
			if overlapErr != nil || overlap {
				return fail(errors.New("qoder candidate live probe roots overlap by identity or ancestry"))
			}
		}
	}
	return handles, nil
}

func openCandidateObject(path string, directory bool) (CandidateBoundObject, error) {
	file, stat, err := openNoSymlinkPath(path, directory)
	if err != nil {
		return CandidateBoundObject{}, err
	}
	valid := privateDirectory(stat, os.Geteuid())
	if !directory {
		valid = stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o111 != 0 && (int(stat.Uid) == os.Geteuid() || stat.Uid == 0)
	}
	if !valid {
		_ = file.Close()
		return CandidateBoundObject{}, errors.New("candidate bound object has invalid type, owner, or mode")
	}
	object := CandidateBoundObject{File: file, CanonicalPath: path, Identity: statObjectIdentity(stat)}
	if !directory {
		if stat.Size < 0 || stat.Size > 128<<20 {
			_ = file.Close()
			return CandidateBoundObject{}, errors.New("candidate executable size is invalid")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, stat.Size+1))
		if readErr != nil || int64(len(data)) != stat.Size {
			_ = file.Close()
			return CandidateBoundObject{}, errors.New("candidate executable is unreadable")
		}
		object.Digest = digestBytes(data)
		_, _ = file.Seek(0, io.SeekStart)
	}
	return object, nil
}

func verifyBoundObjectIdentity(object CandidateBoundObject) error {
	var stat unix.Stat_t
	if object.File == nil || unix.Fstat(int(object.File.Fd()), &stat) != nil || statObjectIdentity(stat) != object.Identity {
		return errors.New("candidate bound object identity changed")
	}
	return nil
}

func statObjectIdentity(stat unix.Stat_t) CandidateObjectIdentity {
	return CandidateObjectIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}
}

func candidateExecutableDigest(object CandidateBoundObject) string {
	return object.Digest
}

func createCandidateScratchDirectory(parentFD int) (CandidateBoundObject, func(), error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return CandidateBoundObject{}, nil, err
	}
	name := "probe-" + hex.EncodeToString(token[:])
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		return CandidateBoundObject{}, nil, errors.New("create qoder candidate scratch directory")
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		return CandidateBoundObject{}, nil, errors.New("open qoder candidate scratch directory")
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !privateDirectory(stat, os.Geteuid()) {
		_ = file.Close()
		return CandidateBoundObject{}, nil, errors.New("qoder candidate scratch directory is not private")
	}
	object := CandidateBoundObject{File: file, CanonicalPath: candidateBoundScratchToken, Identity: statObjectIdentity(stat)}
	cleanup := func() {
		_ = unix.Unlinkat(fd, ".marshal-qoder-probe-challenge", 0)
		_ = file.Close()
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
	}
	return object, cleanup, nil
}

func readCandidateMarker(ctx context.Context, scratchFD int, challenge string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd, err := unix.Openat(scratchFD, ".marshal-qoder-probe-challenge", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("open qoder candidate marker")
	}
	file := os.NewFile(uintptr(fd), "qoder-probe-marker")
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) || stat.Size < 0 || stat.Size > candidateMarkerLimit {
		return "", errors.New("qoder candidate marker identity is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, candidateMarkerLimit+1))
	if err != nil || len(data) > candidateMarkerLimit || int64(len(data)) != stat.Size || !bytes.Equal(data, []byte(challenge+"\n")) {
		return "", errors.New("qoder candidate marker content is invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func candidateVariantChallenge(base string, index int) string {
	data, _ := json.Marshal(map[string]any{"baseChallengeDigest": base, "variantIndex": index})
	return digestBytes(data)
}

func candidateProbePrompt(challenge string) []byte {
	return []byte("Probe-only conformance challenge. Create .marshal-qoder-probe-challenge with mode 0600 in the bound scratch directory containing exactly this line: " + challenge + "\n")
}

func validateCandidateTopology(handles *candidateProbeHandles, scratch CandidateBoundObject) (string, error) {
	if handles == nil {
		return "", errors.New("qoder candidate topology handles are unavailable")
	}
	roots := append([]CandidateBoundObject{handles.credential, handles.scratchRoot}, handles.business...)
	for left := range roots {
		if err := verifyBoundObjectIdentity(roots[left]); err != nil {
			return "", err
		}
		for right := left + 1; right < len(roots); right++ {
			overlap, err := authorityDirectoriesOverlap(int(roots[left].File.Fd()), int(roots[right].File.Fd()))
			if err != nil || overlap {
				return "", errors.New("qoder candidate live probe roots overlap during execution")
			}
		}
	}
	scratchRootStat, err := directoryStat(int(handles.scratchRoot.File.Fd()))
	if err != nil {
		return "", err
	}
	contained, err := directoryIdentityInAncestors(scratchRootStat, int(scratch.File.Fd()))
	if err != nil || !contained {
		return "", errors.New("qoder candidate scratch escaped its held root")
	}
	for _, denied := range append([]CandidateBoundObject{handles.credential}, handles.business...) {
		overlap, err := authorityDirectoriesOverlap(int(scratch.File.Fd()), int(denied.File.Fd()))
		if err != nil || overlap {
			return "", errors.New("qoder candidate execution scratch overlaps a protected root")
		}
	}
	objects := append([]CandidateBoundObject{handles.scratchRoot, scratch, handles.credential}, handles.business...)
	chains := make([][]CandidateObjectIdentity, 0, len(objects))
	for _, object := range objects {
		chain, err := candidateAncestorChain(int(object.File.Fd()))
		if err != nil {
			return "", err
		}
		chains = append(chains, chain)
	}
	data, _ := json.Marshal(map[string]any{"objects": objectIdentities(objects), "ancestorChains": chains})
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData), nil
}

func candidateAncestorChain(directoryFD int) ([]CandidateObjectIdentity, error) {
	current, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var result []CandidateObjectIdentity
	for {
		stat, err := directoryStat(current)
		if err != nil {
			unix.Close(current)
			return nil, err
		}
		result = append(result, statObjectIdentity(stat))
		parent, err := unix.Openat(current, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			unix.Close(current)
			return nil, err
		}
		parentStat, err := directoryStat(parent)
		if err != nil {
			unix.Close(parent)
			unix.Close(current)
			return nil, err
		}
		if sameDirectoryIdentity(stat, parentStat) {
			unix.Close(parent)
			unix.Close(current)
			return result, nil
		}
		unix.Close(current)
		current = parent
	}
}

func digestCandidateInvocation(invocation CandidateProbeInvocation) string {
	data, _ := json.Marshal(map[string]any{"probeRunId": invocation.ProbeRunID, "receiptSequence": invocation.ReceiptSequence, "previousReceiptDigest": invocation.PreviousReceiptDigest, "invocationManifestDigest": invocation.InvocationManifestDigest, "variantIndex": invocation.VariantIndex, "executable": invocation.Executable.Identity, "executableDigest": candidateExecutableDigest(invocation.Executable), "arguments": invocation.Arguments, "environment": invocation.Environment, "workingDirectory": invocation.WorkingDirectory.Identity, "credentialConfigRoot": invocation.CredentialConfigRoot.Identity, "writableRoots": objectIdentities(invocation.WritableRoots), "businessRepositoryRoots": objectIdentities(invocation.BusinessRepositoryRoots), "challengeDigest": invocation.ChallengeDigest, "expectedModel": invocation.ExpectedModel, "expectedTopologyDigest": invocation.ExpectedTopologyDigest})
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData)
}

func digestCandidateInvocationManifest(invocation CandidateProbeInvocation) string {
	data, _ := json.Marshal(map[string]any{"arguments": invocation.Arguments, "environment": invocation.Environment, "inheritedEnvironmentDiscarded": true, "promptDigest": digestBytes(invocation.Prompt), "receiptSequence": invocation.ReceiptSequence, "variantIndex": invocation.VariantIndex})
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData)
}

func newCandidateProbeRunID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", errors.New("create qoder candidate probe run identity")
	}
	return hex.EncodeToString(token[:]), nil
}

func objectIdentities(objects []CandidateBoundObject) []CandidateObjectIdentity {
	result := make([]CandidateObjectIdentity, len(objects))
	for index := range objects {
		result[index] = objects[index].Identity
	}
	return result
}
func equalIdentities(left, right []CandidateObjectIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func normalizeActualReceiptProfile(receipt CandidateExecutionReceipt, invocation CandidateProbeInvocation) ([]string, string, error) {
	for _, path := range []string{receipt.ScratchArgumentPath, receipt.CredentialArgumentPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, "$BOUND_") {
			return nil, "", errors.New("qoder candidate live probe receipt actual path is invalid")
		}
	}
	normalizedArguments := append([]string(nil), receipt.Arguments...)
	for index, value := range normalizedArguments {
		switch value {
		case receipt.ScratchArgumentPath:
			normalizedArguments[index] = "$ISOLATED_WORKTREE"
		case receipt.CredentialArgumentPath:
			normalizedArguments[index] = "$ISOLATED_CONFIG_DIR"
		}
		if invocation.ExpectedModel != "" && value == invocation.ExpectedModel {
			normalizedArguments[index] = "$MODEL"
		}
	}
	expectedArguments := normalizeProbeArguments(invocation.Arguments, CandidateLiveProbeRequest{Model: invocation.ExpectedModel}, candidateBoundScratchToken)
	if !equalStrings(normalizedArguments, expectedArguments) {
		return nil, "", errors.New("qoder candidate live probe receipt actual argv differs from the invocation template")
	}
	normalizedEnvironment := append([]string(nil), receipt.Environment...)
	for index, value := range normalizedEnvironment {
		value = strings.ReplaceAll(value, receipt.ScratchArgumentPath, "$ISOLATED_WORKTREE")
		value = strings.ReplaceAll(value, receipt.CredentialArgumentPath, "$ISOLATED_CONFIG_DIR")
		normalizedEnvironment[index] = value
	}
	sort.Strings(normalizedEnvironment)
	expectedEnvironment := candidateProbeEnvironment("$ISOLATED_WORKTREE", "$ISOLATED_CONFIG_DIR")
	if !equalStrings(normalizedEnvironment, expectedEnvironment) {
		return nil, "", errors.New("qoder candidate live probe receipt actual environment differs from the invocation template")
	}
	data, _ := json.Marshal(normalizedEnvironment)
	return normalizedArguments, digestBytes(data), nil
}

func normalizeProbeArguments(arguments []string, request CandidateLiveProbeRequest, scratch string) []string {
	normalized := append([]string(nil), arguments...)
	for index, value := range normalized {
		switch value {
		case candidateBoundCredentialToken:
			normalized[index] = "$ISOLATED_CONFIG_DIR"
		case scratch, candidateBoundScratchToken:
			normalized[index] = "$ISOLATED_WORKTREE"
		}
		if request.CredentialConfigRoot != "" && value == request.CredentialConfigRoot {
			normalized[index] = "$ISOLATED_CONFIG_DIR"
		}
		if request.Model != "" && value == request.Model {
			normalized[index] = "$MODEL"
		}
	}
	return normalized
}
func candidateObservedProfileDigest() string {
	data, _ := json.Marshal(map[string]any{"ambientCredentialInheritance": false, "businessRepositoryAccess": false, "eventContract": conformanceEventContract, "isolatedWorkingDirectory": true, "permissionMode": qoderPermissionMode, "repositoryWritePermission": true, "settingSources": []string{}, "writableRoots": []string{"$ISOLATED_WORKTREE"}})
	return digestBytes(data)
}
func candidateObservedToolPolicyDigest() string {
	data, _ := json.Marshal(map[string]any{"namedWorkerTools": []string{}, "providerPermissionMode": qoderPermissionMode, "repositoryScope": "isolated-scratch-worktree"})
	return digestBytes(data)
}
func digestObservedCandidateCapabilities() string {
	data, _ := json.Marshal(expectedCapabilities())
	return digestBytes(data)
}
func candidateProbeEnvironment(scratch, config string) []string {
	values := []string{"CI=1", "PATH=/usr/bin:/bin", "HOME=" + config, "XDG_CONFIG_HOME=" + config, "XDG_CACHE_HOME=" + filepath.Join(scratch, "cache"), "XDG_DATA_HOME=" + filepath.Join(scratch, "data"), "XDG_STATE_HOME=" + filepath.Join(scratch, "state"), "TMPDIR=" + filepath.Join(scratch, "tmp"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "PWD=" + scratch}
	sort.Strings(values)
	return values
}
