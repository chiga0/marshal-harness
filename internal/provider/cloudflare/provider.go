package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// Compile-time proof that the Bridge provider implements the ten-operation
// SPI of internal/sandbox/spi.go.
var _ sandbox.SandboxProvider = (*Provider)(nil)

// ProviderConfig configures the Cloudflare Bridge provider. The Bridge
// Bearer token is a transport credential only: it never substitutes for the
// fencingToken and never enters business JSON, events, logs, digests or
// error messages. Fencing/generation adjudication stays at the
// marshal-server authoritative write boundary; this provider only validates
// and passes through the OperationIdentity (internal/sandbox/identity.go).
type ProviderConfig struct {
	// BridgeBaseURL is the absolute URL of the self-deployed official
	// Bridge inside the user's Cloudflare account.
	BridgeBaseURL string
	// BridgeToken is the Bridge Bearer transport credential. Empty fails
	// closed at construction.
	BridgeToken string
	// ConformanceEvidenceRef is the valid suite-issued conformance evidence
	// digest the provider holds; empty means it holds none, so every
	// hardened request is refused fail closed and never downgraded.
	ConformanceEvidenceRef string
	// HTTPClient optionally injects the transport (tests); nil takes a
	// plain client.
	HTTPClient *http.Client
	// MaxRetries / RetryDelay / RequestTimeout mirror ClientConfig.
	MaxRetries     int
	RetryDelay     time.Duration
	RequestTimeout time.Duration
	// StateStore is the durable side-effect store. Nil selects an ephemeral
	// in-memory store (tests); production callers must supply a file-backed
	// store constructed with NewFileStateStore.
	StateStore *FileStateStore
	// LocatorResolver resolves a bound stage locator to its content bytes.
	// It is the provider's window onto the ArtifactStore; nil means locator
	// staging is refused with ErrLocatorUnresolved.
	LocatorResolver func(sandbox.Locator) ([]byte, error)
}

// Diagnostic is one fail-closed observation recorded by the provider.
// Diagnostic text never carries the transport credential.
type Diagnostic struct {
	Operation    string
	AllocationId string
	Reason       string
}

// checkpointRecord holds the raw tar snapshot the provider keeps in memory
// for a later hydrate. The tar is not durable: the official persist returns
// the snapshot to the caller, and durably storing it is the caller's
// (ArtifactStore) responsibility.
type checkpointRecord struct {
	tar []byte
}

// allocationEntry is the provider's bookkeeping of one allocation: the
// opaque SPI record, the workload role, the Bridge locator returned by the
// remote create, the interactive session id and the last checkpoint.
type allocationEntry struct {
	meta           sandbox.SandboxAllocation
	role           sandbox.WorkloadRole
	bridgeLocator  string
	sessionId      string
	lastCheckpoint *checkpointRecord
}

// Provider implements sandbox.SandboxProvider against the official Bridge
// HTTP API. Cloudflare-specific concepts (Durable Objects, R2, Workers
// bindings) never surface here: the Bridge-internal identity of a sandbox
// travels only as the opaque bridgeLocator mapping and the SPI allocationId,
// and Marshal Core never interprets them (ADR 0016 §4).
type Provider struct {
	client          *Client
	evidenceRef     string
	store           *FileStateStore
	locatorResolver func(sandbox.Locator) ([]byte, error)

	mu          sync.Mutex
	allocations map[string]*allocationEntry
	diagnostics []Diagnostic
}

// NewProvider validates the configuration fail closed and reconstructs the
// provider's bookkeeping from the durable store when one is supplied, so a
// re-opened provider converges with a crashed one.
func NewProvider(config ProviderConfig) (*Provider, error) {
	if strings.TrimSpace(config.BridgeBaseURL) == "" {
		return nil, fmt.Errorf("%w: the provider configuration must carry the bridge base URL", sandbox.ErrInvalidRequest)
	}
	credential, err := NewCredential(config.BridgeToken)
	if err != nil {
		return nil, err
	}
	if config.ConformanceEvidenceRef != "" {
		if err := requireEvidenceDigestShape(config.ConformanceEvidenceRef); err != nil {
			return nil, err
		}
	}
	client, err := NewClient(ClientConfig{
		BaseURL:        config.BridgeBaseURL,
		Credential:     credential,
		HTTPClient:     config.HTTPClient,
		MaxRetries:     config.MaxRetries,
		RetryDelay:     config.RetryDelay,
		RequestTimeout: config.RequestTimeout,
	})
	if err != nil {
		return nil, err
	}
	store := config.StateStore
	if store == nil {
		store = newMemoryStateStore()
	}
	provider := &Provider{
		client:          client,
		evidenceRef:     config.ConformanceEvidenceRef,
		store:           store,
		locatorResolver: config.LocatorResolver,
		allocations:     map[string]*allocationEntry{},
	}
	for _, record := range store.Allocations() {
		provider.allocations[record.Meta.AllocationId] = &allocationEntry{
			meta:          record.Meta,
			role:          record.Role,
			bridgeLocator: record.BridgeLocator,
			sessionId:     record.SessionId,
		}
	}
	return provider, nil
}

