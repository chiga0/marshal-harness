//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type fixedDeliveryFixture struct {
	repository string
	session    *RepositorySession
	store      *FixedDeliveryStore
	request    application.StartRunRequest
	deadline   time.Time
}

type fixedStartRunReconcilerStub struct {
	started application.RunStartProjection
	found   bool
	err     error
}

func TestFixedDeliveryRejectsAuthenticatedBindingMismatchBeforePending(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	binding, err := NewFixedStartRunDeliveryBinding("request:bound-mismatch", fixture.request, fixture.deadline)
	if err != nil {
		t.Fatal(err)
	}
	binding.RequestDigest = canonical.DigestBytes([]byte("forged-request"))
	if _, _, err := fixture.store.BeginStartRunBound(context.Background(), "request:bound-mismatch", fixture.request, fixture.deadline, binding); !errors.Is(err, ErrFixedDeliveryConflict) {
		t.Fatalf("BeginStartRunBound err=%v", err)
	}
	leaf := fixedDeliveryPendingLeaf(canonical.DigestBytes([]byte("request:bound-mismatch")))
	if _, found, err := readFixedDeliveryRecord(fixture.session.fixedRoot, leaf, fixedDeliveryMaxRecord); err != nil || found {
		t.Fatalf("binding mismatch published pending: found=%v err=%v", found, err)
	}
}

func TestFixedDeliveryRejectsExpiredOrCancelledBindingBeforePending(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	for _, testCase := range []struct {
		name     string
		deadline time.Time
		context  func() context.Context
	}{
		{name: "expired", deadline: time.Now().UTC().Add(-time.Second), context: context.Background},
		{name: "cancelled", deadline: fixture.deadline, context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			binding, err := NewFixedStartRunDeliveryBinding("request:"+testCase.name, fixture.request, testCase.deadline)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := fixture.store.BeginStartRunBound(testCase.context(), "request:"+testCase.name, fixture.request, testCase.deadline, binding); !errors.Is(err, ErrFixedDeliveryConflict) {
				t.Fatalf("BeginStartRunBound err=%v", err)
			}
			leaf := fixedDeliveryPendingLeaf(canonical.DigestBytes([]byte("request:" + testCase.name)))
			if _, found, err := readFixedDeliveryRecord(fixture.session.fixedRoot, leaf, fixedDeliveryMaxRecord); err != nil || found {
				t.Fatalf("invalid binding published pending: found=%v err=%v", found, err)
			}
		})
	}
}

func (stub fixedStartRunReconcilerStub) ReconcileStartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, bool, error) {
	return stub.started, stub.found, stub.err
}

type publicFixedDeliveryInputs struct {
	repository string
	request    application.StartRunRequest
	inputs     RepositorySessionInputs
}

func newPublicFixedDeliveryInputs(t *testing.T) publicFixedDeliveryInputs {
	t.Helper()
	ownerFixture := newOwnerLockFixture(t)
	// Construct the production held ingress directly. A public composition
	// test must not depend on the legacy pathname Store constructor as an
	// implicit directory producer.
	ingressPath := filepath.Join(ownerFixture.base, "held-result-ingress")
	createOwnerOnlyDirectory(t, ingressPath)
	ingress, err := os.Open(ingressPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ingress.Close() })
	repository := filepath.Join(ownerFixture.base, "repository")
	createOwnerOnlyDirectory(t, repository)
	request := seedFixedDeliveryReadyRun(t, filepath.Join(repository, ".marshal"), "run:public-fixed-delivery")
	heldRepository, err := OpenCanonicalRepositoryRoot(repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = heldRepository.Close() })
	controlPath := filepath.Join(ownerFixture.base, "owner-private-control")
	createOwnerOnlyDirectory(t, controlPath)
	control, err := os.Open(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixed, err = filepath.EvalSymlinks(fixed)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := exactProcessAcquisition(t, fixed)
	acquisition.OwnerEpoch = 0
	return publicFixedDeliveryInputs{repository: repository, request: request, inputs: RepositorySessionInputs{
		HeldIngressDir: ingress, HeldRepositoryRoot: heldRepository, OwnerDirectory: ownerFixture.directory,
		Acquisition: acquisition, FixedMarshalPath: fixed, OwnerPrivateControlRoot: control,
	}}
}

