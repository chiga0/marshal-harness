package verification

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// writeAllowlistFixture builds a run directory carrying a frozen TaskSpec
// (optionally declaring worker.tools) and, when attemptEvidence is true, one
// attempt with the given adapter transcript metas (adapter ID -> toolNames).
// A meta value of nil writes a meta document without the toolNames field.
func writeAllowlistFixture(t *testing.T, tools any, attemptEvidence bool, metas map[string][]string) (string, string) {
	t.Helper()
	runDirectory := t.TempDir()
	worker := map[string]any{"preferredAdapter": "fake", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"}
	if tools != nil {
		worker["tools"] = tools
	}
	specData, err := json.Marshal(map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "Task", "worker": worker})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "task-spec.json"), specData, 0o600); err != nil {
		t.Fatal(err)
	}
	specDigest, err := canonical.DigestJSON(specData)
	if err != nil {
		t.Fatal(err)
	}
	if attemptEvidence {
		attemptDir := filepath.Join(runDirectory, "attempts", "attempt-1")
		outputDir := filepath.Join(attemptDir, "control", "output")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), []byte(`{"attemptNumber":1}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for adapterID, names := range metas {
			var document []byte
			if names == nil {
				document = []byte(`{"eventCount":1}`)
			} else {
				data, err := json.Marshal(map[string]any{"toolNames": names})
				if err != nil {
					t.Fatal(err)
				}
				document = data
			}
			if err := os.WriteFile(filepath.Join(outputDir, adapterID+"-transcript-meta.json"), document, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return runDirectory, specDigest
}

func TestToolAllowlistGateSkippedWithoutDeclaration(t *testing.T) {
	t.Run("no-tools-field", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, nil, true, map[string][]string{"fake": {"read"}})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if assessment.Gate.Status != "skipped" || assessment.Gate.Required || assessment.Gate.ID != toolAllowlistGateID {
			t.Fatalf("gate = %+v, want non-required skipped", assessment.Gate)
		}
	})
	t.Run("missing-task-spec", func(t *testing.T) {
		assessment, err := assessToolAllowlist(t.TempDir(), "sha256:"+strings.Repeat("a", 64), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if assessment.Gate.Status != "skipped" || assessment.Gate.Required {
			t.Fatalf("gate = %+v, want non-required skipped", assessment.Gate)
		}
	})
}

func TestToolAllowlistGatePassesForCompliantAttempt(t *testing.T) {
	runDirectory, specDigest := writeAllowlistFixture(t, []string{"read", "edit", "write"}, true, map[string][]string{"pi": {"read", "edit"}, "fake": {"write"}})
	assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Gate.Required || assessment.Gate.Status != "pass" {
		t.Fatalf("gate = %+v, want required pass", assessment.Gate)
	}
	if assessment.Artifact != nil {
		t.Fatalf("passing gate must not persist violation evidence: %+v", assessment.Artifact)
	}
}

func TestToolAllowlistGateFailsForUndeclaredSuccessfulTool(t *testing.T) {
	runDirectory, specDigest := writeAllowlistFixture(t, []string{"read", "edit", "write"}, true, map[string][]string{"pi": {"read", "grep"}})
	assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
		t.Fatalf("gate = %+v, want required fail", assessment.Gate)
	}
	if !strings.Contains(assessment.Gate.Summary, "grep") {
		t.Fatalf("summary must list the violation: %q", assessment.Gate.Summary)
	}
	if assessment.Artifact == nil || len(assessment.Gate.Evidence) != 1 {
		t.Fatalf("violation evidence not registered: %+v", assessment)
	}
	data, err := os.ReadFile(filepath.Join(runDirectory, toolAllowlistEvidenceFileName))
	if err != nil {
		t.Fatalf("violation evidence file missing: %v", err)
	}
	var evidence struct {
		Attempt    string   `json:"attempt"`
		Declared   []string `json:"declared"`
		Observed   []string `json:"observed"`
		Violations []string `json:"violations"`
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Attempt != "attempt-1" || len(evidence.Violations) != 1 || evidence.Violations[0] != "grep" {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestToolAllowlistGateFailsClosedOnEvidenceGaps(t *testing.T) {
	declared := []string{"read", "edit"}
	t.Run("missing-toolNames-field", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, declared, true, map[string][]string{"pi": nil})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" || !strings.Contains(assessment.Gate.Summary, "toolNames") {
			t.Fatalf("gate = %+v, want required fail on missing toolNames", assessment.Gate)
		}
	})
	t.Run("missing-metas", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, declared, true, nil)
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on missing metas", assessment.Gate)
		}
	})
	t.Run("missing-attempt", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, declared, false, nil)
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on missing attempt", assessment.Gate)
		}
	})
	t.Run("malformed-meta", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, declared, true, map[string][]string{"pi": {"read"}})
		outputDir := filepath.Join(runDirectory, "attempts", "attempt-1", "control", "output")
		if err := os.WriteFile(filepath.Join(outputDir, "broken-transcript-meta.json"), []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on malformed meta", assessment.Gate)
		}
	})
	t.Run("spec-digest-mismatch", func(t *testing.T) {
		runDirectory, _ := writeAllowlistFixture(t, declared, true, map[string][]string{"pi": {"read"}})
		assessment, err := assessToolAllowlist(runDirectory, "sha256:"+strings.Repeat("b", 64), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on digest mismatch", assessment.Gate)
		}
	})
	t.Run("invalid-declaration", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, []string{"read", "read"}, true, map[string][]string{"pi": {"read"}})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on duplicated declaration", assessment.Gate)
		}
	})
	t.Run("malformed-denial-log", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, declared, true, map[string][]string{"pi": {"read"}})
		outputDir := filepath.Join(runDirectory, "attempts", "attempt-1", "control", "output")
		if err := os.WriteFile(filepath.Join(outputDir, "denials.jsonl"), []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" {
			t.Fatalf("gate = %+v, want required fail on malformed denial log", assessment.Gate)
		}
	})
}

func TestToolAllowlistGateNormalizesProviderVocabularies(t *testing.T) {
	t.Run("qwen-read-file-normalizes-to-read", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, []string{"read"}, true, map[string][]string{"qwen": {"read_file"}})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "pass" {
			t.Fatalf("gate = %+v, want required pass after normalization", assessment.Gate)
		}
	})
	t.Run("qwen-shell-normalizes-to-bash-violation", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, []string{"read"}, true, map[string][]string{"qwen": {"read_file", "shell"}})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" || !strings.Contains(assessment.Gate.Summary, "bash") {
			t.Fatalf("gate = %+v, want required fail listing bash", assessment.Gate)
		}
	})
	t.Run("off-table-tool-stays-undeclared", func(t *testing.T) {
		runDirectory, specDigest := writeAllowlistFixture(t, []string{"read", "edit", "write", "grep", "find", "ls", "bash"}, true, map[string][]string{"opencode": {"mystery_tool"}})
		assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Gate.Required || assessment.Gate.Status != "fail" || !strings.Contains(assessment.Gate.Summary, "mystery_tool") {
			t.Fatalf("gate = %+v, want required fail listing mystery_tool", assessment.Gate)
		}
	})
}

func TestToolAllowlistGateCrossesDenialLog(t *testing.T) {
	runDirectory, specDigest := writeAllowlistFixture(t, []string{"read"}, true, map[string][]string{"pi": {"read"}})
	outputDir := filepath.Join(runDirectory, "attempts", "attempt-1", "control", "output")
	denialLine := `{"seq":1,"tool":"grep","kind":"read","path-or-cmd":"/x","grade":"BENIGN","reason":"probe","at":"2026-08-12T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(outputDir, "denials.jsonl"), []byte(denialLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assessment, err := assessToolAllowlist(runDirectory, specDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// The denied grep probe must not fail the gate; the cross is reported.
	if !assessment.Gate.Required || assessment.Gate.Status != "pass" {
		t.Fatalf("gate = %+v, want required pass with denial cross", assessment.Gate)
	}
	if !strings.Contains(assessment.Gate.Summary, "1 条拒绝记录") {
		t.Fatalf("summary must report the denial cross: %q", assessment.Gate.Summary)
	}
}
