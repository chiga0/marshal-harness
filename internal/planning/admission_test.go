package planning

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const (
	admissionBaseSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	admissionSpecDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// admissionAcceptedChain is the shortest legal journal path to ACCEPTED.
var admissionAcceptedChain = []domain.State{
	domain.StatePlanned,
	domain.StateReady,
	domain.StateRunning,
	domain.StateVerifying,
	domain.StateReviewPending,
	domain.StateAccepted,
}

// seedAdmissionRun journals the transition chain with fixture.transition
// events and writes a snapshot that matches the journal tail, mirroring the
// fixture pattern used by the control tests. The resulting run passes the
// journal/snapshot consistency checks of the read-only Inspect.
func seedAdmissionRun(t *testing.T, store *runstore.Store, runID string, chain []domain.State, baseSHA, specDigest string) {
	t.Helper()
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	current := domain.StateCreated
	for index, target := range chain {
		attemptID := ""
		if target == domain.StateRunning {
			attemptID = "attempt-01"
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    fmt.Sprintf("event-%02d", index+1),
			RunID:      runID,
			AttemptID:  attemptID,
			Sequence:   uint64(index + 1),
			Type:       "fixture.transition",
			StateFrom:  current,
			StateTo:    target,
			Timestamp:  time.Unix(int64(index+2), 0).UTC(),
			Payload:    map[string]any{},
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
		current = target
	}
	state := domain.NewRunState("task-admission", runID, time.Unix(1, 0).UTC())
	state.State = current
	state.Sequence = uint64(len(chain))
	state.BaseSHA = baseSHA
	state.SpecDigest = specDigest
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
}

func assertDependencyError(t *testing.T, err error, wantCategory, wantRunID, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ResolveRunDependencies() error = nil, want %q", wantCategory)
	}
	message := err.Error()
	if !strings.Contains(message, wantCategory) || !strings.Contains(message, "runId="+wantRunID) {
		t.Fatalf("error = %q, want category %q naming runId=%s", message, wantCategory, wantRunID)
	}
	if wantField != "" && !strings.Contains(message, "field="+wantField) {
		t.Fatalf("error = %q, want field=%s", message, wantField)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error %q is not permanent", message)
	}
}

func TestResolveRunDependenciesSatisfied(t *testing.T) {
	sealedMigrationSkip(t)
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	seedAdmissionRun(t, store, "run-dep-ready", []domain.State{domain.StatePlanned, domain.StateReady}, admissionBaseSHA, admissionSpecDigest)

	// Exact ACCEPTED match with both optional bindings pinned.
	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		BaseSHA:       admissionBaseSHA,
		SpecDigest:    admissionSpecDigest,
	}})
	if err != nil {
		t.Fatalf("ResolveRunDependencies() = %v, want nil", err)
	}

	// Empty optional fields disable the corresponding checks.
	if err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted}}); err != nil {
		t.Fatalf("ResolveRunDependencies(optional fields empty) = %v, want nil", err)
	}

	// Several satisfied dependencies resolve in order.
	err = ResolveRunDependencies(store, []RunDependency{
		{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted},
		{RunID: "run-dep-ready", RequiredState: domain.StateReady, BaseSHA: admissionBaseSHA, SpecDigest: admissionSpecDigest},
	})
	if err != nil {
		t.Fatalf("ResolveRunDependencies(multiple) = %v, want nil", err)
	}
}

func TestResolveRunDependenciesEmptyList(t *testing.T) {
	store := runstore.New(t.TempDir())
	if err := ResolveRunDependencies(store, nil); err != nil {
		t.Fatalf("ResolveRunDependencies(nil) = %v, want nil", err)
	}
	if err := ResolveRunDependencies(store, []RunDependency{}); err != nil {
		t.Fatalf("ResolveRunDependencies(empty) = %v, want nil", err)
	}
}

func TestResolveRunDependenciesStateMismatch(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-ready", []domain.State{domain.StatePlanned, domain.StateReady}, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-ready", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyStateMismatch, "run-dep-ready", "state")
}

