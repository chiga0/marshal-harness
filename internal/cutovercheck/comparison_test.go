package cutovercheck

import (
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/cutovereq"
)

// R5-D 核心证据：golden old trace 与其 new 投影经三分判据等价，
// 且每条 authority-upgrade 都入账为可审计 diff。
func TestGoldenOldNewEquivalent(t *testing.T) {
	oldTrace := GoldenOldTrace()
	newTrace := ProjectNewTrace(oldTrace)

	verdict, err := cutovereq.CompareAuthorityTrace(oldTrace, newTrace)
	if err != nil {
		t.Fatalf("CompareAuthorityTrace: %v", err)
	}
	if !verdict.Equivalent {
		t.Fatalf("golden old/new must be equivalent, diffs: %+v", verdict.Diffs)
	}
	if len(verdict.Diffs) == 0 {
		t.Errorf("authority upgrades must be recorded as explained diffs")
	}
	for _, d := range verdict.Diffs {
		if d.Class != cutovereq.DiffClass("authority-upgrade") {
			t.Errorf("unexpected diff class in equivalent verdict: %+v", d)
		}
	}
}

// 业务事实漂移阻断 cutover：把 new 侧一条 diff digest 换掉（深拷贝 digests
// map——投影与 old 侧共享 map 头，直接改写会污染两侧变成伪相等）。
func TestGoldenBusinessMismatchBlocks(t *testing.T) {
	oldTrace := GoldenOldTrace()
	newTrace := ProjectNewTrace(oldTrace)
	copied := make(map[string]string, len(newTrace[3].Digests))
	for k, v := range newTrace[3].Digests {
		copied[k] = v
	}
	copied["diff"] = digestOf("tampered-diff")
	newTrace[3].Digests = copied

	verdict, err := cutovereq.CompareAuthorityTrace(oldTrace, newTrace)
	if err != nil {
		t.Fatalf("CompareAuthorityTrace: %v", err)
	}
	if verdict.Equivalent {
		t.Errorf("business digest mismatch must block cutover")
	}
	found := false
	for _, d := range verdict.Diffs {
		if d.Class == cutovereq.DiffClass("business-mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected business-mismatch diff recorded, got %+v", verdict.Diffs)
	}
}

// 未解释语义漂移阻断：改变一个 upgrade 集与非不变量集之外的既有字段
// （agentRegistration.providerId 在 old 侧已有值）。
func TestGoldenUnexplainedDriftBlocks(t *testing.T) {
	oldTrace := GoldenOldTrace()
	newTrace := ProjectNewTrace(oldTrace)
	newTrace[5].Agent.ProviderID = "agent:other"

	verdict, err := cutovereq.CompareAuthorityTrace(oldTrace, newTrace)
	if err != nil {
		t.Fatalf("CompareAuthorityTrace: %v", err)
	}
	if verdict.Equivalent {
		t.Errorf("unexplained drift (changed provider identity) must block cutover")
	}
	found := false
	for _, d := range verdict.Diffs {
		if d.Class == cutovereq.DiffClass("unexplained-drift") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unexplained-drift diff recorded, got %+v", verdict.Diffs)
	}
}

// 步数不齐即 misaligned。
func TestGoldenMisaligned(t *testing.T) {
	oldTrace := GoldenOldTrace()
	newTrace := ProjectNewTrace(oldTrace)[:9]

	if _, err := cutovereq.CompareAuthorityTrace(oldTrace, newTrace); !errors.Is(err, cutovereq.ErrTraceMisaligned) {
		t.Errorf("expected ErrTraceMisaligned, got %v", err)
	}
}

// 真实 Agent 资源归一化不劣化：权威计数相等 + 统计面在容差内。
func TestGoldenResourceNonRegression(t *testing.T) {
	oldStat := cutovereq.ResourceStat{AttemptCount: 2, GateRuns: 3, ReviewRounds: 2, PeakMemoryBytes: 1 << 30, WallMillis: 120000}
	okStat := cutovereq.ResourceStat{AttemptCount: 2, GateRuns: 3, ReviewRounds: 2, PeakMemoryBytes: (1 << 30) + (1 << 28), WallMillis: 121000}

	verdict, err := cutovereq.CompareResource(oldStat, okStat, 5000)
	if err != nil {
		t.Fatalf("CompareResource: %v", err)
	}
	if !verdict.MemoryOK || !verdict.WallOK {
		t.Errorf("within-tolerance stats must pass: %+v", verdict)
	}

	regressed := okStat
	regressed.AttemptCount = 3
	if _, err := cutovereq.CompareResource(oldStat, regressed, 5000); !errors.Is(err, cutovereq.ErrAuthorityRegression) {
		t.Errorf("authority count regression must fail closed, got %v", err)
	}
}
