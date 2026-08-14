package app_test

// sandbox_transport_test.go freezes the M9-d acceptance fixtures of the
// dispatch-bound protocol family: Push and Pull run the identical
// dual-topology conformance matrix with outcome/invariant equivalent
// traces; the cross-topology unique claim, the post-claim invalidation
// matrix (revoke/expire/incompatible/supersede/evidence revoke, ADR 0018
// §7), the deadline semantics, the late-result quarantine and the
// no-dual-active invariant hold under both topologies; the identical
// RunConformance fixtures pass parameterized by embedded, Push and Pull;
// and the ADR 0018 §12 transport security baseline composes onto the
// adapters from its first enable.
//
// This file is an external test package: internal/server imports
// internal/app, so only an external test package may compose the frozen
// internal/server/tls.go baseline (reference only, never a modification).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
	"github.com/chiga0/marshal-harness/internal/server"
)

// transportFixtureNow is the deterministic construction clock of the
// transport fixtures: deliberately far in the past so the one wall-clock
// read the durable lease ledger performs internally (the expire transition
// timestamp) always stays after every fixture createdAt, on any day.
var transportFixtureNow = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// newTransportCoreFixture builds one fresh transport Core over one
// temporary durable state root.
func newTransportCoreFixture(t *testing.T, providerInstance *sandbox.FakeProvider) *app.DispatchTransportCore {
	t.Helper()
	core, err := app.NewDispatchTransportCore(app.DispatchTransportConfig{
		StateRoot: t.TempDir(),
		Provider:  providerInstance,
		Now:       transportFixtureNow,
	})
	if err != nil {
		t.Fatalf("NewDispatchTransportCore: %v", err)
	}
	return core
}

// newTransportDualHarness parameterizes the one dual-topology conformance
// suite by topology over real transport adapters: every scenario run
// receives a fresh Core (fresh durable ledgers), the topology-parameterized
// fault specs apply identically to every topology's provider, and the
// Push/Pull bindings run over real loopback HTTP.
func newTransportDualHarness(t *testing.T, first, second sandbox.DualTopology) sandbox.DualSuiteHarness {
	newCore := func(scenario string) *app.DispatchTransportCore {
		providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
		if faults := sandbox.DualScenarioFaults(scenario); len(faults) > 0 {
			providerInstance = providerInstance.WithFaults(faults...)
		}
		return newTransportCoreFixture(t, providerInstance)
	}
	buildBinding := func(topology sandbox.DualTopology, scenario string, authoritySeam sandbox.DualAuthority) sandbox.DualTopologyBinding {
		core, ok := authoritySeam.(*app.DispatchTransportCore)
		if !ok {
			t.Fatalf("the transport harness requires a transport core authority, got %T", authoritySeam)
		}
		switch topology {
		case sandbox.TopologyEmbedded:
			return app.NewEmbeddedTopologyBinding(core)
		case sandbox.TopologyPush:
			providerServer := httptest.NewServer(app.NewDispatchProviderHandler(core.Provider()))
			t.Cleanup(providerServer.Close)
			coreServer := httptest.NewServer(core.Handler())
			t.Cleanup(coreServer.Close)
			return app.NewPushTopologyBinding(core, providerServer.URL, coreServer.URL, nil)
		case sandbox.TopologyPull:
			coreServer := httptest.NewServer(core.Handler())
			t.Cleanup(coreServer.Close)
			runner := app.NewPullRunner("runner-"+scenario, coreServer.URL, core.Provider(), nil)
			return app.NewPullTopologyBinding(core, runner)
		default:
			t.Fatalf("unknown topology %q", string(topology))
			return nil
		}
	}
	return sandbox.DualSuiteHarness{
		First:        first,
		Second:       second,
		NewAuthority: func(scenario string) sandbox.DualAuthority { return newCore(scenario) },
		NewBinding:   buildBinding,
		SharedAuthority: func(scenario string) sandbox.DualAuthority {
			return newTransportCoreFixture(t, sandbox.NewFakeProvider(sandbox.FakeConfig{}))
		},
	}
}

