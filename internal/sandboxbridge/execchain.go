package sandboxbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultbinding"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// LaunchCapable 是桥对接「Launch 可拆分 Adapter」的最小接口（ADR 0052
// §1.2 + ADR 0055）：实现者提供不可变启动计划与由外部执行驱动的完成
// 管线。未实现本接口的 Adapter 由桥自动回退 legacy Run 路径。
// LaunchPlan 是 provider-neutral 接缝，sandboxbridge 不直接依赖任何
// 特定 Adapter 的类型。
type LaunchCapable interface {
	PrepareLaunch(ctx context.Context, record domain.Record) (LaunchPlan, error)
	CompleteLaunch(ctx context.Context, plan LaunchPlan, transcriptJSONL []byte, stdoutTruncated bool, stderrBytes []byte, started, completed time.Time, exitCode int, signal string, ctxErr error) (domain.Record, error)
}

// TranscriptSource 从 provider 形态读取 staged transcript artifact 的原始
// bytes。v1.0 Local 形态由 CLI 注入基于 AllocationDirectory 的实现；
// 测试注入等价闭包。返回错误 fail closed。
type TranscriptSource func(allocationID, artifactID string) ([]byte, error)

// runWorkerExecChain 是 ADR 0052 §1.2 的 allocation-carried 执行路径：
// PrepareLaunch（Adapter 预计算）→ Provision（声明 WorkDir/Env 白名单快照）
// → Stage（冻结工单与 prompt 内容寻址入账）→ Exec（agent 进程实际运行于
// allocation，TranscriptPolicy 有界收成）→ transcript 回读并 digest 核对
// → CompleteLaunch（同一 Adapter decode/finalize 管线接管 WorkerResult 与
// 全部控制产物）。任何失败 fail closed；allocation 一定 Terminate。
func (b *Bridge) runWorkerExecChain(ctx context.Context, capable LaunchCapable, request domain.Record, view workerRequestView) (domain.Record, error) {
	if b.transcriptSource == nil {
		return domain.Record{}, errors.New("sandboxbridge: exec-chain requires a transcript source")
	}
	plan, err := capable.PrepareLaunch(ctx, request)
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: prepare launch: %w", err)
	}

	requirements, err := domain.SandboxRequirementsFromLegacy(view.ExecutionProfile)
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: %w", err)
	}
	profileDigest, err := deriveProfileDigest(view)
	if err != nil {
		return domain.Record{}, err
	}
	if err := ValidateLaunchPlan(plan); err != nil {
		return domain.Record{}, err
	}

	spec, err := newExecChainSpec(view, profileDigest)
	if err != nil {
		return domain.Record{}, err
	}
	specDigest, err := spec.Digest()
	if err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: %w", err)
	}
	requestedAllocation := allocDigestOf(specDigest)
	const attemptGeneration = int64(1)

	var allocationID string
	var generation int64
	var fencingToken string

	if b.authority != nil {
		// Embedded authority 模式（dispatchBinder 已注入）：BindDispatch 在
		// execution.Run 入口已向 durable LocalRunner 完成 Provision 并签发
		// lease（含 canonical fencingToken 与确定性 AllocationId）。exec-chain
		// 不得二次 Provision allocation，直接复用 BindDispatch 已建立的 allocation
		// 与 lease identity。LeaseState 非 claimed/active fail closed。
		lease, leaseOK := b.authority.LeaseFor(view.RunID, view.AttemptID)
		if !leaseOK {
			return domain.Record{}, fmt.Errorf("sandboxbridge: dispatch lease not found for run=%s attempt=%s (fail closed: no fabricated expiry)", view.RunID, view.AttemptID)
		}
		if lease.LeaseState != dispatch.LeaseStateClaimed && lease.LeaseState != dispatch.LeaseStateActive {
			return domain.Record{}, fmt.Errorf("sandboxbridge: dispatch lease carries terminal state %q for run=%s attempt=%s (fail closed)", string(lease.LeaseState), view.RunID, view.AttemptID)
		}
		if err := dispatch.ValidateLeaseFencing(lease, lease.Generation, lease.FencingToken); err != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: embedded authority lease fencing invalid (fail closed): %w", err)
		}
		// 补充 Provision 以写入 bridge 的 WorkDirAllowlist / EnvironmentAllowlist。
		// AllocationProvider 幂等：同一 (runId, attemptId) 的二次 Provision 以
		// identity+lease fencing 通过并刷新 envelope，不会产生新 allocation。
		allocationID = lease.AllocationId
		generation = lease.Generation
		fencingToken = lease.FencingToken
		provisionIdentity, idErr := identity(view, allocationID, generation, fencingToken, "command-env-enrich")
		if idErr != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: env-enrich identity: %w", idErr)
		}
		if _, provisionErr := b.provider.Provision(ctx, sandbox.ProvisionRequest{
			Identity:             provisionIdentity,
			Requirements:         requirements,
			AllowedStoreIds:      []string{},
			WorkDirAllowlist:     []string{plan.WorkDir()},
			EnvironmentAllowlist: envKeyAllowlist(plan.EnvBlock()),
		}); provisionErr != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: env-enrich provision failed: %w", provisionErr)
		}
	} else {
		fencingToken = fencingDigestOf(view.AdapterID, requestedAllocation, attemptGeneration)

		provisionIdentity, idErr := identity(view, requestedAllocation, attemptGeneration, fencingToken, "command-provision")
		if idErr != nil {
			return domain.Record{}, idErr
		}
		provisionReceipt, provisionErr := b.provider.Provision(ctx, sandbox.ProvisionRequest{
			Identity:             provisionIdentity,
			Requirements:         requirements,
			AllowedStoreIds:      []string{},
			WorkDirAllowlist:     []string{plan.WorkDir()},
			EnvironmentAllowlist: envKeyAllowlist(plan.EnvBlock()),
		})
		if provisionErr != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: provision failed: %w", provisionErr)
		}
		allocationID = provisionReceipt.Allocation.AllocationId
		generation = provisionReceipt.Allocation.Generation
	}

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
		if recErr := recordAllocation(controlRoot, rec); recErr != nil {
			return domain.Record{}, fmt.Errorf("sandboxbridge: write allocation record (fail closed: %w)", recErr)
		}
		b.registry.add(filepath.Dir(controlRoot))
		// R2/R3 纠偏：dispatch 时持久化 immutable AttemptBinding（含 dispatch
		// 冻结的 lease expiry），ingress 时从该文件读取而非以结果携带 Facts
		// 临时构造。binding 文件携带 content digest，篡改 fail closed。
		//
		// R2/R3 深化（composition root 修复）：lease 缺失直接 fail closed，
		// 不再退回虚构的 now+24h。此前两个 runtime 实例导致 lease 不可见，
		// 单实例修复后 lease 在同进程内可查。若 lease 仍缺失，说明 dispatch
		// 未走 BindDispatch 路径或 runtime 实例不一致——这是不可接受的
		// authority 断裂，必须 fail closed 而非虚构 expiry。
		if b.authority != nil {
			lease, leaseOK := b.authority.LeaseFor(view.RunID, view.AttemptID)
			if !leaseOK {
				return domain.Record{}, fmt.Errorf("sandboxbridge: dispatch lease not found for run=%s attempt=%s (fail closed: no fabricated expiry)", view.RunID, view.AttemptID)
			}
			leaseExpiry, parseErr := time.Parse(time.RFC3339, lease.ExpiresAt)
			if parseErr != nil || leaseExpiry.IsZero() {
				return domain.Record{}, fmt.Errorf("sandboxbridge: dispatch lease has invalid expiry %q (fail closed)", lease.ExpiresAt)
			}
			leaseExpiry = leaseExpiry.UTC()
			regID := sandboxProviderRegistrationID
			if reg := b.authority.Registration(); reg.RegistrationId != "" {
				regID = reg.RegistrationId
			}
			// runtimeprofile 要求 SandboxProviderRegistrationID 带
			// "registration:" 前缀；durable authority 返回的原始 registrationId
			//（如 "local-sandbox-provider"）不带前缀，此处规范化补齐。
			if !strings.HasPrefix(regID, "registration:") {
				regID = "registration:" + regID
			}
			// 分离 agent/sandbox capability digest：agent 侧用 adapter probe
			// 的 CapabilitySnapshot digest（view.CapabilityDigest 来自
			// dispatch 时冻结的 adapter probe），sandbox 侧用 provider
			// CapabilitySnapshot digest。此前两者混为一个 capDigest，不是
			// 真正的双 binding。
			agentCapDigest := view.CapabilityDigest
			sandboxCapDigest := agentCapDigest
			if snap := b.authority.CapabilitySnapshot(); snap.ProviderCapabilitySnapshotDigest != "" {
				sandboxCapDigest = snap.ProviderCapabilitySnapshotDigest
			}
			bindingFacts := resultbinding.Facts{
				TaskID:                        view.TaskID,
				RunID:                         view.RunID,
				AttemptID:                     view.AttemptID,
				AgentAdapterID:                view.AdapterID,
				AgentExecutable:               plan.Argv()[0],
				AgentProviderVersion:          plan.ProviderVersion(),
				CapabilityDigest:              agentCapDigest,
				AgentRegistrationID:           view.AgentRegistrationID,
				SandboxCapabilityDigest:       sandboxCapDigest,
				ExecutionProfile:              view.ExecutionProfile,
				SandboxProviderRegistrationID: regID,
				AllocationID:                  allocationID,
				AllocationGeneration:          generation,
				LiveAllocationState:           sandbox.AllocationActive,
				FencingToken:                  fencingToken,
				LeaseExpiry:                   leaseExpiry,
			}
			if writeErr := resultbinding.WriteAttemptBinding(filepath.Dir(controlRoot), bindingFacts); writeErr != nil {
				return domain.Record{}, fmt.Errorf("sandboxbridge: write attempt binding: %w", writeErr)
			}
		}
	}
	// Terminate 由 legacy Provision 路径 owner（本 bridge 创建）负责。
	// Embedded authority 模式下 allocation 由 BindDispatch Provision 创建，
	// lease /state 变更由 execution.Run 完成（ResultIngress/admission 后），
	// bridge 不得 Terminate，否则宿主 runtime 在 result ingress 前提前释放。
	if b.authority == nil {
		defer func() {
			termIdentity, idErr := identity(view, allocationID, generation, fencingToken, "command-terminate")
			if idErr != nil {
				return
			}
			_, _ = b.provider.Terminate(ctx, sandbox.TerminateRequest{Identity: termIdentity, AllocationId: allocationID})
		}()
	}

	if err := b.stageControlInputs(ctx, view, request, allocationID, generation, fencingToken, plan.ControlRootPath()); err != nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: stage control inputs: %w", err)
	}

	// 与 legacy adapter.Run 相同的 attempt 级截止：外部 ctx 由 bridge 叠加，
	// provider 侧 TimeoutSeconds 为同一数值的保底 kill。
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds())*time.Second)
	defer cancel()

	started := b.now().UTC()
	execIdentity, err := identity(view, allocationID, generation, fencingToken, "command-exec")
	if err != nil {
		return domain.Record{}, err
	}
	execReceipt, execErr := b.provider.Exec(runCtx, sandbox.ExecRequest{
		Identity:     execIdentity,
		AllocationId: allocationID,
		Command:      append([]string(nil), plan.Argv()...),
		WorkingDir:   plan.WorkDir(),
		Environment:  envMap(plan.EnvBlock()),
		TranscriptPolicy: sandbox.TranscriptPolicy{
			MaxBytes:   plan.MaxOutput(),
			ArtifactId: transcriptArtifactID,
		},
		TimeoutSeconds: plan.TimeoutSeconds(),
	})
	completed := b.now().UTC()
	if execErr != nil && execReceipt == nil {
		return domain.Record{}, fmt.Errorf("sandboxbridge: exec failed: %w", execErr)
	}

	transcript, err := b.readAndVerifyTranscript(allocationID, execReceipt)
	if err != nil {
		return domain.Record{}, err
	}

	exitCode := 0
	signal := ""
	if execReceipt != nil {
		exitCode = execReceipt.ExitCode
		if execReceipt.Status == sandbox.ExecutionKilled {
			signal = "SIGKILL"
		}
	}
	ctxErr := runCtx.Err()
	if ctxErr != nil && signal == "" {
		signal = "timeout"
	}
	record, err := capable.CompleteLaunch(ctx, plan, transcript, false, nil, started, completed, exitCode, signal, ctxErr)
	if err != nil {
		return domain.Record{}, err
	}
	// ADR 0052 §1.4：真实 WorkerResult 接纳前的双 binding + ResultIngress
	// admission（live allocation state 回读 + anchor 落盘）。任何拒绝以
	// untyped 错误交给 execution 的 typed 归一化（protocol-invalid /
	// do-not-retry 正台语义）。
	if err := b.admitCompletedResult(ctx, view, plan, record.Data, allocationID, generation, fencingToken); err != nil {
		return domain.Record{}, err
	}
	return record, nil
}

