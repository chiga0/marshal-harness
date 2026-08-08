// Package opencode implements the bounded OpenCode Worker adapter.
package opencode

import (
	"bufio"
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
	adapterID       = "opencode"
	adapterVersion  = "0.1.0"
	supportedBinary = "1.18.13"
	maxPromptBytes  = 256 << 10
	maxResultBytes  = 4 << 20
	stderrLimit     = 64 << 10
)

var (
	ErrUnsupportedVersion = errors.New("unsupported opencode version")
	ErrOutputLimit        = errors.New("opencode output limit exceeded")
	ErrProtocol           = errors.New("invalid opencode protocol")
	ErrPermissionDenied   = errors.New("opencode permission denied")
	ErrProcessFailed      = errors.New("opencode process failed")
)

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
		return nil, errors.New("opencode executable must be an absolute clean path")
	}
	realPath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve opencode executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, fmt.Errorf("stat opencode executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("opencode executable must be an executable regular file")
	}
	return &Adapter{executable: realPath, validator: validator, now: time.Now}, nil
}

func (a *Adapter) ID() string { return adapterID }

// PrepareTerminal freezes OpenCode's native TUI transport while retaining the
// Adapter-owned permission configuration and sanitized environment.
func (a *Adapter) PrepareTerminal(ctx context.Context, record domain.Record) (port.TerminalLaunchSpec, error) {
	if record.Kind != domain.KindWorkerRequest {
		return port.TerminalLaunchSpec{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	if request.AdapterID != adapterID || (request.ExecutionProfile != "workspace-write" && request.ExecutionProfile != "read-only") {
		return port.TerminalLaunchSpec{}, errors.New("WorkerRequest does not match the opencode adapter execution profile")
	}
	if err := validateSession(request.SessionPolicy, request.SessionID); err != nil {
		return port.TerminalLaunchSpec{}, err
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
	readOnly, err := optionalReadOnlyScope(request.ExecutionProfile, controlRoot, request.TaskSpecPath)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	config, err := permissionConfigFor(request.ExecutionProfile, worktree, controlRoot, readOnly)
	if err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	environment := terminalWorkerEnvironment(worktree, config)
	if err := validateResolvedConfig(ctx, a.executable, environment, controlRoot, worktree, request.ExecutionProfile, readOnly); err != nil {
		return port.TerminalLaunchSpec{}, err
	}
	return port.TerminalLaunchSpec{
		AdapterID: adapterID, AdapterVersion: adapterVersion, RunID: request.RunID, AttemptID: request.AttemptID, BinaryVersion: identity.version,
		Executable: identity.path, ExecutableDigest: identity.digest, WorkingDirectory: worktree,
		Arguments:   buildTerminalArgs(request.SessionPolicy, request.SessionID, readModel(controlRoot, request.TaskSpecPath)),
		Environment: environment, InitialPrompt: string(prompt),
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
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 OpenCode %s，实际为 %s", supportedBinary, identity.version))
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral", "persist", "resume"}, "modelSelection": true,
			"executionProfiles": []string{"workspace-write", "read-only"}, "nativeBudgets": []string{},
			"processTreeCancellation": true,
			"notes":                   []string{"由 Marshal 实施 wall-time 与 output-bytes 上限。", "workspace-write Local Profile 不构成恶意代码隔离。", "read-only 画像仅允许 allowPaths 内 edit、readRoots 读取与只读命令白名单，同样不构成恶意代码隔离。"},
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
		return executableIdentity{}, errors.New("configured opencode executable is unavailable")
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
		return "", fmt.Errorf("probe opencode version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", errors.New("opencode returned an empty version")
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
		return "", "", errors.New("opencode candidate must be an absolute clean path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("opencode candidate is not an executable regular file")
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
		return domain.Record{}, errors.New("WorkerRequest does not match the opencode adapter execution profile")
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
	model := readModel(controlRoot, request.TaskSpecPath)
	args := buildArgs(request.SessionPolicy, request.SessionID, model, string(prompt))
	readOnly, err := optionalReadOnlyScope(request.ExecutionProfile, controlRoot, request.TaskSpecPath)
	if err != nil {
		return domain.Record{}, err
	}
	config, err := permissionConfigFor(request.ExecutionProfile, worktree, controlRoot, readOnly)
	if err != nil {
		return domain.Record{}, err
	}
	command := exec.Command(a.executable, args...)
	command.Dir = worktree
	command.Env = workerEnvironment(worktree, config)
	if err := validateResolvedConfig(runCtx, a.executable, command.Env, controlRoot, worktree, request.ExecutionProfile, readOnly); err != nil {
		return domain.Record{}, err
	}
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
		return domain.Record{}, fmt.Errorf("start opencode: %w", err)
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { terminateGroup(command) }) }
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
	transcriptPath := filepath.Join(filepath.Dir(resultPath), "opencode-transcript.jsonl")
	if err := atomicWrite(transcriptPath, capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "opencode-stderr.log"), stderrCapture.data); err != nil {
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
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "opencode-transcript-meta.json"), append(metadata, '\n')); err != nil {
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
		return domain.Record{}, fmt.Errorf("%w: sessionID is missing", ErrProtocol)
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

func captureJSONL(reader io.Reader, limit int64, onLimit func()) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var consumed int64
	var line []byte
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
				result.raw = append(result.raw, line...)
				var event struct {
					Type      string `json:"type"`
					SessionID string `json:"sessionID"`
					Part      struct {
						Type   string `json:"type"`
						Text   string `json:"text"`
						Tool   string `json:"tool"`
						Tokens struct {
							Input  int `json:"input"`
							Output int `json:"output"`
						} `json:"tokens"`
						State struct {
							Status string          `json:"status"`
							Error  string          `json:"error"`
							Input  json.RawMessage `json:"input"`
						} `json:"state"`
					} `json:"part"`
				}
				if decodeErr := json.Unmarshal(bytesTrimSpace(line), &event); decodeErr != nil {
					result.err = fmt.Errorf("%w: malformed JSONL: %v", ErrProtocol, decodeErr)
					onLimit()
				} else {
					result.eventCount++
					if result.sessionID == "" {
						result.sessionID = event.SessionID
					}
					if event.Part.Type == "tool" || event.Part.Tool != "" {
						result.toolCalls++
					}
					if event.Part.Tokens.Input > 0 || event.Part.Tokens.Output > 0 {
						result.inputTokens = event.Part.Tokens.Input
						result.outputTokens = event.Part.Tokens.Output
					}
					if event.Part.State.Status == "error" && denials.IsPermissionError(event.Part.State.Error) {
						result.denials = append(result.denials, denials.RawDenial{Tool: event.Part.Tool, Input: event.Part.State.Input})
					}
				}
				line = nil
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if !errors.Is(err, io.EOF) && result.err == nil {
				result.err = err
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

func buildArgs(policy, sessionID, model, prompt string) []string {
	args := []string{"run", "--pure", "--format", "json", "--title", "Marshal Worker"}
	if policy == "resume" {
		args = append(args, "--session", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

func buildTerminalArgs(policy, sessionID, model string) []string {
	args := []string{"--pure"}
	if policy == "resume" {
		args = append(args, "--session", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func validateSession(policy, sessionID string) error {
	switch policy {
	case "ephemeral", "persist":
		return nil
	case "resume":
		if strings.TrimSpace(sessionID) == "" {
			return errors.New("resume session policy requires a sessionId")
		}
		return nil
	default:
		return fmt.Errorf("unsupported session policy %q", policy)
	}
}

func permissionConfig(controlRoot string) (string, error) {
	inputRoot := filepath.ToSlash(filepath.Join(controlRoot, "input")) + "/**"
	outputRoot := filepath.ToSlash(filepath.Join(controlRoot, "output")) + "/**"
	bash := map[string]string{"*": "allow", "/usr/bin/curl *": "deny", "/usr/bin/ssh *": "deny", "bash *": "deny", "curl *": "deny", "env *": "deny", "gh *": "deny", "git commit *": "deny", "git push *": "deny", "git tag *": "deny", "glab *": "deny", "nc *": "deny", "nohup *": "deny", "scp *": "deny", "sh *": "deny", "ssh *": "deny", "sudo *": "deny", "wget *": "deny", "xargs *": "deny"}
	permission := map[string]any{
		"*": "deny", "bash": bash,
		"edit":               map[string]string{"*": "allow", inputRoot: "deny", outputRoot: "allow"},
		"external_directory": map[string]string{"*": "deny", inputRoot: "allow", outputRoot: "allow"},
		"glob":               "allow", "grep": "allow", "list": "allow", "lsp": "allow", "question": "deny", "read": "allow", "skill": "deny", "task": "deny", "webfetch": "deny", "websearch": "deny",
	}
	config := map[string]any{"autoupdate": false, "permission": permission, "share": "disabled", "agent": map[string]any{"build": map[string]any{"permission": permission}}}
	data, err := json.Marshal(config)
	return string(data), err
}

// readOnlyBashAllowlist is the fixed read-only command whitelist of the
// read-only profile (ADR 0014): plain read commands only, never shell
// combinators, redirection writes, networking, or package management. The
// surrounding "*":"deny" makes every other command fail closed; pattern
// matching cannot inspect arguments, so this is a least-privilege grant, not
// a sandbox boundary.
var readOnlyBashAllowlist = []string{"cat", "file", "find", "grep", "head", "ls", "rg", "sed -n", "stat", "tail", "wc"}

// permissionConfigFor selects the profile-specific permission configuration:
// workspace-write keeps the existing fail-closed development grant, and
// read-only builds the ADR 0014 mapping (edit locked to artifact allowPaths,
// bash locked to the read-only whitelist, readRoots read-permitted).
func permissionConfigFor(profile, worktree, controlRoot string, scope readOnlyScope) (string, error) {
	if profile == "read-only" {
		return readOnlyPermissionConfig(worktree, controlRoot, scope)
	}
	return permissionConfig(controlRoot)
}

// readOnlyPermissionConfig builds the read-only profile (ADR 0014): edit is
// only allowed inside control/output and the TaskSpec artifact allowPaths,
// bash is restricted to the read-only whitelist, network tools stay denied,
// and readRoots are read-permitted through external_directory.
func readOnlyPermissionConfig(worktree, controlRoot string, scope readOnlyScope) (string, error) {
	inputRoot := filepath.ToSlash(filepath.Join(controlRoot, "input")) + "/**"
	outputRoot := filepath.ToSlash(filepath.Join(controlRoot, "output")) + "/**"
	edit := map[string]string{"*": "deny", inputRoot: "deny", outputRoot: "allow"}
	for _, allowPath := range scope.allowPaths {
		edit[filepath.ToSlash(filepath.Join(worktree, allowPath))] = "allow"
	}
	bash := map[string]string{"*": "deny"}
	for _, command := range readOnlyBashAllowlist {
		bash[command+" *"] = "allow"
	}
	external := map[string]string{"*": "deny", inputRoot: "allow", outputRoot: "allow"}
	for _, entry := range readOnlyExternalEntries(worktree, scope.readRoots) {
		external[entry] = "allow"
	}
	permission := map[string]any{
		"*": "deny", "bash": bash, "edit": edit, "external_directory": external,
		"glob": "allow", "grep": "allow", "list": "allow", "lsp": "allow", "question": "deny", "read": "allow", "skill": "deny", "task": "deny", "webfetch": "deny", "websearch": "deny",
	}
	config := map[string]any{"autoupdate": false, "permission": permission, "share": "disabled", "agent": map[string]any{"build": map[string]any{"permission": permission}}}
	data, err := json.Marshal(config)
	return string(data), err
}

// readOnlyExternalEntries resolves readRoots that point outside the worktree
// (for example repository symlinks such as sources/<repo>/) into
// external_directory grants. OpenCode's PermissionActionConfig accepts only
// allow/deny (verified against the pinned binary; a "read" action is
// rejected), so the grant is "allow"; the net effect stays read-only because
// edit and bash remain locked out of those paths. Patterns that cannot be
// resolved statically grant nothing, and any provider denial they cause is
// graded FATAL fail-closed under ADR 0013.
func readOnlyExternalEntries(worktree string, readRoots []string) []string {
	worktreeReal, worktreeErr := filepath.EvalSymlinks(worktree)
	base := worktree
	if worktreeErr == nil {
		base = worktreeReal
	}
	seen := map[string]bool{}
	var entries []string
	for _, root := range readRoots {
		candidate := filepath.Join(worktree, filepath.FromSlash(strings.TrimSuffix(root, "/")))
		real, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		if rel, relErr := filepath.Rel(base, real); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		entry := filepath.ToSlash(real) + "/**"
		if !seen[entry] {
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	return entries
}

func validateResolvedConfig(ctx context.Context, executable string, environment []string, controlRoot, worktree, profile string, readOnly readOnlyScope) error {
	check := func(permission map[string]any) error {
		if profile == "read-only" {
			return validateReadOnlyPermissionMap(permission, controlRoot, worktree, readOnly)
		}
		return validatePermissionMap(permission, controlRoot)
	}
	command := exec.CommandContext(ctx, executable, "debug", "config", "--pure")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("probe resolved opencode config: %w", err)
	}
	if len(output) > 1<<20 {
		return errors.New("resolved opencode config exceeds byte limit")
	}
	var config struct {
		Autoupdate bool           `json:"autoupdate"`
		Share      string         `json:"share"`
		Permission map[string]any `json:"permission"`
		Agent      map[string]struct {
			Permission map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(output, &config); err != nil {
		return fmt.Errorf("decode resolved opencode config: %w", err)
	}
	if config.Autoupdate || config.Share != "disabled" {
		return errors.New("resolved opencode config enables autoupdate or sharing")
	}
	if err := check(config.Permission); err != nil {
		return fmt.Errorf("unsafe resolved global permission: %w", err)
	}
	build, ok := config.Agent["build"]
	if !ok {
		return errors.New("resolved opencode config has no build agent override")
	}
	if err := check(build.Permission); err != nil {
		return fmt.Errorf("unsafe resolved build permission: %w", err)
	}
	return nil
}

func validateReadOnlyPermissionMap(permission map[string]any, controlRoot, worktree string, scope readOnlyScope) error {
	if permission["*"] != "deny" {
		return errors.New("global wildcard is not denied")
	}
	for _, name := range []string{"question", "skill", "task", "webfetch", "websearch"} {
		if permission[name] != "deny" {
			return fmt.Errorf("%s is not denied", name)
		}
	}
	inputRoot := filepath.ToSlash(filepath.Join(controlRoot, "input")) + "/**"
	outputRoot := filepath.ToSlash(filepath.Join(controlRoot, "output")) + "/**"
	external, ok := permission["external_directory"].(map[string]any)
	if !ok || external["*"] != "deny" || external[inputRoot] != "allow" || external[outputRoot] != "allow" {
		return errors.New("attempt-scoped external directory rules are missing")
	}
	for _, entry := range readOnlyExternalEntries(worktree, scope.readRoots) {
		if external[entry] != "allow" {
			return fmt.Errorf("readRoot external directory rule %q is missing", entry)
		}
	}
	edit, ok := permission["edit"].(map[string]any)
	if !ok || edit["*"] != "deny" || edit[inputRoot] != "deny" || edit[outputRoot] != "allow" {
		return errors.New("read-only edit rules are missing")
	}
	for _, allowPath := range scope.allowPaths {
		if edit[filepath.ToSlash(filepath.Join(worktree, allowPath))] != "allow" {
			return errors.New("read-only edit rules are missing")
		}
	}
	bash, ok := permission["bash"].(map[string]any)
	if !ok || bash["*"] != "deny" {
		return errors.New("bash rules are missing")
	}
	for _, command := range readOnlyBashAllowlist {
		if bash[command+" *"] != "allow" {
			return fmt.Errorf("bash command %q is not allowlisted", command)
		}
	}
	return nil
}

func validatePermissionMap(permission map[string]any, controlRoot string) error {
	if permission["*"] != "deny" {
		return errors.New("global wildcard is not denied")
	}
	for _, name := range []string{"question", "skill", "task", "webfetch", "websearch"} {
		if permission[name] != "deny" {
			return fmt.Errorf("%s is not denied", name)
		}
	}
	external, ok := permission["external_directory"].(map[string]any)
	if !ok || external["*"] != "deny" || external[filepath.ToSlash(filepath.Join(controlRoot, "input"))+"/**"] != "allow" || external[filepath.ToSlash(filepath.Join(controlRoot, "output"))+"/**"] != "allow" {
		return errors.New("attempt-scoped external directory rules are missing")
	}
	edit, ok := permission["edit"].(map[string]any)
	if !ok || edit[filepath.ToSlash(filepath.Join(controlRoot, "input"))+"/**"] != "deny" {
		return errors.New("control input is not read-only")
	}
	bash, ok := permission["bash"].(map[string]any)
	if !ok {
		return errors.New("bash rules are missing")
	}
	for _, pattern := range []string{"/usr/bin/curl *", "/usr/bin/ssh *", "bash *", "curl *", "env *", "gh *", "git commit *", "git push *", "git tag *", "glab *", "nc *", "nohup *", "scp *", "sh *", "ssh *", "sudo *", "wget *", "xargs *"} {
		if bash[pattern] != "deny" {
			return fmt.Errorf("bash pattern %q is not denied", pattern)
		}
	}
	return nil
}

// processFailureError reports a failed opencode process using only fixed
// classification and exit/signal information. Provider stderr is persisted
// separately as a bounded evidence file (opencode-stderr.log) but is never
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

func workerEnvironment(worktree, config string) []string {
	allowed := map[string]bool{"HOME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TEMP": true, "TERM": true, "TMP": true, "TMPDIR": true, "USER": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	environment := make([]string, 0, len(allowed)+8)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "OPENCODE_CONFIG_CONTENT="+config, "PWD="+worktree)
	return environment
}

func terminalWorkerEnvironment(worktree, config string) []string {
	return nativeTTYEnvironment(workerEnvironment(worktree, config))
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

func readModel(worktree, relative string) string {
	path, err := existingPathWithin(worktree, relative)
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

// readOnlyScope carries the TaskSpec path declarations the read-only profile
// needs: artifact allowPaths (the only write domain) and readRoots (extra
// read domain, possibly symlinked outside the worktree).
type readOnlyScope struct {
	allowPaths []string
	readRoots  []string
}

// optionalReadOnlyScope loads and validates the read-only scope only for the
// read-only profile; every other profile receives an empty scope and keeps
// its existing configuration untouched.
func optionalReadOnlyScope(profile, controlRoot, taskSpecPath string) (readOnlyScope, error) {
	if profile != "read-only" {
		return readOnlyScope{}, nil
	}
	return readReadOnlyScope(controlRoot, taskSpecPath)
}

func readReadOnlyScope(controlRoot, taskSpecPath string) (readOnlyScope, error) {
	path, err := existingPathWithin(controlRoot, taskSpecPath)
	if err != nil {
		return readOnlyScope{}, fmt.Errorf("resolve TaskSpec: %w", err)
	}
	data, err := readBounded(path, maxPromptBytes)
	if err != nil {
		return readOnlyScope{}, fmt.Errorf("read TaskSpec: %w", err)
	}
	var spec struct {
		Scope struct {
			AllowPaths []string `json:"allowPaths"`
		} `json:"scope"`
		Worker struct {
			ReadRoots []string `json:"readRoots"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return readOnlyScope{}, errors.New("read-only scope: malformed TaskSpec")
	}
	if len(spec.Scope.AllowPaths) == 0 {
		return readOnlyScope{}, errors.New("read-only scope: TaskSpec declares no artifact allowPaths")
	}
	for _, pattern := range append(append([]string{}, spec.Scope.AllowPaths...), spec.Worker.ReadRoots...) {
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") || pattern == ".." || strings.HasPrefix(pattern, "../") || strings.Contains(pattern, "/../") || strings.HasSuffix(pattern, "/..") {
			return readOnlyScope{}, errors.New("read-only scope: TaskSpec declares an unsafe path pattern")
		}
	}
	return readOnlyScope{allowPaths: spec.Scope.AllowPaths, readRoots: spec.Worker.ReadRoots}, nil
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
	file, err := os.CreateTemp(filepath.Dir(path), ".opencode-*.tmp")
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

func bytesTrimSpace(data []byte) []byte { return []byte(strings.TrimSpace(string(data))) }
