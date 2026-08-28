package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// embeddedTestClock is the injectable deterministic clock of the embedded
// runtime fixtures.
type embeddedTestClock struct {
	current time.Time
}

func (clock *embeddedTestClock) Now() time.Time { return clock.current }

func newEmbeddedRuntimeFixture(t *testing.T) (*EmbeddedSandboxRuntime, *embeddedTestClock, string, string) {
	t.Helper()
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(base, ".marshal")
	bindEmbeddedRepositoryIdentityFixture(t, stateRoot, repositoryRoot)
	clock := &embeddedTestClock{current: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	runtime, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now)
	if err != nil {
		t.Fatalf("NewEmbeddedSandboxRuntime: %v", err)
	}
	return runtime, clock, stateRoot, repositoryRoot
}

// bindEmbeddedRepositoryIdentityFixture persists the RepositoryIdentity
// record the embedded runtime derives its authority scope from: the record
// is always taken from the real worktree repository identity, mirroring
// repository.State.Init, never fabricated from the state root path.
func bindEmbeddedRepositoryIdentityFixture(t *testing.T, stateRoot, repositoryRoot string) {
	t.Helper()
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.MarshalIndent(map[string]string{
		"apiVersion":     "marshal.dev/v1alpha1",
		"kind":           "RepositoryIdentity",
		"repositoryRoot": repositoryRoot,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "repo.json"), append(record, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workspaceWriteRequirementsFixture(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	return requirements
}

func hardenedRequirementsFixture(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelHardened)
	if err != nil {
		t.Fatal(err)
	}
	return requirements
}

func embeddedClaimRequestFixture(runID, attemptID string, role sandbox.WorkloadRole, principal string, requirements domain.SandboxRequirements) EmbeddedClaimRequest {
	return EmbeddedClaimRequest{
		TaskId:       "task-" + attemptID,
		RunId:        runID,
		AttemptId:    attemptID,
		AllocationId: embeddedAllocationID(runID, attemptID, role),
		WorkloadRole: role,
		Principal:    principal,
		Requirements: requirements,
	}
}

func embeddedLedgerLines(t *testing.T, stateRoot string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateRoot, "providers", "registrations.jsonl"))
	if err != nil {
		t.Fatalf("read registration ledger: %v", err)
	}
	return len(strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
}

// TestEmbeddedSandboxEnabledPredicate freezes the opt-in gate: exactly "1"
// enables the embedded runtime, any other value — including a nil lookup —
// keeps the Local MVP behavior completely unchanged (negative fixture 9).
func TestEmbeddedSandboxEnabledPredicate(t *testing.T) {
	cases := []struct {
		name   string
		lookup func(string) string
		want   bool
	}{
		{name: "nil lookup keeps Local MVP", lookup: nil, want: false},
		{name: "unset keeps Local MVP", lookup: func(string) string { return "" }, want: false},
		{name: "zero keeps Local MVP", lookup: func(string) string { return "0" }, want: false},
		{name: "true literal keeps Local MVP", lookup: func(string) string { return "true" }, want: false},
		{name: "exactly 1 enables embedded", lookup: func(key string) string {
			if key == EmbeddedSandboxEnvironmentVariable {
				return "1"
			}
			return ""
		}, want: true},
		{name: "padded 1 enables embedded", lookup: func(string) string { return " 1 " }, want: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := EmbeddedSandboxEnabled(testCase.lookup); got != testCase.want {
				t.Fatalf("EmbeddedSandboxEnabled = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestNewEmbeddedSandboxRuntimeValidatesInputs(t *testing.T) {
	clock := &embeddedTestClock{current: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	if _, err := NewEmbeddedSandboxRuntime("", clock.Now); err == nil {
		t.Fatal("empty stateRoot was accepted")
	}
	if _, err := NewEmbeddedSandboxRuntime("   ", clock.Now); err == nil {
		t.Fatal("blank stateRoot was accepted")
	}
	if _, err := NewEmbeddedSandboxRuntime(filepath.Join(t.TempDir(), ".marshal"), nil); err == nil {
		t.Fatal("nil clock was accepted")
	}
}

// TestNewEmbeddedSandboxRuntimeRequiresRepositoryIdentity freezes that the
// embedded authority scope is always taken from the real worktree
// repository identity record: a state root without the record, or with a
// malformed record, fails closed at construction.
func TestNewEmbeddedSandboxRuntimeRequiresRepositoryIdentity(t *testing.T) {
	clock := &embeddedTestClock{current: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	stateRoot := filepath.Join(t.TempDir(), ".marshal")
	if _, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now); err == nil || !strings.Contains(err.Error(), "repository identity record") {
		t.Fatalf("construction without the repository identity record must fail closed, got %v", err)
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "repo.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now); err == nil {
		t.Fatal("a malformed repository identity record was accepted")
	}
}

// TestEmbeddedRegistrationDurableAndIdempotent freezes the idempotent
// submission semantics of the embedded Local provider registration: the
// identical replay merges without appending a second ledger fact.
func TestEmbeddedRegistrationDurableAndIdempotent(t *testing.T) {
	runtime, clock, stateRoot, _ := newEmbeddedRuntimeFixture(t)
	if lines := embeddedLedgerLines(t, stateRoot); lines != 1 {
		t.Fatalf("registration ledger lines = %d, want exactly one registration fact", lines)
	}
	// The identical replay merges idempotently.
	stored, err := runtime.RegistrationStore().Put(runtime.Registration())
	if err != nil {
		t.Fatalf("identical registration replay rejected: %v", err)
	}
	if stored.RegistrationDigest != runtime.Registration().RegistrationDigest {
		t.Fatal("idempotent replay must return the existing record")
	}
	if lines := embeddedLedgerLines(t, stateRoot); lines != 1 {
		t.Fatalf("idempotent replay appended a ledger fact: %d lines", lines)
	}
	// A second runtime over the identical state root recovers the identical
	// durable identity from the ledger.
	second, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now)
	if err != nil {
		t.Fatalf("reconstruction over the identical durable ledger rejected: %v", err)
	}
	if second.Registration().RegistrationDigest != runtime.Registration().RegistrationDigest {
		t.Fatal("reconstructed registration diverges from the durable ledger")
	}
	if lines := embeddedLedgerLines(t, stateRoot); lines != 1 {
		t.Fatalf("reconstruction appended a ledger fact: %d lines", lines)
	}
}

// TestEmbeddedRegistrationSameKeyDifferentDigestConflicts freezes negative
// fixture 7: the identical idempotency identity and idempotencyKey with a
// different requestDigest fails closed as a conflict.
func TestEmbeddedRegistrationSameKeyDifferentDigestConflicts(t *testing.T) {
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)
	conflict := runtime.Registration()
	conflict.RequestDigest = sandbox.RecomputeSHA256([]byte("conflicting" + "-request"))
	digest, err := conflict.Digest()
	if err != nil {
		t.Fatal(err)
	}
	conflict.RegistrationDigest = digest
	if _, err := runtime.RegistrationStore().Put(conflict); !errors.Is(err, provider.ErrRegistrationConflict) {
		t.Fatalf("same key with a different digest must conflict fail closed, got %v", err)
	}
}

// TestEmbeddedCrossScopeSubmissionRejected freezes negative fixture 6: a
// registration submitted under a different repository identity (scope) can
// never take over the existing registrationId.
func TestEmbeddedCrossScopeSubmissionRejected(t *testing.T) {
	runtimeA, _, _, _ := newEmbeddedRuntimeFixture(t)
	otherBase := t.TempDir()
	otherRepositoryRoot := filepath.Join(otherBase, "repository")
	if err := os.Mkdir(otherRepositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	otherStateRoot := filepath.Join(otherBase, ".marshal")
	bindEmbeddedRepositoryIdentityFixture(t, otherStateRoot, otherRepositoryRoot)
	runtimeB, err := NewEmbeddedSandboxRuntime(otherStateRoot, time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC).UTC)
	if err != nil {
		t.Fatalf("NewEmbeddedSandboxRuntime(other scope): %v", err)
	}
	if runtimeA.Namespace().AuthorityScopeId == runtimeB.Namespace().AuthorityScopeId {
		t.Fatal("distinct worktree repository identities must derive distinct authority scopes")
	}
	if _, err := runtimeA.RegistrationStore().Put(runtimeB.Registration()); !errors.Is(err, provider.ErrRegistrationConflict) {
		t.Fatalf("cross scope submission must be rejected fail closed, got %v", err)
	}
}

// TestEmbeddedClaimRejectsMemoryOnlyRegistration freezes negative fixture 1:
// a claim against a registration store that is not bound to a durable ledger
// directory fails closed with the memory-only rejection.
func TestEmbeddedClaimRejectsMemoryOnlyRegistration(t *testing.T) {
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)
	request := dispatch.ClaimRequest{
		AuthorityNamespaceId: runtime.Namespace(),
		RegistrationId:       runtime.Registration().RegistrationId,
		Snapshot:             runtime.CapabilitySnapshot(),
		Evidences:            []provider.ConformanceEvidence{},
		Requirements:         workspaceWriteRequirementsFixture(t),
		TargetActor:          runtime.ResultIngressSecurityDomain(),
		TaskId:               "task-memory",
		RunId:                "run-memory",
		AttemptId:            "attempt-memory",
		AllocationId:         "allocation-memory",
		AckDeadlineAt:        "2026-08-13T12:30:00Z",
		ExpiresAt:            "2026-08-14T12:00:00Z",
	}
	cases := map[string]*dispatch.Matcher{
		"nil store":        dispatch.NewMatcher(nil),
		"zero-value store": dispatch.NewMatcherWithEdgeRuntime(&provider.RegistrationStore{}, mustEdgeRuntime(t, runtime.Namespace())),
	}
	for name, matcher := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := matcher.Claim(request, time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)); !errors.Is(err, provider.ErrMemoryOnlyRegistration) {
				t.Fatalf("claim must fail closed with the memory-only rejection, got %v", err)
			}
		})
	}
	// The gate-6 sequence also fails closed when the typed-edge runtime is
	// not bound: the Core issues a DispatchResultCapability with every
	// accepted claim.
	if _, err := dispatch.NewMatcher(runtime.RegistrationStore()).Claim(request, time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "typed-edge runtime is not bound") {
		t.Fatalf("claim without the typed-edge runtime must fail closed, got %v", err)
	}
}

