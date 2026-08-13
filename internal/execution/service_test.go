package execution

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/chiga0/marshal-harness/internal/lifecycle"
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
		state, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state.AttemptsUsed = 2
		writeSnapshotFile(t, fixture, state)
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsReadsRoundBoundDecision(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	state := inspectState(t, fixture)
	decision := reworkDecisionFixture(state, 1)
	decisionData := writeDecisionFile(t, fixture, decision)
	stray := reworkDecisionFixture(state, 2)
	stray.BlockingFindings = []domain.Finding{{ID: "finding-2", Severity: "P0", Title: "stray", Description: "stray round finding", RequiredOutcome: "must never be projected"}}
	writeDecisionFile(t, fixture, stray)
	appendVerifiedAttempt(t, fixture, "attempt-round-bound")
	appendReviewReworkEvent(t, fixture, decisionData)
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-decision.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy review-decision.json should not exist, stat error = %v", err)
	}
	state = inspectState(t, fixture)
	if state.State != domain.StateReworkRequested || state.ReviewRound != 1 {
		t.Fatalf("state = %+v", state)
	}
	findings, err := directLoad(t, fixture)
	if err != nil {
		t.Fatalf("loadReviewFindings failed with real journal authority: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0]["id"] != "finding-1" || findings[0]["severity"] != "P1" || findings[0]["description"] != "verification gate failed" || findings[0]["requiredOutcome"] != "fix the failing gate" {
		t.Fatalf("finding = %+v", findings[0])
	}

	t.Run("snapshot-only-recovery-rejected", func(t *testing.T) {
		snapshotFixture := newExecutionFixture(t, false)
		tampered := inspectState(t, snapshotFixture)
		tampered.State, tampered.ReviewRound, tampered.AttemptsUsed = domain.StateReworkRequested, 2, 1
		writeSnapshotFile(t, snapshotFixture, tampered)
		requireFailsBeforeProbe(t, snapshotFixture)
	})
}

func TestRunSelectsFallbackAdapterFromFrozenCapability(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "missing", fallbackAdapters: []string{"fixture"}, capabilityAdapterID: "fixture"})
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	requestData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.AdapterID != "fixture" {
		t.Fatalf("worker-request adapterId = %q", request.AdapterID)
	}
	promptData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "control", "input", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(promptData), "adapter.id=fixture") {
		t.Fatalf("prompt does not require the selected adapter:\n%s", promptData)
	}
	if strings.Contains(string(promptData), "adapter.id=missing") {
		t.Fatalf("prompt still requires the preferred adapter:\n%s", promptData)
	}
}

func TestRunFailsClosedWhenFrozenCapabilityAdapterDiffers(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "other"})
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatalf("mismatched frozen capability was accepted: %+v", result)
	}
	if strings.Contains(err.Error(), "notes") || strings.Contains(err.Error(), "probeErrors") {
		t.Fatalf("error leaks provider free text: %v", err)
	}
	if entries, statErr := os.ReadDir(filepath.Join(fixture.runDir, "attempts")); statErr == nil && len(entries) > 0 {
		t.Fatalf("worker was started despite capability mismatch: %v", entries)
	}
}

func TestRunSupportsReadOnlyExecutionProfile(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	requestData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		ExecutionProfile string `json:"executionProfile"`
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExecutionProfile != "read-only" {
		t.Fatalf("worker-request executionProfile = %q", request.ExecutionProfile)
	}
}

func TestRunRejectsUnsupportedExecutionProfilesFailClosed(t *testing.T) {
	t.Run("hardened", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "hardened"})
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profiles are supported") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("capability-misses-read-only", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only"})
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profile not supported") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestReworkKeepsOriginalExecutionProfile(t *testing.T) {
	writePreviousAttempt := func(t *testing.T, fixture executionFixture, profile string) {
		t.Helper()
		attemptDir := filepath.Join(fixture.runDir, "attempts", "attempt:prev")
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		request := mustJSON(t, map[string]any{"attemptNumber": 1, "executionProfile": profile})
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), request, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("profile-change-rejected", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
		writePreviousAttempt(t, fixture, "workspace-write")
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profile") {
			t.Fatalf("escalated rework profile accepted: %v", err)
		}
	})
	t.Run("same-profile-accepted", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
		writePreviousAttempt(t, fixture, "read-only")
		result, err := Run(context.Background(), fixture.input)
		if err != nil || result.State.State != domain.StateVerifying {
			t.Fatalf("state = %+v err = %v", result.State, err)
		}
	})
}

type executionFixture struct {
	input              Input
	repository, runDir string
}

type executionFixtureOptions struct {
	preferredAdapter      string
	fallbackAdapters      []string
	capabilityAdapterID   string
	executionProfile      string
	readOnlyCapability    bool
	maxAttempts           int
	maxOperationalRetries int
	maxReworkRounds       int
}

