package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

type planningTestWorker struct {
	id         string
	capability domain.Record
}

func (w *planningTestWorker) ID() string { return w.id }

func (w *planningTestWorker) Probe(context.Context) (domain.Record, error) {
	return w.capability, nil
}

func (w *planningTestWorker) Run(context.Context, domain.Record) (domain.Record, error) {
	panic("planning must not run a Worker")
}

func TestPlanHappyPathPersistsAndReleasesLocks(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	const (
		taskID    = "task-plan-happy"
		runID     = "run-plan-happy"
		adapterID = "adapter-plan"
		remoteURL = "https://fixture-user:fixture-secret@example.invalid/repository.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)

	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	policyData := planningPolicyFixture(t, taskID, runID, adapterID)
	capabilityData := planningCapabilityFixture(t, adapterID)
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: capabilityData},
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(worker); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}

	input := Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: policyData,
		Selector:       selector,
		Validator:      newValidator(t),
		Now:            time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}
	result, err := Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if result.State.State != domain.StateReady || result.State.Sequence != 2 {
		t.Fatalf("state = %s sequence=%d, want READY sequence=2", result.State.State, result.State.Sequence)
	}
	if result.Adapter == nil || result.Adapter.ID() != adapterID {
		t.Fatalf("selected adapter = %#v, want %q", result.Adapter, adapterID)
	}
	if len(result.SelectionAttempts) != 1 || result.SelectionAttempts[0] != (adapter.SelectionAttempt{AdapterID: adapterID, Outcome: adapter.OutcomeSelected}) {
		t.Fatalf("selection attempts = %#v", result.SelectionAttempts)
	}
	if result.State.BaseSHA != baseSHA || result.State.WorktreePath == "" {
		t.Fatalf("base/worktree = %q/%q, want %q/non-empty", result.State.BaseSHA, result.State.WorktreePath, baseSHA)
	}
	if want := filepath.Join(stateRoot, "worktrees", taskID+"-"+runID); result.State.WorktreePath != want {
		t.Fatalf("worktree path = %q, want run-scoped %q", result.State.WorktreePath, want)
	}

	store := runstore.New(stateRoot)
	events, truncated, err := store.ReadEvents(runID)
	if err != nil || truncated {
		t.Fatalf("ReadEvents() events=%d truncated=%v err=%v", len(events), truncated, err)
	}
	if len(events) != 2 || events[0].StateTo != domain.StatePlanned || events[1].StateTo != domain.StateReady {
		t.Fatalf("journal transitions = %#v", events)
	}

	runDirectory := filepath.Join(stateRoot, "runs", runID)
	assertPlanningFrozenFile(t, filepath.Join(runDirectory, "task-spec.json"), taskData, result.State.SpecDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDirectory, "policy-snapshot.json"), policyData, result.State.PolicyDigest)
	assertPlanningFrozenFile(t, filepath.Join(runDirectory, "capability-snapshot.json"), capabilityData, result.State.CapabilityDigest)

	runLease, err := store.Acquire(runID)
	if err != nil {
		t.Fatalf("run lease was not released: %v", err)
	}
	if err := runLease.Release(); err != nil {
		t.Fatalf("release reacquired run lease: %v", err)
	}
	repository, err := gitworktree.Open(repositoryRoot)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	worktree, err := repository.Acquire(stateRoot, taskID, result.State.WorktreePath, baseSHA)
	if err != nil {
		t.Fatalf("worktree lock was not released: %v", err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatalf("release reacquired worktree: %v", err)
	}

	if _, err := Plan(context.Background(), input); err == nil {
		t.Fatal("duplicate Plan() returned nil error")
	}
	events, truncated, err = store.ReadEvents(runID)
	if err != nil || truncated || len(events) != 2 {
		t.Fatalf("duplicate plan changed journal: events=%d truncated=%v err=%v", len(events), truncated, err)
	}
}

func TestPlanUsesExplicitFallbackIdentity(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID      = "task-plan-fallback"
		runID       = "run-plan-fallback"
		preferredID = "adapter-missing"
		fallbackID  = "adapter-fallback"
		remoteURL   = "https://example.invalid/fallback.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, preferredID, remoteURL, baseSHA)
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	task["worker"].(map[string]any)["fallbackAdapters"] = []any{fallbackID}
	taskData = mustMarshal(t, task)
	policyData := planningPolicyFixture(t, taskID, runID, preferredID)
	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatal(err)
	}
	effective := policy["effective"].(map[string]any)
	effective["allowFallbackWorkers"] = true
	effective["allowedAdapters"] = []any{preferredID, fallbackID}
	policyData = sealPolicyDocument(t, policy)
	capabilityData := planningCapabilityFixture(t, fallbackID)
	worker := &planningTestWorker{id: fallbackID, capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: capabilityData}}
	selector := planningSelector(t, worker)

	result, err := Plan(context.Background(), Input{
		StateRoot:      filepath.Join(repositoryRoot, ".marshal"),
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: policyData,
		Selector:       selector,
		Validator:      newValidator(t),
	})
	if err != nil {
		t.Fatalf("Plan(): %v", err)
	}
	if result.Adapter == nil || result.Adapter.ID() != fallbackID {
		t.Fatalf("selected adapter = %#v, want fallback %q", result.Adapter, fallbackID)
	}
	wantAttempts := []adapter.SelectionAttempt{
		{AdapterID: preferredID, Outcome: adapter.OutcomeUnavailable},
		{AdapterID: fallbackID, Outcome: adapter.OutcomeSelected},
	}
	if len(result.SelectionAttempts) != len(wantAttempts) {
		t.Fatalf("selection attempts = %#v", result.SelectionAttempts)
	}
	for index := range wantAttempts {
		if result.SelectionAttempts[index] != wantAttempts[index] {
			t.Fatalf("selection attempt %d = %#v, want %#v", index, result.SelectionAttempts[index], wantAttempts[index])
		}
	}
	frozen, err := os.ReadFile(filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frozen, mustCanonical(t, capabilityData)) {
		t.Fatal("frozen capability does not belong to selected fallback")
	}
}