// TestDispatchProtocolFamilySingleVersion freezes that Push and Pull are
// adapters of ONE versioned protocol family: one family identity, one
// version — no topology derives its own protocol version.
func TestDispatchProtocolFamilySingleVersion(t *testing.T) {
	if app.DispatchProtocolFamily != "marshal-dispatch" {
		t.Fatalf("the dispatch protocol family identity changed: %q", app.DispatchProtocolFamily)
	}
	if app.DispatchProtocolVersion1 != "marshal-dispatch/1" {
		t.Fatalf("the dispatch protocol family version changed: %q", app.DispatchProtocolVersion1)
	}
}

// TestPushPullDualTopologyConformanceMatrix freezes the M9-d exit gate:
// Push and Pull run the identical frozen scenario matrix, every scenario
// passes, and the normalized business traces of the two topologies are
// outcome/invariant equivalent. The embedded family member pairs with both
// remote topologies under the identical verdict.
func TestPushPullDualTopologyConformanceMatrix(t *testing.T) {
	pairs := []struct {
		first, second sandbox.DualTopology
	}{
		{sandbox.TopologyPush, sandbox.TopologyPull},
		{sandbox.TopologyEmbedded, sandbox.TopologyPush},
		{sandbox.TopologyEmbedded, sandbox.TopologyPull},
	}
	for _, pair := range pairs {
		t.Run(string(pair.first)+"-vs-"+string(pair.second), func(t *testing.T) {
			verdicts, err := sandbox.RunDualTopologySuite(context.Background(), newTransportDualHarness(t, pair.first, pair.second))
			if err != nil {
				t.Fatalf("RunDualTopologySuite: %v", err)
			}
			if len(verdicts) != len(sandbox.DualScenarios()) {
				t.Fatalf("the suite must run the frozen scenario matrix once, got %d verdicts", len(verdicts))
			}
			for _, verdict := range verdicts {
				if !verdict.Passed {
					t.Fatalf("scenario %s failed under %s/%s: %s (first: %s; second: %s)",
						verdict.Scenario, string(pair.first), string(pair.second), verdict.Reason,
						verdict.First.Error, verdict.Second.Error)
				}
				if verdict.Scenario != sandbox.DualScenarioCrossTopologyUniqueClaim && !verdict.Equivalent {
					t.Fatalf("scenario %s produced divergent topology traces: %s", verdict.Scenario, verdict.Reason)
				}
			}
		})
	}
}

