package review

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

type OutcomeData struct {
	TaskID              string
	RunID               string
	TerminalState       domain.State
	Verdict             string
	FinalReviewRound    uint
	FinalReviewDigest   string
	FinalEvidenceDigest string
	Summary             string
	FindingCount        uint
	GeneratedAt         time.Time
}

func renderOutcome(data OutcomeData) ([]byte, string, error) {
	outcome := domain.OutcomeBundle{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome, TaskID: data.TaskID, RunID: data.RunID, TerminalState: data.TerminalState, Verdict: data.Verdict, FinalReviewRound: data.FinalReviewRound, FinalReviewDigest: data.FinalReviewDigest, FinalEvidenceDigest: data.FinalEvidenceDigest, Summary: data.Summary, FindingCount: data.FindingCount, RetentionPolicy: "default", GeneratedAt: data.GeneratedAt.UTC()}
	jsonData, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return nil, "", err
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, "", err
	}
	if err := validator.Validate(domain.KindOutcome, jsonData); err != nil {
		return nil, "", fmt.Errorf("generated outcome violates contract: %w", err)
	}
	markdown := fmt.Sprintf("# Run 结果报告\n\n- 任务 ID：%s\n- Run ID：%s\n- 终态：%s\n- Review Verdict：%s\n- Review Round：%d\n- 生成时间：%s\n\n## 摘要\n\n%s\n\n## 证据绑定\n\n- Decision：%s\n- Evidence：%s\n\n## 保留策略\n\n默认保留；清理不得销毁本 Outcome。\n", data.TaskID, data.RunID, data.TerminalState, data.Verdict, data.FinalReviewRound, data.GeneratedAt.UTC().Format(time.RFC3339), data.Summary, data.FinalReviewDigest, data.FinalEvidenceDigest)
	return append(jsonData, '\n'), markdown, nil
}
