// Package authorityprovider implements the deterministic, transport-neutral
// core of ADR 0038. It deliberately contains no socket, SCM_RIGHTS, OS
// principal, secret-delivery, anchor, or adapter registry implementation.
package authorityprovider

import (
	"bytes"
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

var controlOperations = map[Operation]struct{}{
	OperationDescribe: {}, OperationBeginProbe: {}, OperationRunProbeVariant: {}, OperationFinalizeProbe: {},
	OperationReadCurrentBundle: {}, OperationReadBundleLeafBatch: {}, OperationStageBundleLeafBatch: {},
	OperationPrepareEvidenceUpdate: {}, OperationPrepareRotation: {}, OperationPrepareRevocation: {},
	OperationCommitBundleUpdate: {}, OperationInspectBundleTransaction: {}, OperationRecoverBundleTransaction: {},
	OperationPrepareLaunch: {}, OperationCommitLaunch: {}, OperationAbortLaunch: {}, OperationInspectLaunch: {}, OperationWatchEpoch: {},
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
	OperationDescribe:   setOf(PrincipalConsumer, PrincipalVerifierController),
	OperationBeginProbe: setOf(PrincipalVerifierController), OperationRunProbeVariant: setOf(PrincipalVerifierController), OperationFinalizeProbe: setOf(PrincipalVerifierController),
	OperationReadCurrentBundle: setOf(PrincipalConsumer, PrincipalVerifierController), OperationReadBundleLeafBatch: setOf(PrincipalConsumer, PrincipalVerifierController),
	OperationStageBundleLeafBatch:  setOf(PrincipalEvidenceConfig, PrincipalRotation, PrincipalRevocation),
	OperationPrepareEvidenceUpdate: setOf(PrincipalEvidenceConfig), OperationPrepareRotation: setOf(PrincipalRotation), OperationPrepareRevocation: setOf(PrincipalRevocation),
	OperationCommitBundleUpdate:       setOf(PrincipalEvidenceConfig, PrincipalRotation, PrincipalRevocation),
	OperationInspectBundleTransaction: setOf(PrincipalConsumer, PrincipalEvidenceConfig, PrincipalRotation, PrincipalRevocation, PrincipalRecovery),
	OperationRecoverBundleTransaction: setOf(PrincipalRecovery),
	OperationPrepareLaunch:            setOf(PrincipalConsumer), OperationCommitLaunch: setOf(PrincipalConsumer), OperationAbortLaunch: setOf(PrincipalConsumer), OperationInspectLaunch: setOf(PrincipalConsumer), OperationWatchEpoch: setOf(PrincipalConsumer),
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

var noncePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func DecodeControlRequest(raw []byte, peer Principal, now time.Time, fds []FDRef) (APAPRequestEnvelopeV1, error) {
	var request APAPRequestEnvelopeV1
	if err := decodeExact(raw, controlRequestFields, &request); err != nil {
		return request, protocolError(CodeIdentityMismatch, "request-rejected")
	}
	if request.SchemaVersion != RequestSchema || request.ProtocolFamily != ControlFamily || request.ProtocolVersion != ProtocolVersion || request.Audience != ControlAudience {
		return request, protocolError(CodeIdentityMismatch, "framing-mismatch")
	}
	if request.RequestID == "" || request.CommandID == "" || request.ProviderInstanceID == "" || request.CallerPrincipalDigest != string(peer) {
		return request, protocolError(CodeIdentityMismatch, "identity-mismatch")
	}
	if !request.AuthorityProfile.valid() {
		return request, protocolError(CodeProfileUnsupported, "profile-unsupported")
	}
	if !request.Operation.validControl() {
		return request, protocolError(CodeIdentityMismatch, "operation-unsupported")
	}
	if _, ok := operationPrincipals[request.Operation][peer]; !ok {
		return request, protocolError(CodePrincipalUnauthorized, "principal-unauthorized")
	}
	if peer == PrincipalWorker {
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
	if err := validateControlPayload(request.Operation, request.Payload); err != nil {
		return request, err
	}
	if err := ValidateControlFDRoles(request.Operation, fds); err != nil {
		return request, err
	}
	return request, nil
}

func DecodeCredentialIngressRequest(raw []byte, peer Principal, now time.Time, fds []FDRef) (CredentialIngressRequestV1, error) {
	var request CredentialIngressRequestV1
	if err := decodeExact(raw, ingressRequestFields, &request); err != nil {
		return request, protocolError(CodeIdentityMismatch, "request-rejected")
	}
	if peer != PrincipalSecretProvider || request.SecretProviderPrincipalDigest != string(peer) {
		return request, protocolError(CodePrincipalUnauthorized, "principal-unauthorized")
	}
	if request.SchemaVersion != IngressRequestSchema || request.ProtocolFamily != IngressFamily || request.ProtocolVersion != ProtocolVersion || request.Audience != IngressAudience || !request.AuthorityProfile.valid() {
		return request, protocolError(CodeIdentityMismatch, "framing-mismatch")
	}
	if request.RequestID == "" || request.CommandID == "" || request.ProviderInstanceID == "" || request.ProbeSessionID == "" || request.TargetIsolationIdentityDigest == "" || request.CredentialIngressEndpointIdentityDigest == "" || request.CredentialIngressTicketDigest == "" {
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
	if err := decodeClosed(request.Payload, &payload); err != nil || payload.ProbeSessionID != request.ProbeSessionID || payload.TargetIsolationIdentityDigest != request.TargetIsolationIdentityDigest || payload.CapabilityIdentityDigest == "" || payload.CapabilityPolicyDigest == "" || payload.ServiceIdentityDigest == "" || payload.DeliveryNonce == "" || payload.CapabilityExpiresAt.IsZero() {
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

func validateControlPayload(operation Operation, raw json.RawMessage) error {
	switch operation {
	case OperationDescribe:
		var payload struct{}
		if err := decodeClosed(raw, &payload); err != nil {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	case OperationBeginProbe:
		var payload BeginProbePayload
		if err := decodeClosed(raw, &payload); err != nil || payload.CandidateIdentityDigest == "" || payload.SuiteDigest == "" || payload.ProbeArtifactDigest == "" || payload.PolicyDigest == "" || payload.ChallengeDigest == "" || payload.Deadline.IsZero() {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	default:
		allowed, ok := controlPayloadFields[operation]
		if !ok || validateExactObjectFields(raw, allowed) != nil {
			return protocolError(CodeIdentityMismatch, "payload-invalid")
		}
	}
	return nil
}

var controlPayloadFields = map[Operation][]string{
	OperationRunProbeVariant:          {"probeSessionId", "variantId", "invocationManifestDigest", "credentialHandoffReceiptRef", "previousReceiptDigest", "deadline"},
	OperationFinalizeProbe:            {"probeSessionId", "orderedReceiptDigests", "observationDigest"},
	OperationReadCurrentBundle:        {"minProviderSequence"},
	OperationReadBundleLeafBatch:      {"bundleDigest", "orderedLeafIndexes"},
	OperationStageBundleLeafBatch:     {"bundleTransactionId", "updateKind", "orderedLeafDescriptors"},
	OperationPrepareEvidenceUpdate:    {"bundleTransactionId", "manifest", "detachedSignature", "updateAuthorization", "observationCandidateDigest", "previousBundleDigest"},
	OperationPrepareRotation:          {"bundleTransactionId", "manifest", "detachedSignature", "updateAuthorization", "previousBundleDigest"},
	OperationPrepareRevocation:        {"bundleTransactionId", "manifest", "detachedSignature", "updateAuthorization", "previousBundleDigest"},
	OperationCommitBundleUpdate:       {"bundleTransactionId", "bundleDigest", "originalExpectedProviderSequence", "anchoredNextProviderSequence", "preparedReceiptDigest"},
	OperationInspectBundleTransaction: {"bundleTransactionId", "bundleDigest"},
	OperationRecoverBundleTransaction: {"bundleTransactionId", "bundleDigest", "originalExpectedProviderSequence", "observedCurrentProviderSequence", "anchoredNextProviderSequence", "preparedReceiptDigest", "anchorReceiptDigest"},
	OperationPrepareLaunch:            {"taskId", "runId", "attemptId", "authorityNamespaceId", "launchNonce", "apapLaunchRequestDigest", "profileRequestDigest", "bundleDigest", "evidenceDigest", "configDigest", "fenceDigest", "candidateExecutableIdentityDigest", "authorityRootIdentityDigest", "fenceRootIdentityDigest", "worktreeIdentityDigest", "controlRootIdentityDigest", "controlInputIdentityDigest", "controlOutputIdentityDigest", "mountNamespaceIdentityDigest", "argvDigest", "environmentDigest", "deadline"},
	OperationCommitLaunch:             {"launchTransactionId", "launchReceiptDigest", "releaseIdentity", "durableAcceptDigest"},
	OperationAbortLaunch:              {"launchTransactionId", "reasonCode"},
	OperationInspectLaunch:            {"attemptId", "launchNonce", "apapLaunchRequestDigest", "profileRequestDigest"},
	OperationWatchEpoch:               {"afterProviderSequence"},
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

func DecodeControlResponse(raw []byte, request APAPRequestEnvelopeV1) (APAPResponseEnvelopeV1, error) {
	var response APAPResponseEnvelopeV1
	if err := decodeExact(raw, controlResponseFields, &response); err != nil {
		return response, protocolError(CodeIdentityMismatch, "response-rejected")
	}
	if response.SchemaVersion != ResponseSchema || response.ProtocolFamily != ControlFamily || response.ProtocolVersion != ProtocolVersion || response.Audience != ControlAudience || response.RequestID != request.RequestID || response.CommandID != request.CommandID || response.ProviderInstanceID != request.ProviderInstanceID || response.AuthorityProfile != request.AuthorityProfile || response.Operation != request.Operation {
		return response, protocolError(CodeIdentityMismatch, "response-identity-invalid")
	}
	if err := response.SafeCode.Validate(); err != nil {
		return response, protocolError(CodeInternalFailClosed, "response-code-invalid")
	}
	if response.SafeCode == CodeOK && response.SafeMessage != "" {
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
	if response.SchemaVersion != IngressResponseSchema || response.ProtocolFamily != IngressFamily || response.ProtocolVersion != ProtocolVersion || response.Audience != IngressAudience || response.RequestID != request.RequestID || response.CommandID != request.CommandID || response.ProviderInstanceID != request.ProviderInstanceID || response.AuthorityProfile != request.AuthorityProfile {
		return response, protocolError(CodeIdentityMismatch, "response-identity-invalid")
	}
	if err := response.SafeCode.Validate(); err != nil {
		return response, protocolError(CodeInternalFailClosed, "response-code-invalid")
	}
	if response.SafeCode == CodeOK && response.SafeMessage != "" {
		return response, protocolError(CodeInternalFailClosed, "response-message-invalid")
	}
	digest, err := ingressResponseDigest(response)
	if err != nil || digest != response.ResponseDigest {
		return response, protocolError(CodeIdentityMismatch, "response-digest-invalid")
	}
	return response, nil
}

func marshalCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(raw)
}

func fixedPayload() json.RawMessage { return json.RawMessage(`{}`) }
