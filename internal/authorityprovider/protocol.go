// Package authorityprovider implements the deterministic, transport-neutral
// core of ADR 0038. It deliberately contains no socket, SCM_RIGHTS, OS
// principal, secret-delivery, anchor, or adapter registry implementation.
package authorityprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	ProtocolVersion       = 1
	ControlFamily         = "marshal.agent-production-authority"
	IngressFamily         = "marshal.agent-credential-ingress"
	ControlAudience       = "marshal.agent-production-authority.local"
	IngressAudience       = "marshal.agent-credential-ingress.local"
	RequestSchema         = "marshal.agent-production-authority.request.v1"
	ResponseSchema        = "marshal.agent-production-authority.response.v1"
	IngressRequestSchema  = "marshal.agent-credential-ingress.request.v1"
	IngressResponseSchema = "marshal.agent-credential-ingress.response.v1"
	MaxEnvelopeBytes      = 64 << 10
)

type AuthorityProfile string

const (
	ProfileQoder AuthorityProfile = "qoder-cli-adr0034-v1"
	ProfileCodex AuthorityProfile = "codex-cli-adr0037-v1"
)

func (p AuthorityProfile) valid() bool { return p == ProfileQoder || p == ProfileCodex }

type Operation string

const (
	OperationDescribe                 Operation = "Describe"
	OperationBeginProbe               Operation = "BeginProbe"
	OperationAttachProbeCredential    Operation = "AttachProbeCredential"
	OperationRunProbeVariant          Operation = "RunProbeVariant"
	OperationFinalizeProbe            Operation = "FinalizeProbe"
	OperationReadCurrentBundle        Operation = "ReadCurrentBundle"
	OperationReadBundleLeafBatch      Operation = "ReadBundleLeafBatch"
	OperationStageBundleLeafBatch     Operation = "StageBundleLeafBatch"
	OperationPrepareEvidenceUpdate    Operation = "PrepareEvidenceUpdate"
	OperationPrepareRotation          Operation = "PrepareRotation"
	OperationPrepareRevocation        Operation = "PrepareRevocation"
	OperationCommitBundleUpdate       Operation = "CommitBundleUpdate"
	OperationInspectBundleTransaction Operation = "InspectBundleTransaction"
	OperationRecoverBundleTransaction Operation = "RecoverBundleTransaction"
	OperationPrepareLaunch            Operation = "PrepareLaunch"
	OperationCommitLaunch             Operation = "CommitLaunch"
	OperationAbortLaunch              Operation = "AbortLaunch"
	OperationInspectLaunch            Operation = "InspectLaunch"
	OperationWatchEpoch               Operation = "WatchEpoch"
)

// Slice A registers only operations with complete typed request and response
// validators. Known-but-unimplemented operations remain enum members and are
// rejected before authorization or side effects.
var controlOperations = map[Operation]struct{}{
	OperationDescribe: {}, OperationBeginProbe: {},
	OperationPrepareLaunch: {}, OperationCommitLaunch: {}, OperationAbortLaunch: {}, OperationInspectLaunch: {},
}

func (op Operation) validControl() bool { _, ok := controlOperations[op]; return ok }
func (op Operation) readOnly() bool {
	switch op {
	case OperationDescribe, OperationReadCurrentBundle, OperationReadBundleLeafBatch, OperationInspectBundleTransaction, OperationInspectLaunch, OperationWatchEpoch:
		return true
	default:
		return false
	}
}

type Principal string

type PeerIdentity struct {
	PrincipalDigest string
	Role            Principal
}

const (
	PrincipalConsumer           Principal = "marshal-consumer"
	PrincipalVerifierController Principal = "verifier-controller"
	PrincipalSecretProvider     Principal = "secret-provider"
	PrincipalEvidenceConfig     Principal = "evidence-config-authority"
	PrincipalRotation           Principal = "rotation-authority"
	PrincipalRevocation         Principal = "revocation-authority"
	PrincipalRecovery           Principal = "recovery-authority"
	PrincipalWorker             Principal = "worker"
)

var operationPrincipals = map[Operation]map[Principal]struct{}{
	OperationDescribe:      setOf(PrincipalConsumer, PrincipalVerifierController),
	OperationBeginProbe:    setOf(PrincipalVerifierController),
	OperationPrepareLaunch: setOf(PrincipalConsumer),
	OperationCommitLaunch:  setOf(PrincipalConsumer),
	OperationAbortLaunch:   setOf(PrincipalConsumer),
	OperationInspectLaunch: setOf(PrincipalConsumer),
}

func setOf(values ...Principal) map[Principal]struct{} {
	m := make(map[Principal]struct{}, len(values))
	for _, v := range values {
		m[v] = struct{}{}
	}
	return m
}

type FDRole string

