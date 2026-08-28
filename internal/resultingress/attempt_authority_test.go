package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

func attemptTestDigest(seed string) string { return canonical.DigestBytes([]byte(seed)) }

func attemptTestIdentity() AttemptIdentity {
	return AttemptIdentity{
		AuthorityNamespaceID:  authority.AuthorityNamespaceId{TenantNamespace: "tenant-1", ControlPlaneId: "core-1", AuthorityScopeId: "scope-1"},
		AuthorityNamespaceRef: "authority:test", TaskID: "task-1", RunID: "run-1",
		AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1",
		LeaseDigest: attemptTestDigest("lease"), DispatchGeneration: 7,
		FencingTokenDigest: attemptTestDigest("fencing-token"), OrchestratorID: "orchestrator-1",
		RunAuthorityDigest: attemptTestDigest("run-authority"),
	}
}

func attemptTestProcess(t *testing.T) ProcessObservation {
	t.Helper()
	observation, err := SealProcessObservation(ProcessObservation{
		PID: 1234, PGID: 1234, BirthSeconds: 100, BirthMicroseconds: 22,
		WorkingDirectory: "/tmp/work", WorkingDirectoryDevice: 1, WorkingDirectoryInode: 2,
		WorkingDirectoryType: 4, WorkingDirectoryOwner: 501, WorkingDirectoryMode: 0755,
		ExecutablePath: "/fixed/marshal", ExecutableDevice: 1, ExecutableInode: 3,
		ExecutableSize: 99, ExecutableType: 8, ExecutableOwner: 501,
		ExecutableMode: 0755, ExecutableLinkCount: 1, ExecutableSHA256: attemptTestDigest("executable"),
		ObserverIdentity: "core-darwin-observer/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func openStartedAttempt(t *testing.T, store *ingressDurableStore) AttemptAuthorityState {
	t.Helper()
	id := attemptTestIdentity()
	openedResult, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	authorizedResult, err := store.CompareAndAppend(opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-1"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := authorizedResult.State
	startedResult, err := store.CompareAndAppend(authorized.Revision, authorized.HeadDigest, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "command-1", ObservedAt: "2026-08-28T00:00:00Z", Process: attemptTestProcess(t)})
	if err != nil {
		t.Fatal(err)
	}
	return startedResult.State
}

func attemptTestBinding() LedgerBinding {
	return LedgerBinding{
		LeaseID: "lease-1", Generation: 7, FencingToken: "fencing-token",
		AttemptID: "attempt-1", AllocationID: "allocation-1", Expiry: time.Now().Add(time.Hour),
		RegistrationID: "registration-1", SnapshotDigest: attemptTestDigest("snapshot"), EvidenceDigest: attemptTestDigest("evidence"),
	}
}

func attemptTestDRC(kind EnvelopeKind, sequence uint64) (DRC, ResultEnvelope) {
	digest := attemptTestDigest(string(kind) + "-payload")
	op, _ := kindToOperation(kind)
	return DRC{
		AuthorityNamespaceID: "authority:test", TaskID: "task-1", RunID: "run-1",
		AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1",
		Generation: 7, FencingToken: "fencing-token", CommandID: "command-1",
		IdempotencyKey: string(kind) + "-key", RequestDigest: digest, Nonce: "nonce-1",
		Expiry: time.Now().Add(time.Hour), Operation: op, RegistrationID: "registration-1",
		SnapshotDigest: attemptTestDigest("snapshot"), EvidenceDigest: attemptTestDigest("evidence"),
	}, ResultEnvelope{Kind: kind, ResultDigest: digest, Sequence: sequence}
}

func attemptTestRunAuthority(id AttemptIdentity) RunAuthorityBinding {
	return RunAuthorityBinding{AuthorityNamespaceID: id.AuthorityNamespaceID, RunID: id.RunID, OrchestratorID: id.OrchestratorID, RunAuthorityDigest: id.RunAuthorityDigest}
}

func appendTestBarrier(t *testing.T, store *DurableStore, state AttemptAuthorityState, terminalizationID string, reason TerminalReason) AttemptAppendResult {
	t.Helper()
	run := attemptTestRunAuthority(state.Identity)
	result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, BarrierAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: state.Identity, TerminalizationID: terminalizationID, EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: reason}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttemptAuthorityLaunchCrashProjectionAndReplay(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil || !openedResult.Appended || openedResult.State.LaunchState != LaunchNotAuthorized {
		t.Fatalf("opened = %#v, err=%v", openedResult, err)
	}
	opened := openedResult.State
	authorizedResult, err := store.CompareAndAppend(opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-1"})
	if err != nil || !authorizedResult.Appended || authorizedResult.State.LaunchState != LaunchUncertain || authorizedResult.TransitionDigest != authorizedResult.State.LaunchAuthorizedDigest {
		t.Fatalf("authorized = %#v, err=%v", authorizedResult, err)
	}
	authorized := authorizedResult.State
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := reopened.AttemptState(id)
	if err != nil || !ok || recovered.LaunchState != LaunchUncertain || recovered.HeadDigest != authorized.HeadDigest {
		t.Fatalf("recovered = %#v, ok=%v, err=%v", recovered, ok, err)
	}
	// Exact open is a stable replay and never creates a second authority.
	replay, err := reopened.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil || replay.Appended || replay.State.HeadDigest != authorized.HeadDigest || replay.TransitionDigest != authorized.OpenedDigest {
		t.Fatalf("open replay = %#v, err=%v", replay, err)
	}
}

func TestAttemptAuthorityRejectsStaleRevisionAndHead(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	for name, stale := range map[string]struct {
		revision uint64
		head     string
	}{
		"stale-revision": {revision: 0, head: opened.HeadDigest},
		"stale-head":     {revision: opened.Revision, head: attemptTestDigest("stale-head")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.CompareAndAppend(stale.revision, stale.head, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-stale"})
			if !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("err=%v, want ErrAttemptAuthorityConflict", err)
			}
		})
	}
	current, found, err := store.AttemptState(id)
	if err != nil || !found || current.Revision != opened.Revision || current.HeadDigest != opened.HeadDigest {
		t.Fatalf("stale CAS mutated authority: current=%#v found=%v err=%v", current, found, err)
	}
}

func TestAttemptAuthorityTwoStoreCASCompetition(t *testing.T) {
	dir := t.TempDir()
	first, _ := OpenResultIngressStore(dir)
	second, _ := OpenResultIngressStore(dir)
	openedResult, err := first.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: attemptTestIdentity()})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, candidate := range []struct {
		store *ingressDurableStore
		id    string
	}{{first, "launch-a"}, {second, "launch-b"}} {
		wg.Add(1)
		go func(candidate struct {
			store *ingressDurableStore
			id    string
		}) {
			defer wg.Done()
			_, err := candidate.store.CompareAndAppend(opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: attemptTestIdentity(), LaunchAuthorizationID: candidate.id})
			errs <- err
		}(candidate)
	}
	wg.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrAttemptAuthorityConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestAdmissionAndBarrierShareCASAndAllKindsClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	started := openStartedAttempt(t, store)
	ingress, err := NewDurableIngress(attemptTestBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	drc, envelope := attemptTestDRC(KindWorkerResult, 1)
	admission, err := ingress.Admit(context.Background(), drc, envelope)
	if err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.AttemptState(started.Identity)
	if err != nil || !ok || current.CommittedResultFactDigest != admission.FactDigest {
		t.Fatalf("current = %#v ok=%v err=%v", current, ok, err)
	}
	barrierResult := appendTestBarrier(t, store, current, "terminal-1", TerminalAttemptCompleted)
	barrier := barrierResult.State
	if !barrier.AdmissionClosed || barrier.BarrierAdmissionFactDigest != admission.FactDigest || barrier.TerminalGeneration != 8 {
		t.Fatalf("barrier = %#v", barrier)
	}

	for sequence, kind := range []EnvelopeKind{KindWorkerResult, KindCandidate, KindEvidenceRef, KindCheckpoint, KindHeartbeat, KindReceipt, KindLog, KindAssessment} {
		drc, envelope := attemptTestDRC(kind, uint64(sequence+2))
		drc.IdempotencyKey += "-late"
		if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
			t.Errorf("late %s err=%v, want ErrStaleLease", kind, err)
		}
	}
	quarantine := ingress.Quarantine()
	if len(quarantine) < 8 {
		t.Fatalf("quarantine len=%d, want >=8", len(quarantine))
	}
}

