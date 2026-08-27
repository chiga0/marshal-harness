package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/provider"
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
// 每次 RunWorker 新建并终结一个 allocation；并发安全。registry 会进程内
// 收集已 record 的 attempt 目录，供 SweepRegistered 对账孤儿。
// transcriptSource 非空时，实现 LaunchCapable 的 Adapter 走 ADR 0052 §1.2
// allocation-carried 执行路径。
// authority 非空时，admission 从真实 durable ledger 读取 registration/
// snapshot/lease/expiry，而非以结果携带 Facts 临时构造（R2/R3 纠偏）。
type Bridge struct {
	provider         sandbox.SandboxProvider
	registry         *allocRegistry
	now              func() time.Time
	transcriptSource TranscriptSource
	authority        DurableAuthority
}

// DurableAuthority 是 Bridge 在 admission 时读取真实 durable authority 的
// 接缝：sandbox provider registration/snapshot 来自 RegistrationStore（文件
// 型耐久 ledger），agent adapter registration 来自 AgentRegistry（进程内
// 确定性 ledger，restart 后从 adapter probe 重建），lease expiry 来自
// dispatch 时冻结的 DispatchLease。
// 未注入时（测试兼容）退化为 seedRegistry/seedSandboxLedger 路径。
type DurableAuthority interface {
	// RegistrationStore 返回 durable 文件 ledger（append-only registrations.jsonl）。
	RegistrationStore() *provider.RegistrationStore
	// LeaseFor 返回 dispatch 时冻结的 DispatchLease（含 ExpiresAt）。
	LeaseFor(runID, attemptID string) (dispatch.DispatchLease, bool)
	// CapabilitySnapshot 返回当前 provider 的冻结 capability snapshot digest。
	CapabilitySnapshot() provider.ProviderCapabilitySnapshot
	// Registration 返回当前 provider 的 durable registration。
	Registration() provider.ProviderRegistration
	// AgentRegistrationActive 验证 agent adapter registration 当前仍为
	// active（R2/R3 纠偏：agent 侧 current-ledger recheck）。
	AgentRegistrationActive(registrationID string) (bool, error)
}

// NewBridge 构造 Bridge；nil provider fail closed。
func NewBridge(provider sandbox.SandboxProvider) (*Bridge, error) {
	if provider == nil {
		return nil, errors.New("sandboxbridge: NewBridge requires a non-nil SandboxProvider")
	}
	return &Bridge{provider: provider, registry: &allocRegistry{}, now: time.Now}, nil
}

// WithTranscriptSource 注入 staged transcript artifact 的回读实现（v1.0
// Local 形态基于 AllocationDirectory；测试注入等价闭包）。
func (b *Bridge) WithTranscriptSource(source TranscriptSource) *Bridge {
	b.transcriptSource = source
	return b
}

// WithDurableAuthority 注入真实 durable authority 接缝（R2/R3 纠偏）：
// admission 从 RegistrationStore + DispatchLease 读取真实 registration/
// snapshot/lease/expiry，而非以结果携带 Facts 临时构造。未注入时退化为
// seed 路径（仅测试兼容，生产路径必须注入）。
func (b *Bridge) WithDurableAuthority(authority DurableAuthority) *Bridge {
	b.authority = authority
	return b
}

// RunWorker 实现 execution.Input.WorkerRunner。成功时原样返回 adapter 的
// WorkerResult 记录；失败时返回错误（execution 的既有失败归一化与
// fail-closed 持久化链继续适用），且 allocation 一定被 Terminate。
//
// 路径选择：adapter 实现 LaunchCapable 且桥配置了 TranscriptSource →
// allocation-carried 执行链（ADR 0052 §1.2）。
//
// v1.0 production 门禁：adapter 必须实现 LaunchCapable 才能进入
// allocation-carried exec-chain。未实现 LaunchCapable 的 adapter
// 不经 admission anchor / ResultIngress 接纳，不满足 v1.0 生产门禁，
// 必须 fail closed。这是 composition root 纠偏的结论：production profile
// 不允许静默退回宿主 legacy Run 路径。
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
	capable, isLaunchCapable := adapter.(LaunchCapable)
	if !isLaunchCapable {
		return domain.Record{}, fmt.Errorf("sandboxbridge: adapter %q does not implement LaunchCapable (v1.0 production gate: non-LaunchCapable adapters are rejected)", adapter.ID())
	}
	if b.transcriptSource == nil {
		return domain.Record{}, errors.New("sandboxbridge: exec-chain requires a transcript source")
	}
	return b.runWorkerExecChain(ctx, capable, request, view)
}

