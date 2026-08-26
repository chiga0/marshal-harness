// Package planning advances a not-yet-existing Run from CREATED through
// PLANNED to READY. It validates the frozen inputs, resolves the locked
// baseline, selects a Worker adapter under policy, creates the managed
// worktree, persists the frozen artifacts, and records replayable lifecycle
// events under a held Run lease. It performs no Worker execution, observation,
// publication, or credential handling.
package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// Failure errors that carry no dynamic values, so callers can compare and log
// them deterministically without leaking repository or credential content.
const (
	errRemoteURLMismatch         = "planning: resolved remote URL does not match TaskSpec expectedRemoteUrl"
	errCapabilityAdapterMismatch = "planning: selected capability snapshot adapterId does not match the selected adapter"
)

// Input carries everything planning needs to freeze a Run. Selector, Validator,
// Now and PythonSyntaxChecker are injectable so tests can drive deterministic
// behavior. A nil PythonSyntaxChecker selects the production checker that runs
// Marshal's fixed compile/ast.parse helper with the interpreter named by each
// supported inline acceptance command.
type Input struct {
	StateRoot           string
	RepositoryRoot      string
	RunID               string
	TaskSpec            []byte
	PolicySnapshot      []byte
	Selector            *adapter.Selector
	Validator           *contract.Validator
	Now                 time.Time
	PythonSyntaxChecker PythonSyntaxChecker
	LocalSelfIdentity   *selfidentity.LocalSelfIdentityObservationV1
}

// Result reports the final RunState, the adapter actually selected, and the
// structured selection attempts in frozen candidate order.
type Result struct {
	State             domain.RunState            `json:"state"`
	Adapter           port.WorkerAdapter         `json:"-"`
	SelectionAttempts []adapter.SelectionAttempt `json:"selectionAttempts"`
}

