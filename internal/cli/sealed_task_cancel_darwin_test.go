//go:build darwin && arm64

package cli

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"runtime"
	"testing"
	"time"
)

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