func TestResolveRunDependenciesUnknownRun(t *testing.T) {
	sealedMigrationSkip(t)
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-missing", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunNotFound, "run-missing", "")
}

func TestResolveRunDependenciesBaseSHAMismatch(t *testing.T) {
	sealedMigrationSkip(t)
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		BaseSHA:       strings.Repeat("f", 40),
	}})
	assertDependencyError(t, err, ErrDependencyBaseMismatch, "run-dep-accepted", "baseSha")
}

func TestResolveRunDependenciesSpecDigestMismatch(t *testing.T) {
	sealedMigrationSkip(t)
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		SpecDigest:    "sha256:" + strings.Repeat("e", 64),
	}})
	assertDependencyError(t, err, ErrDependencyDigestMismatch, "run-dep-accepted", "specDigest")
}

func TestResolveRunDependenciesUnreadableStateFailsClosed(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionRun(t, store, "run-dep-corrupt", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	if err := os.WriteFile(filepath.Join(root, "runs", "run-dep-corrupt", "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-corrupt", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunUnreadable, "run-dep-corrupt", "")

	// A syntactically invalid run ID fails closed instead of escaping.
	err = ResolveRunDependencies(store, []RunDependency{{RunID: "../escape", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunUnreadable, "../escape", "")
}

func TestResolveRunDependenciesIsReadOnlyAndOrdered(t *testing.T) {
	sealedMigrationSkip(t)
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	// The first failing dependency determines the error.
	err := ResolveRunDependencies(store, []RunDependency{
		{RunID: "run-missing", RequiredState: domain.StateAccepted},
		{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted},
	})
	assertDependencyError(t, err, ErrDependencyRunNotFound, "run-missing", "")

	// Resolution never mutates the depended-on run.
	state, err := store.Inspect("run-dep-accepted")
	if err != nil || state.State != domain.StateAccepted || state.Sequence != uint64(len(admissionAcceptedChain)) ||
		state.BaseSHA != admissionBaseSHA || state.SpecDigest != admissionSpecDigest {
		t.Fatalf("inspect after resolution = %+v, err = %v", state, err)
	}
	events, truncated, err := store.ReadEvents("run-dep-accepted")
	if err != nil || truncated || len(events) != len(admissionAcceptedChain) {
		t.Fatalf("journal after resolution = %d events, truncated = %v, err = %v", len(events), truncated, err)
	}
}

// seedAdmissionTaskRun mirrors seedAdmissionRun but pins the run's taskId
// and updatedAt, so task-scoped dependency resolution can order several runs
// of one task deterministically.
func seedAdmissionTaskRun(t *testing.T, store *runstore.Store, runID, taskID string, chain []domain.State, baseSHA, specDigest string, updatedAt time.Time) {
	t.Helper()
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	current := domain.StateCreated
	for index, target := range chain {
		attemptID := ""
		if target == domain.StateRunning {
			attemptID = "attempt-01"
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    fmt.Sprintf("event-%02d", index+1),
			RunID:      runID,
			AttemptID:  attemptID,
			Sequence:   uint64(index + 1),
			Type:       "fixture.transition",
			StateFrom:  current,
			StateTo:    target,
			Timestamp:  time.Unix(int64(index+2), 0).UTC(),
			Payload:    map[string]any{},
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
		current = target
	}
	state := domain.NewRunState(taskID, runID, time.Unix(1, 0).UTC())
	state.State = current
	state.Sequence = uint64(len(chain))
	state.BaseSHA = baseSHA
	state.SpecDigest = specDigest
	state.UpdatedAt = updatedAt
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
}

func assertTaskDependencyError(t *testing.T, err error, wantCategory, wantTaskID, wantRunID, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("task dependency resolution error = nil, want %q", wantCategory)
	}
	message := err.Error()
	if !strings.Contains(message, wantCategory) || !strings.Contains(message, "taskId="+wantTaskID) {
		t.Fatalf("error = %q, want category %q naming taskId=%s", message, wantCategory, wantTaskID)
	}
	if wantRunID != "" && !strings.Contains(message, "runId="+wantRunID) {
		t.Fatalf("error = %q, want resolved runId=%s", message, wantRunID)
	}
	if wantField != "" && !strings.Contains(message, "field="+wantField) {
		t.Fatalf("error = %q, want field=%s", message, wantField)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error %q is not permanent", message)
	}
}

func TestResolveTaskSpecDependenciesTaskSatisfied(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionTaskRun(t, store, "run-task-old", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(10, 0).UTC())
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())

	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-dep",
		RequiredState: string(domain.StateAccepted),
		BaseSHA:       admissionBaseSHA,
		SpecDigest:    admissionSpecDigest,
	}})
	if err != nil {
		t.Fatalf("resolveTaskSpecDependencies() = %v, want nil", err)
	}
}

