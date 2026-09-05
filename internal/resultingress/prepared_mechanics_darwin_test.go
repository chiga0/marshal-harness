//go:build darwin && arm64

package resultingress

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func TestPreparedAttemptTransitionAppendsV2BootstrapThroughColdProjector(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	state := fixture.storeStateAfterPrepared(t, fixture)
	if state.ProtocolRevision != attemptAuthorityProtocolV2 || state.OpenedSchemaRevision != attemptOpenedSchemaV2 {
		t.Fatalf("prepared attempt is not fresh v2: protocol=%q opened=%q", state.ProtocolRevision, state.OpenedSchemaRevision)
	}
	prepared := preparedBootstrapForState(t, fixture, state, "prepared-v2-bootstrap")
	transition := AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: state.Identity, SupervisorBootstrap: prepared}
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	projection := newAuthorityProjection()
	var next AttemptAuthorityState
	var digest string
	err = fixture.store.transact(projection, func() error {
		var appendErr error
		next, digest, appendErr = fixture.store.appendPreparedAttemptTransitionLocked(projection, state, transition)
		return appendErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.ProtocolRevision != attemptAuthorityProtocolV2 || next.OpenedSchemaRevision != attemptOpenedSchemaV2 || next.SupervisorBootstrapDigest != digest || digest == "" {
		t.Fatalf("v2 bootstrap projection=%+v digest=%q", next, digest)
	}
	after, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) || !bytes.Equal(after[:len(before)], before) {
		t.Fatal("v2 bootstrap did not append one durable extension")
	}
	reopened, err := OpenResultIngressStore(fixture.store.dir)
	if err != nil {
		t.Fatalf("cold replay rejected prepared bootstrap bytes: %v", err)
	}
	replayed, found, err := reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, next) {
		t.Fatalf("cold replay state=%+v found=%v err=%v", replayed, found, err)
	}
}

func TestPreparedAttemptTransitionRejectsProjectorMismatchBeforeWrite(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	state := fixture.storeStateAfterPrepared(t, fixture)
	prepared := preparedBootstrapForState(t, fixture, state, "prepared-projector-reject")
	prepared.Request.CurrentAuthorityHead = attemptTestDigest("forged-bootstrap-head")
	var err error
	prepared.BootstrapRequestDigest, err = canonicalDigest(prepared.Request)
	if err != nil || prepared.Validate() != nil {
		t.Fatalf("self-consistent hostile bootstrap is not structurally valid: %v", err)
	}
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	projection := newAuthorityProjection()
	err = fixture.store.transact(projection, func() error {
		_, _, appendErr := fixture.store.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: state.Identity, SupervisorBootstrap: prepared})
		return appendErr
	})
	if err == nil {
		t.Fatal("projector mismatch was appended")
	}
	after, readErr := os.ReadFile(fixture.store.ledgerPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed prepared transition changed durable ledger bytes")
	}
}

