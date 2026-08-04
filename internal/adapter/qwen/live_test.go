package qwen

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// TestLiveQwen is opt-in because it consumes the locally configured model
// provider. It never commits, pushes, publishes, or touches the source repo.
func TestLiveQwen(t *testing.T) {
	executable := os.Getenv("MARSHAL_LIVE_QWEN_PATH")
	if executable == "" {
		t.Skip("set MARSHAL_LIVE_QWEN_PATH to run the live adapter E2E")
	}
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if output, err := exec.Command("git", "-C", worktree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	controlRoot := t.TempDir()
	controlRoot, err = filepath.EvalSymlinks(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(controlRoot, "output", "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(controlRoot, "input", "task-spec.json")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte(`{"worker":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "在当前仓库创建 hello.txt，内容必须恰好为 marshal-live-e2e 加换行。不要提交、推送或访问网络。完成后把下面 JSON 原样写到 " + resultPath + `：
{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"TASK-LIVE","runId":"run-live","attemptId":"attempt-live","adapter":{"id":"qwen","executable":"declared","version":"declared"},"status":"completed","summary":"created hello.txt","declaredChangedFiles":["hello.txt"],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"startedAt":"2026-08-04T00:00:00Z","completedAt":"2026-08-04T00:00:01Z"}`
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-LIVE", "runId": "run-live", "attemptId": "attempt-live", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "qwen", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 180, "maxOutputBytes": 5 << 20, "reviewFindings": []any{},
	}
	data, _ := json.Marshal(request)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := adapter.Run(ctx, domain.Record{Kind: domain.KindWorkerRequest, Data: data})
	if err != nil {
		transcript, _ := os.ReadFile(filepath.Join(controlRoot, "output", "qwen-transcript.jsonl"))
		metadata, _ := os.ReadFile(filepath.Join(controlRoot, "output", "qwen-transcript-meta.json"))
		t.Fatalf("%v\nmetadata=%s\ntranscript=%s", err, metadata, transcript)
	}
	if err := validator.Validate(domain.KindWorkerResult, result.Data); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(worktree, "hello.txt"))
	if err != nil || string(content) != "marshal-live-e2e\n" {
		t.Fatalf("hello.txt=%q err=%v", content, err)
	}
}
