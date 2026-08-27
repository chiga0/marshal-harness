package soak

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/chiga0/marshal-harness/internal/effectsink"
	"github.com/chiga0/marshal-harness/internal/recovery"
)

// Stats 是一次 InvariantSoak 的迭代统计。
type Stats struct {
	Iterations  int
	ResumeCount int64
	FenceCount  int64
	EffectCount int64
	Rejects     int64
}

// seedRand 是 SplitMix64 确定性伪随机源：同种子永远产生同序列（可重放）。
type seedRand struct{ state uint64 }

func newSeedRand(seed []byte) *seedRand {
	var s uint64 = 0x9E3779B97F4A7C15
	for _, b := range seed {
		s = s*0x100000001B3 + uint64(b)
	}
	return &seedRand{state: s}
}

func (r *seedRand) uint64() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *seedRand) pick(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.uint64() % uint64(n))
}

// InvariantSoak 执行 N 轮原语级 soak：每轮生成一个确定性伪随机故障
// 场景，经 recovery.Decide 决策并断言 invariant；再对同一身份元组执行
// effectsink 复合门禁（先 admit 后重放/失效），断言无第二效果。
//
// 任何 invariant 违例立即返回带迭代号的错误（fail fast，可重放：同一种子
// 永远重现同一场景序列）。
func InvariantSoak(iterations int, seed []byte, sinkLedger *effectsink.EffectLedger, clock func(int) time.Time) (Stats, error) {
	if iterations < 1 {
		return Stats{}, errors.New("soak: iterations must be positive")
	}
	if sinkLedger == nil {
		return Stats{}, errors.New("soak: EffectLedger must not be nil")
	}
	rng := newSeedRand(seed)
	stats := Stats{Iterations: iterations}

	observations := []recovery.ObservationKind{
		recovery.ObservationExecuting,
		recovery.ObservationTerminalSuccess,
		recovery.ObservationTerminalFailure,
		recovery.ObservationNeverReceived,
		recovery.ObservationUnknown,
		recovery.ObservationUnreachable,
	}
	leases := []recovery.LeaseState{
		recovery.LeaseActive,
		recovery.LeaseExpired,
		recovery.LeaseRevoked,
		recovery.LeaseReplaced,
	}

	for i := 0; i < iterations; i++ {
		generation := int64(1 + rng.pick(7))
		in := recovery.RecoveryInput{
			Ledger: recovery.LedgerView{
				AttemptID:        "attempt-soak",
				PendingCommandID: "cmd-soak",
				CommandDigest:    digestOf("cmd", i),
				Lease:            leases[rng.pick(len(leases))],
				Generation:       generation,
				AttemptTerminal:  rng.pick(11) == 0,
			},
			Observation: observations[rng.pick(len(observations))],
			Bindings: recovery.BindingView{
				AgentOK:   rng.pick(9) != 0,
				SandboxOK: rng.pick(9) != 0,
			},
			Failure: recovery.FailureClassView{
				MayRelaxBudget:          rng.pick(5) == 0,
				MayExemptSemanticRework: rng.pick(5) == 0,
			},
			DuplicateOfAdmitted:  rng.pick(4) == 0,
			StaleResultPresented: rng.pick(4) == 0,
			PartialArtifact:      rng.pick(9) == 0,
		}
		in.Ledger.SideEffectDeclared = rng.pick(2) == 0

		d1, ex1, err1 := recovery.Decide(in)
		d2, ex2, err2 := recovery.Decide(in)

		// invariant 1：决策幂等（同值输入同值输出）。
		if (err1 == nil) != (err2 == nil) || d1 != d2 {
			return stats, fmt.Errorf("soak: iteration %d: non-idempotent decision: %+v vs %+v (in=%+v)", i, d1, d2, in)
		}
		// invariant 2：渲染幂等。
		if err1 == nil && recovery.Render(ex1) != recovery.Render(ex2) {
			return stats, fmt.Errorf("soak: iteration %d: non-idempotent render", i)
		}
		if err1 != nil {
			// malformed 输入是合法 fail-closed 路径：生成器只产生合法输入，
			// 此行不可达；若到达即生成器缺陷。
			return stats, fmt.Errorf("soak: iteration %d: unexpected malformed input error: %v", i, err1)
		}

		// invariant 3：安全底线——不能证明安全时必须 fence + new Attempt。
		if unsafe(in) && (d1.Action != recovery.ActionNewAttempt || !d1.RequiresFence) {
			return stats, fmt.Errorf("soak: iteration %d: unsafe input did not fence+new-attempt: %+v", i, d1)
		}
		// invariant 4：ambiguous side effect 必须 reconcile。
		if in.Ledger.SideEffectDeclared && ambiguousObs(in.Observation) && d1.Action == recovery.ActionNewAttempt && !d1.RequiresReconcile {
			return stats, fmt.Errorf("soak: iteration %d: ambiguous side effect without reconcile: %+v", i, d1)
		}
		// invariant 5：budget 豁免只能出现在 authority-observed infra 分类输入。
		if d1.BudgetExempt && !in.Failure.MayRelaxBudget {
			return stats, fmt.Errorf("soak: iteration %d: budget exemption without authority infra input: %+v", i, d1)
		}

		if d1.Action == recovery.ActionResume {
			atomic.AddInt64(&stats.ResumeCount, 1)
		} else if d1.RequiresFence {
			atomic.AddInt64(&stats.FenceCount, 1)
		}

		// effectsink 复合门禁：同一意图先 admit 再重放（幂等），再在授权
		// 撤销后重试（拒绝且不执行）——无第二效果的直接证据。
		intent, ierr := effectsink.NewEffectIntent(
			fmt.Sprintf("intent-%d", i), effectsink.SinkKindSCMMutation,
			"target-main", fmt.Sprintf("idem-%d", i),
			generation, digestOf("fencing", generation),
			digestOf("authorization", i), digestOf("target", i),
		)
		if ierr != nil {
			return stats, fmt.Errorf("soak: iteration %d: intent construction: %w", i, ierr)
		}
		view := effectsink.CurrentView{
			CurrentGeneration:          generation,
			CurrentFencingToken:        intent.FencingToken,
			AuthorizationRevoked:       false,
			CurrentAuthorizationDigest: intent.AuthorizationDigest,
			CurrentTargetDigest:        intent.TargetDigest,
		}
		v1, verr := effectsink.ExecuteIfAdmitted(sinkLedger, intent, view)
		if verr != nil || v1 == nil || !v1.OK {
			return stats, fmt.Errorf("soak: iteration %d: first effect admission failed: verdict=%+v err=%v", i, v1, verr)
		}
		v2, verr := effectsink.ExecuteIfAdmitted(sinkLedger, intent, view)
		if verr != nil || v2 == nil || !v2.OK || !v2.AlreadyExecuted {
			return stats, fmt.Errorf("soak: iteration %d: replay must be idempotent no-op: verdict=%+v err=%v", i, v2, verr)
		}
		revoked := view
		revoked.AuthorizationRevoked = true
		v3, verr := effectsink.ExecuteIfAdmitted(sinkLedger, intent, revoked)
		if verr != nil || v3 == nil || v3.OK || v3.Reason != effectsink.RejectionReasonAuthorizationRevoked {
			return stats, fmt.Errorf("soak: iteration %d: post-revoke attempt must be rejected: verdict=%+v err=%v", i, v3, verr)
		}
		atomic.AddInt64(&stats.EffectCount, 1)

		if clock != nil {
			_ = clock(i)
		}
	}
	return stats, nil
}

