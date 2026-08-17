package runstore

import (
	"encoding/json"
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

func TestLeaseHeldIsReadOnlyOwnershipProbe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	if _, err := store.LeaseHeld("run:missing"); err == nil {
		t.Fatal("missing lease lock was treated as known ownership")
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run:missing", "lease.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownership probe created a missing lock file: %v", err)
	}
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.LeaseHeld("run:1")
	if err != nil || !held {
		t.Fatalf("LeaseHeld while owned = %v, %v", held, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	held, err = store.LeaseHeld("run:1")
	if err != nil || held {
		t.Fatalf("LeaseHeld after release = %v, %v", held, err)
	}
}

func TestLeaseHeldFailsClosedWhenLockPathIsReplaced(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:replace")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	path := filepath.Join(root, "runs", "run:replace", "lease.lock")
	old := path + ".replaced"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if held, err := store.LeaseHeld("run:replace"); err == nil || held {
		t.Fatalf("replacement probe = held:%v err:%v, want fail-closed identity error", held, err)
	}
	if _, err := store.Acquire("run:replace"); err == nil {
		t.Fatal("second Acquire accepted a replacement lease inode")
	}
	if err := store.Append(lease, transition("event:replace", 1, domain.StateCreated, domain.StatePlanned), 0); err == nil {
		t.Fatal("original owner appended after its authoritative pathname was replaced")
	}
}

func TestLeaseMutationRejectsReplacedRunAuthorityDirectory(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:directory-replace")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	runDirectory := filepath.Join(root, "runs", "run:directory-replace")
	if err := os.Rename(runDirectory, runDirectory+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "lease.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, transition("event:directory-replace", 1, domain.StateCreated, domain.StatePlanned), 0); err == nil {
		t.Fatal("old lease appended after the canonical run directory was replaced")
	}
	if _, err := os.Stat(filepath.Join(runDirectory, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement authority received an event: %v", err)
	}
}

func TestAcquireRejectsUnsafeOwnerWithoutMutatingTarget(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			store := New(root)
			lease, err := store.Acquire("run:owner")
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
			owner := filepath.Join(root, "runs", "run:owner", "lease.lock.owner")
			if err := os.Remove(owner); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "outside")
			want := []byte("must-not-change\n")
			if err := os.WriteFile(target, want, 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				err = os.Symlink(target, owner)
			} else {
				err = os.Link(target, owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Acquire("run:owner"); err == nil {
				t.Fatalf("Acquire accepted %s owner", kind)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != string(want) {
				t.Fatalf("unsafe owner target mutated: %q err=%v", got, err)
			}
		})
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

func TestNotifyHookRecordsStateTransitionPayload(t *testing.T) {
	command, record := writeNotifyRecorder(t)
	t.Setenv(notifyCommandEnv, command)
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	event := transition("event:1", 1, domain.StateCreated, domain.StatePlanned)
	event.Payload = map[string]any{"taskId": "task:1"}
	if err := store.Append(lease, event, 0); err != nil {
		t.Fatal(err)
	}
	payload := decodeNotifyPayload(t, waitForNotifyRecord(t, record))
	if len(payload) != 6 {
		t.Fatalf("payload fields = %v, want exactly six fields", payload)
	}
	expected := map[string]string{
		"runId":     "run:1",
		"taskId":    "task:1",
		"stateFrom": string(domain.StateCreated),
		"stateTo":   string(domain.StatePlanned),
	}
	for field, want := range expected {
		if payload[field] != want {
			t.Fatalf("payload field %s = %v, want %q", field, payload[field], want)
		}
	}
	if payload["eventSequence"] != float64(1) {
		t.Fatalf("payload field eventSequence = %v, want 1", payload["eventSequence"])
	}
	wantTimestamp := event.Timestamp.Format(time.RFC3339Nano)
	if payload["timestamp"] != wantTimestamp {
		t.Fatalf("payload field timestamp = %v, want %s", payload["timestamp"], wantTimestamp)
	}
}

func TestNotifyHookIdleWhenEnvUnsetOrEmpty(t *testing.T) {
	cases := []struct {
		name  string
		value string
		unset bool
	}{
		{name: "unset", unset: true},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A recorder is installed, but with the variable unset or empty
			// Append must not start it and must succeed normally.
			command, record := writeNotifyRecorder(t)
			_ = command
			if tc.unset {
				unsetNotifyCommand(t)
			} else {
				t.Setenv(notifyCommandEnv, tc.value)
			}
			store := New(t.TempDir())
			lease, err := store.Acquire("run:1")
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
				t.Fatalf("append without notify command failed: %v", err)
			}
			requireNoNotifyRecord(t, record)
		})
	}
}

func TestNotifyHookMissingCommandKeepsAppendSemantics(t *testing.T) {
	t.Setenv(notifyCommandEnv, filepath.Join(t.TempDir(), "missing-notify-command"))
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatalf("append with unreachable notify command failed: %v", err)
	}
	if err := store.Append(lease, transition("event:2", 2, domain.StatePlanned, domain.StateReady), 1); err != nil {
		t.Fatalf("follow-up append failed: %v", err)
	}
	events, truncated, err := store.ReadEvents("run:1")
	if err != nil || truncated || len(events) != 2 {
		t.Fatalf("journal = %d events, truncated = %v, error = %v", len(events), truncated, err)
	}
}

