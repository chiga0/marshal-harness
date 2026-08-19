package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
)

func TestDenialSummaryGatePassesWithoutAttemptEvidence(t *testing.T) {
	fixture := newVerificationFixture(t)
	result, err := New().Verify(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "pass" || result.Report.Status != "pass" {
		t.Fatalf("denial gate = %s, report = %s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
	if strings.Contains(result.Report.Summary, "permission 拒绝分级") {
		t.Fatalf("summary must stay silent without denial evidence: %s", result.Report.Summary)
	}
}

func TestDenialSummaryGateReportsBenignCounts(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:old", 1)
	writeDenialLog(t, filepath.Join(attempt, "control", "output"), denialRecord(1, "read", "read", "/x/source.go", string(denials.Benign)))
	writeTranscriptMeta(t, filepath.Join(attempt, "control", "output"), false, 1, 0)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "pass" || result.Report.Status != "pass" {
		t.Fatalf("denial gate = %s, report = %s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
	if !strings.Contains(result.Report.Summary, "benign=1") || !strings.Contains(result.Report.Summary, "fatal=0") {
		t.Fatalf("summary lost benign count: %s", result.Report.Summary)
	}
	copied, err := os.ReadFile(filepath.Join(input.RunDirectory, denials.LogFileName))
	if err != nil {
		t.Fatalf("denial evidence not persisted into run directory: %v", err)
	}
	if records, err := denials.ParseLog(copied); err != nil || len(records) != 1 || records[0].Grade != string(denials.Benign) {
		t.Fatalf("copied denial evidence = %s err=%v", copied, err)
	}
	var artifactFound bool
	for _, artifact := range result.Manifest.Artifacts {
		if artifact.ID == "evidence:denial-log" {
			artifactFound = true
			if artifact.RelativePath != denials.LogFileName || artifact.Status != "validated" || artifact.ByteSize == 0 || artifact.Digest == "" {
				t.Fatalf("denial evidence artifact = %+v", artifact)
			}
		}
	}
	if !artifactFound {
		t.Fatalf("manifest lost denial evidence artifact: %+v", result.Manifest.Artifacts)
	}
}

func TestDenialSummaryGateFailsOnFatalDenials(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:fatal", 1)
	writeDenialLog(t, filepath.Join(attempt, "control", "output"), denialRecord(1, "bash", "execute", "curl http://evil.example", string(denials.Fatal)))
	writeTranscriptMeta(t, filepath.Join(attempt, "control", "output"), true, 0, 1)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "fail" || result.Report.Status != "fail" {
		t.Fatalf("fatal denials must fail verification: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func TestDenialSummaryGateFailsOnInconsistentPermissionState(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:inconsistent", 1)
	writeDenialLog(t, filepath.Join(attempt, "control", "output"), denialRecord(1, "read", "read", "/x/source.go", string(denials.Benign)))
	writeTranscriptMeta(t, filepath.Join(attempt, "control", "output"), true, 0, 1)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "fail" || result.Report.Status != "fail" {
		t.Fatalf("inconsistent permissionDenied state must fail: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func TestDenialSummaryGateErrorsWhenTranscriptClaimsDenialWithoutLog(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:missing-log", 1)
	writeTranscriptMeta(t, filepath.Join(attempt, "control", "output"), true, 0, 1)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "error" || result.Report.Status != "error" {
		t.Fatalf("missing denial log must error when transcript claims a denial: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func TestDenialSummaryGateFailsOnCountMismatch(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:count-mismatch", 1)
	writeDenialLog(t, filepath.Join(attempt, "control", "output"), denialRecord(1, "read", "read", "/x/source.go", string(denials.Benign)))
	writeTranscriptMeta(t, filepath.Join(attempt, "control", "output"), false, 2, 0)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "fail" || result.Report.Status != "fail" {
		t.Fatalf("denial count mismatch must fail: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func TestDenialSummaryGateErrorsOnMalformedLog(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	attempt := newDenialAttemptFixture(t, input.RunDirectory, "attempt:bad", 1)
	outputDir := filepath.Join(attempt, "control", "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, denials.LogFileName), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "error" || result.Report.Status != "error" {
		t.Fatalf("malformed denial evidence must error: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func TestDenialSummaryGateReadsOnlyTheNewestAttempt(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	superseded := newDenialAttemptFixture(t, input.RunDirectory, "attempt:one", 1)
	writeDenialLog(t, filepath.Join(superseded, "control", "output"), denialRecord(1, "bash", "execute", "sudo true", string(denials.Fatal)))
	writeTranscriptMeta(t, filepath.Join(superseded, "control", "output"), true, 0, 1)
	current := newDenialAttemptFixture(t, input.RunDirectory, "attempt:two", 2)
	writeDenialLog(t, filepath.Join(current, "control", "output"), denialRecord(1, "read", "read", "/x/inside.go", string(denials.Benign)))
	writeTranscriptMeta(t, filepath.Join(current, "control", "output"), false, 1, 0)
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if gateStatus(result.Report.Gates, denialGateID) != "pass" || result.Report.Status != "pass" {
		t.Fatalf("superseded attempt evidence leaked into the gate: gate=%s report=%s", gateStatus(result.Report.Gates, denialGateID), result.Report.Status)
	}
}

func newDenialAttemptFixture(t *testing.T, runDirectory, attemptID string, attemptNumber int) string {
	t.Helper()
	attemptDir := filepath.Join(runDirectory, "attempts", attemptID)
	if err := os.MkdirAll(filepath.Join(attemptDir, "control", "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "attemptId": attemptID, "attemptNumber": attemptNumber})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), append(request, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return attemptDir
}

func denialRecord(seq int, tool, kind, target, grade string) denials.Record {
	return denials.Record{Seq: seq, Tool: tool, Kind: kind, PathOrCmd: target, Grade: grade, Reason: "fixture", At: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
}

func writeDenialLog(t *testing.T, outputDir string, records ...denials.Record) {
	t.Helper()
	if err := denials.AppendLog(filepath.Join(outputDir, denials.LogFileName), records); err != nil {
		t.Fatal(err)
	}
}

func writeTranscriptMeta(t *testing.T, outputDir string, permissionDenied bool, benign, fatal int) {
	t.Helper()
	meta, err := json.Marshal(map[string]any{
		"eventCount":       1,
		"permissionDenied": permissionDenied,
		"denialsBenign":    benign,
		"denialsFatal":     fatal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "opencode-transcript-meta.json"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
