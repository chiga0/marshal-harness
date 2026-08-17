package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestMergeReobservesBaseImmediatelyBeforeMutation(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	initial := mergeTargetFor(fixture)
	drifted := initial
	drifted.Draft = false
	drifted.BaseOid = fabricatedSHA("9")
	harness.targetObserver.seq = []domain.SCMMergeTarget{initial, drifted}

	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("Merge() immediate-base-drift error = %v, want permanent", err)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != 1 || harness.merger.mergeCalls != 0 {
		t.Fatalf("drifted immediate cut mutations: ready=%d merge=%d", harness.merger.readyCalls, harness.merger.mergeCalls)
	}
}

func TestMergeRecoveryRestoresDurableAuthorizationWithoutIssuance(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persist intent and authorization")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial Merge() unexpectedly succeeded")
	}

	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: filepath.Dir(fixture.stateRoot)}
	restarted, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		t.Fatal(err)
	}
	restarted.BindTargetEligibilityResolver(harness.eligibility)
	harness.authorization = restarted
	harness.merger.readyErr = nil
	if _, err := harness.merge(t); err != nil {
		t.Fatalf("recovery with a fresh runtime failed: %v", err)
	}
	for _, audit := range restarted.AuditTrail() {
		if audit.Action == authority.EdgeAuditIssued || audit.Action == authority.EdgeAuditIssuanceMerged {
			t.Fatalf("recovery re-issued PublicationAuthorization: %+v", audit)
		}
	}
}

func TestMergeRecoveryPersistsRevocationAndNeverRevivesIt(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persist intent and authorization")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial Merge() unexpectedly succeeded")
	}
	intent := readPersistedIntent(t, fixture)
	record, err := loadDurableMergeAuthorization(fixture.runDirectory, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.authorization.RevokePublicationAuthorization(record.LedgerKey, authority.EdgeRevocationOrdinary, harness.now); err != nil {
		t.Fatal(err)
	}
	harness.merger.readyErr = nil
	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("revoked recovery error = %v, want permanent", err)
	}
	persisted, err := loadDurableMergeAuthorization(fixture.runDirectory, intent)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Authorization.RevocationGeneration == 0 {
		t.Fatal("revocation successor was not persisted durably")
	}

	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: filepath.Dir(fixture.stateRoot)}
	restarted, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RestorePublicationAuthorization(persisted.LedgerKey, persisted.Authorization, persisted.DecisionBinding); err != nil {
		t.Fatal(err)
	}
	if current, _, ok := restarted.CurrentPublicationAuthorization(persisted.LedgerKey); !ok || current.RevocationGeneration == 0 {
		t.Fatal("fresh runtime revived a revoked durable authorization")
	}
}

func TestMergeRecoveryRejectsTamperedDurableAuthorization(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persist intent and authorization")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial Merge() unexpectedly succeeded")
	}
	intent := readPersistedIntent(t, fixture)
	record, err := loadDurableMergeAuthorization(fixture.runDirectory, intent)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.runDirectory, "merge-authorizations", strings.TrimPrefix(record.RecordDigest, "sha256:")+".json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.merger.mu.Lock()
	before := harness.merger.readyCalls
	harness.merger.mu.Unlock()
	harness.merger.readyErr = nil
	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("tampered authorization error = %v, want permanent", err)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != before {
		t.Fatal("tampered durable authorization reached another mutation")
	}
}

func countMergeIntents(t *testing.T, fixture *mergeFixture) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.runDirectory, "merge-intents"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func countMergeJournalEvents(t *testing.T, fixture *mergeFixture, eventType string) int {
	t.Helper()
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func mergedTargetFor(fixture *mergeFixture) domain.SCMMergeTarget {
	target := mergeTargetFor(fixture)
	target.State = domain.MergeTargetStateMerged
	target.Draft = false
	return target
}

// TestMergeTransientCheckFailurePersistsNoIntentThenRecovers exercises C1: a
// transient observation failure before the intent write produces zero remote
// side effect and zero orphan intent, and a later re-run recovers cleanly.
func TestMergeTransientCheckFailurePersistsNoIntentThenRecovers(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.checkObserver.failWith = errors.New("simulated check observation outage")

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a transient check observation failure")
	}
	if state := harness.inspect(t); state.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", state.State)
	}
	if count := countMergeIntents(t, fixture); count != 0 {
		t.Fatalf("transient check failure persisted %d merge intents", count)
	}
	harness.merger.mu.Lock()
	readyCalls, mergeCalls := harness.merger.readyCalls, harness.merger.mergeCalls
	harness.merger.mu.Unlock()
	if readyCalls != 0 || mergeCalls != 0 {
		t.Fatalf("transient check failure invoked the merger: ready=%d merge=%d", readyCalls, mergeCalls)
	}

	harness.checkObserver.failWith = nil
	result, err := harness.merge(t)
	if err != nil {
		t.Fatalf("recovery Merge() failed: %v", err)
	}
	if result.State.State != domain.StateAccepted {
		t.Fatalf("state = %s, want ACCEPTED", result.State.State)
	}
	if count := countMergeIntents(t, fixture); count != 1 {
		t.Fatalf("recovery persisted %d merge intents, want 1", count)
	}
}

