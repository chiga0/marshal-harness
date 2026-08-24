// Package qoder implements the bounded Qoder CLI Worker adapter.
package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID = "qoder"
	// adapterVersion is bumped to 0.1.8 for the Qoder 1.1.28 permission
	// drift fix (ADPT-03): the hardened argv now pre-authorizes the Bash
	// tool via --allowed-tools because 1.1.28 started asking a permission
	// question for Bash calls under accept_edits, which fatally refused the
	// WorkerResult tee in non-interactive runs.
	adapterVersion = "0.1.8"
	// supportedBinary is the minimum verified patch in the compatible 1.1.x
	// line. Other minor/major lines and older patches fail closed. The floor
	// moved from 1.1.23 to 1.1.27 with ADPT-03: the hardened argv now depends
	// on --allowed-tools, which was verified against real 1.1.27+ help and
	// cannot be confirmed for 1.1.23-1.1.26.
	supportedBinary      = "1.1.27"
	supportedBinaryRange = ">=1.1.27 <1.2.0"
	maxPromptBytes       = 256 << 10
	maxResultBytes       = 4 << 20
	stderrLimit          = 64 << 10
	versionOutputLimit   = 4 << 10
	versionStderrLimit   = 4 << 10
	probeTimeout         = 10 * time.Second
	// conformanceEventContract covers transport semantics (staging/tee
	// discipline, tool sequence validation, terminal outcome contract). It
	// remains at v7 after ADPT-02 because the system frame tolerance change
	// only affects the non-semantic system frame ignore policy, and after
	// ADPT-03 because the Bash pre-authorization changes only the argv tool
	// surface, which is bound by the argv and tool-policy digests inside the
	// probe suite digest; neither is part of the transport contract digest.
	conformanceEventContract = "qoder-stream-json-1.2.0-v7"
	qoderProtocolVersion     = "1.2.0"
	qoderPermissionMode      = "acceptEdits"
	qoderDenialExtractor     = "qoder-1.1.23-tool-result-metadata-v1"

	// conformancePendingReason is the fixed, searchable reason Probe reports
	// "unsupported" until an independent, credentialed live run verifies the
	// frozen 1.1.27+ argv and stream-json event contract. Hermetic fixtures and
	// unauthenticated help/version probes never flip this gate.
	conformancePendingReason = "credentialed live conformance pending: independent runner evidence is not bound to the Qoder CLI identity and stream-json contract"
)

var (
	ErrUnsupportedVersion       = errors.New("unsupported qoder version")
	ErrConformancePending       = errors.New("qoder live conformance is not bound")
	ErrIdentityDrift            = errors.New("qoder executable identity drift")
	ErrOutputLimit              = errors.New("qoder output limit exceeded")
	ErrProtocol                 = errors.New("invalid qoder protocol")
	ErrProcessFailed            = errors.New("qoder process failed")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")
	ErrUnsupportedWorkerTools   = errors.New("qoder worker tools allowlist unsupported")

	// qoderVersionPattern accepts exactly the bare semantic version the real
	// Qoder CLI reports from `--version`: a single three-component numeric
	// dot-separated string with no leading zeros, no prefix, and no pre-release
	// or build metadata. Any other shape fails closed rather than being
	// silently mis-normalized. The real CLI prints the bare version (e.g.
	// `1.1.23`), not a `qodercli <semver>` tool line.
	qoderVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

type Adapter struct {
	executable          string
	validator           *contract.Validator
	now                 func() time.Time
	ordinaryUserMode    bool
	authorityConfigPath string
	authorityFenceRoot  string
	beforeLaunchGuard   func()

	mu                           sync.Mutex
	pinned                       *executableIdentity
	conformance                  *boundConformance
	authorityGenerationHighWater uint64
	authorityConfigHighWater     string
}

var _ port.WorkerAdapter = (*Adapter)(nil)

// New requires an exact absolute executable path. Marshal never resolves a
// provider executable by a similar name or by an implicit fallback.
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	if validator == nil {
		return nil, errors.New("contract validator is required")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, errors.New("qoder executable must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve qoder executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat qoder executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("qoder executable must be an executable regular file")
	}
	return &Adapter{executable: realPath, validator: validator, now: time.Now}, nil
}

// NewOrdinaryUser enables the explicit Mac ordinary-user mode. It keeps the
// same path, version and identity checks as New, but does not claim signed
// authority or APAP credentials; callers must opt in via the Worker registry.
func NewOrdinaryUser(executable string, validator *contract.Validator) (*Adapter, error) {
	adapter, err := New(executable, validator)
	if err != nil {
		return nil, err
	}
	adapter.ordinaryUserMode = true
	return adapter, nil
}

func (a *Adapter) ID() string { return adapterID }

// Probe pins the executable identity and reports a CapabilitySnapshot. It is
// fail-closed: probeStatus is "unsupported" until a real Qoder CLI live
// conformance verifies the exact non-interactive argv and JSONL event
// contract, so an exact version match alone never authorizes the adapter.
// Probe never launches a Worker attempt.
func (a *Adapter) Probe(ctx context.Context) (domain.Record, error) {
	identity, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, err
	}
	a.pinIdentity(identity)
	probeErrors := []string{}
	if !isSupportedBinaryVersion(identity.version) {
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Qoder %s，实际为 %s", supportedBinaryRange, identity.version))
	}
	status := "unsupported"
	if a.ordinaryUserMode && isSupportedBinaryVersion(identity.version) {
		status = "supported"
		probeErrors = []string{}
	} else if !a.isConformant(identity) {
		probeErrors = append(probeErrors, conformancePendingReason)
	} else if isSupportedBinaryVersion(identity.version) {
		status = "supported"
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": a.capabilities(),
		"probeErrors":  probeErrors, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
	}
	if a.ordinaryUserMode {
		snapshot["authorityMode"] = "ordinary-user"
	}
	if status == "supported" && !a.ordinaryUserMode {
		a.mu.Lock()
		bound := *a.conformance
		a.mu.Unlock()
		snapshot["conformanceEvidenceDigest"] = bound.evidenceDigest
		snapshot["conformanceTrustRootKeyId"] = bound.trustRootKeyID
		snapshot["conformanceProbeProfileDigest"] = bound.probeProfileDigest
		snapshot["conformanceValidUntil"] = bound.validUntil.UTC().Format(time.RFC3339Nano)
		snapshot["conformanceHostFingerprint"] = bound.hostFingerprint
		snapshot["conformanceAuthorityGeneration"] = bound.authorityGeneration
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return domain.Record{}, err
	}
	if err := a.validator.Validate(domain.KindCapabilitySnapshot, data); err != nil {
		return domain.Record{}, fmt.Errorf("validate CapabilitySnapshot: %w", err)
	}
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: data}, nil
}

