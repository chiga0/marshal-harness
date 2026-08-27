package locationattest

import (
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	testAllocationID = "alloc-loc-1"
	testProviderID   = "registration:sandbox-provider-1"
	testObserverID   = "authority:supervisor-kernel-1"
)

func handleDigestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

func mustClaim(t *testing.T) LocationClaim {
	t.Helper()
	claim, err := NewLocationClaim(testAllocationID, 1, testProviderID, "pid=4242")
	if err != nil {
		t.Fatalf("NewLocationClaim: %v", err)
	}
	return claim
}

func mustFact(t *testing.T, allocationID string, generation int64, kind HandleKind, handleDigest, observerID string) LocationFact {
	t.Helper()
	fact, err := NewLocationFact(allocationID, generation, kind, handleDigest, observerID)
	if err != nil {
		t.Fatalf("NewLocationFact: %v", err)
	}
	return fact
}

// ── Claim 构造与校验 ────────────────────────────────────────────────────────

func TestClaim_InvalidIdentity(t *testing.T) {
	cases := []struct {
		name       string
		allocation string
		generation int64
		providerID string
		wantErr    error
	}{
		{"empty allocation", "", 1, testProviderID, ErrMalformedClaim},
		{"zero generation", testAllocationID, 0, testProviderID, ErrMalformedClaim},
		{"negative generation", testAllocationID, -1, testProviderID, ErrMalformedClaim},
		{"empty provider", testAllocationID, 1, "", ErrMalformedClaim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLocationClaim(tc.allocation, tc.generation, tc.providerID, "hint")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected %v, got %v", tc.wantErr, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "locationattest: ") {
				t.Errorf("error %q missing locationattest: prefix", err.Error())
			}
		})
	}
}

func TestClaim_TamperedDigestFailsClosed(t *testing.T) {
	claim := mustClaim(t)
	claim.Generation = 9 // 篡改身份字段但不重算 digest
	if err := claim.Validate(); !errors.Is(err, ErrDigestTampered) {
		t.Errorf("expected ErrDigestTampered, got %v", err)
	}

	claim2 := mustClaim(t)
	claim2.ClaimDigest = handleDigestOf("forged")
	if err := claim2.Validate(); !errors.Is(err, ErrDigestTampered) {
		t.Errorf("expected ErrDigestTampered for forged digest, got %v", err)
	}
}

// ── Fact 构造与校验 ─────────────────────────────────────────────────────────

