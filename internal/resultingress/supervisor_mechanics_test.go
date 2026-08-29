package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func TestSupervisorCommandIntentConsumesProducerPreparedEvidence(t *testing.T) {
	pre := processsupervisor.HandshakeAnchor{
		SessionID: "prepared-session", SessionNonceDigest: attemptTestDigest("prepared-nonce"), Authority: supervisorAuthorityTuple(attemptTestIdentity()),
		OwnerEpoch: 1, CurrentAuthorityHead: attemptTestDigest("prepared-authority"), CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest,
		JournalSequence: 1, JournalHead: attemptTestDigest("prepared-journal"), UID: 501, GID: 20, FixedBinary: attemptTestBinary(),
		ControlSocket: processsupervisor.ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: 501, GID: 20, Mode: 0o140600, LinkCount: 1},
		ControlFiles: processsupervisor.SessionControlFiles{
			Nonce:   processsupervisor.ControlFileIdentity{Device: 8, Inode: 10, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
			Journal: processsupervisor.ControlFileIdentity{Device: 8, Inode: 11, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
		},
	}
	payload := processsupervisor.BindAuthorityPayload{SupervisorStartedFactDigest: attemptTestDigest("supervisor-started"), OwnerEpoch: 1, PreviousAuthorityHead: pre.CurrentAuthorityHead, AuthorityHead: attemptTestDigest("bound-authority")}
	prepared, err := processsupervisor.PrepareCommand(pre, processsupervisor.CommandOptions{Command: processsupervisor.CommandBindAuthority, CommandID: "bind-prepared", Sequence: 1, PreviousCommandDigest: processsupervisor.CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead, Deadline: time.Date(2026, 8, 29, 4, 5, 6, 0, time.UTC)}, payload)
	if err != nil {
		t.Fatal(err)
	}
	evidence := prepared.Evidence()
	intent, err := NewSupervisorCommandIntent(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if intent.RequestDigest != evidence.RequestDigest || intent.PayloadDigest != evidence.PayloadDigest || intent.Rebuild != evidence.Projection || intent.PreCommand.ControlFiles != pre.ControlFiles {
		t.Fatalf("intent does not preserve producer projection: %+v", intent)
	}
	forged := evidence
	forged.Projection.AuthorityHead = attemptTestDigest("forged-authority")
	if _, err := NewSupervisorCommandIntent(forged); err == nil {
		t.Fatal("consumer accepted projection drift from producer evidence")
	}
}

func testCommandEvidence(t *testing.T, session string, command processsupervisor.CommandName, currentHead string, outcome SupervisorProcessOutcome) SupervisorCommandEvidence {
	t.Helper()
	if outcome.MechanicsState == "" {
		outcome.MechanicsState = string(outcome.State)
		switch outcome.State {
		case SupervisorProcessExited, SupervisorTranscriptCollected, SupervisorSessionClosed:
			outcome.MechanicsState = "terminal"
		}
	}
	evidence := SupervisorCommandEvidence{
		ProtocolRevision:     processsupervisor.ProtocolRevision,
		SessionID:            session,
		Command:              command,
		CommandID:            "supervisor-" + string(command),
		Sequence:             2,
		PreviousCommandHead:  attemptTestDigest("previous-" + string(command)),
		CurrentAuthorityHead: currentHead,
		RequestDigest:        attemptTestDigest("request-" + string(command)),
		Disposition:          "ok",
		ReasonCode:           "process-supervisor-" + string(command) + "-ok",
		Outcome:              outcome,
	}
	pre := SupervisorMechanicsAnchor{SessionID: session, SessionNonceDigest: attemptTestDigest("nonce-" + session), Authority: supervisorAuthorityTuple(attemptTestIdentity()), OwnerEpoch: 1, CurrentAuthorityHead: currentHead, CommandSequence: 1, CommandHead: evidence.PreviousCommandHead, JournalSequence: 3, JournalHead: attemptTestDigest("journal-before-" + string(command)), UID: 501, GID: 20, FixedBinary: attemptTestBinary(), ControlSocket: processsupervisor.ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}}
	evidence.PreCommand = pre
	observation, receipt, err := evidence.boundMechanicsDigests()
	if err != nil {
		t.Fatal(err)
	}
	evidence.ObservationDigest, evidence.ReceiptDigest = observation, receipt
	head, err := canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{evidence.PreviousCommandHead, evidence.RequestDigest, evidence.ReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	evidence.CommandHead = head
	evidence.PostCommand = pre
	evidence.PostCommand.CommandSequence = evidence.Sequence
	evidence.PostCommand.CommandHead = evidence.CommandHead
	evidence.PostCommand.JournalSequence += 2
	evidence.PostCommand.JournalHead = attemptTestDigest("journal-after-" + string(command))
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	return evidence
}

// verifiedSupervisorOutcome constructs the exact public client result shape
// from the authenticated mechanics report and its receipt chain. Tests using
// this helper exercise NewSupervisorCommandEvidence rather than bypassing the
// constructor with a hand-authored SupervisorCommandEvidence.
func verifiedSupervisorOutcome(t *testing.T, intent SupervisorCommandIntent, reason string, report processsupervisor.ProcessReport) processsupervisor.VerifiedCommandOutcome {
	t.Helper()
	payload, err := processsupervisor.CanonicalProtocolMessage(report)
	if err != nil {
		t.Fatal(err)
	}
	observation := canonical.DigestBytes(payload)
	result := processsupervisor.MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: observation, Payload: payload}
	receipt, err := canonicalDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	commandHead, err := canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{intent.PreviousCommandHead, intent.RequestDigest, receipt})
	if err != nil {
		t.Fatal(err)
	}
	pre := supervisorHandshakeAnchor(intent.PreCommand)
	post := pre
	post.CommandSequence = intent.Sequence
	post.CommandHead = commandHead
	post.JournalSequence += 2
	post.JournalHead = attemptTestDigest("verified-journal-" + intent.CommandID + "-" + reason)
	return processsupervisor.VerifiedCommandOutcome{
		Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence,
		Status: "ok", Disposition: "ok", ReasonCode: reason, RequestDigest: intent.RequestDigest,
		ReceiptDigest: receipt, ObservationDigest: observation, CommandHead: commandHead,
		ProcessReport: &report,
		Recovery:      processsupervisor.CommandRecoveryEvidence{PreCommand: pre, PostCommand: post},
	}
}