func TestAdmissionBarrierRaceHasOneOrderAndRetryBindsWinner(t *testing.T) {
	dir := t.TempDir()
	admitStore, _ := OpenResultIngressStore(dir)
	barrierStore, _ := OpenResultIngressStore(dir)
	started := openStartedAttempt(t, admitStore)
	ingress, _ := NewDurableIngress(attemptTestBinding(), admitStore)
	drc, envelope := attemptTestDRC(KindWorkerResult, 1)
	start := make(chan struct{})
	var admission AdmissionFact
	var admitErr, barrierErr error
	var racedBarrierResult AttemptAppendResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		admission, admitErr = ingress.Admit(context.Background(), drc, envelope)
	}()
	go func() {
		defer wg.Done()
		<-start
		run := attemptTestRunAuthority(started.Identity)
		racedBarrierResult, barrierErr = barrierStore.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-race", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted}})
	}()
	close(start)
	wg.Wait()
	if admitErr == nil && barrierErr == nil {
		t.Fatal("admission and stale-head barrier both won the same CAS slot")
	}
	if admitErr != nil && barrierErr != nil {
		t.Fatalf("both race sides failed: admit=%v barrier=%v", admitErr, barrierErr)
	}
	current, ok, err := barrierStore.AttemptState(started.Identity)
	if err != nil || !ok {
		t.Fatalf("state ok=%v err=%v", ok, err)
	}
	if barrierErr != nil {
		if !errors.Is(barrierErr, ErrAttemptAuthorityConflict) || admitErr != nil {
			t.Fatalf("admission-first order: admit=%v barrier=%v", admitErr, barrierErr)
		}
		run := attemptTestRunAuthority(started.Identity)
		racedBarrierResult, err = barrierStore.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-race", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted}})
		if err != nil {
			t.Fatal(err)
		}
		racedBarrier := racedBarrierResult.State
		if racedBarrier.BarrierAdmissionFactDigest != admission.FactDigest || !racedBarrier.AdmissionClosed {
			t.Fatalf("retry did not bind admitted winner: %#v", racedBarrier)
		}
	} else {
		racedBarrier := racedBarrierResult.State
		if !errors.Is(admitErr, ErrStaleLease) || !racedBarrier.AdmissionClosed {
			t.Fatalf("barrier-first order: admit=%v barrier=%#v", admitErr, racedBarrier)
		}
	}
}

