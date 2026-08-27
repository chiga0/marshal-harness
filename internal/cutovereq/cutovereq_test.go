package cutovereq

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func requireErrorIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("expected %v, got %v", want, err)
	}
}

func requireCutovereqPrefix(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if !strings.HasPrefix(err.Error(), "cutovereq: ") {
		t.Errorf("error %q missing cutovereq: prefix", err.Error())
	}
}

// baseOldStep 返回 old 路径形态的 step：无 commandId/fencing/allocation/
// DRC/registrationId/attestation，仅携带 agent capabilityDigest 与
// 一条 spec digest。
func baseOldStep() NormalizedStep {
	return NormalizedStep{
		TaskID:          "T-1",
		RunID:           "run-1",
		AttemptID:       "attempt-1",
		Sequence:        3,
		Command:         CommandRef{Kind: "attempt.start", Origin: "cli"},
		LeaseGeneration: 1,
		Agent:           RegistrationRef{CapabilityDigest: testDigest("cap")},
		Digests:         map[string]string{"spec": testDigest("spec")},
	}
}

// upgrade 把 old 形态 step 投影为 new 路径形态：全部授予升级字段
// materialize（null→非空），drcBinding 绑定本 step 的 attemptId 与
// lease.generation。Digests map 深拷贝，避免共享底层引用。
func upgrade(s NormalizedStep) NormalizedStep {
	digests := make(map[string]string, len(s.Digests))
	for k, v := range s.Digests {
		digests[k] = v
	}
	s.Digests = digests
	s.Command.CommandID = fmt.Sprintf("cmd:%d", s.Sequence)
	s.LeaseFencingToken = fmt.Sprintf("fence:%d", s.Sequence)
	s.AllocationID = fmt.Sprintf("alloc:%d", s.Sequence)
	s.SandboxProvider = "local"
	s.Agent.RegistrationID = "reg:agent:1"
	s.Agent.AttestationDigest = testDigest(fmt.Sprintf("attest:%d", s.Sequence))
	s.Sandbox.RegistrationID = "reg:sandbox:1"
	s.ResultCapability = ResultCapabilityRef{
		DrcID: testDigest(fmt.Sprintf("drc:%d", s.Sequence)),
		DrcBinding: &DrcBinding{
			AttemptID:    s.AttemptID,
			AllocationID: s.AllocationID,
			LeaseID:      "lease-1",
			Generation:   s.LeaseGeneration,
		},
	}
	return s
}

func hasDiff(diffs []TraceDiff, field string, class DiffClass) bool {
	for _, d := range diffs {
		if d.Field == field && d.Class == class {
			return true
		}
	}
	return false
}

// ── Validate：校验矩阵 ──────────────────────────────────────────────────────

