package agentruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// executorSpec returns a validated production spec with predictable fields.
func executorSpec(t *testing.T) AgentLaunchSpec {
	t.Helper()
	s, err := NewAgentLaunchSpec(
		"adapter-id", "1.0.0",
		"run-executor", "attempt-executor",
		"/usr/bin/agent", fixedDigest("executable"),
		"/workdir",
		[]string{"--flag", "value"},
		[]string{"HOME=/home/agent"},
		fixedDigest("profile"),
		"",
	)
	if err != nil {
		t.Fatalf("executorSpec: %v", err)
	}
	return s
}

func executorProfile(production bool) ExecutionProfile {
	return ExecutionProfile{Production: production, Digest: fixedDigest("profile")}
}

// newExecutorHarness builds an Executor backed by a fresh FakeProvider and
// FakeAgent for the common case.
func newExecutorHarness(t *testing.T) (*Executor, *sandbox.FakeProvider) {
	t.Helper()
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	runtime := &FakeAgent{FixedHint: "fake-agent-executor"}
	exec, err := NewExecutor(provider, runtime)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return exec, provider
}

func TestNewExecutor_FailClosed(t *testing.T) {
	runtime := &FakeAgent{}
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})

	tests := []struct {
		name     string
		provider sandbox.SandboxProvider
		runtime  AgentRuntime
	}{
		{"nil provider", nil, runtime},
		{"nil runtime", provider, nil},
		{"both nil", nil, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExecutor(tc.provider, tc.runtime)
			if err == nil {
				t.Fatal("expected NewExecutor to fail closed")
			}
		})
	}
}

func TestExecutor_Execute_FailClosed_MalformedInputs(t *testing.T) {
	exec, _ := newExecutorHarness(t)
	validSpec := executorSpec(t)
	validProfile := executorProfile(false)

	tests := []struct {
		name    string
		spec    AgentLaunchSpec
		profile ExecutionProfile
	}{
		{"invalid spec empty executable", func() AgentLaunchSpec { s := validSpec; s.Executable = ""; return s }(), validProfile},
		{"invalid profile empty digest", validSpec, ExecutionProfile{Production: false, Digest: ""}},
		{"invalid profile bad digest", validSpec, ExecutionProfile{Production: false, Digest: "not-a-digest"}},
		{"digest mismatch", validSpec, ExecutionProfile{Production: false, Digest: fixedDigest("other-profile")}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exec.Execute(context.Background(), tc.spec, tc.profile)
			if err == nil {
				t.Fatal("expected Execute to fail closed")
			}
		})
	}
}

func TestExecutor_Execute_HappyPath_FakeProviderFakeAgent(t *testing.T) {
	ctx := context.Background()
	exec, provider := newExecutorHarness(t)
	spec := executorSpec(t)
	profile := executorProfile(false)

	outcome, err := exec.Execute(ctx, spec, profile)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if outcome.AllocationId == "" {
		t.Fatal("expected non-empty AllocationId")
	}
	if outcome.Generation != 1 {
		t.Fatalf("expected generation=1, got %d", outcome.Generation)
	}
	if outcome.ExecStatus != sandbox.ExecutionCompleted {
		t.Fatalf("expected exec status completed, got %q", string(outcome.ExecStatus))
	}
	if outcome.WorkloadResult.Trusted {
		t.Fatal("WorkloadResult.Trusted must be false")
	}
	if outcome.WorkloadResult.EventCount != 1 {
		t.Fatalf("expected one normalized event, got %d", outcome.WorkloadResult.EventCount)
	}
	if outcome.WorkloadResult.ProviderHint != "fake-agent-executor" {
		t.Fatalf("unexpected provider hint: %q", outcome.WorkloadResult.ProviderHint)
	}

	// Verify Terminate was called by observing the allocation is terminal.
	inspectID, err := exec.identity(spec, outcome.AllocationId, outcome.Generation, "command-inspect-verify")
	if err != nil {
		t.Fatalf("inspect identity: %v", err)
	}
	inspectReport, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     inspectID,
		AllocationId: outcome.AllocationId,
	})
	if err != nil {
		t.Fatalf("post-execute inspect: %v", err)
	}
	if inspectReport.State != sandbox.AllocationTerminated {
		t.Fatalf("allocation must be terminated after Execute, got %q", string(inspectReport.State))
	}

	// Execution-location evidence must match the original provision receipt.
	reconcileID, err := exec.identity(spec, outcome.AllocationId, outcome.Generation, "command-reconcile-verify")
	if err != nil {
		t.Fatalf("reconcile identity: %v", err)
	}
	reconcileReport, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  reconcileID,
		RunId:     spec.RunID,
		AttemptId: spec.AttemptID,
	})
	if err != nil {
		t.Fatalf("post-execute reconcile: %v", err)
	}
	if len(reconcileReport.ActiveAllocationIds) != 0 {
		t.Fatalf("expected no active allocations after terminate, got %v", reconcileReport.ActiveAllocationIds)
	}
}

