package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type preparedExecutionFixture struct {
	store    *DurableStore
	owner    ControlOwnerState
	verifier attemptOwnerVerifier
	prepared PreparedExecutionV1
}

func newPreparedExecutionFixture(t *testing.T) preparedExecutionFixture {
	t.Helper()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened := supervisorTestOpened(t, store, id)
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, opened, owner, verifier)
	provisioned := appendTestAcceptedProvision(t, store, bound)
	closure := attemptTestClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "prepared-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, PreparedExecutionCreation{Identity: launch.State.Identity, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: attemptTestDigest("ready-head")})
	if err != nil {
		t.Fatal(err)
	}
	return preparedExecutionFixture{store: store, owner: owner, verifier: verifier, prepared: prepared}
}

func TestPreparedExecutionCreationOnceResolveAndSecretBoundary(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	creation := PreparedExecutionCreation{Identity: fixture.prepared.AttemptIdentity, ExpectedRunSequence: fixture.prepared.ExpectedRunSequence, ExpectedRunAuthorityHead: fixture.prepared.ExpectedRunAuthorityHead}
	replay, err := fixture.store.CreatePreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, creation)
	if err != nil || replay != fixture.prepared {
		t.Fatalf("creation replay=%+v err=%v", replay, err)
	}
	after, _ := os.ReadFile(fixture.store.ledgerPath())
	if !bytes.Equal(before, after) {
		t.Fatal("creation response replay appended a second fact")
	}
	resolved, err := fixture.store.ResolvePreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, fixture.prepared.PreparationDigest)
	if err != nil || resolved != fixture.prepared {
		t.Fatalf("resolve=%+v err=%v", resolved, err)
	}
	for _, secret := range []string{"/fixed/marshal", "/tmp/work"} {
		if bytes.Contains(after, []byte(`"factType":"`+preparedExecutionCreatedFactType+`"`)) && bytes.Contains(bytes.Split(after, []byte(`"factType":"`+preparedExecutionCreatedFactType+`"`))[1], []byte(secret)) {
			t.Fatalf("prepared fact leaked %q", secret)
		}
	}
	raw, _ := json.Marshal(fixture.prepared)
	raw, _ = canonical.JSON(raw)
	if decoded, err := DecodePreparedExecution(raw); err != nil || decoded != fixture.prepared {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	if _, err := DecodePreparedExecution(append([]byte(" "), raw...)); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("non-canonical document accepted: %v", err)
	}
	tampered := fixture.prepared
	tampered.Pi0843IdentityDigest = attemptTestDigest("caller-chosen-pi-identity")
	tampered.PreparationDigest, _ = preparedExecutionDigest(tampered)
	tamperedRaw, _ := json.Marshal(tampered)
	tamperedRaw, _ = canonical.JSON(tamperedRaw)
	if _, err := DecodePreparedExecution(tamperedRaw); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("caller-chosen Pi identity accepted: %v", err)
	}
	zeroSequence := creation
	zeroSequence.ExpectedRunSequence = 0
	if _, err := fixture.store.CreatePreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, zeroSequence); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("zero Run sequence accepted: %v", err)
	}
	creation.ExpectedRunSequence++
	if _, err := fixture.store.CreatePreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, creation); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("different second creation accepted: %v", err)
	}
}

