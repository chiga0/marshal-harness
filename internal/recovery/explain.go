package recovery

import (
	"fmt"
	"strings"
)

// Render 是 Explanation 的唯一文本出口（`marshal explain run` 的渲染
// 模型）。输出确定性：同一 Explanation 永远渲染同一文本，足以让非作者
// 复盘——权威时间线、当前 lease/bindings、外部冲突、恢复决策与下一
// 动作全部呈现。
func Render(ex Explanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "recovery explain: attempt=%s\n", ex.AttemptID)

	b.WriteString("authoritative timeline:\n")
	for _, line := range ex.Timeline {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	fmt.Fprintf(&b, "current: lease=%s generation=%d bindings(agent=%v sandbox=%v)\n",
		ex.LeaseState, ex.Generation, ex.Bindings.AgentOK, ex.Bindings.SandboxOK)

	if len(ex.Conflicts) == 0 {
		b.WriteString("external conflicts: none\n")
	} else {
		b.WriteString("external conflicts:\n")
		for _, c := range ex.Conflicts {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}

	d := ex.Decision
	fmt.Fprintf(&b, "decision: action=%s requiresFence=%v requiresReconcile=%v budgetExempt=%v rationale=%s\n",
		d.Action, d.RequiresFence, d.RequiresReconcile, d.BudgetExempt, d.Rationale)

	if ex.BudgetNote != "" {
		fmt.Fprintf(&b, "budget: %s\n", ex.BudgetNote)
	}

	fmt.Fprintf(&b, "next action: %s\n", ex.NextAction)
	return b.String()
}
