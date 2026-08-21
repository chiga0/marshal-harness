package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestStablePlanPremortemCommandRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "plan-premortem-check", "extra"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d stderr=%q", exit, ExitUsage, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "core-probe-usage-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStableReviewFreshnessCommandRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "review-freshness-check", "extra"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d stderr=%q", exit, ExitUsage, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "checker-arguments-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStableCodexSchemaCommandRejectsOversizeInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := strings.Repeat("x", codexSchemaCheckMaxInputBytes+1)
	if exit := Run([]string{"internal", "codex-provider-schema-check"}, strings.NewReader(input), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d", exit, ExitUsage)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "codex-provider-checker-input-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
