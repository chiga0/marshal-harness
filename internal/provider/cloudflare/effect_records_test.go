package cloudflare

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// testEffectAuthorityContext returns a valid Core authority context fixture.
func testEffectAuthorityContext() AuthorityContext {
	return AuthorityContext{
		Namespace: authority.AuthorityNamespaceId{
			TenantNamespace:  "cloudflare",
			ControlPlaneId:   "default",
			AuthorityScopeId: "test-scope",
		},
		ProviderSecurityDomain: authority.SecurityDomainId{
			TenantNamespace:   "cloudflare",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "bridge",
		},
	}
}

// testEffectAllocation returns a valid allocation fixture for effect records.
func testEffectAllocation(allocationId string) sandbox.SandboxAllocation {
	return sandbox.SandboxAllocation{
		AllocationId:   allocationId,
		RunId:          "run-effect",
		AttemptId:      "attempt-effect",
		Generation:     1,
		State:          sandbox.AllocationActive,
		AccessMode:     domain.AccessModeWorkspaceWrite,
		AssuranceLevel: domain.AssuranceLevelWorkspaceWrite,
	}
}

// validEffectAuthorityRecord builds one valid cross-bound record.
func validEffectAuthorityRecord(t *testing.T, operation string) EffectAuthorityRecord {
	t.Helper()
	record, err := NewEffectAuthorityRecord(testEffectAuthorityContext(), testEffectAllocation("alloc-effect"), sandbox.WorkloadRoleWorker, "br-effect", "cmd-effect", operation)
	if err != nil {
		t.Fatalf("NewEffectAuthorityRecord: %v", err)
	}
	return record
}

// redigest recomputes the intent, receipt and reconcile digests after a
// tamper, so the digest cross-checks alone would pass; the field-level
// cross-bindings must still fail closed.
func redigest(t *testing.T, record *EffectAuthorityRecord) {
	t.Helper()
	intentDigest, err := record.Intent.Digest()
	if err != nil {
		t.Fatalf("redigest intent: %v", err)
	}
	record.Receipt.IntentDigest = intentDigest
	record.Reconcile.IntentDigest = intentDigest
	receiptDigest, err := record.Receipt.Digest()
	if err != nil {
		t.Fatalf("redigest receipt: %v", err)
	}
	record.Reconcile.ReceiptDigest = receiptDigest
}

// TestEffectAuthorityRecordValidates freezes that a well-formed cross-bound
// record validates and re-derives its identities deterministically.
func TestEffectAuthorityRecordValidates(t *testing.T) {
	for _, operation := range []string{sandbox.OperationProvision, sandbox.OperationTerminate} {
		record := validEffectAuthorityRecord(t, operation)
		if err := record.Validate(); err != nil {
			t.Fatalf("%s record must validate: %v", operation, err)
		}
		if record.EffectId != effectIdentity(operation, "alloc-effect", 1) {
			t.Fatalf("the effect id must be the derived identity, got %q", record.EffectId)
		}
		if record.ReconcileIdentity != reconcileIdentity("run-effect", "attempt-effect", 1, record.EffectId) {
			t.Fatalf("the reconcile identity must be the same-scope-derived identity, got %q", record.ReconcileIdentity)
		}
	}
}

// TestEffectAuthorityRecordCrossBindingFailsClosed freezes that any
// inconsistency across namespace, effect identity, scope, intent/receipt
// binding or reconcile identity fails closed.
func TestEffectAuthorityRecordCrossBindingFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(*EffectAuthorityRecord)
	}{
		{"namespace", func(r *EffectAuthorityRecord) {
			r.Namespace = authority.AuthorityNamespaceId{TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: "test-scope"}
		}},
		{"intent-namespace", func(r *EffectAuthorityRecord) {
			r.Intent.AuthorityNamespaceId = authority.AuthorityNamespaceId{TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: "test-scope"}
		}},
		{"receipt-namespace", func(r *EffectAuthorityRecord) {
			r.Receipt.AuthorityNamespaceId = authority.AuthorityNamespaceId{TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: "test-scope"}
		}},
		{"reconcile-namespace", func(r *EffectAuthorityRecord) {
			r.Reconcile.AuthorityNamespaceId = authority.AuthorityNamespaceId{TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: "test-scope"}
		}},
		{"effect-id", func(r *EffectAuthorityRecord) { r.EffectId = "forged-effect" }},
		{"intent-effect-id", func(r *EffectAuthorityRecord) { r.Intent.EffectId = "forged-effect" }},
		{"operation", func(r *EffectAuthorityRecord) { r.Operation = sandbox.OperationTerminate }},
		{"allocation-id", func(r *EffectAuthorityRecord) { r.AllocationId = "alloc-other" }},
		{"run-id", func(r *EffectAuthorityRecord) { r.RunId = "run-other" }},
		{"attempt-id", func(r *EffectAuthorityRecord) { r.AttemptId = "attempt-other" }},
		{"generation", func(r *EffectAuthorityRecord) { r.Generation = 2 }},
		{"reconcile-identity", func(r *EffectAuthorityRecord) { r.ReconcileIdentity = "sha256:forged" }},
		{"receipt-reconcile-identity", func(r *EffectAuthorityRecord) { r.Receipt.ReconcileIdentity = "forged" }},
		{"reconcile-intent-digest", func(r *EffectAuthorityRecord) {
			r.Reconcile.IntentDigest = fixtureDigest("forged-intent")
		}},
		{"receipt-intent-digest", func(r *EffectAuthorityRecord) {
			r.Receipt.IntentDigest = fixtureDigest("forged-intent")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := validEffectAuthorityRecord(t, sandbox.OperationProvision)
			tc.tamper(&record)
			if err := record.Validate(); err == nil {
				t.Fatal("Validate accepted an inconsistent record; it must fail closed")
			}
		})
	}
}

