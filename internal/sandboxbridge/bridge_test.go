package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processcontrol"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

type fakeAdapter struct {
	id      string
	calls   int
	failErr error
	lastReq domain.Record
}

type fakeProductionAdapter struct {
	*fakeAdapter
	plan       LaunchPlan
	prepares   int
	preflights int
}

func (a *fakeProductionAdapter) ProductionLaunchProfileID() string {
	return launchidentity.Pi0843DarwinARM64Profile
}
func (a *fakeProductionAdapter) PrepareLaunch(context.Context, domain.Record) (LaunchPlan, error) {
	a.prepares++
	return a.plan, nil
}
func (a *fakeProductionAdapter) PreflightLaunch(context.Context, domain.Record) (LaunchPlan, error) {
	a.preflights++
	return a.plan, nil
}
func (a *fakeProductionAdapter) CompleteLaunch(context.Context, LaunchPlan, []byte, bool, []byte, time.Time, time.Time, int, string, error) (domain.Record, error) {
	return domain.Record{}, errors.New("must not complete")
}

type countingProvider struct {
	sandbox.SandboxProvider
	provisions int
	stages     int
	execs      int
}

type exactLeaseAuthority struct {
	lease dispatch.DispatchLease
	ok    bool
}

func (*exactLeaseAuthority) RegistrationStore() *provider.RegistrationStore { return nil }
func (a *exactLeaseAuthority) LeaseFor(string, string) (dispatch.DispatchLease, bool) {
	return a.lease, a.ok
}
func (*exactLeaseAuthority) CapabilitySnapshot() provider.ProviderCapabilitySnapshot {
	return provider.ProviderCapabilitySnapshot{}
}
func (*exactLeaseAuthority) Registration() provider.ProviderRegistration {
	return provider.ProviderRegistration{}
}
func (*exactLeaseAuthority) AgentAuthority(string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error) {
	return agentregistry.AgentRegistration{}, agentregistry.AgentCapabilitySnapshot{}, errors.New("unused")
}

func (p *countingProvider) Provision(ctx context.Context, request sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	p.provisions++
	return p.SandboxProvider.Provision(ctx, request)
}
func (p *countingProvider) Stage(ctx context.Context, request sandbox.StageRequest) (*sandbox.StageReport, error) {
	p.stages++
	return p.SandboxProvider.Stage(ctx, request)
}
func (p *countingProvider) Exec(ctx context.Context, request sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	p.execs++
	return p.SandboxProvider.Exec(ctx, request)
}

func (a *fakeAdapter) ID() string { return a.id }
func (a *fakeAdapter) Probe(context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: []byte(`{"fake":true}`)}, nil
}
func (a *fakeAdapter) Run(_ context.Context, request domain.Record) (domain.Record, error) {
	a.calls++
	a.lastReq = request
	if a.failErr != nil {
		return domain.Record{}, a.failErr
	}
	var view workerRequestView
	if err := json.Unmarshal(request.Data, &view); err != nil {
		return domain.Record{}, err
	}
	result := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "WorkerResult",
		"taskId":     view.TaskID,
		"runId":      view.RunID,
		"attemptId":  view.AttemptID,
		"adapter":    map[string]any{"id": a.id},
		"summary":    "ok",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: raw}, nil
}

func validRequest(t *testing.T) domain.Record {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"taskId":                        "T1",
		"runId":                         "R1",
		"attemptId":                     "A1",
		"specDigest":                    "sha256:" + strings.Repeat("1", 64),
		"policyDigest":                  "sha256:" + strings.Repeat("2", 64),
		"capabilityDigest":              "sha256:" + strings.Repeat("3", 64),
		"agentRegistrationId":           "registration:" + strings.Repeat("3", 32),
		"agentCapabilitySnapshotDigest": "sha256:" + strings.Repeat("4", 64),
		"worktreePath":                  "/tmp/worktree",
		"executionProfile":              "workspace-write",
		"sessionPolicy":                 "ephemeral",
		"adapterId":                     "fake",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: raw}
}

func mustParseView(t *testing.T) workerRequestView {
	t.Helper()
	view, err := parseRequest(validRequest(t))
	if err != nil {
		t.Fatalf("mustParseView: %v", err)
	}
	return view
}