func mustEdgeRuntime(t *testing.T, namespace authority.AuthorityNamespaceId) *authority.EdgeRuntime {
	t.Helper()
	runtime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// TestEmbeddedClaimPositiveBaseline freezes the accepted baseline: the
// workspace-write claim issues a sealed generation-1 lease, grants an
// active Local allocation at the workspace-write ceiling and the fencing
// guard accepts the freshly claimed values.
func TestEmbeddedClaimPositiveBaseline(t *testing.T) {
	runtime, _, _, repositoryRoot := newEmbeddedRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	claim, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-baseline", "attempt-baseline", sandbox.WorkloadRoleWorker, "principal-baseline", requirements))
	if err != nil {
		t.Fatalf("ClaimExecution rejected the eligible bundle: %v", err)
	}
	if err := claim.Lease.Validate(); err != nil {
		t.Fatalf("the claimed lease does not validate: %v", err)
	}
	if claim.Lease.Generation != 1 || claim.Lease.LeaseState != dispatch.LeaseStateClaimed {
		t.Fatalf("lease = generation %d state %q, want generation 1 claimed", claim.Lease.Generation, string(claim.Lease.LeaseState))
	}
	if err := dispatch.ValidateLeaseFencing(claim.Lease, claim.Lease.Generation, claim.Lease.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the freshly claimed lease: %v", err)
	}
	if claim.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("allocation state = %q, want active", string(claim.Allocation.State))
	}
	if err := sandbox.ValidateAllocationRequirements(claim.Allocation, requirements); err != nil {
		t.Fatalf("the granted allocation does not satisfy the requirements: %v", err)
	}
	if claim.Allocation.AssuranceLevel != domain.AssuranceLevelWorkspaceWrite {
		t.Fatalf("the local allocation must grant the workspace-write ceiling, got %q", string(claim.Allocation.AssuranceLevel))
	}
	// The authority scope is taken from the real worktree repository
	// identity record and binds the issued lease key space.
	wantScope := "repo:" + filepath.ToSlash(filepath.Clean(repositoryRoot))
	if runtime.Namespace().AuthorityScopeId != wantScope {
		t.Fatalf("authority scope = %q, want the real worktree repository identity %q", runtime.Namespace().AuthorityScopeId, wantScope)
	}
	if claim.Lease.AuthorityNamespaceId.AuthorityScopeId != wantScope {
		t.Fatalf("lease authority scope = %q, want the real worktree repository identity %q", claim.Lease.AuthorityNamespaceId.AuthorityScopeId, wantScope)
	}
}

