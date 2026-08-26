//go:build marshal_r3_cap3_acceptance

package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const cap3CanarySecret = "cap3-secret-do-not-leak"

type cap3RunResult struct {
	State            domain.RunState `json:"state"`
	AttemptID        string          `json:"attemptId"`
	WorkerResult     json.RawMessage `json:"workerResult"`
	CapabilityShadow map[string]any  `json:"capabilityShadow"`
}

// TestTaskRunAgentMatchShadowEmitsDeterministicObservation is a protected
// acceptance gate for Issue #195 / #207 CAP-3. Normal repository tests do not
// enable this build tag. A delivery Task must run it with baselinePolicy=always
// and deny changes to this file: the locked baseline must fail because the
// real task-run JSON has no capabilityShadow, while the candidate must expose
// a deterministic, observe-only projection without changing the Run outcome.
func TestTaskRunAgentMatchShadowEmitsDeterministicObservation(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID = "r3-cap3-shadow-task"
	t.Setenv("MARSHAL_R3_CAP3_CANARY_SECRET", cap3CanarySecret)

	run := func(runID, shadowMode string) cap3RunResult {
		t.Helper()
		t.Setenv("MARSHAL_AGENT_MATCH_SHADOW", shadowMode)
		setup.planAndApprove(t, taskID, runID, true)
		var stdout, stderr bytes.Buffer
		exit := Run([]string{"task", "run", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			t.Fatalf("task run exit = %d, stderr = %s", exit, stderr.String())
		}
		var result cap3RunResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode task run JSON: %v\n%s", err, stdout.String())
		}
		if result.State.State != domain.StateVerifying {
			t.Fatalf("state = %s, want VERIFYING", result.State.State)
		}
		if result.AttemptID == "" || len(result.WorkerResult) == 0 {
			t.Fatalf("legacy result missing attempt or WorkerResult")
		}
		return result
	}

	off := run("r3-cap3-shadow-run-off", "")
	if off.CapabilityShadow != nil {
		t.Fatalf("shadow-off run emitted capabilityShadow")
	}
	first := run("r3-cap3-shadow-run-a", "1")
	second := run("r3-cap3-shadow-run-b", "1")
	if first.CapabilityShadow == nil || second.CapabilityShadow == nil {
		t.Fatalf("Run reached VERIFYING, but capabilityShadow is missing")
	}
	// The opt-in shadow may add only an observation. The legacy state and
	// successful WorkerResult transport must remain identical in shape.
	if off.State.State != first.State.State || first.State.State != second.State.State {
		t.Fatalf("shadow changed legacy Run state: off=%s first=%s second=%s", off.State.State, first.State.State, second.State.State)
	}
	assertCAP3WorkerResultShape(t, off.WorkerResult, first.WorkerResult, second.WorkerResult)

	firstBody := assertCAP3Observation(t, setup.repositoryRoot, first.CapabilityShadow)
	secondBody := assertCAP3Observation(t, setup.repositoryRoot, second.CapabilityShadow)
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("capabilityShadow canonical body is not deterministic")
	}
}

func assertCAP3WorkerResultShape(t *testing.T, results ...json.RawMessage) {
	t.Helper()
	var wantKeys []string
	for i, raw := range results {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode WorkerResult %d: %v", i, err)
		}
		keys := sortedCAP3Keys(body)
		if i == 0 {
			wantKeys = keys
			continue
		}
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Fatalf("shadow changed WorkerResult shape: got %v, want %v", keys, wantKeys)
		}
	}
}

func assertCAP3Observation(t *testing.T, repositoryRoot string, observation map[string]any) []byte {
	t.Helper()
	expectedKeys := []string{
		"capabilityDigest",
		"comparisonScope",
		"evaluatorVersion",
		"legacyAccepted",
		"mappedRegistrationDigest",
		"mappedSnapshotDigest",
		"mode",
		"observationDigest",
		"requirementDigest",
		"shadowMatched",
		"shadowReason",
		"sourceKind",
		"specDigest",
	}
	if got := sortedCAP3Keys(observation); !reflect.DeepEqual(got, expectedKeys) {
		t.Fatalf("capabilityShadow keys = %v, want exact closed set %v", got, expectedKeys)
	}
	for key, want := range map[string]string{
		"mode":             "shadow-observe-only",
		"sourceKind":       "legacy-mapped",
		"comparisonScope":  "overlap-only",
		"evaluatorVersion": "agent-match-shadow/v1",
		"shadowReason":     "matched",
	} {
		if got, ok := observation[key].(string); !ok || got != want {
			t.Fatalf("capabilityShadow.%s = %#v, want %q", key, observation[key], want)
		}
	}
	for _, key := range []string{"legacyAccepted", "shadowMatched"} {
		if got, ok := observation[key].(bool); !ok || !got {
			t.Fatalf("capabilityShadow.%s = %#v, want true", key, observation[key])
		}
	}
	for _, key := range []string{
		"specDigest",
		"capabilityDigest",
		"requirementDigest",
		"mappedRegistrationDigest",
		"mappedSnapshotDigest",
		"observationDigest",
	} {
		assertCAP3SHA256(t, key, observation[key])
	}

	serialized, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(serialized))
	for _, forbidden := range []string{
		repositoryRoot,
		filepath.Join(repositoryRoot, ".marshal"),
		os.TempDir(),
		cap3CanarySecret,
		"/private/",
		"/var/folders/",
		".marshal",
		"apikey",
		"credential",
		"token",
		"transcript",
		"rawerror",
		"executable",
		"repositorypath",
	} {
		if forbidden != "" && strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("capabilityShadow leaked a forbidden path or sensitive field family")
		}
	}

	reported := observation["observationDigest"].(string)
	body := make(map[string]any, len(observation)-1)
	for key, value := range observation {
		if key != "observationDigest" {
			body[key] = value
		}
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBody, err := canonical.JSON(rawBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := canonical.DigestBytes(canonicalBody); got != reported {
		t.Fatalf("observationDigest does not bind the canonical closed observation body")
	}
	return canonicalBody
}

func assertCAP3SHA256(t *testing.T, key string, value any) {
	t.Helper()
	digest, ok := value.(string)
	if !ok || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("capabilityShadow.%s is not a sha256 digest", key)
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		t.Fatalf("capabilityShadow.%s is not lowercase sha256 hex", key)
	}
}

func sortedCAP3Keys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