func TestResolveTaskSpecDependenciesLatestRunWins(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	rejectedChain := []domain.State{
		domain.StatePlanned,
		domain.StateReady,
		domain.StateRunning,
		domain.StateVerifying,
		domain.StateReviewPending,
		domain.StateRejected,
	}
	// The older run is ACCEPTED but the newest run of the task is REJECTED,
	// so an ACCEPTED dependency must fail closed against the latest run.
	seedAdmissionTaskRun(t, store, "run-task-old", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(10, 0).UTC())
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", rejectedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())

	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-dep",
		RequiredState: string(domain.StateAccepted),
	}})
	assertTaskDependencyError(t, err, ErrDependencyStateMismatch, "task-dep", "run-task-new", "state")
}

func TestResolveTaskSpecDependenciesTaskNotFound(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionTaskRun(t, store, "run-other", "task-other", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(10, 0).UTC())

	// A task with no runs at all fails closed.
	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-missing",
		RequiredState: string(domain.StateAccepted),
	}})
	assertTaskDependencyError(t, err, ErrDependencyTaskNotFound, "task-missing", "", "")

	// A state root without a runs directory fails closed the same way.
	err = resolveTaskSpecDependencies(filepath.Join(t.TempDir(), "empty-state"), store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-missing",
		RequiredState: string(domain.StateAccepted),
	}})
	assertTaskDependencyError(t, err, ErrDependencyTaskNotFound, "task-missing", "", "")
}

func TestResolveTaskSpecDependenciesTaskBaseSHAMismatch(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())

	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-dep",
		RequiredState: string(domain.StateAccepted),
		BaseSHA:       strings.Repeat("f", 40),
	}})
	assertTaskDependencyError(t, err, ErrDependencyBaseMismatch, "task-dep", "run-task-new", "baseSha")
}

func TestResolveTaskSpecDependenciesTaskSpecDigestMismatch(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())

	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          domain.DependencyKindTask,
		TaskID:        "task-dep",
		RequiredState: string(domain.StateAccepted),
		SpecDigest:    "sha256:" + strings.Repeat("e", 64),
	}})
	assertTaskDependencyError(t, err, ErrDependencyDigestMismatch, "task-dep", "run-task-new", "specDigest")
}

