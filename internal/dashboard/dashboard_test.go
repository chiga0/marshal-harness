package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRun(t *testing.T, root, runID, taskID, state string) {
	t.Helper()
	runDir := filepath.Join(root, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stateJSON := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "RunState",
		"taskId": taskID, "runId": runID, "state": state, "sequence": 3,
		"reviewRound": 0, "attemptsUsed": 1, "createdAt": now, "updatedAt": now,
	}
	data, _ := json.Marshal(stateJSON)
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	events := `{"sequence":1,"type":"planning.spec-accepted","stateFrom":"CREATED","stateTo":"PLANNED","timestamp":"` + now.Format(time.RFC3339) + `"}` + "\n" +
		`{"sequence":2,"type":"worker.started","stateFrom":"READY","stateTo":"RUNNING","timestamp":"` + now.Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestListRunsReadsOnlyState(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "run-a", "TASK-A", "RUNNING")
	writeRun(t, root, "run-b", "TASK-B", "ACCEPTED")
	runs, err := ListRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].State != "RUNNING" && runs[1].State != "RUNNING" {
		t.Fatalf("expected a RUNNING run: %+v", runs)
	}
}

func TestReadEventsParsesLines(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "run-a", "TASK-A", "RUNNING")
	events, err := ReadEvents(root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "planning.spec-accepted" || events[1].To != "RUNNING" {
		t.Fatalf("events = %+v", events)
	}
}

func TestHandlerReadOnlyAndEndpoints(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "run-a", "TASK-A", "RUNNING")
	handler := NewHandler(Options{StateRoot: root})

	// index
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Marshal Dashboard") {
		t.Fatalf("index code=%d", rec.Code)
	}
	// runs list
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("runs code=%d", rec.Code)
	}
	// per-run events
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/run-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("run events code=%d", rec.Code)
	}
	// non-GET rejected (read-only)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should be rejected, code=%d", rec.Code)
	}
}
