//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
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

// pathBCompositionInputsForLaunch builds a full CompositionInputs wired for
// path B: a READY Run whose WorktreePath is a real linked git worktree, plus
// the held descriptor graph and target. The closure's working directory is
// the target worktree (path B does not re-seal it to staging). It returns the
// inspectable argv builder so tests can assert the precise reserved identity
// and the sealed production argv.
func pathBCompositionInputsForLaunch(t *testing.T) (CompositionInputs, string, string, string, *testLaunchArgvBuilder) {
	t.Helper()
	ownerFixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, ownerFixture)
	acquisition := acquisitionAtEpoch(1)
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
	providerStore, err := provider.NewRegistrationStore(filepath.Join(ownerFixture.base, "provider-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerStore.Close() })
	providerDomain := authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process"}
	attestation := provider.Attestation{ProviderInstanceId: "provider-instance-pathb", ConfigDigest: canonical.DigestBytes([]byte("config")), TrustRootKeyId: "trust-root-1", TrustRootAlgorithm: "ed25519"}
	registration, snapshot, err := LocalProviderAuthority(acquisition.Scope.AuthorityNamespaceID, providerDomain, attestation)
	if err != nil {
		t.Fatal(err)
	}
	registration, err = providerStore.Put(registration)
	if err != nil {
		t.Fatal(err)
	}
	inputs := CompositionInputs{
		Ingress: store, Runs: runStore, RunLease: nil, LeaseLedger: leaseLedger,
		OwnerDirectory: ownerFixture.directory, Acquisition: acquisition, RunID: runID,
		Namespace: acquisition.Scope.AuthorityNamespaceID, OrchestratorID: "orchestrator:composition-pathb",
		ProvisionDomain: providerDomain,
		CleanupDomain:   authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process-cleanup"},
		RegistrationID:  registration.RegistrationId, CapabilitySnapshot: snapshot.ProviderCapabilitySnapshotDigest,
		ConformanceEvidence: []string{}, Attestation: attestation,
		ProviderStore: providerStore, ProviderRegistration: registration, ProviderSnapshot: snapshot,
		ProviderEvidence: []provider.ConformanceEvidence{}, ResultIngressDomain: LocalResultIngressDomain(acquisition.Scope.AuthorityNamespaceID),
		AllocationRoot: filepath.Join(ownerFixture.base, "allocations"), LaunchClosure: reSealed,
		Requirements:     allocationcontrol.SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		WorkDirAllowlist: []string{worktreePath}, EnvironmentAllowlist: []string{"PATH"},
		ExistingWorktreeDescriptorGraph: graph,
		ExistingWorktreeTargetWorktree:  target,
	}
	// The injected production argv builder mirrors adapter/pi's
	// BuildProductionLaunch shape without importing adapter/pi; path B
	// requires a non-nil builder.
	builder := &testLaunchArgvBuilder{node: reSealed.RuntimeExecutable.CanonicalPath, entrypoint: reSealed.Arguments[1], profile: "workspace-write"}
	inputs.LaunchArgvBuilder = builder.build()
	return inputs, runID, worktreePath, ownerFixture.base, builder
}

// pathBCompositionInputs builds a full CompositionInputs wired for path B
// with a default (non-inspectable) argv builder. Tests that need to inspect
// builder calls use pathBCompositionInputsForLaunch directly.
func pathBCompositionInputs(t *testing.T) (CompositionInputs, string, string, string) {
	t.Helper()
	inputs, runID, worktreePath, base, _ := pathBCompositionInputsForLaunch(t)
	return inputs, runID, worktreePath, base
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
	if capability, ok := ledger.resultCapabilities[resolved.AttemptIdentity.LeaseID]; !ok || capability.Validate() != nil || capability.BoundAttemptId != resolved.AttemptIdentity.AttemptID {
		t.Fatalf("path B did not retain durable reserved result capability: ok=%t capability=%+v", ok, capability)
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

type terminalLeaseRecoveryFixture struct {
	ledger  *CompositionLedger
	attempt resultingress.AttemptAuthorityState
	lease   dispatch.DispatchLease
	base    string
}

func newTerminalLeaseRecoveryFixture(t *testing.T) terminalLeaseRecoveryFixture {
	t.Helper()
	inputs, runID, _, base := pathBCompositionInputs(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	fixedNow := time.Now().UTC().Truncate(time.Second)
	ledger.now = func() time.Time { return fixedNow }
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare run start: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("resolve prepared: %v", err)
	}
	attempt, found, err := ledger.ingress.AttemptState(resolved.AttemptIdentity)
	if err != nil || !found {
		t.Fatalf("attempt state: found=%t err=%v", found, err)
	}
	lease, state, _, err := ledger.leaseLedger.Current(attempt.Identity.LeaseID)
	if err != nil || state != dispatch.LeaseStateClaimed {
		t.Fatalf("current lease: state=%s err=%v", state, err)
	}
	return terminalLeaseRecoveryFixture{ledger: ledger, attempt: attempt, lease: lease, base: base}
}

func dispatchLedgerBytes(t *testing.T, base string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, "dispatch-ledger", "leases.jsonl"))
	if err != nil {
		t.Fatalf("read dispatch ledger: %v", err)
	}
	return raw
}

func terminalTestDigest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func terminalSupervisorAnchor(anchor resultingress.SupervisorMechanicsAnchor) processsupervisor.HandshakeAnchor {
	return processsupervisor.HandshakeAnchor{
		SessionID: anchor.SessionID, SessionNonceDigest: anchor.SessionNonceDigest, Authority: anchor.Authority,
		OwnerEpoch: anchor.OwnerEpoch, CurrentAuthorityHead: anchor.CurrentAuthorityHead,
		CommandSequence: anchor.CommandSequence, CommandHead: anchor.CommandHead,
		JournalSequence: anchor.JournalSequence, JournalHead: anchor.JournalHead,
		UID: anchor.UID, GID: anchor.GID, FixedBinary: anchor.FixedBinary,
		ControlSocket: anchor.ControlSocket, ControlFiles: anchor.ControlFiles,
	}
}

func terminalVerifiedOutcome(t *testing.T, intent resultingress.SupervisorCommandIntent, reason string, report *processsupervisor.ProcessReport, boundHead string) processsupervisor.VerifiedCommandOutcome {
	t.Helper()
	pre := terminalSupervisorAnchor(intent.PreCommand)
	post := pre
	post.CommandSequence = intent.Sequence
	post.JournalSequence += 2
	post.JournalHead = canonical.DigestBytes([]byte("terminal-journal-" + intent.CommandID))
	var payload []byte
	if report == nil {
		payload = []byte("{}")
	} else {
		var err error
		payload, err = processsupervisor.CanonicalProtocolMessage(*report)
		if err != nil {
			t.Fatal(err)
		}
	}
	observation := canonical.DigestBytes(payload)
	if intent.Command == processsupervisor.CommandBindAuthority {
		observation = intent.Rebuild.SupervisorStartedFactDigest
	}
	result := processsupervisor.MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: observation, Payload: payload}
	receipt := terminalTestDigest(t, result)
	commandHead := terminalTestDigest(t, struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{intent.PreviousCommandHead, intent.RequestDigest, receipt})
	post.CommandHead = commandHead
	if intent.Command == processsupervisor.CommandBindAuthority {
		post.CurrentAuthorityHead = boundHead
	}
	return processsupervisor.VerifiedCommandOutcome{
		Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence,
		Status: "ok", Disposition: "ok", ReasonCode: reason, RequestDigest: intent.RequestDigest,
		ReceiptDigest: receipt, ObservationDigest: observation, CommandHead: commandHead, ProcessReport: report,
		Recovery: processsupervisor.CommandRecoveryEvidence{PreCommand: pre, PostCommand: post},
	}
}

