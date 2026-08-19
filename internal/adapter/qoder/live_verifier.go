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
	"unicode"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

const (
	candidateEvidenceClassLive       = "credentialed-live"
	candidateEvidenceClassHermetic   = "hermetic-fixture"
	candidateReceiptKind             = "QoderProbeExecutionReceipt"
	candidateReceiptAPIVersion       = "marshal.dev/v1alpha1"
	candidateReceiptSchemaVersion    = 1
	candidateSignatureAlgorithm      = "Ed25519"
	candidateSignatureEncoding       = "base64url-unpadded"
	candidateReceiptSigningDomain    = "marshal-qoder-receipt-v1\x00"
	candidateCredentialSigningDomain = "marshal-qoder-credential-capability-v1\x00"
	candidateReceiptLimit            = 64 << 10
	candidateMarkerLimit             = 256
	candidateBoundScratchToken       = "$BOUND_SCRATCH_DIR"
	candidateBoundCredentialToken    = "$BOUND_CREDENTIAL_DIR"
	candidateReceiptMaxExecution     = 30 * time.Minute
	candidateMaxJSONInteger          = uint64(1<<63 - 1)
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
	ProbeRunChallengeDigest  string
	ExpectedModel            string
	ExpectedTopologyDigest   string
	Prompt                   []byte
}

type CandidateProbeResult struct {
	Transcript       []byte
	ExecutionReceipt []byte
	AuthorityBacked  bool
}

type CandidateCanonicalPathBytes struct {
	Encoding string `json:"encoding"`
	Bytes    string `json:"bytes"`
	Digest   string `json:"digest"`
}

type CandidateExecutableReceiptIdentity struct {
	RealpathBytes CandidateCanonicalPathBytes `json:"realpathBytes"`
	Device        uint64                      `json:"device"`
	Inode         uint64                      `json:"inode"`
	Digest        string                      `json:"digest"`
	BinaryVersion string                      `json:"binaryVersion"`
}

type CandidateRootIdentity struct {
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	IdentityDigest string `json:"identityDigest"`
}

type CandidateArgvEntry struct {
	Index          uint64  `json:"index"`
	Source         string  `json:"source"`
	Representation string  `json:"representation"`
	LiteralValue   *string `json:"literalValue"`
	ValueDigest    string  `json:"valueDigest"`
}

type CandidateArgvManifest struct {
	Entries        []CandidateArgvEntry `json:"entries"`
	ManifestDigest string               `json:"manifestDigest"`
}

type CandidateCredentialCapabilityIdentity struct {
	APIVersion         string `json:"apiVersion"`
	Kind               string `json:"kind"`
	SchemaVersion      uint64 `json:"schemaVersion"`
	ProviderIdentity   string `json:"providerIdentity"`
	CapabilityID       string `json:"capabilityId"`
	ProbeRunID         string `json:"probeRunId"`
	VariantID          string `json:"variantId"`
	CapabilityClass    string `json:"capabilityClass"`
	PolicyScopeDigest  string `json:"policyScopeDigest"`
	IssuedAt           string `json:"issuedAt"`
	ExpiresAt          string `json:"expiresAt"`
	ProviderKeyID      string `json:"providerKeyId"`
	ProviderKeyEpoch   uint64 `json:"providerKeyEpoch"`
	SignatureAlgorithm string `json:"signatureAlgorithm"`
	SignatureEncoding  string `json:"signatureEncoding"`
	Signature          string `json:"signature"`
	RecordDigest       string `json:"recordDigest"`
}

func (value CandidateCredentialCapabilityIdentity) digest() string {
	return digestRecordWithoutFields(value, "signature", "recordDigest")
}

func (value CandidateCredentialCapabilityIdentity) signingBytes() ([]byte, error) {
	if !validSHA256Digest(value.RecordDigest) {
		return nil, errors.New("qoder credential capability record digest is invalid")
	}
	return []byte(candidateCredentialSigningDomain + value.RecordDigest), nil
}

type CandidateEnvironmentEntry struct {
	Name                string                                 `json:"name"`
	Source              string                                 `json:"source"`
	Presence            string                                 `json:"presence"`
	ValueRepresentation string                                 `json:"valueRepresentation"`
	ValueDigest         *string                                `json:"valueDigest"`
	CapabilityIdentity  *CandidateCredentialCapabilityIdentity `json:"capabilityIdentity"`
	OmissionReason      *string                                `json:"omissionReason"`
}

type CandidateEnvironmentManifest struct {
	Entries                       []CandidateEnvironmentEntry `json:"entries"`
	InheritedEnvironmentDiscarded bool                        `json:"inheritedEnvironmentDiscarded"`
	PolicyDigest                  string                      `json:"policyDigest"`
	ManifestDigest                string                      `json:"manifestDigest"`
}

type CandidateVariantInvocationManifest struct {
	ReceiptSequence     uint64                       `json:"receiptSequence"`
	VariantID           string                       `json:"variantId"`
	ArgvManifest        CandidateArgvManifest        `json:"argvManifest"`
	EnvironmentManifest CandidateEnvironmentManifest `json:"environmentManifest"`
	ManifestDigest      string                       `json:"manifestDigest"`
}