// Diagnostics returns a copy of the recorded fail-closed observations.
func (p *Provider) Diagnostics() []Diagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Diagnostic(nil), p.diagnostics...)
}

// Probe implements sandbox.SandboxProvider: one side-effect-free read of
// the Bridge health endpoint plus the Marshal-side evidence adjudication.
// The self-signed pass claim is never produced.
func (p *Provider) Probe(ctx context.Context, request sandbox.ProbeRequest) (*sandbox.ProbeReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationProbe, request.Identity); err != nil {
		return nil, err
	}
	if _, err := domain.ParseAccessMode(string(request.Requirements.AccessMode)); err != nil {
		return nil, fmt.Errorf("cloudflare: probe: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(request.Requirements.MinimumAssuranceLevel)); err != nil {
		return nil, fmt.Errorf("cloudflare: probe: %w", err)
	}
	if _, err := p.client.Health(ctx); err != nil {
		p.recordDiagnostic(sandbox.OperationProbe, request.Identity.AllocationId, "bridge health probe failed: "+err.Error())
		return nil, err
	}
	supported := true
	reason := "the bridge provider serves every closed access mode at the assurance level its conformance evidence supports"
	if request.Requirements.MinimumAssuranceLevel == domain.AssuranceLevelHardened && p.evidenceRef == "" {
		supported = false
		reason = "the bridge provider holds no conformance evidence; hardened requests are refused fail closed"
	}
	return &sandbox.ProbeReport{
		Supported:                  supported,
		Reason:                     reason,
		ConformanceEvidenceRef:     p.evidenceRef,
		SelfSignedConformanceClaim: false,
	}, nil
}

// Provision implements sandbox.SandboxProvider with the durable
// intent/outcome/locator discipline: a durable intent is written first, the
// remote create runs under its idempotency key, the Bridge locator is
// persisted immediately after the create succeeds, and the active allocation
// is installed atomically with the committed outcome. A crash at any write
// point converges on replay because the create is idempotent.
func (p *Provider) Provision(ctx context.Context, request sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationProvision, request.Identity); err != nil {
		return nil, err
	}
	if err := sandbox.CheckAssuranceGate(request.Requirements, p.evidenceRef); err != nil {
		p.recordDiagnostic(sandbox.OperationProvision, request.Identity.AllocationId, "assurance gate refused the request: "+err.Error())
		return nil, err
	}
	candidate := sandbox.SandboxAllocation{
		AllocationId:           request.Identity.AllocationId,
		RunId:                  request.Identity.RunId,
		AttemptId:              request.Identity.AttemptId,
		Generation:             request.Identity.Generation,
		State:                  sandbox.AllocationActive,
		AccessMode:             request.Requirements.AccessMode,
		AssuranceLevel:         request.Requirements.MinimumAssuranceLevel,
		ConformanceEvidenceRef: p.evidenceRef,
		AllowedStoreIds:        append([]string(nil), request.AllowedStoreIds...),
	}
	if err := sandbox.CheckSingleActive(p.allocationsInScope(candidate.RunId, candidate.AttemptId), candidate); err != nil {
		p.recordDiagnostic(sandbox.OperationProvision, candidate.AllocationId, "single-active check rejected the provision: "+err.Error())
		return nil, err
	}
	if entry, ok := p.allocations[candidate.AllocationId]; ok && entry.meta.Generation == candidate.Generation && entry.meta.State == sandbox.AllocationActive {
		// Idempotent replay of an already committed outcome.
		return &sandbox.ProvisionReceipt{Allocation: entry.meta}, nil
	}
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	intent := CreateIntent{
		ReplayKey:    replayKey,
		AllocationId: candidate.AllocationId,
		RunId:        candidate.RunId,
		AttemptId:    candidate.AttemptId,
		Generation:   candidate.Generation,
	}
	if err := p.store.RecordIntent(intent); err != nil {
		return nil, err
	}
	bridgeLocator, err := p.client.CreateSandbox(ctx, replayKey)
	if err != nil {
		// A definitive refusal (conflict, capacity, credential, semantic
		// 4xx) clears the intent; an exhausted retry budget is ambiguous —
		// the create may have happened — so the intent stays and reconcile
		// reports the ambiguity, never clean.
		if !ambiguousCreateError(err) {
			_ = p.store.ClearIntent(replayKey)
		}
		if errors.Is(err, ErrBridgeConflict) {
			err = fmt.Errorf("%w: the bridge observed a conflicting sandbox for %q", sandbox.ErrDuplicateActiveAllocation, candidate.AllocationId)
		}
		p.recordDiagnostic(sandbox.OperationProvision, candidate.AllocationId, "bridge create failed: "+err.Error())
		return nil, err
	}
	if err := p.store.RecordBridgeLocator(candidate.AllocationId, bridgeLocator); err != nil {
		return nil, err
	}
	if err := p.store.CommitCreateOutcome(CreateOutcome{
		ReplayKey:     replayKey,
		AllocationId:  candidate.AllocationId,
		BridgeLocator: bridgeLocator,
		Meta:          candidate,
		Role:          request.Identity.WorkloadRole,
	}); err != nil {
		return nil, err
	}
	p.allocations[candidate.AllocationId] = &allocationEntry{
		meta:          candidate,
		role:          request.Identity.WorkloadRole,
		bridgeLocator: bridgeLocator,
	}
	return &sandbox.ProvisionReceipt{Allocation: candidate}, nil
}

