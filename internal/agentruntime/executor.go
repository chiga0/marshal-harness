package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// ErrHostBypassDenied is returned when a production ExecutionProfile attempts
// the host-bypass execution path. The error prefix follows the sandbox package
// convention.
var ErrHostBypassDenied = errors.New("agentruntime: host bypass denied for production profile")

// ExecutionProfile selects the execution path and binds the profile digest.
type ExecutionProfile struct {
	// Production marks the real sandbox path. When true, the host-bypass
	// entry fails closed.
	Production bool
	// Digest is the sha256 digest of the profile. It must equal the
	// AgentLaunchSpec.ProfileDigest field.
	Digest string
}

// Validate fails closed on a malformed or unbound profile digest.
func (p ExecutionProfile) Validate() error {
	return requireDigest("ExecutionProfile.Digest", p.Digest)
}

// ExecutionOutcome is the untrusted result of one Executor attempt, augmented
// with execution-location evidence from the provider receipts.
type ExecutionOutcome struct {
	WorkloadResult WorkloadResult
	AllocationId   string
	Generation     int64
	ExecStatus     sandbox.ExecutionStatus
}

// HostBypassRun is the function-type entry point for host-bypass execution.
// It is only permitted for nonproduction profiles; production profiles must
// fail closed with ErrHostBypassDenied.
type HostBypassRun func(ctx context.Context, spec AgentLaunchSpec) (ExecutionOutcome, error)

// executorConfig carries optional Executor configuration.
type executorConfig struct {
	hostBypass HostBypassRun
}

// ExecutorOption configures an Executor at construction time.
type ExecutorOption func(*executorConfig)

// WithHostBypass installs a nonproduction host-bypass runner. A production
// profile still fails closed even when a runner is installed.
func WithHostBypass(run HostBypassRun) ExecutorOption {
	return func(cfg *executorConfig) {
		cfg.hostBypass = run
	}
}

// Executor is the thinnest WorkerExecutor slice: it orchestrates a single
// sandbox attempt through Provision → Stage → Exec → Inspect → Terminate and
// normalizes the exec event stream through AgentRuntime.
type Executor struct {
	provider   sandbox.SandboxProvider
	runtime    AgentRuntime
	hostBypass HostBypassRun
}

// NewExecutor constructs an Executor. Both dependencies are required and every
// malformed input fails closed.
func NewExecutor(provider sandbox.SandboxProvider, runtime AgentRuntime, opts ...ExecutorOption) (*Executor, error) {
	if provider == nil {
		return nil, errors.New("agentruntime: NewExecutor requires a non-nil SandboxProvider")
	}
	if runtime == nil {
		return nil, errors.New("agentruntime: NewExecutor requires a non-nil AgentRuntime")
	}
	cfg := &executorConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &Executor{
		provider:   provider,
		runtime:    runtime,
		hostBypass: cfg.hostBypass,
	}, nil
}

