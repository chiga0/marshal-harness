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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
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
	conformanceEventContract = "codex-exec-json-0.145-v1"
	codexProtocolVersion     = "0.145"
	codexPermissionMode      = "workspace-write-network-off-approval-never"
	conformancePendingReason = "credentialed live conformance pending: independent authority evidence is not bound to the Codex CLI identity and exec JSON contract"
)

// supportedCompatibilityLine 冻结已经通过真实 argv、JSONL 与结果契约
// conformance 的 Codex CLI major.minor 线。仅 patch 更新可兼容；major、
// minor、pre-release、build metadata 或无法严格解析的版本全部 fail closed。
// 这样 z 位安全修订不需要重复接线，同时不会把接口可能变化的 minor 更新
// 静默视为兼容。
const supportedCompatibilityLine = "0.145.x"

// isSupportedBinary 仅接受已验证 major.minor 线内的稳定三段 semver。
func isSupportedBinary(version string) bool {
	parts := strings.Split(version, ".")
	return len(parts) == 3 && parts[0] == "0" && parts[1] == "145" &&
		semverComponentPattern.MatchString(parts[2])
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
)

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
	executable string
	validator  *contract.Validator
	now        func() time.Time
	authority  *AuthorityEvidenceStore

	mu          sync.Mutex
	pinned      *executableIdentity
	conformance *boundConformance

	// testHook 只用于确定性触发安全竞态测试；生产构造器始终为 nil。
	testHook func(string)
}

var _ port.WorkerAdapter = (*Adapter)(nil)

// New 要求非空 Validator 与绝对 clean 的可执行文件路径；解析 symlink 后
// 钉住可执行普通文件。Marshal 从不按相似名字或隐式回退解析 provider 可执行文件。
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	return NewWithConformanceAuthority(executable, validator, nil)
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
	return &Adapter{executable: realPath, validator: validator, now: time.Now, authority: authority}, nil
}

func (a *Adapter) ID() string { return adapterID }

// Probe 每次重新 stat/digest/执行 `<executable> --version`，使用受限 probe
// 环境并生成通过 CapabilitySnapshot Schema 的记录。闭集内版本为 supported；
// 其他可解析版本为 unsupported 且进入 probeErrors；无法执行或无法解析则
// 返回 typed/stable error。能力声明保持 truthful：nativeBudgets 不虚报
// Codex 原生保障，普通宿主子进程不是恶意代码 sandbox。
func (a *Adapter) Probe(ctx context.Context) (domain.Record, error) {
	snapshot, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, errors.Join(ErrIdentityInvalid, err), "executable identity probe failed", a.now())
	}
	defer snapshot.close()
	identity := snapshot.identity
	a.pinIdentity(identity)
	status := "unsupported"
	probeErrors := []string{}
	if !isSupportedBinary(identity.version) {
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Codex CLI %s 兼容线，实际为 %s", supportedCompatibilityLine, identity.version))
	}
	if !a.isConformant(identity) {
		probeErrors = append(probeErrors, conformancePendingReason)
	} else if isSupportedBinary(identity.version) {
		status = "supported"
	}
	capability := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": expectedCapabilities(),
		"probeErrors":  probeErrors, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
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
	return snapshotExecutable(ctx, a.executable, a.callTestHook)
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

func (a *Adapter) isConformant(identity executableIdentity) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conformance != nil && a.conformance.identity == identity && a.now().UTC().Before(a.conformance.validUntil)
}