func TestPlanRemoteMismatchDoesNotLeakOrCreateSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID      = "task-plan-remote-mismatch"
		runID       = "run-plan-remote-mismatch"
		adapterID   = "adapter-plan"
		actualURL   = "https://actual-user:actual-secret@example.invalid/repository.git"
		expectedURL = "https://expected-user:expected-secret@example.invalid/repository.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", actualURL)
	selector, err := adapter.NewSelector(adapter.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Plan(context.Background(), Input{
		StateRoot:      filepath.Join(repositoryRoot, ".marshal"),
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, expectedURL, baseSHA),
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       selector,
		Validator:      newValidator(t),
	})
	if err == nil {
		t.Fatal("Plan() returned nil error")
	}
	message := err.Error()
	for _, secret := range []string{actualURL, expectedURL, "actual-secret", "expected-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked remote credential material: %q", message)
		}
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
}

func TestPlanCapabilityIdentityMismatchPrecedesSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-capability-mismatch"
		runID     = "run-plan-capability-mismatch"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/capability.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	worker := &changingIDWorker{
		firstID: adapterID,
		nextID:  "adapter-changed",
		capability: domain.Record{
			Kind: domain.KindCapabilitySnapshot,
			Data: planningCapabilityFixture(t, adapterID),
		},
	}
	_, err := Plan(context.Background(), Input{
		StateRoot:      filepath.Join(repositoryRoot, ".marshal"),
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA),
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelectorForWorker(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil || err.Error() != errCapabilityAdapterMismatch {
		t.Fatalf("Plan() error = %v, want fixed capability identity mismatch", err)
	}
	assertPlanningNoRunSideEffects(t, filepath.Join(repositoryRoot, ".marshal"), taskID, runID)
}

func TestPlanPreJournalFailureRemovesCreatedWorktree(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-cleanup"
		runID     = "run-plan-cleanup"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/cleanup.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(filepath.Join(runDirectory, "task-spec.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	capabilityData := planningCapabilityFixture(t, adapterID)
	worker := &planningTestWorker{id: adapterID, capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: capabilityData}}
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA),
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil {
		t.Fatal("Plan() returned nil error")
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
	if command.Run() == nil {
		t.Fatal("pre-journal cleanup left the task branch")
	}
}

func TestPlanRejectsInvalidAcceptanceSyntaxBeforeSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-preflight"
		runID     = "run-plan-preflight"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/preflight.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	task["acceptance"].(map[string]any)["commands"] = []any{map[string]any{
		"id":             "broken-inline",
		"argv":           []any{"python3", "-c", "def broken(:"},
		"cwd":            ".",
		"timeoutSeconds": 30,
		"required":       true,
		"baselinePolicy": "on-failure",
		"maxLogBytes":    100000,
	}}
	taskData = mustMarshal(t, task)

	checker := &preflightFakeChecker{finding: &SyntaxFinding{Kind: "SyntaxError", Line: 1, Column: 11}}
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")

	result, err := Plan(context.Background(), Input{
		StateRoot:           stateRoot,
		RepositoryRoot:      repositoryRoot,
		RunID:               runID,
		TaskSpec:            taskData,
		PolicySnapshot:      planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:            planningSelector(t, worker),
		Validator:           newValidator(t),
		PythonSyntaxChecker: checker,
	})
	var syntaxErr *PreflightSyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("Plan() error = %v, want *PreflightSyntaxError", err)
	}
	if syntaxErr.CommandID != "broken-inline" || syntaxErr.ArgvIndex != 2 {
		t.Fatalf("syntax error = %#v, want command broken-inline argv[2]", syntaxErr)
	}
	if len(checker.calls) != 1 || checker.calls[0].interpreter != "python3" || checker.calls[0].source != "def broken(:" {
		t.Fatalf("checker calls = %#v, want exactly one check of the inline script", checker.calls)
	}
	if len(result.SelectionAttempts) != 0 {
		t.Fatalf("selection attempts = %#v, want none before the preflight", result.SelectionAttempts)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
	if command.Run() == nil {
		t.Fatal("preflight rejection left the task branch")
	}
}

func TestPlanRejectsReservedBuiltinBeforeAdmissionPrecondition(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-builtin-preflight"
		runID     = "run-plan-builtin-preflight"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/builtin-preflight.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	task["acceptance"].(map[string]any)["commands"] = []any{map[string]any{
		"id": "unknown-builtin", "argv": []any{"marshal-builtin:unknown:v1", "deliverable:implementation"},
		"cwd": ".", "timeoutSeconds": 30, "required": true, "baselinePolicy": "none", "maxLogBytes": 4096,
	}}
	sentinel := filepath.Join(repositoryRoot, "precondition-must-not-run")
	task["preconditions"] = []any{map[string]any{
		"id": "would-mutate", "argv": []any{"/usr/bin/touch", sentinel}, "cwd": ".", "timeoutSeconds": 5,
	}}
	taskData = mustMarshal(t, task)
	worker := &planningTestWorker{id: adapterID, capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)}}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	result, err := Plan(context.Background(), Input{
		StateRoot: stateRoot, RepositoryRoot: repositoryRoot, RunID: runID,
		TaskSpec: taskData, PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector: planningSelector(t, worker), Validator: newValidator(t),
	})
	if err == nil || err.Error() != "planning: "+verificationbuiltin.ReasonDenied {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(result.SelectionAttempts) != 0 {
		t.Fatalf("selection attempts = %#v", result.SelectionAttempts)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("reserved preflight allowed admission precondition: %v", statErr)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
}

// TestPlanValidatesPolicyBeforePreflightSpawn proves that an invalid or
// identity-mismatched PolicySnapshot is rejected before the syntax preflight,
// so a prohibited policy can never trigger a host interpreter spawn: the
// recording checker must observe zero calls, SelectionAttempts stay empty,
// and no stateRoot or repository side effect appears.
func TestPlanValidatesPolicyBeforePreflightSpawn(t *testing.T) {
	requireGit(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-policy-gate"
		runID     = "run-plan-policy-gate"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/policy-gate.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	task["acceptance"].(map[string]any)["commands"] = []any{map[string]any{
		"id":             "inline-python",
		"argv":           []any{"python3", "-c", "x = 1"},
		"cwd":            ".",
		"timeoutSeconds": 30,
		"required":       true,
		"baselinePolicy": "on-failure",
		"maxLogBytes":    100000,
	}}
	taskData = mustMarshal(t, task)

	policyData := planningPolicyFixture(t, taskID, runID, adapterID)
	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatal(err)
	}
	invalidPolicies := map[string]func(map[string]any){
		"wrong run identity": func(policy map[string]any) {
			policy["runId"] = "run-other"
		},
		"wrong task identity": func(policy map[string]any) {
			policy["taskId"] = "task-other"
		},
		"merge granted": func(policy map[string]any) {
			policy["effective"].(map[string]any)["allowMerge"] = true
		},
	}
	for name, mutate := range invalidPolicies {
		t.Run(name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(policyData, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(candidate)
			checker := &preflightFakeChecker{}
			worker := &planningTestWorker{
				id:         adapterID,
				capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
			}
			stateRoot := filepath.Join(repositoryRoot, ".marshal")
			result, err := Plan(context.Background(), Input{
				StateRoot:           stateRoot,
				RepositoryRoot:      repositoryRoot,
				RunID:               runID,
				TaskSpec:            taskData,
				PolicySnapshot:      sealPolicyDocument(t, candidate),
				Selector:            planningSelector(t, worker),
				Validator:           newValidator(t),
				PythonSyntaxChecker: checker,
			})
			if err == nil {
				t.Fatal("Plan() returned nil error")
			}
			if len(checker.calls) != 0 {
				t.Fatalf("checker calls = %#v, want no interpreter check before the policy gate", checker.calls)
			}
			if len(result.SelectionAttempts) != 0 {
				t.Fatalf("selection attempts = %#v, want none", result.SelectionAttempts)
			}
			assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
			command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
			if command.Run() == nil {
				t.Fatal("policy rejection left the task branch")
			}
		})
	}
}

// TestPlanPolicyDigestFailureHasNoSideEffects proves that a policy whose
// embedded policyDigest does not match the detached recomputation is
// rejected before any repository or stateRoot side effect.
func TestPlanPolicyDigestFailureHasNoSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-digest-gate"
		runID     = "run-plan-digest-gate"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/digest-gate.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	policyData := planningPolicyFixture(t, taskID, runID, adapterID)
	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatal(err)
	}
	// Mutate without resealing: the embedded policyDigest is now stale.
	policy["effective"].(map[string]any)["retentionDays"] = float64(31)
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA),
		PolicySnapshot: mustMarshal(t, policy),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil || err.Error() != ErrPolicyDigestMismatch {
		t.Fatalf("Plan() error = %v, want %q", err, ErrPolicyDigestMismatch)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
	if command.Run() == nil {
		t.Fatal("policy digest rejection left the task branch")
	}
}

