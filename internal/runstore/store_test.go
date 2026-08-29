package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

type runstoreTreeEntry struct {
	Mode os.FileMode
	Data string
	Link string
}

func runstoreTreeSnapshot(t *testing.T, root string) map[string]runstoreTreeEntry {
	t.Helper()
	snapshot := make(map[string]runstoreTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		current := runstoreTreeEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			current.Data = string(data)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			current.Link = target
		}
		snapshot[relative] = current
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestAcquireExistingRejectsUnknownWithoutMutation(t *testing.T) {
	t.Run("missing state root", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "missing-state-root")
		before := runstoreTreeSnapshot(t, parent)
		if _, err := New(root).AcquireExisting("run:missing"); err == nil {
			t.Fatal("AcquireExisting created a missing state root")
		}
		after := runstoreTreeSnapshot(t, parent)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("missing-root rejection mutated tree:\nbefore=%#v\nafter=%#v", before, after)
		}
	})

	for _, kind := range []string{"missing run", "unknown directory", "orphan lease lock", "non-directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			runs := filepath.Join(root, "runs")
			if err := os.Mkdir(runs, 0o700); err != nil {
				t.Fatal(err)
			}
			runPath := filepath.Join(runs, "run:unknown")
			switch kind {
			case "missing run":
			case "unknown directory":
				if err := os.Mkdir(runPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runPath, "sentinel"), []byte("unchanged\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "orphan lease lock":
				if err := os.Mkdir(runPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runPath, "lease.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "non-directory":
				if err := os.WriteFile(runPath, []byte("not-a-directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "outside")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("unchanged\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, runPath); err != nil {
					t.Fatal(err)
				}
			}
			before := runstoreTreeSnapshot(t, root)
			if _, err := New(root).AcquireExisting("run:unknown"); err == nil {
				t.Fatalf("AcquireExisting accepted %s", kind)
			}
			after := runstoreTreeSnapshot(t, root)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("%s rejection mutated tree:\nbefore=%#v\nafter=%#v", kind, before, after)
			}
		})
	}
}

func TestAcquireExistingUsesDescriptorBoundLeaseAndReleaseIsNonMutating(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	seed, err := store.Acquire("run:existing")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(); err != nil {
		t.Fatal(err)
	}

	lease, err := store.AcquireExisting("run:existing")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := OpenRunAuthority(lease)
	if err != nil {
		t.Fatalf("existing lease did not retain descriptor authority: %v", err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	beforeRelease := runstoreTreeSnapshot(t, root)
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second release was not idempotent: %v", err)
	}
	afterRelease := runstoreTreeSnapshot(t, root)
	if !reflect.DeepEqual(afterRelease, beforeRelease) {
		t.Fatalf("Release mutated existing Run bytes:\nbefore=%#v\nafter=%#v", beforeRelease, afterRelease)
	}
	reacquired, err := store.AcquireExisting("run:existing")
	if err != nil {
		t.Fatalf("released existing lease could not be reacquired: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRunAuthorityBorrowBlocksReleaseUntilClose(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:guarded-authority")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := OpenRunAuthority(lease)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- lease.Release() }()
	select {
	case err := <-released:
		t.Fatalf("Release escaped live authority borrow: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not resume after authority Close")
	}
	if _, err := OpenRunAuthority(lease); err == nil {
		t.Fatal("released Lease reopened authority")
	}
}

func TestBoundDirectoryBorrowBlocksReleaseUntilClose(t *testing.T) {
	store := New(t.TempDir())
	lease, err := store.Acquire("run:guarded-directory")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := OpenOrCreateDirectoryUnderLease(lease, "attempts")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- lease.Release() }()
	select {
	case err := <-released:
		t.Fatalf("Release escaped BoundDirectory borrow: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not resume after BoundDirectory Close")
	}
}

func TestAcquireExistingLeaseBusyDoesNotRewriteOwner(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:busy")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ownerPath := filepath.Join(root, "runs", "run:busy", "lease.lock.owner")
	before, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireExisting("run:busy"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("AcquireExisting busy error = %v", err)
	}
	after, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("busy existing acquisition rewrote the owner record")
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run:busy", "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("busy existing acquisition created a journal: %v", err)
	}
}

