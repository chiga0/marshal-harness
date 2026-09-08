package cloudflare

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// testAuthorityNamespace returns one valid fixed authority key space for
// fixtures.
func testAuthorityNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "tenant-fixture",
		ControlPlaneId:   "control-fixture",
		AuthorityScopeId: "scope-fixture",
	}
}

// testSecurityDomain returns one valid fixed actor provenance key space for
// fixtures.
func testSecurityDomain() authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   "tenant-fixture",
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "isolation-fixture",
	}
}

// testEffectResolver is the deterministic Core-injected effect context
// resolver for fixtures: the effect id is a stable function of the operation,
// run, attempt, allocation and command, so a replay of the identical identity
// always resolves the identical effect id.
type testEffectResolver struct{}

func (testEffectResolver) ResolveEffectContext(_ context.Context, operation string, identity sandbox.OperationIdentity, allocationId string) (EffectContext, error) {
	return EffectContext{
		RunId:                identity.RunId,
		AttemptId:            identity.AttemptId,
		EffectId:             fixtureDigest("effect:" + operation + ":" + identity.RunId + ":" + identity.AttemptId + ":" + allocationId + ":" + identity.CommandId),
		AuthorityNamespaceId: testAuthorityNamespace(),
		SecurityDomainId:     testSecurityDomain(),
		PolicyDigest:         fixtureDigest("policy:" + identity.RunId),
		AuthorizationDigest:  fixtureDigest("authz:" + identity.RunId + ":" + identity.AttemptId),
		Deadline:             "2099-01-01T00:00:00Z",
	}, nil
}

// testEffectContext resolves one frozen effect context for an identity.
func testEffectContext(operation, allocationId, commandId string, generation int64) EffectContext {
	identity := scenarioIdentity("effect", allocationId, commandId, generation)
	effectCtx, _ := testEffectResolver{}.ResolveEffectContext(context.Background(), operation, identity, allocationId)
	return effectCtx
}

// testEffectIntent builds one valid effect intent plus its context and
// identity for one operation/target.
func testEffectIntent(t *testing.T, operation, targetRef string) (EffectContext, sandbox.OperationIdentity, authority.SideEffectIntent) {
	t.Helper()
	// The effect id must be a stable function of the target reference, so two
	// distinct effects for the same (targetRef, operation) carry distinct
	// effect ids and a same-effect re-put remains idempotent.
	identity := scenarioIdentity("effect", targetRef, "cmd-"+operation, 1)
	effectCtx, err := testEffectResolver{}.ResolveEffectContext(context.Background(), operation, identity, targetRef)
	if err != nil {
		t.Fatalf("ResolveEffectContext: %v", err)
	}
	replayKey, err := identity.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey: %v", err)
	}
	intent, err := buildEffectIntent(effectCtx, identity, operation, targetRef, replayKey)
	if err != nil {
		t.Fatalf("buildEffectIntent: %v", err)
	}
	return effectCtx, identity, intent
}

// testEffectReceipt builds one valid receipt bound to the given intent.
func testEffectReceipt(t *testing.T, intent authority.SideEffectIntent, effectCtx EffectContext, disposition authority.Disposition, providerResourceIdentity string) authority.SideEffectReceipt {
	t.Helper()
	receipt, err := buildEffectReceipt(intent, effectCtx, disposition, providerResourceIdentity, sandbox.RecomputeSHA256([]byte(providerResourceIdentity)), effectCtx.RunId+"/"+effectCtx.AttemptId)
	if err != nil {
		t.Fatalf("buildEffectReceipt: %v", err)
	}
	return receipt
}