// TestMergeReadyFailureRecoversWithSameIntent exercises C2: a lost ready
// response leaves the immutable intent behind, and re-entry continues with
// the same intent without minting a second one.
func TestMergeReadyFailureRecoversWithSameIntent(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("simulated ready response loss")

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a ready failure")
	}
	if state := harness.inspect(t); state.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", state.State)
	}
	if count := countMergeIntents(t, fixture); count != 1 {
		t.Fatalf("ready failure persisted %d merge intents, want 1", count)
	}
	if count := countMergeJournalEvents(t, fixture, "publication.merged"); count != 0 {
		t.Fatalf("ready failure appended %d publication.merged events", count)
	}

	harness.merger.readyErr = nil
	harness.now = harness.now.Add(17 * time.Minute)
	harness.requestedBy = "different-maintainer"
	result, err := harness.merge(t)
	if err != nil {
		t.Fatalf("recovery Merge() failed: %v", err)
	}
	if result.State.State != domain.StateAccepted {
		t.Fatalf("state = %s, want ACCEPTED", result.State.State)
	}
	if count := countMergeIntents(t, fixture); count != 1 {
		t.Fatalf("recovery persisted %d merge intents, want exactly 1 (idempotent)", count)
	}
	harness.merger.mu.Lock()
	mergeCalls := harness.merger.mergeCalls
	harness.merger.mu.Unlock()
	if mergeCalls != 1 {
		t.Fatalf("recovery invoked merge %d times, want 1", mergeCalls)
	}
	if harness.credentialObserver.calls != 1 {
		t.Fatalf("recovery re-observed credentials %d times, want exactly initial observation", harness.credentialObserver.calls)
	}
}

func TestMergeDeliveryRetryBudgetExhaustsToBlocked(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persistent ready outage")

	if _, err := harness.merge(t); err == nil || port.IsPermanent(err) {
		t.Fatalf("first delivery result = %v, want transient", err)
	}
	if _, err := harness.merge(t); err == nil || !errors.Is(err, port.ErrMergeRetryExhausted) {
		t.Fatalf("second delivery result = %v, want ErrMergeRetryExhausted", err)
	}
	if state := harness.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	harness.merger.mu.Lock()
	readyCalls := harness.merger.readyCalls
	harness.merger.mu.Unlock()
	if readyCalls != maxMergeDeliveryAttempts {
		t.Fatalf("ready calls = %d, want durable budget %d", readyCalls, maxMergeDeliveryAttempts)
	}
}

func TestMergeDeliveryLedgerTamperFailsClosedBeforeAnotherMutation(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("transient ready outage")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial Merge() unexpectedly succeeded")
	}
	intent := readPersistedIntent(t, fixture)
	path := filepath.Join(fixture.runDirectory, "merge-delivery", strings.TrimPrefix(intent.IntentDigest, "sha256:"), mergeDeliveryReady, "001-attempt.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.merger.mu.Lock()
	before := harness.merger.readyCalls
	harness.merger.mu.Unlock()
	harness.merger.readyErr = nil
	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("tampered ledger error = %v, want permanent", err)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != before {
		t.Fatalf("tampered ledger reached another mutation: before=%d after=%d", before, harness.merger.readyCalls)
	}
}