func newExecutionFixture(t *testing.T, fail bool) executionFixture {
	t.Helper()
	return newExecutionFixtureWithOptions(t, fail, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture"})
}

func newExecutionFixtureWithOptions(t *testing.T, fail bool, options executionFixtureOptions) executionFixture {
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
	executionProfile := options.executionProfile
	if executionProfile == "" {
		executionProfile = "workspace-write"
	}
	maxAttempts, maxOperationalRetries, maxReworkRounds := options.maxAttempts, options.maxOperationalRetries, options.maxReworkRounds
	if maxAttempts == 0 {
		maxAttempts = 2
	}
	if maxOperationalRetries == 0 {
		maxOperationalRetries = 1
	}
	profiles := []string{"workspace-write"}
	if options.readOnlyCapability {
		profiles = append(profiles, "read-only")
	}
	capability := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "CapabilitySnapshot", "adapterId": options.capabilityAdapterID, "adapterVersion": "0.1.0", "executable": "/fixture", "executableDigest": "sha256:" + strings.Repeat("a", 64), "binaryVersion": "1", "probeStatus": "supported",
		"capabilities": map[string]any{"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true, "sessionPolicies": []string{"ephemeral"}, "modelSelection": false, "executionProfiles": profiles, "nativeBudgets": []string{}, "processTreeCancellation": true, "notes": []string{}}, "probeErrors": []string{}, "probedAt": "2026-08-04T00:00:00Z",
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
		"worker":      map[string]any{"preferredAdapter": options.preferredAdapter, "fallbackAdapters": options.fallbackAdapters, "executionProfile": executionProfile, "sessionPolicy": "ephemeral"},
		"budgets":     map[string]any{"runTimeoutSeconds": 60, "attemptTimeoutSeconds": 10, "maxAttempts": maxAttempts, "maxOperationalRetries": maxOperationalRetries, "maxReworkRounds": maxReworkRounds, "maxOutputBytes": 100000},
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
	// Planning froze the snapshot and both planning events with the same
	// instant; one durable batch keeps fixture setup cheap.
	now := time.Unix(1, 0).UTC()
	state := domain.NewRunState("TASK-1", runID, now)
	state.State, state.Sequence, state.SpecDigest, state.PolicyDigest, state.CapabilityDigest, state.BaseSHA, state.WorktreePath = domain.StateReady, 2, specDigest, policyDigest, capDigest, base, worktree.Path
	// Real planning authority events: admission binds the Run identity and
	// the five frozen fields to exactly these producer records.
	planningEvents := []domain.RunEvent{
		{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    "event:1",
			RunID:      runID,
			Sequence:   1,
			Type:       "planning.spec-accepted",
			StateFrom:  domain.StateCreated,
			StateTo:    domain.StatePlanned,
			Timestamp:  now,
			Actor:      planningActor(),
			Payload:    map[string]any{"specDigest": specDigest, "executionProfile": executionProfile, "sessionPolicy": "ephemeral"},
		},
		{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    "event:2",
			RunID:      runID,
			Sequence:   2,
			Type:       "planning.inputs-frozen",
			StateFrom:  domain.StatePlanned,
			StateTo:    domain.StateReady,
			Timestamp:  now,
			Actor:      planningActor(),
			Payload:    map[string]any{"adapterId": options.capabilityAdapterID, "baseSha": base, "specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capDigest, "worktreePath": worktree.Path},
		},
	}
	var journal []byte
	for _, event := range planningEvents {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		journal = append(append(journal, data...), '\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
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

type countingAdapter struct {
	delegate *fixtureAdapter
	probes   int
	runs     int
}

func (a *countingAdapter) ID() string { return a.delegate.ID() }

func (a *countingAdapter) Probe(ctx context.Context) (domain.Record, error) {
	a.probes++
	return a.delegate.Probe(ctx)
}

func (a *countingAdapter) Run(ctx context.Context, request domain.Record) (domain.Record, error) {
	a.runs++
	return a.delegate.Run(ctx, request)
}

type journalFingerprint struct {
	records int
	raw     string
}

func captureJournal(t *testing.T, fixture executionFixture) journalFingerprint {
	t.Helper()
	events, _, _ := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	raw, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return journalFingerprint{records: len(events), raw: string(raw)}
}

// requireFailsBeforeProbe is the unified fail-closed oracle: every
// negative class enters through Run and must fail before Probe with no side
// effect — no adapter call, no attempt/control directory, and no journal
// sequence or raw byte change.
func requireFailsBeforeProbe(t *testing.T, fixture executionFixture, fragments ...string) {
	t.Helper()
	adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
	input := fixture.input
	input.Adapter = adapter
	before := captureJournal(t, fixture)
	attempts := attemptDirCount(t, fixture)
	_, err := Run(context.Background(), input)
	if err == nil {
		t.Fatal("expected a fail-closed rejection before the worker starts")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("rejection %q does not contain %q", err, fragment)
		}
	}
	if adapter.probes != 0 || adapter.runs != 0 {
		t.Fatalf("adapter was invoked before the fail-closed rejection: probes=%d runs=%d", adapter.probes, adapter.runs)
	}
	if attemptDirCount(t, fixture) != attempts {
		t.Fatal("attempt/control directories advanced despite the fail-closed rejection")
	}
	if after := captureJournal(t, fixture); after != before {
		t.Fatal("journal sequence or raw bytes advanced despite the fail-closed rejection")
	}
}

func systemActor(id string) *domain.Actor { return &domain.Actor{Type: "system", ID: id} }
func planningActor() *domain.Actor        { return systemActor("marshal-planning") }
func workerRunnerActor() *domain.Actor    { return systemActor("marshal-worker-runner") }
func verifierActor() *domain.Actor        { return systemActor("marshal-verifier") }
func reviewActor() *domain.Actor          { return systemActor("marshal-review") }
func publisherActor() *domain.Actor {
	return &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}
}
func reconciliationActor() *domain.Actor { return systemActor("marshal-reconciliation") }

func verificationPayload() map[string]any {
	return map[string]any{"snapshotDigest": "sha256:" + strings.Repeat("5", 64), "diffDigest": "sha256:" + strings.Repeat("6", 64)}
}

func reworkBudgetOptions(retries int) executionFixtureOptions {
	return executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 5, maxOperationalRetries: retries, maxReworkRounds: 1}
}

func appendVerifiedAttempt(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	appendRunEvents(t, fixture,
		step("worker.started", domain.StateRunning, workerRunnerActor(), attemptID, map[string]any{"adapterId": "fixture"}),
		step("worker.completed", domain.StateVerifying, workerRunnerActor(), attemptID, verificationPayload()),
		step("verification.completed", domain.StateReviewPending, verifierActor(), "", nil))
}

func setupReviewPendingFixture(t *testing.T, attemptID string) executionFixture {
	t.Helper()
	fixture := newExecutionFixture(t, false)
	appendVerifiedAttempt(t, fixture, attemptID)
	return fixture
}

func appendRetrySegment(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	appendRunEvents(t, fixture,
		step("worker.started", domain.StateRunning, workerRunnerActor(), attemptID, map[string]any{"adapterId": "fixture"}),
		step("worker.failed", domain.StateRetryPending, workerRunnerActor(), attemptID, map[string]any{"error": "boom"}))
}

func setupRetryPendingFixture(t *testing.T, attemptID string) executionFixture {
	t.Helper()
	fixture := newExecutionFixture(t, false)
	appendRetrySegment(t, fixture, attemptID)
	return fixture
}

func inspectState(t *testing.T, fixture executionFixture) domain.RunState {
	t.Helper()
	state, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func directLoad(t *testing.T, fixture executionFixture) ([]map[string]string, error) {
	t.Helper()
	var capability struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(fixture.input.Adapter.(*fixtureAdapter).capability, &capability); err != nil {
		t.Fatal(err)
	}
	return loadReviewFindings(runstore.New(fixture.input.StateRoot), fixture.runDir, inspectState(t, fixture), fixture.input.Validator, capability.AdapterID)
}

func writeSnapshotFile(t *testing.T, fixture executionFixture, state domain.RunState) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runDir, "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type journalStep struct {
	eventType string
	target    domain.State
	actor     *domain.Actor
	attemptID string
	payload   map[string]any
}

func step(eventType string, target domain.State, actor *domain.Actor, attemptID string, payload map[string]any) journalStep {
	return journalStep{eventType: eventType, target: target, actor: actor, attemptID: attemptID, payload: payload}
}

// appendRunEvents advances the fixture journal with one durable batch: events
// are replay-validated in order and appended to events.jsonl in a single
// write, while the snapshot is left behind so Inspect replays the journal
// tail. This keeps fixture setup cheap without weakening journal authority.
func appendRunEvents(t *testing.T, fixture executionFixture, steps ...journalStep) domain.RunState {
	t.Helper()
	state := inspectState(t, fixture)
	var batch []byte
	for _, current := range steps {
		if current.payload == nil {
			current.payload = map[string]any{}
		}
		eventID, err := domain.NewID("event")
		if err != nil {
			t.Fatal(err)
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: fixture.input.RunID,
			AttemptID: current.attemptID, Sequence: state.Sequence + 1, Type: current.eventType, StateFrom: state.State, StateTo: current.target,
			Timestamp: time.Unix(100+int64(state.Sequence), 0).UTC(), Actor: current.actor, Payload: current.payload,
		}
		next, err := lifecycle.Replay(state, event)
		if err != nil {
			t.Fatalf("append %s: replay: %v", current.eventType, err)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		batch = append(append(batch, data...), '\n')
		state = next
	}
	appendRawJournalBytes(t, fixture, string(batch))
	return state
}

func appendRunEvent(t *testing.T, fixture executionFixture, eventType string, target domain.State, actor *domain.Actor, attemptID string, payload map[string]any) domain.RunState {
	t.Helper()
	return appendRunEvents(t, fixture, step(eventType, target, actor, attemptID, payload))
}

func reworkDecisionFixture(state domain.RunState, round uint) domain.ReviewDecision {
	return domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: state.TaskID, RunID: state.RunID, ReviewRound: round,
		Reviewer:               domain.Reviewer{Type: "lead-agent", ID: "execution-test"},
		SpecDigest:             state.SpecDigest,
		ReviewPacketDigest:     "sha256:" + strings.Repeat("1", 64),
		VerificationDigest:     "sha256:" + strings.Repeat("2", 64),
		ArtifactManifestDigest: "sha256:" + strings.Repeat("3", 64),
		EvidenceDigest:         "sha256:" + strings.Repeat("4", 64),
		Verdict:                "rework", Summary: "rework required",
		BlockingFindings:          []domain.Finding{{ID: "finding-1", Severity: "P1", Title: "gate failed", Description: "verification gate failed", RequiredOutcome: "fix the failing gate"}},
		NonBlockingFindings:       []domain.Finding{{ID: "note-1", Severity: "P3", Title: "style", Description: "cosmetic naming note"}},
		PublicationRecommendation: "do-not-publish", MergeRecommendation: "do-not-merge",
		DecidedAt: time.Unix(2, 0).UTC(),
	}
}

func writeDecisionFile(t *testing.T, fixture executionFixture, decision domain.ReviewDecision) []byte {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.input.Validator.Validate(domain.KindReviewDecision, data); err != nil {
		t.Fatalf("decision fixture fails contract: %v", err)
	}
	path := filepath.Join(fixture.runDir, "decisions", fmt.Sprintf("decision-%03d.json", decision.ReviewRound))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func reviewReworkPayload(decisionData []byte) map[string]any {
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		panic(err)
	}
	digest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		panic(err)
	}
	return map[string]any{"verdict": "rework", "decisionDigest": digest, "evidenceDigest": decision.EvidenceDigest}
}

func appendReviewReworkEvent(t *testing.T, fixture executionFixture, decisionData []byte) {
	t.Helper()
	appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", reviewReworkPayload(decisionData))
}

func setupReviewOriginRework(t *testing.T, fixture executionFixture, decisionData []byte, attemptID string) {
	t.Helper()
	appendVerifiedAttempt(t, fixture, attemptID)
	appendReviewReworkEvent(t, fixture, decisionData)
}

// setupRound1ReviewOrigin builds the canonical round-1 review-origin journal.
func setupRound1ReviewOrigin(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	setupReviewOriginRework(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)), attemptID)
}