func appendVerifiedSupervisorCheckpoint(t *testing.T, store *DurableStore, state AttemptAuthorityState, intent SupervisorCommandIntent, verified processsupervisor.VerifiedCommandOutcome) AttemptAuthorityState {
	t.Helper()
	owner, found, err := store.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	run := attemptTestRunAuthority(state.Identity)
	request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	intended, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewSupervisorCommandEvidence(verified)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := store.AppendSupervisorCommandOutcome(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return closed.State
}

func testPreparedSupervisor(t *testing.T, store *DurableStore, state AttemptAuthorityState, session string, directory processsupervisor.ControlDirectoryIdentity) (AttemptAuthorityState, ControlOwnerAcquisition) {
	t.Helper()
	scope := attemptTestOwnerScope(state.Identity)
	prior, found, err := store.OpenOwner(scope)
	if err != nil {
		t.Fatal(err)
	}
	epoch, previousDigest := uint64(1), ""
	if found {
		epoch, previousDigest = prior.Acquisition.OwnerEpoch+1, prior.FactDigest
	}
	binary := attemptTestBinary()
	ownerProcess := processsupervisor.ProcessIdentity{PID: 8100 + int(epoch), BirthSeconds: 1_700_000_000, BirthMicroseconds: 31, SessionID: 8100 + int(epoch), ProcessGroupID: 8100 + int(epoch)}
	acquisition := ControlOwnerAcquisition{Scope: scope, OwnerEpoch: epoch, OwnerUID: 501, OwnerGID: 20, OwnerProcess: ownerProcess, OwnerBinary: binary, ObserverIdentity: "darwin-owner-observer/v1", ObservedAt: "2026-08-28T00:00:00Z"}
	ownerResult, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, epoch-1, previousDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: scope, OwnerEpoch: epoch, ControlOwnerAcquiredFactDigest: ownerResult.State.FactDigest}
	run := attemptTestRunAuthority(state.Identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, owner)
	if err != nil {
		t.Fatal(err)
	}
	request := processsupervisor.BootstrapRequest{SchemaVersion: processsupervisor.BootstrapSchema, ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: session, SessionNonce: strings.Repeat("2", 64), OwnerEpoch: owner.OwnerEpoch, Authority: supervisorAuthorityTuple(state.Identity), LaunchAuthorizedFact: bound.State.LaunchAuthorizedDigest, CurrentAuthorityHead: bound.State.HeadDigest, ControlDirectoryIdentity: directory, Core: processsupervisor.CoreIdentity{UID: acquisition.OwnerUID, GID: acquisition.OwnerGID, Process: acquisition.OwnerProcess, Binary: acquisition.OwnerBinary}}
	prepared, err := NewSupervisorBootstrapPrepared(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AppendSupervisorBootstrap(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	return result.State, acquisition
}

func testStartPreparedSupervisor(t *testing.T, store *DurableStore, state AttemptAuthorityState, acquisition ControlOwnerAcquisition) AttemptAuthorityState {
	t.Helper()
	prepared := state.SupervisorBootstrap
	socket := processsupervisor.ControlSocketIdentity{Device: prepared.ControlDirectory.Device, Inode: prepared.ControlDirectory.Inode + 100, FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}
	supervisorProcess := processsupervisor.ProcessIdentity{PID: 9200 + int(prepared.Owner.OwnerEpoch), BirthSeconds: 1_700_000_001, BirthMicroseconds: 32, SessionID: 9200 + int(prepared.Owner.OwnerEpoch), ProcessGroupID: 9200 + int(prepared.Owner.OwnerEpoch)}
	handshake := processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema, ProtocolRevision: processsupervisor.ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: prepared.SessionID, SessionNonceDigest: prepared.SessionNonceDigest, OwnerEpoch: prepared.Owner.OwnerEpoch, CurrentAuthorityHead: prepared.Request.CurrentAuthorityHead, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: attemptTestDigest("journal-" + prepared.SessionID), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:01Z", SupervisorProcess: supervisorProcess, SupervisorBinary: prepared.SupervisorBinary, ControlSocket: socket}
	anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: prepared.Request.Authority, OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: prepared.Request.CurrentAuthorityHead, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: handshake.JournalHead, UID: 501, GID: 20, FixedBinary: prepared.SupervisorBinary, ControlSocket: socket}
	started, err := NewProcessSupervisorStartedFromBootstrap(state.SupervisorBootstrapDigest, prepared, handshake, anchor, processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: supervisorProcess, Binary: prepared.SupervisorBinary})
	if err != nil {
		t.Fatal(err)
	}
	run := attemptTestRunAuthority(state.Identity)
	result, err := store.AppendSupervisorStarted(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, started)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func testOpenedAuthorizedAttempt(t *testing.T, store *DurableStore, id AttemptIdentity) AttemptAuthorityState {
	t.Helper()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	provisioned := appendTestAcceptedProvision(t, store, opened.State)
	authorized, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-" + id.AttemptID})
	if err != nil {
		t.Fatal(err)
	}
	return authorized.State
}

func TestSupervisorCommandEvidenceRejectsForgedChainAndAuthorityHead(t *testing.T) {
	process := attemptTestProcess(t)
	outcome := SupervisorProcessOutcome{State: SupervisorProcessExecStopped, Process: processsupervisor.ProcessIdentity{PID: process.PID, BirthSeconds: process.BirthSeconds, BirthMicroseconds: process.BirthMicroseconds, SessionID: process.PID, ProcessGroupID: process.PGID}, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:02Z", RuntimeObjectDigest: attemptTestDigest("runtime"), WorkingObjectDigest: attemptTestDigest("cwd")}
	evidence := testCommandEvidence(t, "session-forgery", processsupervisor.CommandSpawn, attemptTestDigest("authority-head"), outcome)
	for name, mutate := range map[string]func(*SupervisorCommandEvidence){
		"receipt": func(value *SupervisorCommandEvidence) { value.ReceiptDigest = attemptTestDigest("forged-receipt") },
		"observation": func(value *SupervisorCommandEvidence) {
			value.ObservationDigest = attemptTestDigest("forged-observation")
		},
		"head": func(value *SupervisorCommandEvidence) { value.CommandHead = attemptTestDigest("forged-head") },
		"typed-outcome": func(value *SupervisorCommandEvidence) {
			value.Outcome.Process.PID++
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := evidence
			mutate(&forged)
			if err := forged.Validate(); !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("expected forged chain rejection, got %v", err)
			}
		})
	}

	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authorized := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	directory := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/prepared-forgery", Device: 31, Inode: 41, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, owner := testPreparedSupervisor(t, store, authorized, "session-forgery", directory)
	started := testStartPreparedSupervisor(t, store, prepared, owner)
	evidence.CurrentAuthorityHead = attemptTestDigest("wrong-attempt-head")
	evidence.CommandHead, err = canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{evidence.PreviousCommandHead, evidence.RequestDigest, evidence.ReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: started.Identity, CommandID: "command-1", ObservedAt: "2026-08-28T00:00:02Z", Process: process, LaunchMaterialsDigest: started.LaunchMaterialsDigest, AgentLaunchSpecDigest: started.AgentLaunchSpecDigest, SupervisorEvidence: evidence}
	run := attemptTestRunAuthority(started.Identity)
	_, err = store.AppendProcessStarted(context.Background(), attemptOwnerVerifier{want: owner}, attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, AttemptAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, started.Owner, transition)
	if !errors.Is(err, ErrAttemptAuthorityConflict) {
		t.Fatalf("wrong authority head must reject, got %v", err)
	}
}