func TestBarrierFirstClosesAdmissionAndStaleBarrierCASCanRetry(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-first", TerminalAttemptAborted)
	barrier := barrierResult.State
	if !barrier.AdmissionClosed {
		t.Fatalf("barrier = %#v", barrier)
	}
	drc, envelope := attemptTestDRC(KindCheckpoint, 1)
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("late admit err=%v", err)
	}
	run := attemptTestRunAuthority(started.Identity)
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "different", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptFailed}}); !errors.Is(err, ErrAttemptAuthorityConflict) {
		t.Fatalf("stale barrier err=%v", err)
	}
}

func TestBarrierAuthorizationIsHeldForAppendAndExactReplay(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	run := attemptTestRunAuthority(started.Identity)
	transition := AttemptTransition{
		Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity,
		TerminalizationID:   "terminal-authorized",
		EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted},
	}
	if _, err := store.CompareAndAppend(started.Revision, started.HeadDigest, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("generic barrier err=%v", err)
	}
	if _, err := store.CompareAndAppendBarrier(context.Background(), nil, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("nil verifier err=%v", err)
	}
	wrong := run
	wrong.OrchestratorID = "second-orchestrator"
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: wrong}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("wrong orchestrator err=%v", err)
	}
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, skip: true}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("verifier without held callback err=%v", err)
	}
	calls := 0
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, calls: &calls}, started.Revision+1, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrAttemptAuthorityConflict) || calls != 1 {
		t.Fatalf("stale revision err=%v calls=%d", err, calls)
	}
	result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
	if err != nil || !result.Appended {
		t.Fatalf("fresh barrier=%#v err=%v", result, err)
	}
	calls = 0
	replay, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, calls: &calls}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
	if err != nil || replay.Appended || replay.TransitionDigest != result.TransitionDigest || calls != 1 {
		t.Fatalf("replay=%#v calls=%d err=%v", replay, calls, err)
	}
}