// TestEmbeddedClaimRejectsHardenedWithoutDowngrade freezes negative fixture
// 2: hardened requirements against the Local provider fail closed without
// any downgrade to workspace-write, and the failed claim leaves no scope
// bookkeeping behind.
func TestEmbeddedClaimRejectsHardenedWithoutDowngrade(t *testing.T) {
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)
	request := embeddedClaimRequestFixture("run-hardened", "attempt-hardened", sandbox.WorkloadRoleWorker, "principal-hardened", hardenedRequirementsFixture(t))
	_, err := runtime.ClaimExecution(context.Background(), request)
	// The rejection must surface at the assurance adjudication layer —
	// before any capabilities negotiation — never as a capabilities
	// conflict and never as a downgrade.
	if err == nil || !strings.Contains(err.Error(), "assurance adjudication") || !strings.Contains(err.Error(), "fail closed without downgrade") || strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("hardened claim against the local provider must fail closed at the assurance adjudication layer, got %v", err)
	}
	// The local probe reports hardened unsupported instead of downgrading.
	report, err := runtime.Provider().Probe(context.Background(), sandbox.ProbeRequest{
		Identity: sandbox.OperationIdentity{
			TaskId:       request.TaskId,
			RunId:        request.RunId,
			AttemptId:    request.AttemptId,
			WorkloadRole: sandbox.WorkloadRoleWorker,
			AllocationId: request.AllocationId,
			Generation:   1,
			FencingToken: "sha256:" + strings.Repeat("e", 64),
			CommandId:    "probe-hardened",
		},
		Requirements: hardenedRequirementsFixture(t),
	})
	if err != nil {
		t.Fatalf("Probe rejected the hardened request identity: %v", err)
	}
	if report.Supported {
		t.Fatal("the local provider must report hardened requests unsupported")
	}
	// The failed claim left no scope bookkeeping: the identical attempt
	// still claims successfully under workspace-write requirements.
	downgraded := request
	downgraded.Requirements = workspaceWriteRequirementsFixture(t)
	if _, err := runtime.ClaimExecution(context.Background(), downgraded); err != nil {
		t.Fatalf("workspace-write claim after the failed hardened claim rejected: %v", err)
	}
}

