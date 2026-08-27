package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// maxLogLines bounds the per-allocation observation log returned by
// Inspect, keeping the adjudication input bounded.
const maxLogLines = 32

// Deterministic derivation seeds of the frozen Local provider records; the
// two-part concatenation keeps every Digest-family value gitleaks-safe.
var (
	localCapabilitySnapshotDigest = sandbox.RecomputeSHA256([]byte("local-provider" + "-capability-snapshot"))
	localConfigDigest             = sandbox.RecomputeSHA256([]byte("local-provider" + "-effective-config"))
	localPolicyDigest             = sandbox.RecomputeSHA256([]byte("local-provider" + "-policy"))
	localAuthorizationDigest      = sandbox.RecomputeSHA256([]byte("local-provider" + "-authorization"))
)

// Diagnostic is one fail-closed observation recorded by the runner: stale
// fencing, rejected handles, signal delivery attempts and drift.
type Diagnostic struct {
	At           time.Time
	Operation    string
	AllocationId string
	Reason       string
}

// Fault configures one deterministic fault injection of the Local runner;
// only the drop-response kind is supported (restore response loss).
type Fault struct {
	Operation string
	Kind      string
}

// FaultDropResponse drops the matched operation's response after the host
// side effect has been applied, simulating transport response loss.
const FaultDropResponse = "drop-response"

// allocation is the host-side state of one sandbox allocation.
type allocation struct {
	meta        sandbox.SandboxAllocation
	role        sandbox.WorkloadRole
	dir         string
	staged      map[string]string
	violations  []sandbox.BoundaryViolation
	spawnCount  int64
	log         []string
	exitCode    int
	checkpoints int64
	liveProcess *os.Process
	// workDirAllowlist records the closed ADR 0055 §1 binding declared at
	// Provision time: the cleaned absolute paths an Exec WorkingDir may
	// resolve to. Symlinks are re-evaluated on both sides at Exec time.
	workDirAllowlist []string
	// envAllowlist records the closed ADR 0055 §2 environment key
	// declaration granted at Provision time.
	envAllowlist []string
}

// LocalRunner implements sandbox.SandboxProvider as the ordinary host-process
// provider of ADR 0016 §4. Every receipt it returns is an observation of
// host state, never authority.
type LocalRunner struct {
	mu           sync.Mutex
	root         string
	now          func() time.Time
	executor     CommandExecutor
	execTimeout  time.Duration
	faults       []Fault
	allocations  map[string]*allocation
	leases       map[string]dispatch.DispatchLease
	intents      []authority.SideEffectIntent
	reconcileLog []authority.ReconcileRecord
	diagnostics  []Diagnostic
}

// Option customizes a LocalRunner at construction time.
type Option func(*LocalRunner)

// WithExecutor injects the command execution seam; tests substitute a
// deterministic executor with a restricted argv contract.
func WithExecutor(executor CommandExecutor) Option {
	return func(r *LocalRunner) { r.executor = executor }
}

// WithExecTimeout overrides the bounded execution window of Exec.
func WithExecTimeout(timeout time.Duration) Option {
	return func(r *LocalRunner) { r.execTimeout = timeout }
}

// WithFaults appends deterministic fault injections to the runner.
func WithFaults(faults ...Fault) Option {
	return func(r *LocalRunner) { r.faults = append(r.faults, faults...) }
}

// NewLocalRunner constructs the Local provider over one sandbox root
// directory (test semantics: t.TempDir; production semantics: a Git-ignored
// directory under .marshal/, never created by this package's tests) with an
// injected clock and optional executor. No random source participates.
func NewLocalRunner(root string, now func() time.Time, options ...Option) (*LocalRunner, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: the sandbox root directory must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if now == nil {
		return nil, fmt.Errorf("%w: the injected clock must not be nil", sandbox.ErrInvalidRequest)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("local: sandbox root: %w", err)
	}
	runner := &LocalRunner{
		root:        root,
		now:         now,
		executor:    HostExecutor,
		execTimeout: defaultExecTimeout,
		allocations: map[string]*allocation{},
		leases:      map[string]dispatch.DispatchLease{},
	}
	for _, option := range options {
		option(runner)
	}
	if runner.executor == nil {
		runner.executor = HostExecutor
	}
	return runner, nil
}