// TestEffectContextValidateFailsClosed freezes that the frozen effect context
// validates fail closed and binds exactly to its run/attempt.
func TestEffectContextValidateFailsClosed(t *testing.T) {
	valid := testEffectContext(EffectOperationProvision, "alloc-effect", "cmd-provision", 1)
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid context must validate: %v", err)
	}
	identity := scenarioIdentity("effect", "alloc-effect", "cmd-provision", 1)
	if err := valid.bindTo(identity); err != nil {
		t.Fatalf("the context must bind to its identity: %v", err)
	}

	for _, tc := range []struct {
		name   string
		change func(*EffectContext)
	}{
		{"runId", func(c *EffectContext) { c.RunId = "" }},
		{"attemptId", func(c *EffectContext) { c.AttemptId = "" }},
		{"effectId", func(c *EffectContext) { c.EffectId = "" }},
		{"policyDigest", func(c *EffectContext) { c.PolicyDigest = "" }},
		{"authorizationDigest", func(c *EffectContext) { c.AuthorizationDigest = "not-a-digest" }},
		{"deadline", func(c *EffectContext) { c.Deadline = "not-a-timestamp" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			tc.change(&broken)
			if err := broken.Validate(); err == nil {
				t.Fatalf("a broken %s context must fail closed", tc.name)
			}
		})
	}

	other := valid
	other.RunId = "run-other"
	if err := other.bindTo(identity); !errors.Is(err, ErrEffectCrossEffect) {
		t.Fatalf("a context from another run must fail the exact binding, got %v", err)
	}
}

// TestBuildEffectIntentReceiptObservation freezes that a built receipt binds
// to the recomputed digest of its intent and that the whole record validates.
func TestBuildEffectIntentReceiptObservation(t *testing.T) {
	effectCtx, _, intent := testEffectIntent(t, EffectOperationProvision, "alloc-effect-provision")
	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "alloc-effect-provision")
	observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
	if err != nil {
		t.Fatalf("buildEffectObservation: %v", err)
	}

	record := EffectAuthorityRecord{Intent: intent, Receipt: &receipt, Observation: &observation}
	if err := record.Validate(); err != nil {
		t.Fatalf("the built record must validate: %v", err)
	}
	if receipt.IntentDigest == "" {
		t.Fatal("the receipt must carry the recomputed intent digest")
	}
	recomputed, err := intent.Digest()
	if err != nil {
		t.Fatalf("intent.Digest: %v", err)
	}
	if receipt.IntentDigest != recomputed {
		t.Fatal("the receipt intent digest must equal the recomputed intent digest")
	}
	if !receipt.ActorProvenance.SecurityDomainId.Equal(effectCtx.SecurityDomainId) {
		t.Fatal("the receipt actor provenance must carry the frozen security domain")
	}
}

// TestEffectSinkPutIfAbsent freezes put-if-absent intent semantics, including
// the (targetRef, operation) reverse index conflict.
func TestEffectSinkPutIfAbsent(t *testing.T) {
	sink := newMemoryEffectSink()
	_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-put-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("re-recording the identical intent must be idempotent: %v", err)
	}

	// A different intent for the same effect id is a conflict.
	_, _, other := testEffectIntent(t, EffectOperationTerminate, "br-put-2")
	other.EffectId = intent.EffectId
	if err := sink.PutIntent(other); !errors.Is(err, ErrEffectIntentConflict) {
		t.Fatalf("a different intent for the same effect id must conflict, got %v", err)
	}

	// A different effect id for the same (targetRef, operation) is a conflict.
	_, _, third := testEffectIntent(t, EffectOperationTerminate, "br-put-3")
	third.TargetRef = intent.TargetRef
	third.TargetDigest = intent.TargetDigest
	third.Operation = intent.Operation
	if err := sink.PutIntent(third); !errors.Is(err, ErrEffectIntentConflict) {
		t.Fatalf("a different effect for the same target must conflict, got %v", err)
	}
}