// attemptReviewFindings reads the reviewFindings of an attempt's worker-request.json.
func attemptReviewFindings(t *testing.T, fixture executionFixture, attemptID string) ([]map[string]string, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", attemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		ReviewFindings []map[string]string `json:"reviewFindings"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	return request.ReviewFindings, data
}

func publicationPayload(headSHA string) map[string]any {
	return map[string]any{
		"provider": "github", "repository": "example/repo", "headBranch": "marshal/run-1", "baseBranch": "main",
		"externalId": "pr-1", "uri": "https://github.example/pr/1", "headSha": headSHA,
	}
}

// appendAcceptPublicationChain advances a REVIEW_PENDING fixture through an
// accept decision, publication and remote-check request into CI_PENDING.
func appendAcceptPublicationChain(t *testing.T, fixture executionFixture, headSHA string) {
	t.Helper()
	acceptDecision := reworkDecisionFixture(inspectState(t, fixture), 1)
	acceptDecision.Verdict, acceptDecision.Summary = "accept", "accepted for publication"
	acceptDecision.BlockingFindings = []domain.Finding{}
	acceptDecision.PublicationRecommendation = "publish"
	acceptPayload := reviewReworkPayload(writeDecisionFile(t, fixture, acceptDecision))
	acceptPayload["verdict"] = "accept"
	appendRunEvents(t, fixture,
		step("review.accept", domain.StatePublishing, reviewActor(), "", acceptPayload),
		step("publication.completed", domain.StatePublished, publisherActor(), "", publicationPayload(headSHA)),
		step("publication.checks-requested", domain.StateCIPending, publisherActor(), "", map[string]any{"headSha": headSHA}))
}

