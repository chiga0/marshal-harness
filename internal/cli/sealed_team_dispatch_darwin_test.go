//go:build darwin && arm64

package cli

import (
	"context"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

func TestInitialTeamDispatchDoesNotQueueBehindWriter(t *testing.T) {
	adapter := &sealedRepositoryApplication{}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- adapter.advanceInitialTeams(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("team dispatch queued behind writer")
	}
}

func TestInitialTeamDispatchReadFailureStopsOnlyTeamLoop(t *testing.T) {
	// Deliberately uncomposed fixture. No real owner, Worker or Start exists.
	adapter := &sealedRepositoryApplication{session: &productionruntime.RepositorySession{}}
	if err := adapter.advanceInitialTeams(context.Background()); err == nil || !adapter.teamProgressStopped.Load() {
		t.Fatal("unknown authority did not stop team dispatch")
	}
	if err := adapter.advanceInitialTeams(context.Background()); err != nil {
		t.Fatal("circuit breaker retried the failed reader")
	}
	for _, phase := range []domain.State{domain.StateRunning, domain.StateVerifying} {
		if err := adapter.advanceInitialTeamProgress(context.Background(), &fixedcontrolplane.HTTPRouter{}, phase); err != nil {
			t.Fatal("dispatch circuit failed to stop result progression")
		}
	}
	if adapter.closed {
		t.Fatal("team failure closed server")
	}
	if err := adapter.advanceBusinessDeadlines(context.Background()); err != nil {
		t.Fatal("team circuit breaker disabled independent deadline loop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.advanceInitialTeams(ctx); err == nil {
		t.Fatal("cancellation ignored")
	}
}
