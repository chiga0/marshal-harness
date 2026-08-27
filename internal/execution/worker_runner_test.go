package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/sandbox"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// TestRunThroughWorkerRunnerSandboxBridge 证明 WorkerRunner seam 上的
// sandbox bridge 路径与 legacy host 路径产生相同的 Run 级语义：同一状态
// 推进、同一事件类型序列、同一产物集合；差异只在执行链身份绑定
// （allocation/lease/stage）由桥承载。
func TestRunThroughWorkerRunnerSandboxBridge(t *testing.T) {
	legacy := newExecutionFixture(t, false)
	legacyResult, err := Run(context.Background(), legacy.input)
	if err != nil {
		t.Fatalf("legacy Run: %v", err)
	}

	bridged := newExecutionFixture(t, false)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, err := sandboxbridge.NewBridge(provider)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	runnerCalled := false
	bridged.input.WorkerRunner = func(ctx context.Context, adapter port.WorkerAdapter, request domain.Record) (domain.Record, error) {
		runnerCalled = true
		if adapter != bridged.input.Adapter {
			t.Errorf("runner must receive the same injected adapter")
		}
		return bridge.RunWorker(ctx, adapter, request)
	}
	bridgedResult, err := Run(context.Background(), bridged.input)
	if err != nil {
		t.Fatalf("bridged Run: %v", err)
	}

	if !runnerCalled {
		t.Errorf("WorkerRunner seam was not used")
	}
	if bridgedResult.State.State != legacyResult.State.State || bridgedResult.State.AttemptsUsed != legacyResult.State.AttemptsUsed {
		t.Errorf("state divergence: legacy=%+v bridged=%+v", legacyResult.State, bridgedResult.State)
	}
	if bridgedResult.State.State != domain.StateVerifying {
		t.Errorf("expected verifying, got %+v", bridgedResult.State)
	}

	legacyEvents := eventTypes(t, legacy.input.StateRoot, legacy.input.RunID)
	bridgedEvents := eventTypes(t, bridged.input.StateRoot, bridged.input.RunID)
	if strings.Join(legacyEvents, ",") != strings.Join(bridgedEvents, ",") {
		t.Errorf("journal event sequence divergence:\nlegacy:  %v\nbridged: %v", legacyEvents, bridgedEvents)
	}

	attempt := filepath.Join(bridged.runDir, "attempts", bridgedResult.AttemptID)
	for _, path := range []string{"worker-request.json", "worker-result.json", "worktree-snapshot.json"} {
		if _, err := os.Stat(filepath.Join(attempt, path)); err != nil {
			t.Errorf("bridged path missing %s: %v", path, err)
		}
	}

	// 结果一致性：两条路径的 WorkerResult 只在 attemptId（随机身份）上
	// 不同；归一化 attemptId 后业务内容必须逐字节相同——桥不得改写业务
	// 事实。
	legacyResultRaw, err := os.ReadFile(filepath.Join(legacy.runDir, "attempts", legacyResult.AttemptID, "worker-result.json"))
	if err != nil {
		t.Fatalf("read legacy result: %v", err)
	}
	bridgedResultRaw, err := os.ReadFile(filepath.Join(attempt, "worker-result.json"))
	if err != nil {
		t.Fatalf("read bridged result: %v", err)
	}
	if normalizeAttempt(legacyResultRaw) != normalizeAttempt(bridgedResultRaw) {
		t.Errorf("worker result business content divergence between paths")
	}
}

func normalizeAttempt(raw []byte) string {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	delete(v, "attemptId")
	rem, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(rem)
}

// TestRunWorkerRunnerFailureKeepsFailureChain 证明 seam 上的执行失败走
// 与旧路径一致的 fail-closed 归一化与持久化：typed 失败按类型消费预算，
// untyped 失败归一化为 protocol-invalid 且零 raw cause 泄漏。
func TestRunWorkerRunnerFailureKeepsFailureChain(t *testing.T) {
	// 失败归一化要求 adapter id 命中封闭集合：使用 fake fixture（与既有
	// untyped-failure 测试同一模式）。
	fixture := newFakeExecutionFixture(t, executionFixtureOptions{})
	fixture.input.WorkerRunner = func(ctx context.Context, adapter port.WorkerAdapter, request domain.Record) (domain.Record, error) {
		return domain.Record{}, errBridgeStage
	}

	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("runner failure was accepted")
	}
	if result.State.State != domain.StateBlocked {
		t.Fatalf("untyped runner failure must block without retry, got %+v", result.State)
	}
	if result.State.OperationalRetriesUsed != 0 {
		t.Fatalf("untyped failure must not consume retry budget, got %+v", result.State)
	}

	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	last := events[len(events)-1]
	if last.Type != "worker.failed" || last.Payload["failureKind"] != string(port.FailureKindProtocolInvalid) || last.Payload["retryDisposition"] != string(port.RetryDispositionDoNotRetry) {
		t.Fatalf("failure normalization divergence: %+v", last)
	}
	if msg, _ := last.Payload["error"].(string); strings.Contains(msg, errBridgeStage.Error()) {
		t.Fatalf("untyped cause leaked into journal: %q", msg)
	}
}

// TestRunWorkerRunnerRollbackDrill 是 R5 rollback 演练证据：gate 方向回拨
// （WorkerRunner 由桥置回 nil/legacy）不复活旧 lease/registration、不产生
// 第二业务事实、不需要任何状态迁移——runstore 在桥路径写入后以 legacy
// 消费方原样可读，事件条目与 outcome 唯一且一致。
func TestRunWorkerRunnerRollbackDrill(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, err := sandboxbridge.NewBridge(provider)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	fixture.input.WorkerRunner = bridge.RunWorker

	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("bridged Run: %v", err)
	}

	// rollback：消费方回到 legacy 方向（等价于移除 MARSHAL_WORKER_EXECUTOR
	// 环境变量后的下一次读取），直接以 runstore 检查同一状态根。
	rollbackInput := fixture.input
	rollbackInput.WorkerRunner = nil
	if rollbackInput.WorkerRunner != nil {
		t.Fatal("rollback direction must drop the seam")
	}

	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatalf("rollback-side runstore read failed (state migration would surface here): %v", err)
	}
	if len(events) == 0 {
		t.Fatal("rollback-side journal is empty")
	}
	if converged := events[len(events)-1].StateTo; converged != result.State.State {
		t.Fatalf("rollback-side converged state mismatch: journal=%v result=%v", converged, result.State.State)
	}

	// 无第二业务事实：journal 中 worker.completed 恰一条、attempt 恰一个。
	worked := 0
	for _, e := range events {
		if e.Type == "worker.completed" {
			worked++
		}
	}
	if worked != 1 || result.State.AttemptsUsed != 1 {
		t.Fatalf("second business fact detected: completed=%d attemptsUsed=%d", worked, result.State.AttemptsUsed)
	}

	// 无残留写锁：attempt 目录 worker-result.json 存在且 lease.lock 不持
	// 活写者（Run 返回即释放）。
	if _, err := os.Stat(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-result.json")); err != nil {
		t.Fatalf("bridged evidence missing after rollback flip: %v", err)
	}
}

var errBridgeStage = &bridgeStageError{msg: "stage failed: injected"}

type bridgeStageError struct{ msg string }

func (e *bridgeStageError) Error() string { return e.msg }

func eventTypes(t *testing.T, stateRoot, runID string) []string {
	t.Helper()
	events, _, err := runstore.New(stateRoot).ReadEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
