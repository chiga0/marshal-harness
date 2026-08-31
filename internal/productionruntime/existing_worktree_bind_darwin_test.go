//go:build darwin && arm64

package productionruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// pathBTestGit runs the fixed git binary in the given repository.
func pathBTestGit(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}

// pathBTestRepo creates a git repository with a base commit and a linked
// worktree at worktreePath, returning the base SHA. The repository's .git is
// a directory (main worktree) and the linked worktree's .git is a file.
func pathBTestRepo(t *testing.T, repository, worktreePath string) string {
	t.Helper()
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	pathBTestGit(t, repository, "init", "-q")
	pathBTestGit(t, repository, "config", "user.email", "marshal@example.invalid")
	pathBTestGit(t, repository, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathBTestGit(t, repository, "add", "tracked.txt")
	pathBTestGit(t, repository, "commit", "-q", "-m", "base")
	baseSHA := strings.TrimSpace(string(pathBTestGit(t, repository, "rev-parse", "HEAD")))
	pathBTestGit(t, repository, "worktree", "add", "-q", "--detach", worktreePath, baseSHA)
	// The bind observation requires the target worktree and its Git admin
	// directory to be owner-private (verifyPrivateDirectory enforces 0o700).
	if err := os.Chmod(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	dotGitRaw, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	adminLine := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(dotGitRaw)), "gitdir: "))
	if adminLine == "" || !filepath.IsAbs(adminLine) {
		t.Fatalf("worktree .git gitdir unexpected: %q", string(dotGitRaw))
	}
	if err := os.Chmod(filepath.Clean(adminLine), 0o700); err != nil {
		t.Fatal(err)
	}
	return baseSHA
}

