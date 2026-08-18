package codex

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	codexAPAPVersion        = "0.145.0"
	codexAPAPResponseDomain = "marshal-codex-apap-response-v1\x00"
	codexAPAPResponseUsage  = "codex-apap-response"
	codexLaunchDomain       = "marshal-codex-launch-receipt-v1\x00"
	codexLaunchUsage        = "launch-receipt"
	codexLaunchSchema       = "marshal.codex.launch-receipt.v1"
	codexRequiredMemfdSeals = uint32(15)
)

// CodexAPAPAuthority is verifier-owned current state. CandidateExecutable is
// the identity calculated from the held executable; this bridge never opens a
// pathname and cannot activate an Adapter or register Codex as supported.
type CodexAPAPAuthority struct {
	ProviderInstanceID  string
	ProviderSequence    uint64
	Peer                authorityprovider.PeerIdentity
	ResponseKeys        authorityprovider.KeyResolver
	LaunchKeys          authorityprovider.KeyResolver
	Source              atomicAuthoritySource
	CandidateExecutable ExecutableIdentityV1
}

type CodexAPAPSignedResponse struct {
	Document  []byte
	Signature authorityprovider.SignedObjectEnvelopeV1
}

type CodexAPAPBeginInput struct {
	RequestID string
	CommandID string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type CodexAPAPProbeSession struct {
	ProviderInstanceID                      string
	ProviderSequence                        uint64
	PeerPrincipalDigest                     string
	AuthorityProfile                        authorityprovider.AuthorityProfile
	ProbeSessionID                          string
	TargetIsolationIdentityDigest           string
	CredentialIngressEndpointIdentityDigest string
	CandidateExecutable                     ExecutableIdentityV1
	CandidateExecutableIdentityDigest       string
	AuthorityNamespace                      string
	AuthorityGeneration                     uint64
	TrustRootGeneration                     uint64
	ConfigDigest                            string
	EvidenceDigest                          string
	FenceDigest                             string
	HostIdentityDigest                      string
	SuiteDigest                             string
	ProbeArtifactDigest                     string
	ChallengeDigest                         string
	ContractDigest                          string
	RequestEnvelopeDigest                   string
	ResponseEnvelopeDigest                  string
	IssuedAt                                time.Time
	ExpiresAt                               time.Time
}

// CodexLaunchRequestV1 is the exact ADR 0037 requestDigest projection. The
// Core, not this bridge, supplies the launch nonce and Attempt identities.
type CodexLaunchRequestV1 struct {
	AttemptID                 string `json:"attemptId"`
	AuthorityGeneration       uint64 `json:"authorityGeneration"`
	ArgvDigest                string `json:"argvDigest"`
	ConfigDigest              string `json:"configDigest"`
	ControlRootIdentityDigest string `json:"controlRootIdentityDigest"`
	EnvironmentDigest         string `json:"environmentDigest"`
	EvidenceDigest            string `json:"evidenceDigest"`
	FenceDigest               string `json:"fenceDigest"`
	LaunchNonce               string `json:"launchNonce"`
	RunID                     string `json:"runId"`
	TaskID                    string `json:"taskId"`
	TrustRootGeneration       uint64 `json:"trustRootGeneration"`
	WorktreeIdentityDigest    string `json:"worktreeIdentityDigest"`
}

type SealedMemfdIdentityV1 struct {
	DeviceMajor   uint64 `json:"deviceMajor"`
	DeviceMinor   uint64 `json:"deviceMinor"`
	Inode         uint64 `json:"inode"`
	MountIDUnique uint64 `json:"mountIdUnique"`
	Size          uint64 `json:"size"`
	SHA256        string `json:"sha256"`
	Seals         uint32 `json:"seals"`
}

type ChildExecIdentityV1 struct {
	PID                  uint32 `json:"pid"`
	StartTimeTicks       uint64 `json:"startTimeTicks"`
	PidfdInode           uint64 `json:"pidfdInode"`
	ProcExeDeviceMajor   uint64 `json:"procExeDeviceMajor"`
	ProcExeDeviceMinor   uint64 `json:"procExeDeviceMinor"`
	ProcExeInode         uint64 `json:"procExeInode"`
	ProcExeMountIDUnique uint64 `json:"procExeMountIdUnique"`
	ProcExeSize          uint64 `json:"procExeSize"`
	ProcExeSHA256        string `json:"procExeSha256"`
}

type CodexWorkerLaunchReceiptV1 struct {
	SchemaVersion                  string                `json:"schemaVersion"`
	AuthorityNamespace             string                `json:"authorityNamespace"`
	AuthorityGeneration            uint64                `json:"authorityGeneration"`
	TrustRootGeneration            uint64                `json:"trustRootGeneration"`
	TaskID                         string                `json:"taskId"`
	RunID                          string                `json:"runId"`
	AttemptID                      string                `json:"attemptId"`
	LaunchNonce                    string                `json:"launchNonce"`
	RequestDigest                  string                `json:"requestDigest"`
	LauncherBuildDigest            string                `json:"launcherBuildDigest"`
	LaunchKeyID                    string                `json:"launchKeyId"`
	ConfigDigest                   string                `json:"configDigest"`
	EvidenceDigest                 string                `json:"evidenceDigest"`
	FenceDigest                    string                `json:"fenceDigest"`
	HostIdentityDigest             string                `json:"hostIdentityDigest"`
	SourceExecutableIdentityDigest string                `json:"sourceExecutableIdentityDigest"`
	SealedMemfd                    SealedMemfdIdentityV1 `json:"sealedMemfd"`
	Child                          ChildExecIdentityV1   `json:"child"`
	ArgvDigest                     string                `json:"argvDigest"`
	EnvironmentDigest              string                `json:"environmentDigest"`
	PhaseDigests                   []string              `json:"phaseDigests"`
	RequestedAt                    string                `json:"requestedAt"`
	ExecObservedAt                 string                `json:"execObservedAt"`
	IssuedAt                       string                `json:"issuedAt"`
}

type CodexAPAPLaunchBinding struct {
	Session             CodexAPAPProbeSession
	Request             CodexLaunchRequestV1
	Receipt             CodexWorkerLaunchReceiptV1
	LaunchReceiptDigest string
}

type codexAPAPCurrent struct {
	material authorityProbeMaterial
	bundle   VerifiedAuthorityBundleV1
	fence    string
}

func loadCodexAPAPCurrent(ctx context.Context, authority CodexAPAPAuthority, now time.Time, nonceFence *HostAttestationNonceFence) (codexAPAPCurrent, error) {
	if ctx == nil || ctx.Err() != nil || now.IsZero() || authority.Source == nil || authority.ResponseKeys == nil || authority.LaunchKeys == nil || authority.ProviderSequence > maxSafeGeneration || !validID(authority.ProviderInstanceID) || authority.Peer.Role != authorityprovider.PrincipalVerifierController || !validDigest(authority.Peer.PrincipalDigest) || authority.CandidateExecutable.Version != codexAPAPVersion {
		return codexAPAPCurrent{}, errors.New("codex APAP authority identity is invalid")
	}
	if err := authority.CandidateExecutable.Validate(); err != nil {
		return codexAPAPCurrent{}, errors.New("codex APAP held executable identity is invalid")
	}
	material, err := authority.Source.LoadFreshAuthority(ctx)
	if err != nil || material.State.Validate() != nil {
		return codexAPAPCurrent{}, errors.New("codex APAP atomic authority is unavailable")
	}
	bundle, err := VerifyAuthorityBundle(now.UTC(), material.State.ActiveRootPin, material.KeysetEnvelope, material.ConfigEnvelope, material.EvidenceEnvelope, material.ObservationEnvelope, material.ReceiptEnvelopes, material.ExpectedHostNonce, material.HostVerifier, nonceFence)
	if err != nil || validateRecoveredAuthorityState(material, bundle) != nil {
		return codexAPAPCurrent{}, errors.New("codex APAP atomic authority is invalid")
	}
	identityDigest, err := canonicalDigest(authority.CandidateExecutable)
	if err != nil || identityDigest != bundle.Evidence.BinaryIdentityDigest || authority.CandidateExecutable != bundle.Observation.BinaryIdentity || bundle.Observation.BinaryIdentity.Version != codexAPAPVersion {
		return codexAPAPCurrent{}, errors.New("codex APAP held executable differs from evidence")
	}
	contract, err := compiledCodexContractBinding()
	if err != nil || !equalCodexContractBinding(bundle.Observation.Contract, contract) {
		return codexAPAPCurrent{}, errors.New("codex APAP contract binding is invalid")
	}
	validUntil, err := parseAuthorityTime(bundle.Evidence.ValidUntil)
	if err != nil || !now.UTC().Before(validUntil) || contains(bundle.Config.RevokedEvidenceDigests, material.EvidenceEnvelope.PayloadDigest) || contains(bundle.Config.RevokedSuiteDigests, bundle.Evidence.SuiteDigest) || contains(bundle.Config.RevokedChallengeDigests, bundle.Evidence.AggregateChallengeDigest) {
		return codexAPAPCurrent{}, errors.New("codex APAP evidence is expired or revoked")
	}
	fence, err := FenceDigest(material.State.Fence)
	if err != nil {
		return codexAPAPCurrent{}, errors.New("codex APAP fence digest is invalid")
	}
	return codexAPAPCurrent{material: material, bundle: bundle, fence: fence}, nil
}

func codexAPAPIdentityDigest(identity ExecutableIdentityV1) string {
	digest, _ := canonicalDigest(identity)
	return digest
}

func codexLaunchRequestDigest(request CodexLaunchRequestV1) (string, error) {
	if !validID(request.TaskID) || !validID(request.RunID) || !validID(request.AttemptID) || !validGeneration(request.AuthorityGeneration) || !validGeneration(request.TrustRootGeneration) {
		return "", errors.New("codex launch request identity is invalid")
	}
	if _, err := decodeNonce(request.LaunchNonce); err != nil {
		return "", err
	}
	for _, digest := range []string{request.ArgvDigest, request.ConfigDigest, request.ControlRootIdentityDigest, request.EnvironmentDigest, request.EvidenceDigest, request.FenceDigest, request.WorktreeIdentityDigest} {
		if !validDigest(digest) {
			return "", errors.New("codex launch request digest field is invalid")
		}
	}
	return canonicalDigest(request)
}

func decodeCodexLaunchReceipt(raw []byte) (CodexWorkerLaunchReceiptV1, error) {
	var receipt CodexWorkerLaunchReceiptV1
	if err := decodeClosed(raw, 64<<10, &receipt); err != nil {
		return receipt, err
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || string(canonicalRaw) != string(raw) {
		return receipt, errors.New("codex launch receipt is not canonical")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 24 {
		return receipt, errors.New("codex launch receipt is not closed")
	}
	if err := validateCodexLaunchReceiptShape(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func validateCodexLaunchReceiptShape(receipt CodexWorkerLaunchReceiptV1) error {
	if receipt.SchemaVersion != codexLaunchSchema || !validID(receipt.AuthorityNamespace) || !validGeneration(receipt.AuthorityGeneration) || !validGeneration(receipt.TrustRootGeneration) || !validID(receipt.TaskID) || !validID(receipt.RunID) || !validID(receipt.AttemptID) || !validID(receipt.LaunchKeyID) {
		return errors.New("codex launch receipt identity is invalid")
	}
	if _, err := decodeNonce(receipt.LaunchNonce); err != nil {
		return err
	}
	for _, digest := range []string{receipt.RequestDigest, receipt.LauncherBuildDigest, receipt.ConfigDigest, receipt.EvidenceDigest, receipt.FenceDigest, receipt.HostIdentityDigest, receipt.SourceExecutableIdentityDigest, receipt.SealedMemfd.SHA256, receipt.Child.ProcExeSHA256, receipt.ArgvDigest, receipt.EnvironmentDigest} {
		if !validDigest(digest) {
			return errors.New("codex launch receipt digest is invalid")
		}
	}
	if receipt.SealedMemfd.Inode == 0 || receipt.SealedMemfd.MountIDUnique == 0 || receipt.SealedMemfd.Size == 0 || receipt.SealedMemfd.Seals != codexRequiredMemfdSeals || receipt.Child.PID == 0 || receipt.Child.PID > 1<<31-1 || receipt.Child.StartTimeTicks == 0 || receipt.Child.PidfdInode == 0 || receipt.Child.ProcExeInode == 0 || receipt.Child.ProcExeMountIDUnique == 0 || receipt.Child.ProcExeSize == 0 || len(receipt.PhaseDigests) != 4 {
		return errors.New("codex launch receipt kernel identity is invalid")
	}
	for _, digest := range receipt.PhaseDigests {
		if !validDigest(digest) {
			return errors.New("codex launch receipt phase digest is invalid")
		}
	}
	return nil
}