func terminalAppendCommand(t *testing.T, fixture terminalLeaseRecoveryFixture, state resultingress.AttemptAuthorityState, intent resultingress.SupervisorCommandIntent, outcome processsupervisor.VerifiedCommandOutcome) (resultingress.AttemptAuthorityState, string) {
	t.Helper()
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: state.Identity.AuthorityNamespaceID, RunID: state.Identity.RunID, OrchestratorID: state.Identity.OrchestratorID, RunAuthorityDigest: state.Identity.RunAuthorityDigest}
	request := resultingress.AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	intended, err := fixture.ledger.ingress.AppendSupervisorCommandIntent(context.Background(), fixture.ledger.owner, fixture.ledger, state.Revision, state.HeadDigest, request, state.Owner, intent)
	if err != nil {
		t.Fatalf("append %s intent: %v", intent.Command, err)
	}
	evidence, err := resultingress.NewSupervisorCommandEvidence(outcome)
	if err != nil {
		t.Fatalf("project %s outcome: %v", intent.Command, err)
	}
	closed, err := fixture.ledger.ingress.AppendSupervisorCommandOutcome(context.Background(), fixture.ledger.owner, fixture.ledger, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, evidence)
	if err != nil {
		t.Fatalf("append %s outcome: %v", intent.Command, err)
	}
	return closed.State, closed.TransitionDigest
}