func TestCommittedRunStartProofIsNarrowSharedAndSynchronous(t *testing.T) {
	typeOfClaim := reflect.TypeOf(CommittedRunStartClaim{})
	want := []string{"TaskID", "RunID", "AttemptID", "PreparationDigest", "ProcessStartedFactDigest", "ResumeOutcomeFactDigest"}
	if typeOfClaim.NumField() != len(want) {
		t.Fatalf("claim has %d fields", typeOfClaim.NumField())
	}
	for index, name := range want {
		if typeOfClaim.Field(index).Name != name {
			t.Fatalf("claim field %d=%s", index, typeOfClaim.Field(index).Name)
		}
	}
	claim := CommittedRunStartClaim{TaskID: "task", RunID: "run", AttemptID: "attempt", PreparationDigest: attemptTestDigest("prepared"), ProcessStartedFactDigest: attemptTestDigest("started"), ResumeOutcomeFactDigest: attemptTestDigest("resume")}
	proof := newCommittedRunStartProof(claim)
	copyOfProof := proof
	if err := copyOfProof.WithClaim(func(got CommittedRunStartClaim) error {
		if got != claim {
			t.Fatalf("claim=%+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := proof.WithClaim(func(CommittedRunStartClaim) error { return nil }); !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("copy enabled double use: %v", err)
	}
	if err := proof.guard.deactivateAndWait(); !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("double use did not poison proof: %v", err)
	}
	if err := (CommittedRunStartProof{}).WithClaim(func(CommittedRunStartClaim) error { return nil }); !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("zero proof accepted: %v", err)
	}
	unused := newCommittedRunStartProof(claim)
	if err := unused.guard.deactivateAndWait(); !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("unused proof accepted: %v", err)
	}
	async := newCommittedRunStartProof(claim)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- async.WithClaim(func(CommittedRunStartClaim) error { close(entered); <-release; return nil })
	}()
	<-entered
	deactivated := make(chan error, 1)
	go func() { deactivated <- async.guard.deactivateAndWait() }()
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-deactivated; !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("escaped callback accepted: %v", err)
	}
}

type claimCaptureProjector struct {
	called int
	claim  CommittedRunStartClaim
}

func (projector *claimCaptureProjector) ProjectCommittedRunStart(_ context.Context, proof CommittedRunStartProof) error {
	projector.called++
	return proof.WithClaim(func(claim CommittedRunStartClaim) error { projector.claim = claim; return nil })
}

func TestGenericStoreCannotAuthorizePreparedExecutionEvenWithResume(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	state := fixture.storeStateAfterPrepared(t, fixture)
	projector := &claimCaptureProjector{}
	if err := fixture.store.StartPreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, state.Identity, fixture.prepared.PreparationDigest, projector); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("start without process/resume=%v", err)
	}
	state = advancePreparedAttemptToStarted(t, fixture, state)
	if err := fixture.store.StartPreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, state.Identity, fixture.prepared.PreparationDigest, projector); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("start without resume=%v", err)
	}
	resume := testSupervisorIntent(state, processsupervisor.CommandResume, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest})
	started := state.ProcessStartedEvidence.Outcome
	report := processsupervisor.ProcessReport{State: "running", ObserverIdentity: started.ObserverIdentity, ObservedAt: "2026-08-29T00:00:03Z", Process: started.Process, RuntimeObjectDigest: started.RuntimeObjectDigest, WorkingObjectDigest: started.WorkingObjectDigest}
	state = appendVerifiedSupervisorCheckpoint(t, fixture.store, state, resume, verifiedSupervisorOutcome(t, resume, "process-resumed", report))
	if err := fixture.store.StartPreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, state.Identity, fixture.prepared.PreparationDigest, projector); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("generic store authorized exact resume: %v", err)
	}
	if projector.called != 0 {
		t.Fatalf("generic store called projector: %+v", projector)
	}
}

func (fixture preparedExecutionFixture) storeStateAfterPrepared(t *testing.T, _ preparedExecutionFixture) AttemptAuthorityState {
	t.Helper()
	state, found, err := fixture.store.AttemptState(fixture.prepared.AttemptIdentity)
	if err != nil || !found {
		t.Fatalf("state found=%v err=%v", found, err)
	}
	return state
}

func advancePreparedAttemptToStarted(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/prepared-supervisor", Device: 31, Inode: 41, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, acquisition := testPreparedSupervisor(t, fixture.store, state, "prepared-session", control)
	state = testStartPreparedSupervisor(t, fixture.store, prepared, acquisition)
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: state.Identity, CommandID: "prepared-child", ObservedAt: "2026-08-29T00:00:02Z", Process: attemptTestProcess(t), LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest}
	state = appendTestProcessStartedCheckpoints(t, fixture.store, state, &transition)
	run := attemptTestRunAuthority(state.Identity)
	result, err := fixture.store.AppendProcessStarted(context.Background(), fixture.verifier, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, state.Owner, transition)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}
