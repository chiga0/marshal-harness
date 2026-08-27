package sandboxbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultbinding"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

const admissionAnchorName = "sandbox-binding-admission.json"

// admitCompletedResult 在 CompleteLaunch 产出真实 WorkerResult 后执行
// ADR 0052 §1.4 的生产 admission：Inspect 回读 live allocation state →
// 双 binding recheck + ResultIngress 接纳 → anchor 落盘进 attempt 目录。
// 任何一侧 fail closed（含 live state 非 active）都以 typed 错误返回。
//
// R2/R3 纠偏：当 Bridge 注入了 DurableAuthority 时，从 dispatch 时冻结的
// immutable AttemptBinding 文件读取 facts（而非以结果携带 Facts 临时构造），
// 并从真实 RegistrationStore 验证 provider registration 仍 active。
func (b *Bridge) admitCompletedResult(ctx context.Context, view workerRequestView, plan *pi.LaunchPlan, resultBytes []byte, allocationID string, generation int64, fencingToken string) error {
	inspectIdentity, err := identity(view, allocationID, generation, fencingToken, "command-inspect-admission")
	if err != nil {
		return err
	}
	report, err := b.provider.Inspect(ctx, sandbox.InspectRequest{Identity: inspectIdentity, AllocationId: allocationID})
	if err != nil {
		return fmt.Errorf("sandboxbridge: admission inspect failed: %w", err)
	}

	attemptDir := attemptDirFor(view, plan)

	// R2/R3 纠偏：生产路径从 immutable AttemptBinding + 真实 durable
	// authority 接纳；退化为 seed 路径仅在无 authority 注入时（测试兼容）。
	if b.authority != nil && attemptDir != "" {
		binding, readErr := resultbinding.ReadAttemptBinding(attemptDir)
		if readErr != nil {
			return fmt.Errorf("sandboxbridge: admission: %w", readErr)
		}
		authSource := bridgeAuthoritySource{authority: b.authority}
		admission, admitErr := resultbinding.AdmitWithDurableAuthority(ctx, binding, resultBytes, authSource, report.State)
		if writeErr := writeAdmissionAnchor(attemptDir, admission); writeErr != nil {
			return fmt.Errorf("sandboxbridge: admission anchor persist failed: %w", writeErr)
		}
		return admitErr
	}

	// 测试兼容路径：无 durable authority 时以 Inspect 时的临时 facts 走 seed。
	facts := resultbinding.Facts{
		TaskID:                        view.TaskID,
		RunID:                         view.RunID,
		AttemptID:                     view.AttemptID,
		AgentAdapterID:                view.AdapterID,
		AgentExecutable:               plan.ExecArgv[0],
		AgentProviderVersion:          plan.BinaryVersion(),
		CapabilityDigest:              view.CapabilityDigest,
		ExecutionProfile:              view.ExecutionProfile,
		SandboxProviderRegistrationID: sandboxProviderRegistrationID,
		AllocationID:                  allocationID,
		AllocationGeneration:          generation,
		LiveAllocationState:           report.State,
		FencingToken:                  fencingToken,
		LeaseExpiry:                   b.now().UTC().Add(24 * time.Hour),
	}
	admission, err := resultbinding.AdmitWorkerResult(ctx, facts, resultBytes)
	if writeErr := writeAdmissionAnchor(attemptDir, admission); writeErr != nil {
		return fmt.Errorf("sandboxbridge: admission anchor persist failed: %w", writeErr)
	}
	return err
}

// bridgeAuthoritySource 适配 Bridge.DurableAuthority 到
// resultbinding.DurableAuthoritySource。
type bridgeAuthoritySource struct {
	authority DurableAuthority
}

func (s bridgeAuthoritySource) ProviderRegistration() (provider.ProviderRegistration, error) {
	reg := s.authority.Registration()
	if reg.RegistrationId == "" {
		return provider.ProviderRegistration{}, fmt.Errorf("empty registration id from durable authority")
	}
	return reg, nil
}

func (s bridgeAuthoritySource) ProviderRegistrationActive(registrationID string) (bool, error) {
	store := s.authority.RegistrationStore()
	if store == nil {
		return false, fmt.Errorf("nil RegistrationStore from durable authority")
	}
	reg, err := store.Get(registrationID)
	if err != nil {
		return false, err
	}
	return reg.LifecycleState == provider.LifecycleStateActive, nil
}

func attemptDirFor(view workerRequestView, plan *pi.LaunchPlan) string {
	if plan != nil && plan.ControlRoot != "" {
		return filepath.Dir(filepath.Clean(plan.ControlRoot))
	}
	return ""
}

// writeAdmissionAnchor 把 admission 结论原子地写进 attempt 目录
// （与 sandbox-allocation.json 同目录，幂等覆盖同一次 Run 的产物）。
func writeAdmissionAnchor(attemptDir string, admission *resultbinding.Admission) error {
	if attemptDir == "" {
		return nil
	}
	if admission == nil {
		return nil
	}
	raw, err := json.MarshalIndent(admission, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(attemptDir, admissionAnchorName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// sandboxProviderRegistrationID 是 Local embedded provider 的注册 ID
// （runtimeprofile 绑定要求 registration: 前缀）。
const sandboxProviderRegistrationID = "registration:local-runner"
