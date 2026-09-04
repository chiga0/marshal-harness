package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
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
	opened := appendFreshReservedAttempt(t, store, id)
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, opened, owner, verifier)
	provisioned := appendTestAcceptedProvision(t, store, bound)
	closure := preparedTestPiClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "prepared-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, PreparedExecutionCreation{Identity: launch.State.Identity, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: id.RunAuthorityDigest})
	if err != nil {
		t.Fatal(err)
	}
	return preparedExecutionFixture{store: store, owner: owner, verifier: verifier, prepared: prepared}
}

// preparedTestPiClosure is a synthetic but structurally exact Pi 0.84.4
// closure. PreparedExecution is Pi-specific, so a native/v1 fixture would
// compile while making every runtime creation test fail before exercising the
// authority contract.
func preparedTestPiClosure(t *testing.T) launchidentity.ClosureV1 {
	t.Helper()
	object := func(path string, inode uint64, size int64, executable bool) launchidentity.ObjectV1 {
		mode := uint32(POSIXFileTypeRegular | 0o644)
		if executable {
			mode = POSIXFileTypeRegular | 0o755
		}
		return launchidentity.ObjectV1{CanonicalPath: path, Device: 1, Inode: inode, FileType: POSIXFileTypeRegular, Mode: mode, UID: 501, GID: 20, Size: size, LinkCount: 1, RawSHA256: attemptTestDigest(path)}
	}
	root := func(name, path, relative string, inode uint64) launchidentity.MaterialRootV1 {
		return launchidentity.MaterialRootV1{Name: name, CanonicalPath: path, PackageRelative: relative, Object: launchidentity.ObjectV1{CanonicalPath: path, Device: 1, Inode: inode, FileType: POSIXFileTypeDirectory, Mode: POSIXFileTypeDirectory | 0o755, UID: 501, GID: 20, LinkCount: 2}}
	}
	roots := []launchidentity.MaterialRootV1{
		root("photon-node", "/fixed/pi/photon", "node_modules/@silvia-odwyer/photon-node", 10),
		root("pi-bundle", "/fixed/pi/bundle", "dist/bundle", 11),
	}
	materials := make([]launchidentity.LaunchMaterialV1, 0, 55)
	for index := 0; index < 7; index++ {
		size := int64(1)
		if index == 6 {
			size = 2_265_681
		}
		role := fmt.Sprintf("photon-node/file-%02d", index)
		materials = append(materials, launchidentity.LaunchMaterialV1{Role: role, Object: object("/fixed/pi/photon/"+role[len("photon-node/"):], uint64(100+index), size, false)})
	}
	entrypoint := object("/fixed/pi/bundle/cli.js", 200, 629, false)
	entrypoint.RawSHA256 = "sha256:5406c369954516fb56879d685e082ff9095cd6e06e41af406f394942377fd4bf"
	materials = append(materials, launchidentity.LaunchMaterialV1{Role: "pi-bundle/cli.js", Object: entrypoint})
	for index := 0; index < 47; index++ {
		size := int64(1)
		if index == 46 {
			size = 7_439_133
		}
		role := fmt.Sprintf("pi-bundle/file-%02d", index)
		materials = append(materials, launchidentity.LaunchMaterialV1{Role: role, Object: object("/fixed/pi/bundle/"+role[len("pi-bundle/"):], uint64(201+index), size, false)})
	}
	closure, err := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: object("/fixed/node", 2, 99, true), ClosureProfileID: launchidentity.Pi0844DarwinARM64Profile,
		MaterialRoots: roots, LaunchMaterials: materials, Arguments: []string{"/fixed/node", entrypoint.CanonicalPath}, Environment: []string{}, WorkingDirectory: "/tmp/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	return closure
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
	// The sealed PreparationDigest covers the Pi identity: any caller-chosen
	// identity breaks the seal and is rejected at the closed wire form.
	tampered := fixture.prepared
	tampered.Pi0844IdentityDigest = attemptTestDigest("caller-chosen-pi-identity")
	tamperedRaw, _ := json.Marshal(tampered)
	tamperedRaw, _ = canonical.JSON(tamperedRaw)
	if _, err := DecodePreparedExecution(tamperedRaw); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("caller-chosen Pi identity accepted: %v", err)
	}
	// DecodePreparedExecution is a pure closed-form check. A document sealed by
	// the same digest function stays structurally valid; it cannot enter
	// authority because ResultIngress derives the Pi identity exclusively from
	// its own ledger closure and every consumer resolves the preparation by
	// digest against the current ledger.
	resealed := tampered
	resealed.PreparationDigest, _ = preparedExecutionDigest(resealed)
	resealedRaw, _ := json.Marshal(resealed)
	resealedRaw, _ = canonical.JSON(resealedRaw)
	decoded, err := DecodePreparedExecution(resealedRaw)
	if err != nil || decoded.Pi0844IdentityDigest != resealed.Pi0844IdentityDigest {
		t.Fatalf("self-consistent closed wire form rejected: %+v err=%v", decoded, err)
	}
	if _, err := fixture.store.ResolvePreparedExecution(context.Background(), fixture.verifier, fixture.owner.Acquisition, resealed.PreparationDigest); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("ungrounded preparation resolved from ledger: %v", err)
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