func scopeKey(runId, attemptId string) string {
	return runId + "\x00" + attemptId
}

func (r *LocalRunner) appendDiagnostic(operation, allocationId, reason string) {
	r.diagnostics = append(r.diagnostics, Diagnostic{At: r.now().UTC(), Operation: operation, AllocationId: allocationId, Reason: reason})
}

// Diagnostics returns a copy of the recorded fail-closed diagnostics.
func (r *LocalRunner) Diagnostics() []Diagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Diagnostic(nil), r.diagnostics...)
}

// Intents returns a copy of the SideEffectIntent records registered by the
// runner; they are observations for M9 wiring, never an authority ledger.
func (r *LocalRunner) Intents() []authority.SideEffectIntent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]authority.SideEffectIntent(nil), r.intents...)
}

// AllocationDirectory exposes the host directory of one allocation for
// out-of-band observation.
func (r *LocalRunner) AllocationDirectory(allocationId string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.allocations[allocationId]
	if !ok {
		return "", fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, allocationId)
	}
	return entry.dir, nil
}

// enterOperation validates the operation identity fail closed before any
// side effect; fenced operations additionally replay-validate against the
// scope's current dispatch lease and record a diagnostic on refusal.
func (r *LocalRunner) enterOperation(operation string, identity sandbox.OperationIdentity, fenced bool) error {
	if err := identity.Validate(); err != nil {
		r.appendDiagnostic(operation, identity.AllocationId, "operation identity rejected: "+err.Error())
		return err
	}
	if !fenced {
		return nil
	}
	lease, ok := r.leases[scopeKey(identity.RunId, identity.AttemptId)]
	if !ok {
		r.appendDiagnostic(operation, identity.AllocationId, "no dispatch lease is bound to the scope")
		return fmt.Errorf("%w: no dispatch lease is bound to the scope", sandbox.ErrAllocationNotFound)
	}
	if err := identity.ValidateFencing(lease); err != nil {
		r.appendDiagnostic(operation, identity.AllocationId, "fencing rejected the operation identity: "+err.Error())
		return err
	}
	return nil
}

func (r *LocalRunner) matchFault(operation string) (Fault, bool) {
	for _, fault := range r.faults {
		if fault.Operation == operation {
			return fault, true
		}
	}
	return Fault{}, false
}

// sealScopeLease mirrors the dispatch identity granted to the caller into a
// sealed DispatchLease: the fencingToken stays the caller's granted token,
// and the leaseDigest binds it canonically so ValidateLeaseFencing can
// adjudicate every later replay. Deterministic: only the injected clock
// participates.
func (r *LocalRunner) sealScopeLease(identity sandbox.OperationIdentity, allocationId string) (dispatch.DispatchLease, error) {
	now := r.now().UTC()
	lease := dispatch.DispatchLease{
		LeaseId: sandbox.RecomputeSHA256([]byte("local-lease" + "\x00" + identity.RunId + "\x00" + identity.AttemptId + "\x00" + allocationId)),
		AuthorityNamespaceId: authority.AuthorityNamespaceId{
			TenantNamespace:  "marshal-harness",
			ControlPlaneId:   "local-sandbox-provider",
			AuthorityScopeId: identity.RunId + "\x00" + identity.AttemptId,
		},
		SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace:   "marshal-harness",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "host-process",
		},
		RegistrationId:                   "local-runner-registration",
		ProviderCapabilitySnapshotDigest: localCapabilitySnapshotDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "local-runner-instance",
			ConfigDigest:       localConfigDigest,
			TrustRootKeyId:     "local-trust-root" + "-key",
			TrustRootAlgorithm: "ed25519",
		},
		TaskId:        identity.TaskId,
		RunId:         identity.RunId,
		AttemptId:     identity.AttemptId,
		AllocationId:  allocationId,
		Generation:    identity.Generation,
		FencingToken:  identity.FencingToken,
		AckDeadlineAt: now.Add(30 * time.Minute).Format(time.RFC3339),
		ExpiresAt:     now.Add(24 * time.Hour).Format(time.RFC3339),
		LeaseState:    dispatch.LeaseStateActive,
		CreatedAt:     now.Format(time.RFC3339),
	}
	digest, err := lease.Digest()
	if err != nil {
		return dispatch.DispatchLease{}, fmt.Errorf("local: seal scope lease: %w", err)
	}
	lease.LeaseDigest = digest
	return lease, nil
}

