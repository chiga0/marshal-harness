// Package qwen implements the bounded Qwen Code Worker adapter.
package qwen

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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID       = "qwen"
	adapterVersion  = "0.1.0"
	supportedBinary = "0.21.5"
	maxPromptBytes  = 256 << 10
	maxResultBytes  = 4 << 20
	stderrLimit     = 64 << 10

	budgetToolCalls    = 200
	budgetSessionTurns = 60
)

// excludedTools blocks every shell, sub-agent, sub-session, web/network and
// computer-use capability. Qwen Code's safe-mode alone does not remove these
// tools, so Marshal excludes them by name on every attempt.
var excludedTools = []string{
	"shell",
	"run_shell_command",
	"agent",
	"sub_agent",
	"create_sub_session",
	"web_fetch",
	"web_search",
	"computer_use__bring_to_front",
	"computer_use__check_for_update",
	"computer_use__check_permissions",
	"computer_use__click",
	"computer_use__double_click",
	"computer_use__drag",
	"computer_use__end_session",
	"computer_use__get_accessibility_tree",
	"computer_use__get_agent_cursor_state",
	"computer_use__get_config",
	"computer_use__get_cursor_position",
	"computer_use__get_recording_state",
	"computer_use__get_screen_size",
	"computer_use__get_window_state",
	"computer_use__hotkey",
	"computer_use__kill_app",
	"computer_use__launch_app",
	"computer_use__list_apps",
	"computer_use__list_windows",
	"computer_use__move_cursor",
	"computer_use__page",
	"computer_use__press_key",
	"computer_use__replay_trajectory",
	"computer_use__right_click",
	"computer_use__scroll",
	"computer_use__set_agent_cursor_enabled",
	"computer_use__set_agent_cursor_motion",
	"computer_use__set_agent_cursor_style",
	"computer_use__set_config",
	"computer_use__set_value",
	"computer_use__start_recording",
	"computer_use__start_session",
	"computer_use__stop_recording",
	"computer_use__type_text",
	"computer_use__zoom",
}

// readOnlyExcludedTools extends excludedTools with every write-class tool
// except write_file (ADR 0014). The read-only profile forbids editing source
// and running commands; write_file stays granted so the worker can still
// produce its deliverables, and the artifact-path boundary is enforced by
// Marshal's scope gate because Qwen Code has no path-scoped write permission.
var readOnlyExcludedTools = func() []string {
	tools := make([]string, 0, len(excludedTools)+11)
	tools = append(tools, excludedTools...)
	return append(tools,
		"apply_patch", "edit", "insert", "multiedit", "notebook_edit",
		"patch", "replace", "save_file", "save_memory", "write", "write_todos",
	)
}()

// excludedToolsFor selects the tool exclusion list for an execution profile.
func excludedToolsFor(profile string) []string {
	if profile == "read-only" {
		return readOnlyExcludedTools
	}
	return excludedTools
}

var (
	ErrUnsupportedVersion = errors.New("unsupported qwen version")
	ErrOutputLimit        = errors.New("qwen output limit exceeded")
	ErrProtocol           = errors.New("invalid qwen protocol")
	ErrPermissionDenied   = errors.New("qwen permission denied")
	ErrProcessFailed      = errors.New("qwen process failed")
)

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

type Adapter struct {
	executable string
	validator  *contract.Validator
	now        func() time.Time
}

var _ port.TerminalLaunchAdapter = (*Adapter)(nil)

// New requires an exact absolute executable path. Marshal never resolves a
// provider executable by a similar name or by an implicit fallback.
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	if validator == nil {
		return nil, errors.New("contract validator is required")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, errors.New("qwen executable must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve qwen executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat qwen executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("qwen executable must be an executable regular file")
	}
	return &Adapter{executable: realPath, validator: validator, now: time.Now}, nil
}

func (a *Adapter) ID() string { return adapterID }