func TestNewSupervisorCommandEvidencePreservesInspectAndTerminateSemantics(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := openFreshStartedAttempt(t, store)
	process := state.ProcessStartedEvidence.Outcome
	base := processsupervisor.ProcessReport{
		ObserverIdentity: process.ObserverIdentity, ObservedAt: "2026-08-28T00:00:04Z",
		Process: process.Process, RuntimeObjectDigest: process.RuntimeObjectDigest, WorkingObjectDigest: process.WorkingObjectDigest,
	}
	for _, tc := range []struct {
		name    string
		command processsupervisor.CommandName
		reason  string
		state   string
		want    SupervisorProcessState
	}{
		{name: "inspect-running", command: processsupervisor.CommandInspect, reason: "process-inspected", state: "running", want: SupervisorProcessRunning},
		{name: "inspect-exec-stopped", command: processsupervisor.CommandInspect, reason: "process-inspected", state: "exec-stopped", want: SupervisorProcessExecStopped},
		{name: "terminate-already-terminal-is-absent", command: processsupervisor.CommandTerminate, reason: "process-already-terminal", state: "terminal", want: SupervisorProcessAbsent},
		{name: "terminate-after-group-signal", command: processsupervisor.CommandTerminate, reason: "process-terminal", state: "terminal", want: SupervisorProcessExited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := testSupervisorIntent(state, tc.command, SupervisorCommandRebuildProjection{})
			report := base
			report.State = tc.state
			verified := verifiedSupervisorOutcome(t, intent, tc.reason, report)
			evidence, err := NewSupervisorCommandEvidence(verified)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Outcome.State != tc.want {
				t.Fatalf("semantic state=%q want=%q", evidence.Outcome.State, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		command processsupervisor.CommandName
		reason  string
		state   string
	}{
		{name: "inspect-forged-terminate-reason", command: processsupervisor.CommandInspect, reason: "process-terminal", state: "terminal"},
		{name: "terminate-forged-inspect-reason", command: processsupervisor.CommandTerminate, reason: "process-inspected", state: "terminal"},
		{name: "terminate-already-terminal-but-running", command: processsupervisor.CommandTerminate, reason: "process-already-terminal", state: "running"},
		{name: "terminate-process-terminal-but-exec-stopped", command: processsupervisor.CommandTerminate, reason: "process-terminal", state: "exec-stopped"},
	} {
		t.Run("forged-"+tc.name, func(t *testing.T) {
			intent := testSupervisorIntent(state, tc.command, SupervisorCommandRebuildProjection{})
			report := base
			report.State = tc.state
			verified := verifiedSupervisorOutcome(t, intent, tc.reason, report)
			if _, err := NewSupervisorCommandEvidence(verified); !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("forged reason/outcome accepted: %v", err)
			}
		})
	}
}

func TestInspectOutcomeGatesTerminateIntentOnlyAfterTerminal(t *testing.T) {
	for _, tc := range []struct {
		mechanicsState string
		wantAllowed    bool
	}{
		{mechanicsState: "running", wantAllowed: true},
		{mechanicsState: "exec-stopped", wantAllowed: true},
		{mechanicsState: "terminal", wantAllowed: false},
	} {
		t.Run(tc.mechanicsState, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := openFreshStartedAttempt(t, store)
			state = appendTestBarrier(t, store, state, "terminal-inspect-"+tc.mechanicsState, TerminalAttemptFailed).State
			state = appendTestSupervisorReconnect(t, store, state)
			cleanupRebuild := SupervisorCommandRebuildProjection{
				TerminalizationBarrierDigest: state.BarrierDigest, TerminalizationID: state.TerminalizationID,
				TerminalGeneration: uint64(state.TerminalGeneration), CleanupBindingDigest: state.CleanupBindingDigest,
				ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state),
			}
			inspect := testSupervisorIntent(state, processsupervisor.CommandInspect, cleanupRebuild)
			started := state.ProcessStartedEvidence.Outcome
			report := processsupervisor.ProcessReport{
				State: tc.mechanicsState, ObserverIdentity: started.ObserverIdentity, ObservedAt: "2026-08-28T00:00:04Z",
				Process: started.Process, RuntimeObjectDigest: started.RuntimeObjectDigest, WorkingObjectDigest: started.WorkingObjectDigest,
			}
			state = appendVerifiedSupervisorCheckpoint(t, store, state, inspect, verifiedSupervisorOutcome(t, inspect, "process-inspected", report))

			cleanupRebuild.LastObservationDigest = supervisorLastObservation(state)
			terminate := testSupervisorIntent(state, processsupervisor.CommandTerminate, cleanupRebuild)
			owner, found, err := store.OpenOwner(state.Owner.Scope)
			if err != nil || !found {
				t.Fatalf("current owner found=%v err=%v", found, err)
			}
			run := attemptTestRunAuthority(state.Identity)
			request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
			beforeLedger, err := os.ReadFile(store.ledgerPath())
			if err != nil {
				t.Fatal(err)
			}
			beforeFactCount := bytes.Count(beforeLedger, []byte{'\n'})
			resume := testSupervisorIntent(state, processsupervisor.CommandResume, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest})
			if _, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, resume); !errors.Is(err, ErrAttemptAuthorityOrder) {
				t.Fatalf("resume after barrier and %s inspect was not rejected: %v", tc.mechanicsState, err)
			}
			afterLedger, err := os.ReadFile(store.ledgerPath())
			if err != nil {
				t.Fatal(err)
			}
			current, found, err := store.AttemptState(state.Identity)
			if err != nil || !found {
				t.Fatalf("attempt state found=%v err=%v", found, err)
			}
			if !bytes.Equal(beforeLedger, afterLedger) || bytes.Count(afterLedger, []byte{'\n'}) != beforeFactCount || current.Revision != state.Revision || current.HeadDigest != state.HeadDigest {
				t.Fatalf("rejected resume mutated durable authority: facts=%d/%d revision=%d/%d head=%s/%s", beforeFactCount, bytes.Count(afterLedger, []byte{'\n'}), state.Revision, current.Revision, state.HeadDigest, current.HeadDigest)
			}
			_, err = store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, terminate)
			if tc.wantAllowed && err != nil {
				t.Fatalf("terminate after %s inspect rejected: %v", tc.mechanicsState, err)
			}
			if !tc.wantAllowed && !errors.Is(err, ErrAttemptAuthorityOrder) {
				t.Fatalf("terminal inspect did not require business process-terminal fact: %v", err)
			}
		})
	}
}

