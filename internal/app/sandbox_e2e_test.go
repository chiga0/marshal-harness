package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/execution"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/sandbox"
	"github.com/chiga0/marshal-harness/internal/sandbox/local"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// e2eFakeAdapter is the in-process AgentAdapter of the embedded E2E: Probe
// returns the frozen capability and Run writes one declared change into the
// attempt worktree before returning a schema-valid WorkerResult.
type e2eFakeAdapter struct {
	id         string
	capability []byte
}

func (a *e2eFakeAdapter) ID() string { return a.id }

func (a *e2eFakeAdapter) Probe(context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: a.capability}, nil
}

func (a *e2eFakeAdapter) Run(_ context.Context, request domain.Record) (domain.Record, error) {
	var input struct {
		TaskID       string `json:"taskId"`
		RunID        string `json:"runId"`
		AttemptID    string `json:"attemptId"`
		WorktreePath string `json:"worktreePath"`
	}
	if err := json.Unmarshal(request.Data, &input); err != nil {
		return domain.Record{}, err
	}
	if err := os.WriteFile(filepath.Join(input.WorktreePath, "change.txt"), []byte("embedded e2e change\n"), 0o600); err != nil {
		return domain.Record{}, err
	}
	result := map[string]any{
		"apiVersion":           "marshal.dev/v1alpha1",
		"kind":                 "WorkerResult",
		"taskId":               input.TaskID,
		"runId":                input.RunID,
		"attemptId":            input.AttemptID,
		"adapter":              map[string]any{"id": a.id, "executable": "/fixture/adapter", "version": "e2e"},
		"status":               "completed",
		"summary":              "embedded e2e worker completed",
		"declaredChangedFiles": []string{"change.txt"},
		"declaredArtifacts":    []any{},
		"declaredCommands":     []any{},
		"declaredRisks":        []string{},
		"startedAt":            "2026-08-13T12:10:00Z",
		"completedAt":          "2026-08-13T12:15:00Z",
	}
	data, err := json.Marshal(result)
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, err
}

// e2eControlledExecutor is the deterministic executor seam of the E2E: only
// the controlled echo-shaped argv is admitted, and every execution is
// recorded for the wiring assertions.
func e2eControlledExecutor(log *[]string) local.CommandExecutor {
	return func(ctx context.Context, spec local.ExecSpec) local.ExecOutcome {
		if len(spec.Argv) == 0 {
			return local.ExecOutcome{Started: false, ExitCode: -1, Stderr: []byte("empty argv\n")}
		}
		*log = append(*log, strings.Join(spec.Argv, " "))
		if spec.Argv[0] != "echo" {
			return local.ExecOutcome{Started: true, ExitCode: 127, Stderr: []byte("restricted argv\n")}
		}
		return local.ExecOutcome{Started: true, ExitCode: 0, Stdout: []byte(strings.Join(spec.Argv[1:], " ") + "\n")}
	}
}

func e2eGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

// e2eRepositoryFixture builds the hermetic git repository the E2E plans,
// executes and verifies against.
func e2eRepositoryFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	e2eGit(t, root, "init", "-q")
	e2eGit(t, root, "config", "user.name", "Marshal E2E")
	e2eGit(t, root, "config", "user.email", "e2e@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("e2e base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e2eGit(t, root, "add", "README.md")
	e2eGit(t, root, "commit", "-q", "-m", "base")
	e2eGit(t, root, "remote", "add", "origin", "https://example.invalid/e2e.git")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the real worktree identity: %v", err)
	}
	return canonicalRoot, e2eGit(t, root, "rev-parse", "HEAD")
}