// recordIntent registers one SideEffectIntent observation for the operation.
// The record is constructed in memory only: the Local provider never writes
// an authority ledger (runtime wiring is deferred to M9).
func (r *LocalRunner) recordIntent(identity sandbox.OperationIdentity, operation string, class authority.DispositionClass, targetRef string) {
	replayKey, err := identity.ReplayKey()
	if err != nil {
		replayKey = sandbox.RecomputeSHA256([]byte("local-replay" + "\x00" + identity.CommandId))
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		raw = []byte(identity.CommandId)
	}
	intent := authority.SideEffectIntent{
		AuthorityNamespaceId: authority.AuthorityNamespaceId{
			TenantNamespace:  "marshal-harness",
			ControlPlaneId:   "local-sandbox-provider",
			AuthorityScopeId: identity.RunId + "\x00" + identity.AttemptId,
		},
		EffectId:            sandbox.RecomputeSHA256([]byte("local-effect" + "\x00" + operation + "\x00" + identity.AllocationId + "\x00" + identity.CommandId)),
		OwnerIdentity:       "local-runner",
		Port:                "sandbox",
		Operation:           operation,
		TargetRef:           targetRef,
		TargetDigest:        sandbox.RecomputeSHA256([]byte(targetRef)),
		RequestDigest:       sandbox.RecomputeSHA256(raw),
		CommandId:           identity.CommandId,
		IdempotencyKey:      replayKey,
		PolicyDigest:        localPolicyDigest,
		AuthorizationDigest: localAuthorizationDigest,
		Purpose:             "sandbox " + operation,
		DispositionClass:    class,
		Deadline:            r.now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}
	r.intents = append(r.intents, intent)
}

// Probe implements sandbox.SandboxProvider. The Local provider is never
// hardened and holds no conformance evidence: it supports every request up
// to its workspace-write assurance ceiling and reports hardened requests as
// unsupported rather than downgrading them.
func (r *LocalRunner) Probe(ctx context.Context, request sandbox.ProbeRequest) (*sandbox.ProbeReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationProbe, request.Identity, false); err != nil {
		return nil, err
	}
	if _, err := domain.ParseAccessMode(string(request.Requirements.AccessMode)); err != nil {
		return nil, fmt.Errorf("local: probe: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(request.Requirements.MinimumAssuranceLevel)); err != nil {
		return nil, fmt.Errorf("local: probe: %w", err)
	}
	supported := request.Requirements.MinimumAssuranceLevel != domain.AssuranceLevelHardened
	reason := "the local host-process provider serves every access mode at the workspace-write assurance ceiling"
	if !supported {
		reason = "the local provider is never hardened and holds no conformance evidence; hardened requests are refused fail closed"
	}
	return &sandbox.ProbeReport{
		Supported:              supported,
		Reason:                 reason,
		ConformanceEvidenceRef: "",
	}, nil
}