func TestMergeRecoveryRejectsTamperedRemoteCheckBytes(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persist intent and check evidence")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial call unexpectedly succeeded")
	}
	intent := readPersistedIntent(t, fixture)
	path := filepath.Join(fixture.runDirectory, "remote-check-records", strings.TrimPrefix(intent.RemoteCheckRecordDigest, "sha256:")+".json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.merger.readyErr = nil
	harness.merger.mu.Lock()
	readyBefore := harness.merger.readyCalls
	harness.merger.mu.Unlock()
	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("tampered check recovery error = %v, want permanent", err)
	}
	if state := harness.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != readyBefore {
		t.Fatalf("tampered evidence reached a new ready mutation: before=%d after=%d", readyBefore, harness.merger.readyCalls)
	}
}

func TestMergeRecoveryBlocksSecurityDomainDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.readyErr = errors.New("persist intent before recovery")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("initial call unexpectedly succeeded")
	}
	harness.merger.readyErr = nil
	harness.merger.securityDomainID = fabricatedDigest("7")
	if _, err := harness.merge(t); err == nil {
		t.Fatal("recovery accepted a drifted merger security domain")
	}
	if state := harness.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
}

// TestMergeResponseLossRecoversFromMergedTargetWithoutReMerge exercises C4/C5:
// after a lost merge response the run stays CI_PENDING with the intent
// persisted; re-entering against an already-merged PR observes the receipt and
// converges without ever re-calling Merge.
func TestMergeResponseLossRecoversFromMergedTargetWithoutReMerge(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.mergeErr = errors.New("simulated merge response loss")
	harness.receiptObserver.failWith = port.ErrPRNotMerged

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a merge response loss")
	}
	if state := harness.inspect(t); state.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", state.State)
	}
	if count := countMergeIntents(t, fixture); count != 1 {
		t.Fatalf("merge response loss persisted %d merge intents, want 1", count)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("merge response loss must not persist a receipt")
	}

	// Remote now reflects the merge: recovery must observe the receipt and
	// converge without a second merge call.
	harness.merger.mergeErr = nil
	harness.receiptObserver.failWith = nil
	harness.targetObserver.target = mergedTargetFor(fixture)

	harness.merger.mu.Lock()
	mergeCallsBefore := harness.merger.mergeCalls
	harness.merger.mu.Unlock()

	result, err := harness.merge(t)
	if err != nil {
		t.Fatalf("recovery Merge() failed: %v", err)
	}
	if result.State.State != domain.StateAccepted {
		t.Fatalf("state = %s, want ACCEPTED", result.State.State)
	}
	readPersistedReceiptFromRun(t, fixture.runDirectory, fixture.validator)

	harness.merger.mu.Lock()
	mergeCallsAfter := harness.merger.mergeCalls
	harness.merger.mu.Unlock()
	if mergeCallsAfter != mergeCallsBefore {
		t.Fatalf("recovery invoked merge again: before=%d after=%d", mergeCallsBefore, mergeCallsAfter)
	}
	if count := countMergeJournalEvents(t, fixture, "publication.merged"); count != 1 {
		t.Fatalf("recovery appended %d publication.merged events, want 1", count)
	}
}

