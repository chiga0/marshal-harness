//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"runtime"
	"testing"
	"time"
)

func TestTaskCancellationSelectionAdvancesFailedTaskWithoutHidingErrors(t *testing.T) {
	a := &sealedRepositoryApplication{}
	localFailure := errors.New("local Run projection failure")
	ownerFailure := application.NewError("pending-task", application.ReasonOwnerNotCurrent)
	ledgerFailure := errors.New("RB1 unavailable")
	read := func(cursor string) (string, []application.RunProjection, error) {
		switch cursor {
		case "":
			return "task-a", nil, localFailure
		case "task-a":
			return "task-b", []application.RunProjection{{RunID: "run-b"}}, nil
		default:
			return "", nil, ownerFailure
		}
	}
	if id, _, err := a.selectTaskCancellation(read); id != "task-a" || !errors.Is(err, localFailure) || a.taskCancelCursor != "task-a" {
		t.Fatal("local failure lost its diagnostic or held the cursor")
	}
	if id, runs, err := a.selectTaskCancellation(read); id != "task-b" || err != nil || len(runs) != 1 {
		t.Fatal("healthy sibling was starved")
	}
	if _, _, err := a.selectTaskCancellation(read); !errors.Is(err, ownerFailure) || a.taskCancelCursor != "task-b" {
		t.Fatal("owner failure swallowed or cursor invented")
	}
	if _, _, err := a.selectTaskCancellation(func(string) (string, []application.RunProjection, error) { return "task-b", nil, ledgerFailure }); !errors.Is(err, ledgerFailure) {
		t.Fatal("post-selection ledger failure swallowed")
	}
}

func TestTaskCancellationReadReleasesBeforeCloseAndCancelMutex(t *testing.T) {
	a := &sealedRepositoryApplication{session: &productionruntime.RepositorySession{}}
	selected := make(chan struct{})
	releaseRead := make(chan struct{})
	done := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		_ = a.withTaskCancellationRead(func() error { close(selected); <-releaseRead; return nil })
		// The actual CancelRun takes mu only after the selection read guard
		// was released. Closed composition must reject it without openRun.
		_, _ = a.CancelRun(context.Background(), application.CancelRunRequest{})
		close(done)
	}()
	<-selected
	go func() { closed <- a.Close() }()
	// Wait until actual Close owns mu and is blocked on the read guard.
	// No time-based ordering or sleeps: TryLock observes the mutex boundary.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for a.mu.TryLock() {
		a.mu.Unlock()
		select {
		case <-deadline.C:
			close(releaseRead)
			t.Fatal("Close did not acquire mu")
		default:
			runtime.Gosched()
		}
	}
	close(releaseRead)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind cancellation status lock")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel mutation deadlocked against Close")
	}
}