func TestSupervisorBootstrapCrashGapReplaysAcrossTwoRestartsAndIntervenes(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	authorized := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/bootstrap-crash-gap", Device: 51, Inode: 61, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, owner := testPreparedSupervisor(t, store, authorized, "session-crash-gap", control)
	started := testStartPreparedSupervisor(t, store, prepared, owner)
	bind := testSupervisorIntent(started, processsupervisor.CommandBindAuthority, SupervisorCommandRebuildProjection{SupervisorStartedFactDigest: started.SupervisorStartedDigest, OwnerEpoch: started.Owner.OwnerEpoch, PreviousAuthorityHead: started.SupervisorStarted.Handshake.CurrentAuthorityHead, AuthorityHead: started.SupervisorStartedDigest})
	started, _ = appendTestSupervisorCheckpoint(t, store, started, bind, SupervisorProcessOutcome{}, "ok")
	intent := testSupervisorIntent(started, processsupervisor.CommandSpawn, SupervisorCommandRebuildProjection{SupervisorStartedFactDigest: started.SupervisorStartedDigest, LaunchAuthorizedFactDigest: started.LaunchAuthorizedDigest, LaunchMaterialsDigest: started.LaunchMaterialsDigest, AgentLaunchSpecDigest: started.AgentLaunchSpecDigest, RuntimeObjectDigest: attemptTestDigest("crash-runtime"), WorkingObjectDigest: attemptTestDigest("crash-cwd"), ArgvDigest: attemptTestDigest("crash-argv"), EnvironmentDigest: attemptTestDigest("crash-env"), StdinDigest: attemptTestDigest("crash-stdin")})
	run := attemptTestRunAuthority(started.Identity)
	pendingResult, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner}, attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, AttemptAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, started.Owner, intent)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := pendingResult.State.SupervisorPendingIntentDigest
	if pendingResult.State.Revision != started.Revision || pendingResult.State.HeadDigest != started.HeadDigest {
		t.Fatalf("recovery intent advanced Attempt authority: before=%d/%s after=%d/%s", started.Revision, started.HeadDigest, pendingResult.State.Revision, pendingResult.State.HeadDigest)
	}

	for restart := 1; restart <= 2; restart++ {
		store, err = OpenResultIngressStore(dir)
		if err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		pending, err := store.PendingAttemptStates()
		if err != nil || len(pending) != 1 || pending[0].SupervisorPendingIntentDigest != wantDigest || pending[0].SupervisorStartedDigest == "" {
			t.Fatalf("restart %d projection mismatch: pending=%+v err=%v", restart, pending, err)
		}
	}

	current, _, err := store.AttemptState(started.Identity)
	if err != nil {
		t.Fatal(err)
	}
	ownerState, found, err := store.OpenOwner(current.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	recoveryOwner := ownerState.Acquisition
	recoveryOwner.OwnerEpoch++
	recoveryOwner.OwnerProcess = processsupervisor.ProcessIdentity{PID: 14000, BirthSeconds: 1_700_000_020, BirthMicroseconds: 14, SessionID: 14000, ProcessGroupID: 14000}
	recoveryOwner.ObservedAt = "2026-08-29T00:00:20Z"
	acquired, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: recoveryOwner}, ownerState.Acquisition.OwnerEpoch, ownerState.FactDigest, recoveryOwner)
	if err != nil {
		t.Fatal(err)
	}
	recoveryBinding := CurrentOwnerBinding{Scope: current.Owner.Scope, OwnerEpoch: recoveryOwner.OwnerEpoch, ControlOwnerAcquiredFactDigest: acquired.State.FactDigest}
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: recoveryOwner}, attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, AttemptAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: run}, recoveryBinding)
	if err != nil {
		t.Fatal(err)
	}
	pending := processsupervisor.PendingReplayEvidence{ProtocolRevision: intent.ProtocolRevision, SessionID: intent.SessionID, Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence, PreviousCommandDigest: intent.PreviousCommandHead, CurrentAuthorityHead: intent.CurrentAuthorityHead, RequestDigest: intent.RequestDigest, Deadline: intent.Deadline}
	previous := supervisorHandshakeAnchor(intent.PreCommand)
	next := previous
	next.OwnerEpoch = recoveryOwner.OwnerEpoch
	next.CurrentAuthorityHead = bound.State.HeadDigest
	next.JournalSequence++
	next.JournalHead = attemptTestDigest("pending-intent-journal")
	recovery := processsupervisor.SessionRecoveryEvidence{Reconciliation: processsupervisor.ReconciliationIntentPending, Previous: previous, Current: next, Pending: &pending, MechanicsLocked: true}
	reconnected, err := store.AppendSupervisorReconnect(context.Background(), attemptOwnerVerifier{want: recoveryOwner}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: run}, recoveryBinding, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.State.SupervisorReconnectFactDigest == "" || reconnected.State.SupervisorReconnect.Pending != pending || !reconnected.State.SupervisorReconnect.MechanicsLocked || reconnected.State.SupervisorMechanicsAnchor != projectSupervisorMechanicsAnchor(recovery.Current) {
		t.Fatalf("pending reconnect not durably projected: %+v", reconnected.State)
	}
	intervention := SupervisorIntervention{ProtocolRevision: processsupervisor.ProtocolRevision, Owner: recoveryBinding, SessionID: reconnected.State.SupervisorBootstrap.SessionID, Reason: SupervisorInterventionCommandUnresolved, EvidenceDigest: attemptTestDigest("pending-intent-evidence"), Pending: SupervisorPendingCommand{SessionID: intent.SessionID, Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence, PreviousCommandHead: intent.PreviousCommandHead, CurrentAuthorityHead: intent.CurrentAuthorityHead, RequestDigest: intent.RequestDigest}}
	intervened, err := store.AppendSupervisorIntervention(context.Background(), attemptOwnerVerifier{want: recoveryOwner}, attemptRunVerifier{want: run}, reconnected.State.Revision, reconnected.State.HeadDigest, AttemptAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: run}, intervention)
	if err != nil {
		t.Fatal(err)
	}
	if intervened.State.SupervisorInterventionDigest == "" {
		t.Fatal("missing durable intervention digest")
	}
	_, err = store.AppendSupervisorIntervention(context.Background(), attemptOwnerVerifier{want: recoveryOwner}, attemptRunVerifier{want: run}, intervened.State.Revision, intervened.State.HeadDigest, AttemptAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: run}, SupervisorIntervention{ProtocolRevision: processsupervisor.ProtocolRevision, Owner: recoveryBinding, SessionID: current.SupervisorBootstrap.SessionID, Reason: SupervisorInterventionUnavailable, EvidenceDigest: attemptTestDigest("second-intervention")})
	if !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("intervention must permanently fence successors, got %v", err)
	}
}