const (
	FDCandidateExecutable  FDRole = "candidateExecutable"
	FDScratchRoot          FDRole = "scratchRoot"
	FDBusinessDenyRoot     FDRole = "businessDenyRoot"
	FDBundleLeaf           FDRole = "bundleLeaf"
	FDAuthorityRoot        FDRole = "authorityRoot"
	FDFenceRoot            FDRole = "fenceRoot"
	FDWorktree             FDRole = "worktree"
	FDControlRoot          FDRole = "controlRoot"
	FDControlInput         FDRole = "controlInput"
	FDControlOutput        FDRole = "controlOutput"
	FDMountNamespace       FDRole = "mountNamespace"
	FDCredentialRoot       FDRole = "credentialRoot"
	FDCredentialCapability FDRole = "credentialCapability"
)

type FDRef struct {
	Role  FDRole
	Index int
}

type APAPRequestEnvelopeV1 struct {
	SchemaVersion            string           `json:"schemaVersion"`
	ProtocolFamily           string           `json:"protocolFamily"`
	ProtocolVersion          int              `json:"protocolVersion"`
	Audience                 string           `json:"audience"`
	RequestID                string           `json:"requestId"`
	CommandID                string           `json:"commandId"`
	CallerPrincipalDigest    string           `json:"callerPrincipalDigest"`
	ProviderInstanceID       string           `json:"providerInstanceId"`
	AuthorityProfile         AuthorityProfile `json:"authorityProfile"`
	Operation                Operation        `json:"operation"`
	IssuedAt                 time.Time        `json:"issuedAt"`
	ExpiresAt                time.Time        `json:"expiresAt"`
	Nonce                    string           `json:"nonce"`
	ExpectedProviderSequence *uint64          `json:"expectedProviderSequence"`
	Payload                  json.RawMessage  `json:"payload"`
	RequestEnvelopeDigest    string           `json:"requestEnvelopeDigest"`
}

type APAPResponseEnvelopeV1 struct {
	SchemaVersion            string           `json:"schemaVersion"`
	ProtocolFamily           string           `json:"protocolFamily"`
	ProtocolVersion          int              `json:"protocolVersion"`
	Audience                 string           `json:"audience"`
	RequestID                string           `json:"requestId"`
	CommandID                string           `json:"commandId"`
	ProviderInstanceID       string           `json:"providerInstanceId"`
	AuthorityProfile         AuthorityProfile `json:"authorityProfile"`
	Operation                Operation        `json:"operation"`
	ObservedProviderSequence uint64           `json:"observedProviderSequence"`
	SafeCode                 SafeCode         `json:"safeCode"`
	SafeMessage              string           `json:"safeMessage"`
	Payload                  json.RawMessage  `json:"payload"`
	ResponseEnvelopeDigest   string           `json:"responseEnvelopeDigest"`
}

type CredentialIngressRequestV1 struct {
	SchemaVersion                           string           `json:"schemaVersion"`
	ProtocolFamily                          string           `json:"protocolFamily"`
	ProtocolVersion                         int              `json:"protocolVersion"`
	Audience                                string           `json:"audience"`
	RequestID                               string           `json:"requestId"`
	CommandID                               string           `json:"commandId"`
	SecretProviderPrincipalDigest           string           `json:"secretProviderPrincipalDigest"`
	ProviderInstanceID                      string           `json:"providerInstanceId"`
	AuthorityProfile                        AuthorityProfile `json:"authorityProfile"`
	ProbeSessionID                          string           `json:"probeSessionId"`
	TargetIsolationIdentityDigest           string           `json:"targetIsolationIdentityDigest"`
	CredentialIngressEndpointIdentityDigest string           `json:"credentialIngressEndpointIdentityDigest"`
	CredentialIngressTicketDigest           string           `json:"credentialIngressTicketDigest"`
	IssuedAt                                time.Time        `json:"issuedAt"`
	ExpiresAt                               time.Time        `json:"expiresAt"`
	Nonce                                   string           `json:"nonce"`
	Payload                                 json.RawMessage  `json:"payload"`
	RequestDigest                           string           `json:"requestDigest"`
}

type CredentialIngressResponseV1 struct {
	SchemaVersion      string           `json:"schemaVersion"`
	ProtocolFamily     string           `json:"protocolFamily"`
	ProtocolVersion    int              `json:"protocolVersion"`
	Audience           string           `json:"audience"`
	RequestID          string           `json:"requestId"`
	CommandID          string           `json:"commandId"`
	ProviderInstanceID string           `json:"providerInstanceId"`
	AuthorityProfile   AuthorityProfile `json:"authorityProfile"`
	SafeCode           SafeCode         `json:"safeCode"`
	SafeMessage        string           `json:"safeMessage"`
	Payload            json.RawMessage  `json:"payload"`
	ResponseDigest     string           `json:"responseDigest"`
}

type BeginProbePayload struct {
	CandidateIdentityDigest string    `json:"candidateIdentityDigest"`
	SuiteDigest             string    `json:"suiteDigest"`
	ProbeArtifactDigest     string    `json:"probeArtifactDigest"`
	PolicyDigest            string    `json:"policyDigest"`
	ChallengeDigest         string    `json:"challengeDigest"`
	Deadline                time.Time `json:"deadline"`
}