// TestPlanFloatingBaseRefFailureHasNoSideEffects proves that a TaskSpec with
// a floating baseRef is rejected by the immutable-SHA gate before any
// worktree, journal, or frozen artifact side effect.
func TestPlanFloatingBaseRefFailureHasNoSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-base-gate"
		runID     = "run-plan-base-gate"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/base-gate.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	task["repository"].(map[string]any)["baseRef"] = "main"
	taskData = mustMarshal(t, task)
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil || err.Error() != "planning: "+ErrBaseRefNotImmutable {
		t.Fatalf("Plan() error = %v, want %q", err, "planning: "+ErrBaseRefNotImmutable)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
	if command.Run() == nil {
		t.Fatal("floating baseRef rejection left the task branch")
	}
}

type changingIDWorker struct {
	firstID, nextID string
	calls           int
	capability      domain.Record
}

func (w *changingIDWorker) ID() string {
	w.calls++
	if w.calls == 1 {
		return w.firstID
	}
	return w.nextID
}

func (w *changingIDWorker) Probe(context.Context) (domain.Record, error) {
	return w.capability, nil
}

func (w *changingIDWorker) Run(context.Context, domain.Record) (domain.Record, error) {
	panic("planning must not run a Worker")
}

func planningGitFixture(t *testing.T) (string, string) {
	t.Helper()
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	planningGit(t, repositoryRoot, "init")
	planningGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	planningGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planningGit(t, repositoryRoot, "add", "README.md")
	planningGit(t, repositoryRoot, "commit", "-m", "fixture")
	return repositoryRoot, planningGitOutput(t, repositoryRoot, "rev-parse", "HEAD")
}

func planningGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	_ = planningGitOutput(t, root, arguments...)
}

func planningGitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, bytes.TrimSpace(output))
	}
	return string(bytes.TrimSpace(output))
}

func planningTaskFixture(t *testing.T, repositoryRoot, taskID, adapterID, remoteURL, baseSHA string) []byte {
	t.Helper()
	fixture := planningFixtureMap(t, "task-spec.json")
	fixture["metadata"].(map[string]any)["id"] = taskID
	fixture["metadata"].(map[string]any)["title"] = "Planning happy path"
	repository := fixture["repository"].(map[string]any)
	repository["path"] = repositoryRoot
	repository["baseRef"] = baseSHA
	repository["remote"] = "origin"
	repository["expectedRemoteUrl"] = remoteURL
	worker := fixture["worker"].(map[string]any)
	worker["preferredAdapter"] = adapterID
	worker["fallbackAdapters"] = []any{}
	worker["executionProfile"] = "workspace-write"
	worker["sessionPolicy"] = "ephemeral"
	fixture["deliverables"] = []any{map[string]any{
		"id": "implementation", "kind": "code", "required": true, "pathGlob": "README.md",
	}}
	publication := fixture["publication"].(map[string]any)
	publication["required"] = false
	publication["provider"] = "none"
	publication["mode"] = "none"
	publication["remote"] = "origin"
	publication["baseBranch"] = "main"
	publication["mergePolicy"] = "never"
	publication["requiredChecks"] = []any{}
	return mustMarshal(t, fixture)
}

func planningPolicyFixture(t *testing.T, taskID, runID, adapterID string) []byte {
	t.Helper()
	fixture := planningFixtureMap(t, "policy-snapshot.json")
	fixture["taskId"] = taskID
	fixture["runId"] = runID
	effective := fixture["effective"].(map[string]any)
	effective["allowFallbackWorkers"] = false
	effective["allowPublication"] = false
	effective["allowMerge"] = false
	effective["allowedAdapters"] = []any{adapterID}
	return sealPolicyDocument(t, fixture)
}

func planningCapabilityFixture(t *testing.T, adapterID string) []byte {
	t.Helper()
	return mustMarshal(t, map[string]any{
		"apiVersion":       "marshal.dev/v1alpha1",
		"kind":             "CapabilitySnapshot",
		"adapterId":        adapterID,
		"adapterVersion":   "test-1",
		"executable":       "/fixture/adapter",
		"executableDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"binaryVersion":    "test-1",
		"probeStatus":      "supported",
		"capabilities": map[string]any{
			"structuredOutput":        []any{"jsonl"},
			"nonInteractiveEdit":      true,
			"sessionPolicies":         []any{"ephemeral"},
			"modelSelection":          true,
			"executionProfiles":       []any{"workspace-write"},
			"nativeBudgets":           []any{},
			"processTreeCancellation": true,
		},
		"probeErrors": []any{},
		"probedAt":    "2026-08-04T12:00:00Z",
	})
}