func TestSupervisorRecoverySubchainRejectsSequenceHeadIDAndJournalForgery(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authorized := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/recovery-hostile", Device: 151, Inode: 161, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, acquisition := testPreparedSupervisor(t, store, authorized, "session-recovery-hostile", control)
	state := testStartPreparedSupervisor(t, store, prepared, acquisition)
	bind := testSupervisorIntent(state, processsupervisor.CommandBindAuthority, SupervisorCommandRebuildProjection{SupervisorStartedFactDigest: state.SupervisorStartedDigest, OwnerEpoch: state.Owner.OwnerEpoch, PreviousAuthorityHead: state.SupervisorStarted.Handshake.CurrentAuthorityHead, AuthorityHead: state.SupervisorStartedDigest})
	state, _ = appendTestSupervisorCheckpoint(t, store, state, bind, SupervisorProcessOutcome{}, "ok")
	wantRevision, wantHead := state.Revision, state.HeadDigest
	spawn := testSupervisorIntent(state, processsupervisor.CommandSpawn, SupervisorCommandRebuildProjection{SupervisorStartedFactDigest: state.SupervisorStartedDigest, LaunchAuthorizedFactDigest: state.LaunchAuthorizedDigest, LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest, RuntimeObjectDigest: attemptTestDigest("hostile-runtime"), WorkingObjectDigest: attemptTestDigest("hostile-cwd"), ArgvDigest: attemptTestDigest("hostile-argv"), EnvironmentDigest: attemptTestDigest("hostile-env"), StdinDigest: attemptTestDigest("hostile-stdin")})
	owner, _, err := store.OpenOwner(state.Owner.Scope)
	if err != nil {
		t.Fatal(err)
	}
	run := attemptTestRunAuthority(state.Identity)
	request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	for name, mutate := range map[string]func(*SupervisorCommandIntent){
		"sequence": func(value *SupervisorCommandIntent) { value.Sequence++ },
		"head": func(value *SupervisorCommandIntent) {
			value.PreviousCommandHead = attemptTestDigest("wrong-previous-head")
		},
		"duplicate-command-id": func(value *SupervisorCommandIntent) { value.CommandID = bind.CommandID },
		"authority-head": func(value *SupervisorCommandIntent) {
			value.CurrentAuthorityHead = attemptTestDigest("wrong-authority-head")
			value.PreCommand.CurrentAuthorityHead = value.CurrentAuthorityHead
		},
		"journal-head": func(value *SupervisorCommandIntent) {
			value.PreCommand.JournalHead = attemptTestDigest("wrong-journal-head")
		},
	} {
		t.Run("intent-"+name, func(t *testing.T) {
			forged := spawn
			mutate(&forged)
			if _, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, forged); err == nil {
				t.Fatal("forged command intent appended")
			}
		})
	}
	intended, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, spawn)
	if err != nil {
		t.Fatal(err)
	}
	process := attemptTestProcess(t)
	outcome := SupervisorProcessOutcome{State: SupervisorProcessExecStopped, MechanicsState: "exec-stopped", Process: processsupervisor.ProcessIdentity{PID: process.PID, BirthSeconds: process.BirthSeconds, BirthMicroseconds: process.BirthMicroseconds, SessionID: process.PID, ProcessGroupID: process.PGID}, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:02Z", RuntimeObjectDigest: spawn.Rebuild.RuntimeObjectDigest, WorkingObjectDigest: spawn.Rebuild.WorkingObjectDigest}
	evidence := testSupervisorEvidence(t, spawn, outcome, "ok")
	for name, mutate := range map[string]func(*SupervisorCommandEvidence){
		"sequence":   func(value *SupervisorCommandEvidence) { value.Sequence++ },
		"request":    func(value *SupervisorCommandEvidence) { value.RequestDigest = attemptTestDigest("wrong-request") },
		"command-id": func(value *SupervisorCommandEvidence) { value.CommandID = bind.CommandID },
		"pre-journal": func(value *SupervisorCommandEvidence) {
			value.PreCommand.JournalHead = attemptTestDigest("wrong-pre-journal")
		},
		"post-journal-sequence": func(value *SupervisorCommandEvidence) { value.PostCommand.JournalSequence++ },
	} {
		t.Run("outcome-"+name, func(t *testing.T) {
			forged := evidence
			mutate(&forged)
			if _, err := store.AppendSupervisorCommandOutcome(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, forged); err == nil {
				t.Fatal("forged command outcome appended")
			}
		})
	}
	closed, err := store.AppendSupervisorCommandOutcome(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State.Revision != wantRevision || closed.State.HeadDigest != wantHead || closed.State.SupervisorPendingIntentDigest != "" {
		t.Fatalf("recovery outcome changed business authority or remained pending: %+v", closed.State)
	}
}

func TestSupervisorReconnectFactIsRequiredBeforeBusinessHeadReanchor(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := openFreshStartedAttempt(t, store)
	run := attemptTestRunAuthority(state.Identity)
	oldOwner, _, err := store.OpenOwner(state.Owner.Scope)
	if err != nil {
		t.Fatal(err)
	}
	collect := testSupervisorIntent(state, processsupervisor.CommandCollect, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)})
	request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	if _, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: oldOwner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, collect); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("caller-authored business head reanchor err=%v", err)
	}

	epoch := oldOwner.Acquisition.OwnerEpoch + 1
	acquisition := oldOwner.Acquisition
	acquisition.OwnerEpoch = epoch
	acquisition.OwnerProcess = processsupervisor.ProcessIdentity{PID: 13000, BirthSeconds: 1_700_000_010, BirthMicroseconds: 13, SessionID: 13000, ProcessGroupID: 13000}
	acquisition.ObservedAt = "2026-08-29T00:00:10Z"
	ownerResult, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, oldOwner.Acquisition.OwnerEpoch, oldOwner.FactDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: state.Owner.Scope, OwnerEpoch: epoch, ControlOwnerAcquiredFactDigest: ownerResult.State.FactDigest}
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, owner)
	if err != nil {
		t.Fatal(err)
	}
	previous := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
	current := previous
	current.OwnerEpoch = epoch
	current.CurrentAuthorityHead = bound.State.HeadDigest
	recovery := processsupervisor.SessionRecoveryEvidence{Reconciliation: processsupervisor.ReconciliationUnchanged, Previous: previous, Current: current}
	for name, mutate := range map[string]func(*processsupervisor.SessionRecoveryEvidence){
		"previous-head": func(value *processsupervisor.SessionRecoveryEvidence) {
			value.Previous.CurrentAuthorityHead = attemptTestDigest("forged-reconnect-previous")
		},
		"current-head": func(value *processsupervisor.SessionRecoveryEvidence) {
			value.Current.CurrentAuthorityHead = attemptTestDigest("forged-reconnect-current")
		},
		"owner-epoch": func(value *processsupervisor.SessionRecoveryEvidence) { value.Current.OwnerEpoch++ },
	} {
		t.Run(name, func(t *testing.T) {
			forged := recovery
			mutate(&forged)
			if _, err := store.AppendSupervisorReconnect(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, request, owner, forged); err == nil {
				t.Fatal("forged reconnect appended")
			}
		})
	}
	reconnected, err := store.AppendSupervisorReconnect(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, request, owner, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.State.Revision != bound.State.Revision || reconnected.State.HeadDigest != bound.State.HeadDigest || reconnected.State.SupervisorMechanicsAnchor != projectSupervisorMechanicsAnchor(recovery.Current) {
		t.Fatalf("reconnect changed business authority or failed to persist anchor: %+v", reconnected.State)
	}
	collect = testSupervisorIntent(reconnected.State, processsupervisor.CommandCollect, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: reconnected.State.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(reconnected.State)})
	if _, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, reconnected.State.Revision, reconnected.State.HeadDigest, request, owner, collect); err != nil {
		t.Fatalf("authenticated reanchor did not admit next intent: %v", err)
	}
}

