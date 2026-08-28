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
)

type effectAuthorityVerifier struct {
	wantIdentity AttemptIdentity
	wantRun      RunAuthorityBinding
	err          error
	calls        *int
	skip         bool
	double       bool
	deferred     *func() error
}

func (v effectAuthorityVerifier) WithCurrentEffectAuthority(_ context.Context, got CurrentEffectAuthorityCheck, fn func() error) error {
	if v.calls != nil {
		(*v.calls)++
	}
	if v.err != nil {
		return v.err
	}
	if got.Identity != v.wantIdentity || got.CurrentRunAuthority != v.wantRun || got.Now == "" {
		return errors.New("wrong current effect authority")
	}
	if v.skip {
		return nil
	}
	if v.deferred != nil {
		*v.deferred = fn
		return nil
	}
	err := fn()
	if v.double {
		_ = fn()
	}
	return err
}

func effectVerifier(binding EffectBinding) effectAuthorityVerifier {
	return effectAuthorityVerifier{wantIdentity: binding.Identity, wantRun: binding.CurrentRunAuthority}
}

func effectTestIntent(binding EffectBinding, effectID, commandID, idempotencyKey, requestDigest string) authority.SideEffectIntent {
	return authority.SideEffectIntent{
		AuthorityNamespaceId: binding.Identity.AuthorityNamespaceID,
		EffectId:             effectID,
		OwnerIdentity:        binding.Identity.OrchestratorID,
		Port:                 "sandbox",
		Operation:            string(binding.Phase),
		TargetRef:            binding.Identity.AllocationID,
		TargetDigest:         attemptTestDigest("allocation-target"),
		RequestDigest:        requestDigest,
		CommandId:            commandID,
		IdempotencyKey:       idempotencyKey,
		PolicyDigest:         attemptTestDigest("policy"),
		AuthorizationDigest:  binding.AdmissionAuthorityDigest,
		Purpose:              "durable allocation effect",
		Deadline:             "2099-08-29T00:00:00Z",
		DispositionClass: map[EffectPhase]authority.DispositionClass{
			EffectPhaseAllocationProvision: authority.DispositionClassSandboxProvision,
			EffectPhaseAllocationTerminate: authority.DispositionClassSandboxTerminate,
		}[binding.Phase],
	}
}

func effectTestReceipt(state EffectAuthorityState, disposition authority.Disposition) authority.SideEffectReceipt {
	return authority.SideEffectReceipt{
		AuthorityNamespaceId:     state.Binding.Identity.AuthorityNamespaceID,
		IntentDigest:             state.IntentRecordDigest,
		Disposition:              disposition,
		ProviderResourceIdentity: state.Binding.MarkerDigest,
		ObservedDigest:           attemptTestDigest("observed-" + string(disposition)),
		ActorProvenance: authority.ActorProvenance{SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace: "tenant-1", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "local-provider-1",
		}},
		ReconcileIdentity: "reconcile-1",
	}
}

func effectUse(result EffectAppendResult) EffectUseRequest {
	return EffectUseRequest{Binding: result.State.Binding, EffectID: result.State.Intent.EffectId, IntentFactDigest: result.State.IntentFactDigest, IntentDeadline: result.State.Intent.Deadline}
}

func effectOperator(inspectOutcome EffectInspectionOutcome, applyDisposition authority.Disposition, inspectCalls, applyCalls *int) EffectOperator {
	op, err := NewEffectOperator(
		func(_ context.Context, state EffectAuthorityState) (EffectInspection, error) {
			if inspectCalls != nil {
				(*inspectCalls)++
			}
			disposition := map[EffectInspectionOutcome]authority.Disposition{
				EffectInspectionApplied: authority.DispositionApplied, EffectInspectionNotApplied: authority.DispositionNotApplied,
				EffectInspectionAmbiguous: authority.DispositionAmbiguous, EffectInspectionUnknown: authority.DispositionAmbiguous,
				EffectInspectionConflict: authority.DispositionConflict,
			}[inspectOutcome]
			return EffectInspection{Outcome: inspectOutcome, Receipt: effectTestReceipt(state, disposition)}, nil
		},
		func(_ context.Context, state EffectAuthorityState) (authority.SideEffectReceipt, error) {
			if applyCalls != nil {
				(*applyCalls)++
			}
			return effectTestReceipt(state, applyDisposition), nil
		},
	)
	if err != nil {
		panic(err)
	}
	return op
}