func newFixedDeliveryFixture(t *testing.T) fixedDeliveryFixture {
	ownerFixture := newOwnerLockFixture(t)
	acquisition := currentProcessAcquisition()
	acquisition.OwnerEpoch = 0
	repository, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(repository, ".marshal")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	request := seedFixedDeliveryReadyRun(t, stateRoot, "run:fixed-delivery")
	heldRepository, err := OpenCanonicalRepositoryRoot(repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = heldRepository.Close() })
	phase, err := openRepositoryOwnerScopeLock(ownerFixture.directory, acquisition.Scope)
	if err != nil {
		t.Fatal(err)
	}
	ingress := openOwnerStore(t, ownerFixture)
	owner, ownerState, acquired, err := phase.acquireAndBind(context.Background(), ingress, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	if err := phase.Close(); err != nil {
		t.Fatal(err)
	}
	if err := claimRepositorySessionOwner(owner); err != nil {
		t.Fatal(err)
	}
	var fixedRoot fixedServerRoot
	err = owner.WithCurrentOwnerLock(context.Background(), acquired, func() error {
		var rootErr error
		fixedRoot, rootErr = openFixedServerRoot(heldRepository)
		return rootErr
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := runstore.NewFromStateRootDescriptor(fixedRoot.stateRoot())
	if err != nil {
		t.Fatal(err)
	}
	session := &RepositorySession{ingress: ingress, runs: runs, fixedRoot: fixedRoot, owner: owner, ownerState: ownerState, acquisition: acquired}
	t.Cleanup(func() { _ = session.Close() })
	if rootDigest, digestErr := fixedRoot.digest(); digestErr != nil || rootDigest == "" {
		t.Fatalf("fixed root digest: digest=%q err=%v", rootDigest, digestErr)
	}
	if lockErr := owner.WithCurrentOwnerLock(context.Background(), acquired, func() error {
		current, found, openErr := ingress.OpenOwner(acquired.Scope)
		if openErr != nil {
			return openErr
		}
		if !found || current.Acquisition != acquired || current.FactDigest != ownerState.FactDigest {
			return errors.New("current owner mismatch")
		}
		return nil
	}); lockErr != nil {
		t.Fatalf("current owner: %v", lockErr)
	}
	store, err := session.OpenFixedDeliveryStore(context.Background())
	if err != nil {
		t.Fatalf("open fixed delivery store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return fixedDeliveryFixture{repository: repository, session: session, store: store, request: request, deadline: time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)}
}

func seedFixedDeliveryReadyRun(t *testing.T, stateRoot, runID string) application.StartRunRequest {
	t.Helper()
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	planned := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:fixed-planned", RunID: runID, Sequence: 1, Type: "run.transition", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: now, Payload: map[string]any{}}
	ready := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:fixed-ready", RunID: runID, Sequence: 2, Type: "run.transition", StateFrom: domain.StatePlanned, StateTo: domain.StateReady, Timestamp: now.Add(time.Second), Payload: map[string]any{"specDigest": canonical.DigestBytes([]byte("spec")), "policyDigest": canonical.DigestBytes([]byte("policy")), "capabilityDigest": canonical.DigestBytes([]byte("capability")), "baseSha": strings.Repeat("a", 40), "worktreePath": filepath.Join(filepath.Dir(stateRoot), "worktree"), "maxAttempts": 3}}
	if err := store.Append(lease, planned, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, ready, 1); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:fixed-delivery", runID, now)
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, ready)
	if err != nil {
		t.Fatal(err)
	}
	state.SpecDigest = ready.Payload["specDigest"].(string)
	state.PolicyDigest = ready.Payload["policyDigest"].(string)
	state.CapabilityDigest = ready.Payload["capabilityDigest"].(string)
	state.BaseSHA = ready.Payload["baseSha"].(string)
	state.WorktreePath = ready.Payload["worktreePath"].(string)
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	projection, err := store.ReadRunStartAuthorityUnderLease(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	return application.StartRunRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead}
}

func pendingFiles(t *testing.T, repository string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repository, ".marshal", "runtime-v1", "control", "delivery-v1"))
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "p-") {
			result = append(result, entry.Name())
		}
	}
	return result
}

