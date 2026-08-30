package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/processcontrol"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
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
	// AgentRegistrationID 是 execution.Run 从稳定 capability identity 派生并
	// 冻结的精确 agent registration id；缺失时 launch 前 fail closed。
	AgentRegistrationID string `json:"agentRegistrationId"`
	// AgentCapabilitySnapshotDigest 是排除 probedAt、包含其余完整能力与
	// authority 元数据的稳定 snapshot identity。
	AgentCapabilitySnapshotDigest string `json:"agentCapabilitySnapshotDigest"`
	WorktreePath                  string `json:"worktreePath"`
	ExecutionProfile              string `json:"executionProfile"`
	SessionPolicy                 string `json:"sessionPolicy"`
	AdapterID                     string `json:"adapterId"`
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
	exactMu          sync.Mutex
	provider         sandbox.SandboxProvider
	registry         *allocRegistry
	now              func() time.Time
	transcriptSource TranscriptSource
	authority        DurableAuthority
	productionGate   bool
	exactProcess     *ExactProcessRuntime
	exactAllocation  *ExactAllocationRuntime
	exactBound       bool
}

// ExactProcessRuntime is the only interpreted-agent execution route admitted
// by the production bridge. Resolve must return a fresh attempt-bound
// coordinator/authority pair; a global identity may never be reused across
// RunWorker calls.
type ExactProcessRuntime struct {
	Resolve func(context.Context, ExactProcessAttempt) (*processcontrol.Coordinator, DurableProcessAuthority, error)
	// Retain transfers an uncertain/live handle to the Attempt supervisor.
	// It must persist intervention state; returning from Retain transfers
	// ownership and sandboxbridge will never signal or close that handle.
	Retain func(ExactProcessAttempt, *processcontrol.Process, error)
}

type ExactProcessAttempt struct {
	TaskID             string
	RunID              string
	AttemptID          string
	AllocationID       string
	Generation         int64
	FencingTokenDigest string
}

type exactProcessAdmission struct {
	attempt     ExactProcessAttempt
	coordinator *processcontrol.Coordinator
	authority   DurableProcessAuthority
	plan        LaunchPlan
	allocation  ProductionAllocation
}

// ProductionAllocation is an Attempt-bound, non-path staging/readback
// authority. Implementations must re-open current Stage2 authority on every
// method; Current is an observation, never a bearer capability.
type ProductionAllocation interface {
	Current(context.Context) (allocationcontrol.CurrentLiveAllocationV1, error)
	Stage(context.Context, []sandbox.StageInput) (*sandbox.StageReport, error)
	ReadArtifact(context.Context, string, int64) ([]byte, error)
}

// ExactAllocationResolution binds one concrete facade, its held
// ResultIngress AllocationAuthority, and the exact provision effect. The
// Bridge independently reloads that effect from DurableProcessAuthority.Store;
// none of these fields is accepted as a bearer assertion.
type ExactAllocationResolution struct {
	Facade    *allocationcontrol.DurableLocalFacade
	Authority *resultingress.AllocationAuthority
	EffectID  string
}

// ExactAllocationRuntime resolves the already-created Stage2 allocation for
// one exact Attempt. It cannot Provision or Terminate and is deliberately not
// wired by the current CLI/server composition.
type ExactAllocationRuntime struct {
	Resolve func(context.Context, ExactProcessAttempt) (ExactAllocationResolution, error)
}

// ExactRuntimeBinder is a package-owned capability for the production
// composition root. The unexported marker prevents an unrelated ProcessBridge
// from claiming that exact runtimes were installed. Binding is deliberately
// atomic: callers cannot observe a process-only or allocation-only production
// configuration.
type ExactRuntimeBinder interface {
	exactRuntimeBinder()
	BindExactRuntimes(ExactProcessRuntime, ExactAllocationRuntime) error
}

func (*Bridge) exactRuntimeBinder() {}

