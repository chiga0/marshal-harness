package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
)

func effectTestProvision(t *testing.T, store *DurableStore) (AttemptAuthorityState, EffectIntentRequest, EffectAppendResult) {
	t.Helper()
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
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
	result, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: binding.CurrentRunAuthority}, request)
	if err != nil {
		t.Fatal(err)
	}
	return opened, request, result
}

func effectTestIntent(binding EffectBinding, effectID, commandID, idempotencyKey, requestDigest string) authority.SideEffectIntent {
	return authority.SideEffectIntent{
		AuthorityNamespaceId: binding.Identity.AuthorityNamespaceID,
		EffectId:             effectID, OwnerIdentity: binding.Identity.OrchestratorID,
		Port: "sandbox", Operation: string(binding.Phase), TargetRef: binding.Identity.AllocationID,
		TargetDigest: attemptTestDigest("allocation-target"), RequestDigest: requestDigest,
		CommandId: commandID, IdempotencyKey: idempotencyKey,
		PolicyDigest: attemptTestDigest("policy"), AuthorizationDigest: binding.AdmissionAuthorityDigest,
		Purpose: "durable allocation effect", Deadline: "2026-08-29T00:00:00Z",
		DispositionClass: map[EffectPhase]authority.DispositionClass{
			EffectPhaseAllocationProvision: authority.DispositionClassSandboxProvision,
			EffectPhaseAllocationTerminate: authority.DispositionClassSandboxTerminate,
		}[binding.Phase],
	}
}

func effectTestReceipt(state EffectAuthorityState, disposition authority.Disposition) authority.SideEffectReceipt {
	return authority.SideEffectReceipt{
		AuthorityNamespaceId: state.Binding.Identity.AuthorityNamespaceID,
		IntentDigest:         state.IntentRecordDigest, Disposition: disposition,
		ProviderResourceIdentity: "allocation-object-1", ObservedDigest: attemptTestDigest("provider-observation"),
		ActorProvenance: authority.ActorProvenance{SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace: "tenant-1", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "local-provider-1",
		}},
		ReconcileIdentity: "reconcile-1",
	}
}

func effectTestReconcile(state EffectAuthorityState, observation authority.Observation, decision authority.Decision) authority.ReconcileRecord {
	return authority.ReconcileRecord{
		AuthorityNamespaceId: state.Binding.Identity.AuthorityNamespaceID,
		Observation:          observation, Decision: decision,
		IntentDigest: state.IntentRecordDigest, ReceiptDigest: state.ReceiptRecordDigest,
	}
}

func effectUse(result EffectAppendResult) EffectUseRequest {
	return EffectUseRequest{Binding: result.State.Binding, EffectID: result.State.Intent.EffectId, IntentFactDigest: result.State.IntentFactDigest}
}