func fixedDeliveryStarted(request application.StartRunRequest) application.RunStartProjection {
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	prepared := application.PreparedRunStart{
		ProtocolRevision: application.PreparedRunStartProtocolRevision,
		TaskID:           "task:fixed-delivery", RunID: request.RunID, AttemptID: "attempt:fixed-delivery-1",
		ReservationFactDigest: digest("reservation"), AttemptOpenedFactDigest: digest("attempt-opened"),
		AttemptOrdinal: 1, AttemptsUsedBefore: 0, MaxAttempts: 3, State: domain.StateReady,
		Sequence: request.ExpectedSequence, AuthorityHead: request.ExpectedAuthorityHead,
		PreparationDigest: digest("preparation"),
	}
	return application.RunStartProjection{Prepared: prepared, Run: application.RunProjection{
		TaskID: prepared.TaskID, RunID: prepared.RunID, AttemptID: prepared.AttemptID,
		State: domain.StateRunning, Sequence: prepared.Sequence + 1, AuthorityHead: digest("running"),
	}}
}

func TestFixedDeliveryBeginPublishesAndExactlyReplaysPending(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	first, replay, err := fixture.store.BeginStartRun(context.Background(), "request-1", fixture.request, fixture.deadline)
	if err != nil || replay || validateFixedDeliveryPending(first) != nil {
		t.Fatalf("first=%+v replay=%t err=%v", first, replay, err)
	}
	second, replay, err := fixture.store.BeginStartRun(context.Background(), "request-1", fixture.request, fixture.deadline)
	if err != nil || !replay || second != first {
		t.Fatalf("second=%+v replay=%t err=%v", second, replay, err)
	}
	drift := fixture.request
	drift.ExpectedSequence++
	if _, _, err := fixture.store.BeginStartRun(context.Background(), "request-1", drift, fixture.deadline); !errors.Is(err, ErrFixedDeliveryConflict) {
		t.Fatalf("drift err=%v", err)
	}
	if got := pendingFiles(t, fixture.repository); len(got) != 1 {
		t.Fatalf("pending=%v", got)
	}
}

func TestFixedDeliveryReceiptClosesOnlyExactReconciledStart(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	pending, _, err := fixture.store.BeginStartRun(context.Background(), "receipt", fixture.request, fixture.deadline)
	if err != nil {
		t.Fatal(err)
	}
	started := fixedDeliveryStarted(fixture.request)
	reconciler := fixedStartRunReconcilerStub{started: started, found: true}
	receipt, applied, err := fixture.store.ReconcileStartRunDelivery(context.Background(), pending, fixture.request, reconciler)
	if err != nil || !applied || validateFixedDeliveryReceipt(receipt) != nil {
		t.Fatalf("receipt=%+v applied=%t err=%v", receipt, applied, err)
	}
	if receipt.PendingDigest != pending.Digest || receipt.PreparationDigest != started.Prepared.PreparationDigest || receipt.ApplicationReceiptFactDigest != started.Run.AuthorityHead || receipt.PostRevision != started.Run.Sequence || receipt.PostAuthorityHead != started.Run.AuthorityHead {
		t.Fatalf("receipt binding mismatch: %+v", receipt)
	}
	if err := ValidateFixedStartRunDeliveryReceipt(pending, receipt); err != nil {
		t.Fatalf("public receipt validation: %v", err)
	}
	wrongPending := receipt
	wrongPending.PendingDigest = canonical.DigestBytes([]byte("wrong-pending"))
	wrongPending.Digest = ""
	if _, err := sealFixedDeliveryRecord(&wrongPending.Digest, &wrongPending); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFixedStartRunDeliveryReceipt(pending, wrongPending); !errors.Is(err, ErrFixedDeliveryConflict) {
		t.Fatalf("wrong pending receipt err=%v", err)
	}
	replayed, applied, err := fixture.store.ReconcileStartRunDelivery(context.Background(), pending, fixture.request, reconciler)
	if err != nil || !applied || replayed != receipt {
		t.Fatalf("replay=%+v applied=%t err=%v", replayed, applied, err)
	}

	drifted := started
	drifted.Run.AuthorityHead = canonical.DigestBytes([]byte("foreign-running"))
	if _, _, err := fixture.store.ReconcileStartRunDelivery(context.Background(), pending, fixture.request, fixedStartRunReconcilerStub{started: drifted, found: true}); !errors.Is(err, ErrFixedDeliveryConflict) {
		t.Fatalf("drifted durable result err=%v", err)
	}
}