// Stage implements sandbox.SandboxProvider over the official raw-bytes file
// endpoint. The digest discipline is Marshal-side: the content is recomputed
// before the raw write, written, read back, and recomputed once more, so the
// receipt carries recomputed digests only, never an echo of the declared
// digest.
func (p *Provider) Stage(ctx context.Context, request sandbox.StageRequest) (*sandbox.StageReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationStage, request.Identity); err != nil {
		return nil, err
	}
	entry, err := p.resolveActive(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	if err := validateStageInputs(request.Inputs, entry.meta.AllowedStoreIds); err != nil {
		p.recordDiagnostic(sandbox.OperationStage, request.AllocationId, "stage request rejected: "+err.Error())
		return nil, err
	}
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	report := &sandbox.StageReport{Receipts: make([]sandbox.StageReceipt, 0, len(request.Inputs))}
	for _, input := range request.Inputs {
		receipt, stageErr := p.stageInput(ctx, entry, request.AllocationId, replayKey, input)
		if stageErr != nil {
			return nil, stageErr
		}
		report.Receipts = append(report.Receipts, *receipt)
	}
	return report, nil
}

func (p *Provider) stageInput(ctx context.Context, entry *allocationEntry, allocationId, replayKey string, input sandbox.StageInput) (*sandbox.StageReceipt, error) {
	path, err := stagedFilePath(input.InputId)
	if err != nil {
		p.recordDiagnostic(sandbox.OperationStage, allocationId, "stage target rejected: "+err.Error())
		return nil, err
	}
	var content []byte
	if input.Locator == nil {
		content = append([]byte(nil), input.Inline...)
	} else {
		if p.locatorResolver == nil {
			p.recordDiagnostic(sandbox.OperationStage, allocationId, "locator staging refused: no locator resolver is configured")
			return nil, fmt.Errorf("%w: input %q", sandbox.ErrLocatorUnresolved, input.InputId)
		}
		resolved, resolveErr := p.locatorResolver(*input.Locator)
		if resolveErr != nil {
			p.recordDiagnostic(sandbox.OperationStage, allocationId, "locator resolution failed for "+input.InputId)
			return nil, fmt.Errorf("%w: input %q", sandbox.ErrLocatorUnresolved, input.InputId)
		}
		content = append([]byte(nil), resolved...)
		if int64(len(content)) != input.Locator.SizeBytes {
			p.failAllocation(entry, "staged locator size disagreement for "+input.InputId)
			return nil, fmt.Errorf("cloudflare: stage input %q: the staged size disagrees with the locator", input.InputId)
		}
	}
	pre := sandbox.RecomputeSHA256(content)
	if pre != input.DeclaredSHA256 {
		p.failAllocation(entry, "stage pre-consumption digest mismatch for "+input.InputId)
		return nil, fmt.Errorf("%w: input %q", sandbox.ErrStageInputMismatch, input.InputId)
	}
	inputKey := sandbox.RecomputeSHA256([]byte(replayKey + "\x00" + input.InputId))
	if err := p.client.WriteFile(ctx, entry.bridgeLocator, path, content, inputKey); err != nil {
		return nil, p.mapAllocationError(sandbox.OperationStage, allocationId, err)
	}
	readBack, err := p.client.ReadFile(ctx, entry.bridgeLocator, path)
	if err != nil {
		return nil, p.mapAllocationError(sandbox.OperationStage, allocationId, err)
	}
	post := sandbox.RecomputeSHA256(readBack)
	if post != pre || post != input.DeclaredSHA256 {
		p.failAllocation(entry, "stage post-consumption digest mismatch for "+input.InputId)
		return nil, fmt.Errorf("cloudflare: stage input %q: post-consumption digest mismatch", input.InputId)
	}
	return &sandbox.StageReceipt{
		InputId:               input.InputId,
		RecomputedSHA256:      pre,
		PostConsumptionSHA256: post,
		SizeBytes:             int64(len(content)),
	}, nil
}

