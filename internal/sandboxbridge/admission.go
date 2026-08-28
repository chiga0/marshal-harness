package sandboxbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
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
func (b *Bridge) admitCompletedResult(ctx context.Context, view workerRequestView, plan LaunchPlan, resultBytes []byte, allocationID string, generation int64, fencingToken string) error {
	inspectIdentity, err := identity(view, allocationID, generation, fencingToken, "command-inspect-admission")
	if err != nil {
		return err
	}
	report, err := b.provider.Inspect(ctx, sandbox.InspectRequest{Identity: inspectIdentity, AllocationId: allocationID})
	if err != nil {
		return fmt.Errorf("sandboxbridge: admission inspect failed: %w", err)
	}

	attemptDir := attemptDirFor(view, plan)
	// The raw result is the durable outbox payload. It is installed before the
	// ResultIngress fact so every committed admission can be replayed into the
	// Run journal after a driver crash. Creation-once rejects ABA/conflicting
	// bytes for the same attempt.
	if err := persistWorkerResultOnce(attemptDir, resultBytes); err != nil {
		return fmt.Errorf("sandboxbridge: stage worker result before admission: %w", err)
	}

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
		AgentExecutable:               plan.Argv()[0],
		AgentProviderVersion:          plan.ProviderVersion(),
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

// ReconcileAdmittedWorkerResult completes the durable ResultIngress outbox
// after a driver restart. It reopens the immutable AttemptBinding and exact
// staged result, rechecks current authority for a new admission or returns the
// already committed fact as an idempotent replay, then refreshes the audit
// anchor. Execution owns the single worker.completed journal append.
func (b *Bridge) ReconcileAdmittedWorkerResult(ctx context.Context, attemptDir string) ([]byte, *resultbinding.Admission, error) {
	if b == nil || b.authority == nil {
		return nil, nil, errors.New("sandboxbridge: durable result reconciliation requires authority")
	}
	raw, err := os.ReadFile(filepath.Join(attemptDir, "worker-result.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxbridge: read staged worker result: %w", err)
	}
	binding, err := resultbinding.ReadAttemptBinding(attemptDir)
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxbridge: reconcile admission: %w", err)
	}
	authSource := bridgeAuthoritySource{authority: b.authority}
	if committed, found, replayErr := resultbinding.ReplayCommittedWithDurableAuthority(binding, raw, authSource); replayErr != nil {
		return nil, nil, fmt.Errorf("sandboxbridge: reconcile committed admission: %w", replayErr)
	} else if found {
		if writeErr := writeAdmissionAnchor(attemptDir, committed); writeErr != nil {
			return nil, nil, fmt.Errorf("sandboxbridge: recovery admission anchor persist failed: %w", writeErr)
		}
		return raw, committed, nil
	}
	facts := binding.Facts
	view := workerRequestView{TaskID: facts.TaskID, RunID: facts.RunID, AttemptID: facts.AttemptID}
	inspectIdentity, err := identity(view, facts.AllocationID, facts.AllocationGeneration, facts.FencingToken, "command-inspect-admission-recovery")
	if err != nil {
		return nil, nil, err
	}
	report, err := b.provider.Inspect(ctx, sandbox.InspectRequest{Identity: inspectIdentity, AllocationId: facts.AllocationID})
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxbridge: recovery admission inspect failed: %w", err)
	}
	admission, admitErr := resultbinding.AdmitWithDurableAuthority(ctx, binding, raw, authSource, report.State)
	if writeErr := writeAdmissionAnchor(attemptDir, admission); writeErr != nil {
		return nil, nil, fmt.Errorf("sandboxbridge: recovery admission anchor persist failed: %w", writeErr)
	}
	if admitErr != nil {
		return nil, admission, admitErr
	}
	return raw, admission, nil
}

func persistWorkerResultOnce(attemptDir string, resultBytes []byte) error {
	if attemptDir == "" {
		return nil
	}
	path := filepath.Join(attemptDir, "worker-result.json")
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, resultBytes) {
			return errors.New("worker-result creation-once violation")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(attemptDir, ".worker-result-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(resultBytes); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dir, err := os.Open(attemptDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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

func (s bridgeAuthoritySource) AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error) {
	return s.authority.AgentAuthority(registrationID)
}

// ResultIngressDir 给 resultbinding 提供 ResultIngress 耐久 replay 账本目录：
// 若 durable authority 实现了 ResultIngressDir()（EmbeddedSandboxRuntime 提供
// stateRoot/resultingress），直接返回其值；否则（测试 fake）返回空字符串，
// admission 回退进程内存 ingress 保持向后兼容。
func (s bridgeAuthoritySource) ResultIngressDir() string {
	type ingressDirer interface{ ResultIngressDir() string }
	if d, ok := s.authority.(ingressDirer); ok {
		return d.ResultIngressDir()
	}
	return ""
}

func attemptDirFor(view workerRequestView, plan LaunchPlan) string {
	if plan != nil && plan.ControlRootPath() != "" {
		return filepath.Dir(filepath.Clean(plan.ControlRootPath()))
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