// readAndVerifyTranscript 读取 staged artifact 并与 provider 重算 digest
// 核对（一次内容寻址往返；provider digest 为权威一侧，bytes 为读取一侧，
// 不一致立刻 fail closed）。
func (b *Bridge) readAndVerifyTranscript(allocationID string, receipt *sandbox.ExecReceipt) ([]byte, error) {
	raw, err := b.transcriptSource(allocationID, transcriptArtifactID)
	if err != nil {
		return nil, fmt.Errorf("sandboxbridge: transcript readback: %w", err)
	}
	if receipt == nil || receipt.TranscriptDigest == "" {
		return nil, errors.New("sandboxbridge: exec receipt lacks transcript digest")
	}
	if got := canonical.DigestBytes(raw); got != receipt.TranscriptDigest {
		return nil, fmt.Errorf("sandboxbridge: transcript digest mismatch: readback %q, receipt %q", got, receipt.TranscriptDigest)
	}
	return raw, nil
}

// stageControlInputs 把 worker-request 与 controlRoot 内的冻结输入原样
// 内容寻址入账（消费前后由 provider 重算 digest）。
func (b *Bridge) stageControlInputs(ctx context.Context, view workerRequestView, request domain.Record, allocationID string, generation int64, fencingToken, controlRoot string) error {
	stageIdentity, err := identity(view, allocationID, generation, fencingToken, "command-stage")
	if err != nil {
		return err
	}
	inputs := []sandbox.StageInput{{
		InputId:        "worker-request",
		DeclaredSHA256: canonical.DigestBytes(request.Data),
		Inline:         append([]byte(nil), request.Data...),
	}}
	if controlRoot != "" {
		for _, name := range []string{"input/task-spec.json", "input/prompt.md"} {
			raw, readErr := os.ReadFile(filepath.Join(controlRoot, name))
			if readErr != nil || len(raw) == 0 {
				continue
			}
			inputs = append(inputs, sandbox.StageInput{
				InputId:        name,
				DeclaredSHA256: canonical.DigestBytes(raw),
				Inline:         raw,
			})
		}
	}
	if _, err := b.provider.Stage(ctx, sandbox.StageRequest{
		Identity:     stageIdentity,
		AllocationId: allocationID,
		Inputs:       inputs,
	}); err != nil {
		return fmt.Errorf("sandboxbridge: stage failed: %w", err)
	}
	return nil
}

