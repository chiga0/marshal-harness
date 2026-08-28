//go:build darwin

package resultingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
)

type failAllocationAppendAuthority struct {
	base          *AllocationAuthority
	failPrepared  bool
	failTerminate bool
}

func (authorityPort failAllocationAppendAuthority) WithCurrentAllocation(ctx context.Context, effectKey string, operation func(allocationcontrol.AuthoritySession) error) error {
	return authorityPort.base.WithCurrentAllocation(ctx, effectKey, func(session allocationcontrol.AuthoritySession) error {
		return operation(failAllocationAppendSession{
			AuthoritySession: session,
			failPrepared:     authorityPort.failPrepared,
			failTerminate:    authorityPort.failTerminate,
		})
	})
}

type failAllocationAppendSession struct {
	allocationcontrol.AuthoritySession
	failPrepared  bool
	failTerminate bool
}

type observeAllocationAuthority struct {
	base          allocationcontrol.Authority
	operations    *int
	afterPrepared func(allocationcontrol.AuthoritySnapshot)
}

func (authorityPort observeAllocationAuthority) WithCurrentAllocation(ctx context.Context, effectKey string, operation func(allocationcontrol.AuthoritySession) error) error {
	return authorityPort.base.WithCurrentAllocation(ctx, effectKey, func(session allocationcontrol.AuthoritySession) error {
		if authorityPort.operations != nil {
			(*authorityPort.operations)++
		}
		return operation(observeAllocationSession{AuthoritySession: session, afterPrepared: authorityPort.afterPrepared})
	})
}

type observeAllocationSession struct {
	allocationcontrol.AuthoritySession
	afterPrepared func(allocationcontrol.AuthoritySnapshot)
}

func (session observeAllocationSession) AppendProvisionPrepared(ctx context.Context, prepared allocationcontrol.AllocationStagingPreparedV1) (allocationcontrol.AuthoritySnapshot, error) {
	snapshot, err := session.AuthoritySession.AppendProvisionPrepared(ctx, prepared)
	if err == nil && session.afterPrepared != nil {
		session.afterPrepared(snapshot)
	}
	return snapshot, err
}

func (session failAllocationAppendSession) AppendProvisionPrepared(context.Context, allocationcontrol.AllocationStagingPreparedV1) (allocationcontrol.AuthoritySnapshot, error) {
	if session.failPrepared {
		return allocationcontrol.AuthoritySnapshot{}, errors.New("injected provision prepared response loss")
	}
	return allocationcontrol.AuthoritySnapshot{}, errors.New("unexpected provision prepared append")
}

func (session failAllocationAppendSession) AppendTerminateReceipt(context.Context, allocationcontrol.AllocationTerminateReceiptV1) (allocationcontrol.AuthoritySnapshot, error) {
	if session.failTerminate {
		return allocationcontrol.AuthoritySnapshot{}, errors.New("injected terminate receipt response loss")
	}
	return allocationcontrol.AuthoritySnapshot{}, errors.New("unexpected terminate receipt append")
}

