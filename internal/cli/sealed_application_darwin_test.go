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
	collectErrs  []error
	started      application.RunStartProjection
}

func TestRecoverSealedRepositoryOnOpenSeparatesOneShotAndResidentModes(t *testing.T) {
	recoverCalls := 0
	recoverRuns := func(context.Context) error {
		recoverCalls++
		return nil
	}
	if err := recoverSealedRepositoryOnOpen(context.Background(), sealedRepositoryRecoveryOneShot, recoverRuns); err != nil {
		t.Fatalf("one-shot recovery policy error = %v", err)
	}
	if recoverCalls != 0 {
		t.Fatalf("one-shot recovery calls = %d, want 0", recoverCalls)
	}
	if err := recoverSealedRepositoryOnOpen(context.Background(), sealedRepositoryRecoveryResident, recoverRuns); err != nil {
		t.Fatalf("resident recovery policy error = %v", err)
	}
	if recoverCalls != 1 {
		t.Fatalf("resident recovery calls = %d, want 1", recoverCalls)
	}
}

func TestAdvanceSealedRunWithOpenComposesTargetOnce(t *testing.T) {
	running := sealedRunProjection(domain.StateRunning, 52)
	stub := &sealedRunAdvancerStub{
		projections: []application.RunProjection{running},
		collectErr:  productionruntime.ErrAttemptStillRunning,
	}
	openCalls := 0
	closeCalls := 0
	got, stage, err := advanceSealedRunWithOpen(context.Background(), running.RunID, func(_ context.Context, runID string) (sealedRunAdvancer, func() error, error) {
		openCalls++
		if runID != running.RunID {
			t.Fatalf("open runID = %q, want %q", runID, running.RunID)
		}
		return stub, func() error { closeCalls++; return nil }, nil
	})
	if err != nil {
		t.Fatalf("advanceSealedRunWithOpen() error = %v", err)
	}
	if stage != "" {
		t.Fatalf("stage = %q, want empty", stage)
	}
	if got != running {
		t.Fatalf("projection = %#v, want %#v", got, running)
	}
	if openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", openCalls)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	if stub.inspectCalls != 1 || stub.collectCalls != 1 {
		t.Fatalf("InspectRun calls = %d, CollectRunResult calls = %d, want 1 each", stub.inspectCalls, stub.collectCalls)
	}
}

func (stub *sealedRunAdvancerStub) InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error) {
	projection := stub.projections[stub.inspectCalls]
	stub.inspectCalls++
	return projection, nil
}

func (stub *sealedRunAdvancerStub) StartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, error) {
	return stub.started, nil
}

func (stub *sealedRunAdvancerStub) CollectRunResult(context.Context, string) (productionruntime.CollectedRunResult, error) {
	if stub.collectCalls < len(stub.collectErrs) {
		err := stub.collectErrs[stub.collectCalls]
		stub.collectCalls++
		return productionruntime.CollectedRunResult{}, err
	}
	stub.collectCalls++
	return productionruntime.CollectedRunResult{}, stub.collectErr
}

func TestDriveSealedRunToWorkerCompletionKeepsOneCompositionUntilVerifying(t *testing.T) {
	running := sealedRunProjection(domain.StateRunning, 52)
	verifying := sealedRunProjection(domain.StateVerifying, 53)
	stub := &sealedRunAdvancerStub{
		projections: []application.RunProjection{running, running, verifying},
		collectErrs: []error{productionruntime.ErrAttemptStillRunning, nil},
	}
	openCalls, closeCalls, waitCalls := 0, 0, 0
	got, stage, err := driveSealedRunToWorkerCompletionWithOpen(context.Background(), running.RunID, func(_ context.Context, runID string) (sealedRunAdvancer, func() error, error) {
		openCalls++
		if runID != running.RunID {
			t.Fatalf("open runID = %q, want %q", runID, running.RunID)
		}
		return stub, func() error { closeCalls++; return nil }, nil
	}, func(context.Context) error {
		waitCalls++
		return nil
	})
	if err != nil || stage != "" {
		t.Fatalf("drive error = %v, stage = %q", err, stage)
	}
	if got != verifying {
		t.Fatalf("projection = %#v, want %#v", got, verifying)
	}
	if openCalls != 1 || closeCalls != 1 {
		t.Fatalf("open calls = %d, close calls = %d, want 1 each", openCalls, closeCalls)
	}
	if waitCalls != 1 || stub.collectCalls != 2 || stub.inspectCalls != 3 {
		t.Fatalf("wait calls = %d, collect calls = %d, inspect calls = %d; want 1, 2, 3", waitCalls, stub.collectCalls, stub.inspectCalls)
	}
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
