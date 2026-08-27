package sandboxbridge

import (
	"context"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/sandbox/local"
)

func TestRunWorkerAgainstLocalRunner(t *testing.T) {
	root := t.TempDir()
	runner, err := local.NewLocalRunner(root, time.Now)
	if err != nil {
		t.Fatalf("NewLocalRunner: %v", err)
	}
	bridge, err := NewBridge(runner)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	adapter := &fakeAdapter{id: "fake"}
	record, err := bridge.RunWorker(context.Background(), adapter, validRequest(t))
	if err != nil {
		t.Fatalf("RunWorker against LOCAL provider: %v", err)
	}
	_ = record
}
