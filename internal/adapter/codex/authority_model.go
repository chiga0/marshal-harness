package codex

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	consumerStateSchema      = "marshal.codex.consumer-authority-state.v1"
	consumerBootstrapSchema  = "marshal.codex.consumer-bootstrap.v1"
	activeRootPinSchema      = "marshal.codex.active-root-pin.v1"
	consumerFenceSchema      = "marshal.codex.consumer-fence.v1"
	hostIdentitySchema       = "marshal.codex.linux-host-identity.v1"
	hostAttestSchema         = "marshal.codex.linux-host-attestation.v1"
	probeReceiptSchema       = "marshal.codex.probe-receipt.v1"
	productionEvidenceSchema = "marshal.codex.production-evidence.v1"
	authorityConfigSchema    = "marshal.codex.authority-config.v1"
	authorityKeysetSchema    = "marshal.codex.authority-keyset.v1"
	authorityMetadataSchema  = "marshal.codex.authority-metadata.v1"
	workerAuthoritySchema    = "marshal.codex.worker-authority-context.v1"

	maxSafeGeneration = uint64(1<<53 - 1)
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// AuthorityFailure is the package-local typed carrier used by the still
// hard-disabled production authority implementation. It deliberately exposes
// no paths, OS errors, environment, transcript, or credential material.
type AuthorityFailure struct {
	SchemaVersion string                  `json:"schemaVersion"`
	AdapterID     string                  `json:"adapterId"`
	Operation     string                  `json:"operation"`
	Code          string                  `json:"code"`
	RetryClass    string                  `json:"retryClass"`
	SafeMessage   string                  `json:"safeMessage"`
	ObservedAt    string                  `json:"observedAt"`
	Details       AuthorityFailureDetails `json:"details"`
	cause         error
}

type AuthorityFailureDetails struct {
	AuthorityGeneration uint64 `json:"authorityGeneration,omitempty"`
	TrustRootGeneration uint64 `json:"trustRootGeneration,omitempty"`
	EvidenceDigest      string `json:"evidenceDigest,omitempty"`
	ConfigDigest        string `json:"configDigest,omitempty"`
	Phase               string `json:"phase,omitempty"`
	Platform            string `json:"platform,omitempty"`
}

func (failure *AuthorityFailure) Error() string { return failure.Code + ": " + failure.SafeMessage }
func (failure *AuthorityFailure) Unwrap() error { return failure.cause }

// Permanent implements the Core error classification contract. Reconcile and
// transient failures must remain visible to their dedicated recovery paths.
func (failure *AuthorityFailure) Permanent() bool {
	return failure != nil && failure.RetryClass == "permanent"
}

var authorityCodes = map[string]string{
	"codex_conformance_pending": "permanent", "codex_not_registered": "permanent",
	"codex_platform_unsupported": "permanent", "codex_host_attestation_unsupported": "permanent", "codex_mount_identity_unsupported": "permanent", "codex_fd_exec_unavailable": "permanent", "codex_executable_unsafe": "permanent", "codex_launch_identity_mismatch": "permanent", "codex_launch_receipt_invalid": "permanent", "codex_launch_nonce_replay": "permanent", "codex_launch_outcome_ambiguous": "reconcile-required",
	"codex_authority_config_missing": "permanent", "codex_authority_config_invalid": "permanent", "codex_authority_signature_invalid": "permanent", "codex_trust_root_invalid": "permanent", "codex_key_revoked": "permanent",
	"codex_authority_rollback": "permanent", "codex_authority_identity_conflict": "permanent", "codex_authority_temporarily_unavailable": "transient", "codex_fence_invalid": "permanent", "codex_fence_lock_busy": "transient", "codex_fence_commit_failed": "permanent",
	"codex_evidence_missing": "permanent", "codex_evidence_invalid": "permanent", "codex_evidence_expired": "permanent", "codex_evidence_not_yet_valid": "permanent", "codex_evidence_revoked": "permanent", "codex_evidence_host_mismatch": "permanent", "codex_evidence_binary_mismatch": "permanent", "codex_evidence_contract_mismatch": "permanent",
	"codex_path_invalid": "permanent", "codex_path_unsafe_type": "permanent", "codex_path_identity_changed": "permanent", "codex_path_topology_conflict": "permanent", "codex_path_permission_invalid": "permanent",
	"codex_protocol_invalid": "permanent", "codex_permission_denied": "permanent", "codex_output_limit_exceeded": "permanent", "codex_timeout": "permanent", "codex_canceled": "permanent", "codex_process_failed": "permanent", "codex_internal_fail_closed": "permanent",
}

func newAuthorityFailure(operation, code, safe string, details AuthorityFailureDetails, cause error, now time.Time) *AuthorityFailure {
	retry, known := authorityCodes[code]
	if !known {
		code, retry = "codex_internal_fail_closed", "permanent"
	}
	if len(safe) == 0 || len([]byte(safe)) > 512 || !utf8.ValidString(safe) {
		safe = "Codex authority rejected the operation"
	}
	return &AuthorityFailure{
		SchemaVersion: "marshal.adapter-failure.v1", AdapterID: adapterID,
		Operation: operation, Code: code, RetryClass: retry,
		SafeMessage: safe, ObservedAt: formatAuthorityTime(now), Details: details, cause: cause,
	}
}

func (failure AuthorityFailure) Validate() error {
	expectedRetry, ok := authorityCodes[failure.Code]
	validOperation := failure.Operation == "constructor" || failure.Operation == "probe" || failure.Operation == "launch" || failure.Operation == "run" || failure.Operation == "doctor"
	if failure.SchemaVersion != "marshal.adapter-failure.v1" || failure.AdapterID != adapterID || !validOperation || !ok || failure.RetryClass != expectedRetry || len(failure.SafeMessage) == 0 || len([]byte(failure.SafeMessage)) > 512 || !utf8.ValidString(failure.SafeMessage) {
		return errors.New("codex adapter failure carrier is invalid")
	}
	if _, err := parseAuthorityTime(failure.ObservedAt); err != nil {
		return err
	}
	for _, digest := range []string{failure.Details.EvidenceDigest, failure.Details.ConfigDigest} {
		if digest != "" && !validDigest(digest) {
			return errors.New("codex adapter failure detail digest is invalid")
		}
	}
	for _, generation := range []uint64{failure.Details.AuthorityGeneration, failure.Details.TrustRootGeneration} {
		if generation != 0 && !validGeneration(generation) {
			return errors.New("codex adapter failure detail generation is invalid")
		}
	}
	if failure.Details.Phase != "" && !validID(failure.Details.Phase) {
		return errors.New("codex adapter failure detail phase is invalid")
	}
	if failure.Details.Platform != "" && failure.Details.Platform != "linux" && failure.Details.Platform != "darwin" {
		return errors.New("codex adapter failure detail platform is invalid")
	}
	return nil
}

type SignatureV1 struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

type SignedEnvelopeV1 struct {
	Payload       json.RawMessage `json:"payload"`
	PayloadDigest string          `json:"payloadDigest"`
	Signatures    []SignatureV1   `json:"signatures"`
}

