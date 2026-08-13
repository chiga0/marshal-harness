package planning

// TaskSpec admission (issue #23). Phase 1 landed the run-scoped dependsOn
// resolver; phase 2 adds the admission.status gate, task-scoped dependencies
// and the preconditions executor, all evaluated by Plan before any worktree,
// journal, or frozen file side effect.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Dependency resolution and admission errors are fixed category strings.
// Every reported error additionally names the failing runId or taskId and,
// when one applies, the failing field, so callers can locate the violated
// condition without inspecting run state themselves.
const (
	ErrDependencyRunNotFound    = "resolve run dependencies: depended-on run not found"
	ErrDependencyRunUnreadable  = "resolve run dependencies: depended-on run state is unreadable"
	ErrDependencyStateMismatch  = "resolve run dependencies: depended-on run state does not match the required state"
	ErrDependencyBaseMismatch   = "resolve run dependencies: depended-on run baseSha does not match"
	ErrDependencyDigestMismatch = "resolve run dependencies: depended-on run specDigest does not match"

	ErrDependencyUnknownKind    = "resolve dependencies: unknown dependsOn kind"
	ErrDependencyTaskNotFound   = "resolve task dependencies: no readable run found for the depended-on task"
	ErrDependencyTaskUnreadable = "resolve task dependencies: run state root is unreadable"

	ErrAdmissionPrepared     = "planning: TaskSpec admission.status is prepared: prepared-only declarations cannot be planned"
	ErrAdmissionStatusClosed = "planning: TaskSpec admission.status is outside the closed vocabulary prepared/executable"

	ErrPreconditionFailed  = "planning: precondition failed"
	ErrPreconditionTimeout = "planning: precondition timed out"
	ErrPreconditionSpawn   = "planning: precondition could not be started"
)

// maxPreconditionOutputBytes bounds the combined stdout+stderr captured for
// one precondition; only the tail of the bounded capture is ever reported.
const maxPreconditionOutputBytes = 64 * 1024

// preconditionTailBytes bounds how many trailing bytes of the captured
// output the failure error carries.
const preconditionTailBytes = 512

// preconditionDefaultTimeout bounds one precondition execution when the
// TaskSpec does not declare timeoutSeconds. It is a variable so tests can
// exercise the timeout path without waiting for a long real timeout.
var preconditionDefaultTimeout = 300 * time.Second

// RunDependency declares one run-scoped dependency. The depended-on run must
// exist and its inspected state must be exactly RequiredState; when BaseSHA
// or SpecDigest is non-empty, the inspected run must additionally carry
// exactly that frozen value. An empty BaseSHA or SpecDigest disables the
// corresponding check.
type RunDependency struct {
	RunID         string
	RequiredState domain.State
	BaseSHA       string
	SpecDigest    string
}

// ResolveRunDependencies resolves every dependency in order against the run
// store using the read-only, lease-free Inspect. It is a pure read-only
// resolver: it never acquires a lease, never appends an event, and never
// writes any state. Every failure is fail-closed and reports the fixed error
// category together with the failing runId and field: a missing or
// unreadable run, a state mismatch, or a baseSha or specDigest mismatch. An
// empty dependency list resolves successfully.
func ResolveRunDependencies(store *runstore.Store, deps []RunDependency) error {
	for _, dep := range deps {
		state, err := store.Inspect(dep.RunID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return port.Permanentf("%s: runId=%s", ErrDependencyRunNotFound, dep.RunID)
			}
			return port.Permanentf("%s: runId=%s", ErrDependencyRunUnreadable, dep.RunID)
		}
		if state.State != dep.RequiredState {
			return port.Permanentf("%s: runId=%s field=state", ErrDependencyStateMismatch, dep.RunID)
		}
		if dep.BaseSHA != "" && state.BaseSHA != dep.BaseSHA {
			return port.Permanentf("%s: runId=%s field=baseSha", ErrDependencyBaseMismatch, dep.RunID)
		}
		if dep.SpecDigest != "" && state.SpecDigest != dep.SpecDigest {
			return port.Permanentf("%s: runId=%s field=specDigest", ErrDependencyDigestMismatch, dep.RunID)
		}
	}
	return nil
}