// TestEffectAuthorityRecordCoordinatedSubstitutionFailsClosed freezes that a
// coordinated substitution — tampering an intent field and recomputing every
// digest so the digest cross-checks alone pass — still fails closed on the
// field-level cross-bindings.
func TestEffectAuthorityRecordCoordinatedSubstitutionFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(*EffectAuthorityRecord)
	}{
		{"intent-operation", func(r *EffectAuthorityRecord) {
			r.Intent.Operation = sandbox.OperationTerminate
		}},
		{"intent-target-ref", func(r *EffectAuthorityRecord) {
			r.Intent.TargetRef = "alloc-other"
		}},
		{"intent-target-digest", func(r *EffectAuthorityRecord) {
			r.Intent.TargetDigest = fixtureDigest("forged-target")
		}},
		{"intent-idempotency-key", func(r *EffectAuthorityRecord) {
			r.Intent.IdempotencyKey = httpIdempotencyKey("alloc-other", "create")
		}},
		{"intent-disposition-class", func(r *EffectAuthorityRecord) {
			r.Intent.DispositionClass = authority.DispositionClassSandboxTerminate
		}},
		{"intent-policy-digest", func(r *EffectAuthorityRecord) {
			r.Intent.PolicyDigest = fixtureDigest("forged-policy")
		}},
		{"intent-authorization-digest", func(r *EffectAuthorityRecord) {
			r.Intent.AuthorizationDigest = fixtureDigest("forged-authorization")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := validEffectAuthorityRecord(t, sandbox.OperationProvision)
			tc.tamper(&record)
			redigest(t, &record)
			if err := record.Validate(); err == nil {
				t.Fatal("Validate accepted a coordinated substitution with recomputed digests; it must fail closed")
			}
		})
	}
}

// TestEffectAuthorityRecordNormalizedClasses freezes the disposition-class
// normalization: provision and terminate map to their frozen classes.
func TestEffectAuthorityRecordNormalizedClasses(t *testing.T) {
	provision := validEffectAuthorityRecord(t, sandbox.OperationProvision)
	if provision.Intent.DispositionClass != authority.DispositionClassSandboxProvision {
		t.Fatalf("provision must normalize to sandbox-provision, got %q", string(provision.Intent.DispositionClass))
	}
	if provision.Receipt.Disposition != authority.DispositionApplied || provision.Reconcile.Observation != authority.ObservationApplied {
		t.Fatalf("the provision receipt/reconcile must observe the applied outcome")
	}
	terminate := validEffectAuthorityRecord(t, sandbox.OperationTerminate)
	if terminate.Intent.DispositionClass != authority.DispositionClassSandboxTerminate {
		t.Fatalf("terminate must normalize to sandbox-terminate, got %q", string(terminate.Intent.DispositionClass))
	}
}

// TestHTTPIdempotencyKeyLayered freezes the layering discipline: the external
// key is HTTP-safe, deterministic, allocation-derived and distinct from the
// internal durable phase key.
func TestHTTPIdempotencyKeyLayered(t *testing.T) {
	key := httpIdempotencyKey("alloc-http", "create")
	if !strings.HasPrefix(key, "marshal-") {
		t.Fatalf("the external key must carry the marshal- prefix, got %q", key)
	}
	for _, r := range strings.TrimPrefix(key, "marshal-") {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			t.Fatalf("the external key must be HTTP-safe base64url, got %q", key)
		}
	}
	if httpIdempotencyKey("alloc-http", "create") != key {
		t.Fatal("the external key must be deterministic")
	}
	if httpIdempotencyKey("alloc-http-other", "create") == key {
		t.Fatal("the external key must be allocation-derived")
	}
	identity := scenarioIdentity("http", "alloc-http", "cmd-create", 1)
	replayKey, err := identity.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey: %v", err)
	}
	if key == replayKey {
		t.Fatal("the external HTTP key must never coincide with the internal durable phase key")
	}
}