func TestBarrierCommitsClosedTerminalEligibilityUnion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal EligibilityTerminal
	}{
		{name: "completed", terminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptFailed}},
		{name: "cancelled", terminal: EligibilityTerminal{Kind: EligibilityTerminalCancelled, CancelReason: EligibilityCancelDeadlineExceeded}},
		{name: "security-revoke", terminal: EligibilityTerminal{Kind: EligibilityTerminalCancelled, CancelReason: EligibilityCancelSecurityCriticalRevoke}},
		{name: "expired", terminal: EligibilityTerminal{Kind: EligibilityTerminalExpired}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := OpenResultIngressStore(t.TempDir())
			started := openStartedAttempt(t, store)
			run := attemptTestRunAuthority(started.Identity)
			transition := AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-" + tc.name, EligibilityTerminal: tc.terminal}
			result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
			if err != nil || result.State.EligibilityTerminal != tc.terminal || !result.State.AdmissionClosed {
				t.Fatalf("barrier=%#v err=%v", result, err)
			}
		})
	}
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	run := attemptTestRunAuthority(started.Identity)
	for _, invalid := range []EligibilityTerminal{
		{},
		{Kind: EligibilityTerminalCompleted},
		{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted, CancelReason: EligibilityCancelDeadlineExceeded},
		{Kind: EligibilityTerminalCancelled, CompletionReason: TerminalAttemptFailed, CancelReason: EligibilityCancelDeadlineExceeded},
		{Kind: EligibilityTerminalExpired, CancelReason: EligibilityCancelDeadlineExceeded},
	} {
		transition := AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "invalid", EligibilityTerminal: invalid}
		if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); err == nil {
			t.Fatalf("invalid terminal union accepted: %#v", invalid)
		}
	}
}

func TestBarrierBindsBusinessResultNotLaterAuxiliaryAdmissions(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	resultDRC, resultEnvelope := attemptTestDRC(KindWorkerResult, 1)
	resultFact, err := ingress.Admit(context.Background(), resultDRC, resultEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var auxiliary []struct {
		drc      DRC
		envelope ResultEnvelope
		fact     AdmissionFact
	}
	for sequence, kind := range []EnvelopeKind{KindLog, KindHeartbeat} {
		drc, envelope := attemptTestDRC(kind, uint64(sequence+2))
		fact, err := ingress.Admit(context.Background(), drc, envelope)
		if err != nil {
			t.Fatal(err)
		}
		auxiliary = append(auxiliary, struct {
			drc      DRC
			envelope ResultEnvelope
			fact     AdmissionFact
		}{drc: drc, envelope: envelope, fact: fact})
	}
	current, ok, err := store.AttemptState(started.Identity)
	if err != nil || !ok || current.CommittedResultFactDigest != resultFact.FactDigest {
		t.Fatalf("current=%#v ok=%v err=%v", current, ok, err)
	}
	barrier := appendTestBarrier(t, store, current, "terminal-business", TerminalAttemptCompleted).State
	if barrier.BarrierAdmissionFactDigest != resultFact.FactDigest {
		t.Fatalf("barrier bound auxiliary admission: %#v", barrier)
	}
	replay, err := ingress.Admit(context.Background(), resultDRC, resultEnvelope)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("business result replay=%#v err=%v", replay, err)
	}
	for _, admission := range auxiliary {
		if _, err := ingress.Admit(context.Background(), admission.drc, admission.envelope); !errors.Is(err, ErrStaleLease) {
			t.Fatalf("auxiliary replay after barrier err=%v", err)
		}
	}
}

func TestBarrierWithoutBusinessResultClosesEmptySlot(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	drc, envelope := attemptTestDRC(KindLog, 1)
	if _, err := ingress.Admit(context.Background(), drc, envelope); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.AttemptState(started.Identity)
	barrier := appendTestBarrier(t, store, current, "terminal-empty", TerminalAttemptFailed).State
	if !barrier.AdmissionClosed || barrier.BarrierAdmissionFactDigest != "" || barrier.BarrierAdmissionSequence != 0 {
		t.Fatalf("barrier=%#v", barrier)
	}
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("auxiliary replay after empty barrier err=%v", err)
	}
}

type attemptRunVerifier struct {
	want  RunAuthorityBinding
	err   error
	calls *int
	skip  bool
}

func (v attemptRunVerifier) WithCurrentRunAuthority(_ context.Context, got RunAuthorityBinding, fn func() error) error {
	if v.calls != nil {
		*v.calls = *v.calls + 1
	}
	if v.err != nil {
		return v.err
	}
	if got != v.want {
		return errors.New("wrong current Run authority")
	}
	if v.skip {
		return nil
	}
	return fn()
}

