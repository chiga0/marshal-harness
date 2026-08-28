//go:build darwin

package resultingress

import (
	"bytes"
	"context"
	"errors"
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