// TestFileEffectAuthoritySinkReopenConverges freezes the durable sink: a
// persisted record survives a reopen, an identical effect id coalesces, and a
// divergent record under the same effect id fails closed.
func TestFileEffectAuthoritySinkReopenConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effects.json")
	record := validEffectAuthorityRecord(t, sandbox.OperationProvision)

	sinkA, err := NewFileEffectAuthoritySink(path)
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	if err := sinkA.PersistEffectAuthority(record); err != nil {
		t.Fatalf("PersistEffectAuthority: %v", err)
	}
	if err := sinkA.PersistEffectAuthority(record); err != nil {
		t.Fatalf("the identical effect id must coalesce idempotently: %v", err)
	}

	reopened, err := NewFileEffectAuthoritySink(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	records := reopened.Records()
	if len(records) != 1 || !records[0].Equal(record) {
		t.Fatalf("the reopened sink must converge on exactly the persisted record, got %d records", len(records))
	}

	// A valid record derived from the identical allocation carries the same
	// effect id but different content (a different command id), so the sink
	// must reject it fail closed rather than silently overwrite.
	divergent, err := NewEffectAuthorityRecord(testEffectAuthorityContext(), testEffectAllocation("alloc-effect"), sandbox.WorkloadRoleWorker, "br-effect", "cmd-effect-other", sandbox.OperationProvision)
	if err != nil {
		t.Fatalf("NewEffectAuthorityRecord divergent: %v", err)
	}
	if divergent.EffectId != record.EffectId {
		t.Fatalf("the identical allocation must derive the identical effect id")
	}
	if err := sinkA.PersistEffectAuthority(divergent); err == nil {
		t.Fatal("a divergent record under an already-persisted effect id must fail closed")
	}
}

// TestFileEffectAuthoritySinkLoadValidatesFailsClosed freezes the reopen
// admission gate: a sink that was persisted normally reopens cleanly, while
// a syntax-valid tampered record, a truncated document, a duplicate-divergent
// fork and a duplicate-identical document each resolve deterministically.
func TestFileEffectAuthoritySinkLoadValidatesFailsClosed(t *testing.T) {
	record := validEffectAuthorityRecord(t, sandbox.OperationProvision)

	t.Run("recovery", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "effects.json")
		sink, err := NewFileEffectAuthoritySink(path)
		if err != nil {
			t.Fatalf("NewFileEffectAuthoritySink: %v", err)
		}
		if err := sink.PersistEffectAuthority(record); err != nil {
			t.Fatalf("PersistEffectAuthority: %v", err)
		}
		reopened, err := NewFileEffectAuthoritySink(path)
		if err != nil {
			t.Fatalf("a valid persisted sink must reopen: %v", err)
		}
		if got := reopened.Records(); len(got) != 1 || !got[0].Equal(record) {
			t.Fatalf("the reopened sink must carry the persisted record, got %d", len(got))
		}
	})

	t.Run("syntax-valid-tampered", func(t *testing.T) {
		tampered := record
		tampered.Intent.PolicyDigest = fixtureDigest("tampered-policy")
		path := writeSinkFile(t, tampered)
		if _, err := NewFileEffectAuthoritySink(path); !errors.Is(err, ErrEffectAuthoritySinkInvalid) {
			t.Fatalf("a syntax-valid tampered record must fail closed, got %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		data, err := json.Marshal([]EffectAuthorityRecord{record})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		path := filepath.Join(t.TempDir(), "effects.json")
		if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
			t.Fatalf("write truncated file: %v", err)
		}
		if _, err := NewFileEffectAuthoritySink(path); !errors.Is(err, ErrEffectAuthoritySinkInvalid) {
			t.Fatalf("a truncated sink must fail closed, got %v", err)
		}
	})

	t.Run("duplicate-identical-coalesces", func(t *testing.T) {
		path := writeSinkFile(t, record, record)
		reopened, err := NewFileEffectAuthoritySink(path)
		if err != nil {
			t.Fatalf("identical duplicates must coalesce on reopen: %v", err)
		}
		if got := reopened.Records(); len(got) != 1 || !got[0].Equal(record) {
			t.Fatalf("identical duplicates must collapse to one record, got %d", len(got))
		}
	})

	t.Run("duplicate-divergent-fork", func(t *testing.T) {
		// A second individually-valid record for the identical allocation and
		// generation carries the identical effect id but different content (a
		// different command id), so the reopen must reject the fork rather
		// than silently coalesce it.
		divergent, err := NewEffectAuthorityRecord(testEffectAuthorityContext(), testEffectAllocation("alloc-effect"), sandbox.WorkloadRoleWorker, "br-effect", "cmd-effect-other", sandbox.OperationProvision)
		if err != nil {
			t.Fatalf("divergent record: %v", err)
		}
		if divergent.EffectId != record.EffectId {
			t.Fatalf("the identical allocation must derive the identical effect id")
		}
		if divergent.Equal(record) {
			t.Fatal("the divergent record must actually differ from the original")
		}
		path := writeSinkFile(t, record, divergent)
		if _, err := NewFileEffectAuthoritySink(path); !errors.Is(err, ErrEffectAuthoritySinkInvalid) {
			t.Fatalf("a divergent fork under one effect id must fail closed, got %v", err)
		}
	})
}

// writeSinkFile serializes the records into a fresh sink file and returns its
// path, for reopen admission-gate fixtures.
func writeSinkFile(t *testing.T, records ...EffectAuthorityRecord) string {
	t.Helper()
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "effects.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write sink file: %v", err)
	}
	return path
}