func TestEffectAuthorityIntentReceiptReconcileRestartAndResponseLoss(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	opened, request, intentResult := effectTestProvision(t, store)
	if !intentResult.Appended || intentResult.State.IntentRecordDigest == "" || intentResult.State.IntentFactDigest != intentResult.FactDigest {
		t.Fatalf("intent=%#v", intentResult)
	}
	attempt, found, err := store.AttemptState(opened.Identity)
	if err != nil || !found || attempt.Revision != opened.Revision+1 || attempt.HeadDigest != intentResult.FactDigest || attempt.PendingEffectIntentFactDigest != intentResult.FactDigest {
		t.Fatalf("attempt=%#v found=%v err=%v", attempt, found, err)
	}
	if _, err := appendAuthorizedAttempt(store, attempt.Revision, attempt.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: opened.Identity, LaunchAuthorizationID: "premature-launch"}); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("pending effect allowed launch: %v", err)
	}
	replay, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, request)
	if err != nil || replay.Appended || replay.FactDigest != intentResult.FactDigest {
		t.Fatalf("intent replay=%#v err=%v", replay, err)
	}

	use := effectUse(intentResult)
	effectCalls := 0
	receiptResult, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		effectCalls++
		// This nested replay would deadlock if the Provider callback ran while
		// the ResultIngress flock/mutex was held.
		if pending, nestedErr := store.PendingEffects(); nestedErr != nil || len(pending) != 1 {
			t.Fatalf("nested pending=%#v err=%v", pending, nestedErr)
		}
		return effectTestReceipt(state, authority.DispositionApplied), nil
	})
	if err != nil || !receiptResult.Appended || effectCalls != 1 || receiptResult.State.ReceiptFactDigest == "" {
		t.Fatalf("receipt=%#v calls=%d err=%v", receiptResult, effectCalls, err)
	}
	// Lost response across a process restart returns the durable receipt and
	// never invokes the Provider again.
	receiptStore, _ := OpenResultIngressStore(dir)
	receiptReplay, err := receiptStore.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(EffectAuthorityState) (authority.SideEffectReceipt, error) {
		effectCalls++
		return authority.SideEffectReceipt{}, errors.New("must not run")
	})
	if err != nil || receiptReplay.Appended || receiptReplay.FactDigest != receiptResult.FactDigest || effectCalls != 1 {
		t.Fatalf("receipt replay=%#v calls=%d err=%v", receiptReplay, effectCalls, err)
	}

	reconcileCalls := 0
	store = receiptStore
	reconcileResult, err := store.ReconcilePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(state EffectAuthorityState) (authority.ReconcileRecord, error) {
		reconcileCalls++
		if pending, nestedErr := store.PendingEffects(); nestedErr != nil || len(pending) != 1 {
			t.Fatalf("nested reconcile pending=%#v err=%v", pending, nestedErr)
		}
		return effectTestReconcile(state, authority.ObservationApplied, authority.DecisionAccept), nil
	})
	if err != nil || !reconcileResult.Appended || reconcileCalls != 1 || reconcileResult.State.ReconcileFactDigest == "" {
		t.Fatalf("reconcile=%#v calls=%d err=%v", reconcileResult, reconcileCalls, err)
	}
	if pending, err := store.PendingEffects(); err != nil || len(pending) != 0 {
		t.Fatalf("closed pending=%#v err=%v", pending, err)
	}
	current, _, _ := store.AttemptState(opened.Identity)
	if current.PendingEffectIntentFactDigest != "" || current.AllocationProvisionEffectDigest != reconcileResult.FactDigest || current.AllocationProvisionReceiptDigest != receiptResult.State.ReceiptRecordDigest {
		t.Fatalf("closed attempt=%#v", current)
	}

	reopened, _ := OpenResultIngressStore(dir)
	recovered, ok, err := reopened.EffectState(opened.Identity.AuthorityNamespaceID, request.Intent.EffectId)
	if err != nil || !ok || recovered != reconcileResult.State {
		t.Fatalf("recovered=%#v ok=%v err=%v", recovered, ok, err)
	}
	reconcileReplay, err := reopened.ReconcilePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(EffectAuthorityState) (authority.ReconcileRecord, error) {
		reconcileCalls++
		return authority.ReconcileRecord{}, errors.New("must not run")
	})
	if err != nil || reconcileReplay.Appended || reconcileReplay.FactDigest != reconcileResult.FactDigest || reconcileCalls != 1 {
		t.Fatalf("reconcile replay=%#v calls=%d err=%v", reconcileReplay, reconcileCalls, err)
	}
	current, _, _ = reopened.AttemptState(opened.Identity)
	launch, err := appendAuthorizedAttempt(reopened, current.Revision, current.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: opened.Identity, LaunchAuthorizationID: "launch-after-provision"})
	if err != nil || !launch.Appended {
		t.Fatalf("launch=%#v err=%v", launch, err)
	}
}