func setupCIOriginToCIPending(t *testing.T, fixture executionFixture, attemptID string) string {
	t.Helper()
	const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
	appendVerifiedAttempt(t, fixture, attemptID)
	appendAcceptPublicationChain(t, fixture, headSHA)
	return headSHA
}

func attemptDirCount(t *testing.T, fixture executionFixture) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.runDir, "attempts"))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count
}

func appendRawJournalBytes(t *testing.T, fixture executionFixture, extra string) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(extra); err != nil {
		t.Fatal(err)
	}
}

func TestRunReviewReworkOperationalRetryPersistsFindingsAfterRestart(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
	first, err := Run(context.Background(), fixture.input)
	if err != nil || first.State.State != domain.StateVerifying {
		t.Fatalf("first attempt: state=%+v err=%v", first.State, err)
	}
	appendRunEvent(t, fixture, "verification.completed", domain.StateReviewPending, verifierActor(), "", map[string]any{})
	state := inspectState(t, fixture)
	if state.ReviewRound != 1 {
		t.Fatalf("reviewRound = %d", state.ReviewRound)
	}
	decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(state, 1))
	appendReviewReworkEvent(t, fixture, decisionData)

	capability := fixture.input.Adapter.(*fixtureAdapter).capability
	failInput := fixture.input
	failInput.Adapter = &fixtureAdapter{capability: capability, fail: true}
	second, err := Run(context.Background(), failInput)
	if err == nil {
		t.Fatal("rework attempt operational failure was accepted")
	}
	if second.State.State != domain.StateRetryPending || second.State.OperationalRetriesUsed != 1 || second.State.AttemptsUsed != 2 || second.State.ReviewRound != 1 {
		t.Fatalf("state after rework attempt failure = %+v", second.State)
	}

	// Restart with brand-new caller-side objects; no in-memory state survives.
	freshValidator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	freshState, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if freshState.State != domain.StateRetryPending || freshState.ReviewRound != 1 || freshState.CurrentAttemptID != second.AttemptID {
		t.Fatalf("fresh inspect after restart = %+v", freshState)
	}
	restartInput := Input{StateRoot: fixture.input.StateRoot, RepositoryRoot: fixture.input.RepositoryRoot, RunID: fixture.input.RunID, Adapter: &fixtureAdapter{capability: capability}, Validator: freshValidator}
	third, err := Run(context.Background(), restartInput)
	if err != nil {
		t.Fatalf("restart after operational retry of a rework attempt: %v", err)
	}
	if third.State.State != domain.StateVerifying || third.State.AttemptsUsed != 3 || third.State.ReviewRound != 1 {
		t.Fatalf("state after restart = %+v", third.State)
	}
	reviewFindings, requestData := attemptReviewFindings(t, fixture, third.AttemptID)
	if len(reviewFindings) != 1 {
		t.Fatalf("reviewFindings after restart = %+v", reviewFindings)
	}
	for key, want := range map[string]string{"id": "finding-1", "severity": "P1", "description": "verification gate failed", "requiredOutcome": "fix the failing gate"} {
		if reviewFindings[0][key] != want {
			t.Fatalf("reviewFindings[0][%s] = %q, want %q", key, reviewFindings[0][key], want)
		}
	}
	if len(reviewFindings[0]) != len(projectionFindingKeys) {
		t.Fatalf("reviewFindings[0] has unexpected fields: %+v", reviewFindings[0])
	}
	promptData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", third.AttemptID, "control", "input", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(promptData)
	if !strings.Contains(prompt, `"id":"finding-1"`) || !strings.Contains(prompt, `"requiredOutcome":"fix the failing gate"`) {
		t.Fatalf("prompt after restart does not project the blocking finding:\n%s", prompt)
	}
	for _, leaked := range []string{"note-1", "cosmetic naming note"} {
		if strings.Contains(string(requestData), leaked) || strings.Contains(prompt, leaked) {
			t.Fatalf("non-blocking finding leaked into request or prompt: %q", leaked)
		}
	}
}

func TestRunInvalidReworkAuthorityFailsBeforeWorkerStart(t *testing.T) {
	cases := map[string]func(t *testing.T, fixture executionFixture){
		"decision-digest-mismatch": func(t *testing.T, fixture executionFixture) {
			setupRound1ReviewOrigin(t, fixture, "attempt-authority")
			mutated := reworkDecisionFixture(inspectState(t, fixture), 1)
			mutated.Summary = "tampered after the journal event"
			writeDecisionFile(t, fixture, mutated)
			requireFailsBeforeProbe(t, fixture, "canonical digest")
		},
		"decision-missing": func(t *testing.T, fixture executionFixture) {
			setupRound1ReviewOrigin(t, fixture, "attempt-authority")
			if err := os.Remove(filepath.Join(fixture.runDir, "decisions", "decision-001.json")); err != nil {
				t.Fatal(err)
			}
			requireFailsBeforeProbe(t, fixture, "round-bound ReviewDecision")
		},
		"forged-origin-actor": func(t *testing.T, fixture executionFixture) {
			appendVerifiedAttempt(t, fixture, "attempt-authority")
			decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1))
			appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, workerRunnerActor(), "", reviewReworkPayload(decisionData))
			requireFailsBeforeProbe(t, fixture, "system/marshal-review")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t, newExecutionFixture(t, false))
		})
	}
}