// TestCrossTopologyUniqueClaimOneSharedLedger freezes the cross-topology
// unique-claim invariant on one shared durable ledger: the identical
// capability requirement claimed through Push and Pull is accepted exactly
// once, and the contended claim carries the duplicate-claim reason class.
func TestCrossTopologyUniqueClaimOneSharedLedger(t *testing.T) {
	core := newTransportCoreFixture(t, sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	providerServer := httptest.NewServer(app.NewDispatchProviderHandler(core.Provider()))
	defer providerServer.Close()
	coreServer := httptest.NewServer(core.Handler())
	defer coreServer.Close()
	pushBinding := app.NewPushTopologyBinding(core, providerServer.URL, coreServer.URL, nil)
	pullRunner := app.NewPullRunner("runner-unique", coreServer.URL, core.Provider(), nil)
	pullBinding := app.NewPullTopologyBinding(core, pullRunner)
	ctx := context.Background()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	request := sandbox.DualClaimRequest{
		TaskId:       "task-unique",
		RunId:        "run-unique",
		AttemptId:    "attempt-unique",
		AllocationId: "alloc-unique",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-unique",
		Requirements: requirements,
	}
	firstReceipt, err := pushBinding.Claim(ctx, core, request)
	if err != nil {
		t.Fatalf("the push claim failed: %v", err)
	}
	if !firstReceipt.Outcome.Accepted {
		t.Fatalf("the first claim must be accepted, got %s (%s)", string(firstReceipt.Outcome.ReasonClass), firstReceipt.Outcome.Detail)
	}
	secondReceipt, err := pullBinding.Claim(ctx, core, request)
	if err != nil {
		t.Fatalf("the pull claim failed: %v", err)
	}
	if secondReceipt.Outcome.Accepted {
		t.Fatal("the identical capability requirement must never be accepted by two topologies")
	}
	if secondReceipt.Outcome.ReasonClass != sandbox.DualReasonDuplicateClaim {
		t.Fatalf("the contended claim must carry the duplicate-claim reason class, got %q", string(secondReceipt.Outcome.ReasonClass))
	}
	trace := core.Trace()
	accepted := 0
	rejected := 0
	for _, event := range trace {
		if event.Kind == sandbox.DualEventClaimAccepted {
			accepted++
		}
		if event.Kind == sandbox.DualEventClaimRejected && event.ReasonClass == sandbox.DualReasonDuplicateClaim {
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("the shared-ledger trace must carry exactly one accepted and one duplicate-rejected claim, got accepted=%d rejected=%d trace=%+v", accepted, rejected, trace)
	}
	if violations := sandbox.AssertDualBusinessInvariants(trace); len(violations) != 0 {
		t.Fatalf("the shared-ledger trace violates the business invariants: %+v", violations)
	}
}

// TestPostClaimInvalidationFixturesBothTopologies freezes the ADR 0018 §7
// post-claim invalidation matrix on both Push and Pull: the in-flight
// lease loses eligibility immediately, the terminal event carries the
// mapped closed reason class, the late result is quarantined, and the
// identical attempt can never be re-claimed.
func TestPostClaimInvalidationFixturesBothTopologies(t *testing.T) {
	cases := []struct {
		kind  sandbox.DualInvalidationKind
		class sandbox.DualReasonClass
	}{
		{sandbox.DualInvalidateRegistrationRevoke, sandbox.DualReasonRevoked},
		{sandbox.DualInvalidateRegistrationExpire, sandbox.DualReasonExpired},
		{sandbox.DualInvalidateRegistrationIncompatible, sandbox.DualReasonIncompatible},
		{sandbox.DualInvalidateSnapshotSupersede, sandbox.DualReasonSuperseded},
		{sandbox.DualInvalidateEvidenceRevoke, sandbox.DualReasonEvidenceRevoked},
	}
	topologies := []sandbox.DualTopology{sandbox.TopologyPush, sandbox.TopologyPull}
	for _, topology := range topologies {
		for _, testCase := range cases {
			t.Run(string(topology)+"-"+string(testCase.kind), func(t *testing.T) {
				core := newTransportCoreFixture(t, sandbox.NewFakeProvider(sandbox.FakeConfig{}))
				providerServer := httptest.NewServer(app.NewDispatchProviderHandler(core.Provider()))
				defer providerServer.Close()
				coreServer := httptest.NewServer(core.Handler())
				defer coreServer.Close()
				var binding sandbox.DualTopologyBinding
				switch topology {
				case sandbox.TopologyPush:
					binding = app.NewPushTopologyBinding(core, providerServer.URL, coreServer.URL, nil)
				case sandbox.TopologyPull:
					runner := app.NewPullRunner("runner-invalidation", coreServer.URL, core.Provider(), nil)
					binding = app.NewPullTopologyBinding(core, runner)
				}
				ctx := context.Background()
				requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
				if err != nil {
					t.Fatal(err)
				}
				request := sandbox.DualClaimRequest{
					TaskId:       "task-invalidation",
					RunId:        "run-invalidation",
					AttemptId:    "attempt-invalidation",
					AllocationId: "alloc-invalidation",
					WorkloadRole: sandbox.WorkloadRoleWorker,
					Principal:    "principal-invalidation",
					Requirements: requirements,
				}
				receipt, err := binding.Claim(ctx, core, request)
				if err != nil || !receipt.Outcome.Accepted {
					t.Fatalf("the claim must be accepted before the invalidation, got %+v (%v)", receipt.Outcome, err)
				}
				if err := core.Invalidate(ctx, testCase.kind); err != nil {
					t.Fatalf("Invalidate(%s): %v", string(testCase.kind), err)
				}
				// The in-flight heartbeat fails closed against the current
				// ledger.
				outcome, err := binding.Heartbeat(ctx, core, receipt.Lease)
				if err != nil {
					t.Fatalf("heartbeat: %v", err)
				}
				if outcome.Accepted {
					t.Fatal("a heartbeat after the invalidation must fail closed")
				}
				// The late result is quarantined, never admitted.
				outcome, err = binding.SubmitResult(ctx, core, receipt.Lease, "cmd-late", sandbox.RecomputeSHA256([]byte("late")))
				if err != nil {
					t.Fatalf("late result: %v", err)
				}
				if outcome.Accepted {
					t.Fatal("a late result after the invalidation must never be admitted")
				}
				if outcome.ReasonClass != sandbox.DualReasonLateResult {
					t.Fatalf("the late result must carry the late-result class, got %q", string(outcome.ReasonClass))
				}
				// The identical attempt can never be re-claimed.
				reclaim, err := binding.Claim(ctx, core, request)
				if err != nil {
					t.Fatalf("reclaim: %v", err)
				}
				if reclaim.Outcome.Accepted {
					t.Fatal("the identical attempt must never be re-claimed after the invalidation")
				}
				if reclaim.Outcome.ReasonClass != testCase.class {
					t.Fatalf("the re-claim rejection must carry the current-ledger class %q, got %q", string(testCase.class), string(reclaim.Outcome.ReasonClass))
				}
				// The trace carries the terminal lease event with the
				// mapped class.
				trace := core.Trace()
				var terminal *sandbox.DualTraceEvent
				for index, event := range trace {
					if event.Kind == sandbox.DualEventLeaseRevoked && event.LeaseId == receipt.Lease.LeaseId {
						terminal = &trace[index]
						break
					}
				}
				if terminal == nil {
					t.Fatalf("the invalidation must record lease-revoked for the in-flight lease, trace=%+v", trace)
				}
				if terminal.ReasonClass != testCase.class {
					t.Fatalf("lease-revoked carries class %q, want %q", string(terminal.ReasonClass), string(testCase.class))
				}
				if violations := sandbox.AssertDualBusinessInvariants(trace); len(violations) != 0 {
					t.Fatalf("the invalidation trace violates the business invariants: %+v", violations)
				}
			})
		}
	}
}

// TestConformanceSuiteParameterizedByTopology freezes that the identical
// RunConformance fixtures pass under the embedded, Push and Pull
// parameterizations of the dispatch-bound Port: one suite, parameterized
// by topology, never one suite per topology. The verdict traces agree on
// kind and outcome under every topology.
func TestConformanceSuiteParameterizedByTopology(t *testing.T) {
	fixture := sandbox.ConformanceFixture{
		Name:         "topology-parameter",
		Requirements: workspaceWriteRequirements(t),
		Payload:      []byte("topology-parameter-payload"),
	}
	runEmbedded := func(t *testing.T) sandbox.ConformanceVerdict {
		providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
		return sandbox.RunConformance(providerInstance, fixture)[0]
	}
	runPush := func(t *testing.T) sandbox.ConformanceVerdict {
		providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
		endpoint := httptest.NewServer(app.NewDispatchProviderHandler(providerInstance))
		defer endpoint.Close()
		adapter := app.NewPushSPIAdapter(endpoint.URL, nil)
		return sandbox.RunConformance(adapter, fixture)[0]
	}
	runPull := func(t *testing.T) sandbox.ConformanceVerdict {
		providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
		core := newTransportCoreFixture(t, providerInstance)
		coreServer := httptest.NewServer(core.Handler())
		defer coreServer.Close()
		runner := app.NewPullRunner("runner-spi", coreServer.URL, core.Provider(), nil)
		adapter := app.NewPullSPIAdapter(core, runner)
		return sandbox.RunConformance(adapter, fixture)[0]
	}
	embeddedVerdict := runEmbedded(t)
	pushVerdict := runPush(t)
	pullVerdict := runPull(t)
	for name, verdict := range map[string]sandbox.ConformanceVerdict{
		"embedded": embeddedVerdict,
		"push":     pushVerdict,
		"pull":     pullVerdict,
	} {
		if !verdict.Passed || verdict.ReasonCode != sandbox.ReasonOK {
			t.Fatalf("the %s parameterization must pass the identical conformance suite, got passed=%v reason=%q", name, verdict.Passed, verdict.ReasonCode)
		}
	}
	if len(pushVerdict.Trace) != len(embeddedVerdict.Trace) || len(pullVerdict.Trace) != len(embeddedVerdict.Trace) {
		t.Fatalf("the topology parameterizations must produce the identical verdict trace shape: embedded=%d push=%d pull=%d",
			len(embeddedVerdict.Trace), len(pushVerdict.Trace), len(pullVerdict.Trace))
	}
	for index := range embeddedVerdict.Trace {
		if pushVerdict.Trace[index].Kind != embeddedVerdict.Trace[index].Kind ||
			pushVerdict.Trace[index].Outcome != embeddedVerdict.Trace[index].Outcome {
			t.Fatalf("the push trace diverges at event %d: %+v vs %+v", index, pushVerdict.Trace[index], embeddedVerdict.Trace[index])
		}
		if pullVerdict.Trace[index].Kind != embeddedVerdict.Trace[index].Kind ||
			pullVerdict.Trace[index].Outcome != embeddedVerdict.Trace[index].Outcome {
			t.Fatalf("the pull trace diverges at event %d: %+v vs %+v", index, pullVerdict.Trace[index], embeddedVerdict.Trace[index])
		}
	}
}

func workspaceWriteRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	return requirements
}

// TestStaleFencingRejectedOverTheWire freezes that a stale-generation
// operation presented through the wire surface is rejected with the frozen
// sentinel before any provider side effect.
func TestStaleFencingRejectedOverTheWire(t *testing.T) {
	providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	endpoint := httptest.NewServer(app.NewDispatchProviderHandler(providerInstance))
	defer endpoint.Close()
	core := newTransportCoreFixture(t, providerInstance)
	coreServer := httptest.NewServer(core.Handler())
	defer coreServer.Close()
	pushBinding := app.NewPushTopologyBinding(core, endpoint.URL, coreServer.URL, nil)
	ctx := context.Background()
	receipt, err := pushBinding.Claim(ctx, core, sandbox.DualClaimRequest{
		TaskId:       "task-stale-wire",
		RunId:        "run-stale-wire",
		AttemptId:    "attempt-stale-wire",
		AllocationId: "alloc-stale-wire",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-stale-wire",
		Requirements: workspaceWriteRequirements(t),
	})
	if err != nil || !receipt.Outcome.Accepted {
		t.Fatalf("claim rejected: %+v (%v)", receipt.Outcome, err)
	}
	adapter := app.NewPushSPIAdapter(endpoint.URL, nil)
	staleIdentity := sandbox.OperationIdentity{
		TaskId:       receipt.Lease.TaskId,
		RunId:        receipt.Lease.RunId,
		AttemptId:    receipt.Lease.AttemptId,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: receipt.Lease.AllocationId,
		Generation:   receipt.Lease.Generation + 5,
		FencingToken: receipt.Lease.FencingToken,
		CommandId:    "stale-exec",
	}
	if _, err := adapter.Exec(ctx, sandbox.ExecRequest{Identity: staleIdentity, AllocationId: receipt.Lease.AllocationId, Command: []string{"stale"}}); !errors.Is(err, sandbox.ErrStaleAllocationGeneration) {
		t.Fatalf("a stale-generation operation over the wire must be rejected with the frozen sentinel, got %v", err)
	}
}

// TestWireEnvelopeRejectsDuplicateMembers freezes the ADR 0017 §11
// canonical admission of the protocol family: duplicate JSON members at any
// depth fail closed at the wire surface.
func TestWireEnvelopeRejectsDuplicateMembers(t *testing.T) {
	endpoint := httptest.NewServer(app.NewDispatchProviderHandler(sandbox.NewFakeProvider(sandbox.FakeConfig{})))
	defer endpoint.Close()
	body := `{"apiVersion":"marshal.dev/v1alpha1","apiVersion":"marshal.dev/v1alpha1","kind":"DispatchRequest","protocolVersion":"marshal-dispatch/1","operation":"spi","requestId":"r-dup"}`
	response, err := http.Post(endpoint.URL+"/marshal-dispatch/v1/spi", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "DispatchError") {
		t.Fatalf("a duplicate-member envelope must be rejected with a DispatchError envelope, got %s", string(raw))
	}
}

// TestPullRunnerIsOutboundOnly freezes the Pull runner discipline: the
// runner carries no listener or server surface — every interaction is an
// outbound request.
func TestPullRunnerIsOutboundOnly(t *testing.T) {
	runnerType := reflect.TypeOf(app.PullRunner{})
	for index := 0; index < runnerType.NumField(); index++ {
		field := runnerType.Field(index)
		lowered := strings.ToLower(field.Name)
		if strings.Contains(lowered, "listener") || strings.Contains(lowered, "server") {
			t.Fatalf("the pull runner must stay outbound-only, found field %s", field.Name)
		}
		if field.Type == reflect.TypeOf((*net.Listener)(nil)).Elem() {
			t.Fatalf("the pull runner must never hold a net.Listener, found field %s", field.Name)
		}
	}
	// The functional half: one outbound poll cycle executes a staged offer.
	core := newTransportCoreFixture(t, sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	coreServer := httptest.NewServer(core.Handler())
	defer coreServer.Close()
	runner := app.NewPullRunner("runner-outbound", coreServer.URL, core.Provider(), nil)
	binding := app.NewPullTopologyBinding(core, runner)
	receipt, err := binding.Claim(context.Background(), core, sandbox.DualClaimRequest{
		TaskId:       "task-outbound",
		RunId:        "run-outbound",
		AttemptId:    "attempt-outbound",
		AllocationId: "alloc-outbound",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-outbound",
		Requirements: workspaceWriteRequirements(t),
	})
	if err != nil || !receipt.Outcome.Accepted {
		t.Fatalf("the outbound-only claim must complete through poll/ack, got %+v (%v)", receipt.Outcome, err)
	}
}

// recordingHandler captures every request body served by one wire surface
// for the credential-hygiene assertions.
type recordingHandler struct {
	inner  http.Handler
	mu     chan struct{}
	bodies []string
}

func newRecordingHandler(inner http.Handler) *recordingHandler {
	return &recordingHandler{inner: inner, mu: make(chan struct{}, 1)}
}

func (handler *recordingHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err == nil {
		handler.mu <- struct{}{}
		handler.bodies = append(handler.bodies, string(body))
		<-handler.mu
		request.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	handler.inner.ServeHTTP(writer, request)
}

// TestCredentialsNeverEnterBusinessJSON freezes the credential hygiene of
// the protocol family: no business JSON crossing the wire carries
// credential-shaped material under any topology; the fencingToken is a
// non-credential stale-write guard and stays the only token-shaped field.
func TestCredentialsNeverEnterBusinessJSON(t *testing.T) {
	providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	providerRecorder := newRecordingHandler(app.NewDispatchProviderHandler(providerInstance))
	providerServer := httptest.NewServer(providerRecorder)
	defer providerServer.Close()
	core := newTransportCoreFixture(t, providerInstance)
	coreRecorder := newRecordingHandler(core.Handler())
	coreServer := httptest.NewServer(coreRecorder)
	defer coreServer.Close()
	pushBinding := app.NewPushTopologyBinding(core, providerServer.URL, coreServer.URL, nil)
	ctx := context.Background()
	receipt, err := pushBinding.Claim(ctx, core, sandbox.DualClaimRequest{
		TaskId:       "task-hygiene",
		RunId:        "run-hygiene",
		AttemptId:    "attempt-hygiene",
		AllocationId: "alloc-hygiene",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-hygiene",
		Requirements: workspaceWriteRequirements(t),
	})
	if err != nil || !receipt.Outcome.Accepted {
		t.Fatalf("claim rejected: %+v (%v)", receipt.Outcome, err)
	}
	executionId, outcome, err := pushBinding.StartExecution(ctx, core, receipt.Lease, "cmd-hygiene")
	if err != nil || !outcome.Accepted {
		t.Fatalf("exec start rejected: %+v (%v)", outcome, err)
	}
	digest, outcome, err := pushBinding.FinishExecution(ctx, core, receipt.Lease, "cmd-hygiene", executionId)
	if err != nil || !outcome.Accepted {
		t.Fatalf("exec finish rejected: %+v (%v)", outcome, err)
	}
	if outcome, err := pushBinding.SubmitResult(ctx, core, receipt.Lease, "cmd-hygiene", digest); err != nil || !outcome.Accepted {
		t.Fatalf("result admission rejected: %+v (%v)", outcome, err)
	}
	forbidden := []string{"credential", "secret", "privatekey", "password", "-----begin", "authorization"}
	for _, body := range append(append([]string{}, providerRecorder.bodies...), coreRecorder.bodies...) {
		lowered := strings.ToLower(body)
		for _, marker := range forbidden {
			if strings.Contains(lowered, marker) {
				t.Fatalf("business JSON carries credential-shaped material %q: %s", marker, body)
			}
		}
	}
	// The fencingToken (a non-credential stale-write guard) is the only
	// token-shaped field and must cross the wire on the offer.
	joined := strings.Join(providerRecorder.bodies, "\n")
	if !strings.Contains(joined, "fencingToken") {
		t.Fatal("the offer delivery must bind the lease fencingToken")
	}
}

// ---- ADR 0018 §12 transport security baseline composition ---------------

// transportPKI is the hermetic certificate fixture of the TLS baseline
// composition test (mirrors the internal/server/tls.go fixture shape).
type transportPKI struct {
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientKey     *ecdsa.PrivateKey
	clientTLS     *tls.Config
	caPEM         []byte
}

func newTransportPKI(t *testing.T) *transportPKI {
	t.Helper()
	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(24 * time.Hour)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dispatch-fixture-ca"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "dispatch-fixture-server"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "dispatch-fixture-provider"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("the fixture CA pool is unusable")
	}
	clientPair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: mustMarshalECKey(t, clientKey)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &transportPKI{
		serverCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		serverKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER}),
		clientKey:     clientKey,
		clientTLS: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{clientPair},
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		},
		caPEM: caPEM,
	}
}

func mustMarshalECKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// writeBaselineFiles persists the TLS baseline PEM files.
func (pki *transportPKI) writeBaselineFiles(t *testing.T) server.TLSBaseline {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "client-ca.pem")
	for path, data := range map[string][]byte{certFile: pki.serverCertPEM, keyFile: pki.serverKeyPEM, caFile: pki.caPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return server.TLSBaseline{CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile}
}

// fixtureSigner adapts the frozen server.SignRequest onto the transport
// signer seam.
type fixtureSigner struct {
	key *ecdsa.PrivateKey
}

func (signer fixtureSigner) SignRequest(method, path, timestamp, nonce string, body []byte) (string, error) {
	return server.SignRequest(signer.key, method, path, timestamp, nonce, body)
}

// serveTLSBaseline composes one TLS-only transport surface from one
// handler under the frozen internal/server baseline.
func serveTLSBaseline(t *testing.T, baseline server.TLSBaseline, handler http.Handler) string {
	t.Helper()
	transport, err := server.NewTransport("127.0.0.1:0", baseline, handler, server.NewReplayGuard(0, nil))
	if err != nil {
		t.Fatalf("the TLS baseline composition failed: %v", err)
	}
	if !transport.TLSEnabled {
		t.Fatal("the composed transport must be TLS-enabled")
	}
	httpServer := &http.Server{Handler: transport.Handler}
	go func() { _ = httpServer.Serve(transport.Listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return "https://" + transport.Listener.Addr().String()
}

// TestTLSBaselineComposesOntoThePushTransport freezes the ADR 0018 §12
// first-enable baseline: the Push adapter runs over mutual TLS with
// request-level replay protection composed from internal/server/tls.go
// (referenced only), and the full happy path completes under the baseline.
func TestTLSBaselineComposesOntoThePushTransport(t *testing.T) {
	pki := newTransportPKI(t)
	baseline := pki.writeBaselineFiles(t)
	providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	providerURL := serveTLSBaseline(t, baseline, app.NewDispatchProviderHandler(providerInstance))
	core := newTransportCoreFixture(t, providerInstance)
	coreURL := serveTLSBaseline(t, baseline, core.Handler())
	client := app.NewDispatchTransportClient(&http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: pki.clientTLS.Clone(),
		},
	}, fixtureSigner{key: pki.clientKey})
	pushBinding := app.NewPushTopologyBinding(core, providerURL, coreURL, client)
	ctx := context.Background()
	receipt, err := pushBinding.Claim(ctx, core, sandbox.DualClaimRequest{
		TaskId:       "task-tls",
		RunId:        "run-tls",
		AttemptId:    "attempt-tls",
		AllocationId: "alloc-tls",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-tls",
		Requirements: workspaceWriteRequirements(t),
	})
	if err != nil || !receipt.Outcome.Accepted {
		t.Fatalf("the claim over mutual TLS failed: %+v (%v)", receipt.Outcome, err)
	}
	executionId, outcome, err := pushBinding.StartExecution(ctx, core, receipt.Lease, "cmd-tls")
	if err != nil || !outcome.Accepted {
		t.Fatalf("exec start over mutual TLS failed: %+v (%v)", outcome, err)
	}
	digest, outcome, err := pushBinding.FinishExecution(ctx, core, receipt.Lease, "cmd-tls", executionId)
	if err != nil || !outcome.Accepted {
		t.Fatalf("exec finish failed: %+v (%v)", outcome, err)
	}
	outcome, err = pushBinding.SubmitResult(ctx, core, receipt.Lease, "cmd-tls", digest)
	if err != nil || !outcome.Accepted {
		t.Fatalf("result admission over mutual TLS failed: %+v (%v)", outcome, err)
	}
	if violations := sandbox.AssertDualBusinessInvariants(core.Trace()); len(violations) != 0 {
		t.Fatalf("the TLS-baseline trace violates the business invariants: %+v", violations)
	}
}