func e2eMustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func e2eTaskFixture(t *testing.T, repositoryRoot, taskID, adapterID, baseSHA string) []byte {
	t.Helper()
	return e2eMustMarshal(t, map[string]any{
		"apiVersion":   "marshal.dev/v1alpha1",
		"kind":         "Task",
		"metadata":     map[string]any{"id": taskID, "title": "embedded e2e"},
		"repository":   map[string]any{"path": repositoryRoot, "baseRef": baseSHA, "remote": "origin"},
		"work":         map[string]any{"objective": "embedded e2e objective", "constraints": []string{}, "nonGoals": []string{}},
		"scope":        map[string]any{"allowPaths": []string{"change.txt"}, "denyPaths": []string{}, "allowSubmodules": false, "maxChangedFiles": 4, "maxDiffBytes": 10000},
		"acceptance":   map[string]any{"commands": []any{}, "allowNoChange": false},
		"deliverables": []any{map[string]any{"id": "code", "kind": "code", "required": true, "pathGlob": "change.txt"}},
		// The synthetic worker declares no worker.tools allowlist: the
		// tool-allowlist/tool-audit gates require transcript-meta evidence a
		// synthetic in-process adapter never produces, and an absent
		// declaration keeps both gates skipped (non-required), preserving the
		// hermetic in-process chain.
		"worker":      map[string]any{"preferredAdapter": adapterID, "fallbackAdapters": []string{}, "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"},
		"budgets":     map[string]any{"runTimeoutSeconds": 600, "attemptTimeoutSeconds": 300, "maxAttempts": 3, "maxOperationalRetries": 1, "maxReworkRounds": 1, "maxOutputBytes": 1000000},
		"publication": map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	})
}

// e2eSealPolicy computes the detached policyDigest exactly as
// planning.ValidatePolicy recomputes it: blank the field, canonicalize,
// digest.
func e2eSealPolicy(t *testing.T, policy map[string]any) []byte {
	t.Helper()
	policy["policyDigest"] = ""
	raw := e2eMustMarshal(t, policy)
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = canonical.DigestBytes(canonicalized)
	return e2eMustMarshal(t, policy)
}