func TestRunCIFailureReworkAndOperationalRetryDoesNotRequireReworkDecision(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
	first, err := Run(context.Background(), fixture.input)
	if err != nil || first.State.State != domain.StateVerifying {
		t.Fatalf("first attempt: state=%+v err=%v", first.State, err)
	}
	appendRunEvent(t, fixture, "verification.completed", domain.StateReviewPending, verifierActor(), "", map[string]any{})
	const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
	appendAcceptPublicationChain(t, fixture, headSHA)
	appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA})

	capability := fixture.input.Adapter.(*fixtureAdapter).capability
	failInput := fixture.input
	failInput.Adapter = &fixtureAdapter{capability: capability, fail: true}
	second, err := Run(context.Background(), failInput)
	if err == nil {
		t.Fatal("ci-origin rework attempt failure was accepted")
	}
	if second.State.State != domain.StateRetryPending || second.State.OperationalRetriesUsed != 1 {
		t.Fatalf("state = %+v", second.State)
	}
	restartInput := fixture.input
	restartInput.Adapter = &fixtureAdapter{capability: capability}
	third, err := Run(context.Background(), restartInput)
	if err != nil {
		t.Fatalf("operational retry after ci-origin rework: %v", err)
	}
	if third.State.State != domain.StateVerifying || third.State.AttemptsUsed != 3 {
		t.Fatalf("state = %+v", third.State)
	}
	reviewFindings, _ := attemptReviewFindings(t, fixture, third.AttemptID)
	if len(reviewFindings) != 0 {
		t.Fatalf("ci-origin operational retry must not project findings: %+v", reviewFindings)
	}
	acceptRaw, err := os.ReadFile(filepath.Join(fixture.runDir, "decisions", "decision-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var preserved domain.ReviewDecision
	if err := json.Unmarshal(acceptRaw, &preserved); err != nil || preserved.Verdict != "accept" {
		t.Fatalf("ci-origin retry demanded the accept decision change: verdict=%q err=%v", preserved.Verdict, err)
	}
}

func TestLoadReviewFindingsRejectsDecisionDigestMismatchBeforeWorkerStart(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	state := inspectState(t, fixture)
	decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(state, 1))
	setupReviewOriginRework(t, fixture, decisionData, "attempt-digest")
	mutated := reworkDecisionFixture(inspectState(t, fixture), 1)
	mutated.Summary = "tampered decision body"
	writeDecisionFile(t, fixture, mutated)
	requireFailsBeforeProbe(t, fixture, "canonical digest")
}

func TestLoadReviewFindingsInitialRetryHasNoDecision(t *testing.T) {
	fixture := newExecutionFixture(t, true)
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("worker failure was accepted")
	}
	if result.State.State != domain.StateRetryPending || result.State.ReviewRound != 0 {
		t.Fatalf("state = %+v", result.State)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDir, "decisions")); !os.IsNotExist(statErr) {
		t.Fatalf("initial retry must not have any review decision, stat = %v", statErr)
	}
	findings, err := directLoad(t, fixture)
	if err != nil {
		t.Fatalf("initial operational retry lineage rejected: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("initial retry must project no findings: %+v", findings)
	}
}