// Provision implements sandbox.SandboxProvider. The Local provider grants
// at most workspace-write assurance and refuses hardened requests fail
// closed; the allocation directory name is deterministically derived from
// the allocationId, and the single-active invariant is adjudicated through
// sandbox.CheckSingleActive.
func (r *LocalRunner) Provision(ctx context.Context, request sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationProvision, request.Identity, false); err != nil {
		return nil, err
	}
	if lease, exists := r.leases[scopeKey(request.Identity.RunId, request.Identity.AttemptId)]; exists {
		if err := request.Identity.ValidateFencing(lease); err != nil {
			r.appendDiagnostic(sandbox.OperationProvision, request.Identity.AllocationId, "fencing rejected the provision identity: "+err.Error())
			return nil, err
		}
	}
	if err := sandbox.CheckAssuranceGate(request.Requirements, ""); err != nil {
		r.appendDiagnostic(sandbox.OperationProvision, request.Identity.AllocationId, "assurance gate refused the request: "+err.Error())
		return nil, err
	}
	for _, storeId := range request.AllowedStoreIds {
		if err := validateStoreAliasShape(storeId); err != nil {
			return nil, fmt.Errorf("local: provision: %w", err)
		}
	}
	// ADR 0055 §1/§2: the optional envelope declarations are registered fail
	// closed before any host side effect. The working-root entries are
	// recorded cleaned; their symlink-resolved binding is adjudicated at
	// Exec time. Credential-semantic environment keys never register.
	if err := sandbox.ValidateWorkDirAllowlist(request.WorkDirAllowlist); err != nil {
		r.appendDiagnostic(sandbox.OperationProvision, request.Identity.AllocationId, "workdir allowlist rejected: "+err.Error())
		return nil, err
	}
	if err := sandbox.ValidateEnvironmentAllowlist(request.EnvironmentAllowlist); err != nil {
		r.appendDiagnostic(sandbox.OperationProvision, request.Identity.AllocationId, "environment allowlist rejected: "+err.Error())
		return nil, err
	}
	workDirAllowlist := make([]string, 0, len(request.WorkDirAllowlist))
	for _, declared := range request.WorkDirAllowlist {
		workDirAllowlist = append(workDirAllowlist, filepath.Clean(declared))
	}
	candidate := sandbox.SandboxAllocation{
		AllocationId:           request.Identity.AllocationId,
		RunId:                  request.Identity.RunId,
		AttemptId:              request.Identity.AttemptId,
		Generation:             request.Identity.Generation,
		State:                  sandbox.AllocationActive,
		AccessMode:             request.Requirements.AccessMode,
		AssuranceLevel:         domain.AssuranceLevelWorkspaceWrite,
		ConformanceEvidenceRef: "",
		AllowedStoreIds:        append([]string(nil), request.AllowedStoreIds...),
		WorkDirAllowlist:       append([]string(nil), request.WorkDirAllowlist...),
		EnvironmentAllowlist:   append([]string(nil), request.EnvironmentAllowlist...),
	}
	if err := sandbox.CheckSingleActive(r.allocationsInScope(candidate.RunId, candidate.AttemptId), candidate); err != nil {
		r.appendDiagnostic(sandbox.OperationProvision, candidate.AllocationId, "single-active check rejected the provision: "+err.Error())
		return nil, err
	}
	dir := filepath.Join(r.root, "allocations", allocationDirName(candidate.AllocationId))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("local: provision: allocation directory: %w", err)
	}
	r.allocations[candidate.AllocationId] = &allocation{
		meta:             candidate,
		role:             request.Identity.WorkloadRole,
		dir:              dir,
		staged:           map[string]string{},
		workDirAllowlist: workDirAllowlist,
		envAllowlist:     append([]string(nil), request.EnvironmentAllowlist...),
	}
	lease, err := r.sealScopeLease(request.Identity, candidate.AllocationId)
	if err != nil {
		delete(r.allocations, candidate.AllocationId)
		_ = os.RemoveAll(dir)
		return nil, err
	}
	r.leases[scopeKey(candidate.RunId, candidate.AttemptId)] = lease
	r.recordIntent(request.Identity, sandbox.OperationProvision, authority.DispositionClassSandboxProvision, candidate.AllocationId)
	return &sandbox.ProvisionReceipt{Allocation: candidate}, nil
}

// allocationDirName derives the allocation directory name deterministically
// from the opaque allocationId so any locator shape stays filesystem-safe.
func allocationDirName(allocationId string) string {
	digest := sandbox.RecomputeSHA256([]byte(allocationId))
	return "alloc-" + strings.TrimPrefix(digest, sandbox.DigestPrefix)
}

