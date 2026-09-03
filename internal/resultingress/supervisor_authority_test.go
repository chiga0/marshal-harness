package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func TestSupervisorUnionZeroValuesRemainAbsentFromHistoricalFactShape(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(AttemptTransition{Kind: AttemptTransitionOpened, Identity: attemptTestIdentity()})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"owner"`, `"supervisorStarted"`, `"supervisorClosed"`, `"supervisorClosedFactDigest"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("new union zero value changed historical transition shape: %s", raw)
		}
	}
}

func supervisorTestAcquisition(id AttemptIdentity, epoch uint64) ControlOwnerAcquisition {
	pid := 7000 + int(epoch)
	return ControlOwnerAcquisition{
		Scope:            attemptTestOwnerScope(id),
		OwnerEpoch:       epoch,
		OwnerUID:         501,
		OwnerGID:         20,
		OwnerProcess:     processsupervisor.ProcessIdentity{PID: pid, BirthSeconds: 1_700_000_000, BirthMicroseconds: 10, SessionID: pid, ProcessGroupID: pid},
		OwnerBinary:      attemptTestBinary(),
		ObserverIdentity: "darwin-owner-observer/v1",
		ObservedAt:       "2026-08-28T00:00:00Z",
	}
}

func supervisorTestAcquireOwner(t *testing.T, store *DurableStore, id AttemptIdentity) (ControlOwnerState, attemptOwnerVerifier) {
	t.Helper()
	scope := attemptTestOwnerScope(id)
	prior, found, err := store.OpenOwner(scope)
	if err != nil {
		t.Fatal(err)
	}
	epoch, previous := uint64(1), ""
	if found {
		epoch, previous = prior.Acquisition.OwnerEpoch+1, prior.FactDigest
	}
	acquisition := supervisorTestAcquisition(id, epoch)
	verifier := attemptOwnerVerifier{want: acquisition}
	result, err := store.AcquireOwner(context.Background(), verifier, epoch-1, previous, acquisition)
	if err != nil || !result.Appended {
		t.Fatalf("AcquireOwner result=%#v err=%v", result, err)
	}
	return result.State, verifier
}