func TestFixedDeliveryMissingOutcomeRemainsPendingAndRunningReplayFindsPending(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	pending, _, err := fixture.store.BeginStartRun(context.Background(), "response-loss", fixture.request, fixture.deadline)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, applied, err := fixture.store.ReconcileStartRunDelivery(context.Background(), pending, fixture.request, fixedStartRunReconcilerStub{}); err != nil || applied || receipt != (FixedDeliveryReceipt{}) {
		t.Fatalf("missing outcome receipt=%+v applied=%t err=%v", receipt, applied, err)
	}

	statePath := filepath.Join(fixture.repository, ".marshal", "runs", fixture.request.RunID, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state domain.RunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.State = domain.StateRunning
	state.CurrentAttemptID = "attempt:fixed-delivery-1"
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := fixture.store.BeginStartRun(context.Background(), "response-loss", fixture.request, fixture.deadline)
	if err != nil || !replay || replayed != pending {
		t.Fatalf("running response-loss replay=%+v replay=%t err=%v", replayed, replay, err)
	}
}

func TestFixedDeliveryProductionWiringUsesPublicRepositorySession(t *testing.T) {
	fixture := newPublicFixedDeliveryInputs(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	store, err := session.OpenFixedDeliveryStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pending, replay, err := store.BeginStartRun(context.Background(), "public-wiring", fixture.request, time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC))
	if err != nil || replay || validateFixedDeliveryPending(pending) != nil {
		t.Fatalf("pending=%+v replay=%t err=%v", pending, replay, err)
	}
	rootDigest, err := session.fixedRoot.digest()
	if err != nil || pending.AuthorityRootDigest != rootDigest {
		t.Fatalf("S1.1 current mutation observation mismatch: pending=%q root=%q err=%v", pending.AuthorityRootDigest, rootDigest, err)
	}
	// The equality above is deliberately session-local. It is not evidence
	// that AuthorityRootDigest is a stable S1.3 or successor identity.
}

func TestRepositorySessionReconcileStartRunIsReadOnlyBeforePreparation(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	readRun := func() domain.RunState {
		lease, err := fixture.session.runs.AcquireExisting(fixture.request.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state, readErr := runstore.InspectUnderLease(lease)
		if err := errors.Join(readErr, lease.Release()); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readRun()
	got, found, err := fixture.session.ReconcileStartRun(context.Background(), fixture.request)
	if err != nil || found || got != (application.RunStartProjection{}) {
		t.Fatalf("reconcile got=%+v found=%t err=%v", got, found, err)
	}
	after := readRun()
	if after != before || after.State != domain.StateReady {
		t.Fatalf("read-only reconcile mutated Run: before=%+v after=%+v", before, after)
	}
	if _, err := fixture.session.OwnerProjection(context.Background()); err != nil {
		t.Fatalf("read-only reconcile invalidated resident owner: %v", err)
	}
}

func TestFixedDeliveryStrictSuccessorReplaysOldPendingAndClosesReceipt(t *testing.T) {
	fixture := newPublicFixedDeliveryInputs(t)
	session1, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	store1, err := session1.OpenFixedDeliveryStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending, replay, err := store1.BeginStartRun(context.Background(), "successor-replay", fixture.request, time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC))
	if err != nil || replay {
		t.Fatalf("pending=%+v replay=%t err=%v", pending, replay, err)
	}
	root1 := store1.authorityRootDigest
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session1.Close(); err != nil {
		t.Fatal(err)
	}

	session2, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session2.Close() })
	store2, err := session2.OpenFixedDeliveryStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	if session2.acquisition.OwnerEpoch <= session1.acquisition.OwnerEpoch || store2.authorityRootDigest != root1 {
		t.Fatalf("successor/root identity mismatch: oldEpoch=%d newEpoch=%d oldRoot=%s newRoot=%s", session1.acquisition.OwnerEpoch, session2.acquisition.OwnerEpoch, root1, store2.authorityRootDigest)
	}
	replayed, replay, err := store2.BeginStartRun(context.Background(), "successor-replay", fixture.request, time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC))
	if err != nil || !replay || replayed != pending {
		t.Fatalf("successor pending=%+v replay=%t err=%v", replayed, replay, err)
	}
	receipt, applied, err := store2.ReconcileStartRunDelivery(context.Background(), pending, fixture.request, fixedStartRunReconcilerStub{started: fixedDeliveryStarted(fixture.request), found: true})
	if err != nil || !applied || validateFixedDeliveryReceipt(receipt) != nil {
		t.Fatalf("successor receipt=%+v applied=%t err=%v", receipt, applied, err)
	}
}

