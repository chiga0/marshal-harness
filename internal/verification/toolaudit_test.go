package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// stageToolAuditAttempt stages one attempt directory beneath runDirectory
// with a worker-request.json recording its attempt number and, when
// toolNames is non-nil, a fake adapter transcript-meta carrying those tool
// names. It mirrors the attempt layout latestAttemptDirectory consumes.
func stageToolAuditAttempt(t *testing.T, runDirectory, attemptName string, attemptNumber int, toolNames []string) string {
	t.Helper()
	attemptDir := filepath.Join(runDirectory, "attempts", attemptName)
	outputDir := filepath.Join(attemptDir, "control", "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), []byte(fmt.Sprintf("{\"attemptNumber\":%d}\n", attemptNumber)), 0o600); err != nil {
		t.Fatal(err)
	}
	if toolNames != nil {
		writeToolAuditTranscriptMeta(t, outputDir, "fake-transcript-meta.json", toolNames)
	}
	return attemptDir
}

func writeToolAuditTranscriptMeta(t *testing.T, outputDir, name string, toolNames []string) {
	t.Helper()
	metaData, err := json.Marshal(map[string]any{"toolNames": toolNames})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, name), metaData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssessToolAudit(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		allowlist    []string
		toolNames    []string
		noAttempt    bool
		wantStatus   string
		wantRequired bool
		wantSummary  string
		wantEvidence []string
	}{
		{
			name:         "compliant-calls-pass",
			allowlist:    []string{"read", "edit", "write"},
			toolNames:    []string{"read", "write"},
			wantStatus:   "pass",
			wantRequired: true,
			wantSummary:  "tool-audit：2 个成功工具调用均在 worker.tools allowlist 内（allowlist：read、edit、write）",
			wantEvidence: []string{},
		},
		{
			name:         "unauthorized-call-fails-with-evidence",
			allowlist:    []string{"read", "edit"},
			toolNames:    []string{"read", "bash"},
			wantStatus:   "fail",
			wantRequired: true,
			wantSummary:  "tool-audit：1 个成功工具调用越权：bash（allowlist：read、edit）",
			wantEvidence: []string{"越权工具：bash"},
		},
		{
			name:         "violations-deduplicated-and-sorted",
			allowlist:    []string{"read"},
			toolNames:    []string{"write", "bash", "write"},
			wantStatus:   "fail",
			wantRequired: true,
			wantSummary:  "tool-audit：2 个成功工具调用越权：bash、write（allowlist：read）",
			wantEvidence: []string{"越权工具：bash", "越权工具：write"},
		},
		{
			name:         "undeclared-allowlist-skipped",
			allowlist:    nil,
			toolNames:    []string{"read", "bash"},
			wantStatus:   "skipped",
			wantRequired: false,
			wantSummary:  "tool-audit：TaskSpec 未声明 worker.tools，跳过对账",
			wantEvidence: []string{},
		},
		{
			name:         "declared-but-meta-missing-fails-closed",
			allowlist:    []string{"read"},
			toolNames:    nil,
			wantStatus:   "fail",
			wantRequired: true,
			wantSummary:  "tool-audit：声明了 worker.tools 但缺少 transcript-meta 证据，fail-closed",
			wantEvidence: []string{},
		},
		{
			name:         "declared-without-attempt-directory-fails-closed",
			allowlist:    []string{"read"},
			toolNames:    nil,
			noAttempt:    true,
			wantStatus:   "fail",
			wantRequired: true,
			wantSummary:  "tool-audit：声明了 worker.tools 但没有 Attempt 证据，fail-closed",
			wantEvidence: []string{},
		},
		{
			name:         "empty-observed-passes",
			allowlist:    []string{"read"},
			toolNames:    []string{},
			wantStatus:   "pass",
			wantRequired: true,
			wantSummary:  "tool-audit：0 个成功工具调用均在 worker.tools allowlist 内（allowlist：read）",
			wantEvidence: []string{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			attemptDir := ""
			if !testCase.noAttempt {
				attemptDir = stageToolAuditAttempt(t, t.TempDir(), "attempt-1", 1, testCase.toolNames)
			}
			gate := assessToolAudit(Input{ToolAllowlist: testCase.allowlist}, attemptDir)
			if gate.ID != "tool-audit" || gate.Category != "policy" {
				t.Fatalf("gate identity = %+v", gate)
			}
			if gate.Status != testCase.wantStatus || gate.Required != testCase.wantRequired {
				t.Fatalf("gate = %+v, want status %s required %v", gate, testCase.wantStatus, testCase.wantRequired)
			}
			if gate.Summary != testCase.wantSummary {
				t.Fatalf("summary = %q, want %q", gate.Summary, testCase.wantSummary)
			}
			if !reflect.DeepEqual(gate.Evidence, testCase.wantEvidence) {
				t.Fatalf("evidence = %+v, want %+v", gate.Evidence, testCase.wantEvidence)
			}
		})
	}
}