func TestAcquireExistingRejectsRunDirectoryReplacementBeforeOwnerWrite(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	seed, err := store.Acquire("run:aba")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(root, "runs", "run:aba")
	ownerBefore, err := os.ReadFile(filepath.Join(runPath, "lease.lock.owner"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := runPath + ".replacement"
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := runPath + ".held"
	store.beforeAcquireExistingOwnerWrite = func() error {
		if err := os.Rename(runPath, held); err != nil {
			return err
		}
		return os.Rename(replacement, runPath)
	}
	if _, err := store.AcquireExisting("run:aba"); err == nil {
		t.Fatal("AcquireExisting accepted a replaced canonical Run directory")
	}
	ownerAfter, err := os.ReadFile(filepath.Join(held, "lease.lock.owner"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ownerAfter) != string(ownerBefore) {
		t.Fatal("detached Run received replacement owner bytes")
	}
	for _, directory := range []string{held, runPath} {
		if _, err := os.Stat(filepath.Join(directory, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement check wrote a journal under %s: %v", directory, err)
		}
	}
	if _, err := os.Stat(filepath.Join(runPath, "lease.lock.owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement Run received an owner record: %v", err)
	}
}

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

func TestLeaseOwnerProcessAliveDistinguishesExitedOwner(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:owner-probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	alive, err := store.LeaseOwnerProcessAlive("run:owner-probe")
	if err != nil || !alive {
		t.Fatalf("current owner process probe = %v, %v", alive, err)
	}
	ownerPath := filepath.Join(root, "runs", "run:owner-probe", "lease.lock.owner")
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var owner leaseOwnerRecord
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	owner.PID = 99999999
	data, err = json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	alive, err = store.LeaseOwnerProcessAlive("run:owner-probe")
	if err != nil || alive {
		t.Fatalf("exited owner process probe = %v, %v", alive, err)
	}
}

func TestAcquireMigratesLegacyLeaseOwnerAfterExclusiveLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:legacy-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	ownerPath := filepath.Join(root, "runs", "run:legacy-owner", "lease.lock.owner")
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "device")
	delete(legacy, "inode")
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := store.Acquire("run:legacy-owner")
	if err != nil {
		t.Fatalf("legacy owner was not migrated after exclusive lock: %v", err)
	}
	defer migrated.Release()
	data, err = os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var current leaseOwnerRecord
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	if current.Device == 0 || current.Inode == 0 {
		t.Fatalf("migrated owner lacks descriptor identity: %+v", current)
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

func TestMutationHookRejectsReplacementBeforeAnyJournalOrSnapshotBytes(t *testing.T) {
	for _, operation := range []string{"journal", "snapshot"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			store := New(root)
			lease, err := store.Acquire("run:mutation-window")
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			runDirectory := filepath.Join(root, "runs", "run:mutation-window")
			oldDirectory := runDirectory + ".old"
			lease.beforeMutation = func() error {
				lease.beforeMutation = nil
				if err := os.Rename(runDirectory, oldDirectory); err != nil {
					return err
				}
				if err := os.Mkdir(runDirectory, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(runDirectory, "lease.lock"), nil, 0o600)
			}
			if operation == "journal" {
				err = store.Append(lease, transition("event:mutation-window", 1, domain.StateCreated, domain.StatePlanned), 0)
			} else {
				err = store.WriteSnapshot(lease, domain.NewRunState("task:mutation-window", "run:mutation-window", time.Unix(1, 0).UTC()))
			}
			if err == nil {
				t.Fatal("mutation crossed a replaced run authority")
			}
			name := "events.jsonl"
			if operation == "snapshot" {
				name = "state.json"
			}
			for _, directory := range []string{runDirectory, oldDirectory} {
				if data, readErr := os.ReadFile(filepath.Join(directory, name)); readErr == nil && len(data) != 0 {
					t.Fatalf("%s received unauthorized bytes: %q", directory, data)
				} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal(readErr)
				}
			}
		})
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

func TestDescriptorRelativeRunFilesRejectAncestorSwapSymlinkHardlinkAndABA(t *testing.T) {
	newFixture := func(t *testing.T) (*Lease, string, string) {
		t.Helper()
		root := t.TempDir()
		store := New(root)
		lease, err := store.Acquire("run-authority")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lease.Release() })
		bound, err := OpenOrCreateDirectoryUnderLease(lease, "attempts", "attempt-1")
		if err != nil {
			t.Fatal(err)
		}
		if err := bound.Close(); err != nil {
			t.Fatal(err)
		}
		runPath := filepath.Join(root, "runs", "run-authority")
		leaf := filepath.Join(runPath, "attempts", "attempt-1", "lineage.json")
		if err := os.WriteFile(leaf, []byte("bound"), 0o400); err != nil {
			t.Fatal(err)
		}
		return lease, runPath, leaf
	}

	t.Run("leaf symlink", func(t *testing.T) {
		lease, _, leaf := newFixture(t)
		if err := os.Rename(leaf, leaf+".target"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("lineage.json.target", leaf); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFileUnderLease(lease, 64, "attempts", "attempt-1", "lineage.json"); err == nil {
			t.Fatal("leaf symlink was accepted")
		}
	})

	t.Run("same bytes hardlink", func(t *testing.T) {
		lease, _, leaf := newFixture(t)
		if err := os.Link(leaf, leaf+".alias"); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFileUnderLease(lease, 64, "attempts", "attempt-1", "lineage.json"); err == nil {
			t.Fatal("multi-link leaf was accepted")
		}
	})

	t.Run("ancestor symlink", func(t *testing.T) {
		lease, runPath, _ := newFixture(t)
		attempts := filepath.Join(runPath, "attempts")
		if err := os.Rename(attempts, attempts+".held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("attempts.held", attempts); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFileUnderLease(lease, 64, "attempts", "attempt-1", "lineage.json"); err == nil {
			t.Fatal("ancestor symlink was accepted")
		}
	})

	t.Run("ancestor rename ABA", func(t *testing.T) {
		lease, runPath, _ := newFixture(t)
		attempts := filepath.Join(runPath, "attempts")
		replacement := filepath.Join(runPath, "attempts.replacement")
		if err := os.Mkdir(replacement, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := readFileUnderLease(lease, 64, func() {
			if renameErr := os.Rename(attempts, attempts+".held"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if renameErr := os.Rename(replacement, attempts); renameErr != nil {
				t.Fatal(renameErr)
			}
		}, "attempts", "attempt-1", "lineage.json")
		if err == nil {
			t.Fatal("ancestor rename ABA was accepted")
		}
	})

	t.Run("run root swap", func(t *testing.T) {
		lease, runPath, _ := newFixture(t)
		replacement := runPath + ".replacement"
		if err := os.Mkdir(replacement, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := readFileUnderLease(lease, 64, func() {
			if renameErr := os.Rename(runPath, runPath+".held"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if renameErr := os.Rename(replacement, runPath); renameErr != nil {
				t.Fatal(renameErr)
			}
		}, "attempts", "attempt-1", "lineage.json")
		if err == nil {
			t.Fatal("run root swap was accepted")
		}
	})

	t.Run("worker request immutable crash replay", func(t *testing.T) {
		lease, _, _ := newFixture(t)
		attempt, err := OpenDirectoryUnderLease(lease, "attempts", "attempt-1")
		if err != nil {
			t.Fatal(err)
		}
		defer attempt.Close()
		request := []byte("{\"kind\":\"WorkerRequest\"}\n")
		if err := WriteFileInDirectory(attempt, "worker-request.json", request, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteFileInDirectory(attempt, "worker-request.json", request, 0o600); err != nil {
			t.Fatalf("exact crash replay was rejected: %v", err)
		}
		if err := WriteFileInDirectory(attempt, "worker-request.json", []byte("{\"kind\":\"forged\"}\n"), 0o600); err == nil {
			t.Fatal("different crash replay replaced immutable WorkerRequest")
		}
	})
}