func TestLocalDogfoodPlanRejectsProfileAndStrictAuthorityWithoutRunSideEffects(t *testing.T) {
	const remoteURL = "https://example.invalid/repository.git"
	observation := localDogfoodObservationFixture()

	for _, test := range []struct {
		name             string
		adapterID        string
		executionProfile string
		capability       func(*testing.T, string) []byte
		wantError        string
		wantAttempts     int
	}{
		{
			name: "hardened task is rejected before selection", adapterID: "qwen", executionProfile: "hardened",
			capability: ordinaryUserCapabilityFixture, wantError: ErrPolicyLocalSurface, wantAttempts: 0,
		},
		{
			name: "strict qoder capability is not ordinary user", adapterID: "qoder", executionProfile: "workspace-write",
			capability: strictQoderCapabilityFixture, wantError: ErrPolicyLocalCapabilityAuthority, wantAttempts: 1,
		},
		{
			name: "strict codex capability is not ordinary user", adapterID: "codex", executionProfile: "workspace-write",
			capability: strictCodexCapabilityFixture, wantError: ErrPolicyLocalCapabilityAuthority, wantAttempts: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositoryRoot, baseSHA := planningGitFixture(t)
			planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
			stateRoot := filepath.Join(repositoryRoot, ".marshal")
			taskID, runID := "task-local-authority", "run-local-authority"

			var task map[string]any
			if err := json.Unmarshal(planningTaskFixture(t, repositoryRoot, taskID, test.adapterID, remoteURL, baseSHA), &task); err != nil {
				t.Fatal(err)
			}
			task["worker"].(map[string]any)["executionProfile"] = test.executionProfile

			var policy map[string]any
			if err := json.Unmarshal(planningPolicyFixture(t, taskID, runID, test.adapterID), &policy); err != nil {
				t.Fatal(err)
			}
			policy["control"].(map[string]any)["requiredApprovals"] = []any{ApprovalGatePlan}
			policy["environmentBinding"] = map[string]any{
				"schemaVersion":         LocalDogfoodEnvironmentBindingSchema,
				"selfProfile":           selfidentity.LocalProfile,
				"activationDigest":      observation.ActivationDigest,
				"identitySubjectDigest": observation.IdentitySubjectDigest,
				"assurance":             "ordinary-user", "execution": "workspace-write",
				"production": false, "publication": "none",
			}

			worker := &planningTestWorker{
				id:         test.adapterID,
				capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: test.capability(t, test.adapterID)},
			}
			result, err := Plan(context.Background(), Input{
				StateRoot: stateRoot, RepositoryRoot: repositoryRoot, RunID: runID,
				TaskSpec: mustMarshal(t, task), PolicySnapshot: sealPolicyDocument(t, policy),
				Selector: planningSelector(t, worker), Validator: newValidator(t),
				Now: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), LocalSelfIdentity: &observation,
			})
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("Plan() err=%v, want %q", err, test.wantError)
			}
			if len(result.SelectionAttempts) != test.wantAttempts {
				t.Fatalf("selection attempts=%d, want %d", len(result.SelectionAttempts), test.wantAttempts)
			}
			if _, statErr := os.Stat(filepath.Join(stateRoot, "runs", runID)); !os.IsNotExist(statErr) {
				t.Fatalf("rejected local plan left Run artifacts: %v", statErr)
			}
			worktreeList := planningGitOutput(t, repositoryRoot, "worktree", "list", "--porcelain")
			if strings.Count(worktreeList, "worktree ") != 1 {
				t.Fatalf("rejected local plan changed worktrees: %s", worktreeList)
			}
		})
	}
}

func ordinaryUserCapabilityFixture(t *testing.T, adapterID string) []byte {
	t.Helper()
	var capability map[string]any
	if err := json.Unmarshal(planningCapabilityFixture(t, adapterID), &capability); err != nil {
		t.Fatal(err)
	}
	capability["authorityMode"] = "ordinary-user"
	return mustMarshal(t, capability)
}

func strictQoderCapabilityFixture(t *testing.T, adapterID string) []byte {
	t.Helper()
	var capability map[string]any
	if err := json.Unmarshal(planningCapabilityFixture(t, adapterID), &capability); err != nil {
		t.Fatal(err)
	}
	addStrictConformanceFixture(capability)
	return mustMarshal(t, capability)
}

func strictCodexCapabilityFixture(t *testing.T, adapterID string) []byte {
	t.Helper()
	var capability map[string]any
	if err := json.Unmarshal(planningCapabilityFixture(t, adapterID), &capability); err != nil {
		t.Fatal(err)
	}
	capability["binaryVersion"] = "0.145.0"
	digest := "sha256:" + strings.Repeat("d", 64)
	nativeBudgetsDigest, err := canonical.DigestJSON(mustMarshal(t, []any{}))
	if err != nil {
		t.Fatal(err)
	}
	capability["codexAuthority"] = map[string]any{
		"schemaVersion": "marshal.codex.authority-metadata.v1", "codexVersion": "0.145.0",
		"binaryIdentityDigest": digest, "hostIdentityDigest": digest, "platform": "linux",
		"launcherKind": "linux-execveat-sealed-memfd-ptrace-v1", "evidenceDigest": digest,
		"configDigest": digest, "keysetDigest": digest, "fenceDigest": digest, "suiteDigest": digest,
		"profileDigest": digest, "argvMatrixDigest": digest, "environmentDigest": digest,
		"eventContractDigest": digest, "permissionContractDigest": digest, "toolPolicyDigest": digest,
		"resultContractDigest": digest, "outputLimitDigest": digest, "nativeBudgetsDigest": nativeBudgetsDigest,
		"trustRootKeyId": "trust-root", "evidenceSignerKeyId": "signer", "trustRootGeneration": 1,
		"authorityGeneration": 1, "revocationSetDigest": digest,
		"observedAt": "2026-08-27T00:00:00Z", "validUntil": "2026-08-27T01:00:00Z",
		"executionProfiles": []any{"workspace-write"},
		"isolationClaim":    "cooperative-host-process-not-malicious-code-sandbox",
	}
	capability["conformanceEvidenceDigest"] = digest
	capability["conformanceTrustRootKeyId"] = "trust-root"
	capability["conformanceProbeProfileDigest"] = digest
	capability["conformanceValidUntil"] = "2026-08-27T01:00:00Z"
	capability["conformanceHostFingerprint"] = digest
	capability["conformanceAuthorityGeneration"] = 1
	return mustMarshal(t, capability)
}

func addStrictConformanceFixture(capability map[string]any) {
	digest := "sha256:" + strings.Repeat("c", 64)
	capability["conformanceEvidenceDigest"] = digest
	capability["conformanceTrustRootKeyId"] = "trust-root"
	capability["conformanceProbeProfileDigest"] = digest
	capability["conformanceValidUntil"] = "2026-08-27T01:00:00Z"
	capability["conformanceHostFingerprint"] = digest
	capability["conformanceAuthorityGeneration"] = 1
}

