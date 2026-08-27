package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

var (
	// ErrAllocationNotFound is returned when no allocation carries the
	// requested allocationId.
	ErrAllocationNotFound = errors.New("sandbox: allocation not found")
	// ErrAllocationNotActive is returned when an operation requires an
	// active allocation but the addressed allocation is in any other state.
	ErrAllocationNotActive = errors.New("sandbox: allocation is not active")
	// ErrResponseLost is returned when the provider drops a response, as the
	// deterministic fake provider does under the drop-response fault.
	ErrResponseLost = errors.New("sandbox: provider response lost")
	// ErrInvalidSignal rejects signal names outside the closed enumeration.
	ErrInvalidSignal = errors.New("sandbox: invalid signal name")
	// ErrInvalidRequest rejects a malformed provider request after the
	// operation identity already validated.
	ErrInvalidRequest = errors.New("sandbox: invalid provider request")
	// ErrInvalidWorkDir rejects an Exec WorkingDir or a Provision-time
	// workDirAllowlist entry that is not an absolute path, does not exist
	// provider side, or resolves outside the declared binding set of the
	// allocation (ADR 0055 §1).
	ErrInvalidWorkDir = errors.New("sandbox: invalid working directory")
	// ErrEnvKeyNotAllowed rejects a per-op environment key outside the
	// allocation's closed Provision-time environmentAllowlist; providers
	// must never union unknown keys into the execution environment
	// (ADR 0055 §2).
	ErrEnvKeyNotAllowed = errors.New("sandbox: environment key outside the allocation environmentAllowlist")
	// ErrCredentialKeyRejected rejects any environment key carrying
	// credential semantics, both at Provision-time allowlist registration
	// and at exec time (ADR 0055 §2.4, ADR 0018: credentials never flow
	// through the workload envelope, events or logs).
	ErrCredentialKeyRejected = errors.New("sandbox: credential semantic environment keys are rejected")
	// ErrInvalidTranscriptPolicy rejects a malformed bounded transcript
	// declaration: MaxBytes must be positive and ArtifactId must be a
	// non-empty relative path inside the allocation (ADR 0055 §3).
	ErrInvalidTranscriptPolicy = errors.New("sandbox: invalid transcript policy")
	// ErrTranscriptLimitExceeded freezes the fail-closed transcript bound:
	// the workload is killed the moment the stdout capture exceeds
	// MaxBytes and no partial capture is ever reported as a successful
	// execution (ADR 0055 §3.2).
	ErrTranscriptLimitExceeded = errors.New("sandbox: transcript capture exceeded the MaxBytes bound, workload killed")
)

// Operation names of the SPI, used by fault injection and diagnostics.
const (
	OperationProbe      = "probe"
	OperationProvision  = "provision"
	OperationStage      = "stage"
	OperationExec       = "exec"
	OperationInspect    = "inspect"
	OperationSignal     = "signal"
	OperationCheckpoint = "checkpoint"
	OperationRestore    = "restore"
	OperationTerminate  = "terminate"
	OperationReconcile  = "reconcile"
)

// Adversarial probe command tokens: the conformance suite drives them
// through Exec as simulated hostile workload, and a conformant provider
// contains every one of them.
const (
	ProbeCommandBoundaryWrite    = "sandbox-probe:boundary-write"
	ProbeCommandSensitiveEnvRead = "sandbox-probe:sensitive-env-read"
	ProbeCommandSpawnFlood       = "sandbox-probe:spawn-flood"
)

// Boundary violation kinds reported by Inspect.
const (
	ViolationOutOfBoundsWrite   = "out-of-bounds-write"
	ViolationSensitiveEnvRead   = "sensitive-env-read"
	ViolationSpawnLimitExceeded = "spawn-limit-exceeded"
)

// SignalName is the closed enumeration of process signals the SPI delivers.
type SignalName string

// Closed members of SignalName.
const (
	SignalTerm      SignalName = "term"
	SignalKill      SignalName = "kill"
	SignalInterrupt SignalName = "interrupt"
)

// Validate rejects every value outside the closed enumeration.
func (signal SignalName) Validate() error {
	switch signal {
	case SignalTerm, SignalKill, SignalInterrupt:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSignal, string(signal))
	}
}

