// Package pi implements the bounded Pi Worker adapter.
package pi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID          = "pi"
	adapterVersion     = "0.4.0"
	supportedBinary    = "0.84.1"
	supportedBinary843 = "0.84.3"
	// supportedSessionVersion is the exact pi session event protocol version
	// Marshal accepts. Any other header version is a protocol violation.
	supportedSessionVersion = 3
	maxPromptBytes          = 256 << 10
	maxResultBytes          = 4 << 20
	stderrLimit             = 64 << 10
)

// isSupportedBinary is deliberately an exact closed set. Pi's JSON event
// protocol is versioned independently from the CLI package, so a new CLI
// version is admitted only after its real argv/help and JSONL stream have
// been checked against the frozen session v3 contract.
func isSupportedBinary(version string) bool {
	return version == supportedBinary || version == supportedBinary843
}

// workerTools is the frozen tool allowlist. bash is never granted and the
// list never grows implicitly: Marshal passes it via direct argv only.
const workerTools = "read,grep,find,ls,write,edit"

// readOnlyTools is the frozen read-only tool allowlist (ADR 0014): the read
// tools plus edit, whose write targets are confined to artifact paths by
// Marshal's scope gate because Pi has no path-scoped tool permission. bash is
// removed from the workspace-write list and never granted here either.
const readOnlyTools = "read,grep,find,ls,edit"

// toolsForProfile selects the frozen tool allowlist for an execution profile.
func toolsForProfile(profile string) string {
	if profile == "read-only" {
		return readOnlyTools
	}
	return workerTools
}

// toolsArgFor resolves the effective --tools value. Undeclared tasks keep the
// frozen profile default (backward compatibility). Declared tasks receive
// exactly the declared set intersected with Pi's tool surface for the
// profile, in frozen surface order; Pi fails closed before launch when it
// cannot provide a declared tool (bash is never available, write is
// unavailable under the read-only profile). The declaration never expands the
// profile surface.
func toolsArgFor(profile string, tools []string) (string, error) {
	if len(tools) == 0 {
		return toolsForProfile(profile), nil
	}
	surface := strings.Split(toolsForProfile(profile), ",")
	supported := make(map[string]bool, len(surface))
	for _, tool := range surface {
		supported[tool] = true
	}
	for _, tool := range tools {
		if !supported[tool] {
			return "", fmt.Errorf("pi cannot provide declared tool %q under execution profile %q", tool, profile)
		}
	}
	selected := make([]string, 0, len(tools))
	for _, tool := range surface {
		for _, declared := range tools {
			if declared == tool {
				selected = append(selected, tool)
				break
			}
		}
	}
	return strings.Join(selected, ","), nil
}

// declaredWorkerTools reads the frozen TaskSpec worker.tools declaration
// from the control input. A nil result means no allowlist is declared and
// the frozen profile defaults apply. Any read or format failure fails closed
// before launch; the enforcement layer never runs on a partial declaration.
func declaredWorkerTools(controlRoot, taskSpecPath string) ([]string, error) {
	path, err := existingPathWithin(controlRoot, taskSpecPath)
	if err != nil {
		return nil, fmt.Errorf("resolve TaskSpec: %w", err)
	}
	data, err := readBounded(path, maxResultBytes)
	if err != nil {
		return nil, fmt.Errorf("read TaskSpec: %w", err)
	}
	tools, err := denials.ParseDeclaredWorkerTools(data)
	if err != nil {
		return nil, fmt.Errorf("worker tools: %w", err)
	}
	return tools, nil
}

var (
	ErrUnsupportedVersion       = errors.New("unsupported pi version")
	ErrOutputLimit              = errors.New("pi output limit exceeded")
	ErrProtocol                 = errors.New("invalid pi protocol")
	ErrPermissionDenied         = errors.New("pi permission denied")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")
	ErrProcessFailed            = errors.New("pi process failed")
	ErrProviderFailed           = errors.New("pi provider reported a terminal failure")
)

type Adapter struct {
	executable string
	validator  *contract.Validator
	now        func() time.Time
	// spawn starts the prepared worker process. It is an injectable seam used
	// only by Run: tests replace it to prove PrepareLaunch and every
	// fail-closed gate complete without ever starting a process. The
	// production default is (*exec.Cmd).Start. PrepareLaunch and
	// CompleteLaunch never call spawn.
	spawn func(cmd *exec.Cmd) error
}

// startCommand starts command through the injectable spawn seam; a nil seam
// is the production (*exec.Cmd).Start.
func (a *Adapter) startCommand(command *exec.Cmd) error {
	if a.spawn != nil {
		return a.spawn(command)
	}
	return command.Start()
}

var _ port.TerminalLaunchAdapter = (*Adapter)(nil)

// New requires an exact absolute executable path. Marshal never resolves a
// provider executable by a similar name or by an implicit fallback.
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	if validator == nil {
		return nil, errors.New("contract validator is required")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, errors.New("pi executable must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve pi executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat pi executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("pi executable must be an executable regular file")
	}
	return &Adapter{executable: realPath, validator: validator, now: time.Now}, nil
}

func (a *Adapter) ID() string { return adapterID }