func planningSelector(t *testing.T, workers ...*planningTestWorker) *adapter.Selector {
	t.Helper()
	registry := adapter.NewRegistry()
	for _, worker := range workers {
		if err := registry.Register(worker); err != nil {
			t.Fatalf("register worker: %v", err)
		}
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	return selector
}

func planningSelectorForWorker(t *testing.T, worker interface {
	ID() string
	Probe(context.Context) (domain.Record, error)
	Run(context.Context, domain.Record) (domain.Record, error)
}) *adapter.Selector {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(worker); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	return selector
}

func mustCanonical(t *testing.T, data []byte) []byte {
	t.Helper()
	result, err := canonical.JSON(data)
	if err != nil {
		t.Fatalf("canonicalize fixture: %v", err)
	}
	return result
}

func assertPlanningNoRunSideEffects(t *testing.T, stateRoot, taskID, runID string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(stateRoot, "worktrees", taskID),
		filepath.Join(stateRoot, "worktrees", taskID+"-"+runID),
		filepath.Join(stateRoot, "runs", runID, "events.jsonl"),
		filepath.Join(stateRoot, "runs", runID, "state.json"),
		filepath.Join(stateRoot, "runs", runID, "task-spec.json"),
		filepath.Join(stateRoot, "runs", runID, "policy-snapshot.json"),
		filepath.Join(stateRoot, "runs", runID, "capability-snapshot.json"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected planning side effect %s: %v", path, err)
		}
	}
}

func planningFixtureMap(t *testing.T, name string) map[string]any {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "schemas", "examples", "happy-path", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return fixture
}

func assertPlanningFrozenFile(t *testing.T, path string, source []byte, digest string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat frozen file %s: %v", filepath.Base(path), err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("frozen file %s mode = %o, want 600", filepath.Base(path), info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frozen file %s: %v", filepath.Base(path), err)
	}
	canonicalSource, err := canonical.JSON(source)
	if err != nil {
		t.Fatalf("canonical source %s: %v", filepath.Base(path), err)
	}
	if !bytes.Equal(data, canonicalSource) {
		t.Fatalf("frozen file %s is not canonical input", filepath.Base(path))
	}
	if got := canonical.DigestBytes(data); got != digest {
		t.Fatalf("frozen file %s digest = %q, want %q", filepath.Base(path), got, digest)
	}
}

// planningMutatedTaskFixture decodes the standard planning task fixture and
// applies mutate before re-marshaling it, so tests can pin the issue #23
// admission, dependsOn and preconditions declarations.
func planningMutatedTaskFixture(t *testing.T, repositoryRoot, taskID, adapterID, remoteURL, baseSHA string, mutate func(map[string]any)) []byte {
	t.Helper()
	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA)
	if mutate == nil {
		return taskData
	}
	var task map[string]any
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	mutate(task)
	return mustMarshal(t, task)
}

func assertPlanningNoTaskBranch(t *testing.T, repositoryRoot, taskID, runID string) {
	t.Helper()
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID+"-"+runID)
	if command.Run() == nil {
		t.Fatal("planning rejection left the task branch")
	}
}

func TestPlanRejectsPreparedAdmissionBeforeSideEffects(t *testing.T) {
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-prepared"
		runID     = "run-plan-prepared"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/prepared.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
		task["admission"] = map[string]any{"status": "prepared"}
	})
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil || err.Error() != ErrAdmissionPrepared {
		t.Fatalf("Plan() error = %v, want fixed sentinel %q", err, ErrAdmissionPrepared)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	assertPlanningNoTaskBranch(t, repositoryRoot, taskID, runID)
}

func TestPlanRunDependencyGate(t *testing.T) {
	sealedMigrationSkip(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-rundep"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/rundep.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	store := runstore.New(stateRoot)
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	seedAdmissionRun(t, store, "run-dep-ready", []domain.State{domain.StatePlanned, domain.StateReady}, admissionBaseSHA, admissionSpecDigest)
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	planWithDependency := func(runID, dependedRunID, requiredState string) (Result, error) {
		taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
			task["dependsOn"] = []any{map[string]any{
				"kind": "run", "runId": dependedRunID, "requiredState": requiredState,
			}}
		})
		return Plan(context.Background(), Input{
			StateRoot:      stateRoot,
			RepositoryRoot: repositoryRoot,
			RunID:          runID,
			TaskSpec:       taskData,
			PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
			Selector:       planningSelector(t, worker),
			Validator:      newValidator(t),
		})
	}

	// A satisfied dependency (exact ACCEPTED match) lets the plan reach READY;
	// the happy-path fixture carries no admission fields at all, so the same
	// run also pins the zero-value backward-compatible behavior end to end.
	result, err := planWithDependency("run-plan-rundep-ok", "run-dep-accepted", string(domain.StateAccepted))
	if err != nil {
		t.Fatalf("Plan(satisfied dependency) = %v, want READY", err)
	}
	if result.State.State != domain.StateReady {
		t.Fatalf("state = %s, want READY", result.State.State)
	}

	cases := []struct {
		name          string
		runID         string
		dependedRunID string
		requiredState string
		wantCategory  string
		wantRunID     string
	}{
		{name: "state mismatch", runID: "run-plan-rundep-mismatch", dependedRunID: "run-dep-ready", requiredState: string(domain.StateAccepted), wantCategory: ErrDependencyStateMismatch, wantRunID: "run-dep-ready"},
		{name: "missing run", runID: "run-plan-rundep-missing", dependedRunID: "run-missing", requiredState: string(domain.StateAccepted), wantCategory: ErrDependencyRunNotFound, wantRunID: "run-missing"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := planWithDependency(test.runID, test.dependedRunID, test.requiredState)
			assertDependencyError(t, err, test.wantCategory, test.wantRunID, "")
			assertPlanningNoRunSideEffects(t, stateRoot, taskID, test.runID)
			assertPlanningNoTaskBranch(t, repositoryRoot, taskID, test.runID)
		})
	}
}

