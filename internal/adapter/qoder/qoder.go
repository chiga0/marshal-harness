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
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	adapterID      = "qoder"
	adapterVersion = "0.1.0"
	// supportedBinary is the minimum verified patch in the compatible 1.1.x
	// line. Other minor/major lines and older patches fail closed.
	supportedBinary          = "1.1.23"
	supportedBinaryRange     = ">=1.1.23 <1.2.0"
	maxPromptBytes           = 256 << 10
	maxResultBytes           = 4 << 20
	stderrLimit              = 64 << 10
	versionOutputLimit       = 4 << 10
	versionStderrLimit       = 4 << 10
	probeTimeout             = 10 * time.Second
	conformanceEventContract = "qoder-stream-json-1.2.0-v1"
	qoderProtocolVersion     = "1.2.0"
	qoderPermissionMode      = "acceptEdits"

	// conformancePendingReason is the fixed, searchable reason Probe reports
	// "unsupported" until an independent, credentialed live run verifies the
	// frozen 1.1.23 argv and stream-json event contract. Hermetic fixtures and
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
	executable string
	validator  *contract.Validator
	now        func() time.Time
	authority  *AuthorityEvidenceStore

	mu          sync.Mutex
	pinned      *executableIdentity
	conformance *boundConformance
}

var _ port.WorkerAdapter = (*Adapter)(nil)

// New requires an exact absolute executable path. Marshal never resolves a
// provider executable by a similar name or by an implicit fallback.
func New(executable string, validator *contract.Validator) (*Adapter, error) {
	return NewWithConformanceAuthority(executable, validator, nil)
}

// NewWithConformanceAuthority binds an authority-owned evidence store. A nil
// store is deliberately valid and leaves Probe permanently unsupported; it is
// the default application wiring until independently signed live evidence is
// configured.
func NewWithConformanceAuthority(executable string, validator *contract.Validator, authority *AuthorityEvidenceStore) (*Adapter, error) {
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
	return &Adapter{executable: realPath, validator: validator, now: time.Now, authority: authority}, nil
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
	if !a.isConformant(identity) {
		probeErrors = append(probeErrors, conformancePendingReason)
	} else if isSupportedBinaryVersion(identity.version) {
		status = "supported"
	}
	snapshot := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindCapabilitySnapshot),
		"adapterId": adapterID, "adapterVersion": adapterVersion,
		"executable": identity.path, "executableDigest": identity.digest,
		"binaryVersion": identity.version, "probeStatus": status,
		"capabilities": expectedCapabilities(),
		"probeErrors":  probeErrors, "probedAt": a.now().UTC().Format(time.RFC3339Nano),
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

type executableIdentity struct{ path, digest, version string }

// boundConformance retains the authority evidence freshness boundary as well
// as the executable identity. Keeping only the identity would turn a
// time-limited conformance statement into permanent admission after one
// successful BindConformance call.
type boundConformance struct {
	identity       executableIdentity
	evidenceDigest string
	validUntil     time.Time
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
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conformance != nil && a.conformance.identity == identity && a.now().UTC().Before(a.conformance.validUntil)
}

// BindConformance accepts only a content digest. The corresponding record is
// resolved and signature-verified by the authority store fixed at adapter
// construction; callers cannot authorize execution with a locally populated
// struct or a rewritten CapabilitySnapshot.
func (a *Adapter) BindConformance(ctx context.Context, evidenceDigest string) error {
	if a.authority == nil {
		return port.Permanent(ErrConformancePending)
	}
	evidence, err := a.authority.resolve(ctx, evidenceDigest, a.now().UTC())
	if err != nil {
		return err
	}
	identity, err := a.inspect(ctx)
	if err != nil {
		return err
	}
	if !isSupportedBinaryVersion(identity.version) {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, identity.version)
	}
	if evidence.Executable != identity.path || evidence.ExecutableDigest != identity.digest || evidence.BinaryVersion != identity.version || evidence.QoderCLIVersion != identity.version {
		return fmt.Errorf("%w: conformance identity does not match current executable", ErrIdentityDrift)
	}
	validUntil, err := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if err != nil {
		// resolve already validates this field; keep this guard local so the
		// stored admission boundary can never become a zero-time wildcard.
		return port.Permanent(ErrConformancePending)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	pinned := identity
	a.pinned = &pinned
	a.conformance = &boundConformance{identity: identity, evidenceDigest: evidence.EvidenceDigest, validUntil: validUntil}
	return nil
}

func (a *Adapter) verifyExecutionIdentity(identity executableIdentity) error {
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
	return major == 1 && minor == 1 && patch >= 23
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
	configDir, err := managedConfigDir(controlRoot)
	if err != nil {
		return domain.Record{}, err
	}
	task, err := readTaskProjection(controlRoot, request.TaskSpecPath)
	if err != nil {
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
	observation, err := a.runLocalAttempt(runCtx, launchExecutable, buildArgs(task.model, configDir, worktree, task.disableAllTools), prompt, worktree, workerEnvironment(worktree, configDir), int64(request.MaxOutputBytes))
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
	resolved := resolveAttemptFailure(capture, observation, runCtx, a.now())
	if resolved == nil && (capture.cliVersion != identity.version || capture.protocolVersion != qoderProtocolVersion || capture.permissionMode != qoderPermissionMode) {
		resolved = qoderProtocolInvalid("system contract does not match the bound Qoder protocol", a.now())
	}
	if bindingErr := trustedOutput.verifyPathBinding(); bindingErr != nil {
		resolved = qoderProtocolInvalid("output directory binding changed during execution", a.now())
	}
	var declared declaredResult
	if resolved == nil {
		declared, resolved = resolveDeclaredResultLeaf(claimedResult, request, capture.sessionID, a.validator, a.now())
	}
	if resolved == nil && task.model != "" && capture.model != task.model {
		resolved = qoderProtocolInvalid("system model does not match requested model", a.now())
	}
	metadata, err := json.MarshalIndent(map[string]any{
		"sessionId": capture.sessionID, "model": capture.model,
		"qodercliVersion": capture.cliVersion, "protocolVersion": capture.protocolVersion, "permissionMode": capture.permissionMode,
		"eventCount": capture.eventCount, "assistantMessages": capture.assistantCount,
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
		return qoderProtocolInvalid("malformed or inconsistent stream-json transcript", now)
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

func resolveDeclaredResultLeaf(leaf *claimedLeaf, request workerRequest, sessionID string, validator *contract.Validator, now time.Time) (declaredResult, error) {
	data, err := leaf.readBounded(int64(maxResultBytes))
	if err != nil {
		return declaredResult{}, newQoderFailure(port.FailureKindResultMissing, "WorkerResult declaration missing or unreadable", now)
	}
	declared, err := decodeDeclaredResult(data, validator)
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

func decodeDeclaredResult(data []byte, validator *contract.Validator) (declaredResult, error) {
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return declaredResult{}, fmt.Errorf("validate WorkerResult declaration: %w", err)
	}
	var result declaredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}
