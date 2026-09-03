package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file hosts the truthful live conformance harness tests: the closed
// live/simulated/unavailable distinction, the live gate, the unavailable
// (never-fabricated) path, the simulated run with precise receipt/effect
// binding, the post-termination leak scan and repeated reconcile, and the
// credential/URL/path redaction negatives. No test connects to real
// Cloudflare infrastructure; the simulated path drives the in-process fake
// Bridge under an explicit Simulated flag, so it can never satisfy the live
// gate.

// liveConformanceFixture builds one harness config wired to a durable
// production composition (file-backed store, durable sink, Core-backed
// resolver) against the in-process fake Bridge.
func liveConformanceFixture(t *testing.T, simulated bool) (LiveConformanceConfig, *fakeBridge, *FileStateStore, *FileEffectAuthoritySink) {
	t.Helper()
	fb := newFakeBridge(t, testBridgeToken("live"))
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	config := LiveConformanceConfig{
		BridgeBaseURL:     fb.server.URL,
		BridgeToken:       fb.token,
		ServiceIdentity:   testEffectAuthorityContext().Namespace,
		ProviderDomain:    testEffectAuthorityContext().ProviderSecurityDomain,
		Simulated:         simulated,
		Identity:          scenarioIdentity("live", "alloc-live", "cmd-provision", 1),
		Requirements:      workspaceRequirements(t),
		StateStore:        store,
		AuthoritySink:     sink,
		AuthorityResolver: staticTestResolver{},
		MaxRetries:        2,
		RetryDelay:        -1,
		RequestTimeout:    5 * time.Second,
	}
	return config, fb, store, sink
}

// TestLiveConformanceStatusClosed freezes the closed three-state distinction:
// live, simulated and unavailable are the only valid members, and only live
// satisfies the live gate.
func TestLiveConformanceStatusClosed(t *testing.T) {
	cases := []struct {
		status LiveConformanceStatus
		valid  bool
		gate   bool
	}{
		{StatusLive, true, true},
		{StatusSimulated, true, false},
		{StatusUnavailable, true, false},
		{"", false, false},
		{"forged", false, false},
		{"LIVE", false, false},
	}
	for _, tc := range cases {
		err := tc.status.Validate()
		if tc.valid && err != nil {
			t.Fatalf("status %q must validate, got %v", tc.status, err)
		}
		if !tc.valid && err == nil {
			t.Fatalf("status %q must be rejected", tc.status)
		}
		if tc.status.SatisfiesLiveGate() != tc.gate {
			t.Fatalf("status %q gate = %t, want %t", tc.status, tc.status.SatisfiesLiveGate(), tc.gate)
		}
	}
}

// TestLiveConformanceResultLiveGate freezes that a simulated or unavailable
// result can never satisfy the live gate; only a live result does.
func TestLiveConformanceResultLiveGate(t *testing.T) {
	for _, status := range []LiveConformanceStatus{StatusLive, StatusSimulated, StatusUnavailable} {
		result := LiveConformanceResult{Status: status}
		if result.SatisfiesLiveGate() != (status == StatusLive) {
			t.Fatalf("result %q gate = %t", status, result.SatisfiesLiveGate())
		}
	}
}

// TestLiveConformanceUnavailableWithoutEndpoint freezes the never-fabricated
// path: a missing endpoint reports unavailable, never live, never simulated.
func TestLiveConformanceUnavailableWithoutEndpoint(t *testing.T) {
	config, _, _, _ := liveConformanceFixture(t, false)
	config.BridgeBaseURL = ""
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusUnavailable {
		t.Fatalf("a missing endpoint must report unavailable, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a missing endpoint must never satisfy the live gate")
	}
	if result.Summary != summaryUnavailableNoEndpoint {
		t.Fatalf("summary = %q, want the fixed unavailable summary", result.Summary)
	}
}

// TestLiveConformanceUnavailableWithoutCredential freezes that a configured
// endpoint with no credentialed executor reports unavailable and issues no
// network request to the endpoint.
func TestLiveConformanceUnavailableWithoutCredential(t *testing.T) {
	config, fb, _, _ := liveConformanceFixture(t, false)
	config.BridgeToken = ""
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusUnavailable {
		t.Fatalf("a missing credentialed executor must report unavailable, got %q", result.Status)
	}
	if result.Summary != summaryUnavailableNoEndpoint {
		t.Fatalf("summary = %q, want the fixed unavailable summary", result.Summary)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("a missing credentialed executor must issue no network request, got %d", got)
	}
}