// ExecutionStatus is the closed enumeration of exec receipt statuses.
type ExecutionStatus string

// Closed members of ExecutionStatus.
const (
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionKilled    ExecutionStatus = "killed"
)

// Validate rejects every value outside the closed enumeration.
func (status ExecutionStatus) Validate() error {
	switch status {
	case ExecutionCompleted, ExecutionFailed, ExecutionKilled:
		return nil
	default:
		return fmt.Errorf("sandbox: invalid execution status %q", string(status))
	}
}

// SandboxAllocation is one provider-scoped execution environment. The
// AllocationId is an opaque locator with provider-internal semantics: Core
// never interprets it and only ever compares it for equality. The record
// freezes the effective two-dimensional combination granted by the provider
// (AccessMode x AssuranceLevel), the optional conformance evidence digest
// and the closed set of artifact store aliases bound at provision time.
type SandboxAllocation struct {
	AllocationId           string                `json:"allocationId"`
	RunId                  string                `json:"runId"`
	AttemptId              string                `json:"attemptId"`
	Generation             int64                 `json:"generation"`
	State                  AllocationState       `json:"state"`
	AccessMode             domain.AccessMode     `json:"accessMode"`
	AssuranceLevel         domain.AssuranceLevel `json:"assuranceLevel"`
	ConformanceEvidenceRef string                `json:"conformanceEvidenceRef"`
	AllowedStoreIds        []string              `json:"allowedStoreIds"`
	// WorkDirAllowlist snapshots the closed ADR 0055 §1 working-root
	// declaration granted at Provision time; absent when the attempt never
	// declared one.
	WorkDirAllowlist []string `json:"workDirAllowlist,omitempty"`
	// EnvironmentAllowlist snapshots the closed ADR 0055 §2 environment key
	// declaration granted at Provision time; absent when the attempt never
	// declared one.
	EnvironmentAllowlist []string `json:"environmentAllowlist,omitempty"`
}