type CandidateReceiptIsolationAudit struct {
	AuditProviderIdentity      string   `json:"auditProviderIdentity"`
	AuditSessionID             string   `json:"auditSessionId"`
	LaunchAuditDigest          string   `json:"launchAuditDigest"`
	DenialAuditDigest          string   `json:"denialAuditDigest"`
	ExitAuditDigest            string   `json:"exitAuditDigest"`
	AncestorChainDigest        string   `json:"ancestorChainDigest"`
	BusinessRootDenialDigests  []string `json:"businessRootDenialDigests"`
	CredentialReadOnlyEnforced bool     `json:"credentialReadOnlyEnforced"`
	BusinessRootsDenied        bool     `json:"businessRootsDenied"`
	ScratchOnlyWriteEnforced   bool     `json:"scratchOnlyWriteEnforced"`
	NetworkPolicyEnforced      bool     `json:"networkPolicyEnforced"`
	AmbientStateDenied         bool     `json:"ambientStateDenied"`
}

type CandidateExecutionReceipt struct {
	APIVersion                  string                             `json:"apiVersion"`
	Kind                        string                             `json:"kind"`
	SchemaVersion               uint64                             `json:"schemaVersion"`
	ReceiptID                   string                             `json:"receiptId"`
	ProbeRunID                  string                             `json:"probeRunId"`
	ReceiptSequence             uint64                             `json:"receiptSequence"`
	VariantID                   string                             `json:"variantId"`
	ProbeRunChallengeDigest     string                             `json:"probeRunChallengeDigest"`
	VariantChallengeDigest      string                             `json:"variantChallengeDigest"`
	PreviousReceiptDigest       *string                            `json:"previousReceiptDigest"`
	CandidateExecutableIdentity CandidateExecutableReceiptIdentity `json:"candidateExecutableIdentity"`
	InvocationManifest          CandidateVariantInvocationManifest `json:"invocationManifest"`
	ScratchRootIdentity         CandidateRootIdentity              `json:"scratchRootIdentity"`
	CredentialRootIdentity      CandidateRootIdentity              `json:"credentialRootIdentity"`
	BusinessRootIdentities      []CandidateRootIdentity            `json:"businessRootIdentities"`
	IsolationProfileDigest      string                             `json:"isolationProfileDigest"`
	TopologyDigest              string                             `json:"topologyDigest"`
	HostIdentityDigest          string                             `json:"hostIdentityDigest"`
	IsolationAudit              CandidateReceiptIsolationAudit     `json:"isolationAudit"`
	SessionID                   string                             `json:"sessionId"`
	ModelID                     string                             `json:"modelId"`
	ProtocolVersion             string                             `json:"protocolVersion"`
	PermissionMode              string                             `json:"permissionMode"`
	EventContract               string                             `json:"eventContract"`
	WorkerResultTransportDigest string                             `json:"workerResultTransportDigest"`
	TranscriptDigest            string                             `json:"transcriptDigest"`
	MarkerDigest                string                             `json:"markerDigest"`
	StartedAt                   string                             `json:"startedAt"`
	CompletedAt                 string                             `json:"completedAt"`
	ReceiptAuthorityKeyID       string                             `json:"receiptAuthorityKeyId"`
	ReceiptAuthorityKeyEpoch    uint64                             `json:"receiptAuthorityKeyEpoch"`
	SignatureAlgorithm          string                             `json:"signatureAlgorithm"`
	SignatureEncoding           string                             `json:"signatureEncoding"`
	Signature                   string                             `json:"signature"`
	RecordDigest                string                             `json:"recordDigest"`
}

func (receipt CandidateExecutionReceipt) signingBytes() ([]byte, error) {
	if !validSHA256Digest(receipt.RecordDigest) {
		return nil, errors.New("qoder candidate receipt record digest is invalid")
	}
	return []byte(candidateReceiptSigningDomain + receipt.RecordDigest), nil
}

func (receipt CandidateExecutionReceipt) digest() (string, error) {
	detached := receipt
	detached.Signature = ""
	detached.RecordDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	delete(value, "signature")
	delete(value, "recordDigest")
	data, err = json.Marshal(value)
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
	receiptRole                 string
	receiptIssuer               string
	receiptKeyID                string
	receiptKeyEpoch             uint64
	receiptPublicKey            ed25519.PublicKey
	receiptLedgerTailDigest     string
	credentialProviderKeyID     string
	credentialProviderKeyEpoch  uint64
	credentialProviderPublicKey ed25519.PublicKey
	verifierKeyID               string
	verifierPublicKey           ed25519.PublicKey
}

// NewCandidateLiveVerifier remains an exported hard-disabled boundary while
// ADR 0034 is Proposed. A caller cannot establish its own receipt trust root.
func NewCandidateLiveVerifier(sandbox CandidateProbeSandbox, receiptIssuer string, receiptKey ed25519.PublicKey) (*CandidateLiveVerifier, error) {
	return nil, port.Permanent(ErrConformancePending)
}

