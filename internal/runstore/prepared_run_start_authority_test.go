package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

type preparedRunStartFixture struct {
	store    *Store
	lease    *Lease
	prepared application.PreparedRunStart
	claim    resultingress.CommittedRunStartClaim
}

func newPreparedRunStartFixture(t *testing.T) preparedRunStartFixture {
	t.Helper()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:prepared-start")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	planned := transition("event:prepared-planned", 1, domain.StateCreated, domain.StatePlanned)
	ready := transition("event:prepared-ready", 2, domain.StatePlanned, domain.StateReady)
	planned.RunID = lease.runID
	ready.RunID = lease.runID
	specDigest := canonical.DigestBytes([]byte("prepared-spec"))
	policyDigest := canonical.DigestBytes([]byte("prepared-policy"))
	capabilityDigest := canonical.DigestBytes([]byte("prepared-capability"))
	baseSHA := strings.Repeat("a", 40)
	worktreePath := "/tmp/marshal-prepared-worktree"
	ready.Payload = map[string]any{
		"specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capabilityDigest,
		"baseSha": baseSHA, "worktreePath": worktreePath, "maxAttempts": 3,
	}
	if err := store.Append(lease, planned, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(lease, ready, 1); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:prepared-start", "run:prepared-start", time.Unix(1_700_000_000, 0))
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, ready)
	if err != nil {
		t.Fatal(err)
	}
	state.SpecDigest = specDigest
	state.PolicyDigest = policyDigest
	state.CapabilityDigest = capabilityDigest
	state.BaseSHA = baseSHA
	state.WorktreePath = worktreePath
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	lease.guard.mu.RLock()
	authority, err := OpenRunAuthority(lease)
	if err != nil {
		lease.guard.mu.RUnlock()
		t.Fatal(err)
	}
	records, err := strictRunJournalAt(int(authority.Fd()))
	authority.Close()
	lease.guard.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	preparation := canonical.DigestBytes([]byte("prepared"))
	reservationDigest := canonical.DigestBytes([]byte("reservation"))
	openedDigest := canonical.DigestBytes([]byte("opened"))
	prepared := application.PreparedRunStart{ProtocolRevision: application.PreparedRunStartProtocolRevision, TaskID: state.TaskID, RunID: state.RunID, AttemptID: "attempt:prepared-start", ReservationFactDigest: reservationDigest, AttemptOpenedFactDigest: openedDigest, AttemptOrdinal: 1, AttemptsUsedBefore: 0, MaxAttempts: 3, State: domain.StateReady, Sequence: 2, AuthorityHead: records[1].digest, PreparationDigest: preparation}
	return preparedRunStartFixture{store: store, lease: lease, prepared: prepared, claim: resultingress.CommittedRunStartClaim{TaskID: prepared.TaskID, RunID: prepared.RunID, AttemptID: prepared.AttemptID, ReservationFactDigest: reservationDigest, AttemptOpenedFactDigest: openedDigest, AttemptOrdinal: 1, AttemptsUsedBefore: 0, MaxAttempts: 3, ReadySequence: 2, ReadyAuthorityHead: records[1].digest, PreparationDigest: preparation, ProcessStartedFactDigest: canonical.DigestBytes([]byte("process-started")), ResumeOutcomeFactDigest: canonical.DigestBytes([]byte("resume"))}}
}

