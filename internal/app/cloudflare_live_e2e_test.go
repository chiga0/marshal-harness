package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider/cloudflare"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file hosts the app-level live conformance E2E: the Cloudflare live
// conformance harness is wired through the app's production composition
// pieces (the Core-backed authority resolver, the durable file-backed state
// store and the durable effect authority sink) against an in-process minimal
// Bridge. The run is explicitly simulated, so it can never satisfy the live
// gate; no test connects to real Cloudflare infrastructure.

// liveBridge is the minimal in-process Bridge the app-level E2E drives: only
// the create and destroy endpoints the Provision/Terminate flow touches,
// plus the running observation the reconcile path may read.
type liveBridge struct {
	mu        sync.Mutex
	token     string
	next      int
	sandboxes map[string]bool
}

func newLiveBridge(t *testing.T, token string) *httptest.Server {
	t.Helper()
	fb := &liveBridge{token: token, sandboxes: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sandbox", fb.handleCreate)
	mux.HandleFunc("DELETE /v1/sandbox/{id}", fb.handleDestroy)
	mux.HandleFunc("GET /v1/sandbox/{id}/running", fb.handleRunning)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (fb *liveBridge) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+fb.token
}

func (fb *liveBridge) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !fb.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	fb.mu.Lock()
	fb.next++
	id := fmt.Sprintf("br-%d", fb.next)
	fb.sandboxes[id] = false
	fb.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
}

func (fb *liveBridge) handleDestroy(w http.ResponseWriter, r *http.Request) {
	if !fb.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	_, ok := fb.sandboxes[id]
	if !ok {
		fb.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	fb.sandboxes[id] = true
	fb.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (fb *liveBridge) handleRunning(w http.ResponseWriter, r *http.Request) {
	if !fb.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	destroyed, ok := fb.sandboxes[id]
	fb.mu.Unlock()
	if !ok || destroyed {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"running":true}`))
}

// appLiveIdentity builds one valid dispatch-bound identity for the app E2E.
func appLiveIdentity() sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       "task-live-app",
		RunId:        "run-live-app",
		AttemptId:    "attempt-live-app",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: "alloc-live-app",
		Generation:   1,
		FencingToken: "app-live-fencing",
		CommandId:    "cmd-provision",
	}
}

// appLiveHarness wires one harness config through the app production
// composition: the Core-backed authority resolver, the durable file-backed
// state store and the durable effect authority sink.
func appLiveHarness(t *testing.T, serverURL, token string, simulated bool) (cloudflare.LiveConformanceConfig, *cloudflare.FileEffectAuthoritySink) {
	t.Helper()
	namespace, providerDomain := validCloudflareAuthority()
	edgeRuntime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		t.Fatalf("NewEdgeRuntime: %v", err)
	}
	store, err := cloudflare.NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := cloudflare.NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatalf("NewSandboxRequirements: %v", err)
	}
	config := cloudflare.LiveConformanceConfig{
		BridgeBaseURL:     serverURL,
		BridgeToken:       token,
		ServiceIdentity:   namespace,
		ProviderDomain:    providerDomain,
		Simulated:         simulated,
		Identity:          appLiveIdentity(),
		Requirements:      requirements,
		StateStore:        store,
		AuthoritySink:     sink,
		AuthorityResolver: cloudflareAuthorityResolver{edgeRuntime: edgeRuntime, providerDomain: providerDomain},
		MaxRetries:        2,
		RetryDelay:        -1,
		RequestTimeout:    5 * time.Second,
	}
	return config, sink
}

// TestCloudflareLiveConformanceSimulatedE2E wires the harness through the
// app production composition and freezes that a simulated run completes with
// bound receipts and effect records, yet can never satisfy the live gate.
func TestCloudflareLiveConformanceSimulatedE2E(t *testing.T) {
	token := "cf-bridge" + "-fixture-token-live-app"
	server := newLiveBridge(t, token)
	config, sink := appLiveHarness(t, server.URL, token, true)

	result := cloudflare.NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != cloudflare.StatusSimulated {
		t.Fatalf("a simulated E2E must report simulated, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a simulated E2E must never satisfy the live gate")
	}
	if result.LeakScanScope != cloudflare.LeakScanScopeBookkeeping {
		t.Fatalf("leak scan scope = %q, want bookkeeping", result.LeakScanScope)
	}
	if result.ResourceCounts.Provisioned != 1 || result.ResourceCounts.Terminated != 1 || result.ResourceCounts.EffectRecords != 2 {
		t.Fatalf("resource counts = %+v, want one provision, one terminate and two effect records", result.ResourceCounts)
	}

	namespace, providerDomain := validCloudflareAuthority()
	records := sink.Records()
	if len(records) != 2 {
		t.Fatalf("exactly two effect records were expected, got %d", len(records))
	}
	for _, record := range records {
		if !record.Namespace.Equal(namespace) {
			t.Fatalf("the effect record must bind the app Core namespace")
		}
		if !record.Receipt.ActorProvenance.SecurityDomainId.Equal(providerDomain) {
			t.Fatalf("the effect receipt must bind the app provider actor domain")
		}
		if record.RunId != config.Identity.RunId || record.AttemptId != config.Identity.AttemptId || record.AllocationId != config.Identity.AllocationId {
			t.Fatalf("the effect record must bind the run/attempt/allocation identity, got %+v", record)
		}
	}
}

// TestCloudflareLiveConformanceUnavailableE2E freezes the unavailable path at
// the app boundary: a missing endpoint reports unavailable and never issues a
// network request.
func TestCloudflareLiveConformanceUnavailableE2E(t *testing.T) {
	config, _ := appLiveHarness(t, "", "cf-bridge"+"-fixture-token-live-app", false)
	result := cloudflare.NewLiveConformanceHarness(config).Run(context.Background())
	if result.Status != cloudflare.StatusUnavailable {
		t.Fatalf("a missing endpoint must report unavailable, got %q", result.Status)
	}
	if result.SatisfiesLiveGate() {
		t.Fatal("a missing endpoint must never satisfy the live gate")
	}
}