func effectTestProvision(t *testing.T, store *DurableStore) (AttemptAuthorityState, EffectIntentRequest, EffectAppendResult) {
	t.Helper()
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	binding := EffectBinding{
		Identity: id, CurrentRunAuthority: attemptTestRunAuthority(id),
		AdmissionAttemptRevision: opened.Revision, AdmissionAuthorityDigest: opened.HeadDigest,
		Phase: EffectPhaseAllocationProvision, MarkerDigest: attemptTestDigest("allocation-marker"),
	}
	request := EffectIntentRequest{Binding: binding, Intent: effectTestIntent(binding, "provision-effect", "provision-command", "provision-key", attemptTestDigest("provision-request"))}
	result, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(binding), request)
	if err != nil {
		t.Fatal(err)
	}
	return opened, request, result
}

func appendTestAcceptedProvision(t *testing.T, store *DurableStore, opened AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	binding := EffectBinding{
		Identity: opened.Identity, CurrentRunAuthority: attemptTestRunAuthority(opened.Identity),
		AdmissionAttemptRevision: opened.Revision, AdmissionAuthorityDigest: opened.HeadDigest,
		Phase: EffectPhaseAllocationProvision, MarkerDigest: attemptTestDigest("allocation-marker-" + opened.Identity.AttemptID),
	}
	request := EffectIntentRequest{Binding: binding, Intent: effectTestIntent(binding, "provision-"+opened.Identity.AttemptID, "provision-command-"+opened.Identity.AttemptID, "provision-key-"+opened.Identity.AttemptID, attemptTestDigest("provision-request-"+opened.Identity.AttemptID))}
	intent, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(binding), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverPendingEffect(context.Background(), effectVerifier(binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, nil, nil)); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.AttemptState(opened.Identity)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func appendTestAcceptedTerminate(t *testing.T, store *DurableStore, terminal AttemptAuthorityState) (AttemptAuthorityState, string) {
	t.Helper()
	provision, ok, err := store.EffectState(terminal.Identity.AuthorityNamespaceID, "provision-"+terminal.Identity.AttemptID)
	if err != nil || !ok {
		t.Fatalf("provision effect missing: %#v %v", provision, err)
	}
	binding := EffectBinding{
		Identity: terminal.Identity, CurrentRunAuthority: attemptTestRunAuthority(terminal.Identity),
		AdmissionAttemptRevision: terminal.Revision, AdmissionAuthorityDigest: terminal.HeadDigest,
		Phase: EffectPhaseAllocationTerminate, MarkerDigest: provision.Binding.MarkerDigest,
		TerminalizationID: terminal.TerminalizationID, TerminalGeneration: terminal.TerminalGeneration,
		CleanupBindingDigest: terminal.CleanupBindingDigest, ProcessTerminalFactDigest: terminal.ProcessTerminalDigest,
	}
	request := EffectIntentRequest{Binding: binding, Intent: effectTestIntent(binding, "terminate-"+terminal.Identity.AttemptID, "terminate-command-"+terminal.Identity.AttemptID, "terminate-key-"+terminal.Identity.AttemptID, attemptTestDigest("terminate-request-"+terminal.Identity.AttemptID))}
	intent, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(binding), request)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := store.RecoverPendingEffect(context.Background(), effectVerifier(binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := store.AttemptState(terminal.Identity)
	if err != nil {
		t.Fatal(err)
	}
	return current, closed.State.ReceiptRecordDigest
}

func TestEffectRecoveryInspectFirstRestartAndDirectLaunchBarrier(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	opened, request, intent := effectTestProvision(t, store)
	current, _, _ := store.AttemptState(opened.Identity)
	launch := AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: opened.Identity, LaunchAuthorizationID: "direct", LaunchClosure: attemptTestClosure(t)}
	if _, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, current.Revision, current.HeadDigest, AttemptAuthorizationRequest{Identity: opened.Identity, CurrentRunAuthority: request.Binding.CurrentRunAuthority}, launch); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("direct launch without accepted effect err=%v", err)
	}
	intentReplay, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(request.Binding), request)
	if err != nil || intentReplay.Appended || intentReplay.FactDigest != intent.FactDigest {
		t.Fatalf("intent replay=%#v err=%v", intentReplay, err)
	}
	inspectCalls, applyCalls := 0, 0
	closed, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &inspectCalls, &applyCalls))
	if err != nil || inspectCalls != 1 || applyCalls != 1 || closed.State.ReconcileFactDigest == "" {
		t.Fatalf("closed=%#v inspect=%d apply=%d err=%v", closed, inspectCalls, applyCalls, err)
	}
	reopened, _ := OpenResultIngressStore(dir)
	replay, err := reopened.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionConflict, authority.DispositionConflict, &inspectCalls, &applyCalls))
	if err != nil || replay.Appended || replay.FactDigest != closed.FactDigest || inspectCalls != 1 || applyCalls != 1 {
		t.Fatalf("replay=%#v inspect=%d apply=%d err=%v", replay, inspectCalls, applyCalls, err)
	}
	state, _, _ := reopened.AttemptState(opened.Identity)
	if _, err := appendAuthorizedAttempt(t, reopened, state.Revision, state.HeadDigest, launch); err != nil {
		t.Fatal(err)
	}
}

