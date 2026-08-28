// Package codex implements the bounded Codex CLI captured-mode Worker adapter.
// 首切片仅支持 adapterId=codex、executionProfile=workspace-write、
// sessionPolicy=ephemeral 的未注册核心：闭集版本钉住、冻结 argv、
// 完整替换环境、进程组监督与 WorkerResult 归一化；任何未知版本、
// 身份漂移、协议/结果畸形、缺失终态、进程失败、超限、超时或取消均 fail closed。
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/stablegotest"
)

const (
	adapterID      = "codex"
	adapterVersion = "0.1.0"
	maxPromptBytes = 256 << 10
	maxResultBytes = 4 << 20
	stderrLimit    = 64 << 10
	// probeTimeout 为每次 --version 探测的固定上限，避免 preflight 在
	// 无响应的可执行文件上无限挂起；attempt wall-time 预算不用于探测。
	probeTimeout             = 10 * time.Second
	maxVersionBytes          = 4 << 10
	maxConformanceAge        = 24 * time.Hour
	maxConformanceTTL        = 24 * time.Hour
	conformanceEventContract = "codex-exec-json-0.145-v1"
	codexProtocolVersion     = "0.145"
	codexPermissionMode      = "workspace-write-network-off-approval-never"
	conformancePendingReason = "credentialed live conformance pending: independent authority evidence is not bound to the Codex CLI identity and exec JSON contract"
	secureFDPublicReason     = "authenticated Codex fd-exec is unavailable"
)

// supportedCompatibilityLine remains the only strict authority/APAP line.
// Ordinary-user admission is deliberately separate: adding a Mac-observed
// CLI line must never expand the signed authority contract by accident.
const supportedCompatibilityLine = "0.145.x"

const ordinaryUserCompatibilityLine0149 = "0.149.x"

// isSupportedBinary 仅接受已验证 major.minor 线内的稳定三段 semver。
func isSupportedBinary(version string) bool {
	return matchesCompatibilityLine(version, supportedCompatibilityLine)
}

func matchesCompatibilityLine(version, compatibilityLine string) bool {
	parts := strings.Split(version, ".")
	compatibility := strings.TrimSuffix(compatibilityLine, ".x")
	return len(parts) == 3 && parts[0]+"."+parts[1] == compatibility &&
		semverComponentPattern.MatchString(parts[2])
}

func ordinaryUserCompatibilityLine(goos, version string) (string, bool) {
	for _, compatibilityLine := range [...]string{supportedCompatibilityLine, ordinaryUserCompatibilityLine0149} {
		if compatibilityLine == ordinaryUserCompatibilityLine0149 && goos != "darwin" {
			continue
		}
		if matchesCompatibilityLine(version, compatibilityLine) {
			return compatibilityLine, true
		}
	}
	return "", false
}

var (
	ErrUnsupportedVersion       = errors.New("unsupported codex version")
	ErrConformancePending       = errors.New("codex live conformance is not bound")
	ErrVersionUnrecognized      = errors.New("unrecognized codex version output")
	ErrIdentityInvalid          = errors.New("codex executable identity is invalid")
	ErrIdentityDrift            = errors.New("codex executable identity drift")
	ErrOutputLimit              = errors.New("codex output limit exceeded")
	ErrProtocol                 = errors.New("invalid codex protocol")
	ErrProviderFailed           = errors.New("codex provider reported a terminal failure")
	ErrProcessFailed            = errors.New("codex process failed")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")
	ErrUnsupportedWorkerTools   = errors.New("codex worker tools allowlist unsupported")
	ErrPlatformUnsupported      = errors.New("codex secure launcher unsupported on this platform")
)

// unsafePathExecutionForTests is set only by this package's TestMain. It is
// absent from every production API and lets Darwin exercise protocol fixtures
// without weakening the production platform gate.
var unsafePathExecutionForTests bool

