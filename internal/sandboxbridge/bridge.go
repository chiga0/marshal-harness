package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// ErrMalformedRequest 拒绝缺身份字段的 worker request（fail closed）。
var ErrMalformedRequest = errors.New("sandboxbridge: malformed worker request")

// workerRequestView 是桥从冻结 KindWorkerRequest 中提取的身份与绑定字段。
// 任何缺失字段 fail closed——执行链身份不允许从上下文猜测。
type workerRequestView struct {
	TaskID           string `json:"taskId"`
	RunID            string `json:"runId"`
	AttemptID        string `json:"attemptId"`
	SpecDigest       string `json:"specDigest"`
	PolicyDigest     string `json:"policyDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
	WorktreePath     string `json:"worktreePath"`
	ExecutionProfile string `json:"executionProfile"`
	SessionPolicy    string `json:"sessionPolicy"`
	AdapterID        string `json:"adapterId"`
}

// Outcome 是一次桥执行的执行链观察：allocation 身份、generation 与
// Inspect 末态退出码。观察是 provider-asserted，不构成 authority。
type Outcome struct {
	AllocationID string
	Generation   int64
	LeaseFencing string
	LastExitCode int
}

// Bridge 把 legacy WorkerAdapter 包在绑定 allocation/lease 身份的执行链中。
// 每次 RunWorker 新建并终结一个 allocation；并发安全（状态只在方法栈内）。
type Bridge struct {
	provider sandbox.SandboxProvider
}

// NewBridge 构造 Bridge；nil provider fail closed。
func NewBridge(provider sandbox.SandboxProvider) (*Bridge, error) {
	if provider == nil {
		return nil, errors.New("sandboxbridge: NewBridge requires a non-nil SandboxProvider")
	}
	return &Bridge{provider: provider}, nil
}

// RunWorker 实现 execution.Input.WorkerRunner。成功时原样返回 adapter 的
// WorkerResult 记录；失败时返回错误（execution 的既有失败归一化与
// fail-closed 持久化链继续适用），且 allocation 一定被 Terminate。
func (b *Bridge) RunWorker(ctx context.Context, adapter port.WorkerAdapter, request domain.Record) (domain.Record, error) {
	if adapter == nil {
		return domain.Record{}, errors.New("sandboxbridge: adapter must not be nil")
	}
	view, err := parseRequest(request)
	if err != nil {
		return domain.Record{}, err
	}
	if view.AdapterID != "" && view.AdapterID != adapter.ID() {
		return domain.Record{}, fmt.Errorf("sandboxbridge: request adapterId %q does not match injected adapter %q", view.AdapterID, adapter.ID())
	}

	requirements, err := domain.SandboxRequirementsFromLegacy(view.ExecutionProfile)
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: %w", err)
	}

	profileDigest, err := deriveProfileDigest(view)
	if err != nil {
		return domain.Record{}, err
	}

	spec, err := agentruntime.NewAgentLaunchSpec(
		view.AdapterID, "capability-bound",
		view.RunID, view.AttemptID,
		view.AdapterID, view.CapabilityDigest,
		view.WorktreePath,
		nil, nil,
		profileDigest, "",
	)
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: %w", err)
	}
	specDigest, err := spec.Digest()
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: %w", err)
	}
	requestedAllocation := canonical.DigestBytes([]byte("sandboxbridge:allocation:" + specDigest))

	provisionIdentity, err := identity(view, requestedAllocation, 1, "command-provision")
	if err != nil {
		return domain.Record{}, err
	}
	provisionReceipt, err := b.provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        provisionIdentity,
		Requirements:    requirements,
		AllowedStoreIds: []string{},
	})
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: provision failed: %w", err)
	}
	allocationID := provisionReceipt.Allocation.AllocationId
	generation := provisionReceipt.Allocation.Generation

	// 资源生命周期：provision 成功即保证 Terminate（幂等）。
	defer func() {
		termIdentity, idErr := identity(view, allocationID, generation, "command-terminate")
		if idErr != nil {
			return
		}
		_, _ = b.provider.Terminate(ctx, sandbox.TerminateRequest{Identity: termIdentity, AllocationId: allocationID})
	}()

	// 冻结工单原样 content-address 入账：provider 消费前后重算 digest。
	stageIdentity, err := identity(view, allocationID, generation, "command-stage")
	if err != nil {
		return domain.Record{}, err
	}
	if _, err := b.provider.Stage(ctx, sandbox.StageRequest{
		Identity:     stageIdentity,
		AllocationId: allocationID,
		Inputs: []sandbox.StageInput{{
			InputId:        "worker-request",
			DeclaredSHA256: canonical.DigestBytes(request.Data),
			Inline:         append([]byte(nil), request.Data...),
		}},
	}); err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: stage failed: %w", err)
	}

	// 真实 agent 协议栈保持不变地执行（Local profile 执行等价）。
	record, runErr := adapter.Run(ctx, request)

	// Inspect 观察是 provider-asserted：仅作为诊断/审计材料，不构成
	// authority（执行位置 attestation 的 claim 侧，LocationFact 归高保证链）。
	inspectIdentity, idErr := identity(view, allocationID, generation, "command-inspect")
	if idErr == nil {
		_, _ = b.provider.Inspect(ctx, sandbox.InspectRequest{Identity: inspectIdentity, AllocationId: allocationID})
	}

	if runErr != nil {
		return domain.Record{}, runErr
	}
	return record, nil
}

// parseRequest 提取并校验身份字段（fail closed，不猜测）。
func parseRequest(request domain.Record) (workerRequestView, error) {
	if request.Kind != domain.KindWorkerRequest {
		return workerRequestView{}, fmt.Errorf("%w: kind %q is not %q", ErrMalformedRequest, string(request.Kind), string(domain.KindWorkerRequest))
	}
	var view workerRequestView
	if err := json.Unmarshal(request.Data, &view); err != nil {
		return workerRequestView{}, fmt.Errorf("%w: %v", ErrMalformedRequest, err)
	}
	for field, value := range map[string]string{
		"taskId":           view.TaskID,
		"runId":            view.RunID,
		"attemptId":        view.AttemptID,
		"capabilityDigest": view.CapabilityDigest,
		"worktreePath":     view.WorktreePath,
		"executionProfile": view.ExecutionProfile,
		"adapterId":        view.AdapterID,
	} {
		if strings.TrimSpace(value) == "" {
			return workerRequestView{}, fmt.Errorf("%w: %s must not be empty", ErrMalformedRequest, field)
		}
	}
	return view, nil
}

// deriveProfileDigest 派生绑定本执行链的 profile digest：路由、身份与全部
// 冻结输入参与派生，任何输入变化产生不同 digest（profile 不可暗中替换）。
func deriveProfileDigest(view workerRequestView) (string, error) {
	raw, err := json.Marshal(map[string]string{
		"route":            "i186-sandbox-exec",
		"taskId":           view.TaskID,
		"runId":            view.RunID,
		"attemptId":        view.AttemptID,
		"specDigest":       view.SpecDigest,
		"policyDigest":     view.PolicyDigest,
		"capabilityDigest": view.CapabilityDigest,
		"executionProfile": view.ExecutionProfile,
		"sessionPolicy":    view.SessionPolicy,
	})
	if err != nil {
		return "", fmt.Errorf("sandboxbridge: profile digest derivation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// identity 按 sandbox SPI 语义构造 dispatch-bound OperationIdentity。
func identity(view workerRequestView, allocationID string, generation int64, commandID string) (sandbox.OperationIdentity, error) {
	if allocationID == "" {
		return sandbox.OperationIdentity{}, errors.New("sandboxbridge: allocation id must not be empty")
	}
	token := canonical.DigestBytes([]byte(view.AdapterID + ":" + allocationID + ":" + strconv.FormatInt(generation, 10) + ":" + commandID))
	return sandbox.OperationIdentity{
		TaskId:       view.TaskID,
		RunId:        view.RunID,
		AttemptId:    view.AttemptID,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: allocationID,
		Generation:   generation,
		FencingToken: token,
		CommandId:    commandID,
	}, nil
}