// PrepareTerminal freezes Pi's native TUI launch. JSON/print mode and the
// positional prompt are removed, while the no-shell tool allowlist and all
// extension/context hardening flags remain intact.
func (a *Adapter) PrepareTerminal(ctx context.Context, record domain.Record) (port.TerminalLaunchSpec, error) {
	if record.Kind != domain.KindWorkerRequest {
		return port.TerminalLaunchSpec{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	if request.AdapterID != adapterID || (request.ExecutionProfile != "workspace-write" && request.ExecutionProfile != "read-only") {
		return port.TerminalLaunchSpec{}, errors.New("WorkerRequest does not match the pi adapter execution profile")
	}
	if request.SessionPolicy != "ephemeral" {
		return port.TerminalLaunchSpec{}, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
	}
	identity, err := a.inspect(ctx)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	if !isSupportedBinary(identity.version) {
		return port.TerminalLaunchSpec{}, fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	worktree, controlRoot, prompt, err := resolveTerminalInput(request)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	tools, err := declaredWorkerTools(controlRoot, request.TaskSpecPath)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	arguments, err := buildTerminalArgsWithTools(request.ExecutionProfile, readModel(controlRoot, request.TaskSpecPath), tools)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	return port.TerminalLaunchSpec{
		AdapterID: adapterID, AdapterVersion: adapterVersion, RunID: request.RunID, AttemptID: request.AttemptID, BinaryVersion: identity.version,
		Executable: identity.path, ExecutableDigest: identity.digest, WorkingDirectory: worktree,
		Arguments:   arguments,
		Environment: terminalWorkerEnvironment(worktree), InitialPrompt: string(prompt),
		CompletionGate: port.TerminalCompletionSupervisedConfirmation,
	}, nil
}

func resolveTerminalInput(request workerRequest) (string, string, []byte, error) {
	worktree, err := filepath.EvalSymlinks(request.WorktreePath)
	if err != nil || !filepath.IsAbs(worktree) {
		return "", "", nil, errors.New("worktree must be an existing absolute directory")
	}
	controlRoot, err := filepath.EvalSymlinks(request.ControlRoot)
	if err != nil || !filepath.IsAbs(controlRoot) {
		return "", "", nil, errors.New("control root must be an existing absolute directory")
	}
	promptPath, err := existingPathWithin(controlRoot, request.PromptPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve prompt: %w", err)
	}
	prompt, err := readBounded(promptPath, maxPromptBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("read prompt: %w", err)
	}
	return worktree, controlRoot, prompt, nil
}

func (a *Adapter) Probe(ctx context.Context) (domain.Record, error) {
	identity, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, err
	}
	status := "supported"
	probeErrors := []string{}
	if !isSupportedBinary(identity.version) {
		status = "unsupported"
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Pi %s 或 %s，实际为 %s", supportedBinary, supportedBinary843, identity.version))
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status, "authorityMode": "ordinary-user",
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
			"executionProfiles": []string{"workspace-write", "read-only"}, "nativeBudgets": []string{},
			"processTreeCancellation": true,
			"notes": []string{
				"由 Marshal 实施 wall-time 与 output-bytes 上限。",
				"workspace-write 工具白名单固定为 " + workerTools + "，read-only 白名单固定为 " + readOnlyTools + "，永不授予 bash。",
				"Pi 非交互模式不是恶意代码隔离边界。",
			},
		},
		"probeErrors": probeErrors, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
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

type executableIdentity struct{ path, digest, version string }

// inspect pins the executable identity through realpath and SHA256 and
// verifies the exact binary version before Marshal trusts the adapter.
func (a *Adapter) inspect(ctx context.Context) (executableIdentity, error) {
	info, err := os.Stat(a.executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return executableIdentity{}, errors.New("configured pi executable is unavailable")
	}
	digest, err := digestFile(a.executable)
	if err != nil {
		return executableIdentity{}, err
	}
	version, err := readBinaryVersion(ctx, a.executable)
	if err != nil {
		return executableIdentity{}, err
	}
	return executableIdentity{a.executable, digest, version}, nil
}

// readBinaryVersion runs `<executable> --version` inside the sanitized probe
// environment and parses the version string reported by the binary.
func readBinaryVersion(ctx context.Context, executable string) (string, error) {
	command := exec.CommandContext(ctx, executable, "--version")
	command.Env = probeEnvironment()
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("probe pi version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", errors.New("pi returned an empty version")
	}
	return version, nil
}

// identifyTimeout bounds every advisory Identify call so doctor discovery can
// never hang on an unresponsive candidate binary.
const identifyTimeout = 10 * time.Second

// Identify pins the version and SHA256 digest of an absolute candidate
// executable, reusing the probe's sanitized environment and version parsing.
// It is advisory identity collection shared by doctor discovery and future
// tooling; it never registers the adapter, writes files, or touches Marshal
// state.
func Identify(executable string) (version, digest string, err error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", "", errors.New("pi candidate must be an absolute clean path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("pi candidate is not an executable regular file")
	}
	digest, err = digestFile(executable)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), identifyTimeout)
	defer cancel()
	version, err = readBinaryVersion(ctx, executable)
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

// LaunchPlan is the immutable launch plan for one pi attempt. Every field is
// frozen by PrepareLaunch before any process starts, so an external executor
// (for example a sandbox allocation) can run the exact argv/env/cwd Marshal
// would have used and then hand the captured stdout/stderr and exit
// disposition back to CompleteLaunch.
type LaunchPlan struct {
	ExecArgv              []string // executable path + args (argv[0] absolute executable)
	Environment           []string // complete env block ("K=V") from workerEnvironment semantics
	WorkingDirectory      string   // resolved worktree absolute path
	AttemptTimeoutSeconds int64
	ResultPath            string // declared worker-result path under ControlRoot (absolute, validated)
	ControlRoot           string // absolute, validated
	SessionPolicy         string
	MaxOutputBytes        int64

	// request, model, identity, and attemptDeadline are the private bindings
	// frozen by PrepareLaunch that CompleteLaunch consumes: the decoded
	// WorkerRequest (task/run/attempt identity for the result identity
	// check), the TaskSpec model, the inspected executable identity written
	// into the normalized WorkerResult, and the attempt deadline whose start
	// instant matches the historical Run timeout creation point. A plan whose
	// private bindings are missing or inconsistent was not produced by
	// PrepareLaunch and is rejected by CompleteLaunch.
	request         workerRequest
	model           string
	identity        executableIdentity
	attemptDeadline time.Time
}

// PrepareLaunch performs every precompute Run performed before starting the
// worker process, and nothing else: WorkerRequest decode and validation,
// adapter/profile/session-policy fail-closed gates, executable identity
// inspection with the exact supported-binary version gate (inspect keeps its
// bounded `<executable> --version` probe and stays cheaply re-runnable),
// prompt read, result path lexical validation, model read, tool allowlist
// resolution, hardened argv construction, and the sanitized worker
// environment. It never starts the worker process: Run's spawn seam is only
// consumed after a plan is returned.
func (a *Adapter) PrepareLaunch(ctx context.Context, record domain.Record) (*LaunchPlan, error) {
	if record.Kind != domain.KindWorkerRequest {
		return nil, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return nil, err
	}
	if request.AdapterID != adapterID || (request.ExecutionProfile != "workspace-write" && request.ExecutionProfile != "read-only") {
		return nil, errors.New("WorkerRequest does not match the pi adapter execution profile")
	}
	// Fail-closed: persist would write into the user's default pi session
	// directory (outside the managed state boundary) and WorkerRequest has
	// no managed sessionDir/mapping, so cross-attempt resume cannot be done
	// safely. Both are permanent, unsupported errors; never launch a process.
	if request.SessionPolicy != "ephemeral" {
		return nil, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
	}
	// The deadline is computed at exactly the point Run historically created
	// its attempt timeout context (after the session-policy gate, before
	// inspect), so Run's context.WithDeadline reproduces the legacy
	// context.WithTimeout coverage byte-for-byte.
	attemptDeadline := time.Now().Add(time.Duration(request.AttemptTimeoutSeconds) * time.Second)
	inspectCtx, cancel := context.WithDeadline(ctx, attemptDeadline)
	defer cancel()
	identity, err := a.inspect(inspectCtx)
	if err != nil {
		return nil, err
	}
	if !isSupportedBinary(identity.version) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	worktree, err := filepath.EvalSymlinks(request.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree: %w", err)
	}
	if !filepath.IsAbs(worktree) {
		return nil, errors.New("worktree path must be absolute")
	}
	controlRoot, err := filepath.EvalSymlinks(request.ControlRoot)
	if err != nil || !filepath.IsAbs(controlRoot) {
		return nil, errors.New("control root must be an existing absolute directory")
	}
	promptPath, err := existingPathWithin(controlRoot, request.PromptPath)
	if err != nil {
		return nil, fmt.Errorf("resolve prompt: %w", err)
	}
	prompt, err := readBounded(promptPath, maxPromptBytes)
	if err != nil {
		return nil, fmt.Errorf("read prompt: %w", err)
	}
	resultPath, err := lexicalPathWithin(controlRoot, request.ResultPath)
	if err != nil {
		return nil, fmt.Errorf("resolve result: %w", err)
	}
	model := readModel(controlRoot, request.TaskSpecPath)
	tools, err := declaredWorkerTools(controlRoot, request.TaskSpecPath)
	if err != nil {
		return nil, err
	}
	args, err := buildArgsWithTools(request.ExecutionProfile, model, string(prompt), tools)
	if err != nil {
		return nil, err
	}
	return &LaunchPlan{
		ExecArgv:              append([]string{identity.path}, args...),
		Environment:           workerEnvironment(worktree),
		WorkingDirectory:      worktree,
		AttemptTimeoutSeconds: int64(request.AttemptTimeoutSeconds),
		ResultPath:            resultPath,
		ControlRoot:           controlRoot,
		SessionPolicy:         request.SessionPolicy,
		MaxOutputBytes:        int64(request.MaxOutputBytes),
		request:               request,
		model:                 model,
		identity:              identity,
		attemptDeadline:       attemptDeadline,
	}, nil
}

// BinaryVersion 报告由 PrepareLaunch 的版本门验收过的 executable 版本。
// 仅对 PrepareLaunch 产出的 plan 有意义；零值 plan 返回空串。
func (p *LaunchPlan) BinaryVersion() string {
	if p == nil {
		return ""
	}
	return p.identity.version
}

// executionOutcome carries every observation of one executed attempt that the
// completion pipeline consumes, independent of how the process was executed:
// Run feeds the live capture directly, CompleteLaunch reconstructs it from
// the sandbox-returned transcript and exit disposition.
type executionOutcome struct {
	capture       captureResult
	stderr        streamCapture
	exitCode      int
	signal        string
	processFailed bool // mirrors a non-nil Wait error: nonzero exit or signaled
	ctxErr        error
	started       time.Time
	completed     time.Time
}

// CompleteLaunch drives the entire post-execution pipeline Run performs after
// the worker process has started, given the full bounded stdout transcript
// and the exit disposition reported by an external executor. It writes the
// same bounded artifacts Run writes (pi-transcript.jsonl, pi-stderr.log,
// pi-transcript-meta.json, denials.jsonl) at the same paths, grades denials,
// applies the identical output-limit/context-error/permission-denied/
// process/provider failure precedence, reads the declared WorkerResult via
// readDeclaredResult, normalizes it, and returns the final WorkerResult
// record. It never spawns, signals, or kills a process.
//
// Input contract (every violation fails closed before any artifact write):
//   - plan must be non-nil and produced by PrepareLaunch: absolute argv[0]
//     equal to the inspected executable, non-empty absolute
//     WorkingDirectory/ControlRoot/ResultPath, and intact private bindings;
//   - exitCode must be in [-1, 255], the POSIX wait representation: -1 means
//     the process did not exit normally; signal must be empty unless
//     exitCode == -1, because a signaled wait status reports ExitCode() == -1;
//   - when ctxErr is nil the timing evidence must be complete and ordered:
//     started and completed non-zero and completed not before started. A
//     non-nil ctxErr (attempt deadline hit or cancellation) tolerates missing
//     or unordered timing evidence, mirroring Run where started/completed are
//     always present but the deadline error stays authoritative either way;
//   - transcriptJSONL is the full captured stdout already bounded by the
//     executor to at most plan.MaxOutputBytes. It is re-decoded through the
//     identical strict session-protocol state machine under the same byte
//     limit, so a malformed or truncated stream fails closed exactly as Run's
//     live capture does (an empty transcript of a nominally successful
//     attempt fails closed with ErrProtocol). Retry backoff declarations are
//     decoded and validated but never paced: the executor already waited them;
//   - stdoutTruncated is the executor's authoritative output-limit signal;
//     the reported truncation is stdoutTruncated OR the decoder's own
//     limit verdict, so truncation evidence can never be discarded;
//   - stderrBytes is the raw captured stderr; CompleteLaunch applies the same
//     bounded captureStream Run applies to the live stream, so a caller that
//     hands the unbounded stream reproduces Run's stderr artifact and
//     truncation flag exactly (a caller that pre-truncates to the bound loses
//     only the truncation flag for streams longer than the bound);
//   - started/completed/exitCode/signal are the caller-provided deterministic
//     substitutes for Run's clock and wait observations and are stamped into
//     the normalized WorkerResult and metadata verbatim.
func (a *Adapter) CompleteLaunch(ctx context.Context, plan *LaunchPlan, transcriptJSONL []byte, stdoutTruncated bool, stderrBytes []byte, started, completed time.Time, exitCode int, signal string, ctxErr error) (domain.Record, error) {
	if err := validateCompletionInput(plan, started, completed, exitCode, signal, ctxErr); err != nil {
		return domain.Record{}, err
	}
	capture := decodeTranscript(ctx, transcriptJSONL, plan.WorkingDirectory, plan.MaxOutputBytes)
	if stdoutTruncated {
		capture.limitExceeded = true
	}
	return a.completeAttempt(plan, executionOutcome{
		capture:       capture,
		stderr:        captureStream(bytes.NewReader(stderrBytes), stderrLimit),
		exitCode:      exitCode,
		signal:        signal,
		processFailed: exitCode != 0 || signal != "",
		ctxErr:        ctxErr,
		started:       started,
		completed:     completed,
	})
}

// validateCompletionInput enforces the CompleteLaunch input contract.
func validateCompletionInput(plan *LaunchPlan, started, completed time.Time, exitCode int, signal string, ctxErr error) error {
	if plan == nil {
		return errors.New("LaunchPlan is nil")
	}
	if plan.attemptDeadline.IsZero() || plan.identity.path == "" || plan.request.AttemptID == "" {
		return errors.New("LaunchPlan was not produced by PrepareLaunch")
	}
	if len(plan.ExecArgv) == 0 || !filepath.IsAbs(plan.ExecArgv[0]) || plan.ExecArgv[0] != plan.identity.path {
		return errors.New("LaunchPlan argv does not match the inspected executable")
	}
	if !filepath.IsAbs(plan.WorkingDirectory) || !filepath.IsAbs(plan.ControlRoot) || !filepath.IsAbs(plan.ResultPath) {
		return errors.New("LaunchPlan paths must be absolute")
	}
	if exitCode < -1 || exitCode > 255 {
		return fmt.Errorf("exit disposition out of POSIX wait range: exit=%d", exitCode)
	}
	if signal != "" && exitCode != -1 {
		return fmt.Errorf("signaled exit must report exitCode -1, got exit=%d signal=%s", exitCode, signal)
	}
	if ctxErr == nil && (started.IsZero() || completed.IsZero() || completed.Before(started)) {
		return errors.New("timing evidence is incomplete or unordered without an attempt context error")
	}
	return nil
}

// decodeTranscript runs the strict session-protocol state machine over a
// fully captured transcript without pacing retry backoffs and without a
// process to terminate (CompleteLaunch never spawns or kills a process).
func decodeTranscript(ctx context.Context, transcript []byte, worktree string, limit int64) captureResult {
	return captureTranscript(ctx, bytes.NewReader(transcript), worktree, limit, func() {}, false)
}

// completeAttempt is the single post-execution pipeline shared by Run and
// CompleteLaunch: bounded transcript/meta/denial artifacts at the frozen
// paths, denial grading, and the frozen error precedence that ends in the
// normalized WorkerResult record.
func (a *Adapter) completeAttempt(plan *LaunchPlan, outcome executionOutcome) (domain.Record, error) {
	request := plan.request
	identity := plan.identity
	resultPath := plan.ResultPath
	capture := outcome.capture
	started, completed := outcome.started, outcome.completed
	transcriptPath := filepath.Join(filepath.Dir(resultPath), "pi-transcript.jsonl")
	if err := atomicWrite(transcriptPath, capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "pi-stderr.log"), outcome.stderr.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	denialRecords := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: plan.WorkingDirectory, ControlRoot: plan.ControlRoot, TempDir: os.TempDir()}, capture.denials, a.now)
	fatalDenials := denials.CountFatal(denialRecords)
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "eventCount": capture.eventCount,
		"toolCalls": capture.toolCalls, "inputTokens": capture.inputTokens,
		"outputTokens": capture.outputTokens, "cachedInputTokens": capture.cachedInputTokens,
		"cost": capture.cost, "capturedBytes": len(capture.raw),
		"outputTruncated": capture.limitExceeded, "permissionDenied": fatalDenials > 0,
		"denialsBenign": len(denialRecords) - fatalDenials, "denialsFatal": fatalDenials, "toolNames": denials.SortedToolNames(capture.toolNames),
		"exitCode": outcome.exitCode, "signal": outcome.signal, "stderrBytes": len(outcome.stderr.data), "stderrTruncated": outcome.stderr.truncated,
		"contextError": contextErrorOf(outcome.ctxErr),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, err
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "pi-transcript-meta.json"), append(metadata, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript metadata: %w", err)
	}
	if err := denials.AppendLog(filepath.Join(filepath.Dir(resultPath), denials.LogFileName), denialRecords); err != nil {
		return domain.Record{}, fmt.Errorf("write denial log: %w", err)
	}
	if outcome.ctxErr != nil {
		return domain.Record{}, outcome.ctxErr
	}
	if capture.limitExceeded {
		return domain.Record{}, ErrOutputLimit
	}
	if capture.err != nil {
		return domain.Record{}, capture.err
	}
	// Fatal permission denial stays authoritative over a concurrent provider
	// or nonzero process failure, and every provider/process terminal failure
	// is returned before Marshal reads any pre-written, stale, or partial
	// WorkerResult.
	if fatalDenials > 0 {
		return domain.Record{}, ErrPermissionDenied
	}
	if outcome.processFailed {
		return domain.Record{}, processFailureError(outcome.exitCode, outcome.signal)
	}
	if capture.providerFailed {
		return domain.Record{}, ErrProviderFailed
	}
	if capture.sessionID == "" {
		return domain.Record{}, fmt.Errorf("%w: session id is missing", ErrProtocol)
	}
	declared, err := readDeclaredResult(resultPath, int64(maxResultBytes), a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	if declared.TaskID != request.TaskID || declared.RunID != request.RunID || declared.AttemptID != request.AttemptID || declared.Adapter.ID != adapterID {
		return domain.Record{}, errors.New("WorkerResult identity does not match WorkerRequest")
	}
	if declared.Session != nil && declared.Session.ID != "" && declared.Session.ID != capture.sessionID {
		return domain.Record{}, errors.New("WorkerResult session does not match transcript")
	}
	declared.Adapter.Executable, declared.Adapter.Version = identity.path, identity.version
	declared.Session = &declaredSession{ID: capture.sessionID, Resumable: false}
	declared.StartedAt, declared.CompletedAt = started, completed
	if plan.model != "" {
		declared.Adapter.Model = plan.model
	}
	if capture.inputTokens > 0 || capture.outputTokens > 0 || capture.cost > 0 {
		usage := map[string]any{"inputTokens": capture.inputTokens, "outputTokens": capture.outputTokens, "cachedInputTokens": capture.cachedInputTokens}
		if capture.cost > 0 {
			usage["cost"], usage["currency"] = capture.cost, "USD"
		}
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
	if err := atomicWrite(resultPath, append(data, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write normalized WorkerResult: %w", err)
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, nil
}

// Run executes one non-interactive attempt as a thin composition of
// PrepareLaunch, the local spawn/capture (including the process-group kill
// guarantee, which lives only here), and the shared completion pipeline.
// Provider/process/protocol failures are returned as errors so Core can apply
// the operational retry budget.
func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	plan, err := a.PrepareLaunch(ctx, record)
	if err != nil {
		return domain.Record{}, err
	}
	runCtx, cancel := context.WithDeadline(ctx, plan.attemptDeadline)
	defer cancel()
	command := exec.Command(plan.ExecArgv[0], plan.ExecArgv[1:]...)
	command.Dir = plan.WorkingDirectory
	command.Env = plan.Environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return domain.Record{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return domain.Record{}, err
	}
	started := a.now().UTC()
	if err := a.startCommand(command); err != nil {
		return domain.Record{}, fmt.Errorf("start pi: %w", err)
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { terminateGroup(command) }) }
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() { stdoutDone <- captureJSONL(runCtx, stdout, plan.WorkingDirectory, plan.MaxOutputBytes, kill) }()
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
	exitCode, signal := processOutcome(command)
	return a.completeAttempt(plan, executionOutcome{
		capture:       capture,
		stderr:        stderrCapture,
		exitCode:      exitCode,
		signal:        signal,
		processFailed: waitErr != nil,
		ctxErr:        runCtx.Err(),
		started:       started,
		completed:     completed,
	})
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

// NormalizeDeclaredWorkerResult drops the optional session field from a
// declared WorkerResult when the declared session is unusable: the value is
// not a JSON object, the id is missing or an empty string, or the resumable
// flag is missing or not a boolean. Marshal overwrites session metadata with
// observed values afterwards, so an invalid optional field must not
// fail-closed the entire attempt. Inputs that cannot be parsed as a JSON
// object are returned unchanged so schema validation fails exactly as before,
// and every remaining field is preserved byte-for-byte.
func NormalizeDeclaredWorkerResult(data []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return data
	}
	session, present := fields["session"]
	if !present || declaredSessionValid(session) {
		return data
	}
	delete(fields, "session")
	normalized, err := json.Marshal(fields)
	if err != nil {
		return data
	}
	return normalized
}

func declaredSessionValid(session json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(session, &fields); err != nil || fields == nil {
		return false
	}
	idRaw, present := fields["id"]
	if !present {
		return false
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err == nil && id == "" {
		return false
	}
	resumableRaw, present := fields["resumable"]
	if !present {
		return false
	}
	var resumable *bool
	if err := json.Unmarshal(resumableRaw, &resumable); err != nil || resumable == nil {
		return false
	}
	return true
}

func readDeclaredResult(path string, limit int64, validator *contract.Validator) (declaredResult, error) {
	data, err := readBounded(path, limit)
	if err != nil {
		return declaredResult{}, fmt.Errorf("read WorkerResult declaration: %w", err)
	}
	data = NormalizeDeclaredWorkerResult(data)
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

type captureResult struct {
	raw               []byte
	sessionID         string
	eventCount        int
	toolCalls         int
	inputTokens       int
	outputTokens      int
	cachedInputTokens int
	cost              float64
	denials           []denials.RawDenial
	toolNames         []string
	limitExceeded     bool
	providerFailed    bool
	err               error
}

// piEvent covers only the fields Marshal validates. Unknown fields are
// ignored on purpose; protocol decisions rely solely on type, version, id,
// cwd, the explicit agent_end willRetry flag, and the auto_retry attempt
// bookkeeping, including the declared backoff delayMs that the capture paces
// as a cancellable wait. Free-text errorMessage is decoded only to validate
// required/non-empty wire shape; its contents remain confined to the raw
// transcript and never reach authorization, budgets, or diagnostics.
type piEvent struct {
	Type         string          `json:"type"`
	Version      *int            `json:"version"`
	ID           string          `json:"id"`
	Cwd          string          `json:"cwd"`
	ToolName     string          `json:"toolName"`
	ToolCallID   string          `json:"toolCallId"`
	Args         json.RawMessage `json:"args"`
	IsError      *bool           `json:"isError"`
	Error        string          `json:"error"`
	ErrorMessage *string         `json:"errorMessage"`
	WillRetry    *bool           `json:"willRetry"`
	Attempt      *int            `json:"attempt"`
	MaxAttempts  *int            `json:"maxAttempts"`
	DelayMs      json.Number     `json:"delayMs"`
	Success      *bool           `json:"success"`
	Reason       string          `json:"reason"`
	Source       string          `json:"source"`
	Aborted      *bool           `json:"aborted"`
	Result       json.RawMessage `json:"result"`
	Messages     []piMessage     `json:"messages"`
}

type piMessage struct {
	Role       string   `json:"role"`
	StopReason *string  `json:"stopReason"`
	Usage      *piUsage `json:"usage"`
}

type piUsage struct {
	Input        int       `json:"input"`
	Output       int       `json:"output"`
	CacheRead    int       `json:"cacheRead"`
	CacheWrite   int       `json:"cacheWrite"`
	CacheWrite1h *int      `json:"cacheWrite1h,omitempty"`
	Reasoning    *int      `json:"reasoning,omitempty"`
	TotalTokens  int       `json:"totalTokens"`
	Cost         usageCost `json:"cost"`
}

type piCompactionResult struct {
	Summary              *string            `json:"summary"`
	FirstKeptEntryID     *string            `json:"firstKeptEntryId"`
	TokensBefore         *int               `json:"tokensBefore"`
	EstimatedTokensAfter *int               `json:"estimatedTokensAfter,omitempty"`
	Usage                *piCompactionUsage `json:"usage,omitempty"`
	Details              json.RawMessage    `json:"details,omitempty"`
}

type piCompactionUsage struct {
	Input        *int       `json:"input"`
	Output       *int       `json:"output"`
	CacheRead    *int       `json:"cacheRead"`
	CacheWrite   *int       `json:"cacheWrite"`
	CacheWrite1h *int       `json:"cacheWrite1h,omitempty"`
	Reasoning    *int       `json:"reasoning,omitempty"`
	TotalTokens  *int       `json:"totalTokens"`
	Cost         *usageCost `json:"cost"`
}

// usageCost accepts the two cost encodings emitted by the pinned Pi protocol:
// a legacy number, or a structured cost breakdown. It intentionally rejects
// every other JSON shape so accounting evidence cannot be silently discarded.
type usageCost struct{ value float64 }

func (c *usageCost) UnmarshalJSON(data []byte) error {
	value, err := decodeUsageCost(data)
	if err != nil {
		return err
	}
	c.value = value
	return nil
}

func decodeUsageCost(data []byte) (float64, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return 0, fmt.Errorf("decode usage cost: %w", err)
	}
	switch value := token.(type) {
	case json.Number:
		return finiteNonNegativeCost(value)
	case json.Delim:
		if value != '{' {
			return 0, errors.New("usage cost must be a number or object")
		}
		return decodeUsageCostObject(decoder)
	default:
		return 0, errors.New("usage cost must be a number or object")
	}
}

func decodeUsageCostObject(decoder *json.Decoder) (float64, error) {
	seen := map[string]bool{}
	var total, components float64
	var hasTotal bool
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("decode usage cost key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] {
			return 0, errors.New("usage cost object contains an ambiguous field")
		}
		seen[key] = true
		switch key {
		case "input", "output", "cacheRead", "cacheWrite", "total":
		default:
			return 0, fmt.Errorf("usage cost object contains unknown field %q", key)
		}
		valueToken, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("decode usage cost field %q: %w", key, err)
		}
		number, ok := valueToken.(json.Number)
		if !ok {
			return 0, fmt.Errorf("usage cost field %q must be a number", key)
		}
		amount, err := finiteNonNegativeCost(number)
		if err != nil {
			return 0, fmt.Errorf("usage cost field %q: %w", key, err)
		}
		if key == "total" {
			total, hasTotal = amount, true
		} else {
			components += amount
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, fmt.Errorf("close usage cost object: %w", err)
	}
	if len(seen) == 0 {
		return 0, errors.New("usage cost object must not be empty")
	}
	if !isFinite(components) {
		return 0, errors.New("usage cost component sum is not finite")
	}
	if hasTotal {
		// Pi defines total as authoritative. Components are only summed when
		// total is absent, avoiding false protocol failures from provider-side
		// rounding while still validating every supplied component.
		return total, nil
	}
	return components, nil
}

func finiteNonNegativeCost(number json.Number) (float64, error) {
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || !isFinite(value) || value < 0 {
		return 0, errors.New("usage cost must be finite and non-negative")
	}
	return value, nil
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func decodeCompactionResult(raw json.RawMessage) (*piCompactionResult, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var result piCompactionResult
	if err := decoder.Decode(&result); err != nil {
		return nil, false, fmt.Errorf("decode compaction result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false, errors.New("compaction result contains trailing data")
	}
	if result.Summary == nil || strings.TrimSpace(*result.Summary) == "" ||
		result.FirstKeptEntryID == nil || strings.TrimSpace(*result.FirstKeptEntryID) == "" ||
		result.TokensBefore == nil || *result.TokensBefore < 0 ||
		(result.EstimatedTokensAfter != nil && *result.EstimatedTokensAfter < 0) {
		return nil, false, errors.New("compaction result is missing required bounded metadata")
	}
	if usage := result.Usage; usage != nil {
		if usage.Input == nil || usage.Output == nil || usage.CacheRead == nil || usage.CacheWrite == nil ||
			usage.TotalTokens == nil || usage.Cost == nil || *usage.Input < 0 || *usage.Output < 0 ||
			*usage.CacheRead < 0 || *usage.CacheWrite < 0 || *usage.TotalTokens < 0 ||
			(usage.CacheWrite1h != nil && *usage.CacheWrite1h < 0) || (usage.Reasoning != nil && *usage.Reasoning < 0) {
			return nil, false, errors.New("compaction usage is incomplete or negative")
		}
	}
	return &result, true, nil
}

// captureState is one node of the explicit closed state machine that
// authorizes every Pi session event during capture. Each event kind is
// authorized in exactly one state; unknown, duplicate, or out-of-order
// events, and any event observed after a terminal or closed state, fail
// closed with ErrProtocol.
type captureState int

const (
	stateActive captureState = iota
	stateAwaitingAutoRetryStart
	stateRetryActive
	stateAwaitingFinalAgentEnd
	stateAwaitingRetryFailureEnd
	statePostAgentEnd
	stateCompacting
	stateAwaitingCompactionContinuation
	stateTerminalProviderFailure
	stateTerminalSettled
)

func (s captureState) String() string {
	switch s {
	case stateActive:
		return "active"
	case stateAwaitingAutoRetryStart:
		return "awaiting-auto-retry-start"
	case stateRetryActive:
		return "retry-active"
	case stateAwaitingFinalAgentEnd:
		return "awaiting-final-agent-end"
	case stateAwaitingRetryFailureEnd:
		return "awaiting-retry-failure-end"
	case statePostAgentEnd:
		return "post-agent-end"
	case stateCompacting:
		return "compacting"
	case stateAwaitingCompactionContinuation:
		return "awaiting-compaction-continuation"
	case stateTerminalProviderFailure:
		return "terminal-provider-failure"
	case stateTerminalSettled:
		return "terminal-settled"
	default:
		return "unspecified"
	}
}

// closed reports whether EOF completes the stream successfully in this state.
func (s captureState) closed() bool {
	switch s {
	case statePostAgentEnd, stateTerminalProviderFailure, stateTerminalSettled:
		return true
	default:
		return false
	}
}

// agentFailureStopReasons are the final-invocation stop reasons that turn a
// syntactically complete stream into a stable Provider failure.
var agentFailureStopReasons = map[string]bool{"error": true, "aborted": true, "length": true}

var activeWorkEvents = map[string]bool{
	"agent_start": true, "turn_start": true, "turn_end": true,
	"message_start": true, "message_update": true, "message_end": true,
	"bash_execution_update": true,
	"tool_execution_start":  true, "tool_execution_update": true, "tool_execution_end": true,
	"queue_update": true, "entry_appended": true, "session_info_changed": true,
	"thinking_level_changed": true, "extension_error": true,
}

// addUsageCount rejects negative usage counters first and accumulates with an
// explicit overflow decision instead of wrapping.
func addUsageCount(current, delta int) (int, error) {
	if delta < 0 {
		return 0, fmt.Errorf("%w: usage counters must be non-negative", ErrProtocol)
	}
	sum := int64(current) + int64(delta)
	if sum > math.MaxInt {
		return 0, fmt.Errorf("%w: usage counter sum overflows", ErrProtocol)
	}
	return int(sum), nil
}

// finalAssistantStopReason reports the explicit stopReason of the last
// assistant message. explicit is false when there is no assistant message or
// when the last assistant message carries no stopReason at all.
func finalAssistantStopReason(messages []piMessage) (string, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "assistant" {
			continue
		}
		if messages[index].StopReason == nil {
			return "", false
		}
		return *messages[index].StopReason, true
	}
	return "", false
}

// eofClosureError classifies a stream that ends before reaching a closed
// state, using only stable reason codes.
func eofClosureError(state captureState) error {
	switch state {
	case stateAwaitingAutoRetryStart:
		return fmt.Errorf("%w: stream ended before auto_retry_start", ErrProtocol)
	case stateAwaitingFinalAgentEnd:
		return fmt.Errorf("%w: stream ended before the final agent_end", ErrProtocol)
	case stateAwaitingRetryFailureEnd:
		return fmt.Errorf("%w: stream ended before the auto_retry_end failure closure", ErrProtocol)
	case stateCompacting:
		return fmt.Errorf("%w: stream ended before compaction_end", ErrProtocol)
	case stateAwaitingCompactionContinuation:
		return fmt.Errorf("%w: stream ended before the compaction continuation", ErrProtocol)
	default:
		return fmt.Errorf("%w: stream ended without terminal agent_end in state %s", ErrProtocol, state)
	}
}

// maxBackoffDelayMs bounds the auto_retry backoff window a capture paces;
// larger declarations fail closed like every other out-of-bounds retry
// bookkeeping value, and the bound keeps the timer duration representable.
const maxBackoffDelayMs = int64(math.MaxInt64 / int64(time.Millisecond))

// captureJSONL enforces the strict pi session protocol through an explicit
// closed state machine:
//   - the first event must be the session header with version exactly 3 and
//     cwd equal to the resolved attempt worktree;
//   - every event must arrive as a complete LF-terminated JSON fragment; each
//     fragment is appended to the raw evidence byte-for-byte in input order,
//     whitespace around valid JSON is accepted and preserved exactly, and a
//     blank fragment is not an event and fails closed;
//   - every agent_end carries an explicit willRetry flag. willRetry=true is
//     only valid when the last assistant message stopped with stopReason
//     error, and the next event must be the matching auto_retry_start with a
//     strictly ordered, constant attempt budget. A successful closure requires
//     auto_retry_end(success=true) followed by exactly one final
//     agent_end(willRetry=false); a failed final invocation inside an active
//     retry must be followed by the matching auto_retry_end(success=false);
//   - an authorized auto_retry_start opens the declared backoff window: the
//     capture paces delayMs itself through a select between the attempt
//     context and the backoff timer, so a cancellation ends the wait and the
//     capture immediately with the context error instead of idling out the
//     window, while an intact window admits every later byte unchanged;
//   - agent_end closes only one low-level run. A non-retrying agent_end may be
//     followed by an ordered automatic compaction and continuation; only EOF
//     or agent_settled closes the session. Compaction reason, nesting,
//     aborted/willRetry flags, and continuation ordering are validated rather
//     than ignored;
//   - termination is the complete closed stream; any further event or any
//     non-LF tail fails closed, admits no later byte, and terminates the
//     process group exactly once.
//
// Output is bounded; exceeding the limit keeps raw exactly equal to the first
// limit input bytes and terminates exactly once without fabricating a
// protocol success.
//
// captureJSONL is the live-capture entrypoint used by Run: it paces declared
// retry backoffs against the attempt context and terminates the process.
func captureJSONL(ctx context.Context, reader io.Reader, worktree string, limit int64, onLimit func()) captureResult {
	return captureTranscript(ctx, reader, worktree, limit, onLimit, true)
}

// captureTranscript is the shared strict session-protocol machine. When
// paceBackoff is false (offline decode via CompleteLaunch) declared backoff
// windows are validated but never waited, because the executor already paced
// them while the bytes were produced; the decoded result is otherwise
// identical.
func captureTranscript(ctx context.Context, reader io.Reader, worktree string, limit int64, onLimit func(), paceBackoff bool) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	if capacity < 0 {
		capacity = 0
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var received int64
	var line []byte
	state := stateActive
	retryAttempt := 0
	retryMaxAttempts := 0
	pendingProviderFailure := false
	pendingStopReason := ""
	compactionReason := ""
	overflowRecoverySeen := false
	summarizationRetryAttempt := 0
	summarizationRetryMaxAttempts := 0
	summarizationRetryPhase := 0
	pending := map[string]json.RawMessage{}
	terminated := false
	terminate := func() {
		if !terminated {
			terminated = true
			onLimit()
		}
	}
	fail := func(reason error) {
		if result.err == nil {
			result.err = reason
			terminate()
		}
	}
	unauthorized := func() {
		fail(fmt.Errorf("%w: event is not authorized in state %s", ErrProtocol, state))
	}
	aborted := false
	// waitBackoff paces the backoff window declared by an authorized
	// auto_retry_start. The wait is a select between the attempt context and
	// the backoff timer, so a cancellation terminates the window immediately
	// instead of idling out the delay: the capture records the context error,
	// terminates the process group exactly once, and stops admitting bytes.
	waitBackoff := func(delayMs int64) {
		if delayMs <= 0 || !paceBackoff {
			return
		}
		timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			aborted = true
			if result.err == nil {
				result.err = ctx.Err()
			}
			terminate()
		}
	}
	accumulateOneUsage := func(inputDelta, outputDelta, cacheReadDelta int, costDelta float64) bool {
		input, err := addUsageCount(result.inputTokens, inputDelta)
		if err != nil {
			fail(err)
			return false
		}
		output, err := addUsageCount(result.outputTokens, outputDelta)
		if err != nil {
			fail(err)
			return false
		}
		cacheRead, err := addUsageCount(result.cachedInputTokens, cacheReadDelta)
		if err != nil {
			fail(err)
			return false
		}
		nextCost := result.cost + costDelta
		if !isFinite(nextCost) {
			fail(fmt.Errorf("%w: usage cost sum is not finite", ErrProtocol))
			return false
		}
		result.inputTokens, result.outputTokens, result.cachedInputTokens = input, output, cacheRead
		result.cost = nextCost
		return true
	}
	accumulateUsage := func(messages []piMessage) bool {
		for _, message := range messages {
			if message.Role != "assistant" || message.Usage == nil {
				continue
			}
			if !accumulateOneUsage(message.Usage.Input, message.Usage.Output, message.Usage.CacheRead, message.Usage.Cost.value) {
				return false
			}
		}
		return true
	}
	acceptAgentEnd := func(event *piEvent) {
		if event.WillRetry == nil {
			fail(fmt.Errorf("%w: agent_end must carry an explicit willRetry flag", ErrProtocol))
			return
		}
		if !accumulateUsage(event.Messages) {
			return
		}
		stopReason, explicit := finalAssistantStopReason(event.Messages)
		if *event.WillRetry {
			if !explicit || stopReason != "error" {
				fail(fmt.Errorf("%w: retryable agent_end requires a final assistant message with stopReason error", ErrProtocol))
				return
			}
			switch state {
			case stateActive:
				state = stateAwaitingAutoRetryStart
			case stateRetryActive:
				if retryAttempt >= retryMaxAttempts {
					fail(fmt.Errorf("%w: retry budget is exhausted before agent_end willRetry=true", ErrProtocol))
					return
				}
				state = stateAwaitingAutoRetryStart
			default:
				unauthorized()
			}
			return
		}
		failingStop := explicit && agentFailureStopReasons[stopReason]
		switch state {
		case stateActive, stateAwaitingFinalAgentEnd:
			pendingProviderFailure = failingStop
			pendingStopReason = stopReason
			if !failingStop {
				overflowRecoverySeen = false
			}
			state = statePostAgentEnd
		case stateRetryActive:
			if !failingStop {
				fail(fmt.Errorf("%w: retry-active success closure requires a matching auto_retry_end first", ErrProtocol))
				return
			}
			pendingProviderFailure = true
			pendingStopReason = stopReason
			state = stateAwaitingRetryFailureEnd
		default:
			unauthorized()
		}
	}
	acceptAutoRetryStart := func(event *piEvent) {
		if event.Attempt == nil || event.MaxAttempts == nil {
			fail(fmt.Errorf("%w: auto_retry_start requires explicit attempt and maxAttempts", ErrProtocol))
			return
		}
		attempt, maxAttempts := *event.Attempt, *event.MaxAttempts
		if attempt < 1 {
			fail(fmt.Errorf("%w: auto_retry_start attempt must be at least 1", ErrProtocol))
			return
		}
		if maxAttempts < 1 || maxAttempts > 3 {
			fail(fmt.Errorf("%w: auto_retry_start maxAttempts must stay within 1..3", ErrProtocol))
			return
		}
		if attempt > maxAttempts {
			fail(fmt.Errorf("%w: auto_retry_start attempt exceeds maxAttempts", ErrProtocol))
			return
		}
		if retryMaxAttempts != 0 && maxAttempts != retryMaxAttempts {
			fail(fmt.Errorf("%w: auto_retry_start maxAttempts changed mid-chain", ErrProtocol))
			return
		}
		if attempt != retryAttempt+1 {
			fail(fmt.Errorf("%w: auto_retry_start attempt must increment by exactly one", ErrProtocol))
			return
		}
		var backoffMs int64
		if event.DelayMs != "" {
			delay, delayErr := strconv.ParseInt(string(event.DelayMs), 10, 64)
			if delayErr != nil || delay < 0 || delay > maxBackoffDelayMs {
				fail(fmt.Errorf("%w: auto_retry_start delayMs must be a bounded non-negative integer", ErrProtocol))
				return
			}
			backoffMs = delay
		}
		retryAttempt, retryMaxAttempts = attempt, maxAttempts
		state = stateRetryActive
		waitBackoff(backoffMs)
	}
	acceptAutoRetryEnd := func(event *piEvent) {
		if event.Success == nil || event.Attempt == nil {
			fail(fmt.Errorf("%w: auto_retry_end requires explicit success and attempt", ErrProtocol))
			return
		}
		if *event.Attempt != retryAttempt {
			fail(fmt.Errorf("%w: auto_retry_end attempt does not match the current attempt", ErrProtocol))
			return
		}
		if *event.Success {
			if state != stateRetryActive {
				unauthorized()
				return
			}
			state = stateAwaitingFinalAgentEnd
			return
		}
		if state != stateAwaitingRetryFailureEnd {
			unauthorized()
			return
		}
		state = statePostAgentEnd
	}
	acceptCompactionStart := func(event *piEvent) {
		if event.Reason != "threshold" && event.Reason != "overflow" {
			fail(fmt.Errorf("%w: automatic compaction reason must be threshold or overflow", ErrProtocol))
			return
		}
		if compactionReason != "" {
			fail(fmt.Errorf("%w: compaction_start cannot be nested or repeated", ErrProtocol))
			return
		}
		compactionReason = event.Reason
		summarizationRetryAttempt = 0
		summarizationRetryMaxAttempts = 0
		summarizationRetryPhase = 0
		state = stateCompacting
	}
	acceptSummarizationRetryScheduled := func(event *piEvent) {
		if (summarizationRetryPhase != 0 && summarizationRetryPhase != 2) || event.Attempt == nil || event.MaxAttempts == nil {
			fail(fmt.Errorf("%w: summarization retry schedule is incomplete or overlapping", ErrProtocol))
			return
		}
		if event.ErrorMessage == nil || strings.TrimSpace(*event.ErrorMessage) == "" {
			fail(fmt.Errorf("%w: summarization retry errorMessage is required", ErrProtocol))
			return
		}
		attempt, maxAttempts := *event.Attempt, *event.MaxAttempts
		if attempt != summarizationRetryAttempt+1 || maxAttempts < 1 || maxAttempts > 3 || attempt > maxAttempts ||
			(summarizationRetryMaxAttempts != 0 && maxAttempts != summarizationRetryMaxAttempts) {
			fail(fmt.Errorf("%w: summarization retry budget is inconsistent", ErrProtocol))
			return
		}
		if event.DelayMs == "" {
			fail(fmt.Errorf("%w: summarization retry delayMs is required", ErrProtocol))
			return
		}
		delay, err := strconv.ParseInt(string(event.DelayMs), 10, 64)
		if err != nil || delay < 0 || delay > maxBackoffDelayMs {
			fail(fmt.Errorf("%w: summarization retry delayMs must be a bounded non-negative integer", ErrProtocol))
			return
		}
		summarizationRetryAttempt, summarizationRetryMaxAttempts = attempt, maxAttempts
		summarizationRetryPhase = 1
	}
	acceptSummarizationRetryStart := func(event *piEvent) {
		if summarizationRetryPhase != 1 || event.Source != "compaction" || event.Reason != compactionReason {
			fail(fmt.Errorf("%w: summarization retry start does not match the active compaction", ErrProtocol))
			return
		}
		summarizationRetryPhase = 2
	}
	acceptSummarizationRetryFinished := func() {
		if summarizationRetryPhase != 2 {
			fail(fmt.Errorf("%w: summarization retry finished without an active retry", ErrProtocol))
			return
		}
		summarizationRetryPhase = 0
	}
	acceptCompactionEnd := func(event *piEvent, paired bool) {
		if event.Aborted == nil || event.WillRetry == nil {
			fail(fmt.Errorf("%w: compaction_end requires explicit aborted and willRetry flags", ErrProtocol))
			return
		}
		if paired {
			if summarizationRetryPhase != 0 {
				fail(fmt.Errorf("%w: compaction ended before summarization retry settled", ErrProtocol))
				return
			}
			if event.Reason != compactionReason {
				fail(fmt.Errorf("%w: compaction_end reason does not match compaction_start", ErrProtocol))
				return
			}
		} else if event.Reason != "overflow" || !overflowRecoverySeen || !pendingProviderFailure {
			fail(fmt.Errorf("%w: unpaired compaction_end is not an overflow recovery closure", ErrProtocol))
			return
		}
		compactionResult, hasResult, decodeErr := decodeCompactionResult(event.Result)
		if decodeErr != nil {
			fail(fmt.Errorf("%w: invalid compaction result", ErrProtocol))
			return
		}
		hasError := event.ErrorMessage != nil && strings.TrimSpace(*event.ErrorMessage) != ""
		if !paired && (hasResult || *event.Aborted || *event.WillRetry || !hasError ||
			(pendingStopReason != "length" && pendingStopReason != "error")) {
			fail(fmt.Errorf("%w: unpaired overflow recovery closure has invalid outcome fields", ErrProtocol))
			return
		}
		if *event.Aborted && *event.WillRetry {
			fail(fmt.Errorf("%w: aborted compaction cannot retry", ErrProtocol))
			return
		}
		switch {
		case hasResult:
			if *event.Aborted || hasError {
				fail(fmt.Errorf("%w: successful compaction has contradictory outcome fields", ErrProtocol))
				return
			}
		case *event.Aborted:
			if hasError || *event.WillRetry {
				fail(fmt.Errorf("%w: aborted compaction has contradictory outcome fields", ErrProtocol))
				return
			}
			pendingProviderFailure = true
		default:
			if !hasError || *event.WillRetry {
				fail(fmt.Errorf("%w: failed compaction requires a stable non-retrying error closure", ErrProtocol))
				return
			}
			pendingProviderFailure = true
		}
		if *event.WillRetry && (event.Reason != "overflow" || *event.Aborted ||
			(pendingStopReason != "length" && pendingStopReason != "error")) {
			fail(fmt.Errorf("%w: compaction retry requires a completed overflow compaction", ErrProtocol))
			return
		}
		if hasResult && compactionResult.Usage != nil {
			usage := compactionResult.Usage
			if !accumulateOneUsage(*usage.Input, *usage.Output, *usage.CacheRead, usage.Cost.value) {
				return
			}
		}
		if paired && event.Reason == "overflow" && *event.WillRetry {
			overflowRecoverySeen = true
		}
		compactionReason = ""
		if *event.WillRetry {
			state = stateAwaitingCompactionContinuation
			return
		}
		if !paired {
			overflowRecoverySeen = false
			state = stateTerminalProviderFailure
			return
		}
		state = statePostAgentEnd
	}
	handle := func(fragment []byte) {
		trimmed := bytes.TrimSpace(fragment)
		if len(trimmed) == 0 {
			fail(fmt.Errorf("%w: blank JSONL fragment is not an event", ErrProtocol))
			return
		}
		var event piEvent
		if json.Unmarshal(trimmed, &event) != nil {
			fail(fmt.Errorf("%w: malformed JSONL fragment", ErrProtocol))
			return
		}
		result.eventCount++
		if result.eventCount == 1 {
			if event.Type != "session" {
				fail(fmt.Errorf("%w: first event must be the session header", ErrProtocol))
				return
			}
			if event.Version == nil || *event.Version != supportedSessionVersion {
				fail(fmt.Errorf("%w: session header version must be %d", ErrProtocol, supportedSessionVersion))
				return
			}
			if filepath.Clean(event.Cwd) != worktree {
				fail(fmt.Errorf("%w: session cwd does not match worktree", ErrProtocol))
				return
			}
			result.sessionID = event.ID
			return
		}
		switch state {
		case stateActive, stateRetryActive:
			switch event.Type {
			case "agent_end":
				acceptAgentEnd(&event)
			case "auto_retry_end":
				if state != stateRetryActive {
					unauthorized()
					return
				}
				acceptAutoRetryEnd(&event)
			case "tool_execution_start":
				result.toolCalls++
				if event.ToolCallID != "" && len(pending) < 4096 {
					pending[event.ToolCallID] = event.Args
				}
			case "tool_execution_end":
				tool, args := event.ToolName, event.Args
				if event.ToolCallID != "" {
					if startArgs, ok := pending[event.ToolCallID]; ok {
						delete(pending, event.ToolCallID)
						if len(args) == 0 {
							args = startArgs
						}
					}
				}
				// Denial grading is fail-closed: only an explicit permission
				// marker turns a tool error into a denial event, and anything
				// the classifier cannot prove benign stays FATAL.
				if event.IsError != nil && *event.IsError && denials.IsPermissionError(event.Error) {
					result.denials = append(result.denials, denials.RawDenial{Tool: tool, Input: args})
				} else if tool != "" {
					// Allowlist reconciliation is a read-only side channel:
					// every successful (non-denial) tool completion is
					// recorded by name; state transitions never change.
					result.toolNames = append(result.toolNames, tool)
				}
			case "session", "agent_settled", "auto_retry_start", "compaction_start", "compaction_end":
				unauthorized()
			default:
				if !activeWorkEvents[event.Type] {
					unauthorized()
				}
			}
		case stateAwaitingAutoRetryStart:
			if event.Type != "auto_retry_start" {
				unauthorized()
				return
			}
			acceptAutoRetryStart(&event)
		case stateAwaitingFinalAgentEnd:
			if event.Type != "agent_end" {
				unauthorized()
				return
			}
			acceptAgentEnd(&event)
		case stateAwaitingRetryFailureEnd:
			if event.Type != "auto_retry_end" {
				unauthorized()
				return
			}
			acceptAutoRetryEnd(&event)
		case statePostAgentEnd:
			switch event.Type {
			case "agent_settled":
				result.providerFailed = pendingProviderFailure
				state = stateTerminalSettled
			case "compaction_start":
				acceptCompactionStart(&event)
			case "compaction_end":
				acceptCompactionEnd(&event, false)
			case "agent_start":
				pendingProviderFailure = false
				pendingStopReason = ""
				state = stateActive
			case "queue_update":
				// A compaction_end extension may queue the continuation before
				// Pi emits its next agent_start.
			default:
				unauthorized()
			}
		case stateCompacting:
			switch event.Type {
			case "compaction_end":
				acceptCompactionEnd(&event, true)
			case "summarization_retry_scheduled":
				acceptSummarizationRetryScheduled(&event)
			case "summarization_retry_attempt_start":
				acceptSummarizationRetryStart(&event)
			case "summarization_retry_finished":
				acceptSummarizationRetryFinished()
			case "entry_appended":
				// The successful compaction entry may be persisted before end.
			default:
				unauthorized()
			}
		case stateAwaitingCompactionContinuation:
			if event.Type != "agent_start" {
				unauthorized()
				return
			}
			pendingProviderFailure = false
			pendingStopReason = ""
			state = stateActive
		case stateTerminalProviderFailure:
			if event.Type != "agent_settled" {
				unauthorized()
				return
			}
			result.providerFailed = true
			state = stateTerminalSettled
		case stateTerminalSettled:
			unauthorized()
		}
	}
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 && result.err == nil && !result.limitExceeded {
			room := limit - received
			switch {
			case int64(len(fragment)) > room:
				result.limitExceeded = true
				terminate()
				result.raw = append(result.raw, line...)
				line = nil
				if remaining := limit - int64(len(result.raw)); remaining > 0 {
					result.raw = append(result.raw, fragment[:remaining]...)
				}
				received = limit
			case err == nil:
				received += int64(len(fragment))
				line = append(line, fragment...)
				result.raw = append(result.raw, line...)
				handle(line)
				line = line[:0]
				if aborted {
					return result
				}
			case errors.Is(err, bufio.ErrBufferFull):
				received += int64(len(fragment))
				line = append(line, fragment...)
			default:
				fail(fmt.Errorf("%w: final fragment is not LF-terminated", ErrProtocol))
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if result.err == nil && !result.limitExceeded {
				switch {
				case !errors.Is(err, io.EOF):
					result.err = err
					terminate()
				case len(line) > 0:
					fail(fmt.Errorf("%w: final fragment is not LF-terminated", ErrProtocol))
				case !state.closed():
					fail(eofClosureError(state))
				case state == statePostAgentEnd || state == stateTerminalProviderFailure:
					result.providerFailed = pendingProviderFailure
				}
			}
			return result
		}
	}
}

type streamCapture struct {
	data      []byte
	truncated bool
}

func captureStream(reader io.Reader, limit int64) streamCapture {
	var output []byte
	buffer := make([]byte, 32<<10)
	var total int64
	truncated := false
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			remaining := limit - int64(len(output))
			if remaining > 0 {
				take := int64(count)
				if take > remaining {
					take = remaining
				}
				output = append(output, buffer[:take]...)
			}
			if total > limit {
				truncated = true
			}
		}
		if err != nil {
			return streamCapture{data: output, truncated: truncated}
		}
	}
}

