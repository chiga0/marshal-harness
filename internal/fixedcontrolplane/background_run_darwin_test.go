//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"testing"
	"time"
)

func TestBackgroundTeamRunLanesAndVerificationYield(t *testing.T) {
	for _, verifying := range []bool{false, true} {
		port, delivery := testHTTPApplication()
		router, err := NewHTTPRouter(port, delivery)
		if err != nil {
			t.Fatal(err)
		}
		called, err := router.TryBackgroundRunMutation(context.Background(), "run-team", verifying, func(ctx context.Context) error {
			if release, err := router.runMutations.tryAcquire(ctx, "run-team"); err != nil || release != nil {
				t.Fatal("same Run overlap")
			}
			other, err := router.TryBackgroundMutation(ctx, func(context.Context) error { return nil })
			if err != nil || other != verifying {
				t.Fatal("wrong global writer lifetime")
			}
			other, err = router.TryBackgroundRunMutation(ctx, "run-other", false, func(context.Context) error { return nil })
			if err != nil || other != verifying {
				t.Fatal("verification starved sibling collection")
			}
			return nil
		})
		if !called || err != nil || len(router.runMutations.entries) != 0 || len(router.mutation) != 0 {
			t.Fatal("lane leak")
		}
		if err := router.acquireMutation(context.Background()); err != nil {
			t.Fatal(err)
		}
		called, err = router.TryBackgroundRunMutation(context.Background(), "run-team", verifying, func(context.Context) error { t.Fatal("queued behind writer"); return nil })
		if called || err != nil || len(router.runMutations.entries) != 0 {
			t.Fatal("busy writer not skipped")
		}
		router.releaseMutation()
	}
}

func TestBackgroundRunReleasePreservesPublicWaiter(t *testing.T) {
	var lanes runMutationLanes
	release, err := lanes.tryAcquire(context.Background(), "run-team")
	if err != nil || release == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := &mutationWaitContext{Context: ctx, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		unlock, err := lanes.acquire(waiting, "run-team")
		if unlock != nil {
			unlock()
		}
		done <- err
	}()
	select {
	case <-waiting.entered:
	case <-time.After(time.Second):
		t.Fatal("public waiter missing")
	}
	if next, err := lanes.tryAcquire(ctx, "run-team"); next != nil || err != nil {
		t.Fatal("background jumped queue")
	}
	release()
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter lost")
	}
	lanes.mu.Lock()
	defer lanes.mu.Unlock()
	if len(lanes.entries) != 0 {
		t.Fatal("entry retained")
	}
}
