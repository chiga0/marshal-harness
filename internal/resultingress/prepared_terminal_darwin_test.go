//go:build darwin && arm64

package resultingress

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func TestInspectPreparedExecutionPersistsTerminalOutcomeAndReplaysWithoutTransport(t *testing.T) {
	store, _, acquisition, identity, verifier, directory := rebindRecoveryStore(t)
	var rebindCalls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, directory, nil, &rebindCalls)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	state = appendTestBarrier(t, store, state, "terminal-prepared-inspect", TerminalAttemptFailed).State

	var inspectCalls int32
	transport := func(_ context.Context, options processsupervisor.AttachOptions, callback func(AttachedRebindSession) error) error {
		atomic.AddInt32(&inspectCalls, 1)
		return callback(&fakeRebindSession{authority: options.Authority, collect: func(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
			intent, intentErr := NewSupervisorCommandIntent(prepared.Evidence())
			if intentErr != nil {
				t.Fatalf("inspect intent: %v", intentErr)
			}
			started := state.ProcessStartedEvidence.Outcome
			report := processsupervisor.ProcessReport{
				State: "terminal", ObserverIdentity: started.ObserverIdentity, ObservedAt: "2026-08-29T00:00:30Z",
				Process: started.Process, RuntimeObjectDigest: started.RuntimeObjectDigest, WorkingObjectDigest: started.WorkingObjectDigest,
			}
			return verifiedSupervisorOutcome(t, intent, "process-inspected", report)
		}})
	}
	first, err := store.inspectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, directory, "/fixed/marshal", transport)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if first.OutcomeFactDigest == "" || first.Evidence.Command != processsupervisor.CommandInspect || !terminalSupervisorState(first.Evidence.Outcome.State) || atomic.LoadInt32(&inspectCalls) != 1 {
		t.Fatalf("unexpected inspect result=%+v calls=%d", first, inspectCalls)
	}
	second, err := store.inspectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, directory, "/fixed/marshal", transport)
	if err != nil {
		t.Fatalf("inspect replay: %v", err)
	}
	if second != first || atomic.LoadInt32(&inspectCalls) != 1 {
		t.Fatalf("inspect replay changed result or reissued transport: first=%+v second=%+v calls=%d", first, second, inspectCalls)
	}
}

func TestInspectPreparedExecutionReplaysExactPendingIntentAfterResponseLoss(t *testing.T) {
	store, _, acquisition, identity, verifier, directory := rebindRecoveryStore(t)
	var rebindCalls int32
	state, err := doRebind(t, store, verifier, acquisition, identity, directory, nil, &rebindCalls)
	if err != nil {
		t.Fatal(err)
	}
	state = appendTestBarrier(t, store, state, "terminal-pending-inspect", TerminalAttemptFailed).State

	var calls int32
	transport := func(_ context.Context, options processsupervisor.AttachOptions, callback func(AttachedRebindSession) error) error {
		call := atomic.AddInt32(&calls, 1)
		session := &fakeRebindSession{authority: options.Authority, collect: func(prepared processsupervisor.PreparedCommand) processsupervisor.VerifiedCommandOutcome {
			intent, intentErr := NewSupervisorCommandIntent(prepared.Evidence())
			if intentErr != nil {
				t.Fatal(intentErr)
			}
			started := state.ProcessStartedEvidence.Outcome
			report := processsupervisor.ProcessReport{State: "terminal", ObserverIdentity: started.ObserverIdentity, ObservedAt: "2026-08-29T00:00:31Z", Process: started.Process, RuntimeObjectDigest: started.RuntimeObjectDigest, WorkingObjectDigest: started.WorkingObjectDigest}
			return verifiedSupervisorOutcome(t, intent, "process-inspected", report)
		}}
		if call == 1 {
			session.executeErr = errors.New("response lost")
		}
		return callback(session)
	}
	if _, err := store.inspectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, directory, "/fixed/marshal", transport); err == nil {
		t.Fatal("first inspect unexpectedly succeeded")
	}
	pending, found, err := store.AttemptState(identity)
	if err != nil || !found || pending.SupervisorPendingIntent.Command != processsupervisor.CommandInspect {
		t.Fatalf("pending inspect not preserved: found=%v err=%v state=%+v", found, err, pending)
	}
	recovered, err := store.inspectPreparedExecutionWithTransport(context.Background(), verifier, acquisition, identity, directory, "/fixed/marshal", transport)
	if err != nil || recovered.OutcomeFactDigest == "" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("pending inspect replay failed: recovered=%+v err=%v calls=%d", recovered, err, calls)
	}
	after, _, _ := store.AttemptState(identity)
	if after.SupervisorPendingIntentDigest != "" {
		t.Fatal("pending inspect was not closed by exact replay")
	}
}

func TestPreparedTerminalCommandFromIntentReconstructsExactClose(t *testing.T) {
	state := openFreshStartedAttempt(t, mustOpenTestStore(t))
	pre := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
	prepared, err := processsupervisor.PrepareCommand(pre, processsupervisor.CommandOptions{
		Command: processsupervisor.CommandClose, CommandID: "close-terminal-rebuild", Sequence: pre.CommandSequence + 1,
		PreviousCommandDigest: pre.CommandHead, CurrentAuthorityHead: state.HeadDigest, Deadline: time.Date(2026, 8, 29, 0, 1, 0, 0, time.UTC),
	}, processsupervisor.ClosePayload{ProcessTerminalFactDigest: attemptTestDigest("terminal-rebuild"), AllocationTerminatedDigest: attemptTestDigest("allocation-rebuild"), CleanupBindingDigest: attemptTestDigest("cleanup-rebuild")})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := NewSupervisorCommandIntent(prepared.Evidence())
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := preparedTerminalCommandFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Evidence() != prepared.Evidence() {
		t.Fatalf("rebuilt close evidence drifted: got=%+v want=%+v", rebuilt.Evidence(), prepared.Evidence())
	}
}

func mustOpenTestStore(t *testing.T) *DurableStore {
	t.Helper()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