// BindConformance 只接受 authority store 中内容寻址、独立签名的 evidence。
// 调用者不能通过传入自造结构或 CapabilitySnapshot 获得执行授权。
func (a *Adapter) BindConformance(ctx context.Context, evidenceDigest string) error {
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
	if a.conformance == nil || a.conformance.identity != identity || !a.now().UTC().Before(a.conformance.validUntil) {
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
	command := exec.Command(executable, "--version")
	command.Env = probeEnvironment()
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	type probeRead struct {
		data []byte
		err  error
	}
	readDone := make(chan probeRead, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdout, maxVersionBytes+1))
		readDone <- probeRead{data, err}
	}()
	var output []byte
	select {
	case read := <-readDone:
		output = read.data
		if read.err != nil {
			terminateGroup(command)
			_ = stdout.Close()
			_ = command.Wait()
			return "", fmt.Errorf("probe codex version: %w", read.err)
		}
	case <-probeCtx.Done():
		terminateGroup(command)
		_ = stdout.Close()
		_ = command.Wait()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("probe codex version: timed out after %s", probeTimeout)
	}
	if len(output) > maxVersionBytes {
		terminateGroup(command)
		_ = stdout.Close()
		_ = command.Wait()
		return "", fmt.Errorf("%w: --version output exceeds %d bytes", ErrVersionUnrecognized, maxVersionBytes)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case err = <-waitDone:
	case <-probeCtx.Done():
		terminateGroup(command)
		_ = stdout.Close()
		<-waitDone
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
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
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
	if !isSupportedBinary(identity.version) {
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
	promptPath, err := existingPathWithin(controlRoot, request.PromptPath)
	if err != nil {
		return domain.Record{}, fmt.Errorf("resolve prompt: %w", err)
	}
	prompt, err := readBounded(promptPath, maxPromptBytes)
	if err != nil {
		return domain.Record{}, fmt.Errorf("read prompt: %w", err)
	}
	resultPath, err := lexicalPathWithin(controlRoot, request.ResultPath)
	if err != nil {
		return domain.Record{}, fmt.Errorf("resolve result: %w", err)
	}
	evidenceDir, err := evidenceDirectory(controlRoot, resultPath)
	if err != nil {
		return domain.Record{}, codexProtocolFailure("attempt evidence path escapes the control root or is unsafe", a.now())
	}
	projection, err := readTaskProjection(controlRoot, request.TaskSpecPath, request.SpecDigest, a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	a.callTestHook("after-task-projection")
	// 本切片的冻结 argv 无法机械表达逐工具 allowlist；非空声明在启动前
	// fail closed，绝不静默扩大工具面。
	if len(projection.tools) > 0 {
		return domain.Record{}, fmt.Errorf("%w: the frozen codex argv cannot express a per-tool allowlist", ErrUnsupportedWorkerTools)
	}
	schemaDocument, err := contract.SchemaDocument("worker-result")
	if err != nil {
		return domain.Record{}, codexProtocolFailure("durable output schema is unavailable", a.now())
	}
	evidence, err := prepareAttemptEvidence(evidenceDir, resultPath, schemaDocument)
	if err != nil {
		var claimErr *leafClaimError
		if errors.As(err, &claimErr) {
			return domain.Record{}, codexProtocolFailure("attempt "+claimErr.kind+" leaf already exists or is unsafe", a.now())
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
	// Schema/result 通过继承 fd 暴露给 child，避免 provider 按可替换路径
	// 重新打开叶子；父进程始终持有同一 inode 直至最终验证完成。
	command := exec.Command(snapshot.path, buildArgs(worktree, inheritedFilePath(0), inheritedFilePath(1), projection.model)...)
	command.Dir = worktree
	command.Env = workerEnvironment(worktree)
	command.Stdin = bytes.NewReader(prompt)
	command.ExtraFiles = []*os.File{evidence.schema, evidence.result}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider stdout pipe could not be created", a.now())
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider stderr pipe could not be created", a.now())
	}
	started := a.now().UTC()
	if err := command.Start(); err != nil {
		return domain.Record{}, newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider process could not be started", a.now())
	}
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			terminateGroup(command)
			// 关闭读端强制解除捕获阻塞：进程组外的残留子进程即使继续
			// 持有管道，证据落盘也不得被无限期挂起。
			_ = stdout.Close()
			_ = stderr.Close()
		})
	}
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() { stdoutDone <- captureJSONL(stdout, int64(request.MaxOutputBytes), kill) }()
	go func() { stderrDone <- captureStream(stderr, stderrLimit) }()
	processFinished := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			kill()
		case <-processFinished:
		}
	}()
	capture := <-stdoutDone
	stderrCapture := <-stderrDone
	waitErr := command.Wait()
	close(processFinished)
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
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}
