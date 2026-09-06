package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func restoredFixture(t *testing.T) (Input, *preparationWorker, PreparedInputs) {
	t.Helper()
	input, worker, _ := preparationFixture(t)
	prepared, _, err := Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise serialization, not reuse of the original private handle. The
	// fixture is not a production RB1 receipt or a Goal approval.
	raw, err := json.Marshal(prepared.Inputs())
	if err != nil {
		t.Fatal(err)
	}
	var frozen PreparedInputs
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	input.Now = time.Time{}
	return input, worker, frozen
}

func TestRestorePreparedColdCreationRetainsOriginalInputsAndTimestamp(t *testing.T) {
	input, worker, frozen := restoredFixture(t)
	wantTask, wantPolicy, wantCapability := bytes.Clone(frozen.Task), bytes.Clone(frozen.Policy), bytes.Clone(frozen.Capability)
	wantTime, wantBase := frozen.PreparedAt, frozen.BaseSHA
	clear(worker.capability.Data) // a probe would now fail; do not reselect.
	planningGit(t, input.RepositoryRoot, "commit", "--allow-empty", "-m", "advance after freeze")
	restored, err := RestorePrepared(context.Background(), input, frozen)
	if err != nil || worker.probes != 1 {
		t.Fatalf("restore: probes=%d err=%v", worker.probes, err)
	}
	assertPreparationNoRun(t, input)
	clear(frozen.Task)
	clear(frozen.Policy)
	clear(frozen.Capability)
	frozen.SelectionAttempts[0].AdapterID = "forged"
	result, err := restored.Create(context.Background())
	if err != nil || result.State.State != domain.StateReady || result.State.Sequence != 2 || result.State.BaseSHA != wantBase || worker.probes != 1 {
		t.Fatalf("cold create: state=%+v probes=%d err=%v", result.State, worker.probes, err)
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "task-spec.json"), wantTask, result.State.SpecDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "policy-snapshot.json"), wantPolicy, result.State.PolicyDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "capability-snapshot.json"), wantCapability, result.State.CapabilityDigest)
	events, truncated, err := runstore.New(input.StateRoot).ReadEvents(input.RunID)
	if err != nil || truncated || len(events) != 2 {
		t.Fatalf("events: %v", err)
	}
	for _, event := range events {
		if !event.Timestamp.Equal(wantTime) {
			t.Fatal("creation refreshed the original timestamp")
		}
	}
	if _, err := restored.Create(context.Background()); err == nil {
		t.Fatal("restore overwrote an existing Run")
	}
}

func TestRestorePreparedRejectsDriftWithoutRunOrProbe(t *testing.T) {
	for _, mode := range []string{"cancel", "nil-context", "no-selector", "run-id", "repository", "base", "zero-time", "non-utc", "new-time", "task", "policy", "capability-schema", "capability-id", "capability-status", "capability-noncanonical", "extra-attempt", "fallback-attempt", "empty-remote", "changed-remote", "adapter", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			input, worker, frozen := restoredFixture(t)
			var ctx context.Context = context.Background()
			switch mode {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			case "no-selector":
				input.Selector = nil
			case "run-id":
				frozen.RunID = "other-run"
			case "repository":
				frozen.RepositoryRoot += "-other"
			case "base":
				frozen.BaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "zero-time":
				frozen.PreparedAt = time.Time{}
			case "non-utc":
				frozen.PreparedAt = frozen.PreparedAt.In(time.FixedZone("offset", 3600))
			case "new-time":
				input.Now = frozen.PreparedAt.Add(time.Second)
			case "task":
				frozen.Task = []byte("{}")
			case "policy":
				frozen.Policy = []byte("{}")
			case "capability-schema":
				frozen.Capability = []byte(`{"adapterId":"adapter-prepared","probeStatus":"supported"}`)
			case "capability-id":
				frozen.Capability = mustCanonical(t, planningCapabilityFixture(t, "other"))
			case "capability-status":
				frozen.Capability = bytes.ReplaceAll(frozen.Capability, []byte(`"supported"`), []byte(`"unsupported"`))
			case "capability-noncanonical":
				frozen.Capability = append([]byte(" "), frozen.Capability...)
			case "extra-attempt":
				frozen.SelectionAttempts = append(frozen.SelectionAttempts, frozen.SelectionAttempts[0])
			case "fallback-attempt":
				frozen.SelectionAttempts[0].Outcome = adapter.OutcomeUnavailable
			case "empty-remote":
				var task map[string]any
				if err := json.Unmarshal(frozen.Task, &task); err != nil {
					t.Fatal(err)
				}
				delete(task["repository"].(map[string]any), "expectedRemoteUrl")
				frozen.Task = mustCanonical(t, mustMarshal(t, task))
				input.TaskSpec = bytes.Clone(frozen.Task)
			case "changed-remote":
				planningGit(t, input.RepositoryRoot, "remote", "set-url", "origin", "https://example.invalid/changed.git")
			case "adapter":
				worker.id = "other"
			case "oversize":
				frozen.Capability = bytes.Repeat([]byte(" "), (64<<10)+1)
			}
			if _, err := RestorePrepared(ctx, input, frozen); err == nil || worker.probes != 1 {
				t.Fatalf("invalid restore: probes=%d err=%v", worker.probes, err)
			}
			assertPreparationNoRun(t, input)
		})
	}
}