func TestLoadReviewFindingsRejectsUnknownOrConflictingLineage(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("7", 64)

	for name, forged := range map[string]struct {
		eventType string
		actor     *domain.Actor
		payload   map[string]any
		fragment  string
	}{
		"unknown-origin-type":       {"review.custom", reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest}, "not a recognized authority event"},
		"conflicting-verdict":       {"review.rework", reviewActor(), map[string]any{"verdict": "accept", "decisionDigest": validDigest}, "verdict must be rework"},
		"ci-type-from-review-state": {"publication.checks-failed", publisherActor(), map[string]any{"headSha": "deadbeef"}, "ci lineage"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := setupReviewPendingFixture(t, "attempt-lineage")
			appendRunEvent(t, fixture, forged.eventType, domain.StateReworkRequested, forged.actor, "", forged.payload)
			requireFailsBeforeProbe(t, fixture, forged.fragment)
		})
	}
	t.Run("truncated-tail", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-lineage")
		appendRawJournalBytes(t, fixture, `{"apiVersion":"marshal.dev/v1alpha1","ki`)
		requireFailsBeforeProbe(t, fixture, "truncated")
	})
	t.Run("rework-requested-round-zero-lie", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-lineage")
		tampered := inspectState(t, fixture)
		tampered.ReviewRound = 0
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture)
	})
	t.Run("ready-with-review-round-lie", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		tampered := inspectState(t, fixture)
		tampered.ReviewRound = 1
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsValidatesFullJournalAndSnapshot(t *testing.T) {
	newReviewOrigin := func(t *testing.T) executionFixture {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-journal")
		return fixture
	}
	matrix := map[string]func(*domain.RunState){
		"task-id":           func(s *domain.RunState) { s.TaskID = "TASK-OTHER" },
		"review-round":      func(s *domain.RunState) { s.ReviewRound = 9 },
		"attempts-used":     func(s *domain.RunState) { s.AttemptsUsed = 7 },
		"operational-retry": func(s *domain.RunState) { s.OperationalRetriesUsed = 3 },
		"rework-rounds":     func(s *domain.RunState) { s.ReworkRoundsUsed = 4 },
		"current-attempt":   func(s *domain.RunState) { s.CurrentAttemptID = "attempt-other" },
		"terminal-reason":   func(s *domain.RunState) { s.TerminalReason = "forged-terminal" },
		"run-id":            func(s *domain.RunState) { s.RunID = "run-evil" },
		"created-at":        func(s *domain.RunState) { s.CreatedAt = time.Unix(999, 0).UTC() },
		"updated-at":        func(s *domain.RunState) { s.UpdatedAt = time.Unix(999, 0).UTC() },
		"state-kind":        func(s *domain.RunState) { s.Kind = domain.Kind("ForgedKind") },
	}
	for name, mutate := range matrix {
		t.Run("snapshot-"+name, func(t *testing.T) {
			fixture := newReviewOrigin(t)
			tampered := inspectState(t, fixture)
			mutate(&tampered)
			writeSnapshotFile(t, fixture, tampered)
			requireFailsBeforeProbe(t, fixture)
		})
	}
	t.Run("publication-fails-before-probe", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
		headSHA := setupCIOriginToCIPending(t, fixture, "attempt-ci")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA})
		tampered := inspectState(t, fixture)
		tampered.Publication.HeadSHA = "forged-head-sha"
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture, "publication")
	})
	t.Run("raw-record-schema-unknown-field", func(t *testing.T) {
		fixture := newReviewOrigin(t)
		mutateRawJournalLine(t, fixture, "review.rework", `"injected":true`)
		requireFailsBeforeProbe(t, fixture, "RunEvent contract")
	})
	t.Run("raw-record-count-mismatch", func(t *testing.T) {
		fixture := newReviewOrigin(t)
		data, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		appendRawJournalBytes(t, fixture, lines[len(lines)-1]+"\n")
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsRejectsInvalidRoundBoundDecision(t *testing.T) {
	buildWithDecision := func(t *testing.T, mutate func(*domain.ReviewDecision)) executionFixture {
		t.Helper()
		fixture := setupReviewPendingFixture(t, "attempt-decision")
		decision := reworkDecisionFixture(inspectState(t, fixture), 1)
		if mutate != nil {
			mutate(&decision)
		}
		data, err := json.Marshal(decision)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.runDir, "decisions", "decision-001.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", reviewReworkPayload(data))
		return fixture
	}
	t.Run("schema-invalid", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.ReviewPacketDigest = "not-a-digest" })
		requireFailsBeforeProbe(t, fixture, "contract")
	})
	t.Run("identity-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.TaskID = "TASK-OTHER" })
		requireFailsBeforeProbe(t, fixture, "identity")
	})
	t.Run("spec-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.SpecDigest = "sha256:" + strings.Repeat("8", 64) })
		requireFailsBeforeProbe(t, fixture, "identity")
	})
	t.Run("round-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.ReviewRound = 2 })
		requireFailsBeforeProbe(t, fixture, "round")
	})
	t.Run("verdict-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) {
			decision.Verdict = "reject"
			decision.BlockingFindings = []domain.Finding{}
		})
		requireFailsBeforeProbe(t, fixture, "verdict")
	})
	t.Run("only-non-blocking-findings", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.BlockingFindings = []domain.Finding{} })
		findings, err := directLoad(t, fixture)
		if err != nil {
			t.Fatalf("decision without blocking findings rejected: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("non-blocking findings must never become rework findings: %+v", findings)
		}
	})
}

func TestLoadReviewFindingsRequiresAdjacentAttemptChain(t *testing.T) {
	t.Run("mismatched-attempt-between-started-and-failed", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-first", map[string]any{"adapterId": "fixture"}),
			step("worker.failed", domain.StateRetryPending, workerRunnerActor(), "attempt-second", map[string]any{"error": "boom"}))
		requireFailsBeforeProbe(t, fixture, "retry lineage")
	})
	t.Run("ready-origin-duplicate-attempt-id", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRetrySegment(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		requireFailsBeforeProbe(t, fixture, "reused across retry segments")
	})
	t.Run("review-origin-duplicate-attempt-id", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(2))
		setupRound1ReviewOrigin(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		requireFailsBeforeProbe(t, fixture, "reused across retry segments")
	})
	t.Run("non-retry-tail", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRetrySegment(t, fixture, "attempt-tail")
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-tail-2", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-tail-2", verificationPayload()))
		if _, err := directLoad(t, fixture); err == nil || !strings.Contains(err.Error(), "expected worker.failed") {
			t.Fatalf("non-retry tail accepted: %v", err)
		}
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsRejectsForgedOriginActor(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("7", 64)

	for name, forged := range map[string]struct {
		actor    *domain.Actor
		payload  map[string]any
		fragment string
	}{
		"forged-review-actor":     {workerRunnerActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": validDigest}, "system/marshal-review"},
		"missing-review-actor":    {nil, map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": validDigest}, "system/marshal-review"},
		"missing-decision-digest": {reviewActor(), map[string]any{"verdict": "rework", "evidenceDigest": validDigest}, "decisionDigest"},
		"invalid-decision-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": "sha256:nothex", "evidenceDigest": validDigest}, "decisionDigest"},
		"missing-evidence-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest}, "evidenceDigest"},
		"invalid-evidence-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": "md5:abc"}, "evidenceDigest"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := setupReviewPendingFixture(t, "attempt-forged")
			appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, forged.actor, "", forged.payload)
			requireFailsBeforeProbe(t, fixture, forged.fragment)
		})
	}
	t.Run("evidence-digest-mismatch-with-decision", func(t *testing.T) {
		fixture := setupReviewPendingFixture(t, "attempt-forged")
		decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1))
		payload := reviewReworkPayload(decisionData)
		payload["evidenceDigest"] = "sha256:" + strings.Repeat("9", 64)
		appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", payload)
		requireFailsBeforeProbe(t, fixture, "evidenceDigest does not match")
	})
	t.Run("forged-ci-actor", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		headSHA := setupCIOriginToCIPending(t, fixture, "attempt-ci-forged")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, reviewActor(), "", map[string]any{"headSha": headSHA})
		requireFailsBeforeProbe(t, fixture, "publisher/marshal-github-publisher")
	})
	t.Run("ci-head-sha-mismatch", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupCIOriginToCIPending(t, fixture, "attempt-ci-sha")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": "forged-head-sha"})
		requireFailsBeforeProbe(t, fixture, "frozen publication")
	})
	t.Run("ci-head-sha-empty", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupCIOriginToCIPending(t, fixture, "attempt-ci-empty")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": ""})
		requireFailsBeforeProbe(t, fixture, "headSha")
	})
}