func (a *Adapter) capabilities() map[string]any {
	capabilities := expectedCapabilities()
	if a.ordinaryUserMode {
		capabilities["notes"] = []string{
			"由 Marshal 实施 wall-time 与 output-bytes 上限。",
			"Qoder 非交互模式不是恶意代码隔离边界。",
			"当前为 ordinary-user：无签名 authority、APAP 凭据或恶意代码沙箱保证。",
			"ordinary-user 仅继承 allowlist 中的宿主 HOME/XDG 以复用现有登录；不绑定 Marshal managed config，也不禁用宿主账户配置源。",
			"仅在 executable realpath、digest、version 与显式 ordinary-user mode 均匹配时声明 supported。",
		}
	}
	return capabilities
}

func expectedCapabilities() map[string]any {
	return map[string]any{
		"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
		"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
		"executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{},
		"processTreeCancellation": true,
		"notes": []string{
			"由 Marshal 实施 wall-time 与 output-bytes 上限。",
			"Qoder 非交互模式不是恶意代码隔离边界。",
			"执行环境被完整替换：HOME 绑定 Marshal 管理的独立 config dir，user/project/local setting sources 被禁用。",
			"仅当 live conformance 记录与当前 realpath、digest、version 精确一致时才声明 supported。",
		},
	}
}

func expectedCapabilitiesDigest() string {
	data, _ := json.Marshal(expectedCapabilities())
	return digestBytes(data)
}

func expectedProbeProfileDigest() string {
	return probeProfileDigestForEventContract(conformanceEventContract)
}

func probeProfileDigestForEventContract(eventContract string) string {
	profile := map[string]any{
		"ambientCredentialInheritance": false,
		"businessRepositoryAccess":     false,
		"eventContract":                eventContract,
		"isolatedWorkingDirectory":     true,
		"permissionMode":               qoderPermissionMode,
		"repositoryWritePermission":    true,
		"settingSources":               []string{},
		"writableRoots":                []string{"$ISOLATED_WORKTREE"},
	}
	data, _ := json.Marshal(profile)
	return digestBytes(data)
}

func expectedProbeArgvDigest() string {
	data, _ := json.Marshal(expectedProbeArgvVariants())
	return digestBytes(data)
}

func expectedProbeArgvVariants() [][]string {
	const configDir = "$ISOLATED_CONFIG_DIR"
	const worktree = "$ISOLATED_WORKTREE"
	return [][]string{
		buildArgs("", configDir, worktree, false),
		buildArgs("$MODEL", configDir, worktree, false),
		buildArgs("", configDir, worktree, true),
		buildArgs("$MODEL", configDir, worktree, true),
	}
}

func expectedProbeEnvironmentDigest() string {
	data, _ := json.Marshal(candidateProbeEnvironment("$ISOLATED_WORKTREE", "$ISOLATED_CONFIG_DIR"))
	return digestBytes(data)
}

func expectedProbeToolPolicyDigest() string {
	data, _ := json.Marshal(map[string]any{"namedWorkerTools": []string{}, "providerAllowedTools": []string{"Bash"}, "providerPermissionMode": qoderPermissionMode, "providerDisallowedTools": []string{"Agent"}, "repositoryScope": "isolated-scratch-worktree"})
	return digestBytes(data)
}

// workerResultTransportContract is the closed, deterministic description of
// the authority-bearing WorkerResult transport. Every security-relevant
// field is scalar so a contract mutation cannot disappear through ordering or
// normalization. The live observation, each execution receipt and both
// evidence consumers bind its digest; changing any field therefore requires
// a new Adapter/event contract and fresh live conformance.
type workerResultTransportContract struct {
	StagingBasename                     string `json:"stagingBasename"`
	StagingFileType                     string `json:"stagingFileType"`
	StagingMode                         string `json:"stagingMode"`
	CreationFlags                       string `json:"creationFlags"`
	UnlinkBeforeLaunch                  bool   `json:"unlinkBeforeLaunch"`
	UnlinkedLinkCount                   uint64 `json:"unlinkedLinkCount"`
	WorkerPathExposure                  string `json:"workerPathExposure"`
	WorkerDescriptorExposure            string `json:"workerDescriptorExposure"`
	ControlInodeRelationship            string `json:"controlInodeRelationship"`
	HeldDirectoryBinding                string `json:"heldDirectoryBinding"`
	HeldInodeCommit                     string `json:"heldInodeCommit"`
	HeldInodeConsume                    string `json:"heldInodeConsume"`
	HeldInodeCleanup                    string `json:"heldInodeCleanup"`
	ToolName                            string `json:"toolName"`
	ToolInputContract                   string `json:"toolInputContract"`
	ToolInputDescriptionRequired        bool   `json:"toolInputDescriptionRequired"`
	ToolInputDescriptionAuthority       string `json:"toolInputDescriptionAuthority"`
	ToolInputDescriptionMinBytes        uint64 `json:"toolInputDescriptionMinBytes"`
	ToolInputDescriptionMaxBytes        uint64 `json:"toolInputDescriptionMaxBytes"`
	ToolInputDescriptionUTF8Required    bool   `json:"toolInputDescriptionUtf8Required"`
	ToolInputDescriptionControls        string `json:"toolInputDescriptionControls"`
	ToolInputCanonicalMemberOrder       string `json:"toolInputCanonicalMemberOrder"`
	ToolInputUnknownMembers             string `json:"toolInputUnknownMembers"`
	CanonicalCommand                    string `json:"canonicalCommand"`
	TeeSequence                         string `json:"teeSequence"`
	DeclarationRuntimeMetadataAuthority string `json:"declarationRuntimeMetadataAuthority"`
	DeclarationSemanticSynthesis        string `json:"declarationSemanticSynthesis"`
	DeclarationIdentityBinding          string `json:"declarationIdentityBinding"`
	InvalidDeclarationDisposition       string `json:"invalidDeclarationDisposition"`
	DenialExtractor                     string `json:"denialExtractor"`
	TranscriptEventContract             string `json:"transcriptEventContract"`
}