func preparedBootstrapForState(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState, sessionID string) SupervisorBootstrapPrepared {
	t.Helper()
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/" + sessionID, Device: 301, Inode: 401, FileType: "directory", UID: fixture.owner.Acquisition.OwnerUID, GID: fixture.owner.Acquisition.OwnerGID, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	request := processsupervisor.BootstrapRequest{
		SchemaVersion: processsupervisor.BootstrapSchema, ProtocolRevision: processsupervisor.ProtocolRevision,
		SessionID: sessionID, SessionNonce: strings.Repeat("7", 64), OwnerEpoch: state.Owner.OwnerEpoch,
		Authority: supervisorAuthorityTuple(state.Identity), LaunchAuthorizedFact: state.LaunchAuthorizedDigest,
		CurrentAuthorityHead: state.HeadDigest, ControlDirectoryIdentity: control,
		Core: processsupervisor.CoreIdentity{UID: fixture.owner.Acquisition.OwnerUID, GID: fixture.owner.Acquisition.OwnerGID, Process: fixture.owner.Acquisition.OwnerProcess, Binary: fixture.owner.Acquisition.OwnerBinary},
	}
	prepared, err := NewSupervisorBootstrapPrepared(state.Owner, request)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestLauncherV2BootstrapUsesExistingDurableAdmissionAndColdReplay(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	state := fixture.storeStateAfterPrepared(t, fixture)
	_, request := testBootstrapV2Input()
	request.SessionID = "launcher-v2-durable-bootstrap"
	request.OwnerEpoch, request.Authority = state.Owner.OwnerEpoch, supervisorAuthorityTuple(state.Identity)
	request.CurrentAuthorityHead, request.LaunchAuthorizedFact = state.HeadDigest, state.LaunchAuthorizedDigest
	request.Core = processsupervisor.CoreIdentity{UID: fixture.owner.Acquisition.OwnerUID, GID: fixture.owner.Acquisition.OwnerGID, Process: fixture.owner.Acquisition.OwnerProcess, Binary: fixture.owner.Acquisition.OwnerBinary}
	request.ControlDirectoryIdentity.UID, request.ControlDirectoryIdentity.GID = request.Core.UID, request.Core.GID
	prepared, err := NewSupervisorBootstrapPreparedV2(state.Owner, request)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	// Even an internally consistent, rehashed v2 request cannot substitute a
	// different current head. Rejection must happen before the durable write.
	forged := prepared
	forged.Request.CurrentAuthorityHead = attemptTestDigest("forged-current-head")
	forged.BootstrapRequestDigest, err = canonicalDigest(forged.Request)
	if err != nil || forged.Validate() != nil {
		t.Fatal("malformed negative fixture")
	}
	projection := newAuthorityProjection()
	err = fixture.store.transact(projection, func() error {
		_, _, err := fixture.store.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: state.Identity, SupervisorBootstrap: forged})
		return err
	})
	if err == nil {
		t.Fatal("forged current authority admitted")
	}
	after, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected input changed ledger")
	}
	var next AttemptAuthorityState
	err = fixture.store.transact(projection, func() error {
		var err error
		next, _, err = fixture.store.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: state.Identity, SupervisorBootstrap: prepared})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(fixture.store.ledgerPath())
	if err != nil || len(after) <= len(before) || !bytes.Equal(before, after[:len(before)]) || bytes.Contains(after, []byte(request.SessionNonce)) {
		t.Fatal("append-only/secret boundary")
	}
	reopened, err := OpenResultIngressStore(fixture.store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, found, err := reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, next) || replayed.SupervisorBootstrap.Request.Generation != processsupervisor.DormantV2ProtocolContract() {
		t.Fatalf("cold v2 projection: %v", err)
	}
	started := testInitialStartedV2(t, next.SupervisorBootstrap, next.SupervisorBootstrapDigest)
	preStarted := next
	beforeStarted, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProcessSupervisorStarted){
		func(s *ProcessSupervisorStarted) {
			s.V2.Handshake.SupervisorProcess = next.SupervisorBootstrap.Request.Core.Process
		},
		func(s *ProcessSupervisorStarted) {
			s.BootstrapPreparedFactDigest = attemptTestDigest("unrelated-bootstrap-fact")
		},
	} {
		forged := started
		mutate(&forged)
		if forged.Validate() != nil {
			t.Fatal("started negative fixture not self-consistent")
		}
		err := fixture.store.transact(projection, func() error {
			_, _, err := fixture.store.appendPreparedAttemptTransitionLocked(projection, preStarted, AttemptTransition{Kind: AttemptTransitionProcessSupervisorStarted, Identity: preStarted.Identity, SupervisorStarted: forged})
			return err
		})
		if err == nil {
			t.Fatal("self-consistent forgery bypassed current ledger")
		}
		after, err := os.ReadFile(fixture.store.ledgerPath())
		if err != nil || !bytes.Equal(beforeStarted, after) {
			t.Fatal("rejected started modified ledger")
		}
	}
	err = fixture.store.transact(projection, func() error {
		var err error
		next, _, err = fixture.store.appendPreparedAttemptTransitionLocked(projection, preStarted, AttemptTransition{Kind: AttemptTransitionProcessSupervisorStarted, Identity: preStarted.Identity, SupervisorStarted: started})
		return err
	})
	if err != nil {
		t.Fatalf("durable v2 started: %v", err)
	}
	replayed, found, err = reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, next) || replayed.SupervisorMechanicsAnchor != projectSupervisorMechanicsAnchorV2(started.V2.Anchor) || replayed.SupervisorMechanicsAnchor.Validate() != nil {
		t.Fatalf("cold started/anchor: %v", err)
	}
	intent, preparedCommand, payload := testBindIntentV2(t, started.V2.Anchor, next.SupervisorStartedDigest)
	preIntent := next
	beforeIntent, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	// A freshly created and internally valid command still cannot cite an
	// unrelated business started fact. The current ledger decides admission.
	forgedIntent, _, _ := testBindIntentV2(t, started.V2.Anchor, attemptTestDigest("other-started"))
	err = fixture.store.transact(projection, func() error {
		_, _, err := fixture.store.appendPreparedSupervisorIntentLocked(projection, preIntent, forgedIntent)
		return err
	})
	if err == nil {
		t.Fatal("unrelated started fact admitted as bind intent")
	}
	after, err = os.ReadFile(fixture.store.ledgerPath())
	if err != nil || !bytes.Equal(beforeIntent, after) {
		t.Fatal("rejected v2 command modified ledger")
	}
	err = fixture.store.transact(projection, func() error {
		var err error
		next, _, err = fixture.store.appendPreparedSupervisorIntentLocked(projection, preIntent, intent)
		return err
	})
	if err != nil {
		t.Fatalf("durable v2 intent: %v", err)
	}
	replayed, found, err = reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, next) || replayed.SupervisorPendingIntent != intent || replayed.SupervisorPendingIntentDigest == "" {
		t.Fatalf("cold v2 pending intent: %v", err)
	}
	evidence, err := SupervisorPreparedCommandEvidenceV2(replayed.SupervisorPendingIntent)
	if err != nil || evidence != preparedCommand.Evidence() {
		t.Fatal("cold replay lost exact preparation")
	}
	if _, err := processsupervisor.RebuildPreparedCommandV2(evidence, payload); err != nil {
		t.Fatal("cold replay cannot rebuild exact request")
	}
	after, err = os.ReadFile(fixture.store.ledgerPath())
	if err != nil || len(after) <= len(beforeIntent) || !bytes.Equal(beforeIntent, after[:len(beforeIntent)]) {
		t.Fatal("intent changed history")
	}
	line := bytes.TrimSpace(after[len(beforeIntent):])
	var fact supervisorCommandFact
	if err := json.Unmarshal(line, &fact); err != nil || fact.ProtocolRevision != processsupervisor.DormantV2ProtocolContract().CommandRecoveryRevision {
		t.Fatalf("v2 intent lacks exact recovery header: %v", err)
	}
	key, _ := preIntent.Identity.Key()
	fresh := newAuthorityProjection()
	fresh.attempts[key] = preIntent
	if err := applySupervisorCommandLine(line, fresh, fact.Sequence); err != nil || !reflect.DeepEqual(fresh.attempts[key], next) {
		t.Fatalf("exact recovery line rejected: %v", err)
	}
	// Changing the outer recovery generation and recomputing its digest must
	// not translate an otherwise valid v2 command into a legacy fact.
	fact.ProtocolRevision, fact.Digest = supervisorCommandProtocolRevision, ""
	fact.Digest, err = canonicalDigest(fact)
	if err != nil {
		t.Fatal(err)
	}
	forgedLine, err := processsupervisor.CanonicalProtocolMessage(fact)
	if err != nil {
		t.Fatal(err)
	}
	fresh.attempts[key] = preIntent
	if applySupervisorCommandLine(forgedLine, fresh, fact.Sequence) == nil || !reflect.DeepEqual(fresh.attempts[key], preIntent) {
		t.Fatal("mixed recovery generation accepted or changed state")
	}
	outcome := testBindOutcomeV2(t, intent)
	preOutcome := next
	beforeOutcome, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	wrongOutcome := testBindOutcomeV2(t, forgedIntent)
	err = fixture.store.transact(projection, func() error {
		_, _, err := fixture.store.appendPreparedSupervisorOutcomeLocked(projection, preOutcome, wrongOutcome)
		return err
	})
	if err == nil {
		t.Fatal("valid receipt for unrelated intent admitted")
	}
	after, err = os.ReadFile(fixture.store.ledgerPath())
	if err != nil || !bytes.Equal(beforeOutcome, after) {
		t.Fatal("rejected outcome modified ledger")
	}
	err = fixture.store.transact(projection, func() error {
		var err error
		next, _, err = fixture.store.appendPreparedSupervisorOutcomeLocked(projection, preOutcome, outcome)
		return err
	})
	if err != nil {
		t.Fatalf("durable v2 outcome: %v", err)
	}
	replayed, found, err = reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, next) || replayed.SupervisorPendingIntentDigest != "" || replayed.SupervisorCommandSequence != 1 ||
		replayed.SupervisorMechanicsAnchor != outcome.PostCommand || replayed.SupervisorBoundAuthorityHead != next.SupervisorStartedDigest || len(replayed.SupervisorCommandCheckpoints) != 1 {
		t.Fatalf("cold v2 command closure: %v", err)
	}
	checkpoint := replayed.SupervisorCommandCheckpoints[0]
	if checkpoint.Intent != intent || checkpoint.Evidence != outcome || checkpoint.FactDigest == "" {
		t.Fatal("cold checkpoint lost exact intent/outcome")
	}
	after, err = os.ReadFile(fixture.store.ledgerPath())
	if err != nil || len(after) <= len(beforeOutcome) || !bytes.Equal(beforeOutcome, after[:len(beforeOutcome)]) {
		t.Fatal("outcome changed history")
	}
	line = bytes.TrimSpace(after[len(beforeOutcome):])
	var outcomeFact supervisorCommandFact
	if err := json.Unmarshal(line, &outcomeFact); err != nil || outcomeFact.ProtocolRevision != processsupervisor.DormantV2ProtocolContract().CommandRecoveryRevision {
		t.Fatalf("outcome lost v2 recovery generation: %v", err)
	}
	testLauncherV2StartedAndResume(t, fixture, projection, next)
}

