package sandboxbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processcontrol"
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
	plan     LaunchPlan
	prepares int
}

func (a *fakeProductionAdapter) ProductionLaunchProfileID() string {
	return launchidentity.Pi0843DarwinARM64Profile
}
func (a *fakeProductionAdapter) PrepareLaunch(context.Context, domain.Record) (LaunchPlan, error) {
	a.prepares++
	return a.plan, nil
}
func (a *fakeProductionAdapter) CompleteLaunch(context.Context, LaunchPlan, []byte, bool, []byte, time.Time, time.Time, int, string, error) (domain.Record, error) {
	return domain.Record{}, errors.New("must not complete")
}

type fakeLaunchPlan struct{ closure launchidentity.ClosureV1 }

func (p fakeLaunchPlan) Argv() []string                          { return append([]string(nil), p.closure.Arguments...) }
func (p fakeLaunchPlan) EnvBlock() []string                      { return append([]string(nil), p.closure.Environment...) }
func (p fakeLaunchPlan) WorkDir() string                         { return p.closure.WorkingDirectory }
func (fakeLaunchPlan) TimeoutSeconds() int64                     { return 30 }
func (fakeLaunchPlan) ResultFilePath() string                    { return "/fixed/control/result.json" }
func (fakeLaunchPlan) ControlRootPath() string                   { return "/fixed/control" }
func (fakeLaunchPlan) SessionPolicyName() string                 { return "ephemeral" }
func (fakeLaunchPlan) MaxOutput() int64                          { return 4096 }
func (fakeLaunchPlan) ProviderVersion() string                   { return "0.84.3" }
func (p fakeLaunchPlan) LaunchClosure() launchidentity.ClosureV1 { return p.closure }
func (fakeLaunchPlan) CloseLaunchClosure()                       {}

type countingProvider struct {
	sandbox.SandboxProvider
	provisions int
	stages     int
	execs      int
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

func nonExactLaunchPlan(t *testing.T) LaunchPlan {
	t.Helper()
	input := launchidentity.SpecInput{
		RuntimeExecutable: launchidentity.ObjectV1{CanonicalPath: "/fixed/runtime", Device: 1, Inode: 2, FileType: 0o100000, Mode: 0o100700, UID: 501, GID: 20, Size: 10, LinkCount: 1, RawSHA256: "sha256:" + strings.Repeat("a", 64)},
		ClosureProfileID:  launchidentity.NativeProfile,
		MaterialRoots:     []launchidentity.MaterialRootV1{}, LaunchMaterials: []launchidentity.LaunchMaterialV1{},
		Arguments: []string{"/fixed/runtime"}, Environment: []string{"LANG=C"}, WorkingDirectory: "/fixed/worktree",
	}
	closure, err := launchidentity.Seal(input)
	if err != nil {
		t.Fatal(err)
	}
	return fakeLaunchPlan{closure: closure}
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
		name         string
		withRuntime  bool
		wantPrepares int
	}{
		{name: "missing exact runtime"},
		{name: "non-exact closure", withRuntime: true, wantPrepares: 1},
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
			worker := &fakeProductionAdapter{fakeAdapter: &fakeAdapter{id: "fake"}, plan: nonExactLaunchPlan(t)}
			if _, err := bridge.RunWorker(context.Background(), worker, validRequest(t)); !errors.Is(err, launchidentity.ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if worker.prepares != test.wantPrepares || provider.provisions != 0 || provider.stages != 0 || provider.execs != 0 {
				t.Fatalf("prepares=%d provision=%d stage=%d exec=%d", worker.prepares, provider.provisions, provider.stages, provider.execs)
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