// TestEffectSinkLookupExactBinding freezes that Lookup and LookupByTarget
// return exactly the effect they address and fail closed on unknown ids.
func TestEffectSinkLookupExactBinding(t *testing.T) {
	sink := newMemoryEffectSink()
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-lookup-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}

	record, err := sink.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if record.Intent.EffectId != intent.EffectId {
		t.Fatalf("Lookup must return the exact effect, got %q want %q", record.Intent.EffectId, intent.EffectId)
	}
	if record.Receipt != nil {
		t.Fatalf("an unresolved intent must carry no receipt, got %+v", record.Receipt)
	}

	byTarget, err := sink.LookupByTarget(intent.TargetRef, intent.Operation)
	if err != nil {
		t.Fatalf("LookupByTarget: %v", err)
	}
	if byTarget.Intent.EffectId != intent.EffectId {
		t.Fatalf("LookupByTarget must discover the exact effect, got %q want %q", byTarget.Intent.EffectId, intent.EffectId)
	}

	if _, err := sink.Lookup("unknown-effect"); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("an unknown effect id must report ErrEffectNotFound, got %v", err)
	}
	if _, err := sink.LookupByTarget("unknown-target", EffectOperationTerminate); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("an unknown target must report ErrEffectNotFound, got %v", err)
	}
	if _, err := sink.LookupByTarget(intent.TargetRef, EffectOperationProvision); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("a wrong operation for the target must report ErrEffectNotFound, got %v", err)
	}

	_ = effectCtx
}

// TestEffectSinkResolveIntentAtomic freezes that ResolveIntent records the
// receipt and observation in one mutation and clears the pending intent.
func TestEffectSinkResolveIntentAtomic(t *testing.T) {
	sink := newMemoryEffectSink()
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-resolve-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	if pending := sink.PendingIntents(); len(pending) != 1 {
		t.Fatalf("the intent must be pending before resolution, got %d", len(pending))
	}

	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "br-resolve-1")
	observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
	if err != nil {
		t.Fatalf("buildEffectObservation: %v", err)
	}
	if err := sink.ResolveIntent(receipt, observation); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	record, err := sink.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if record.Receipt == nil || record.Observation == nil {
		t.Fatalf("the resolved record must carry the receipt and observation, got %+v", record)
	}
	if record.Receipt.Disposition != authority.DispositionApplied {
		t.Fatalf("the receipt must carry the applied disposition, got %q", record.Receipt.Disposition)
	}
	if pending := sink.PendingIntents(); len(pending) != 0 {
		t.Fatalf("the resolved intent must not be pending, got %d", len(pending))
	}
}

// TestEffectSinkReceiptMismatchFailsClosed freezes that a receipt whose
// intent digest does not recompute to a stored intent is rejected and never
// stored.
func TestEffectSinkReceiptMismatchFailsClosed(t *testing.T) {
	sink := newMemoryEffectSink()
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-mismatch-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "br-mismatch-1")
	receipt.IntentDigest = fixtureDigest("wrong-intent-digest")
	if err := sink.AppendReceipt(receipt); !errors.Is(err, ErrEffectReceiptMismatch) {
		t.Fatalf("a receipt with a mismatched intent digest must fail closed, got %v", err)
	}
	record, err := sink.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if record.Receipt != nil {
		t.Fatal("a rejected receipt must never be stored")
	}
}

// TestEffectSinkCrossEffectNamespaceRejected freezes that a receipt carrying
// a different authority namespace is rejected as a cross-effect binding.
func TestEffectSinkCrossEffectNamespaceRejected(t *testing.T) {
	sink := newMemoryEffectSink()
	_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-cross-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	intentDigest, err := intent.Digest()
	if err != nil {
		t.Fatalf("intent.Digest: %v", err)
	}
	otherNamespace := authority.AuthorityNamespaceId{
		TenantNamespace:  "tenant-other",
		ControlPlaneId:   "control-other",
		AuthorityScopeId: "scope-other",
	}
	receipt := authority.SideEffectReceipt{
		AuthorityNamespaceId:     otherNamespace,
		IntentDigest:             intentDigest,
		Disposition:              authority.DispositionApplied,
		ProviderResourceIdentity: "br-cross-1",
		ObservedDigest:           fixtureDigest("observed-cross"),
		ActorProvenance:          authority.ActorProvenance{SecurityDomainId: testSecurityDomain()},
		ReconcileIdentity:        "run/attempt",
	}
	if err := sink.AppendReceipt(receipt); !errors.Is(err, ErrEffectCrossEffect) {
		t.Fatalf("a cross-effect namespace must fail closed, got %v", err)
	}
}