func expectedWorkerResultTransportContract() workerResultTransportContract {
	return workerResultTransportContract{
		StagingBasename:                     workerResultStagingName,
		StagingFileType:                     "regular-file",
		StagingMode:                         "0600",
		CreationFlags:                       "O_RDWR|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC",
		UnlinkBeforeLaunch:                  true,
		UnlinkedLinkCount:                   0,
		WorkerPathExposure:                  "none",
		WorkerDescriptorExposure:            "none",
		ControlInodeRelationship:            "must-be-distinct",
		HeldDirectoryBinding:                "held-dirfd-exact-worktree-no-symlink",
		HeldInodeCommit:                     "post-terminal-post-tee-last-exact-inode",
		HeldInodeConsume:                    "held-fd-bounded-exact-inode",
		HeldInodeCleanup:                    "close-already-unlinked-held-fd-and-dirfd",
		ToolName:                            "Bash",
		ToolInputContract:                   "canonical-json-command-plus-bounded-description-no-extra-members",
		ToolInputDescriptionRequired:        true,
		ToolInputDescriptionAuthority:       "non-authoritative",
		ToolInputDescriptionMinBytes:        qoderBashDescriptionMinBytes,
		ToolInputDescriptionMaxBytes:        qoderBashDescriptionMaxBytes,
		ToolInputDescriptionUTF8Required:    true,
		ToolInputDescriptionControls:        "forbidden",
		ToolInputCanonicalMemberOrder:       "command,description",
		ToolInputUnknownMembers:             "forbidden",
		CanonicalCommand:                    workerResultTeeFirstLine + "\n<CANONICAL_WORKER_RESULT_JSON>\nMARSHAL_RESULT (optional one final LF)",
		TeeSequence:                         "exactly-one-successful-tee-as-final-tool-call",
		DeclarationRuntimeMetadataAuthority: "adapter-overwrites-adapter.executable-and-adapter.version-from-held-identity-before-schema",
		DeclarationSemanticSynthesis:        "forbidden",
		DeclarationIdentityBinding:          "taskId,runId,attemptId,adapter.id=exact-declaration-match",
		InvalidDeclarationDisposition:       "protocol-invalid-do-not-retry",
		DenialExtractor:                     qoderDenialExtractor,
		TranscriptEventContract:             conformanceEventContract,
	}
}

func workerResultTransportContractDigest(contract workerResultTransportContract) string {
	data, _ := json.Marshal(contract)
	return digestBytes(data)
}

func expectedWorkerResultTransportDigest() string {
	return workerResultTransportContractDigest(expectedWorkerResultTransportContract())
}

func expectedProbeSuiteDigest() string {
	return probeSuiteDigestForWorkerResultTransport(expectedWorkerResultTransportDigest())
}

func probeSuiteDigestForWorkerResultTransport(transportDigest string) string {
	return probeSuiteDigestForIdentity(adapterVersion, conformanceEventContract, expectedProbeProfileDigest(), transportDigest)
}

func probeSuiteDigestForIdentity(adapterVersionValue, eventContract, profileDigest, transportDigest string) string {
	data, _ := json.Marshal(map[string]any{"adapterId": adapterID, "adapterVersion": adapterVersionValue, "argvDigest": expectedProbeArgvDigest(), "environmentDigest": expectedProbeEnvironmentDigest(), "profileDigest": profileDigest, "toolPolicyDigest": expectedProbeToolPolicyDigest(), "workerResultTransportDigest": transportDigest, "eventContract": eventContract, "protocolVersion": qoderProtocolVersion})
	return digestBytes(data)
}

func currentHostFingerprint() (string, error) {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "", errors.New("qoder host identity is unavailable")
	}
	data, _ := json.Marshal(map[string]string{"hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH})
	return digestBytes(data), nil
}

// CurrentHostFingerprint returns the non-secret target-host binding used by
// the external verifier and the production consumer.
func CurrentHostFingerprint() (string, error) { return currentHostFingerprint() }

type executableIdentity struct{ path, digest, version string }

// boundConformance retains the authority evidence freshness boundary as well
// as the executable identity. Keeping only the identity would turn a
// time-limited conformance statement into permanent admission after one
// successful candidate authority validation.
type boundConformance struct {
	identity            executableIdentity
	evidenceDigest      string
	validUntil          time.Time
	trustRootKeyID      string
	probeProfileDigest  string
	hostFingerprint     string
	authorityGeneration uint64
}

func (a *Adapter) pinIdentity(identity executableIdentity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conformance != nil {
		return
	}
	pinned := identity
	a.pinned = &pinned
}

func (a *Adapter) isConformant(identity executableIdentity) bool {
	return a.revalidateConformance(context.Background(), identity) == nil
}

// hasConformanceCandidate reports whether Run has any authority state that
// could admit an exact executable identity. When neither bound evidence nor a
// configured authority exists, version inspection cannot change the decision;
// reject before spawning the short-lived --version probe. Besides avoiding
// unnecessary execution, this makes the permanent fail-closed result
// independent of a fast child's process-group acquisition timing.
func (a *Adapter) hasConformanceCandidate() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ordinaryUserMode || a.conformance != nil || a.authorityConfigPath != ""
}