// TestLiveConformanceUnavailableWithoutServiceIdentity freezes that a
// missing or invalid non-sensitive service identity reports unavailable
// before any construction or network request.
func TestLiveConformanceUnavailableWithoutServiceIdentity(t *testing.T) {
	config, fb, _, _ := liveConformanceFixture(t, false)
	config.ServiceIdentity = authority.AuthorityNamespaceId{}
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusUnavailable {
		t.Fatalf("a missing service identity must report unavailable, got %q", result.Status)
	}
	if result.Summary != summaryUnavailableNoServiceIdentity {
		t.Fatalf("summary = %q, want the fixed no-service-identity summary", result.Summary)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("a missing service identity must issue no network request, got %d", got)
	}
}

// TestLiveConformanceUnavailableInvalidIdentity freezes that an invalid
// run/attempt/allocation identity reports unavailable before any side effect.
func TestLiveConformanceUnavailableInvalidIdentity(t *testing.T) {
	config, fb, _, _ := liveConformanceFixture(t, false)
	config.Identity.RunId = ""
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusUnavailable {
		t.Fatalf("an invalid identity must report unavailable, got %q", result.Status)
	}
	if result.Summary != summaryUnavailableInvalidIdentity {
		t.Fatalf("summary = %q, want the fixed invalid-identity summary", result.Summary)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("an invalid identity must issue no network request, got %d", got)
	}
}

// TestLiveConformanceSimulatedRun freezes the full simulated flow through the
// production composition: provision/terminate receipts bound to the identity,
// durable effect records bound to the service identity, a clean repeated
// reconcile and a zero-known-leak bookkeeping scan — all reported as
// simulated, which can never satisfy the live gate.
func TestLiveConformanceSimulatedRun(t *testing.T) {
	config, fb, _, sink := liveConformanceFixture(t, true)
	result := NewLiveConformanceHarness(config).Run(context.Background())

	if result.Status != StatusSimulated {
		t.Fatalf("a simulated run must report simulated, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a simulated run must never satisfy the live gate")
	}
	if result.Summary != summarySimulatedPassed {
		t.Fatalf("summary = %q, want the fixed simulated-pass summary", result.Summary)
	}
	if result.LeakScanScope != LeakScanScopeBookkeeping {
		t.Fatalf("leak scan scope = %q, want bookkeeping", result.LeakScanScope)
	}
	if result.LeakScanScope == LeakScanScopeGlobal {
		t.Fatal("the Bridge harness must never claim a global leak scan scope")
	}
	want := ResourceCounts{
		Provisioned:       1,
		Terminated:        1,
		EffectRecords:     2,
		ActiveAllocations: 0,
		OrphanAllocations: 0,
		PendingIntents:    0,
		ReconcilePasses:   reconcilePasses,
	}
	if result.ResourceCounts != want {
		t.Fatalf("resource counts = %+v, want %+v", result.ResourceCounts, want)
	}
	if got := fb.activeSandboxCount(); got != 0 {
		t.Fatalf("the simulated terminate must leave no undestroyed bridge sandbox, got %d", got)
	}

	records := sink.Records()
	if len(records) != 2 {
		t.Fatalf("exactly one provision and one terminate effect were expected, got %d", len(records))
	}
	if records[0].Operation != sandbox.OperationProvision || records[1].Operation != sandbox.OperationTerminate {
		t.Fatalf("effect records must observe provision then terminate, got %q then %q", records[0].Operation, records[1].Operation)
	}
	for _, record := range records {
		if !effectRecordBinds(record, config.Identity, testEffectAuthorityContext()) {
			t.Fatalf("effect record %q must bind to the configured identity and service identity", record.Operation)
		}
	}

	// The closed output carries no credential, URL, path or environment
	// value.
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	assertNoCredential(t, fb.token, result.Summary, string(raw))
	if strings.Contains(string(raw), fb.server.URL) {
		t.Fatalf("the result JSON leaked the bridge endpoint URL: %s", string(raw))
	}
}

// TestLiveConformanceServiceIdentityMismatch freezes the precise service
// identity binding: a declared namespace that diverges from the resolved Core
// authority context fails closed before any remote side effect.
func TestLiveConformanceServiceIdentityMismatch(t *testing.T) {
	config, fb, _, _ := liveConformanceFixture(t, false)
	config.ServiceIdentity = authority.AuthorityNamespaceId{
		TenantNamespace:  "cloudflare",
		ControlPlaneId:   "default",
		AuthorityScopeId: "other-scope",
	}
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusUnavailable {
		t.Fatalf("a service identity mismatch must report unavailable, got %q", result.Status)
	}
	if !strings.Contains(result.Summary, "service-identity-mismatch") {
		t.Fatalf("summary = %q, want the service-identity-mismatch reason", result.Summary)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("a service identity mismatch must fail before any network request, got %d", got)
	}
}

