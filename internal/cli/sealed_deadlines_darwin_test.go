//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeadlineRunBatchIsBoundedFairAndRebuildable(t *testing.T) {
	runs := map[string]struct{}{"a": {}, "b": {}, "c": {}, "d": {}, "e": {}}
	for _, tc := range []struct {
		cursor string
		limit  int
		want   []string
	}{
		{"", 3, []string{"a", "b", "c"}},
		{"c", 3, []string{"d", "e", "a"}},
		{"z", 3, []string{"a", "b", "c"}},
		{"b", 10, []string{"c", "d", "e", "a", "b"}},
		{"b", 0, nil},
	} {
		if got := deadlineRunBatch(runs, tc.cursor, tc.limit); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("cursor=%s got=%v want=%v", tc.cursor, got, tc.want)
		}
	}
	delete(runs, "c")
	if got := deadlineRunBatch(runs, "c", 3); !reflect.DeepEqual(got, []string{"d", "e", "a"}) {
		t.Fatalf("removed cursor: %v", got)
	}
}

func TestDeadlineDriverCancelsInflightStepAndDoesNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 2)
	started, finished := make(chan struct{}), make(chan struct{})
	var calls, reports atomic.Int32
	go func() {
		defer close(finished)
		driveResidentReconciliation(ctx, ticks, func(step context.Context) error {
			calls.Add(1)
			deadline, ok := step.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				reports.Add(100)
			}
			close(started)
			<-step.Done()
			return step.Err()
		}, func(error) { reports.Add(1) })
	}()
	ticks <- time.Now()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("driver did not start")
	}
	// A queued tick cannot create a concurrent step while the first is active.
	ticks <- time.Now()
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("driver did not drain")
	}
	if calls.Load() != 1 || reports.Load() != 0 {
		t.Fatalf("calls=%d reports=%d", calls.Load(), reports.Load())
	}
}

func TestDeadlineDriverReportsFailureAndClosedTicksExit(t *testing.T) {
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	close(ticks)
	want := errors.New("bounded failure")
	var calls, reports int
	driveResidentReconciliation(context.Background(), ticks, func(context.Context) error { calls++; return want }, func(err error) {
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
		reports++
	})
	if calls != 1 || reports != 1 {
		t.Fatalf("calls=%d reports=%d", calls, reports)
	}
}

func TestDeadlineAdvanceNeverQueuesBehindPublicMutation(t *testing.T) {
	adapter := &sealedRepositoryApplication{}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- adapter.advanceBusinessDeadlines(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background step queued behind writer")
	}
}