func TestNotifyHookSkipsSameStateEvent(t *testing.T) {
	command, record := writeNotifyRecorder(t)
	t.Setenv(notifyCommandEnv, command)
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatal(err)
	}
	_ = waitForNotifyRecord(t, record)
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	audit := transition("event:2", 2, domain.StatePlanned, domain.StatePlanned)
	audit.Type = "run.audit"
	if err := store.Append(lease, audit, 1); err != nil {
		// The lifecycle may refuse same-state audit events; a failed append
		// must not notify either way.
		requireNoNotifyRecord(t, record)
		return
	}
	requireNoNotifyRecord(t, record)
}

func TestNotifyHookGateHonoursFirstEventAndSameState(t *testing.T) {
	command, record := writeNotifyRecorder(t)
	t.Setenv(notifyCommandEnv, command)
	audit := transition("event:2", 2, domain.StatePlanned, domain.StatePlanned)
	notifyStateTransition(false, []domain.RunEvent{audit})
	requireNoNotifyRecord(t, record)
	first := transition("event:1", 1, domain.StateCreated, domain.StateCreated)
	first.Payload = map[string]any{"taskId": "task:1"}
	notifyStateTransition(true, []domain.RunEvent{first})
	payload := decodeNotifyPayload(t, waitForNotifyRecord(t, record))
	if payload["stateFrom"] != string(domain.StateCreated) || payload["stateTo"] != string(domain.StateCreated) || payload["eventSequence"] != float64(1) {
		t.Fatalf("first-event payload = %v", payload)
	}
}

func TestNotifyHookReportsOnlyLastTransitionAmongMultipleEvents(t *testing.T) {
	command, record := writeNotifyRecorder(t)
	t.Setenv(notifyCommandEnv, command)
	first := transition("event:1", 1, domain.StateCreated, domain.StatePlanned)
	second := transition("event:2", 2, domain.StatePlanned, domain.StateReady)
	second.Payload = map[string]any{"taskId": "task:1"}
	notifyStateTransition(true, []domain.RunEvent{first, second})
	payload := decodeNotifyPayload(t, waitForNotifyRecord(t, record))
	if payload["stateFrom"] != string(domain.StatePlanned) || payload["stateTo"] != string(domain.StateReady) {
		t.Fatalf("payload = %v, want the last transition planned to ready", payload)
	}
	if payload["eventSequence"] != float64(2) || payload["taskId"] != "task:1" {
		t.Fatalf("payload = %v, want eventSequence 2 and taskId task:1", payload)
	}
}

func TestNotifyHookSequentialAppendsReportLatestTransition(t *testing.T) {
	command, record := writeNotifyRecorder(t)
	t.Setenv(notifyCommandEnv, command)
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := store.Append(lease, transition("event:1", 1, domain.StateCreated, domain.StatePlanned), 0); err != nil {
		t.Fatal(err)
	}
	waitForNotifyStateTo(t, record, string(domain.StatePlanned))
	if err := store.Append(lease, transition("event:2", 2, domain.StatePlanned, domain.StateReady), 1); err != nil {
		t.Fatal(err)
	}
	payload := waitForNotifyStateTo(t, record, string(domain.StateReady))
	if payload["stateFrom"] != string(domain.StatePlanned) || payload["eventSequence"] != float64(2) {
		t.Fatalf("latest payload = %v", payload)
	}
}

func writeNotifyRecorder(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	record := filepath.Join(directory, "notify-record.json")
	command := filepath.Join(directory, "notify-recorder.sh")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > \"" + record + "\"\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return command, record
}

func unsetNotifyCommand(t *testing.T) {
	t.Helper()
	previous, had := os.LookupEnv(notifyCommandEnv)
	if err := os.Unsetenv(notifyCommandEnv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(notifyCommandEnv, previous)
		}
	})
}

func waitForNotifyRecord(t *testing.T, record string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil {
			return data
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read notify record: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("notify record %s was not created in time", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForNotifyStateTo(t *testing.T, record, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil {
			var payload map[string]any
			if jsonErr := json.Unmarshal(data, &payload); jsonErr == nil && payload["stateTo"] == state {
				return payload
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read notify record: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("notify record never reached stateTo %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireNoNotifyRecord(t *testing.T, record string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("notify record %s unexpectedly present: %v", record, err)
	}
}

func decodeNotifyPayload(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode notify payload: %v", err)
	}
	return payload
}