// Exec implements sandbox.SandboxProvider over the Bridge exec SSE stream.
// The provider creates (or reuses) the allocation's session and executes
// with the Session-Id, matching the official "create a session, then execute
// with the Session-Id" model. The receipt is a lifecycle guard only.
func (p *Provider) Exec(ctx context.Context, request sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationExec, request.Identity); err != nil {
		return nil, err
	}
	if len(request.Command) == 0 {
		return nil, fmt.Errorf("%w: exec requires a non-empty command", sandbox.ErrInvalidRequest)
	}
	entry, err := p.resolveActive(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	if entry.sessionId == "" {
		sessionId, sessionErr := p.client.CreateSession(ctx, entry.bridgeLocator, replayKey)
		if sessionErr != nil {
			return nil, p.mapAllocationError(sandbox.OperationExec, request.AllocationId, sessionErr)
		}
		entry.sessionId = sessionId
	}
	stream, err := p.client.Exec(ctx, entry.bridgeLocator, entry.sessionId, ExecStreamRequest{
		Argv: append([]string(nil), request.Command...),
	})
	if err != nil {
		return nil, p.mapAllocationError(sandbox.OperationExec, request.AllocationId, err)
	}
	status := sandbox.ExecutionCompleted
	switch {
	case stream.Signaled:
		status = sandbox.ExecutionKilled
	case stream.ExitCode != 0:
		status = sandbox.ExecutionFailed
	}
	return &sandbox.ExecReceipt{
		Status:       status,
		ExitCode:     stream.ExitCode,
		StdoutSHA256: sandbox.RecomputeSHA256(stream.Stdout),
		StderrSHA256: sandbox.RecomputeSHA256(stream.Stderr),
	}, nil
}

// Inspect implements sandbox.SandboxProvider over the official running
// observation. The official wire exposes no containment-violation or log
// observation channel, so Inspect reports only the observable running state
// and carries empty violation/log/spawn observations; the provider never
// fabricates an observation it cannot make.
func (p *Provider) Inspect(ctx context.Context, request sandbox.InspectRequest) (*sandbox.InspectReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationInspect, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	entry, ok := p.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.WorkloadRole != entry.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	if request.Identity.Generation != entry.meta.Generation {
		p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, fmt.Sprintf("stale generation %d rejected: the allocation carries generation %d", request.Identity.Generation, entry.meta.Generation))
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, request.Identity.Generation, entry.meta.Generation)
	}
	running, err := p.client.SandboxRunning(ctx, entry.bridgeLocator)
	if err != nil {
		if errors.Is(err, ErrSandboxNotFound) {
			if entry.meta.State.IsTerminal() {
				return &sandbox.InspectReport{State: entry.meta.State, ExitCode: -1}, nil
			}
			p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, "the bridge holds no sandbox for this locally active allocation")
			return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
		}
		if errors.Is(err, ErrContainerLost) {
			p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, "the container state was lost after hibernation")
			return &sandbox.InspectReport{State: sandbox.AllocationFailed, ExitCode: -1}, nil
		}
		return nil, err
	}
	if running {
		return &sandbox.InspectReport{State: sandbox.AllocationActive, ExitCode: -1}, nil
	}
	p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, "the bridge observed the sandbox is no longer running")
	return &sandbox.InspectReport{State: sandbox.AllocationFailed, ExitCode: -1}, nil
}

// Signal implements sandbox.SandboxProvider by deleting the exact session:
// the official wire has no dedicated kill endpoint, so the kill is
// delivered by deleting the allocation's tracked session. The closed
// enumeration is validated before any wire call.
func (p *Provider) Signal(ctx context.Context, request sandbox.SignalRequest) (*sandbox.SignalReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationSignal, request.Identity); err != nil {
		return nil, err
	}
	if err := request.Signal.Validate(); err != nil {
		p.recordDiagnostic(sandbox.OperationSignal, request.AllocationId, "signal rejected: "+err.Error())
		return nil, err
	}
	entry, err := p.resolveActive(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	if entry.sessionId == "" {
		return &sandbox.SignalReceipt{Delivered: false}, nil
	}
	if err := p.client.DeleteSession(ctx, entry.bridgeLocator, entry.sessionId); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			entry.sessionId = ""
			return &sandbox.SignalReceipt{Delivered: false}, nil
		}
		return nil, p.mapAllocationError(sandbox.OperationSignal, request.AllocationId, err)
	}
	entry.sessionId = ""
	return &sandbox.SignalReceipt{Delivered: true}, nil
}

