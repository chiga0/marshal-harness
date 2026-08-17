package publication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// mutateState rewrites the RunState snapshot under a held lease.
func (f *mergeFixture) mutateState(t *testing.T, mutate func(*domain.RunState)) {
	t.Helper()
	lease, err := f.store.Acquire(f.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state, err := f.store.Inspect(f.runID)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&state)
	if err := f.store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
}

// rewriteFile overwrites a run record and returns its new canonical digest.
func (f *mergeFixture) rewriteFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(f.runDirectory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func (f *mergeFixture) readPolicy(t *testing.T) fixturePolicy {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.runDirectory, "policy-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy fixturePolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	return policy
}

func assertNoMergeSideEffect(t *testing.T, fixture *mergeFixture, harness *mergeHarness, wantState domain.State) {
	t.Helper()
	if state := harness.inspect(t); state.State != wantState {
		t.Fatalf("state = %s, want %s", state.State, wantState)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "merge-intents")); !os.IsNotExist(err) {
		t.Fatalf("merge intent was persisted on a rejected admission: %v", err)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != 0 || harness.merger.mergeCalls != 0 {
		t.Fatalf("merger was called on a rejected admission: ready=%d merge=%d", harness.merger.readyCalls, harness.merger.mergeCalls)
	}
}

func TestMergeRejectsPolicyWithoutAllowMerge(t *testing.T) {
	fixture := newMergeFixture(t)
	policy := fixture.readPolicy(t)
	policy.Effective.AllowMerge = false
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.rewriteFile(t, "policy-snapshot.json", data)
	fixture.mutateState(t, func(state *domain.RunState) { state.PolicyDigest = digest })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a policy without allowMerge")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsPolicyWithoutAllowPublication(t *testing.T) {
	fixture := newMergeFixture(t)
	policy := fixture.readPolicy(t)
	policy.Effective.AllowPublication = false
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.rewriteFile(t, "policy-snapshot.json", data)
	fixture.mutateState(t, func(state *domain.RunState) { state.PolicyDigest = digest })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a policy without allowPublication")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsPolicyDigestMismatch(t *testing.T) {
	fixture := newMergeFixture(t)
	// The snapshot policy digest diverges from the persisted policy document
	// without the document being rewritten.
	fixture.mutateState(t, func(state *domain.RunState) { state.PolicyDigest = fabricatedDigest("9") })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a policy digest mismatch")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeRejectsNeverMergePolicy(t *testing.T) {
	fixture := newMergeFixture(t)
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	task.Publication.MergePolicy = domain.MergePolicyNever
	rewritten, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindTask, rewritten); err != nil {
		t.Fatalf("mutated TaskSpec invalid: %v", err)
	}
	digest := fixture.rewriteFile(t, "task-spec.json", rewritten)
	fixture.mutateState(t, func(state *domain.RunState) { state.SpecDigest = digest })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a never-merge task")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsDecisionWithoutEligibleMergeRecommendation(t *testing.T) {
	fixture := newMergeFixture(t)
	decisionPath := filepath.Join(fixture.runDirectory, "decisions", "decision-001.json")
	data, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	decision.MergeRecommendation = "do-not-merge"
	rewritten, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	// The journal's review.accept decisionDigest no longer matches the file.
	// Rebuild the journal is avoided by writing an entirely new decision and
	// leaving the frozen journal decisionDigest unchanged, so the admission
	// must fail closed on the mismatch.
	digest := fixture.rewriteFile(t, filepath.Join("decisions", "decision-001.json"), rewritten)
	if digest == fixture.decisionDigest {
		t.Fatal("mutated decision digest did not change")
	}

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a do-not-merge decision")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeRejectsMissingHumanPublishApproval(t *testing.T) {
	fixture := newMergeFixture(t)
	if err := os.Remove(filepath.Join(fixture.runDirectory, "control", "records.jsonl")); err != nil {
		t.Fatal(err)
	}

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted without a human publish approval")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsEmptyRequiredChecks(t *testing.T) {
	fixture := newMergeFixture(t)
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	task.Publication.RequiredChecks = []string{}
	rewritten, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.rewriteFile(t, "task-spec.json", rewritten)
	fixture.mutateState(t, func(state *domain.RunState) { state.SpecDigest = digest })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted an empty requiredChecks set")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsPendingChecks(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.checkObserver = &fakeObserver{status: "pending"}

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted pending required checks")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsFailedChecks(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.checkObserver = &fakeObserver{status: "fail"}

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted failed required checks")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsHeadDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.HeadOid = fabricatedSHA("9")

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a drifted head OID")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeRejectsBaseAdvance(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.BaseOid = fabricatedSHA("a")

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted an advanced base OID")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeRejectsBaseBranchDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.BaseBranch = "release"

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a drifted base branch")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeAuthorizationCurrentLedgerRejectsIneligibleTarget(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.eligibility.eligible = false

	if _, err := harness.merge(t); err == nil || !port.IsPermanent(err) {
		t.Fatalf("Merge() authorization recheck error = %v, want permanent failure", err)
	}
	if state := harness.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != 0 || harness.merger.mergeCalls != 0 {
		t.Fatalf("ineligible authorization reached remote mutation: ready=%d merge=%d", harness.merger.readyCalls, harness.merger.mergeCalls)
	}
}