// SandboxProvider is the ten-operation SPI of ADR 0016 §4. Every
// dispatch-bound request carries an OperationIdentity; a provider must fail
// closed before any side effect when the identity is invalid, when the
// addressed allocation is unknown or not active, or when the identity
// generation is stale. Every receipt a provider returns is an observation,
// never authority: fencing, single-active and conformance adjudication
// never trust a receipt alone.
type SandboxProvider interface {
	// Probe reports whether the provider can serve the requested
	// requirements. Input: operation identity plus requirements. Output: a
	// support flag with reason, the provider's conformance evidence
	// reference (empty when it holds none) and any self-signed pass claim,
	// which callers must ignore as evidence. Fail closed: an invalid
	// identity returns an error and no report.
	Probe(ctx context.Context, request ProbeRequest) (*ProbeReport, error)

	// Provision creates the allocation that all later operations address.
	// Input: identity (its allocationId is the locator granted), the
	// two-dimensional requirements and the artifact store aliases to bind.
	// Output: a receipt observing the granted allocation. Fail closed: a
	// hardened request without valid evidence is refused outright
	// (ErrAssuranceNotMet) and never downgraded; a second concurrent
	// allocation for the same (runId, attemptId) and generation is rejected
	// (ErrDuplicateActiveAllocation).
	Provision(ctx context.Context, request ProvisionRequest) (*ProvisionReceipt, error)

	// Stage materializes content-addressed inputs inside the allocation.
	// Input: identity, allocation locator and the stage inputs. Output: one
	// receipt per input carrying the digests the provider recomputed before
	// and after consumption. Fail closed: a pre-consumption digest mismatch
	// fails the attempt with ErrStageInputMismatch and produces no receipt.
	Stage(ctx context.Context, request StageRequest) (*StageReport, error)

	// Exec runs one workload command inside the allocation. Input:
	// identity, allocation locator, the command and the optional ADR 0055
	// workload envelope (WorkingDir/Environment/TranscriptPolicy/
	// TimeoutSeconds), adjudicated fail closed against the closed
	// Provision-time declarations. Output: a receipt whose status is a
	// lifecycle guard only; conformance adjudication never reads the
	// verdict from it. Fail closed: invalid identity, unknown or inactive
	// allocation, a stale generation, or any envelope dimension outside the
	// declared bindings, return an error and the command never executes.
	Exec(ctx context.Context, request ExecRequest) (*ExecReceipt, error)

	// Inspect returns the out-of-band observation of the allocation:
	// recorded boundary violations, bounded log lines, spawn count and the
	// last exit code. Input: identity and allocation locator. Output: the
	// observation report. Inspect is the adjudication channel of the
	// conformance suite; it must reflect observed state, never the
	// provider's self-report.
	Inspect(ctx context.Context, request InspectRequest) (*InspectReport, error)

	// Signal delivers one closed-enumeration signal to the allocation's
	// workload. Input: identity, allocation locator and signal name.
	// Output: a delivery receipt. Fail closed like Exec.
	Signal(ctx context.Context, request SignalRequest) (*SignalReceipt, error)

	// Checkpoint snapshots the staged content of the allocation. Input:
	// identity and allocation locator. Output: a receipt with the
	// deterministic checkpoint id, the sha256 digest and the size of the
	// snapshot. Fail closed like Exec.
	Checkpoint(ctx context.Context, request CheckpointRequest) (*CheckpointReceipt, error)

	// Restore applies the frozen restore semantics of PlanRestore. Input:
	// identity carrying the post-restore generation, the previous
	// allocation locator, the replacement locator (replacement mode) and the
	// caller's in-place confirmation. Output: a receipt observing the next
	// allocation. Fail closed: an unconfirmed in-place restore, a stale
	// generation, or a reuse of the previous allocationId in replacement
	// mode, return an error; the previous allocation becomes replaced.
	Restore(ctx context.Context, request RestoreOperationRequest) (*RestoreReceipt, error)

	// Terminate ends the allocation. Input: identity and allocation
	// locator. Output: a receipt observing the terminal state. Terminating
	// an already terminal allocation is idempotent. Fail closed on a stale
	// generation.
	Terminate(ctx context.Context, request TerminateRequest) (*TerminateReceipt, error)

	// Reconcile reports the provider's view of one (runId, attemptId)
	// scope: the active allocation ids at the current generation, orphaned
	// stale-generation actives, and whether drift was detected. Input:
	// identity bound to the scope. Output: the reconciliation report. Fail
	// closed on an invalid identity or a scope the identity does not bind.
	Reconcile(ctx context.Context, request ReconcileRequest) (*ReconcileReport, error)
}

// ProbeRequest is the input of Probe.
type ProbeRequest struct {
	Identity     OperationIdentity
	Requirements domain.SandboxRequirements
}

// ProbeReport is the output of Probe.
type ProbeReport struct {
	Supported              bool
	Reason                 string
	ConformanceEvidenceRef string
	// SelfSignedConformanceClaim is the provider's own pass claim. It is a
	// self-report and must never be treated as conformance evidence by any
	// verifier or suite; adjudication reads only out-of-band observations.
	SelfSignedConformanceClaim bool
}

// ProvisionRequest is the input of Provision. WorkDirAllowlist and
// EnvironmentAllowlist carry the optional ADR 0055 declarations of the
// requesting attempt: the provider validates them fail closed (absolute
// paths, closed key shape, no credential semantics) and records the granted
// binding in the allocation record, and every later Exec envelope is
// adjudicated against that closed declaration.
type ProvisionRequest struct {
	Identity        OperationIdentity
	Requirements    domain.SandboxRequirements
	AllowedStoreIds []string
	// WorkDirAllowlist declares the closed set of absolute paths any later
	// Exec WorkingDir of this attempt may bind to (ADR 0055 §1.1); an
	// empty value keeps the ADR 0017 allocation-directory cwd only.
	WorkDirAllowlist []string
	// EnvironmentAllowlist declares the closed set of environment keys any
	// later Exec Environment of this attempt may carry (ADR 0055 §2.2);
	// credential-semantic keys are rejected at registration time.
	EnvironmentAllowlist []string
}

// ProvisionReceipt observes the provisioned allocation. It is an
// observation, not an authority grant: the requirements gate and the
// conformance suite adjudicate the granted combination independently.
type ProvisionReceipt struct {
	Allocation SandboxAllocation
}