// TestLiveConformanceLeakScanDetectsPendingIntent freezes the post-termination
// leak scan: a durable pending intent in the bound scope is detected as a
// leak and the run fails closed instead of reporting clean.
func TestLiveConformanceLeakScanDetectsPendingIntent(t *testing.T) {
	config, _, store, _ := liveConformanceFixture(t, true)
	if err := store.RecordIntent(CreateIntent{
		ReplayKey:    "orphan-create-key",
		AllocationId: "alloc-orphan",
		RunId:        config.Identity.RunId,
		AttemptId:    config.Identity.AttemptId,
		Generation:   1,
	}); err != nil {
		t.Fatalf("seed pending intent: %v", err)
	}
	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusSimulated {
		t.Fatalf("a simulated leak must report simulated, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a leaking simulated run must never satisfy the live gate")
	}
	if !strings.Contains(result.Summary, "leak-scan-drift") {
		t.Fatalf("summary = %q, want the leak-scan-drift reason", result.Summary)
	}
	if result.ResourceCounts.PendingIntents < 1 && result.ResourceCounts.OrphanAllocations < 1 {
		t.Fatalf("the leak scan must observe the pending intent, got counts %+v", result.ResourceCounts)
	}
	// The seeded intent is never resolved by the harness, so it stays pending
	// after the run: the bookkeeping-scoped scan reported the ambiguity
	// rather than a fabricated zero-leak.
	if _, ok := store.Intent("orphan-create-key"); !ok {
		t.Fatal("the harness must not silently resolve an unrelated pending intent")
	}
}

// TestLiveConformanceDivergentEffectRecordFailsClosed freezes that a durable
// sink already carrying a divergent record under the same effect id makes the
// run fail closed — never a fabricated pass that overwrites the divergence.
func TestLiveConformanceDivergentEffectRecordFailsClosed(t *testing.T) {
	config, _, _, sink := liveConformanceFixture(t, true)
	seed := sandbox.SandboxAllocation{
		AllocationId:   config.Identity.AllocationId,
		RunId:          "run-seed",
		AttemptId:      "attempt-seed",
		Generation:     1,
		State:          sandbox.AllocationActive,
		AccessMode:     domain.AccessModeWorkspaceWrite,
		AssuranceLevel: domain.AssuranceLevelWorkspaceWrite,
	}
	seedRecord, err := NewEffectAuthorityRecord(testEffectAuthorityContext(), seed, sandbox.WorkloadRoleWorker, "br-seed", "cmd-seed", sandbox.OperationProvision)
	if err != nil {
		t.Fatalf("seed effect record: %v", err)
	}
	if err := sink.PersistEffectAuthority(seedRecord); err != nil {
		t.Fatalf("persist seed effect record: %v", err)
	}

	result := NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != StatusSimulated {
		t.Fatalf("a divergent effect record must fail the simulated run closed, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a failed simulated run must never satisfy the live gate")
	}
	if !strings.Contains(result.Summary, "effect-record-invalid") {
		t.Fatalf("summary = %q, want the effect-record-invalid reason", result.Summary)
	}
}

// TestLiveConformanceClassifyErrorRedacts freezes the error classification
// redaction: the classifier returns fixed closed reason codes and never
// splices a credential or endpoint into a reason.
func TestLiveConformanceClassifyErrorRedacts(t *testing.T) {
	token := testBridgeToken("redact")
	if got := classifyLiveError(fmt.Errorf("wrap: %w", ErrCredentialRejected)); got != "credential-rejected" {
		t.Fatalf("credential rejection must classify to credential-rejected, got %q", got)
	}
	if got := classifyLiveError(fmt.Errorf("wrap: %w", ErrProductionConfigInvalid)); got != "production-config-invalid" {
		t.Fatalf("production config failure must classify to production-config-invalid, got %q", got)
	}
	if got := classifyLiveError(errors.New("unexpected " + token)); got != "provider-error" {
		t.Fatalf("an unclassified error must classify to provider-error, got %q", got)
	}
	for _, reason := range []string{
		classifyLiveError(errors.New("unexpected " + token)),
		classifyLiveError(fmt.Errorf("wrap: %w", ErrCredentialRejected)),
		classifyLiveError(fmt.Errorf("wrap: %w", ErrBridgeUnavailable)),
	} {
		assertNoCredential(t, token, reason)
	}
}
