package pi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseProductionWorkerResultExtractsFinalAssistantObject(t *testing.T) {
	declared := validDeclaredResult("worker-claim")
	declared["adapter"] = map[string]any{"id": "pi", "executable": "worker-claim", "version": "worker-claim", "model": "worker-claim"}
	declared["startedAt"] = "2020-01-01T00:00:00Z"
	declared["completedAt"] = "2020-01-01T00:00:01Z"
	declaredBytes, err := json.Marshal(declared)
	if err != nil {
		t.Fatal(err)
	}
	end, err := json.Marshal(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "request"}}},
			map[string]any{
				"role":       "assistant",
				"stopReason": "stop",
				"usage":      map[string]any{"input": 17, "output": 9, "cacheRead": 4, "cost": 0.003},
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "omitted"},
					map[string]any{"type": "text", "text": string(declaredBytes)},
				},
			},
		},
		"willRetry": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript := jsonLines(
		captureSessionHeader("session-1"),
		`{"type":"agent_start"}`,
		string(end),
		`{"type":"agent_settled"}`,
	)
	started := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Second)
	record, err := ParseProductionWorkerResult(context.Background(), ProductionResultInput{
		Transcript: []byte(transcript), Worktree: "/worktree",
		TaskID: "TASK-1", RunID: "run-1", AttemptID: "attempt-1",
		Executable: "/usr/local/bin/pi", Version: "0.84.4", Model: "anthropic/claude-sonnet",
		StartedAt: started, CompletedAt: completed, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != "WorkerResult" {
		t.Fatalf("kind = %s", record.Kind)
	}
	var got declaredResult
	if err := json.Unmarshal(record.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Adapter.Executable != "/usr/local/bin/pi" || got.Adapter.Version != "0.84.4" || got.Adapter.Model != "anthropic/claude-sonnet" {
		t.Fatalf("adapter = %+v", got.Adapter)
	}
	if got.Session == nil || got.Session.ID != "session-1" || got.Session.Resumable {
		t.Fatalf("session = %+v", got.Session)
	}
	if !got.StartedAt.Equal(started) || !got.CompletedAt.Equal(completed) {
		t.Fatalf("timing = %s..%s", got.StartedAt, got.CompletedAt)
	}
	if !strings.Contains(string(got.Usage), `"inputTokens":17`) || !strings.Contains(string(got.Usage), `"cachedInputTokens":4`) {
		t.Fatalf("usage = %s", got.Usage)
	}
}

func TestExtractFinalWorkerResultFailsClosed(t *testing.T) {
	valid := `{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult"}`
	cases := []struct {
		name    string
		content []any
	}{
		{name: "missing", content: nil},
		{name: "duplicate-text", content: []any{map[string]any{"type": "text", "text": valid}, map[string]any{"type": "text", "text": valid}}},
		{name: "tool-call", content: []any{map[string]any{"type": "toolCall", "text": valid}}},
		{name: "trailing-json", content: []any{map[string]any{"type": "text", "text": valid + ` {}`}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, err := json.Marshal(map[string]any{
				"type": "agent_end", "messages": []any{map[string]any{"role": "assistant", "stopReason": "stop", "content": tc.content}}, "willRetry": false,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = extractFinalWorkerResult([]byte(jsonLines(string(end))))
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestParseProductionWorkerResultRejectsIdentityMismatch(t *testing.T) {
	declaredBytes, err := json.Marshal(validDeclaredResult("worker-claim"))
	if err != nil {
		t.Fatal(err)
	}
	end, err := json.Marshal(map[string]any{
		"type": "agent_end", "messages": []any{map[string]any{
			"role": "assistant", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": string(declaredBytes)}},
		}}, "willRetry": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	_, err = ParseProductionWorkerResult(context.Background(), ProductionResultInput{
		Transcript: []byte(jsonLines(captureSessionHeader("session-1"), `{"type":"agent_start"}`, string(end))), Worktree: "/worktree",
		TaskID: "OTHER", RunID: "run-1", AttemptID: "attempt-1", Executable: "/usr/local/bin/pi", Version: "0.84.4",
		StartedAt: started, CompletedAt: started.Add(time.Second), MaxOutputBytes: 1 << 20,
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("error = %v, want identity mismatch", err)
	}
}