// TestEffectSinkLookupFailClosedOnErroneousRecord freezes that a corrupted
// stored record never hydrates: Lookup fails closed instead of returning a
// partially valid record.
func TestEffectSinkLookupFailClosedOnErroneousRecord(t *testing.T) {
	sink := newMemoryEffectSink()
	_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-hydrate-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	// Corrupt the stored receipt directly, bypassing validation.
	badReceipt := authority.SideEffectReceipt{
		AuthorityNamespaceId:     intent.AuthorityNamespaceId,
		IntentDigest:             fixtureDigest("corrupt-intent-digest"),
		Disposition:              authority.DispositionApplied,
		ProviderResourceIdentity: "br-hydrate-1",
		ObservedDigest:           fixtureDigest("observed-hydrate"),
		ActorProvenance:          authority.ActorProvenance{SecurityDomainId: testSecurityDomain()},
		ReconcileIdentity:        "run/attempt",
	}
	sink.mu.Lock()
	sink.live.Receipts[intent.EffectId] = badReceipt
	sink.mu.Unlock()

	if _, err := sink.Lookup(intent.EffectId); err == nil {
		t.Fatal("a corrupted stored receipt must fail Lookup closed, never hydrate")
	}
	if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); err == nil {
		t.Fatal("a corrupted stored receipt must fail LookupByTarget closed, never hydrate")
	}
}

// TestEffectSinkLookupFailClosedOnCrossEffectFields freezes that a stored
// receipt whose provider resource identity crosses to a different target, and
// a stored observation whose effect id crosses effects, never hydrate.
func TestEffectSinkLookupFailClosedOnCrossEffectFields(t *testing.T) {
	t.Run("receipt-provider-resource-identity", func(t *testing.T) {
		sink := newMemoryEffectSink()
		effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "alloc-cross-receipt")
		if err := sink.PutIntent(intent); err != nil {
			t.Fatalf("PutIntent: %v", err)
		}
		receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "alloc-cross-receipt")
		observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
		if err != nil {
			t.Fatalf("buildEffectObservation: %v", err)
		}
		if err := sink.ResolveIntent(receipt, observation); err != nil {
			t.Fatalf("ResolveIntent: %v", err)
		}
		sink.mu.Lock()
		corrupted := sink.live.Receipts[intent.EffectId]
		corrupted.ProviderResourceIdentity = "alloc-some-other-target"
		sink.live.Receipts[intent.EffectId] = corrupted
		sink.mu.Unlock()

		if _, err := sink.Lookup(intent.EffectId); !errors.Is(err, ErrEffectCrossEffect) {
			t.Fatalf("a cross-target provider resource identity must fail Lookup closed, got %v", err)
		}
		if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); !errors.Is(err, ErrEffectCrossEffect) {
			t.Fatalf("a cross-target provider resource identity must fail LookupByTarget closed, got %v", err)
		}
	})

	t.Run("observation-effect-id", func(t *testing.T) {
		sink := newMemoryEffectSink()
		effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "alloc-cross-observation")
		if err := sink.PutIntent(intent); err != nil {
			t.Fatalf("PutIntent: %v", err)
		}
		receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "alloc-cross-observation")
		observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
		if err != nil {
			t.Fatalf("buildEffectObservation: %v", err)
		}
		if err := sink.ResolveIntent(receipt, observation); err != nil {
			t.Fatalf("ResolveIntent: %v", err)
		}
		sink.mu.Lock()
		corrupted := sink.live.Observations[intent.EffectId]
		corrupted.EffectId = "some-other-effect"
		sink.live.Observations[intent.EffectId] = corrupted
		sink.mu.Unlock()

		if _, err := sink.Lookup(intent.EffectId); !errors.Is(err, ErrEffectCrossEffect) {
			t.Fatalf("a cross-effect observation effect id must fail Lookup closed, got %v", err)
		}
		if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); !errors.Is(err, ErrEffectCrossEffect) {
			t.Fatalf("a cross-effect observation effect id must fail LookupByTarget closed, got %v", err)
		}
	})
}