func TestCleanupAuthorizationRejectsWrongTupleBindingOrchestratorAndRelease(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-1", TerminalAttemptFailed)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: started.Identity.AuthorityNamespaceID, RunID: started.Identity.RunID, OrchestratorID: started.Identity.OrchestratorID, RunAuthorityDigest: started.Identity.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupTerminate}
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*CleanupAuthorizationRequest){
		func(r *CleanupAuthorizationRequest) { r.Identity.LeaseID = "wrong" },
		func(r *CleanupAuthorizationRequest) { r.CleanupBindingDigest = attemptTestDigest("wrong") },
		func(r *CleanupAuthorizationRequest) { r.CurrentRunAuthority.OrchestratorID = "other" },
		func(r *CleanupAuthorizationRequest) { r.TerminalGeneration++ },
	}
	for _, mutate := range mutations {
		forged := request
		mutate(&forged)
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, forged); !errors.Is(err, ErrCleanupUnauthorized) && !errors.Is(err, ErrAttemptAuthorityUnknown) {
			t.Errorf("forged request err=%v", err)
		}
	}
	terminalTransition := AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("absent")}
	terminalResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, terminalTransition)
	if err != nil {
		t.Fatal(err)
	}
	terminal := terminalResult.State
	verifierCalls := 0
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, err: errors.New("run authority drifted"), calls: &verifierCalls}, barrier.Revision, barrier.HeadDigest, request, terminalTransition); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("exact cleanup replay with authority drift err=%v", err)
	}
	if verifierCalls != 1 {
		t.Fatalf("exact cleanup replay verifier calls=%d, want 1", verifierCalls)
	}
	verifierCalls = 0
	replayedTerminal, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, calls: &verifierCalls}, barrier.Revision, barrier.HeadDigest, request, terminalTransition)
	if err != nil || replayedTerminal.Appended || replayedTerminal.State.HeadDigest != terminal.HeadDigest || replayedTerminal.TransitionDigest != terminal.ProcessTerminalDigest || verifierCalls != 1 {
		t.Fatalf("exact cleanup replay=%#v calls=%d err=%v", replayedTerminal, verifierCalls, err)
	}
	allocationResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminal.Revision, terminal.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: attemptTestDigest("allocation")})
	if err != nil {
		t.Fatal(err)
	}
	allocation := allocationResult.State
	cleanedResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, allocation.Revision, allocation.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID})
	if err != nil {
		t.Fatal(err)
	}
	cleaned := cleanedResult.State
	releasedResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, cleaned.Revision, cleaned.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupReleased, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID})
	if err != nil || releasedResult.State.CleanupReleasedDigest == "" {
		t.Fatalf("released=%#v err=%v", releasedResult, err)
	}
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released binding err=%v", err)
	}
	releaseTransition := AttemptTransition{Kind: AttemptTransitionCleanupReleased, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID}
	verifierCalls = 0
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, err: errors.New("run authority drifted"), calls: &verifierCalls}, cleaned.Revision, cleaned.HeadDigest, request, releaseTransition); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released replay with authority drift err=%v", err)
	}
	if verifierCalls != 1 {
		t.Fatalf("released replay verifier calls=%d, want 1", verifierCalls)
	}
	verifierCalls = 0
	releasedReplay, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, calls: &verifierCalls}, cleaned.Revision, cleaned.HeadDigest, request, releaseTransition)
	if err != nil || releasedReplay.Appended || releasedReplay.TransitionDigest != releasedResult.TransitionDigest {
		t.Fatalf("released exact replay=%#v err=%v", releasedReplay, err)
	}
	if verifierCalls != 1 {
		t.Fatalf("released replay verifier calls=%d, want 1", verifierCalls)
	}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, releasedResult.State.Revision, releasedResult.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID}); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released binding authorized cleanup re-effect: %v", err)
	}
	forgedRelease := releaseTransition
	forgedRelease.TerminalizationID = "different"
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, cleaned.Revision, cleaned.HeadDigest, request, forgedRelease); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("different release replay err=%v", err)
	}
	pending, err := store.PendingAttemptStates()
	if err != nil || len(pending) != 0 {
		t.Fatalf("released attempt remained pending: %#v err=%v", pending, err)
	}
}

