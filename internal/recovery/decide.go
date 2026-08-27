package recovery

import (
	"fmt"
	"strings"
)

// Decide 是单一恢复模型的唯一判定入口。纯函数：同值输入永远得到同值
// Decision 与 Explanation。决策表按下列优先级取第一份匹配（即故障矩阵
// 每类的唯一幂等结论）：
//
//  1. 账本上 Attempt 已终态 → resume（按既有 Outcome 继续下游，不得重启）；
//  2. partial artifact → fence + new Attempt（产物不可证明完整）；
//  3. 任一侧 binding 失效 → fence + new Attempt（旧组合不可继续，且无法
//     证明在途静止）；
//  4. current lease 失效（expired/revoked/replaced）→ new Attempt；仅当
//     Inspect 确认为 never-received/terminal 时免 fence（在途可证静止）；
//  5. duplicate delivery（payload digest 与已接纳 command 相同）→ resume：
//     幂等消费，绝不产生第二效果；
//  6. terminal-success（lost response）→ resume：结果由 Inspect 重建，
//     不重放执行；
//  7. terminal-failure → authority-observed infra 分类时 new Attempt +
//     预算豁免（无需 fence）；否则 resume：失败按 Outcome 消费（rework
//     属另一协议层，不是恢复）；
//  8. never-received → new Attempt 且免 fence（执行侧可证未启动）；
//  9. executing 且 binding/lease 全绿 → resume；
//  10. unknown/unreachable（process death、provider restart 未重验、
//     network partition）→ fence + new Attempt：不能证明安全。
//
// 横切冻结规则：
//   - new Attempt 的 RequiresFence = 观察不能证明在途静止（never-received
//     与两类 terminal 免 fence）；
//   - RequiresReconcile = new Attempt 且 command 声明副作用且观察为
//     unknown/unreachable（ambiguous side effect：重复 Publication 的唯一
//     防线是幂等键对账，不是猜测）；
//   - BudgetExempt 只随 failureclass 的 authority-observed 放宽标志，
//     且仅在 new Attempt 时生效；
//   - stale result 只能在 ingress 被隔离并在 Explanation 记为冲突，永远
//     不能驱动 new Attempt。
func Decide(in RecoveryInput) (Decision, Explanation, error) {
	if err := in.validate(); err != nil {
		return Decision{}, Explanation{}, err
	}

	ex := Explanation{
		AttemptID:  in.Ledger.AttemptID,
		LeaseState: in.Ledger.Lease,
		Generation: in.Ledger.Generation,
		Bindings:   in.Bindings,
	}
	ex.Timeline = append(ex.Timeline,
		fmt.Sprintf("ledger: attempt=%s lease=%s generation=%d pending=%q terminal=%v",
			in.Ledger.AttemptID, in.Ledger.Lease, in.Ledger.Generation, in.Ledger.PendingCommandID, in.Ledger.AttemptTerminal),
		fmt.Sprintf("inspect: observation=%s", in.Observation),
		fmt.Sprintf("bindings: agent=%v sandbox=%v", in.Bindings.AgentOK, in.Bindings.SandboxOK),
	)
	if in.StaleResultPresented {
		ex.Conflicts = append(ex.Conflicts, "stale-result-quarantined")
	}
	if in.DuplicateOfAdmitted {
		ex.Conflicts = append(ex.Conflicts, "duplicate-delivery-idempotent")
	}
	if in.Observation == ObservationUnreachable {
		ex.Conflicts = append(ex.Conflicts, "provider-unreachable")
	}

	var d Decision
	switch {
	case in.Ledger.AttemptTerminal:
		d = Decision{Action: ActionResume, Rationale: RationaleAttemptAlreadyFinal}

	case in.PartialArtifact:
		d = Decision{Action: ActionNewAttempt, RequiresFence: true, Rationale: RationalePartialArtifact}

	case !in.Bindings.ok():
		d = Decision{Action: ActionNewAttempt, RequiresFence: true, Rationale: RationaleBindingLost}

	case in.Ledger.Lease != LeaseActive:
		d = Decision{Action: ActionNewAttempt, Rationale: RationaleLeaseDead}
		d.RequiresFence = !quiesced(in.Observation)
		d.RequiresReconcile = in.Ledger.SideEffectDeclared && ambiguous(in.Observation)
		if d.RequiresReconcile {
			d.Rationale = RationaleAmbiguousSideEffect
		}

	case in.DuplicateOfAdmitted:
		d = Decision{Action: ActionResume, Rationale: RationaleDuplicateDelivery}

	case in.Observation == ObservationTerminalSuccess:
		d = Decision{Action: ActionResume, Rationale: RationaleLostResponseRebuild}

	case in.Observation == ObservationTerminalFailure:
		if in.Failure.MayRelaxBudget {
			// authority-observed infra failure：新 Attempt 且预算豁免；
			// 已确认终态，在途可证静止，免 fence。
			d = Decision{Action: ActionNewAttempt, Rationale: RationaleTerminalFailure, BudgetExempt: true}
		} else {
			d = Decision{Action: ActionResume, Rationale: RationaleTerminalFailure}
		}

	case in.Observation == ObservationNeverReceived:
		d = Decision{Action: ActionNewAttempt, Rationale: RationaleNeverReceived}

	case in.Observation == ObservationExecuting && in.Ledger.Lease == LeaseActive && in.Bindings.ok():
		d = Decision{Action: ActionResume, Rationale: RationaleExecutingResume}

	default:
		// unknown / unreachable / executing 但条件不全：不能证明安全。
		d = Decision{Action: ActionNewAttempt, RequiresFence: true, Rationale: RationaleUnsafeToProve}
		d.RequiresReconcile = in.Ledger.SideEffectDeclared && ambiguous(in.Observation)
		if d.RequiresReconcile {
			d.Rationale = RationaleAmbiguousSideEffect
		}
	}

	ex.Decision = d
	ex.NextAction = nextAction(d, in)
	if d.BudgetExempt {
		ex.BudgetNote = "authority-observed infra：新 Attempt 不消耗 semantic 预算，豁免 semantic rework"
	} else if in.Failure.MayExemptSemanticRework && d.Action == ActionResume {
		ex.BudgetNote = ""
	}
	ex.Timeline = append(ex.Timeline,
		fmt.Sprintf("decision: action=%s requiresFence=%v requiresReconcile=%v rationale=%s",
			d.Action, d.RequiresFence, d.RequiresReconcile, d.Rationale),
	)
	return d, ex, nil
}

