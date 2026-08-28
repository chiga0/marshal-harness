package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
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

// appendHistoricalEffectIntent constructs the pre-Stage2 generic allocation
// history needed by replay tests. Production admission is intentionally closed
// and must use AllocationAuthority with the exact typed payload.
func appendHistoricalEffectIntent(t *testing.T, store *DurableStore, request EffectIntentRequest) EffectAppendResult {
	t.Helper()
	projection := newAuthorityProjection()
	var result EffectAppendResult
	err := store.transact(projection, func() error {
		var err error
		result, err = store.appendEffectIntentLocked(projection, request)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
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
	result := appendHistoricalEffectIntent(t, store, request)
	return opened, request, result
}

func appendTestAcceptedProvision(t *testing.T, store *DurableStore, opened AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	authorityPort := allocationTestAuthority(t, store, opened.Identity, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened, "provision-"+opened.Identity.AttemptID)
	intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), opened.Identity, generic, typed)
	if err != nil {
		t.Fatal(err)
	}
	err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		snapshot, err := session.Snapshot()
		if err != nil {
			return err
		}
		prepared := allocationTestPrepared(t, snapshot)
		snapshot, err = session.AppendProvisionPrepared(context.Background(), prepared)
		if err != nil {
			return err
		}
		if _, err = session.AppendProvisionReceipt(context.Background(), allocationTestProvisionReceipt(t, snapshot)); err != nil {
			return err
		}
		_, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error { return nil })
		return err
	})
	if err != nil {
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
	provisionKey := mustEffectKey(terminal.Identity.AuthorityNamespaceID, "provision-"+terminal.Identity.AttemptID)
	_, _, allocationState, err := store.loadAllocationEffect(provisionKey)
	if err != nil {
		t.Fatal(err)
	}
	authorityPort := allocationTestAuthority(t, store, terminal.Identity, false, true)
	generic, typed := allocationTestTerminateIntentForEffect(t, terminal, allocationState.Snapshot, "terminate-"+terminal.Identity.AttemptID)
	intent, err := authorityPort.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed)
	if err != nil {
		t.Fatal(err)
	}
	var receipt allocationcontrol.AllocationTerminateReceiptV1
	err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		snapshot, err := session.Snapshot()
		if err != nil {
			return err
		}
		receipt = allocationTestTerminateReceipt(t, snapshot)
		if _, err = session.AppendTerminateReceipt(context.Background(), receipt); err != nil {
			return err
		}
		_, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error { return nil })
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := store.AttemptState(terminal.Identity)
	if err != nil {
		t.Fatal(err)
	}
	effect, ok, err := store.EffectState(terminal.Identity.AuthorityNamespaceID, generic.EffectId)
	if err != nil || !ok {
		t.Fatalf("terminate effect missing: %#v %v", effect, err)
	}
	return state, effect.ReceiptRecordDigest
}

func TestGenericAllocationFreshAndHistoricalRecoveryEnterInterventionWithoutProvider(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	opened, request, intent := effectTestProvision(t, store)
	if _, err := store.CompareAndAppendEffectIntent(context.Background(), effectVerifier(request.Binding), request); !errors.Is(err, ErrEffectAuthorityConflict) {
		t.Fatalf("fresh generic allocation admission err=%v", err)
	}
	callbacks := 0
	result, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &callbacks, &callbacks))
	if !errors.Is(err, ErrAllocationIntervention) || callbacks != 0 {
		t.Fatalf("historical generic result=%#v callbacks=%d err=%v", result, callbacks, err)
	}
	state, _, _ := store.AttemptState(opened.Identity)
	if state.EffectInterventionDigest == "" || state.AllocationProvisionEffectDigest != "" || state.PendingEffectID != "" {
		t.Fatalf("intervention state=%#v", state)
	}
	for restart := 0; restart < 2; restart++ {
		reopened, reopenErr := OpenResultIngressStore(dir)
		if reopenErr != nil {
			t.Fatal(reopenErr)
		}
		replay, replayErr := reopened.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), effectUse(intent), effectOperator(EffectInspectionApplied, authority.DispositionApplied, &callbacks, &callbacks))
		if !errors.Is(replayErr, ErrAllocationIntervention) || replay.FactDigest != result.FactDigest || callbacks != 0 {
			t.Fatalf("restart=%d replay=%#v callbacks=%d err=%v", restart, replay, callbacks, replayErr)
		}
	}
}

func TestHistoricalGenericAcceptedFactsProjectOnlyAsIntervention(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	opened, request, intent := effectTestProvision(t, store)
	use := effectUse(intent)
	receipt := effectTestReceipt(intent.State, authority.DispositionApplied)
	receipted, err := store.appendEffectReceipt(use, intent.State, receipt)
	if err != nil {
		t.Fatal(err)
	}
	legacyAccepted, err := store.appendEffectDecision(use, receipted.State, EffectInspection{Outcome: EffectInspectionApplied, Receipt: receipt}, true)
	if err != nil || legacyAccepted.State.Reconcile.Decision != authority.DecisionAccept {
		t.Fatalf("legacy accepted=%#v err=%v", legacyAccepted, err)
	}

	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := reopened.AttemptState(opened.Identity)
	if err != nil || current.AllocationProvisionEffectDigest != "" || current.EffectInterventionDigest != legacyAccepted.FactDigest || current.PendingEffectID != "" {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	callbacks := 0
	replay, err := reopened.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), use, effectOperator(EffectInspectionApplied, authority.DispositionApplied, &callbacks, &callbacks))
	if !errors.Is(err, ErrAllocationIntervention) || replay.FactDigest != legacyAccepted.FactDigest || callbacks != 0 {
		t.Fatalf("replay=%#v callbacks=%d err=%v", replay, callbacks, err)
	}
}

