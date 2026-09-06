package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type preparationWorker struct {
	planningTestWorker
	probes int
}

func (w *preparationWorker) Probe(ctx context.Context) (domain.Record, error) {
	w.probes++
	return w.planningTestWorker.Probe(ctx)
}

func preparationFixture(t *testing.T) (Input, *preparationWorker, string) {
	t.Helper()
	repository, base := planningGitFixture(t)
	const remote = "https://example.invalid/preparation.git"
	planningGit(t, repository, "remote", "add", "origin", remote)
	worker := &preparationWorker{planningTestWorker: planningTestWorker{
		id: "adapter-prepared", capability: domain.Record{
			Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, "adapter-prepared"),
		},
	}}
	return Input{
		StateRoot: filepath.Join(repository, ".marshal"), RepositoryRoot: repository,
		RunID:          "run-prepared",
		TaskSpec:       planningTaskFixture(t, repository, "task-prepared", worker.ID(), remote, base),
		PolicySnapshot: planningPolicyFixture(t, "task-prepared", "run-prepared", worker.ID()),
		Selector:       planningSelectorForWorker(t, worker), Validator: newValidator(t),
		Now: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
	}, worker, base
}

func assertPreparationNoRun(t *testing.T, input Input) {
	t.Helper()
	if _, err := os.Lstat(input.StateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preparation created state root: %v", err)
	}
	if entries := planningGitOutput(t, input.RepositoryRoot, "worktree", "list", "--porcelain"); strings.Count(entries, "worktree ") != 1 {
		t.Fatalf("preparation changed worktrees: %s", entries)
	}
}

func TestPrepareThenCreateUsesExactlyFrozenInputWithoutReprobe(t *testing.T) {
	input, worker, base := preparationFixture(t)
	wantTask, wantPolicy := mustCanonical(t, input.TaskSpec), mustCanonical(t, input.PolicySnapshot)
	wantCapability := mustCanonical(t, worker.capability.Data)
	prepared, attempts, err := Prepare(context.Background(), input)
	if err != nil || prepared == nil || len(attempts) != 1 || worker.probes != 1 {
		t.Fatalf("prepare = %v, attempts=%v probes=%d err=%v", prepared != nil, attempts, worker.probes, err)
	}
	assertPreparationNoRun(t, input)
	frozen := prepared.Inputs()
	if frozen.RunID != input.RunID || frozen.BaseSHA != base || !frozen.PreparedAt.Equal(input.Now) ||
		!bytes.Equal(frozen.Task, wantTask) || !bytes.Equal(frozen.Policy, wantPolicy) || !bytes.Equal(frozen.Capability, wantCapability) {
		t.Fatal("preparation did not preserve complete canonical input")
	}
	var task map[string]json.RawMessage
	if json.Unmarshal(frozen.Task, &task) != nil || !bytes.Contains(task["work"], []byte(`"context"`)) {
		t.Fatal("full Task context was lost through the partial domain model")
	}
	// None of these caller-owned values can replace a validated preparation.
	clear(input.TaskSpec)
	clear(input.PolicySnapshot)
	clear(worker.capability.Data)
	clear(frozen.Task)
	clear(frozen.Policy)
	clear(frozen.Capability)
	frozen.SelectionAttempts[0].AdapterID = "forged"
	attempts[0].AdapterID = "forged"
	// Repository HEAD advances, but the admitted Task uses an immutable SHA.
	planningGit(t, input.RepositoryRoot, "commit", "--allow-empty", "-m", "advance HEAD")
	if planningGitOutput(t, input.RepositoryRoot, "rev-parse", "HEAD") == base {
		t.Fatal("fixture did not advance HEAD")
	}
	result, err := prepared.Create(context.Background())
	if err != nil || result.State.State != domain.StateReady || result.State.Sequence != 2 || result.State.BaseSHA != base || worker.probes != 1 {
		t.Fatalf("create state=%+v probes=%d err=%v", result.State, worker.probes, err)
	}
	if head := planningGitOutput(t, result.State.WorktreePath, "rev-parse", "HEAD"); head != base {
		t.Fatal("creation replaced the approved base with the new repository HEAD")
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "task-spec.json"), wantTask, result.State.SpecDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "policy-snapshot.json"), wantPolicy, result.State.PolicyDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDir, "capability-snapshot.json"), wantCapability, result.State.CapabilityDigest)
	if result.SelectionAttempts[0].AdapterID != worker.ID() {
		t.Fatal("returned selection evidence was mutated by caller")
	}
	// This seam is not yet a recovery API. It must never pretend that a
	// second create is a fresh Run or discard the original journal.
	if _, err := prepared.Create(context.Background()); err == nil {
		t.Fatal("duplicate create accepted")
	}
	events, truncated, err := runstore.New(input.StateRoot).ReadEvents(input.RunID)
	if err != nil || truncated || len(events) != 2 || worker.probes != 1 {
		t.Fatalf("duplicate create changed evidence: events=%d truncated=%v probes=%d err=%v", len(events), truncated, worker.probes, err)
	}
}

func TestPreparedCreationRejectsDriftOrCancellationWithoutRun(t *testing.T) {
	for _, mode := range []string{"cancel", "remote", "adapter"} {
		t.Run(mode, func(t *testing.T) {
			input, worker, _ := preparationFixture(t)
			prepared, _, err := Prepare(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch mode {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "remote":
				planningGit(t, input.RepositoryRoot, "remote", "set-url", "origin", "https://example.invalid/changed.git")
			case "adapter":
				worker.id = "another-adapter"
			}
			if _, err := prepared.Create(ctx); err == nil {
				t.Fatal("invalid creation accepted")
			}
			assertPreparationNoRun(t, input)
			if worker.probes != 1 {
				t.Fatal("creation re-probed after drift")
			}
		})
	}
}

func TestPrepareInvalidInputsAndUninitializedCreateCannotMutate(t *testing.T) {
	input, worker, _ := preparationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Prepare(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation: %v", err)
	}
	//lint:ignore SA1012 Deliberately verify that invalid callers fail closed.
	if _, _, err := Prepare(nil, input); err == nil {
		t.Fatal("nil context accepted")
	}
	input.PolicySnapshot = []byte("{}")
	if _, _, err := Prepare(context.Background(), input); err == nil || worker.probes != 0 {
		t.Fatalf("invalid policy reached probe: probes=%d err=%v", worker.probes, err)
	}
	for _, prepared := range []*PreparedPlan{nil, {}} {
		if _, err := prepared.Create(context.Background()); err == nil {
			t.Fatal("unvalidated preparation accepted")
		}
	}
	assertPreparationNoRun(t, input)
}

func TestPrepareRejectsMutableBaseBeforeProbe(t *testing.T) {
	input, worker, _ := preparationFixture(t)
	var task map[string]any
	if err := json.Unmarshal(input.TaskSpec, &task); err != nil {
		t.Fatal(err)
	}
	task["repository"].(map[string]any)["baseRef"] = "HEAD"
	input.TaskSpec = mustMarshal(t, task)
	if _, _, err := Prepare(context.Background(), input); err == nil || worker.probes != 0 {
		t.Fatalf("mutable baseline accepted: probes=%d err=%v", worker.probes, err)
	}
	assertPreparationNoRun(t, input)
}