// unsafe 与 recovery 包的 frozen 决策表对齐：这些情况必须 fence+new-attempt。
// 观察歧义（unknown/unreachable）只在 command 声明了外部副作用时才构成
// 不安全——无声明副作用时重复投递幂等 resume 是结构性安全的结论
// （iteration 148：dedupe 消耗账本事实，没有可复查的外部效果）。
func unsafe(in recovery.RecoveryInput) bool {
	if in.Ledger.AttemptTerminal {
		return false
	}
	if in.PartialArtifact {
		return true
	}
	if !in.Bindings.AgentOK || !in.Bindings.SandboxOK {
		return true
	}
	if in.Ledger.Lease != recovery.LeaseActive && !quiesced(in.Observation) {
		return true
	}
	return in.Ledger.SideEffectDeclared && ambiguousObs(in.Observation)
}

func quiesced(k recovery.ObservationKind) bool {
	switch k {
	case recovery.ObservationNeverReceived, recovery.ObservationTerminalSuccess, recovery.ObservationTerminalFailure:
		return true
	default:
		return false
	}
}

func ambiguousObs(k recovery.ObservationKind) bool {
	return k == recovery.ObservationUnknown || k == recovery.ObservationUnreachable
}

func digestOf(parts ...any) string {
	sum := fmt.Sprintf("%v", parts)
	r := newSeedRand([]byte(sum))
	return fmt.Sprintf("sha256:%016x%016x%016x%016x", r.uint64(), r.uint64(), r.uint64(), r.uint64())
}
