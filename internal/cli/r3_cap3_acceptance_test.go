//go:build marshal_r3_cap3_acceptance

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// TestTaskRunAgentMatchShadowEmitsDeterministicObservation is a protected
// acceptance gate for Issue #195 / #207 CAP-3. Normal repository tests do not
// enable this build tag. A delivery Task must run it with baselinePolicy=always
// and deny changes to this file: the locked baseline must fail because the
// real task-run JSON has no capabilityShadow, while the candidate must expose
// a deterministic, observe-only projection without changing the Run outcome.
func TestTaskRunAgentMatchShadowEmitsDeterministicObservation(t *testing.T) {
	t.Setenv("MARSHAL_AGENT_MATCH_SHADOW", "1")
	setup := newAutoFlowSetup(t)
	const taskID = "r3-cap3-shadow-task"

	run := func(runID string) map[string]any {
		t.Helper()
		setup.planAndApprove(t, taskID, runID, true)
		var stdout, stderr bytes.Buffer
		exit := Run([]string{"task", "run", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			t.Fatalf("task run exit = %d, stderr = %s", exit, stderr.String())
		}
		var result struct {
			State            domain.RunState `json:"state"`
			CapabilityShadow map[string]any  `json:"capabilityShadow"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode task run JSON: %v\n%s", err, stdout.String())
		}
		if result.State.State != domain.StateVerifying {
			t.Fatalf("state = %s, want VERIFYING", result.State.State)
		}
		if result.CapabilityShadow == nil {
			t.Fatalf("Run reached VERIFYING, but capabilityShadow is missing")
		}
		return result.CapabilityShadow
	}

	first := run("r3-cap3-shadow-run-a")
	second := run("r3-cap3-shadow-run-b")

	for key, want := range map[string]string{
		"mode":             "shadow-observe-only",
		"sourceKind":       "legacy-mapped",
		"comparisonScope":  "overlap-only",
		"evaluatorVersion": "agent-match-shadow/v1",
	} {
		if got, _ := first[key].(string); got != want {
			t.Fatalf("capabilityShadow.%s = %q, want %q", key, got, want)
		}
	}
	firstDigest, _ := first["observationDigest"].(string)
	secondDigest, _ := second["observationDigest"].(string)
	if firstDigest == "" || firstDigest != secondDigest {
		t.Fatalf("observationDigest = %q then %q, want equal non-empty digests", firstDigest, secondDigest)
	}
	if legacyAccepted, ok := first["legacyAccepted"].(bool); !ok || !legacyAccepted {
		t.Fatalf("legacyAccepted = %#v, want true", first["legacyAccepted"])
	}
	if _, ok := first["shadowMatched"].(bool); !ok {
		t.Fatalf("shadowMatched = %#v, want boolean", first["shadowMatched"])
	}
	if reason, _ := first["shadowReason"].(string); reason == "" {
		t.Fatalf("shadowReason = %#v, want closed non-empty reason", first["shadowReason"])
	}
	for _, forbidden := range []string{"executable", "repositoryPath", "environment", "credential", "token", "transcript", "rawError"} {
		if _, exists := first[forbidden]; exists {
			t.Fatalf("capabilityShadow leaked forbidden field %q", forbidden)
		}
	}
}