// Execute drives the full sandbox lifecycle for spec under profile.
// Execution-location evidence (AllocationId, Generation) is taken from the
// Provision receipt and returned so it can be mechanically reconciled with
// provider observations.
func (e *Executor) Execute(ctx context.Context, spec AgentLaunchSpec, profile ExecutionProfile) (ExecutionOutcome, error) {
	if err := spec.Validate(); err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: %w", err)
	}
	if spec.ProfileDigest != profile.Digest {
		return ExecutionOutcome{}, errors.New("agentruntime: Execute: ExecutionProfile.Digest does not match AgentLaunchSpec.ProfileDigest")
	}

	requirements, err := e.requirements(profile)
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: %w", err)
	}

	specDigest, err := spec.Digest()
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: %w", err)
	}

	allocationID := canonical.DigestBytes([]byte("agentruntime:allocation:" + specDigest))
	generation := int64(1)

	provisionIdentity, err := e.identity(spec, allocationID, generation, "command-provision")
	if err != nil {
		return ExecutionOutcome{}, err
	}
	provisionReceipt, err := e.provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        provisionIdentity,
		Requirements:    requirements,
		AllowedStoreIds: []string{},
	})
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: provision failed: %w", err)
	}

	// Observed execution-location evidence must be mechanically reconcilable
	// with the provider receipt.
	allocationID = provisionReceipt.Allocation.AllocationId
	generation = provisionReceipt.Allocation.Generation

	// Resource cleanup: if we successfully provisioned, we must terminate.
	defer func() {
		termIdentity, idErr := e.identity(spec, allocationID, generation, "command-terminate")
		if idErr != nil {
			return
		}
		_, _ = e.provider.Terminate(ctx, sandbox.TerminateRequest{
			Identity:     termIdentity,
			AllocationId: allocationID,
		})
	}()

	stageIdentity, err := e.identity(spec, allocationID, generation, "command-stage")
	if err != nil {
		return ExecutionOutcome{}, err
	}
	stageMarker := []byte("agentruntime:executor:stage-marker")
	if _, err := e.provider.Stage(ctx, sandbox.StageRequest{
		Identity:     stageIdentity,
		AllocationId: allocationID,
		Inputs: []sandbox.StageInput{{
			InputId:        "agentruntime-executor-marker",
			DeclaredSHA256: canonical.DigestBytes(stageMarker),
			Inline:         stageMarker,
		}},
	}); err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: stage failed: %w", err)
	}

	execIdentity, err := e.identity(spec, allocationID, generation, "command-exec")
	if err != nil {
		return ExecutionOutcome{}, err
	}
	command := append([]string{spec.Executable}, spec.Arguments...)
	execReceipt, err := e.provider.Exec(ctx, sandbox.ExecRequest{
		Identity:     execIdentity,
		AllocationId: allocationID,
		Command:      command,
	})
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: exec failed: %w", err)
	}

	// Exec statuses other than completed are lifecycle guards; the attempt
	// fails closed but resource cleanup still runs.
	if execReceipt.Status != sandbox.ExecutionCompleted {
		return ExecutionOutcome{
			AllocationId: allocationID,
			Generation:   generation,
			ExecStatus:   execReceipt.Status,
		}, fmt.Errorf("agentruntime: Execute: exec status %q is not completed", string(execReceipt.Status))
	}

	inspectIdentity, err := e.identity(spec, allocationID, generation, "command-inspect")
	if err != nil {
		return ExecutionOutcome{}, err
	}
	inspectReceipt, err := e.provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     inspectIdentity,
		AllocationId: allocationID,
	})
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: inspect failed: %w", err)
	}

	rawEvent, err := json.Marshal(execObservation{
		Status:       string(execReceipt.Status),
		ExitCode:     execReceipt.ExitCode,
		AllocationId: allocationID,
		Generation:   generation,
	})
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: event serialization failed: %w", err)
	}
	event, err := e.runtime.DecodeEvent(rawEvent)
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: decode event failed: %w", err)
	}

	result, err := e.runtime.FinalizeResult([]AgentEvent{event}, ExecEvidence{
		ExitCode: inspectReceipt.ExitCode,
		Stderr:   "",
	})
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: Execute: finalize result failed: %w", err)
	}

	return ExecutionOutcome{
		WorkloadResult: result,
		AllocationId:   allocationID,
		Generation:     generation,
		ExecStatus:     execReceipt.Status,
	}, nil
}

// RunHostBypass is the host-bypass entry point. Production profiles fail
// closed; nonproduction profiles delegate to the configured HostBypassRun.
func (e *Executor) RunHostBypass(ctx context.Context, spec AgentLaunchSpec, profile ExecutionProfile) (ExecutionOutcome, error) {
	if err := spec.Validate(); err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: RunHostBypass: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return ExecutionOutcome{}, fmt.Errorf("agentruntime: RunHostBypass: %w", err)
	}
	if spec.ProfileDigest != profile.Digest {
		return ExecutionOutcome{}, errors.New("agentruntime: RunHostBypass: ExecutionProfile.Digest does not match AgentLaunchSpec.ProfileDigest")
	}
	if profile.Production {
		return ExecutionOutcome{}, ErrHostBypassDenied
	}
	if e.hostBypass == nil {
		return ExecutionOutcome{}, errors.New("agentruntime: RunHostBypass: no host bypass runner configured")
	}
	return e.hostBypass(ctx, spec)
}

// requirements derives the closed sandbox requirements. For this thinnest
// slice every profile maps to workspace-write; the Production flag only gates
// the host-bypass path.
func (e *Executor) requirements(profile ExecutionProfile) (domain.SandboxRequirements, error) {
	return domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
}

// identity builds a dispatch-bound OperationIdentity according to sandbox
// semantics from the frozen spec and the current allocation locator.
func (e *Executor) identity(spec AgentLaunchSpec, allocationID string, generation int64, commandID string) (sandbox.OperationIdentity, error) {
	token := canonical.DigestBytes([]byte(spec.AdapterID + ":" + allocationID + ":" + strconv.FormatInt(generation, 10) + ":" + commandID))
	return sandbox.OperationIdentity{
		TaskId:       spec.AdapterID,
		RunId:        spec.RunID,
		AttemptId:    spec.AttemptID,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: allocationID,
		Generation:   generation,
		FencingToken: token,
		CommandId:    commandID,
	}, nil
}

// execObservation is the synthetic raw event payload derived from exec and
// inspect receipts for normalization by AgentRuntime.
type execObservation struct {
	Status       string `json:"status"`
	ExitCode     int    `json:"exitCode"`
	AllocationId string `json:"allocationId"`
	Generation   int64  `json:"generation"`
}