func TestEffectAuthorityRejectsForgeryABAAndConflictingIdentity(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	opened, request, intentResult := effectTestProvision(t, store)
	run := request.Binding.CurrentRunAuthority

	for name, mutate := range map[string]func(*EffectIntentRequest){
		"attempt-head": func(r *EffectIntentRequest) {
			r.Binding.AdmissionAuthorityDigest = attemptTestDigest("aba-head")
			r.Intent.AuthorizationDigest = r.Binding.AdmissionAuthorityDigest
		},
		"revision": func(r *EffectIntentRequest) { r.Binding.AdmissionAttemptRevision++ },
		"run-authority": func(r *EffectIntentRequest) {
			r.Binding.CurrentRunAuthority.RunAuthorityDigest = attemptTestDigest("stale-run")
		},
		"namespace":     func(r *EffectIntentRequest) { r.Intent.AuthorityNamespaceId.AuthorityScopeId = "forged" },
		"authorization": func(r *EffectIntentRequest) { r.Intent.AuthorizationDigest = attemptTestDigest("forged-auth") },
		"marker":        func(r *EffectIntentRequest) { r.Binding.MarkerDigest = attemptTestDigest("other-marker") },
	} {
		t.Run(name, func(t *testing.T) {
			forged := request
			mutate(&forged)
			if _, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: run}, forged); err == nil {
				t.Fatal("forged intent accepted")
			}
		})
	}

	current, _, _ := store.AttemptState(opened.Identity)
	conflictBinding := request.Binding
	conflictBinding.AdmissionAttemptRevision, conflictBinding.AdmissionAuthorityDigest = current.Revision, current.HeadDigest
	for _, conflicting := range []authority.SideEffectIntent{
		effectTestIntent(conflictBinding, "other-effect", request.Intent.CommandId, "other-key", attemptTestDigest("different-request")),
		effectTestIntent(conflictBinding, "other-effect", "other-command", request.Intent.IdempotencyKey, attemptTestDigest("different-request")),
	} {
		if _, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: run}, EffectIntentRequest{Binding: conflictBinding, Intent: conflicting}); !errors.Is(err, ErrEffectAuthorityConflict) && !errors.Is(err, ErrAttemptAuthorityConflict) {
			t.Fatalf("command/idempotency conflict err=%v", err)
		}
	}

	use := effectUse(intentResult)
	wrongUse := use
	wrongUse.IntentFactDigest = attemptTestDigest("other-intent-fact")
	calls := 0
	if _, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: run}, wrongUse, func(EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		return authority.SideEffectReceipt{}, nil
	}); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 0 {
		t.Fatalf("forged use err=%v calls=%d", err, calls)
	}
	unknown := use
	unknown.EffectID = "unknown-effect"
	if _, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: run}, unknown, func(EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		return authority.SideEffectReceipt{}, nil
	}); !errors.Is(err, ErrEffectAuthorityUnknown) || calls != 0 {
		t.Fatalf("zero-match use err=%v calls=%d", err, calls)
	}
	if _, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: run}, use, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		receipt := effectTestReceipt(state, authority.DispositionApplied)
		receipt.IntentDigest = attemptTestDigest("forged-intent")
		return receipt, nil
	}); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 1 {
		t.Fatalf("forged receipt err=%v calls=%d", err, calls)
	}
	state, _, _ := store.EffectState(opened.Identity.AuthorityNamespaceID, request.Intent.EffectId)
	if state.ReceiptFactDigest != "" {
		t.Fatalf("forged receipt became durable: %#v", state)
	}
}

func TestEffectAuthorityCallbackFailureAndVerifierAbuseRemainPending(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	_, request, intentResult := effectTestProvision(t, store)
	use := effectUse(intentResult)
	calls := 0
	if _, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		return authority.SideEffectReceipt{}, errors.New("provider failed")
	}); err == nil || calls != 1 {
		t.Fatalf("provider failure err=%v calls=%d", err, calls)
	}
	pending, _ := store.PendingEffects()
	if len(pending) != 1 || pending[0].ReceiptFactDigest != "" {
		t.Fatalf("pending after provider failure=%#v", pending)
	}
	if _, err := store.ExecutePendingEffect(context.Background(), attemptDeferredRunVerifier{want: request.Binding.CurrentRunAuthority, deferred: new(func() error)}, use, func(EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		return authority.SideEffectReceipt{}, nil
	}); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 1 {
		t.Fatalf("deferred verifier err=%v calls=%d", err, calls)
	}
	if _, err := store.ExecutePendingEffect(context.Background(), attemptDoubleRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		calls++
		return effectTestReceipt(state, authority.DispositionApplied), nil
	}); !errors.Is(err, ErrEffectAuthorityConflict) || calls != 2 {
		t.Fatalf("double verifier err=%v calls=%d", err, calls)
	}
	state, _, _ := store.EffectState(request.Binding.Identity.AuthorityNamespaceID, request.Intent.EffectId)
	if state.ReceiptFactDigest == "" {
		t.Fatal("first, sole callback receipt was not durable")
	}
}