// TestEmbeddedClaimRejectsSecondActiveAllocation freezes negative fixture 4:
// the identical (runId, attemptId) scope never carries two live claims or
// two active allocations.
func TestEmbeddedClaimRejectsSecondActiveAllocation(t *testing.T) {
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	first, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-dual", "attempt-dual", sandbox.WorkloadRoleWorker, "principal-dual", requirements))
	if err != nil {
		t.Fatalf("first claim rejected: %v", err)
	}

	t.Run("second claim of the identical attempt", func(t *testing.T) {
		second := embeddedClaimRequestFixture("run-dual", "attempt-dual", sandbox.WorkloadRoleWorker, "principal-dual", requirements)
		if _, err := runtime.ClaimExecution(context.Background(), second); err == nil || !strings.Contains(err.Error(), "single-allocation invariant") {
			t.Fatalf("second claim of the identical attempt must be rejected, got %v", err)
		}
	})

	t.Run("second active allocation at the provider", func(t *testing.T) {
		identity := sandbox.OperationIdentity{
			TaskId:       "task-dual-second",
			RunId:        "run-dual",
			AttemptId:    "attempt-dual",
			WorkloadRole: sandbox.WorkloadRoleWorker,
			AllocationId: "allocation-dual-second",
			Generation:   first.Lease.Generation,
			FencingToken: first.Lease.FencingToken,
			CommandId:    "provision-second",
		}
		if _, err := runtime.Provider().Provision(context.Background(), sandbox.ProvisionRequest{Identity: identity, Requirements: requirements}); !errors.Is(err, sandbox.ErrDuplicateActiveAllocation) {
			t.Fatalf("second active allocation must violate the single-active invariant, got %v", err)
		}
	})
}