// quiesced 报告 Inspect 是否证明了在途可证静止（免 fence 的唯一依据）。
func quiesced(k ObservationKind) bool {
	switch k {
	case ObservationNeverReceived, ObservationTerminalSuccess, ObservationTerminalFailure:
		return true
	default:
		return false
	}
}

// ambiguous 报告观察是否无法区分副作用是否已发生。
func ambiguous(k ObservationKind) bool {
	return k == ObservationUnknown || k == ObservationUnreachable
}

func nextAction(d Decision, in RecoveryInput) string {
	var b strings.Builder
	switch d.Action {
	case ActionResume:
		if d.Rationale == RationaleAttemptAlreadyFinal {
			b.WriteString("按账本既有 Outcome 继续下游消费；不得重启 Attempt")
		} else if d.Rationale == RationaleLostResponseRebuild {
			b.WriteString("由 Inspect 重建丢失的结果并继续流水线（不重放执行）")
		} else if d.Rationale == RationaleTerminalFailure {
			b.WriteString("按 terminal-failure Outcome 消费（semantic rework 走 rework 协议层，非恢复）")
		} else {
			b.WriteString("恢复既有 Attempt 的执行观察（幂等，不产生第二效果）")
		}
	case ActionNewAttempt:
		if d.RequiresFence {
			fmt.Fprintf(&b, "先 fence：cancel + generation bump（当前 generation=%d）", in.Ledger.Generation)
		}
		if d.RequiresReconcile {
			if b.Len() > 0 {
				b.WriteString("；")
			}
			b.WriteString("先做幂等键对账（ambiguous side effect，杜绝重复 Publication）")
		}
		if b.Len() > 0 {
			b.WriteString("；")
		}
		b.WriteString("创建新 Attempt")
		if d.BudgetExempt {
			b.WriteString("（预算豁免）")
		}
	}
	return b.String()
}