// runWorkerLegacy 是 R5 兼容形态的执行：allocation 身份绑定 + adapter.Run。
func (b *Bridge) runWorkerLegacy(ctx context.Context, adapter port.WorkerAdapter, request domain.Record, view workerRequestView) (domain.Record, error) {
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

	// fencingToken 冻结规则：每个 (attempt, allocation, generation) 派生
	// 唯一 token，Provision 与后续 Stage/Inspect/Terminate 出示同一值——
	// Local runner 在 Provision 时把首个 token 封进 sealed lease，后续
	// 操作的 fencing guard 要求精确匹配（按 command 派生不同 token 会被
	// 正确拒绝，这正是 fencing guard 的职责）。
	const attemptGeneration = int64(1)
	fencingToken := canonical.DigestBytes([]byte(view.AdapterID + ":" + requestedAllocation + ":" + strconv.FormatInt(attemptGeneration, 10)))

	provisionIdentity, err := identity(view, requestedAllocation, attemptGeneration, fencingToken, "command-provision")
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

	// 执行前把 allocation 身份落盘（R6 孤儿对账锚点）：driver 崩溃后
	// reconciler 据此终结残留 allocation。写失败仅降级为现状（无锚点），
	// 不打断执行。
	if controlRoot := controlRootOf(request.Data); controlRoot != "" {
		rec := AllocationRecord{
			Schema:              allocationRecordSchema,
			TaskID:              view.TaskID,
			RunID:               view.RunID,
			AttemptID:           view.AttemptID,
			AllocationID:        allocationID,
			Generation:          generation,
			FencingToken:        fencingToken,
			RequirementsProfile: view.ExecutionProfile,
			RecordedAt:          b.now().UTC().Format(time.RFC3339),
			OwnerState:          "running",
		}
		if recErr := recordAllocation(controlRoot, rec); recErr == nil {
			b.registry.add(filepath.Dir(controlRoot))
		}
	}

	// 资源生命周期：provision 成功即保证 Terminate（幂等）。
	defer func() {
		termIdentity, idErr := identity(view, allocationID, generation, fencingToken, "command-terminate")
		if idErr != nil {
			return
		}
		_, _ = b.provider.Terminate(ctx, sandbox.TerminateRequest{Identity: termIdentity, AllocationId: allocationID})
	}()

	// 冻结工单原样 content-address 入账：provider 消费前后重算 digest。
	stageIdentity, err := identity(view, allocationID, generation, fencingToken, "command-stage")
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
	inspectIdentity, idErr := identity(view, allocationID, generation, fencingToken, "command-inspect")
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
// fencingToken 由调用方在 attempt 生命周期内固定出示（见 RunWorker 的
// 冻结规则）。
func identity(view workerRequestView, allocationID string, generation int64, fencingToken, commandID string) (sandbox.OperationIdentity, error) {
	if allocationID == "" {
		return sandbox.OperationIdentity{}, errors.New("sandboxbridge: allocation id must not be empty")
	}
	if fencingToken == "" {
		return sandbox.OperationIdentity{}, errors.New("sandboxbridge: fencing token must not be empty")
	}
	return sandbox.OperationIdentity{
		TaskId:       view.TaskID,
		RunId:        view.RunID,
		AttemptId:    view.AttemptID,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: allocationID,
		Generation:   generation,
		FencingToken: fencingToken,
		CommandId:    commandID,
	}, nil
}