// pathBDescriptorGraph builds the held descriptor graph for a main-worktree
// repository (where .git is a directory) plus the held target worktree
// descriptor. All handles are cleaned up via t.Cleanup.
func pathBDescriptorGraph(t *testing.T, repository, worktreePath string) (allocationcontrol.ExistingWorktreeDescriptorGraphV1, *os.File) {
	t.Helper()
	open := func(path string) *os.File {
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	filesystemRoot := open("/")
	repositoryParent := open(filepath.Dir(repository))
	repositoryRoot := open(repository)
	repositoryCommon := open(filepath.Join(repository, ".git"))
	graph, err := allocationcontrol.NewExistingWorktreeDescriptorGraph(filesystemRoot, repositoryParent, repositoryRoot, repositoryCommon, filepath.Base(repository))
	if err != nil {
		t.Fatalf("descriptor graph: %v", err)
	}
	target := open(worktreePath)
	return graph, target
}

// pathBCompositionInputs builds a full CompositionInputs wired for path B:
// a READY Run whose WorktreePath is a real linked git worktree, plus the held
// descriptor graph and target. The closure's working directory is the target
// worktree (path B does not re-seal it to staging).
func pathBCompositionInputs(t *testing.T) (CompositionInputs, string, string, string) {
	t.Helper()
	ownerFixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, ownerFixture)
	acquisition := acquisitionAtEpoch(1)
	fixed, fixedErr := os.Executable()
	if fixedErr != nil {
		t.Fatal(fixedErr)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(fixed); resolveErr != nil {
		t.Fatal(resolveErr)
	} else {
		fixed = resolved
	}
	repository := filepath.Join(ownerFixture.base, "repository")
	worktreePath := filepath.Join(ownerFixture.base, "worktree")
	baseSHA := pathBTestRepo(t, repository, worktreePath)
	graph, target := pathBDescriptorGraph(t, repository, worktreePath)

	runStore := runstore.New(filepath.Join(ownerFixture.base, "run-store"))
	runID := "run:composition-pathb"
	// Run initialization uses a temporary lease that is released before the
	// ledger is composed. Path B (ADR 0069 lock order: repository owner → Run
	// Lease) acquires the Run Lease inside NewCompositionLedger, so the formal
	// inputs must carry no pre-held lease. The cleanup is a safety net for a
	// mid-init failure; the explicit release below makes it a no-op on success.
	initLease, err := runStore.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = initLease.Release() })
	timestamp := time.Unix(1_800_000_000, 0).UTC()
	planned := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:pathb-planned", RunID: runID, Sequence: 1, Type: "run.transition", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: timestamp, Payload: map[string]any{}}
	readyEvent := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:pathb-ready", RunID: runID, Sequence: 2, Type: "run.transition", StateFrom: domain.StatePlanned, StateTo: domain.StateReady, Timestamp: timestamp.Add(time.Second), Payload: map[string]any{}}
	specDigest := canonical.DigestBytes([]byte("pathb-spec"))
	policyDigest := canonical.DigestBytes([]byte("pathb-policy"))
	capabilityDigest := canonical.DigestBytes([]byte("pathb-capability"))
	readyEvent.Payload = map[string]any{"specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capabilityDigest, "baseSha": baseSHA, "worktreePath": worktreePath, "maxAttempts": 3}
	if err := runStore.Append(initLease, planned, 0); err != nil {
		t.Fatal(err)
	}
	if err := runStore.Append(initLease, readyEvent, 1); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:composition-pathb", runID, timestamp)
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, readyEvent)
	if err != nil {
		t.Fatal(err)
	}
	state.SpecDigest, state.PolicyDigest, state.CapabilityDigest = specDigest, policyDigest, capabilityDigest
	state.BaseSHA, state.WorktreePath = baseSHA, worktreePath
	if err := runStore.WriteSnapshot(initLease, state); err != nil {
		t.Fatal(err)
	}
	if err := initLease.Release(); err != nil {
		t.Fatal(err)
	}

	leaseLedger, err := dispatch.NewLeaseLedger(filepath.Join(ownerFixture.base, "dispatch-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	closure := compositionTestClosure(t)
	reSealed, sealErr := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: closure.RuntimeExecutable, ClosureProfileID: closure.ClosureProfileID,
		MaterialRoots: closure.MaterialRoots, LaunchMaterials: closure.LaunchMaterials,
		Arguments: closure.Arguments, Environment: closure.Environment, WorkingDirectory: worktreePath,
	})
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	inputs := CompositionInputs{
		Ingress: store, Runs: runStore, RunLease: nil, LeaseLedger: leaseLedger,
		OwnerDirectory: ownerFixture.directory, Acquisition: acquisition, RunID: runID,
		Namespace: acquisition.Scope.AuthorityNamespaceID, OrchestratorID: "orchestrator:composition-pathb",
		ProvisionDomain: authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process"},
		CleanupDomain:   authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process-cleanup"},
		RegistrationID:  "registration-composition-pathb", CapabilitySnapshot: canonical.DigestBytes([]byte("pathb-snapshot")),
		ConformanceEvidence: []string{}, Attestation: provider.Attestation{ProviderInstanceId: "provider-instance-pathb", ConfigDigest: canonical.DigestBytes([]byte("config")), TrustRootKeyId: "trust-root-1", TrustRootAlgorithm: "ed25519"},
		AllocationRoot: filepath.Join(ownerFixture.base, "allocations"), LaunchClosure: reSealed,
		Requirements:     allocationcontrol.SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		WorkDirAllowlist: []string{worktreePath}, EnvironmentAllowlist: []string{"PATH"},
		ExistingWorktreeDescriptorGraph: graph,
		ExistingWorktreeTargetWorktree:  target,
	}
	return inputs, runID, worktreePath, ownerFixture.base
}