// AdmitTaskSpec enforces the TaskSpec admission declarations before planning
// creates any side effect: a prepared admission is rejected fail-closed, an
// admission status outside the closed vocabulary is rejected fail-closed,
// every dependsOn entry resolves read-only against the run store, and every
// precondition must exit zero when executed as a controlled subprocess at
// the repository root. An absent admission declaration and empty dependsOn
// and preconditions lists impose no restriction, preserving the pre-issue-23
// behavior exactly.
func AdmitTaskSpec(ctx context.Context, stateRoot, repositoryRoot string, task domain.TaskSpec) error {
	switch task.Admission.Status {
	case "", domain.AdmissionStatusExecutable:
	case domain.AdmissionStatusPrepared:
		return port.Permanentf("%s", ErrAdmissionPrepared)
	default:
		return port.Permanentf("%s: status=%s", ErrAdmissionStatusClosed, task.Admission.Status)
	}
	store := runstore.New(stateRoot)
	if err := resolveTaskSpecDependencies(stateRoot, store, task.DependsOn); err != nil {
		return err
	}
	return runPreconditions(ctx, repositoryRoot, task.Preconditions)
}

// resolveTaskSpecDependencies resolves every dependsOn entry in declaration
// order: run-scoped entries reuse the phase-1 read-only resolver, task-
// scoped entries resolve the depended-on task's latest readable run first.
// An unknown kind fails closed instead of silently passing.
func resolveTaskSpecDependencies(stateRoot string, store *runstore.Store, dependencies []domain.TaskDependency) error {
	for _, dependency := range dependencies {
		switch dependency.Kind {
		case domain.DependencyKindRun:
			if err := ResolveRunDependencies(store, []RunDependency{{
				RunID:         dependency.RunID,
				RequiredState: domain.State(dependency.RequiredState),
				BaseSHA:       dependency.BaseSHA,
				SpecDigest:    dependency.SpecDigest,
			}}); err != nil {
				return err
			}
		case domain.DependencyKindTask:
			if err := resolveTaskDependency(stateRoot, store, dependency); err != nil {
				return err
			}
		default:
			return port.Permanentf("%s: kind=%s", ErrDependencyUnknownKind, dependency.Kind)
		}
	}
	return nil
}

// resolveTaskDependency resolves one task-scoped dependency against the
// depended-on task's latest readable run, then applies the same state,
// baseSha and specDigest checks as the run-scoped resolver. Errors name both
// the taskId and the resolved runId.
func resolveTaskDependency(stateRoot string, store *runstore.Store, dependency domain.TaskDependency) error {
	state, runID, found, err := latestRunForTask(stateRoot, store, dependency.TaskID)
	if err != nil {
		return port.Permanentf("%s: taskId=%s", ErrDependencyTaskUnreadable, dependency.TaskID)
	}
	if !found {
		return port.Permanentf("%s: taskId=%s", ErrDependencyTaskNotFound, dependency.TaskID)
	}
	if state.State != domain.State(dependency.RequiredState) {
		return port.Permanentf("%s: taskId=%s runId=%s field=state", ErrDependencyStateMismatch, dependency.TaskID, runID)
	}
	if dependency.BaseSHA != "" && state.BaseSHA != dependency.BaseSHA {
		return port.Permanentf("%s: taskId=%s runId=%s field=baseSha", ErrDependencyBaseMismatch, dependency.TaskID, runID)
	}
	if dependency.SpecDigest != "" && state.SpecDigest != dependency.SpecDigest {
		return port.Permanentf("%s: taskId=%s runId=%s field=specDigest", ErrDependencyDigestMismatch, dependency.TaskID, runID)
	}
	return nil
}

