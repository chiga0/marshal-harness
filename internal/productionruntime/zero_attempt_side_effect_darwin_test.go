//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type zeroAttemptProductionFixture struct {
	ownerFixture ownerLockFixture
	store        *resultingress.DurableStore
	owner        repositoryOwnerLock
	runStore     *runstore.Store
	runLease     *runstore.Lease
	runVerifier  *runstore.AttemptRunAuthorityVerifier
	dispatch     *dispatch.LeaseLedger
	ready        resultingress.ReadyRunAuthority
}

func newZeroAttemptProductionFixture(t *testing.T) zeroAttemptProductionFixture {
	t.Helper()
	ownerFixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, ownerFixture)
	acquisition := acquisitionAtEpoch(1)
	phase, err := openRepositoryOwnerScopeLock(ownerFixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	_ = acquireAndReplay(t, phase, store, 0, "", acquisition)
	owner, err := phase.bindAcquisition(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := phase.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	runStore := runstore.New(filepath.Join(ownerFixture.base, "run-store"))
	runID := "run:zero-attempt-production"
	runLease, err := runStore.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runLease.Release() })
	timestamp := time.Unix(1_800_000_000, 0).UTC()
	planned := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:zero-attempt-planned", RunID: runID, Sequence: 1, Type: "run.transition", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: timestamp, Payload: map[string]any{}}
	readyEvent := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:zero-attempt-ready", RunID: runID, Sequence: 2, Type: "run.transition", StateFrom: domain.StatePlanned, StateTo: domain.StateReady, Timestamp: timestamp.Add(time.Second), Payload: map[string]any{}}
	specDigest := canonical.DigestBytes([]byte("zero-attempt-spec"))
	policyDigest := canonical.DigestBytes([]byte("zero-attempt-policy"))
	capabilityDigest := canonical.DigestBytes([]byte("zero-attempt-capability"))
	baseSHA := strings.Repeat("a", 40)
	worktreePath := filepath.Join(ownerFixture.base, "worktree")
	readyEvent.Payload = map[string]any{"specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capabilityDigest, "baseSha": baseSHA, "worktreePath": worktreePath, "maxAttempts": 3}
	if err := runStore.Append(runLease, planned, 0); err != nil {
		t.Fatal(err)
	}
	if err := runStore.Append(runLease, readyEvent, 1); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:zero-attempt-production", runID, timestamp)
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, readyEvent)
	if err != nil {
		t.Fatal(err)
	}
	state.SpecDigest, state.PolicyDigest, state.CapabilityDigest = specDigest, policyDigest, capabilityDigest
	state.BaseSHA, state.WorktreePath = baseSHA, worktreePath
	if err := runStore.WriteSnapshot(runLease, state); err != nil {
		t.Fatal(err)
	}
	projection, err := runStore.ReadRunStartAuthorityUnderLease(context.Background(), runLease)
	if err != nil {
		t.Fatal(err)
	}
	ready := resultingress.ReadyRunAuthority{AuthorityNamespaceID: acquisition.Scope.AuthorityNamespaceID, TaskID: projection.Run.TaskID, RunID: projection.Run.RunID, OrchestratorID: "orchestrator:zero-attempt-production", ReadySequence: projection.Run.Sequence, ReadyAuthorityHead: projection.Run.AuthorityHead, AttemptsUsed: projection.AttemptsUsed, MaxAttempts: projection.MaxAttempts, SpecDigest: projection.SpecDigest, PolicyDigest: projection.PolicyDigest, CapabilityDigest: projection.CapabilityDigest, BaseSHA: projection.BaseSHA, WorktreePath: projection.WorktreePath}
	runVerifier, err := runstore.NewAttemptRunAuthorityVerifier(runStore, runLease, acquisition.Scope.AuthorityNamespaceID, ready.OrchestratorID)
	if err != nil {
		t.Fatal(err)
	}
	leaseLedger, err := dispatch.NewLeaseLedger(filepath.Join(ownerFixture.base, "dispatch-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	return zeroAttemptProductionFixture{ownerFixture: ownerFixture, store: store, owner: owner, runStore: runStore, runLease: runLease, runVerifier: runVerifier, dispatch: leaseLedger, ready: ready}
}

func resultIngressBytes(t *testing.T, fixture zeroAttemptProductionFixture) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixture.ownerFixture.base, "result-ingress", "result-ingress.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type zeroAttemptResponseLoss struct {
	verifier *zeroAttemptSideEffectVerifier
	err      error
}

func (loss zeroAttemptResponseLoss) WithZeroAttemptSideEffects(ctx context.Context, store *resultingress.DurableStore, state resultingress.AttemptReservationState, fn func(resultingress.ZeroSideEffectProof) error) error {
	if err := loss.verifier.WithZeroAttemptSideEffects(ctx, store, state, fn); err != nil {
		return err
	}
	return loss.err
}

func TestProductionZeroAttemptCancellationClosesResponseLossAndExactReplay(t *testing.T) {
	fixture := newZeroAttemptProductionFixture(t)
	reservation, err := fixture.store.ReserveAttempt(context.Background(), fixture.runVerifier, fixture.ready)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newZeroAttemptSideEffectVerifier(fixture.owner, fixture.owner.acquisition(), fixture.runVerifier, fixture.dispatch, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("injected zero-side-effect response loss")
	if _, err := fixture.store.CancelAttemptReservation(context.Background(), zeroAttemptResponseLoss{verifier: verifier, err: lost}, reservation.ReservationFactDigest); !errors.Is(err, resultingress.ErrAttemptReservationConflict) {
		t.Fatalf("response-loss classification=%v", err)
	}
	afterLoss := resultIngressBytes(t, fixture)
	cancelled, err := fixture.store.CancelAttemptReservation(context.Background(), verifier, reservation.ReservationFactDigest)
	if err != nil || cancelled.Status != resultingress.AttemptReservationCancelled {
		t.Fatalf("replay=%+v err=%v", cancelled, err)
	}
	replay, err := fixture.store.CancelAttemptReservation(context.Background(), verifier, reservation.ReservationFactDigest)
	if err != nil || replay != cancelled || !bytes.Equal(afterLoss, resultIngressBytes(t, fixture)) {
		t.Fatalf("second replay=%+v err=%v", replay, err)
	}
}

func TestProductionZeroAttemptCancellationRejectsOwnerABAAndForeignStore(t *testing.T) {
	fixture := newZeroAttemptProductionFixture(t)
	reservation, err := fixture.store.ReserveAttempt(context.Background(), fixture.runVerifier, fixture.ready)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newZeroAttemptSideEffectVerifier(fixture.owner, fixture.owner.acquisition(), fixture.runVerifier, fixture.dispatch, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := resultingress.OpenResultIngressStore(filepath.Join(fixture.ownerFixture.base, "foreign-result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := verifier.WithZeroAttemptSideEffects(context.Background(), foreign, reservation, func(resultingress.ZeroSideEffectProof) error { called = true; return nil }); err == nil || called {
		t.Fatalf("foreign ResultIngress err=%v called=%t", err, called)
	}
	before := resultIngressBytes(t, fixture)
	renameAwayAndBackSameObject(t, fixture.ownerFixture.ownerPath, filepath.Join(fixture.ownerFixture.base, "owner-moved"))
	if _, err := fixture.store.CancelAttemptReservation(context.Background(), verifier, reservation.ReservationFactDigest); err == nil {
		t.Fatal("owner ABA authorized cancellation")
	}
	if !bytes.Equal(before, resultIngressBytes(t, fixture)) {
		t.Fatal("owner ABA rejection changed ResultIngress ledger")
	}
}