// hardeningFlags returns the frozen, ordered hardening surface for a
// resolved --tools allowlist value. Every flag is listed exactly once;
// buildArgs copies it verbatim so no hardening flag can ever appear twice in
// the argv Marshal hands to pi.
func hardeningFlags(toolsArg string) []string {
	return []string{
		"--mode", "json", "--print", "--no-approve",
		"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--tools", toolsArg,
		"--no-session",
	}
}

// buildArgs produces the exact hardened argv for a TaskSpec without a
// declared tool allowlist: the frozen profile defaults apply unchanged.
// Sessions are always disabled: Marshal only supports ephemeral attempts.
// The prompt is always the final positional argument; Marshal never invokes
// pi through a shell.
func buildArgs(profile, model, prompt string) []string {
	args := append([]string{}, hardeningFlags(toolsForProfile(profile))...)
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

// buildArgsWithTools produces the hardened argv for a declared tool
// allowlist; it fails closed when Pi cannot provide a declared tool.
func buildArgsWithTools(profile, model, prompt string, tools []string) ([]string, error) {
	toolsArg, err := toolsArgFor(profile, tools)
	if err != nil {
		return nil, err
	}
	args := append([]string{}, hardeningFlags(toolsArg)...)
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt), nil
}

// buildTerminalArgsWithTools is the single native TUI argv construction path
// for every terminal launch: an undeclared task keeps the frozen profile tool
// surface, and a declared worker.tools allowlist converges --tools to the
// declared intersection. It fails closed when Pi cannot provide a declared
// tool, so terminal mode never weakens the allowlist.
func buildTerminalArgsWithTools(profile, model string, tools []string) ([]string, error) {
	toolsArg, err := toolsArgFor(profile, tools)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates",
		"--no-themes", "--no-context-files", "--tools", toolsArg, "--no-session",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

// processFailureError reports a failed pi process using only fixed
// classification and exit/signal information. Provider stderr is persisted
// separately as a bounded evidence file (pi-stderr.log) but is never
// concatenated into the returned error, so tokens, secrets, or user content
// cannot reach Events, CLI output, or Outcome.
func processFailureError(exitCode int, signal string) error {
	if signal != "" {
		return fmt.Errorf("%w: exit=%d signal=%s", ErrProcessFailed, exitCode, signal)
	}
	return fmt.Errorf("%w: exit=%d", ErrProcessFailed, exitCode)
}

func processOutcome(command *exec.Cmd) (int, string) {
	if command.ProcessState == nil {
		return -1, ""
	}
	exitCode := command.ProcessState.ExitCode()
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return exitCode, status.Signal().String()
	}
	return exitCode, ""
}