type AttachProbeCredentialPayload struct {
	ProbeSessionID                string    `json:"probeSessionId"`
	CapabilityIdentityDigest      string    `json:"capabilityIdentityDigest"`
	CapabilityPolicyDigest        string    `json:"capabilityPolicyDigest"`
	ServiceIdentityDigest         string    `json:"serviceIdentityDigest"`
	CapabilityExpiresAt           time.Time `json:"capabilityExpiresAt"`
	DeliveryNonce                 string    `json:"deliveryNonce"`
	TargetIsolationIdentityDigest string    `json:"targetIsolationIdentityDigest"`
}

type UpdateKind string

const (
	UpdateEvidence   UpdateKind = "evidence-update"
	UpdateRotation   UpdateKind = "planned-rotation"
	UpdateRevocation UpdateKind = "security-revocation"
)

type BundleLeafDescriptor struct {
	LeafKind  string `json:"leafKind"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

type StageBundleLeafBatchPayload struct {
	BundleTransactionID    string                 `json:"bundleTransactionId"`
	UpdateKind             UpdateKind             `json:"updateKind"`
	OrderedLeafDescriptors []BundleLeafDescriptor `json:"orderedLeafDescriptors"`
}

type DescribeSuccessPayload struct {
	ProviderBuildDigest string             `json:"providerBuildDigest"`
	Platform            string             `json:"platform"`
	Profiles            []AuthorityProfile `json:"profiles"`
}

type BeginProbeSuccessPayload struct {
	ProbeSessionID                          string    `json:"probeSessionId"`
	TargetIsolationIdentityDigest           string    `json:"targetIsolationIdentityDigest"`
	CredentialIngressEndpointIdentityDigest string    `json:"credentialIngressEndpointIdentityDigest"`
	ExpiresAt                               time.Time `json:"expiresAt"`
}

type StageBundleLeafBatchSuccessPayload struct {
	BundleTransactionID  string   `json:"bundleTransactionId"`
	StagedLeafDigests    []string `json:"stagedLeafDigests"`
	StagingReceiptDigest string   `json:"stagingReceiptDigest"`
}

type CredentialIngressSuccessPayload struct {
	DeliveryReceiptDigest string `json:"deliveryReceiptDigest"`
	InstallReceiptDigest  string `json:"installReceiptDigest"`
}

// PrepareLaunchPayload is the shared, non-secret launch projection. Every
// identity digest is derived from a held descriptor; the authority provider
// must still verify the corresponding FD table before creating a stopped child.
type PrepareLaunchPayload struct {
	TaskID                            string    `json:"taskId"`
	RunID                             string    `json:"runId"`
	AttemptID                         string    `json:"attemptId"`
	AuthorityNamespaceID              string    `json:"authorityNamespaceId"`
	LaunchNonce                       string    `json:"launchNonce"`
	APAPLaunchRequestDigest           string    `json:"apapLaunchRequestDigest"`
	ProfileRequestDigest              string    `json:"profileRequestDigest"`
	BundleDigest                      string    `json:"bundleDigest"`
	EvidenceDigest                    string    `json:"evidenceDigest"`
	ConfigDigest                      string    `json:"configDigest"`
	FenceDigest                       string    `json:"fenceDigest"`
	CandidateExecutableIdentityDigest string    `json:"candidateExecutableIdentityDigest"`
	AuthorityRootIdentityDigest       string    `json:"authorityRootIdentityDigest"`
	FenceRootIdentityDigest           string    `json:"fenceRootIdentityDigest"`
	WorktreeIdentityDigest            string    `json:"worktreeIdentityDigest"`
	ControlRootIdentityDigest         string    `json:"controlRootIdentityDigest"`
	ControlInputIdentityDigest        string    `json:"controlInputIdentityDigest"`
	ControlOutputIdentityDigest       string    `json:"controlOutputIdentityDigest"`
	MountNamespaceIdentityDigest      string    `json:"mountNamespaceIdentityDigest"`
	ArgvDigest                        string    `json:"argvDigest"`
	EnvironmentDigest                 string    `json:"environmentDigest"`
	Deadline                          time.Time `json:"deadline"`
}

type PrepareLaunchSuccessPayload struct {
	LaunchTransactionID     string          `json:"launchTransactionId"`
	APAPLaunchRequestDigest string          `json:"apapLaunchRequestDigest"`
	ProfileRequestDigest    string          `json:"profileRequestDigest"`
	LaunchReceiptDigest     string          `json:"launchReceiptDigest"`
	LaunchReceipt           json.RawMessage `json:"launchReceipt"`
	ReleaseIdentity         string          `json:"releaseIdentity"`
	Deadline                time.Time       `json:"deadline"`
}

type CommitLaunchPayload struct {
	LaunchTransactionID string `json:"launchTransactionId"`
	LaunchReceiptDigest string `json:"launchReceiptDigest"`
	ReleaseIdentity     string `json:"releaseIdentity"`
	DurableAcceptDigest string `json:"durableAcceptDigest"`
}

type CommitLaunchSuccessPayload struct {
	Status               string          `json:"status"`
	ReleaseReceiptDigest string          `json:"releaseReceiptDigest"`
	ReleaseReceipt       json.RawMessage `json:"releaseReceipt"`
}

type AbortLaunchPayload struct {
	LaunchTransactionID string `json:"launchTransactionId"`
	ReasonCode          string `json:"reasonCode"`
}

type AbortLaunchSuccessPayload struct {
	Status             string          `json:"status"`
	AbortReceiptDigest string          `json:"abortReceiptDigest"`
	AbortReceipt       json.RawMessage `json:"abortReceipt"`
}

type InspectLaunchPayload struct {
	AttemptID               string `json:"attemptId"`
	LaunchNonce             string `json:"launchNonce"`
	APAPLaunchRequestDigest string `json:"apapLaunchRequestDigest"`
	ProfileRequestDigest    string `json:"profileRequestDigest"`
}

type InspectLaunchSuccessPayload struct {
	Status               string `json:"status"`
	LaunchTransactionID  string `json:"launchTransactionId"`
	ChildIdentityDigest  string `json:"childIdentityDigest"`
	LaunchReceiptDigest  string `json:"launchReceiptDigest"`
	ReleaseReceiptDigest string `json:"releaseReceiptDigest"`
	AbortReceiptDigest   string `json:"abortReceiptDigest"`
}

var noncePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func DecodeControlRequest(raw []byte, peer PeerIdentity, now time.Time, fds []FDRef) (APAPRequestEnvelopeV1, error) {
	var request APAPRequestEnvelopeV1
	if err := decodeExact(raw, controlRequestFields, &request); err != nil {
		return request, protocolError(CodeIdentityMismatch, "request-rejected")
	}
	if request.SchemaVersion != RequestSchema || request.ProtocolFamily != ControlFamily || request.ProtocolVersion != ProtocolVersion || request.Audience != ControlAudience {
		return request, protocolError(CodeIdentityMismatch, "framing-mismatch")
	}
	if !validID(request.RequestID) || !validID(request.CommandID) || !validID(request.ProviderInstanceID) || !validDigest(peer.PrincipalDigest) || request.CallerPrincipalDigest != peer.PrincipalDigest {
		return request, protocolError(CodeIdentityMismatch, "identity-mismatch")
	}
	if !request.AuthorityProfile.valid() {
		return request, protocolError(CodeProfileUnsupported, "profile-unsupported")
	}
	if !request.Operation.validControl() {
		return request, protocolError(CodeIdentityMismatch, "operation-unsupported")
	}
	if _, ok := operationPrincipals[request.Operation][peer.Role]; !ok {
		return request, protocolError(CodePrincipalUnauthorized, "principal-unauthorized")
	}
	if peer.Role == PrincipalWorker {
		return request, protocolError(CodePrincipalUnauthorized, "principal-unauthorized")
	}
	if err := validateTimeNonce(request.IssuedAt, request.ExpiresAt, request.Nonce, now); err != nil {
		return request, err
	}
	if request.Operation.readOnly() != (request.ExpectedProviderSequence == nil) {
		return request, protocolError(CodeIdentityMismatch, "sequence-shape-invalid")
	}
	digest, err := controlRequestDigest(request)
	if err != nil || digest != request.RequestEnvelopeDigest {
		return request, protocolError(CodeIdentityMismatch, "request-digest-invalid")
	}
	if err := validateControlPayload(request, peer, fds); err != nil {
		return request, err
	}
	if err := ValidateControlFDRoles(request.Operation, fds); err != nil {
		return request, err
	}
	return request, nil
}

func DecodeCredentialIngressRequest(raw []byte, peer PeerIdentity, now time.Time, fds []FDRef) (CredentialIngressRequestV1, error) {
	var request CredentialIngressRequestV1
	if err := decodeExact(raw, ingressRequestFields, &request); err != nil {
		return request, protocolError(CodeIdentityMismatch, "request-rejected")
	}
	if peer.Role != PrincipalSecretProvider || !validDigest(peer.PrincipalDigest) || request.SecretProviderPrincipalDigest != peer.PrincipalDigest {
		return request, protocolError(CodePrincipalUnauthorized, "principal-unauthorized")
	}
	if request.SchemaVersion != IngressRequestSchema || request.ProtocolFamily != IngressFamily || request.ProtocolVersion != ProtocolVersion || request.Audience != IngressAudience || !request.AuthorityProfile.valid() {
		return request, protocolError(CodeIdentityMismatch, "framing-mismatch")
	}
	if !validID(request.RequestID) || !validID(request.CommandID) || !validID(request.ProviderInstanceID) || !validID(request.ProbeSessionID) || !validDigest(request.TargetIsolationIdentityDigest) || !validDigest(request.CredentialIngressEndpointIdentityDigest) || !validDigest(request.CredentialIngressTicketDigest) {
		return request, protocolError(CodeIdentityMismatch, "identity-mismatch")
	}
	if err := validateTimeNonce(request.IssuedAt, request.ExpiresAt, request.Nonce, now); err != nil {
		return request, err
	}
	digest, err := ingressRequestDigest(request)
	if err != nil || digest != request.RequestDigest {
		return request, protocolError(CodeIdentityMismatch, "request-digest-invalid")
	}
	var payload AttachProbeCredentialPayload
	if err := decodeClosed(request.Payload, &payload); err != nil || payload.ProbeSessionID != request.ProbeSessionID || payload.TargetIsolationIdentityDigest != request.TargetIsolationIdentityDigest || !validDigest(payload.CapabilityIdentityDigest) || !validDigest(payload.CapabilityPolicyDigest) || !validDigest(payload.ServiceIdentityDigest) || !noncePattern.MatchString(payload.DeliveryNonce) || payload.CapabilityExpiresAt.IsZero() || !payload.CapabilityExpiresAt.Equal(request.ExpiresAt) {
		return request, protocolError(CodeSecretBoundaryViolation, "credential-payload-invalid")
	}
	if len(fds) != 1 || fds[0].Role != FDCredentialCapability || fds[0].Index != 0 {
		return request, protocolError(CodeSecretBoundaryViolation, "credential-fd-invalid")
	}
	return request, nil
}

func ValidateExpectedSequence(request APAPRequestEnvelopeV1, current uint64) error {
	if request.Operation.readOnly() {
		if request.ExpectedProviderSequence != nil {
			return protocolError(CodeIdentityMismatch, "sequence-shape-invalid")
		}
		return nil
	}
	if request.ExpectedProviderSequence == nil || *request.ExpectedProviderSequence != current {
		return protocolError(CodeIdentityMismatch, "sequence-conflict")
	}
	return nil
}

func ValidateControlFDRoles(operation Operation, refs []FDRef) error {
	if len(refs) > 32 {
		return protocolError(CodeIdentityMismatch, "fd-table-invalid")
	}
	for _, ref := range refs {
		if ref.Role == FDCredentialCapability || ref.Role == FDCredentialRoot {
			return protocolError(CodeSecretBoundaryViolation, "credential-fd-on-control")
		}
	}
	switch operation {
	case OperationBeginProbe:
		if len(refs) < 3 || len(refs) > 18 || refs[0] != (FDRef{Role: FDCandidateExecutable}) || refs[1] != (FDRef{Role: FDScratchRoot}) {
			return protocolError(CodeIdentityMismatch, "fd-table-invalid")
		}
		for i, ref := range refs[2:] {
			if ref.Role != FDBusinessDenyRoot || ref.Index != i {
				return protocolError(CodeIdentityMismatch, "fd-table-invalid")
			}
		}
	case OperationStageBundleLeafBatch:
		if len(refs) < 1 || len(refs) > 24 {
			return protocolError(CodeIdentityMismatch, "fd-table-invalid")
		}
		for i, ref := range refs {
			if ref.Role != FDBundleLeaf || ref.Index != i {
				return protocolError(CodeIdentityMismatch, "fd-table-invalid")
			}
		}
	case OperationPrepareLaunch:
		want := []FDRole{FDCandidateExecutable, FDAuthorityRoot, FDFenceRoot, FDWorktree, FDControlRoot, FDControlInput, FDControlOutput, FDMountNamespace}
		if len(refs) != len(want) {
			return protocolError(CodeIdentityMismatch, "fd-table-invalid")
		}
		for i := range want {
			if refs[i].Role != want[i] || refs[i].Index != 0 {
				return protocolError(CodeIdentityMismatch, "fd-table-invalid")
			}
		}
	default:
		if len(refs) != 0 {
			return protocolError(CodeIdentityMismatch, "fd-table-invalid")
		}
	}
	return nil
}

func validateControlPayload(request APAPRequestEnvelopeV1, peer PeerIdentity, fds []FDRef) error {
	switch request.Operation {
	case OperationDescribe:
		var payload struct{}
		if err := decodeClosed(request.Payload, &payload); err != nil {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationBeginProbe:
		var payload BeginProbePayload
		if err := decodeClosed(request.Payload, &payload); err != nil || !validDigest(payload.CandidateIdentityDigest) || !validDigest(payload.SuiteDigest) || !validDigest(payload.ProbeArtifactDigest) || !validDigest(payload.PolicyDigest) || !validDigest(payload.ChallengeDigest) || !payload.Deadline.Equal(request.ExpiresAt) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationStageBundleLeafBatch:
		var payload StageBundleLeafBatchPayload
		if err := decodeClosed(request.Payload, &payload); err != nil || !validID(payload.BundleTransactionID) || payload.OrderedLeafDescriptors == nil || len(payload.OrderedLeafDescriptors) == 0 || len(payload.OrderedLeafDescriptors) > 24 || len(payload.OrderedLeafDescriptors) != len(fds) || !updateKindAuthorized(payload.UpdateKind, peer.Role) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
		previous := ""
		for _, leaf := range payload.OrderedLeafDescriptors {
			key := leaf.LeafKind + "\x00" + leaf.Digest
			if !validID(leaf.LeafKind) || !validDigest(leaf.Digest) || leaf.Size < 1 || leaf.Size > 1<<20 || leaf.MediaType != "application/json" || (previous != "" && key <= previous) {
				return protocolError(CodeIdentityMismatch, "payload-invalid")
			}
			previous = key
		}
	case OperationPrepareLaunch:
		var payload PrepareLaunchPayload
		if decodeClosed(request.Payload, &payload) != nil || !validPrepareLaunchPayload(payload, request) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationCommitLaunch:
		var payload CommitLaunchPayload
		if decodeClosed(request.Payload, &payload) != nil || !validID(payload.LaunchTransactionID) || !validDigest(payload.LaunchReceiptDigest) || !validDigest(payload.ReleaseIdentity) || !validDigest(payload.DurableAcceptDigest) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationAbortLaunch:
		var payload AbortLaunchPayload
		if decodeClosed(request.Payload, &payload) != nil || !validID(payload.LaunchTransactionID) || !validID(payload.ReasonCode) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationInspectLaunch:
		var payload InspectLaunchPayload
		if decodeClosed(request.Payload, &payload) != nil || !validID(payload.AttemptID) || !validNonce(payload.LaunchNonce) || !validDigest(payload.APAPLaunchRequestDigest) || !validDigest(payload.ProfileRequestDigest) {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	default:
		return protocolError(CodeIdentityMismatch, "operation-unsupported")
	}
	return nil
}

func validPrepareLaunchPayload(payload PrepareLaunchPayload, request APAPRequestEnvelopeV1) bool {
	if !validID(payload.TaskID) || !validID(payload.RunID) || !validID(payload.AttemptID) || !validID(payload.AuthorityNamespaceID) || !validNonce(payload.LaunchNonce) || !payload.Deadline.Equal(request.ExpiresAt) {
		return false
	}
	for _, digest := range []string{
		payload.APAPLaunchRequestDigest, payload.ProfileRequestDigest, payload.BundleDigest,
		payload.EvidenceDigest, payload.ConfigDigest, payload.FenceDigest,
		payload.CandidateExecutableIdentityDigest, payload.AuthorityRootIdentityDigest,
		payload.FenceRootIdentityDigest, payload.WorktreeIdentityDigest,
		payload.ControlRootIdentityDigest, payload.ControlInputIdentityDigest,
		payload.ControlOutputIdentityDigest, payload.MountNamespaceIdentityDigest,
		payload.ArgvDigest, payload.EnvironmentDigest,
	} {
		if !validDigest(digest) {
			return false
		}
	}
	return true
}

func validNonce(value string) bool { return noncePattern.MatchString(value) }

func updateKindAuthorized(kind UpdateKind, role Principal) bool {
	switch role {
	case PrincipalEvidenceConfig:
		return kind == UpdateEvidence
	case PrincipalRotation:
		return kind == UpdateRotation
	case PrincipalRevocation:
		return kind == UpdateRevocation
	default:
		return false
	}
}

func validID(value string) bool     { return idPattern.MatchString(value) }
func validDigest(value string) bool { return digestPattern.MatchString(value) }
func stableDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + fmtHex(h.Sum(nil))
}

func fmtHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[i*2], out[i*2+1] = alphabet[b>>4], alphabet[b&15]
	}
	return string(out)
}

func validateExactObjectFields(raw []byte, fields []string) error {
	admitted, err := canonical.JSON(raw)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(admitted))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return errors.New("rejected")
	}
	if err := requireEOF(decoder); err != nil || len(object) != len(fields) {
		return errors.New("rejected")
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return errors.New("rejected")
		}
	}
	return nil
}

func validateTimeNonce(issued, expires time.Time, nonce string, now time.Time) error {
	if now.IsZero() || issued.IsZero() || expires.IsZero() || issued.After(now) || !expires.After(now) || !expires.After(issued) || !noncePattern.MatchString(nonce) {
		return protocolError(CodeIdentityMismatch, "freshness-invalid")
	}
	return nil
}

func decodeClosed(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return errors.New("rejected")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("rejected")
	}
	admitted, err := canonical.JSON(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(admitted))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	return nil
}

func decodeExact(raw []byte, fields []string, target any) error {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return errors.New("rejected")
	}
	if err := validateExactObjectFields(raw, fields); err != nil {
		return err
	}
	return decodeClosed(raw, target)
}

var controlRequestFields = []string{"schemaVersion", "protocolFamily", "protocolVersion", "audience", "requestId", "commandId", "callerPrincipalDigest", "providerInstanceId", "authorityProfile", "operation", "issuedAt", "expiresAt", "nonce", "expectedProviderSequence", "payload", "requestEnvelopeDigest"}
var controlResponseFields = []string{"schemaVersion", "protocolFamily", "protocolVersion", "audience", "requestId", "commandId", "providerInstanceId", "authorityProfile", "operation", "observedProviderSequence", "safeCode", "safeMessage", "payload", "responseEnvelopeDigest"}
var ingressRequestFields = []string{"schemaVersion", "protocolFamily", "protocolVersion", "audience", "requestId", "commandId", "secretProviderPrincipalDigest", "providerInstanceId", "authorityProfile", "probeSessionId", "targetIsolationIdentityDigest", "credentialIngressEndpointIdentityDigest", "credentialIngressTicketDigest", "issuedAt", "expiresAt", "nonce", "payload", "requestDigest"}
var ingressResponseFields = []string{"schemaVersion", "protocolFamily", "protocolVersion", "audience", "requestId", "commandId", "providerInstanceId", "authorityProfile", "safeCode", "safeMessage", "payload", "responseDigest"}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing")
	}
	return err
}

func controlRequestDigest(r APAPRequestEnvelopeV1) (string, error) {
	r.RequestEnvelopeDigest = ""
	return digestWithoutEmptyField(r, "requestEnvelopeDigest")
}
func ingressRequestDigest(r CredentialIngressRequestV1) (string, error) {
	r.RequestDigest = ""
	return digestWithoutEmptyField(r, "requestDigest")
}

func digestWithoutEmptyField(value any, field string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", err
	}
	delete(object, field)
	raw, err = json.Marshal(object)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func SealControlRequest(request APAPRequestEnvelopeV1) ([]byte, error) {
	digest, err := controlRequestDigest(request)
	if err != nil {
		return nil, err
	}
	request.RequestEnvelopeDigest = digest
	return marshalCanonical(request)
}
func SealCredentialIngressRequest(request CredentialIngressRequestV1) ([]byte, error) {
	digest, err := ingressRequestDigest(request)
	if err != nil {
		return nil, err
	}
	request.RequestDigest = digest
	return marshalCanonical(request)
}

func controlResponseDigest(r APAPResponseEnvelopeV1) (string, error) {
	r.ResponseEnvelopeDigest = ""
	return digestWithoutEmptyField(r, "responseEnvelopeDigest")
}
func ingressResponseDigest(r CredentialIngressResponseV1) (string, error) {
	r.ResponseDigest = ""
	return digestWithoutEmptyField(r, "responseDigest")
}
func SealControlResponse(response APAPResponseEnvelopeV1) ([]byte, error) {
	digest, err := controlResponseDigest(response)
	if err != nil {
		return nil, err
	}
	response.ResponseEnvelopeDigest = digest
	return marshalCanonical(response)
}
func SealCredentialIngressResponse(response CredentialIngressResponseV1) ([]byte, error) {
	digest, err := ingressResponseDigest(response)
	if err != nil {
		return nil, err
	}
	response.ResponseDigest = digest
	return marshalCanonical(response)
}

func DecodeControlResponse(raw []byte, request APAPRequestEnvelopeV1, expectedObservedSequence uint64) (APAPResponseEnvelopeV1, error) {
	var response APAPResponseEnvelopeV1
	if err := decodeExact(raw, controlResponseFields, &response); err != nil {
		return response, protocolError(CodeIdentityMismatch, "response-rejected")
	}
	if hasNullMember(raw, "observedProviderSequence", "safeMessage") {
		return response, protocolError(CodeIdentityMismatch, "response-null-scalar")
	}
	if response.SchemaVersion != ResponseSchema || response.ProtocolFamily != ControlFamily || response.ProtocolVersion != ProtocolVersion || response.Audience != ControlAudience || response.RequestID != request.RequestID || response.CommandID != request.CommandID || response.ProviderInstanceID != request.ProviderInstanceID || response.AuthorityProfile != request.AuthorityProfile || response.Operation != request.Operation || response.ObservedProviderSequence != expectedObservedSequence {
		return response, protocolError(CodeIdentityMismatch, "response-identity-invalid")
	}
	if request.ExpectedProviderSequence != nil && response.ObservedProviderSequence != *request.ExpectedProviderSequence {
		return response, protocolError(CodeIdentityMismatch, "response-sequence-invalid")
	}
	if err := response.SafeCode.Validate(); err != nil {
		return response, protocolError(CodeInternalFailClosed, "response-code-invalid")
	}
	if response.SafeMessage != SafeMessageFor(response.SafeCode) || !validateControlResponsePayload(response, request) {
		return response, protocolError(CodeInternalFailClosed, "response-message-invalid")
	}
	digest, err := controlResponseDigest(response)
	if err != nil || digest != response.ResponseEnvelopeDigest {
		return response, protocolError(CodeIdentityMismatch, "response-digest-invalid")
	}
	return response, nil
}

func DecodeCredentialIngressResponse(raw []byte, request CredentialIngressRequestV1) (CredentialIngressResponseV1, error) {
	var response CredentialIngressResponseV1
	if err := decodeExact(raw, ingressResponseFields, &response); err != nil {
		return response, protocolError(CodeIdentityMismatch, "response-rejected")
	}
	if hasNullMember(raw, "safeMessage") {
		return response, protocolError(CodeIdentityMismatch, "response-null-scalar")
	}
	if response.SchemaVersion != IngressResponseSchema || response.ProtocolFamily != IngressFamily || response.ProtocolVersion != ProtocolVersion || response.Audience != IngressAudience || response.RequestID != request.RequestID || response.CommandID != request.CommandID || response.ProviderInstanceID != request.ProviderInstanceID || response.AuthorityProfile != request.AuthorityProfile {
		return response, protocolError(CodeIdentityMismatch, "response-identity-invalid")
	}
	if err := response.SafeCode.Validate(); err != nil {
		return response, protocolError(CodeInternalFailClosed, "response-code-invalid")
	}
	if response.SafeMessage != SafeMessageFor(response.SafeCode) || !validateIngressResponsePayload(response) {
		return response, protocolError(CodeInternalFailClosed, "response-message-invalid")
	}
	digest, err := ingressResponseDigest(response)
	if err != nil || digest != response.ResponseDigest {
		return response, protocolError(CodeIdentityMismatch, "response-digest-invalid")
	}
	return response, nil
}

func hasNullMember(raw []byte, fields ...string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return true
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(object[field]), []byte("null")) {
			return true
		}
	}
	return false
}

func validateControlResponsePayload(response APAPResponseEnvelopeV1, request APAPRequestEnvelopeV1) bool {
	if response.SafeCode != CodeOK {
		return bytes.Equal(bytes.TrimSpace(response.Payload), []byte("null"))
	}
	if bytes.Equal(bytes.TrimSpace(response.Payload), []byte("null")) {
		return false
	}
	switch request.Operation {
	case OperationDescribe:
		var payload DescribeSuccessPayload
		if decodeClosed(response.Payload, &payload) != nil || !validDigest(payload.ProviderBuildDigest) || (payload.Platform != "linux" && payload.Platform != "darwin") || len(payload.Profiles) == 0 || len(payload.Profiles) > 2 {
			return false
		}
		previous := AuthorityProfile("")
		for _, profile := range payload.Profiles {
			if !profile.valid() || (previous != "" && profile <= previous) {
				return false
			}
			previous = profile
		}
		return true
	case OperationBeginProbe:
		var payload BeginProbeSuccessPayload
		return decodeClosed(response.Payload, &payload) == nil && validID(payload.ProbeSessionID) && validDigest(payload.TargetIsolationIdentityDigest) && validDigest(payload.CredentialIngressEndpointIdentityDigest) && payload.ExpiresAt.Equal(request.ExpiresAt)
	case OperationStageBundleLeafBatch:
		var requestPayload StageBundleLeafBatchPayload
		var payload StageBundleLeafBatchSuccessPayload
		if decodeClosed(request.Payload, &requestPayload) != nil || decodeClosed(response.Payload, &payload) != nil || payload.BundleTransactionID != requestPayload.BundleTransactionID || payload.StagedLeafDigests == nil || len(payload.StagedLeafDigests) != len(requestPayload.OrderedLeafDescriptors) || !validDigest(payload.StagingReceiptDigest) {
			return false
		}
		for i, digest := range payload.StagedLeafDigests {
			if !validDigest(digest) || digest != requestPayload.OrderedLeafDescriptors[i].Digest {
				return false
			}
		}
		return true
	case OperationPrepareLaunch:
		var payload PrepareLaunchSuccessPayload
		return decodeClosed(response.Payload, &payload) == nil && validID(payload.LaunchTransactionID) && validDigest(payload.APAPLaunchRequestDigest) && validDigest(payload.ProfileRequestDigest) && validDigest(payload.LaunchReceiptDigest) && validReceiptObject(payload.LaunchReceipt) && validDigest(payload.ReleaseIdentity) && payload.Deadline.Equal(request.ExpiresAt)
	case OperationCommitLaunch:
		var payload CommitLaunchSuccessPayload
		return decodeClosed(response.Payload, &payload) == nil && payload.Status == "released" && validDigest(payload.ReleaseReceiptDigest) && validReceiptObject(payload.ReleaseReceipt)
	case OperationAbortLaunch:
		var payload AbortLaunchSuccessPayload
		return decodeClosed(response.Payload, &payload) == nil && (payload.Status == "aborted" || payload.Status == "exited") && validDigest(payload.AbortReceiptDigest) && validReceiptObject(payload.AbortReceipt)
	case OperationInspectLaunch:
		var payload InspectLaunchSuccessPayload
		if decodeClosed(response.Payload, &payload) != nil || payload.Status != "pending" && payload.Status != "released" && payload.Status != "aborted" && payload.Status != "exited" && payload.Status != "unknown" {
			return false
		}
		if payload.Status == "unknown" {
			return payload.LaunchReceiptDigest == "" && payload.ReleaseReceiptDigest == "" && payload.AbortReceiptDigest == ""
		}
		if !validID(payload.LaunchTransactionID) || !validDigest(payload.ChildIdentityDigest) || !validDigest(payload.LaunchReceiptDigest) {
			return false
		}
		if payload.Status == "released" {
			return validDigest(payload.ReleaseReceiptDigest) && payload.AbortReceiptDigest == ""
		}
		if payload.Status == "aborted" {
			return validDigest(payload.AbortReceiptDigest) && payload.ReleaseReceiptDigest == ""
		}
		return payload.ReleaseReceiptDigest == "" && payload.AbortReceiptDigest == ""
	default:
		return false
	}
}

func validReceiptObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return decodeClosed(raw, &object) == nil && len(object) > 0
}

func validateIngressResponsePayload(response CredentialIngressResponseV1) bool {
	if response.SafeCode != CodeOK {
		return bytes.Equal(bytes.TrimSpace(response.Payload), []byte("null"))
	}
	if bytes.Equal(bytes.TrimSpace(response.Payload), []byte("null")) {
		return false
	}
	var payload CredentialIngressSuccessPayload
	return decodeClosed(response.Payload, &payload) == nil && validDigest(payload.DeliveryReceiptDigest) && validDigest(payload.InstallReceiptDigest)
}

func marshalCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(raw)
}