// TestEffectSinkClearIntent freezes that clearing an intent removes it from
// both the effect and target indexes.
func TestEffectSinkClearIntent(t *testing.T) {
	sink := newMemoryEffectSink()
	_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-clear-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	if err := sink.ClearIntent(intent.EffectId); err != nil {
		t.Fatalf("ClearIntent: %v", err)
	}
	if _, err := sink.Lookup(intent.EffectId); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("the cleared intent must be gone from Lookup, got %v", err)
	}
	if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("the cleared intent must be gone from LookupByTarget, got %v", err)
	}
	// Clearing an already-cleared intent is idempotent.
	if err := sink.ClearIntent(intent.EffectId); err != nil {
		t.Fatalf("clearing an absent intent must be idempotent: %v", err)
	}
}

// TestEffectSinkReopen freezes that a file-backed sink re-opens with the
// persisted intents, receipts, observations and target index.
func TestEffectSinkReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect.json")
	sink, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("NewFileEffectSink: %v", err)
	}
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-reopen-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, "br-reopen-1")
	observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
	if err != nil {
		t.Fatalf("buildEffectObservation: %v", err)
	}
	if err := sink.ResolveIntent(receipt, observation); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	reopened, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	record, err := reopened.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup after reopen: %v", err)
	}
	if record.Receipt == nil || record.Observation == nil {
		t.Fatalf("the reopened sink must load the resolved record, got %+v", record)
	}
	if pending := reopened.PendingIntents(); len(pending) != 0 {
		t.Fatalf("the reopened sink must not report the resolved intent pending, got %d", len(pending))
	}
}

// TestEffectSinkWriteFailureLeavesLiveUnchanged freezes the failure atomicity
// of the sink: a failed persist leaves the live state untouched.
func TestEffectSinkWriteFailureLeavesLiveUnchanged(t *testing.T) {
	sink := newMemoryEffectSink()
	_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-atomic-1")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	sink.write = func([]byte) error { return errors.New("injected sink write failure") }
	_, _, other := testEffectIntent(t, EffectOperationTerminate, "br-atomic-2")
	if err := sink.PutIntent(other); err == nil {
		t.Fatal("the injected write failure must surface")
	}
	if _, err := sink.Lookup(other.EffectId); !errors.Is(err, ErrEffectNotFound) {
		t.Fatalf("a failed mutation must not change the live state, got %v", err)
	}
	if _, err := sink.Lookup(intent.EffectId); err != nil {
		t.Fatalf("the previously persisted intent must remain, got %v", err)
	}
}

// TestEffectSinkRejectsMalformedConstruction freezes fail-closed construction.
func TestEffectSinkRejectsMalformedConstruction(t *testing.T) {
	if _, err := NewFileEffectSink(""); err == nil {
		t.Fatal("an empty path must be rejected")
	}
	path := filepath.Join(t.TempDir(), "effect.json")
	if err := atomicWriteFile(path, []byte("{not json")); err != nil {
		t.Fatalf("fixture write: %v", err)
	}
	if _, err := NewFileEffectSink(path); err == nil {
		t.Fatal("a malformed persisted sink must fail closed")
	}
}

// TestProviderRequiresEffectSeam freezes that production construction refuses
// a missing effect context resolver or authority sink fail closed.
func TestProviderRequiresEffectSeam(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("effect-required"))
	if _, err := NewProvider(ProviderConfig{
		BridgeBaseURL:         fb.server.URL,
		BridgeToken:           fb.token,
		EffectAuthoritySink:   newMemoryEffectSink(),
		EffectContextResolver: nil,
	}); !errors.Is(err, ErrEffectContextRequired) {
		t.Fatalf("a missing effect context resolver must fail closed, got %v", err)
	}
	if _, err := NewProvider(ProviderConfig{
		BridgeBaseURL:         fb.server.URL,
		BridgeToken:           fb.token,
		EffectContextResolver: testEffectResolver{},
		EffectAuthoritySink:   nil,
	}); !errors.Is(err, ErrEffectSinkRequired) {
		t.Fatalf("a missing effect authority sink must fail closed, got %v", err)
	}
}