func pathBProjection(t *testing.T, ledger *CompositionLedger) runstore.RunStartAuthorityProjection {
	t.Helper()
	projection, err := ledger.runs.ReadRunStartAuthorityUnderLease(context.Background(), ledger.runLease)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

// pathBLedgerBytes reads the ResultIngress ledger bytes for the fixture so
// tests can prove no new bind authority facts were appended.
func pathBLedgerBytes(t *testing.T, base string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, "result-ingress", "result-ingress.jsonl"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return raw
}

// TestPathBFrozenInputsDigestIsClosedStructSha256 proves the FrozenInputsDigest
// is the sha256 of the canonical JSON closed struct {specDigest, policyDigest,
// capabilityDigest} and that any field drift produces a different digest.
func TestPathBFrozenInputsDigestIsClosedStructSha256(t *testing.T) {
	spec := canonical.DigestBytes([]byte("spec"))
	policy := canonical.DigestBytes([]byte("policy"))
	capability := canonical.DigestBytes([]byte("capability"))
	digest, err := existingWorktreeFrozenInputsDigest(spec, policy, capability)
	if err != nil {
		t.Fatal(err)
	}
	// The closed struct must canonicalize to exactly these three fields.
	wantRaw, err := canonical.JSON([]byte(`{"capabilityDigest":"` + capability + `","policyDigest":"` + policy + `","specDigest":"` + spec + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := canonical.DigestBytes(wantRaw); digest != want {
		t.Fatalf("frozen inputs digest=%q want=%q", digest, want)
	}
	// Drift in any of the three fields must change the digest.
	for _, mutated := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"spec", func() (string, error) {
			return existingWorktreeFrozenInputsDigest(canonical.DigestBytes([]byte("other")), policy, capability)
		}},
		{"policy", func() (string, error) {
			return existingWorktreeFrozenInputsDigest(spec, canonical.DigestBytes([]byte("other")), capability)
		}},
		{"capability", func() (string, error) {
			return existingWorktreeFrozenInputsDigest(spec, policy, canonical.DigestBytes([]byte("other")))
		}},
	} {
		got, err := mutated.fn()
		if err != nil {
			t.Fatal(err)
		}
		if got == digest {
			t.Fatalf("%s drift did not change FrozenInputsDigest", mutated.name)
		}
	}
}

// TestPathBBindReceiptReachesPreparedExecution drives the full path B
// PrepareRunStart chain and proves the resulting PreparedExecution binds the
// existing-worktree bind receipt (not a staging provision receipt). The
// attempt authority must carry the bind receipt fact/digest.
func TestPathBBindReceiptReachesPreparedExecution(t *testing.T) {
	inputs, runID, _, _ := pathBCompositionInputs(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare run start: %v", err)
	}
	// The prepared execution must resolve and bind the existing-worktree
	// receipt, not a staging provision receipt.
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("resolve prepared: %v", err)
	}
	if resolved.ExistingWorktreeBindReceiptFactDigest == "" || resolved.ExistingWorktreeBindReceiptDigest == "" {
		t.Fatalf("path B prepared execution missing worktree receipt: %+v", resolved)
	}
	if resolved.AllocationProvisionReceiptFactDigest != "" || resolved.AllocationProvisionReceiptDigest != "" {
		t.Fatalf("path B prepared execution synthesized a staging provision receipt: %+v", resolved)
	}
	// The attempt authority must carry the bind receipt projection.
	current, found, err := ledger.ingress.AttemptState(resolved.AttemptIdentity)
	if err != nil || !found {
		t.Fatalf("attempt state: found=%t err=%v", found, err)
	}
	if current.ExistingWorktreeBindReceiptFactDigest == "" || current.ExistingWorktreeBindReceiptDigest == "" {
		t.Fatalf("attempt authority missing bind receipt projection: %+v", current)
	}
	if current.AllocationProvisionReceiptDigest != "" {
		t.Fatalf("path B attempt synthesized a provision receipt: %+v", current)
	}
}

// TestPathBReplayProducesNoSibling proves a second PrepareRunStart for the
// same READY projection returns a byte-identical prepared execution and
// appends no new RB1 facts (no sibling reservation, attempt, or bind fact).
func TestPathBReplayProducesNoSibling(t *testing.T) {
	inputs, runID, _, base := pathBCompositionInputs(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	request := application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead}
	first, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, request)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	before := pathBLedgerBytes(t, base)
	second, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, request)
	if err != nil {
		t.Fatalf("replay prepare: %v", err)
	}
	if second != first {
		t.Fatalf("path B replay drifted: first=%+v second=%+v", first, second)
	}
	if after := pathBLedgerBytes(t, base); string(before) != string(after) {
		t.Fatalf("path B replay appended new RB1 facts: before=%d bytes after=%d bytes", len(before), len(after))
	}
}

