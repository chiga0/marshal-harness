package execution

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func samplePrompt(t *testing.T) string {
	t.Helper()
	task := domain.TaskSpec{}
	task.Work.Objective = "测试目标"
	state := domain.RunState{TaskID: "TASK-1", RunID: "run-1", WorktreePath: "/state/worktrees/TASK-1"}
	return renderPrompt(task, state, "attempt-1", "/state/runs/run-1/attempts/attempt-1/control", "opencode", []map[string]string{})
}

func TestRenderPromptEmbedsWorkerResultTemplateAnchors(t *testing.T) {
	prompt := samplePrompt(t)
	if !strings.HasPrefix(prompt, "# Marshal Worker 任务") {
		t.Fatalf("prompt prefix changed:\n%s", prompt)
	}
	heading := strings.Index(prompt, "## WorkerResult 输出模板")
	originalEnding := strings.Index(prompt, "Marshal 会以实际观测值覆盖不可信的运行元数据。\n")
	if heading < 0 || originalEnding < 0 || heading < originalEnding {
		t.Fatalf("template section must be appended after the original prompt ending (heading=%d ending=%d)", heading, originalEnding)
	}
	for _, anchor := range []string{
		`"apiVersion": "marshal.dev/v1alpha1"`,
		`"kind": "WorkerResult"`,
		`"taskId":`,
		`"runId":`,
		`"attemptId":`,
		`"adapter":`,
		`"id":`,
		`"executable":`,
		`"version":`,
		`"status":`,
		`"summary":`,
		`"declaredChangedFiles":`,
		`"declaredArtifacts":`,
		`"declaredCommands":`,
		`"declaredRisks":`,
		`"startedAt":`,
		`"completedAt":`,
		"session 为可选字段",
		"省略整个 session 字段",
		"可为空数组，但必须存在",
		"RFC3339",
	} {
		if !strings.Contains(prompt[heading:], anchor) {
			t.Fatalf("template section is missing anchor %q:\n%s", anchor, prompt[heading:])
		}
	}
	block := extractPromptJSONBlock(t, prompt)
	if strings.Contains(block, "session") {
		t.Fatalf("template must omit the optional session field entirely:\n%s", block)
	}
}

func TestRenderPromptWorkerResultTemplateMatchesSchema(t *testing.T) {
	prompt := samplePrompt(t)
	block := extractPromptJSONBlock(t, prompt)
	var template any
	if err := json.Unmarshal([]byte(block), &template); err != nil {
		t.Fatalf("template block is not valid JSON: %v\n%s", err, block)
	}
	substituted := substituteWorkerResultPlaceholders(t, template, "")
	data, err := json.Marshal(substituted)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		t.Fatalf("template with substituted placeholders fails the WorkerResult schema: %v\n%s", err, data)
	}
}

func extractPromptJSONBlock(t *testing.T, prompt string) string {
	t.Helper()
	fence := "```json\n"
	start := strings.Index(prompt, fence)
	if start < 0 {
		t.Fatalf("prompt does not contain a ```json template block:\n%s", prompt)
	}
	start += len(fence)
	end := strings.Index(prompt[start:], "\n```")
	if end < 0 {
		t.Fatalf("prompt template block is not closed:\n%s", prompt[start:])
	}
	return prompt[start : start+end]
}

var workerResultTemplatePlaceholderValues = map[string]string{
	"/taskId":                 "TASK-1",
	"/runId":                  "run-1",
	"/attemptId":              "attempt-1",
	"/adapter/id":             "opencode",
	"/adapter/executable":     "/usr/local/bin/opencode",
	"/adapter/version":        "1.0.0",
	"/status":                 "completed",
	"/summary":                "模板结构校验示例。",
	"/declaredChangedFiles/0": "path/to/file.go",
	"/startedAt":              "2026-08-06T00:00:00Z",
	"/completedAt":            "2026-08-06T01:00:00Z",
}

func substituteWorkerResultPlaceholders(t *testing.T, value any, pointer string) any {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = substituteWorkerResultPlaceholders(t, item, pointer+"/"+key)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = substituteWorkerResultPlaceholders(t, item, fmt.Sprintf("%s/%d", pointer, index))
		}
		return typed
	case string:
		if !strings.HasPrefix(typed, "<") || !strings.HasSuffix(typed, ">") {
			return typed
		}
		replacement, ok := workerResultTemplatePlaceholderValues[pointer]
		if !ok {
			t.Fatalf("no schema-valid substitution for placeholder %q at %s", typed, pointer)
		}
		return replacement
	default:
		return typed
	}
}