func appendPreparedClaim(t *testing.T, fixture preparedRunStartFixture, claim resultingress.CommittedRunStartClaim) application.RunProjection {
	t.Helper()
	fixture.lease.guard.mu.Lock()
	defer fixture.lease.guard.mu.Unlock()
	guard := newBorrowedRunStartProjector(int(fixture.lease.runDir.Fd()), fixture.prepared).guard
	projection, err := appendPreparedRunStartClaim(guard, claim)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestPreparedRunStartCommitsOnceAndReplaysFromRunJournal(t *testing.T) {
	fixture := newPreparedRunStartFixture(t)
	ready, err := fixture.store.ReadRunStartAuthorityUnderLease(context.Background(), fixture.lease)
	if err != nil || ready.Run.State != domain.StateReady || ready.Run.AuthorityHead != fixture.prepared.AuthorityHead || ready.PreparationDigest != "" || ready.SpecDigest == "" || ready.PolicyDigest == "" || ready.CapabilityDigest == "" || ready.BaseSHA == "" || ready.WorktreePath == "" {
		t.Fatalf("READY authority=%+v err=%v", ready, err)
	}
	projection := appendPreparedClaim(t, fixture, fixture.claim)
	if projection.State != domain.StateRunning || projection.Sequence != 3 || projection.AttemptID != fixture.prepared.AttemptID {
		t.Fatalf("projection=%+v", projection)
	}
	before, _, err := fixture.store.ReadEvents(fixture.prepared.RunID)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	replay, err := fixture.store.WithPreparedRunStartAuthority(context.Background(), fixture.lease, fixture.prepared, "", func(resultingress.RunStartProjector) error {
		called = true
		return errors.New("must not be called")
	})
	if err != nil || called || replay != projection {
		t.Fatalf("replay=%+v called=%v err=%v", replay, called, err)
	}
	running, err := fixture.store.ReadRunStartAuthorityUnderLease(context.Background(), fixture.lease)
	if err != nil || running.Run != projection || running.PreparationDigest != fixture.prepared.PreparationDigest || running.SpecDigest != ready.SpecDigest || running.PolicyDigest != ready.PolicyDigest || running.CapabilityDigest != ready.CapabilityDigest || running.BaseSHA != ready.BaseSHA || running.WorktreePath != ready.WorktreePath {
		t.Fatalf("RUNNING authority=%+v err=%v", running, err)
	}
	after, _, err := fixture.store.ReadEvents(fixture.prepared.RunID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("response replay appended: before=%d after=%d err=%v", len(before), len(after), err)
	}
	conflict := fixture.claim
	conflict.ResumeOutcomeFactDigest = canonical.DigestBytes([]byte("different-resume"))
	fixture.lease.guard.mu.Lock()
	guard := newBorrowedRunStartProjector(int(fixture.lease.runDir.Fd()), fixture.prepared).guard
	_, conflictErr := appendPreparedRunStartClaim(guard, conflict)
	fixture.lease.guard.mu.Unlock()
	if !errors.Is(conflictErr, ErrConflict) {
		t.Fatalf("different provenance replay=%v", conflictErr)
	}
}

func TestPreparedRunStartBindsDescriptorLocalDispatchAndRejectsEvidenceDrift(t *testing.T) {
	fixture := newPreparedRunStartFixture(t)
	dispatch := preparedLocalObservation(t, time.Unix(1_800_000_010, 0).UTC())
	attempt, err := OpenOrCreateDirectoryUnderLease(fixture.lease, "attempts", fixture.prepared.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selfidentity.PersistPhaseObservationIn(attempt, "local-self-identity-dispatch.json", dispatch); err != nil {
		attempt.Close()
		t.Fatal(err)
	}
	if err := attempt.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.lease.guard.mu.Lock()
	guard := newBorrowedRunStartProjector(int(fixture.lease.runDir.Fd()), fixture.prepared).guard
	guard.dispatchObservationDigest = dispatch.ObservationDigest
	projection, err := appendPreparedRunStartClaim(guard, fixture.claim)
	fixture.lease.guard.mu.Unlock()
	if err != nil || projection.State != domain.StateRunning {
		t.Fatalf("local Run-start projection=%+v err=%v", projection, err)
	}
	events, _, err := fixture.store.ReadEvents(fixture.prepared.RunID)
	if err != nil || len(events) != 3 || events[2].Payload["dispatchObservationDigest"] != dispatch.ObservationDigest {
		t.Fatalf("local Run-start event=%+v err=%v", events, err)
	}
	called := false
	replay, err := fixture.store.WithPreparedRunStartAuthority(context.Background(), fixture.lease, fixture.prepared, dispatch.ObservationDigest, func(resultingress.RunStartProjector) error {
		called = true
		return errors.New("replay callback must not run")
	})
	if err != nil || called || replay != projection {
		t.Fatalf("bound replay=%+v called=%t err=%v", replay, called, err)
	}
	path := filepath.Join(fixture.store.root, "runs", fixture.prepared.RunID, "attempts", fixture.prepared.AttemptID, "local-self-identity-dispatch.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	called = false
	if _, err := fixture.store.WithPreparedRunStartAuthority(context.Background(), fixture.lease, fixture.prepared, dispatch.ObservationDigest, func(resultingress.RunStartProjector) error {
		called = true
		return nil
	}); !errors.Is(err, ErrConflict) || called {
		t.Fatalf("deleted local dispatch accepted: called=%t err=%v", called, err)
	}
}

func TestRunStartAuthorityRejectsMissingOrDriftedFrozenInputs(t *testing.T) {
	tests := map[string]func(*domain.RunState){
		"missing-spec":       func(state *domain.RunState) { state.SpecDigest = "" },
		"drifted-policy":     func(state *domain.RunState) { state.PolicyDigest = canonical.DigestBytes([]byte("other-policy")) },
		"missing-capability": func(state *domain.RunState) { state.CapabilityDigest = "" },
		"malformed-base":     func(state *domain.RunState) { state.BaseSHA = strings.Repeat("A", 40) },
		"unclean-worktree":   func(state *domain.RunState) { state.WorktreePath += "/.." },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPreparedRunStartFixture(t)
			state, err := InspectUnderLease(fixture.lease)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&state)
			if err := fixture.store.WriteSnapshot(fixture.lease, state); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.ReadRunStartAuthorityUnderLease(context.Background(), fixture.lease); !errors.Is(err, ErrConflict) {
				t.Fatalf("hostile frozen input accepted: %v", err)
			}
		})
	}
}