// TestEffectSinkRejectsCrossTargetReceiptBeforePersist freezes that a receipt
// whose provider resource identity crosses to a different target is rejected
// before persistence: the failed write does not pollute the live or persisted
// state, and a correct receipt can still be written afterwards instead of
// being suppressed by an existing-conflict on the erroneous receipt.
func TestEffectSinkRejectsCrossTargetReceiptBeforePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect.json")
	sink, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("NewFileEffectSink: %v", err)
	}
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-cross-persist")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	badReceipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, intent.TargetRef)
	badReceipt.ProviderResourceIdentity = "alloc-some-other-target"
	if err := sink.AppendReceipt(badReceipt); !errors.Is(err, ErrEffectCrossEffect) {
		t.Fatalf("a cross-target receipt must be rejected before persist, got %v", err)
	}

	// The live state must be untouched: the intent remains pending.
	record, err := sink.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if record.Receipt != nil {
		t.Fatal("the rejected receipt must never hydrate into the live state")
	}

	// The persisted state must be untouched after a reopen.
	reopened, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopenedRecord, err := reopened.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup after reopen: %v", err)
	}
	if reopenedRecord.Receipt != nil {
		t.Fatal("the rejected receipt must never reach the persisted state")
	}

	// A correct receipt can still be written afterwards, never suppressed by
	// the rejected cross-target receipt.
	correct := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, intent.TargetRef)
	if err := reopened.AppendReceipt(correct); err != nil {
		t.Fatalf("the correct receipt must still bind after the rejected write: %v", err)
	}
	resolved, err := reopened.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup after the correct receipt: %v", err)
	}
	if resolved.Receipt == nil {
		t.Fatal("the correct receipt must hydrate into the resolved record")
	}
}

// TestEffectSinkRejectsObservationWithoutReceipt freezes that an observation
// cannot bind before its effect carries a resolved receipt: the failed bind
// does not pollute the live or persisted state, and a correct resolve can
// still be written afterwards.
func TestEffectSinkRejectsObservationWithoutReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect.json")
	sink, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("NewFileEffectSink: %v", err)
	}
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-orphan-persist")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, intent.TargetRef)
	observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
	if err != nil {
		t.Fatalf("buildEffectObservation: %v", err)
	}
	if err := sink.BindObservation(observation); !errors.Is(err, ErrEffectRecordInvalid) {
		t.Fatalf("an observation without a resolved receipt must be rejected, got %v", err)
	}

	record, err := sink.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if record.Observation != nil {
		t.Fatal("the rejected observation must never hydrate into the live state")
	}

	reopened, err := NewFileEffectSink(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopenedRecord, err := reopened.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup after reopen: %v", err)
	}
	if reopenedRecord.Observation != nil {
		t.Fatal("the rejected observation must never reach the persisted state")
	}

	// The correct receipt+observation resolve must still succeed afterwards.
	if err := reopened.ResolveIntent(receipt, observation); err != nil {
		t.Fatalf("the correct resolve must still succeed after the rejected observation: %v", err)
	}
	resolved, err := reopened.Lookup(intent.EffectId)
	if err != nil {
		t.Fatalf("Lookup after resolve: %v", err)
	}
	if resolved.Receipt == nil || resolved.Observation == nil {
		t.Fatal("the resolved record must carry the receipt and observation")
	}
}

