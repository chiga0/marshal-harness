package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalrepo "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type fixtureAdapter struct {
	capability  []byte
	fail        bool
	breakGit    bool
	badIdentity bool
}

func (a *fixtureAdapter) ID() string { return "fixture" }
func (a *fixtureAdapter) Probe(context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: a.capability}, nil
}
func (a *fixtureAdapter) Run(_ context.Context, request domain.Record) (domain.Record, error) {
	if a.fail {
		return domain.Record{}, os.ErrDeadlineExceeded
	}
	var input struct{ TaskID, RunID, AttemptID, WorktreePath string }
	if err := json.Unmarshal(request.Data, &input); err != nil {
		return domain.Record{}, err
	}
	if err := os.WriteFile(filepath.Join(input.WorktreePath, "change.txt"), []byte("worker change\n"), 0o600); err != nil {
		return domain.Record{}, err
	}
	if a.breakGit {
		if err := os.Rename(filepath.Join(input.WorktreePath, ".git"), filepath.Join(input.WorktreePath, ".git.broken")); err != nil {
			return domain.Record{}, err
		}
	}
	result := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": input.TaskID, "runId": input.RunID, "attemptId": input.AttemptID,
		"adapter": map[string]any{"id": "fixture", "executable": "/fixture", "version": "1"}, "status": "completed", "summary": "done",
		"declaredChangedFiles": []string{"change.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{},
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
	if a.badIdentity {
		result["attemptId"] = "attempt-other"
	}
	data, err := json.Marshal(result)
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, err
}

func TestRunPersistsAttemptAndRequiresIndependentVerification(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying || result.State.AttemptsUsed != 1 {
		t.Fatalf("state = %+v", result.State)
	}
	attempt := filepath.Join(fixture.runDir, "attempts", result.AttemptID)
	for _, path := range []string{"worker-request.json", "worker-result.json", "worktree-snapshot.json", "control/input/task-spec.json", "control/input/prompt.md"} {
		if _, err := os.Stat(filepath.Join(attempt, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.repository, "change.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker edit leaked into main checkout: %v", err)
	}
}

func TestRunClassifiesOperationalFailureAndConsumesRetryBudget(t *testing.T) {
	fixture := newExecutionFixture(t, true)
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("worker failure was accepted")
	}
	if result.State.State != domain.StateRetryPending || result.State.OperationalRetriesUsed != 1 {
		t.Fatalf("state = %+v", result.State)
	}
}

func TestRunBlocksWhenPostWorkerEvidenceCannotBeRecorded(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	fixture.input.Adapter.(*fixtureAdapter).breakGit = true
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("observation failure was accepted")
	}
	if result.State.State != domain.StateBlocked || result.State.TerminalReason == "" {
		t.Fatalf("state = %+v", result.State)
	}
	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if events[len(events)-1].Type != "worker.evidence-failed" {
		t.Fatalf("last event = %+v", events[len(events)-1])
	}
}

func TestRunRejectsWorkerResultIdentityAndAttemptBudget(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		fixture.input.Adapter.(*fixtureAdapter).badIdentity = true
		result, err := Run(context.Background(), fixture.input)
		if err == nil || result.State.State != domain.StateRetryPending {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("budget", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		store := runstore.New(fixture.input.StateRoot)
		lease, err := store.Acquire(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.Inspect(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state.AttemptsUsed = 2
		if err := store.WriteSnapshot(lease, state); err != nil {
			t.Fatal(err)
		}
		_ = lease.Release()
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
			t.Fatalf("err=%v", err)
		}
	})
}

type executionFixture struct {
	input              Input
	repository, runDir string
}

func newExecutionFixture(t *testing.T, fail bool) executionFixture {
	t.Helper()
	repository := t.TempDir()
	git(t, repository, "init", "-q")
	git(t, repository, "config", "user.name", "Marshal Test")
	git(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "README.md")
	git(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	location, err := marshalrepo.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, "TASK-1", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	capability := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "CapabilitySnapshot", "adapterId": "fixture", "adapterVersion": "0.1.0", "executable": "/fixture", "executableDigest": "sha256:" + strings.Repeat("a", 64), "binaryVersion": "1", "probeStatus": "supported",
		"capabilities": map[string]any{"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true, "sessionPolicies": []string{"ephemeral"}, "modelSelection": false, "executionProfiles": []string{"workspace-write"}, "nativeBudgets": []string{}, "processTreeCancellation": true, "notes": []string{}}, "probeErrors": []string{}, "probedAt": "2026-08-04T00:00:00Z",
	})
	policy := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "PolicySnapshot", "taskId": "TASK-1", "runId": "run-1",
		"sources":      []any{map[string]any{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"effective":    map[string]any{"minimumExecutionProfile": "workspace-write", "requireEnforcedNetworkPolicy": false, "networkPolicy": "unenforced", "allowFallbackWorkers": false, "allowWorkerSubagents": false, "allowPublication": false, "allowMerge": false, "allowGateWaivers": false, "allowedAdapters": []string{"fixture"}, "environmentAllowlist": []string{"PATH"}, "retentionDays": 1},
		"policyDigest": "sha256:" + strings.Repeat("c", 64), "generatedAt": "2026-08-04T00:00:00Z",
	})
	task := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "Task", "metadata": map[string]any{"id": "TASK-1", "title": "fixture"},
		"repository": map[string]any{"path": repository, "baseRef": "HEAD", "remote": "origin"}, "work": map[string]any{"objective": "write change.txt", "constraints": []string{}, "nonGoals": []string{}},
		"scope":      map[string]any{"allowPaths": []string{"change.txt"}, "denyPaths": []string{}, "allowSubmodules": false, "maxChangedFiles": 2, "maxDiffBytes": 10000},
		"acceptance": map[string]any{"commands": []any{}, "allowNoChange": false}, "deliverables": []any{map[string]any{"id": "code", "kind": "code", "required": true, "pathGlob": "change.txt"}},
		"worker":      map[string]any{"preferredAdapter": "fixture", "fallbackAdapters": []string{}, "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"},
		"budgets":     map[string]any{"runTimeoutSeconds": 60, "attemptTimeoutSeconds": 10, "maxAttempts": 2, "maxOperationalRetries": 1, "maxReworkRounds": 0, "maxOutputBytes": 100000},
		"publication": map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	})
	if err := validator.Validate(domain.KindTask, task); err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	runDir := filepath.Join(location.StateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), task, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "capability-snapshot.json"), capability, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "policy-snapshot.json"), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	specDigest, _ := canonical.DigestJSON(task)
	capDigest, _ := canonical.DigestJSON(capability)
	policyDigest, _ := canonical.DigestJSON(policy)
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1, 0).UTC()
	for index, states := range [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}} {
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:" + string(rune('1'+index)), RunID: runID, Sequence: uint64(index + 1), Type: "fixture.transition", StateFrom: states[0], StateTo: states[1], Timestamp: now, Payload: map[string]any{}}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState("TASK-1", runID, now)
	state.State, state.Sequence, state.SpecDigest, state.PolicyDigest, state.CapabilityDigest, state.BaseSHA, state.WorktreePath = domain.StateReady, 2, specDigest, policyDigest, capDigest, base, worktree.Path
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	return executionFixture{Input{StateRoot: location.StateRoot, RepositoryRoot: manager.Root, RunID: runID, Adapter: &fixtureAdapter{capability: capability, fail: fail}, Validator: validator}, repository, runDir}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", directory}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