// TestEmbeddedClaimWorkerVerifierSeparation freezes negative fixture 5 and
// the worker/verifier wiring assertions: the two roles must claim under
// distinct principals and distinct allocations, the verifier reuses the
// accepted worker lease of the identical scope, and cross-role allocation
// reuse is rejected.
func TestEmbeddedClaimWorkerVerifierSeparation(t *testing.T) {
	ctx := context.Background()
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	workerRequest := embeddedClaimRequestFixture("run-sep", "attempt-sep", sandbox.WorkloadRoleWorker, "principal-worker", requirements)
	workerClaim, err := runtime.ClaimExecution(ctx, workerRequest)
	if err != nil {
		t.Fatalf("worker claim rejected: %v", err)
	}

	t.Run("shared principal rejected", func(t *testing.T) {
		shared := embeddedClaimRequestFixture("run-sep", "attempt-sep", sandbox.WorkloadRoleVerifier, "principal-worker", requirements)
		if _, err := runtime.ClaimExecution(ctx, shared); err == nil || !strings.Contains(err.Error(), "must not share the principal") {
			t.Fatalf("worker and verifier sharing a principal must be rejected, got %v", err)
		}
	})

	t.Run("shared allocation rejected", func(t *testing.T) {
		shared := embeddedClaimRequestFixture("run-sep", "attempt-sep", sandbox.WorkloadRoleVerifier, "principal-verifier", requirements)
		shared.AllocationId = workerRequest.AllocationId
		if _, err := runtime.ClaimExecution(ctx, shared); err == nil || !strings.Contains(err.Error(), "distinct allocations") {
			t.Fatalf("worker and verifier sharing an allocation must be rejected, got %v", err)
		}
	})

	t.Run("verifier requires a worker claim", func(t *testing.T) {
		fresh, _, _, _ := newEmbeddedRuntimeFixture(t)
		orphan := embeddedClaimRequestFixture("run-orphan", "attempt-orphan", sandbox.WorkloadRoleVerifier, "principal-verifier", requirements)
		if _, err := fresh.ClaimExecution(ctx, orphan); err == nil || !strings.Contains(err.Error(), "requires an accepted worker claim") {
			t.Fatalf("verifier claim without a worker claim must be rejected, got %v", err)
		}
	})

	// The accepted wiring: terminate the worker allocation, then the
	// verifier claims the identical scope lease under a distinct principal
	// and a distinct allocation.
	workerIdentity := sandbox.OperationIdentity{
		TaskId:       workerRequest.TaskId,
		RunId:        workerRequest.RunId,
		AttemptId:    workerRequest.AttemptId,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: workerRequest.AllocationId,
		Generation:   workerClaim.Lease.Generation,
		FencingToken: workerClaim.Lease.FencingToken,
		CommandId:    "terminate-worker",
	}
	if _, err := runtime.Provider().Terminate(ctx, sandbox.TerminateRequest{Identity: workerIdentity, AllocationId: workerRequest.AllocationId}); err != nil {
		t.Fatalf("terminate worker allocation: %v", err)
	}
	verifierRequest := embeddedClaimRequestFixture("run-sep", "attempt-sep", sandbox.WorkloadRoleVerifier, "principal-verifier", requirements)
	verifierClaim, err := runtime.ClaimExecution(ctx, verifierRequest)
	if err != nil {
		t.Fatalf("verifier claim rejected: %v", err)
	}
	if verifierClaim.Lease.LeaseId != workerClaim.Lease.LeaseId {
		t.Fatal("the verifier must reuse the accepted worker lease of the identical scope")
	}
	if verifierClaim.Allocation.AllocationId == workerClaim.Allocation.AllocationId {
		t.Fatal("the verifier allocation must differ from the worker allocation")
	}
	// Cross-role allocation reuse is rejected fail closed.
	crossRole := verifierIdentityFor(verifierRequest, verifierClaim)
	crossRole.AllocationId = workerRequest.AllocationId
	if _, err := runtime.Provider().Inspect(ctx, sandbox.InspectRequest{Identity: crossRole, AllocationId: workerRequest.AllocationId}); err == nil {
		t.Fatal("cross-role allocation reuse was accepted")
	}
}

func verifierIdentityFor(request EmbeddedClaimRequest, claim EmbeddedClaim) sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       request.TaskId,
		RunId:        request.RunId,
		AttemptId:    request.AttemptId,
		WorkloadRole: sandbox.WorkloadRoleVerifier,
		AllocationId: claim.Allocation.AllocationId,
		Generation:   claim.Lease.Generation,
		FencingToken: claim.Lease.FencingToken,
		CommandId:    "verifier-inspect",
	}
}