func TestAllocationControllerUsesCanonicalEffectKeyAndAppliesExactlyOnceAcrossTwoRestarts(t *testing.T) {
	ledgerDir := t.TempDir()
	providerRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenResultIngressStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "controller-canonical-effect")
	appended, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || !strings.Contains(appended.EffectKey, "\x00effect\x00") {
		t.Fatalf("append=%#v err=%v", appended, err)
	}
	scope, err := allocationcontrol.StoreScopeForBinding(typed.Binding)
	if err != nil {
		t.Fatal(err)
	}

	var first allocationcontrol.AllocationProvisionReceiptV1
	for restart := 0; restart < 3; restart++ {
		currentStore, err := OpenResultIngressStore(ledgerDir)
		if err != nil {
			t.Fatal(err)
		}
		currentAuthority := allocationTestAuthority(t, currentStore, id, true, false)
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		controller, err := allocationcontrol.NewController(provider, currentAuthority)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := controller.RecoverProvision(context.Background(), appended.EffectKey)
		if err != nil {
			controller.Close()
			t.Fatalf("restart=%d err=%v", restart, err)
		}
		if restart == 0 {
			first = receipt
		} else if receipt.ReceiptDigest != first.ReceiptDigest {
			controller.Close()
			t.Fatalf("restart=%d changed receipt", restart)
		}
		if records := provider.JournalRecords(); len(records) != 3 {
			controller.Close()
			t.Fatalf("restart=%d journal records=%d, Provider Apply was not exactly once", restart, len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
	}
	effect, ok, err := store.EffectState(id.AuthorityNamespaceID, generic.EffectId)
	if err != nil || !ok || effect.Reconcile.Decision != authority.DecisionAccept || effect.ReconcileFactDigest == "" {
		t.Fatalf("effect=%#v ok=%v err=%v", effect, ok, err)
	}
	attempt, _, err := store.AttemptState(id)
	if err != nil || attempt.AllocationProvisionEffectDigest != effect.ReconcileFactDigest || attempt.PendingEffectID != "" {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
}

func TestAllocationControllerExactReplayAfterDeadlineRecoversProviderCrash(t *testing.T) {
	t.Run("provision staging durable before prepared fact", func(t *testing.T) {
		clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
		ledgerDir := t.TempDir()
		providerRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store, err := openResultIngressStoreWithClock(ledgerDir, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
		if err != nil {
			t.Fatal(err)
		}
		generic, typed := allocationTestProvisionIntent(t, opened.State, "controller-provision-deadline-replay")
		generic.Deadline = "2026-08-29T00:00:01Z"
		authorityPort := allocationTestAuthority(t, store, id, true, false)
		first, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
		if err != nil || !first.Appended {
			t.Fatalf("fresh intent=%#v err=%v", first, err)
		}
		scope, err := allocationcontrol.StoreScopeForBinding(typed.Binding)
		if err != nil {
			t.Fatal(err)
		}
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		crashing, err := allocationcontrol.NewController(provider, failAllocationAppendAuthority{base: authorityPort, failPrepared: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := crashing.RecoverProvision(context.Background(), first.EffectKey); err == nil {
			t.Fatal("injected prepared response loss did not stop provision")
		}
		if records := provider.JournalRecords(); len(records) != 1 {
			t.Fatalf("pre-restart projection records=%d, want intent only", len(records))
		}
		if err := crashing.Close(); err != nil {
			t.Fatal(err)
		}

		clock = clock.Add(2 * time.Second)
		ledgerBefore := allocationLedgerBytes(t, ledgerDir)
		linesBefore := bytes.Count(ledgerBefore, []byte{'\n'})
		headBefore, _, err := store.AttemptState(id)
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := openResultIngressStoreWithClock(ledgerDir, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		restartedAuthority := allocationTestAuthority(t, reopened, id, true, false)
		replay, err := restartedAuthority.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
		headAfter, _, stateErr := reopened.AttemptState(id)
		ledgerAfter := allocationLedgerBytes(t, ledgerDir)
		if err != nil || stateErr != nil || replay.Appended || replay.EffectKey != first.EffectKey || replay.EffectFactDigest != first.EffectFactDigest || replay.AllocationDigest != first.AllocationDigest || headAfter.HeadDigest != headBefore.HeadDigest || headAfter.Revision != headBefore.Revision || bytes.Count(ledgerAfter, []byte{'\n'}) != linesBefore || !bytes.Equal(ledgerBefore, ledgerAfter) {
			t.Fatalf("expired exact replay=%#v err=%v stateErr=%v", replay, err, stateErr)
		}
		restartedProvider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		controller, err := allocationcontrol.NewController(restartedProvider, restartedAuthority)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := controller.RecoverProvision(context.Background(), replay.EffectKey)
		if err != nil || receipt.Validate(typed, *mustAllocationSnapshot(t, reopened, replay.EffectKey).ProvisionPrepared) != nil {
			controller.Close()
			t.Fatalf("expired provider recovery receipt=%#v err=%v", receipt, err)
		}
		if records := restartedProvider.JournalRecords(); len(records) != 3 {
			controller.Close()
			t.Fatalf("recovered provision projection records=%d, want exact three", len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("terminate rename durable before receipt fact", func(t *testing.T) {
		clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
		ledgerDir := t.TempDir()
		providerRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store, err := openResultIngressStoreWithClock(ledgerDir, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
		if err != nil {
			t.Fatal(err)
		}
		provisionGeneric, provisionTyped := allocationTestProvisionIntent(t, opened.State, "controller-terminate-setup")
		provisionIntent, err := allocationTestAuthority(t, store, id, true, false).CompareAndAppendAllocationProvisionIntent(context.Background(), id, provisionGeneric, provisionTyped)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := allocationcontrol.StoreScopeForBinding(provisionTyped.Binding)
		if err != nil {
			t.Fatal(err)
		}
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		provisionAuthority := allocationTestAuthority(t, store, id, true, false)
		provisionController, err := allocationcontrol.NewController(provider, provisionAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provisionController.RecoverProvision(context.Background(), provisionIntent.EffectKey); err != nil {
			provisionController.Close()
			t.Fatal(err)
		}
		if err := provisionController.Close(); err != nil {
			t.Fatal(err)
		}
		provisioned, _, err := store.AttemptState(id)
		if err != nil {
			t.Fatal(err)
		}
		terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
		provisionSnapshot := mustAllocationSnapshot(t, store, provisionIntent.EffectKey)
		generic, typed := allocationTestTerminateIntent(t, terminal, provisionSnapshot)
		generic.Deadline = "2026-08-29T00:00:01Z"
		terminateAuthority := allocationTestAuthority(t, store, id, false, true)
		first, err := terminateAuthority.CompareAndAppendAllocationTerminateIntent(context.Background(), id, generic, typed)
		if err != nil || !first.Appended {
			t.Fatalf("fresh terminate intent=%#v err=%v", first, err)
		}
		terminatingProvider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		crashing, err := allocationcontrol.NewController(terminatingProvider, failAllocationAppendAuthority{base: terminateAuthority, failTerminate: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := crashing.RecoverTerminate(context.Background(), first.EffectKey); err == nil {
			t.Fatal("injected terminate receipt response loss did not stop cleanup")
		}
		if records := terminatingProvider.JournalRecords(); len(records) != 4 {
			crashing.Close()
			t.Fatalf("pre-restart terminate projection records=%d, want four", len(records))
		}
		if err := crashing.Close(); err != nil {
			t.Fatal(err)
		}

		clock = clock.Add(2 * time.Second)
		ledgerBefore := allocationLedgerBytes(t, ledgerDir)
		linesBefore := bytes.Count(ledgerBefore, []byte{'\n'})
		headBefore, _, err := store.AttemptState(id)
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := openResultIngressStoreWithClock(ledgerDir, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		restartedAuthority := allocationTestAuthority(t, reopened, id, false, true)
		replay, err := restartedAuthority.CompareAndAppendAllocationTerminateIntent(context.Background(), id, generic, typed)
		headAfter, _, stateErr := reopened.AttemptState(id)
		ledgerAfter := allocationLedgerBytes(t, ledgerDir)
		if err != nil || stateErr != nil || replay.Appended || replay.EffectKey != first.EffectKey || replay.EffectFactDigest != first.EffectFactDigest || replay.AllocationDigest != first.AllocationDigest || headAfter.HeadDigest != headBefore.HeadDigest || headAfter.Revision != headBefore.Revision || bytes.Count(ledgerAfter, []byte{'\n'}) != linesBefore || !bytes.Equal(ledgerBefore, ledgerAfter) {
			t.Fatalf("expired exact terminate replay=%#v err=%v stateErr=%v", replay, err, stateErr)
		}
		restartedProvider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		controller, err := allocationcontrol.NewController(restartedProvider, restartedAuthority)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := controller.RecoverTerminate(context.Background(), replay.EffectKey)
		if err != nil || !receipt.LiveAbsent || !receipt.TombstonePresent {
			controller.Close()
			t.Fatalf("expired terminate provider recovery receipt=%#v err=%v", receipt, err)
		}
		if records := restartedProvider.JournalRecords(); len(records) != 5 {
			controller.Close()
			t.Fatalf("recovered terminate projection records=%d, want exact five", len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAllocationControllerDivergentProjectionClosesBeforeFurtherProviderMutation(t *testing.T) {
	t.Run("provision initial projection ahead", func(t *testing.T) {
		ledgerDir := t.TempDir()
		providerRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store, err := OpenResultIngressStore(ledgerDir)
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
		if err != nil {
			t.Fatal(err)
		}
		generic, typed := allocationTestProvisionIntent(t, opened.State, "projection-ahead-provision")
		authorityPort := allocationTestAuthority(t, store, id, true, false)
		intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := allocationcontrol.StoreScopeForBinding(typed.Binding)
		if err != nil {
			t.Fatal(err)
		}
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		ahead := appendSyntheticPreparedProjection(t, intent.Snapshot)
		if err := provider.SyncAuthorityProjection(ahead); err != nil {
			t.Fatal(err)
		}
		before := providerTreeBytes(t, providerRoot)
		controller, err := allocationcontrol.NewController(provider, authorityPort)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.RecoverProvision(context.Background(), intent.EffectKey); !errors.Is(err, allocationcontrol.ErrAuthorityConflict) {
			controller.Close()
			t.Fatalf("ahead projection err=%v", err)
		}
		if records := provider.JournalRecords(); len(records) != 2 {
			controller.Close()
			t.Fatalf("initial conflict appended Provider projection: %d", len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if after := providerTreeBytes(t, providerRoot); !bytes.Equal(before, after) || providerTreeHasBase(t, providerRoot, typed.StagingRelativeName) || providerTreeHasBase(t, providerRoot, typed.LiveRelativeName) {
			t.Fatal("initial projection conflict mutated Provider filesystem")
		}
		assertProjectionIntervention(t, store, id, generic.EffectId, false)
		assertProjectionRestartClosed(t, ledgerDir, providerRoot, scope, id, generic.EffectId, intent.EffectKey, false)
	})

	t.Run("provision prepared projection divergent", func(t *testing.T) {
		ledgerDir := t.TempDir()
		providerRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store, err := OpenResultIngressStore(ledgerDir)
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
		if err != nil {
			t.Fatal(err)
		}
		generic, typed := allocationTestProvisionIntent(t, opened.State, "projection-divergent-prepared")
		authorityPort := allocationTestAuthority(t, store, id, true, false)
		intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := allocationcontrol.StoreScopeForBinding(typed.Binding)
		if err != nil {
			t.Fatal(err)
		}
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		injectingAuthority := observeAllocationAuthority{
			base: authorityPort,
			afterPrepared: func(snapshot allocationcontrol.AuthoritySnapshot) {
				divergent := divergentProjection(snapshot.Facts, 1)
				if err := provider.SyncAuthorityProjection(divergent); err != nil {
					t.Fatalf("seed prepared divergence: %v", err)
				}
			},
		}
		controller, err := allocationcontrol.NewController(provider, injectingAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.RecoverProvision(context.Background(), intent.EffectKey); !errors.Is(err, allocationcontrol.ErrAuthorityConflict) {
			controller.Close()
			t.Fatalf("prepared projection err=%v", err)
		}
		if records := provider.JournalRecords(); len(records) != 2 {
			controller.Close()
			t.Fatalf("prepared conflict appended Provider projection: %d", len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if !providerTreeHasBase(t, providerRoot, typed.StagingRelativeName) || providerTreeHasBase(t, providerRoot, typed.LiveRelativeName) {
			t.Fatal("prepared projection conflict promoted or lost staging")
		}
		assertProjectionIntervention(t, store, id, generic.EffectId, false)
		assertProjectionRestartClosed(t, ledgerDir, providerRoot, scope, id, generic.EffectId, intent.EffectKey, false)
	})

	t.Run("terminate initial projection divergent", func(t *testing.T) {
		ledgerDir := t.TempDir()
		providerRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store, err := OpenResultIngressStore(ledgerDir)
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
		if err != nil {
			t.Fatal(err)
		}
		provisionGeneric, provisionTyped := allocationTestProvisionIntent(t, opened.State, "projection-divergent-terminate-setup")
		provisionAuthority := allocationTestAuthority(t, store, id, true, false)
		provisionIntent, err := provisionAuthority.CompareAndAppendAllocationProvisionIntent(context.Background(), id, provisionGeneric, provisionTyped)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := allocationcontrol.StoreScopeForBinding(provisionTyped.Binding)
		if err != nil {
			t.Fatal(err)
		}
		provider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		provisionController, err := allocationcontrol.NewController(provider, provisionAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provisionController.RecoverProvision(context.Background(), provisionIntent.EffectKey); err != nil {
			provisionController.Close()
			t.Fatal(err)
		}
		if err := provisionController.Close(); err != nil {
			t.Fatal(err)
		}
		provisioned, _, err := store.AttemptState(id)
		if err != nil {
			t.Fatal(err)
		}
		terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
		provisionSnapshot := mustAllocationSnapshot(t, store, provisionIntent.EffectKey)
		generic, typed := allocationTestTerminateIntentForEffect(t, terminal, provisionSnapshot, "projection-divergent-terminate")
		terminateAuthority := allocationTestAuthority(t, store, id, false, true)
		intent, err := terminateAuthority.CompareAndAppendAllocationTerminateIntent(context.Background(), id, generic, typed)
		if err != nil {
			t.Fatal(err)
		}
		terminatingProvider, err := allocationcontrol.OpenStore(providerRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := terminatingProvider.SyncAuthorityProjection(divergentProjection(intent.Snapshot.Facts, 3)); err != nil {
			t.Fatal(err)
		}
		before := providerTreeBytes(t, providerRoot)
		controller, err := allocationcontrol.NewController(terminatingProvider, terminateAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.RecoverTerminate(context.Background(), intent.EffectKey); !errors.Is(err, allocationcontrol.ErrAuthorityConflict) {
			controller.Close()
			t.Fatalf("terminate projection err=%v", err)
		}
		if records := terminatingProvider.JournalRecords(); len(records) != 4 {
			controller.Close()
			t.Fatalf("terminate conflict appended Provider projection: %d", len(records))
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		if after := providerTreeBytes(t, providerRoot); !bytes.Equal(before, after) || !providerTreeHasBase(t, providerRoot, typed.LiveRelativeName) || providerTreeHasBase(t, providerRoot, typed.TombstoneRelativeName) {
			t.Fatal("terminate projection conflict renamed Provider allocation")
		}
		assertProjectionIntervention(t, store, id, generic.EffectId, true)
		assertProjectionRestartClosed(t, ledgerDir, providerRoot, scope, id, generic.EffectId, intent.EffectKey, true)
	})
}

func appendSyntheticPreparedProjection(t *testing.T, snapshot allocationcontrol.AuthoritySnapshot) []allocationcontrol.CommittedAuthorityFact {
	t.Helper()
	prepared := allocationTestPrepared(t, snapshot)
	payload, err := allocationcontrol.EncodeFactPayload(prepared)
	if err != nil {
		t.Fatal(err)
	}
	facts := cloneProjectionFacts(snapshot.Facts)
	prior := facts[len(facts)-1]
	facts = append(facts, allocationcontrol.CommittedAuthorityFact{
		RecordKind: allocationcontrol.RecordProvisionPrepared, RecordID: "divergent-provision-prepared-ahead",
		RecordedAt: prior.RecordedAt, Binding: prepared.Binding,
		ExpectedAttemptSequence:    prior.ExpectedAttemptSequence + 1,
		AttemptAuthorityFactDigest: attemptTestDigest("divergent-provision-prepared-ahead"),
		RequestDigest:              prepared.RequestDigest, AuthorityFact: payload,
	})
	return facts
}

func divergentProjection(facts []allocationcontrol.CommittedAuthorityFact, index int) []allocationcontrol.CommittedAuthorityFact {
	result := cloneProjectionFacts(facts)
	result[index].RecordID += ":divergent"
	return result
}

func cloneProjectionFacts(facts []allocationcontrol.CommittedAuthorityFact) []allocationcontrol.CommittedAuthorityFact {
	result := append([]allocationcontrol.CommittedAuthorityFact(nil), facts...)
	for index := range result {
		result[index].AuthorityFact = append([]byte(nil), result[index].AuthorityFact...)
	}
	return result
}

func assertProjectionIntervention(t *testing.T, store *DurableStore, id AttemptIdentity, effectID string, terminate bool) {
	t.Helper()
	effect, ok, err := store.EffectState(id.AuthorityNamespaceID, effectID)
	if err != nil || !ok || effect.AllocationFailureKind != allocationcontrol.AuthorityFailureConflict || effect.Reconcile.Decision != authority.DecisionBlock || effect.Receipt.Disposition != authority.DispositionConflict || effect.ReconcileFactDigest == "" {
		t.Fatalf("projection intervention effect=%#v ok=%v err=%v", effect, ok, err)
	}
	attempt, found, err := store.AttemptState(id)
	if err != nil || !found || attempt.EffectInterventionDigest != effect.ReconcileFactDigest || attempt.PendingEffectID != "" || attempt.AllocationProvisionEffectDigest != "" && !terminate || attempt.AllocationTerminateEffectDigest != "" && terminate {
		t.Fatalf("projection intervention attempt=%#v found=%v err=%v", attempt, found, err)
	}
	ledgerBefore := allocationLedgerBytes(t, store.dir)
	if !terminate {
		_, err = appendAuthorizedAttempt(t, store, attempt.Revision, attempt.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "projection-conflict-launch"})
		if !errors.Is(err, ErrAttemptAuthorityOrder) {
			t.Fatalf("projection intervention unlocked launch err=%v", err)
		}
	} else {
		run := attemptTestRunAuthority(id)
		request := CleanupAuthorizationRequest{Identity: id, CurrentRunAuthority: run, TerminalizationID: attempt.TerminalizationID, TerminalGeneration: attempt.TerminalGeneration, CleanupBindingDigest: attempt.CleanupBindingDigest, Operation: CleanupTerminate}
		_, err = store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, attempt.Revision, attempt.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: id, TerminalizationID: attempt.TerminalizationID, ReceiptDigest: attemptTestDigest("projection-conflict-receipt")})
		if !errors.Is(err, ErrCleanupUnauthorized) {
			t.Fatalf("projection intervention unlocked cleanup successor err=%v", err)
		}
	}
	if !bytes.Equal(ledgerBefore, allocationLedgerBytes(t, store.dir)) {
		t.Fatal("rejected successor mutated authority ledger")
	}
}

func assertProjectionRestartClosed(t *testing.T, ledgerDir, providerRoot string, scope allocationcontrol.AllocationStoreScopeV1, id AttemptIdentity, effectID, effectKey string, terminate bool) {
	t.Helper()
	reopened, err := OpenResultIngressStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	base := allocationTestAuthority(t, reopened, id, !terminate, terminate)
	operations := 0
	observed := observeAllocationAuthority{base: base, operations: &operations}
	provider, err := allocationcontrol.OpenStore(providerRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := allocationcontrol.NewController(provider, observed)
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore := allocationLedgerBytes(t, ledgerDir)
	providerBefore := providerTreeBytes(t, providerRoot)
	if terminate {
		_, err = controller.RecoverTerminate(context.Background(), effectKey)
	} else {
		_, err = controller.RecoverProvision(context.Background(), effectKey)
	}
	if !errors.Is(err, ErrAllocationIntervention) || operations != 0 {
		controller.Close()
		t.Fatalf("restart entered Provider: operations=%d err=%v", operations, err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ledgerBefore, allocationLedgerBytes(t, ledgerDir)) || !bytes.Equal(providerBefore, providerTreeBytes(t, providerRoot)) {
		t.Fatal("durable intervention replay mutated authority or Provider")
	}
	effect, ok, stateErr := reopened.EffectState(id.AuthorityNamespaceID, effectID)
	if stateErr != nil || !ok || effect.ReconcileFactDigest == "" || effect.Reconcile.Decision != authority.DecisionBlock {
		t.Fatalf("restart intervention effect=%#v ok=%v err=%v", effect, ok, stateErr)
	}
}

func providerTreeBytes(t *testing.T, root string) []byte {
	t.Helper()
	var result bytes.Buffer
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&result, "%s\x00%s\x00", relative, info.Mode().String())
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result.Write(content)
		}
		result.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func providerTreeHasBase(t *testing.T, root, name string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filepath.Base(path) == name {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func mustAllocationSnapshot(t *testing.T, store *DurableStore, effectKey string) allocationcontrol.AuthoritySnapshot {
	t.Helper()
	authorityPort := allocationTestAuthority(t, store, attemptTestIdentity(), true, false)
	var snapshot allocationcontrol.AuthoritySnapshot
	if err := authorityPort.WithCurrentAllocation(context.Background(), effectKey, func(session allocationcontrol.AuthoritySession) error {
		var err error
		snapshot, err = session.Snapshot()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