func TestValidate_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(s *NormalizedStep)
		wantErr error
	}{
		{name: "valid old-path step"},
		{name: "valid upgraded step", mutate: func(s *NormalizedStep) {
			*s = upgrade(*s)
		}},
		{name: "bundled digests all accepted", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{
				"spec":         testDigest("spec"),
				"diff":         testDigest("diff"),
				"verification": testDigest("verification"),
				"decision":     testDigest("decision"),
			}
		}},
		{name: "empty taskId", mutate: func(s *NormalizedStep) {
			s.TaskID = ""
		}, wantErr: ErrMalformedStep},
		{name: "empty runId", mutate: func(s *NormalizedStep) {
			s.RunID = ""
		}, wantErr: ErrMalformedStep},
		{name: "empty attemptId", mutate: func(s *NormalizedStep) {
			s.AttemptID = ""
		}, wantErr: ErrMalformedStep},
		{name: "empty command kind", mutate: func(s *NormalizedStep) {
			s.Command.Kind = ""
		}, wantErr: ErrMalformedStep},
		{name: "zero sequence", mutate: func(s *NormalizedStep) {
			s.Sequence = 0
		}, wantErr: ErrMalformedStep},
		{name: "negative lease generation", mutate: func(s *NormalizedStep) {
			s.LeaseGeneration = -1
		}, wantErr: ErrMalformedStep},
		{name: "spec digest missing prefix", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{"spec": strings.TrimPrefix(testDigest("spec"), "sha256:")}
		}, wantErr: ErrMalformedStep},
		{name: "spec digest short hex", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{"spec": "sha256:abcd"}
		}, wantErr: ErrMalformedStep},
		{name: "spec digest uppercase hex", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{"spec": "sha256:" + strings.ToUpper(strings.TrimPrefix(testDigest("spec"), "sha256:"))}
		}, wantErr: ErrMalformedStep},
		{name: "empty digest value", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{"spec": ""}
		}, wantErr: ErrMalformedStep},
		{name: "unknown digest key", mutate: func(s *NormalizedStep) {
			s.Digests = map[string]string{"mystery": testDigest("x")}
		}, wantErr: ErrMalformedStep},
		{name: "bad agent capabilityDigest", mutate: func(s *NormalizedStep) {
			s.Agent.CapabilityDigest = "not-a-digest"
		}, wantErr: ErrMalformedStep},
		{name: "bad agent attestationDigest", mutate: func(s *NormalizedStep) {
			s.Agent.AttestationDigest = "sha256:zz" + strings.Repeat("0", 62)
		}, wantErr: ErrMalformedStep},
		{name: "bad sandbox capabilityDigest", mutate: func(s *NormalizedStep) {
			s.Sandbox.CapabilityDigest = "   "
		}, wantErr: ErrMalformedStep},
		{name: "bad sandbox attestationDigest", mutate: func(s *NormalizedStep) {
			s.Sandbox.AttestationDigest = "not-a-digest"
		}, wantErr: ErrMalformedStep},
		{name: "drcId present without binding", mutate: func(s *NormalizedStep) {
			s.ResultCapability = ResultCapabilityRef{DrcID: testDigest("drc")}
		}, wantErr: ErrMalformedStep},
		{name: "malformed drcId", mutate: func(s *NormalizedStep) {
			s.ResultCapability = ResultCapabilityRef{
				DrcID:      "drc:not-a-digest",
				DrcBinding: &DrcBinding{AttemptID: s.AttemptID, Generation: s.LeaseGeneration},
			}
		}, wantErr: ErrMalformedStep},
		{name: "binding without drcId", mutate: func(s *NormalizedStep) {
			s.ResultCapability = ResultCapabilityRef{
				DrcBinding: &DrcBinding{AttemptID: s.AttemptID, Generation: s.LeaseGeneration},
			}
		}, wantErr: ErrMalformedStep},
		{name: "binding negative generation", mutate: func(s *NormalizedStep) {
			s.ResultCapability = ResultCapabilityRef{
				DrcID:      testDigest("drc"),
				DrcBinding: &DrcBinding{AttemptID: s.AttemptID, Generation: -2},
			}
		}, wantErr: ErrMalformedStep},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseOldStep()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			err := s.Validate()
			requireErrorIs(t, err, tc.wantErr)
			requireCutovereqPrefix(t, err)
		})
	}
}

// ── golden chain helpers ────────────────────────────────────────────────────

// goldenKinds 对齐 golden trace 样本的 rework→accept 业务链（10 步）。
var goldenKinds = []string{
	"attempt.start", "attempt.result", "verify", "review",
	"attempt.start", "attempt.result", "verify", "review",
	"publish", "attempt.start",
}

// goldenOldChain 构造 10 步 old golden trace：rework 前 attempt-a、rework
// 后 attempt-b；digest 按事件类型分布。
func goldenOldChain() []NormalizedStep {
	steps := make([]NormalizedStep, 0, len(goldenKinds))
	for i, kind := range goldenKinds {
		seq := uint64(i + 1)
		attemptID := "attempt-a"
		if i >= 4 {
			attemptID = "attempt-b"
		}
		s := NormalizedStep{
			TaskID:          "M10-WIRE-01",
			RunID:           "run-m10-wire-r1",
			AttemptID:       attemptID,
			Sequence:        seq,
			Command:         CommandRef{Kind: kind, Origin: "cli"},
			LeaseGeneration: 1,
			Agent:           RegistrationRef{CapabilityDigest: testDigest("cap:qoder")},
			Digests:         map[string]string{},
		}
		switch kind {
		case "attempt.start":
			s.Digests["spec"] = testDigest("spec")
		case "attempt.result":
			s.Digests["diff"] = testDigest(fmt.Sprintf("diff:%s", attemptID))
		case "verify":
			s.Digests["verification"] = testDigest(fmt.Sprintf("verification:%d", seq))
		case "review":
			s.Digests["decision"] = testDigest(fmt.Sprintf("decision:%d", seq))
		}
		steps = append(steps, s)
	}
	return steps
}

