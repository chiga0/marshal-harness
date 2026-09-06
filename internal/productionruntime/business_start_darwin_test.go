//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func writeBusinessSpecFixture(t *testing.T, runDirectory, taskID string) string {
	t.Helper()
	task := domain.TaskSpec{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask, Budgets: domain.TaskBudgets{RunTimeoutSeconds: 600, AttemptTimeoutSeconds: 120}}
	task.Metadata.ID = taskID
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "task-spec.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestBusinessStartRejectsExpiredReadyWithoutAttemptSideEffects(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			fixture := newCompositionFixture(t)
			ctx := context.Background()
			budget, err := fixture.ledger.runs.ReadBusinessBudgetUnderLease(ctx, fixture.ledger.runLease)
			if err != nil {
				t.Fatal(err)
			}
			fixture.ledger.now = func() time.Time { return budget.CreatedAt.Add(600*time.Second + offset) }
			before, err := fixture.ledger.ingress.AttemptStates()
			if err != nil {
				t.Fatal(err)
			}
			err = fixture.ledger.admitBusinessStart(ctx, budget.Run)
			if offset < 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, resultingress.ErrBusinessDeadlineExceeded) {
				t.Fatalf("boundary admission: %v", err)
			}
			prepared, err := fixture.ledger.PrepareRunStart(ctx, fixture.ledger.owner, fixture.acquisition, application.PrepareRunStartRequest{RunID: budget.Run.RunID, ExpectedSequence: budget.Run.Sequence, ExpectedAuthorityHead: budget.Run.AuthorityHead})
			if !errors.Is(err, resultingress.ErrBusinessDeadlineExceeded) || prepared != (application.PreparedRunStart{}) {
				t.Fatalf("expired preparation returned %+v, %v", prepared, err)
			}
			after, err := fixture.ledger.ingress.AttemptStates()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("expired preparation changed Attempts: %v", err)
			}
			run, err := fixture.ledger.runs.ReadCurrentRunProjectionUnderLease(fixture.ledger.runLease)
			if err != nil || run != budget.Run {
				t.Fatalf("expired preparation changed Run: %+v, %v", run, err)
			}
		})
	}
}

func TestBusinessStartRejectsDifferentRunProjection(t *testing.T) {
	fixture := newCompositionFixture(t)
	budget, err := fixture.ledger.runs.ReadBusinessBudgetUnderLease(context.Background(), fixture.ledger.runLease)
	if err != nil {
		t.Fatal(err)
	}
	forged := budget.Run
	forged.Sequence++
	if err := fixture.ledger.admitBusinessStart(context.Background(), forged); err == nil {
		t.Fatal("budget from a different current Run was accepted")
	}
}

func TestBusinessStartRechecksBudgetAfterPreparationBeforeBridgeLaunch(t *testing.T) {
	fixture := newCompositionFixture(t)
	ctx := context.Background()
	budget, err := fixture.ledger.runs.ReadBusinessBudgetUnderLease(ctx, fixture.ledger.runLease)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.ledger.PrepareRunStart(ctx, fixture.ledger.owner, fixture.acquisition, application.PrepareRunStartRequest{RunID: budget.Run.RunID, ExpectedSequence: budget.Run.Sequence, ExpectedAuthorityHead: budget.Run.AuthorityHead})
	if err != nil {
		t.Fatal(err)
	}
	before, err := fixture.ledger.ingress.AttemptStates()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := launchidentity.Pi0844IdentityFromClosure(fixture.ledger.closure)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewPi0844Profile(fixture.ledger.closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ledger.now = func() time.Time { return budget.CreatedAt.Add(600 * time.Second) }
	bridge := &piBridge{ledger: fixture.ledger}
	if err := bridge.StartPreparedRun(ctx, fixture.ledger.owner, fixture.acquisition, OwnerProjection{}, profile, prepared); !errors.Is(err, resultingress.ErrBusinessDeadlineExceeded) {
		t.Fatalf("delayed start: %v", err)
	}
	after, err := fixture.ledger.ingress.AttemptStates()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("expired prepared launch changed Attempts: %v", err)
	}
	if _, found, err := fixture.ledger.RehydrateRunStartOutcome(ctx, fixture.ledger.owner, fixture.acquisition, prepared.PreparationDigest); err != nil || found {
		t.Fatalf("expired start published an outcome: found=%t err=%v", found, err)
	}
}
