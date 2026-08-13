package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// toolAuditGateID is the fixed identifier of the tool-audit gate.
const toolAuditGateID = "tool-audit"

// assessToolAudit implements the tool-audit gate, mirroring the
// denial-summary pattern: it reconciles the toolNames recorded in the
// newest attempt's transcript metadata against the frozen TaskSpec's
// worker.tools allowlist carried by Input.ToolAllowlist. An empty allowlist
// keeps the gate skipped for backward compatibility; a declared allowlist
// without transcript evidence fails closed; any successful tool call
// outside the allowlist fails the gate with the offending tools listed in
// the gate evidence.
func assessToolAudit(input Input, attemptDir string) Gate {
	gate := Gate{ID: toolAuditGateID, Category: "policy", Required: true, Status: "pass", Evidence: []string{}}
	if len(input.ToolAllowlist) == 0 {
		gate.Required = false
		gate.Status = "skipped"
		gate.Summary = "tool-audit：TaskSpec 未声明 worker.tools，跳过对账"
		return gate
	}
	if attemptDir == "" {
		gate.Status = "fail"
		gate.Summary = "tool-audit：声明了 worker.tools 但没有 Attempt 证据，fail-closed"
		return gate
	}
	metas, err := filepath.Glob(filepath.Join(attemptDir, "control", "output", "*-transcript-meta.json"))
	if err != nil {
		gate.Status = "fail"
		gate.Summary = truncateSummary("tool-audit：枚举 transcript 元数据失败：" + err.Error())
		return gate
	}
	if len(metas) == 0 {
		gate.Status = "fail"
		gate.Summary = "tool-audit：声明了 worker.tools 但缺少 transcript-meta 证据，fail-closed"
		return gate
	}
	allowed := make(map[string]struct{}, len(input.ToolAllowlist))
	for _, name := range input.ToolAllowlist {
		allowed[name] = struct{}{}
	}
	var observed []string
	for _, metaPath := range metas {
		data, readErr := os.ReadFile(metaPath)
		if readErr != nil {
			gate.Status = "fail"
			gate.Summary = truncateSummary("tool-audit：读取 transcript 元数据失败：" + readErr.Error())
			return gate
		}
		names, extractErr := extractToolNames(data)
		if extractErr != nil {
			gate.Status = "fail"
			gate.Summary = truncateSummary(fmt.Sprintf("tool-audit：%s：%v", filepath.Base(metaPath), extractErr))
			return gate
		}
		observed = append(observed, names...)
	}
	seen := make(map[string]struct{})
	violations := []string{}
	for _, name := range observed {
		if _, ok := allowed[name]; ok {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		violations = append(violations, name)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		gate.Status = "fail"
		gate.Summary = truncateSummary(fmt.Sprintf("tool-audit：%d 个成功工具调用越权：%s（allowlist：%s）", len(violations), strings.Join(violations, "、"), strings.Join(input.ToolAllowlist, "、")))
		for _, name := range violations {
			gate.Evidence = append(gate.Evidence, "越权工具："+name)
		}
		return gate
	}
	gate.Summary = fmt.Sprintf("tool-audit：%d 个成功工具调用均在 worker.tools allowlist 内（allowlist：%s）", len(observed), strings.Join(input.ToolAllowlist, "、"))
	return gate
}
