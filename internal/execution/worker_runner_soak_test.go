package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/sandbox"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// TestPathSoakBridgedRuns 是 R6 路径层 accelerated soak：连续多轮完整
// execution.Run（bridged 默认路径，fake adapter + Fake provider），每轮
// 断言 invariant——journal sequence 严格单调、attemptId 全局唯一、终态后
// 无第二业务事实、runstore 重开 replay 等价、无失败残留。轮数刻意保守
// （CI 预算内）；wall-clock 24h soak 由同一 harness 放大轮数后在外部
// 运行器执行（见 internal/soak/doc.go 的边界说明）。
func TestPathSoakBridgedRuns(t *testing.T) {
	sealedMigrationSkip(t)
	const rounds = 5
	prevSeqMax := 1 // spec-accepted + inputs-frozen 起步事件序列长度下界
	seenAttempts := map[string]int{}

	for round := 0; round < rounds; round++ {
		fixture := newExecutionFixture(t, false)
		bridge, err := sandboxbridge.NewBridge(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
		if err != nil {
			t.Fatalf("round %d: NewBridge: %v", round, err)
		}
		fixture.input.WorkerRunner = bridge.RunWorker

		result, err := Run(context.Background(), fixture.input)
		if err != nil {
			t.Fatalf("round %d: bridged Run: %v", round, err)
		}
		if result.State.State != domain.StateVerifying {
			t.Errorf("round %d: expected VERIFYING, got %+v", round, result.State)
		}

		if _, dup := seenAttempts[result.AttemptID]; dup {
			t.Errorf("round %d: attemptId collision across rounds: %s", round, result.AttemptID)
		}
		seenAttempts[result.AttemptID] = round

		events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
		if err != nil {
			t.Fatalf("round %d: read events: %v", round, err)
		}
		seq := 0
		worked := 0
		for _, e := range events {
			seq++
			if e.Sequence != uint64(seq) {
				t.Errorf("round %d: journal sequence break at %d: %+v", round, seq, e)
			}
			if e.Type == "worker.completed" {
				worked++
			}
		}
		if seq < prevSeqMax {
			t.Errorf("round %d: journal too short: %d", round, seq)
		}
		if worked != 1 {
			t.Errorf("round %d: worker.completed count = %d, want exactly 1 (no second business fact)", round, worked)
		}

		// runstore 重开 replay 等价（重启对账面）。
		reopened, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
		if err != nil {
			t.Fatalf("round %d: reopen read: %v", round, err)
		}
		if len(reopened) != len(events) {
			t.Errorf("round %d: reopen replay divergence: %d vs %d", round, len(reopened), len(events))
		}

		// allocation record 必须已落盘且身份完整（孤儿对账锚点）。
		rec, ok, err := sandboxbridge.LoadAllocationRecord(filepath.Join(fixture.runDir, "attempts", result.AttemptID))
		if err != nil || !ok {
			t.Fatalf("round %d: allocation record missing after bridged run: ok=%v err=%v", round, ok, err)
		}
		if rec.AttemptID != result.AttemptID || rec.FencingToken == "" {
			t.Errorf("round %d: allocation record identity incomplete: %+v", round, rec)
		}
	}
}