// TestPathBHeldTargetIdentityDriftRejectsAndNoBindAuthority proves that
// replacing the target worktree directory after the descriptor graph is
// built (identity drift) makes the bind fail closed, and that no
// existing-worktree bind-intent or bind-receipt fact is appended.
func TestPathBHeldTargetIdentityDriftRejectsAndNoBindAuthority(t *testing.T) {
	inputs, runID, worktreePath, base := pathBCompositionInputs(t)
	// Replace the target worktree directory object: rename it away and create
	// a new directory at the same pathname. The held target descriptor and the
	// descriptor graph still reference the original object identity, so the
	// bind must fail closed on identity drift.
	renamed := worktreePath + ".drifted"
	if err := os.Rename(worktreePath, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	_, err = ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err == nil {
		t.Fatal("path B bind accepted a drifted target worktree identity")
	}
	// No existing-worktree bind-intent or bind-receipt fact may have been
	// appended. The producer chain (reservation/opened/owner-binding) may have
	// appended before the bind, but the bind authority must remain absent.
	ledgerBytes := pathBLedgerBytes(t, base)
	if strings.Contains(string(ledgerBytes), "existing-worktree-bind-intent") || strings.Contains(string(ledgerBytes), "existing-worktree-bind-receipt") {
		t.Fatal("drifted-target bind appended an existing-worktree bind fact")
	}
}

// TestPathBSingleSideInputsReject proves that supplying only the descriptor
// graph or only the target worktree (but not both) fails closed in
// NewCompositionLedger. Path B must not silently fall back to staging.
func TestPathBSingleSideInputsReject(t *testing.T) {
	inputs, _, _, _ := pathBCompositionInputs(t)
	t.Run("graph-only", func(t *testing.T) {
		single := inputs
		single.ExistingWorktreeTargetWorktree = nil
		if _, err := NewCompositionLedger(context.Background(), single); err == nil {
			t.Fatal("graph-only path B config was accepted")
		}
	})
	t.Run("target-only", func(t *testing.T) {
		single := inputs
		single.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
		if _, err := NewCompositionLedger(context.Background(), single); err == nil {
			t.Fatal("target-only path B config was accepted")
		}
	})
}

// TestPathBPreheldRunLeaseRejected proves path B (existing-worktree binding
// enabled) rejects a caller-supplied RunLease so the ADR 0069 lock order
// (repository owner → Run Lease) cannot be re-violated by a pre-held lease.
func TestPathBPreheldRunLeaseRejected(t *testing.T) {
	inputs, _, _, _ := pathBCompositionInputs(t)
	preheld, err := inputs.Runs.AcquireExisting(inputs.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = preheld.Release() }()
	rejected := inputs
	rejected.RunLease = preheld
	if _, err := NewCompositionLedger(context.Background(), rejected); err == nil {
		t.Fatal("path B accepted a caller-supplied (pre-held) RunLease")
	}
}

// TestPathAStagingProvisionRegressionUnchanged proves path A (staging
// provision) is still selected and produces a provision receipt when path B
// inputs are absent. It reuses the path B fixture setup but clears the path
// B inputs so the ledger falls back to staging provision.
func TestPathAStagingProvisionRegressionUnchanged(t *testing.T) {
	inputs, runID, _, _ := pathBCompositionInputs(t)
	inputs.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
	inputs.ExistingWorktreeTargetWorktree = nil
	// Path A keeps accepting a caller-supplied Run Lease. The fixture's
	// temporary init lease has been released, so acquire one for path A here.
	pathALease, err := inputs.Runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pathALease.Release() })
	inputs.RunLease = pathALease
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("path A prepare: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("path A resolve: %v", err)
	}
	if resolved.AllocationProvisionReceiptFactDigest == "" || resolved.AllocationProvisionReceiptDigest == "" {
		t.Fatalf("path A prepared execution missing provision receipt: %+v", resolved)
	}
	if resolved.ExistingWorktreeBindReceiptFactDigest != "" || resolved.ExistingWorktreeBindReceiptDigest != "" {
		t.Fatalf("path A prepared execution synthesized a worktree receipt: %+v", resolved)
	}
}