// appendDurableTerminalStartedAttempt reaches ProcessStarted only through the
// public ResultIngress producer chain. It intentionally models mechanics
// evidence without executing a temporary test binary.
func appendDurableTerminalStartedAttempt(t *testing.T, fixture terminalLeaseRecoveryFixture) resultingress.AttemptAuthorityState {
	t.Helper()
	state := fixture.attempt
	owner, found, err := fixture.ledger.ingress.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("open owner: found=%t err=%v", found, err)
	}
	run := resultingress.RunAuthorityBinding{AuthorityNamespaceID: state.Identity.AuthorityNamespaceID, RunID: state.Identity.RunID, OrchestratorID: state.Identity.OrchestratorID, RunAuthorityDigest: state.Identity.RunAuthorityDigest}
	request := resultingress.AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	tuple := processsupervisor.AuthorityTuple{AuthorityNamespaceID: state.Identity.AuthorityNamespaceRef, TaskID: state.Identity.TaskID, RunID: state.Identity.RunID, AttemptID: state.Identity.AttemptID, AllocationID: state.Identity.AllocationID, LeaseID: state.Identity.LeaseID, LeaseDigest: state.Identity.LeaseDigest, Generation: uint64(state.Identity.DispatchGeneration), FencingTokenDigest: state.Identity.FencingTokenDigest, OrchestratorID: state.Identity.OrchestratorID}
	directory := processsupervisor.ControlDirectoryIdentity{CanonicalPath: filepath.Join(fixture.base, "terminal-control"), Device: 71, Inode: 81, FileType: "directory", UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Mode: resultingress.POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	bootstrapRequest := processsupervisor.BootstrapRequest{SchemaVersion: processsupervisor.BootstrapSchema, ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: "terminal-lease-recovery", SessionNonce: strings.Repeat("7", 64), OwnerEpoch: state.Owner.OwnerEpoch, Authority: tuple, LaunchAuthorizedFact: state.LaunchAuthorizedDigest, CurrentAuthorityHead: state.HeadDigest, ControlDirectoryIdentity: directory, Core: processsupervisor.CoreIdentity{UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Process: owner.Acquisition.OwnerProcess, Binary: owner.Acquisition.OwnerBinary}}
	prepared, err := resultingress.NewSupervisorBootstrapPrepared(state.Owner, bootstrapRequest)
	if err != nil {
		t.Fatalf("prepare supervisor: %v", err)
	}
	bootstrapped, err := fixture.ledger.ingress.AppendSupervisorBootstrap(context.Background(), fixture.ledger.owner, fixture.ledger, state.Revision, state.HeadDigest, request, prepared)
	if err != nil {
		t.Fatalf("append supervisor bootstrap: %v", err)
	}
	state = bootstrapped.State
	socket := processsupervisor.ControlSocketIdentity{Device: 71, Inode: 82, FileType: "socket", UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Mode: 0o140600, LinkCount: 1}
	files := processsupervisor.SessionControlFiles{Nonce: processsupervisor.ControlFileIdentity{Device: 71, Inode: 83, FileType: "regular", UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Mode: 0o100600, LinkCount: 1}, Journal: processsupervisor.ControlFileIdentity{Device: 71, Inode: 84, FileType: "regular", UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Mode: 0o100600, LinkCount: 1}}
	supervisorProcess := processsupervisor.ProcessIdentity{PID: 9301, BirthSeconds: owner.Acquisition.OwnerProcess.BirthSeconds + 1, BirthMicroseconds: 0, SessionID: 9301, ProcessGroupID: 9301}
	handshakeAt := time.Unix(supervisorProcess.BirthSeconds, 0).UTC().Format(time.RFC3339Nano)
	handshake := processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema, ProtocolRevision: processsupervisor.ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: prepared.SessionID, SessionNonceDigest: prepared.SessionNonceDigest, OwnerEpoch: state.Owner.OwnerEpoch, CurrentAuthorityHead: prepared.Request.CurrentAuthorityHead, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: canonical.DigestBytes([]byte("terminal-initial-journal")), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: handshakeAt, SupervisorProcess: supervisorProcess, SupervisorBinary: prepared.SupervisorBinary, ControlSocket: socket, ControlFiles: files}
	anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: tuple, OwnerEpoch: handshake.OwnerEpoch, CurrentAuthorityHead: handshake.CurrentAuthorityHead, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: handshake.JournalHead, UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, FixedBinary: prepared.SupervisorBinary, ControlSocket: socket, ControlFiles: files}
	finalDirectory := directory
	finalDirectory.LinkCount++
	connection := processsupervisor.ConnectionEvidence{Core: bootstrapRequest.Core, ControlDirectory: finalDirectory, Handshake: handshake, Anchor: anchor}
	startedSupervisor, err := resultingress.NewProcessSupervisorStartedFromBootstrap(state.SupervisorBootstrapDigest, prepared, connection, processsupervisor.CoreIdentity{UID: owner.Acquisition.OwnerUID, GID: owner.Acquisition.OwnerGID, Process: supervisorProcess, Binary: prepared.SupervisorBinary})
	if err != nil {
		t.Fatalf("project supervisor started: %v", err)
	}
	startedResult, err := fixture.ledger.ingress.AppendSupervisorStarted(context.Background(), fixture.ledger.owner, fixture.ledger, state.Revision, state.HeadDigest, request, startedSupervisor)
	if err != nil {
		t.Fatalf("append supervisor started: %v", err)
	}
	state = startedResult.State
	bindPrepared, err := processsupervisor.PrepareCommand(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandBindAuthority, CommandID: "terminal-bind", Sequence: 1, PreviousCommandDigest: anchor.CommandHead, CurrentAuthorityHead: anchor.CurrentAuthorityHead, Deadline: time.Now().UTC().Add(10 * time.Second)}, processsupervisor.BindAuthorityPayload{SupervisorStartedFactDigest: state.SupervisorStartedDigest, OwnerEpoch: state.Owner.OwnerEpoch, PreviousAuthorityHead: anchor.CurrentAuthorityHead, AuthorityHead: state.SupervisorStartedDigest})
	if err != nil {
		t.Fatalf("prepare bind: %v", err)
	}
	bindIntent, err := resultingress.NewSupervisorCommandIntent(bindPrepared.Evidence())
	if err != nil {
		t.Fatal(err)
	}
	state, bindDigest := terminalAppendCommand(t, fixture, state, bindIntent, terminalVerifiedOutcome(t, bindIntent, "process-authority-bound", nil, state.SupervisorStartedDigest))
	runtimeDigest := canonical.DigestBytes([]byte("terminal-runtime"))
	workingDigest := canonical.DigestBytes([]byte("terminal-working"))
	exactSetDigest := canonical.DigestBytes([]byte("terminal-exact-set"))
	spawnIntent := resultingress.SupervisorCommandIntent{ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: state.SupervisorStarted.Handshake.SessionID, Command: processsupervisor.CommandSpawn, CommandID: "terminal-spawn", Sequence: state.SupervisorCommandSequence + 1, PreviousCommandHead: state.SupervisorCommandHead, CurrentAuthorityHead: state.HeadDigest, Deadline: time.Now().UTC().Add(10 * time.Second).Format(time.RFC3339Nano), RequestDigest: canonical.DigestBytes([]byte("terminal-spawn-request")), PayloadDigest: canonical.DigestBytes([]byte("terminal-spawn-payload")), Rebuild: processsupervisor.PreparedCommandProjection{SupervisorStartedFactDigest: state.SupervisorStartedDigest, LaunchAuthorizedFactDigest: state.LaunchAuthorizedDigest, LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest, SourceGateRevision: processsupervisor.SourceGateRevisionV1, RuntimeObjectDigest: runtimeDigest, WorkingObjectDigest: workingDigest, ClosureProfileID: state.LaunchClosure.ClosureProfileID, ArgvDigest: canonical.DigestBytes([]byte("terminal-argv")), EnvironmentDigest: canonical.DigestBytes([]byte("terminal-env")), StdinDigest: canonical.DigestBytes([]byte("terminal-stdin"))}, PreCommand: state.SupervisorMechanicsAnchor}
	child := processsupervisor.ProcessIdentity{PID: 9401, BirthSeconds: supervisorProcess.BirthSeconds + 1, BirthMicroseconds: 0, SessionID: 9401, ProcessGroupID: 9401}
	observedAt := time.Unix(child.BirthSeconds, 0).UTC().Format(time.RFC3339Nano)
	report := processsupervisor.ProcessReport{State: "exec-stopped", ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: observedAt, Process: child, RuntimeObjectDigest: runtimeDigest, WorkingObjectDigest: workingDigest, SourceGateRevision: processsupervisor.SourceGateRevisionV1, ExactSetDigest: exactSetDigest}
	state, spawnDigest := terminalAppendCommand(t, fixture, state, spawnIntent, terminalVerifiedOutcome(t, spawnIntent, "process-exec-stopped", &report, ""))
	runtime := state.LaunchClosure.RuntimeExecutable
	observation, err := resultingress.SealProcessObservation(resultingress.ProcessObservation{PID: child.PID, PGID: child.ProcessGroupID, BirthSeconds: child.BirthSeconds, BirthMicroseconds: child.BirthMicroseconds, WorkingDirectory: state.LaunchClosure.WorkingDirectory, WorkingDirectoryDevice: 71, WorkingDirectoryInode: 85, WorkingDirectoryType: resultingress.POSIXFileTypeDirectory, WorkingDirectoryOwner: owner.Acquisition.OwnerUID, WorkingDirectoryMode: resultingress.POSIXFileTypeDirectory | 0o700, ExecutablePath: runtime.CanonicalPath, ExecutableDevice: runtime.Device, ExecutableInode: runtime.Inode, ExecutableSize: runtime.Size, ExecutableType: resultingress.POSIXFileTypeRegular, ExecutableOwner: runtime.UID, ExecutableGroup: runtime.GID, ExecutableMode: runtime.Mode, ExecutableLinkCount: runtime.LinkCount, ExecutableSHA256: runtime.RawSHA256, ObserverIdentity: "darwin-terminal-lease-recovery/v1"})
	if err != nil {
		t.Fatalf("seal process observation: %v", err)
	}
	transition := resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionProcessStarted, Identity: state.Identity, CommandID: spawnIntent.CommandID, ObservedAt: observedAt, Process: observation, LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest, SupervisorBindOutcomeFactDigest: bindDigest, SupervisorOutcomeFactDigest: spawnDigest}
	appended, err := fixture.ledger.ingress.AppendProcessStarted(context.Background(), fixture.ledger.owner, fixture.ledger, state.Revision, state.HeadDigest, request, state.Owner, transition)
	if err != nil {
		t.Fatalf("append process started: %v", err)
	}
	return appended.State
}