func supervisorTestBindOwner(t *testing.T, store *DurableStore, state AttemptAuthorityState, owner ControlOwnerState, verifier attemptOwnerVerifier) AttemptAuthorityState {
	t.Helper()
	binding := CurrentOwnerBinding{Scope: owner.Acquisition.Scope, OwnerEpoch: owner.Acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: owner.FactDigest}
	run := attemptTestRunAuthority(state.Identity)
	result, err := store.BindOwnerToAttempt(context.Background(), verifier, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, binding)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func TestCurrentOwnerLineageAcceptsExactPredecessorAndRejectsForgery(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	owner1, _ := supervisorTestAcquireOwner(t, store, id)
	owner2, verifier2 := supervisorTestAcquireOwner(t, store, id)
	digest1, err := ControlOwnerAcquisitionDigest(owner1.Acquisition)
	if err != nil {
		t.Fatal(err)
	}
	reference := ControlOwnerLineageReference{Scope: owner1.Acquisition.Scope, OwnerFactDigest: owner1.FactDigest, OwnerAcquisitionDigest: digest1}
	called := false
	if err := store.WithCurrentOwnerLineage(context.Background(), verifier2, owner2.Acquisition, reference, func(current ControlOwnerState) error {
		called = true
		if current != owner2 {
			t.Fatalf("current=%+v want=%+v", current, owner2)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("exact predecessor called=%t err=%v", called, err)
	}
	for name, mutate := range map[string]func(*ControlOwnerLineageReference){
		"fact": func(value *ControlOwnerLineageReference) { value.OwnerFactDigest = attemptTestDigest("foreign-owner") },
		"acquisition": func(value *ControlOwnerLineageReference) {
			value.OwnerAcquisitionDigest = attemptTestDigest("foreign-acquisition")
		},
		"epoch": func(value *ControlOwnerLineageReference) { value.OwnerEpoch = owner2.Acquisition.OwnerEpoch + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			forged := reference
			mutate(&forged)
			if err := store.WithCurrentOwnerLineage(context.Background(), verifier2, owner2.Acquisition, forged, func(ControlOwnerState) error { return nil }); !errors.Is(err, ErrControlOwnerConflict) {
				t.Fatalf("forged lineage err=%v", err)
			}
		})
	}
}

func supervisorTestOpened(t *testing.T, store *DurableStore, id AttemptIdentity) AttemptAuthorityState {
	t.Helper()
	result, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func TestControlOwnerEpochCrashGapMultiAttemptBindingAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstID := attemptTestIdentity()
	first := supervisorTestOpened(t, store, firstID)
	owner1, verifier1 := supervisorTestAcquireOwner(t, store, firstID)
	ownerReplay, err := store.AcquireOwner(context.Background(), verifier1, 0, "", owner1.Acquisition)
	if err != nil || ownerReplay.Appended || ownerReplay.State != owner1 {
		t.Fatalf("owner exact replay=%#v err=%v", ownerReplay, err)
	}

	// A durable global epoch may survive a crash before any Attempt binding.
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	current, found, err := reopened.OpenOwner(owner1.Acquisition.Scope)
	if err != nil || !found || current != owner1 {
		t.Fatalf("owner after crash found=%v state=%#v err=%v", found, current, err)
	}
	unbound, found, err := reopened.AttemptState(firstID)
	if err != nil || !found || unbound.ControlOwnerBindingDigest != "" {
		t.Fatalf("unbound attempt=%#v found=%v err=%v", unbound, found, err)
	}
	first = supervisorTestBindOwner(t, reopened, first, owner1, verifier1)

	secondID := firstID
	secondID.AttemptID = "attempt-2"
	secondID.AllocationID = "allocation-2"
	secondID.LeaseID = "lease-2"
	secondID.LeaseDigest = attemptTestDigest("lease-2")
	secondID.FencingTokenDigest = attemptTestDigest("fencing-token-2")
	second := supervisorTestOpened(t, reopened, secondID)
	second = supervisorTestBindOwner(t, reopened, second, owner1, verifier1)
	if first.ControlOwnerBindingDigest == "" || second.ControlOwnerBindingDigest == "" || first.HeadDigest == second.HeadDigest || first.Owner != second.Owner {
		t.Fatalf("multi-attempt owner bindings first=%#v second=%#v", first, second)
	}

	owner2, verifier2 := supervisorTestAcquireOwner(t, reopened, firstID)
	if _, err := reopened.AcquireOwner(context.Background(), verifier2, 1, attemptTestDigest("wrong-owner-predecessor"), owner2.Acquisition); !errors.Is(err, ErrControlOwnerConflict) {
		t.Fatalf("owner replay with wrong predecessor err=%v", err)
	}
	if _, err := reopened.BindOwnerToAttempt(context.Background(), verifier1, attemptRunVerifier{want: attemptTestRunAuthority(firstID)}, first.Revision, first.HeadDigest, AttemptAuthorizationRequest{Identity: firstID, CurrentRunAuthority: attemptTestRunAuthority(firstID)}, first.Owner); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("stale epoch rebound err=%v", err)
	}
	first = supervisorTestBindOwner(t, reopened, first, owner2, verifier2)
	if first.Owner.OwnerEpoch != 2 {
		t.Fatalf("current owner epoch=%d", first.Owner.OwnerEpoch)
	}

	secondRestart, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := secondRestart.PendingAttemptStates()
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestControlOwnerConcurrentEpochCASHasSingleWinner(t *testing.T) {
	dir := t.TempDir()
	firstStore, _ := OpenResultIngressStore(dir)
	secondStore, _ := OpenResultIngressStore(dir)
	id := attemptTestIdentity()
	first := supervisorTestAcquisition(id, 1)
	second := first
	second.OwnerProcess.PID++
	second.OwnerProcess.BirthMicroseconds++
	type outcome struct {
		result ControlOwnerAppendResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, candidate := range []struct {
		store       *DurableStore
		acquisition ControlOwnerAcquisition
	}{{firstStore, first}, {secondStore, second}} {
		candidate := candidate
		go func() {
			start.Wait()
			result, err := candidate.store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: candidate.acquisition}, 0, "", candidate.acquisition)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	start.Done()
	winners, conflicts := 0, 0
	for range 2 {
		got := <-outcomes
		switch {
		case got.err == nil && got.result.Appended:
			winners++
		case errors.Is(got.err, ErrControlOwnerConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent outcome=%#v", got)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}

func TestSupervisorStartedRejectsOwnerProcessPhysicalIdentityAlias(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	opened := supervisorTestOpened(t, store, id)
	opened = appendTestAcceptedProvision(t, store, opened)
	authorized, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-owner-alias"})
	if err != nil {
		t.Fatal(err)
	}
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, authorized.State, owner, verifier)
	supervisorProcess := owner.Acquisition.OwnerProcess
	supervisorProcess.SessionID++
	supervisorProcess.ProcessGroupID++
	directory := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/marshal-control-owner-alias", Device: 9, Inode: 901, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	socket := processsupervisor.ControlSocketIdentity{Device: 9, Inode: 902, FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}
	handshake := processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema, ProtocolRevision: processsupervisor.ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: "owner-process-alias", SessionNonceDigest: attemptTestDigest("owner-process-alias-nonce"), OwnerEpoch: owner.Acquisition.OwnerEpoch, CurrentAuthorityHead: bound.HeadDigest, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: attemptTestDigest("owner-process-alias-journal"), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:01Z", SupervisorProcess: supervisorProcess, SupervisorBinary: owner.Acquisition.OwnerBinary, ControlSocket: socket}
	anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: handshake.CurrentAuthorityHead, CommandSequence: handshake.CommandSequence, CommandHead: handshake.CommandHead, JournalSequence: handshake.JournalSequence, JournalHead: handshake.JournalHead, UID: 501, GID: 20, FixedBinary: handshake.SupervisorBinary, ControlSocket: socket}
	started, err := NewProcessSupervisorStarted(bound.Owner, bound.LaunchAuthorizedDigest, directory, handshake, anchor, processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: supervisorProcess, Binary: handshake.SupervisorBinary})
	if err != nil {
		t.Fatal(err)
	}
	run := attemptTestRunAuthority(id)
	if _, err := store.AppendSupervisorStarted(context.Background(), verifier, attemptRunVerifier{want: run}, bound.Revision, bound.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, started); !errors.Is(err, ErrAttemptAuthorityConflict) {
		t.Fatalf("same PID/birth with changed session/PGID err=%v", err)
	}
	current, found, err := store.AttemptState(id)
	if err != nil || !found || current.Revision != bound.Revision || current.HeadDigest != bound.HeadDigest || current.SupervisorStartedDigest != "" {
		t.Fatalf("owner process alias mutated Attempt: state=%#v found=%v err=%v", current, found, err)
	}
}

func TestAuthorityObservedAtRejectsSameSecondBeforeFullBirth(t *testing.T) {
	id := attemptTestIdentity()
	owner := supervisorTestAcquisition(id, 1)
	owner.OwnerProcess.BirthMicroseconds = 500_000
	owner.ObservedAt = time.Unix(owner.OwnerProcess.BirthSeconds, 499_999*int64(time.Microsecond)).UTC().Format(time.RFC3339Nano)
	if err := owner.Validate(); err == nil {
		t.Fatal("control-owner-acquired accepted same-second pre-birth observedAt")
	}

	store, _ := OpenResultIngressStore(t.TempDir())
	state := openFreshStartedAttempt(t, store)
	started := state.SupervisorStarted
	started.Handshake.ObservedAt = time.Unix(started.Handshake.SupervisorProcess.BirthSeconds, (started.Handshake.SupervisorProcess.BirthMicroseconds-1)*int64(time.Microsecond)).UTC().Format(time.RFC3339Nano)
	if err := started.Validate(); err == nil {
		t.Fatal("process-supervisor-started accepted same-second pre-birth observedAt")
	}

	closed := ProcessSupervisorClosed{ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: state.SupervisorStarted.Handshake.SessionID, Owner: state.Owner, SupervisorStartedFactDigest: state.SupervisorStartedDigest, TerminalizationID: "terminal-time-order", CleanupBindingDigest: attemptTestDigest("cleanup-time-order"), ProcessTerminalFactDigest: attemptTestDigest("process-terminal-time-order"), AllocationTerminatedFactDigest: attemptTestDigest("allocation-terminal-time-order"), CloseIntentDigest: attemptTestDigest("close-intent-time-order"), CloseReceiptDigest: attemptTestDigest("close-receipt-time-order"), FinalCommandHead: attemptTestDigest("final-command-time-order"), SupervisorAbsenceObservationDigest: attemptTestDigest("absence-time-order"), SupervisorProcess: state.SupervisorStarted.Handshake.SupervisorProcess, ObserverIdentity: "darwin-supervisor-absence-observer/v1"}
	closed.ObservedAt = time.Unix(closed.SupervisorProcess.BirthSeconds, (closed.SupervisorProcess.BirthMicroseconds-1)*int64(time.Microsecond)).UTC().Format(time.RFC3339Nano)
	if err := closed.Validate(); err == nil {
		t.Fatal("process-supervisor-closed accepted same-second pre-birth observedAt")
	}
}

func TestSupervisorStartedRejectsEachCrossAttemptIdentityABA(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	first := openFreshStartedAttempt(t, store)

	secondID := attemptTestIdentity()
	secondID.AttemptID = "attempt-aba"
	secondID.AllocationID = "allocation-aba"
	secondID.LeaseID = "lease-aba"
	secondID.LeaseDigest = attemptTestDigest("lease-aba")
	secondID.FencingTokenDigest = attemptTestDigest("fencing-aba")
	second := supervisorTestOpened(t, store, secondID)
	second = appendTestAcceptedProvision(t, store, second)
	authorized, err := appendAuthorizedAttempt(t, store, second.Revision, second.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: secondID, LaunchAuthorizationID: "launch-aba"})
	if err != nil {
		t.Fatal(err)
	}
	owner, verifier := supervisorTestAcquireOwner(t, store, secondID)
	second = supervisorTestBindOwner(t, store, authorized.State, owner, verifier)

	run := attemptTestRunAuthority(secondID)
	for _, test := range []struct {
		name   string
		mutate func(*processsupervisor.HandshakeResponse, *processsupervisor.ControlDirectoryIdentity)
	}{
		{name: "session", mutate: func(handshake *processsupervisor.HandshakeResponse, _ *processsupervisor.ControlDirectoryIdentity) {
			handshake.SessionID = first.SupervisorStarted.Handshake.SessionID
		}},
		{name: "process-birth", mutate: func(handshake *processsupervisor.HandshakeResponse, _ *processsupervisor.ControlDirectoryIdentity) {
			handshake.SupervisorProcess = first.SupervisorStarted.Handshake.SupervisorProcess
		}},
		{name: "control-directory", mutate: func(_ *processsupervisor.HandshakeResponse, directory *processsupervisor.ControlDirectoryIdentity) {
			*directory = first.SupervisorStarted.ControlDirectory
		}},
		{name: "control-socket", mutate: func(handshake *processsupervisor.HandshakeResponse, _ *processsupervisor.ControlDirectoryIdentity) {
			handshake.ControlSocket = first.SupervisorStarted.Handshake.ControlSocket
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handshake := first.SupervisorStarted.Handshake
			handshake.SessionID = "unique-" + test.name
			handshake.SessionNonceDigest = attemptTestDigest("unique-nonce-" + test.name)
			handshake.OwnerEpoch = owner.Acquisition.OwnerEpoch
			handshake.CurrentAuthorityHead = second.HeadDigest
			handshake.JournalHead = attemptTestDigest("aba-journal-head-" + test.name)
			handshake.SupervisorProcess = processsupervisor.ProcessIdentity{PID: 9900, BirthSeconds: 1_700_000_002, BirthMicroseconds: int64(len(test.name)), SessionID: 9900, ProcessGroupID: 9900}
			handshake.ControlSocket = processsupervisor.ControlSocketIdentity{Device: 3, Inode: uint64(300 + len(test.name)), FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}
			controlDirectory := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/marshal-control-unique-" + test.name, Device: 3, Inode: uint64(400 + len(test.name)), FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
			test.mutate(&handshake, &controlDirectory)
			anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: supervisorAuthorityTuple(secondID), OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: handshake.CurrentAuthorityHead, CommandSequence: handshake.CommandSequence, CommandHead: handshake.CommandHead, JournalSequence: handshake.JournalSequence, JournalHead: handshake.JournalHead, UID: 501, GID: 20, FixedBinary: handshake.SupervisorBinary, ControlSocket: handshake.ControlSocket, ControlFiles: handshake.ControlFiles}
			started, err := NewProcessSupervisorStarted(second.Owner, second.LaunchAuthorizedDigest, controlDirectory, handshake, anchor, processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: handshake.SupervisorProcess, Binary: handshake.SupervisorBinary})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendSupervisorStarted(context.Background(), verifier, attemptRunVerifier{want: run}, second.Revision, second.HeadDigest, AttemptAuthorizationRequest{Identity: secondID, CurrentRunAuthority: run}, started); !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("cross-attempt %s ABA err=%v", test.name, err)
			}
		})
	}
	current, _, _ := store.AttemptState(secondID)
	if current.SupervisorStartedDigest != "" {
		t.Fatalf("ABA appended supervisor fact: %#v", current)
	}
}