// versionPattern 冻结 --version 输出的唯一可接受格式：单行
// `codex-cli <semver>`。三段均为 canonical 十进制整数，不接受前导零、
// pre-release 或 build metadata；其他任何格式都不构成版本证据。
var (
	semverComponentPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	versionPattern         = regexp.MustCompile(`^codex-cli ((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)
)

// Adapter 持有钉住的可执行文件身份绑定：Probe 每次成功后刷新 pinned，
// Run 启动前重新解析并与 pinned 比较 realpath+digest，拒绝 Probe 后的
// 同版本内容漂移与替换。pinned 为空时 Run 首次使用即钉住（fail closed
// 的版本闭集校验在此之前完成），因此漂移比较永远有确定基准。
type Adapter struct {
	executable       string
	validator        *contract.Validator
	now              func() time.Time
	authority        *AuthorityEvidenceStore
	ordinaryUserMode bool
	// platform is frozen from runtime.GOOS by every production constructor.
	// Tests may replace it only to prove the closed platform matrix on one host.
	platform string

	mu          sync.Mutex
	pinned      *executableIdentity
	conformance *boundConformance

	authorityMu         sync.Mutex
	authoritySource     atomicAuthoritySource
	authorityNonceFence *HostAttestationNonceFence
	lastAuthorityState  *CodexConsumerAuthorityStateV1

	// testHook 只用于确定性触发安全竞态测试；生产构造器始终为 nil。
	testHook                 func(string)
	launcherTestGate         string
	launcherTestCloseFailure bool
	// unsafePathExecutionForTest exists only for script fixtures on platforms
	// without fd-exec. Production constructors always leave it false.
	unsafePathExecutionForTest bool
	// providerSchemaMutationForTest deterministically injects an incompatible
	// projected schema before the production compatibility checker. Production
	// constructors always leave it nil.
	providerSchemaMutationForTest func([]byte) []byte
	// legacyAuthorityForTest preserves the old signed-store fixture only for
	// native launcher tests. No production constructor or public method can set
	// it, so legacy BindConformance can never grant production execution.
	legacyAuthorityForTest bool
}

func (a *Adapter) supportsBinary(version string) bool {
	if a.ordinaryUserMode {
		_, supported := ordinaryUserCompatibilityLine(a.platform, version)
		return supported
	}
	return isSupportedBinary(version)
}

func (a *Adapter) schemaCompatibilityLine(version string) (string, bool) {
	if a.ordinaryUserMode {
		return ordinaryUserCompatibilityLine(a.platform, version)
	}
	return supportedCompatibilityLine, isSupportedBinary(version)
}

var _ port.WorkerAdapter = (*Adapter)(nil)

// New 要求非空 Validator 与绝对 clean 的可执行文件路径；解析 symlink 后
// 钉住可执行普通文件。Marshal 从不按相似名字或隐式回退解析 provider 可执行文件。
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	return NewWithConformanceAuthority(executable, validator, nil)
}

// NewOrdinaryUser enables the explicit Mac ordinary-user mode. The adapter
// still pins path/version/digest and validates WorkerResult, but it does not
// claim signed authority, APAP credentials, or a malicious-code sandbox.
func NewOrdinaryUser(executable string, validator *contract.Validator) (*Adapter, error) {
	adapter, err := New(executable, validator)
	if err != nil {
		return nil, err
	}
	adapter.ordinaryUserMode = true
	return adapter, nil
}

// NewWithConformanceAuthority 绑定只读、签名验证的 conformance authority。
// nil 是安全默认值：Probe 保持 unsupported，Run 永不获得执行授权。
func NewWithConformanceAuthority(executable string, validator *contract.Validator, authority *AuthorityEvidenceStore) (*Adapter, error) {
	if validator == nil {
		return nil, errors.New("contract validator is required")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, errors.New("codex executable must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve codex executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat codex executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("codex executable must be an executable regular file")
	}
	return &Adapter{executable: realPath, validator: validator, now: time.Now, authority: authority, platform: runtime.GOOS, unsafePathExecutionForTest: unsafePathExecutionForTests}, nil
}

// Identify collects advisory candidate identity for discovery. It never
// registers the adapter or grants execution/conformance authority.
func Identify(executable string) (version, digest string, err error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", "", errors.New("codex candidate must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", errors.New("codex candidate is unavailable")
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("codex candidate is not an executable regular file")
	}
	digest, err = digestConfiguredExecutable(realPath)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	version, err = readBinaryVersion(ctx, realPath)
	if err != nil {
		return "", "", err
	}
	return version, digest, nil
}

func (a *Adapter) ID() string { return adapterID }

// Probe 每次重新 stat/digest/执行 `<executable> --version`，使用受限 probe
// 环境并生成通过 CapabilitySnapshot Schema 的记录。闭集内版本为 supported；
// 其他可解析版本为 unsupported 且进入 probeErrors；无法执行或无法解析则
// 返回 typed/stable error。能力声明保持 truthful：nativeBudgets 不虚报
// Codex 原生保障，普通宿主子进程不是恶意代码 sandbox。
func (a *Adapter) Probe(ctx context.Context) (domain.Record, error) {
	if !secureFDExecutionAvailable() && !a.unsafePathExecutionForTest && !a.ordinaryUserMode {
		return a.unsupportedPlatformProbe()
	}
	// Strict production mode has no reason to execute or inspect a candidate
	// until a credentialed authority source is bound.  Apart from being
	// fail-closed, this keeps registration-only probes deterministic on Linux,
	// where the authenticated launcher is available but a configured fixture
	// may not be a real Codex ELF image.  Hermetic adapter tests and the explicit
	// ordinary-user mode intentionally bypass this guard.
	if !a.ordinaryUserMode && !a.unsafePathExecutionForTest && a.testHook == nil && !a.hasAtomicAuthoritySource() {
		return a.unsupportedConformanceProbe()
	}
	snapshot, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, errors.Join(ErrIdentityInvalid, err), "executable identity probe failed", a.now())
	}
	defer snapshot.close()
	identity := snapshot.identity
	a.pinIdentity(identity)
	if a.ordinaryUserMode {
		failure := ""
		var adapterFailure *AuthorityFailure
		if !a.supportsBinary(identity.version) {
			failure = "Codex CLI version is outside the ordinary-user compatibility line"
			code := "codex_evidence_contract_mismatch"
			cause := ErrUnsupportedVersion
			details := AuthorityFailureDetails{}
			if matchesCompatibilityLine(identity.version, ordinaryUserCompatibilityLine0149) && a.platform != "darwin" {
				failure = "Codex CLI ordinary-user compatibility line is unsupported on this platform"
				code = "codex_platform_unsupported"
				cause = ErrPlatformUnsupported
				details.Platform = a.platform
			}
			adapterFailure = newAuthorityFailure("probe", code, failure, details, cause, a.now())
		}
		status := "supported"
		probeErrors := []string{}
		if failure != "" {
			status = "unsupported"
			probeErrors = []string{failure}
		}
		capability := map[string]any{
			"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
			"adapterId": adapterID, "adapterVersion": adapterVersion,
			"executable": identity.path, "executableDigest": identity.digest,
			"binaryVersion": identity.version, "probeStatus": status,
			"capabilities": a.capabilities(), "probeErrors": probeErrors,
			"probedAt": a.now().UTC().Format(time.RFC3339Nano),
		}
		capability["authorityMode"] = "ordinary-user"
		if adapterFailure != nil {
			capability["adapterFailure"] = adapterFailure
		}
		return a.capabilityRecord(capability)
	}
	if a.hasAtomicAuthoritySource() {
		authority, failure := a.consumeFreshAuthority(ctx, snapshot, "probe")
		if failure != nil {
			capability := map[string]any{
				"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
				"adapterId": adapterID, "adapterVersion": adapterVersion,
				"executable": identity.path, "executableDigest": identity.digest,
				"binaryVersion": identity.version, "probeStatus": "unsupported",
				"capabilities": a.capabilities(), "probeErrors": []string{failure.SafeMessage}, "adapterFailure": failure,
				"probedAt": a.now().UTC().Format(time.RFC3339Nano),
			}
			return a.capabilityRecord(capability)
		}
		capability := map[string]any{
			"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
			"adapterId": adapterID, "adapterVersion": adapterVersion,
			"executable": identity.path, "executableDigest": identity.digest,
			"binaryVersion": identity.version, "probeStatus": "supported",
			"capabilities": a.capabilities(), "probeErrors": []string{}, "codexAuthority": authority.metadata,
			"conformanceEvidenceDigest": authority.metadata.EvidenceDigest, "conformanceTrustRootKeyId": authority.metadata.TrustRootKeyID,
			"conformanceProbeProfileDigest": authority.metadata.ProfileDigest, "conformanceValidUntil": authority.metadata.ValidUntil,
			"conformanceHostFingerprint": authority.metadata.HostIdentityDigest, "conformanceAuthorityGeneration": authority.metadata.AuthorityGeneration,
			"probedAt": a.now().UTC().Format(time.RFC3339Nano),
		}
		return a.capabilityRecord(capability)
	}
	// ADR 0037 live production admission is intentionally still hard-disabled.
	// The legacy conformance store remains usable by hermetic execution tests,
	// but it cannot promote a production CapabilitySnapshot to supported.
	failure := newAuthorityFailure("probe", "codex_conformance_pending", conformancePendingReason, AuthorityFailureDetails{}, ErrCodexConformancePending, a.now())
	if !a.supportsBinary(identity.version) {
		failure = newAuthorityFailure("probe", "codex_evidence_contract_mismatch", "Codex CLI version is outside the admitted compatibility line", AuthorityFailureDetails{}, ErrUnsupportedVersion, a.now())
	}
	capability := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": "unsupported",
		"capabilities": a.capabilities(),
		"probeErrors":  []string{failure.SafeMessage}, "adapterFailure": failure, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
	}
	return a.capabilityRecord(capability)
}

func (a *Adapter) unsupportedConformanceProbe() (domain.Record, error) {
	digest, err := digestConfiguredExecutable(a.executable)
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, errors.Join(ErrIdentityInvalid, err), "platform capability probe could not bind executable digest", a.now())
	}
	failure := newAuthorityFailure("probe", "codex_conformance_pending", conformancePendingReason, AuthorityFailureDetails{}, ErrCodexConformancePending, a.now())
	capability := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": a.executable, "executableDigest": digest,
		"binaryVersion": "unavailable", "probeStatus": "unsupported",
		"capabilities": a.capabilities(),
		"probeErrors":  []string{failure.SafeMessage}, "adapterFailure": failure, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
	}
	return a.capabilityRecord(capability)
}

func (a *Adapter) capabilityRecord(capability map[string]any) (domain.Record, error) {
	data, err := json.Marshal(capability)
	if err != nil {
		return domain.Record{}, err
	}
	if err := a.validator.Validate(domain.KindCapabilitySnapshot, data); err != nil {
		return domain.Record{}, fmt.Errorf("validate CapabilitySnapshot: %w", err)
	}
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: data}, nil
}

func (a *Adapter) unsupportedPlatformProbe() (domain.Record, error) {
	digest, err := digestConfiguredExecutable(a.executable)
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, errors.Join(ErrIdentityInvalid, err), "platform capability probe could not bind executable digest", a.now())
	}
	failure := newAuthorityFailure("probe", "codex_platform_unsupported", secureFDPublicReason, AuthorityFailureDetails{Platform: runtime.GOOS}, ErrPlatformUnsupported, a.now())
	capability := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": a.executable, "executableDigest": digest,
		"binaryVersion": "unavailable", "probeStatus": "unsupported",
		"capabilities": expectedCapabilities(),
		"probeErrors":  []string{failure.SafeMessage}, "adapterFailure": failure, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(capability)
	if err != nil {
		return domain.Record{}, err
	}
	if err := a.validator.Validate(domain.KindCapabilitySnapshot, data); err != nil {
		return domain.Record{}, fmt.Errorf("validate CapabilitySnapshot: %w", err)
	}
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: data}, nil
}

func expectedCapabilities() map[string]any {
	return map[string]any{
		"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
		"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
		"executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{},
		"processTreeCancellation": true,
		"notes": []string{
			"由 Marshal 实施 wall-time 与 output-bytes 上限。",
			"workspace-write sandbox 显式关闭网络且 approval=never，仍不构成恶意代码隔离。",
			"仅支持 ephemeral 会话；--ignore-user-config/--ignore-rules 阻止用户配置、rules、MCP 与 plugin 介入。",
			"仅当独立签名 conformance 记录与当前 executable identity 精确一致时才声明 supported。",
		},
	}
}

func (a *Adapter) capabilities() map[string]any {
	capabilities := expectedCapabilities()
	if a.ordinaryUserMode {
		notes := capabilities["notes"].([]string)
		notes = append(notes, "当前为 ordinary-user：无签名 authority、APAP 凭据或恶意代码沙箱保证。")
		capabilities["notes"] = notes
	}
	return capabilities
}

func expectedCapabilitiesDigest() string {
	data, _ := json.Marshal(expectedCapabilities())
	return digestBytes(data)
}

type executableIdentity struct{ path, digest, version string }
type boundConformance struct {
	identity       executableIdentity
	validUntil     time.Time
	evidenceDigest string
}

// inspect 每次重新钉住可执行文件身份：realpath、SHA-256 digest 与
// 受限 probe 环境下执行 `--version` 解析出的版本，防止 Probe 后替换。
func (a *Adapter) inspect(ctx context.Context) (*executableSnapshot, error) {
	// Mac ordinary-user mode has no authenticated fd-exec primitive. Keep the
	// configured, already-registered executable path stable instead of copying
	// it into a random temporary pathname that Gatekeeper treats as a new
	// program identity. The stable-path implementation still holds the source
	// inode while probing and rechecks the pinned digest before launch; it is
	// intentionally ordinary-user (not hardened authority) semantics.
	if a.ordinaryUserMode {
		return snapshotExecutableByStablePath(ctx, a.executable, a.callTestHook)
	}
	return snapshotExecutable(ctx, a.executable, a.callTestHook, a.unsafePathExecutionForTest || a.ordinaryUserMode)
}

func (a *Adapter) callTestHook(stage string) {
	if a.testHook != nil {
		a.testHook(stage)
	}
}

// pinIdentity 在 Probe 成功后刷新钉住身份；Probe 失败不改动既有绑定，
// 避免一次失败探测清空仍被校验保护的身份基准。
func (a *Adapter) pinIdentity(identity executableIdentity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conformance != nil {
		return
	}
	pinned := identity
	a.pinned = &pinned
}

// BindConformance 只接受 authority store 中内容寻址、独立签名的 evidence。
// 调用者不能通过传入自造结构或 CapabilitySnapshot 获得执行授权。
func (a *Adapter) BindConformance(ctx context.Context, evidenceDigest string) error {
	if !secureFDExecutionAvailable() && !a.unsafePathExecutionForTest && !a.ordinaryUserMode {
		return port.Permanent(fmt.Errorf("%w: %s", ErrPlatformUnsupported, secureFDPublicReason))
	}
	if !a.unsafePathExecutionForTest && !a.legacyAuthorityForTest && !a.ordinaryUserMode {
		return port.Permanent(ErrConformancePending)
	}
	if a.authority == nil {
		return port.Permanent(ErrConformancePending)
	}
	evidence, err := a.authority.resolve(ctx, evidenceDigest, a.now().UTC())
	if err != nil {
		return err
	}
	snapshot, err := a.inspect(ctx)
	if err != nil {
		return err
	}
	defer snapshot.close()
	identity := snapshot.identity
	if !isSupportedBinary(identity.version) {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	if evidence.Executable != identity.path || evidence.ExecutableDigest != identity.digest || evidence.BinaryVersion != identity.version || evidence.CodexCLIVersion != identity.version {
		return fmt.Errorf("%w: conformance identity does not match current executable", ErrIdentityDrift)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	validUntil, _ := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	pinned := identity
	a.pinned = &pinned
	a.conformance = &boundConformance{identity: identity, validUntil: validUntil, evidenceDigest: evidence.EvidenceDigest}
	return nil
}

// verifyPinnedIdentity 将 Run 启动前重新解析的身份与钉住身份比较：
// 尚无钉住身份时（未经 Probe 的首次 Run）当场钉住；否则 realpath 或
// SHA-256 digest 任一不一致都返回 ErrIdentityDrift，拒绝同版本内容漂移、
// 二进制替换或 symlink 重定向。
func (a *Adapter) verifyPinnedIdentity(identity executableIdentity) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pinned == nil {
		pinned := identity
		a.pinned = &pinned
	}
	if a.pinned.path != identity.path || a.pinned.digest != identity.digest {
		return fmt.Errorf("%w: executable content changed since the identity was pinned", ErrIdentityDrift)
	}
	if !a.ordinaryUserMode && (a.unsafePathExecutionForTest || a.legacyAuthorityForTest) && (a.conformance == nil || a.conformance.identity != identity || !a.now().UTC().Before(a.conformance.validUntil)) {
		return port.Permanent(ErrConformancePending)
	}
	return nil
}

// readBinaryVersion 在受限 probe 环境中执行 `<executable> --version`，
// 只接受冻结格式 `codex-cli <semver>` 的单行输出；解析失败返回
// ErrVersionUnrecognized，执行失败返回包装后的 typed error。探测以固定
// probeTimeout 封顶，调用方 context 取消仍优先透传。
func readBinaryVersion(ctx context.Context, executable string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, executable, "--version")
	return readBinaryVersionCommand(ctx, probeCtx, command)
}

func readBinaryVersionCommand(ctx, probeCtx context.Context, command *exec.Cmd) (string, error) {
	command.Env = probeEnvironment()
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	command.Cancel = func() error {
		terminateGroup(command)
		return stdout.Close()
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxVersionBytes+1))
	if len(output) > maxVersionBytes {
		terminateGroup(command)
		_ = command.Wait()
		return "", fmt.Errorf("%w: --version output exceeds %d bytes", ErrVersionUnrecognized, maxVersionBytes)
	}
	err = command.Wait()
	if readErr != nil && probeCtx.Err() == nil {
		return "", fmt.Errorf("probe codex version: %w", readErr)
	}
	if probeCtx.Err() != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("probe codex version: timed out after %s", probeTimeout)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if probeCtx.Err() != nil {
			return "", fmt.Errorf("probe codex version: timed out after %s", probeTimeout)
		}
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	return parseBinaryVersion(string(output))
}

// parseBinaryVersion 按冻结格式解析版本字符串；不做任何范围或兼容性猜测。
func parseBinaryVersion(output string) (string, error) {
	if !strings.HasSuffix(output, "\n") || strings.Count(output, "\n") != 1 || strings.Contains(output, "\r") {
		return "", fmt.Errorf("%w: codex-cli version format is frozen", ErrVersionUnrecognized)
	}
	match := versionPattern.FindStringSubmatch(strings.TrimSuffix(output, "\n"))
	if match == nil {
		return "", fmt.Errorf("%w: codex-cli version format is frozen", ErrVersionUnrecognized)
	}
	return match[1], nil
}

type workerRequest struct {
	TaskID, RunID, AttemptID                                        string
	SpecDigest                                                      string
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
		SpecDigest            string `json:"specDigest"`
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

// Run 执行一次非交互 captured attempt：启动前重新解析并校验可执行文件身份
// 与版本闭集，冻结 argv 与完整替换环境，在 Marshal 的 wall-time/output-byte
// 边界下监督整个进程组，最终只接受与 WorkerRequest 身份一致且通过现有
// WorkerResult Schema 的候选工作声明。Provider/进程/协议失败以错误返回，
// 由 Core 决定是否消耗运维重试预算。
func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindWorkerRequest {
		return domain.Record{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	if !secureFDExecutionAvailable() && !a.unsafePathExecutionForTest && !a.ordinaryUserMode {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrPlatformUnsupported, secureFDPublicReason, a.now())
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	// 首切片固定为 codex + workspace-write + ephemeral 单一画像；
	// 其他画像与会话画像在启动前以稳定的永久/不支持错误失败。
	if request.AdapterID != adapterID || request.ExecutionProfile != "workspace-write" {
		return domain.Record{}, errors.New("WorkerRequest does not match the codex adapter execution profile")
	}
	if request.SessionPolicy != "ephemeral" {
		return domain.Record{}, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
	}
	// Preflight（inspect/version probe、路径解析与 leaf 占用）在调用方
	// context 下执行，不消耗 attempt 的 wall-time 预算；attempt 预算只在
	// worker 启动时开始计时，杜绝高并发负载下 pre-start 耗尽预算导致
	// transcript 从不落盘。
	snapshot, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, errors.Join(ErrIdentityInvalid, err), "executable identity probe failed", a.now())
	}
	defer snapshot.close()
	identity := snapshot.identity
	if !a.supportsBinary(identity.version) {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrUnsupportedVersion, "binary version is outside the compatible line", a.now())
	}
	// 启动前把重新解析的身份与钉住身份比较，防止 Probe 后替换、
	// symlink/内容漂移或同版本二进制漂移进入 captured worker。
	if err := a.verifyPinnedIdentity(identity); err != nil {
		if errors.Is(err, ErrConformancePending) {
			return domain.Record{}, err
		}
		return domain.Record{}, newCodexFailure(port.FailureKindProtocolInvalid, ErrIdentityDrift, "executable identity changed after pinning", a.now())
	}
	a.callTestHook("after-identity-verify")
	worktree, err := openPinnedDirectory(request.WorktreePath)
	if err != nil {
		return domain.Record{}, fmt.Errorf("pin worktree: %w", err)
	}
	defer worktree.close()
	controlRoot, err := openPinnedDirectory(request.ControlRoot)
	if err != nil {
		return domain.Record{}, fmt.Errorf("pin control root: %w", err)
	}
	defer controlRoot.close()
	a.callTestHook("before-prompt-read")
	prompt, err := readInputFileAt(controlRoot.file, request.PromptPath, maxPromptBytes)
	if err != nil {
		return domain.Record{}, fmt.Errorf("read prompt: %w", err)
	}
	evidenceDir, resultName, err := prepareEvidenceDirectory(controlRoot.file, request.ResultPath)
	if err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence path escapes the control root or is unsafe", a.now())
	}
	defer evidenceDir.close()
	projection, err := readTaskProjection(controlRoot.file, request.TaskSpecPath, request.SpecDigest, a.validator)
	if err != nil {
		if errors.Is(err, ErrUnsupportedWorkerTools) {
			return domain.Record{}, newCodexFailure(port.FailureKindProtocolInvalid, ErrUnsupportedWorkerTools, "named worker tools unsupported", a.now())
		}
		return domain.Record{}, err
	}
	a.callTestHook("after-task-projection")
	schemaDocument, err := contract.SchemaDocument("worker-result")
	if err != nil {
		return domain.Record{}, codexProtocolFailure("durable output schema is unavailable", a.now())
	}
	compatibilityLine, supported := a.schemaCompatibilityLine(identity.version)
	if !supported {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrUnsupportedVersion, "binary version has no provider schema profile", a.now())
	}
	evidence, err := prepareAttemptEvidence(evidenceDir, resultName, schemaDocument, compatibilityLine, a.providerSchemaMutationForTest)
	if err != nil {
		var claimErr *leafClaimError
		if errors.As(err, &claimErr) {
			return domain.Record{}, codexProtocolFailure("attempt "+claimErr.kind+" leaf already exists or is unsafe", a.now())
		}
		var compatibilityErr *providerSchemaCompatibilityError
		if errors.As(err, &compatibilityErr) {
			return domain.Record{}, codexProtocolFailure(compatibilityErr.reasonCode, a.now())
		}
		return domain.Record{}, codexProtocolFailure("attempt evidence leaves could not be claimed", a.now())
	}
	defer evidence.close()
	a.callTestHook("after-evidence-claim")
	// attempt wall-time 预算从 worker 启动前一刻才开始计时，preflight
	// 不消耗它；调用方 context 已过期时在启动前 fail，绝不带病启动。
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(request.AttemptTimeoutSeconds)*time.Second)
	defer cancel()
	if runCtx.Err() != nil {
		return domain.Record{}, runCtx.Err()
	}
	// Authorization and both configured directory identities are rechecked at
	// the launch boundary. The worktree descriptor remains pinned across Start;
	// the path must name that same inode both before and after the child's chdir.
	a.callTestHook("before-command-start")
	if !a.unsafePathExecutionForTest && !a.legacyAuthorityForTest && !a.ordinaryUserMode {
		if _, failure := a.consumeFreshAuthority(ctx, snapshot, "run"); failure != nil {
			return domain.Record{}, failure
		}
	}
	if err := a.verifyPinnedIdentity(identity); err != nil {
		return domain.Record{}, err
	}
	if err := worktree.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("worktree changed before provider launch", a.now())
	}
	if err := controlRoot.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("control root changed before provider launch", a.now())
	}
	if err := evidence.verifyLeaves(); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence containment changed before provider launch", a.now())
	}
	if err := snapshot.verifyStablePathIdentity(); err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProtocolInvalid, ErrIdentityDrift, "configured executable changed before provider launch", a.now())
	}
	// Schema/result 通过继承 fd 暴露给 child，避免 provider 按可替换路径
	// 重新打开叶子；父进程始终持有同一 inode 直至最终验证完成。
	processCtx, cancelProcess := context.WithCancel(runCtx)
	defer cancelProcess()
	// The child performs its os-level chdir before Start returns. Codex receives
	// no -C/PWD pathname to resolve later, so replacement after Start cannot
	// redirect its actual workspace away from that already-open directory.
	closeMode := ""
	if a.launcherTestCloseFailure {
		closeMode = launcherCloseFailure
	}
	launcher := ""
	target := ""
	extraFiles := []*os.File{evidence.schema, evidence.result, worktree.file}
	if a.ordinaryUserMode || a.unsafePathExecutionForTest {
		launcher, err = os.Executable()
		if err != nil {
			return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "test launcher is unavailable", a.now())
		}
		target = snapshot.path
	} else {
		launcherFD, launcherErr := secureLauncherFD()
		if launcherErr != nil || snapshot.file == nil {
			return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "authenticated provider launcher is unavailable", a.now())
		}
		// ExtraFiles are fd 3..7: schema, result, worktree, the running
		// Marshal image, and the already-attested Codex image.
		launcher = secureFDPath(6)
		target = secureFDPath(7)
		extraFiles = append(extraFiles, launcherFD, snapshot.file)
	}
	launcherArgs := []string{codexLauncherArgument, target, a.launcherTestGate, closeMode}
	launcherArgs = append(launcherArgs, buildArgs(inheritedFilePath(0), inheritedFilePath(1), projection.model)...)
	environment, err := stablegotest.WithEnvironment(workerEnvironment())
	if err != nil {
		return domain.Record{}, fmt.Errorf("stable Go test runner unavailable: %w", err)
	}
	command := exec.CommandContext(processCtx, launcher, launcherArgs...)
	command.Env = environment
	command.Stdin = bytes.NewReader(prompt)
	command.ExtraFiles = extraFiles
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider stdout pipe could not be created", a.now())
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider stderr pipe could not be created", a.now())
	}
	command.Cancel = func() error {
		terminateGroup(command)
		_ = stdout.Close()
		return stderr.Close()
	}
	started := a.now().UTC()
	if err := command.Start(); err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider process could not be started", a.now())
	}
	a.callTestHook("after-command-start")
	// Start returns only after the child has performed chdir. Requiring the
	// configured path to name the pinned inode both immediately before and
	// immediately after Start makes rename/symlink races fail closed.
	if err := worktree.verifyLinked(); err != nil {
		cancelProcess()
		_ = command.Wait()
		return domain.Record{}, codexProtocolFailure("worktree changed during provider launch", a.now())
	}
	a.callTestHook("after-worktree-launch-verify")
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() { stdoutDone <- captureJSONL(stdout, int64(request.MaxOutputBytes), cancelProcess) }()
	go func() { stderrDone <- captureStream(stderr, stderrLimit) }()
	capture := <-stdoutDone
	stderrCapture := <-stderrDone
	waitErr := command.Wait()
	completed := a.now().UTC()
	// 证据在任何失败分类之前落盘：timeout/取消/超限/协议/进程失败场景都
	// 保留 transcript、有界 stderr 与结构化 metadata。
	if err := replaceFileContents(evidence.transcript, capture.raw); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt transcript evidence could not be persisted", a.now())
	}
	if err := replaceFileContents(evidence.stderr, stderrCapture.data); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt stderr evidence could not be persisted", a.now())
	}
	exitCode, signal := processOutcome(command)
	// metadata 只含结构化计数、截断、exit/signal/context 分类与 digest；
	// 不含 prompt、自由文本、凭据或配置内容。
	metadata, err := json.MarshalIndent(map[string]any{
		"threadId": capture.threadID, "eventCount": capture.eventCount,
		"turnCount": capture.turnCount, "itemCount": capture.itemCount,
		"inputTokens": capture.inputTokens, "outputTokens": capture.outputTokens,
		"capturedBytes":   len(capture.raw),
		"outputTruncated": capture.limitExceeded, "transcriptDigest": digestBytes(capture.raw),
		"exitCode": exitCode, "signal": signal, "stderrBytes": len(stderrCapture.data), "stderrTruncated": stderrCapture.truncated,
		"contextError": contextError(runCtx),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence metadata could not be encoded", a.now())
	}
	if err := replaceFileContents(evidence.metadata, append(metadata, '\n')); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence metadata could not be persisted", a.now())
	}
	if err := worktree.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("worktree changed during execution", a.now())
	}
	if err := controlRoot.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("control root changed during execution", a.now())
	}
	if err := evidence.verifyLeaves(); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence containment changed during execution", a.now())
	}
	if runCtx.Err() != nil {
		return domain.Record{}, runCtx.Err()
	}
	if capture.limitExceeded {
		return domain.Record{}, ErrOutputLimit
	}
	if capture.err != nil {
		if errors.Is(capture.err, ErrProviderFailed) {
			return domain.Record{}, codexProviderFailure(a.now())
		}
		return domain.Record{}, codexProtocolFailure("stream protocol validation failed", a.now())
	}
	if waitErr != nil {
		return domain.Record{}, processFailureError(a.now())
	}
	if capture.threadID == "" {
		return domain.Record{}, codexProtocolFailure("thread identity is missing", a.now())
	}
	declared, err := readDeclaredResultFile(evidence.result, int64(maxResultBytes), a.validator)
	if err != nil {
		return domain.Record{}, codexResultFailure("WorkerResult declaration is missing or invalid", a.now())
	}
	if declared.TaskID != request.TaskID || declared.RunID != request.RunID || declared.AttemptID != request.AttemptID || declared.Adapter.ID != adapterID {
		return domain.Record{}, codexProtocolFailure("WorkerResult identity does not match WorkerRequest", a.now())
	}
	if declared.Session != nil && declared.Session.ID != "" && declared.Session.ID != capture.threadID {
		return domain.Record{}, codexProtocolFailure("WorkerResult session does not match transcript", a.now())
	}
	// 声明中的 executable/version 不作为权威；身份字段匹配后由 Adapter
	// 覆盖为本 attempt 钉住的 realpath/version，session 绑定 transcript
	// 的 thread id 且恒为 resumable=false。
	declared.Adapter.Executable, declared.Adapter.Version = identity.path, identity.version
	declared.Session = &declaredSession{ID: capture.threadID, Resumable: false}
	declared.StartedAt, declared.CompletedAt = started, completed
	// model 只由冻结 TaskSpec 投影：无条件覆盖，worker 自述的 model
	// 声明不作为权威（未声明时置空并由 omitempty 丢弃）。
	declared.Adapter.Model = projection.model
	data, err := json.Marshal(declared)
	if err != nil {
		return domain.Record{}, codexProtocolFailure("normalized WorkerResult could not be encoded", a.now())
	}
	if err := a.validator.Validate(domain.KindWorkerResult, data); err != nil {
		return domain.Record{}, codexProtocolFailure("normalized WorkerResult violates the durable schema", a.now())
	}
	if err := replaceFileContents(evidence.result, append(data, '\n')); err != nil {
		return domain.Record{}, codexResultFailure("normalized WorkerResult could not be persisted", a.now())
	}
	if err := worktree.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("worktree changed before completion", a.now())
	}
	if err := controlRoot.verifyLinked(); err != nil {
		return domain.Record{}, codexProtocolFailure("control root changed before completion", a.now())
	}
	if err := evidence.verifyLeaves(); err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence containment changed before completion", a.now())
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, nil
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

func readDeclaredResultFile(file *os.File, limit int64, validator *contract.Validator) (declaredResult, error) {
	data, err := readBoundedFile(file, limit)
	if err != nil {
		return declaredResult{}, fmt.Errorf("read WorkerResult declaration: %w", err)
	}
	data = pi.NormalizeDeclaredWorkerResult(data)
	data = normalizeProviderOptionalFields(data)
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

// normalizeProviderOptionalFields removes empty values emitted only because
// Codex strict response schemas require every property to be present. The
// durable schema remains authoritative: non-empty values and all required
// fields are untouched, while empty optional path/uri/blocker/currency fields
// are treated as absent before validation.
func normalizeProviderOptionalFields(data []byte) []byte {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return data
	}
	if blocker, ok := document["blocker"].(string); ok && blocker == "" {
		delete(document, "blocker")
	}
	if adapter, ok := document["adapter"].(map[string]any); ok {
		if model, ok := adapter["model"].(string); ok && model == "" {
			delete(adapter, "model")
		}
	}
	if session, ok := document["session"].(map[string]any); ok {
		if id, ok := session["id"].(string); ok && id == "" {
			delete(document, "session")
		}
	}
	if artifacts, ok := document["declaredArtifacts"].([]any); ok {
		for _, item := range artifacts {
			if artifact, ok := item.(map[string]any); ok {
				for _, name := range []string{"path", "uri"} {
					if value, ok := artifact[name].(string); ok && value == "" {
						delete(artifact, name)
					}
				}
			}
		}
	}
	if usage, ok := document["usage"].(map[string]any); ok {
		if currency, ok := usage["currency"].(string); ok && currency == "" {
			delete(usage, "currency")
		}
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return data
	}
	return normalized
}