func TestOpenRepositorySessionRootFailureReleasesOwnerForFreshSuccessor(t *testing.T) {
	for _, mutation := range []string{"replacement", "invalid-mode"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newPublicFixedDeliveryInputs(t)
			old := fixture.repository + "-old"
			switch mutation {
			case "replacement":
				if err := os.Rename(fixture.repository, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.repository, 0o700); err != nil {
					t.Fatal(err)
				}
			case "invalid-mode":
				if err := os.Chmod(fixture.repository, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			if session, err := OpenRepositorySession(context.Background(), fixture.inputs); err == nil {
				_ = session.Close()
				t.Fatal("root drift was accepted after owner claim")
			}
			if err := fixture.inputs.HeldRepositoryRoot.Close(); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "replacement":
				if err := os.Remove(fixture.repository); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(old, fixture.repository); err != nil {
					t.Fatal(err)
				}
			case "invalid-mode":
				if err := os.Chmod(fixture.repository, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := OpenCanonicalRepositoryRoot(fixture.repository)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			fixture.inputs.HeldRepositoryRoot = fresh
			successor, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatalf("fresh successor could not acquire after failed construction: %v", err)
			}
			store, err := successor.OpenFixedDeliveryStore(context.Background())
			if err != nil {
				_ = successor.Close()
				t.Fatalf("fresh successor borrow failed: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := successor.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFixedDeliveryUnknownAndStaleRunLeaveNoPending(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	unknown := fixture.request
	unknown.RunID = "run:unknown"
	if _, _, err := fixture.store.BeginStartRun(context.Background(), "unknown", unknown, fixture.deadline); err == nil {
		t.Fatal("unknown Run accepted")
	}
	stale := fixture.request
	stale.ExpectedAuthorityHead = canonical.DigestBytes([]byte("stale"))
	if _, _, err := fixture.store.BeginStartRun(context.Background(), "stale", stale, fixture.deadline); !errors.Is(err, ErrFixedDeliveryConflict) {
		t.Fatalf("stale err=%v", err)
	}
	if got := pendingFiles(t, fixture.repository); len(got) != 0 {
		t.Fatalf("pending=%v", got)
	}
}

func TestFixedDeliveryEveryRootLayerFailsClosedOnNameAndIdentityDrift(t *testing.T) {
	layers := []string{"repository", ".marshal", "runtime-v1", "control", "delivery-v1"}
	mutations := []string{"rename", "replacement", "symlink", "aba", "mode", "owner", "type"}
	for layerIndex, layer := range layers {
		for _, mutation := range mutations {
			t.Run(layer+"/"+mutation, func(t *testing.T) {
				fixture := newFixedDeliveryFixture(t)
				path := fixedDeliveryRootLayerPath(fixture.repository, layerIndex)
				old := path + "-old"
				switch mutation {
				case "rename":
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(old) })
				case "replacement":
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(old) })
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(old) })
					if err := os.Symlink(old, path); err != nil {
						t.Fatal(err)
					}
				case "aba":
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(old, path); err != nil {
						t.Fatal(err)
					}
				case "mode":
					if err := os.Chmod(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "owner":
					if err := os.Chown(path, -1, 12); err != nil {
						t.Fatal(err)
					}
				case "type":
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(old) })
					if err := os.WriteFile(path, []byte("not-a-directory"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := fixture.store.BeginStartRun(context.Background(), "root-drift", fixture.request, fixture.deadline); !errors.Is(err, ErrFixedDeliveryConflict) {
					t.Fatalf("layer=%s mutation=%s err=%v", layer, mutation, err)
				}
			})
		}
	}
}

func fixedDeliveryRootLayerPath(repository string, index int) string {
	path := repository
	for componentIndex := 0; componentIndex < index; componentIndex++ {
		path = filepath.Join(path, fixedServerRootComponents[componentIndex])
	}
	return path
}

func TestFixedDeliveryPublishPhasesAndResponseLossAdoption(t *testing.T) {
	for _, phase := range []fixedDeliveryPublishPhase{fixedDeliveryPhaseBeforeStageWrite, fixedDeliveryPhaseAfterStageWrite, fixedDeliveryPhaseAfterStageSync, fixedDeliveryPhaseBeforeRename} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newFixedDeliveryFixture(t)
			fixture.store.publishHook = func(got fixedDeliveryPublishPhase) error {
				if got == phase {
					return errors.New("injected")
				}
				return nil
			}
			if _, _, err := fixture.store.BeginStartRun(context.Background(), "phase", fixture.request, fixture.deadline); err == nil {
				t.Fatal("phase failure accepted")
			}
			if got := pendingFiles(t, fixture.repository); len(got) != 0 {
				t.Fatalf("visible pending=%v", got)
			}
		})
	}
	for _, lostPhase := range []fixedDeliveryPublishPhase{fixedDeliveryPhaseAfterRename, fixedDeliveryPhaseAfterParentSync} {
		t.Run(string(lostPhase), func(t *testing.T) {
			fixture := newFixedDeliveryFixture(t)
			fixture.store.publishHook = func(phase fixedDeliveryPublishPhase) error {
				if phase == lostPhase {
					return errors.New("lost response")
				}
				return nil
			}
			if _, _, err := fixture.store.BeginStartRun(context.Background(), "lost", fixture.request, fixture.deadline); !errors.Is(err, ErrFixedDeliveryUnknown) {
				t.Fatalf("response loss err=%v", err)
			}
			if lostPhase == fixedDeliveryPhaseAfterRename {
				fixture.store.publishHook = func(phase fixedDeliveryPublishPhase) error {
					if phase == fixedDeliveryPhaseBeforeParentSync {
						return errors.New("parent sync unavailable")
					}
					return nil
				}
				if _, _, err := fixture.store.BeginStartRun(context.Background(), "lost", fixture.request, fixture.deadline); !errors.Is(err, ErrFixedDeliveryUnknown) {
					t.Fatalf("after-rename adoption skipped failed parent sync: %v", err)
				}
			}
			fixture.store.publishHook = nil
			pending, replay, err := fixture.store.BeginStartRun(context.Background(), "lost", fixture.request, fixture.deadline)
			if err != nil || !replay || validateFixedDeliveryPending(pending) != nil {
				t.Fatalf("adopt pending=%+v replay=%t err=%v", pending, replay, err)
			}
		})
	}
}