func TestSupervisorClosedPredecessorAndCleanupBinding(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	started := openFreshStartedAttempt(t, store)
	barrier := appendTestBarrier(t, store, started, "terminal-supervisor-close", TerminalAttemptFailed).State
	run := attemptTestRunAuthority(started.Identity)
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupReconcile}
	terminal, _, err := appendTestProcessTerminal(t, store, barrier, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("supervisor-close-process-absent")})
	if err != nil {
		t.Fatal(err)
	}
	terminated, receipt := appendTestAcceptedTerminate(t, store, terminal.State)
	request.Operation = CleanupTerminate
	allocation, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminated.Revision, terminated.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: receipt})
	if err != nil {
		t.Fatal(err)
	}
	request.Operation = CleanupReconcile
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, allocation.State.Revision, allocation.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID}); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("cleanup without supervisor closed err=%v", err)
	}
	closed := appendTestSupervisorClosed(t, store, allocation.State, request)
	owner, _, _ := store.OpenOwner(closed.Owner.Scope)
	replay, err := store.AppendSupervisorClosed(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, allocation.State.Revision, allocation.State.HeadDigest, request, closed.SupervisorClosed, closed.SupervisorClosedOutcomeDigest)
	if err != nil || replay.Appended || replay.TransitionDigest != closed.SupervisorClosedDigest {
		t.Fatalf("closed replay=%#v err=%v", replay, err)
	}
	wrong := AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, SupervisorClosedFactDigest: attemptTestDigest("wrong-closed")}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, closed.Revision, closed.HeadDigest, request, wrong); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("cleanup wrong closed digest err=%v", err)
	}
	completed, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, closed.Revision, closed.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, SupervisorClosedFactDigest: closed.SupervisorClosedDigest})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.PendingAttemptStates()
	if err != nil || len(pending) != 1 || pending[0].HeadDigest != completed.State.HeadDigest || pending[0].SupervisorClosedDigest == "" || pending[0].CleanupCompletedDigest == "" {
		t.Fatalf("restarted pending=%#v err=%v", pending, err)
	}
}