// TestEmbeddedLeaseExpiryRevalidateRequiresNewAttempt freezes negative
// fixture 8: after the lease expiry the current-ledger recheck fails closed
// with the deadline cancel reason, the identical attempt can never be
// reissued, and continuation requires a new attempt with a new claim.
func TestEmbeddedLeaseExpiryRevalidateRequiresNewAttempt(t *testing.T) {
	runtime, clock, _, _ := newEmbeddedRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	claim, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-expiry", "attempt-expiry", sandbox.WorkloadRoleWorker, "principal-expiry", requirements))
	if err != nil {
		t.Fatalf("claim rejected: %v", err)
	}
	if err := runtime.RevalidateLease(claim.Lease, requirements); err != nil {
		t.Fatalf("revalidate rejected an in-flight lease before its expiry: %v", err)
	}

	clock.current = clock.current.Add(25 * time.Hour)
	err = runtime.RevalidateLease(claim.Lease, requirements)
	if err == nil || !strings.Contains(err.Error(), string(dispatch.CancelReasonDeadlineExceeded)) {
		t.Fatalf("revalidate past the expiry must fail closed with the deadline cancel reason, got %v", err)
	}
	// The identical attempt can never be reissued after the expiry.
	if _, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-expiry", "attempt-expiry", sandbox.WorkloadRoleWorker, "principal-expiry", requirements)); err == nil {
		t.Fatal("the expired attempt was reissued instead of failing closed")
	}
	// Continuation requires a new attempt with a new claim.
	renewed, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-expiry", "attempt-expiry-2", sandbox.WorkloadRoleWorker, "principal-expiry-2", requirements))
	if err != nil {
		t.Fatalf("new attempt claim after the expiry rejected: %v", err)
	}
	if renewed.Lease.LeaseId == claim.Lease.LeaseId {
		t.Fatal("the new attempt must carry a new lease, never an in-place renewal")
	}
	if renewed.Lease.Generation != 1 {
		t.Fatalf("the new claim must start at generation 1, got %d", renewed.Lease.Generation)
	}
	if err := dispatch.ValidateLeaseFencing(renewed.Lease, renewed.Lease.Generation, renewed.Lease.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the renewed lease: %v", err)
	}
}

// TestEmbeddedLeasePersistsAcrossRestart 锁定 R2 纵切的 durable lease：worker
// claim 在 Provision 成功后写入 append-only 账本（`stateRoot/leases`），
// 崩溃/重启后由全新的 EmbeddedSandboxRuntime 在同一 stateRoot 确定性重放，
// `LeaseFor` 必须恢复同一份 DispatchLease（fencing token、generation、expiry、
// leaseId 逐字相等）——admission 的跨进程 recheck 不再依赖易失内存。
func TestEmbeddedLeasePersistsAcrossRestart(t *testing.T) {
	runtime, clock, stateRoot, _ := newEmbeddedRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	claim, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-durable", "attempt-durable", sandbox.WorkloadRoleWorker, "principal-durable", requirements))
	if err != nil {
		t.Fatalf("claim rejected: %v", err)
	}
	if _, ok := runtime.LeaseFor("run-durable", "attempt-durable"); !ok {
		t.Fatal("in-process LeaseFor must return the freshly claimed worker lease")
	}

	// 崩溃/重启：同一 stateRoot 的全新 runtime（进程内内存态全空）必须
	// 从 append-only 账本重建 scope 索引并恢复同一份 lease。
	restarted, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now)
	if err != nil {
		t.Fatalf("reconstruction over the durable lease ledger rejected: %v", err)
	}
	recovered, ok := restarted.LeaseFor("run-durable", "attempt-durable")
	if !ok {
		t.Fatal("restarted runtime failed to recover the durable worker lease")
	}
	if recovered.LeaseId != claim.Lease.LeaseId ||
		recovered.Generation != claim.Lease.Generation ||
		recovered.FencingToken != claim.Lease.FencingToken ||
		recovered.ExpiresAt != claim.Lease.ExpiresAt ||
		recovered.AllocationId != claim.Lease.AllocationId {
		t.Fatalf("recovered lease diverges from the durable ledger: got %+v, want identity of %+v", recovered, claim.Lease)
	}
	if err := dispatch.ValidateLeaseFencing(recovered, recovered.Generation, recovered.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the recovered lease: %v", err)
	}

	// 崩溃/重启后单活不变量仍成立：同一 (runId, attemptId) 不得二次 claim；
	// 全新 attempt 仍可在原 runtime 与新 runtime 上各自独立 claim。
	if _, err := restarted.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-durable", "attempt-durable", sandbox.WorkloadRoleWorker, "principal-durable", requirements)); err == nil {
		t.Fatal("the recovered identical attempt was reissued instead of failing closed")
	}
	fresh, err := restarted.ClaimExecution(context.Background(), embeddedClaimRequestFixture("run-durable-2", "attempt-durable-2", sandbox.WorkloadRoleWorker, "principal-durable-2", requirements))
	if err != nil {
		t.Fatalf("claiming a fresh attempt over the recovered runtime rejected: %v", err)
	}
	if fresh.Lease.LeaseId == claim.Lease.LeaseId {
		t.Fatal("the fresh attempt must carry a new leaseId")
	}
}