func TestHistoricalGenericReceiptOnlyFactsCloseAsIntervention(t *testing.T) {
	for _, disposition := range []authority.Disposition{
		authority.DispositionApplied,
		authority.DispositionNotApplied,
		authority.DispositionAmbiguous,
		authority.DispositionConflict,
	} {
		t.Run(string(disposition), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := OpenResultIngressStore(dir)
			opened, request, intent := effectTestProvision(t, store)
			use := effectUse(intent)
			receipt := effectTestReceipt(intent.State, disposition)
			receipted, err := store.appendEffectReceipt(use, intent.State, receipt)
			if err != nil {
				t.Fatal(err)
			}

			callbacks := 0
			result, err := store.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), use, effectOperator(EffectInspectionApplied, authority.DispositionApplied, &callbacks, &callbacks))
			if !errors.Is(err, ErrAllocationIntervention) || callbacks != 0 || result.State.AllocationFailureKind != allocationcontrol.AuthorityFailureConflict || result.State.Reconcile.Decision != authority.DecisionBlock || result.State.ReceiptRecordDigest != receipted.State.ReceiptRecordDigest || result.State.Receipt != receipt {
				t.Fatalf("result=%#v callbacks=%d err=%v", result, callbacks, err)
			}
			wantInspection, _ := effectInspectionOutcomeForReceipt(disposition)
			if result.State.ReconcileInspection.Outcome != wantInspection || result.State.ReconcileInspection.Receipt != receipt {
				t.Fatalf("inspection=%#v want=%q", result.State.ReconcileInspection, wantInspection)
			}
			current, _, err := store.AttemptState(opened.Identity)
			if err != nil || current.AllocationProvisionEffectDigest != "" || current.EffectInterventionDigest != result.FactDigest || current.PendingEffectID != "" {
				t.Fatalf("current=%#v err=%v", current, err)
			}
			for restart := 0; restart < 2; restart++ {
				reopened, reopenErr := OpenResultIngressStore(dir)
				if reopenErr != nil {
					t.Fatal(reopenErr)
				}
				replay, replayErr := reopened.RecoverPendingEffect(context.Background(), effectVerifier(request.Binding), use, effectOperator(EffectInspectionConflict, authority.DispositionConflict, &callbacks, &callbacks))
				if !errors.Is(replayErr, ErrAllocationIntervention) || replay.FactDigest != result.FactDigest || replay.State.ReceiptRecordDigest != receipted.State.ReceiptRecordDigest || callbacks != 0 {
					t.Fatalf("restart=%d replay=%#v callbacks=%d err=%v", restart, replay, callbacks, replayErr)
				}
			}
		})
	}
}

func TestHistoricalGenericTerminateCannotHideBehindTypedProvision(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	provisioned := appendTestAcceptedProvision(t, store, opened.State)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	provisionEffect, ok, err := store.EffectState(id.AuthorityNamespaceID, "provision-"+id.AttemptID)
	if err != nil || !ok {
		t.Fatalf("typed provision effect=%#v ok=%v err=%v", provisionEffect, ok, err)
	}
	binding := EffectBinding{
		Identity: id, CurrentRunAuthority: attemptTestRunAuthority(id),
		AdmissionAttemptRevision: terminal.Revision, AdmissionAuthorityDigest: terminal.HeadDigest,
		Phase: EffectPhaseAllocationTerminate, MarkerDigest: provisionEffect.Binding.MarkerDigest,
		TerminalizationID: terminal.TerminalizationID, TerminalGeneration: terminal.TerminalGeneration,
		CleanupBindingDigest: terminal.CleanupBindingDigest, ProcessTerminalFactDigest: terminal.ProcessTerminalDigest,
	}
	request := EffectIntentRequest{Binding: binding, Intent: effectTestIntent(binding, "legacy-terminate", "legacy-terminate-command", "legacy-terminate-key", attemptTestDigest("legacy-terminate-request"))}
	intent := appendHistoricalEffectIntent(t, store, request)
	callbacks := 0
	result, err := store.RecoverPendingEffect(context.Background(), effectVerifier(binding), effectUse(intent), effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &callbacks, &callbacks))
	if !errors.Is(err, ErrAllocationIntervention) || callbacks != 0 || result.State.AllocationFailureKind != allocationcontrol.AuthorityFailureConflict || result.State.Reconcile.Decision != authority.DecisionBlock {
		t.Fatalf("result=%#v callbacks=%d err=%v", result, callbacks, err)
	}
	current, _, _ := store.AttemptState(id)
	if current.AllocationTerminateEffectDigest != "" || current.EffectInterventionDigest != result.FactDigest || current.PendingEffectID != "" {
		t.Fatalf("historical terminate manufactured success: %#v", current)
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
	if _, err := expiredAdmissionStore.CompareAndAppendEffectIntent(context.Background(), expiredVerifier, EffectIntentRequest{Binding: expiredBinding, Intent: expiredIntent}); !errors.Is(err, ErrEffectAuthorityConflict) || verifierCalls != 0 {
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
	if _, err := store.RecoverPendingEffect(context.Background(), double, use, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
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
	if _, err := expiredStore.RecoverPendingEffect(context.Background(), effectVerifier(pending[0].Binding), expiredUse, effectOperator(EffectInspectionNotApplied, authority.DispositionApplied, &calls, &calls)); !errors.Is(err, ErrAllocationIntervention) || calls != 0 {
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
	current, _, _ := store.AttemptState(terminal.Identity)
	if current.AllocationTerminateEffectDigest != "" || current.AllocationTerminateReceiptDigest != "" {
		t.Fatalf("untyped history manufactured accepted allocation facts: %#v", current)
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
