package review

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const PromptVersion = "marshal-worker/v1"
const PromptSizeCap = 128 << 10

type WorkerPromptInput struct {
	Task             domain.TaskSpec
	TaskID           string
	RunID            string
	SpecDigest       string
	BaseSHA          string
	ReviewRound      uint
	PreviousFindings []domain.PreviousFinding
	AttemptsUsed     uint
}

func RenderWorkerPrompt(input WorkerPromptInput) string {
	const footer = "\n只在受管 worktree 中实现。Worker 声明不构成验证证据；不得 push、创建 PR 或修改 Marshal 状态。\n"
	contentCap := PromptSizeCap - len(footer)
	var builder strings.Builder
	write := func(value string) {
		remaining := contentCap - builder.Len()
		if remaining <= 0 {
			return
		}
		if len(value) > remaining {
			value = value[:remaining]
			for !utf8.ValidString(value) {
				value = value[:len(value)-1]
			}
		}
		builder.WriteString(value)
	}
	write("# Marshal Worker Request\n\n")
	write("Prompt-Version: " + PromptVersion + "\n")
	write("Task-ID: " + input.TaskID + "\n")
	write("Run-ID: " + input.RunID + "\n")
	write("Spec-Digest: " + input.SpecDigest + "\n")
	write("Base-SHA: " + input.BaseSHA + "\n")
	write(fmt.Sprintf("Review-Round: %d\n\n", input.ReviewRound))
	write("## 冻结目标\n\n" + input.Task.Work.Objective + "\n\n")
	write("## 允许范围\n\n")
	for _, path := range input.Task.Scope.AllowPaths {
		write("- " + path + "\n")
	}
	if len(input.Task.Scope.DenyPaths) > 0 {
		write("\n## 禁止范围\n\n")
		for _, path := range input.Task.Scope.DenyPaths {
			write("- " + path + "\n")
		}
	}
	if len(input.Task.Work.Constraints) > 0 {
		write("\n## 约束\n\n")
		for _, constraint := range input.Task.Work.Constraints {
			write("- " + constraint + "\n")
		}
	}
	if len(input.Task.Work.NonGoals) > 0 {
		write("\n## 非目标\n\n")
		for _, nonGoal := range input.Task.Work.NonGoals {
			write("- " + nonGoal + "\n")
		}
	}
	write("\n## 剩余预算\n\n")
	remainingAttempts := input.Task.Budgets.MaxAttempts - int(input.AttemptsUsed)
	if remainingAttempts < 0 {
		remainingAttempts = 0
	}
	write(fmt.Sprintf("- Worker Attempt：%d\n", remainingAttempts))
	remainingRework := input.Task.Budgets.MaxReworkRounds - int(input.ReviewRound-1)
	if remainingRework < 0 {
		remainingRework = 0
	}
	write(fmt.Sprintf("- Rework Round：%d\n", remainingRework))
	if len(input.PreviousFindings) > 0 {
		write("\n## 未关闭阻塞问题\n\n")
		for _, finding := range input.PreviousFindings {
			write(fmt.Sprintf("### %s [%s] %s\n\n%s\n", finding.ID, finding.Severity, finding.Title, finding.Description))
			if finding.RequiredOutcome != "" {
				write("要求结果：" + finding.RequiredOutcome + "\n")
			}
		}
	}
	builder.WriteString(footer)
	return builder.String()
}