// goldenNewChain 把 old golden trace 投影为带全部授予升级的 new 链。
func goldenNewChain(old []NormalizedStep) []NormalizedStep {
	steps := make([]NormalizedStep, 0, len(old))
	for _, s := range old {
		steps = append(steps, upgrade(s))
	}
	return steps
}

// grantedUpgradeFields 是每步预期出现的 authority-upgrade 字段集。
var grantedUpgradeFields = []string{
	"command.commandId",
	"lease.fencingToken",
	"allocation.allocationId",
	"allocation.sandboxProvider",
	"agentRegistration.registrationId",
	"agentRegistration.attestationDigest",
	"sandboxRegistration.registrationId",
	"resultCapability.drcId",
	"resultCapability.drcBinding",
}

// ── CompareAuthorityTrace ───────────────────────────────────────────────────

func TestCompareAuthorityTrace_GoldenChainEquivalentWithRecordedUpgrades(t *testing.T) {
	old := goldenOldChain()
	newChain := goldenNewChain(old)

	verdict, err := CompareAuthorityTrace(old, newChain)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Equivalent {
		for _, d := range verdict.Diffs {
			t.Logf("diff: seq %d %s %s %s", d.Sequence, d.Field, d.Class, d.Detail)
		}
		t.Fatalf("expected Equivalent, got %d diffs", len(verdict.Diffs))
	}
	wantDiffs := len(goldenKinds) * len(grantedUpgradeFields)
	if len(verdict.Diffs) != wantDiffs {
		t.Fatalf("expected %d upgrade diffs, got %d", wantDiffs, len(verdict.Diffs))
	}
	for _, d := range verdict.Diffs {
		if d.Class != ClassAuthorityUpgrade {
			t.Errorf("unexpected non-upgrade diff: %+v", d)
		}
		if d.Detail == "" {
			t.Errorf("upgrade diff without Detail: %+v", d)
		}
		if d.Sequence == 0 {
			t.Errorf("diff missing Sequence: %+v", d)
		}
	}
	for i := range goldenKinds {
		seq := uint64(i + 1)
		var perStep []TraceDiff
		for _, d := range verdict.Diffs {
			if d.Sequence == seq {
				perStep = append(perStep, d)
			}
		}
		for _, field := range grantedUpgradeFields {
			if !hasDiff(perStep, field, ClassAuthorityUpgrade) {
				t.Errorf("step %d missing authority-upgrade diff for %q", seq, field)
			}
		}
	}
}

func TestCompareAuthorityTrace_BusinessMismatchBlocks(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(old, newChain []NormalizedStep)
		wantField string
	}{
		{name: "attemptId flip", mutate: func(_, n []NormalizedStep) {
			n[2].AttemptID = "attempt-c"
		}, wantField: "attemptId"},
		{name: "runId flip", mutate: func(_, n []NormalizedStep) {
			n[2].RunID = "run-other"
		}, wantField: "runId"},
		{name: "taskId flip", mutate: func(_, n []NormalizedStep) {
			n[2].TaskID = "T-other"
		}, wantField: "taskId"},
		{name: "command kind change", mutate: func(_, n []NormalizedStep) {
			n[2].Command.Kind = "review"
		}, wantField: "command.kind"},
		{name: "spec digest changed", mutate: func(_, n []NormalizedStep) {
			n[0].Digests["spec"] = testDigest("spec:tampered")
		}, wantField: "digests.spec"},
		{name: "old-side digest dropped on new side", mutate: func(_, n []NormalizedStep) {
			delete(n[0].Digests, "spec")
		}, wantField: "digests.spec"},
		{name: "drcBinding generation mismatch", mutate: func(_, n []NormalizedStep) {
			n[2].ResultCapability.DrcBinding.Generation = 77
		}, wantField: "resultCapability.drcBinding"},
		{name: "drcBinding attemptId mismatch", mutate: func(_, n []NormalizedStep) {
			n[2].ResultCapability.DrcBinding.AttemptID = "attempt-foreign"
		}, wantField: "resultCapability.drcBinding"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := goldenOldChain()
			newChain := goldenNewChain(old)
			tc.mutate(old, newChain)
			verdict, err := CompareAuthorityTrace(old, newChain)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if verdict.Equivalent {
				t.Fatalf("expected blocking verdict, got Equivalent")
			}
			if !hasDiff(verdict.Diffs, tc.wantField, ClassBusinessMismatch) {
				t.Errorf("missing business-mismatch diff at %q; diffs: %+v", tc.wantField, verdict.Diffs)
			}
		})
	}
}

