//go:build darwin && arm64

package cli

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

type sealedRunAdvancerStub struct {
	projections  []application.RunProjection
	inspectCalls int
	collectCalls int
	collectErr   error
}

func (stub *sealedRunAdvancerStub) InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error) {
	projection := stub.projections[stub.inspectCalls]
	stub.inspectCalls++
	return projection, nil
}

func (*sealedRunAdvancerStub) StartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, error) {
	return application.RunStartProjection{}, nil
}

func (stub *sealedRunAdvancerStub) CollectRunResult(context.Context, string) (productionruntime.CollectedRunResult, error) {
	stub.collectCalls++
	return productionruntime.CollectedRunResult{}, stub.collectErr
}

func TestAdvanceSealedRunReturnsVerifiedRunningProjectionWithoutSecondInspect(t *testing.T) {
	running := sealedRunProjection(domain.StateRunning, 52)
	stub := &sealedRunAdvancerStub{
		projections: []application.RunProjection{running},
		collectErr:  productionruntime.ErrAttemptStillRunning,
	}

	got, stage, err := advanceSealedRun(context.Background(), stub, running.RunID)
	if err != nil {
		t.Fatalf("advanceSealedRun() error = %v", err)
	}
	if stage != "" {
		t.Fatalf("stage = %q, want empty", stage)
	}
	if got != running {
		t.Fatalf("projection = %#v, want %#v", got, running)
	}
	if stub.inspectCalls != 1 {
		t.Fatalf("InspectRun calls = %d, want 1", stub.inspectCalls)
	}
	if stub.collectCalls != 1 {
		t.Fatalf("CollectRunResult calls = %d, want 1", stub.collectCalls)
	}
}

func TestAdvanceSealedRunInspectsVerifyingProjectionAfterSuccessfulCollect(t *testing.T) {
	running := sealedRunProjection(domain.StateRunning, 52)
	verifying := sealedRunProjection(domain.StateVerifying, 53)
	stub := &sealedRunAdvancerStub{
		projections: []application.RunProjection{running, verifying},
	}

	got, stage, err := advanceSealedRun(context.Background(), stub, running.RunID)
	if err != nil {
		t.Fatalf("advanceSealedRun() error = %v", err)
	}
	if stage != "" {
		t.Fatalf("stage = %q, want empty", stage)
	}
	if got != verifying {
		t.Fatalf("projection = %#v, want %#v", got, verifying)
	}
	if stub.inspectCalls != 2 {
		t.Fatalf("InspectRun calls = %d, want 2", stub.inspectCalls)
	}
	if stub.collectCalls != 1 {
		t.Fatalf("CollectRunResult calls = %d, want 1", stub.collectCalls)
	}
}

func sealedRunProjection(state domain.State, sequence uint64) application.RunProjection {
	return application.RunProjection{
		TaskID:        "task:test",
		RunID:         "run:test",
		AttemptID:     "attempt:test",
		State:         state,
		Sequence:      sequence,
		AuthorityHead: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}