func TestPlanTaskDependencyResolvesLatestRun(t *testing.T) {
	sealedMigrationSkip(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-taskdep"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/taskdep.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	store := runstore.New(stateRoot)
	rejectedChain := []domain.State{
		domain.StatePlanned,
		domain.StateReady,
		domain.StateRunning,
		domain.StateVerifying,
		domain.StateReviewPending,
		domain.StateRejected,
	}
	// The older run is ACCEPTED but the newest run of the task is REJECTED.
	seedAdmissionTaskRun(t, store, "run-task-old", taskID, admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(10, 0).UTC())
	seedAdmissionTaskRun(t, store, "run-task-new", taskID, rejectedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	planWithTaskDependency := func(runID string) (Result, error) {
		taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
			task["dependsOn"] = []any{map[string]any{
				"kind": "task", "taskId": taskID, "requiredState": string(domain.StateAccepted),
			}}
		})
		return Plan(context.Background(), Input{
			StateRoot:      stateRoot,
			RepositoryRoot: repositoryRoot,
			RunID:          runID,
			TaskSpec:       taskData,
			PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
			Selector:       planningSelector(t, worker),
			Validator:      newValidator(t),
		})
	}

	// The latest run is REJECTED, so the ACCEPTED dependency fails closed
	// naming both the taskId and the resolved latest runId.
	_, err := planWithTaskDependency("run-plan-taskdep-mismatch")
	assertTaskDependencyError(t, err, ErrDependencyStateMismatch, taskID, "run-task-new", "state")
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, "run-plan-taskdep-mismatch")
	assertPlanningNoTaskBranch(t, repositoryRoot, taskID, "run-plan-taskdep-mismatch")

	// A newer ACCEPTED run satisfies the same declaration.
	seedAdmissionTaskRun(t, store, "run-task-newest", taskID, admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(30, 0).UTC())
	result, err := planWithTaskDependency("run-plan-taskdep-ok")
	if err != nil {
		t.Fatalf("Plan(satisfied task dependency) = %v, want READY", err)
	}
	if result.State.State != domain.StateReady {
		t.Fatalf("state = %s, want READY", result.State.State)
	}

	// A task with no runs at all fails closed before any side effect.
	taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
		task["dependsOn"] = []any{map[string]any{
			"kind": "task", "taskId": "task-nowhere", "requiredState": string(domain.StateAccepted),
		}}
	})
	_, err = Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          "run-plan-taskdep-nowhere",
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, "run-plan-taskdep-nowhere", adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	assertTaskDependencyError(t, err, ErrDependencyTaskNotFound, "task-nowhere", "", "")
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, "run-plan-taskdep-nowhere")
}

func TestPlanPreconditionFailureLeavesNoSideEffects(t *testing.T) {
	requireShell(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-pre-fail"
		runID     = "run-plan-pre-fail"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/pre-fail.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
		task["preconditions"] = []any{map[string]any{
			"id": "pre-fail", "argv": []any{"sh", "-c", "echo pre-boom; exit 2"},
		}}
	})
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil {
		t.Fatal("Plan() = nil error, want precondition rejection")
	}
	message := err.Error()
	for _, token := range []string{ErrPreconditionFailed, "id=pre-fail", "exit=2", `tail="pre-boom"`} {
		if !strings.Contains(message, token) {
			t.Fatalf("error %q missing %q", message, token)
		}
	}
	// No partial frozen artifacts, journal, snapshot, worktree, or branch may
	// survive a precondition failure.
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	assertPlanningNoTaskBranch(t, repositoryRoot, taskID, runID)
}

