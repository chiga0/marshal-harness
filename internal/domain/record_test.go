package domain

import (
	"slices"
	"testing"
)

// TestKindsAreUniqueAndStable pins the durable kind inventory: the eighteen
// Issue #65 baseline kinds plus scm-merge-receipt, publication-reconcile-
// record and candidate-record. The seven envelope-less Issue #65 M8 schemas
// are reserved kinds below and deliberately not part of this inventory.
func TestKindsAreUniqueAndStable(t *testing.T) {
	kinds := Kinds()
	if len(kinds) != 21 {
		t.Fatalf("Kinds() has %d entries, want 21", len(kinds))
	}
	seen := make(map[Kind]bool, len(kinds))
	for _, kind := range kinds {
		if kind == "" {
			t.Fatal("Kinds() contains an empty kind")
		}
		if seen[kind] {
			t.Fatalf("Kinds() contains duplicate kind %q", kind)
		}
		seen[kind] = true
	}
}

// TestParseKindRoundTripsEveryDurableKind pins that ParseKind accepts every
// durable kind and rejects unknown or mangled values.
func TestParseKindRoundTripsEveryDurableKind(t *testing.T) {
	for _, kind := range Kinds() {
		parsed, err := ParseKind(string(kind))
		if err != nil {
			t.Fatalf("ParseKind(%q) rejected a durable kind: %v", kind, err)
		}
		if parsed != kind {
			t.Fatalf("ParseKind(%q) = %q", kind, parsed)
		}
	}
	for _, value := range []string{"", "task", "Task2", "SCMMergeReceipt ", " SideEffectIntent"} {
		if _, err := ParseKind(value); err == nil {
			t.Fatalf("ParseKind accepted unknown kind %q", value)
		}
	}
}

// TestIssue65ReservedKindsAreNotDurableKinds pins the Issue #65 exception:
// the seven M8 gate-1/gate-2 schemas that freeze internal authority and
// provider Go types reserve Kind constants, but their frozen schema
// documents carry no apiVersion/kind envelope, so they never surface
// through Kinds or ParseKind until their schemas gain the envelope. The
// other two Issue #65 schemas carry the envelope and stay durable kinds.
func TestIssue65ReservedKindsAreNotDurableKinds(t *testing.T) {
	reserved := []Kind{
		KindAuthorityNamespace,
		KindConformanceEvidence,
		KindProviderCapabilitySnapshot,
		KindProviderRegistration,
		KindReconcileRecord,
		KindSideEffectIntent,
		KindSideEffectReceipt,
	}
	kinds := Kinds()
	for _, kind := range reserved {
		if kind == "" {
			t.Fatal("reserved Issue #65 kind is empty")
		}
		if slices.Contains(kinds, kind) {
			t.Fatalf("reserved kind %q must stay outside Kinds() while its schema lacks the durable envelope", kind)
		}
		if _, err := ParseKind(string(kind)); err == nil {
			t.Fatalf("ParseKind accepted reserved kind %q", kind)
		}
	}
	for _, durable := range []Kind{KindSCMMergeReceipt, KindPublicationReconcileRecord} {
		if !slices.Contains(kinds, durable) {
			t.Fatalf("Issue #65 ledger kind %q must stay registered as a durable kind", durable)
		}
	}
}