func newCandidateLiveVerifier(sandbox CandidateProbeSandbox, policy candidateAuthorityPolicy, verifierPrivateKey ed25519.PrivateKey) (*CandidateLiveVerifier, error) {
	if sandbox == nil || policy.receiptRole != "receipt" || !validCandidateASCII(policy.receiptIssuer) || !validCandidateASCII(policy.receiptKeyID) || policy.receiptKeyEpoch > candidateMaxJSONInteger || len(policy.receiptPublicKey) != ed25519.PublicKeySize || !validSHA256Digest(policy.receiptLedgerTailDigest) || !validCandidateASCII(policy.credentialProviderKeyID) || policy.credentialProviderKeyEpoch > candidateMaxJSONInteger || len(policy.credentialProviderPublicKey) != ed25519.PublicKeySize || !validCandidateASCII(policy.verifierKeyID) || len(policy.verifierPublicKey) != ed25519.PublicKeySize || len(verifierPrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(verifierPrivateKey.Public().(ed25519.PublicKey), policy.verifierPublicKey) {
		return nil, errors.New("qoder candidate authority policy is invalid")
	}
	policy.receiptPublicKey = append(ed25519.PublicKey(nil), policy.receiptPublicKey...)
	policy.credentialProviderPublicKey = append(ed25519.PublicKey(nil), policy.credentialProviderPublicKey...)
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
	capabilityIDs := map[string]struct{}{}
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
		if _, replay := capabilityIDs[result.capabilityID]; replay {
			return nil, "", errors.New("qoder candidate live probe replayed a credential capability across variants")
		}
		capabilityIDs[result.capabilityID] = struct{}{}
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
		CapabilitiesDigest: digestObservedCandidateCapabilities(), ProbeProfileDigest: candidateObservedProfileDigest(), ArgvDigest: observedArgvDigest, EnvironmentDigest: observedEnvironmentDigest, ToolPolicyDigest: candidateObservedToolPolicyDigest(), WorkerResultTransportDigest: expectedWorkerResultTransportDigest(),
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
	capabilityID                string
}

func (verifier *CandidateLiveVerifier) runVariant(ctx context.Context, request CandidateLiveProbeRequest, handles *candidateProbeHandles, scratch CandidateBoundObject, probeRunID, previousReceiptDigest string, index int, variant candidateProbeVariant) (candidateVariantResult, error) {
	challenge := candidateVariantChallenge(request.ChallengeDigest, index)
	arguments := buildArgs(variant.model, candidateBoundCredentialToken, candidateBoundScratchToken, variant.disableAllTools)
	environment := candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken)
	invocation := CandidateProbeInvocation{
		ProbeRunID: probeRunID, ReceiptSequence: index + 1, PreviousReceiptDigest: previousReceiptDigest,
		VariantIndex: index, Executable: handles.executable, Arguments: arguments, Environment: environment,
		WorkingDirectory: scratch, ScratchRoot: handles.scratchRoot, CredentialConfigRoot: handles.credential, WritableRoots: []CandidateBoundObject{scratch}, BusinessRepositoryRoots: handles.business,
		ChallengeDigest: challenge, ProbeRunChallengeDigest: request.ChallengeDigest, ExpectedModel: variant.model,
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
	capability, _ := credentialCapabilityFromManifest(receipt.InvocationManifest.EnvironmentManifest)
	return candidateVariantResult{transcriptDigest: receipt.TranscriptDigest, receiptDigest: receiptDigest, receiptDocument: append([]byte(nil), result.ExecutionReceipt...), sessionID: receipt.SessionID, binaryVersion: receipt.CandidateExecutableIdentity.BinaryVersion, normalizedArguments: normalizedArguments, normalizedEnvironmentDigest: normalizedEnvironmentDigest, startedAt: startedAt, completedAt: completedAt, capabilityID: capability.CapabilityID}, nil
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
	if !result.AuthorityBacked {
		return CandidateExecutionReceipt{}, "", errors.New("qoder legacy or hermetic receipt cannot authorize a live probe")
	}
	receipt, err := decodeCandidateExecutionReceipt(document)
	if err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	if receipt.APIVersion != candidateReceiptAPIVersion || receipt.Kind != candidateReceiptKind || receipt.SchemaVersion != candidateReceiptSchemaVersion || receipt.ReceiptAuthorityKeyID != verifier.policy.receiptKeyID || receipt.ReceiptAuthorityKeyEpoch != verifier.policy.receiptKeyEpoch || receipt.SignatureAlgorithm != candidateSignatureAlgorithm || receipt.SignatureEncoding != candidateSignatureEncoding || validateCandidateExactReceipt(receipt) != nil {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt provenance is invalid")
	}
	computedDigest, err := receipt.digest()
	if err != nil || computedDigest != receipt.RecordDigest {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt record digest is invalid")
	}
	message, err := receipt.signingBytes()
	if err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	signature, err := decodeCandidateRawURL(receipt.Signature)
	if err != nil || !ed25519.Verify(verifier.policy.receiptPublicKey, message, signature) {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt signature is not trusted")
	}
	executable := candidateExecutableReceiptIdentity(invocation.Executable, capture.cliVersion)
	scratchRoot := candidateRootIdentity(invocation.WorkingDirectory.Identity)
	credentialRoot := candidateRootIdentity(invocation.CredentialConfigRoot.Identity)
	businessRoots := candidateRootIdentities(invocation.BusinessRepositoryRoots)
	previousOK := (invocation.ReceiptSequence == 1 && receipt.PreviousReceiptDigest == nil) || (invocation.ReceiptSequence > 1 && receipt.PreviousReceiptDigest != nil && *receipt.PreviousReceiptDigest == invocation.PreviousReceiptDigest)
	capability, capabilityErr := credentialCapabilityFromManifest(receipt.InvocationManifest.EnvironmentManifest)
	expectedManifest, manifestErr := candidateInvocationManifest(invocation, capability)
	capabilityAuthorityErr := verifyCandidateCredentialCapability(capability, verifier.policy)
	if receipt.ProbeRunID != invocation.ProbeRunID || receipt.ReceiptSequence != uint64(invocation.ReceiptSequence) || receipt.VariantID != candidateVariantID(invocation.VariantIndex) || receipt.ProbeRunChallengeDigest != invocation.ProbeRunChallengeDigest || receipt.VariantChallengeDigest != invocation.ChallengeDigest || !previousOK || receipt.CandidateExecutableIdentity != executable || receipt.ScratchRootIdentity != scratchRoot || receipt.CredentialRootIdentity != credentialRoot || !equalCandidateRootIdentities(receipt.BusinessRootIdentities, businessRoots) || capabilityErr != nil || capabilityAuthorityErr != nil || manifestErr != nil || !candidateManifestsEqual(receipt.InvocationManifest, expectedManifest) || receipt.IsolationProfileDigest != candidateObservedProfileDigest() || receipt.TopologyDigest != invocation.ExpectedTopologyDigest || !validSHA256Digest(receipt.HostIdentityDigest) || receipt.TranscriptDigest != digestBytes(result.Transcript) || receipt.SessionID != capture.sessionID || receipt.ModelID != capture.model || receipt.CandidateExecutableIdentity.BinaryVersion != capture.cliVersion || receipt.ProtocolVersion != capture.protocolVersion || receipt.PermissionMode != capture.permissionMode || receipt.EventContract != conformanceEventContract || receipt.WorkerResultTransportDigest != expectedWorkerResultTransportDigest() || receipt.MarkerDigest != markerDigest {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt does not bind actual execution")
	}
	if validateCandidateReceiptIsolationAudit(receipt.IsolationAudit, len(businessRoots), receipt.TopologyDigest) != nil {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt isolation authority is invalid")
	}
	if _, _, err := normalizeActualReceiptProfile(receipt, invocation); err != nil {
		return CandidateExecutionReceipt{}, "", err
	}
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	capabilityIssued, capabilityIssueErr := time.Parse(time.RFC3339Nano, capability.IssuedAt)
	capabilityExpires, capabilityExpiryErr := time.Parse(time.RFC3339Nano, capability.ExpiresAt)
	now := verifier.now().UTC()
	if startErr != nil || completeErr != nil || capabilityIssueErr != nil || capabilityExpiryErr != nil || started.Before(capabilityIssued) || completed.After(capabilityExpires) || completed.Before(started) || completed.After(now) || completed.Sub(started) > candidateReceiptMaxExecution || now.Sub(started) > maxConformanceValidity {
		return CandidateExecutionReceipt{}, "", errors.New("qoder candidate live probe receipt time bounds are invalid")
	}
	return receipt, receipt.RecordDigest, nil
}

func decodeCandidateExecutionReceipt(document []byte) (CandidateExecutionReceipt, error) {
	if len(document) == 0 || len(document) > candidateReceiptLimit {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt is empty or oversized")
	}
	if err := rejectCandidateDuplicateMembers(document); err != nil {
		return CandidateExecutionReceipt{}, errors.New("qoder candidate live probe receipt has duplicate members")
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

func rejectCandidateDuplicateMembers(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("invalid object member")
				}
				if _, duplicate := seen[name]; duplicate {
					return errors.New("duplicate object member")
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validateCandidateLiveProbeRequest(request CandidateLiveProbeRequest) error {
	if !validCandidateASCII(request.RunnerID) || request.RunnerID == adapterID || !validCandidateASCII(request.RunnerVersion) || request.AuthorityGeneration == 0 || request.AuthorityGeneration > candidateMaxJSONInteger || !validCandidateASCII(request.TrustRootKeyID) || !validSHA256Digest(request.ProbeArtifactDigest) || !validSHA256Digest(request.ChallengeDigest) || request.Validity <= 0 || request.Validity > maxConformanceValidity || !validCandidateASCII(request.Model) || len(request.BusinessRepositoryRoots) == 0 || len(request.BusinessRepositoryRoots) > 256 {
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
		leftData, _ := canonical.JSON(mustCandidateJSON(candidateRootIdentity(handles.business[left].Identity)))
		rightData, _ := canonical.JSON(mustCandidateJSON(candidateRootIdentity(handles.business[right].Identity)))
		return bytes.Compare(leftData, rightData) < 0
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
	data, _ := json.Marshal(map[string]any{"probeRunId": invocation.ProbeRunID, "receiptSequence": invocation.ReceiptSequence, "previousReceiptDigest": invocation.PreviousReceiptDigest, "invocationManifestDigest": invocation.InvocationManifestDigest, "variantIndex": invocation.VariantIndex, "executable": invocation.Executable.Identity, "executableDigest": candidateExecutableDigest(invocation.Executable), "arguments": invocation.Arguments, "environment": invocation.Environment, "workingDirectory": invocation.WorkingDirectory.Identity, "credentialConfigRoot": invocation.CredentialConfigRoot.Identity, "writableRoots": objectIdentities(invocation.WritableRoots), "businessRepositoryRoots": objectIdentities(invocation.BusinessRepositoryRoots), "probeRunChallengeDigest": invocation.ProbeRunChallengeDigest, "challengeDigest": invocation.ChallengeDigest, "expectedModel": invocation.ExpectedModel, "expectedTopologyDigest": invocation.ExpectedTopologyDigest})
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
	capability, err := credentialCapabilityFromManifest(receipt.InvocationManifest.EnvironmentManifest)
	if err != nil {
		return nil, "", err
	}
	expected, err := candidateInvocationManifest(invocation, capability)
	if err != nil || !candidateManifestsEqual(receipt.InvocationManifest, expected) {
		return nil, "", errors.New("qoder candidate live probe invocation manifest differs from held execution")
	}
	return normalizeProbeArguments(invocation.Arguments, CandidateLiveProbeRequest{Model: invocation.ExpectedModel}, candidateBoundScratchToken), expectedProbeEnvironmentDigest(), nil
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

func candidateVariantID(index int) string {
	if index < 0 || index > 3 {
		return ""
	}
	return []string{"model-omitted-tools-omitted", "model-present-tools-omitted", "model-omitted-tools-empty", "model-present-tools-empty"}[index]
}

func candidateRootIdentity(identity CandidateObjectIdentity) CandidateRootIdentity {
	data, _ := json.Marshal(map[string]uint64{"device": identity.Device, "inode": identity.Inode})
	canonicalData, _ := canonical.JSON(data)
	return CandidateRootIdentity{Device: identity.Device, Inode: identity.Inode, IdentityDigest: digestBytes(canonicalData)}
}

func candidateRootIdentities(objects []CandidateBoundObject) []CandidateRootIdentity {
	result := make([]CandidateRootIdentity, len(objects))
	for index := range objects {
		result[index] = candidateRootIdentity(objects[index].Identity)
	}
	return result
}

func equalCandidateRootIdentities(left, right []CandidateRootIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func candidateExecutableReceiptIdentity(object CandidateBoundObject, binaryVersion string) CandidateExecutableReceiptIdentity {
	pathBytes := []byte(object.CanonicalPath)
	return CandidateExecutableReceiptIdentity{
		RealpathBytes: CandidateCanonicalPathBytes{Encoding: candidateSignatureEncoding, Bytes: base64.RawURLEncoding.EncodeToString(pathBytes), Digest: digestBytes(pathBytes)},
		Device:        object.Identity.Device, Inode: object.Identity.Inode, Digest: object.Digest, BinaryVersion: binaryVersion,
	}
}

func candidateInvocationManifest(invocation CandidateProbeInvocation, capability CandidateCredentialCapabilityIdentity) (CandidateVariantInvocationManifest, error) {
	if err := validateCandidateCredentialCapability(capability, invocation.ProbeRunID, candidateVariantID(invocation.VariantIndex)); err != nil {
		return CandidateVariantInvocationManifest{}, err
	}
	argv := CandidateArgvManifest{Entries: make([]CandidateArgvEntry, 0, len(invocation.Arguments))}
	for index, value := range invocation.Arguments {
		source, representation := "fixed-literal", "literal"
		literal := value
		literalValue := &literal
		if value == invocation.ExpectedModel && invocation.ExpectedModel != "" {
			source, representation, literalValue = "model-id", "digest-only", nil
		} else if value == candidateBoundCredentialToken || value == candidateBoundScratchToken {
			representation, literalValue = "digest-only", nil
		} else if value == "" {
			representation, literalValue = "digest-only", nil
			if index > 0 && invocation.Arguments[index-1] == "--tools" {
				source = "tools-empty"
			}
		}
		argv.Entries = append(argv.Entries, CandidateArgvEntry{Index: uint64(index), Source: source, Representation: representation, LiteralValue: literalValue, ValueDigest: digestBytes([]byte(value))})
	}
	argv.ManifestDigest = digestRecordWithoutFields(argv, "manifestDigest")
	environment := CandidateEnvironmentManifest{InheritedEnvironmentDiscarded: true, PolicyDigest: expectedProbeEnvironmentDigest()}
	for _, value := range invocation.Environment {
		name, actual, ok := strings.Cut(value, "=")
		if !ok || name == "" {
			return CandidateVariantInvocationManifest{}, errors.New("qoder candidate environment manifest is malformed")
		}
		entry := CandidateEnvironmentEntry{Name: name, Source: "fixed-policy", Presence: "present", ValueRepresentation: "digest-only"}
		if name == "HOME" || name == "XDG_CONFIG_HOME" {
			copy := capability
			entry.Source, entry.ValueRepresentation, entry.CapabilityIdentity = "credential-capability", "capability-identity", &copy
		} else if actual == "" {
			entry.ValueRepresentation = "literal-empty"
		} else {
			digest := digestBytes([]byte(actual))
			entry.ValueDigest = &digest
		}
		environment.Entries = append(environment.Entries, entry)
	}
	sort.Slice(environment.Entries, func(left, right int) bool { return environment.Entries[left].Name < environment.Entries[right].Name })
	environment.ManifestDigest = digestRecordWithoutFields(environment, "manifestDigest")
	manifest := CandidateVariantInvocationManifest{ReceiptSequence: uint64(invocation.ReceiptSequence), VariantID: candidateVariantID(invocation.VariantIndex), ArgvManifest: argv, EnvironmentManifest: environment}
	manifest.ManifestDigest = digestRecordWithoutFields(manifest, "manifestDigest")
	return manifest, nil
}

func digestRecordWithoutFields(value any, fields ...string) string {
	data, _ := json.Marshal(value)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	for _, field := range fields {
		delete(object, field)
	}
	data, _ = json.Marshal(object)
	canonicalData, _ := canonical.JSON(data)
	return digestBytes(canonicalData)
}

func candidateManifestsEqual(left, right CandidateVariantInvocationManifest) bool {
	leftData, _ := json.Marshal(left)
	rightData, _ := json.Marshal(right)
	leftData, _ = canonical.JSON(leftData)
	rightData, _ = canonical.JSON(rightData)
	return bytes.Equal(leftData, rightData)
}

func credentialCapabilityFromManifest(manifest CandidateEnvironmentManifest) (CandidateCredentialCapabilityIdentity, error) {
	var found *CandidateCredentialCapabilityIdentity
	for _, entry := range manifest.Entries {
		if entry.Source != "credential-capability" {
			continue
		}
		if entry.CapabilityIdentity == nil {
			return CandidateCredentialCapabilityIdentity{}, errors.New("qoder credential capability identity is missing")
		}
		if found == nil {
			copy := *entry.CapabilityIdentity
			found = &copy
		} else if *found != *entry.CapabilityIdentity {
			return CandidateCredentialCapabilityIdentity{}, errors.New("qoder credential capability identity is inconsistent")
		}
	}
	if found == nil {
		return CandidateCredentialCapabilityIdentity{}, errors.New("qoder credential capability identity is missing")
	}
	return *found, nil
}

func validateCandidateCredentialCapability(value CandidateCredentialCapabilityIdentity, probeRunID, variantID string) error {
	capabilityBytes, err := decodeCandidateRawURL(value.CapabilityID)
	if err != nil || len(capabilityBytes) != 16 ||
		!validCandidateASCII(value.APIVersion) || value.APIVersion != candidateReceiptAPIVersion ||
		!validCandidateASCII(value.Kind) || value.Kind != "QoderCredentialCapabilityIdentity" || value.SchemaVersion != 1 ||
		!validCandidateASCII(value.ProviderIdentity) || !validCandidateASCII(value.CapabilityID) ||
		!validCandidateASCII(value.ProbeRunID) || value.ProbeRunID != probeRunID ||
		!validCandidateASCII(value.VariantID) || value.VariantID != variantID ||
		!validCandidateASCII(value.CapabilityClass) || !validSHA256Digest(value.PolicyScopeDigest) ||
		!validCandidateTimestamp(value.IssuedAt) || !validCandidateTimestamp(value.ExpiresAt) ||
		!validCandidateASCII(value.ProviderKeyID) || value.ProviderKeyEpoch > candidateMaxJSONInteger ||
		!validCandidateASCII(value.SignatureAlgorithm) || value.SignatureAlgorithm != candidateSignatureAlgorithm ||
		!validCandidateASCII(value.SignatureEncoding) || value.SignatureEncoding != candidateSignatureEncoding ||
		!validCandidateASCII(value.Signature) || !validSHA256Digest(value.RecordDigest) {
		return errors.New("qoder credential capability identity is invalid")
	}
	signature, err := decodeCandidateRawURL(value.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || value.RecordDigest != value.digest() {
		return errors.New("qoder credential capability signature encoding is invalid")
	}
	issuedAt, _ := time.Parse(time.RFC3339Nano, value.IssuedAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, value.ExpiresAt)
	if !issuedAt.Before(expiresAt) {
		return errors.New("qoder credential capability time bounds are invalid")
	}
	return nil
}

func verifyCandidateCredentialCapability(value CandidateCredentialCapabilityIdentity, policy candidateAuthorityPolicy) error {
	if value.ProviderKeyID != policy.credentialProviderKeyID || value.ProviderKeyEpoch != policy.credentialProviderKeyEpoch || value.RecordDigest != value.digest() {
		return errors.New("qoder credential capability authority identity is not trusted")
	}
	message, err := value.signingBytes()
	if err != nil {
		return err
	}
	signature, err := decodeCandidateRawURL(value.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(policy.credentialProviderPublicKey, message, signature) {
		return errors.New("qoder credential capability signature is not trusted")
	}
	return nil
}

func validateCandidateInvocationManifest(manifest CandidateVariantInvocationManifest, probeRunID, variantID string) error {
	if !validCandidateASCII(manifest.VariantID) || manifest.VariantID != variantID || len(manifest.ArgvManifest.Entries) == 0 || len(manifest.ArgvManifest.Entries) > 64 || len(manifest.EnvironmentManifest.Entries) > 256 || manifest.ManifestDigest != digestRecordWithoutFields(manifest, "manifestDigest") || manifest.ArgvManifest.ManifestDigest != digestRecordWithoutFields(manifest.ArgvManifest, "manifestDigest") || manifest.EnvironmentManifest.ManifestDigest != digestRecordWithoutFields(manifest.EnvironmentManifest, "manifestDigest") || !manifest.EnvironmentManifest.InheritedEnvironmentDiscarded || manifest.EnvironmentManifest.PolicyDigest != expectedProbeEnvironmentDigest() {
		return errors.New("qoder candidate invocation manifest digest is invalid")
	}
	for index, entry := range manifest.ArgvManifest.Entries {
		validSource := validCandidateASCII(entry.Source) && (entry.Source == "fixed-literal" || entry.Source == "model-id" || entry.Source == "probe-prompt" || entry.Source == "tools-empty")
		validRepresentation := validCandidateASCII(entry.Representation) && ((entry.Representation == "literal" && entry.Source == "fixed-literal" && entry.LiteralValue != nil && validCandidateLiteral(*entry.LiteralValue)) || (entry.Representation == "digest-only" && entry.LiteralValue == nil))
		digestOnlySource := entry.Source == "fixed-literal" || entry.Source == "model-id" || entry.Source == "probe-prompt" || entry.Source == "tools-empty"
		if entry.Index != uint64(index) || !validSource || !validRepresentation || (entry.Representation == "digest-only" && !digestOnlySource) || !validSHA256Digest(entry.ValueDigest) || (entry.LiteralValue != nil && digestBytes([]byte(*entry.LiteralValue)) != entry.ValueDigest) {
			return errors.New("qoder candidate argv manifest is invalid")
		}
	}
	for index, entry := range manifest.EnvironmentManifest.Entries {
		if !candidateEnvironmentName(entry.Name) || (index > 0 && manifest.EnvironmentManifest.Entries[index-1].Name >= entry.Name) {
			return errors.New("qoder candidate environment manifest order is invalid")
		}
		switch entry.Source {
		case "fixed-policy":
			if !validCandidateASCII(entry.Source) || !validCandidateASCII(entry.Presence) || !validCandidateASCII(entry.ValueRepresentation) || entry.Presence != "present" || entry.CapabilityIdentity != nil || entry.OmissionReason != nil || !((entry.ValueRepresentation == "digest-only" && entry.ValueDigest != nil && validSHA256Digest(*entry.ValueDigest)) || (entry.ValueRepresentation == "literal-empty" && entry.ValueDigest == nil)) {
				return errors.New("qoder fixed environment entry is invalid")
			}
		case "credential-capability":
			if !validCandidateASCII(entry.Source) || !validCandidateASCII(entry.Presence) || !validCandidateASCII(entry.ValueRepresentation) || entry.Presence != "present" || entry.ValueRepresentation != "capability-identity" || entry.ValueDigest != nil || entry.CapabilityIdentity == nil || entry.OmissionReason != nil {
				return errors.New("qoder credential environment entry is invalid")
			}
		case "cleared-setting-source":
			if !validCandidateASCII(entry.Source) || !validCandidateASCII(entry.Presence) || !validCandidateASCII(entry.ValueRepresentation) || entry.Presence != "omitted" || entry.ValueRepresentation != "omitted" || entry.ValueDigest != nil || entry.CapabilityIdentity != nil || entry.OmissionReason == nil || !validCandidateASCII(*entry.OmissionReason) || (*entry.OmissionReason != "policy-absent" && *entry.OmissionReason != "setting-source-cleared") {
				return errors.New("qoder omitted environment entry is invalid")
			}
		default:
			return errors.New("qoder environment entry source is invalid")
		}
	}
	capability, err := credentialCapabilityFromManifest(manifest.EnvironmentManifest)
	if err != nil {
		return err
	}
	return validateCandidateCredentialCapability(capability, probeRunID, variantID)
}

func candidateEnvironmentName(value string) bool {
	if !validCandidateASCII(value) || len(value) > 128 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for _, character := range []byte(value[1:]) {
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validateCandidateReceiptIsolationAudit(audit CandidateReceiptIsolationAudit, businessRootCount int, topologyDigest string) error {
	if !validCandidateASCII(audit.AuditProviderIdentity) || !validCandidateASCII(audit.AuditSessionID) || audit.AncestorChainDigest != topologyDigest || len(audit.BusinessRootDenialDigests) != businessRootCount || !audit.CredentialReadOnlyEnforced || !audit.BusinessRootsDenied || !audit.ScratchOnlyWriteEnforced || !audit.NetworkPolicyEnforced || !audit.AmbientStateDenied {
		return errors.New("qoder receipt isolation audit is incomplete")
	}
	for _, digest := range []string{audit.LaunchAuditDigest, audit.DenialAuditDigest, audit.ExitAuditDigest, audit.AncestorChainDigest} {
		if !validSHA256Digest(digest) {
			return errors.New("qoder receipt isolation audit digest is invalid")
		}
	}
	if !validCandidateSortedDigests(audit.BusinessRootDenialDigests, businessRootCount, businessRootCount) {
		return errors.New("qoder receipt isolation audit digest array is invalid")
	}
	return nil
}

func validateCandidateExactReceipt(receipt CandidateExecutionReceipt) error {
	receiptID, receiptIDErr := decodeCandidateRawURL(receipt.ReceiptID)
	signature, signatureErr := decodeCandidateRawURL(receipt.Signature)
	if receiptIDErr != nil || len(receiptID) != 16 || signatureErr != nil || len(signature) != ed25519.SignatureSize ||
		!validCandidateASCII(receipt.APIVersion) || receipt.APIVersion != candidateReceiptAPIVersion ||
		!validCandidateASCII(receipt.Kind) || receipt.Kind != candidateReceiptKind || receipt.SchemaVersion != candidateReceiptSchemaVersion ||
		!validCandidateASCII(receipt.ReceiptID) || !validCandidateASCII(receipt.ProbeRunID) ||
		receipt.ReceiptSequence < 1 || receipt.ReceiptSequence > 4 || !validCandidateASCII(receipt.VariantID) ||
		!validSHA256Digest(receipt.ProbeRunChallengeDigest) || !validSHA256Digest(receipt.VariantChallengeDigest) ||
		(receipt.ReceiptSequence == 1 && receipt.PreviousReceiptDigest != nil) ||
		(receipt.ReceiptSequence > 1 && (receipt.PreviousReceiptDigest == nil || !validSHA256Digest(*receipt.PreviousReceiptDigest))) ||
		!validSHA256Digest(receipt.IsolationProfileDigest) || !validSHA256Digest(receipt.TopologyDigest) ||
		!validSHA256Digest(receipt.HostIdentityDigest) || !validSHA256Digest(receipt.TranscriptDigest) || !validSHA256Digest(receipt.MarkerDigest) ||
		!validCandidateASCII(receipt.SessionID) || !validCandidateASCII(receipt.ModelID) ||
		!validCandidateASCII(receipt.ProtocolVersion) || !validCandidateASCII(receipt.PermissionMode) || !validCandidateASCII(receipt.EventContract) || !validSHA256Digest(receipt.WorkerResultTransportDigest) ||
		!validCandidateTimestamp(receipt.StartedAt) || !validCandidateTimestamp(receipt.CompletedAt) ||
		!validCandidateASCII(receipt.ReceiptAuthorityKeyID) || receipt.ReceiptAuthorityKeyEpoch > candidateMaxJSONInteger ||
		!validCandidateASCII(receipt.SignatureAlgorithm) || receipt.SignatureAlgorithm != candidateSignatureAlgorithm ||
		!validCandidateASCII(receipt.SignatureEncoding) || receipt.SignatureEncoding != candidateSignatureEncoding || !validCandidateASCII(receipt.Signature) ||
		!validSHA256Digest(receipt.RecordDigest) {
		return errors.New("qoder exact receipt scalar is invalid")
	}
	pathBytes, err := decodeCandidateRawURL(receipt.CandidateExecutableIdentity.RealpathBytes.Bytes)
	if err != nil || !validCandidateASCII(receipt.CandidateExecutableIdentity.RealpathBytes.Encoding) || receipt.CandidateExecutableIdentity.RealpathBytes.Encoding != candidateSignatureEncoding || len(pathBytes) == 0 || len(pathBytes) > 4096 || pathBytes[0] != '/' || bytes.IndexByte(pathBytes, 0) >= 0 || filepath.Clean(string(pathBytes)) != string(pathBytes) || string(pathBytes) == "/" || receipt.CandidateExecutableIdentity.RealpathBytes.Digest != digestBytes(pathBytes) || !validSHA256Digest(receipt.CandidateExecutableIdentity.Digest) || !validCandidateASCII(receipt.CandidateExecutableIdentity.BinaryVersion) || !isSupportedBinaryVersion(receipt.CandidateExecutableIdentity.BinaryVersion) {
		return errors.New("qoder exact receipt executable identity is invalid")
	}
	for _, root := range append([]CandidateRootIdentity{receipt.ScratchRootIdentity, receipt.CredentialRootIdentity}, receipt.BusinessRootIdentities...) {
		if root != candidateRootIdentity(CandidateObjectIdentity{Device: root.Device, Inode: root.Inode}) {
			return errors.New("qoder exact receipt root identity is invalid")
		}
	}
	if len(receipt.BusinessRootIdentities) == 0 || len(receipt.BusinessRootIdentities) > 256 {
		return errors.New("qoder exact receipt business roots are invalid")
	}
	for index := 1; index < len(receipt.BusinessRootIdentities); index++ {
		left, _ := canonical.JSON(mustCandidateJSON(receipt.BusinessRootIdentities[index-1]))
		right, _ := canonical.JSON(mustCandidateJSON(receipt.BusinessRootIdentities[index]))
		if bytes.Compare(left, right) >= 0 {
			return errors.New("qoder exact receipt business roots are not canonical ordered")
		}
	}
	if receipt.InvocationManifest.ReceiptSequence != receipt.ReceiptSequence || receipt.InvocationManifest.VariantID != receipt.VariantID || validateCandidateInvocationManifest(receipt.InvocationManifest, receipt.ProbeRunID, receipt.VariantID) != nil || validateCandidateReceiptIsolationAudit(receipt.IsolationAudit, len(receipt.BusinessRootIdentities), receipt.TopologyDigest) != nil {
		return errors.New("qoder exact receipt nested contract is invalid")
	}
	return nil
}

func validCandidateASCII(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func decodeCandidateRawURL(value string) ([]byte, error) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("qoder base64url value is empty or contains a line break")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("qoder base64url value is not canonical unpadded encoding")
	}
	return decoded, nil
}

func validCandidateLiteral(value string) bool {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || !unicode.IsPrint(character) {
			return false
		}
	}
	return true
}

func validCandidateTimestamp(value string) bool {
	if len(value) != len("2006-01-02T15:04:05Z") && len(value) != len("2006-01-02T15:04:05.000000000Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		return false
	}
	layout := "2006-01-02T15:04:05Z"
	if len(value) == len("2006-01-02T15:04:05.000000000Z") {
		layout = "2006-01-02T15:04:05.000000000Z"
	}
	return value == parsed.UTC().Format(layout)
}

func validCandidateSortedDigests(values []string, minimum, maximum int) bool {
	if len(values) < minimum || len(values) > maximum {
		return false
	}
	for index, value := range values {
		if !validSHA256Digest(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func mustCandidateJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
func candidateObservedProfileDigest() string {
	data, _ := json.Marshal(map[string]any{"ambientCredentialInheritance": false, "businessRepositoryAccess": false, "eventContract": conformanceEventContract, "isolatedWorkingDirectory": true, "permissionMode": qoderPermissionMode, "repositoryWritePermission": true, "settingSources": []string{}, "writableRoots": []string{"$ISOLATED_WORKTREE"}})
	return digestBytes(data)
}
func candidateObservedToolPolicyDigest() string {
	data, _ := json.Marshal(map[string]any{"namedWorkerTools": []string{}, "providerPermissionMode": qoderPermissionMode, "providerDisallowedTools": []string{"Agent"}, "repositoryScope": "isolated-scratch-worktree"})
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