func sealExactLease(t *testing.T) dispatch.DispatchLease {
	t.Helper()
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	lease := dispatch.DispatchLease{
		LeaseId: digest("lease"),
		AuthorityNamespaceId: authority.AuthorityNamespaceId{
			TenantNamespace: "tenant", ControlPlaneId: "control", AuthorityScopeId: "scope",
		},
		SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "isolation",
		},
		RegistrationId: "registration:local", ProviderCapabilitySnapshotDigest: digest("snapshot"),
		ConformanceEvidenceDigests: []string{digest("evidence")},
		Attestation:                provider.Attestation{ProviderInstanceId: "provider:local", ConfigDigest: digest("config"), TrustRootKeyId: "root", TrustRootAlgorithm: "ed25519"},
		TaskId:                     "T1", RunId: "R1", AttemptId: "A1", AllocationId: "allocation-1", Generation: 1,
		AckDeadlineAt: "2026-08-29T02:00:00Z", ExpiresAt: "2026-08-29T03:00:00Z", LeaseState: dispatch.LeaseStateClaimed, CreatedAt: "2026-08-29T01:00:00Z",
	}
	canonicalDigest := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		canonicalRaw, err := canonical.JSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		return canonical.DigestBytes(canonicalRaw)
	}
	detached := lease
	detached.FencingToken = ""
	detached.LeaseDigest = ""
	lease.FencingToken = canonicalDigest(detached)
	detached = lease
	detached.LeaseDigest = ""
	lease.LeaseDigest = canonicalDigest(detached)
	if err := lease.Validate(); err != nil {
		t.Fatalf("sealed lease invalid: %v", err)
	}
	return lease
}

func exactAttemptIdentity(lease dispatch.DispatchLease) resultingress.AttemptIdentity {
	return resultingress.AttemptIdentity{
		AuthorityNamespaceID: lease.AuthorityNamespaceId, AuthorityNamespaceRef: "authority:test",
		TaskID: lease.TaskId, RunID: lease.RunId, AttemptID: lease.AttemptId, AllocationID: lease.AllocationId,
		LeaseID: lease.LeaseId, LeaseDigest: lease.LeaseDigest, DispatchGeneration: lease.Generation,
		FencingTokenDigest: canonical.DigestBytes([]byte(lease.FencingToken)), OrchestratorID: "orchestrator:test",
		RunAuthorityDigest: canonical.DigestBytes([]byte("run-authority")),
	}
}