func TestAssessToolAuditSelectsLatestAttemptLikeDenialSummary(t *testing.T) {
	t.Run("latest-compliant-passes-despite-superseded-violation", func(t *testing.T) {
		runDirectory := t.TempDir()
		stageToolAuditAttempt(t, runDirectory, "attempt-superseded", 1, []string{"bash"})
		stageToolAuditAttempt(t, runDirectory, "attempt-latest", 2, []string{"read"})
		attemptDir, attemptName, ok := latestAttemptDirectory(filepath.Join(runDirectory, "attempts"))
		if !ok || attemptName != "attempt-latest" {
			t.Fatalf("latest attempt = %q ok=%v, want attempt-latest", attemptName, ok)
		}
		gate := assessToolAudit(Input{ToolAllowlist: []string{"read"}}, attemptDir)
		if gate.Status != "pass" || gate.Summary != "tool-audit：1 个成功工具调用均在 worker.tools allowlist 内（allowlist：read）" {
			t.Fatalf("gate = %+v", gate)
		}
	})
	t.Run("latest-violation-fails-despite-compliant-history", func(t *testing.T) {
		runDirectory := t.TempDir()
		stageToolAuditAttempt(t, runDirectory, "attempt-superseded", 1, []string{"read"})
		stageToolAuditAttempt(t, runDirectory, "attempt-latest", 2, []string{"bash"})
		attemptDir, _, ok := latestAttemptDirectory(filepath.Join(runDirectory, "attempts"))
		if !ok {
			t.Fatal("latest attempt missing")
		}
		gate := assessToolAudit(Input{ToolAllowlist: []string{"read"}}, attemptDir)
		if gate.Status != "fail" || !reflect.DeepEqual(gate.Evidence, []string{"越权工具：bash"}) {
			t.Fatalf("gate = %+v", gate)
		}
	})
}

func TestAssessToolAuditAggregatesAllTranscriptMetasOfLatestAttempt(t *testing.T) {
	runDirectory := t.TempDir()
	attemptDir := stageToolAuditAttempt(t, runDirectory, "attempt-1", 1, []string{"read"})
	writeToolAuditTranscriptMeta(t, filepath.Join(attemptDir, "control", "output"), "zzz-transcript-meta.json", []string{"edit", "bash"})
	gate := assessToolAudit(Input{ToolAllowlist: []string{"read", "edit"}}, attemptDir)
	if gate.Status != "fail" || gate.Summary != "tool-audit：1 个成功工具调用越权：bash（allowlist：read、edit）" || !reflect.DeepEqual(gate.Evidence, []string{"越权工具：bash"}) {
		t.Fatalf("gate = %+v", gate)
	}
}