func TestCompareAuthorityTrace_UnexplainedDriftBlocks(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(o, n *NormalizedStep)
		wantField string
	}{
		{name: "new-side emptied allocationId", mutate: func(o, n *NormalizedStep) {
			o.AllocationID = "alloc:legacy"
			n.AllocationID = ""
		}, wantField: "allocation.allocationId"},
		{name: "changed fencing token", mutate: func(o, n *NormalizedStep) {
			o.LeaseFencingToken = "fence:1"
			n.LeaseFencingToken = "fence:2"
		}, wantField: "lease.fencingToken"},
		{name: "lease generation change is not a granted upgrade", mutate: func(o, n *NormalizedStep) {
			n.LeaseGeneration = 2
			n.ResultCapability.DrcBinding.Generation = 2 // 保持 new 侧自洽，隔离本字段
		}, wantField: "lease.generation"},
		{name: "changed commandId is not a granted upgrade", mutate: func(o, n *NormalizedStep) {
			o.Command.CommandID = "cmd:old"
			n.Command.CommandID = "cmd:new"
		}, wantField: "command.commandId"},
		{name: "origin change", mutate: func(_, n *NormalizedStep) {
			n.Command.Origin = "cli-transport-adapter"
		}, wantField: "command.origin"},
		{name: "agent capabilityDigest change", mutate: func(_, n *NormalizedStep) {
			n.Agent.CapabilityDigest = testDigest("cap:changed")
		}, wantField: "agentRegistration.capabilityDigest"},
		{name: "agent providerId is not in the upgrade set", mutate: func(_, n *NormalizedStep) {
			n.Agent.ProviderID = "agent:qoder"
		}, wantField: "agentRegistration.providerId"},
		{name: "sandboxProvider change after materialization", mutate: func(o, n *NormalizedStep) {
			o.SandboxProvider = "local"
			n.SandboxProvider = "docker"
		}, wantField: "allocation.sandboxProvider"},
		{name: "sandbox attestationDigest is not in the upgrade set", mutate: func(_, n *NormalizedStep) {
			n.Sandbox.AttestationDigest = testDigest("sbx-attest")
		}, wantField: "sandboxRegistration.attestationDigest"},
		{name: "new-only digest key", mutate: func(_, n *NormalizedStep) {
			n.Digests = map[string]string{"decision": testDigest("decision")}
		}, wantField: "digests.decision"},
		{name: "drcBinding emptied on new side", mutate: func(o, n *NormalizedStep) {
			o.ResultCapability = ResultCapabilityRef{
				DrcID:      testDigest("drc:old"),
				DrcBinding: &DrcBinding{AttemptID: o.AttemptID, Generation: o.LeaseGeneration},
			}
			n.ResultCapability = ResultCapabilityRef{}
		}, wantField: "resultCapability.drcBinding"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := baseOldStep()
			n := upgrade(o)
			tc.mutate(&o, &n)
			verdict, err := CompareAuthorityTrace([]NormalizedStep{o}, []NormalizedStep{n})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if verdict.Equivalent {
				t.Fatalf("expected blocking verdict, got Equivalent")
			}
			if !hasDiff(verdict.Diffs, tc.wantField, ClassUnexplainedDrift) {
				t.Errorf("missing unexplained-drift diff at %q; diffs: %+v", tc.wantField, verdict.Diffs)
			}
		})
	}
}

func TestCompareAuthorityTrace_Misaligned(t *testing.T) {
	old := goldenOldChain()

	t.Run("count mismatch", func(t *testing.T) {
		_, err := CompareAuthorityTrace(old, goldenNewChain(old)[:len(old)-1])
		requireErrorIs(t, err, ErrTraceMisaligned)
		requireCutovereqPrefix(t, err)
	})

	t.Run("sequence mismatch at same index", func(t *testing.T) {
		newChain := goldenNewChain(old)
		newChain[2].Sequence = 99
		_, err := CompareAuthorityTrace(old, newChain)
		requireErrorIs(t, err, ErrTraceMisaligned)
		requireCutovereqPrefix(t, err)
	})
}