// Checkpoint implements sandbox.SandboxProvider over the Bridge persist
// endpoint, which returns the raw tar snapshot. The receipt carries the
// deterministic checkpoint id, the sha256 recomputed over the snapshot bytes
// and the snapshot size; checkpoint semantics cover the staged file-system
// content only (SPI: "snapshot the staged content").
func (p *Provider) Checkpoint(ctx context.Context, request sandbox.CheckpointRequest) (*sandbox.CheckpointReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationCheckpoint, request.Identity); err != nil {
		return nil, err
	}
	entry, err := p.resolveActive(request.Identity, request.AllocationId)
	if err != nil {
		return nil, err
	}
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	tar, err := p.client.Persist(ctx, entry.bridgeLocator, replayKey)
	if err != nil {
		return nil, p.mapAllocationError(sandbox.OperationCheckpoint, request.AllocationId, err)
	}
	receipt := &sandbox.CheckpointReceipt{
		CheckpointId: sandbox.RecomputeSHA256([]byte("checkpoint\x00" + replayKey)),
		SHA256:       sandbox.RecomputeSHA256(tar),
		SizeBytes:    int64(len(tar)),
	}
	entry.lastCheckpoint = &checkpointRecord{tar: append([]byte(nil), tar...)}
	return receipt, nil
}

// Restore implements sandbox.SandboxProvider on top of the frozen
// sandbox.PlanRestore semantics. The default is a replacement allocation:
// durable intent, Bridge create for the fresh locator, hydrate from the
// previous allocation's checkpoint (an implicit persist when none exists
// yet), destroy of the previous sandbox and an atomic outcome commit; an
// in-place restore re-verifies the container survived and bumps the
// generation. The identity must carry the post-restore generation.
func (p *Provider) Restore(ctx context.Context, request sandbox.RestoreOperationRequest) (*sandbox.RestoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationRestore, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.PreviousAllocationId) == "" {
		return nil, fmt.Errorf("%w: previousAllocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	previous, ok := p.allocations[request.PreviousAllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.PreviousAllocationId)
	}
	if previous.meta.State.IsTerminal() {
		return nil, fmt.Errorf("%w: %q is %s", sandbox.ErrAllocationNotActive, request.PreviousAllocationId, string(previous.meta.State))
	}
	if request.Identity.AllocationId != request.PreviousAllocationId && request.Identity.AllocationId != request.NextAllocationId {
		return nil, fmt.Errorf("%w: the identity must bind the previous or the next allocation", sandbox.ErrInvalidRequest)
	}
	if request.Identity.WorkloadRole != previous.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	next, err := sandbox.PlanRestore(sandbox.RestoreRequest{
		Previous:         previous.meta,
		NextAllocationId: request.NextAllocationId,
		InPlaceConfirmed: request.InPlaceConfirmed,
	})
	if err != nil {
		return nil, err
	}
	if request.Identity.Generation != next.Generation {
		p.recordDiagnostic(sandbox.OperationRestore, request.PreviousAllocationId, fmt.Sprintf("restore identity carries generation %d, the post-restore generation is %d", request.Identity.Generation, next.Generation))
		return nil, fmt.Errorf("%w: the restore identity must carry the post-restore generation %d", sandbox.ErrStaleAllocationGeneration, next.Generation)
	}
	if request.InPlaceConfirmed {
		return p.restoreInPlace(ctx, previous, next)
	}
	return p.restoreReplacement(ctx, previous, next, request.Identity)
}

func (p *Provider) restoreInPlace(ctx context.Context, previous *allocationEntry, next sandbox.SandboxAllocation) (*sandbox.RestoreReceipt, error) {
	running, err := p.client.SandboxRunning(ctx, previous.bridgeLocator)
	if err != nil {
		if errors.Is(err, ErrContainerLost) || errors.Is(err, ErrSandboxNotFound) {
			p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "in-place restore rejected: the container state did not survive")
			return nil, fmt.Errorf("%w: in-place restore rejected: the container state did not survive", sandbox.ErrRestoreRejected)
		}
		return nil, err
	}
	if !running {
		p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "in-place restore rejected: the container state was lost")
		return nil, fmt.Errorf("%w: in-place restore rejected: the container state was lost", sandbox.ErrRestoreRejected)
	}
	next.State = sandbox.AllocationActive
	previous.meta = next
	_ = p.store.UpdateAllocation(AllocationRecord{
		Meta:          next,
		Role:          previous.role,
		BridgeLocator: previous.bridgeLocator,
		SessionId:     previous.sessionId,
	})
	return &sandbox.RestoreReceipt{Allocation: next}, nil
}

