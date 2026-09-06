//go:build unix

package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// CreationGuard must hold the current repository owner and recheck the exact
// approved RB1 creation fact around fn. It belongs to trusted composition, not
// request input. No long Git/precondition/provider operation runs inside it.
type CreationGuard func(context.Context, func() error) error

// ReconcileCreation completes only the original unstarted Run. A partial
// failure leaves its worktree/files/journal intact for the same obligation;
// there is no reset, deletion, new identity, probe, Attempt or Worker launch.
func (p *PreparedPlan) ReconcileCreation(ctx context.Context, guard CreationGuard) (result Result, err error) {
	if ctx == nil || guard == nil || p == nil || p.selection.Adapter == nil || p.repositoryRoot == "" {
		return Result{}, errors.New("planning: current creation authority is required")
	}
	checked := func(fn func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		called := false
		var callbackErr error
		err := guard(ctx, func() error {
			if called {
				callbackErr = errors.New("planning: repeated creation authority callback")
				return callbackErr
			}
			called = true
			if err := ctx.Err(); err != nil {
				return err
			}
			callbackErr = fn()
			return callbackErr
		})
		if err != nil || callbackErr != nil {
			return errors.Join(err, callbackErr)
		}
		if !called {
			return errors.New("planning: creation authority was not checked")
		}
		return ctx.Err()
	}
	if err := checked(func() error { return nil }); err != nil {
		return Result{}, err
	}
	repository, err := gitworktree.OpenContext(ctx, p.input.RepositoryRoot)
	if err != nil || repository.Root != p.repositoryRoot {
		return Result{}, errors.New("planning: prepared repository changed")
	}
	remote, err := ResolveRemote(ctx, repository.Root, p.task.Repository.Remote)
	if err != nil || remote != p.remoteURL {
		return Result{}, errors.New(errRemoteURLMismatch)
	}
	id, err := adapter.ValidateCapability(p.selection.Capability, p.task)
	if err != nil || id != p.selection.Adapter.ID() {
		return Result{}, errors.New(errCapabilityAdapterMismatch)
	}
	store := runstore.New(p.input.StateRoot)
	var lease *runstore.Lease
	err = checked(func() error {
		var acquireErr error
		lease, acquireErr = store.Acquire(p.input.RunID)
		return acquireErr
	})
	if lease != nil {
		defer func() { err = errors.Join(err, lease.Release()) }()
	}
	if err != nil {
		return Result{}, err
	}
	// Creation has exactly two small events, not an unbounded historical Run.
	if _, err := runstore.ReadFileUnderLease(lease, 64<<10, "events.jsonl"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	existing, truncated, err := store.ReadEventsUnderLease(lease)
	if truncated || (err != nil && !errors.Is(err, os.ErrNotExist)) || len(existing) > 2 {
		return Result{}, errors.New("planning: creation journal conflicts")
	}
	for _, event := range existing {
		if event.AttemptID != "" || (event.StateTo != domain.StatePlanned && event.StateTo != domain.StateReady) {
			return Result{}, errors.New("planning: creation Run already advanced")
		}
	}
	// Validate existing immutable inputs before changing any Git metadata.
	files := []struct {
		name    string
		data    []byte
		missing bool
	}{
		{"task-spec.json", p.taskCanonical, false}, {"policy-snapshot.json", p.policyCanonical, false}, {"capability-snapshot.json", p.capabilityCanonical, false},
	}
	for index, file := range files {
		data, readErr := runstore.ReadFileUnderLease(lease, int64(len(file.data)+1), file.name)
		if errors.Is(readErr, os.ErrNotExist) {
			files[index].missing = true
			continue
		}
		if readErr != nil || !bytes.Equal(data, file.data) {
			return Result{}, errors.New("planning: frozen creation input conflicts")
		}
	}
	if err := checked(func() error { return nil }); err != nil {
		return Result{}, err
	}
	worktree, err := repository.RecoverPreparationForRun(p.input.StateRoot, p.task.Metadata.ID, p.input.RunID, p.baseSHA)
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, worktree.Release()) }()
	events, states, err := p.creationTransitions(worktree)
	if err != nil {
		return Result{}, err
	}
	for index, event := range existing {
		want := events[index]
		want.EventID = event.EventID
		if !sameCreationValue(want, event) {
			return Result{}, errors.New("planning: frozen creation event conflicts")
		}
	}
	// Do not hide an alien/advanced snapshot behind a journal rebuild.
	snapshotBytes, snapshotErr := runstore.ReadFileUnderLease(lease, 64<<10, "state.json")
	var snapshot domain.RunState
	if snapshotErr == nil {
		decoder := json.NewDecoder(bytes.NewReader(snapshotBytes))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&snapshot) != nil || snapshot.Sequence > uint64(len(existing)) || !sameCreationValue(snapshot, states[snapshot.Sequence]) {
			return Result{}, errors.New("planning: creation snapshot conflicts")
		}
		if _, err := canonical.JSON(snapshotBytes); err != nil {
			return Result{}, errors.New("planning: malformed creation snapshot")
		}
	} else if !errors.Is(snapshotErr, os.ErrNotExist) {
		return Result{}, snapshotErr
	}
	err = checked(func() error {
		directory, err := runstore.OpenDirectoryUnderLease(lease)
		if err != nil {
			return err
		}
		defer directory.Close()
		for _, file := range files {
			if !file.missing {
				continue
			}
			if err := runstore.WriteFileInDirectory(directory, file.name, file.data, 0o600); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	for index := len(existing); index < len(events); index++ {
		err = checked(func() error {
			if err := store.Append(lease, events[index], uint64(index)); err != nil {
				return err
			}
			return store.WriteSnapshot(lease, states[index+1])
		})
		if err != nil {
			return Result{}, err
		}
	}
	// A journaled READY with a missing/stale snapshot is a normal crash window.
	if len(existing) == 2 && (snapshotErr != nil || snapshot.Sequence != 2) {
		if err := checked(func() error { return store.WriteSnapshot(lease, states[2]) }); err != nil {
			return Result{}, err
		}
	}
	if err := checked(func() error { return nil }); err != nil {
		return Result{}, err
	}
	return Result{State: states[2], Adapter: p.selection.Adapter, SelectionAttempts: append([]adapter.SelectionAttempt(nil), p.selection.Attempts...)}, nil
}

func sameCreationValue(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	left, err = canonical.JSON(left)
	if err != nil {
		return false
	}
	right, err = canonical.JSON(right)
	return err == nil && bytes.Equal(left, right)
}
