// Package codex implements the bounded Codex CLI Worker adapter.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID      = "codex"
	adapterVersion = "0.1.0"
	// supportedBinary is the exact Codex CLI version this adapter supports;
	// any other version fails closed.
	supportedBinary = "0.145.0"
	maxPromptBytes  = 256 << 10
	maxResultBytes  = 4 << 20
	stderrLimit     = 64 << 10

	// codexVersionPrefix is the exact tool prefix the official Codex CLI
	// reports in its `--version` line before the bare semantic version.
	codexVersionPrefix = "codex-cli"
)

var (
	ErrUnsupportedVersion       = errors.New("unsupported codex version")
	ErrOutputLimit              = errors.New("codex output limit exceeded")
	ErrProtocol                 = errors.New("invalid codex protocol")
	ErrPermissionDenied         = errors.New("codex permission denied")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")
	ErrProcessFailed            = errors.New("codex process failed")

	// codexVersionPattern accepts exactly the bare semantic version core the
	// official Codex CLI reports: three numeric dot-separated components with
	// no leading zeros. Pre-release and build metadata are rejected so any
	// unknown format fails closed rather than being silently mis-normalized.
	codexVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

type Adapter struct {
	executable string
	validator  *contract.Validator
	now        func() time.Time
}

var _ port.WorkerAdapter = (*Adapter)(nil)

// New requires an exact absolute executable path. Marshal never resolves a
// provider executable by a similar name or by an implicit fallback.
func New(executable string, validator *contract.Validator) (*Adapter, error) {
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
	return &Adapter{executable: realPath, validator: validator, now: time.Now}, nil
}

func (a *Adapter) ID() string { return adapterID }

// Probe pins the executable identity and reports a CapabilitySnapshot whose
// probeStatus is "supported" only for the exact supported binary version. It
// never launches a Worker attempt.
func (a *Adapter) Probe(ctx context.Context) (domain.Record, error) {
	identity, err := a.inspect(ctx)
	if err != nil {
		return domain.Record{}, err
	}
	status := "supported"
	probeErrors := []string{}
	if identity.version != supportedBinary {
		status = "unsupported"
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Codex %s，实际为 %s", supportedBinary, identity.version))
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
				"Codex workspace-write sandbox 不是恶意代码隔离边界。",
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
		return executableIdentity{}, errors.New("configured codex executable is unavailable")
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
		return "", fmt.Errorf("probe codex version: %w", err)
	}
	return parseCodexVersion(string(output))
}

// parseCodexVersion normalizes the official `codex-cli <semver>` version line
// into the bare semantic version. It fails closed on empty output, extra
// fields, a missing or unexpected prefix, and any malformed version, so the
// exact supported-version gates in Probe and Run only ever compare bare
// semantic versions.
func parseCodexVersion(output string) (string, error) {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", errors.New("codex returned an empty version")
	}
	if len(fields) != 2 || fields[0] != codexVersionPrefix {
		return "", errors.New("codex returned an unrecognized version")
	}
	version := fields[1]
	if !codexVersionPattern.MatchString(version) {
		return "", errors.New("codex returned a malformed version")
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
		return "", "", errors.New("codex candidate must be an absolute clean path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("codex candidate is not an executable regular file")
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

// Run executes one non-interactive attempt as the composition
// inspect -> local exec -> normalize. Provider/process/protocol failures are
// returned as errors so Core can apply the operational retry budget.
func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindWorkerRequest {
		return domain.Record{}, fmt.Errorf("expected WorkerRequest, got %s", record.Kind)
	}
	request, err := decodeRequest(record.Data, a.validator)
	if err != nil {
		return domain.Record{}, err
	}
	if request.AdapterID != adapterID || request.ExecutionProfile != "workspace-write" {
		return domain.Record{}, errors.New("WorkerRequest does not match the codex adapter execution profile")
	}
	// Fail-closed: persist would write outside the managed state boundary and
	// WorkerRequest carries no managed sessionDir/mapping, so cross-attempt
	// resume cannot be done safely. Never launch a process for it.
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
	observation, err := a.runLocalAttempt(runCtx, identity.path, buildArgs(model, string(prompt)), worktree, workerEnvironment(worktree), int64(request.MaxOutputBytes))
	if err != nil {
		return domain.Record{}, err
	}
	capture := observation.capture
	outputDir := filepath.Dir(resultPath)
	if err := atomicWrite(filepath.Join(outputDir, "codex-transcript.jsonl"), capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(outputDir, "codex-stderr.log"), observation.stderr.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	denialRecords := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: worktree, ControlRoot: controlRoot, TempDir: os.TempDir()}, capture.denials, a.now)
	fatalDenials := denials.CountFatal(denialRecords)
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "eventCount": capture.eventCount,
		"toolCalls": capture.toolCalls, "inputTokens": capture.inputTokens,
		"outputTokens": capture.outputTokens, "capturedBytes": len(capture.raw),
		"outputTruncated": capture.limitExceeded, "permissionDenied": fatalDenials > 0,
		"denialsBenign": len(denialRecords) - fatalDenials, "denialsFatal": fatalDenials, "toolNames": denials.SortedToolNames(capture.toolNames),
		"exitCode": observation.exitCode, "signal": observation.signal, "stderrBytes": len(observation.stderr.data), "stderrTruncated": observation.stderr.truncated,
		"contextError": contextError(runCtx),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, err
	}
	if err := atomicWrite(filepath.Join(outputDir, "codex-transcript-meta.json"), append(metadata, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript metadata: %w", err)
	}
	if err := denials.AppendLog(filepath.Join(outputDir, denials.LogFileName), denialRecords); err != nil {
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
	// Fatal permission denial stays authoritative over a concurrent provider
	// or nonzero process failure, and every provider/process terminal failure
	// is returned before Marshal reads any pre-written, stale, or partial
	// WorkerResult.
	if fatalDenials > 0 {
		return domain.Record{}, ErrPermissionDenied
	}
	if observation.processFailed {
		return domain.Record{}, processFailureError(observation.exitCode, observation.signal)
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
	declared.StartedAt, declared.CompletedAt = observation.startedAt, observation.completedAt
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