// Plan freezes a new Run. It is fail-closed: any validation, resolution,
// selection, or persistence failure returns an error and never leaves a false
// READY. It validates the TaskSpec against its schema, decodes it, and fully
// validates the PolicySnapshot into the effective policy first; only then
// does it enforce the TaskSpec admission gate (admission.status, dependsOn,
// preconditions) and syntax-preflight the supported inline Python acceptance
// scripts, before any repository resolution, adapter probe, worktree, run
// lease, journal, or frozen file side effect. An invalid,
// identity-mismatched, or prohibited policy can therefore never trigger a
// precondition or interpreter spawn, and an admission or preflight failure
// leaves no SelectionAttempts and no stateRoot or repository change.
// Before the first journal record is written, a later failure releases and
// removes the worktree and frozen artifacts it created. After the CREATED ->
// PLANNED record is journaled, a failure releases the worktree handle but
// leaves a diagnosable PLANNED state for reconciliation and never fabricates
// a rollback event. A successful READY also releases the worktree handle so
// Execution can acquire it immediately.
func Plan(ctx context.Context, input Input) (result Result, err error) {
	if input.Selector == nil {
		return Result{}, errors.New("planning: selector is required")
	}
	if input.Validator == nil {
		return Result{}, errors.New("planning: validator is required")
	}
	if err := domain.ValidateID(input.RunID); err != nil {
		return Result{}, fmt.Errorf("planning: invalid run ID: %w", err)
	}
	if len(bytes.TrimSpace(input.TaskSpec)) == 0 {
		return Result{}, errors.New("planning: task spec is required")
	}
	if len(bytes.TrimSpace(input.PolicySnapshot)) == 0 {
		return Result{}, errors.New("planning: policy snapshot is required")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	// 1. Schema validation and decode of the TaskSpec.
	if err := input.Validator.Validate(domain.KindTask, input.TaskSpec); err != nil {
		return Result{}, fmt.Errorf("planning: invalid TaskSpec: %w", err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(input.TaskSpec, &task); err != nil {
		return Result{}, fmt.Errorf("planning: decode TaskSpec: %w", err)
	}

	// 2. Validate the PolicySnapshot against its schema and the frozen task,
	// yielding the effective policy the Selector must honor. This completes
	// before the admission gate and the syntax preflight, so an invalid,
	// identity-mismatched, or prohibited policy can never trigger a
	// precondition or host interpreter spawn.
	effective, err := ValidatePolicy(input.PolicySnapshot, task, input.RunID, input.Validator)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocalDogfoodBinding(effective.EnvironmentBinding, input.LocalSelfIdentity); err != nil {
		return Result{}, err
	}
	if err := validateLocalDogfoodSurface(effective, task, input.LocalSelfIdentity); err != nil {
		return Result{}, err
	}

	// 3. TaskSpec admission gate (issue #23): a prepared admission is
	// rejected fail-closed, every dependsOn entry resolves read-only against
	// the run store, and every precondition executes as a controlled
	// subprocess at the repository root. The gate runs after full policy
	// validation and before the syntax preflight, any repository resolution,
	// adapter probe, worktree, run lease, journal, or frozen file side
	// effect, so a prohibited policy can never trigger a precondition spawn
	// and a failing admission leaves no state behind.
	if err := AdmitTaskSpec(ctx, input.StateRoot, input.RepositoryRoot, task); err != nil {
		return Result{}, err
	}

	// 4. Syntax-preflight the supported inline Python acceptance scripts.
	// This runs after TaskSpec schema validation, decode, full policy
	// validation, and the admission gate, and before any repository
	// resolution, adapter probe, worktree, run lease, journal, or frozen
	// file side effect. Unknown interpreters and unsupported forms are
	// skipped for independent Verification; supported forms that cannot be
	// checked fail closed without side effects.
	syntaxChecker := input.PythonSyntaxChecker
	if syntaxChecker == nil {
		syntaxChecker = execPythonSyntaxChecker{}
	}
	if err := preflightAcceptanceCommands(ctx, task.Acceptance.Commands, syntaxChecker); err != nil {
		return Result{}, err
	}

	// 5. Canonical repository path must match the TaskSpec repository.
	repository, err := gitworktree.OpenContext(ctx, input.RepositoryRoot)
	if err != nil {
		return Result{}, fmt.Errorf("planning: open repository: %w", err)
	}
	taskRepository, err := canonicalPath(task.Repository.Path)
	if err != nil {
		return Result{}, fmt.Errorf("planning: canonicalize TaskSpec repository path: %w", err)
	}
	if taskRepository != repository.Root {
		return Result{}, errors.New("planning: TaskSpec repository does not match active repository")
	}

	// 6. Resolve the base ref to a unique commit SHA.
	baseSHA, err := ResolveBase(ctx, repository.Root, task.Repository.BaseRef)
	if err != nil {
		return Result{}, fmt.Errorf("planning: %w", err)
	}

	// 7. Confirm the remote name and, when declared, the exact remote URL.
	// Resolved URLs may carry credentials, so a mismatch is reported as a
	// fixed error that never echoes either URL.
	resolvedURL, err := ResolveRemote(ctx, repository.Root, task.Repository.Remote)
	if err != nil {
		return Result{}, fmt.Errorf("planning: %w", err)
	}
	if task.Repository.ExpectedRemoteURL != "" && resolvedURL != task.Repository.ExpectedRemoteURL {
		return Result{}, errors.New(errRemoteURLMismatch)
	}

	// 8. Select the adapter strictly from the effective policy's explicit
	// candidate list; when fallback is not allowed it carries none.
	selection, err := input.Selector.Select(ctx, effective.SelectionRequest())
	if err != nil {
		return Result{SelectionAttempts: selection.Attempts}, fmt.Errorf("planning: select adapter: %w", err)
	}
	if selection.Adapter == nil {
		return Result{SelectionAttempts: selection.Attempts}, errors.New("planning: no adapter was selected")
	}

	// 9. The selected CapabilitySnapshot must pass the schema again and the
	// provider-neutral capability gate, and its adapterId must exactly match
	// the selected adapter.
	if err := input.Validator.Validate(domain.KindCapabilitySnapshot, selection.Capability.Data); err != nil {
		return Result{SelectionAttempts: selection.Attempts}, fmt.Errorf("planning: selected capability snapshot failed schema: %w", err)
	}
	adapterID, err := adapter.ValidateCapability(selection.Capability, task)
	if err != nil {
		return Result{SelectionAttempts: selection.Attempts}, fmt.Errorf("planning: %w", err)
	}
	if adapterID != selection.Adapter.ID() {
		return Result{SelectionAttempts: selection.Attempts}, errors.New(errCapabilityAdapterMismatch)
	}
	if err := validateLocalDogfoodCapabilityAuthority(selection.Capability.Data, input.LocalSelfIdentity); err != nil {
		return Result{SelectionAttempts: selection.Attempts}, err
	}

	// 10. Canonicalize the three frozen artifacts and compute their digests.
	// The policy digest covers the whole frozen policy document, not the
	// embedded policyDigest field.
	taskCanonical, err := canonical.JSON(input.TaskSpec)
	if err != nil {
		return Result{}, fmt.Errorf("planning: canonicalize TaskSpec: %w", err)
	}
	policyCanonical, err := canonical.JSON(input.PolicySnapshot)
	if err != nil {
		return Result{}, fmt.Errorf("planning: canonicalize PolicySnapshot: %w", err)
	}
	capabilityCanonical, err := canonical.JSON(selection.Capability.Data)
	if err != nil {
		return Result{}, fmt.Errorf("planning: canonicalize CapabilitySnapshot: %w", err)
	}
	specDigest := canonical.DigestBytes(taskCanonical)
	policyDigest := canonical.DigestBytes(policyCanonical)
	capabilityDigest := canonical.DigestBytes(capabilityCanonical)

	// 11. Acquire the Run lease and refuse to plan over an existing Run.
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("planning: acquire run lease: %w", err)
	}
	defer func() { _ = lease.Release() }()
	exists, err := runExists(store, input.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("planning: inspect existing run: %w", err)
	}
	if exists {
		return Result{}, fmt.Errorf("planning: run %q already exists", input.RunID)
	}

	// From here on, side effects are cleaned up unless the CREATED -> PLANNED
	// journal record has been written (committed), after which a diagnosable
	// PLANNED state is intentionally left for reconciliation. The worktree
	// handle is released on every path so its flock never leaks: on cleanup
	// it is released inside removeCreatedWorktree, on committed failure and
	// on success it is released here so Execution can acquire it.
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	var worktree *gitworktree.Worktree
	committed := false
	defer func() {
		if worktree == nil {
			return
		}
		if committed {
			_ = worktree.Release()
			return
		}
		var cleanupErrs []error
		if removeErr := removeCreatedWorktree(repository, worktree); removeErr != nil {
			cleanupErrs = append(cleanupErrs, removeErr)
		}
		removeFrozenFiles(runDir)
		if len(cleanupErrs) > 0 && err != nil {
			err = errors.Join(append([]error{err}, cleanupErrs...)...)
		}
	}()

	// 12. Create the managed linked worktree locked at the base SHA, keyed by
	// (taskId, runId) so a retried taskId plans onto a fresh worktree instead
	// of colliding with the previous run's directory and branch.
	worktree, err = repository.CreateForRun(input.StateRoot, task.Metadata.ID, input.RunID, baseSHA)
	if err != nil {
		return Result{}, fmt.Errorf("planning: create worktree: %w", err)
	}

	// 13. Freeze the canonical artifacts with owner-only permissions.
	if err := atomicWrite(filepath.Join(runDir, "task-spec.json"), taskCanonical); err != nil {
		return Result{}, fmt.Errorf("planning: freeze TaskSpec: %w", err)
	}
	if err := atomicWrite(filepath.Join(runDir, "policy-snapshot.json"), policyCanonical); err != nil {
		return Result{}, fmt.Errorf("planning: freeze PolicySnapshot: %w", err)
	}
	if err := atomicWrite(filepath.Join(runDir, "capability-snapshot.json"), capabilityCanonical); err != nil {
		return Result{}, fmt.Errorf("planning: freeze CapabilitySnapshot: %w", err)
	}

	// 14. CREATED -> PLANNED under the held lease.
	state := domain.NewRunState(task.Metadata.ID, input.RunID, now)
	state.SpecDigest = specDigest
	state.PolicyDigest = policyDigest
	plannedEvent, plannedState, err := transition(state, "planning.spec-accepted", domain.StatePlanned, now, map[string]any{
		"specDigest":       specDigest,
		"executionProfile": task.Worker.ExecutionProfile,
		"sessionPolicy":    task.Worker.SessionPolicy,
	}, lifecycle.Guard{LeaseHeld: true, DraftValid: true})
	if err != nil {
		return Result{}, fmt.Errorf("planning: build planned transition: %w", err)
	}
	if err := store.Append(lease, plannedEvent, state.Sequence); err != nil {
		return Result{}, fmt.Errorf("planning: append planned event: %w", err)
	}
	committed = true
	if err := store.WriteSnapshot(lease, plannedState); err != nil {
		return Result{}, fmt.Errorf("planning: write planned snapshot: %w", err)
	}

	// 15. PLANNED -> READY with the resolved baseline and frozen inputs.
	// M8 embedded vertical slice: record the frozen two-dimensional sandbox
	// requirements derived from the legacy execution profile into the READY
	// freeze event, on top of the issue-23 admission gate. The mapping is the
	// single compatibility direction of domain.SandboxRequirementsFromLegacy
	// and fails closed on an unknown profile; the existing planning
	// validations are unchanged.
	sandboxRequirements, err := domain.SandboxRequirementsFromLegacy(task.Worker.ExecutionProfile)
	if err != nil {
		return Result{}, fmt.Errorf("planning: %w", err)
	}
	readyPayload := map[string]any{
		"adapterId":         selection.Adapter.ID(),
		"baseSha":           baseSHA,
		"specDigest":        specDigest,
		"policyDigest":      policyDigest,
		"capabilityDigest":  capabilityDigest,
		"worktreePath":      worktree.Path,
		"branch":            worktree.Branch,
		"fallbackAllowed":   effective.AllowFallbackWorkers,
		"selectionAttempts": selectionAttemptPayload(selection.Attempts),
		"sandboxRequirements": map[string]any{
			"accessMode":            string(sandboxRequirements.AccessMode),
			"minimumAssuranceLevel": string(sandboxRequirements.MinimumAssuranceLevel),
		},
	}
	readyEvent, readyState, err := transition(plannedState, "planning.inputs-frozen", domain.StateReady, now, readyPayload, lifecycle.Guard{
		LeaseHeld:     true,
		BaseResolved:  true,
		PolicyAllowed: true,
		AdapterProbed: true,
		InputsFrozen:  true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("planning: build ready transition: %w", err)
	}
	readyState.CapabilityDigest = capabilityDigest
	readyState.BaseSHA = baseSHA
	readyState.WorktreePath = worktree.Path
	if err := store.Append(lease, readyEvent, plannedState.Sequence); err != nil {
		return Result{}, fmt.Errorf("planning: append ready event: %w", err)
	}
	if err := store.WriteSnapshot(lease, readyState); err != nil {
		return Result{}, fmt.Errorf("planning: write ready snapshot: %w", err)
	}

	return Result{State: readyState, Adapter: selection.Adapter, SelectionAttempts: selection.Attempts}, nil
}

// validateLocalDogfoodCapabilityAuthority binds the selected adapter fact to
// the same ordinary-user claim as the frozen policy. It runs after schema and
// provider-neutral capability validation but before worktree, lease, journal,
// or frozen-artifact side effects. Strict/conformance evidence cannot be
// reinterpreted as a Darwin ordinary-user capability.
func validateLocalDogfoodCapabilityAuthority(data []byte, observation *selfidentity.LocalSelfIdentityObservationV1) error {
	if observation == nil {
		return nil
	}
	var capability struct {
		AuthorityMode                  string          `json:"authorityMode"`
		ConformanceEvidenceDigest      string          `json:"conformanceEvidenceDigest"`
		ConformanceTrustRootKeyID      string          `json:"conformanceTrustRootKeyId"`
		ConformanceProbeProfileDigest  string          `json:"conformanceProbeProfileDigest"`
		ConformanceValidUntil          string          `json:"conformanceValidUntil"`
		ConformanceHostFingerprint     string          `json:"conformanceHostFingerprint"`
		ConformanceAuthorityGeneration uint64          `json:"conformanceAuthorityGeneration"`
		CodexAuthority                 json.RawMessage `json:"codexAuthority"`
	}
	if err := json.Unmarshal(data, &capability); err != nil || capability.AuthorityMode != "ordinary-user" ||
		capability.ConformanceEvidenceDigest != "" || capability.ConformanceTrustRootKeyID != "" ||
		capability.ConformanceProbeProfileDigest != "" || capability.ConformanceValidUntil != "" ||
		capability.ConformanceHostFingerprint != "" || capability.ConformanceAuthorityGeneration != 0 ||
		len(capability.CodexAuthority) != 0 {
		return port.Permanentf("%s", ErrPolicyLocalCapabilityAuthority)
	}
	return nil
}

// runExists reports whether the Run already has a journal or snapshot.
func runExists(store *runstore.Store, runID string) (bool, error) {
	events, _, err := store.ReadEvents(runID)
	switch {
	case err == nil:
		if len(events) > 0 {
			return true, nil
		}
	case errors.Is(err, os.ErrNotExist):
		// no journal yet
	case errors.Is(err, runstore.ErrTruncatedTail):
		return true, nil
	default:
		return false, err
	}
	if _, snapshotErr := store.ReadSnapshot(runID); snapshotErr == nil {
		return true, nil
	} else if !errors.Is(snapshotErr, os.ErrNotExist) {
		return false, snapshotErr
	}
	return false, nil
}

// transition builds a lifecycle event and reduces it under the supplied guard.
func transition(state domain.RunState, eventType string, target domain.State, at time.Time, payload map[string]any, guard lifecycle.Guard) (domain.RunEvent, domain.RunState, error) {
	eventID, err := domain.NewID("event")
	if err != nil {
		return domain.RunEvent{}, state, err
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindRunEvent,
		EventID:    eventID,
		RunID:      state.RunID,
		Sequence:   state.Sequence + 1,
		Type:       eventType,
		StateFrom:  state.State,
		StateTo:    target,
		Timestamp:  at,
		Actor:      &domain.Actor{Type: "system", ID: "marshal-planning"},
		Payload:    payload,
	}
	next, err := lifecycle.Reduce(state, event, guard)
	return event, next, err
}

// selectionAttemptPayload converts attempts into structured, provider-neutral
// maps that never carry environment or stderr content.
func selectionAttemptPayload(attempts []adapter.SelectionAttempt) []map[string]string {
	payload := make([]map[string]string, 0, len(attempts))
	for _, attempt := range attempts {
		payload = append(payload, map[string]string{"adapterId": attempt.AdapterID, "outcome": attempt.Outcome})
	}
	return payload
}

// removeCreatedWorktree releases and removes a worktree this service created.
// It only ever targets the worktree and branch returned by the Create call.
func removeCreatedWorktree(repository gitworktree.Repository, worktree *gitworktree.Worktree) error {
	if worktree == nil || repository.Root == "" {
		return nil
	}
	var result error
	if err := worktree.Release(); err != nil {
		result = errors.Join(result, err)
	}
	if err := gitRun(repository.Root, "worktree", "remove", "--force", worktree.Path); err != nil {
		result = errors.Join(result, err)
	}
	if err := gitRun(repository.Root, "branch", "-D", worktree.Branch); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

// removeFrozenFiles best-effort removes the frozen artifacts this service may
// have written before the journal boundary.
func removeFrozenFiles(runDir string) {
	for _, name := range []string{"task-spec.json", "policy-snapshot.json", "capability-snapshot.json"} {
		_ = os.Remove(filepath.Join(runDir, name))
	}
}

// atomicWrite writes data to path using a same-directory temp file, fsync and
// rename, with owner-only permissions. It never follows symlinks out of the
// target directory.
func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".marshal-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// canonicalPath resolves a path to its absolute, symlink-evaluated form.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

// gitRun executes a direct git argv for cleanup, discarding output.
func gitRun(repositoryRoot string, args ...string) error {
	command := exec.Command("git", append([]string{"-C", repositoryRoot}, args...)...)
	command.Env = gitEnvironment()
	return command.Run()
}

// gitEnvironment builds a restricted environment for git invocations, mirroring
// gitworktree: no system/global config, no prompts, hooks disabled.
func gitEnvironment() []string {
	environment := []string{
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