func TestPlanPreconditionsPassWithExplicitExecutableAdmission(t *testing.T) {
	requireShell(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-pre-ok"
		runID     = "run-plan-pre-ok"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/pre-ok.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
		task["admission"] = map[string]any{"status": "executable"}
		task["preconditions"] = []any{map[string]any{
			"id": "pre-ok", "argv": []any{"sh", "-c", "exit 0"},
		}}
	})
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	result, err := Plan(context.Background(), Input{
		StateRoot:      filepath.Join(repositoryRoot, ".marshal"),
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err != nil {
		t.Fatalf("Plan(passing preconditions) = %v, want READY", err)
	}
	if result.State.State != domain.StateReady {
		t.Fatalf("state = %s, want READY", result.State.State)
	}
}

// TestPlanRecordsSandboxRequirementsInReadyEvent freezes the M8 recording:
// the READY freeze event carries the two-dimensional sandbox requirements
// derived from the legacy execution profile via
// domain.SandboxRequirementsFromLegacy, on top of the issue-23 gate and
// without changing any existing planning validation.
func TestPlanRecordsSandboxRequirementsInReadyEvent(t *testing.T) {
	assertRecordedRequirements := func(t *testing.T, stateRoot, runID, wantAccessMode, wantAssuranceLevel string) {
		t.Helper()
		events, truncated, err := runstore.New(stateRoot).ReadEvents(runID)
		if err != nil || truncated || len(events) != 2 {
			t.Fatalf("ReadEvents() events=%d truncated=%v err=%v", len(events), truncated, err)
		}
		if events[1].Type != "planning.inputs-frozen" {
			t.Fatalf("second event = %q, want planning.inputs-frozen", events[1].Type)
		}
		recorded, ok := events[1].Payload["sandboxRequirements"].(map[string]any)
		if !ok {
			t.Fatalf("planning.inputs-frozen payload does not record sandboxRequirements: %#v", events[1].Payload)
		}
		if recorded["accessMode"] != wantAccessMode || recorded["minimumAssuranceLevel"] != wantAssuranceLevel {
			t.Fatalf("sandboxRequirements = %#v, want accessMode=%s minimumAssuranceLevel=%s", recorded, wantAccessMode, wantAssuranceLevel)
		}
	}

	t.Run("workspace-write", func(t *testing.T) {
		repositoryRoot, baseSHA := planningGitFixture(t)
		const (
			taskID    = "task-plan-sandbox-ws"
			runID     = "run-plan-sandbox-ws"
			adapterID = "adapter-plan"
			remoteURL = "https://example.invalid/sandbox-ws.git"
		)
		planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
		worker := &planningTestWorker{
			id:         adapterID,
			capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
		}
		stateRoot := filepath.Join(repositoryRoot, ".marshal")
		result, err := Plan(context.Background(), Input{
			StateRoot:      stateRoot,
			RepositoryRoot: repositoryRoot,
			RunID:          runID,
			TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA),
			PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
			Selector:       planningSelector(t, worker),
			Validator:      newValidator(t),
		})
		if err != nil {
			t.Fatalf("Plan(): %v", err)
		}
		if result.State.State != domain.StateReady {
			t.Fatalf("state = %s, want READY", result.State.State)
		}
		assertRecordedRequirements(t, stateRoot, runID, "workspace-write", "workspace-write")
	})

	t.Run("read-only", func(t *testing.T) {
		repositoryRoot, baseSHA := planningGitFixture(t)
		const (
			taskID    = "task-plan-sandbox-ro"
			runID     = "run-plan-sandbox-ro"
			adapterID = "adapter-plan"
			remoteURL = "https://example.invalid/sandbox-ro.git"
		)
		planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
		capabilityData := planningCapabilityFixture(t, adapterID)
		var capability map[string]any
		if err := json.Unmarshal(capabilityData, &capability); err != nil {
			t.Fatal(err)
		}
		capability["capabilities"].(map[string]any)["executionProfiles"] = []any{"workspace-write", "read-only"}
		worker := &planningTestWorker{
			id:         adapterID,
			capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: mustMarshal(t, capability)},
		}
		taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
			task["worker"].(map[string]any)["executionProfile"] = "read-only"
		})
		policyData := planningPolicyFixture(t, taskID, runID, adapterID)
		var policy map[string]any
		if err := json.Unmarshal(policyData, &policy); err != nil {
			t.Fatal(err)
		}
		policy["effective"].(map[string]any)["minimumExecutionProfile"] = "read-only"
		stateRoot := filepath.Join(repositoryRoot, ".marshal")
		result, err := Plan(context.Background(), Input{
			StateRoot:      stateRoot,
			RepositoryRoot: repositoryRoot,
			RunID:          runID,
			TaskSpec:       taskData,
			PolicySnapshot: sealPolicyDocument(t, policy),
			Selector:       planningSelector(t, worker),
			Validator:      newValidator(t),
		})
		if err != nil {
			t.Fatalf("Plan(): %v", err)
		}
		if result.State.State != domain.StateReady {
			t.Fatalf("state = %s, want READY", result.State.State)
		}
		assertRecordedRequirements(t, stateRoot, runID, "read-only", "workspace-write")
	})
}

func TestPlanPreconditionTimeoutFailsClosedBeforeSideEffects(t *testing.T) {
	requireShellAndSleep(t)
	repositoryRoot, baseSHA := planningGitFixture(t)
	const (
		taskID    = "task-plan-pre-slow"
		runID     = "run-plan-pre-slow"
		adapterID = "adapter-plan"
		remoteURL = "https://example.invalid/pre-slow.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningMutatedTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL, baseSHA, func(task map[string]any) {
		task["preconditions"] = []any{map[string]any{
			"id": "pre-slow", "argv": []any{"sh", "-c", "sleep 5"}, "timeoutSeconds": 1,
		}}
	})
	worker := &planningTestWorker{
		id:         adapterID,
		capability: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: planningCapabilityFixture(t, adapterID)},
	}
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	_, err := Plan(context.Background(), Input{
		StateRoot:      stateRoot,
		RepositoryRoot: repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskData,
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil || !strings.Contains(err.Error(), ErrPreconditionTimeout) || !strings.Contains(err.Error(), "id=pre-slow") {
		t.Fatalf("Plan() error = %v, want %q naming id=pre-slow", err, ErrPreconditionTimeout)
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	assertPlanningNoTaskBranch(t, repositoryRoot, taskID, runID)
}