func TestLoadReviewFindingsAcceptsOnlyValidRepairAudit(t *testing.T) {
	appendRepair := func(t *testing.T, fixture executionFixture, actor *domain.Actor, attemptID string, mutate func(map[string]any)) {
		t.Helper()
		state := inspectState(t, fixture)
		payload := map[string]any{"repairKind": "snapshot-rebuild", "sourceJournalSequence": float64(state.Sequence)}
		if mutate != nil {
			mutate(payload)
		}
		appendRunEvent(t, fixture, lifecycle.RepairAuditEventType, state.State, actor, attemptID, payload)
	}

	t.Run("valid-repair-skipped-in-lineage", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		findings, err := directLoad(t, fixture)
		if err != nil || len(findings) != 0 {
			t.Fatalf("valid repair audit broke the ready-origin lineage: findings=%+v err=%v", findings, err)
		}
	})
	t.Run("valid-repair-between-origin-and-retry", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
		setupRound1ReviewOrigin(t, fixture, "attempt-repair-2")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		appendRetrySegment(t, fixture, "attempt-repair-2")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		findings, err := directLoad(t, fixture)
		if err != nil {
			t.Fatalf("valid repair audits broke the review lineage: %v", err)
		}
		if len(findings) != 1 || findings[0]["id"] != "finding-1" {
			t.Fatalf("findings = %+v", findings)
		}
	})
	t.Run("forged-repair-actor-fails-before-probe", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-3")
		appendRepair(t, fixture, workerRunnerActor(), "", nil)
		requireFailsBeforeProbe(t, fixture, "system/marshal-reconciliation")
	})
	t.Run("forged-repair-attempt-id", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-4")
		appendRepair(t, fixture, reconciliationActor(), "attempt-forged", nil)
		requireFailsBeforeProbe(t, fixture, "attempt id")
	})
	t.Run("forged-repair-kind", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-5")
		appendRepair(t, fixture, reconciliationActor(), "", func(payload map[string]any) { payload["repairKind"] = "manual-edit" })
		requireFailsBeforeProbe(t, fixture, "repairKind")
	})
	t.Run("forged-source-sequence", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-6")
		appendRepair(t, fixture, reconciliationActor(), "", func(payload map[string]any) { payload["sourceJournalSequence"] = float64(0) })
		requireFailsBeforeProbe(t, fixture, "sourceJournalSequence")
	})
	// sourceJournalSequence must be a canonical unsigned decimal integer in
	// the raw journal bytes: non-canonical notations that decode to the same
	// or another number all fail closed before Probe.
	for name, literal := range map[string]string{
		"fraction":            "4.0",
		"exponent":            "4e0",
		"negative":            "-4",
		"leading-zero-string": `"05"`,
		"string":              `"4"`,
		"wrong-value":         "3",
	} {
		t.Run("non-canonical-sequence-"+name, func(t *testing.T) {
			fixture := setupRetryPendingFixture(t, "attempt-repair-notation")
			appendRepair(t, fixture, reconciliationActor(), "", nil)
			mutateJournalLineContaining(t, fixture, `"type":"`+lifecycle.RepairAuditEventType+`"`, `"sourceJournalSequence":4`, `"sourceJournalSequence":`+literal)
			requireFailsBeforeProbe(t, fixture, "sourceJournalSequence")
		})
	}
	t.Run("non-canonical-sequence-leading-zero-number", func(t *testing.T) {
		// The numeric leading-zero literal 04 is invalid JSON under
		// RFC 8259, but the JCS number parser behind canonical admission
		// (strconv.ParseFloat based) accepts it, so the canonical admission
		// gate lets the raw line through. The strict authoritative journal
		// decode inside runstore (encoding/json) therefore fail-closes this
		// input first with a decode journal record error — earlier than both
		// the execution canonical admission rejection and the repair-layer
		// sourceJournalSequence notation check. That earlier rejection layer
		// is the expected fail-closed behavior: the run is refused before
		// any review findings admission, and every valid JSON non-canonical
		// notation still reaches the repair-layer sentinel in the notation
		// table above.
		fixture := setupRetryPendingFixture(t, "attempt-repair-notation")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		mutateJournalLineContaining(t, fixture, `"type":"`+lifecycle.RepairAuditEventType+`"`, `"sourceJournalSequence":4`, `"sourceJournalSequence":04`)
		requireFailsBeforeProbe(t, fixture, "decode journal record")
	})
}

// mutateRawJournalLines rewrites events.jsonl through a line-level callback,
// keeping the trailing newline so journal shape is preserved.
func mutateRawJournalLines(t *testing.T, fixture executionFixture, mutate func(lines []string)) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	mutate(lines)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mutateJournalLineContaining applies one old->new replacement inside the
// first journal line that carries marker, so a single event line can be
// forged without touching the rest of the journal.
func mutateJournalLineContaining(t *testing.T, fixture executionFixture, marker, old, new string) {
	t.Helper()
	mutateRawJournalLines(t, fixture, func(lines []string) {
		for index, line := range lines {
			if strings.Contains(line, marker) {
				if !strings.Contains(line, old) {
					t.Fatalf("journal line with %q does not contain %q", marker, old)
				}
				lines[index] = strings.Replace(line, old, new, 1)
				return
			}
		}
		t.Fatalf("journal has no line containing %q", marker)
	})
}

func TestRunRejectsCrossDirectoryRunIdentityBeforeProbe(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	tampered := inspectState(t, fixture)
	tampered.RunID = "run-forged-directory"
	writeSnapshotFile(t, fixture, tampered)
	requireFailsBeforeProbe(t, fixture, "identity does not match the requested run")
}

