package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/explain"
	"github.com/chiga0/marshal-harness/internal/recovery"
	"github.com/chiga0/marshal-harness/internal/repository"
)

// recoverTakeoverAdmission 是 --recover-dead-driver 逃生舱的单一恢复模型
// 门禁（ADR 0053 决策 5）：staleness 约零——owner 死亡已由耐用 lease 记录
// 独立证明之后才调用，失效观测立即成立。仅当 Decision 为 new-attempt 且
// 不需幂等键对账时允许即时接管；其余决策（对账/binding 损伤/装配失败）
// 一概 fail closed，并按 `marshal explain run` 指引人工下一步。
func recoverTakeoverAdmission(stateRoot, runID string, now time.Time) error {
	x, err := explain.AssembleWithStaleness(stateRoot, runID, now, time.Nanosecond)
	if x == nil {
		return fmt.Errorf("无法装配恢复事实：%v", err)
	}
	if err != nil {
		return fmt.Errorf("恢复决策不可用（%v）；按 `marshal explain run %s` 人工判定", err, runID)
	}
	if x.Decision.Action != recovery.ActionNewAttempt || x.Decision.RequiresReconcile {
		return fmt.Errorf("恢复决策禁止立即接管（action=%s rationale=%s）；按 `marshal explain run %s` 完成幂等键对账后重试", x.Decision.Action, x.Decision.Rationale, runID)
	}
	return nil
}

// runExplain 是 `marshal explain run RUN_ID [--json]`：从权威账本装配单一
// 恢复模型的 decision/explanation 并渲染。只读，不改变任何权威状态。
// `explain run` 与 ADR 0045 决策 2 / ADR 0052 §6 的 R4 要求一致；Darwin
// dogfood profile 的命令分类以此命令当前不在生命周期类内为由拒绝
// （自证域之外，fail closed）。
func runExplain(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "run" {
		fmt.Fprintln(stderr, "用法：marshal explain run RUN_ID [--json]")
		return ExitUsage
	}
	flags := flag.NewFlagSet("explain run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "用法：marshal explain run RUN_ID [--json]")
		return ExitUsage
	}
	runID := strings.TrimSpace(flags.Arg(0))
	if runID == "" {
		fmt.Fprintln(stderr, "用法：marshal explain run RUN_ID [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "explain 失败：%v\n", err)
		return ExitFailure
	}
	experience, assembleErr := explain.Assemble(location.StateRoot, runID, time.Now().UTC())
	if experience == nil {
		fmt.Fprintf(stderr, "explain 失败：%v\n", assembleErr)
		return ExitFailure
	}
	if assembleErr != nil {
		fmt.Fprintf(stderr, "explain 警告：%v\n", assembleErr)
	}
	if *jsonOutput {
		if err := writeJSON(stdout, experience); err != nil {
			fmt.Fprintf(stderr, "explain 输出失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintln(stdout, explain.Render(experience))
	return ExitOK
}