// latestRunForTask scans stateRoot/runs read-only and returns the inspected
// state of the most recent readable run whose taskId matches, ordered by the
// state UpdatedAt timestamp with the runId as a deterministic tie break. It
// mirrors the dashboard ListRuns traversal without importing the dashboard:
// unreadable run directories are skipped. It never acquires a lease and
// never writes any state.
func latestRunForTask(stateRoot string, store *runstore.Store, taskID string) (domain.RunState, string, bool, error) {
	runsDirectory := filepath.Join(stateRoot, "runs")
	entries, err := os.ReadDir(runsDirectory)
	if err != nil {
		if os.IsNotExist(err) {
			return domain.RunState{}, "", false, nil
		}
		return domain.RunState{}, "", false, err
	}
	var latest domain.RunState
	latestID := ""
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		if domain.ValidateID(runID) != nil {
			continue
		}
		state, err := store.Inspect(runID)
		if err != nil {
			continue
		}
		if state.TaskID != taskID {
			continue
		}
		if !found || state.UpdatedAt.After(latest.UpdatedAt) ||
			(state.UpdatedAt.Equal(latest.UpdatedAt) && runID > latestID) {
			latest = state
			latestID = runID
			found = true
		}
	}
	return latest, latestID, found, nil
}

// runPreconditions executes every precondition in declaration order at the
// repository root and fails closed on the first non-zero exit, timeout, or
// unspawnable command.
func runPreconditions(ctx context.Context, repositoryRoot string, preconditions []domain.TaskPrecondition) error {
	for _, precondition := range preconditions {
		if err := runPrecondition(ctx, repositoryRoot, precondition); err != nil {
			return err
		}
	}
	return nil
}

// runPrecondition executes one precondition as a controlled subprocess: argv
// is executed directly without a shell in its own process group, with a
// restricted environment, a bounded timeout, and bounded combined output
// capture. A non-zero exit fails closed with the fixed sentinel and the tail
// of the captured output; a timeout or an unspawnable command fail closed
// with their own fixed sentinels.
func runPrecondition(ctx context.Context, repositoryRoot string, precondition domain.TaskPrecondition) error {
	if len(precondition.Argv) == 0 {
		return port.Permanentf("%s: id=%s", ErrPreconditionSpawn, precondition.ID)
	}
	timeout := preconditionDefaultTimeout
	if precondition.TimeoutSeconds > 0 {
		timeout = time.Duration(precondition.TimeoutSeconds) * time.Second
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	directory := repositoryRoot
	if precondition.CWD != "" {
		directory = filepath.Join(repositoryRoot, precondition.CWD)
	}
	output := &limitedWriter{limit: maxPreconditionOutputBytes}
	runErr := runPreconditionCommand(ctx, directory, precondition.Argv, output)
	if parentErr := parent.Err(); parentErr != nil {
		return parentErr
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		return port.Permanentf("%s: id=%s", ErrPreconditionTimeout, precondition.ID)
	}
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return port.Permanentf("%s: id=%s exit=%d tail=%q", ErrPreconditionFailed, precondition.ID, exitErr.ExitCode(), preconditionOutputTail(output.buf))
	}
	return port.Permanentf("%s: id=%s", ErrPreconditionSpawn, precondition.ID)
}

// runPreconditionCommand runs argv directly at directory, never through a
// shell, with the restricted precondition environment and combined output
// capture. It mirrors runDirectCommand but additionally binds the working
// directory and captures stderr together with stdout.
func runPreconditionCommand(ctx context.Context, directory string, argv []string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = preconditionEnvironment()
	command.Stdout = output
	command.Stderr = output
	isolateCommandGroup(command)

	if err := command.Start(); err != nil {
		return err
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	select {
	case runErr := <-waitDone:
		return runErr
	case <-ctx.Done():
		killCommandTree(command)
		select {
		case <-waitDone:
		case <-time.After(commandReapBound):
		}
		return ctx.Err()
	}
}

// preconditionOutputTail returns at most the trailing preconditionTailBytes
// of the captured output with trailing line breaks removed, so the failure
// error carries a bounded, readable excerpt.
func preconditionOutputTail(output []byte) string {
	if len(output) > preconditionTailBytes {
		output = output[len(output)-preconditionTailBytes:]
	}
	return strings.TrimRight(string(output), "\r\n")
}

// preconditionEnvironment builds the restricted environment for precondition
// invocations, mirroring the git and syntax-preflight invocations: a stable
// locale and only PATH, HOME, and TMPDIR pass through, so no credentials or
// harness variables reach the precondition command.
func preconditionEnvironment() []string {
	environment := []string{
		"LC_ALL=C",
		"LANG=C",
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
