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
