package cloudflare

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func storeTestAllocation(allocationId string, generation int64, state sandbox.AllocationState) sandbox.SandboxAllocation {
	return sandbox.SandboxAllocation{
		AllocationId:    allocationId,
		RunId:           "run-store",
		AttemptId:       "attempt-store",
		Generation:      generation,
		State:           state,
		AccessMode:      domain.AccessModeWorkspaceWrite,
		AssuranceLevel:  domain.AssuranceLevelWorkspaceWrite,
		AllowedStoreIds: []string{"store-store"},
	}
}

func storeTestIntent(allocationId string, generation int64) CreateIntent {
	return CreateIntent{
		ReplayKey:    "key-" + allocationId,
		AllocationId: allocationId,
		RunId:        "run-store",
		AttemptId:    "attempt-store",
		Generation:   generation,
	}
}

func storeTestOutcome(allocationId, bridgeLocator string, generation int64) CreateOutcome {
	return CreateOutcome{
		ReplayKey:     "key-" + allocationId,
		AllocationId:  allocationId,
		BridgeLocator: bridgeLocator,
		Meta:          storeTestAllocation(allocationId, generation, sandbox.AllocationActive),
		Role:          sandbox.WorkloadRoleWorker,
	}
}

// TestFileStateStoreCommitFlow freezes the durable intent -> locator ->
// outcome sequence and that a committed outcome clears the pending intent.
func TestFileStateStoreCommitFlow(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	intent := storeTestIntent("alloc-store", 1)
	if err := store.RecordIntent(intent); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if pending := store.PendingIntents(); len(pending) != 1 || pending[0] != intent {
		t.Fatalf("the intent must be pending, got %+v", pending)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if locator, ok := store.BridgeLocator("alloc-store"); !ok || locator != "br-1" {
		t.Fatalf("the locator must be recorded, got %q ok=%t", locator, ok)
	}
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-1", 1)); err != nil {
		t.Fatalf("CommitCreateOutcome: %v", err)
	}
	if pending := store.PendingIntents(); len(pending) != 0 {
		t.Fatalf("the committed outcome must clear the pending intent, got %+v", pending)
	}
	record, ok := store.Allocation("alloc-store")
	if !ok || record.BridgeLocator != "br-1" || record.Meta.State != sandbox.AllocationActive {
		t.Fatalf("the committed allocation must be installed, got %+v ok=%t", record, ok)
	}
}

// TestFileStateStoreCommitCreateOutcomeRejectsInconsistency freezes that a
// committed outcome whose bridge locator disagrees with the persisted
// locator is rejected, and that a missing locator is rejected too.
func TestFileStateStoreCommitCreateOutcomeRejectsInconsistency(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-other", 1)); !errors.Is(err, ErrStateStoreInconsistent) {
		t.Fatalf("a mismatched outcome locator must be rejected, got %v", err)
	}
	if _, ok := store.Allocation("alloc-store"); ok {
		t.Fatal("a rejected outcome must not install an allocation")
	}

	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-unknown", "br-1", 1)); !errors.Is(err, ErrStateStoreInconsistent) {
		t.Fatalf("an outcome without a persisted locator must be rejected, got %v", err)
	}
}

// TestFileStateStoreWriteFailureLeavesMemoryUnchanged freezes the failure
// atomicity: a failed persist leaves the live in-memory state untouched.
func TestFileStateStoreWriteFailureLeavesMemoryUnchanged(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	intent := storeTestIntent("alloc-store", 1)
	if err := store.RecordIntent(intent); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	store.write = func([]byte) error { return errors.New("injected disk failure") }
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err == nil {
		t.Fatal("the injected write failure must surface")
	}
	if _, ok := store.BridgeLocator("alloc-store"); ok {
		t.Fatal("a failed mutation must not change the live state")
	}
	if _, ok := store.Intent(intent.ReplayKey); !ok {
		t.Fatal("the previously persisted intent must remain in the live state")
	}
}

// TestFileStateStoreDeepCopy freezes that committed records are deep copies:
// mutating the input after commit does not mutate the stored record.
func TestFileStateStoreDeepCopy(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	outcome := storeTestOutcome("alloc-store", "br-1", 1)
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if err := store.CommitCreateOutcome(outcome); err != nil {
		t.Fatalf("CommitCreateOutcome: %v", err)
	}
	outcome.Meta.AllowedStoreIds[0] = "store-mutated"
	record, ok := store.Allocation("alloc-store")
	if !ok {
		t.Fatal("the committed allocation must be present")
	}
	if len(record.Meta.AllowedStoreIds) != 1 || record.Meta.AllowedStoreIds[0] != "store-store" {
		t.Fatalf("mutating the committed input must not mutate the stored record, got %+v", record.Meta.AllowedStoreIds)
	}
}

// TestFileStateStoreReopen freezes that a re-opened store loads the
// persisted state, so a provider can converge with a crashed one.
func TestFileStateStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewFileStateStore(path)
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-1", 1)); err != nil {
		t.Fatalf("CommitCreateOutcome: %v", err)
	}

	reopened, err := NewFileStateStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	record, ok := reopened.Allocation("alloc-store")
	if !ok || record.BridgeLocator != "br-1" || record.Meta.Generation != 1 {
		t.Fatalf("the reopened store must load the committed allocation, got %+v ok=%t", record, ok)
	}
	if locator, ok := reopened.BridgeLocator("alloc-store"); !ok || locator != "br-1" {
		t.Fatalf("the reopened store must load the locator, got %q ok=%t", locator, ok)
	}
	if pending := reopened.PendingIntents(); len(pending) != 0 {
		t.Fatalf("the committed intent must not be pending after reopen, got %+v", pending)
	}
}