func TestRequireExactLeaseRejectsEveryAttemptIdentityMismatchBeforeEffects(t *testing.T) {
	lease := sealExactLease(t)
	base := exactAttemptIdentity(lease)
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	tests := []struct {
		name   string
		mutate func(*resultingress.AttemptIdentity)
	}{
		{name: "authority namespace", mutate: func(id *resultingress.AttemptIdentity) { id.AuthorityNamespaceID.AuthorityScopeId = "other" }},
		{name: "task", mutate: func(id *resultingress.AttemptIdentity) { id.TaskID = "T2" }},
		{name: "run", mutate: func(id *resultingress.AttemptIdentity) { id.RunID = "R2" }},
		{name: "attempt", mutate: func(id *resultingress.AttemptIdentity) { id.AttemptID = "A2" }},
		{name: "allocation", mutate: func(id *resultingress.AttemptIdentity) { id.AllocationID = "allocation-2" }},
		{name: "lease id", mutate: func(id *resultingress.AttemptIdentity) { id.LeaseID = digest("other-lease") }},
		{name: "lease digest", mutate: func(id *resultingress.AttemptIdentity) { id.LeaseDigest = digest("other-lease-digest") }},
		{name: "generation", mutate: func(id *resultingress.AttemptIdentity) { id.DispatchGeneration++ }},
		{name: "fencing digest", mutate: func(id *resultingress.AttemptIdentity) { id.FencingTokenDigest = digest("other-fencing") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := base
			test.mutate(&identity)
			providerCalls := &countingProvider{SandboxProvider: sandbox.NewFakeProvider(sandbox.FakeConfig{})}
			worker := &fakeProductionAdapter{fakeAdapter: &fakeAdapter{id: "fake"}}
			bridge, err := NewBridge(providerCalls)
			if err != nil {
				t.Fatal(err)
			}
			bridge.authority = &exactLeaseAuthority{lease: lease, ok: true}
			_, err = bridge.requireExactLease(mustParseView(t), &exactProcessAdmission{authority: DurableProcessAuthority{Identity: identity}})
			if !errors.Is(err, launchidentity.ErrUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if worker.preflights > 1 || worker.prepares != 0 || worker.calls != 0 || providerCalls.provisions != 0 || providerCalls.stages != 0 || providerCalls.execs != 0 {
				t.Fatalf("side effects: preflight=%d prepare=%d run=%d provision=%d stage=%d exec=%d", worker.preflights, worker.prepares, worker.calls, providerCalls.provisions, providerCalls.stages, providerCalls.execs)
			}
		})
	}
	bridge, _ := NewBridge(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	if _, err := bridge.requireExactLease(mustParseView(t), &exactProcessAdmission{authority: DurableProcessAuthority{Identity: base}}); !errors.Is(err, launchidentity.ErrUnavailable) {
		t.Fatalf("nil durable authority error=%v", err)
	}
}

func TestRunWorker_HappyPathAllocatesAndTerminates(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, err := NewBridge(provider)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	adapter := &fakeAdapter{id: "fake"}

	record, err := bridge.runWorkerLegacy(context.Background(), adapter, validRequest(t), mustParseView(t))
	if err != nil {
		t.Fatalf("runWorkerLegacy: %v", err)
	}
	if adapter.calls != 1 {
		t.Errorf("adapter must be invoked exactly once, got %d", adapter.calls)
	}
	if record.Kind != domain.KindWorkerResult {
		t.Errorf("expected WorkerResult passthrough, got %q", record.Kind)
	}
	var identityOut struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(record.Data, &identityOut); err != nil || identityOut.TaskID != "T1" {
		t.Errorf("result record must pass through unchanged: %v / %+v", err, identityOut)
	}
}

func TestRunWorker_StageIsContentAddressed(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, _ := NewBridge(provider)
	adapter := &fakeAdapter{id: "fake"}
	if _, err := bridge.runWorkerLegacy(context.Background(), adapter, validRequest(t), mustParseView(t)); err != nil {
		t.Fatalf("runWorkerLegacy: %v", err)
	}
	// Fake Provider 的 Stage 在 declared digest 与内容不一致时直接失败；
	// 成功通过即证明入账 digest 与请求字节一致（content addressing 成立）。
	if adapter.calls != 1 {
		t.Errorf("stage success implies digest match; adapter calls = %d", adapter.calls)
	}
}

func TestRunWorker_AdapterMismatch(t *testing.T) {
	bridge, _ := NewBridge(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	adapter := &fakeAdapter{id: "different"}
	_, err := bridge.RunWorker(context.Background(), adapter, validRequest(t))
	if err == nil || !strings.Contains(err.Error(), "sandboxbridge: ") {
		t.Errorf("expected sandboxbridge-prefixed mismatch error, got %v", err)
	}
	if adapter.calls != 0 {
		t.Errorf("adapter must not run on identity mismatch")
	}
}

func TestRunWorker_ProductionGateRejectsLegacyBeforeAllocationOrRun(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, err := NewBridge(provider)
	if err != nil {
		t.Fatal(err)
	}
	bridge.WithProductionGate()
	bridge.WithTranscriptSource(func(string, string) ([]byte, error) { return nil, nil })
	bridge.WithExactProcessRuntime(ExactProcessRuntime{
		Resolve: func(context.Context, ExactProcessAttempt) (*processcontrol.Coordinator, DurableProcessAuthority, error) {
			return nil, DurableProcessAuthority{}, launchidentity.ErrUnavailable
		},
		Retain: func(ExactProcessAttempt, *processcontrol.Process, error) {},
	})
	worker := &fakeAdapter{id: "fake"}
	_, err = bridge.RunWorker(context.Background(), worker, validRequest(t))
	if err == nil || !strings.Contains(err.Error(), "exact production launch profile") {
		t.Fatalf("production gate error = %v", err)
	}
	if worker.calls != 0 {
		t.Fatalf("legacy adapter ran %d times", worker.calls)
	}
	// No allocation was created: the same deterministic identity remains free
	// for the explicitly invoked compatibility implementation.
	if _, err := bridge.runWorkerLegacy(context.Background(), worker, validRequest(t), mustParseView(t)); err != nil {
		t.Fatalf("production rejection left an allocation side effect: %v", err)
	}
}

func TestProductionGateRejectsBeforeProviderSideEffects(t *testing.T) {
	tests := []struct {
		name        string
		withRuntime bool
	}{
		{name: "missing exact runtime"},
		{name: "attempt resolver unavailable", withRuntime: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &countingProvider{SandboxProvider: sandbox.NewFakeProvider(sandbox.FakeConfig{})}
			bridge, err := NewBridge(provider)
			if err != nil {
				t.Fatal(err)
			}
			bridge.WithProductionGate().WithTranscriptSource(func(string, string) ([]byte, error) { return nil, nil })
			if test.withRuntime {
				bridge.WithExactProcessRuntime(ExactProcessRuntime{
					Resolve: func(context.Context, ExactProcessAttempt) (*processcontrol.Coordinator, DurableProcessAuthority, error) {
						return nil, DurableProcessAuthority{}, launchidentity.ErrUnavailable
					},
					Retain: func(ExactProcessAttempt, *processcontrol.Process, error) {},
				})
			}
			// The resolver must reject before Adapter preflight, so the plan is
			// deliberately nil; constructing a closure here would test the wrong
			// boundary.
			worker := &fakeProductionAdapter{fakeAdapter: &fakeAdapter{id: "fake"}}
			if _, err := bridge.RunWorker(context.Background(), worker, validRequest(t)); !errors.Is(err, launchidentity.ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if worker.prepares != 0 || worker.preflights != 0 || worker.calls != 0 || provider.provisions != 0 || provider.stages != 0 || provider.execs != 0 {
				t.Fatalf("prepares=%d preflights=%d provision=%d stage=%d exec=%d", worker.prepares, worker.preflights, provider.provisions, provider.stages, provider.execs)
			}
		})
	}
}

func TestAbortPreservesEstablishedTerminalReason(t *testing.T) {
	tests := []struct {
		name string
		in   resultingress.EligibilityTerminal
		want resultingress.EligibilityTerminal
	}{
		{name: "normal admission failure becomes aborted", in: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptCompleted}, want: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptAborted}},
		{name: "failed preserved", in: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptFailed}, want: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCompleted, CompletionReason: resultingress.TerminalAttemptFailed}},
		{name: "expired preserved", in: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalExpired}, want: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalExpired}},
		{name: "cancelled preserved", in: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCancelled, CancelReason: resultingress.EligibilityCancelDeadlineExceeded}, want: resultingress.EligibilityTerminal{Kind: resultingress.EligibilityTerminalCancelled, CancelReason: resultingress.EligibilityCancelDeadlineExceeded}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion := &exactProcessCompletion{eligibility: test.in}
			completion.abort()
			if completion.eligibility != test.want {
				t.Fatalf("eligibility=%+v, want %+v", completion.eligibility, test.want)
			}
		})
	}
}