// StageRequest is the input of Stage.
type StageRequest struct {
	Identity     OperationIdentity
	AllocationId string
	Inputs       []StageInput
}

// StageReport is the output of Stage: one receipt per staged input.
type StageReport struct {
	Receipts []StageReceipt
}

// TranscriptPolicy declares the bounded transcript sink of one Exec op
// (ADR 0055 §3). The provider captures the op's stdout bounded by MaxBytes
// in an append-only capture; the moment the capture exceeds MaxBytes the
// workload is killed fail closed (ExecutionKilled plus the closed reason,
// never a partial result reported as success). On clean completion the
// provider writes the transcript as one content-addressed staged artifact
// of the allocation under ArtifactId, recomputes its sha256 digest and
// echoes it in the ExecReceipt; stderr follows the identical capture bound
// but participates digest-only, without any artifact. Semantic
// interpretation of the transcript never belongs to this SPI.
type TranscriptPolicy struct {
	MaxBytes   int64
	ArtifactId string
}

// Absent reports whether the policy is unset (the zero value), which keeps
// the ADR 0017 digest-only observation behavior exactly.
func (policy TranscriptPolicy) Absent() bool {
	return policy == TranscriptPolicy{}
}

// Validate fails closed unless MaxBytes is positive and ArtifactId is a
// non-empty relative path without parent traversal.
func (policy TranscriptPolicy) Validate() error {
	if policy.MaxBytes <= 0 {
		return fmt.Errorf("%w: MaxBytes must be a positive integer", ErrInvalidTranscriptPolicy)
	}
	if strings.TrimSpace(policy.ArtifactId) == "" {
		return fmt.Errorf("%w: ArtifactId must be a non-empty string", ErrInvalidTranscriptPolicy)
	}
	if filepath.IsAbs(policy.ArtifactId) {
		return fmt.Errorf("%w: ArtifactId must be a relative path inside the allocation", ErrInvalidTranscriptPolicy)
	}
	for _, part := range strings.Split(filepath.ToSlash(policy.ArtifactId), "/") {
		if part == ".." {
			return fmt.Errorf("%w: ArtifactId escapes the allocation directory", ErrInvalidTranscriptPolicy)
		}
	}
	return nil
}

// ExecRequest is the input of Exec. The four optional envelope dimensions
// of ADR 0055 (WorkingDir, Environment, TranscriptPolicy, TimeoutSeconds)
// are strictly additive: the zero envelope keeps the ADR 0017 behavior
// exactly — cwd bound to the allocation directory, the sanitized baseline
// environment, digest-only output observation and the provider-internal
// default timeout.
type ExecRequest struct {
	Identity     OperationIdentity
	AllocationId string
	Command      []string
	Stdin        []byte
	// WorkingDir is the optional absolute execution root (ADR 0055 §1): it
	// must have been declared by the same attempt at Provision time and
	// recorded in the allocation record (WorkDirAllowlist), it must exist
	// provider side, and its symlink-resolved target must equal a declared
	// target — any path rewrite or soft-link traversal into an undeclared
	// target is rejected fail closed. A WorkingDir declares the execution
	// root only; it grants no extra filesystem write authority.
	WorkingDir string
	// Environment carries the optional per-op allow-listed environment
	// values (ADR 0055 §2): every key must be a member of the allocation's
	// Provision-time EnvironmentAllowlist and must not carry credential
	// semantics; all other keys keep the provider's sanitized baseline.
	Environment map[string]string
	// TranscriptPolicy is the optional bounded transcript sink declaration
	// (ADR 0055 §3); the zero value disables raw capture and keeps the
	// digest-only observation of ADR 0017.
	TranscriptPolicy TranscriptPolicy
	// TimeoutSeconds is the optional per-op timeout (ADR 0055 §4): a
	// positive value takes effect as min(TimeoutSeconds, the provider cap)
	// together with the context deadline, and any non-positive value keeps
	// the provider default. A timeout kills the workload and observes
	// ExecutionKilled, never the normal-completion branch.
	TimeoutSeconds int64
}