// TestTerminalCollectLeaseRecoveryMatrix freezes the completion-only lease
// rules. AckDeadlineAt remains a pre-start gate; a durably started process
// may replay its original claim after that boundary, but never after expiry
// or after any identity drift. Reopening the ledger/matcher must replay the
// exact bytes without appending a sibling fact.
func TestTerminalCollectLeaseRecoveryMatrix(t *testing.T) {
	t.Run("started-after-ack-replays-original", func(t *testing.T) {
		fixture := newTerminalLeaseRecoveryFixture(t)
		ack, err := time.Parse(time.RFC3339, fixture.lease.AckDeadlineAt)
		if err != nil {
			t.Fatal(err)
		}
		late := ack.Add(time.Second)
		fixture.ledger.now = func() time.Time { return late }
		if _, _, _, err := fixture.ledger.leaseLedger.CurrentByAttempt(fixture.lease.RunId, fixture.lease.AttemptId, late); !errors.Is(err, dispatch.ErrLeaseConflict) {
			t.Fatalf("pre-start lookup after ack err=%v", err)
		}
		started := appendDurableTerminalStartedAttempt(t, fixture)
		before := dispatchLedgerBytes(t, fixture.base)
		got, capability, err := fixture.ledger.recoverStartedAttemptLease(started)
		if err != nil {
			t.Fatalf("recover started lease: %v", err)
		}
		if !reflect.DeepEqual(got, fixture.lease) || capability.Validate() != nil || capability.BoundAttemptId != started.Identity.AttemptID || capability.BoundAllocationId != started.Identity.AllocationID {
			t.Fatalf("recovered claim drifted: leaseEqual=%t capability=%+v", reflect.DeepEqual(got, fixture.lease), capability)
		}
		if after := dispatchLedgerBytes(t, fixture.base); !reflect.DeepEqual(after, before) {
			t.Fatal("started recovery appended a dispatch fact")
		}
	})

	t.Run("unstarted-after-ack-remains-rejected", func(t *testing.T) {
		fixture := newTerminalLeaseRecoveryFixture(t)
		ack, _ := time.Parse(time.RFC3339, fixture.lease.AckDeadlineAt)
		fixture.ledger.now = func() time.Time { return ack.Add(time.Second) }
		before := dispatchLedgerBytes(t, fixture.base)
		_, err := fixture.ledger.ensureAttemptLease(fixture.attempt.ReservationFactDigest, fixture.attempt.Identity.TaskID, fixture.attempt.Identity.RunID, fixture.attempt.Identity.AttemptID, fixture.attempt.Identity.AllocationID)
		if !errors.Is(err, dispatch.ErrLeaseConflict) {
			t.Fatalf("unstarted attempt after ack err=%v", err)
		}
		if after := dispatchLedgerBytes(t, fixture.base); !reflect.DeepEqual(after, before) {
			t.Fatal("rejected unstarted recovery appended a dispatch fact")
		}
	})

	t.Run("started-at-expiry-remains-rejected", func(t *testing.T) {
		fixture := newTerminalLeaseRecoveryFixture(t)
		expires, _ := time.Parse(time.RFC3339, fixture.lease.ExpiresAt)
		fixture.ledger.now = func() time.Time { return expires }
		started := appendDurableTerminalStartedAttempt(t, fixture)
		before := dispatchLedgerBytes(t, fixture.base)
		if _, _, err := fixture.ledger.recoverStartedAttemptLease(started); !errors.Is(err, dispatch.ErrLeaseConflict) {
			t.Fatalf("expired started attempt err=%v", err)
		}
		if after := dispatchLedgerBytes(t, fixture.base); !reflect.DeepEqual(after, before) {
			t.Fatal("expired recovery appended a dispatch fact")
		}
	})

	t.Run("started-identity-drift-remains-rejected", func(t *testing.T) {
		mutations := []struct {
			name   string
			mutate func(*resultingress.AttemptIdentity)
		}{
			{"typed-namespace", func(id *resultingress.AttemptIdentity) { id.AuthorityNamespaceID.AuthorityScopeId += "-drift" }},
			{"namespace-ref", func(id *resultingress.AttemptIdentity) { id.AuthorityNamespaceRef += "-drift" }},
			{"task", func(id *resultingress.AttemptIdentity) { id.TaskID += "-drift" }},
			{"run", func(id *resultingress.AttemptIdentity) { id.RunID += "-drift" }},
			{"attempt", func(id *resultingress.AttemptIdentity) { id.AttemptID += "-drift" }},
			{"allocation", func(id *resultingress.AttemptIdentity) { id.AllocationID += "-drift" }},
			{"lease-id", func(id *resultingress.AttemptIdentity) { id.LeaseID += "-drift" }},
			{"generation", func(id *resultingress.AttemptIdentity) { id.DispatchGeneration++ }},
			{"fencing", func(id *resultingress.AttemptIdentity) {
				id.FencingTokenDigest = canonical.DigestBytes([]byte("drifted-fencing"))
			}},
			{"lease-digest", func(id *resultingress.AttemptIdentity) {
				id.LeaseDigest = canonical.DigestBytes([]byte("drifted-lease"))
			}},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				fixture := newTerminalLeaseRecoveryFixture(t)
				ack, _ := time.Parse(time.RFC3339, fixture.lease.AckDeadlineAt)
				fixture.ledger.now = func() time.Time { return ack.Add(time.Second) }
				started := appendDurableTerminalStartedAttempt(t, fixture)
				mutation.mutate(&started.Identity)
				before := dispatchLedgerBytes(t, fixture.base)
				if _, _, err := fixture.ledger.recoverStartedAttemptLease(started); err == nil {
					t.Fatal("identity drift was accepted")
				}
				if after := dispatchLedgerBytes(t, fixture.base); !reflect.DeepEqual(after, before) {
					t.Fatal("identity-drift recovery appended a dispatch fact")
				}
			})
		}
	})

	t.Run("reopened-authority-replays-without-amplification", func(t *testing.T) {
		fixture := newTerminalLeaseRecoveryFixture(t)
		ack, _ := time.Parse(time.RFC3339, fixture.lease.AckDeadlineAt)
		late := ack.Add(time.Second)
		started := appendDurableTerminalStartedAttempt(t, fixture)
		reopenedIngress, err := resultingress.OpenResultIngressStore(filepath.Join(fixture.base, "result-ingress"))
		if err != nil {
			t.Fatalf("reopen ResultIngress: %v", err)
		}
		t.Cleanup(func() { _ = reopenedIngress.Close() })
		replayed, found, err := reopenedIngress.AttemptState(started.Identity)
		if err != nil || !found || !reflect.DeepEqual(replayed, started) || replayed.ProcessStartedDigest == "" {
			t.Fatalf("cold ProcessStarted replay: found=%t equal=%t digest=%q err=%v", found, reflect.DeepEqual(replayed, started), replayed.ProcessStartedDigest, err)
		}
		started = replayed
		before := dispatchLedgerBytes(t, fixture.base)
		reopened, err := dispatch.NewLeaseLedger(filepath.Join(fixture.base, "dispatch-ledger"))
		if err != nil {
			t.Fatalf("reopen dispatch ledger: %v", err)
		}
		edges, err := authority.NewEdgeRuntime(fixture.ledger.namespace)
		if err != nil {
			t.Fatal(err)
		}
		edges.BindLeaseResolver(compositionLeaseResolver{ledger: reopened})
		edges.BindTargetEligibilityResolver(compositionTargetResolver{store: fixture.ledger.providerStore, registrationID: fixture.ledger.providerRecord.RegistrationId, target: fixture.ledger.resultTarget})
		fixture.ledger.leaseLedger = reopened
		fixture.ledger.matcher = dispatch.NewMatcherWithReservedClaimLedger(fixture.ledger.providerStore, edges, reopened)
		fixture.ledger.resultCapabilities = map[string]authority.DispatchResultCapability{}
		fixture.ledger.now = func() time.Time { return late }
		got, capability, err := fixture.ledger.recoverStartedAttemptLease(started)
		if err != nil {
			t.Fatalf("recover through reopened authority: %v", err)
		}
		if !reflect.DeepEqual(got, fixture.lease) || capability.Validate() != nil {
			t.Fatalf("reopened recovery drifted: leaseEqual=%t capabilityErr=%v", reflect.DeepEqual(got, fixture.lease), capability.Validate())
		}
		if after := dispatchLedgerBytes(t, fixture.base); !reflect.DeepEqual(after, before) {
			t.Fatal("reopened recovery amplified the dispatch ledger")
		}
	})
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
