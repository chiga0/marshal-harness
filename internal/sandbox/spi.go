package sandbox

import (
	"context"
	"errors"
	"fmt"

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
	// identity, allocation locator and the command. Output: a receipt whose
	// status is a lifecycle guard only; conformance adjudication never
	// reads the verdict from it. Fail closed: invalid identity, unknown or
	// inactive allocation, or a stale generation, return an error and the
	// command never executes.
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

// ProvisionRequest is the input of Provision.
type ProvisionRequest struct {
	Identity        OperationIdentity
	Requirements    domain.SandboxRequirements
	AllowedStoreIds []string
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

// ExecRequest is the input of Exec.
type ExecRequest struct {
	Identity     OperationIdentity
	AllocationId string
	Command      []string
	Stdin        []byte
}

// ExecReceipt observes one executed command. It is a lifecycle guard only:
// completed/failed/killed statuses gate subsequent operations, but no
// conformance or fencing verdict is ever derived from this receipt.
type ExecReceipt struct {
	Status       ExecutionStatus
	ExitCode     int
	StdoutSHA256 string
	StderrSHA256 string
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
