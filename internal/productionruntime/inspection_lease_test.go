package productionruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/runstore"
)

type inspectionWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *inspectionWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestInspectionLeaseWaitsForCurrentWriterOrCancellation(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer-releases", true: "caller-cancels"}[cancelWait], func(t *testing.T) {
			root := t.TempDir()
			store := runstore.New(root)
			held, err := store.Acquire("run:inspect")
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			ownerPath := filepath.Join(root, "runs", "run:inspect", "lease.lock.owner")
			before, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := &inspectionWaitContext{Context: ctx, waiting: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				lease, err := acquireInspectionLease(observed, store, "run:inspect")
				if lease != nil {
					err = errors.Join(err, lease.Release())
				}
				done <- err
			}()
			select {
			case <-observed.waiting:
			case <-time.After(time.Second):
				t.Fatal("query did not enter cancellable contention wait")
			}
			if cancelWait {
				cancel()
			} else if err := held.Release(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if cancelWait && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
				if !cancelWait && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("query failed to complete")
			}
			if cancelWait {
				after, err := os.ReadFile(ownerPath)
				if err != nil || string(before) != string(after) {
					t.Fatal("cancelled waiter changed lease owner")
				}
				if lease, err := store.AcquireExisting("run:inspect"); !errors.Is(err, runstore.ErrLeaseHeld) {
					if lease != nil {
						_ = lease.Release()
					}
					t.Fatalf("waiter released writer lease: %v", err)
				}
			}
		})
	}
}

func TestInspectionLeaseRejectsMissingRunAndPreCancelledContext(t *testing.T) {
	root := t.TempDir()
	store := runstore.New(root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lease, err := acquireInspectionLease(ctx, store, "missing"); lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: %v", err)
	}
	if lease, err := acquireInspectionLease(context.Background(), store, "missing"); lease != nil || err == nil || errors.Is(err, runstore.ErrLeaseHeld) {
		t.Fatalf("missing Run must fail without retry: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("inspection created missing Run")
	}
}