func TestAssessToolAuditFailsClosedOnMalformedEvidence(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		metaContent  string
		wantSummary  string
		wantContains string
	}{
		{
			name:        "missing-toolnames-field",
			metaContent: `{"adapterId":"fake"}`,
			wantSummary: "tool-audit：fake-transcript-meta.json：transcript 元数据缺少 toolNames 字段",
		},
		{
			name:        "null-toolnames",
			metaContent: `{"toolNames":null}`,
			wantSummary: "tool-audit：fake-transcript-meta.json：toolNames 字段缺失",
		},
		{
			name:         "invalid-json",
			metaContent:  "{",
			wantContains: "tool-audit：fake-transcript-meta.json：transcript 元数据无效：",
		},
		{
			name:         "non-string-array",
			metaContent:  `{"toolNames":[1,2]}`,
			wantContains: "tool-audit：fake-transcript-meta.json：toolNames 不是字符串数组：",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDirectory := t.TempDir()
			attemptDir := filepath.Join(runDirectory, "attempts", "attempt-1")
			outputDir := filepath.Join(attemptDir, "control", "output")
			if err := os.MkdirAll(outputDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), []byte("{\"attemptNumber\":1}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outputDir, "fake-transcript-meta.json"), []byte(testCase.metaContent), 0o600); err != nil {
				t.Fatal(err)
			}
			gate := assessToolAudit(Input{ToolAllowlist: []string{"read"}}, attemptDir)
			if gate.Status != "fail" || !gate.Required {
				t.Fatalf("gate = %+v", gate)
			}
			if testCase.wantSummary != "" && gate.Summary != testCase.wantSummary {
				t.Fatalf("summary = %q, want %q", gate.Summary, testCase.wantSummary)
			}
			if testCase.wantContains != "" && !strings.Contains(gate.Summary, testCase.wantContains) {
				t.Fatalf("summary = %q, want contains %q", gate.Summary, testCase.wantContains)
			}
		})
	}
}

func TestVerifyAppliesToolAuditGateEndToEnd(t *testing.T) {
	stageAttempt := func(t *testing.T, fixture verificationFixture, tools, toolNames []string) Input {
		t.Helper()
		attemptDir := filepath.Join(fixture.runDirectory, "attempts", "attempt-1")
		outputDir := filepath.Join(attemptDir, "control", "output")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), []byte("{\"attemptNumber\":1}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if toolNames != nil {
			writeToolAuditTranscriptMeta(t, outputDir, "fake-transcript-meta.json", toolNames)
		}
		var task domain.TaskSpec
		task.Worker.Tools = tools
		input := fixture.input()
		input.ToolAllowlist = ToolAllowlistFromTask(task)
		input.Scope = ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
		return input
	}
	t.Run("compliant-declaration-passes", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read", "edit"}, []string{"read"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "pass" || gateStatus(result.Report.Gates, "tool-audit") != "pass" {
			t.Fatalf("report = %+v", result.Report)
		}
		denialIndex, auditIndex := -1, -1
		for index := range result.Report.Gates {
			switch result.Report.Gates[index].ID {
			case "denial-summary":
				denialIndex = index
			case "tool-audit":
				auditIndex = index
				if gate := result.Report.Gates[index]; !gate.Required || gate.Category != "policy" {
					t.Fatalf("tool-audit gate = %+v", gate)
				}
			}
		}
		if denialIndex < 0 || auditIndex != denialIndex+1 {
			t.Fatalf("tool-audit must sit directly after denial-summary: denial=%d audit=%d", denialIndex, auditIndex)
		}
	})
	t.Run("violation-fails-report", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read", "edit"}, []string{"read", "bash"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "tool-audit") != "fail" {
			t.Fatalf("report = %+v", result.Report)
		}
		for _, gate := range result.Report.Gates {
			if gate.ID == "tool-audit" && !reflect.DeepEqual(gate.Evidence, []string{"越权工具：bash"}) {
				t.Fatalf("tool-audit evidence = %+v", gate.Evidence)
			}
		}
	})
	t.Run("undeclared-keeps-report-green", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, nil, []string{"read", "bash"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "pass" || gateStatus(result.Report.Gates, "tool-audit") != "skipped" {
			t.Fatalf("undeclared runs must keep tool-audit skipped and the report green: %+v", result.Report)
		}
	})
	t.Run("declared-but-evidence-missing-fails-closed", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read"}, nil)
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "tool-audit") != "fail" {
			t.Fatalf("missing evidence must fail closed: %+v", result.Report)
		}
	})
}