func TestEffectRecoveryLostApplyResponseInspectsAndRecovers(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	_, request, intent := effectTestProvision(t, store)
	inspectCalls, applyCalls, applied := 0, 0, false
	lost, _ := NewEffectOperator(
		func(_ context.Context, state EffectAuthorityState) (EffectInspection, error) {
			inspectCalls++
			return EffectInspection{Outcome: EffectInspectionNotApplied, Receipt: effectTestReceipt(state, authority.DispositionNotApplied)}, nil
		},
		func(_ context.Context, _ EffectAuthorityState) (authority.SideEffectReceipt, error) {
			applyCalls++
			applied = true
			return authority.SideEffectReceipt{}, errors.New("response lost after mutation")
		},
	)
	if _, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), lost); err == nil || !applied {
		t.Fatalf("lost response err=%v applied=%v", err, applied)
	}
	recoverOp, _ := NewEffectOperator(
		func(_ context.Context, state EffectAuthorityState) (EffectInspection, error) {
			inspectCalls++
			return EffectInspection{Outcome: EffectInspectionApplied, Receipt: effectTestReceipt(state, authority.DispositionApplied)}, nil
		},
		func(context.Context, EffectAuthorityState) (authority.SideEffectReceipt, error) {
			applyCalls++
			return authority.SideEffectReceipt{}, errors.New("must not apply")
		},
	)
	closed, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), recoverOp)
	if err != nil || closed.State.ReconcileFactDigest == "" || inspectCalls != 2 || applyCalls != 1 {
		t.Fatalf("recovery=%#v inspect=%d apply=%d err=%v", closed, inspectCalls, applyCalls, err)
	}
}

func TestEffectRecoveryAmbiguousThenAppliedClosesLegally(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	_, request, intent := effectTestProvision(t, store)
	first, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionAmbiguous, nil, nil))
	if err != nil || first.State.Receipt.Disposition != authority.DispositionAmbiguous || first.State.ReconcileFactDigest != "" {
		t.Fatalf("ambiguous=%#v err=%v", first, err)
	}
	second, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionApplied, authority.DispositionConflict, nil, nil))
	if err != nil || second.State.ReconcileFactDigest == "" || second.State.ReconcileInspection.Outcome != EffectInspectionApplied {
		t.Fatalf("recovered=%#v err=%v", second, err)
	}
	current, _, _ := store.AttemptState(request.Binding.Identity)
	if current.AllocationProvisionEffectDigest != second.FactDigest || current.EffectInterventionDigest != "" {
		t.Fatalf("attempt=%#v", current)
	}
}

func TestEffectRecoveryConflictEntersInterventionWithoutApply(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	opened, request, intent := effectTestProvision(t, store)
	applyCalls := 0
	blocked, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionConflict, authority.DispositionApplied, nil, &applyCalls))
	if err != nil || applyCalls != 0 || blocked.State.Reconcile.Decision != authority.DecisionBlock {
		t.Fatalf("blocked=%#v apply=%d err=%v", blocked, applyCalls, err)
	}
	current, _, _ := store.AttemptState(opened.Identity)
	if current.EffectInterventionDigest != blocked.FactDigest || current.AllocationProvisionEffectDigest != "" {
		t.Fatalf("attempt=%#v", current)
	}
}