func TestResolveTaskSpecDependenciesMixedAndUnknownKind(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())

	// A satisfied mixed run- and task-scoped list resolves in order.
	err := resolveTaskSpecDependencies(root, store, []domain.TaskDependency{
		{Kind: domain.DependencyKindRun, RunID: "run-dep-accepted", RequiredState: string(domain.StateAccepted)},
		{Kind: domain.DependencyKindTask, TaskID: "task-dep", RequiredState: string(domain.StateAccepted)},
	})
	if err != nil {
		t.Fatalf("resolveTaskSpecDependencies(mixed) = %v, want nil", err)
	}

	// The first failing entry determines the error.
	err = resolveTaskSpecDependencies(root, store, []domain.TaskDependency{
		{Kind: domain.DependencyKindRun, RunID: "run-missing", RequiredState: string(domain.StateAccepted)},
		{Kind: domain.DependencyKindTask, TaskID: "task-dep", RequiredState: string(domain.StateAccepted)},
	})
	assertDependencyError(t, err, ErrDependencyRunNotFound, "run-missing", "")

	// An unknown kind fails closed instead of silently passing.
	err = resolveTaskSpecDependencies(root, store, []domain.TaskDependency{{
		Kind:          "branch",
		RequiredState: string(domain.StateAccepted),
	}})
	if err == nil || !strings.Contains(err.Error(), ErrDependencyUnknownKind) || !strings.Contains(err.Error(), "kind=branch") {
		t.Fatalf("resolveTaskSpecDependencies(unknown kind) = %v, want %q naming kind=branch", err, ErrDependencyUnknownKind)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("unknown kind error %q is not permanent", err)
	}
}

func TestLatestRunForTaskSkipsForeignAndUnreadableRuns(t *testing.T) {
	sealedMigrationSkip(t)
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionTaskRun(t, store, "run-task-old", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(10, 0).UTC())
	seedAdmissionTaskRun(t, store, "run-task-new", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(20, 0).UTC())
	seedAdmissionTaskRun(t, store, "run-foreign", "task-other", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(30, 0).UTC())
	// A corrupt run that could be newer is skipped instead of failing the
	// whole scan, mirroring the dashboard traversal it is modeled on.
	seedAdmissionTaskRun(t, store, "run-task-corrupt", "task-dep", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest, time.Unix(40, 0).UTC())
	if err := os.WriteFile(filepath.Join(root, "runs", "run-task-corrupt", "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	state, runID, found, err := latestRunForTask(root, store, "task-dep")
	if err != nil || !found || runID != "run-task-new" || state.State != domain.StateAccepted {
		t.Fatalf("latestRunForTask() = (%+v, %q, %v, %v), want readable run-task-new", state, runID, found, err)
	}

	_, _, found, err = latestRunForTask(root, store, "task-nowhere")
	if err != nil || found {
		t.Fatalf("latestRunForTask(unknown task) = (%v, %v), want not found without error", found, err)
	}
}

func TestAdmitTaskSpecPreparedRejectedFailClosed(t *testing.T) {
	err := AdmitTaskSpec(context.Background(), t.TempDir(), t.TempDir(), domain.TaskSpec{
		Admission: domain.TaskAdmission{Status: domain.AdmissionStatusPrepared},
	})
	if err == nil || err.Error() != ErrAdmissionPrepared {
		t.Fatalf("AdmitTaskSpec(prepared) = %v, want fixed sentinel %q", err, ErrAdmissionPrepared)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("prepared admission error %q is not permanent", err)
	}
}

func TestAdmitTaskSpecZeroValuesImposeNoRestriction(t *testing.T) {
	// The three fields at their zero values reproduce the pre-issue-23
	// behavior exactly: no sentinel, no store access, no subprocess.
	if err := AdmitTaskSpec(context.Background(), t.TempDir(), t.TempDir(), domain.TaskSpec{}); err != nil {
		t.Fatalf("AdmitTaskSpec(zero values) = %v, want nil", err)
	}
	if err := AdmitTaskSpec(context.Background(), t.TempDir(), t.TempDir(), domain.TaskSpec{
		Admission: domain.TaskAdmission{Status: domain.AdmissionStatusExecutable},
	}); err != nil {
		t.Fatalf("AdmitTaskSpec(executable) = %v, want nil", err)
	}
}

func TestAdmitTaskSpecUnknownStatusFailsClosed(t *testing.T) {
	err := AdmitTaskSpec(context.Background(), t.TempDir(), t.TempDir(), domain.TaskSpec{
		Admission: domain.TaskAdmission{Status: "bogus"},
	})
	if err == nil || !strings.Contains(err.Error(), ErrAdmissionStatusClosed) || !strings.Contains(err.Error(), "status=bogus") {
		t.Fatalf("AdmitTaskSpec(unknown status) = %v, want %q naming status=bogus", err, ErrAdmissionStatusClosed)
	}
}

func requireShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available on this host")
	}
}