// BindExactRuntimes installs both exact runtime objects once. It validates all
// inputs and the existing state before changing either field, so every error
// path is side-effect free.
func (b *Bridge) BindExactRuntimes(processRuntime ExactProcessRuntime, allocationRuntime ExactAllocationRuntime) error {
	if b == nil || processRuntime.Resolve == nil || processRuntime.Retain == nil || allocationRuntime.Resolve == nil {
		return fmt.Errorf("sandboxbridge: exact runtime pair is incomplete: %w", launchidentity.ErrUnavailable)
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	if b.exactProcess != nil || b.exactAllocation != nil {
		return fmt.Errorf("sandboxbridge: exact runtime pair already bound: %w", launchidentity.ErrUnavailable)
	}
	b.exactProcess = &processRuntime
	b.exactAllocation = &allocationRuntime
	b.exactBound = true
	return nil
}

func (b *Bridge) WithExactProcessRuntime(runtime ExactProcessRuntime) *Bridge {
	if b == nil {
		return nil
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	if runtime.Resolve != nil && runtime.Retain != nil {
		b.exactProcess = &runtime
	}
	return b
}

// WithExactAllocationRuntime installs the future production-only Stage2
// staging facade. Merely configuring this seam does not enable production;
// productionGate still requires all exact authorities and the current CLI
// intentionally does not compose it.
func (b *Bridge) WithExactAllocationRuntime(runtime ExactAllocationRuntime) *Bridge {
	if b == nil {
		return nil
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	if runtime.Resolve != nil {
		b.exactAllocation = &runtime
	}
	return b
}

// BindExactProcessRuntime installs the exact process runtime once. It is the
// composition-root seam used by productionruntime; unlike the historical
// WithExactProcessRuntime test/configuration helper, it never overwrites an
// existing binding and rejects incomplete values.
func (b *Bridge) BindExactProcessRuntime(runtime ExactProcessRuntime) error {
	if b == nil || runtime.Resolve == nil || runtime.Retain == nil {
		return fmt.Errorf("sandboxbridge: exact process runtime is incomplete: %w", launchidentity.ErrUnavailable)
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	if b.exactProcess != nil {
		return fmt.Errorf("sandboxbridge: exact process runtime already bound: %w", launchidentity.ErrUnavailable)
	}
	b.exactProcess = &runtime
	return nil
}

// BindExactAllocationRuntime installs the exact allocation runtime once. It
// rejects incomplete values and never overwrites a prior binding.
func (b *Bridge) BindExactAllocationRuntime(runtime ExactAllocationRuntime) error {
	if b == nil || runtime.Resolve == nil {
		return fmt.Errorf("sandboxbridge: exact allocation runtime is incomplete: %w", launchidentity.ErrUnavailable)
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	if b.exactAllocation != nil {
		return fmt.Errorf("sandboxbridge: exact allocation runtime already bound: %w", launchidentity.ErrUnavailable)
	}
	b.exactAllocation = &runtime
	return nil
}

func (b *Bridge) exactRuntimeReady() bool {
	if b == nil {
		return false
	}
	b.exactMu.Lock()
	defer b.exactMu.Unlock()
	return b.exactBound && b.exactProcess != nil && b.exactProcess.Resolve != nil && b.exactProcess.Retain != nil && b.exactAllocation != nil && b.exactAllocation.Resolve != nil
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
	// AgentAuthority 返回 exact registration 与 current active capability
	// snapshot 的耐久一致视图（R2/R3 current-ledger recheck）。
	AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error)
}

// NewBridge 构造 Bridge；nil provider fail closed。
func NewBridge(provider sandbox.SandboxProvider) (*Bridge, error) {
	if provider == nil {
		return nil, errors.New("sandboxbridge: NewBridge requires a non-nil SandboxProvider")
	}
	return &Bridge{provider: provider, registry: &allocRegistry{}, now: time.Now}, nil
}

// WithTranscriptSource 注入 legacy/non-production staged transcript artifact
// 回读实现（Local 形态基于 AllocationDirectory；测试注入等价闭包）。
// Production uses ProductionAllocation.ReadArtifact and rejects this path.
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

// WithProductionGate 启用 production gate：非 LaunchCapable adapter
// 在 RunWorker 中被拒绝（fail closed），不允许静默走 legacy Run。
func (b *Bridge) WithProductionGate() *Bridge {
	b.productionGate = true
	return b
}

// RunWorker 实现 execution.Input.WorkerRunner。成功时原样返回 adapter 的
// WorkerResult 记录；失败时返回错误（execution 的既有失败归一化与
// fail-closed 持久化链继续适用），且 allocation 一定被 Terminate。
//
// 路径选择：adapter 实现 LaunchCapable 且桥配置了 TranscriptSource →
// allocation-carried 执行链（ADR 0052 §1.2）；否则 legacy 记账式路径
// （allocation 身份绑定 + adapter.Run）。
//
// v1.0 production 门禁要求 durableAuthority + ExactProcessRuntime +
// ExactAllocationRuntime；path transcriptSource is non-production only.
// 当前 CLI 没有装配 ExactAllocationRuntime，因此 production 仍不可达。
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
	var exactAdmission *exactProcessAdmission
	if b.productionGate {
		if b.authority == nil || !b.exactRuntimeReady() {
			return domain.Record{}, fmt.Errorf("sandboxbridge: incomplete production runtime: %w", launchidentity.ErrUnavailable)
		}
		capable, ok := adapter.(ProductionLaunchCapable)
		if !ok || capable.ProductionLaunchProfileID() != launchidentity.Pi0843DarwinARM64Profile {
			return domain.Record{}, fmt.Errorf("sandboxbridge: adapter %q lacks the exact production launch profile: %w", adapter.ID(), launchidentity.ErrUnavailable)
		}
		exactAdmission, err = b.resolveProductionAttempt(ctx, view)
		if err != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: exact production attempt unavailable: %w", err)
		}
		exactAdmission.allocation, err = b.resolveProductionAllocation(ctx, view, exactAdmission)
		if err != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: exact Stage2 allocation unavailable: %w", err)
		}
		exactAdmission.plan, err = capable.PreflightLaunch(ctx, request)
		if err != nil || ValidateLaunchPlan(exactAdmission.plan) != nil || b.validateProductionLaunch(exactAdmission.plan) != nil {
			if exactAdmission.plan != nil {
				exactAdmission.plan.CloseLaunchClosure()
			}
			return domain.Record{}, fmt.Errorf("sandboxbridge: exact production closure unavailable: %w", launchidentity.ErrUnavailable)
		}
	}
	if capable, ok := adapter.(LaunchCapable); ok && (b.transcriptSource != nil || exactAdmission != nil) {
		return b.runWorkerExecChain(ctx, capable, request, view, exactAdmission)
	}
	// productionGate 为 true时，上面的 closed capability checks make this
	// branch unreachable; keep a typed fail-closed guard against composition
	// changes.
	if b.productionGate {
		return domain.Record{}, fmt.Errorf("sandboxbridge: adapter %q cannot enter the production launch chain: %w", adapter.ID(), launchidentity.ErrUnavailable)
	}
	return b.runWorkerLegacy(ctx, adapter, request, view)
}

func (b *Bridge) resolveProductionAllocation(ctx context.Context, view workerRequestView, admission *exactProcessAdmission) (ProductionAllocation, error) {
	if b == nil || b.exactAllocation == nil || b.exactAllocation.Resolve == nil || admission == nil {
		return nil, launchidentity.ErrUnavailable
	}
	lease, err := b.requireExactLease(view, admission)
	if err != nil {
		return nil, err
	}
	resolution, err := b.exactAllocation.Resolve(ctx, admission.attempt)
	if err != nil {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	if err := validateProductionAllocationResolution(admission, resolution); err != nil {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	effect, found, err := admission.authority.Store.EffectState(admission.authority.Identity.AuthorityNamespaceID, resolution.EffectID)
	if err != nil || !found {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	current, err := resolution.Facade.Current(ctx)
	if err != nil {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	if err := validateProductionAllocationCurrent(current, admission, lease); err != nil {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	if err := validateProductionAllocationEffect(current, admission, resolution.EffectID, effect); err != nil {
		return nil, errors.Join(launchidentity.ErrUnavailable, err)
	}
	return resolution.Facade, nil
}

func validateProductionAllocationResolution(admission *exactProcessAdmission, resolution ExactAllocationResolution) error {
	if admission == nil || admission.authority.Store == nil || resolution.Facade == nil || resolution.Authority == nil {
		return resultingress.ErrAllocationAuthorityConflict
	}
	allocationAuthority := admission.authority.AllocationAuthority
	if allocationAuthority == nil || resolution.Authority != allocationAuthority || !allocationAuthority.BoundToStore(admission.authority.Store) {
		return resultingress.ErrAllocationAuthorityConflict
	}
	canonicalEffectKey, err := resultingress.CanonicalAllocationEffectKey(admission.authority.Identity.AuthorityNamespaceID, resolution.EffectID)
	if err != nil || !resolution.Facade.BoundTo(allocationAuthority, canonicalEffectKey) {
		return errors.Join(resultingress.ErrAllocationAuthorityConflict, err)
	}
	return nil
}

func validateProductionAllocationCurrent(current allocationcontrol.CurrentLiveAllocationV1, admission *exactProcessAdmission, lease dispatch.DispatchLease) error {
	if current.Validate() != nil || admission == nil || admission.authority.Identity.Validate() != nil || lease.Validate() != nil {
		return launchidentity.ErrUnavailable
	}
	namespaceDigest, err := admission.authority.Identity.AuthorityNamespaceID.Digest()
	if err != nil {
		return launchidentity.ErrUnavailable
	}
	binding := current.Binding
	if binding.AuthorityNamespaceID != namespaceDigest || binding.TaskID != admission.attempt.TaskID || binding.RunID != admission.attempt.RunID ||
		binding.AttemptID != admission.attempt.AttemptID || binding.AllocationID != admission.attempt.AllocationID || binding.LeaseID != lease.LeaseId ||
		binding.Generation != admission.attempt.Generation || binding.FencingTokenDigest != admission.attempt.FencingTokenDigest {
		return launchidentity.ErrUnavailable
	}
	return nil
}

func validateProductionAllocationEffect(current allocationcontrol.CurrentLiveAllocationV1, admission *exactProcessAdmission, effectID string, effect resultingress.EffectAuthorityState) error {
	providerResourceIdentity, resourceErr := resultingress.CanonicalAllocationProviderResourceIdentity(current.Binding.AllocationID, current.LiveIdentity, current.MarkerDigest)
	intentDigest, intentErr := effect.Intent.Digest()
	receiptDigest, receiptErr := effect.Receipt.Digest()
	reconcileDigest, reconcileErr := effect.Reconcile.Digest()
	if current.Validate() != nil || admission == nil || admission.authority.Identity.Validate() != nil || strings.TrimSpace(effectID) == "" ||
		effect.Binding.Validate() != nil || effect.Intent.Validate() != nil || effect.Receipt.Validate() != nil || effect.Reconcile.Validate() != nil ||
		resourceErr != nil || intentErr != nil || receiptErr != nil || reconcileErr != nil ||
		effect.Binding.Identity != admission.authority.Identity || effect.Binding.Phase != resultingress.EffectPhaseAllocationProvision ||
		effect.Intent.EffectId != effectID || effect.Intent.CommandId != current.Binding.CommandID || effect.Intent.IdempotencyKey != current.Binding.IdempotencyKey ||
		effect.Intent.RequestDigest != current.ProvisionRequestDigest || effect.IntentRecordDigest != intentDigest || effect.IntentFactDigest != current.ProvisionIntentFactDigest ||
		effect.Binding.MarkerDigest != current.MarkerNonceDigest || effect.ReceiptFactDigest != current.ProvisionReceiptFactDigest ||
		effect.Receipt.IntentDigest != effect.IntentRecordDigest || effect.ReceiptRecordDigest != receiptDigest || effect.Receipt.Disposition != authority.DispositionApplied ||
		effect.Receipt.ProviderResourceIdentity != providerResourceIdentity || effect.Receipt.ObservedDigest != current.ProvisionReceiptDigest ||
		effect.Reconcile.IntentDigest != effect.IntentRecordDigest || effect.Reconcile.ReceiptDigest != effect.ReceiptRecordDigest ||
		effect.ReconcileRecordDigest != reconcileDigest || effect.ReconcileFactDigest == "" || effect.Reconcile.Decision != authority.DecisionAccept {
		return resultingress.ErrAllocationAuthorityConflict
	}
	return nil
}

// resolveProductionAttempt runs before Adapter.PrepareLaunch and before every
// provider/file effect owned by Bridge. Resolve receives only the immutable
// logical Attempt tuple; B2 must return the already-durable full allocation
// identity. RB2 never fabricates the missing tuple from a later Provision.
func (b *Bridge) resolveProductionAttempt(ctx context.Context, view workerRequestView) (*exactProcessAdmission, error) {
	if b == nil || b.exactProcess == nil || b.exactProcess.Resolve == nil || b.exactProcess.Retain == nil {
		return nil, launchidentity.ErrUnavailable
	}
	logical := ExactProcessAttempt{TaskID: view.TaskID, RunID: view.RunID, AttemptID: view.AttemptID}
	coordinator, authority, err := b.exactProcess.Resolve(ctx, logical)
	if err != nil || coordinator == nil || authority.Store == nil || authority.Verifier == nil || authority.Identity.Validate() != nil ||
		authority.Identity.TaskID != logical.TaskID || authority.Identity.RunID != logical.RunID || authority.Identity.AttemptID != logical.AttemptID {
		return nil, launchidentity.ErrUnavailable
	}
	attempt := ExactProcessAttempt{
		TaskID: authority.Identity.TaskID, RunID: authority.Identity.RunID, AttemptID: authority.Identity.AttemptID,
		AllocationID: authority.Identity.AllocationID, Generation: authority.Identity.DispatchGeneration,
		FencingTokenDigest: authority.Identity.FencingTokenDigest,
	}
	if err := validateProductionAttemptState(authority); err != nil {
		return nil, err
	}
	return &exactProcessAdmission{attempt: attempt, coordinator: coordinator, authority: authority}, nil
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
		"taskId":                        view.TaskID,
		"runId":                         view.RunID,
		"attemptId":                     view.AttemptID,
		"capabilityDigest":              view.CapabilityDigest,
		"worktreePath":                  view.WorktreePath,
		"executionProfile":              view.ExecutionProfile,
		"adapterId":                     view.AdapterID,
		"agentRegistrationId":           view.AgentRegistrationID,
		"agentCapabilitySnapshotDigest": view.AgentCapabilitySnapshotDigest,
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