func TestEffectRecoveryConcurrentSingleFlightAppliesOnce(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	secondStore, _ := OpenResultIngressStore(dir)
	_, request, intent := effectTestProvision(t, store)
	var mu sync.Mutex
	inspectCalls, applyCalls := 0, 0
	op, _ := NewEffectOperator(
		func(_ context.Context, state EffectAuthorityState) (EffectInspection, error) {
			mu.Lock()
			inspectCalls++
			mu.Unlock()
			return EffectInspection{Outcome: EffectInspectionNotApplied, Receipt: effectTestReceipt(state, authority.DispositionNotApplied)}, nil
		},
		func(_ context.Context, state EffectAuthorityState) (authority.SideEffectReceipt, error) {
			mu.Lock()
			applyCalls++
			mu.Unlock()
			return effectTestReceipt(state, authority.DispositionApplied), nil
		},
	)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, candidate := range []*DurableStore{store, secondStore} {
		wg.Add(1)
		go func(candidate *DurableStore) {
			defer wg.Done()
			_, err := candidate.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), op)
			errs <- err
		}(candidate)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if inspectCalls != 1 || applyCalls != 1 {
		t.Fatalf("inspect=%d apply=%d", inspectCalls, applyCalls)
	}
}

func TestEffectRecoveryRejectsExpiredCancelledAndVerifierAbuse(t *testing.T) {
	clock := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	expiredAdmissionStore, _ := openResultIngressStoreWithClock(t.TempDir(), func() time.Time { return clock })
	id := attemptTestIdentity()
	openedResult, _ := appendAuthorizedAttempt(t, expiredAdmissionStore, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	expiredBinding := EffectBinding{Identity: id, CurrentRunAuthority: attemptTestRunAuthority(id), AdmissionAttemptRevision: openedResult.State.Revision, AdmissionAuthorityDigest: openedResult.State.HeadDigest, Phase: EffectPhaseAllocationProvision, MarkerDigest: attemptTestDigest("expired-marker")}
	expiredIntent := effectTestIntent(expiredBinding, "expired-effect", "expired-command", "expired-key", attemptTestDigest("expired-request"))
	expiredIntent.Deadline = clock.Format(time.RFC3339Nano)
	verifierCalls := 0
	expiredVerifier := effectVerifier(expiredBinding)
	expiredVerifier.calls = &verifierCalls
	if _, err := expiredAdmissionStore.CompareAndAppendEffectIntent(context.Background(), expiredVerifier, EffectIntentRequest{Binding: expiredBinding, Intent: expiredIntent}); !errors.Is(err, ErrEffectAuthorityExpired) || verifierCalls != 0 {
		t.Fatalf("expired admission err=%v verifierCalls=%d", err, verifierCalls)
	}

	store, _ := openResultIngressStoreWithClock(t.TempDir(), func() time.Time { return clock })
	_, request, intent := effectTestProvision(t, store)
	use := effectUse(intent)
	calls := 0
	rejected := effectVerifier(request.Binding)
	rejected.err = errors.New("lease cancelled or revoked")
	if _, err := store.RecoverPendingEffect(context.Background(), rejected, use, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
		t.Fatalf("cancelled err=%v calls=%d", err, calls)
	}
	var deferred func() error
	abuse := effectVerifier(request.Binding)
	abuse.deferred = &deferred
	if _, err := store.RecoverPendingEffect(context.Background(), abuse, use, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
		t.Fatalf("deferred err=%v calls=%d", err, calls)
	}
	if err := deferred(); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
		t.Fatalf("late callback err=%v calls=%d", err, calls)
	}
	double := effectVerifier(request.Binding)
	double.double = true
	if _, err := store.RecoverPendingEffect(context.Background(), double, use, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 2 {
		t.Fatalf("double err=%v calls=%d", err, calls)
	}

	expiredStore, _ := openResultIngressStoreWithClock(t.TempDir(), func() time.Time { return clock })
	opened, _, _ := effectTestProvision(t, expiredStore)
	expiredStore.clock = func() time.Time { return time.Date(2099, 8, 29, 0, 0, 0, 0, time.UTC) }
	pending, _ := expiredStore.PendingEffects()
	if len(pending) != 1 {
		t.Fatalf("pending=%#v opened=%#v", pending, opened)
	}
	expiredUse := EffectUseRequest{Binding: pending[0].Binding, EffectID: pending[0].Intent.EffectId, IntentFactDigest: pending[0].IntentFactDigest, IntentDeadline: pending[0].Intent.Deadline}
	if _, err := expiredStore.RecoverPendingEffect(context.Background(), effectVerifier(pending[0].Binding), expiredUse, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityExpired) {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestTerminateEffectRequiresAcceptedReceiptBeforeTerminalTransition(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	barrier := appendTestBarrier(t, store, started, "terminal-effect", TerminalAttemptFailed).State
	run := attemptTestRunAuthority(started.Identity)
	cleanup := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupReconcile}
	terminalResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, cleanup, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessTerminated, ObservationDigest: attemptTestDigest("terminal")})
	if err != nil {
		t.Fatal(err)
	}
	terminal := terminalResult.State
	cleanup.Operation = CleanupTerminate
	direct := AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: terminal.TerminalizationID, ReceiptDigest: attemptTestDigest("forged")}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminal.Revision, terminal.HeadDigest, cleanup, direct); !errors.Is(err, ErrCleanupUnauthorized) && !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("direct terminal err=%v", err)
	}
	current, receiptDigest := appendTestAcceptedTerminate(t, store, terminal)
	direct.ReceiptDigest = receiptDigest
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, cleanup, direct); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyAdmissionAndEffectForgeryFailClosed(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	if err := store.RecordAdmitted("legacy", attemptTestDigest("drc"), validEnvelope(attemptTestDigest("result"), 1), attemptTestDigest("fact"), 1); !errors.Is(err, ErrLegacyAdmissionMutationDisabled) {
		t.Fatalf("legacy admission err=%v", err)
	}
	opened, request, intent := effectTestProvision(t, store)
	aba := request
	aba.Binding.AdmissionAttemptRevision++
	if _, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(aba.Binding), aba); !errors.Is(err, ErrEffectAuthorityConflict) {
		t.Fatalf("ABA intent err=%v", err)
	}
	wrong := effectUse(intent)
	wrong.IntentFactDigest = attemptTestDigest("forged")
	calls := 0
	if _, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), wrong, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
		t.Fatalf("forged use err=%v calls=%d", err, calls)
	}
	unknown := effectUse(intent)
	unknown.EffectID = "unknown"
	if _, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), unknown, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityUnknown) || calls != 0 {
		t.Fatalf("unknown use err=%v calls=%d", err, calls)
	}
	state, _, _ := store.AttemptState(opened.Identity)
	if state.PendingEffectIntentFactDigest == "" {
		t.Fatal("forgery changed pending barrier")
	}
}

func TestEffectAuthorityCorruptDuplicateReorderAndPartialTailFailClosed(t *testing.T) {
	build := func(t *testing.T) (string, []byte) {
		t.Helper()
		dir := t.TempDir()
		store, _ := OpenResultIngressStore(dir)
		_, _, _ = effectTestProvision(t, store)
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
		{"partial-tail", func(raw []byte) []byte { return raw[:len(raw)-1] }},
		{"corrupt-digest", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"phase":"allocation-provision"`, `"phase":"allocation-terminate"`, 1))
		}},
		{"duplicate", func(raw []byte) []byte {
			lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
			return append(append(append([]byte{}, raw...), lines[len(lines)-1]...), '\n')
		}},
		{"reorder", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"attemptRevision":2`, `"attemptRevision":3`, 1))
		}},
		{"unknown-field", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"factType":"effect-intent"`, `"factType":"effect-intent","forged":true`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, raw := build(t)
			if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), test.edit(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			store, _ := OpenResultIngressStore(dir)
			if _, err := store.PendingEffects(); err == nil {
				t.Fatal("corrupt effect authority replayed")
			}
		})
	}
}