func (p *Provider) restoreReplacement(ctx context.Context, previous *allocationEntry, next sandbox.SandboxAllocation, identity sandbox.OperationIdentity) (*sandbox.RestoreReceipt, error) {
	if err := sandbox.CheckSingleActive(p.allocationsInScope(next.RunId, next.AttemptId), next); err != nil {
		p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "single-active check rejected the restore: "+err.Error())
		return nil, err
	}
	baseKey, err := identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	subKey := func(purpose string) string {
		return sandbox.RecomputeSHA256([]byte(baseKey + "\x00" + purpose))
	}
	checkpoint := previous.lastCheckpoint
	if checkpoint == nil {
		tar, persistErr := p.client.Persist(ctx, previous.bridgeLocator, subKey("persist"))
		if persistErr != nil {
			if errors.Is(persistErr, ErrContainerLost) {
				p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "restore rejected: no checkpoint exists and the previous container state was lost")
				return nil, fmt.Errorf("%w: no checkpoint exists and the previous container state was lost", sandbox.ErrRestoreRejected)
			}
			return nil, persistErr
		}
		checkpoint = &checkpointRecord{tar: append([]byte(nil), tar...)}
	}

	createKey := subKey("create")
	intent := CreateIntent{
		ReplayKey:    createKey,
		AllocationId: next.AllocationId,
		RunId:        next.RunId,
		AttemptId:    next.AttemptId,
		Generation:   next.Generation,
	}
	if err := p.store.RecordIntent(intent); err != nil {
		return nil, err
	}
	bridgeLocator, err := p.client.CreateSandbox(ctx, createKey)
	if err != nil {
		if !ambiguousCreateError(err) {
			_ = p.store.ClearIntent(createKey)
		}
		if errors.Is(err, ErrBridgeConflict) {
			err = fmt.Errorf("%w: the bridge observed a conflicting sandbox for %q", sandbox.ErrDuplicateActiveAllocation, next.AllocationId)
		}
		p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "bridge create failed during restore: "+err.Error())
		return nil, err
	}
	if err := p.store.RecordBridgeLocator(next.AllocationId, bridgeLocator); err != nil {
		return nil, err
	}
	if err := p.client.Hydrate(ctx, bridgeLocator, checkpoint.tar, subKey("hydrate")); err != nil {
		if !ambiguousCreateError(err) {
			// Deterministic failure: compensate by destroying the
			// half-hydrated replacement sandbox and resolve the intent.
			if destroyErr := p.client.Destroy(ctx, bridgeLocator, subKey("cleanup")); destroyErr != nil {
				p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "post-failure cleanup of the replacement sandbox failed: "+destroyErr.Error())
			}
			_ = p.store.ClearIntent(createKey)
		}
		// An ambiguous (lost) hydrate response leaves the intent pending, so
		// reconcile reports the unknown remote side effect, never clean.
		return nil, err
	}
	if err := p.client.Destroy(ctx, previous.bridgeLocator, subKey("destroy")); err != nil {
		// The replacement is fully hydrated; a failed destroy of the
		// previous sandbox is a leak for the reconcile/leak-scan path, not
		// a reason to roll back the restore.
		p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "previous sandbox destroy failed; reconcile and leak scan must recover it: "+err.Error())
	}
	next.State = sandbox.AllocationActive
	if err := p.store.CommitCreateOutcome(CreateOutcome{
		ReplayKey:     createKey,
		AllocationId:  next.AllocationId,
		BridgeLocator: bridgeLocator,
		Meta:          next,
		Role:          previous.role,
	}); err != nil {
		return nil, err
	}
	previous.meta.State = sandbox.AllocationReplaced
	_ = p.store.UpdateAllocation(AllocationRecord{
		Meta:          previous.meta,
		Role:          previous.role,
		BridgeLocator: previous.bridgeLocator,
		SessionId:     previous.sessionId,
	})
	p.allocations[next.AllocationId] = &allocationEntry{
		meta:           next,
		role:           previous.role,
		bridgeLocator:  bridgeLocator,
		lastCheckpoint: checkpoint,
	}
	return &sandbox.RestoreReceipt{Allocation: next}, nil
}

// Terminate implements sandbox.SandboxProvider over the Bridge destroy
// endpoint. Terminating a terminated or replaced allocation is idempotent
// and performs no further Bridge call.
func (p *Provider) Terminate(ctx context.Context, request sandbox.TerminateRequest) (*sandbox.TerminateReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationTerminate, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.AllocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if request.Identity.AllocationId != request.AllocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, request.Identity.AllocationId, request.AllocationId)
	}
	entry, ok := p.allocations[request.AllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
	}
	if request.Identity.WorkloadRole != entry.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	if request.Identity.Generation != entry.meta.Generation {
		p.recordDiagnostic(sandbox.OperationTerminate, request.AllocationId, fmt.Sprintf("stale generation %d rejected: the allocation carries generation %d", request.Identity.Generation, entry.meta.Generation))
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, request.Identity.Generation, entry.meta.Generation)
	}
	if entry.meta.State == sandbox.AllocationTerminated || entry.meta.State == sandbox.AllocationReplaced {
		// Already terminal: an idempotent observation, no further Bridge call.
		return &sandbox.TerminateReceipt{State: entry.meta.State}, nil
	}
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	if err := p.client.Destroy(ctx, entry.bridgeLocator, replayKey); err != nil {
		switch {
		case errors.Is(err, ErrSandboxNotFound):
			p.recordDiagnostic(sandbox.OperationTerminate, request.AllocationId, "the bridge no longer knew the sandbox; the platform reclaimed it silently")
		case errors.Is(err, ErrContainerLost):
			p.recordDiagnostic(sandbox.OperationTerminate, request.AllocationId, "destroy observed the terminal transition of a lost container")
		default:
			return nil, err
		}
	}
	entry.meta.State = sandbox.AllocationTerminated
	entry.sessionId = ""
	_ = p.store.UpdateAllocation(AllocationRecord{
		Meta:          entry.meta,
		Role:          entry.role,
		BridgeLocator: entry.bridgeLocator,
	})
	return &sandbox.TerminateReceipt{State: entry.meta.State}, nil
}