func TestRunRejectsForgedPlanningAuthorityBeforeProbe(t *testing.T) {
	const specAcceptedType = `"type":"planning.spec-accepted"`
	const inputsFrozenType = `"type":"planning.inputs-frozen"`
	const planningActorLiteral = `"actor":{"type":"system","id":"marshal-planning"}`
	forgedDigest := "sha256:" + strings.Repeat("f", 64)

	t.Run("first-event-type-not-spec-accepted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, specAcceptedType, `"type":"planning.inputs-frozen"`)
		requireFailsBeforeProbe(t, fixture, "planning.spec-accepted")
	})
	t.Run("spec-accepted-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, ","+planningActorLiteral, "")
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("spec-accepted-actor-id-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, `"id":"marshal-planning"`, `"id":"marshal-planning-forged"`)
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("spec-accepted-spec-digest-mismatch", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		state := inspectState(t, fixture)
		mutateJournalLineContaining(t, fixture, specAcceptedType, `"specDigest":"`+state.SpecDigest+`"`, `"specDigest":"`+forgedDigest+`"`)
		requireFailsBeforeProbe(t, fixture, "planning.spec-accepted payload specDigest")
	})
	t.Run("inputs-frozen-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, ","+planningActorLiteral, "")
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("inputs-frozen-actor-id-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, `"id":"marshal-planning"`, `"id":"marshal-planning-forged"`)
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("inputs-frozen-missing", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, inputsFrozenType, `"type":"planning.spec-accepted"`)
		requireFailsBeforeProbe(t, fixture, "exactly one planning.inputs-frozen")
	})
	t.Run("inputs-frozen-field-mismatch", func(t *testing.T) {
		for field, forged := range map[string]string{
			"specDigest":       `"specDigest":"` + forgedDigest + `"`,
			"policyDigest":     `"policyDigest":"` + forgedDigest + `"`,
			"capabilityDigest": `"capabilityDigest":"` + forgedDigest + `"`,
			"baseSha":          `"baseSha":"` + strings.Repeat("9", 40) + `"`,
			"worktreePath":     `"worktreePath":"/forged/worktree/path"`,
		} {
			t.Run(field, func(t *testing.T) {
				fixture := newExecutionFixture(t, false)
				state := inspectState(t, fixture)
				original := map[string]string{
					"specDigest":       `"specDigest":"` + state.SpecDigest + `"`,
					"policyDigest":     `"policyDigest":"` + state.PolicyDigest + `"`,
					"capabilityDigest": `"capabilityDigest":"` + state.CapabilityDigest + `"`,
					"baseSha":          `"baseSha":"` + state.BaseSHA + `"`,
					"worktreePath":     `"worktreePath":"` + state.WorktreePath + `"`,
				}[field]
				mutateJournalLineContaining(t, fixture, inputsFrozenType, original, forged)
				requireFailsBeforeProbe(t, fixture, "planning.inputs-frozen")
			})
		}
	})
}

func TestRunRejectsUnsafeRawJournalBytesBeforeProbe(t *testing.T) {
	t.Run("invalid-utf8", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateRawJournalLines(t, fixture, func(lines []string) {
			lines[0] = lines[0][:20] + "\xff" + lines[0][20:]
		})
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
	t.Run("nested-duplicate-member", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		state := inspectState(t, fixture)
		mutateJournalLineContaining(t, fixture, `"type":"planning.inputs-frozen"`,
			`"worktreePath":"`+state.WorktreePath+`"`,
			`"worktreePath":"`+state.WorktreePath+`","nested":{"dup":1,"dup":2}`)
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
	t.Run("trailing-second-value", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateRawJournalLines(t, fixture, func(lines []string) {
			lines[0] = lines[0] + `{"trailing":"second-value"}`
		})
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
}

func TestRunRejectsForgedEvidenceAuthorityBeforeProbe(t *testing.T) {
	t.Run("verification-completed-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-vauth", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-vauth", verificationPayload()),
			step("verification.completed", domain.StateReviewPending, nil, "", map[string]any{}))
		// The run state gate only admits REWORK_REQUESTED runs, so a
		// legitimate round-1 review.rework origin brings the journal back to
		// an admittable state; admission then reaches the journal authority
		// check, which must reject the omitted verifier actor itself instead
		// of the state gate rejecting the fixture first.
		appendReviewReworkEvent(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)))
		requireFailsBeforeProbe(t, fixture, "system/marshal-verifier")
	})
	t.Run("verification-completed-actor-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-vauth-forged", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-vauth-forged", verificationPayload()),
			step("verification.completed", domain.StateReviewPending, workerRunnerActor(), "", map[string]any{}))
		// Same admission path as the omitted case: the legitimate rework
		// origin keeps the run admittable so the forged worker-runner actor
		// is rejected by the verifier authority check itself.
		appendReviewReworkEvent(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)))
		requireFailsBeforeProbe(t, fixture, "system/marshal-verifier")
	})
	t.Run("publication-completed-actor-omitted", func(t *testing.T) {
		fixture := setupReviewPendingFixture(t, "attempt-pauth")
		acceptDecision := reworkDecisionFixture(inspectState(t, fixture), 1)
		acceptDecision.Verdict, acceptDecision.Summary = "accept", "accepted for publication"
		acceptDecision.BlockingFindings = []domain.Finding{}
		acceptDecision.PublicationRecommendation = "publish"
		acceptPayload := reviewReworkPayload(writeDecisionFile(t, fixture, acceptDecision))
		acceptPayload["verdict"] = "accept"
		const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
		appendRunEvents(t, fixture,
			step("review.accept", domain.StatePublishing, reviewActor(), "", acceptPayload),
			step("publication.completed", domain.StatePublished, nil, "", publicationPayload(headSHA)),
			step("publication.checks-requested", domain.StateCIPending, publisherActor(), "", map[string]any{"headSha": headSHA}),
			step("publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA}))
		requireFailsBeforeProbe(t, fixture, "publisher/marshal-github-publisher")
	})
}

func mutateRawJournalLine(t *testing.T, fixture executionFixture, typePrefix, injectedField string) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for index, line := range lines {
		if strings.Contains(line, `"type":"`+typePrefix+`"`) {
			lines[index] = strings.TrimSuffix(line, "}") + "," + injectedField + "}"
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("journal has no event with type %q", typePrefix)
}