func TestRunWorker_MalformedRequests(t *testing.T) {
	bridge, _ := NewBridge(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	adapter := &fakeAdapter{id: "fake"}

	cases := []struct {
		name    string
		request domain.Record
	}{{
		name:    "wrong kind",
		request: domain.Record{Kind: domain.KindWorkerResult, Data: []byte(`{}`)},
	}, {
		name:    "missing attemptId",
		request: domain.Record{Kind: domain.KindWorkerRequest, Data: []byte(`{"taskId":"T1","runId":"R1","capabilityDigest":"sha256:` + strings.Repeat("3", 64) + `","worktreePath":"/w","executionProfile":"workspace-write","adapterId":"fake"}`)},
	}, {
		name:    "malformed json",
		request: domain.Record{Kind: domain.KindWorkerRequest, Data: []byte(`{oops`)},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bridge.RunWorker(context.Background(), adapter, tc.request)
			if !errors.Is(err, ErrMalformedRequest) {
				t.Errorf("expected ErrMalformedRequest, got %v", err)
			}
			if adapter.calls != 0 {
				t.Errorf("adapter must not run on malformed request")
			}
		})
	}
}

func TestRunWorker_AdapterErrorStillTerminates(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, _ := NewBridge(provider)
	boom := errors.New("worker exploded")
	failOnce := &fakeAdapter{id: "fake", failErr: boom}

	_, err := bridge.runWorkerLegacy(context.Background(), failOnce, validRequest(t), mustParseView(t))
	if !errors.Is(err, boom) {
		t.Errorf("adapter error must propagate unchanged, got %v", err)
	}

	// 终态证明：allocation 身份由冻结输入确定性派生——若首次失败后未
	// 终结，同一请求再次 Provision 会因重复 active allocation 被拒；
	// 二次运行成功即证明首次 allocation 已被终结。
	ok := &fakeAdapter{id: "fake"}
	if _, err := bridge.runWorkerLegacy(context.Background(), ok, validRequest(t), mustParseView(t)); err != nil {
		t.Errorf("second runWorkerLegacy after failure must reprovision cleanly, got %v", err)
	}
}

func TestNewBridge_NilProvider(t *testing.T) {
	if _, err := NewBridge(nil); err == nil {
		t.Errorf("nil provider must fail closed")
	}
}

func TestRunWorker_NilAdapter(t *testing.T) {
	bridge, _ := NewBridge(sandbox.NewFakeProvider(sandbox.FakeConfig{}))
	if _, err := bridge.RunWorker(context.Background(), nil, validRequest(t)); err == nil {
		t.Errorf("nil adapter must fail closed")
	}
}

func jsonUnmarshalForTest(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

func jsonMarshalForTest(v any) ([]byte, error) {
	return json.Marshal(v)
}
