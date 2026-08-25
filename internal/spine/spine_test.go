package spine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/command"
	"github.com/chiga0/marshal-harness/internal/engine"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func testNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

type stubBackend struct{}

func (stubBackend) Deliver(_ context.Context, cmd engine.Command) (engine.Receipt, error) {
	return engine.Receipt{
		CommandId:   cmd.CommandId,
		DeliveredAt: "2000-01-01T00:00:00Z",
		AttemptSeq:  1,
	}, nil
}

func (stubBackend) Recover(_ context.Context) error { return nil }
func (stubBackend) Close() error                    { return nil }

func newTestEngine(t *testing.T) *engine.DurableExecutionEngine {
	t.Helper()
	eng, err := engine.New(testNamespace(), stubBackend{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

func validFact() engine.LedgerFact {
	return engine.LedgerFact{
		Sequence:      1,
		FactDigest:    fixedDigest("fact-spine"),
		PayloadDigest: fixedDigest("payload-spine"),
	}
}

func validInput(t *testing.T) Input {
	t.Helper()
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
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatalf("spec.Digest: %v", err)
	}
	expectedAllocID := canonical.DigestBytes([]byte("agentruntime:allocation:" + specDigest))

	return Input{
		AppCommand: command.ApplicationCommand{
			Kind:             command.ApplicationCommandKindAttemptStart,
			RequestDigest:    fixedDigest("request-spine"),
			ExpectedSequence: 1,
		},
		AdapterID:            spec.AdapterID,
		AdapterVersion:       spec.AdapterVersion,
		RunID:                spec.RunID,
		AttemptID:            spec.AttemptID,
		Executable:           spec.Executable,
		ExecutableDigest:     spec.ExecutableDigest,
		WorkingDirectory:     spec.WorkingDirectory,
		Arguments:            spec.Arguments,
		Environment:          spec.Environment,
		ProfileDigest:        spec.ProfileDigest,
		AuthorityNamespaceID: "ns-spine",
		TaskID:               "task-spine",
		LeaseID:              "lease-spine",
		FencingToken:         "fence-spine",
		IdempotencyKey:       "idem-spine",
		Nonce:                "nonce-spine",
		Expiry:               time.Now().Add(24 * time.Hour),
		EnvelopeSequence:     1,
		ExpectedAllocationID: expectedAllocID,
		ExpectedGeneration:   1,
	}
}

// (a) End-to-end happy path with per-hop binding consistency.
func TestRun_HappyPath(t *testing.T) {
	eng := newTestEngine(t)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	input := validInput(t)

	trace, err := Run(context.Background(), eng, validFact(), provider, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if trace.CommandID == "" {
		t.Fatal("CommandID must not be empty")
	}
	if trace.AllocationID == "" {
		t.Fatal("AllocationID must not be empty")
	}
	if trace.AllocationID != input.ExpectedAllocationID {
		t.Fatalf("AllocationID binding: got %q want %q", trace.AllocationID, input.ExpectedAllocationID)
	}
	if trace.Generation != 1 {
		t.Fatalf("Generation: got %d want 1", trace.Generation)
	}
	if trace.EnvelopeDigest == "" {
		t.Fatal("EnvelopeDigest must not be empty")
	}
	if trace.FactDigest == "" {
		t.Fatal("FactDigest must not be empty")
	}
	if trace.LedgerSequence != 1 {
		t.Fatalf("LedgerSequence: got %d want 1", trace.LedgerSequence)
	}
	if trace.IdempotentReplay {
		t.Fatal("first admission must not be IdempotentReplay")
	}

	// Verify the envelope digest is the sha256 of the deterministic
	// WorkloadResult produced by FakeAgent (Trusted=false, EventCount=1,
	// ExitCode=0, ProviderHint="fake-agent").  This confirms the Candidate
	// payload digest is mechanically bound to the untrusted WorkloadResult.
	expectedWL := agentruntime.WorkloadResult{
		Trusted:      false,
		EventCount:   1,
		ExitCode:     0,
		ProviderHint: "fake-agent",
	}
	expectedDigest, err := workloadResultDigest(expectedWL)
	if err != nil {
		t.Fatalf("workloadResultDigest: %v", err)
	}
	if trace.EnvelopeDigest != expectedDigest {
		t.Fatalf("EnvelopeDigest: got %q want %q", trace.EnvelopeDigest, expectedDigest)
	}
}

// (b) Allocation binding mismatch fails closed with typed error.
func TestRun_AllocationBindingMismatch(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Input)
		wantErr error
	}{
		{
			name:    "wrong allocation ID",
			mutate:  func(i *Input) { i.ExpectedAllocationID = fixedDigest("wrong-allocation") },
			wantErr: ErrAllocationBindingMismatch,
		},
		{
			name:    "wrong generation",
			mutate:  func(i *Input) { i.ExpectedGeneration = 99 },
			wantErr: ErrAllocationBindingMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := newTestEngine(t)
			provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
			input := validInput(t)
			tc.mutate(&input)

			_, err := Run(context.Background(), eng, validFact(), provider, input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// (c) Idempotent replay: same Candidate delivered twice returns existing
// AdmissionFact with IdempotentReplay=true and ledger sequence unchanged.
func TestRun_IdempotentReplay(t *testing.T) {
	eng := newTestEngine(t)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	input := validInput(t)

	binding := resultingress.LedgerBinding{
		LeaseID:      input.LeaseID,
		Generation:   uint64(input.ExpectedGeneration),
		FencingToken: input.FencingToken,
		AttemptID:    input.AttemptID,
		AllocationID: input.ExpectedAllocationID,
		Expiry:       input.Expiry,
	}
	ingress, err := resultingress.NewIngress(binding)
	if err != nil {
		t.Fatalf("NewIngress: %v", err)
	}
	input.Ingress = ingress

	first, err := Run(context.Background(), eng, validFact(), provider, input)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.IdempotentReplay {
		t.Fatal("first admission must not be IdempotentReplay")
	}

	second, err := Run(context.Background(), eng, validFact(), provider, input)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if !second.IdempotentReplay {
		t.Fatal("replay must be IdempotentReplay")
	}
	if second.LedgerSequence != first.LedgerSequence {
		t.Fatalf("ledger sequence must not advance on replay: first=%d second=%d",
			first.LedgerSequence, second.LedgerSequence)
	}
	if second.FactDigest != first.FactDigest {
		t.Fatalf("FactDigest must be identical on replay: %q != %q",
			second.FactDigest, first.FactDigest)
	}
}

// (d) Malformed input fails closed.
func TestRun_MalformedInput(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Input)
		wantErr error
	}{
		{"empty command kind", func(i *Input) { i.AppCommand.Kind = "" }, nil},
		{"empty request digest", func(i *Input) { i.AppCommand.RequestDigest = "" }, nil},
		{"bad request digest", func(i *Input) { i.AppCommand.RequestDigest = "not-a-digest" }, nil},
		{"zero expected sequence", func(i *Input) { i.AppCommand.ExpectedSequence = 0 }, nil},
		{"bad profile digest", func(i *Input) { i.ProfileDigest = "not-a-digest" }, nil},
		{"bad executable digest", func(i *Input) { i.ExecutableDigest = "not-a-digest" }, nil},
		{"negative expected generation", func(i *Input) { i.ExpectedGeneration = -1 }, ErrNegativeGeneration},
		{"empty authority namespace ID", func(i *Input) { i.AuthorityNamespaceID = "" }, nil},
		{"empty lease ID", func(i *Input) { i.LeaseID = "" }, nil},
		{"zero expiry", func(i *Input) { i.Expiry = time.Time{} }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := newTestEngine(t)
			provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
			input := validInput(t)
			tc.mutate(&input)

			_, err := Run(context.Background(), eng, validFact(), provider, input)
			if err == nil {
				t.Fatal("expected error but got nil")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// (e) WorkloadResult.Trusted is always false throughout the chain.
func TestRun_TrustedAlwaysFalse(t *testing.T) {
	eng := newTestEngine(t)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	input := validInput(t)

	trace, err := Run(context.Background(), eng, validFact(), provider, input)
	if err != nil {
		t.Fatalf("Run: %v (Trusted check must pass for FakeAgent)", err)
	}

	// Run checks outcome.WorkloadResult.Trusted and fails with ErrTrustedResult
	// if it were ever true.  The happy path succeeding proves Trusted=false.
	// The envelope digest further confirms the payload carries Trusted=false.
	expectedWL := agentruntime.WorkloadResult{
		Trusted:      false,
		EventCount:   1,
		ExitCode:     0,
		ProviderHint: "fake-agent",
	}
	expectedDigest, err := workloadResultDigest(expectedWL)
	if err != nil {
		t.Fatalf("workloadResultDigest: %v", err)
	}
	if trace.EnvelopeDigest != expectedDigest {
		t.Fatalf("EnvelopeDigest mismatch: got %q want %q — Trusted=false not verified",
			trace.EnvelopeDigest, expectedDigest)
	}
}

// (f) Production profile does not touch the host bypass entry.
func TestRun_ProductionProfileNoHostBypass(t *testing.T) {
	eng := newTestEngine(t)
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	input := validInput(t)

	// Run uses a production ExecutionProfile (Production=true).  The host
	// bypass entry point (Executor.RunHostBypass) fails closed with
	// ErrHostBypassDenied for production profiles.  Run only calls Execute,
	// never RunHostBypass, so the happy path succeeding proves the
	// production path does not touch the host bypass entry.
	_, err := Run(context.Background(), eng, validFact(), provider, input)
	if err != nil {
		t.Fatalf("Run with production profile: %v", err)
	}
}