func TestLaunchUncertainCleanupOnlyAllowsInspectAndReconcile(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	authorizedResult, err := store.CompareAndAppend(opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-uncertain"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := authorizedResult.State
	barrierResult := appendTestBarrier(t, store, authorized, "terminal-uncertain", TerminalOrphanReconciled)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: id.AuthorityNamespaceID, RunID: id.RunID, OrchestratorID: id.OrchestratorID, RunAuthorityDigest: id.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: id, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest}
	for _, operation := range []CleanupOperation{CleanupInspect, CleanupReconcile} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); err != nil {
			t.Fatalf("operation %q rejected: %v", operation, err)
		}
	}
	for _, operation := range []CleanupOperation{CleanupSignal, CleanupTerminate} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
			t.Fatalf("operation %q err=%v, want ErrCleanupUnauthorized", operation, err)
		}
	}
	request.Operation = CleanupReconcile
	absentResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: id, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("launch-uncertain-absent")})
	if err != nil || absentResult.State.ProcessTerminalKind != ProcessAbsent {
		t.Fatalf("reconciled absence=%#v err=%v", absentResult, err)
	}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, absentResult.State.Revision, absentResult.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: id, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: attemptTestDigest("launch-uncertain-allocation")}); err != nil {
		t.Fatalf("launch-uncertain absence did not unblock allocation cleanup: %v", err)
	}
}

func TestProcessIdentityConflictPermanentlyBlocksKillAndCompletion(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := openStartedAttempt(t, store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-conflict", TerminalOrphanReconciled)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: started.Identity.AuthorityNamespaceID, RunID: started.Identity.RunID, OrchestratorID: started.Identity.OrchestratorID, RunAuthorityDigest: started.Identity.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupInspect}
	conflictResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessIdentityConflict, ObservationDigest: attemptTestDigest("identity-conflict")})
	if err != nil {
		t.Fatal(err)
	}
	conflict := conflictResult.State
	for _, operation := range []CleanupOperation{CleanupSignal, CleanupTerminate} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
			t.Fatalf("identity conflict operation %q err=%v", operation, err)
		}
	}
	request.Operation = CleanupReconcile
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, conflict.Revision, conflict.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: attemptTestDigest("forged-allocation-terminal")}); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("identity conflict advanced to allocation terminal: %v", err)
	}
}

func TestAttemptStateEnumerationIsDeterministicAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	first := attemptTestIdentity()
	second := attemptTestIdentity()
	second.AttemptID, second.AllocationID, second.LeaseID = "attempt-2", "allocation-2", "lease-2"
	second.LeaseDigest = attemptTestDigest("lease-2")
	for _, identity := range []AttemptIdentity{second, first} {
		if _, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: identity}); err != nil {
			t.Fatal(err)
		}
	}
	states, err := store.AttemptStates()
	if err != nil || len(states) != 2 {
		t.Fatalf("states=%#v err=%v", states, err)
	}
	firstKey, _ := first.Key()
	secondKey, _ := second.Key()
	wantFirst := first
	if secondKey < firstKey {
		wantFirst = second
	}
	if states[0].Identity != wantFirst {
		t.Fatalf("enumeration is not key ordered: %#v", states)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.PendingAttemptStates()
	if err != nil || len(recovered) != len(states) || recovered[0] != states[0] || recovered[1] != states[1] {
		t.Fatalf("recovered=%#v states=%#v err=%v", recovered, states, err)
	}
}

func TestAttemptAuthorityCorruptTruncatedDuplicateAndReorderFailClosed(t *testing.T) {
	build := func(t *testing.T) (string, []byte) {
		t.Helper()
		dir := t.TempDir()
		store, _ := OpenResultIngressStore(dir)
		_ = func() error {
			_, err := store.CompareAndAppend(0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: attemptTestIdentity()})
			return err
		}()
		raw, err := os.ReadFile(filepath.Join(dir, resultIngressStoreFileName))
		if err != nil {
			t.Fatal(err)
		}
		return dir, raw
	}
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"truncated", func(raw []byte) []byte { return raw[:len(raw)-1] }},
		{"corrupt", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), "attempt-opened", "attempt-broken", 1))
		}},
		{"duplicate", func(raw []byte) []byte { return append(append([]byte{}, raw...), raw...) }},
		{"trailing-json-value", func(raw []byte) []byte { return append(bytes.TrimSpace(raw), []byte("{}\n")...) }},
		{"reorder", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"revision":1`, `"revision":2`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, raw := build(t)
			if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), test.edit(raw), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AttemptState(attemptTestIdentity()); err == nil {
				t.Fatal("corrupt authority unexpectedly replayed")
			}
		})
	}
}