func TestCompareAuthorityTrace_InvalidStepFailsClosed(t *testing.T) {
	old := goldenOldChain()
	newChain := goldenNewChain(old)

	t.Run("old side invalid", func(t *testing.T) {
		broken := append([]NormalizedStep(nil), old...)
		broken[0].TaskID = ""
		_, err := CompareAuthorityTrace(broken, newChain)
		requireErrorIs(t, err, ErrMalformedStep)
		requireCutovereqPrefix(t, err)
	})

	t.Run("new side invalid", func(t *testing.T) {
		broken := append([]NormalizedStep(nil), newChain...)
		broken[0].Digests = map[string]string{"mystery": testDigest("x")}
		_, err := CompareAuthorityTrace(old, broken)
		requireErrorIs(t, err, ErrMalformedStep)
		requireCutovereqPrefix(t, err)
	})
}

// ── CompareFakeDeterministic ────────────────────────────────────────────────

func TestCompareFakeDeterministic_Pass(t *testing.T) {
	t.Run("identical steps", func(t *testing.T) {
		if err := CompareFakeDeterministic(baseOldStep(), baseOldStep()); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
	t.Run("granted upgrades only", func(t *testing.T) {
		if err := CompareFakeDeterministic(baseOldStep(), upgrade(baseOldStep())); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}

func TestCompareFakeDeterministic_DriftFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(o, n *NormalizedStep)
	}{
		{name: "diff digest value changed", mutate: func(o, n *NormalizedStep) {
			n.Digests = map[string]string{"spec": testDigest("spec:drifted")}
		}},
		{name: "new-only digest key", mutate: func(o, n *NormalizedStep) {
			n.Digests["decision"] = testDigest("decision")
		}},
		{name: "old digest removed on new side", mutate: func(o, n *NormalizedStep) {
			delete(n.Digests, "spec")
		}},
		{name: "sequence differs", mutate: func(o, n *NormalizedStep) {
			n.Sequence = 9
		}},
		{name: "kind differs", mutate: func(o, n *NormalizedStep) {
			n.Command.Kind = "review"
		}},
		{name: "attemptId differs", mutate: func(o, n *NormalizedStep) {
			n.AttemptID = "attempt-2"
			n.ResultCapability.DrcBinding.AttemptID = "attempt-2" // 隔离业务字段
		}},
		{name: "drcBinding generation mismatched", mutate: func(o, n *NormalizedStep) {
			n.ResultCapability.DrcBinding.Generation = 42
		}},
		{name: "drcBinding attemptId mismatched", mutate: func(o, n *NormalizedStep) {
			n.ResultCapability.DrcBinding.AttemptID = "attempt-foreign"
		}},
		{name: "fencing token changed", mutate: func(o, n *NormalizedStep) {
			o.LeaseFencingToken = "fence:1"
			n.LeaseFencingToken = "fence:2"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := baseOldStep()
			n := upgrade(o)
			tc.mutate(&o, &n)
			err := CompareFakeDeterministic(o, n)
			requireErrorIs(t, err, ErrFakeDrift)
			requireCutovereqPrefix(t, err)
		})
	}
}

func TestCompareFakeDeterministic_InvalidStepPropagates(t *testing.T) {
	o := baseOldStep()
	n := upgrade(o)
	n.Sequence = 0
	err := CompareFakeDeterministic(o, n)
	requireErrorIs(t, err, ErrMalformedStep)
	requireCutovereqPrefix(t, err)
}

// ── CompareResource ─────────────────────────────────────────────────────────

func TestCompareResource_Matrix(t *testing.T) {
	baseOld := ResourceStat{AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 100, WallMillis: 100}

	cases := []struct {
		name    string
		old     ResourceStat
		new     ResourceStat
		tolBP   int
		wantErr error
	}{
		{name: "counts equal within tolerance", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 105, WallMillis: 110,
		}, tolBP: 1000},
		{name: "zero tolerance exact match", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 100, WallMillis: 100,
		}, tolBP: 0},
		{name: "memory at exact limit passes", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 110, WallMillis: 100,
		}, tolBP: 1000},
		{name: "attemptCount degrade blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 3, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 100, WallMillis: 100,
		}, tolBP: 1000, wantErr: ErrAuthorityRegression},
		{name: "gateRuns degrade blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 1, ReviewRounds: 2, PeakMemoryBytes: 100, WallMillis: 100,
		}, tolBP: 1000, wantErr: ErrAuthorityRegression},
		{name: "reviewRounds degrade blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 3, PeakMemoryBytes: 100, WallMillis: 100,
		}, tolBP: 1000, wantErr: ErrAuthorityRegression},
		{name: "memory over tolerance blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 111, WallMillis: 100,
		}, tolBP: 1000, wantErr: ErrResourceRegression},
		{name: "wall over tolerance blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 100, WallMillis: 111,
		}, tolBP: 1000, wantErr: ErrResourceRegression},
		{name: "tolerance below range", old: baseOld, new: baseOld, tolBP: -1, wantErr: ErrInvalidTolerance},
		{name: "tolerance above range", old: baseOld, new: baseOld, tolBP: 10001, wantErr: ErrInvalidTolerance},
		{name: "zero baseline both zero ok", old: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 0, WallMillis: 0,
		}, new: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 0, WallMillis: 0,
		}, tolBP: 500},
		{name: "zero baseline new memory positive regresses", old: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 0, WallMillis: 0,
		}, new: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 1, WallMillis: 0,
		}, tolBP: 500, wantErr: ErrResourceRegression},
		{name: "zero baseline new wall positive regresses", old: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 0, WallMillis: 0,
		}, new: ResourceStat{
			AttemptCount: 1, GateRuns: 1, ReviewRounds: 1, PeakMemoryBytes: 0, WallMillis: 42,
		}, tolBP: 500, wantErr: ErrResourceRegression},
		{name: "10000BP doubles exactly at limit", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 200, WallMillis: 200,
		}, tolBP: 10000},
		{name: "10000BP over double blocks", old: baseOld, new: ResourceStat{
			AttemptCount: 2, GateRuns: 2, ReviewRounds: 2, PeakMemoryBytes: 201, WallMillis: 100,
		}, tolBP: 10000, wantErr: ErrResourceRegression},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, err := CompareResource(tc.old, tc.new, tc.tolBP)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				if len(verdict.Regressions) != 0 {
					t.Errorf("expected no regressions, got %v", verdict.Regressions)
				}
				return
			}
			requireErrorIs(t, err, tc.wantErr)
			requireCutovereqPrefix(t, err)
			if tc.wantErr != ErrInvalidTolerance && len(verdict.Regressions) == 0 {
				t.Errorf("expected recorded regressions, got none")
			}
		})
	}
}

