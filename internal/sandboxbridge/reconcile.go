package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// AllocationRecord 是桥在执行前持久化到 attempt 目录的 allocation 身份
// 记录：driver 崩溃（含 SIGKILL）后，reconciler 据此终结孤儿 allocation。
// 记录先于执行落盘：桥路径上「无不可解释孤儿」的最小权威锚点。
type AllocationRecord struct {
	Schema              string `json:"schema"`
	TaskID              string `json:"taskId"`
	RunID               string `json:"runId"`
	AttemptID           string `json:"attemptId"`
	AllocationID        string `json:"allocationId"`
	Generation          int64  `json:"generation"`
	FencingToken        string `json:"fencingToken"`
	RequirementsProfile string `json:"requirementsProfile"`
	RecordedAt          string `json:"recordedAt"`
	OwnerState          string `json:"ownerState"`
}

const allocationRecordSchema = "marshal.dev/v1alpha1"

// allocationRecordName 是 attempt 目录内记录文件名（Output 目录之外，
// 逃逸不出 worktree 之外的状态根）。
const allocationRecordName = "sandbox-allocation.json"

// RunStateResolver 是 reconciler 的权威状态源抽象（避免 direct store
// 依赖）：返回 (runID, attemptID) 的当前语义终态。终态判定由调用方冻结
// （见 Sweep 的约定）。
type RunStateResolver interface {
	// OwnerTerminal 报告 runID 对应 Run 是否已终态或 driver 已死亡
	// （挂起时间超过 staleThreshold）。实现必须 fail closed：查不到/不确定
	// 一律视为存活（不终结 allocation）。
	OwnerTerminal(runID string, staleThreshold time.Duration, now time.Time) (bool, error)
}

// sweepResult 是单次孤儿扫描的结论。
type SweepResult struct {
	Scanned    int
	Terminated int
	KeptAlive  int
	Errors     []error
}

// recordAllocation 把 allocation 身份写入 controlRoot 所在 attempt 目录——
// bridge 在执行前调用（best-effort；写失败仅降级为无 reconciler 锚点的
// 现状，不打断执行）。
func recordAllocation(attemptControlRoot string, rec AllocationRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("sandboxbridge: allocation record marshal: %w", err)
	}
	dir := filepath.Dir(filepath.Clean(attemptControlRoot))
	path := filepath.Join(dir, allocationRecordName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("sandboxbridge: allocation record write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sandboxbridge: allocation record commit: %w", err)
	}
	return nil
}

// LoadAllocationRecord 读取 attempt 目录中的 allocation 记录（不存在返回
// ok=false；损坏返回错误，fail closed——不把无人能解释的记录当证据）。
func LoadAllocationRecord(attemptDir string) (AllocationRecord, bool, error) {
	raw, err := os.ReadFile(filepath.Join(attemptDir, allocationRecordName))
	if errors.Is(err, os.ErrNotExist) {
		return AllocationRecord{}, false, nil
	}
	if err != nil {
		return AllocationRecord{}, false, err
	}
	var rec AllocationRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return AllocationRecord{}, false, fmt.Errorf("sandboxbridge: allocation record unreadable: %w", err)
	}
	if rec.Schema != allocationRecordSchema || rec.AllocationID == "" || rec.RunID == "" || rec.AttemptID == "" || rec.Generation < 1 || rec.FencingToken == "" {
		return AllocationRecord{}, false, fmt.Errorf("sandboxbridge: allocation record %q fails schema validation", attemptDir)
	}
	return rec, true, nil
}

// SweepOrphans 扫描一组 attempt 目录（调用方负责枚举），对 owner 已终态
// 或 driver 已死亡的记录调用 provider.Terminate 终结孤儿 allocation。
//
// 冻结语义：
//   - owner 活（Run 未终态且 driver 新鲜）→ 一律保留（Keep）；
//   - owner 终态/死亡 → Terminate（幂等，重复扫描安全）；
//   - provider.Terminate 失败是 scan 级错误，不中断其他记录；
//   - resolver 不确定（查不到 Run）按存活处理（fail closed，不误杀）。
func SweepOrphans(ctx context.Context, provider sandbox.SandboxProvider, resolver RunStateResolver, attemptDirs []string, staleThreshold time.Duration, now time.Time) SweepResult {
	result := SweepResult{Scanned: len(attemptDirs)}
	if provider == nil || resolver == nil {
		result.Errors = append(result.Errors, errors.New("sandboxbridge: sweep requires non-nil provider and resolver"))
		return result
	}
	for _, dir := range attemptDirs {
		rec, ok, err := LoadAllocationRecord(dir)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}
		if !ok {
			continue
		}
		terminal, err := resolver.OwnerTerminal(rec.RunID, staleThreshold, now)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("sandboxbridge: resolve owner %q: %w", rec.RunID, err))
			continue
		}
		if !terminal {
			result.KeptAlive++
			continue
		}
		identity := sandbox.OperationIdentity{
			TaskId:       rec.TaskID,
			RunId:        rec.RunID,
			AttemptId:    rec.AttemptID,
			WorkloadRole: sandbox.WorkloadRoleWorker,
			AllocationId: rec.AllocationID,
			Generation:   rec.Generation,
			FencingToken: rec.FencingToken,
			CommandId:    "command-reconcile-terminate",
		}
		if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{Identity: identity, AllocationId: rec.AllocationID}); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("sandboxbridge: terminate orphan %q: %w", rec.AllocationID, err))
			continue
		}
		result.Terminated++
	}
	return result
}

// staleResolver 是 reconciler 的最小内置 resolver：owner 状态由调用方
// 以 map[runID]terminal 提供（生产接线由 CLI/doctor 装配 runstore 查询）。
type staleResolver struct {
	terminal map[string]bool
}

// NewMapResolver 构造静态 resolver（测试与简化装配用）。
func NewMapResolver(terminal map[string]bool) RunStateResolver {
	return &staleResolver{terminal: terminal}
}

func (r *staleResolver) OwnerTerminal(runID string, _ time.Duration, _ time.Time) (bool, error) {
	return r.terminal[runID], nil
}

// allocRegistry 随 Bridge 实例收集已 record 的 attempt 目录（进程内枚举
// 源；跨进程枚举由调用方基于 attempt 目录结构装配）。
type allocRegistry struct {
	mu   sync.Mutex
	dirs []string
}

func (r *allocRegistry) add(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append(r.dirs, dir)
}

// SweepRegistered 终结本 Bridge 会话中已 record 且 owner 终态的 allocation。
func (b *Bridge) SweepRegistered(ctx context.Context, provider sandbox.SandboxProvider, resolver RunStateResolver, staleThreshold time.Duration, now time.Time) SweepResult {
	b.registry.mu.Lock()
	dirs := append([]string(nil), b.registry.dirs...)
	b.registry.mu.Unlock()
	return SweepOrphans(ctx, provider, resolver, dirs, staleThreshold, now)
}

// controlRootOf 从 request 提取 attempt control root（落盘目录的父目录即
// attempt 目录）。空字段原样返回空——filepath.Clean("") 返回 "."，必须
// 在 Clean 前判空，否则会把记录错误写到当前工作目录。
func controlRootOf(data []byte) string {
	var v struct {
		ControlRoot string `json:"controlRoot"`
	}
	_ = json.Unmarshal(data, &v)
	trimmed := strings.TrimSpace(v.ControlRoot)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}