func TestProcessStartedRequiresSupervisorOnGovernedAttempt(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	opened := supervisorTestOpened(t, store, id)
	opened = appendTestAcceptedProvision(t, store, opened)
	authorized, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-without-supervisor"})
	if err != nil {
		t.Fatal(err)
	}
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, authorized.State, owner, verifier)
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "forged-process-start", ObservedAt: "2026-08-29T00:00:00Z", Process: attemptTestProcess(t)}
	closure := attemptTestClosure(t)
	transition.LaunchMaterialsDigest, transition.AgentLaunchSpecDigest = closure.LaunchMaterialsDigest, closure.AgentLaunchSpecDigest
	run := attemptTestRunAuthority(id)
	if _, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run}, bound.Revision, bound.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("owner-governed process-start through Run-only port err=%v", err)
	}
	if _, err := appendAuthorizedAttempt(t, store, bound.Revision, bound.HeadDigest, transition); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("governed process-start without supervisor err=%v", err)
	}
	if state, _, _ := store.AttemptState(id); state.ProcessStartedDigest != "" {
		t.Fatalf("forged process-start appended: %s", fmt.Sprint(state))
	}
}

func TestProcessStartedRequiresCurrentAttemptOwnerBinding(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	opened := supervisorTestOpened(t, store, id)
	opened = appendTestAcceptedProvision(t, store, opened)
	authorized, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-owner-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	supervised := appendTestSupervisorStarted(t, store, authorized.State)
	owner2, verifier2 := supervisorTestAcquireOwner(t, store, id)
	binding2 := CurrentOwnerBinding{Scope: owner2.Acquisition.Scope, OwnerEpoch: owner2.Acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: owner2.FactDigest}
	closure := attemptTestClosure(t)
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "owner-recovery-process", ObservedAt: "2026-08-28T00:00:02Z", Process: attemptTestProcess(t), LaunchMaterialsDigest: closure.LaunchMaterialsDigest, AgentLaunchSpecDigest: closure.AgentLaunchSpecDigest}
	run := attemptTestRunAuthority(id)
	request := AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}
	if _, err := store.AppendProcessStarted(context.Background(), verifier2, attemptRunVerifier{want: run}, supervised.Revision, supervised.HeadDigest, request, binding2, transition); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("new global owner without Attempt binding appended process-started: %v", err)
	}
	rebound := supervisorTestBindOwner(t, store, supervised, owner2, verifier2)
	if _, err := store.AppendProcessStarted(context.Background(), verifier2, attemptRunVerifier{want: run}, rebound.Revision, rebound.HeadDigest, request, binding2, transition); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("rebound owner without a matching supervisor recovery chain err=%v", err)
	}
}