// Reconcile implements sandbox.SandboxProvider. The official Bridge exposes
// no remote listing endpoint, so reconcile is derived from the local
// bookkeeping plus the per-sandbox running observation. Any pending intent —
// the durable trace of a create whose outcome is unknown — is ambiguity and
// fails closed: the report is returned together with an error, never clean.
func (p *Provider) Reconcile(ctx context.Context, request sandbox.ReconcileRequest) (*sandbox.ReconcileReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enterOperation(sandbox.OperationReconcile, request.Identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.RunId) == "" || strings.TrimSpace(request.AttemptId) == "" {
		return nil, fmt.Errorf("%w: runId and attemptId must be non-empty strings", sandbox.ErrInvalidRequest)
	}
	if request.Identity.RunId != request.RunId || request.Identity.AttemptId != request.AttemptId {
		return nil, fmt.Errorf("%w: the identity does not bind the requested scope", sandbox.ErrInvalidRequest)
	}
	metas := p.allocationsInScope(request.RunId, request.AttemptId)
	var currentGeneration int64
	for _, meta := range metas {
		if meta.Generation > currentGeneration {
			currentGeneration = meta.Generation
		}
	}
	report := &sandbox.ReconcileReport{
		ActiveAllocationIds: []string{},
		OrphanAllocationIds: []string{},
	}
	drift := false
	for _, meta := range metas {
		if meta.State != sandbox.AllocationActive {
			continue
		}
		if meta.Generation != currentGeneration {
			report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
			drift = true
			continue
		}
		entry := p.allocations[meta.AllocationId]
		running, err := p.client.SandboxRunning(ctx, entry.bridgeLocator)
		if err != nil {
			if errors.Is(err, ErrSandboxNotFound) || errors.Is(err, ErrContainerLost) {
				report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
				drift = true
				p.recordDiagnostic(sandbox.OperationReconcile, meta.AllocationId, "locally active allocation is missing or lost on the bridge")
				continue
			}
			return nil, err
		}
		if running {
			report.ActiveAllocationIds = append(report.ActiveAllocationIds, meta.AllocationId)
		} else {
			report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
			drift = true
			p.recordDiagnostic(sandbox.OperationReconcile, meta.AllocationId, "locally active allocation is no longer running on the bridge")
		}
	}
	for _, intent := range p.store.PendingIntents() {
		if intent.RunId != request.RunId || intent.AttemptId != request.AttemptId {
			continue
		}
		report.OrphanAllocationIds = append(report.OrphanAllocationIds, intent.AllocationId)
		drift = true
		p.recordDiagnostic(sandbox.OperationReconcile, intent.AllocationId, "a create intent has no committed outcome; the remote side effect is unknown")
	}
	sort.Strings(report.ActiveAllocationIds)
	sort.Strings(report.OrphanAllocationIds)
	if len(report.ActiveAllocationIds) > 1 {
		drift = true
	}
	report.DriftDetected = drift
	if drift {
		p.recordDiagnostic(sandbox.OperationReconcile, request.Identity.AllocationId, "reconcile drift detected fail closed for scope "+request.RunId+"/"+request.AttemptId)
		return report, fmt.Errorf("cloudflare: reconcile drift detected fail closed for scope %s/%s", request.RunId, request.AttemptId)
	}
	return report, nil
}

// enterOperation validates the operation identity fail closed before any
// side effect. Callers must hold p.mu.
func (p *Provider) enterOperation(operation string, identity sandbox.OperationIdentity) error {
	if err := identity.Validate(); err != nil {
		p.recordDiagnostic(operation, identity.AllocationId, "operation identity rejected: "+err.Error())
		return err
	}
	return nil
}

