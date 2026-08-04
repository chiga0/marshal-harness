package planning

import (
	"bytes"
	"context"
	"encoding/json"
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

	taskData := planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL)
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
	repositoryRoot, _ := planningGitFixture(t)
	const (
		taskID      = "task-plan-fallback"
		runID       = "run-plan-fallback"
		preferredID = "adapter-missing"
		fallbackID  = "adapter-fallback"
		remoteURL   = "https://example.invalid/fallback.git"
	)
	planningGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	taskData := planningTaskFixture(t, repositoryRoot, taskID, preferredID, remoteURL)
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
	policyData = mustMarshal(t, policy)
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
	repositoryRoot, _ := planningGitFixture(t)
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
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, expectedURL),
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
	repositoryRoot, _ := planningGitFixture(t)
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
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL),
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
	repositoryRoot, _ := planningGitFixture(t)
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
		TaskSpec:       planningTaskFixture(t, repositoryRoot, taskID, adapterID, remoteURL),
		PolicySnapshot: planningPolicyFixture(t, taskID, runID, adapterID),
		Selector:       planningSelector(t, worker),
		Validator:      newValidator(t),
	})
	if err == nil {
		t.Fatal("Plan() returned nil error")
	}
	assertPlanningNoRunSideEffects(t, stateRoot, taskID, runID)
	command := exec.Command("git", "-C", repositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/marshal/"+taskID)
	if command.Run() == nil {
		t.Fatal("pre-journal cleanup left the task branch")
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

func planningTaskFixture(t *testing.T, repositoryRoot, taskID, adapterID, remoteURL string) []byte {
	t.Helper()
	fixture := planningFixtureMap(t, "task-spec.json")
	fixture["metadata"].(map[string]any)["id"] = taskID
	fixture["metadata"].(map[string]any)["title"] = "Planning happy path"
	repository := fixture["repository"].(map[string]any)
	repository["path"] = repositoryRoot
	repository["baseRef"] = "HEAD"
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
	return mustMarshal(t, fixture)
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
