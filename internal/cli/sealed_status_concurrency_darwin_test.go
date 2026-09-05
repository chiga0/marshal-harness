//go:build darwin && arm64

package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

// The unclaimed session intentionally fails current-owner validation. The
// property under test is reaching that validation without waiting for a
// repository mutation, not manufacturing a successful authority projection.
func TestSealedStatusDoesNotWaitForMutation(t *testing.T) {
	adapter := &sealedRepositoryApplication{session: &productionruntime.RepositorySession{}}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := adapter.Status(context.Background(), application.StatusRequest{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unclaimed owner must not report ready")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Status waited for the mutation lock")
	}
}

func TestSealedStatusCloseConcurrency(t *testing.T) {
	// No authority resource is supplied: this checks the adapter lifetime
	// synchronization under -race without invoking a fake production runtime.
	adapter := &sealedRepositoryApplication{}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 50 {
				if _, err := adapter.Status(context.Background(), application.StatusRequest{}); err == nil {
					t.Error("missing/closed session must not report ready")
				}
			}
		}()
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSealedStatusInvalidReceiver(t *testing.T) {
	var adapter *sealedRepositoryApplication
	if _, err := adapter.Status(context.Background(), application.StatusRequest{}); err == nil {
		t.Fatal("nil adapter accepted")
	}
}