func (a *Adapter) refreshConfiguredConformance(ctx context.Context) error {
	if a.authorityConfigPath == "" {
		return port.Permanent(ErrConformancePending)
	}
	config, err := loadAuthorityConfig(a.authorityConfigPath)
	if err != nil {
		return port.Permanent(ErrConformancePending)
	}
	store, err := authorityFromConfig(config)
	if err != nil {
		return port.Permanent(ErrConformancePending)
	}
	defer store.Close()
	if err := a.observeAuthorityConfig(config, store.directory); err != nil {
		return err
	}
	if authorityConfigRevokes(config, config.EvidenceDigest) {
		return port.Permanent(ErrConformancePending)
	}
	evidence, err := store.resolve(ctx, config.EvidenceDigest, a.now().UTC())
	if err != nil || evidence.ProbeArtifactDigest != config.ProbeArtifactDigest || evidence.ChallengeDigest != config.ChallengeDigest || evidence.AuthorityGeneration != config.AuthorityGeneration {
		return port.Permanent(ErrConformancePending)
	}
	identity, err := a.inspect(ctx)
	if err != nil {
		return err
	}
	return a.bindVerifiedConformance(identity, evidence, config, store.directory)
}

func (a *Adapter) bindVerifiedConformance(identity executableIdentity, evidence ConformanceEvidence, config AuthorityConfig, evidenceDirectory *os.File) error {
	if !isSupportedBinaryVersion(identity.version) {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	hostFingerprint, err := currentHostFingerprint()
	if err != nil {
		return err
	}
	if evidence.Executable != identity.path || evidence.ExecutableDigest != identity.digest || evidence.BinaryVersion != identity.version || evidence.QoderCLIVersion != identity.version || evidence.HostFingerprint != hostFingerprint {
		return fmt.Errorf("%w: conformance identity does not match current executable or host", ErrIdentityDrift)
	}
	validUntil, err := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if err != nil {
		return port.Permanent(ErrConformancePending)
	}
	if err := a.observeAuthorityConfig(config, evidenceDirectory); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	pinned := identity
	a.pinned = &pinned
	a.conformance = &boundConformance{identity: identity, evidenceDigest: evidence.EvidenceDigest, validUntil: validUntil, trustRootKeyID: evidence.TrustRootKeyID, probeProfileDigest: evidence.ProbeProfileDigest, hostFingerprint: evidence.HostFingerprint, authorityGeneration: evidence.AuthorityGeneration}
	return nil
}

func authorityConfigRevokes(config AuthorityConfig, evidenceDigest string) bool {
	for _, revoked := range config.RevokedEvidenceDigests {
		if revoked == evidenceDigest {
			return true
		}
	}
	return false
}

// observeAuthorityConfig consumes the generation before resolving its leaf.
// A trusted newer config therefore cannot be rolled back merely because its
// selected evidence is revoked, missing, or not yet readable.
func (a *Adapter) observeAuthorityConfig(config AuthorityConfig, evidenceDirectory *os.File) error {
	configDigest, err := authorityConfigIdentity(config)
	if err != nil {
		return port.Permanent(ErrConformancePending)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if config.AuthorityGeneration < a.authorityGenerationHighWater {
		return port.Permanent(ErrConformancePending)
	}
	if config.AuthorityGeneration == a.authorityGenerationHighWater && a.authorityGenerationHighWater != 0 && configDigest != a.authorityConfigHighWater {
		return port.Permanent(ErrConformancePending)
	}
	// Configured authority admission must never fall back to a process-local
	// high-water. The consumer-owned fence is committed before resolving the
	// selected evidence, so restart cannot revive an older generation after a
	// newer config selected missing or revoked evidence.
	if a.authorityConfigPath != "" {
		if a.authorityFenceRoot == "" {
			return port.Permanent(ErrConformancePending)
		}
		if err := consumeAuthorityGenerationSeparated(a.authorityFenceRoot, evidenceDirectory, config.AuthorityGeneration, configDigest); err != nil {
			return port.Permanent(ErrConformancePending)
		}
	}
	a.authorityGenerationHighWater = config.AuthorityGeneration
	a.authorityConfigHighWater = configDigest
	return nil
}

func (a *Adapter) revalidateConformance(ctx context.Context, identity executableIdentity) error {
	if a.authorityConfigPath != "" {
		if err := a.refreshConfiguredConformance(ctx); err != nil {
			return err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conformance == nil || a.conformance.identity != identity || !a.now().UTC().Before(a.conformance.validUntil) {
		return port.Permanent(ErrConformancePending)
	}
	return nil
}

func (a *Adapter) verifyExecutionIdentity(identity executableIdentity) error {
	a.mu.Lock()
	if a.pinned != nil && *a.pinned != identity {
		a.mu.Unlock()
		return fmt.Errorf("%w: executable changed after capability probe", ErrIdentityDrift)
	}
	a.mu.Unlock()
	if a.ordinaryUserMode {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.pinned == nil {
			pinned := identity
			a.pinned = &pinned
		}
		if *a.pinned != identity {
			return fmt.Errorf("%w: executable changed after capability probe", ErrIdentityDrift)
		}
		return nil
	}
	if err := a.revalidateConformance(context.Background(), identity); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pinned == nil {
		pinned := identity
		a.pinned = &pinned
	}
	if *a.pinned != identity {
		return fmt.Errorf("%w: executable changed after capability probe", ErrIdentityDrift)
	}
	if a.conformance == nil || a.conformance.identity != identity || !a.now().UTC().Before(a.conformance.validUntil) {
		return port.Permanent(ErrConformancePending)
	}
	return nil
}

// inspect pins the executable identity through realpath and SHA256 and reads
// the binary version for the patch-compatible gate applied by Probe and Run.
func (a *Adapter) inspect(ctx context.Context) (executableIdentity, error) {
	info, err := os.Stat(a.executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return executableIdentity{}, errors.New("configured qoder executable is unavailable")
	}
	digest, err := digestFile(a.executable)
	if err != nil {
		return executableIdentity{}, err
	}
	version, err := readBinaryVersion(ctx, a.executable)
	if err != nil {
		return executableIdentity{}, err
	}
	confirmedDigest, err := digestFile(a.executable)
	if err != nil {
		return executableIdentity{}, err
	}
	if confirmedDigest != digest {
		return executableIdentity{}, fmt.Errorf("%w: executable changed during identity inspection", ErrIdentityDrift)
	}
	return executableIdentity{a.executable, digest, version}, nil
}

// readBinaryVersion runs `<executable> --version` inside the sanitized probe
// environment and parses the bare version string reported by the binary.
func readBinaryVersion(ctx context.Context, executable string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	probeRoot, err := os.MkdirTemp("", "marshal-qoder-probe-")
	if err != nil {
		return "", fmt.Errorf("create qoder probe root: %w", err)
	}
	defer os.RemoveAll(probeRoot)
	if err := os.Chmod(probeRoot, 0o700); err != nil {
		return "", fmt.Errorf("lock qoder probe root: %w", err)
	}
	configDir := filepath.Join(probeRoot, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		return "", fmt.Errorf("create qoder probe config: %w", err)
	}
	output, err := runBoundedVersionProbe(probeCtx, executable, configDir, probeEnvironment(probeRoot))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if probeCtx.Err() != nil {
			return "", fmt.Errorf("probe qoder version: timed out after %s", probeTimeout)
		}
		return "", fmt.Errorf("probe qoder version: %w", err)
	}
	return parseQoderVersion(string(output))
}

// parseQoderVersion normalizes the real Qoder `--version` output into the
// bare semantic version. The real CLI prints only the bare version (e.g.
// `1.1.23`), so any tool prefix, extra field, "v" prefix, pre-release or
// build metadata fails closed. The exact supported-version gates in Probe and
// Run only ever compare bare semantic versions.
func parseQoderVersion(output string) (string, error) {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", errors.New("qoder returned an empty version")
	}
	if len(fields) != 1 {
		return "", errors.New("qoder returned an unrecognized version")
	}
	version := fields[0]
	if !qoderVersionPattern.MatchString(version) {
		return "", errors.New("qoder returned a malformed version")
	}
	return version, nil
}

func isSupportedBinaryVersion(version string) bool {
	if !qoderVersionPattern.MatchString(version) {
		return false
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major == 1 && minor == 1 && patch >= 27
}

// Identify pins the version and SHA256 digest of an absolute candidate
// executable, reusing the probe's sanitized environment and version parsing.
// It is advisory identity collection shared by doctor discovery and future
// tooling; it never registers the adapter, writes files, or touches Marshal
// state.
func Identify(executable string) (version, digest string, err error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", "", errors.New("qoder candidate must be an absolute clean path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("qoder candidate is not an executable regular file")
	}
	digest, err = digestFile(executable)
	if err != nil {
		return "", "", err
	}
	version, err = readBinaryVersion(context.Background(), executable)
	if err != nil {
		return "", "", err
	}
	return version, digest, nil
}

type workerRequest struct {
	TaskID, RunID, AttemptID                                        string
	WorktreePath, ControlRoot, TaskSpecPath, PromptPath, ResultPath string
	AdapterID, ExecutionProfile, SessionPolicy                      string
	SessionID                                                       string
	AttemptTimeoutSeconds, MaxOutputBytes                           int
}

func decodeRequest(data []byte, validator *contract.Validator) (workerRequest, error) {
	if err := validator.Validate(domain.KindWorkerRequest, data); err != nil {
		return workerRequest{}, fmt.Errorf("validate WorkerRequest: %w", err)
	}
	var raw struct {
		TaskID                string `json:"taskId"`
		RunID                 string `json:"runId"`
		AttemptID             string `json:"attemptId"`
		WorktreePath          string `json:"worktreePath"`
		ControlRoot           string `json:"controlRoot"`
		TaskSpecPath          string `json:"taskSpecPath"`
		PromptPath            string `json:"promptPath"`
		ResultPath            string `json:"resultPath"`
		AdapterID             string `json:"adapterId"`
		ExecutionProfile      string `json:"executionProfile"`
		SessionPolicy         string `json:"sessionPolicy"`
		SessionID             string `json:"sessionId"`
		AttemptTimeoutSeconds int    `json:"attemptTimeoutSeconds"`
		MaxOutputBytes        int    `json:"maxOutputBytes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return workerRequest{}, err
	}
	return workerRequest(raw), nil
}

// Run executes one non-interactive attempt as the composition
// inspect -> bind managed config -> local exec -> normalize.
// Provider/process/protocol failures are returned as errors so Core can apply
// the operational retry budget.
func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindWorkerRequest {
		return domain.Record{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	if request.AdapterID != adapterID || request.ExecutionProfile != "workspace-write" {
		return domain.Record{}, errors.New("WorkerRequest does not match the qoder adapter execution profile")
	}
	// Fail-closed: persist would write outside the managed state boundary and
	// WorkerRequest carries no managed sessionDir/mapping, so cross-attempt
	// resume cannot be done safely. Never launch a process for it.
	if request.SessionPolicy != "ephemeral" {
		return domain.Record{}, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
	}
	if !a.hasConformanceCandidate() {
		return domain.Record{}, port.Permanent(ErrConformancePending)
	}
	identity, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, err
	}
	if !isSupportedBinaryVersion(identity.version) {
		return domain.Record{}, fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	if err := a.verifyExecutionIdentity(identity); err != nil {
		return domain.Record{}, err
	}
	launchExecutable, cleanupExecutable, err := snapshotExecutable(ctx, identity)
	if err != nil {
		return domain.Record{}, err
	}
	defer cleanupExecutable()
	worktree, err := filepath.EvalSymlinks(request.WorktreePath)
	if err != nil {
		return domain.Record{}, fmt.Errorf("resolve worktree: %w", err)
	}
	if !filepath.IsAbs(worktree) {
		return domain.Record{}, errors.New("worktree path must be absolute")
	}
	controlRoot, err := filepath.EvalSymlinks(request.ControlRoot)
	if err != nil || !filepath.IsAbs(controlRoot) {
		return domain.Record{}, errors.New("control root must be an existing absolute directory")
	}
	prompt, err := readBoundedWithin(controlRoot, request.PromptPath, maxPromptBytes)
	if err != nil {
		return domain.Record{}, fmt.Errorf("read prompt: %w", err)
	}
	resultPath, err := lexicalPathWithin(controlRoot, request.ResultPath)
	if err != nil {
		return domain.Record{}, fmt.Errorf("resolve result: %w", err)
	}
	outputDir, err := preparePrivateDirectory(controlRoot, filepath.Dir(resultPath))
	if err != nil {
		return domain.Record{}, err
	}
	resultPath = filepath.Join(outputDir, filepath.Base(resultPath))
	// Bind the Marshal-managed, isolated config dir before launching anything:
	// user/project/local settings must never influence the attempt, and a
	// symlink, escape, or abnormal permission must fail closed up front.
	// Ordinary-user mode skips the managed dir and leaves configDir empty so
	// the ambient account config (e.g. an existing login) can be used; the
	// hardened mode is unchanged.
	var configDir string
	if !a.ordinaryUserMode {
		configDir, err = managedConfigDir(controlRoot)
		if err != nil {
			return domain.Record{}, err
		}
	}
	task, err := readTaskProjection(controlRoot, request.TaskSpecPath)
	if err != nil {
		if errors.Is(err, ErrUnsupportedWorkerTools) {
			return domain.Record{}, fmt.Errorf("%w: %w", qoderProtocolInvalid("named worker tools unsupported", a.now()), ErrUnsupportedWorkerTools)
		}
		return domain.Record{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(request.AttemptTimeoutSeconds)*time.Second)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return domain.Record{}, err
	}
	trustedOutput, err := openTrustedOutputDirectory(outputDir)
	if err != nil {
		return domain.Record{}, err
	}
	defer trustedOutput.close()
	claim := func(name, kind string) (*claimedLeaf, error) { return trustedOutput.claim(name, kind) }
	claimedResult, err := claim(filepath.Base(resultPath), "result")
	if err != nil {
		return domain.Record{}, err
	}
	defer claimedResult.close()
	resultTransport, err := bindWorkerResultTransport(worktree)
	if err != nil {
		return domain.Record{}, qoderProtocolInvalid("worker result staging admission failed", a.now())
	}
	defer resultTransport.close()
	transcriptLeaf, err := claim("qoder-transcript.jsonl", "transcript")
	if err != nil {
		return domain.Record{}, err
	}
	defer transcriptLeaf.close()
	stderrLeaf, err := claim("qoder-stderr.log", "stderr")
	if err != nil {
		return domain.Record{}, err
	}
	defer stderrLeaf.close()
	metadataLeaf, err := claim("qoder-transcript-meta.json", "metadata")
	if err != nil {
		return domain.Record{}, err
	}
	defer metadataLeaf.close()
	if err := trustedOutput.dir.Sync(); err != nil {
		return domain.Record{}, err
	}
	// Recheck authority admission at the launch boundary. Identity inspection,
	// executable snapshotting and input/output preparation can take long enough
	// for a short-lived conformance statement to expire; a Run admitted earlier
	// must never launch after that authority window closes.
	if a.beforeLaunchGuard != nil {
		a.beforeLaunchGuard()
	}
	launchGuard := func() error { return a.verifyExecutionIdentity(identity) }
	observation, err := a.runLocalAttempt(runCtx, launchExecutable, buildArgs(task.model, configDir, worktree, task.disableAllTools), prompt, worktree, workerEnvironment(worktree, configDir), int64(request.MaxOutputBytes), launchGuard)
	if err != nil {
		return domain.Record{}, err
	}
	capture := observation.capture
	if err := transcriptLeaf.write(capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := stderrLeaf.write(observation.stderr.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	denialRecords := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: worktree, ControlRoot: controlRoot, TempDir: os.TempDir()}, capture.denials, a.now)
	// The reviewed result command begins with `cat`, so the shared generic
	// execute-denial classifier would otherwise treat it as read-only
	// introspection. Qoder's staging declaration is an execute/write transport:
	// denial is always fatal and must never be retried as a benign probe.
	for index := range denialRecords {
		if access, _, _ := classifyWorkerResultTransportTool(capture.denials[index].Tool, capture.denials[index].Input); access {
			denialRecords[index].Grade = string(denials.Fatal)
			denialRecords[index].Kind = string(denials.KindExecute)
			denialRecords[index].Reason = "Qoder WorkerResult transport 拒绝一律 FATAL"
		}
	}
	fatalDenials := denials.CountFatal(denialRecords)
	if len(denialRecords) > 0 {
		denialLeaf, claimErr := claim(denials.LogFileName, "denial log")
		if claimErr != nil {
			return domain.Record{}, qoderProtocolInvalid("denial log claim failed", a.now())
		}
		denialData, encodeErr := encodeDenialLog(denialRecords)
		writeErr := error(nil)
		if encodeErr == nil {
			writeErr = denialLeaf.write(denialData)
		}
		closeErr := denialLeaf.close()
		if err := errors.Join(encodeErr, writeErr, closeErr); err != nil {
			return domain.Record{}, qoderProtocolInvalid("denial log persistence failed", a.now())
		}
	}
	resolved := resolveAttemptFailure(capture, observation, runCtx, fatalDenials, a.now())
	// A post-tee tool call or repeated/invalid declaration is structural even
	// when Qoder also reports a provider/process failure. Cancellation and
	// bounded-output truncation retain their explicit outer-bound precedence;
	// malformed transcript already resolves to the same typed protocol class.
	if runCtx.Err() == nil && !capture.limitExceeded && capture.err == nil && workerResultTransportSequenceViolation(capture) {
		resolved = qoderProtocolInvalid("worker result transport tool sequence is invalid", a.now())
	}
	if resolved == nil && validateWorkerResultTransportSequence(capture) != nil {
		resolved = qoderProtocolInvalid("worker result transport tool sequence is invalid", a.now())
	}
	if resolved == nil && (capture.cliVersion != identity.version || capture.protocolVersion != qoderProtocolVersion || capture.permissionMode != qoderPermissionMode) {
		resolved = qoderProtocolInvalid("system contract does not match the bound Qoder protocol", a.now())
	}
	// Only the Adapter commits the declaration payload, and only after the
	// transcript, terminal contract and tee-last sequence all pass. The Worker
	// never receives the unlinked held inode or any pathname that names it.
	if resolved == nil {
		if commitErr := resultTransport.commit(capture.resultTransport.payload, int64(maxResultBytes)); commitErr != nil {
			resolved = qoderProtocolInvalid("worker result staging identity or content is invalid", a.now())
		}
	}
	stagedResult, transportErr := resultTransport.consume(int64(maxResultBytes))
	// Cancellation and output truncation are the explicit outer bounds. A
	// simultaneous staging anomaly stays recorded by cleanup but does not
	// replace those outcomes; every other transport anomaly is a permanent
	// protocol-integrity failure.
	if transportErr != nil && runCtx.Err() == nil && !capture.limitExceeded {
		resolved = qoderProtocolInvalid("worker result staging identity or content is invalid", a.now())
	}
	if bindingErr := trustedOutput.verifyPathBinding(); bindingErr != nil {
		resolved = qoderProtocolInvalid("output directory binding changed during execution", a.now())
	}
	var declared declaredResult
	if resolved == nil {
		declared, resolved = resolveDeclaredResultData(stagedResult, request, capture.sessionID, identity.path, identity.version, a.validator, a.now())
	}
	if resolved == nil && task.model != "" && capture.model != task.model {
		resolved = qoderProtocolInvalid("system model does not match requested model", a.now())
	}
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "model": capture.model,
		"qodercliVersion": capture.cliVersion, "protocolVersion": capture.protocolVersion, "permissionMode": capture.permissionMode,
		"eventCount": capture.eventCount, "assistantMessages": capture.assistantCount, "toolCalls": capture.toolCalls,
		"workerResultTeeAttempts": capture.resultTransport.attempts, "workerResultTeeSuccesses": capture.resultTransport.successes,
		"workerResultTeeLast": capture.resultTransport.attempts == 1 && capture.resultTransport.successes == 1 && capture.resultTransport.successfulOrdinal == capture.toolCalls && !capture.resultTransport.invalidAccess,
		"inputTokens":         capture.inputTokens, "outputTokens": capture.outputTokens,
		"capturedBytes": len(capture.raw), "outputTruncated": capture.limitExceeded,
		"permissionDenied": fatalDenials > 0,
		"denialsBenign":    len(denialRecords) - fatalDenials, "denialsFatal": fatalDenials,
		"toolNames": denials.SortedToolNames(capture.toolNames),
		"exitCode":  observation.exitCode, "signal": observation.signal,
		"stderrBytes": len(observation.stderr.data), "stderrTruncated": observation.stderr.truncated,
		"contextError": contextError(runCtx),
		"failureKind":  failureKindOf(resolved), "retryDisposition": retryDispositionOf(resolved),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, err
	}
	if err := metadataLeaf.write(append(metadata, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript metadata: %w", err)
	}
	if resolved != nil {
		return domain.Record{}, resolved
	}
	declared.Adapter.Executable, declared.Adapter.Version = identity.path, identity.version
	declared.Session = &declaredSession{ID: capture.sessionID, Resumable: false}
	declared.StartedAt, declared.CompletedAt = observation.startedAt, observation.completedAt
	declared.Adapter.Model = capture.model
	if capture.inputTokens > 0 || capture.outputTokens > 0 {
		usage := map[string]any{"inputTokens": capture.inputTokens, "outputTokens": capture.outputTokens}
		if usageData, err := json.Marshal(usage); err == nil {
			declared.Usage = usageData
		}
	}
	data, err := json.Marshal(declared)
	if err != nil {
		return domain.Record{}, err
	}
	if err := a.validator.Validate(domain.KindWorkerResult, data); err != nil {
		return domain.Record{}, fmt.Errorf("validate normalized WorkerResult: %w", err)
	}
	if err := claimedResult.write(append(data, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write normalized WorkerResult: %w", err)
	}
	if err := trustedOutput.verifyPathBinding(); err != nil {
		return domain.Record{}, qoderProtocolInvalid("output directory binding changed before result publication", a.now())
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, nil
}

func encodeDenialLog(records []denials.Record) ([]byte, error) {
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return data, nil
}

// resolveAttemptFailure orders terminal conditions before the WorkerResult is
// read. Context cancellation/deadline, output truncation, malformed protocol,
// a terminal provider failure, and process failure all fail closed in fixed
// precedence; a successful run must then carry a session id and a success
// terminal before the declaration is trusted.
func resolveAttemptFailure(capture captureResult, observation attemptObservation, runCtx context.Context, fatalDenials int, now time.Time) error {
	if err := runCtx.Err(); err != nil {
		return err
	}
	if capture.limitExceeded {
		return ErrOutputLimit
	}
	if capture.err != nil {
		return qoderProtocolInvalid("malformed or inconsistent stream-json transcript", now)
	}
	// A confirmed FATAL denial is permanent even when Qoder also exits
	// non-zero or emits an error terminal. This prevents retryable transport
	// symptoms from masking a provider permission refusal.
	if fatalDenials > 0 {
		return newQoderFailure(port.FailureKindProviderTerminal, "permission denial observed", now)
	}
	if capture.terminal.seen && !capture.terminal.success {
		return classifyTerminalFailure(capture.terminal.code, now)
	}
	if observation.processFailed {
		return processFailureError(observation.exitCode, observation.signal)
	}
	if capture.sessionID == "" {
		return qoderProtocolInvalid("session id is missing", now)
	}
	if capture.model == "" {
		return qoderProtocolInvalid("system model is missing", now)
	}
	if !capture.terminal.seen {
		return qoderProtocolInvalid("terminal result event is missing", now)
	}
	return nil
}

func resolveDeclaredResultData(data []byte, request workerRequest, sessionID, executable, version string, validator *contract.Validator, now time.Time) (declaredResult, error) {
	declared, err := decodeDeclaredResult(data, executable, version, validator)
	if err != nil {
		return declaredResult{}, qoderProtocolInvalid("WorkerResult declaration is missing or invalid", now)
	}
	if declared.TaskID != request.TaskID || declared.RunID != request.RunID || declared.AttemptID != request.AttemptID || declared.Adapter.ID != adapterID {
		return declaredResult{}, qoderProtocolInvalid("WorkerResult identity does not match WorkerRequest", now)
	}
	if declared.Session != nil && declared.Session.ID != "" && declared.Session.ID != sessionID {
		return declaredResult{}, qoderProtocolInvalid("WorkerResult session does not match transcript", now)
	}
	return declared, nil
}

func failureKindOf(err error) string {
	if failure, ok := port.AsAdapterFailure(err); ok {
		return string(failure.Kind)
	}
	return ""
}

func retryDispositionOf(err error) string {
	if failure, ok := port.AsAdapterFailure(err); ok {
		return string(failure.Disposition)
	}
	return ""
}

type declaredResult struct {
	APIVersion           domain.APIVersion `json:"apiVersion"`
	Kind                 domain.Kind       `json:"kind"`
	TaskID               string            `json:"taskId"`
	RunID                string            `json:"runId"`
	AttemptID            string            `json:"attemptId"`
	Adapter              declaredAdapter   `json:"adapter"`
	Session              *declaredSession  `json:"session,omitempty"`
	Status               string            `json:"status"`
	Summary              string            `json:"summary"`
	DeclaredChangedFiles []string          `json:"declaredChangedFiles"`
	DeclaredArtifacts    []json.RawMessage `json:"declaredArtifacts"`
	DeclaredCommands     []json.RawMessage `json:"declaredCommands"`
	DeclaredRisks        []string          `json:"declaredRisks"`
	Blocker              string            `json:"blocker,omitempty"`
	Usage                json.RawMessage   `json:"usage,omitempty"`
	OutputTruncated      bool              `json:"outputTruncated"`
	StartedAt            time.Time         `json:"startedAt"`
	CompletedAt          time.Time         `json:"completedAt"`
}

type declaredAdapter struct {
	ID         string `json:"id"`
	Executable string `json:"executable"`
	Version    string `json:"version"`
	Model      string `json:"model,omitempty"`
}

type declaredSession struct {
	ID        string `json:"id"`
	Resumable bool   `json:"resumable"`
}

func decodeDeclaredResult(data []byte, executable, version string, validator *contract.Validator) (declaredResult, error) {
	// The Qoder declaration carries Worker-owned semantics plus two runtime
	// metadata fields that only the Adapter can know authoritatively. Reject
	// malformed JSON and duplicate members before touching the document, then
	// replace only adapter.executable and adapter.version with the already-held
	// executable identity. The complete WorkerResult Schema still runs after
	// this normalization, so unknown members, missing semantic fields and all
	// task/run/attempt/adapter.id identity requirements remain fail closed.
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return declaredResult{}, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return declaredResult{}, errors.New("WorkerResult declaration is not an object")
	}
	adapterData, ok := document["adapter"]
	if !ok {
		return declaredResult{}, errors.New("WorkerResult declaration has no adapter")
	}
	var adapter map[string]json.RawMessage
	if err := json.Unmarshal(adapterData, &adapter); err != nil || adapter == nil {
		return declaredResult{}, errors.New("WorkerResult declaration adapter is not an object")
	}
	adapter["executable"], _ = json.Marshal(executable)
	adapter["version"], _ = json.Marshal(version)
	normalizedAdapter, err := json.Marshal(adapter)
	if err != nil {
		return declaredResult{}, err
	}
	document["adapter"] = normalizedAdapter
	normalized, err := json.Marshal(document)
	if err != nil {
		return declaredResult{}, err
	}
	if err := validator.Validate(domain.KindWorkerResult, normalized); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(normalized, &result); err != nil {
		return result, err
	}
	return result, nil
}