// TestEffectSinkRejectsNonCloudflareIntent freezes that an intent whose port,
// operation, disposition class or target digest is not a valid Cloudflare
// provision/terminate binding is rejected before persistence and never stored.
func TestEffectSinkRejectsNonCloudflareIntent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*authority.SideEffectIntent)
	}{
		{"port", func(i *authority.SideEffectIntent) { i.Port = "aws" }},
		{"operation", func(i *authority.SideEffectIntent) { i.Operation = EffectOperationProvision + "-unknown" }},
		{"disposition-class", func(i *authority.SideEffectIntent) { i.DispositionClass = authority.DispositionClassSandboxProvision }},
		{"target-digest", func(i *authority.SideEffectIntent) { i.TargetDigest = fixtureDigest("wrong-target-digest") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := newMemoryEffectSink()
			_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-intent-"+tc.name)
			tc.mutate(&intent)
			if err := sink.PutIntent(intent); !errors.Is(err, ErrEffectSinkInvalid) {
				t.Fatalf("a non-cloudflare intent (%s) must be rejected, got %v", tc.name, err)
			}
			if _, err := sink.Lookup(intent.EffectId); !errors.Is(err, ErrEffectNotFound) {
				t.Fatalf("the rejected intent must never be stored, got %v", err)
			}
		})
	}
}

// TestEffectSinkLookupFailClosedOnTamperedIntent freezes that a stored intent
// whose Cloudflare binding (port, operation, disposition class or target
// digest) was tampered never hydrates: Lookup and LookupByTarget fail closed.
func TestEffectSinkLookupFailClosedOnTamperedIntent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*authority.SideEffectIntent)
	}{
		{"port", func(i *authority.SideEffectIntent) { i.Port = "aws" }},
		{"operation", func(i *authority.SideEffectIntent) { i.Operation = EffectOperationProvision + "-unknown" }},
		{"disposition-class", func(i *authority.SideEffectIntent) { i.DispositionClass = authority.DispositionClassSandboxProvision }},
		{"target-digest", func(i *authority.SideEffectIntent) { i.TargetDigest = fixtureDigest("wrong-target-digest") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := newMemoryEffectSink()
			_, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-tamper-"+tc.name)
			if err := sink.PutIntent(intent); err != nil {
				t.Fatalf("PutIntent: %v", err)
			}
			sink.mu.Lock()
			corrupted := sink.live.Intents[intent.EffectId]
			tc.mutate(&corrupted)
			sink.live.Intents[intent.EffectId] = corrupted
			sink.mu.Unlock()

			if _, err := sink.Lookup(intent.EffectId); err == nil {
				t.Fatal("a tampered intent must fail Lookup closed, never hydrate")
			}
			if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); err == nil {
				t.Fatal("a tampered intent must fail LookupByTarget closed, never hydrate")
			}
		})
	}
}

// TestEffectSinkLookupFailClosedOnOrphanObservation freezes that a stored
// observation whose receipt was lost (an incomplete record) never hydrates:
// Lookup and LookupByTarget fail closed.
func TestEffectSinkLookupFailClosedOnOrphanObservation(t *testing.T) {
	sink := newMemoryEffectSink()
	effectCtx, _, intent := testEffectIntent(t, EffectOperationTerminate, "br-orphan-lookup")
	if err := sink.PutIntent(intent); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	receipt := testEffectReceipt(t, intent, effectCtx, authority.DispositionApplied, intent.TargetRef)
	observation, err := buildEffectObservation(intent, receipt, authority.ObservationApplied)
	if err != nil {
		t.Fatalf("buildEffectObservation: %v", err)
	}
	if err := sink.ResolveIntent(receipt, observation); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}
	// Simulate an externally corrupted sink where the receipt was lost but
	// the observation survived.
	sink.mu.Lock()
	delete(sink.live.Receipts, intent.EffectId)
	sink.mu.Unlock()

	if _, err := sink.Lookup(intent.EffectId); err == nil {
		t.Fatal("an orphan observation must fail Lookup closed, never hydrate")
	}
	if _, err := sink.LookupByTarget(intent.TargetRef, intent.Operation); err == nil {
		t.Fatal("an orphan observation must fail LookupByTarget closed, never hydrate")
	}
}