func TestGenericAppendCannotBypassPreparedRunStart(t *testing.T) {
	fixture := newPreparedRunStartFixture(t)
	for _, eventType := range []string{"run.start-outcome", "worker.started", "legacy.ready-running"} {
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:bypass-" + eventType, RunID: fixture.prepared.RunID, AttemptID: fixture.prepared.AttemptID, Sequence: 3, Type: eventType, StateFrom: domain.StateReady, StateTo: domain.StateRunning, Timestamp: time.Now().UTC(), Payload: map[string]any{"preparationDigest": fixture.prepared.PreparationDigest}}
		if err := fixture.store.Append(fixture.lease, event, 2); !errors.Is(err, ErrConflict) {
			t.Fatalf("type %s bypass err=%v", eventType, err)
		}
	}
}

func TestRunStartOutcomeV1RemainsStrictReplayOnly(t *testing.T) {
	legacy := runStartOutcomePayload{ProtocolRevision: runStartOutcomeProtocolV1, TaskID: "task:legacy-run-start", PreparationDigest: canonical.DigestBytes([]byte("legacy-prepared")), ProcessStartedFactDigest: canonical.DigestBytes([]byte("legacy-started")), ResumeOutcomeFactDigest: canonical.DigestBytes([]byte("legacy-resume"))}
	if err := legacy.validate(); err != nil {
		t.Fatalf("legacy payload no longer replays: %v", err)
	}
	mixed := legacy
	mixed.ReservationFactDigest = canonical.DigestBytes([]byte("forged-reservation"))
	if err := mixed.validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy payload accepted v2 field: %v", err)
	}
	fresh := legacy
	fresh.ProtocolRevision = runStartOutcomeProtocolV2
	if err := fresh.validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh v2 payload omitted reservation/budget: %v", err)
	}
}