func TestFact_InvalidIdentity(t *testing.T) {
	validHandle := handleDigestOf("pid", "4242")
	cases := []struct {
		name    string
		mutate  func(*LocationFact)
		wantErr error
	}{
		{"empty allocation", func(f *LocationFact) { f.AllocationID = "" }, ErrMalformedFact},
		{"zero generation", func(f *LocationFact) { f.Generation = 0 }, ErrMalformedFact},
		{"unknown handle kind", func(f *LocationFact) { f.HandleKind = HandleKind("magic") }, ErrMalformedFact},
		{"malformed handle digest", func(f *LocationFact) { f.HandleDigest = "sha256:short" }, ErrMalformedFact},
		{"empty observer", func(f *LocationFact) { f.ObserverID = "" }, ErrMalformedFact},
		{"malformed fact digest", func(f *LocationFact) { f.FactDigest = "not-a-digest" }, ErrMalformedFact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := NewLocationFact(testAllocationID, 1, HandleKindPID, validHandle, testObserverID)
			if err != nil {
				t.Fatalf("NewLocationFact: %v", err)
			}
			tc.mutate(&base)
			// digest 字段被改坏时需保持 FactDigest 与内容一致以打到形态校验；
			// 其它篡改会命中 digest 重算。
			if tc.name == "malformed fact digest" || tc.name == "malformed handle digest" {
				if err := base.Validate(); !errors.Is(err, tc.wantErr) {
					t.Errorf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err := base.Validate(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestFact_AllHandleKindsAccepted(t *testing.T) {
	kinds := []HandleKind{HandleKindPID, HandleKindProcessGroup, HandleKindCgroup, HandleKindVMHandle, HandleKindIndependentAttestation}
	for _, k := range kinds {
		if _, err := NewLocationFact(testAllocationID, 1, k, handleDigestOf("h", string(k)), testObserverID); err != nil {
			t.Errorf("handle kind %q must be accepted: %v", k, err)
		}
	}
}

func TestFact_TamperedDigestFailsClosed(t *testing.T) {
	fact := mustFact(t, testAllocationID, 1, HandleKindCgroup, handleDigestOf("cgroup", "x"), testObserverID)
	fact.HandleDigest = handleDigestOf("different") // 篡改但不重算
	if err := fact.Validate(); !errors.Is(err, ErrDigestTampered) {
		t.Errorf("expected ErrDigestTampered, got %v", err)
	}
}

// ── FactLedger ──────────────────────────────────────────────────────────────

func TestLedger_RegisterAndFactsFor(t *testing.T) {
	ledger := NewFactLedger()
	f1 := mustFact(t, testAllocationID, 1, HandleKindPID, handleDigestOf("pid", "4242"), testObserverID)
	f2 := mustFact(t, testAllocationID, 1, HandleKindCgroup, handleDigestOf("cgroup", "y"), testObserverID)

	if err := ledger.RegisterFact(f1); err != nil {
		t.Fatalf("RegisterFact f1: %v", err)
	}
	if err := ledger.RegisterFact(f1); err != nil {
		t.Errorf("idempotent re-register must succeed, got %v", err)
	}
	if err := ledger.RegisterFact(f2); err != nil {
		t.Fatalf("RegisterFact f2: %v", err)
	}

	got := ledger.FactsFor(testAllocationID, 1)
	if len(got) != 2 {
		t.Errorf("expected 2 facts, got %d", len(got))
	}
	if got := ledger.FactsFor(testAllocationID, 2); len(got) != 0 {
		t.Errorf("generation 2 must not leak facts, got %d", len(got))
	}
	if got := ledger.FactsFor("alloc-other", 1); len(got) != 0 {
		t.Errorf("other allocation must not leak facts, got %d", len(got))
	}
}

func TestLedger_InvalidFactRejected(t *testing.T) {
	ledger := NewFactLedger()
	if err := ledger.RegisterFact(LocationFact{}); err == nil {
		t.Errorf("zero fact must fail closed")
	}
}

func TestLedger_ConflictFailsClosed(t *testing.T) {
	ledger := NewFactLedger()
	f := mustFact(t, testAllocationID, 1, HandleKindPID, handleDigestOf("pid", "4242"), testObserverID)
	if err := ledger.RegisterFact(f); err != nil {
		t.Fatalf("RegisterFact: %v", err)
	}
	// 同身份元组（allocation/generation/kind/observer）但 HandleDigest 不同：
	// 重复观测同一句柄身份却给出不同内容，必须 fail closed。
	conflicting := mustFact(t, testAllocationID, 1, HandleKindPID, handleDigestOf("pid", "9999"), testObserverID)
	if err := ledger.RegisterFact(conflicting); !errors.Is(err, ErrFactConflict) {
		t.Errorf("expected ErrFactConflict, got %v", err)
	}
	// 冲突注册不得覆盖既有 fact：裁决仍只见原始内容。
	got := ledger.FactsFor(testAllocationID, 1)
	if len(got) != 1 || got[0].FactDigest != f.FactDigest {
		t.Errorf("conflict must not overwrite, got %+v", got)
	}
}

// ── Verifier 裁决 ───────────────────────────────────────────────────────────

func TestVerifier_FactBoundProductionAssurance(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	fact := mustFact(t, testAllocationID, 1, HandleKindPID, handleDigestOf("pid", "4242"), testObserverID)
	if err := ledger.RegisterFact(fact); err != nil {
		t.Fatalf("RegisterFact: %v", err)
	}

	v, err := NewVerifier(ledger)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !assurance.ProductionAssurance {
		t.Errorf("expected production assurance with bound fact, got %+v", assurance)
	}
	if assurance.Reason != AssuranceReasonFactBound {
		t.Errorf("expected fact-bound, got %q", assurance.Reason)
	}
	if len(assurance.BoundFacts) != 1 || assurance.BoundFacts[0] != fact.FactDigest {
		t.Errorf("expected bound fact %q, got %v", fact.FactDigest, assurance.BoundFacts)
	}
	if assurance.ClaimDigest != claim.ClaimDigest {
		t.Errorf("assurance must echo claim digest")
	}
}

func TestVerifier_ClaimOnlyNoProductionAssurance(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	v, _ := NewVerifier(ledger)

	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assurance.ProductionAssurance {
		t.Errorf("claim alone must never support production assurance")
	}
	if assurance.Reason != AssuranceReasonClaimOnly {
		t.Errorf("expected claim-only, got %q", assurance.Reason)
	}
}

// 自证 fact：observer 即出示方本人 → 排除，视同 claim-only。
func TestVerifier_SelfAttestedFactExcluded(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	selfFact := mustFact(t, testAllocationID, 1, HandleKindIndependentAttestation, handleDigestOf("self"), testProviderID)
	if err := ledger.RegisterFact(selfFact); err != nil {
		t.Fatalf("RegisterFact: %v", err)
	}

	v, _ := NewVerifier(ledger)
	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assurance.ProductionAssurance {
		t.Errorf("self-attested fact must not support production assurance")
	}
	if len(assurance.BoundFacts) != 0 {
		t.Errorf("self-attested fact must be excluded from bound facts, got %v", assurance.BoundFacts)
	}
	if assurance.Reason != AssuranceReasonClaimOnly {
		t.Errorf("expected claim-only, got %q", assurance.Reason)
	}
}

// 自证 fact + 独立 fact 并存：只有独立 fact 计入裁决。
func TestVerifier_MixedSelfAndIndependentFacts(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	selfFact := mustFact(t, testAllocationID, 1, HandleKindIndependentAttestation, handleDigestOf("self"), testProviderID)
	independent := mustFact(t, testAllocationID, 1, HandleKindVMHandle, handleDigestOf("vm", "h-vm-1"), testObserverID)
	if err := ledger.RegisterFact(selfFact); err != nil {
		t.Fatalf("RegisterFact self: %v", err)
	}
	if err := ledger.RegisterFact(independent); err != nil {
		t.Fatalf("RegisterFact independent: %v", err)
	}

	v, _ := NewVerifier(ledger)
	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !assurance.ProductionAssurance {
		t.Errorf("independent fact must support production assurance")
	}
	if len(assurance.BoundFacts) != 1 || assurance.BoundFacts[0] != independent.FactDigest {
		t.Errorf("only the independent fact must be bound, got %v", assurance.BoundFacts)
	}
}

// 跨 allocation 挪用 fact：fact 存在但绑定不同 allocation → claim-only。
func TestVerifier_FactFromOtherAllocationDoesNotBind(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	other := mustFact(t, "alloc-loc-2", 1, HandleKindPID, handleDigestOf("pid", "4242"), testObserverID)
	if err := ledger.RegisterFact(other); err != nil {
		t.Fatalf("RegisterFact: %v", err)
	}

	v, _ := NewVerifier(ledger)
	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assurance.ProductionAssurance {
		t.Errorf("fact from another allocation must not bind")
	}
}

// 跨 generation 挪用 fact：同 allocation 不同 generation → claim-only。
func TestVerifier_FactFromOtherGenerationDoesNotBind(t *testing.T) {
	ledger := NewFactLedger()
	claim := mustClaim(t)
	stale := mustFact(t, testAllocationID, 2, HandleKindPID, handleDigestOf("pid", "4242"), testObserverID)
	if err := ledger.RegisterFact(stale); err != nil {
		t.Fatalf("RegisterFact: %v", err)
	}

	v, _ := NewVerifier(ledger)
	assurance, err := v.Evaluate(claim)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if assurance.ProductionAssurance {
		t.Errorf("fact from another generation must not bind")
	}
}

// 伪造 claim：digest 与内容不一致 → 结构性拒绝，error（非业务裁决）。
func TestVerifier_TamperedClaimRejected(t *testing.T) {
	ledger := NewFactLedger()
	v, _ := NewVerifier(ledger)
	claim := mustClaim(t)
	claim.Generation = 7
	if _, err := v.Evaluate(claim); !errors.Is(err, ErrDigestTampered) {
		t.Errorf("expected ErrDigestTampered, got %v", err)
	}
}

func TestNewVerifier_NilLedger(t *testing.T) {
	if _, err := NewVerifier(nil); !errors.Is(err, ErrNilDependency) {
		t.Errorf("expected ErrNilDependency, got %v", err)
	}
}
