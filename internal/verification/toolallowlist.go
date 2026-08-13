package verification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// toolAllowlistGateID is the fixed identifier of the tool-allowlist gate.
const toolAllowlistGateID = "tool-allowlist"

// toolAllowlistEvidenceFileName is the violation evidence file persisted at
// the run directory root, mirroring the denial log evidence placement.
const toolAllowlistEvidenceFileName = "tool-allowlist-violations.json"

// toolAllowlistAssessment carries the gate and the optional persisted
// violation evidence artifact.
type toolAllowlistAssessment struct {
	Gate     Gate
	Artifact *Artifact
}

func toolAllowlistGate(required bool, status, summary string) Gate {
	return Gate{ID: toolAllowlistGateID, Category: "policy", Required: required, Status: status, Summary: summary, Evidence: []string{}}
}

// truncateSummary bounds gate summaries to the report schema limit so long
// provider tool names can never invalidate the VerificationReport.
func truncateSummary(summary string) string {
	const limit = 7800
	if len(summary) <= limit {
		return summary
	}
	return summary[:limit] + "…"
}

// assessToolAllowlist implements the tool-allowlist gate: when the frozen
// TaskSpec declares worker.tools, the newest attempt's transcript metadata
// toolNames (the successful, non-denial tool calls) are reconciled against
// the declared set, crossed with the denial log so blocked calls never count
// as success, and any successful call outside the declaration fails the gate
// with a persisted violation list. Missing, unreadable, or malformed
// evidence fails closed; Runs without a declaration keep profile defaults
// and the gate stays skipped without blocking them.
func assessToolAllowlist(runDirectory, specDigest string, createdAt time.Time) (toolAllowlistAssessment, error) {
	specPath := filepath.Join(runDirectory, "task-spec.json")
	specData, err := os.ReadFile(specPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return toolAllowlistAssessment{Gate: toolAllowlistGate(false, "skipped", "tool-allowlist：无冻结 TaskSpec，视为未声明 worker.tools")}, nil
		}
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：读取冻结 TaskSpec 失败："+err.Error()))}, nil
	}
	digest, digestErr := canonical.DigestJSON(specData)
	if digestErr != nil || digest != specDigest {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", "tool-allowlist：冻结 TaskSpec 摘要与 Run 不一致，fail-closed")}, nil
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(specData, &task); err != nil {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：冻结 TaskSpec 无法解析："+err.Error()))}, nil
	}
	declared := ToolAllowlistFromTask(task)
	if len(declared) == 0 {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(false, "skipped", "tool-allowlist：未声明 worker.tools，保持 profile 缺省工具面")}, nil
	}
	if err := denials.ValidateAllowlist(declared); err != nil {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：声明无效："+err.Error()))}, nil
	}
	attemptDir, attemptName, ok := latestAttemptDirectory(filepath.Join(runDirectory, "attempts"))
	if !ok {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", "tool-allowlist：声明了 worker.tools 但没有 Attempt 证据，fail-closed")}, nil
	}
	outputDir := filepath.Join(attemptDir, "control", "output")
	metas, err := filepath.Glob(filepath.Join(outputDir, "*-transcript-meta.json"))
	if err != nil {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：枚举 transcript 元数据失败："+err.Error()))}, nil
	}
	if len(metas) == 0 {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", "tool-allowlist：声明了 worker.tools 但缺少 transcript 元数据证据，fail-closed")}, nil
	}
	var observed []string
	for _, metaPath := range metas {
		data, readErr := os.ReadFile(metaPath)
		if readErr != nil {
			return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：读取 transcript 元数据失败："+readErr.Error()))}, nil
		}
		names, extractErr := extractToolNames(data)
		if extractErr != nil {
			return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary(fmt.Sprintf("tool-allowlist：%s：%v", filepath.Base(metaPath), extractErr)))}, nil
		}
		observed = append(observed, names...)
	}
	// Cross with the denial log: denied calls are never successful calls,
	// and malformed denial evidence fails closed exactly like the
	// denial-summary gate does.
	denialRecords := 0
	if denialData, readErr := os.ReadFile(filepath.Join(outputDir, denials.LogFileName)); readErr == nil {
		records, parseErr := denials.ParseLog(denialData)
		if parseErr != nil {
			return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：denial log 无效："+parseErr.Error()))}, nil
		}
		denialRecords = len(records)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "fail", truncateSummary("tool-allowlist：读取 denial log 失败："+readErr.Error()))}, nil
	}
	normalizedObserved := denials.SortedToolNames(observed)
	violations := denials.AllowlistViolations(normalizedObserved, declared)
	if len(violations) > 0 {
		evidenceData, err := json.MarshalIndent(struct {
			Attempt    string   `json:"attempt"`
			Declared   []string `json:"declared"`
			Observed   []string `json:"observed"`
			Violations []string `json:"violations"`
		}{attemptName, declared, normalizedObserved, violations}, "", "  ")
		if err != nil {
			return toolAllowlistAssessment{}, err
		}
		if err := atomicWrite(filepath.Join(runDirectory, toolAllowlistEvidenceFileName), evidenceData); err != nil {
			return toolAllowlistAssessment{}, err
		}
		gate := toolAllowlistGate(true, "fail", truncateSummary(fmt.Sprintf("tool-allowlist：%d 个成功工具调用超出声明集：%s（声明：%s）", len(violations), strings.Join(violations, "、"), strings.Join(declared, "、"))))
		gate.Evidence = []string{"artifact://evidence:tool-allowlist-violations"}
		return toolAllowlistAssessment{Gate: gate, Artifact: &Artifact{
			ID: "evidence:tool-allowlist-violations", Kind: "tool-allowlist-evidence", MediaType: "application/json", Producer: "system",
			Required: false, Status: "validated", PathRoot: "run", RelativePath: toolAllowlistEvidenceFileName,
			ByteSize: int64(len(evidenceData)), Digest: canonical.DigestBytes(evidenceData), CreatedAt: createdAt,
			RelatedGates: []string{toolAllowlistGateID}, Description: "Attempt " + attemptName + " 的越权工具调用清单",
		}}, nil
	}
	summary := fmt.Sprintf("tool-allowlist：%d 个成功工具调用均在声明集内（声明：%s；交叉 denials.jsonl %d 条拒绝记录）", len(normalizedObserved), strings.Join(declared, "、"), denialRecords)
	return toolAllowlistAssessment{Gate: toolAllowlistGate(true, "pass", truncateSummary(summary))}, nil
}

// extractToolNames pulls the toolNames field out of one transcript-meta
// document. The field must be present and be a string array; a missing
// field, null, or a malformed value is an error so evidence gaps fail
// closed instead of passing.
func extractToolNames(metaData []byte) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metaData, &fields); err != nil {
		return nil, fmt.Errorf("transcript 元数据无效：%w", err)
	}
	rawNames, present := fields["toolNames"]
	if !present {
		return nil, errors.New("transcript 元数据缺少 toolNames 字段")
	}
	trimmed := bytes.TrimSpace(rawNames)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, errors.New("toolNames 字段缺失")
	}
	var names []string
	if err := json.Unmarshal(trimmed, &names); err != nil {
		return nil, fmt.Errorf("toolNames 不是字符串数组：%w", err)
	}
	return names, nil
}