func TestTerminateEffectRequiresProvisionMarkerAndExactCleanupReceipt(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	_, provisionRequest, provisionIntent := effectTestProvision(t, store)
	provisionUse := effectUse(provisionIntent)
	provisionReceipt, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: provisionRequest.Binding.CurrentRunAuthority}, provisionUse, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		return effectTestReceipt(state, authority.DispositionApplied), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcilePendingEffect(context.Background(), attemptRunVerifier{want: provisionRequest.Binding.CurrentRunAuthority}, provisionUse, func(state EffectAuthorityState) (authority.ReconcileRecord, error) {
		return effectTestReconcile(state, authority.ObservationApplied, authority.DecisionAccept), nil
	}); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.AttemptState(provisionRequest.Binding.Identity)
	launch, _ := appendAuthorizedAttempt(store, current.Revision, current.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: current.Identity, LaunchAuthorizationID: "launch-terminate-test"})
	started, _ := appendAuthorizedAttempt(store, launch.State.Revision, launch.State.HeadDigest, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: current.Identity, CommandID: "command-1", ObservedAt: "2026-08-28T00:00:00Z", Process: attemptTestProcess(t)})
	barrier := appendTestBarrier(t, store, started.State, "terminal-effect-test", TerminalAttemptFailed).State
	cleanup := CleanupAuthorizationRequest{Identity: current.Identity, CurrentRunAuthority: provisionRequest.Binding.CurrentRunAuthority, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupReconcile}
	terminalResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: cleanup.CurrentRunAuthority}, barrier.Revision, barrier.HeadDigest, cleanup, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: current.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessTerminated, ObservationDigest: attemptTestDigest("process-terminal")})
	if err != nil {
		t.Fatal(err)
	}
	terminal := terminalResult.State
	binding := EffectBinding{
		Identity: current.Identity, CurrentRunAuthority: cleanup.CurrentRunAuthority,
		AdmissionAttemptRevision: terminal.Revision, AdmissionAuthorityDigest: terminal.HeadDigest,
		Phase: EffectPhaseAllocationTerminate, MarkerDigest: provisionRequest.Binding.MarkerDigest,
		TerminalizationID: terminal.TerminalizationID, TerminalGeneration: terminal.TerminalGeneration,
		CleanupBindingDigest: terminal.CleanupBindingDigest, ProcessTerminalFactDigest: terminal.ProcessTerminalDigest,
	}
	terminateRequest := EffectIntentRequest{Binding: binding, Intent: effectTestIntent(binding, "terminate-effect", "terminate-command", "terminate-key", attemptTestDigest("terminate-request"))}
	wrongMarker := terminateRequest
	wrongMarker.Binding.MarkerDigest = attemptTestDigest("unknown-marker")
	if _, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: binding.CurrentRunAuthority}, wrongMarker); !errors.Is(err, ErrEffectAuthorityConflict) {
		t.Fatalf("unknown marker terminate err=%v", err)
	}
	terminateIntent, err := store.CompareAndAppendEffectIntent(context.Background(), attemptRunVerifier{want: binding.CurrentRunAuthority}, terminateRequest)
	if err != nil {
		t.Fatal(err)
	}
	terminateUse := effectUse(terminateIntent)
	terminateReceipt, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: binding.CurrentRunAuthority}, terminateUse, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		return effectTestReceipt(state, authority.DispositionApplied), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	terminateReconcile, err := store.ReconcilePendingEffect(context.Background(), attemptRunVerifier{want: binding.CurrentRunAuthority}, terminateUse, func(state EffectAuthorityState) (authority.ReconcileRecord, error) {
		return effectTestReconcile(state, authority.ObservationApplied, authority.DecisionAccept), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ = store.AttemptState(current.Identity)
	cleanup.Operation = CleanupTerminate
	wrongReceipt := AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: current.Identity, TerminalizationID: current.TerminalizationID, ReceiptDigest: provisionReceipt.State.ReceiptRecordDigest}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: cleanup.CurrentRunAuthority}, current.Revision, current.HeadDigest, cleanup, wrongReceipt); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("wrong terminate receipt err=%v", err)
	}
	correct := AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: current.Identity, TerminalizationID: current.TerminalizationID, ReceiptDigest: terminateReceipt.State.ReceiptRecordDigest}
	allocation, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: cleanup.CurrentRunAuthority}, current.Revision, current.HeadDigest, cleanup, correct)
	if err != nil || !allocation.Appended || current.AllocationTerminateEffectDigest != terminateReconcile.FactDigest {
		t.Fatalf("allocation=%#v current=%#v err=%v", allocation, current, err)
	}
}

func TestNonAcceptedReconcileEntersInterventionAndBlocksLifecycle(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	opened, request, intent := effectTestProvision(t, store)
	use := effectUse(intent)
	if _, err := store.ExecutePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(state EffectAuthorityState) (authority.SideEffectReceipt, error) {
		return effectTestReceipt(state, authority.DispositionConflict), nil
	}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.ReconcilePendingEffect(context.Background(), attemptRunVerifier{want: request.Binding.CurrentRunAuthority}, use, func(state EffectAuthorityState) (authority.ReconcileRecord, error) {
		return effectTestReconcile(state, authority.ObservationConflict, authority.DecisionBlock), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.AttemptState(opened.Identity)
	if current.EffectInterventionDigest != reconciled.FactDigest || current.PendingEffectIntentFactDigest != "" {
		t.Fatalf("intervention state=%#v", current)
	}
	if _, err := appendAuthorizedAttempt(store, current.Revision, current.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: opened.Identity, LaunchAuthorizationID: "unsafe-launch"}); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("intervention allowed launch: %v", err)
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