// contextErrorOf formats the attempt context error for transcript metadata;
// the empty string reports a clean context.
func contextErrorOf(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

// workerEnvironment strips every inherited variable outside a benign
// allowlist. GitHub, cloud, SSH, and model-provider credentials never reach
// the worker process; model authentication comes only from Pi's own
// configuration under HOME.
func workerEnvironment(worktree string) []string {
	allowed := map[string]bool{"HOME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TEMP": true, "TERM": true, "TMP": true, "TMPDIR": true, "USER": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	environment := make([]string, 0, len(allowed)+6)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "CI=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes", "PWD="+worktree)
	return environment
}

func terminalWorkerEnvironment(worktree string) []string {
	return nativeTTYEnvironment(workerEnvironment(worktree))
}

func nativeTTYEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CI" && key != "TERM" && key != "COLORTERM" {
			result = append(result, entry)
		}
	}
	return append(result, "TERM=xterm-256color", "COLORTERM=truecolor")
}

func probeEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "HOME" || key == "TMPDIR" || key == "LANG" {
			result = append(result, entry)
		}
	}
	return result
}

func readModel(controlRoot, relative string) string {
	path, err := existingPathWithin(controlRoot, relative)
	if err != nil {
		return ""
	}
	data, err := readBounded(path, maxResultBytes)
	if err != nil {
		return ""
	}
	var task struct {
		Worker struct {
			Model string `json:"model"`
		} `json:"worker"`
	}
	if json.Unmarshal(data, &task) != nil {
		return ""
	}
	return task.Worker.Model
}

func lexicalPathWithin(root, relative string) (string, error) {
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes control root")
	}
	return path, nil
}

func existingPathWithin(root, relative string) (string, error) {
	path, err := lexicalPathWithin(root, relative)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("symlink escapes control root")
	}
	return real, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds byte limit")
	}
	return data, nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pi-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func terminateGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	group, err := syscall.Getpgid(command.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = command.Process.Kill()
	}
}
