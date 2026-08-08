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
	adapterID       = "pi"
	adapterVersion  = "0.1.0"
	supportedBinary = "0.83.0"
	// supportedSessionVersion is the exact pi session event protocol version
	// Marshal accepts. Any other header version is a protocol violation.
	supportedSessionVersion = 3
	maxPromptBytes          = 256 << 10
	maxResultBytes          = 4 << 20
	stderrLimit             = 64 << 10
)

// workerTools is the frozen tool allowlist. bash is never granted and the
// list never grows implicitly: Marshal passes it via direct argv only.
const workerTools = "read,grep,find,ls,write,edit"

var (
	ErrUnsupportedVersion       = errors.New("unsupported pi version")
	ErrOutputLimit              = errors.New("pi output limit exceeded")
	ErrProtocol                 = errors.New("invalid pi protocol")
	ErrPermissionDenied         = errors.New("pi permission denied")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")
	ErrProcessFailed            = errors.New("pi process failed")
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
	if request.AdapterID != adapterID || request.ExecutionProfile != "workspace-write" {
		return port.TerminalLaunchSpec{}, errors.New("WorkerRequest does not match the pi workspace-write adapter")
	}
	if request.SessionPolicy != "ephemeral" {
		return port.TerminalLaunchSpec{}, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
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
	return port.TerminalLaunchSpec{
		AdapterID: adapterID, AdapterVersion: adapterVersion, RunID: request.RunID, AttemptID: request.AttemptID, BinaryVersion: identity.version,
		Executable: identity.path, ExecutableDigest: identity.digest, WorkingDirectory: worktree,
		Arguments:   buildTerminalArgs(readModel(controlRoot, request.TaskSpecPath)),
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
	if identity.version != supportedBinary {
		status = "unsupported"
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Pi %s，实际为 %s", supportedBinary, identity.version))
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
			"executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{},
			"processTreeCancellation": true,
			"notes": []string{
				"由 Marshal 实施 wall-time 与 output-bytes 上限。",
				"工具白名单固定为 " + workerTools + "，永不授予 bash。",
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
	command := exec.CommandContext(ctx, a.executable, "--version")
	command.Env = probeEnvironment()
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return executableIdentity{}, ctx.Err()
		}
		return executableIdentity{}, fmt.Errorf("probe pi version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return executableIdentity{}, errors.New("pi returned an empty version")
	}
	return executableIdentity{a.executable, digest, version}, nil
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
	if request.AdapterID != adapterID || request.ExecutionProfile != "workspace-write" {
		return domain.Record{}, errors.New("WorkerRequest does not match the pi workspace-write adapter")
	}
	// Fail-closed: persist would write into the user's default pi session
	// directory (outside the managed state boundary) and WorkerRequest has
	// no managed sessionDir/mapping, so cross-attempt resume cannot be done
	// safely. Both are permanent, unsupported errors; never launch a process.
	if request.SessionPolicy != "ephemeral" {
		return domain.Record{}, fmt.Errorf("%w: %q is permanently unsupported; only ephemeral sessions are managed by Marshal", ErrUnsupportedSessionPolicy, request.SessionPolicy)
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
	args := buildArgs(model, string(prompt))
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
		return domain.Record{}, fmt.Errorf("start pi: %w", err)
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { terminateGroup(command) }) }
	stdoutDone := make(chan captureResult, 1)
	stderrDone := make(chan streamCapture, 1)
	go func() { stdoutDone <- captureJSONL(stdout, worktree, int64(request.MaxOutputBytes), kill) }()
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
	transcriptPath := filepath.Join(filepath.Dir(resultPath), "pi-transcript.jsonl")
	if err := atomicWrite(transcriptPath, capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(filepath.Dir(resultPath), "pi-stderr.log"), stderrCapture.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	exitCode, signal := processOutcome(command)
	denialRecords := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: worktree, ControlRoot: controlRoot, TempDir: os.TempDir()}, capture.denials, a.now)
	fatalDenials := denials.CountFatal(denialRecords)
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "eventCount": capture.eventCount,
		"toolCalls": capture.toolCalls, "inputTokens": capture.inputTokens,
		"outputTokens": capture.outputTokens, "cachedInputTokens": capture.cachedInputTokens,
		"cost": capture.cost, "capturedBytes": len(capture.raw),
		"outputTruncated": capture.limitExceeded, "permissionDenied": fatalDenials > 0,
		"denialsBenign": len(denialRecords) - fatalDenials, "denialsFatal": fatalDenials,
		"exitCode": exitCode, "signal": signal, "stderrBytes": len(stderrCapture.data), "stderrTruncated": stderrCapture.truncated,
		"contextError": contextError(runCtx),
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
	if model != "" {
		declared.Adapter.Model = model
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
	limitExceeded     bool
	err               error
}

// piEvent covers only the fields Marshal validates. Unknown fields are
// ignored on purpose; protocol decisions rely solely on type, version, id,
// cwd, and the terminal agent_end event.
type piEvent struct {
	Type       string          `json:"type"`
	Version    *int            `json:"version"`
	ID         string          `json:"id"`
	Cwd        string          `json:"cwd"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Args       json.RawMessage `json:"args"`
	IsError    *bool           `json:"isError"`
	Error      string          `json:"error"`
	Messages   []struct {
		Role  string `json:"role"`
		Usage *struct {
			Input      int       `json:"input"`
			Output     int       `json:"output"`
			CacheRead  int       `json:"cacheRead"`
			CacheWrite int       `json:"cacheWrite"`
			Cost       usageCost `json:"cost"`
		} `json:"usage"`
	} `json:"messages"`
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

// captureJSONL enforces the strict pi session protocol:
//   - the first event must be the session header with version exactly 3 and
//     cwd equal to the resolved attempt worktree;
//   - every line must decode as JSON;
//   - termination is exactly one agent_end, optionally followed by exactly one
//     agent_settled event; no other event may follow agent_end.
//
// Output is bounded; exceeding the limit or detecting a protocol violation
// kills the process group immediately.
func captureJSONL(reader io.Reader, worktree string, limit int64, onLimit func()) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var consumed int64
	var line []byte
	lastType := ""
	sawAgentEnd := false
	sawAgentSettled := false
	pending := map[string]json.RawMessage{}
	fail := func(reason error) {
		if result.err == nil {
			result.err = reason
		}
		onLimit()
	}
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
				var event piEvent
				if decodeErr := json.Unmarshal(trimmed, &event); decodeErr != nil {
					fail(fmt.Errorf("%w: malformed JSONL: %v", ErrProtocol, decodeErr))
					continue
				}
				result.eventCount++
				switch {
				case result.eventCount == 1:
					if event.Type != "session" {
						fail(fmt.Errorf("%w: first event must be the session header, got %q", ErrProtocol, event.Type))
						continue
					}
					if event.Version == nil || *event.Version != supportedSessionVersion {
						fail(fmt.Errorf("%w: session header version must be %d", ErrProtocol, supportedSessionVersion))
						continue
					}
					if filepath.Clean(event.Cwd) != worktree {
						fail(fmt.Errorf("%w: session cwd %q does not match worktree %q", ErrProtocol, event.Cwd, worktree))
						continue
					}
					result.sessionID = event.ID
				case sawAgentSettled:
					fail(fmt.Errorf("%w: event %q follows terminal agent_settled", ErrProtocol, event.Type))
					continue
				case sawAgentEnd:
					if event.Type != "agent_settled" {
						fail(fmt.Errorf("%w: event %q follows terminal agent_end", ErrProtocol, event.Type))
						continue
					}
					sawAgentSettled = true
				case event.Type == "agent_settled":
					fail(fmt.Errorf("%w: agent_settled appeared before agent_end", ErrProtocol))
					continue
				case event.Type == "tool_execution_start":
					result.toolCalls++
					if event.ToolCallID != "" && len(pending) < 4096 {
						pending[event.ToolCallID] = event.Args
					}
				case event.Type == "tool_execution_end":
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
					}
				case event.Type == "agent_end":
					sawAgentEnd = true
					for _, message := range event.Messages {
						if message.Role != "assistant" || message.Usage == nil {
							continue
						}
						result.inputTokens += message.Usage.Input
						result.outputTokens += message.Usage.Output
						result.cachedInputTokens += message.Usage.CacheRead
						nextCost := result.cost + message.Usage.Cost.value
						if !isFinite(nextCost) {
							fail(fmt.Errorf("%w: usage cost sum is not finite", ErrProtocol))
							continue
						}
						result.cost = nextCost
					}
				}
				lastType = event.Type
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if !errors.Is(err, io.EOF) && result.err == nil {
				result.err = err
			}
			if result.err == nil && !result.limitExceeded && !sawAgentEnd {
				result.err = fmt.Errorf("%w: stream ended without agent_end (last event %q)", ErrProtocol, lastType)
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

// hardeningFlags is the frozen, ordered hardening surface. Every flag is
// listed exactly once here; buildArgs copies it verbatim so no hardening
// flag can ever appear twice in the argv Marshal hands to pi.
var hardeningFlags = []string{
	"--mode", "json", "--print", "--no-approve",
	"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
	"--tools", workerTools,
	"--no-session",
}

// buildArgs produces the exact hardened argv. Sessions are always disabled:
// Marshal only supports ephemeral attempts. The prompt is always the final
// positional argument; Marshal never invokes pi through a shell.
func buildArgs(model, prompt string) []string {
	args := append([]string{}, hardeningFlags...)
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

func buildTerminalArgs(model string) []string {
	args := []string{
		"--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates",
		"--no-themes", "--no-context-files", "--tools", workerTools, "--no-session",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// processFailureError reports a failed pi process using only fixed
// classification and exit/signal information. Provider stderr is persisted
// separately as a bounded evidence file (pi-stderr.log) but is never
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