func TestExecutor_Execute_ExecFault_FailClosedAndTerminates(t *testing.T) {
	ctx := context.Background()
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{}).
		WithFaults(sandbox.FaultSpec{Operation: sandbox.OperationExec, Fault: sandbox.FaultReject})
	exec, err := NewExecutor(provider, &FakeAgent{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	spec := executorSpec(t)
	profile := executorProfile(false)

	_, err = exec.Execute(ctx, spec, profile)
	if err == nil {
		t.Fatal("expected Execute to fail closed on exec fault")
	}
	if !errors.Is(err, sandbox.ErrFaultInjected) {
		t.Fatalf("expected ErrFaultInjected, got %v", err)
	}

	// Even though Execute errored, the allocation must still be terminated.
	// The outcome is unavailable, so derive the allocation id the same way the
	// executor does.
	specDigest, _ := spec.Digest()
	allocationID := canonical.DigestBytes([]byte("agentruntime:allocation:" + specDigest))
	inspectID, err := exec.identity(spec, allocationID, 1, "command-inspect-verify")
	if err != nil {
		t.Fatalf("inspect identity: %v", err)
	}
	inspectReport, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     inspectID,
		AllocationId: allocationID,
	})
	if err != nil {
		t.Fatalf("post-fault inspect: %v", err)
	}
	if inspectReport.State != sandbox.AllocationTerminated {
		t.Fatalf("allocation must be terminated after exec fault, got %q", string(inspectReport.State))
	}
}

func TestExecutor_RunHostBypass_Production_FailClosed_NoProviderCalls(t *testing.T) {
	// Inject faults on every operation. If the production path touches the
	// provider, the fault surfaces; the required behavior is the typed
	// ErrHostBypassDenied instead.
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{}).
		WithFaults(
			sandbox.FaultSpec{Operation: sandbox.OperationProvision, Fault: sandbox.FaultReject},
			sandbox.FaultSpec{Operation: sandbox.OperationStage, Fault: sandbox.FaultReject},
			sandbox.FaultSpec{Operation: sandbox.OperationExec, Fault: sandbox.FaultReject},
			sandbox.FaultSpec{Operation: sandbox.OperationInspect, Fault: sandbox.FaultReject},
			sandbox.FaultSpec{Operation: sandbox.OperationTerminate, Fault: sandbox.FaultReject},
		)
	exec, err := NewExecutor(provider, &FakeAgent{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	_, err = exec.RunHostBypass(context.Background(), executorSpec(t), executorProfile(true))
	if !errors.Is(err, ErrHostBypassDenied) {
		t.Fatalf("expected ErrHostBypassDenied, got %v", err)
	}
	if errors.Is(err, sandbox.ErrFaultInjected) {
		t.Fatal("production host bypass must not invoke the provider")
	}
}

func TestExecutor_RunHostBypass_Nonproduction_Success(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	called := false
	runner := HostBypassRun(func(ctx context.Context, spec AgentLaunchSpec) (ExecutionOutcome, error) {
		called = true
		return ExecutionOutcome{
			WorkloadResult: WorkloadResult{
				Trusted:      false,
				EventCount:   1,
				ExitCode:     0,
				ProviderHint: MigrationProvenance,
			},
		}, nil
	})
	exec, err := NewExecutor(provider, &FakeAgent{}, WithHostBypass(runner))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	outcome, err := exec.RunHostBypass(context.Background(), executorSpec(t), executorProfile(false))
	if err != nil {
		t.Fatalf("RunHostBypass: %v", err)
	}
	if !called {
		t.Fatal("nonproduction host bypass runner was not invoked")
	}
	if outcome.WorkloadResult.ProviderHint != MigrationProvenance {
		t.Fatalf("expected compat provenance hint, got %q", outcome.WorkloadResult.ProviderHint)
	}
}

func TestExecutor_RunHostBypass_Nonproduction_NoRunner_FailClosed(t *testing.T) {
	exec, _ := newExecutorHarness(t)
	_, err := exec.RunHostBypass(context.Background(), executorSpec(t), executorProfile(false))
	if err == nil {
		t.Fatal("expected RunHostBypass to fail closed when no runner is configured")
	}
}
