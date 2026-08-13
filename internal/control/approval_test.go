package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

type approvalFixture struct {
	root      string
	runDir    string
	runID     string
	taskID    string
	validator *contract.Validator
}

func TestPlanApprovalLifecycle(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	input := fixture.input(domain.ApprovalGatePlan)
	if err := Require(input); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Require() error = %v", err)
	}
	record, err := Approve(input)
	if err != nil {
		t.Fatal(err)
	}
	if record.ControlSequence != 1 || record.Gate != domain.ApprovalGatePlan || record.Binding.StateSequence != 2 ||
		record.TaskID != fixture.taskID || record.RunID != fixture.runID || record.Source.Type != domain.ControlSourceTypeHuman {
		t.Fatalf("ApprovalRecord = %+v", record)
	}
	if err := Require(input); err != nil {
		t.Fatalf("Require() after approval = %v", err)
	}
}

func TestAutonomousAndLegacyApprovalPolicy(t *testing.T) {
	t.Parallel()
	t.Run("autonomous", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, func(policy map[string]any) {
			control := policy["control"].(map[string]any)
			control["autonomyProfile"] = "autonomous"
			control["requiredApprovals"] = []any{}
		}, false)
		input := fixture.input(domain.ApprovalGatePlan)
		if err := Require(input); err != nil {
			t.Fatalf("autonomous Require() = %v", err)
		}
		if _, err := Approve(input); !errors.Is(err, ErrApprovalNotRequired) {
			t.Fatalf("autonomous Approve() = %v", err)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, func(policy map[string]any) { delete(policy, "control") }, false)
		if err := Require(fixture.input(domain.ApprovalGatePlan)); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("legacy Require() = %v", err)
		}
	})
}

func TestStalePlanApprovalCannotPass(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"state sequence", func(record map[string]any) { record["binding"].(map[string]any)["stateSequence"] = float64(1) }},
		{"spec digest", func(record map[string]any) {
			record["binding"].(map[string]any)["specDigest"] = "sha256:" + strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newApprovalFixture(t, nil, false)
			input := fixture.input(domain.ApprovalGatePlan)
			if _, err := Approve(input); err != nil {
				t.Fatal(err)
			}
			mutateFirstApproval(t, fixture, test.mutate)
			if err := Require(input); !errors.Is(err, ErrApprovalStale) {
				t.Fatalf("Require() stale error = %v", err)
			}
		})
	}
}

func TestPublishApprovalBindsReviewEvidence(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, true)
	input := fixture.input(domain.ApprovalGatePublish)
	record, err := Approve(input)
	if err != nil {
		t.Fatal(err)
	}
	if record.Binding.ReviewRound != 1 || record.Binding.DecisionDigest == "" || record.Binding.EvidenceDigest == "" {
		t.Fatalf("publish binding = %+v", record.Binding)
	}
	if err := Require(input); err != nil {
		t.Fatal(err)
	}
	decisionPath := filepath.Join(fixture.runDir, "decisions", "decision-001.json")
	decision := readObject(t, decisionPath)
	decision["summary"] = "新的、仍然有效但未经审批的 Review 摘要。"
	writeObject(t, decisionPath, decision)
	if err := Require(input); !errors.Is(err, ErrApprovalStale) {
		t.Fatalf("Require() after decision rotation = %v", err)
	}
}

func TestPublishApprovalReadsRoundBoundDecisionFile(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, true)
	decisionPath := filepath.Join(fixture.runDir, "decisions", "decision-001.json")
	decisionData, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	decision := decodeObject(t, decisionData)
	if decision["reviewRound"] != float64(1) || decision["verdict"] != "accept" ||
		decision["publicationRecommendation"] != "publish" || decision["specDigest"] != mustDigest(t, readSchemaFixture(t, "examples/happy-path/task-spec.json")) {
		t.Fatalf("fixture decision = %+v", decision)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-decision.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy review-decision.json should not exist, stat error = %v", err)
	}
	expectedDigest := mustDigest(t, decisionData)
	record, err := Approve(fixture.input(domain.ApprovalGatePublish))
	if err != nil {
		t.Fatalf("Approve() with round-bound decision = %v", err)
	}
	if record.Binding.ReviewRound != 1 || record.Binding.DecisionDigest != expectedDigest ||
		record.Binding.EvidenceDigest == "" || record.Gate != domain.ApprovalGatePublish {
		t.Fatalf("publish binding = %+v", record.Binding)
	}
}

func TestApprovalFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("wrong state", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, nil, false)
		if _, err := Approve(fixture.input(domain.ApprovalGatePublish)); !errors.Is(err, ErrInvalidApprovalState) {
			t.Fatalf("Approve() wrong state = %v", err)
		}
	})
	t.Run("invalid gate and source", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, nil, false)
		input := fixture.input("deploy")
		if _, err := Approve(input); !errors.Is(err, ErrInvalidControlInput) {
			t.Fatalf("invalid gate = %v", err)
		}
		input = fixture.input(domain.ApprovalGatePlan)
		input.SourceID = ""
		if _, err := Approve(input); !errors.Is(err, ErrInvalidControlInput) {
			t.Fatalf("invalid source = %v", err)
		}
	})
	t.Run("corrupt journal", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, nil, false)
		controlDir := filepath.Join(fixture.runDir, "control")
		if err := os.MkdirAll(controlDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(controlDir, "records.jsonl"), []byte("{bad}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Approve(fixture.input(domain.ApprovalGatePlan)); err == nil {
			t.Fatal("Approve() accepted corrupt journal")
		}
	})
	t.Run("frozen digest mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, nil, false)
		path := filepath.Join(fixture.runDir, "task-spec.json")
		task := readObject(t, path)
		task["work"].(map[string]any)["objective"] = "被篡改的目标"
		writeObject(t, path, task)
		if _, err := Approve(fixture.input(domain.ApprovalGatePlan)); !errors.Is(err, ErrInvalidControlInput) {
			t.Fatalf("Approve() digest mismatch = %v", err)
		}
	})
}

func TestApprovalOnlyAppendsControlJournal(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	paths := []string{"events.jsonl", "state.json", "task-spec.json", "policy-snapshot.json", "capability-snapshot.json"}
	before := map[string][]byte{}
	for _, name := range paths {
		data, err := os.ReadFile(filepath.Join(fixture.runDir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	if _, err := Approve(fixture.input(domain.ApprovalGatePlan)); err != nil {
		t.Fatal(err)
	}
	for _, name := range paths {
		after, err := os.ReadFile(filepath.Join(fixture.runDir, name))
		if err != nil || string(after) != string(before[name]) {
			t.Fatalf("Approve() modified %s: %v", name, err)
		}
	}
}

func newApprovalFixture(t *testing.T, mutatePolicy func(map[string]any), publishing bool) approvalFixture {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	root, runID, taskID := t.TempDir(), "run-01", "ENG-123"
	store := runstore.New(root)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	runDir := filepath.Join(root, "runs", runID)
	taskData := readSchemaFixture(t, "examples/happy-path/task-spec.json")
	policy := decodeObject(t, readSchemaFixture(t, "examples/happy-path/policy-snapshot.json"))
	if mutatePolicy != nil {
		mutatePolicy(policy)
	}
	stampPolicyDigest(t, policy)
	policyData := encodeObject(t, policy)
	capabilityData := readSchemaFixture(t, "examples/happy-path/capability-snapshot.json")
	for name, data := range map[string][]byte{
		"task-spec.json": taskData, "policy-snapshot.json": policyData, "capability-snapshot.json": capabilityData,
	} {
		if err := os.WriteFile(filepath.Join(runDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, time.Unix(1, 0).UTC())
	state.SpecDigest = mustDigest(t, taskData)
	state.PolicyDigest = mustDigest(t, policyData)
	state.CapabilityDigest = mustDigest(t, capabilityData)
	state.BaseSHA = strings.Repeat("a", 40)
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	states := []domain.State{domain.StatePlanned, domain.StateReady}
	if publishing {
		states = append(states, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing)
	}
	current := domain.StateCreated
	for index, target := range states {
		attemptID := ""
		if target == domain.StateRunning {
			attemptID = "attempt-01"
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event-%02d", index+1), RunID: runID, AttemptID: attemptID,
			Sequence: uint64(index + 1), Type: "fixture.transition", StateFrom: current, StateTo: target,
			Timestamp: time.Unix(int64(index+2), 0).UTC(), Payload: map[string]any{},
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
		current = target
	}
	if publishing {
		decision := decodeObject(t, readSchemaFixture(t, "examples/happy-path/review-decision.json"))
		decision["taskId"], decision["runId"], decision["reviewRound"] = taskID, runID, float64(1)
		decision["specDigest"] = state.SpecDigest
		decisionDir := filepath.Join(runDir, "decisions")
		if err := os.MkdirAll(decisionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeObject(t, filepath.Join(decisionDir, "decision-001.json"), decision)
	}
	return approvalFixture{root: root, runDir: runDir, runID: runID, taskID: taskID, validator: validator}
}

func (f approvalFixture) input(gate string) ApprovalInput {
	return ApprovalInput{StateRoot: f.root, RunID: f.runID, Gate: gate, SourceID: "operator-01", Now: time.Unix(100, 0), Validator: f.validator}
}

func mutateFirstApproval(t *testing.T, fixture approvalFixture, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "control", "records.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record := decodeObject(t, []byte(strings.TrimSpace(string(data))))
	mutate(record)
	if err := os.WriteFile(path, append(encodeObject(t, record), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSchemaFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := marshalSchemas.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeObject(t, data)
}

func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeObject(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.WriteFile(path, encodeObject(t, value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func encodeObject(t *testing.T, value map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustDigest(t *testing.T, data []byte) string {
	t.Helper()
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// stampPolicyDigest recomputes the detached policyDigest of a policy
// document exactly the way the production planning gate verifies it: blank
// the policyDigest field, marshal the document, canonicalize it with
// canonical.JSON and digest it with canonical.DigestBytes. Fixtures that
// construct or mutate a PolicySnapshot must seal it at test runtime instead
// of hardcoding a placeholder digest.
func stampPolicyDigest(t *testing.T, policy map[string]any) {
	t.Helper()
	policy["policyDigest"] = ""
	digest, err := canonical.DigestJSON(encodeObject(t, policy))
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = digest
}