// TestTLSBaselineFailsClosed freezes that the baseline composition never
// degrades: an incomplete baseline and a non-loopback listen without a
// baseline both fail closed.
func TestTLSBaselineFailsClosed(t *testing.T) {
	pki := newTransportPKI(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	if err := os.WriteFile(certFile, pki.serverCertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := app.NewDispatchProviderHandler(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	if _, err := server.NewTransport("127.0.0.1:0", server.TLSBaseline{CertFile: certFile}, handler, nil); err == nil {
		t.Fatal("an incomplete TLS baseline must fail closed")
	}
	if _, err := server.NewTransport("192.0.2.10:0", server.TLSBaseline{}, handler, nil); err == nil {
		t.Fatal("a non-loopback transport without the complete TLS baseline must fail closed")
	}
}

// TestLeaseLedgerDurableAcrossReopen freezes that the M9-a durable lease
// ledger backing the transport Core survives a reopen: the accepted claim
// rebuilds from the ledger and the single-active binding is never lost.
func TestLeaseLedgerDurableAcrossReopen(t *testing.T) {
	stateRoot := t.TempDir()
	providerInstance := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	core, err := app.NewDispatchTransportCore(app.DispatchTransportConfig{
		StateRoot: stateRoot,
		Provider:  providerInstance,
		Now:       transportFixtureNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreServer := httptest.NewServer(core.Handler())
	defer coreServer.Close()
	binding := app.NewEmbeddedTopologyBinding(core)
	receipt, err := binding.Claim(context.Background(), core, sandbox.DualClaimRequest{
		TaskId:       "task-durable",
		RunId:        "run-durable",
		AttemptId:    "attempt-durable",
		AllocationId: "alloc-durable",
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "principal-durable",
		Requirements: workspaceWriteRequirements(t),
	})
	if err != nil || !receipt.Outcome.Accepted {
		t.Fatalf("claim rejected: %+v (%v)", receipt.Outcome, err)
	}
	reopened, err := dispatch.NewLeaseLedger(filepath.Join(stateRoot, "leases"))
	if err != nil {
		t.Fatalf("reopen the durable lease ledger: %v", err)
	}
	lease, state, generation, err := reopened.Current(receipt.Lease.LeaseId)
	if err != nil {
		t.Fatalf("the reopened ledger must carry the accepted claim: %v", err)
	}
	if state != dispatch.LeaseStateClaimed || generation != 1 || lease.RunId != "run-durable" {
		t.Fatalf("the reopened lease diverged: state=%s generation=%d runId=%s", string(state), generation, lease.RunId)
	}
}

// TestFaultInjectionTopologyParameterized freezes that the fault scenarios
// inject the identical fault specs under every topology and stay
// outcome/invariant equivalent: fault injection is additive over the
// existing internal/sandbox fault kinds.
func TestFaultInjectionTopologyParameterized(t *testing.T) {
	verdicts, err := sandbox.RunDualTopologySuite(context.Background(), newTransportDualHarness(t, sandbox.TopologyPush, sandbox.TopologyPull))
	if err != nil {
		t.Fatal(err)
	}
	for _, verdict := range verdicts {
		switch verdict.Scenario {
		case sandbox.DualScenarioFaultDelayExec, sandbox.DualScenarioFaultRejectExec, sandbox.DualScenarioFaultDropProvisionResponse:
			if !verdict.Passed || !verdict.Equivalent {
				t.Fatalf("fault scenario %s must pass equivalent under both topologies: %s", verdict.Scenario, verdict.Reason)
			}
		}
	}
}
