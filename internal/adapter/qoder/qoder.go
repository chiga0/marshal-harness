// Package qoder implements the bounded Qoder CLI Worker adapter.
package qoder

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

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID      = "qoder"
	adapterVersion = "0.1.0"
	// supportedBinary is the exact Qoder CLI version this adapter supports;
	// any other version fails closed.
	supportedBinary = "1.1.23"
	maxPromptBytes  = 256 << 10
	maxResultBytes  = 4 << 20
	stderrLimit     = 64 << 10

	// conformancePendingReason is the fixed, searchable reason Probe reports
	// "unsupported" until a real Qoder CLI live conformance verifies the exact
	// non-interactive argv and JSONL event contract. The hermetic fake
	// fixtures in this package freeze the protocol but never flip this gate;
	// a real capability/live conformance run is the only thing allowed to
	// lift it. --permission-mode and --setting-sources are frozen from the
	// real 1.1.23 help, while --output-format and the JSONL event schema stay
	// unverified until that live conformance captures the real CLI output.
	conformancePendingReason = "live conformance pending: --output-format and the JSONL event schema are unverified against the real Qoder CLI"
)

var (
	ErrUnsupportedVersion       = errors.New("unsupported qoder version")
	ErrOutputLimit              = errors.New("qoder output limit exceeded")
	ErrProtocol                 = errors.New("invalid qoder protocol")
	ErrProcessFailed            = errors.New("qoder process failed")
	ErrUnsupportedSessionPolicy = errors.New("unsupported session policy")

	// qoderVersionPattern accepts exactly the bare semantic version the real
	// Qoder CLI reports from `--version`: a single three-component numeric
	// dot-separated string with no leading zeros, no prefix, and no pre-release
	// or build metadata. Any other shape fails closed rather than being
	// silently mis-normalized. The real CLI prints the bare version (e.g.
	// `1.1.23`), not a `qodercli <semver>` tool line.
	qoderVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
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
	probeErrors := []string{}
	if identity.version != supportedBinary {
		probeErrors = append(probeErrors, fmt.Sprintf("仅支持 Qoder %s，实际为 %s", supportedBinary, identity.version))
	}
	// Always append the conformance gate: even a matching version stays
	// "unsupported" until real capability/live conformance passes.
	probeErrors = append(probeErrors, conformancePendingReason)
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": "unsupported",
		"capabilities": map[string]any{
			"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true,
			"sessionPolicies": []string{"ephemeral"}, "modelSelection": true,
			"executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{},
			"processTreeCancellation": true,
			"notes": []string{
				"由 Marshal 实施 wall-time 与 output-bytes 上限。",
				"Qoder 非交互模式不是恶意代码隔离边界。",
				"执行环境被完整替换：HOME 绑定 Marshal 管理的独立 config dir，user/project/local setting sources 被禁用。",
				"live conformance 尚未执行，本适配器不声明 supported。",
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
	return executableIdentity{a.executable, digest, version}, nil
}

// readBinaryVersion runs `<executable> --version` inside the sanitized probe
// environment and parses the bare version string reported by the binary.
func readBinaryVersion(ctx context.Context, executable string) (string, error) {
	command := exec.CommandContext(ctx, executable, "--version")
	command.Env = probeEnvironment()
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
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
	// Bind the Marshal-managed, isolated config dir before launching anything:
	// user/project/local settings must never influence the attempt, and a
	// symlink, escape, or abnormal permission must fail closed up front.
	configDir, err := managedConfigDir(controlRoot)
	if err != nil {
		return domain.Record{}, err
	}
	model := readModel(controlRoot, request.TaskSpecPath)
	observation, err := a.runLocalAttempt(runCtx, identity.path, buildArgs(model, configDir, string(prompt)), worktree, workerEnvironment(worktree, configDir), int64(request.MaxOutputBytes))
	if err != nil {
		return domain.Record{}, err
	}
	capture := observation.capture
	outputDir := filepath.Dir(resultPath)
	if err := atomicWrite(filepath.Join(outputDir, "qoder-transcript.jsonl"), capture.raw); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := atomicWrite(filepath.Join(outputDir, "qoder-stderr.log"), observation.stderr.data); err != nil {
		return domain.Record{}, fmt.Errorf("write bounded stderr: %w", err)
	}
	resolved := resolveAttemptFailure(capture, observation, runCtx, a.now())
	var declared declaredResult
	if resolved == nil {
		declared, resolved = resolveDeclaredResult(resultPath, request, capture.sessionID, a.validator, a.now())
	}
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "eventCount": capture.eventCount,
		"inputTokens": capture.inputTokens, "outputTokens": capture.outputTokens,
		"capturedBytes": len(capture.raw), "outputTruncated": capture.limitExceeded,
		"exitCode": observation.exitCode, "signal": observation.signal,
		"stderrBytes": len(observation.stderr.data), "stderrTruncated": observation.stderr.truncated,
		"contextError": contextError(runCtx),
		"failureKind":  failureKindOf(resolved), "retryDisposition": retryDispositionOf(resolved),
	}, "", "  ")
	if err != nil {
		return domain.Record{}, err
	}
	if err := atomicWrite(filepath.Join(outputDir, "qoder-transcript-meta.json"), append(metadata, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write transcript metadata: %w", err)
	}
	if resolved != nil {
		return domain.Record{}, resolved
	}
	declared.Adapter.Executable, declared.Adapter.Version = identity.path, identity.version
	declared.Session = &declaredSession{ID: capture.sessionID, Resumable: false}
	declared.StartedAt, declared.CompletedAt = observation.startedAt, observation.completedAt
	if model != "" {
		declared.Adapter.Model = model
	}
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
	if err := atomicWrite(resultPath, append(data, '\n')); err != nil {
		return domain.Record{}, fmt.Errorf("write normalized WorkerResult: %w", err)
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, nil
}

// resolveAttemptFailure orders terminal conditions before the WorkerResult is
// read. Context cancellation/deadline, output truncation, malformed protocol,
// a terminal provider failure, and process failure all fail closed in fixed
// precedence; a successful run must then carry a session id and a success
// terminal before the declaration is trusted.
func resolveAttemptFailure(capture captureResult, observation attemptObservation, runCtx context.Context, now time.Time) error {
	if err := runCtx.Err(); err != nil {
		return err
	}
	if capture.limitExceeded {
		return ErrOutputLimit
	}
	if capture.err != nil {
		return capture.err
	}
	if capture.terminal.seen && !capture.terminal.success {
		return classifyTerminalFailure(capture.terminal.code, now)
	}
	if observation.processFailed {
		return processFailureError(observation.exitCode, observation.signal)
	}
	if capture.sessionID == "" {
		return fmt.Errorf("%w: session id is missing", ErrProtocol)
	}
	if !capture.terminal.seen {
		return fmt.Errorf("%w: terminal result event is missing", ErrProtocol)
	}
	return nil
}

// resolveDeclaredResult reads and validates the declared WorkerResult and
// returns a typed failure when the declaration is missing, unreadable,
// schema-invalid, or carries an identity/session that does not match the
// request and transcript.
func resolveDeclaredResult(resultPath string, request workerRequest, sessionID string, validator *contract.Validator, now time.Time) (declaredResult, error) {
	declared, err := readDeclaredResult(resultPath, int64(maxResultBytes), validator)
	if err != nil {
		return declaredResult{}, newQoderFailure(port.FailureKindResultMissing, "WorkerResult declaration missing or unreadable", now)
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

func readDeclaredResult(path string, limit int64, validator *contract.Validator) (declaredResult, error) {
	data, err := readBounded(path, limit)
	if err != nil {
		return declaredResult{}, fmt.Errorf("read WorkerResult declaration: %w", err)
	}
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}