// TestMergeReentryAfterConvergenceRebuildsOutcomeIdempotently exercises C6/C7:
// after the run already reached ACCEPTED but the final Outcome is missing,
// re-entry reconstructs it from the journal/intent/receipt binding without
// duplicating the convergence event or rewriting an existing Outcome.
func TestMergeReentryAfterConvergenceRebuildsOutcomeIdempotently(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)

	if _, err := harness.merge(t); err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}
	if state := harness.inspect(t); state.State != domain.StateAccepted {
		t.Fatalf("state = %s, want ACCEPTED", state.State)
	}
	mergedEvents := countMergeJournalEvents(t, fixture, "publication.merged")
	if mergedEvents != 1 {
		t.Fatalf("happy path appended %d publication.merged events, want 1", mergedEvents)
	}

	// Simulate a crash after convergence but before the Outcome landed.
	if err := os.Remove(filepath.Join(fixture.runDirectory, "outcome.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.runDirectory, "outcome.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := harness.merge(t); err != nil {
		t.Fatalf("recovery Merge() failed: %v", err)
	}
	if count := countMergeJournalEvents(t, fixture, "publication.merged"); count != mergedEvents {
		t.Fatalf("recovery appended duplicate merged events: want %d got %d", mergedEvents, count)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("recovery did not rebuild the Outcome: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, outcomeData); err != nil {
		t.Fatalf("rebuilt Outcome is invalid: %v", err)
	}

	// A third re-entry must be a no-op that never rewrites the Outcome.
	if _, err := harness.merge(t); err != nil {
		t.Fatalf("idempotent re-entry Merge() failed: %v", err)
	}
	if count := countMergeJournalEvents(t, fixture, "publication.merged"); count != mergedEvents {
		t.Fatalf("idempotent re-entry appended duplicate merged events: want %d got %d", mergedEvents, count)
	}
}

func TestMergeC7RepairsEitherMissingOutcomeDocumentWithoutOverwrite(t *testing.T) {
	for _, missing := range []string{"outcome.json", "outcome.md"} {
		t.Run(missing, func(t *testing.T) {
			fixture := newMergeFixture(t)
			harness := newMergeHarness(t, fixture)
			if _, err := harness.merge(t); err != nil {
				t.Fatal(err)
			}
			kept := "outcome.md"
			if missing == kept {
				kept = "outcome.json"
			}
			keptPath := filepath.Join(fixture.runDirectory, kept)
			keptBefore, err := os.ReadFile(keptPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(fixture.runDirectory, missing)); err != nil {
				t.Fatal(err)
			}
			if _, err := harness.merge(t); err != nil {
				t.Fatalf("C7 repair failed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(fixture.runDirectory, missing)); err != nil {
				t.Fatalf("missing side was not repaired: %v", err)
			}
			keptAfter, err := os.ReadFile(keptPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(keptBefore) != string(keptAfter) {
				t.Fatal("C7 repair overwrote the existing outcome document")
			}
		})
	}
}

func TestMergeC7RevalidatesReceiptEvenWhenOutcomeExists(t *testing.T) {
	for _, removeOutcome := range []bool{false, true} {
		name := "existing-outcome"
		if removeOutcome {
			name = "missing-outcome"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newMergeFixture(t)
			harness := newMergeHarness(t, fixture)
			if _, err := harness.merge(t); err != nil {
				t.Fatalf("initial Merge() failed: %v", err)
			}
			if removeOutcome {
				if err := os.Remove(filepath.Join(fixture.runDirectory, "outcome.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(fixture.runDirectory, "outcome.md")); err != nil {
					t.Fatal(err)
				}
			}
			receiptPath := filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")
			receipt := readPersistedReceiptFromRun(t, fixture.runDirectory, fixture.validator)
			receipt.HeadOid = fabricatedSHA("9")
			digest, err := receipt.Digest()
			if err != nil {
				t.Fatal(err)
			}
			receipt.ReceiptDigest = digest
			data, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(receiptPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := harness.merge(t); err == nil {
				t.Fatal("C7 accepted a receipt that no longer binds the merge intent")
			}
		})
	}
}

func TestMergeC7RejectsMissingReceipt(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err != nil {
		t.Fatalf("initial Merge() failed: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.merge(t); err == nil {
		t.Fatal("C7 accepted an existing Outcome after its bound receipt disappeared")
	}
}

// TestObserveChecksBlocksExternalMergeWithoutIntent covers T7: under
// mergePolicy=policy, a PR merged outside Marshal with no local
// SCMMergeIntent must block and never be claimed as a controlled merge; only
// ADR 0026 reconcile may later migrate it.
func TestObserveChecksBlocksExternalMergeWithoutIntent(t *testing.T) {
	fixture := newMergeFixture(t)
	mergeObserver := &mergeReceiptObserver{authorityNamespaceID: fixture.authorityNamespaceID, mergedBy: "maintainer", method: domain.MergeMethodSquash}

	result, err := ObserveChecks(context.Background(), CheckInput{
		StateRoot: fixture.stateRoot, RunID: fixture.runID,
		Observer: &fakeObserver{status: "pass"}, MergeObserver: mergeObserver,
		Validator: fixture.validator, Now: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("ObserveChecks accepted an external merge without a local intent")
	}
	if result.State.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", result.State.State)
	}
	if count := countMergeIntents(t, fixture); count != 0 {
		t.Fatalf("external merge minted %d local merge intents", count)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("external merge under policy must not persist a controlled-merge receipt")
	}
	if count := countMergeJournalEvents(t, fixture, "publication.merged"); count != 0 {
		t.Fatalf("external merge appended %d publication.merged events", count)
	}
}