// TestEmbeddedAgentRegistrationPersistsAcrossRestart 锁定 R2 纵切的 durable
// agent registry：注册 + 撤销落账后，同一 stateRoot 的全新 runtime 在
// 崩溃/重启后确定性重放——active 注册跨进程保持可 exact lookup，撤销的注册
// 在重启后仍保持 revoked（不得被重新注册回 active）。admission 的
// AgentAuthority 跨进程不依赖易失内存。
func TestEmbeddedAgentRegistrationPersistsAcrossRestart(t *testing.T) {
	runtime, clock, stateRoot, _ := newEmbeddedRuntimeFixture(t)
	reg := agentregistry.AgentRegistration{
		RegistrationID:       "registration:durable-agent",
		AuthorityNamespaceID: "authority:marshal-local",
		SecurityDomainID:     "default/execution/embedded-pi",
		Principal:            "principal:agent:pi",
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         "pi",
		ProviderVersion:      "0.84.3",
		ProtocolVersion:      "marshal-worker/v1alpha1",
		Scope:                "worker",
		IdempotencyKey:       "cap:sha256:" + strings.Repeat("a", 64),
		RequestDigest:        "sha256:" + strings.Repeat("a", 64),
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            clock.Now().UTC(),
		UpdatedAt:            clock.Now().UTC(),
	}
	if err := runtime.RegisterAgent(reg); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest: "sha256:" + strings.Repeat("b", 64), RegistrationID: reg.RegistrationID,
		ProtocolVersion: reg.ProtocolVersion, ProviderName: reg.ProviderName, ProviderVersion: reg.ProviderVersion,
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{"sha256:" + strings.Repeat("c", 64)},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if err := runtime.RegisterAgentSnapshot(snap); err != nil {
		t.Fatalf("RegisterAgentSnapshot: %v", err)
	}
	if gotReg, gotSnap, err := runtime.AgentAuthority(reg.RegistrationID); err != nil || gotReg.LifecycleState != agentregistry.LifecycleStateActive || gotSnap.SnapshotDigest != snap.SnapshotDigest {
		t.Fatalf("in-process authority must be active and exact, reg=%+v snap=%+v err=%v", gotReg, gotSnap, err)
	}

	// 崩溃/重启（第一个 runtime 仍 active 时）：同一 ID 跨进程恢复为 active。
	restarted, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now)
	if err != nil {
		t.Fatalf("reconstruction over the durable agent registry rejected: %v", err)
	}
	if gotReg, gotSnap, err := restarted.AgentAuthority(reg.RegistrationID); err != nil || gotReg.LifecycleState != agentregistry.LifecycleStateActive || gotSnap.SnapshotDigest != snap.SnapshotDigest {
		t.Fatalf("restarted runtime must recover exact active authority, reg=%+v snap=%+v err=%v", gotReg, gotSnap, err)
	}

	// 撤销落账后崩溃/重启：revoked 在恢复后仍保持 revoked，且不能重新注册回 active。
	if err := runtime.RevokeAgent(reg.RegistrationID); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}
	revokedRuntime, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now)
	if err != nil {
		t.Fatalf("reconstruction after revoke rejected: %v", err)
	}
	if gotReg, gotSnap, err := revokedRuntime.AgentAuthority(reg.RegistrationID); err != nil || gotReg.LifecycleState != agentregistry.LifecycleStateRevoked || gotSnap.SnapshotDigest != snap.SnapshotDigest {
		t.Fatalf("revocation and snapshot must persist across restart, reg=%+v snap=%+v err=%v", gotReg, gotSnap, err)
	}
	// 终态注册不得被重新注册回 active。
	if _, err := revokedRuntime.agentRegistry.Reactivate(reg.RegistrationID); err == nil {
		t.Fatal("a revoked (terminal) registration must not be reactivated")
	}
}