func TestSupervisorOutcomeRejectsChildSessionRuntimeAndWorkingObjectDrift(t *testing.T) {
	for name, mutate := range map[string]func(*SupervisorProcessOutcome){
		"child-session":  func(value *SupervisorProcessOutcome) { value.Process.SessionID++ },
		"runtime":        func(value *SupervisorProcessOutcome) { value.RuntimeObjectDigest = attemptTestDigest("drift-runtime") },
		"working-object": func(value *SupervisorProcessOutcome) { value.WorkingObjectDigest = attemptTestDigest("drift-cwd") },
	} {
		t.Run(name, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := openFreshStartedAttempt(t, store)
			state = appendTestSupervisorReconnect(t, store, state)
			intent := testSupervisorIntent(state, processsupervisor.CommandCollect, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)})
			owner, _, err := store.OpenOwner(state.Owner.Scope)
			if err != nil {
				t.Fatal(err)
			}
			run := attemptTestRunAuthority(state.Identity)
			request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
			intended, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, intent)
			if err != nil {
				t.Fatal(err)
			}
			outcome := state.ProcessStartedEvidence.Outcome
			outcome.State, outcome.MechanicsState = SupervisorTranscriptCollected, "terminal"
			outcome.ObservedAt = "2026-08-28T00:00:03Z"
			outcome.StdoutDigest, outcome.StderrDigest, outcome.TranscriptDigest = attemptTestDigest("drift-stdout"), attemptTestDigest("drift-stderr"), attemptTestDigest("drift-transcript")
			mutate(&outcome)
			forged := testSupervisorEvidence(t, intent, outcome, "ok")
			if _, err := store.AppendSupervisorCommandOutcome(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, forged); !errors.Is(err, ErrAttemptAuthorityOrder) {
				t.Fatalf("child drift err=%v", err)
			}
		})
	}
}