// resolveActive enforces the dispatch-bound bindings fail closed. Callers
// must hold p.mu.
func (p *Provider) resolveActive(identity sandbox.OperationIdentity, allocationId string) (*allocationEntry, error) {
	if strings.TrimSpace(allocationId) == "" {
		return nil, fmt.Errorf("%w: allocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if identity.AllocationId != allocationId {
		return nil, fmt.Errorf("%w: the identity binds allocation %q, not %q", sandbox.ErrInvalidRequest, identity.AllocationId, allocationId)
	}
	entry, ok := p.allocations[allocationId]
	if !ok {
		p.recordDiagnostic("resolve", allocationId, "unknown allocation")
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, allocationId)
	}
	if identity.WorkloadRole != entry.role {
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	if entry.meta.State != sandbox.AllocationActive {
		return nil, fmt.Errorf("%w: %q is %s", sandbox.ErrAllocationNotActive, allocationId, string(entry.meta.State))
	}
	if identity.Generation != entry.meta.Generation {
		p.recordDiagnostic("resolve", allocationId, fmt.Sprintf("stale generation %d rejected: the allocation carries generation %d", identity.Generation, entry.meta.Generation))
		return nil, fmt.Errorf("%w: the identity carries generation %d, the allocation carries generation %d", sandbox.ErrStaleAllocationGeneration, identity.Generation, entry.meta.Generation)
	}
	return entry, nil
}

// allocationsInScope returns the allocation records of one (runId,
// attemptId) scope in stable order. Callers must hold p.mu.
func (p *Provider) allocationsInScope(runId, attemptId string) []sandbox.SandboxAllocation {
	var result []sandbox.SandboxAllocation
	for _, entry := range p.allocations {
		if entry.meta.RunId == runId && entry.meta.AttemptId == attemptId {
			result = append(result, entry.meta)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AllocationId < result[j].AllocationId
	})
	return result
}

// failAllocation transitions an active allocation to the failed state and
// persists the transition durably. Callers must hold p.mu.
func (p *Provider) failAllocation(entry *allocationEntry, reason string) {
	if entry.meta.State == sandbox.AllocationActive {
		entry.meta.State = sandbox.AllocationFailed
		_ = p.store.UpdateAllocation(AllocationRecord{
			Meta:          entry.meta,
			Role:          entry.role,
			BridgeLocator: entry.bridgeLocator,
			SessionId:     entry.sessionId,
		})
	}
	p.recordDiagnostic(sandbox.OperationStage, entry.meta.AllocationId, reason)
}

// recordDiagnostic appends one fail-closed observation. Callers must hold
// p.mu; the reason text is always constructed without the transport
// credential (client errors are scrubbed before they reach this point).
func (p *Provider) recordDiagnostic(operation, allocationId, reason string) {
	p.diagnostics = append(p.diagnostics, Diagnostic{Operation: operation, AllocationId: allocationId, Reason: reason})
}

// mapAllocationError maps Bridge observations about an addressed sandbox
// onto the SPI sentinels and records a diagnostic. Callers must hold p.mu.
func (p *Provider) mapAllocationError(operation, allocationId string, err error) error {
	switch {
	case errors.Is(err, ErrContainerLost):
		p.recordDiagnostic(operation, allocationId, "the container state was lost after hibernation")
		return fmt.Errorf("%w: the container state was lost after hibernation", sandbox.ErrAllocationNotActive)
	case errors.Is(err, ErrSandboxNotFound):
		p.recordDiagnostic(operation, allocationId, "the bridge holds no sandbox for this allocation")
		return fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, allocationId)
	default:
		return err
	}
}

// ambiguousCreateError reports whether a create error is the ambiguous
// exhausted-retry outcome: the remote side effect may or may not have
// happened, so the durable intent is kept and reconcile reports ambiguity.
func ambiguousCreateError(err error) bool {
	return errors.Is(err, ErrBridgeUnavailable)
}

// validateStageInputs admits one stage request fail closed.
func validateStageInputs(inputs []sandbox.StageInput, allowedStoreIds []string) error {
	for _, input := range inputs {
		if input.Locator == nil {
			continue
		}
		if err := input.Locator.Validate(allowedStoreIds); err != nil {
			return fmt.Errorf("cloudflare: stage input %q: %w", input.InputId, err)
		}
	}
	return sandbox.ValidateStageRequest(inputs, allowedStoreIds)
}

// stagedFilePath derives the Bridge-side path of one staged input and
// rejects every traversal-shaped target.
func stagedFilePath(inputId string) (string, error) {
	if strings.TrimSpace(inputId) == "" {
		return "", fmt.Errorf("%w: the stage target must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	if strings.HasPrefix(inputId, "/") {
		return "", fmt.Errorf("%w: the stage target must be a relative path", sandbox.ErrInvalidRequest)
	}
	for _, part := range strings.Split(inputId, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: the stage target escapes the allocation boundary", sandbox.ErrInvalidRequest)
		}
	}
	return "staged/" + inputId, nil
}

// requireEvidenceDigestShape fails closed unless the configured evidence
// reference is a sha256:-prefixed 64 character lowercase hex digest.
func requireEvidenceDigestShape(value string) error {
	if !strings.HasPrefix(value, sandbox.DigestPrefix) {
		return fmt.Errorf("%w: conformanceEvidenceRef must carry the %s digest prefix", sandbox.ErrInvalidRequest, sandbox.DigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, sandbox.DigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%w: conformanceEvidenceRef must be a 64 character sha256 hex digest", sandbox.ErrInvalidRequest)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: conformanceEvidenceRef must be lowercase hex", sandbox.ErrInvalidRequest)
		}
	}
	return nil
}
