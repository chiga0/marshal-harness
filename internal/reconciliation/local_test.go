package reconciliation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

type localFixture struct {
	input  Input
	runDir string
	state  domain.RunState
	files  map[string][]byte
}

func TestInspectHappyPathIsReadOnly(t *testing.T) {
	fixture := newLocalFixture(t)
	before, err := os.Stat(fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || len(report.Findings) != 0 || report.State == nil || report.State.RunID != fixture.input.RunID {
		t.Fatalf("unexpected report: %+v", report)
	}
	after, err := os.Stat(fixture.runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("inspection changed run directory mtime")
	}
	for name, want := range fixture.files {
		got, err := os.ReadFile(filepath.Join(fixture.runDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("inspection changed %s", name)
		}
	}
}

func TestInspectReplaysStaleSnapshotAndToleratesTruncatedTail(t *testing.T) {
	fixture := newLocalFixture(t)
	event := transitionEvent(fixture.input.RunID)
	eventData, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	journal := append(append(eventData, '\n'), []byte(`{"partial":"secret-value"}`)...)
	writeLocalFile(t, fixture, "events.jsonl", journal)

	report, err := Inspect(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, report, "journal-truncated", "warning", true)
	assertFinding(t, report, "snapshot-stale", "warning", true)
	if report.Status != "warning" || report.JournalSequence != 1 || report.State == nil || report.State.State != domain.StatePlanned {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestInspectSnapshotAheadIsBlocked(t *testing.T) {
	fixture := newLocalFixture(t)
	fixture.state.State = domain.StatePlanned
	fixture.state.Sequence = 1
	writeJSONFile(t, fixture, "state.json", fixture.state)

	report, err := Inspect(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, report, "journal-snapshot-conflict", "error", false)
	if report.Status != "blocked" || report.State != nil {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestInspectMissingInvalidAndMismatchedCoreEvidence(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *localFixture)
		code string
	}{
		{"snapshot missing", func(t *testing.T, f *localFixture) {
			if err := os.Remove(filepath.Join(f.runDir, "state.json")); err != nil {
				t.Fatal(err)
			}
		}, "snapshot-missing"},
		{"snapshot invalid", func(t *testing.T, f *localFixture) {
			writeLocalFile(t, f, "state.json", []byte(`{"secret":"must-not-leak"}`))
		}, "snapshot-invalid"},
		{"snapshot identity", func(t *testing.T, f *localFixture) {
			f.state.RunID = "another-run"
			writeJSONFile(t, f, "state.json", f.state)
		}, "snapshot-identity-mismatch"},
		{"journal missing", func(t *testing.T, f *localFixture) {
			if err := os.Remove(filepath.Join(f.runDir, "events.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, "journal-missing"},
		{"journal invalid", func(t *testing.T, f *localFixture) {
			writeLocalFile(t, f, "events.jsonl", []byte("{not-json}\n"))
		}, "journal-invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalFixture(t)
			test.edit(t, fixture)
			report, err := Inspect(context.Background(), fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			assertFinding(t, report, test.code, "error", false)
			if report.Status != "blocked" {
				t.Fatalf("status = %s", report.Status)
			}
			assertNoLeak(t, report, "must-not-leak", filepath.Join(fixture.runDir, "state.json"))
		})
	}
}

func TestInspectFrozenDigestAndIdentityMismatches(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *localFixture)
		code string
	}{
		{"task digest", func(t *testing.T, f *localFixture) {
			f.state.SpecDigest = zeroDigest()
			writeJSONFile(t, f, "state.json", f.state)
		}, "task-spec-digest-mismatch"},
		{"policy digest", func(t *testing.T, f *localFixture) {
			f.state.PolicyDigest = zeroDigest()
			writeJSONFile(t, f, "state.json", f.state)
		}, "policy-snapshot-digest-mismatch"},
		{"capability digest", func(t *testing.T, f *localFixture) {
			f.state.CapabilityDigest = zeroDigest()
			writeJSONFile(t, f, "state.json", f.state)
		}, "capability-snapshot-digest-mismatch"},
		{"task identity", func(t *testing.T, f *localFixture) {
			var task map[string]any
			mustJSON(t, f.files["task-spec.json"], &task)
			task["repository"].(map[string]any)["path"] = filepath.Join(f.input.RepositoryRoot, "other")
			data := marshalJSON(t, task)
			writeLocalFile(t, f, "task-spec.json", data)
			f.state.SpecDigest = digestJSON(t, data)
			writeJSONFile(t, f, "state.json", f.state)
		}, "task-spec-identity-mismatch"},
		{"policy identity", func(t *testing.T, f *localFixture) {
			var policy map[string]any
			mustJSON(t, f.files["policy-snapshot.json"], &policy)
			policy["runId"] = "another-run"
			data := marshalJSON(t, policy)
			writeLocalFile(t, f, "policy-snapshot.json", data)
			f.state.PolicyDigest = digestJSON(t, data)
			writeJSONFile(t, f, "state.json", f.state)
		}, "policy-snapshot-identity-mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalFixture(t)
			test.edit(t, fixture)
			report, err := Inspect(context.Background(), fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			assertFinding(t, report, test.code, "error", false)
		})
	}
}

func TestInspectRejectsSymlinkAndOversizeEvidenceWithoutLeaks(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		fixture := newLocalFixture(t)
		target := filepath.Join(t.TempDir(), "secret-target")
		if err := os.WriteFile(target, []byte("secret-value"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.runDir, "capability-snapshot.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		report, err := Inspect(context.Background(), fixture.input)
		if err != nil {
			t.Fatal(err)
		}
		assertFinding(t, report, "capability-snapshot-invalid", "error", false)
		assertNoLeak(t, report, target, "secret-value")
	})

	t.Run("oversize", func(t *testing.T) {
		fixture := newLocalFixture(t)
		path := filepath.Join(fixture.runDir, "policy-snapshot.json")
		if err := os.Truncate(path, maxEvidenceBytes+1); err != nil {
			t.Fatal(err)
		}
		report, err := Inspect(context.Background(), fixture.input)
		if err != nil {
			t.Fatal(err)
		}
		assertFinding(t, report, "policy-snapshot-invalid", "error", false)
		assertNoLeak(t, report, path)
	})
}

func TestInspectInvalidInputAndCancellation(t *testing.T) {
	fixture := newLocalFixture(t)
	for _, input := range []Input{
		{StateRoot: fixture.input.StateRoot, RepositoryRoot: fixture.input.RepositoryRoot, RunID: "../escape", Validator: fixture.input.Validator},
		{StateRoot: "relative", RepositoryRoot: fixture.input.RepositoryRoot, RunID: fixture.input.RunID, Validator: fixture.input.Validator},
		{StateRoot: fixture.input.StateRoot, RepositoryRoot: fixture.input.RepositoryRoot, RunID: fixture.input.RunID},
	} {
		_, err := Inspect(context.Background(), input)
		if err == nil || strings.Contains(err.Error(), fixture.input.StateRoot) {
			t.Fatalf("unsafe input error: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, fixture.input); err != context.Canceled {
		t.Fatalf("cancellation error = %v", err)
	}
}

func newLocalFixture(t *testing.T) *localFixture {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), ".marshal")
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-01"
	runDir := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	task := fixtureDocument(t, "examples/happy-path/task-spec.json")
	var taskObject map[string]any
	mustJSON(t, task, &taskObject)
	taskObject["repository"].(map[string]any)["path"] = repositoryRoot
	task = marshalJSON(t, taskObject)
	policy := fixtureDocument(t, "examples/happy-path/policy-snapshot.json")
	capability := fixtureDocument(t, "examples/happy-path/capability-snapshot.json")
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	state := domain.NewRunState("ENG-123", runID, now)
	state.SpecDigest = digestJSON(t, task)
	state.PolicyDigest = digestJSON(t, policy)
	state.CapabilityDigest = digestJSON(t, capability)
	fixture := &localFixture{
		input:  Input{StateRoot: stateRoot, RepositoryRoot: repositoryRoot, RunID: runID, Validator: validator},
		runDir: runDir,
		state:  state,
		files:  map[string][]byte{"task-spec.json": task, "policy-snapshot.json": policy, "capability-snapshot.json": capability, "events.jsonl": {}},
	}
	writeJSONFile(t, fixture, "state.json", state)
	for name, data := range fixture.files {
		writeLocalFile(t, fixture, name, data)
	}
	stateData, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.files["state.json"] = stateData
	return fixture
}

func transitionEvent(runID string) domain.RunEvent {
	return domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event-01", RunID: runID, Sequence: 1, Type: "state.transitioned", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: time.Date(2026, 8, 4, 12, 0, 1, 0, time.UTC), Payload: map[string]any{}}
}

func fixtureDocument(t *testing.T, name string) []byte {
	t.Helper()
	data, err := marshalSchemas.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeJSONFile(t *testing.T, fixture *localFixture, name string, value any) {
	t.Helper()
	writeLocalFile(t, fixture, name, marshalJSON(t, value))
}

func writeLocalFile(t *testing.T, fixture *localFixture, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.runDir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.files[name] = append([]byte(nil), data...)
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func digestJSON(t *testing.T, data []byte) string {
	t.Helper()
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func zeroDigest() string { return "sha256:" + strings.Repeat("0", 64) }

func assertFinding(t *testing.T, report Report, code, severity string, repairable bool) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			if finding.Severity != severity || finding.Repairable != repairable {
				t.Fatalf("finding %s = %+v", code, finding)
			}
			return
		}
	}
	t.Fatalf("finding %s absent from %+v", code, report)
}

func assertNoLeak(t *testing.T, report Report, values ...string) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value != "" && strings.Contains(string(data), value) {
			t.Fatalf("report leaked %q", value)
		}
	}
}