func e2ePolicyFixture(t *testing.T, taskID, runID, adapterID string) []byte {
	t.Helper()
	return e2eSealPolicy(t, map[string]any{
		"apiVersion":   "marshal.dev/v1alpha1",
		"kind":         "PolicySnapshot",
		"taskId":       taskID,
		"runId":        runID,
		"sources":      []any{map[string]any{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"effective":    map[string]any{"minimumExecutionProfile": "workspace-write", "requireEnforcedNetworkPolicy": false, "networkPolicy": "unenforced", "allowFallbackWorkers": false, "allowWorkerSubagents": false, "allowPublication": false, "allowMerge": false, "allowGateWaivers": false, "allowedAdapters": []string{adapterID}, "environmentAllowlist": []string{"PATH"}, "retentionDays": 1},
		"policyDigest": "",
		"generatedAt":  "2026-08-13T11:00:00Z",
	})
}

func e2eCapabilityFixture(t *testing.T, adapterID string) []byte {
	t.Helper()
	return e2eMustMarshal(t, map[string]any{
		"apiVersion":       "marshal.dev/v1alpha1",
		"kind":             "CapabilitySnapshot",
		"adapterId":        adapterID,
		"adapterVersion":   "0.1.0",
		"executable":       "/fixture/adapter",
		"executableDigest": "sha256:" + strings.Repeat("a", 64),
		"binaryVersion":    "1",
		"probeStatus":      "supported",
		"capabilities":     map[string]any{"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true, "sessionPolicies": []string{"ephemeral"}, "modelSelection": false, "executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{}, "processTreeCancellation": true, "notes": []string{}},
		"probeErrors":      []string{},
		"probedAt":         "2026-08-13T11:00:00Z",
	})
}

// TestEmbeddedSandboxE2EFullChain proves the complete M8 embedded vertical
// slice in one hermetic in-process chain: idempotent registration
// submission → frozen Run → durable READY → Matcher.Claim + fencing → Local
// provider Exec/checkpoint/log evidence → independent Verifier sandbox →
// REVIEW_PENDING → ACCEPTED, without any publication.
func TestEmbeddedSandboxE2EFullChain(t *testing.T) {
	sealedMigrationSkip(t)
	ctx := context.Background()
	repositoryRoot, baseSHA := e2eRepositoryFixture(t)
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	// The embedded authority scope is taken from the real worktree
	// repository identity: bind the identity record of the fixture
	// worktree exactly as repository.State.Init persists it.
	if err := (repository.State{RepositoryRoot: repositoryRoot, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind the real worktree repository identity: %v", err)
	}
	clock := &embeddedTestClock{current: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	var execLog []string
	runtime, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now, WithLocalRunnerOptions(local.WithExecutor(e2eControlledExecutor(&execLog))))
	if err != nil {
		t.Fatalf("NewEmbeddedSandboxRuntime: %v", err)
	}
	wantScope := "repo:" + filepath.ToSlash(repositoryRoot)
	if runtime.Namespace().AuthorityScopeId != wantScope {
		t.Fatalf("the embedded authority scope must be taken from the real worktree repository identity: got %q, want %q", runtime.Namespace().AuthorityScopeId, wantScope)
	}

	const (
		taskID    = "task-e2e-embedded"
		runID     = "run-e2e-embedded"
		adapterID = "e2e-fake"
	)

	// Step 1 — idempotent submission: the identical registration replay
	// merges, the same key with a different digest conflicts fail closed.
	if _, err := NewEmbeddedSandboxRuntime(stateRoot, clock.Now, WithLocalRunnerOptions(local.WithExecutor(e2eControlledExecutor(&execLog)))); err != nil {
		t.Fatalf("idempotent registration replay rejected: %v", err)
	}
	if lines := embeddedLedgerLines(t, stateRoot); lines != 1 {
		t.Fatalf("idempotent replay must keep exactly one registration fact, got %d", lines)
	}
	conflict := runtime.Registration()
	conflict.RequestDigest = sandbox.RecomputeSHA256([]byte("conflicting" + "-request"))
	conflictDigest, err := conflict.Digest()
	if err != nil {
		t.Fatal(err)
	}
	conflict.RegistrationDigest = conflictDigest
	if _, err := runtime.RegistrationStore().Put(conflict); !errors.Is(err, provider.ErrRegistrationConflict) {
		t.Fatalf("same idempotencyKey with a different digest must conflict, got %v", err)
	}

	// Step 2 — freeze the Run: planning advances CREATED → PLANNED → READY
	// and records the two-dimensional sandbox requirements in the freeze
	// event; the READY snapshot is durable.
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	capability := e2eCapabilityFixture(t, adapterID)
	fakeWorker := &e2eFakeAdapter{id: adapterID, capability: capability}
	registry := adapter.NewRegistry()
	if err := registry.Register(fakeWorker); err != nil {
		t.Fatal(err)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := planning.Plan(ctx, planning.Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       e2eTaskFixture(t, repositoryRoot, taskID, adapterID, baseSHA),
		PolicySnapshot: e2ePolicyFixture(t, taskID, runID, adapterID),
		Selector:       selector,
		Validator:      validator,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planResult.State.State != domain.StateReady {
		t.Fatalf("plan state = %s, want READY", planResult.State.State)
	}
	store := runstore.New(stateRoot)
	events, truncated, err := store.ReadEvents(runID)
	if err != nil || truncated || len(events) != 2 {
		t.Fatalf("durable READY journal: events=%d truncated=%v err=%v", len(events), truncated, err)
	}
	requirements, recorded := events[1].Payload["sandboxRequirements"].(map[string]any)
	if !recorded || requirements["accessMode"] != "workspace-write" || requirements["minimumAssuranceLevel"] != "workspace-write" {
		t.Fatalf("the READY freeze event must record the two-dimensional sandbox requirements: %#v", events[1].Payload)
	}
	state, err := store.Inspect(runID)
	if err != nil || state.State != domain.StateReady {
		t.Fatalf("durable READY snapshot: state=%+v err=%v", state.State, err)
	}

	// Step 3 — claim + fencing → dispatch-bound Local execution: the binder
	// claims the gate-6 lease for the attempt, admission validates the
	// fencing, and the fake adapter completes the attempt into VERIFYING.
	runResult, err := execution.Run(ctx, execution.Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		Adapter:        fakeWorker,
		Validator:      validator,
		DispatchBinder: runtime,
	})
	if err != nil {
		t.Fatalf("dispatch-bound execution.Run: %v", err)
	}
	if runResult.State.State != domain.StateVerifying {
		t.Fatalf("execution state = %s, want VERIFYING", runResult.State.State)
	}
	lease, bound := runtime.LeaseFor(runID, runResult.AttemptID)
	if !bound {
		t.Fatal("no dispatch lease is bound to the accepted attempt")
	}
	if err := lease.Validate(); err != nil {
		t.Fatalf("the bound lease does not validate: %v", err)
	}
	if err := dispatch.ValidateLeaseFencing(lease, lease.Generation, lease.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the bound lease: %v", err)
	}
	if lease.AuthorityNamespaceId.AuthorityScopeId != wantScope {
		t.Fatalf("the bound lease must carry the real worktree repository identity scope: got %q, want %q", lease.AuthorityNamespaceId.AuthorityScopeId, wantScope)
	}
	// Hardened requirements against the Local provider fail closed at the
	// assurance adjudication layer — before any capabilities negotiation —
	// without downgrade to workspace-write.
	hardenedRequest := EmbeddedClaimRequest{
		TaskId:       taskID,
		RunId:        runID,
		AttemptId:    runResult.AttemptID + "-hardened",
		AllocationId: embeddedAllocationID(runID, runResult.AttemptID+"-hardened", sandbox.WorkloadRoleWorker),
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    "e2e-hardened-principal",
		Requirements: hardenedRequirementsFixture(t),
	}
	if _, err := runtime.ClaimExecution(ctx, hardenedRequest); err == nil || !strings.Contains(err.Error(), "assurance adjudication") || !strings.Contains(err.Error(), "fail closed without downgrade") {
		t.Fatalf("hardened claim must fail closed at the assurance adjudication layer, got %v", err)
	}
	workerAllocationID := runtime.WorkerAllocationID(runID, runResult.AttemptID)
	workerIdentity := sandbox.OperationIdentity{
		TaskId:       taskID,
		RunId:        runID,
		AttemptId:    runResult.AttemptID,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: workerAllocationID,
		Generation:   lease.Generation,
		FencingToken: lease.FencingToken,
		CommandId:    "e2e-worker-exec",
	}

	// Step 4 — Local provider Exec, checkpoint and log evidence.
	execReceipt, err := runtime.Provider().Exec(ctx, sandbox.ExecRequest{Identity: workerIdentity, AllocationId: workerAllocationID, Command: []string{"echo", "embedded-e2e"}})
	if err != nil {
		t.Fatalf("local provider Exec: %v", err)
	}
	if execReceipt.Status != sandbox.ExecutionCompleted || execReceipt.ExitCode != 0 {
		t.Fatalf("exec receipt = %+v, want completed exit 0", execReceipt)
	}
	if execReceipt.StdoutSHA256 != sandbox.RecomputeSHA256([]byte("embedded-e2e\n")) {
		t.Fatalf("exec stdout digest = %q", execReceipt.StdoutSHA256)
	}
	firstCheckpoint, err := runtime.Provider().Checkpoint(ctx, sandbox.CheckpointRequest{Identity: workerIdentity, AllocationId: workerAllocationID})
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	secondCheckpoint, err := runtime.Provider().Checkpoint(ctx, sandbox.CheckpointRequest{Identity: workerIdentity, AllocationId: workerAllocationID})
	if err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if firstCheckpoint.SHA256 != secondCheckpoint.SHA256 || firstCheckpoint.CheckpointId == secondCheckpoint.CheckpointId {
		t.Fatalf("checkpoints must be deterministic with advancing ids: %+v vs %+v", firstCheckpoint, secondCheckpoint)
	}
	inspectReport, err := runtime.Provider().Inspect(ctx, sandbox.InspectRequest{Identity: workerIdentity, AllocationId: workerAllocationID})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspectReport.State != sandbox.AllocationActive || len(inspectReport.Violations) != 0 {
		t.Fatalf("inspect report = %+v, want active without violations", inspectReport)
	}
	sawExecLog := false
	for _, line := range inspectReport.LogLines {
		if strings.Contains(line, "exec: echo status=completed") {
			sawExecLog = true
		}
	}
	if !sawExecLog {
		t.Fatalf("inspect log must carry the exec observation: %v", inspectReport.LogLines)
	}

	// Step 5 — independent Verifier sandbox: the worker allocation ends, the
	// verifier claims the identical scope under a distinct principal and a
	// distinct allocation, observes independently, and cross-role reuse is
	// rejected.
	if _, err := runtime.Provider().Terminate(ctx, sandbox.TerminateRequest{Identity: workerIdentity, AllocationId: workerAllocationID}); err != nil {
		t.Fatalf("terminate worker allocation: %v", err)
	}
	workerRequirements := workspaceWriteRequirementsFixture(t)
	verifierRequest := EmbeddedClaimRequest{
		TaskId:       taskID,
		RunId:        runID,
		AttemptId:    runResult.AttemptID,
		AllocationId: embeddedAllocationID(runID, runResult.AttemptID, sandbox.WorkloadRoleVerifier),
		WorkloadRole: sandbox.WorkloadRoleVerifier,
		Principal:    "e2e-verifier-principal",
		Requirements: workerRequirements,
	}
	verifierClaim, err := runtime.ClaimExecution(ctx, verifierRequest)
	if err != nil {
		t.Fatalf("verifier claim: %v", err)
	}
	if verifierClaim.Lease.LeaseId != lease.LeaseId {
		t.Fatal("the verifier sandbox must bind the identical accepted claim")
	}
	if verifierClaim.Allocation.AllocationId == workerAllocationID {
		t.Fatal("the verifier allocation must differ from the worker allocation")
	}
	verifierIdentity := sandbox.OperationIdentity{
		TaskId:       taskID,
		RunId:        runID,
		AttemptId:    runResult.AttemptID,
		WorkloadRole: sandbox.WorkloadRoleVerifier,
		AllocationId: verifierClaim.Allocation.AllocationId,
		Generation:   verifierClaim.Lease.Generation,
		FencingToken: verifierClaim.Lease.FencingToken,
		CommandId:    "e2e-verifier-exec",
	}
	verifierReceipt, err := runtime.Provider().Exec(ctx, sandbox.ExecRequest{Identity: verifierIdentity, AllocationId: verifierClaim.Allocation.AllocationId, Command: []string{"echo", "verifier-observed"}})
	if err != nil || verifierReceipt.Status != sandbox.ExecutionCompleted {
		t.Fatalf("verifier exec: receipt=%+v err=%v", verifierReceipt, err)
	}
	verifierReport, err := runtime.Provider().Inspect(ctx, sandbox.InspectRequest{Identity: verifierIdentity, AllocationId: verifierClaim.Allocation.AllocationId})
	if err != nil || verifierReport.State != sandbox.AllocationActive {
		t.Fatalf("verifier inspect: report=%+v err=%v", verifierReport, err)
	}
	crossRole := verifierIdentity
	crossRole.AllocationId = workerAllocationID
	if _, err := runtime.Provider().Inspect(ctx, sandbox.InspectRequest{Identity: crossRole, AllocationId: workerAllocationID}); err == nil {
		t.Fatal("cross-role allocation reuse was accepted")
	}
	if len(execLog) < 2 {
		t.Fatalf("the controlled executor must observe the worker and the verifier executions: %v", execLog)
	}

	// Step 6 — independent verification drives VERIFYING → REVIEW_PENDING.
	repositoryIdentity, err := gitworktree.Open(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(stateRoot, "runs", runID)
	verifyResult, err := verification.New().Verify(ctx, verification.Input{
		TaskID:            taskID,
		RunID:             runID,
		SpecDigest:        state.SpecDigest,
		BaseSHA:           baseSHA,
		Worktree:          state.WorktreePath,
		ExpectedCommonDir: repositoryIdentity.CommonDir,
		RunDirectory:      runDir,
		Scope: verification.ScopePolicy{
			AllowPaths:      []string{"change.txt"},
			DenyPaths:       []string{},
			AllowSubmodules: false,
			MaxChangedFiles: 4,
			MaxDiffBytes:    10000,
		},
		Deliverables:      []verification.Deliverable{{ID: "code", Kind: "code", Required: true, PathGlob: "change.txt"}},
		Commands:          []verification.CommandSpec{},
		PatchCaptureBytes: 10001,
	})
	if err != nil || verifyResult.Report.Status != "pass" {
		t.Fatalf("verification: status=%q gates=%+v err=%v", verifyResult.Report.Status, verifyResult.Report.Gates, err)
	}
	reportData, err := os.ReadFile(filepath.Join(runDir, "verification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(runDir, "artifact-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	reportDigest, err := canonical.DigestJSON(reportData)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := canonical.DigestJSON(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	var report verification.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	var manifest verification.ArtifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	runLease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer runLease.Release()
	verifyingState, err := store.Inspect(runID)
	if err != nil || verifyingState.State != domain.StateVerifying {
		t.Fatalf("pre-verify state = %+v err=%v", verifyingState.State, err)
	}
	verifyEventID, err := domain.NewID("event")
	if err != nil {
		t.Fatal(err)
	}
	verifyEvent := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindRunEvent,
		EventID:    verifyEventID,
		RunID:      runID,
		Sequence:   verifyingState.Sequence + 1,
		Type:       "verification.completed",
		StateFrom:  domain.StateVerifying,
		StateTo:    domain.StateReviewPending,
		Timestamp:  verifyResult.Report.CompletedAt,
		Actor:      &domain.Actor{Type: "system", ID: "marshal-verifier"},
		Payload:    map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": verifyResult.Report.Status},
	}
	reviewPendingState, err := lifecycle.Reduce(verifyingState, verifyEvent, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, ReportComplete: true})
	if err != nil {
		t.Fatalf("verification transition: %v", err)
	}
	if reviewPendingState.State != domain.StateReviewPending {
		t.Fatalf("post-verify state = %s, want REVIEW_PENDING", reviewPendingState.State)
	}
	if err := store.Append(runLease, verifyEvent, verifyingState.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(runLease, reviewPendingState); err != nil {
		t.Fatal(err)
	}

	// Step 7 — review accept drives REVIEW_PENDING → ACCEPTED without any
	// publication.
	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	observation, err := verification.ObserveContext(ctx, state.WorktreePath, baseSHA, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if err := review.ValidateCurrentObservation(report, observation); err != nil {
		t.Fatalf("worktree evidence changed after verification: %v", err)
	}
	builder := review.PacketBuilder{RunDirectory: runDir, Validator: validator}
	packet, packetDigest, err := builder.Build(review.PacketBuildInput{
		Task:         task,
		TaskData:     taskData,
		Report:       report,
		ReportData:   reportData,
		Manifest:     manifest,
		ManifestData: manifestData,
		TaskID:       taskID,
		RunID:        runID,
		SpecDigest:   state.SpecDigest,
		BaseSHA:      baseSHA,
		ReviewRound:  reviewPendingState.ReviewRound,
		AttemptsUsed: reviewPendingState.AttemptsUsed,
	})
	if err != nil {
		t.Fatalf("build review packet: %v", err)
	}
	decision := domain.ReviewDecision{
		APIVersion:                domain.APIVersionV1Alpha1,
		Kind:                      domain.KindReviewDecision,
		TaskID:                    taskID,
		RunID:                     runID,
		ReviewRound:               reviewPendingState.ReviewRound,
		Reviewer:                  domain.Reviewer{Type: "lead-agent", ID: "e2e-reviewer"},
		SpecDigest:                state.SpecDigest,
		ReviewPacketDigest:        packetDigest,
		VerificationDigest:        reportDigest,
		ArtifactManifestDigest:    manifestDigest,
		EvidenceDigest:            packet.EvidenceDigest,
		Verdict:                   "accept",
		Summary:                   "embedded e2e accept",
		BlockingFindings:          []domain.Finding{},
		NonBlockingFindings:       []domain.Finding{},
		PublicationRecommendation: "do-not-publish",
		MergeRecommendation:       "do-not-merge",
		DecidedAt:                 time.Now().UTC(),
	}
	decisionData := e2eMustMarshal(t, decision)
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		t.Fatalf("decision fixture fails contract: %v", err)
	}
	decisionPath := filepath.Join(t.TempDir(), "review-decision.json")
	if err := os.WriteFile(decisionPath, decisionData, 0o600); err != nil {
		t.Fatal(err)
	}
	importer := review.DecisionImporter{RunDirectory: runDir, Validator: validator}
	reviewResult, err := importer.Import(review.DecisionInput{
		Path:             decisionPath,
		Task:             task,
		TaskID:           taskID,
		RunID:            runID,
		SpecDigest:       state.SpecDigest,
		ReviewRound:      reviewPendingState.ReviewRound,
		AttemptsUsed:     reviewPendingState.AttemptsUsed,
		ReworkRoundsUsed: reviewPendingState.ReworkRoundsUsed,
		Report:           report,
		Manifest:         manifest,
	})
	if err != nil {
		t.Fatalf("import review decision: %v", err)
	}
	if reviewResult.TargetState != domain.StateAccepted {
		t.Fatalf("accept target state = %s, want ACCEPTED", reviewResult.TargetState)
	}
	reviewEventID, err := domain.NewID("event")
	if err != nil {
		t.Fatal(err)
	}
	reviewEvent := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindRunEvent,
		EventID:    reviewEventID,
		RunID:      runID,
		Sequence:   reviewPendingState.Sequence + 1,
		Type:       "review.accept",
		StateFrom:  domain.StateReviewPending,
		StateTo:    reviewResult.TargetState,
		Timestamp:  time.Now().UTC(),
		Actor:      &domain.Actor{Type: "system", ID: "marshal-review"},
		Payload:    map[string]any{"verdict": reviewResult.Decision.Verdict, "decisionDigest": reviewResult.DecisionDigest, "evidenceDigest": reviewResult.Decision.EvidenceDigest, "terminalReason": reviewResult.Decision.Summary},
	}
	finalState, err := lifecycle.Reduce(reviewPendingState, reviewEvent, lifecycle.Guard{
		LeaseHeld:         true,
		EvidenceCurrent:   true,
		RequiredGatesPass: report.Status == "pass",
		DecisionCurrent:   true,
		NoChangeAllowed:   task.Acceptance.AllowNoChange,
		BudgetAvailable:   !reviewResult.BudgetExhausted,
	})
	if err != nil {
		t.Fatalf("review transition: %v", err)
	}
	outcome := review.TerminalOutcome(taskID, runID, finalState.State, reviewResult, time.Now().UTC())
	if outcome == nil {
		t.Fatal("ACCEPTED must be terminal and carry an outcome bundle")
	}
	prepared, err := review.PrepareRecords(runDir, reviewResult, outcome)
	if err != nil {
		t.Fatalf("prepare review records: %v", err)
	}
	if err := store.Append(runLease, reviewEvent, reviewPendingState.Sequence); err != nil {
		prepared.Abort()
		t.Fatal(err)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(runLease, finalState); err != nil {
		t.Fatal(err)
	}

	// Step 8 — terminal assertions: ACCEPTED, no publication, durable
	// outcome.
	if finalState.State != domain.StateAccepted {
		t.Fatalf("final state = %s, want ACCEPTED", finalState.State)
	}
	if finalState.Publication != nil {
		t.Fatalf("the embedded chain must not publish: %+v", finalState.Publication)
	}
	finalEvents, truncated, err := store.ReadEvents(runID)
	if err != nil || truncated {
		t.Fatalf("read final journal: truncated=%v err=%v", truncated, err)
	}
	for _, event := range finalEvents {
		if strings.HasPrefix(event.Type, "publication.") {
			t.Fatalf("the embedded chain recorded a publication event: %s", event.Type)
		}
	}
	terminalRecords := []string{
		"outcome.json",
		"result.md",
		filepath.Join("decisions", fmt.Sprintf("decision-%03d.json", reviewPendingState.ReviewRound)),
	}
	for _, name := range terminalRecords {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Fatalf("terminal record %s missing: %v", name, err)
		}
	}
}