func TestPreparedRunStartRejectsZeroProofAndLeavesJournalUntouched(t *testing.T) {
	fixture := newPreparedRunStartFixture(t)
	before, _, err := fixture.store.ReadEvents(fixture.prepared.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.WithPreparedRunStartAuthority(context.Background(), fixture.lease, fixture.prepared, "", func(projector resultingress.RunStartProjector) error {
		bypass := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:nested-bypass", RunID: fixture.prepared.RunID, AttemptID: fixture.prepared.AttemptID, Sequence: 3, Type: "worker.started", StateFrom: domain.StateReady, StateTo: domain.StateRunning, Timestamp: time.Now().UTC(), Payload: map[string]any{}}
		if appendErr := fixture.store.Append(fixture.lease, bypass, 2); !errors.Is(appendErr, ErrConflict) {
			t.Fatalf("nested append did not fail before blocking: %v", appendErr)
		}
		if _, _, readErr := ReadEventsUnderLease(fixture.lease); !errors.Is(readErr, ErrConflict) {
			t.Fatalf("nested read did not fail before blocking: %v", readErr)
		}
		readResult := make(chan error, 1)
		go func() {
			_, readErr := fixture.store.ReadRunStartAuthorityUnderLease(context.Background(), fixture.lease)
			readResult <- readErr
		}()
		select {
		case readErr := <-readResult:
			if !errors.Is(readErr, ErrConflict) {
				t.Fatalf("nested Run-start authority read=%v", readErr)
			}
		case <-time.After(time.Second):
			t.Fatal("nested Run-start authority read blocked on borrowed guard")
		}
		if _, openErr := OpenRunAuthority(fixture.lease); openErr == nil {
			t.Fatal("nested authority reopen succeeded")
		}
		return projector.ProjectCommittedRunStart(context.Background(), resultingress.CommittedRunStartProof{})
	})
	if err == nil {
		t.Fatal("zero proof accepted")
	}
	after, _, readErr := fixture.store.ReadEvents(fixture.prepared.RunID)
	if readErr != nil || !bytes.Equal(mustJSON(t, before), mustJSON(t, after)) {
		t.Fatalf("failed proof changed journal: %v", readErr)
	}
}

func TestPreparedRunStartGuardSerializesReleaseBeforeLeaseFieldRead(t *testing.T) {
	fixture := newPreparedRunStartFixture(t)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		_, err := fixture.store.WithPreparedRunStartAuthority(context.Background(), fixture.lease, fixture.prepared, "", func(resultingress.RunStartProjector) error {
			close(entered)
			<-unblock
			return errors.New("stop before projection")
		})
		completed <- err
	}()
	<-entered
	released := make(chan error, 1)
	go func() { released <- fixture.lease.Release() }()
	select {
	case err := <-released:
		t.Fatalf("Release escaped prepared authority guard: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(unblock)
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("borrower failure disappeared")
		}
	case <-time.After(time.Second):
		t.Fatal("prepared authority did not finish")
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not resume after prepared authority returned")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.JSON(mustMarshal(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func preparedLocalObservation(t *testing.T, observedAt time.Time) selfidentity.LocalSelfIdentityObservationV1 {
	t.Helper()
	observation := selfidentity.LocalSelfIdentityObservationV1{
		SchemaVersion: selfidentity.ObservationSchema, ActivationDigest: canonical.DigestBytes([]byte("activation")),
		ProcessID: 42, ProcessExecutablePath: "/fixed/bin/marshal",
		RepositoryIdentity: canonical.DigestBytes([]byte("repository")), CanonicalRepositoryRoot: "/fixed/repository",
		CurrentPathObject: selfidentity.CurrentPathObjectV1{
			CanonicalPath: "/fixed/bin/marshal", Device: "1", Inode: "2", Size: 3,
			RawSHA256: canonical.DigestBytes([]byte("marshal")), PathRechecked: true, ObservationKind: "darwin-current-path-fd-object",
		},
		SourceHead: strings.Repeat("a", 40), SelfProfile: selfidentity.LocalProfile,
		ObservedAt: observedAt.Format(time.RFC3339), Status: "pass", ReasonCode: selfidentity.ReasonObserved,
	}
	observation.IdentitySubjectDigest, _ = canonical.DigestJSON(mustJSON(t, map[string]any{
		"activationDigest": observation.ActivationDigest, "repositoryIdentity": observation.RepositoryIdentity,
		"canonicalRepositoryRoot": observation.CanonicalRepositoryRoot, "canonicalExecutablePath": observation.CurrentPathObject.CanonicalPath,
		"device": observation.CurrentPathObject.Device, "inode": observation.CurrentPathObject.Inode, "size": observation.CurrentPathObject.Size,
		"rawSHA256": observation.CurrentPathObject.RawSHA256, "sourceHead": observation.SourceHead, "selfProfile": observation.SelfProfile,
	}))
	observation.ObservationDigest, _ = canonical.DigestJSON(mustJSON(t, map[string]any{
		"schemaVersion": observation.SchemaVersion, "activationDigest": observation.ActivationDigest,
		"processId": observation.ProcessID, "processExecutablePath": observation.ProcessExecutablePath,
		"repositoryIdentity": observation.RepositoryIdentity, "canonicalRepositoryRoot": observation.CanonicalRepositoryRoot,
		"currentPathObject": observation.CurrentPathObject, "sourceHead": observation.SourceHead, "selfProfile": observation.SelfProfile,
		"observedAt": observation.ObservedAt, "status": observation.Status, "reasonCode": observation.ReasonCode,
		"identitySubjectDigest": observation.IdentitySubjectDigest,
	}))
	if err := selfidentity.ValidateObservation(observation); err != nil {
		t.Fatal(err)
	}
	return observation
}
