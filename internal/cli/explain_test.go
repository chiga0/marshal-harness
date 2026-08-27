package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeExplainFixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	runGit(t, repositoryRoot, "remote", "add", "origin", "git@example.invalid:fixture/repo.git")
	runID := "run-explain-cli"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	runDir := filepath.Join(repositoryRoot, ".marshal", "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []map[string]any{
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:1", "runId": runID, "attemptId": "", "sequence": 1, "type": "planning.spec-accepted", "stateFrom": "CREATED", "stateTo": "PLANNED", "timestamp": now.Add(-time.Hour).Format(time.RFC3339), "payload": map[string]any{}},
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:2", "runId": runID, "attemptId": "", "sequence": 2, "type": "planning.inputs-frozen", "stateFrom": "PLANNED", "stateTo": "READY", "timestamp": now.Add(-50 * time.Minute).Format(time.RFC3339), "payload": map[string]any{}},
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:3", "runId": runID, "attemptId": "attempt-1", "sequence": 3, "type": "worker.started", "stateFrom": "READY", "stateTo": "RUNNING", "timestamp": now.Add(-40 * time.Minute).Format(time.RFC3339), "payload": map[string]any{}},
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:4", "runId": runID, "attemptId": "attempt-1", "sequence": 4, "type": "worker.completed", "stateFrom": "RUNNING", "stateTo": "VERIFYING", "timestamp": now.Add(-30 * time.Minute).Format(time.RFC3339), "payload": map[string]any{}},
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:5", "runId": runID, "attemptId": "attempt-1", "sequence": 5, "type": "verification.completed", "stateFrom": "VERIFYING", "stateTo": "REVIEW_PENDING", "timestamp": now.Add(-20 * time.Minute).Format(time.RFC3339), "payload": map[string]any{}},
	}
	var eventsBytes strings.Builder
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		eventsBytes.Write(append(data, '\n'))
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(eventsBytes.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "RunState", "taskId": "T1", "runId": runID, "state": "REVIEW_PENDING",
		"sequence": 5, "reviewRound": 0, "attemptsUsed": 1, "operationalRetriesUsed": 0, "reworkRoundsUsed": 0,
		"createdAt": now.Format(time.RFC3339), "updatedAt": now.Format(time.RFC3339),
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), append(stateBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), []byte(`{"publication":{"required":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return repositoryRoot, runID
}

func TestExplainRunRendersRecoveryDecision(t *testing.T) {
	repositoryRoot, runID := writeExplainFixtureRepo(t)
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"explain", "run", runID}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("explain exit = %d, stderr = %s", exit, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"authoritative timeline:",
		"decision: action=resume",
		"rationale=attempt-already-terminal",
		"next action:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("explain output missing %q:\n%s", want, text)
		}
	}
}

func TestExplainRunMissingRunFailsClosed(t *testing.T) {
	repositoryRoot, _ := writeExplainFixtureRepo(t)
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"explain", "run", "run-does-not-exist"}, strings.NewReader(""), &stdout, &stderr)
	if exit == ExitOK {
		t.Errorf("unknown run must fail closed")
	}
}

// seedRecoverTakeoverFixtureRepo 构造一个 stale RUNNING 的孤儿 Attempt
// （worker.started 距 now 远超 staleness；owner 死亡证明由 task run 的
// 耐用 lease 记录在调用前完成，本测试只覆盖恢复模型判定的两层结果）。
func seedRecoverTakeoverFixtureRepo(t *testing.T, publicationRequired bool) (stateRoot, runID string) {
	t.Helper()
	stateRoot = t.TempDir()
	runID = "run-recover-takeover"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	runDir := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []map[string]any{
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:1", "runId": runID, "attemptId": "", "sequence": 1, "type": "planning.spec-accepted", "stateFrom": "CREATED", "stateTo": "PLANNED", "timestamp": now.Add(-3 * time.Hour).Format(time.RFC3339), "payload": map[string]any{}},
		{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:2", "runId": runID, "attemptId": "attempt-1", "sequence": 2, "type": "worker.started", "stateFrom": "READY", "stateTo": "RUNNING", "timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339), "payload": map[string]any{}},
	}
	var eventsBytes strings.Builder
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		eventsBytes.Write(append(data, '\n'))
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(eventsBytes.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "RunState", "taskId": "T1", "runId": runID, "state": "RUNNING",
		"sequence": 2, "reviewRound": 0, "attemptsUsed": 1, "operationalRetriesUsed": 0, "reworkRoundsUsed": 0,
		"createdAt": now.Add(-3 * time.Hour).Format(time.RFC3339), "updatedAt": now.Add(-2 * time.Hour).Format(time.RFC3339),
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), append(stateBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	specBytes := []byte(`{"publication":{"required":false}}`)
	if publicationRequired {
		specBytes = []byte(`{"publication":{"required":true}}`)
	}
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), specBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return stateRoot, runID
}

// ADR 0053 决策 5 / I186-R4：--recover-dead-driver 逃生舱唯一经由单一恢复
// 模型判定。无副作用声明的孤儿 → new-attempt 免 reconcile，允许接管；声明
// publication 副作用且观察 unreachable → ambiguous-side-effect，fail
// closed 并把幂等键对账交回 `marshal explain run`。
func TestRecoverTakeoverAdmissionMatrix(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	t.Run("orphan without side effect admitted", func(t *testing.T) {
		stateRoot, runID := seedRecoverTakeoverFixtureRepo(t, false)
		if err := recoverTakeoverAdmission(stateRoot, runID, now); err != nil {
			t.Fatalf("side-effect-free orphan must be admitted: %v", err)
		}
	})
	t.Run("side-effect orphan needs reconcile first", func(t *testing.T) {
		stateRoot, runID := seedRecoverTakeoverFixtureRepo(t, true)
		err := recoverTakeoverAdmission(stateRoot, runID, now)
		if err == nil {
			t.Fatal("side-effect orphan must fail closed pending reconcile")
		}
		if !strings.Contains(err.Error(), "ambiguous-side-effect") || !strings.Contains(err.Error(), "marshal explain run "+runID) {
			t.Fatalf("error = %q, want ambiguous-side-effect rationale with explain pointer", err)
		}
	})
	t.Run("unknown run fails closed", func(t *testing.T) {
		if err := recoverTakeoverAdmission(t.TempDir(), "run-missing", now); err == nil {
			t.Fatal("unknown run must fail closed")
		}
	})
}