type AuthorityPublicKeyV1 struct {
	KeyID     string `json:"keyId"`
	Usage     string `json:"usage"`
	Algorithm string `json:"alg"`
	PublicKey string `json:"publicKey"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
}

type RootRotationV1 struct {
	NewRootKeyID     string `json:"newRootKeyId"`
	Algorithm        string `json:"alg"`
	NewRootPublicKey string `json:"newRootPublicKey"`
	NotBefore        string `json:"notBefore"`
}

type CodexAuthorityKeysetV1 struct {
	SchemaVersion        string                 `json:"schemaVersion"`
	AuthorityNamespace   string                 `json:"authorityNamespace"`
	TrustRootGeneration  uint64                 `json:"trustRootGeneration"`
	PreviousKeysetDigest *string                `json:"previousKeysetDigest"`
	ValidFrom            string                 `json:"validFrom"`
	Keys                 []AuthorityPublicKeyV1 `json:"keys"`
	RevokedKeyIDs        []string               `json:"revokedKeyIds"`
	RootRotation         *RootRotationV1        `json:"rootRotation"`
}

type CodexConsumerBootstrapV1 struct {
	SchemaVersion                 string `json:"schemaVersion"`
	AuthorityNamespace            string `json:"authorityNamespace"`
	AdapterID                     string `json:"adapterId"`
	BootstrapID                   string `json:"bootstrapId"`
	HostIdentityDigest            string `json:"hostIdentityDigest"`
	MachineIDDigest               string `json:"machineIdDigest"`
	TPMHostKeyPublic              string `json:"tpmHostKeyPublic"`
	TPMHostKeyPublicDigest        string `json:"tpmHostKeyPublicDigest"`
	TPMHostKeyQualifiedNameDigest string `json:"tpmHostKeyQualifiedNameDigest"`
	CreatedAt                     string `json:"createdAt"`
}

func (bootstrap CodexConsumerBootstrapV1) Validate() error {
	if bootstrap.SchemaVersion != consumerBootstrapSchema || bootstrap.AdapterID != adapterID || !validID(bootstrap.AuthorityNamespace) || !validDigest(bootstrap.HostIdentityDigest) || !validDigest(bootstrap.MachineIDDigest) || !validDigest(bootstrap.TPMHostKeyPublicDigest) || !validDigest(bootstrap.TPMHostKeyQualifiedNameDigest) {
		return errors.New("codex consumer bootstrap is invalid")
	}
	if _, err := decodeNonce(bootstrap.BootstrapID); err != nil {
		return err
	}
	publicArea, err := base64.StdEncoding.DecodeString(bootstrap.TPMHostKeyPublic)
	if err != nil || len(publicArea) == 0 || len(publicArea) > 1024 || base64.StdEncoding.EncodeToString(publicArea) != bootstrap.TPMHostKeyPublic || canonicalDigestBytes(publicArea) != bootstrap.TPMHostKeyPublicDigest {
		return errors.New("codex consumer bootstrap TPM public area is invalid")
	}
	_, err = parseAuthorityTime(bootstrap.CreatedAt)
	return err
}

func BootstrapDigest(bootstrap CodexConsumerBootstrapV1) (string, error) {
	if err := bootstrap.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(bootstrap)
}

func ValidateBootstrapHostIdentity(bootstrap CodexConsumerBootstrapV1, identity LinuxHostIdentityV1) error {
	bootstrapDigest, err := BootstrapDigest(bootstrap)
	if err != nil || bootstrapDigest == "" {
		return errors.New("codex consumer bootstrap is invalid")
	}
	hostDigest, err := HostIdentityDigest(identity)
	if err != nil || bootstrap.BootstrapID != identity.BootstrapID || bootstrap.HostIdentityDigest != hostDigest || bootstrap.MachineIDDigest != identity.MachineIDDigest || bootstrap.TPMHostKeyPublic != identity.TPMHostKeyPublic || bootstrap.TPMHostKeyPublicDigest != identity.TPMHostKeyPublicDigest || bootstrap.TPMHostKeyQualifiedNameDigest != identity.TPMHostKeyQualifiedNameDigest {
		return errors.New("codex consumer bootstrap and host identity differ")
	}
	return nil
}

type CodexActiveRootPinV1 struct {
	SchemaVersion       string `json:"schemaVersion"`
	AuthorityNamespace  string `json:"authorityNamespace"`
	BootstrapDigest     string `json:"bootstrapDigest"`
	RootKeyID           string `json:"rootKeyId"`
	RootAlgorithm       string `json:"rootAlg"`
	RootPublicKey       string `json:"rootPublicKey"`
	RootPublicKeyDigest string `json:"rootPublicKeyDigest"`
	TrustRootGeneration uint64 `json:"trustRootGeneration"`
	KeysetDigest        string `json:"keysetDigest"`
	ActivatedAt         string `json:"activatedAt"`
}

type CodexConsumerFenceV1 struct {
	SchemaVersion         string `json:"schemaVersion"`
	AuthorityNamespace    string `json:"authorityNamespace"`
	AdapterID             string `json:"adapterId"`
	BootstrapDigest       string `json:"bootstrapDigest"`
	HostIdentityDigest    string `json:"hostIdentityDigest"`
	BootstrapID           string `json:"bootstrapId"`
	TrustRootGeneration   uint64 `json:"trustRootGeneration"`
	AuthorityGeneration   uint64 `json:"authorityGeneration"`
	KeysetDigest          string `json:"keysetDigest"`
	ConfigDigest          string `json:"configDigest"`
	RevocationSetDigest   string `json:"revocationSetDigest"`
	CurrentEvidenceDigest string `json:"currentEvidenceDigest"`
	CommittedAt           string `json:"committedAt"`
}

type CodexConsumerAuthorityStateV1 struct {
	SchemaVersion string               `json:"schemaVersion"`
	TransactionID string               `json:"transactionId"`
	ActiveRootPin CodexActiveRootPinV1 `json:"activeRootPin"`
	Fence         CodexConsumerFenceV1 `json:"fence"`
	CommittedAt   string               `json:"committedAt"`
}

type LinuxHostIdentityV1 struct {
	SchemaVersion                 string `json:"schemaVersion"`
	OS                            string `json:"os"`
	Arch                          string `json:"arch"`
	MachineIDDigest               string `json:"machineIdDigest"`
	TPMEKCertificateDigest        string `json:"tpmEkCertificateDigest"`
	TPMHostKeyPublic              string `json:"tpmHostKeyPublic"`
	TPMHostKeyPublicDigest        string `json:"tpmHostKeyPublicDigest"`
	TPMHostKeyQualifiedNameDigest string `json:"tpmHostKeyQualifiedNameDigest"`
	BootstrapID                   string `json:"bootstrapId"`
}

type LinuxHostAttestationV1 struct {
	SchemaVersion      string              `json:"schemaVersion"`
	HostIdentity       LinuxHostIdentityV1 `json:"hostIdentity"`
	ChallengeNonce     string              `json:"challengeNonce"`
	ChallengeAlgorithm string              `json:"challengeAlg"`
	ChallengeSignature string              `json:"challengeSignature"`
}

type ExecutableIdentityV1 struct {
	CanonicalRealpath   string `json:"canonicalRealpath"`
	DeviceMajor         uint64 `json:"deviceMajor"`
	DeviceMinor         uint64 `json:"deviceMinor"`
	Inode               uint64 `json:"inode"`
	MountIDUnique       uint64 `json:"mountIdUnique"`
	Size                uint64 `json:"size"`
	Mode                uint32 `json:"mode"`
	SHA256              string `json:"sha256"`
	Version             string `json:"version"`
	VersionOutputDigest string `json:"versionOutputDigest"`
}

type CodexContractBindingV1 struct {
	AdapterContractDigest    string   `json:"adapterContractDigest"`
	LauncherBuildDigest      string   `json:"launcherBuildDigest"`
	ProfileDigest            string   `json:"profileDigest"`
	ArgvMatrixDigest         string   `json:"argvMatrixDigest"`
	EnvironmentDigest        string   `json:"environmentDigest"`
	EventContractDigest      string   `json:"eventContractDigest"`
	PermissionContractDigest string   `json:"permissionContractDigest"`
	ToolPolicyDigest         string   `json:"toolPolicyDigest"`
	ResultContractDigest     string   `json:"resultContractDigest"`
	OutputLimitDigest        string   `json:"outputLimitDigest"`
	NativeBudgetsDigest      string   `json:"nativeBudgetsDigest"`
	ExecutionProfiles        []string `json:"executionProfiles"`
}

type CodexProbeObservationV1 struct {
	SchemaVersion            string                 `json:"schemaVersion"`
	AuthorityNamespace       string                 `json:"authorityNamespace"`
	AuthorityGeneration      uint64                 `json:"authorityGeneration"`
	TrustRootGeneration      uint64                 `json:"trustRootGeneration"`
	BootstrapID              string                 `json:"bootstrapId"`
	ObservationNonce         string                 `json:"observationNonce"`
	VerifierKeyID            string                 `json:"verifierKeyId"`
	VerifierBuildDigest      string                 `json:"verifierBuildDigest"`
	ObservedAt               string                 `json:"observedAt"`
	ValidUntil               string                 `json:"validUntil"`
	HostAttestation          LinuxHostAttestationV1 `json:"hostAttestation"`
	BinaryIdentity           ExecutableIdentityV1   `json:"binaryIdentity"`
	Contract                 CodexContractBindingV1 `json:"contract"`
	SuiteDigest              string                 `json:"suiteDigest"`
	ProbeArtifactDigest      string                 `json:"probeArtifactDigest"`
	AggregateChallengeDigest string                 `json:"aggregateChallengeDigest"`
	TopologyDigest           string                 `json:"topologyDigest"`
	ReceiptDigests           []string               `json:"receiptDigests"`
	Verdicts                 AuthorityVerdictsV1    `json:"verdicts"`
}

type CodexProbeExecutionReceiptV1 struct {
	SchemaVersion            string `json:"schemaVersion"`
	AuthorityNamespace       string `json:"authorityNamespace"`
	AuthorityGeneration      uint64 `json:"authorityGeneration"`
	TrustRootGeneration      uint64 `json:"trustRootGeneration"`
	BootstrapID              string `json:"bootstrapId"`
	SuiteDigest              string `json:"suiteDigest"`
	ProbeArtifactDigest      string `json:"probeArtifactDigest"`
	VariantID                string `json:"variantId"`
	ChallengeNonce           string `json:"challengeNonce"`
	StartedAt                string `json:"startedAt"`
	EndedAt                  string `json:"endedAt"`
	HostIdentityDigest       string `json:"hostIdentityDigest"`
	BinaryIdentityDigest     string `json:"binaryIdentityDigest"`
	ArgvDigest               string `json:"argvDigest"`
	EnvironmentDigest        string `json:"environmentDigest"`
	TopologyDigest           string `json:"topologyDigest"`
	TranscriptDigest         string `json:"transcriptDigest"`
	MarkerDigest             string `json:"markerDigest"`
	ReceiptChallengeDigest   string `json:"receiptChallengeDigest"`
	EventContractDigest      string `json:"eventContractDigest"`
	PermissionContractDigest string `json:"permissionContractDigest"`
	ExitCode                 uint8  `json:"exitCode"`
	ReceiptKeyID             string `json:"receiptKeyId"`
}

type AuthorityVerdictsV1 struct {
	CredentialedInvocation bool `json:"credentialedInvocation"`
	JSONLContract          bool `json:"jsonlContract"`
	PermissionProfile      bool `json:"permissionProfile"`
	ScratchOnlyWrite       bool `json:"scratchOnlyWrite"`
	BusinessRootsDenied    bool `json:"businessRootsDenied"`
}

func (v AuthorityVerdictsV1) allTrue() bool {
	return v.CredentialedInvocation && v.JSONLContract && v.PermissionProfile && v.ScratchOnlyWrite && v.BusinessRootsDenied
}

type CodexProductionEvidenceV1 struct {
	SchemaVersion            string              `json:"schemaVersion"`
	AuthorityNamespace       string              `json:"authorityNamespace"`
	AuthorityGeneration      uint64              `json:"authorityGeneration"`
	TrustRootGeneration      uint64              `json:"trustRootGeneration"`
	BootstrapID              string              `json:"bootstrapId"`
	EvidenceKeyID            string              `json:"evidenceKeyId"`
	ObservationDigest        string              `json:"observationDigest"`
	ReceiptDigests           []string            `json:"receiptDigests"`
	IssuedAt                 string              `json:"issuedAt"`
	ValidFrom                string              `json:"validFrom"`
	ValidUntil               string              `json:"validUntil"`
	HostIdentityDigest       string              `json:"hostIdentityDigest"`
	BinaryIdentityDigest     string              `json:"binaryIdentityDigest"`
	ContractDigest           string              `json:"contractDigest"`
	ProfileDigest            string              `json:"profileDigest"`
	SuiteDigest              string              `json:"suiteDigest"`
	ProbeArtifactDigest      string              `json:"probeArtifactDigest"`
	AggregateChallengeDigest string              `json:"aggregateChallengeDigest"`
	TopologyDigest           string              `json:"topologyDigest"`
	VerifierKeyID            string              `json:"verifierKeyId"`
	ProbeReceiptKeyID        string              `json:"probeReceiptKeyId"`
	Verdicts                 AuthorityVerdictsV1 `json:"verdicts"`
}

type CodexAuthorityConfigV1 struct {
	SchemaVersion            string   `json:"schemaVersion"`
	AuthorityNamespace       string   `json:"authorityNamespace"`
	AuthorityGeneration      uint64   `json:"authorityGeneration"`
	TrustRootGeneration      uint64   `json:"trustRootGeneration"`
	KeysetDigest             string   `json:"keysetDigest"`
	CurrentEvidenceDigest    string   `json:"currentEvidenceDigest"`
	RevokedEvidenceDigests   []string `json:"revokedEvidenceDigests"`
	RevokedSuiteDigests      []string `json:"revokedSuiteDigests"`
	RevokedChallengeDigests  []string `json:"revokedChallengeDigests"`
	RevocationSetDigest      string   `json:"revocationSetDigest"`
	HostIdentityDigest       string   `json:"hostIdentityDigest"`
	BootstrapID              string   `json:"bootstrapId"`
	SuiteDigest              string   `json:"suiteDigest"`
	ProbeArtifactDigest      string   `json:"probeArtifactDigest"`
	AggregateChallengeDigest string   `json:"aggregateChallengeDigest"`
	ContractDigest           string   `json:"contractDigest"`
	ProfileDigest            string   `json:"profileDigest"`
	ConfigKeyID              string   `json:"configKeyId"`
	IssuedAt                 string   `json:"issuedAt"`
}

type ChallengeProjectionV1 struct {
	ReceiptChallengeDigest string `json:"receiptChallengeDigest"`
	ReceiptDigest          string `json:"receiptDigest"`
	VariantID              string `json:"variantId"`
}

type CodexAuthorityMetadataV1 struct {
	SchemaVersion            string   `json:"schemaVersion"`
	CodexVersion             string   `json:"codexVersion"`
	BinaryIdentityDigest     string   `json:"binaryIdentityDigest"`
	HostIdentityDigest       string   `json:"hostIdentityDigest"`
	Platform                 string   `json:"platform"`
	LauncherKind             string   `json:"launcherKind"`
	EvidenceDigest           string   `json:"evidenceDigest"`
	ConfigDigest             string   `json:"configDigest"`
	KeysetDigest             string   `json:"keysetDigest"`
	FenceDigest              string   `json:"fenceDigest"`
	SuiteDigest              string   `json:"suiteDigest"`
	ProfileDigest            string   `json:"profileDigest"`
	ArgvMatrixDigest         string   `json:"argvMatrixDigest"`
	EnvironmentDigest        string   `json:"environmentDigest"`
	EventContractDigest      string   `json:"eventContractDigest"`
	PermissionContractDigest string   `json:"permissionContractDigest"`
	ToolPolicyDigest         string   `json:"toolPolicyDigest"`
	ResultContractDigest     string   `json:"resultContractDigest"`
	OutputLimitDigest        string   `json:"outputLimitDigest"`
	NativeBudgetsDigest      string   `json:"nativeBudgetsDigest"`
	TrustRootKeyID           string   `json:"trustRootKeyId"`
	EvidenceSignerKeyID      string   `json:"evidenceSignerKeyId"`
	TrustRootGeneration      uint64   `json:"trustRootGeneration"`
	AuthorityGeneration      uint64   `json:"authorityGeneration"`
	RevocationSetDigest      string   `json:"revocationSetDigest"`
	ObservedAt               string   `json:"observedAt"`
	ValidUntil               string   `json:"validUntil"`
	ExecutionProfiles        []string `json:"executionProfiles"`
	IsolationClaim           string   `json:"isolationClaim"`
}

// CodexWorkerAuthorityContextV1 is intentionally public-key-free and
// private-key-free. The count is explicit so adapters cannot silently add an
// authority signer to a Worker context without failing validation.
type CodexWorkerAuthorityContextV1 struct {
	SchemaVersion                   string `json:"schemaVersion"`
	ConfigDigest                    string `json:"configDigest"`
	EvidenceDigest                  string `json:"evidenceDigest"`
	FenceDigest                     string `json:"fenceDigest"`
	LaunchReceiptDigest             string `json:"launchReceiptDigest"`
	AuthoritySigningPrivateKeyCount uint8  `json:"authoritySigningPrivateKeyCount"`
}

func parseSignedPayload[T any](data []byte, maximum int, validate func(T) error) (SignedEnvelopeV1, T, error) {
	var zero T
	var envelope SignedEnvelopeV1
	if err := decodeClosed(data, maximum, &envelope); err != nil {
		return SignedEnvelopeV1{}, zero, err
	}
	if len(envelope.Signatures) != 1 || !validDigest(envelope.PayloadDigest) {
		return SignedEnvelopeV1{}, zero, errors.New("codex signed object envelope is invalid")
	}
	signature := envelope.Signatures[0]
	if len(signature.Value) != base64.StdEncoding.EncodedLen(ed25519.SignatureSize) {
		return SignedEnvelopeV1{}, zero, errors.New("codex signed object signature shape is invalid")
	}
	decodedSignature, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
	if signature.Algorithm != "Ed25519" || !validID(signature.KeyID) || decodeErr != nil || len(decodedSignature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(decodedSignature) != signature.Value {
		return SignedEnvelopeV1{}, zero, errors.New("codex signed object signature shape is invalid")
	}
	digest, err := canonical.DigestJSON(envelope.Payload)
	if err != nil || digest != envelope.PayloadDigest {
		return SignedEnvelopeV1{}, zero, errors.New("codex signed object payload digest is invalid")
	}
	var payload T
	if err := decodeClosed(envelope.Payload, maximum, &payload); err != nil {
		return SignedEnvelopeV1{}, zero, err
	}
	if err := validate(payload); err != nil {
		return SignedEnvelopeV1{}, zero, err
	}
	return envelope, payload, nil
}

func ParseCodexAuthorityConfig(data []byte) (SignedEnvelopeV1, CodexAuthorityConfigV1, error) {
	return parseSignedPayload(data, 64<<10, func(config CodexAuthorityConfigV1) error { return config.Validate() })
}

func ParseCodexProductionEvidence(data []byte) (SignedEnvelopeV1, CodexProductionEvidenceV1, error) {
	return parseSignedPayload(data, 256<<10, func(evidence CodexProductionEvidenceV1) error { return evidence.Validate() })
}

func ParseCodexProbeObservation(data []byte) (SignedEnvelopeV1, CodexProbeObservationV1, error) {
	return parseSignedPayload(data, 256<<10, func(observation CodexProbeObservationV1) error { return observation.Validate() })
}

func ParseCodexProbeReceipt(data []byte) (SignedEnvelopeV1, CodexProbeExecutionReceiptV1, error) {
	return parseSignedPayload(data, 64<<10, func(receipt CodexProbeExecutionReceiptV1) error { return receipt.Validate() })
}

func ParseCodexAuthorityKeysetEnvelope(data []byte) (SignedEnvelopeV1, error) {
	var envelope SignedEnvelopeV1
	if err := decodeClosed(data, 64<<10, &envelope); err != nil {
		return SignedEnvelopeV1{}, err
	}
	if len(envelope.Signatures) == 0 || len(envelope.Signatures) > 2 || !validDigest(envelope.PayloadDigest) {
		return SignedEnvelopeV1{}, errors.New("codex authority keyset envelope is invalid")
	}
	payloadDigest, err := canonical.DigestJSON(envelope.Payload)
	if err != nil || payloadDigest != envelope.PayloadDigest {
		return SignedEnvelopeV1{}, errors.New("codex authority keyset envelope digest is invalid")
	}
	for index, signature := range envelope.Signatures {
		if len(signature.Value) != base64.StdEncoding.EncodedLen(ed25519.SignatureSize) {
			return SignedEnvelopeV1{}, errors.New("codex authority keyset envelope signature set is invalid")
		}
		value, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
		if signature.Algorithm != "Ed25519" || !validID(signature.KeyID) || decodeErr != nil || len(value) != ed25519.SignatureSize || index > 0 && envelope.Signatures[index-1].KeyID >= signature.KeyID {
			return SignedEnvelopeV1{}, errors.New("codex authority keyset envelope signature set is invalid")
		}
	}
	var keyset CodexAuthorityKeysetV1
	if err := decodeClosed(envelope.Payload, 64<<10, &keyset); err != nil {
		return SignedEnvelopeV1{}, err
	}
	if err := keyset.validate(); err != nil {
		return SignedEnvelopeV1{}, err
	}
	return envelope, nil
}

func (context CodexWorkerAuthorityContextV1) Validate() error {
	if context.SchemaVersion != workerAuthoritySchema || context.AuthoritySigningPrivateKeyCount != 0 ||
		!validDigest(context.ConfigDigest) || !validDigest(context.EvidenceDigest) || !validDigest(context.FenceDigest) || !validDigest(context.LaunchReceiptDigest) {
		return errors.New("codex worker authority context is invalid")
	}
	return nil
}

func canonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func formatAuthorityTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseAuthorityTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("authority time is not UTC")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		return time.Time{}, errors.New("authority time is invalid")
	}
	return parsed, nil
}

func validDigest(value string) bool     { return digestPattern.MatchString(value) }
func validID(value string) bool         { return idPattern.MatchString(value) }
func validGeneration(value uint64) bool { return value > 0 && value <= maxSafeGeneration }

func decodeNonce(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("authority nonce is invalid")
	}
	return decoded, nil
}

func newNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validateDigestSet(values []string, maximum int) error {
	if len(values) > maximum {
		return errors.New("authority digest set is oversized")
	}
	for index, value := range values {
		if !validDigest(value) || index > 0 && values[index-1] >= value {
			return errors.New("authority digest set is invalid")
		}
	}
	return nil
}

func validateIDSet(values []string, maximum int) error {
	if len(values) > maximum {
		return errors.New("authority id set is oversized")
	}
	for index, value := range values {
		if !validID(value) || index > 0 && values[index-1] >= value {
			return errors.New("authority id set is invalid")
		}
	}
	return nil
}

func decodeClosed(data []byte, maximum int, target any) error {
	if len(data) == 0 || len(data) > maximum {
		return errors.New("authority document size is invalid")
	}
	if _, err := canonical.JSON(data); err != nil {
		return errors.New("authority document is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errors.New("authority document is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("authority document is invalid")
	}
	return nil
}

func receiptChallengeDigest(receipt CodexProbeExecutionReceiptV1) (string, error) {
	projection := struct {
		ChallengeNonce   string `json:"challengeNonce"`
		MarkerDigest     string `json:"markerDigest"`
		TranscriptDigest string `json:"transcriptDigest"`
		VariantID        string `json:"variantId"`
	}{receipt.ChallengeNonce, receipt.MarkerDigest, receipt.TranscriptDigest, receipt.VariantID}
	return canonicalDigest(projection)
}

func AggregateChallengeDigest(receipts []CodexProbeExecutionReceiptV1, receiptDigests map[string]string) (string, error) {
	if len(receipts) == 0 || len(receipts) > 32 {
		return "", errors.New("probe receipt set is invalid")
	}
	ordered := append([]CodexProbeExecutionReceiptV1(nil), receipts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].VariantID < ordered[j].VariantID })
	projection := make([]ChallengeProjectionV1, 0, len(ordered))
	for index, receipt := range ordered {
		if !validID(receipt.VariantID) || index > 0 && ordered[index-1].VariantID == receipt.VariantID {
			return "", errors.New("probe variant set is invalid")
		}
		digest, ok := receiptDigests[receipt.VariantID]
		if !ok || !validDigest(digest) {
			return "", errors.New("probe receipt digest is missing")
		}
		computed, err := receiptChallengeDigest(receipt)
		if err != nil || computed != receipt.ReceiptChallengeDigest {
			return "", errors.New("probe receipt challenge projection is invalid")
		}
		projection = append(projection, ChallengeProjectionV1{receipt.ReceiptChallengeDigest, digest, receipt.VariantID})
	}
	return canonicalDigest(projection)
}

func RevocationSetDigest(config CodexAuthorityConfigV1) (string, error) {
	projection := struct {
		RevokedChallengeDigests []string `json:"revokedChallengeDigests"`
		RevokedEvidenceDigests  []string `json:"revokedEvidenceDigests"`
		RevokedSuiteDigests     []string `json:"revokedSuiteDigests"`
	}{config.RevokedChallengeDigests, config.RevokedEvidenceDigests, config.RevokedSuiteDigests}
	return canonicalDigest(projection)
}

func (identity ExecutableIdentityV1) Validate() error {
	if !filepath.IsAbs(identity.CanonicalRealpath) || filepath.Clean(identity.CanonicalRealpath) != identity.CanonicalRealpath || len(identity.CanonicalRealpath) > 4096 || identity.Inode == 0 || identity.MountIDUnique == 0 || identity.Size == 0 || strings.TrimSpace(identity.Version) == "" || !validDigest(identity.SHA256) || !validDigest(identity.VersionOutputDigest) {
		return errors.New("codex executable identity is invalid")
	}
	return nil
}

func (binding CodexContractBindingV1) Validate() error {
	for _, digest := range []string{binding.AdapterContractDigest, binding.LauncherBuildDigest, binding.ProfileDigest, binding.ArgvMatrixDigest, binding.EnvironmentDigest, binding.EventContractDigest, binding.PermissionContractDigest, binding.ToolPolicyDigest, binding.ResultContractDigest, binding.OutputLimitDigest, binding.NativeBudgetsDigest} {
		if !validDigest(digest) {
			return errors.New("codex contract binding digest is invalid")
		}
	}
	return validateExecutionProfiles(binding.ExecutionProfiles)
}

func validateExecutionProfiles(profiles []string) error {
	if len(profiles) < 1 || len(profiles) > 2 {
		return errors.New("codex execution profiles are invalid")
	}
	previous := -1
	for _, profile := range profiles {
		index := -1
		if profile == "read-only" {
			index = 0
		} else if profile == "workspace-write" {
			index = 1
		}
		if index < 0 || index <= previous {
			return errors.New("codex execution profiles are invalid")
		}
		previous = index
	}
	return nil
}

func (observation CodexProbeObservationV1) Validate() error {
	if observation.SchemaVersion != "marshal.codex.probe-observation.v1" || !validID(observation.AuthorityNamespace) || !validGeneration(observation.AuthorityGeneration) || !validGeneration(observation.TrustRootGeneration) || !validID(observation.VerifierKeyID) || !observation.Verdicts.allTrue() {
		return errors.New("codex probe observation is invalid")
	}
	if _, err := decodeNonce(observation.BootstrapID); err != nil {
		return err
	}
	if _, err := decodeNonce(observation.ObservationNonce); err != nil {
		return err
	}
	for _, digest := range []string{observation.VerifierBuildDigest, observation.SuiteDigest, observation.ProbeArtifactDigest, observation.AggregateChallengeDigest, observation.TopologyDigest} {
		if !validDigest(digest) {
			return errors.New("codex probe observation digest is invalid")
		}
	}
	if err := observation.HostAttestation.HostIdentity.Validate(); err != nil {
		return err
	}
	if err := observation.BinaryIdentity.Validate(); err != nil {
		return err
	}
	if err := observation.Contract.Validate(); err != nil {
		return err
	}
	if err := validateDigestSet(observation.ReceiptDigests, 32); err != nil || len(observation.ReceiptDigests) == 0 {
		return errors.New("codex probe observation receipt set is invalid")
	}
	observedAt, err := parseAuthorityTime(observation.ObservedAt)
	if err != nil {
		return err
	}
	validUntil, err := parseAuthorityTime(observation.ValidUntil)
	if err != nil || validUntil.Before(observedAt) || validUntil.Sub(observedAt) > 24*time.Hour {
		return errors.New("codex probe observation time window is invalid")
	}
	return nil
}

func (receipt CodexProbeExecutionReceiptV1) Validate() error {
	if receipt.SchemaVersion != probeReceiptSchema || !validID(receipt.AuthorityNamespace) || !validGeneration(receipt.AuthorityGeneration) || !validGeneration(receipt.TrustRootGeneration) || !validID(receipt.VariantID) || !validID(receipt.ReceiptKeyID) {
		return errors.New("codex probe receipt identity is invalid")
	}
	if _, err := decodeNonce(receipt.BootstrapID); err != nil {
		return err
	}
	if _, err := decodeNonce(receipt.ChallengeNonce); err != nil {
		return err
	}
	for _, digest := range []string{receipt.SuiteDigest, receipt.ProbeArtifactDigest, receipt.HostIdentityDigest, receipt.BinaryIdentityDigest, receipt.ArgvDigest, receipt.EnvironmentDigest, receipt.TopologyDigest, receipt.TranscriptDigest, receipt.MarkerDigest, receipt.ReceiptChallengeDigest, receipt.EventContractDigest, receipt.PermissionContractDigest} {
		if !validDigest(digest) {
			return errors.New("codex probe receipt digest is invalid")
		}
	}
	started, err := parseAuthorityTime(receipt.StartedAt)
	if err != nil {
		return err
	}
	ended, err := parseAuthorityTime(receipt.EndedAt)
	if err != nil || ended.Before(started) {
		return errors.New("codex probe receipt time order is invalid")
	}
	projected, err := receiptChallengeDigest(receipt)
	if err != nil || projected != receipt.ReceiptChallengeDigest {
		return errors.New("codex probe receipt challenge projection is invalid")
	}
	return nil
}

func (evidence CodexProductionEvidenceV1) Validate() error {
	if evidence.SchemaVersion != productionEvidenceSchema || !validID(evidence.AuthorityNamespace) || !validGeneration(evidence.AuthorityGeneration) || !validGeneration(evidence.TrustRootGeneration) || !validID(evidence.EvidenceKeyID) || !validID(evidence.VerifierKeyID) || !validID(evidence.ProbeReceiptKeyID) || !evidence.Verdicts.allTrue() {
		return errors.New("codex production evidence identity is invalid")
	}
	if _, err := decodeNonce(evidence.BootstrapID); err != nil {
		return err
	}
	for _, digest := range []string{evidence.ObservationDigest, evidence.HostIdentityDigest, evidence.BinaryIdentityDigest, evidence.ContractDigest, evidence.ProfileDigest, evidence.SuiteDigest, evidence.ProbeArtifactDigest, evidence.AggregateChallengeDigest, evidence.TopologyDigest} {
		if !validDigest(digest) {
			return errors.New("codex production evidence digest is invalid")
		}
	}
	if len(evidence.ReceiptDigests) == 0 || validateDigestSet(evidence.ReceiptDigests, 32) != nil {
		return errors.New("codex production evidence receipt set is invalid")
	}
	validFrom, err := parseAuthorityTime(evidence.ValidFrom)
	if err != nil {
		return err
	}
	issuedAt, err := parseAuthorityTime(evidence.IssuedAt)
	if err != nil {
		return err
	}
	validUntil, err := parseAuthorityTime(evidence.ValidUntil)
	if err != nil || issuedAt.Before(validFrom) || validUntil.Before(issuedAt) {
		return errors.New("codex production evidence time order is invalid")
	}
	return nil
}

func (config CodexAuthorityConfigV1) Validate() error {
	if config.SchemaVersion != authorityConfigSchema || !validID(config.AuthorityNamespace) || !validGeneration(config.AuthorityGeneration) || !validGeneration(config.TrustRootGeneration) || !validID(config.ConfigKeyID) {
		return errors.New("codex authority config identity is invalid")
	}
	if _, err := decodeNonce(config.BootstrapID); err != nil {
		return err
	}
	for _, digest := range []string{config.KeysetDigest, config.CurrentEvidenceDigest, config.RevocationSetDigest, config.HostIdentityDigest, config.SuiteDigest, config.ProbeArtifactDigest, config.AggregateChallengeDigest, config.ContractDigest, config.ProfileDigest} {
		if !validDigest(digest) {
			return errors.New("codex authority config digest is invalid")
		}
	}
	if err := validateDigestSet(config.RevokedEvidenceDigests, 256); err != nil {
		return err
	}
	if err := validateDigestSet(config.RevokedSuiteDigests, 64); err != nil {
		return err
	}
	if err := validateDigestSet(config.RevokedChallengeDigests, 256); err != nil {
		return err
	}
	if _, err := parseAuthorityTime(config.IssuedAt); err != nil {
		return err
	}
	projected, err := RevocationSetDigest(config)
	if err != nil || projected != config.RevocationSetDigest {
		return errors.New("codex authority revocation projection is invalid")
	}
	return nil
}

func validateAuthorityProjection(config CodexAuthorityConfigV1, evidence CodexProductionEvidenceV1, evidenceDigest string, receipts []CodexProbeExecutionReceiptV1, receiptDigests map[string]string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	for _, receipt := range receipts {
		if err := receipt.Validate(); err != nil {
			return err
		}
	}
	revocationDigest, err := RevocationSetDigest(config)
	if err != nil || revocationDigest != config.RevocationSetDigest {
		return errors.New("codex revocation projection is invalid")
	}
	aggregate, err := AggregateChallengeDigest(receipts, receiptDigests)
	if err != nil || aggregate != config.AggregateChallengeDigest || aggregate != evidence.AggregateChallengeDigest {
		return errors.New("codex aggregate challenge projection is invalid")
	}
	if config.CurrentEvidenceDigest != evidenceDigest ||
		config.AuthorityNamespace != evidence.AuthorityNamespace || config.AuthorityGeneration != evidence.AuthorityGeneration || config.TrustRootGeneration != evidence.TrustRootGeneration ||
		config.HostIdentityDigest != evidence.HostIdentityDigest || config.BootstrapID != evidence.BootstrapID || config.SuiteDigest != evidence.SuiteDigest ||
		config.ProbeArtifactDigest != evidence.ProbeArtifactDigest || config.ContractDigest != evidence.ContractDigest || config.ProfileDigest != evidence.ProfileDigest {
		return errors.New("codex config and evidence identity differ")
	}
	if !evidence.Verdicts.allTrue() {
		return errors.New("codex evidence verdicts are not all true")
	}
	expectedReceiptDigests := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		expectedReceiptDigests = append(expectedReceiptDigests, receiptDigests[receipt.VariantID])
	}
	sort.Strings(expectedReceiptDigests)
	if !equalStrings(expectedReceiptDigests, evidence.ReceiptDigests) || validateDigestSet(evidence.ReceiptDigests, 32) != nil {
		return errors.New("codex evidence receipt set differs")
	}
	if contains(config.RevokedEvidenceDigests, evidenceDigest) || contains(config.RevokedSuiteDigests, evidence.SuiteDigest) || contains(config.RevokedChallengeDigests, aggregate) {
		return errors.New("codex evidence is revoked")
	}
	for _, receipt := range receipts {
		if receipt.AuthorityNamespace != evidence.AuthorityNamespace || receipt.AuthorityGeneration != evidence.AuthorityGeneration || receipt.TrustRootGeneration != evidence.TrustRootGeneration || receipt.BootstrapID != evidence.BootstrapID || receipt.SuiteDigest != evidence.SuiteDigest || receipt.ProbeArtifactDigest != evidence.ProbeArtifactDigest || receipt.HostIdentityDigest != evidence.HostIdentityDigest || receipt.BinaryIdentityDigest != evidence.BinaryIdentityDigest || receipt.TopologyDigest != evidence.TopologyDigest {
			return errors.New("codex receipt and evidence identity differ")
		}
		if contains(config.RevokedChallengeDigests, receipt.ReceiptChallengeDigest) {
			return errors.New("codex receipt challenge is revoked")
		}
	}
	return nil
}

type VerifiedAuthorityBundleV1 struct {
	Config       CodexAuthorityConfigV1
	Evidence     CodexProductionEvidenceV1
	Observation  CodexProbeObservationV1
	Receipts     []CodexProbeExecutionReceiptV1
	ConfigDigest string
}

func verifyPinnedKeyset(pin CodexActiveRootPinV1, envelope SignedEnvelopeV1, now time.Time) (CodexAuthorityKeysetV1, error) {
	if err := pin.Validate(); err != nil {
		return CodexAuthorityKeysetV1{}, err
	}
	var keyset CodexAuthorityKeysetV1
	if err := decodeClosed(envelope.Payload, 64<<10, &keyset); err != nil || keyset.validate() != nil {
		return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset payload is invalid")
	}
	if envelope.PayloadDigest != pin.KeysetDigest || keyset.AuthorityNamespace != pin.AuthorityNamespace || keyset.TrustRootGeneration != pin.TrustRootGeneration {
		return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset differs from active root pin")
	}
	expectedSignatures := 1
	if keyset.RootRotation != nil {
		expectedSignatures = 2
	}
	if len(envelope.Signatures) != expectedSignatures {
		return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset signature cardinality is invalid")
	}
	for index, signature := range envelope.Signatures {
		if len(signature.Value) != base64.StdEncoding.EncodedLen(ed25519.SignatureSize) {
			return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset signature set is invalid")
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
		if signature.Algorithm != "Ed25519" || !validID(signature.KeyID) || decodeErr != nil || len(decoded) != ed25519.SignatureSize || index > 0 && envelope.Signatures[index-1].KeyID >= signature.KeyID {
			return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset signature set is invalid")
		}
	}
	root, err := decodeEd25519Public(pin.RootPublicKey)
	if err != nil {
		return CodexAuthorityKeysetV1{}, err
	}
	for _, leaf := range keyset.Keys {
		leafPublic, _ := decodeEd25519Public(leaf.PublicKey)
		if bytes.Equal(root, leafPublic) {
			return CodexAuthorityKeysetV1{}, errors.New("codex root key is reused as a leaf authority")
		}
	}
	var rootSignature *SignatureV1
	for index := range envelope.Signatures {
		if envelope.Signatures[index].KeyID == pin.RootKeyID {
			rootSignature = &envelope.Signatures[index]
		}
	}
	if rootSignature == nil {
		return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset lacks active root signature")
	}
	rootEnvelope := envelope
	rootEnvelope.Signatures = []SignatureV1{*rootSignature}
	if err := verifyEnvelope(rootEnvelope, authorityKeysetSchema, authorityKeysetSchema, 64<<10, map[string]ed25519.PublicKey{pin.RootKeyID: root}, 1); err != nil {
		return CodexAuthorityKeysetV1{}, err
	}
	validFrom, err := parseAuthorityTime(keyset.ValidFrom)
	if err != nil || validFrom.After(now.Add(time.Minute)) {
		return CodexAuthorityKeysetV1{}, errors.New("codex authority keyset is not yet valid")
	}
	return keyset, nil
}

func keyForUsage(keyset CodexAuthorityKeysetV1, keyID, usage string, at time.Time) (ed25519.PublicKey, error) {
	if contains(keyset.RevokedKeyIDs, keyID) {
		return nil, errors.New("codex authority signing key is revoked")
	}
	for _, key := range keyset.Keys {
		if key.KeyID != keyID {
			continue
		}
		notBefore, err := parseAuthorityTime(key.NotBefore)
		if err != nil {
			return nil, err
		}
		notAfter, err := parseAuthorityTime(key.NotAfter)
		if err != nil || key.Usage != usage || at.Before(notBefore) || at.After(notAfter) {
			return nil, errors.New("codex authority signing key usage or validity is invalid")
		}
		return decodeEd25519Public(key.PublicKey)
	}
	return nil, errors.New("codex authority signing key is unknown")
}

func verifyLeafEnvelope(envelope SignedEnvelopeV1, domain, schema, usage, keyID string, at time.Time, maximum int, keyset CodexAuthorityKeysetV1) error {
	if len(envelope.Signatures) != 1 || envelope.Signatures[0].KeyID != keyID {
		return errors.New("codex signed object key identity is invalid")
	}
	key, err := keyForUsage(keyset, keyID, usage, at)
	if err != nil {
		return err
	}
	return verifyEnvelope(envelope, domain, schema, maximum, map[string]ed25519.PublicKey{keyID: key}, 1)
}

// VerifyAuthorityBundle is the only authority-object consumption entry point
// in this core slice. It accepts signed envelopes, validates exact domains and
// key usages, and binds config -> evidence -> observation -> receipts before
// returning payloads to a caller.
func VerifyAuthorityBundle(now time.Time, pin CodexActiveRootPinV1, keysetEnvelope, configEnvelope, evidenceEnvelope, observationEnvelope SignedEnvelopeV1, receiptEnvelopes []SignedEnvelopeV1, expectedHostNonce []byte, hostVerifier TPMHostAttestationVerifier, nonceFence *HostAttestationNonceFence) (VerifiedAuthorityBundleV1, error) {
	keyset, err := verifyPinnedKeyset(pin, keysetEnvelope, now)
	if err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	var config CodexAuthorityConfigV1
	if err := decodeClosed(configEnvelope.Payload, 64<<10, &config); err != nil || config.Validate() != nil {
		return VerifiedAuthorityBundleV1{}, errors.New("codex signed config payload is invalid")
	}
	configTime, _ := parseAuthorityTime(config.IssuedAt)
	if configTime.After(now.Add(time.Minute)) {
		return VerifiedAuthorityBundleV1{}, errors.New("codex authority config is from the future")
	}
	if err := verifyLeafEnvelope(configEnvelope, authorityConfigSchema, authorityConfigSchema, "config", config.ConfigKeyID, configTime, 64<<10, keyset); err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	if config.KeysetDigest != keysetEnvelope.PayloadDigest || config.TrustRootGeneration != keyset.TrustRootGeneration || config.AuthorityNamespace != keyset.AuthorityNamespace {
		return VerifiedAuthorityBundleV1{}, errors.New("codex config and pinned keyset differ")
	}
	var evidence CodexProductionEvidenceV1
	if err := decodeClosed(evidenceEnvelope.Payload, 256<<10, &evidence); err != nil || evidence.Validate() != nil {
		return VerifiedAuthorityBundleV1{}, errors.New("codex signed evidence payload is invalid")
	}
	evidenceTime, _ := parseAuthorityTime(evidence.IssuedAt)
	if err := verifyLeafEnvelope(evidenceEnvelope, productionEvidenceSchema, productionEvidenceSchema, "evidence", evidence.EvidenceKeyID, evidenceTime, 256<<10, keyset); err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	var observation CodexProbeObservationV1
	if err := decodeClosed(observationEnvelope.Payload, 256<<10, &observation); err != nil || observation.Validate() != nil {
		return VerifiedAuthorityBundleV1{}, errors.New("codex signed observation payload is invalid")
	}
	observedAt, _ := parseAuthorityTime(observation.ObservedAt)
	if err := verifyLeafEnvelope(observationEnvelope, "marshal.codex.probe-observation.v1", "marshal.codex.probe-observation.v1", "verifier-attestation", observation.VerifierKeyID, observedAt, 256<<10, keyset); err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	if evidence.ObservationDigest != observationEnvelope.PayloadDigest || !equalStrings(evidence.ReceiptDigests, observation.ReceiptDigests) || evidence.AuthorityNamespace != observation.AuthorityNamespace || evidence.AuthorityGeneration != observation.AuthorityGeneration || evidence.TrustRootGeneration != observation.TrustRootGeneration || evidence.BootstrapID != observation.BootstrapID || evidence.VerifierKeyID != observation.VerifierKeyID || evidence.SuiteDigest != observation.SuiteDigest || evidence.ProbeArtifactDigest != observation.ProbeArtifactDigest || evidence.AggregateChallengeDigest != observation.AggregateChallengeDigest || evidence.TopologyDigest != observation.TopologyDigest || evidence.Verdicts != observation.Verdicts {
		return VerifiedAuthorityBundleV1{}, errors.New("codex evidence and observation projection differ")
	}
	hostDigest, _ := HostIdentityDigest(observation.HostAttestation.HostIdentity)
	binaryDigest, _ := canonicalDigest(observation.BinaryIdentity)
	contractDigest, _ := canonicalDigest(observation.Contract)
	if evidence.HostIdentityDigest != hostDigest || evidence.BinaryIdentityDigest != binaryDigest || evidence.ContractDigest != contractDigest || evidence.ProfileDigest != observation.Contract.ProfileDigest {
		return VerifiedAuthorityBundleV1{}, errors.New("codex observation identity projection differs")
	}
	validFrom, _ := parseAuthorityTime(evidence.ValidFrom)
	validUntil, _ := parseAuthorityTime(evidence.ValidUntil)
	observationValidUntil, _ := parseAuthorityTime(observation.ValidUntil)
	if observedAt.After(validFrom) || validFrom.After(evidenceTime) || evidenceTime.After(validUntil) || validUntil.After(observationValidUntil) || now.Sub(observedAt) > 24*time.Hour || now.After(validUntil) || now.Before(validFrom.Add(-time.Minute)) || observedAt.After(now.Add(time.Minute)) || evidenceTime.After(now.Add(time.Minute)) {
		return VerifiedAuthorityBundleV1{}, errors.New("codex evidence or observation freshness is invalid")
	}
	receipts := make([]CodexProbeExecutionReceiptV1, 0, len(receiptEnvelopes))
	receiptDigests := make(map[string]string, len(receiptEnvelopes))
	for _, envelope := range receiptEnvelopes {
		var receipt CodexProbeExecutionReceiptV1
		if err := decodeClosed(envelope.Payload, 64<<10, &receipt); err != nil || receipt.Validate() != nil {
			return VerifiedAuthorityBundleV1{}, errors.New("codex signed probe receipt payload is invalid")
		}
		endedAt, _ := parseAuthorityTime(receipt.EndedAt)
		if err := verifyLeafEnvelope(envelope, probeReceiptSchema, probeReceiptSchema, "probe-receipt", receipt.ReceiptKeyID, endedAt, 64<<10, keyset); err != nil {
			return VerifiedAuthorityBundleV1{}, err
		}
		if receipt.ReceiptKeyID != evidence.ProbeReceiptKeyID || endedAt.After(observedAt) {
			return VerifiedAuthorityBundleV1{}, errors.New("codex receipt signer or observation time differs")
		}
		if _, duplicate := receiptDigests[receipt.VariantID]; duplicate {
			return VerifiedAuthorityBundleV1{}, errors.New("codex receipt variant is duplicated")
		}
		receiptDigests[receipt.VariantID] = envelope.PayloadDigest
		receipts = append(receipts, receipt)
	}
	if observation.VerifierKeyID == evidence.ProbeReceiptKeyID || observation.VerifierKeyID == evidence.EvidenceKeyID || observation.VerifierKeyID == config.ConfigKeyID || evidence.ProbeReceiptKeyID == evidence.EvidenceKeyID || evidence.ProbeReceiptKeyID == config.ConfigKeyID || evidence.EvidenceKeyID == config.ConfigKeyID {
		return VerifiedAuthorityBundleV1{}, errors.New("codex authority signing roles are not separated")
	}
	if err := validateAuthorityProjection(config, evidence, evidenceEnvelope.PayloadDigest, receipts, receiptDigests); err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	if err := ValidateHostAttestation(observation.HostAttestation, config.HostIdentityDigest, expectedHostNonce, hostVerifier, nonceFence); err != nil {
		return VerifiedAuthorityBundleV1{}, err
	}
	return VerifiedAuthorityBundleV1{Config: config, Evidence: evidence, Observation: observation, Receipts: receipts, ConfigDigest: configEnvelope.PayloadDigest}, nil
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}

func verifyEnvelope(envelope SignedEnvelopeV1, domain, schema string, maximum int, keys map[string]ed25519.PublicKey, expectedSignatures int) error {
	if len(envelope.Payload) == 0 || len(envelope.Payload) > maximum || !validDigest(envelope.PayloadDigest) || len(envelope.Signatures) != expectedSignatures {
		return errors.New("signed envelope is invalid")
	}
	digest, err := canonical.DigestJSON(envelope.Payload)
	if err != nil || digest != envelope.PayloadDigest {
		return errors.New("signed envelope digest is invalid")
	}
	messageValue := struct {
		Domain        string `json:"domain"`
		PayloadDigest string `json:"payloadDigest"`
		SchemaVersion string `json:"schemaVersion"`
	}{domain, envelope.PayloadDigest, schema}
	raw, _ := json.Marshal(messageValue)
	message, err := canonical.JSON(raw)
	if err != nil {
		return errors.New("signed envelope projection is invalid")
	}
	for index, signature := range envelope.Signatures {
		if signature.Algorithm != "Ed25519" || !validID(signature.KeyID) || len(signature.Value) != base64.StdEncoding.EncodedLen(ed25519.SignatureSize) || index > 0 && envelope.Signatures[index-1].KeyID >= signature.KeyID {
			return errors.New("signed envelope signature set is invalid")
		}
		publicKey, ok := keys[signature.KeyID]
		decoded, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
		if !ok || decodeErr != nil || len(decoded) != ed25519.SignatureSize || !ed25519.Verify(publicKey, message, decoded) {
			return errors.New("signed envelope signature is invalid")
		}
	}
	return nil
}

func VerifyRootRotation(current CodexActiveRootPinV1, envelope SignedEnvelopeV1, now time.Time) (CodexActiveRootPinV1, error) {
	if err := current.Validate(); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	var keyset CodexAuthorityKeysetV1
	if err := decodeClosed(envelope.Payload, 64<<10, &keyset); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	if err := keyset.validate(); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	if keyset.SchemaVersion != authorityKeysetSchema || keyset.RootRotation == nil || keyset.AuthorityNamespace != current.AuthorityNamespace || keyset.TrustRootGeneration != current.TrustRootGeneration+1 || keyset.PreviousKeysetDigest == nil || *keyset.PreviousKeysetDigest != current.KeysetDigest {
		return CodexActiveRootPinV1{}, errors.New("codex root rotation lineage is invalid")
	}
	rotation := keyset.RootRotation
	oldKey, err := decodeEd25519Public(current.RootPublicKey)
	if err != nil {
		return CodexActiveRootPinV1{}, err
	}
	newKey, err := decodeEd25519Public(rotation.NewRootPublicKey)
	if err != nil || rotation.Algorithm != "Ed25519" || !validID(rotation.NewRootKeyID) {
		return CodexActiveRootPinV1{}, errors.New("codex new root is invalid")
	}
	for _, leaf := range keyset.Keys {
		leafPublic, _ := decodeEd25519Public(leaf.PublicKey)
		if bytes.Equal(oldKey, leafPublic) || bytes.Equal(newKey, leafPublic) {
			return CodexActiveRootPinV1{}, errors.New("codex root key is reused as a leaf authority")
		}
	}
	keys := map[string]ed25519.PublicKey{current.RootKeyID: oldKey, rotation.NewRootKeyID: newKey}
	if len(keys) != 2 {
		return CodexActiveRootPinV1{}, errors.New("codex root rotation reuses root identity")
	}
	if err := verifyEnvelope(envelope, authorityKeysetSchema, authorityKeysetSchema, 64<<10, keys, 2); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	validFrom, err := parseAuthorityTime(keyset.ValidFrom)
	if err != nil || validFrom.After(now.Add(time.Minute)) {
		return CodexActiveRootPinV1{}, errors.New("codex root rotation is not yet valid")
	}
	rootNotBefore, err := parseAuthorityTime(rotation.NotBefore)
	if err != nil || rootNotBefore.After(now.Add(time.Minute)) {
		return CodexActiveRootPinV1{}, errors.New("codex new root is not yet valid")
	}
	publicDigest := canonical.DigestBytes(newKey)
	return CodexActiveRootPinV1{
		SchemaVersion: activeRootPinSchema, AuthorityNamespace: current.AuthorityNamespace, BootstrapDigest: current.BootstrapDigest,
		RootKeyID: rotation.NewRootKeyID, RootAlgorithm: "Ed25519", RootPublicKey: rotation.NewRootPublicKey,
		RootPublicKeyDigest: publicDigest, TrustRootGeneration: keyset.TrustRootGeneration,
		KeysetDigest: envelope.PayloadDigest, ActivatedAt: formatAuthorityTime(now),
	}, nil
}

func VerifyInitialKeyset(pin CodexActiveRootPinV1, envelope SignedEnvelopeV1, now time.Time) error {
	if err := pin.Validate(); err != nil {
		return err
	}
	var keyset CodexAuthorityKeysetV1
	if err := decodeClosed(envelope.Payload, 64<<10, &keyset); err != nil || keyset.validate() != nil {
		return errors.New("codex initial authority keyset is invalid")
	}
	if keyset.PreviousKeysetDigest != nil || keyset.RootRotation != nil || keyset.AuthorityNamespace != pin.AuthorityNamespace || keyset.TrustRootGeneration != pin.TrustRootGeneration || envelope.PayloadDigest != pin.KeysetDigest || len(envelope.Signatures) != 1 || envelope.Signatures[0].KeyID != pin.RootKeyID {
		return errors.New("codex initial authority keyset lineage is invalid")
	}
	root, err := decodeEd25519Public(pin.RootPublicKey)
	if err != nil {
		return err
	}
	for _, leaf := range keyset.Keys {
		leafPublic, _ := decodeEd25519Public(leaf.PublicKey)
		if bytes.Equal(root, leafPublic) {
			return errors.New("codex initial root key is reused as a leaf authority")
		}
	}
	if err := verifyEnvelope(envelope, authorityKeysetSchema, authorityKeysetSchema, 64<<10, map[string]ed25519.PublicKey{pin.RootKeyID: root}, 1); err != nil {
		return err
	}
	validFrom, err := parseAuthorityTime(keyset.ValidFrom)
	if err != nil || validFrom.After(now.Add(time.Minute)) {
		return errors.New("codex initial authority keyset is not yet valid")
	}
	return nil
}

func VerifyKeysetAdvance(current CodexActiveRootPinV1, envelope SignedEnvelopeV1, now time.Time) (CodexActiveRootPinV1, error) {
	if err := current.Validate(); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	var keyset CodexAuthorityKeysetV1
	if err := decodeClosed(envelope.Payload, 64<<10, &keyset); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	if err := keyset.validate(); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	if keyset.RootRotation != nil || keyset.AuthorityNamespace != current.AuthorityNamespace || keyset.TrustRootGeneration != current.TrustRootGeneration || keyset.PreviousKeysetDigest == nil || *keyset.PreviousKeysetDigest != current.KeysetDigest || envelope.PayloadDigest == current.KeysetDigest {
		return CodexActiveRootPinV1{}, errors.New("codex keyset advance lineage is invalid")
	}
	root, err := decodeEd25519Public(current.RootPublicKey)
	if err != nil {
		return CodexActiveRootPinV1{}, err
	}
	if err := verifyEnvelope(envelope, authorityKeysetSchema, authorityKeysetSchema, 64<<10, map[string]ed25519.PublicKey{current.RootKeyID: root}, 1); err != nil {
		return CodexActiveRootPinV1{}, err
	}
	validFrom, err := parseAuthorityTime(keyset.ValidFrom)
	if err != nil || validFrom.After(now.Add(time.Minute)) {
		return CodexActiveRootPinV1{}, errors.New("codex keyset advance is not yet valid")
	}
	next := current
	next.KeysetDigest = envelope.PayloadDigest
	return next, nil
}

func (keyset CodexAuthorityKeysetV1) validate() error {
	if keyset.SchemaVersion != authorityKeysetSchema || !validID(keyset.AuthorityNamespace) || !validGeneration(keyset.TrustRootGeneration) || len(keyset.Keys) == 0 || len(keyset.Keys) > 32 {
		return errors.New("codex authority keyset is invalid")
	}
	if _, err := parseAuthorityTime(keyset.ValidFrom); err != nil {
		return err
	}
	if err := validateIDSet(keyset.RevokedKeyIDs, 32); err != nil {
		return err
	}
	validUsage := map[string]bool{"verifier-attestation": true, "probe-receipt": true, "launch-receipt": true, "evidence": true, "config": true}
	publicKeys := make(map[string]struct{}, len(keyset.Keys))
	for index, key := range keyset.Keys {
		if !validID(key.KeyID) || !validUsage[key.Usage] || key.Algorithm != "Ed25519" || index > 0 && keyset.Keys[index-1].KeyID >= key.KeyID {
			return errors.New("codex authority keyset leaf is invalid")
		}
		if _, err := decodeEd25519Public(key.PublicKey); err != nil {
			return err
		}
		if _, duplicate := publicKeys[key.PublicKey]; duplicate {
			return errors.New("codex authority key material is reused across roles")
		}
		publicKeys[key.PublicKey] = struct{}{}
		notBefore, err := parseAuthorityTime(key.NotBefore)
		if err != nil {
			return err
		}
		notAfter, err := parseAuthorityTime(key.NotAfter)
		if err != nil || !notAfter.After(notBefore) {
			return errors.New("codex authority key validity is invalid")
		}
	}
	if keyset.RootRotation != nil {
		if !validID(keyset.RootRotation.NewRootKeyID) || keyset.RootRotation.Algorithm != "Ed25519" {
			return errors.New("codex root rotation is invalid")
		}
		if _, err := decodeEd25519Public(keyset.RootRotation.NewRootPublicKey); err != nil {
			return err
		}
		if _, err := parseAuthorityTime(keyset.RootRotation.NotBefore); err != nil {
			return err
		}
	}
	return nil
}

func decodeEd25519Public(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("codex root public key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

func (pin CodexActiveRootPinV1) Validate() error {
	publicKey, err := decodeEd25519Public(pin.RootPublicKey)
	if pin.SchemaVersion != activeRootPinSchema || !validID(pin.AuthorityNamespace) || !validDigest(pin.BootstrapDigest) || !validID(pin.RootKeyID) || pin.RootAlgorithm != "Ed25519" || err != nil || canonical.DigestBytes(publicKey) != pin.RootPublicKeyDigest || !validGeneration(pin.TrustRootGeneration) || !validDigest(pin.KeysetDigest) {
		return errors.New("codex active root pin is invalid")
	}
	_, err = parseAuthorityTime(pin.ActivatedAt)
	return err
}

func (state CodexConsumerAuthorityStateV1) Validate() error {
	if state.SchemaVersion != consumerStateSchema {
		return errors.New("codex consumer authority state schema is invalid")
	}
	if _, err := decodeNonce(state.TransactionID); err != nil {
		return err
	}
	if err := state.ActiveRootPin.Validate(); err != nil {
		return err
	}
	fence := state.Fence
	if fence.SchemaVersion != consumerFenceSchema || fence.AdapterID != adapterID || !validGeneration(fence.TrustRootGeneration) || !validGeneration(fence.AuthorityGeneration) || !validDigest(fence.BootstrapDigest) || !validDigest(fence.HostIdentityDigest) || !validDigest(fence.KeysetDigest) || !validDigest(fence.ConfigDigest) || !validDigest(fence.RevocationSetDigest) || !validDigest(fence.CurrentEvidenceDigest) {
		return errors.New("codex consumer fence is invalid")
	}
	if _, err := decodeNonce(fence.BootstrapID); err != nil {
		return err
	}
	if state.ActiveRootPin.AuthorityNamespace != fence.AuthorityNamespace || state.ActiveRootPin.BootstrapDigest != fence.BootstrapDigest || state.ActiveRootPin.TrustRootGeneration != fence.TrustRootGeneration || state.ActiveRootPin.KeysetDigest != fence.KeysetDigest {
		return errors.New("codex root pin and fence differ")
	}
	committed, err := parseAuthorityTime(state.CommittedAt)
	if err != nil {
		return err
	}
	fenceCommitted, err := parseAuthorityTime(fence.CommittedAt)
	if err != nil || !committed.Equal(fenceCommitted) {
		return errors.New("codex authority state commit times differ")
	}
	return nil
}

func StateDigest(state CodexConsumerAuthorityStateV1) (string, error) {
	if err := state.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(state)
}
func FenceDigest(fence CodexConsumerFenceV1) (string, error) { return canonicalDigest(fence) }

func NewConsumerAuthorityState(pin CodexActiveRootPinV1, fence CodexConsumerFenceV1, now time.Time) (CodexConsumerAuthorityStateV1, error) {
	nonce, err := newNonce()
	if err != nil {
		return CodexConsumerAuthorityStateV1{}, err
	}
	fence.CommittedAt = formatAuthorityTime(now)
	state := CodexConsumerAuthorityStateV1{consumerStateSchema, nonce, pin, fence, formatAuthorityTime(now)}
	if err := state.Validate(); err != nil {
		return CodexConsumerAuthorityStateV1{}, err
	}
	return state, nil
}

func ValidateStateAdvance(current *CodexConsumerAuthorityStateV1, next CodexConsumerAuthorityStateV1) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if err := current.Validate(); err != nil {
		return err
	}
	oldFence, newFence := current.Fence, next.Fence
	if oldFence.AuthorityNamespace != newFence.AuthorityNamespace || oldFence.BootstrapDigest != newFence.BootstrapDigest || oldFence.HostIdentityDigest != newFence.HostIdentityDigest || oldFence.BootstrapID != newFence.BootstrapID {
		return errors.New("codex authority bootstrap identity changed")
	}
	if newFence.TrustRootGeneration < oldFence.TrustRootGeneration || newFence.AuthorityGeneration < oldFence.AuthorityGeneration {
		return errors.New("codex authority rollback rejected")
	}
	if newFence.TrustRootGeneration == oldFence.TrustRootGeneration && next.ActiveRootPin.RootPublicKeyDigest != current.ActiveRootPin.RootPublicKeyDigest {
		return errors.New("codex same-generation root replacement rejected")
	}
	if newFence.TrustRootGeneration == oldFence.TrustRootGeneration &&
		(next.ActiveRootPin.RootKeyID != current.ActiveRootPin.RootKeyID || next.ActiveRootPin.RootPublicKey != current.ActiveRootPin.RootPublicKey || next.ActiveRootPin.ActivatedAt != current.ActiveRootPin.ActivatedAt) {
		return errors.New("codex same-generation root identity replacement rejected")
	}
	if newFence.AuthorityGeneration == oldFence.AuthorityGeneration {
		if oldFence.ConfigDigest != newFence.ConfigDigest || oldFence.RevocationSetDigest != newFence.RevocationSetDigest || oldFence.CurrentEvidenceDigest != newFence.CurrentEvidenceDigest || oldFence.KeysetDigest != newFence.KeysetDigest {
			return errors.New("codex same-generation authority identity conflict")
		}
	}
	return nil
}

func NewAuthorityMetadata(config CodexAuthorityConfigV1, evidence CodexProductionEvidenceV1, pin CodexActiveRootPinV1, fence CodexConsumerFenceV1, contract CodexContractMetadataInput, observedAt time.Time) (CodexAuthorityMetadataV1, error) {
	fenceDigest, err := FenceDigest(fence)
	if err != nil {
		return CodexAuthorityMetadataV1{}, err
	}
	metadata := CodexAuthorityMetadataV1{
		SchemaVersion: authorityMetadataSchema, CodexVersion: contract.CodexVersion, BinaryIdentityDigest: evidence.BinaryIdentityDigest,
		HostIdentityDigest: evidence.HostIdentityDigest, Platform: "linux", LauncherKind: "linux-execveat-sealed-memfd-ptrace-v1",
		EvidenceDigest: config.CurrentEvidenceDigest, ConfigDigest: fence.ConfigDigest, KeysetDigest: config.KeysetDigest, FenceDigest: fenceDigest,
		SuiteDigest: evidence.SuiteDigest, ProfileDigest: evidence.ProfileDigest, ArgvMatrixDigest: contract.ArgvMatrixDigest,
		EnvironmentDigest: contract.EnvironmentDigest, EventContractDigest: contract.EventContractDigest, PermissionContractDigest: contract.PermissionContractDigest,
		ToolPolicyDigest: contract.ToolPolicyDigest, ResultContractDigest: contract.ResultContractDigest, OutputLimitDigest: contract.OutputLimitDigest,
		NativeBudgetsDigest: contract.NativeBudgetsDigest, TrustRootKeyID: pin.RootKeyID, EvidenceSignerKeyID: evidence.EvidenceKeyID,
		TrustRootGeneration: evidence.TrustRootGeneration, AuthorityGeneration: evidence.AuthorityGeneration, RevocationSetDigest: config.RevocationSetDigest,
		ObservedAt: formatAuthorityTime(observedAt), ValidUntil: evidence.ValidUntil, ExecutionProfiles: append([]string(nil), contract.ExecutionProfiles...),
		IsolationClaim: "cooperative-host-process-not-malicious-code-sandbox",
	}
	if err := metadata.Validate(); err != nil {
		return CodexAuthorityMetadataV1{}, err
	}
	return metadata, nil
}

type CodexContractMetadataInput struct {
	CodexVersion, ArgvMatrixDigest, EnvironmentDigest, EventContractDigest, PermissionContractDigest string
	ToolPolicyDigest, ResultContractDigest, OutputLimitDigest, NativeBudgetsDigest                   string
	ExecutionProfiles                                                                                []string
}

func (metadata CodexAuthorityMetadataV1) Validate() error {
	digests := []string{metadata.BinaryIdentityDigest, metadata.HostIdentityDigest, metadata.EvidenceDigest, metadata.ConfigDigest, metadata.KeysetDigest, metadata.FenceDigest, metadata.SuiteDigest, metadata.ProfileDigest, metadata.ArgvMatrixDigest, metadata.EnvironmentDigest, metadata.EventContractDigest, metadata.PermissionContractDigest, metadata.ToolPolicyDigest, metadata.ResultContractDigest, metadata.OutputLimitDigest, metadata.NativeBudgetsDigest, metadata.RevocationSetDigest}
	if metadata.SchemaVersion != authorityMetadataSchema || strings.TrimSpace(metadata.CodexVersion) == "" || metadata.Platform != "linux" || metadata.LauncherKind != "linux-execveat-sealed-memfd-ptrace-v1" || metadata.IsolationClaim != "cooperative-host-process-not-malicious-code-sandbox" || !validGeneration(metadata.TrustRootGeneration) || !validGeneration(metadata.AuthorityGeneration) || !validID(metadata.TrustRootKeyID) || !validID(metadata.EvidenceSignerKeyID) {
		return errors.New("codex authority metadata is invalid")
	}
	for _, digest := range digests {
		if !validDigest(digest) {
			return errors.New("codex authority metadata digest is invalid")
		}
	}
	if err := validateExecutionProfiles(metadata.ExecutionProfiles); err != nil {
		return err
	}
	observedAt, err := parseAuthorityTime(metadata.ObservedAt)
	if err != nil {
		return err
	}
	validUntil, err := parseAuthorityTime(metadata.ValidUntil)
	if err != nil {
		return err
	}
	if validUntil.Before(observedAt) {
		return errors.New("codex authority metadata time order is invalid")
	}
	return nil
}

func digestBytesHex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