// PrepareTerminal freezes a native Qwen TUI launch. It preserves the same
// permission and native budget flags as Run, but removes structured-output and
// print-prompt flags so the prompt can be delivered through the audited PTY.
func (a *Adapter) PrepareTerminal(ctx context.Context, record domain.Record) (port.TerminalLaunchSpec, error) {
	if record.Kind != domain.KindWorkerRequest {
		return port.TerminalLaunchSpec{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	if request.AdapterID != adapterID || (request.ExecutionProfile != "workspace-write" && request.ExecutionProfile != "read-only") {
		return port.TerminalLaunchSpec{}, errors.New("WorkerRequest does not match the qwen adapter execution profile")
	}
	identity, err := a.inspect(ctx)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	if identity.version != supportedBinary {
		return port.TerminalLaunchSpec{}, fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	worktree, controlRoot, prompt, err := resolveTerminalInput(request)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	model := readModel(controlRoot, request.TaskSpecPath)
	args, err := buildTerminalArgs(request.ExecutionProfile, request.SessionPolicy, request.SessionID, model, request.AttemptTimeoutSeconds)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	return port.TerminalLaunchSpec{
		AdapterID: adapterID, AdapterVersion: adapterVersion, RunID: request.RunID, AttemptID: request.AttemptID, BinaryVersion: identity.version,
		Executable: identity.path, ExecutableDigest: identity.digest, WorkingDirectory: worktree,
		Arguments: args, Environment: terminalWorkerEnvironment(worktree), InitialPrompt: string(prompt),
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
	if identity.version != supportedBinary {
		status = "unsupported"
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Qwen Code %s，实际为 %s", supportedBinary, identity.version))
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral", "persist", "resume"}, "modelSelection": true,
			"executionProfiles":       []string{"workspace-write", "read-only"},
			"nativeBudgets":           []string{"wall-time", "tool-calls", "turns"},
			"processTreeCancellation": true,
			"notes": []string{
				"由 Marshal 实施 wall-time 与输出字节数上限。",
				"safe-mode + auto-edit + exclude-tools 不构成恶意代码隔离。",
				"shell、sub-agent、sub-session、web/network 与 computer-use 工具被按名排除。",
				"read-only 画像额外排除源码编辑类工具，写域由 Marshal scope 门禁兜底。",
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

func (a *Adapter) inspect(ctx context.Context) (executableIdentity, error) {
	info, err := os.Stat(a.executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return executableIdentity{}, errors.New("configured qwen executable is unavailable")
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
		return "", fmt.Errorf("probe qwen version: %w", err)
	}
	version := versionPattern.FindString(string(output))
	if version == "" {
		return "", fmt.Errorf("qwen returned an unrecognized version: %q", strings.TrimSpace(string(output)))
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
		return "", "", errors.New("qwen candidate must be an absolute clean path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("qwen candidate is not an executable regular file")
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

// Run executes one non-interactive attempt. Provider/process/protocol failures
// are returned as errors so Core can apply the operational retry budget.
func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindWorkerRequest {
		return domain.Record{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	if request.AdapterID != adapterID || (request.ExecutionProfile != "workspace-write" && request.ExecutionProfile != "read-only") {
		return domain.Record{}, errors.New("WorkerRequest does not match the qwen adapter execution profile")
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(request.AttemptTimeoutSeconds)*time.Second)
	defer cancel()
	identity, err := a.inspect(runCtx)
	if err != nil {
		return domain.Record{}, err
	}
	if identity.version != supportedBinary {
		return domain.Record{}, fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
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
	if request.SessionPolicy == "resume" && strings.TrimSpace(request.SessionID) == "" {
		return domain.Record{}, errors.New("resume session policy requires a sessionId")
	}
	model := readModel(controlRoot, request.TaskSpecPath)
	args, err := buildArgs(request.ExecutionProfile, request.SessionPolicy, request.SessionID, model, request.AttemptTimeoutSeconds, string(prompt))
	if err != nil {
		return domain.Record{}, err
	}
	command := exec.Command(a.executable, args...)
	command.Dir = worktree
	command.Env = workerEnvironment(worktree)
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
	if err := command.Start(); err != nil {
		return domain.Record{}, fmt.Errorf("start qwen: %w", err)
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { terminateGroup(command) }) }
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() {
		stdoutDone <- captureStreamJSONL(stdout, worktree, int64(request.MaxOutputBytes), kill, identity.version)
	}()
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
	transcriptPath := filepath.Join(filepath.Dir(resultPath), "qwen-transcript.jsonl")
	if err := atomicWrite(transcriptPath, capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "qwen-stderr.log"), stderrCapture.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	exitCode, signal := processOutcome(command)
	denialRecords := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: worktree, ControlRoot: controlRoot, TempDir: os.TempDir()}, capture.denials, a.now)
	fatalDenials := denials.CountFatal(denialRecords)
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "eventCount": capture.eventCount,
		"toolCalls": capture.toolCalls, "inputTokens": capture.inputTokens,
		"outputTokens": capture.outputTokens, "capturedBytes": len(capture.raw),
		"outputTruncated": capture.limitExceeded, "permissionDenied": fatalDenials > 0,
		"denialsBenign": len(denialRecords) - fatalDenials, "denialsFatal": fatalDenials,
		"exitCode": exitCode, "signal": signal, "stderrBytes": len(stderrCapture.data), "stderrTruncated": stderrCapture.truncated,
		"contextError": contextError(runCtx),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, err
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "qwen-transcript-meta.json"), append(metadata, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript metadata: %w", err)
	}
	if err := denials.AppendLog(filepath.Join(filepath.Dir(resultPath), denials.LogFileName), denialRecords); err != nil {
		return domain.Record{}, fmt.Errorf("write denial log: %w", err)
	}
	if runCtx.Err() != nil {
		return domain.Record{}, runCtx.Err()
	}
	if capture.limitExceeded {
		return domain.Record{}, ErrOutputLimit
	}
	if capture.err != nil {
		return domain.Record{}, capture.err
	}
	if waitErr != nil {
		return domain.Record{}, processFailureError(command)
	}
	if fatalDenials > 0 {
		return domain.Record{}, ErrPermissionDenied
	}
	if capture.sessionID == "" {
		return domain.Record{}, fmt.Errorf("%w: session_id is missing", ErrProtocol)
	}
	if request.SessionPolicy == "resume" && capture.sessionID != request.SessionID {
		return domain.Record{}, fmt.Errorf("%w: resumed session does not match requested session", ErrProtocol)
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
	declared.Session = &declaredSession{ID: capture.sessionID, Resumable: request.SessionPolicy != "ephemeral"}
	declared.StartedAt, declared.CompletedAt = started, completed
	if model != "" {
		declared.Adapter.Model = model
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

func readDeclaredResult(path string, limit int64, validator *contract.Validator) (declaredResult, error) {
	data, err := readBounded(path, limit)
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

type captureResult struct {
	raw           []byte
	sessionID     string
	eventCount    int
	toolCalls     int
	inputTokens   int
	outputTokens  int
	denials       []denials.RawDenial
	limitExceeded bool
	err           error
}

// captureStreamJSONL enforces the measured Qwen Code 0.21.5 stream-json
// contract: the first non-empty event must be system/init bound to this
// worktree, and the last non-empty event must be result/success.
func captureStreamJSONL(reader io.Reader, worktree string, limit int64, onLimit func(), binaryVersion string) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	fail := func(err error) {
		if result.err == nil {
			result.err = err
		}
		onLimit()
	}
	var consumed int64
	var line []byte
	resultSubtype := ""
	sawResult := false
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 {
			consumed += int64(len(fragment))
			if consumed > limit {
				if !result.limitExceeded {
					result.limitExceeded = true
					onLimit()
				}
				line = nil
			} else if !result.limitExceeded {
				line = append(line, fragment...)
			}
			complete := !errors.Is(err, bufio.ErrBufferFull)
			if complete && len(line) > 0 && !result.limitExceeded {
				trimmed := bytes.TrimSpace(line)
				line = nil
				if len(trimmed) == 0 {
					continue
				}
				result.raw = append(result.raw, append(trimmed, '\n')...)
				var event struct {
					Type            string          `json:"type"`
					Subtype         string          `json:"subtype"`
					SessionID       string          `json:"session_id"`
					Cwd             string          `json:"cwd"`
					QwenCodeVersion string          `json:"qwen_code_version"`
					ToolName        string          `json:"tool_name"`
					Args            json.RawMessage `json:"args"`
					IsError         *bool           `json:"is_error"`
					Error           string          `json:"error"`
					Usage           struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
					Stats struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"stats"`
				}
				if decodeErr := json.Unmarshal(trimmed, &event); decodeErr != nil {
					fail(fmt.Errorf("%w: malformed JSONL: %v", ErrProtocol, decodeErr))
				} else {
					result.eventCount++
					if sawResult {
						fail(fmt.Errorf("%w: trailing event after result: %s/%s", ErrProtocol, event.Type, event.Subtype))
						continue
					}
					if result.eventCount == 1 {
						if event.Type != "system" || event.Subtype != "init" {
							fail(fmt.Errorf("%w: first event must be system/init, got %s/%s", ErrProtocol, event.Type, event.Subtype))
						} else if event.SessionID == "" || event.Cwd == "" || event.QwenCodeVersion == "" {
							fail(fmt.Errorf("%w: init event is missing session_id, cwd or qwen_code_version", ErrProtocol))
						} else if initVersion := versionPattern.FindString(event.QwenCodeVersion); initVersion != binaryVersion {
							fail(fmt.Errorf("%w: init qwen_code_version %s does not match binary %s", ErrProtocol, event.QwenCodeVersion, binaryVersion))
						} else if filepath.Clean(event.Cwd) != worktree {
							fail(fmt.Errorf("%w: init cwd %q does not match worktree %q", ErrProtocol, event.Cwd, worktree))
						} else {
							result.sessionID = event.SessionID
						}
					}
					if event.Type == "tool" || event.ToolName != "" {
						result.toolCalls++
					}
					// Denial grading is fail-closed: only an explicit
					// permission marker turns a tool error into a denial
					// event, and anything the classifier cannot prove benign
					// stays FATAL.
					if event.IsError != nil && *event.IsError && denials.IsPermissionError(event.Error) {
						result.denials = append(result.denials, denials.RawDenial{Tool: event.ToolName, Input: event.Args})
					}
					if event.Type == "result" {
						sawResult = true
						resultSubtype = event.Subtype
						if event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 {
							result.inputTokens, result.outputTokens = event.Usage.InputTokens, event.Usage.OutputTokens
						}
						if event.Stats.InputTokens > 0 || event.Stats.OutputTokens > 0 {
							result.inputTokens, result.outputTokens = event.Stats.InputTokens, event.Stats.OutputTokens
						}
					}
				}
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if !errors.Is(err, io.EOF) && result.err == nil {
				result.err = err
			}
			if result.err == nil && !result.limitExceeded {
				if !sawResult {
					result.err = fmt.Errorf("%w: stream ended without a result event", ErrProtocol)
				} else if resultSubtype != "success" {
					result.err = fmt.Errorf("%w: result subtype is %s, expected success", ErrProtocol, resultSubtype)
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

func buildArgs(profile, policy, sessionID, model string, wallTimeSeconds int, prompt string) ([]string, error) {
	args := []string{
		"--safe-mode",
		"--approval-mode", "auto-edit",
		"--output-format", "stream-json",
		"--max-wall-time", strconv.Itoa(wallTimeSeconds),
		"--max-tool-calls", strconv.Itoa(budgetToolCalls),
		"--max-session-turns", strconv.Itoa(budgetSessionTurns),
		"--exclude-tools", strings.Join(excludedToolsFor(profile), ","),
	}
	args, err := appendSessionAndModel(args, policy, sessionID, model)
	if err != nil {
		return nil, err
	}
	return append(args, "-p", prompt), nil
}

func buildTerminalArgs(profile, policy, sessionID, model string, wallTimeSeconds int) ([]string, error) {
	args := []string{
		"--safe-mode",
		"--approval-mode", "auto-edit",
		"--max-wall-time", strconv.Itoa(wallTimeSeconds),
		"--max-tool-calls", strconv.Itoa(budgetToolCalls),
		"--max-session-turns", strconv.Itoa(budgetSessionTurns),
		"--exclude-tools", strings.Join(excludedToolsFor(profile), ","),
	}
	return appendSessionAndModel(args, policy, sessionID, model)
}

func appendSessionAndModel(args []string, policy, sessionID, model string) ([]string, error) {
	switch policy {
	case "ephemeral":
		args = append(args, "--chat-recording=false")
	case "persist":
		args = append(args, "--chat-recording=true")
	case "resume":
		if strings.TrimSpace(sessionID) == "" {
			return nil, errors.New("resume session policy requires a sessionId")
		}
		args = append(args, "--chat-recording=true", "--resume", sessionID)
	default:
		return nil, fmt.Errorf("unsupported session policy %q", policy)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

// processFailureError reports a failed qwen process using only fixed
// classification and exit/signal information. Provider stderr is persisted
// separately as a bounded evidence file (qwen-stderr.log) but is never
// concatenated into the returned error, so tokens, secrets, or user content
// cannot reach Events, CLI output, or Outcome.
func processFailureError(command *exec.Cmd) error {
	exitCode, signal := processOutcome(command)
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

func contextError(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return ""
}

func workerEnvironment(worktree string) []string {
	allowed := map[string]bool{"HOME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TEMP": true, "TERM": true, "TMP": true, "TMPDIR": true, "USER": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	environment := make([]string, 0, len(allowed)+6)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "PWD="+worktree)
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
		return "", errors.New("path escapes worktree")
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
		return "", errors.New("symlink escapes worktree")
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
	file, err := os.CreateTemp(filepath.Dir(path), ".qwen-*.tmp")
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