func TestMergeRejectsMissingMarker(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.MarkerPresent = false

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a PR without the run marker")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeBlocksWhenProviderCannotBindHead(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.merger.bindsHead = false

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a provider that cannot bind the expected head")
	}
	if state := harness.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	harness.merger.mu.Lock()
	defer harness.merger.mu.Unlock()
	if harness.merger.readyCalls != 0 || harness.merger.mergeCalls != 0 {
		t.Fatalf("merger was called despite missing head-binding capability: ready=%d merge=%d", harness.merger.readyCalls, harness.merger.mergeCalls)
	}
}

// rewriteTaskSpec mutates the frozen task-spec.json and re-pins the snapshot
// SpecDigest, so admission reads the new document without rebuilding the
// journal.
func (f *mergeFixture) rewriteTaskSpec(t *testing.T, mutate func(*domain.TaskSpec)) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.runDirectory, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	mutate(&task)
	rewritten, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no schema re-validation: a schema-invalid mutation (e.g. a
	// missing mergeMethod under mergePolicy=policy) must be rejected by the
	// admission's own validator, which is exactly what these tests exercise.
	digest := f.rewriteFile(t, "task-spec.json", rewritten)
	f.mutateState(t, func(state *domain.RunState) { state.SpecDigest = digest })
}

func TestMergeRejectsManualMergePolicy(t *testing.T) {
	fixture := newMergeFixture(t)
	fixture.rewriteTaskSpec(t, func(task *domain.TaskSpec) { task.Publication.MergePolicy = domain.MergePolicyManual })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a manual-merge task")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsMissingMergeMethod(t *testing.T) {
	fixture := newMergeFixture(t)
	fixture.rewriteTaskSpec(t, func(task *domain.TaskSpec) { task.Publication.MergeMethod = "" })

	harness := newMergeHarness(t, fixture)
	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a policy-merge task without a mergeMethod")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsRequiredChecksSetMismatch(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.checkObserver = &fakeObserver{status: "pass", mutate: func(checks *domain.RemoteCheckRecord) {
		checks.Checks = nil
	}}

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a mismatched requiredChecks set")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateCIPending)
}

func TestMergeRejectsRepositoryDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.Repository = "other/repo"

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a drifted repository")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeRejectsPRNumberDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.PRNumber = 99

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a drifted PR number")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

func TestMergeBlocksMergedTargetWithIdentityDrift(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.State = domain.MergeTargetStateMerged
	harness.targetObserver.target.Draft = false
	harness.targetObserver.target.MarkerPresent = false

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a merged PR whose marker drifted")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}

// TestMergeRejectsReadyPRWithoutIntent covers T17: a ready PR with no prior
// intent must be rejected without persisting an orphan intent, so a
// "manually ready, then back-fill intent" bypass can never succeed.
func TestMergeRejectsReadyPRWithoutIntent(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)
	harness.targetObserver.target.Draft = false

	if _, err := harness.merge(t); err == nil {
		t.Fatal("Merge() accepted a ready PR without a prior intent")
	}
	assertNoMergeSideEffect(t, fixture, harness, domain.StateBlocked)
}