func TestResolvePreparedRunStartUsesExactReadyHeadWithoutMutation(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	key := PreparedRunStartKey{
		RunID: fixture.prepared.AttemptIdentity.RunID, ReadySequence: fixture.prepared.ExpectedRunSequence,
		ReadyAuthorityHead: fixture.prepared.ExpectedRunAuthorityHead,
	}
	resolved, err := fixture.store.ResolvePreparedRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, key)
	if err != nil || resolved != fixture.prepared {
		t.Fatalf("resolve by READY head=%+v err=%v", resolved, err)
	}
	after, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("READY-head lookup mutated the authority ledger")
	}
	key.ReadyAuthorityHead = attemptTestDigest("other-ready-head")
	if _, err := fixture.store.ResolvePreparedRunStart(context.Background(), fixture.verifier, fixture.owner.Acquisition, key); !errors.Is(err, ErrPreparedRunStartNotFound) {
		t.Fatalf("wrong READY head resolved: %v", err)
	}
}

func TestCommittedRunStartProofIsNarrowSharedAndSynchronous(t *testing.T) {
	typeOfClaim := reflect.TypeOf(CommittedRunStartClaim{})
	want := []string{"TaskID", "RunID", "AttemptID", "ReservationFactDigest", "AttemptOpenedFactDigest", "AttemptOrdinal", "AttemptsUsedBefore", "MaxAttempts", "ReadySequence", "ReadyAuthorityHead", "PreparationDigest", "ProcessStartedFactDigest", "ResumeOutcomeFactDigest"}
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
	// The in-flight callback must still be inside WithClaim when
	// deactivateAndWait reads inFlight, otherwise the escape is not observable.
	// active flips false only after that read, so it is the deterministic entry
	// barrier; the callback stays blocked until release below.
	for {
		async.guard.mu.Lock()
		observed := !async.guard.active
		async.guard.mu.Unlock()
		if observed {
			break
		}
		runtime.Gosched()
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-deactivated; !errors.Is(err, ErrCommittedRunStartProof) {
		t.Fatalf("escaped callback accepted: %v", err)
	}
}

func TestLegacyPreparedExecutionReplaysWithoutEnteringFreshAuthority(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	current := fixture.prepared
	legacy := legacyPreparedExecutionV1{
		SchemaVersion: legacyPreparedExecutionSchema, ProtocolRevision: legacyPreparedExecutionProtocol,
		AttemptIdentity: current.AttemptIdentity, RunAuthorityBinding: current.RunAuthorityBinding,
		ExpectedRunSequence: current.ExpectedRunSequence, ExpectedRunAuthorityHead: current.ExpectedRunAuthorityHead,
		CurrentOwnerBinding: current.CurrentOwnerBinding, ControlOwnerBoundFactDigest: current.ControlOwnerBoundFactDigest,
		AttemptAuthorityHeadAtPreparation:    current.AttemptAuthorityHeadAtPreparation,
		AllocationProvisionReceiptFactDigest: current.AllocationProvisionReceiptFactDigest,
		AllocationProvisionReceiptDigest:     current.AllocationProvisionReceiptDigest,
		LaunchAuthorizationID:                current.LaunchAuthorizationID, LaunchAuthorizedFactDigest: current.LaunchAuthorizedFactDigest,
		StoredClosureDigest: current.StoredClosureDigest, LaunchMaterialsDigest: current.LaunchMaterialsDigest,
		AgentLaunchSpecDigest: current.AgentLaunchSpecDigest, Pi0844IdentityDigest: current.Pi0844IdentityDigest,
	}
	legacy.PreparationDigest, _ = canonicalDigest(legacy)
	if legacy.validate() != nil {
		t.Fatal("legacy fixture is invalid")
	}
	fact := legacyPreparedExecutionFact{ProtocolRevision: legacyPreparedAuthorityProtocol, FactType: preparedExecutionCreatedFactType, Sequence: 1, Prepared: legacy}
	fact.Digest, _ = canonicalDigest(fact)
	raw, _ := json.Marshal(fact)
	raw, _ = canonical.JSON(raw)
	projection := newAuthorityProjection()
	if err := applyPreparedExecutionLine(raw, projection, 1); err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
	key, _ := legacy.AttemptIdentity.Key()
	if projection.legacyPreparedExecutionKeys[key] != legacy.PreparationDigest || len(projection.preparedExecutions) != 0 || len(projection.preparedExecutionKeys) != 0 {
		t.Fatalf("legacy record entered fresh authority: %+v", projection)
	}
	mixed := current
	mixed.SchemaVersion = legacyPreparedExecutionSchema
	mixed.ProtocolRevision = legacyPreparedExecutionProtocol
	raw, _ = json.Marshal(mixed)
	raw, _ = canonical.JSON(raw)
	if _, err := DecodePreparedExecution(raw); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("legacy/v2 mixed PreparedExecution decoded as fresh: %v", err)
	}
}