func TestSupervisorCloseCheckpointRequiresExactTerminalReport(t *testing.T) {
	process := attemptTestProcess(t)
	child := processsupervisor.ProcessIdentity{PID: process.PID, BirthSeconds: process.BirthSeconds, BirthMicroseconds: process.BirthMicroseconds, SessionID: process.PID, ProcessGroupID: process.PGID}
	terminalOutcome := SupervisorProcessOutcome{State: SupervisorProcessExited, MechanicsState: "terminal", Process: child, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:04Z", RuntimeObjectDigest: attemptTestDigest("close-runtime"), WorkingObjectDigest: attemptTestDigest("close-cwd"), ExitCode: 7, StdoutDigest: attemptTestDigest("close-stdout"), StderrDigest: attemptTestDigest("close-stderr")}
	terminal := testCommandEvidence(t, "session-close-report", processsupervisor.CommandTerminate, attemptTestDigest("terminal-authority"), terminalOutcome)
	closedOutcome := terminalOutcome
	closedOutcome.State = SupervisorSessionClosed
	closed := testCommandEvidence(t, "session-close-report", processsupervisor.CommandClose, attemptTestDigest("close-authority"), closedOutcome)
	checkpointDigest := attemptTestDigest("close-outcome-fact")
	state := AttemptAuthorityState{ProcessTerminalEvidence: terminal, SupervisorCommandCheckpoints: []SupervisorCommandCheckpoint{{FactDigest: checkpointDigest, Evidence: closed}}}
	transition := AttemptTransition{SupervisorOutcomeFactDigest: checkpointDigest, SupervisorClosed: ProcessSupervisorClosed{CloseIntentDigest: closed.RequestDigest, CloseReceiptDigest: closed.ReceiptDigest, CloseObservationDigest: closed.ObservationDigest, FinalCommandHead: closed.CommandHead}}
	if !closedCheckpointMatches(state, transition) {
		t.Fatal("exact terminal report did not match close checkpoint")
	}
	state.SupervisorCommandCheckpoints[0].Evidence.Outcome.ExitCode++
	if closedCheckpointMatches(state, transition) {
		t.Fatal("close checkpoint accepted a different terminal report")
	}
}

func TestSupervisorBootstrapPreparedRejectsTypedRequestMismatch(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/bootstrap-request-mismatch", Device: 171, Inode: 181, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, _ := testPreparedSupervisor(t, store, state, "session-bootstrap-request-mismatch", control)
	forged := prepared.SupervisorBootstrap
	forged.Request.CurrentAuthorityHead = attemptTestDigest("wrong-bootstrap-head")
	forged.BootstrapRequestDigest, err = canonicalDigest(forged.Request)
	if err != nil || forged.Validate() != nil {
		t.Fatalf("self-consistent hostile projection invalid before authority check: %v", err)
	}
	prior := prepared
	prior.Revision--
	prior.HeadDigest = prepared.SupervisorBootstrap.Request.CurrentAuthorityHead
	prior.SupervisorBootstrap = SupervisorBootstrapPrepared{}
	prior.SupervisorBootstrapDigest = ""
	projection := newAuthorityProjection()
	err = store.transact(projection, func() error {
		return validateSupervisorTransitionAgainstProjection(projection, prior, true, AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: prior.Identity, SupervisorBootstrap: forged}, false)
	})
	if !errors.Is(err, ErrAttemptAuthorityConflict) {
		t.Fatalf("typed bootstrap current-head mismatch err=%v", err)
	}
}

func TestSupervisorBootstrapRejectsSessionAndControlObjectABA(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstID := attemptTestIdentity()
	first := testOpenedAuthorizedAttempt(t, store, firstID)
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/bootstrap-aba", Device: 71, Inode: 81, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	first, _ = testPreparedSupervisor(t, store, first, "session-aba", control)

	secondID := firstID
	secondID.AttemptID = "attempt-2"
	secondID.AllocationID = "allocation-2"
	secondID.LeaseID = "lease-2"
	secondID.LeaseDigest = attemptTestDigest("lease-2")
	secondID.FencingTokenDigest = attemptTestDigest("fencing-2")
	second := testOpenedAuthorizedAttempt(t, store, secondID)
	scope := attemptTestOwnerScope(secondID)
	current, found, err := store.OpenOwner(scope)
	if err != nil || !found {
		t.Fatal(err)
	}
	epoch := current.Acquisition.OwnerEpoch + 1
	acquisition := current.Acquisition
	acquisition.OwnerEpoch = epoch
	acquisition.OwnerProcess = processsupervisor.ProcessIdentity{PID: 8300, BirthSeconds: 1_700_000_010, BirthMicroseconds: 1, SessionID: 8300, ProcessGroupID: 8300}
	acquisition.ObservedAt = "2026-08-28T00:00:10Z"
	ownerResult, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, current.Acquisition.OwnerEpoch, current.FactDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: scope, OwnerEpoch: epoch, ControlOwnerAcquiredFactDigest: ownerResult.State.FactDigest}
	run := attemptTestRunAuthority(secondID)
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, second.Revision, second.HeadDigest, AttemptAuthorizationRequest{Identity: secondID, CurrentRunAuthority: run}, owner)
	if err != nil {
		t.Fatal(err)
	}
	for name, prepared := range map[string]SupervisorBootstrapPrepared{
		"session": {ProtocolRevision: processsupervisor.ProtocolRevision, Owner: owner, LaunchAuthorizedFactDigest: bound.State.LaunchAuthorizedDigest, SessionID: first.SupervisorBootstrap.SessionID, SessionNonceDigest: attemptTestDigest("nonce-reuse"), ControlDirectory: processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/bootstrap-aba-2", Device: 71, Inode: 82, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}, SupervisorBinary: acquisition.OwnerBinary, BootstrapRequestDigest: attemptTestDigest("bootstrap-reuse-session")},
		"object":  {ProtocolRevision: processsupervisor.ProtocolRevision, Owner: owner, LaunchAuthorizedFactDigest: bound.State.LaunchAuthorizedDigest, SessionID: "session-aba-2", SessionNonceDigest: attemptTestDigest("nonce-object-reuse"), ControlDirectory: control, SupervisorBinary: acquisition.OwnerBinary, BootstrapRequestDigest: attemptTestDigest("bootstrap-reuse-object")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.AppendSupervisorBootstrap(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: secondID, CurrentRunAuthority: run}, prepared)
			if !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("expected ABA rejection, got %v", err)
			}
		})
	}
}