// TestFileStateStoreReadDoesNotAliasLive freezes that the records returned
// by Allocation and Allocations are deep copies: mutating a read result never
// mutates the live in-memory state.
func TestFileStateStoreReadDoesNotAliasLive(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-1", 1)); err != nil {
		t.Fatalf("CommitCreateOutcome: %v", err)
	}

	record, ok := store.Allocation("alloc-store")
	if !ok {
		t.Fatal("the committed allocation must be present")
	}
	record.Meta.AllowedStoreIds[0] = "store-mutated"
	record.Meta.State = sandbox.AllocationFailed
	record.BridgeLocator = "br-mutated"

	again, ok := store.Allocation("alloc-store")
	if !ok {
		t.Fatal("the committed allocation must remain present after the read mutation")
	}
	if len(again.Meta.AllowedStoreIds) != 1 || again.Meta.AllowedStoreIds[0] != "store-store" {
		t.Fatalf("mutating the read result must not mutate the live record, got %+v", again.Meta.AllowedStoreIds)
	}
	if again.Meta.State != sandbox.AllocationActive || again.BridgeLocator != "br-1" {
		t.Fatalf("mutating the read result must not mutate the live record fields, got state=%q locator=%q", string(again.Meta.State), again.BridgeLocator)
	}

	all := store.Allocations()
	if len(all) != 1 {
		t.Fatalf("exactly one allocation was expected, got %d", len(all))
	}
	all[0].Meta.AllowedStoreIds[0] = "store-mutated-2"
	last, ok := store.Allocation("alloc-store")
	if !ok || len(last.Meta.AllowedStoreIds) != 1 || last.Meta.AllowedStoreIds[0] != "store-store" {
		t.Fatalf("mutating the Allocations result must not mutate the live record, got %+v ok=%t", last.Meta.AllowedStoreIds, ok)
	}
}

// TestFileStateStoreCommitCreateOutcomeWriteFailureLeavesLiveUnchanged
// freezes the failure atomicity of the outcome commit: a failed persist
// installs no allocation, clears no intent and preserves the persisted
// locator.
func TestFileStateStoreCommitCreateOutcomeWriteFailureLeavesLiveUnchanged(t *testing.T) {
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	store.write = func([]byte) error { return errors.New("injected disk failure") }
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-1", 1)); err == nil {
		t.Fatal("the injected write failure must surface")
	}
	if _, ok := store.Allocation("alloc-store"); ok {
		t.Fatal("a failed outcome commit must not install the allocation")
	}
	if _, ok := store.Intent("key-alloc-store"); !ok {
		t.Fatal("a failed outcome commit must not clear the pending intent")
	}
	if locator, ok := store.BridgeLocator("alloc-store"); !ok || locator != "br-1" {
		t.Fatalf("a failed outcome commit must preserve the persisted locator, got %q ok=%t", locator, ok)
	}
}

// TestFileStateStoreReopenPreservesDeepCopy freezes that a re-opened store
// loads an independent copy of the committed record: mutating the reopened
// read result never mutates the reopened store's live state.
func TestFileStateStoreReopenPreservesDeepCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewFileStateStore(path)
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	if err := store.RecordIntent(storeTestIntent("alloc-store", 1)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordBridgeLocator("alloc-store", "br-1"); err != nil {
		t.Fatalf("RecordBridgeLocator: %v", err)
	}
	if err := store.CommitCreateOutcome(storeTestOutcome("alloc-store", "br-1", 1)); err != nil {
		t.Fatalf("CommitCreateOutcome: %v", err)
	}

	reopened, err := NewFileStateStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	record, ok := reopened.Allocation("alloc-store")
	if !ok {
		t.Fatal("the reopened store must load the committed allocation")
	}
	if len(record.Meta.AllowedStoreIds) != 1 || record.Meta.AllowedStoreIds[0] != "store-store" {
		t.Fatalf("the reopened record must preserve the committed store ids, got %+v", record.Meta.AllowedStoreIds)
	}
	record.Meta.AllowedStoreIds[0] = "store-mutated"
	again, ok := reopened.Allocation("alloc-store")
	if !ok || len(again.Meta.AllowedStoreIds) != 1 || again.Meta.AllowedStoreIds[0] != "store-store" {
		t.Fatalf("mutating the reopened read result must not alias the reopened live state, got %+v ok=%t", again.Meta.AllowedStoreIds, ok)
	}
}

// TestFileStateStoreRejectsMalformedConstruction freezes fail-closed
// construction.
func TestFileStateStoreRejectsMalformedConstruction(t *testing.T) {
	if _, err := NewFileStateStore(""); err == nil {
		t.Fatal("an empty path must be rejected")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeMalformedFile(path, []byte("{not json")); err != nil {
		t.Fatalf("fixture write: %v", err)
	}
	if _, err := NewFileStateStore(path); err == nil {
		t.Fatal("a malformed persisted state must fail closed")
	}
}

func writeMalformedFile(path string, data []byte) error {
	return atomicWriteFile(path, data)
}