// Continue the same durable business chain, not an independently seeded
// registry. The only fake is the peer report; no executable is launched.
func testLauncherV2StartedAndResume(t *testing.T, fixture preparedExecutionFixture, projection *Ingress, state AttemptAuthorityState) {
	t.Helper()
	_, provision, err := currentPreparedProvisionReceipt(projection, state)
	if err != nil {
		t.Fatal(err)
	}
	live, err := preparedAllocationLiveIdentity(provision.LiveIdentity)
	if err != nil {
		t.Fatal(err)
	}
	source := preparedAllocationSource{liveIdentity: live}
	closure, err := state.LaunchClosure.Closure()
	if err != nil {
		t.Fatal(err)
	}
	spawnPayload, err := preparedSpawnPayload(state, closure, source)
	if err != nil {
		t.Fatal(err)
	}
	appendCommand := func(command processsupervisor.CommandName, payload any, report *processsupervisor.ProcessReport) string {
		t.Helper()
		anchor := supervisorSessionAnchorV2(state.SupervisorMechanicsAnchor)
		prepared, err := processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: command, CommandID: "durable-v2-" + string(command),
			Sequence: anchor.Binding.CommandSequence + 1, PreviousCommandDigest: anchor.Binding.CommandHead, CurrentAuthorityHead: state.HeadDigest,
			Deadline: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}, payload)
		if err != nil {
			t.Fatalf("prepare %s: %v", command, err)
		}
		intent, err := NewSupervisorCommandIntentV2(prepared.Evidence())
		if err != nil {
			t.Fatal(err)
		}
		if command == processsupervisor.CommandSpawn {
			report.RuntimeObjectDigest, report.WorkingObjectDigest = intent.Rebuild.RuntimeObjectDigest, intent.Rebuild.WorkingObjectDigest
		}
		outcome := testCommandOutcomeV2(t, intent, report, spawnPayload.EnvironmentKeys)
		var digest string
		err = fixture.store.transact(projection, func() error {
			var err error
			state, _, err = fixture.store.appendPreparedSupervisorIntentLocked(projection, state, intent)
			if err != nil {
				return err
			}
			state, digest, err = fixture.store.appendPreparedSupervisorOutcomeLocked(projection, state, outcome)
			return err
		})
		if err != nil {
			t.Fatalf("persist %s: %v", command, err)
		}
		return digest
	}
	bindDigest := state.SupervisorCommandCheckpoints[0].FactDigest
	report := processsupervisor.ProcessReport{State: "exec-stopped", ObserverIdentity: state.SupervisorStarted.V2.Anchor.Generation.ObserverIdentity,
		ObservedAt: "2026-09-05T00:00:01Z", Process: processsupervisor.ProcessIdentity{PID: 4321, ProcessGroupID: 4321, SessionID: 4321, BirthSeconds: 100, BirthMicroseconds: 33},
		SourceGateRevision: processsupervisor.SourceGateRevisionV1, ExactSetDigest: attemptTestDigest("v2-exact-set")}
	spawnDigest := appendCommand(processsupervisor.CommandSpawn, spawnPayload, &report)
	spawn, _ := supervisorCheckpointEvidence(state, spawnDigest)
	process, err := preparedProcessObservation(closure, source, spawn.Outcome)
	if err != nil {
		t.Fatal(err)
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: state.Identity, CommandID: spawn.CommandID, ObservedAt: spawn.Outcome.ObservedAt, Process: process,
		LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest, SupervisorBindOutcomeFactDigest: bindDigest, SupervisorOutcomeFactDigest: spawnDigest}
	before, err := os.ReadFile(fixture.store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AttemptTransition){
		"observation-time": func(v *AttemptTransition) { v.ObservedAt = "2026-09-05T00:00:02Z" },
		"command-id":       func(v *AttemptTransition) { v.CommandID = "unrelated-spawn" },
		"observer": func(v *AttemptTransition) {
			v.Process.ObserverIdentity = "core-darwin-observer/v1"
			v.Process, err = SealProcessObservation(v.Process)
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		bad := transition
		mutate(&bad)
		err = fixture.store.transact(projection, func() error {
			_, _, err := fixture.store.appendPreparedAttemptTransitionLocked(projection, state, bad)
			return err
		})
		if err == nil {
			t.Fatalf("%s substituted business observation", name)
		}
		after, readErr := os.ReadFile(fixture.store.ledgerPath())
		if readErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("%s changed ledger", name)
		}
	}
	err = fixture.store.transact(projection, func() error {
		var err error
		state, _, err = fixture.store.appendPreparedAttemptTransitionLocked(projection, state, transition)
		return err
	})
	if err != nil {
		t.Fatalf("v2 business process started: %v", err)
	}
	if _, err := exactSuccessfulResume(state); err == nil {
		t.Fatal("spawn alone claimed resume")
	}
	report.State, report.ObservedAt = "running", "2026-09-05T00:00:02Z"
	resumeDigest := appendCommand(processsupervisor.CommandResume, processsupervisor.ResumePayload{ProcessStartedFactDigest: state.ProcessStartedDigest}, &report)
	reopened, err := OpenResultIngressStore(fixture.store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, found, err := reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(replayed, state) {
		t.Fatalf("cold started/resume: %v", err)
	}
	if digest, err := exactSuccessfulResume(replayed); err != nil || digest != resumeDigest {
		t.Fatalf("cold exact resume: %v", err)
	}
	replayed.ProcessStartedDigest = attemptTestDigest("unrelated-process-started")
	if _, err := exactSuccessfulResume(replayed); err == nil {
		t.Fatal("resume accepted unrelated business started fact")
	}
}