func requireShellAndSleep(t *testing.T) {
	t.Helper()
	requireShell(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available on this host")
	}
}

func TestRunPreconditionsSuccessHonorsCwd(t *testing.T) {
	requireShell(t)
	repositoryRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repositoryRoot, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "sub", "marker.txt"), []byte("marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preconditions := []domain.TaskPrecondition{
		{ID: "root-check", Argv: []string{"sh", "-c", "test ! -f marker.txt"}},
		{ID: "sub-check", Argv: []string{"sh", "-c", "test -f marker.txt"}, CWD: "sub"},
	}
	if err := runPreconditions(context.Background(), repositoryRoot, preconditions); err != nil {
		t.Fatalf("runPreconditions() = %v, want nil", err)
	}
}

func TestRunPreconditionsNonZeroExitFailsClosedWithTail(t *testing.T) {
	requireShell(t)
	sentinel := filepath.Join(t.TempDir(), "second-ran")
	preconditions := []domain.TaskPrecondition{
		{ID: "pre-fail", Argv: []string{"sh", "-c", "echo precondition-output-tail; exit 3"}},
		{ID: "pre-second", Argv: []string{"sh", "-c", "touch '" + sentinel + "'"}},
	}
	err := runPreconditions(context.Background(), t.TempDir(), preconditions)
	if err == nil {
		t.Fatal("runPreconditions() = nil, want fail-closed rejection")
	}
	message := err.Error()
	for _, token := range []string{ErrPreconditionFailed, "id=pre-fail", "exit=3", `tail="precondition-output-tail"`} {
		if !strings.Contains(message, token) {
			t.Fatalf("error %q missing %q", message, token)
		}
	}
	if !port.IsPermanent(err) {
		t.Fatalf("precondition failure %q is not permanent", message)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("a later precondition ran after the first failure: stat = %v", statErr)
	}
}

func TestRunPreconditionsTimeoutFailsClosed(t *testing.T) {
	requireShellAndSleep(t)
	started := time.Now()
	err := runPreconditions(context.Background(), t.TempDir(), []domain.TaskPrecondition{{
		ID:             "pre-slow",
		Argv:           []string{"sh", "-c", "sleep 5"},
		TimeoutSeconds: 1,
	}})
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), ErrPreconditionTimeout) || !strings.Contains(err.Error(), "id=pre-slow") {
		t.Fatalf("runPreconditions(slow) = %v, want %q naming id=pre-slow", err, ErrPreconditionTimeout)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("precondition timeout %q is not permanent", err)
	}
	if elapsed >= 4*time.Second {
		t.Fatalf("timeout rejection took %v, want the declared 1s bound to win", elapsed)
	}
}

func TestRunPreconditionsUnspawnableFailsClosed(t *testing.T) {
	preconditions := []domain.TaskPrecondition{{
		ID:   "pre-missing",
		Argv: []string{"marshal-no-such-binary-0f9c2e"},
	}}
	err := runPreconditions(context.Background(), t.TempDir(), preconditions)
	if err == nil || !strings.Contains(err.Error(), ErrPreconditionSpawn) || !strings.Contains(err.Error(), "id=pre-missing") {
		t.Fatalf("runPreconditions(unspawnable) = %v, want %q naming id=pre-missing", err, ErrPreconditionSpawn)
	}

	// An empty argv fails closed with the same sentinel instead of panicking.
	err = runPreconditions(context.Background(), t.TempDir(), []domain.TaskPrecondition{{ID: "pre-empty"}})
	if err == nil || !strings.Contains(err.Error(), ErrPreconditionSpawn) || !strings.Contains(err.Error(), "id=pre-empty") {
		t.Fatalf("runPreconditions(empty argv) = %v, want %q naming id=pre-empty", err, ErrPreconditionSpawn)
	}
}