// resolveAllocation enforces the dispatch-bound bindings fail closed: the
// identity must bind the addressed locator and the allocation's workload
// role, and must carry the allocation's current generation, so a stale
// handle presented after a restore is rejected.
func (r *LocalRunner) resolveAllocation(operation string, identity sandbox.OperationIdentity, allocationId string, requireActive bool) (*allocation, error) {
	if strings.TrimSpace(allocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if identity.AllocationId != allocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, identity.AllocationId, allocationId)
	}
	entry, ok := r.allocations[allocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, allocationId)
	}
	if identity.WorkloadRole != entry.role {
		r.appendDiagnostic(operation, allocationId, fmt.Sprintf("workload role %q rejected: the allocation is bound to role %q", string(identity.WorkloadRole), string(entry.role)))
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected: the allocation is bound to workload role %q, not %q", sandbox.ErrInvalidRequest, string(entry.role), string(identity.WorkloadRole))
	}
	if requireActive && entry.meta.State != sandbox.AllocationActive {
		return nil, fmt.Errorf("%w: %q is %s", sandbox.ErrAllocationNotActive, allocationId, string(entry.meta.State))
	}
	if identity.Generation != entry.meta.Generation {
		r.appendDiagnostic(operation, allocationId, fmt.Sprintf("stale generation %d rejected: the allocation carries generation %d", identity.Generation, entry.meta.Generation))
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, identity.Generation, entry.meta.Generation)
	}
	return entry, nil
}

func (r *LocalRunner) allocationsInScope(runId, attemptId string) []sandbox.SandboxAllocation {
	var result []sandbox.SandboxAllocation
	for _, entry := range r.allocations {
		if entry.meta.RunId == runId && entry.meta.AttemptId == attemptId {
			result = append(result, entry.meta)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AllocationId < result[j].AllocationId
	})
	return result
}

func appendLog(entry *allocation, line string) {
	entry.log = append(entry.log, line)
	if len(entry.log) > maxLogLines {
		entry.log = entry.log[len(entry.log)-maxLogLines:]
	}
}

// Inspect implements sandbox.SandboxProvider and returns the real host
// observation of the allocation: lifecycle state, last exit code, recorded
// violations, spawn count, the bounded log and a directory content summary.
// It never mutates state.
func (r *LocalRunner) Inspect(ctx context.Context, request sandbox.InspectRequest) (*sandbox.InspectReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationInspect, request.Identity, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	entry, ok := r.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.WorkloadRole != entry.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	if request.Identity.Generation != entry.meta.Generation {
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, request.Identity.Generation, entry.meta.Generation)
	}
	logLines := append([]string(nil), entry.log...)
	if names, err := directoryListing(entry.dir); err == nil {
		for _, name := range names {
			if len(logLines) >= maxLogLines {
				break
			}
			logLines = append(logLines, "directory-entry: "+name)
		}
	}
	return &sandbox.InspectReport{
		State:      entry.meta.State,
		ExitCode:   entry.exitCode,
		Violations: append([]sandbox.BoundaryViolation(nil), entry.violations...),
		SpawnCount: entry.spawnCount,
		LogLines:   logLines,
	}, nil
}

// directoryListing returns the sorted names directly inside dir; a missing
// directory yields an error the caller treats as an empty observation.
func directoryListing(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Signal implements sandbox.SandboxProvider. Exec runs synchronously in the
// M8 embedded form, so a live workload process only exists while Exec holds
// the lock; a signal addressed to an allocation without a live process is
// observed as not delivered and recorded in the diagnostics.
func (r *LocalRunner) Signal(ctx context.Context, request sandbox.SignalRequest) (*sandbox.SignalReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationSignal, request.Identity, true); err != nil {
		return nil, err
	}
	if err := request.Signal.Validate(); err != nil {
		return nil, err
	}
	entry, err := r.resolveAllocation(sandbox.OperationSignal, request.Identity, request.AllocationId, true)
	if err != nil {
		return nil, err
	}
	if entry.liveProcess == nil {
		appendLog(entry, "signal not delivered: no live workload process for "+string(request.Signal))
		r.appendDiagnostic(sandbox.OperationSignal, request.AllocationId, "signal "+string(request.Signal)+" not delivered: no live workload process")
		return &sandbox.SignalReceipt{Delivered: false}, nil
	}
	signal, ok := hostSignal(request.Signal)
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrInvalidSignal, string(request.Signal))
	}
	delivered := entry.liveProcess.Signal(signal) == nil
	appendLog(entry, fmt.Sprintf("signal delivered=%t: %s", delivered, string(request.Signal)))
	r.appendDiagnostic(sandbox.OperationSignal, request.AllocationId, fmt.Sprintf("signal %s delivered=%t", string(request.Signal), delivered))
	return &sandbox.SignalReceipt{Delivered: delivered}, nil
}