func envKeyAllowlist(environment []string) []string {
	keys := make([]string, 0, len(environment))
	for _, kv := range environment {
		if i := strings.IndexByte(kv, '='); i > 0 && !credentialKey(kv[:i]) {
			keys = append(keys, kv[:i])
		}
	}
	return keys
}

// credentialKey 与 SPI 的凭据语义键一致判定（粗粒度子串）。
func credentialKey(key string) bool {
	l := strings.ToLower(key)
	for _, token := range []string{"key", "token", "secret", "password", "passwd", "credential"} {
		if strings.Contains(l, token) {
			return true
		}
	}
	return false
}

func envMap(environment []string) map[string]string {
	out := make(map[string]string, len(environment))
	for _, kv := range environment {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// newExecChainSpec 构造执行链的 AgentLaunchSpec（身份源与 legacy 路径
// 一致：adapter 身份 + capability digest 作为 executable 绑定）。
func newExecChainSpec(view workerRequestView, profileDigest string) (agentruntime.AgentLaunchSpec, error) {
	spec, err := agentruntime.NewAgentLaunchSpec(
		view.AdapterID, "capability-bound",
		view.RunID, view.AttemptID,
		view.AdapterID, view.CapabilityDigest,
		view.WorktreePath,
		nil, nil,
		profileDigest, "",
	)
	if err != nil {
		return agentruntime.AgentLaunchSpec{}, fmt.Errorf("sandboxbridge: %w", err)
	}
	return spec, nil
}

const transcriptArtifactID = "marshal-transcript"

func allocDigestOf(specDigest string) string {
	return canonical.DigestBytes([]byte("sandboxbridge:execchain:allocation:" + specDigest))
}

func fencingDigestOf(adapterID, allocationID string, generation int64) string {
	return canonical.DigestBytes([]byte(adapterID + ":" + allocationID + ":" + fmt.Sprint(generation)))
}
