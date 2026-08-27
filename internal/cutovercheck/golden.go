package cutovercheck

import (
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/cutovereq"
)

// golden 常量来自 R0 冻结 golden trace（run-m10-wire-r1，docs/research/
// i186-r0-golden-trace.md §2）：两次 attempt（rework→accept）全链。
// NormalizedStep 要求每步携带 governing attemptId：规划步归属首 attempt，
// review/verify 步归属其裁决对象 attempt（与真实事件出处一致）。
const (
	goldenTaskID    = "M10-WIRE-01"
	goldenRunID     = "run-m10-wire-r1"
	goldenAttempt1  = "attempt:9a9fa650"
	goldenAttempt2  = "attempt:9e09229f"
	goldenAdapterID = "qoder"
	goldenBaseSHA   = "5895122fa8686fe56c5345ff67af816d73964820"
)

// digestOf 派生形态合法的确定性 digest（fixture 双侧一致即可；真实 digest
// 在前缀索引下不可全量还原，见 doc.go）。
func digestOf(parts ...string) string {
	sum := ""
	for _, p := range parts {
		sum += p + "|"
	}
	return canonical.DigestBytes([]byte(sum))
}

func goldenDigests(spec, diff, verification, decision string) map[string]string {
	out := map[string]string{}
	if spec != "" {
		out["spec"] = digestOf("spec", spec)
	}
	if diff != "" {
		out["diff"] = digestOf("diff", diff)
	}
	if verification != "" {
		out["verification"] = digestOf("verification", verification)
	}
	if decision != "" {
		out["decision"] = digestOf("decision", decision)
	}
	return out
}

type stepSeed struct {
	sequence  uint64
	kind      string
	attemptID string
	digests   map[string]string
}

func goldenSeeds() []stepSeed {
	return []stepSeed{
		{1, "plan.spec-accepted", goldenAttempt1, goldenDigests("2492e622", "", "", "")},
		{2, "plan.inputs-frozen", goldenAttempt1, goldenDigests(goldenBaseSHA, "", "", "")},
		{3, "attempt.start", goldenAttempt1, nil},
		{4, "attempt.result", goldenAttempt1, goldenDigests("", "96fb83ed", "", "")},
		{5, "verify", goldenAttempt1, goldenDigests("", "", "b3adc3f3", "")},
		{6, "review.rework", goldenAttempt1, goldenDigests("", "", "", "3a04d10e")},
		{7, "attempt.start", goldenAttempt2, nil},
		{8, "attempt.result", goldenAttempt2, goldenDigests("", "b0a003b1", "", "")},
		{9, "verify", goldenAttempt2, goldenDigests("", "", "de3b7269", "")},
		{10, "review.accept", goldenAttempt2, goldenDigests("", "", "", "5b3afa9c")},
	}
}

// GoldenOldTrace 构造 old 侧 normalized trace（§3 schema 固定空值：
// commandId=null、allocation/sandboxProvider=null、drcId=null、无
// fencingToken；generation=1——新旧两侧 generation 保持相等，fencing/
// allocation 等才是允许的 authority-upgrade）。
func GoldenOldTrace() []cutovereq.NormalizedStep {
	seeds := goldenSeeds()
	out := make([]cutovereq.NormalizedStep, len(seeds))
	for i, s := range seeds {
		out[i] = cutovereq.NormalizedStep{
			TaskID:          goldenTaskID,
			RunID:           goldenRunID,
			AttemptID:       s.attemptID,
			Sequence:        s.sequence,
			Command:         cutovereq.CommandRef{Kind: s.kind, Origin: "cli", CommandID: ""},
			LeaseGeneration: 1,
			Agent: cutovereq.RegistrationRef{
				ProviderID:       "agent:" + goldenAdapterID,
				RegistrationID:   "",
				CapabilityDigest: digestOf("capability", goldenAdapterID),
			},
			Digests: s.digests,
		}
	}
	return out
}

// ProjectNewTrace 把 old 侧投影为 new 侧（§4 schema）：业务事实原样承袭，
// authority-upgrade 字段按规则非空化（commandId/fencingToken/
// allocationId/sandboxProvider/drcId+drcBinding/双 registrationId）。
func ProjectNewTrace(old []cutovereq.NormalizedStep) []cutovereq.NormalizedStep {
	out := make([]cutovereq.NormalizedStep, len(old))
	for i, s := range old {
		n := s
		stepTag := string(rune('a' + i))
		n.Command.CommandID = digestOf("command", s.Command.Kind, goldenRunID, stepTag)
		// command.origin 属业务事实，投影不得改变（ADR 0051 决策1：
		// upgrade 集之外的字段变化一律 unexplained-drift）。
		n.LeaseFencingToken = digestOf("fencing", goldenRunID, stepTag)
		n.SandboxProvider = "local"
		n.AllocationID = digestOf("allocation", goldenRunID, s.AttemptID, stepTag)
		n.Agent.RegistrationID = "registration:" + goldenAdapterID
		n.Agent.AttestationDigest = digestOf("attestation", goldenAdapterID)
		// sandboxRegistration 只有 RegistrationID 在冻结 upgrade 集内；
		// providerId/capabilityDigest 变化同样判 drift。
		n.Sandbox = cutovereq.RegistrationRef{
			RegistrationID: "registration:sandbox-local",
		}
		drcID := digestOf("drc", goldenRunID, stepTag)
		n.ResultCapability = cutovereq.ResultCapabilityRef{
			DrcID: drcID,
			DrcBinding: &cutovereq.DrcBinding{
				AttemptID:    s.AttemptID,
				AllocationID: n.AllocationID,
				LeaseID:      digestOf("lease", goldenRunID),
				Generation:   s.LeaseGeneration,
			},
		}
		out[i] = n
	}
	return out
}