func TestCompareResource_VerdictFlags(t *testing.T) {
	old := ResourceStat{AttemptCount: 3, GateRuns: 3, ReviewRounds: 3, PeakMemoryBytes: 100, WallMillis: 100}
	verdict, err := CompareResource(old, ResourceStat{
		AttemptCount: 4, GateRuns: 3, ReviewRounds: 3, PeakMemoryBytes: 50, WallMillis: 100,
	}, 1000)
	requireErrorIs(t, err, ErrAuthorityRegression)
	if verdict.AttemptsOK {
		t.Errorf("AttemptsOK should be false")
	}
	if !verdict.GatesOK || !verdict.ReviewsOK || !verdict.MemoryOK || !verdict.WallOK {
		t.Errorf("non-degraded axes should be OK: %+v", verdict)
	}
	found := false
	for _, r := range verdict.Regressions {
		if strings.Contains(r, "attemptCount 3→4") {
			found = true
		}
	}
	if !found {
		t.Errorf("regression record missing degraded count: %v", verdict.Regressions)
	}
}

// ── 全部外部错误的 cutovereq: 前缀 ──────────────────────────────────────────

func TestErrorsCarryCutovereqPrefix(t *testing.T) {
	errs := []error{
		baseOldStep().Validate(),
	}
	bad := baseOldStep()
	bad.Sequence = 0
	errs = append(errs, bad.Validate())

	_, err := CompareAuthorityTrace(nil, []NormalizedStep{baseOldStep()})
	errs = append(errs, err)
	errs = append(errs, CompareFakeDeterministic(baseOldStep(), func() NormalizedStep {
		n := baseOldStep()
		n.TaskID = "T-2"
		return n
	}()))
	_, err = CompareResource(ResourceStat{}, ResourceStat{}, -1)
	errs = append(errs, err)

	for _, err := range errs {
		if err == nil {
			continue
		}
		if !strings.HasPrefix(err.Error(), "cutovereq: ") {
			t.Errorf("error %q missing cutovereq: prefix", err.Error())
		}
	}
}
