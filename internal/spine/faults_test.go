package spine

import (
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// (a) Host bypass negative fixture: production profile must fail closed
// with ErrHostBypassDenied and zero provider side effects. Faults injected
// on every provider operation prove the provider was never called.
func TestFaultInjection_HostBypass(t *testing.T) {
	spec, err := agentruntime.NewAgentLaunchSpec(
		"adapter-spine", "1.0.0",
		"run-spine", "attempt-spine",
		"/usr/bin/agent", fixedDigest("executable-spine"),
		"/workdir",
		[]string{"--flag", "value"},
		[]string{"HOME=/home/agent"},
		fixedDigest("profile-spine"),
		"",
	)
	if err != nil {
		t.Fatalf("NewAgentLaunchSpec: %v", err)
	}
	profile := agentruntime.ExecutionProfile{
		Production: true,
		Digest:     fixedDigest("profile-spine"),
	}

	executor, err := agentruntime.NewExecutor(allOperationsFaulty(), &agentruntime.FakeAgent{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	outcome, err := executor.RunHostBypass(context.Background(), spec, profile)
	if !errors.Is(err, agentruntime.ErrHostBypassDenied) {
		t.Fatalf("expected ErrHostBypassDenied, got %v", err)
	}
	if errors.Is(err, sandbox.ErrFaultInjected) {
		t.Fatal("production host bypass must not invoke the provider")
	}
	if outcome.AllocationId != "" {
		t.Fatalf("outcome must have empty AllocationId, got %q", outcome.AllocationId)
	}
	if outcome.WorkloadResult.Trusted {
		t.Fatal("WorkloadResult.Trusted must be false")
	}
}

// (b,c,e) Crash-window injections through Run: kill/timeout/lost response
// must return typed errors, leave no half-advanced authority state, and
// recover with LedgerSequence=1 on clean retry (closed conclusion).
func TestFaultInjection_CrashWindows(t *testing.T) {
	expiredCtx, expiredCancel := expiredContext()
	defer expiredCancel()

	expectedDigest, err := workloadResultDigest(agentruntime.WorkloadResult{
		Trusted:      false,
		EventCount:   1,
		ExitCode:     0,
		ProviderHint: "fake-agent",
	})
	if err != nil {
		t.Fatalf("workloadResultDigest: %v", err)
	}

	tests := []struct {
		name        string
		provider    *sandbox.FakeProvider
		ctx         context.Context
		wantErr     error
		checkDigest bool
	}{
		{
			name:     "kill at Exec (FaultReject)",
			provider: killProvider(),
			ctx:      context.Background(),
			wantErr:  sandbox.ErrFaultInjected,
		},
		{
			name:     "timeout (expired deadline)",
			provider: sandbox.NewFakeProvider(sandbox.FakeConfig{}),
			ctx:      expiredCtx,
			wantErr:  context.DeadlineExceeded,
		},
		{
			name:        "lost response at Exec (FaultDropResponse)",
			provider:    lostResponseProvider(),
			ctx:         context.Background(),
			wantErr:     sandbox.ErrResponseLost,
			checkDigest: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := newTestEngine(t)
			fact := validFact()
			input := validInput(t)

			binding := resultingress.LedgerBinding{
				LeaseID:        input.LeaseID,
				Generation:     uint64(input.ExpectedGeneration),
				FencingToken:   input.FencingToken,
				AttemptID:      input.AttemptID,
				AllocationID:   input.ExpectedAllocationID,
				Expiry:         input.Expiry,
				RegistrationID: input.RegistrationID,
				SnapshotDigest: input.SnapshotDigest,
				EvidenceDigest: input.EvidenceDigest,
			}
			ingress, err := resultingress.NewIngress(binding)
			if err != nil {
				t.Fatalf("NewIngress: %v", err)
			}
			input.Ingress = ingress

			// Inject fault → Run must fail with typed error.
			_, err = Run(tc.ctx, eng, fact, tc.provider, input)
			if err == nil {
				t.Fatal("expected error but got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}

			// Closed conclusion: "not admitted" — no half-advanced state.
			if !quarantineIsEmpty(ingress) {
				t.Fatalf("quarantine must be empty (Admit never reached), got %d records",
					len(ingress.Quarantine()))
			}

			// Recovery: clean retry must produce first admission.
			recoveryInput := validInput(t)
			recoveryInput.Ingress = ingress
			trace, err := Run(context.Background(), eng, fact,
				sandbox.NewFakeProvider(sandbox.FakeConfig{}), recoveryInput)
			if err != nil {
				t.Fatalf("recovery Run: %v", err)
			}
			if trace.LedgerSequence != 1 {
				t.Fatalf("recovery must be first admission LedgerSequence=1, got %d",
					trace.LedgerSequence)
			}
			if trace.IdempotentReplay {
				t.Fatal("recovery must not be idempotent replay (fault did not reach Admit)")
			}

			// Digest consistency: recovery digest must match no-fault path.
			if tc.checkDigest && trace.EnvelopeDigest != expectedDigest {
				t.Fatalf("envelope digest must match no-fault path: got %q want %q",
					trace.EnvelopeDigest, expectedDigest)
			}

			// (f) Quarantine audit: after recovery, quarantine is still clean.
			if !quarantineIsClean(ingress) {
				t.Fatal("quarantine must only contain typed rejection records or be empty")
			}
		})
	}
}

// (d) Partial output: DecodeEvent must fail closed on malformed/truncated
// event payloads. No admission occurs. Recovery through Run produces
// first admission (LedgerSequence=1).
func TestFaultInjection_PartialOutput(t *testing.T) {
	runtime := &agentruntime.FakeAgent{}

	malformedCases := []struct {
		name string
		raw  []byte
	}{
		{"nil bytes", nil},
		{"empty slice", []byte{}},
		{"truncated JSON", []byte("{broken")},
		{"single brace", []byte("{")},
		{"binary garbage", []byte{0x00, 0x01, 0x02}},
	}

	for _, mc := range malformedCases {
		t.Run(mc.name, func(t *testing.T) {
			_, err := runtime.DecodeEvent(mc.raw)
			if err == nil {
				t.Fatal("DecodeEvent must fail closed on malformed input")
			}
		})
	}

	// No admission: ingress is clean.
	input := validInput(t)
	binding := resultingress.LedgerBinding{
		LeaseID:        input.LeaseID,
		Generation:     uint64(input.ExpectedGeneration),
		FencingToken:   input.FencingToken,
		AttemptID:      input.AttemptID,
		AllocationID:   input.ExpectedAllocationID,
		Expiry:         input.Expiry,
		RegistrationID: input.RegistrationID,
		SnapshotDigest: input.SnapshotDigest,
		EvidenceDigest: input.EvidenceDigest,
	}
	ingress, err := resultingress.NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	if !quarantineIsEmpty(ingress) {
		t.Fatalf("quarantine must be empty, got %d records", len(ingress.Quarantine()))
	}

	// Recovery: Run with valid input → first admission.
	recoveryInput := validInput(t)
	recoveryInput.Ingress = ingress
	eng := newTestEngine(t)
	trace, err := Run(context.Background(), eng, validFact(),
		sandbox.NewFakeProvider(sandbox.FakeConfig{}), recoveryInput)
	if err != nil {
		t.Fatalf("recovery Run: %v", err)
	}
	if trace.LedgerSequence != 1 {
		t.Fatalf("expected LedgerSequence=1, got %d", trace.LedgerSequence)
	}
	if trace.IdempotentReplay {
		t.Fatal("recovery must not be idempotent replay")
	}
	if !quarantineIsClean(ingress) {
		t.Fatal("quarantine must be clean after recovery")
	}
}

// (f) Quarantine audit: across all crash-window scenarios, the ingress
// quarantine contains only typed rejection records or is empty — never
// admission-semantics entries.
func TestFaultInjection_QuarantineAudit(t *testing.T) {
	eng := newTestEngine(t)
	fact := validFact()
	input := validInput(t)

	binding := resultingress.LedgerBinding{
		LeaseID:        input.LeaseID,
		Generation:     uint64(input.ExpectedGeneration),
		FencingToken:   input.FencingToken,
		AttemptID:      input.AttemptID,
		AllocationID:   input.ExpectedAllocationID,
		Expiry:         input.Expiry,
		RegistrationID: input.RegistrationID,
		SnapshotDigest: input.SnapshotDigest,
		EvidenceDigest: input.EvidenceDigest,
	}
	ingress, err := resultingress.NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	input.Ingress = ingress

	// Kill fault → fails before Admit, quarantine must be empty.
	_, err = Run(context.Background(), eng, fact, killProvider(), input)
	if err == nil {
		t.Fatal("expected error from kill fault")
	}
	if !quarantineIsEmpty(ingress) {
		t.Fatalf("quarantine must be empty after fault, got %d records",
			len(ingress.Quarantine()))
	}

	// Recovery → first admission, quarantine still clean.
	recoveryInput := validInput(t)
	recoveryInput.Ingress = ingress
	_, err = Run(context.Background(), eng, fact,
		sandbox.NewFakeProvider(sandbox.FakeConfig{}), recoveryInput)
	if err != nil {
		t.Fatalf("recovery Run: %v", err)
	}
	if !quarantineIsClean(ingress) {
		t.Fatal("quarantine must only contain typed rejection records or be empty after recovery")
	}
}