// Checkpoint implements sandbox.SandboxProvider with a deterministic
// snapshot of the allocation directory: files are walked in sorted relative
// order, so identical content always yields the identical checkpoint digest.
func (r *LocalRunner) Checkpoint(ctx context.Context, request sandbox.CheckpointRequest) (*sandbox.CheckpointReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationCheckpoint, request.Identity, true); err != nil {
		return nil, err
	}
	entry, err := r.resolveAllocation(sandbox.OperationCheckpoint, request.Identity, request.AllocationId, true)
	if err != nil {
		return nil, err
	}
	files, err := collectFiles(entry.dir)
	if err != nil {
		return nil, fmt.Errorf("local: checkpoint: %w", err)
	}
	var payload []byte
	for _, file := range files {
		content, readErr := os.ReadFile(filepath.Join(entry.dir, filepath.FromSlash(file.rel)))
		if readErr != nil {
			return nil, fmt.Errorf("local: checkpoint: %w", readErr)
		}
		payload = append(payload, []byte(file.rel)...)
		payload = append(payload, 0)
		payload = append(payload, content...)
	}
	entry.checkpoints++
	receipt := &sandbox.CheckpointReceipt{
		CheckpointId: fmt.Sprintf("ckpt:%s:%d", entry.meta.AllocationId, entry.checkpoints),
		SHA256:       sandbox.RecomputeSHA256(payload),
		SizeBytes:    int64(len(payload)),
	}
	appendLog(entry, "checkpoint: "+receipt.CheckpointId)
	return receipt, nil
}

// stagedFile is one file observed under an allocation directory.
type stagedFile struct {
	rel string
}

// collectFiles walks dir recursively and returns every regular file's
// slash-separated relative path in sorted order.
func collectFiles(dir string) ([]stagedFile, error) {
	var files []stagedFile
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, stagedFile{rel: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

// Terminate implements sandbox.SandboxProvider and is idempotent on
// allocations that are already terminal. The live process (when one exists)
// is killed, the allocation directory is removed and a SideEffectIntent is
// registered for the transition.
func (r *LocalRunner) Terminate(ctx context.Context, request sandbox.TerminateRequest) (*sandbox.TerminateReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationTerminate, request.Identity, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	entry, ok := r.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.WorkloadRole != entry.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	if request.Identity.Generation != entry.meta.Generation {
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, request.Identity.Generation, entry.meta.Generation)
	}
	if entry.meta.State != sandbox.AllocationTerminated && entry.meta.State != sandbox.AllocationReplaced {
		if entry.liveProcess != nil {
			_ = entry.liveProcess.Kill()
			entry.liveProcess = nil
		}
		if err := os.RemoveAll(entry.dir); err != nil {
			return nil, fmt.Errorf("local: terminate: %w", err)
		}
		entry.meta.State = sandbox.AllocationTerminated
		appendLog(entry, "terminated")
		r.recordIntent(request.Identity, "allocation-terminate", authority.DispositionClassSandboxTerminate, request.AllocationId)
		return &sandbox.TerminateReceipt{State: entry.meta.State}, nil
	}
	_ = os.RemoveAll(entry.dir)
	return &sandbox.TerminateReceipt{State: entry.meta.State}, nil
}
