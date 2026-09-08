//go:build unix

package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"golang.org/x/sys/unix"
)

// Explicit fixture only: production must use current owner + exact RB1 fact.
func fixtureCreationGuard(_ context.Context, fn func() error) error { return fn() }

func seedPartialCreation(t *testing.T, input Input, count, snapshot int) (*PreparedPlan, string) {
	t.Helper()
	prepared, _, err := Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.CreateForRun(input.StateRoot, prepared.task.Metadata.ID, input.RunID, prepared.baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	defer worktree.Release()
	events, states, err := prepared.creationTransitions(worktree)
	if err != nil {
		t.Fatal(err)
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for index := 0; index < count; index++ {
		if err := store.Append(lease, events[index], uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot >= 0 {
		if err := store.WriteSnapshot(lease, states[snapshot]); err != nil {
			t.Fatal(err)
		}
	}
	return prepared, worktree.Path
}

func TestCreationRecoveryCompletesEveryJournalSnapshotBoundary(t *testing.T) {
	for count := 0; count <= 2; count++ {
		for snapshot := -1; snapshot <= count; snapshot++ {
			t.Run(fmt.Sprintf("journal-%d-snapshot-%d", count, snapshot), func(t *testing.T) {
				input, worker, _ := preparationFixture(t)
				prepared, path := seedPartialCreation(t, input, count, snapshot)
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				original, _, _ := runstore.New(input.StateRoot).ReadEvents(input.RunID)
				// Cold reconstruction of the original inputs, no new probe/time.
				prepared, err = RestorePrepared(context.Background(), input, prepared.Inputs())
				if err != nil {
					t.Fatal(err)
				}
				result, err := prepared.ReconcileCreation(context.Background(), fixtureCreationGuard)
				if err != nil || result.State.State != domain.StateReady || result.State.Sequence != 2 || worker.probes != 1 {
					t.Fatalf("recover: %+v probes=%d err=%v", result.State, worker.probes, err)
				}
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatal("worktree recreated")
				}
				store := runstore.New(input.StateRoot)
				events, truncated, err := store.ReadEvents(input.RunID)
				if err != nil || truncated || len(events) != 2 {
					t.Fatal("wrong recovered journal")
				}
				for index, old := range original {
					if !sameCreationValue(events[index], old) {
						t.Fatal("existing event replaced")
					}
				}
				runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
				for name, data := range map[string][]byte{"task-spec.json": prepared.taskCanonical, "policy-snapshot.json": prepared.policyCanonical, "capability-snapshot.json": prepared.capabilityCanonical} {
					assertPlanningFrozenFile(t, filepath.Join(runDir, name), data, canonical.DigestBytes(data))
				}
				journal, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := prepared.ReconcileCreation(context.Background(), fixtureCreationGuard); err != nil {
					t.Fatal(err)
				}
				replayed, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
				if err != nil || !bytes.Equal(journal, replayed) {
					t.Fatal("exact replay appended or changed events")
				}
			})
		}
	}
}

func TestCreationRecoveryDeniedOrInterruptedGuardDoesNotRestart(t *testing.T) {
	for stop := 1; stop <= 7; stop++ {
		t.Run(fmt.Sprintf("guard-%d", stop), func(t *testing.T) {
			input, worker, _ := preparationFixture(t)
			prepared, _, err := Prepare(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			guard := func(ctx context.Context, fn func() error) error {
				calls++
				if calls == stop {
					return errors.New("owner no longer current")
				}
				return fn()
			}
			if _, err := prepared.ReconcileCreation(context.Background(), guard); err == nil {
				t.Fatal("owner loss accepted")
			}
			result, err := prepared.ReconcileCreation(context.Background(), fixtureCreationGuard)
			if err != nil || result.State.Sequence != 2 || worker.probes != 1 {
				t.Fatalf("recovery: %v", err)
			}
		})
	}
}

func TestCreationRecoveryRejectsMissingOrBrokenGuardBeforeWrites(t *testing.T) {
	input, _, _ := preparationFixture(t)
	prepared, _, err := Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, guard := range []CreationGuard{
		nil,
		func(context.Context, func() error) error { return nil },
		func(_ context.Context, fn func() error) error { _ = fn(); _ = fn(); return nil },
	} {
		if _, err := prepared.ReconcileCreation(context.Background(), guard); err == nil {
			t.Fatal("broken guard accepted")
		}
		assertPreparationNoRun(t, input)
	}
}

func TestCreationRecoveryPreservesMalformedOrConflictingEvidence(t *testing.T) {
	for _, mode := range []string{"journal-payload", "journal-truncated", "journal-fifo", "snapshot", "snapshot-unknown", "task", "task-symlink", "task-hardlink", "task-fifo", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			input, worker, _ := preparationFixture(t)
			prepared, path := seedPartialCreation(t, input, 1, 1)
			runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
			journalPath := filepath.Join(runDir, "events.jsonl")
			journal, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "journal-payload":
				var event domain.RunEvent
				if err := json.Unmarshal(journal, &event); err != nil {
					t.Fatal(err)
				}
				event.Payload["specDigest"] = "sha256:forged"
				raw, _ := json.Marshal(event)
				if err := os.WriteFile(journalPath, append(raw, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal-truncated":
				if err := os.WriteFile(journalPath, journal[:len(journal)-1], 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal-fifo":
				if err := os.Remove(journalPath); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(journalPath, 0o600); err != nil {
					t.Fatal(err)
				}
			case "snapshot", "snapshot-unknown":
				file := filepath.Join(runDir, "state.json")
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var state map[string]any
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				if mode == "snapshot" {
					state["attemptsUsed"] = 1
				} else {
					state["forged"] = true
				}
				raw, _ = json.Marshal(state)
				if err := os.WriteFile(file, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "task":
				if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "task-symlink":
				if err := os.Symlink(filepath.Join(input.RepositoryRoot, "README.md"), filepath.Join(runDir, "task-spec.json")); err != nil {
					t.Fatal(err)
				}
			case "task-hardlink":
				if err := os.Link(filepath.Join(input.RepositoryRoot, "README.md"), filepath.Join(runDir, "task-spec.json")); err != nil {
					t.Fatal(err)
				}
			case "task-fifo":
				if err := unix.Mkfifo(filepath.Join(runDir, "task-spec.json"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				if err := os.WriteFile(filepath.Join(path, "user.txt"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepared.ReconcileCreation(context.Background(), fixtureCreationGuard); err == nil || worker.probes != 1 {
				t.Fatalf("conflict accepted: %v", err)
			}
			if mode != "journal-fifo" && mode != "journal-payload" && mode != "journal-truncated" {
				after, err := os.ReadFile(journalPath)
				if err != nil || !bytes.Equal(journal, after) {
					t.Fatal("conflict changed journal")
				}
			}
		})
	}
}