// ValidateEnvelope validates the provider-independent shape of the optional
// ADR 0055 envelope dimensions fail closed: WorkingDir must be absolute
// when set, environment keys must be well-formed and free of credential
// semantics, and the transcript policy — when present — must be
// well-formed. The zero envelope validates clean. Providers run it after
// resolving the allocation and adjudicate the declared bindings (allowlist
// membership, provider-side existence, symlink-resolved target equality)
// on top of it.
func (request ExecRequest) ValidateEnvelope() error {
	if request.WorkingDir != "" && !filepath.IsAbs(request.WorkingDir) {
		return fmt.Errorf("%w: WorkingDir %q must be an absolute path", ErrInvalidWorkDir, request.WorkingDir)
	}
	for key := range request.Environment {
		if err := validateEnvironmentKeyShape(key); err != nil {
			return err
		}
		if IsCredentialEnvironmentKey(key) {
			return fmt.Errorf("%w: environment key %q carries credential semantics", ErrCredentialKeyRejected, key)
		}
	}
	if !request.TranscriptPolicy.Absent() {
		if err := request.TranscriptPolicy.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// credentialEnvironmentSubstrings is the closed case-insensitive credential
// semantics set of the environment channel (ADR 0055 §2.4, ADR 0018): any
// environment key containing one of these substrings is treated as a
// credential channel and is rejected both at allowlist registration and at
// exec time. The freeze is verbatim case-insensitive substring containment,
// so e.g. the key "MONKEY" matches through the substring "key".
var credentialEnvironmentSubstrings = []string{"key", "token", "secret", "password"}

// IsCredentialEnvironmentKey reports whether key carries credential
// semantics under the frozen case-insensitive substring rule.
func IsCredentialEnvironmentKey(key string) bool {
	lowered := strings.ToLower(key)
	for _, substring := range credentialEnvironmentSubstrings {
		if strings.Contains(lowered, substring) {
			return true
		}
	}
	return false
}

// validateEnvironmentKeyShape rejects empty, blank, whitespace-padded or
// '='-carrying environment keys with ErrInvalidRequest.
func validateEnvironmentKeyShape(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: environment keys must be non-empty strings", ErrInvalidRequest)
	}
	if key != strings.TrimSpace(key) {
		return fmt.Errorf("%w: environment key %q must not carry surrounding whitespace", ErrInvalidRequest, key)
	}
	if strings.ContainsRune(key, '=') {
		return fmt.Errorf("%w: environment key %q must not contain '='", ErrInvalidRequest, key)
	}
	return nil
}

// ValidateEnvironmentAllowlist validates the Provision-time closed
// declaration of environment keys (ADR 0055 §2.2): every key must be
// well-formed, unique and free of credential semantics.
func ValidateEnvironmentAllowlist(allowlist []string) error {
	seen := make(map[string]struct{}, len(allowlist))
	for index, key := range allowlist {
		if err := validateEnvironmentKeyShape(key); err != nil {
			return fmt.Errorf("environmentAllowlist[%d]: %w", index, err)
		}
		if IsCredentialEnvironmentKey(key) {
			return fmt.Errorf("%w: environmentAllowlist[%d] %q carries credential semantics", ErrCredentialKeyRejected, index, key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: environmentAllowlist[%d] %q is declared twice", ErrInvalidRequest, index, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ResolveExecEnvironment validates the per-op environment of one Exec
// against the allocation's closed Provision-time allowlist and returns the
// allow-listed key=value pairs in deterministic sorted order, to overlay
// onto the provider's sanitized baseline. An empty environment resolves to
// no overlay; any key outside the closed allowlist or carrying credential
// semantics fails closed and is never unioned into the execution
// environment.
func ResolveExecEnvironment(environment map[string]string, allowlist []string) ([]string, error) {
	if len(environment) == 0 {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, key := range allowlist {
		allowed[key] = struct{}{}
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		if err := validateEnvironmentKeyShape(key); err != nil {
			return nil, err
		}
		if IsCredentialEnvironmentKey(key) {
			return nil, fmt.Errorf("%w: environment key %q carries credential semantics", ErrCredentialKeyRejected, key)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: environment key %q is not declared in the allocation's environmentAllowlist", ErrEnvKeyNotAllowed, key)
		}
		pairs = append(pairs, key+"="+environment[key])
	}
	return pairs, nil
}

// ValidateWorkDirAllowlist validates the Provision-time closed declaration
// of executable working roots (ADR 0055 §1): every entry must be an
// absolute path. Provider-side existence and the symlink-resolved binding
// are adjudicated by the provider at Exec time.
func ValidateWorkDirAllowlist(allowlist []string) error {
	for index, dir := range allowlist {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("%w: workDirAllowlist[%d] must be a non-empty absolute path", ErrInvalidWorkDir, index)
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("%w: workDirAllowlist[%d] %q must be an absolute path", ErrInvalidWorkDir, index, dir)
		}
	}
	return nil
}

// EffectiveTimeoutSeconds freezes the per-op timeout arithmetic of ADR 0055
// §4: a positive request is honored as min(requested, the provider cap) and
// any non-positive value keeps the provider cap. The computation is
// integer-only and never overflows.
func EffectiveTimeoutSeconds(requestedSeconds, providerCapSeconds int64) int64 {
	if requestedSeconds <= 0 || requestedSeconds > providerCapSeconds {
		return providerCapSeconds
	}
	return requestedSeconds
}

// ExecReceipt observes one executed command. It is a lifecycle guard only:
// completed/failed/killed statuses gate subsequent operations, but no
// conformance or fencing verdict is ever derived from this receipt. The
// transcript digests are populated only under ADR 0055 §3: an op carrying a
// TranscriptPolicy that completed its capture cleanly.
type ExecReceipt struct {
	Status       ExecutionStatus
	ExitCode     int
	StdoutSHA256 string
	StderrSHA256 string
	// TranscriptDigest echoes the provider-recomputed sha256 digest of the
	// stdout transcript artifact staged under
	// ExecRequest.TranscriptPolicy.ArtifactId (ADR 0055 §3.3); it is empty
	// when no TranscriptPolicy was carried or the capture never completed
	// (overflow kill, timeout, start failure).
	TranscriptDigest string
	// TranscriptStderrDigest echoes the provider-recomputed digest of the
	// stderr captured under the identical policy bound (ADR 0055 §3.5); no
	// stderr artifact is ever staged.
	TranscriptStderrDigest string
}

// InspectRequest is the input of Inspect.
type InspectRequest struct {
	Identity     OperationIdentity
	AllocationId string
}

// BoundaryViolation is one observed containment failure.
type BoundaryViolation struct {
	Kind   string
	Detail string
}

// InspectReport is the out-of-band observation of one allocation.
type InspectReport struct {
	State      AllocationState
	ExitCode   int
	Violations []BoundaryViolation
	SpawnCount int64
	LogLines   []string
}

// SignalRequest is the input of Signal.
type SignalRequest struct {
	Identity     OperationIdentity
	AllocationId string
	Signal       SignalName
}

// SignalReceipt observes the signal delivery.
type SignalReceipt struct {
	Delivered bool
}

// CheckpointRequest is the input of Checkpoint.
type CheckpointRequest struct {
	Identity     OperationIdentity
	AllocationId string
}

// CheckpointReceipt observes one checkpoint snapshot.
type CheckpointReceipt struct {
	CheckpointId string
	SHA256       string
	SizeBytes    int64
}

// RestoreOperationRequest is the input of Restore.
type RestoreOperationRequest struct {
	Identity             OperationIdentity
	PreviousAllocationId string
	NextAllocationId     string
	InPlaceConfirmed     bool
}

// RestoreReceipt observes the post-restore allocation.
type RestoreReceipt struct {
	Allocation SandboxAllocation
}

// TerminateRequest is the input of Terminate.
type TerminateRequest struct {
	Identity     OperationIdentity
	AllocationId string
}

// TerminateReceipt observes the terminal state.
type TerminateReceipt struct {
	State AllocationState
}

// ReconcileRequest is the input of Reconcile.
type ReconcileRequest struct {
	Identity  OperationIdentity
	RunId     string
	AttemptId string
}

// ReconcileReport observes the provider's allocation bookkeeping for one
// (runId, attemptId) scope.
type ReconcileReport struct {
	ActiveAllocationIds []string
	OrphanAllocationIds []string
	DriftDetected       bool
}