func TestFixedDeliveryRenameEEXISTUsesDurableAdoption(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	raw := []byte(`{"record":"exact-existing"}`)
	leaf := "p-" + strings.Repeat("a", 64) + ".json"
	created := false
	hook := func(phase fixedDeliveryPublishPhase) error {
		if phase == fixedDeliveryPhaseBeforeRename && !created {
			created = true
			path := filepath.Join(fixture.repository, ".marshal", "runtime-v1", "control", "delivery-v1", leaf)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				return err
			}
		}
		if phase == fixedDeliveryPhaseBeforeParentSync {
			return errors.New("parent sync unavailable")
		}
		return nil
	}
	if err := publishFixedDeliveryRecord(fixture.session.fixedRoot, leaf, raw, hook); !errors.Is(err, ErrFixedDeliveryUnknown) {
		t.Fatalf("EEXIST adoption skipped failed parent sync: %v", err)
	}
	if err := publishFixedDeliveryRecord(fixture.session.fixedRoot, leaf, raw, nil); err != nil {
		t.Fatalf("durable EEXIST adoption: %v", err)
	}
}

func TestFixedDeliveryRejectsTruncatedHardlinkedAndForgedRecord(t *testing.T) {
	for _, name := range []string{"truncated", "hardlink", "forged", "mode", "symlink"} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixedDeliveryFixture(t)
			pending, _, err := fixture.store.BeginStartRun(context.Background(), "record", fixture.request, fixture.deadline)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.repository, ".marshal", "runtime-v1", "control", "delivery-v1", fixedDeliveryPendingLeaf(pending.RequestKeyDigest))
			switch name {
			case "truncated":
				if err := os.Truncate(path, 1); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			case "forged":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw[len(raw)-2] ^= 1
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".old", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := fixture.store.BeginStartRun(context.Background(), "record", fixture.request, fixture.deadline); !errors.Is(err, ErrFixedDeliveryConflict) {
				t.Fatalf("record err=%v", err)
			}
		})
	}
}

