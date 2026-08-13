package fake

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// allowlistCompliantFixture replays only successful tool calls that stay
// inside the declared allowlist [read edit write].
const allowlistCompliantFixture = `{
  "capability": {"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"fake","adapterVersion":"1.0.0","executable":"fixture","executableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","probedAt":"2026-08-12T00:00:00Z","capabilities":{"structuredOutput":true}},
  "events": [
    {"type":"worker.started"},
    {"type":"tool","tool":"read"},
    {"type":"tool","tool":"edit"},
    {"type":"tool","tool":"write"},
    {"type":"tool","tool":"grep","denied":true},
    {"type":"worker.completed"}
  ],
  "result": {"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"task:fixture","runId":"run:fixture","attemptId":"attempt:fixture","adapterId":"fake","status":"completed","summary":"compliant fixture"}
}`

// allowlistViolationFixture replays a successful grep call outside the
// declared allowlist [read edit write].
const allowlistViolationFixture = `{
  "capability": {"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"fake","adapterVersion":"1.0.0","executable":"fixture","executableDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","probedAt":"2026-08-12T00:00:00Z","capabilities":{"structuredOutput":true}},
  "events": [
    {"type":"worker.started"},
    {"type":"tool","tool":"read"},
    {"type":"tool","tool":"grep"},
    {"type":"worker.completed"}
  ],
  "result": {"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"task:fixture","runId":"run:fixture","attemptId":"attempt:fixture","adapterId":"fake","status":"completed","summary":"violation fixture"}
}`

func TestFakeAllowlistFixturesReconcileDeterministically(t *testing.T) {
	declared := []string{"read", "edit", "write"}
	t.Run("compliant-fixture-passes", func(t *testing.T) {
		adapter, err := New([]byte(allowlistCompliantFixture))
		if err != nil {
			t.Fatal(err)
		}
		toolNames := SuccessfulToolNames(adapter.Events())
		if strings.Join(toolNames, ",") != "edit,read,write" {
			t.Fatalf("toolNames = %v, want [edit read write]; denied calls must be excluded", toolNames)
		}
		if violations := denials.AllowlistViolations(toolNames, declared); len(violations) != 0 {
			t.Fatalf("compliant fixture produced violations: %v", violations)
		}
	})
	t.Run("violation-fixture-fails", func(t *testing.T) {
		adapter, err := New([]byte(allowlistViolationFixture))
		if err != nil {
			t.Fatal(err)
		}
		toolNames := SuccessfulToolNames(adapter.Events())
		violations := denials.AllowlistViolations(toolNames, declared)
		if strings.Join(violations, ",") != "grep" {
			t.Fatalf("violations = %v, want exactly [grep]", violations)
		}
	})
}

func TestFakeRunPersistsReconciliationInputForWorkerRequest(t *testing.T) {
	controlRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(controlRoot, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	requestData, err := json.Marshal(map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest",
		"controlRoot": controlRoot, "resultPath": "output/worker-result.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		fixture   string
		wantNames string
	}{
		{name: "compliant", fixture: allowlistCompliantFixture, wantNames: "edit,read,write"},
		{name: "violation", fixture: allowlistViolationFixture, wantNames: "grep,read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := New([]byte(test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Run(context.Background(), domain.Record{Kind: domain.KindWorkerRequest, Data: requestData}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(controlRoot, "output", MetaFileName))
			if err != nil {
				t.Fatalf("fake transcript meta missing: %v", err)
			}
			var meta struct {
				ToolNames        []string `json:"toolNames"`
				PermissionDenied bool     `json:"permissionDenied"`
			}
			if err := json.Unmarshal(data, &meta); err != nil {
				t.Fatal(err)
			}
			if strings.Join(meta.ToolNames, ",") != test.wantNames {
				t.Fatalf("meta toolNames = %v, want %q", meta.ToolNames, test.wantNames)
			}
			if meta.PermissionDenied {
				t.Fatal("fake adapter never denies; permissionDenied must stay false")
			}
		})
	}
	// An empty record keeps the historical behavior: no meta is written and
	// Run still returns the fixture result.
	emptyDir := t.TempDir()
	adapter, err := New([]byte(allowlistCompliantFixture))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Run(context.Background(), domain.Record{})
	if err != nil || result.Kind != domain.KindWorkerResult {
		t.Fatalf("Run with empty record = %+v, %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(emptyDir, MetaFileName)); !os.IsNotExist(statErr) {
		t.Fatal("meta must not be written without a WorkerRequest")
	}
}

func TestTranscriptAdapterIsDeterministic(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/success.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := adapter.Probe(context.Background())
	if err != nil || capability.Kind != domain.KindCapabilitySnapshot {
		t.Fatalf("Probe = %+v, %v", capability, err)
	}
	result, err := adapter.Run(context.Background(), domain.Record{})
	if err != nil || result.Kind != domain.KindWorkerResult {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	events := adapter.Events()
	events[0][0] = 'X'
	if adapter.Events()[0][0] == 'X' {
		t.Fatal("Events exposed mutable transcript storage")
	}
}
