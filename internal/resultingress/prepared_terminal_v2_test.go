//go:build darwin && arm64

package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// Extends the same durable bootstrap/start/rebind/Collect chain. The kernel
// peer and absence remain explicit substitutes; this is not a live Pi gate.
func testLauncherV2Terminal(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState, owner ControlOwnerState, verifier attemptOwnerVerifier, directory *os.File, report processsupervisor.ProcessReport) {
	testLauncherV2TerminalCommand(t, fixture, state, owner, verifier, directory, report, processsupervisor.CommandInspect)
}

func testLauncherV2TerminalCommand(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState, owner ControlOwnerState, verifier attemptOwnerVerifier, directory *os.File, report processsupervisor.ProcessReport, command processsupervisor.CommandName) {
	t.Helper()
	store := fixture.store
	if command == processsupervisor.CommandTerminate {
		// Carry an actual durable operator stop through the same v2 signal,
		// lost reply, Close/absence, cleanup and cold-replay chain. A generic
		// failed-attempt barrier does not exercise the cancellation contract.
		intent, err := SealAttemptStopIntent(state.Identity, AttemptStopIntent{
			RequestID: "cancel-v2-terminal-chain", ExpectedSequence: 3, ExpectedAuthorityHead: attemptTestDigest("current-run"),
			OperatorUID: owner.Acquisition.OwnerUID, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Category: StopOperatorRequest,
		})
		if err != nil {
			t.Fatal(err)
		}
		stopped, err := appendStopForTest(store, state, intent)
		if err != nil {
			t.Fatalf("v2 operator stop barrier: %v", err)
		}
		state = stopped.State
	} else {
		state = appendTestBarrier(t, store, state, "v2-terminal-chain", TerminalAttemptFailed).State
	}
	var inspected, collected, closedOutcome processsupervisor.VerifiedCommandOutcomeV2
	inspectCalls, closeCalls, transportCalls := 0, 0, 0
	collectCalls := 0
	collectedReport := report
	if command == processsupervisor.CommandTerminate {
		collectedReport.StdoutDigest, collectedReport.StderrDigest = canonical.DigestBytes(nil), canonical.DigestBytes(nil)
		observedAt, err := time.Parse(time.RFC3339Nano, report.ObservedAt)
		if err != nil {
			t.Fatal(err)
		}
		collectedReport.ObservedAt = observedAt.Add(time.Millisecond).Format(time.RFC3339Nano)
	}
	assertIntent := func(p processsupervisor.PreparedCommandV2) SupervisorCommandIntent {
		t.Helper()
		intent, err := NewSupervisorCommandIntentV2(p.Evidence())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(store.ledgerPath())
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		var fact supervisorCommandFact
		if json.Unmarshal(lines[len(lines)-1], &fact) != nil || fact.Intent != intent || fact.ProtocolRevision != p.Evidence().PreCommand.Generation.CommandRecoveryRevision {
			t.Fatal("terminal command sent before exact intent")
		}
		return intent
	}
	transport := func(ctx context.Context, o processsupervisor.AttachOptionsV2, fn func(attachedContinuationV2) error) error {
		transportCalls++
		return o.OwnerVerifier.WithCurrentAttachOwnerV2(ctx, o.Authority, func() error {
			executeTerminal := func(p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
				if p.Evidence().Command != command {
					t.Fatal("terminal method selected a different command")
				}
				inspectCalls++
				intent := assertIntent(p)
				var err error
				inspected, err = verifiedSupervisorOutcomeV2(testCommandOutcomeV2(t, intent, &report, nil))
				if err != nil {
					t.Fatal(err)
				}
				return processsupervisor.VerifiedCommandOutcomeV2{}, processsupervisor.ErrIntervention
			}
			return fn(fakeContinuationV2{observation: testRebindObservationV2(t, o.Authority), inspect: executeTerminal, terminate: executeTerminal, execute: func(p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
				collectCalls++
				if command != processsupervisor.CommandTerminate || p.Evidence().Command != processsupervisor.CommandCollect || collectCalls != 1 {
					t.Fatal("cleanup duplicated or misrouted Collect")
				}
				intent := assertIntent(p)
				var err error
				collected, err = verifiedSupervisorOutcomeV2(testCommandOutcomeV2(t, intent, &collectedReport, nil))
				if err != nil {
					t.Fatal(err)
				}
				// Freeze the receipt, lose the reply, then recover before Close.
				return processsupervisor.VerifiedCommandOutcomeV2{}, processsupervisor.ErrIntervention
			}, close: func(p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
				if command == processsupervisor.CommandTerminate && collectCalls != 1 {
					t.Fatal("real mechanics requires transcript Collect before Close")
				}
				closeCalls++
				intent := assertIntent(p)
				var err error
				closedOutcome, err = verifiedSupervisorOutcomeV2(testCommandOutcomeV2(t, intent, &collectedReport, nil))
				if err != nil {
					t.Fatal(err)
				}
				return processsupervisor.VerifiedCommandOutcomeV2{}, processsupervisor.ErrIntervention
			}})
		})
	}
	observer := func(_ context.Context, o processsupervisor.PreparedJournalOptionsV2) (processsupervisor.PreparedJournalObservationV2, error) {
		outcome := inspected
		if o.Prepared.Evidence().Command == processsupervisor.CommandCollect {
			outcome = collected
		}
		if o.Prepared.Evidence().Command == processsupervisor.CommandClose {
			outcome = closedOutcome
		}
		if o.Prepared.Evidence() != outcome.Preparation {
			t.Fatal("terminal replay replaced request or deadline")
		}
		return processsupervisor.PreparedJournalObservationV2{Reconciliation: processsupervisor.ReconciliationReceiptCommitted, Outcome: &outcome}, nil
	}
	transaction := func(fn func(*Ingress, AttemptAuthorityState) error) error {
		return withCurrentOwnerLock(context.Background(), verifier, owner.Acquisition, func() error {
			projection := newAuthorityProjection()
			return store.transact(projection, func() error {
				key, err := state.Identity.Key()
				if err != nil {
					return err
				}
				return fn(projection, projection.attempts[key])
			})
		})
	}
	var inspection PreparedExecutionTerminalObservation
	beforeAdmission, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AttemptAuthorityState){
		func(s *AttemptAuthorityState) { s.BarrierDigest = "" },
		func(s *AttemptAuthorityState) { s.SupervisorInterventionDigest = attemptTestDigest("intervention") },
		func(s *AttemptAuthorityState) { s.SupervisorBoundAuthorityHead = attemptTestDigest("stale-owner") },
	} {
		invalid := state
		mutate(&invalid)
		if _, err := store.observeTerminalPreparedExecutionV2Locked(context.Background(), nil, invalid, owner, state.Identity, directory, owner.Acquisition.OwnerBinary.CanonicalPath, transport, observer, command); err == nil {
			t.Fatal("invalid terminal authority reached transport")
		}
	}
	afterAdmission, err := os.ReadFile(store.ledgerPath())
	if err != nil || !bytes.Equal(beforeAdmission, afterAdmission) || transportCalls != 0 {
		t.Fatal("terminal admission failure changed ledger or borrowed transport")
	}
	inspect := func() error {
		return transaction(func(p *Ingress, current AttemptAuthorityState) error {
			var err error
			inspection, err = store.observeTerminalPreparedExecutionV2Locked(context.Background(), p, current, owner, state.Identity, directory, owner.Acquisition.OwnerBinary.CanonicalPath, transport, observer, command)
			return err
		})
	}
	if err := inspect(); !errors.Is(err, processsupervisor.ErrIntervention) || inspectCalls != 1 {
		t.Fatalf("inspect response loss: %v", err)
	}
	if err := inspect(); err != nil || inspectCalls != 1 || inspection.OutcomeFactDigest == "" {
		t.Fatalf("inspect exact receipt recovery: %v", err)
	}
	beforeReplay, _ := os.ReadFile(store.ledgerPath())
	if err := inspect(); err != nil || inspectCalls != 1 {
		t.Fatalf("terminal idempotent repeat: %v", err)
	}
	afterReplay, _ := os.ReadFile(store.ledgerPath())
	if !bytes.Equal(beforeReplay, afterReplay) {
		t.Fatal("terminal idempotent repeat appended facts")
	}
	current, found, err := store.AttemptState(state.Identity)
	if err != nil || !found {
		t.Fatal(err)
	}
	run := attemptTestRunAuthority(state.Identity)
	request := CleanupAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run, TerminalizationID: current.TerminalizationID, TerminalGeneration: current.TerminalGeneration, CleanupBindingDigest: current.CleanupBindingDigest, Operation: CleanupInspect}
	kind := ProcessAbsent
	if command == processsupervisor.CommandTerminate {
		// The signal effect is already durably observed. Recording the process
		// fact is reconciliation, not authorization to terminate an allocation.
		kind, request.Operation = ProcessTerminated, CleanupReconcile
	}
	if command == processsupervisor.CommandTerminate {
		wrong := request
		wrong.Operation = CleanupTerminate
		before, readErr := os.ReadFile(store.ledgerPath())
		if readErr != nil {
			t.Fatal(readErr)
		}
		_, rejected := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, wrong,
			AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: state.Identity, TerminalizationID: current.TerminalizationID, ProcessTerminalKind: kind, ObservationDigest: inspection.Evidence.ObservationDigest, SupervisorOutcomeFactDigest: inspection.OutcomeFactDigest})
		after, readErr := os.ReadFile(store.ledgerPath())
		if !errors.Is(rejected, ErrCleanupUnauthorized) || readErr != nil || !bytes.Equal(before, after) {
			t.Fatal("allocation terminate authorization recorded a process fact")
		}
	}
	terminal, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, request,
		AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: state.Identity, TerminalizationID: current.TerminalizationID, ProcessTerminalKind: kind, ObservationDigest: inspection.Evidence.ObservationDigest, SupervisorOutcomeFactDigest: inspection.OutcomeFactDigest})
	if err != nil {
		t.Fatalf("v2 process terminal: %v", err)
	}
	terminated, receipt := appendTestAcceptedTerminate(t, store, terminal.State)
	request.Operation = CleanupTerminate
	allocation, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminated.Revision, terminated.HeadDigest, request,
		AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: state.Identity, TerminalizationID: current.TerminalizationID, ReceiptDigest: receipt})
	if err != nil {
		t.Fatalf("v2 allocation terminal: %v", err)
	}
	state = allocation.State
	if command == processsupervisor.CommandTerminate {
		if !stoppedTranscriptCollectible(state) {
			t.Fatal("sealed stopped child cannot preserve cleanup transcript")
		}
		for name, mutate := range map[string]func(*AttemptAuthorityState){
			"unsealed-stop":     func(s *AttemptAuthorityState) { s.StopIntent.IntentDigest = attemptTestDigest("forged") },
			"no-barrier":        func(s *AttemptAuthorityState) { s.BarrierDigest = "" },
			"open-admission":    func(s *AttemptAuthorityState) { s.AdmissionClosed = false },
			"wrong-eligibility": func(s *AttemptAuthorityState) { s.EligibilityTerminal = EligibilityTerminal{} },
			"live-process":      func(s *AttemptAuthorityState) { s.ProcessTerminalDigest = "" },
			"live-allocation":   func(s *AttemptAuthorityState) { s.AllocationTerminalDigest = "" },
			"accepted-result":   func(s *AttemptAuthorityState) { s.CommittedResultFactDigest = attemptTestDigest("result") },
			"closed":            func(s *AttemptAuthorityState) { s.SupervisorClosedDigest = attemptTestDigest("closed") },
			"intervention":      func(s *AttemptAuthorityState) { s.SupervisorInterventionDigest = attemptTestDigest("intervention") },
			"legacy":            func(s *AttemptAuthorityState) { s.SupervisorStarted.V2 = SupervisorStartedV2{} },
		} {
			bad := state
			mutate(&bad)
			if stoppedTranscriptCollectible(bad) {
				t.Fatalf("cleanup collect admitted %s", name)
			}
		}
	}
	absent := false
	recoverClose := func(_ context.Context, o processsupervisor.CommittedCloseRecoveryOptionsV2) (processsupervisor.CommittedCloseRecoveryEvidenceV2, error) {
		if !absent {
			return processsupervisor.CommittedCloseRecoveryEvidenceV2{}, processsupervisor.ErrIntervention
		}
		if o.PreparedClose.Evidence() != closedOutcome.Preparation || o.ExpectedSupervisor != state.SupervisorStarted.V2.Handshake.SupervisorProcess {
			t.Fatal("close recovery wrong expected command/process")
		}
		b := closedOutcome.PostCommand.Binding
		a := owner.Acquisition
		recovered := processsupervisor.CommittedCloseRecoveryEvidenceV2{Outcome: closedOutcome, Absence: processsupervisor.SupervisorAbsenceEvidence{SchemaVersion: processsupervisor.SupervisorAbsenceSchema,
			State: processsupervisor.SupervisorExpectedAbsent, Expected: o.ExpectedSupervisor, Observer: processsupervisor.CoreIdentity{UID: a.OwnerUID, GID: a.OwnerGID, Process: a.OwnerProcess, Binary: a.OwnerBinary},
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ControlFiles: b.ControlFiles, FinalJournalSequence: b.JournalSequence, FinalJournalHead: b.JournalHead}}
		if recovered.Validate() != nil {
			t.Fatal("invalid independent absence fixture")
		}
		return recovered, nil
	}
	var closeEvidence PreparedExecutionClose
	reader := func(o processsupervisor.CollectedTranscriptReadOptionsV2) (processsupervisor.CollectedTranscript, error) {
		if !reflect.DeepEqual(o.Outcome, collected) || o.ControlDirectory != directory {
			t.Fatal("cleanup transcript read not bound to collected receipt")
		}
		return processsupervisor.CollectedTranscript{Report: collectedReport, TranscriptDigest: collected.TranscriptDigest}, nil
	}
	closeAttempt := func() error {
		return transaction(func(p *Ingress, current AttemptAuthorityState) error {
			var err error
			current, err = store.collectStoppedBeforeCloseV2Locked(context.Background(), p, current, owner, state.Identity, directory, owner.Acquisition.OwnerBinary.CanonicalPath, transport, reader, observer)
			if err != nil {
				return err
			}
			closeEvidence, err = store.closePreparedExecutionV2Locked(context.Background(), p, current, owner, state.Identity, directory, owner.Acquisition.OwnerBinary.CanonicalPath, transport, recoverClose, observer)
			return err
		})
	}
	wantTransports := 3
	if command == processsupervisor.CommandTerminate {
		wantTransports += 2
		if err := closeAttempt(); !errors.Is(err, processsupervisor.ErrIntervention) || collectCalls != 1 || closeCalls != 0 {
			t.Fatalf("lost cleanup Collect reply advanced Close: %v", err)
		}
	}
	if err := closeAttempt(); !errors.Is(err, processsupervisor.ErrIntervention) || closeCalls != 1 {
		t.Fatalf("close without absence: %v", err)
	}
	before, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := closeAttempt(); !errors.Is(err, processsupervisor.ErrIntervention) || closeCalls != 1 {
		t.Fatal("committed close repeated without absence")
	}
	after, err := os.ReadFile(store.ledgerPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("absence wait modified ledger")
	}
	absent = true
	if err := closeAttempt(); err != nil || closeCalls != 1 || transportCalls != wantTransports || closeEvidence.OutcomeFactDigest == "" || closeEvidence.RecoveryV2 == nil {
		t.Fatalf("committed close recovery: %v", err)
	}
	current, found, err = store.AttemptState(state.Identity)
	if err != nil || !found {
		t.Fatal(err)
	}
	if command == processsupervisor.CommandTerminate && (current.CommittedResultFactDigest != "" || !current.AdmissionClosed || current.StopIntent != state.StopIntent) {
		t.Fatal("cleanup transcript was promoted to business result or changed stop")
	}
	authority := ProcessSupervisorCloseAuthority{Owner: current.Owner, SupervisorStartedFactDigest: current.SupervisorStartedDigest, TerminalizationID: current.TerminalizationID, CleanupBindingDigest: current.CleanupBindingDigest, ProcessTerminalFactDigest: current.ProcessTerminalDigest, AllocationTerminatedFactDigest: current.AllocationTerminalDigest}
	closed, err := closeEvidence.SupervisorClosed(authority)
	if err != nil {
		t.Fatal(err)
	}
	if command == processsupervisor.CommandTerminate {
		if terminalReportsEquivalent(current.ProcessTerminalEvidence, closeEvidence.Evidence) || !stoppedCloseReportsEquivalent(current, closeEvidence.Evidence) {
			t.Fatal("stop Close must bridge the exact intervening Collect report")
		}
		for name, mutate := range map[string]func(*AttemptAuthorityState){
			"missing-terminal-reference": func(s *AttemptAuthorityState) { s.ProcessTerminalOutcomeDigest = attemptTestDigest("missing") },
			"missing-collect": func(s *AttemptAuthorityState) {
				var kept []SupervisorCommandCheckpoint
				for _, c := range s.SupervisorCommandCheckpoints {
					if c.Evidence.Command != processsupervisor.CommandCollect {
						kept = append(kept, c)
					}
				}
				s.SupervisorCommandCheckpoints = kept
			},
			"changed-process":  func(s *AttemptAuthorityState) { s.ProcessTerminalEvidence.Outcome.Process.PID++ },
			"changed-exit":     func(s *AttemptAuthorityState) { s.ProcessTerminalEvidence.Outcome.ExitCode++ },
			"changed-signal":   func(s *AttemptAuthorityState) { s.ProcessTerminalEvidence.Outcome.Signal = "SIGKILL" },
			"changed-observer": func(s *AttemptAuthorityState) { s.ProcessTerminalEvidence.Outcome.ObserverIdentity = "other" },
		} {
			bad := current
			mutate(&bad)
			if stoppedCloseReportsEquivalent(bad, closeEvidence.Evidence) {
				t.Fatalf("stop Close admitted %s", name)
			}
		}
		wrongReport := closeEvidence.Evidence
		wrongReport.Outcome.StdoutDigest = attemptTestDigest("uncollected")
		if stoppedCloseReportsEquivalent(current, wrongReport) {
			t.Fatal("Close invented transcript output")
		}
	}
	request.Operation = CleanupReconcile
	wrong := closed
	wrong.AuthenticatedSupervisorAbsence.FinalJournalHead = attemptTestDigest("wrong-final-head")
	wrong.SupervisorAbsenceObservationDigest, _ = canonicalDigest(wrong.AuthenticatedSupervisorAbsence)
	if _, err := store.AppendSupervisorClosed(context.Background(), verifier, attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, request, wrong, closeEvidence.OutcomeFactDigest); err == nil {
		t.Fatal("forged absence checkpoint entered business lifecycle")
	}
	closedState, err := store.AppendSupervisorClosed(context.Background(), verifier, attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, request, closed, closeEvidence.OutcomeFactDigest)
	if err != nil {
		t.Fatalf("append v2 supervisor closed: %v", err)
	}
	completed, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, closedState.State.Revision, closedState.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: state.Identity, TerminalizationID: state.TerminalizationID, SupervisorClosedFactDigest: closedState.State.SupervisorClosedDigest})
	if err != nil {
		t.Fatal(err)
	}
	released, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, completed.State.Revision, completed.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupReleased, Identity: state.Identity, TerminalizationID: state.TerminalizationID})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cold, found, err := reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(cold, released.State) || cold.CleanupReleasedDigest == "" {
		t.Fatalf("full v2 cleanup cold replay: %v", err)
	}
	if command == processsupervisor.CommandTerminate && (cold.StopIntent != state.StopIntent || cold.CommittedResultFactDigest != "" || !cold.AdmissionClosed || cold.StopIntent.Category != StopOperatorRequest) {
		t.Fatal("v2 cancellation lost its original intent or became admitted completion")
	}
}