func TestFixedDeliveryRunningSnapshotWithoutPendingIsRejected(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	statePath := filepath.Join(fixture.repository, ".marshal", "runs", fixture.request.RunID, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state domain.RunState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.State = domain.StateRunning
	state.CurrentAttemptID = "attempt:running"
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.BeginStartRun(context.Background(), "running", fixture.request, fixture.deadline); err == nil {
		t.Fatal("RUNNING Run without pending was accepted")
	}
	if got := pendingFiles(t, fixture.repository); len(got) != 0 {
		t.Fatalf("pending=%v", got)
	}
}

func TestRepositorySessionAPIHasNoRawRunStoreEscape(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "repository_session.go", mustReadTestFile(t, "repository_session.go"), 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.FuncDecl)
		if !ok || declaration.Recv == nil || declaration.Type.Results == nil {
			return true
		}
		for _, result := range declaration.Type.Results.List {
			selector, ok := result.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			qualified, ok := selector.X.(*ast.SelectorExpr)
			if ok && qualified.Sel.Name == "Store" {
				t.Errorf("RepositorySession method %s exposes raw *Store", declaration.Name.Name)
			}
		}
		return true
	})
	source := string(mustReadTestFile(t, "fixed_delivery.go"))
	begin := strings.Index(source, "func (store *FixedDeliveryStore) BeginStartRun")
	acquire := strings.Index(source[begin:], "AcquireExisting")
	owner := strings.Index(source[begin:], "withCurrentOwner")
	if begin < 0 || acquire < 0 || owner < 0 || acquire >= owner {
		t.Fatalf("BeginStartRun lock order is not RunLease then current owner")
	}
}

func mustReadTestFile(t *testing.T, name string) []byte {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
