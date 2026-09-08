package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
)

func businessBudgetFixture(t *testing.T, badFirst bool) (*Store, *Lease, domain.RunState) {
	t.Helper()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:budget")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	task := domain.TaskSpec{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask, Budgets: domain.TaskBudgets{RunTimeoutSeconds: 600, AttemptTimeoutSeconds: 120}}
	task.Metadata.ID = "task:budget"
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := canonical.DigestJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "runs", lease.runID, "task-spec.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	planned := transition("event:budget-plan", 1, domain.StateCreated, domain.StatePlanned)
	planned.Type, planned.RunID = "planning.spec-accepted", lease.runID
	if badFirst {
		planned.Type = "run.transition"
	}
	planned.Payload = map[string]any{"specDigest": spec}
	ready := transition("event:budget-ready", 2, domain.StatePlanned, domain.StateReady)
	ready.RunID = lease.runID
	ready.Payload = map[string]any{"specDigest": spec, "policyDigest": canonical.DigestBytes([]byte("policy")), "capabilityDigest": canonical.DigestBytes([]byte("capability")), "baseSha": strings.Repeat("a", 40), "worktreePath": "/tmp/budget-fixture", "maxAttempts": 3}
	state := domain.NewRunState(task.Metadata.ID, lease.runID, planned.Timestamp)
	for _, event := range []domain.RunEvent{planned, ready} {
		if err := store.Append(lease, event, state.Sequence); err != nil {
			t.Fatal(err)
		}
		state, err = lifecycle.Replay(state, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	state.SpecDigest = spec
	state.PolicyDigest = ready.Payload["policyDigest"].(string)
	state.CapabilityDigest = ready.Payload["capabilityDigest"].(string)
	state.BaseSHA = ready.Payload["baseSha"].(string)
	state.WorktreePath = ready.Payload["worktreePath"].(string)
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	return store, lease, state
}

func TestBusinessBudgetUsesFrozenSourcesAcrossReopen(t *testing.T) {
	store, lease, state := businessBudgetFixture(t, false)
	budget, err := store.ReadBusinessBudgetUnderLease(context.Background(), lease)
	if err != nil || budget.Run.RunID != state.RunID || budget.RunTimeoutSeconds != 600 || budget.AttemptTimeoutSeconds != 120 || budget.SpecDigest != state.SpecDigest || !budget.CreatedAt.Equal(state.CreatedAt) || budget.CreationEventDigest == "" {
		t.Fatalf("budget=%+v err=%v", budget, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	reopened := New(store.root)
	recoveredLease, err := reopened.AcquireExisting(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredLease.Release()
	recovered, err := reopened.ReadBusinessBudgetUnderLease(context.Background(), recoveredLease)
	if err != nil || recovered != budget {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func TestBusinessBudgetRejectsUnprovenSources(t *testing.T) {
	for _, name := range []string{"snapshot-time", "task-bytes", "missing-task", "wrong-first-event", "cancelled-context", "different-store"} {
		t.Run(name, func(t *testing.T) {
			store, lease, state := businessBudgetFixture(t, name == "wrong-first-event")
			ctx := context.Background()
			path := filepath.Join(store.root, "runs", lease.runID, "task-spec.json")
			switch name {
			case "snapshot-time":
				state.CreatedAt = state.CreatedAt.Add(time.Hour)
				if err := store.WriteSnapshot(lease, state); err != nil {
					t.Fatal(err)
				}
			case "task-bytes":
				if err := os.WriteFile(path, []byte(`{"kind":"TaskSpec","budgets":{"runTimeoutSeconds":9999}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-task":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "different-store":
				store = New(t.TempDir())
			}
			budget, err := store.ReadBusinessBudgetUnderLease(ctx, lease)
			if err == nil || budget != (RunBusinessBudgetProjection{}) {
				t.Fatalf("accepted invalid sources: %+v err=%v", budget, err)
			}
			if name != "missing-task" && !errors.Is(err, ErrConflict) {
				t.Fatalf("expected conflict: %v", err)
			}
		})
	}
}