func TestFreshPreparedExecutionRejectsLegacyMixedHistoryWithoutWrite(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened := appendFreshReservedAttempt(t, store, id)
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, opened, owner, verifier)
	provisioned := appendTestAcceptedProvision(t, store, bound)
	closure := preparedTestPiClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "legacy-mixed-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatal(err)
	}
	creation := PreparedExecutionCreation{Identity: launch.State.Identity, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: id.RunAuthorityDigest}

	projection := newAuthorityProjection()
	err = store.transact(projection, func() error {
		key, keyErr := id.Key()
		state, found := projection.attempts[key]
		if keyErr != nil || !found {
			return ErrPreparedExecutionConflict
		}
		fresh, deriveErr := derivePreparedExecution(projection, state, creation)
		if deriveErr != nil {
			return deriveErr
		}
		legacy := legacyPreparedExecutionV1{
			SchemaVersion: legacyPreparedExecutionSchema, ProtocolRevision: legacyPreparedExecutionProtocol,
			AttemptIdentity: fresh.AttemptIdentity, RunAuthorityBinding: fresh.RunAuthorityBinding,
			ExpectedRunSequence: fresh.ExpectedRunSequence, ExpectedRunAuthorityHead: fresh.ExpectedRunAuthorityHead,
			CurrentOwnerBinding: fresh.CurrentOwnerBinding, ControlOwnerBoundFactDigest: fresh.ControlOwnerBoundFactDigest,
			AttemptAuthorityHeadAtPreparation:    fresh.AttemptAuthorityHeadAtPreparation,
			AllocationProvisionReceiptFactDigest: fresh.AllocationProvisionReceiptFactDigest,
			AllocationProvisionReceiptDigest:     fresh.AllocationProvisionReceiptDigest,
			LaunchAuthorizationID:                fresh.LaunchAuthorizationID, LaunchAuthorizedFactDigest: fresh.LaunchAuthorizedFactDigest,
			StoredClosureDigest: fresh.StoredClosureDigest, LaunchMaterialsDigest: fresh.LaunchMaterialsDigest,
			AgentLaunchSpecDigest: fresh.AgentLaunchSpecDigest, Pi0844IdentityDigest: fresh.Pi0844IdentityDigest,
		}
		legacy.PreparationDigest, deriveErr = canonicalDigest(legacy)
		if deriveErr != nil || legacy.validate() != nil {
			return ErrPreparedExecutionConflict
		}
		fact := &legacyPreparedExecutionFact{ProtocolRevision: legacyPreparedAuthorityProtocol, FactType: preparedExecutionCreatedFactType, Sequence: store.nextSequence, Prepared: legacy}
		fact.Digest, deriveErr = canonicalDigest(fact)
		if deriveErr != nil {
			return deriveErr
		}
		raw, marshalErr := json.Marshal(fact)
		if marshalErr != nil {
			return marshalErr
		}
		raw, marshalErr = canonical.JSON(raw)
		if marshalErr != nil {
			return marshalErr
		}
		if replayErr := applyPreparedExecutionLine(raw, projection, store.nextSequence); replayErr != nil {
			return replayErr
		}
		fact.Digest = ""
		if appendErr := store.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); appendErr != nil {
			return appendErr
		}
		store.nextSequence++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, creation); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("fresh v2 creation accepted replay-only v1 history: %v", err)
	}
	after, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("mixed-history rejection changed durable ledger bytes")
	}
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatalf("cold replay rejected valid legacy history: %v", err)
	}
	if _, err := reopened.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, creation); !errors.Is(err, ErrPreparedExecutionConflict) {
		t.Fatalf("cold replay admitted fresh v2 over legacy history: %v", err)
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

// advancePreparedAttemptToStarted drives the supervisor bootstrap, start and
// command checkpoint chain for the prepared execution's attempt. The observed
// child process is derived from the prepared Pi closure's runtime executable,
// because AppendProcessStarted binds the process observation to that exact
// runtime object.
func advancePreparedAttemptToStarted(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/prepared-supervisor", Device: 31, Inode: 41, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, acquisition := testPreparedSupervisor(t, fixture.store, state, "prepared-session", control)
	state = testStartPreparedSupervisor(t, fixture.store, prepared, acquisition)
	runtimeExecutable := state.LaunchClosure.RuntimeExecutable
	process, err := SealProcessObservation(ProcessObservation{
		PID: 4321, PGID: 4321, BirthSeconds: 100, BirthMicroseconds: 33,
		WorkingDirectory: state.LaunchClosure.WorkingDirectory, WorkingDirectoryDevice: 1, WorkingDirectoryInode: 2,
		WorkingDirectoryType: POSIXFileTypeDirectory, WorkingDirectoryOwner: 501, WorkingDirectoryMode: POSIXFileTypeDirectory | 0755,
		ExecutablePath: runtimeExecutable.CanonicalPath, ExecutableDevice: runtimeExecutable.Device, ExecutableInode: runtimeExecutable.Inode,
		ExecutableSize: runtimeExecutable.Size, ExecutableType: runtimeExecutable.FileType, ExecutableOwner: runtimeExecutable.UID,
		ExecutableGroup: runtimeExecutable.GID, ExecutableMode: runtimeExecutable.Mode, ExecutableLinkCount: runtimeExecutable.LinkCount,
		ExecutableSHA256: runtimeExecutable.RawSHA256, ObserverIdentity: "core-darwin-observer/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: state.Identity, CommandID: "prepared-child", ObservedAt: "2026-08-29T00:00:02Z", Process: process, LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest}
	state = appendTestProcessStartedCheckpoints(t, fixture.store, state, &transition)
	run := attemptTestRunAuthority(state.Identity)
	result, err := fixture.store.AppendProcessStarted(context.Background(), fixture.verifier, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, state.Owner, transition)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}