func TestHistoricalSupervisorLedgerWithoutMechanicsEvidenceStillReplays(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	authorized := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	scope := attemptTestOwnerScope(authorized.Identity)
	binary := attemptTestBinary()
	ownerProcess := processsupervisor.ProcessIdentity{PID: 8401, BirthSeconds: 1_700_000_000, BirthMicroseconds: 51, SessionID: 8401, ProcessGroupID: 8401}
	acquisition := ControlOwnerAcquisition{Scope: scope, OwnerEpoch: 1, OwnerUID: 501, OwnerGID: 20, OwnerProcess: ownerProcess, OwnerBinary: binary, ObserverIdentity: "darwin-owner-observer/v1", ObservedAt: "2026-08-28T00:00:00Z"}
	ownerFact, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, 0, "", acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: scope, OwnerEpoch: 1, ControlOwnerAcquiredFactDigest: ownerFact.State.FactDigest}
	run := attemptTestRunAuthority(authorized.Identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, authorized.Revision, authorized.HeadDigest, AttemptAuthorizationRequest{Identity: authorized.Identity, CurrentRunAuthority: run}, owner)
	if err != nil {
		t.Fatal(err)
	}
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/historical-supervisor", Device: 121, Inode: 131, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	socket := processsupervisor.ControlSocketIdentity{Device: 121, Inode: 132, FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}
	supervisorProcess := processsupervisor.ProcessIdentity{PID: 9401, BirthSeconds: 1_700_000_001, BirthMicroseconds: 52, SessionID: 9401, ProcessGroupID: 9401}
	handshake := processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema, ProtocolRevision: processsupervisor.ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: "historical-session", SessionNonceDigest: attemptTestDigest("historical-nonce"), OwnerEpoch: 1, CurrentAuthorityHead: bound.State.HeadDigest, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: attemptTestDigest("historical-journal"), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:01Z", SupervisorProcess: supervisorProcess, SupervisorBinary: binary, ControlSocket: socket}
	anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: supervisorAuthorityTuple(authorized.Identity), OwnerEpoch: 1, CurrentAuthorityHead: bound.State.HeadDigest, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: handshake.JournalHead, UID: 501, GID: 20, FixedBinary: binary, ControlSocket: socket}
	started, err := NewProcessSupervisorStarted(owner, bound.State.LaunchAuthorizedDigest, control, handshake, anchor, processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: supervisorProcess, Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendSupervisorStarted(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: authorized.Identity, CurrentRunAuthority: run}, started); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("fresh no-bootstrap supervisor start was not rejected: %v", err)
	}
	historicalStarted := appendHistoricalAttemptTransition(t, store, bound.State, AttemptTransition{Kind: AttemptTransitionProcessSupervisorStarted, Identity: authorized.Identity, SupervisorStarted: started})
	process := attemptTestProcess(t)
	legacy := appendHistoricalAttemptTransition(t, store, historicalStarted, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: authorized.Identity, CommandID: "historical-command", ObservedAt: "2026-08-28T00:00:02Z", Process: process, LaunchMaterialsDigest: historicalStarted.LaunchMaterialsDigest, AgentLaunchSpecDigest: historicalStarted.AgentLaunchSpecDigest})
	if legacy.SupervisorStartedDigest == "" || legacy.SupervisorBootstrapDigest != "" || !zeroSupervisorCommandEvidence(legacy.ProcessStartedEvidence) {
		t.Fatalf("unexpected legacy fixture: %+v", legacy)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, found, err := reopened.AttemptState(legacy.Identity)
	if err != nil || !found || state.HeadDigest != legacy.HeadDigest || state.ProcessStartedDigest != legacy.ProcessStartedDigest || !zeroSupervisorCommandEvidence(state.ProcessStartedEvidence) {
		t.Fatalf("historical replay changed: state=%+v found=%v err=%v", state, found, err)
	}
}

func TestSupervisorEvidenceRejectsTypedProcessMismatch(t *testing.T) {
	process := attemptTestProcess(t)
	outcome := SupervisorProcessOutcome{State: SupervisorProcessExecStopped, Process: processsupervisor.ProcessIdentity{PID: process.PID + 1, BirthSeconds: process.BirthSeconds, BirthMicroseconds: process.BirthMicroseconds, SessionID: process.PID + 1, ProcessGroupID: process.PGID + 1}, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:02Z", RuntimeObjectDigest: attemptTestDigest("runtime-mismatch"), WorkingObjectDigest: attemptTestDigest("cwd-mismatch")}
	evidence := testCommandEvidence(t, "session-process-mismatch", processsupervisor.CommandSpawn, attemptTestDigest("authority-mismatch"), outcome)
	if commandEvidenceMatchesProcess(evidence, process) {
		t.Fatal("mismatched process identity unexpectedly matched")
	}
}

func TestSupervisorCollectBindingIsRequiredAndReplaysFromSingleLedger(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	authorized := testOpenedAuthorizedAttempt(t, store, attemptTestIdentity())
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/collect-binding", Device: 91, Inode: 101, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, owner := testPreparedSupervisor(t, store, authorized, "session-collect", control)
	supervised := testStartPreparedSupervisor(t, store, prepared, owner)
	process := attemptTestProcess(t)
	started, err := appendAuthorizedAttempt(t, store, supervised.Revision, supervised.HeadDigest, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: supervised.Identity, CommandID: "command-1", ObservedAt: "2026-08-28T00:00:02Z", Process: process})
	if err != nil {
		t.Fatal(err)
	}

	ingress, err := NewDurableIngress(attemptTestBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	drc, envelope := attemptTestDRC(KindWorkerResult, 1)
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("missing collect binding err=%v", err)
	}
	state := started.State
	state = appendTestSupervisorReconnect(t, store, state)
	rejectedIntent := testSupervisorIntent(state, processsupervisor.CommandCollect, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)})
	state, rejectedDigest := appendTestSupervisorCheckpoint(t, store, state, rejectedIntent, SupervisorProcessOutcome{}, "rejected")
	if rejectedDigest == "" || state.SupervisorCommandSequence != rejectedIntent.Sequence || state.SupervisorPendingIntentDigest != "" {
		t.Fatalf("rejected collect did not close recovery chain: %+v", state)
	}
	collectIntent := testSupervisorIntent(state, processsupervisor.CommandCollect, SupervisorCommandRebuildProjection{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)})
	collectOutcome := state.ProcessStartedEvidence.Outcome
	collectOutcome.State, collectOutcome.MechanicsState = SupervisorTranscriptCollected, "terminal"
	collectOutcome.ObservedAt = "2026-08-28T00:00:03Z"
	collectOutcome.StdoutDigest, collectOutcome.StderrDigest, collectOutcome.TranscriptDigest = attemptTestDigest("stdout"), attemptTestDigest("stderr"), attemptTestDigest("transcript")
	collectOutcome.StdoutBytes, collectOutcome.StderrBytes = 10, 2
	state, collectDigest := appendTestSupervisorCheckpoint(t, store, state, collectIntent, collectOutcome, "ok")
	if _, err := ingress.AdmitWithSupervisorCollectOutcome(context.Background(), drc, envelope, rejectedDigest); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("rejected collect outcome admitted: %v", err)
	}
	fact, err := ingress.AdmitWithSupervisorCollectOutcome(context.Background(), drc, envelope, collectDigest)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	replayed, found, err := reopened.AttemptState(started.State.Identity)
	if err != nil || !found || replayed.CommittedResultFactDigest != fact.FactDigest || replayed.CommittedResultOutcomeDigest != collectDigest || replayed.SupervisorCommandSequence != state.SupervisorCommandSequence {
		t.Fatalf("collect projection mismatch: state=%+v found=%v err=%v", replayed, found, err)
	}
}
