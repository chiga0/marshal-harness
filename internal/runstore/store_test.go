package runstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestAppendRejectsStaleSequenceAndDuplicateEvent(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	event := transition("event:1", 1, domain.StateCreated, domain.StatePlanned)
	if err := store.Append(lease, event, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, transition("event:2", 2, domain.StatePlanned, domain.StateReady), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale append error = %v", err)
	}
	duplicate := transition("event:1", 2, domain.StatePlanned, domain.StateReady)
	if err := store.Append(lease, duplicate, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate append error = %v", err)
	}
}

func TestLeaseIsExclusive(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	first, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := store.Acquire("run:1"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second lease error = %v", err)
	}
}

func TestRebuildIgnoresTruncatedJournalTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "runs", "run:1", "events.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"apiVersion":"marshal.dev/v1alpha1"`)
	_ = file.Close()
	initial := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	state, err := store.Rebuild(initial)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != domain.StatePlanned || state.Sequence != 1 {
		t.Fatalf("rebuilt state = %+v", state)
	}
}

func TestSnapshotAtomicRoundTrip(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadSnapshot("run:1")
	if err != nil || read.RunID != state.RunID {
		t.Fatalf("ReadSnapshot = %+v, %v", read, err)
	}
}

func TestFrozenInputChangeRequiresNewRun(t *testing.T) {
	t.Parallel()
	state := domain.NewRunState("task:1", "run:1", time.Now())
	state.State = domain.StateReady
	state.SpecDigest = "sha256:old"
	if !errors.Is(CheckFrozenInputs(state, FrozenInputs{SpecDigest: "sha256:new"}), ErrConflict) {
		t.Fatal("changed frozen input accepted")
	}
}

func TestAppendRejectsIllegalTransition(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	illegal := transition("event:1", 1, domain.StateCreated, domain.StateAccepted)
	if err := store.Append(lease, illegal, 0); err == nil {
		t.Fatal("illegal transition entered journal")
	}
}

func TestInspectReplaysJournalAheadOfSnapshot(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Inspect("run:1")
	if err != nil || recovered.State != domain.StatePlanned || recovered.Sequence != 1 {
		t.Fatalf("recovered snapshot = %+v, error=%v", recovered, err)
	}
}

func TestInspectReplaysPublicationIdentityAfterSnapshotCrash(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	steps := []domain.State{domain.StatePlanned, domain.StateReady, domain.StateRunning, domain.StateVerifying, domain.StateReviewPending, domain.StatePublishing}
	for index, next := range steps {
		event := transition("event:"+string(rune('1'+index)), uint64(index+1), state.State, next)
		if err := store.Append(lease, event, state.Sequence); err != nil {
			t.Fatal(err)
		}
		state.State, state.Sequence = next, uint64(index+1)
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	event := transition("event:7", 7, domain.StatePublishing, domain.StatePublished)
	event.Type = "publication.completed"
	event.Payload = map[string]any{"provider": "github", "repository": "example/repo", "headBranch": "marshal/task-1234", "baseBranch": "main", "externalId": "PR_1", "uri": "https://github.com/example/repo/pull/1", "headSha": "0123456789abcdef0123456789abcdef01234567"}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Inspect("run:1")
	if err != nil || recovered.State != domain.StatePublished || recovered.Publication == nil || recovered.Publication.ExternalID != "PR_1" {
		t.Fatalf("recovered publication = %+v, error=%v", recovered, err)
	}
}

func TestInspectDetectsStateMismatchAtSameSequence(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	state.Sequence = 1
	state.State = domain.StateReady
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect("run:1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("state mismatch error = %v", err)
	}
}

func TestLeaseCannotWriteAnotherRun(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	event := transition("event:1", 1, domain.StateCreated, domain.StatePlanned)
	event.RunID = "run:2"
	if err := store.Append(lease, event, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-run lease error = %v", err)
	}
}

func transition(id string, sequence uint64, from, to domain.State) domain.RunEvent {
	return domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: id, RunID: "run:1", Sequence: sequence, Type: "run.transition", StateFrom: from, StateTo: to, Timestamp: time.Unix(int64(sequence+1), 0), Payload: map[string]any{}}
}
