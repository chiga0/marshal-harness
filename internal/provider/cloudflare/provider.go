package cloudflare

import (
	"context"
	"encoding/base64"
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

// maxLogLines bounds the observation log surfaced through Inspect; it
// mirrors internal/sandbox's bounded adjudication input.
const maxLogLines = 32

// bridgeStateRunning is the Bridge lifecycle word mapped onto
// sandbox.AllocationActive.
const bridgeStateRunning = "running"

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
	// ProtocolVersion overrides the Bridge protocol version the client
	// requires (default DefaultProtocolVersion); mismatch fails closed.
	ProtocolVersion string
	// HTTPClient optionally injects the transport (tests); nil takes a
	// plain client.
	HTTPClient *http.Client
	// MaxRetries / RetryDelay / RequestTimeout mirror ClientConfig.
	MaxRetries     int
	RetryDelay     time.Duration
	RequestTimeout time.Duration
}

// Diagnostic is one fail-closed observation recorded by the provider:
// rejected identities, stale generations, assurance refusals, container
// loss and drift. Diagnostic text never carries the transport credential.
type Diagnostic struct {
	Operation    string
	AllocationId string
	Reason       string
}

// allocationEntry is the provider's bookkeeping of one allocation: the
// opaque SPI record, the workload role bound at provision time and the last
// checkpoint observation feeding a later Restore.
type allocationEntry struct {
	meta           sandbox.SandboxAllocation
	role           sandbox.WorkloadRole
	lastCheckpoint *sandbox.CheckpointReceipt
}

// Provider implements sandbox.SandboxProvider against the official Bridge
// OpenAPI family (create / running / exec SSE / file / persist / hydrate /
// destroy). Cloudflare-specific concepts (Durable Objects, R2, Workers
// bindings) never surface here: the Bridge-internal identity of a sandbox
// travels only as the opaque allocationId locator and as receipt fields,
// and Marshal Core never interprets them (ADR 0016 §4). Every receipt this
// provider returns is an observation of Bridge state, never authority.
type Provider struct {
	client      *Client
	evidenceRef string

	mu          sync.Mutex
	allocations map[string]*allocationEntry
	diagnostics []Diagnostic
}

// NewProvider validates the configuration fail closed: a missing transport
// credential or a malformed evidence digest refuses construction outright.
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
		BaseURL:         config.BridgeBaseURL,
		Credential:      credential,
		ProtocolVersion: config.ProtocolVersion,
		HTTPClient:      config.HTTPClient,
		MaxRetries:      config.MaxRetries,
		RetryDelay:      config.RetryDelay,
		RequestTimeout:  config.RequestTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &Provider{
		client:      client,
		evidenceRef: config.ConformanceEvidenceRef,
		allocations: map[string]*allocationEntry{},
	}, nil
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

// Provision implements sandbox.SandboxProvider: the assurance gate, the
// single-active invariant and the Bridge create call. A hardened request
// without valid evidence is refused outright (ErrAssuranceNotMet) and never
// downgraded; the granted allocation carries exactly the requested
// two-dimensional combination.
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
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	if _, err := p.client.CreateSandbox(ctx, CreateSandboxRequest{
		SandboxId:  candidate.AllocationId,
		RunId:      candidate.RunId,
		AttemptId:  candidate.AttemptId,
		Generation: candidate.Generation,
	}, replayKey); err != nil {
		if errors.Is(err, ErrBridgeConflict) {
			err = fmt.Errorf("%w: the bridge observed a conflicting sandbox for %q", sandbox.ErrDuplicateActiveAllocation, candidate.AllocationId)
		}
		p.recordDiagnostic(sandbox.OperationProvision, candidate.AllocationId, "bridge create failed: "+err.Error())
		return nil, err
	}
	p.allocations[candidate.AllocationId] = &allocationEntry{
		meta: candidate,
		role: request.Identity.WorkloadRole,
	}
	return &sandbox.ProvisionReceipt{Allocation: candidate}, nil
}

// Stage implements sandbox.SandboxProvider over the Bridge file endpoint.
// The digest is recomputed before consumption (the Bridge refuses a
// mismatch without writing; inline inputs are additionally recomputed on
// this side of the wire) and once more after consumption by reading the
// staged bytes back: the receipt carries recomputed digests only, never an
// echo of the declared digest.
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
	writeRequest := WriteFileRequest{Path: path, DeclaredSHA256: input.DeclaredSHA256}
	var localPre string
	if input.Locator == nil {
		writeRequest.ContentBase64 = base64.StdEncoding.EncodeToString(input.Inline)
		localPre = sandbox.RecomputeSHA256(input.Inline)
	} else {
		writeRequest.Locator = &LocatorRef{
			StoreId:   input.Locator.StoreId,
			SHA256:    input.Locator.SHA256,
			SizeBytes: input.Locator.SizeBytes,
		}
	}
	inputKey := sandbox.RecomputeSHA256([]byte(replayKey + "\x00" + input.InputId))
	result, err := p.client.WriteFile(ctx, allocationId, writeRequest, inputKey)
	if err != nil {
		if errors.Is(err, ErrDigestMismatch) {
			p.failAllocation(entry, "stage input digest mismatch detected before consumption for "+input.InputId)
			return nil, fmt.Errorf("%w: input %q", sandbox.ErrStageInputMismatch, input.InputId)
		}
		if errors.Is(err, ErrBridgeLocatorUnresolved) {
			p.recordDiagnostic(sandbox.OperationStage, allocationId, "locator unresolved for "+input.InputId)
			return nil, fmt.Errorf("%w: input %q", sandbox.ErrLocatorUnresolved, input.InputId)
		}
		if errors.Is(err, ErrContainerLost) {
			p.recordDiagnostic(sandbox.OperationStage, allocationId, "container state lost before staging "+input.InputId)
			return nil, fmt.Errorf("%w: the container state was lost after hibernation", sandbox.ErrAllocationNotActive)
		}
		var bridgeErr *BridgeError
		if errors.As(err, &bridgeErr) && bridgeErr.Code == "post-write-mismatch" {
			p.failAllocation(entry, "post-consumption digest mismatch reported by the bridge for "+input.InputId)
		}
		return nil, err
	}
	// Belt-and-braces pre-consumption check for inline inputs: the digest
	// this side computed over the very bytes it sent must equal the digest
	// the Bridge computed over the very bytes it received.
	if localPre != "" && result.PreSHA256 != localPre {
		p.failAllocation(entry, "stage transport integrity failure for "+input.InputId)
		return nil, fmt.Errorf("cloudflare: stage input %q: the transport altered the staged bytes", input.InputId)
	}
	if result.PreSHA256 != input.DeclaredSHA256 {
		p.failAllocation(entry, "stage pre-consumption digest disagreement for "+input.InputId)
		return nil, fmt.Errorf("%w: input %q", sandbox.ErrStageInputMismatch, input.InputId)
	}
	// Post-consumption recomputation: read the staged bytes back and
	// recompute the digest out-of-band, so the receipt can never be
	// satisfied by echoing a Bridge self-report.
	readBack, err := p.client.ReadFile(ctx, allocationId, path)
	if err != nil {
		return nil, err
	}
	content, err := base64.StdEncoding.DecodeString(readBack.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("%w: the bridge returned a malformed staged payload", ErrInvalidBridgeResponse)
	}
	post := sandbox.RecomputeSHA256(content)
	if post != result.PostSHA256 || post != input.DeclaredSHA256 {
		p.failAllocation(entry, "stage post-consumption digest mismatch for "+input.InputId)
		return nil, fmt.Errorf("cloudflare: stage input %q: post-consumption digest mismatch", input.InputId)
	}
	sizeBytes := int64(len(content))
	if input.Locator != nil && sizeBytes != input.Locator.SizeBytes {
		p.failAllocation(entry, "staged locator size disagreement for "+input.InputId)
		return nil, fmt.Errorf("cloudflare: stage input %q: the staged size disagrees with the locator", input.InputId)
	}
	return &sandbox.StageReceipt{
		InputId:               input.InputId,
		RecomputedSHA256:      result.PreSHA256,
		PostConsumptionSHA256: post,
		SizeBytes:             sizeBytes,
	}, nil
}

// Exec implements sandbox.SandboxProvider over the Bridge exec SSE stream.
// The receipt is a lifecycle guard only: no conformance or fencing verdict
// is ever derived from it.
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
	streamRequest := ExecStreamRequest{Command: append([]string(nil), request.Command...)}
	if len(request.Stdin) > 0 {
		streamRequest.StdinBase64 = base64.StdEncoding.EncodeToString(request.Stdin)
	}
	stream, err := p.client.Exec(ctx, entry.meta.AllocationId, streamRequest)
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

// Inspect implements sandbox.SandboxProvider: the out-of-band observation
// comes from the Bridge observation endpoint, never from provider
// self-report. A container whose state was lost after hibernation is
// observed as failed; a locally active allocation the Bridge no longer
// knows fails closed.
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
	status, err := p.client.SandboxStatus(ctx, request.AllocationId)
	if err != nil {
		if errors.Is(err, ErrContainerLost) {
			p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, "the container state was lost after hibernation")
			return &sandbox.InspectReport{
				State:    sandbox.AllocationFailed,
				ExitCode: -1,
				LogLines: []string{"observed: the container state was lost after hibernation"},
			}, nil
		}
		if errors.Is(err, ErrSandboxNotFound) {
			if entry.meta.State.IsTerminal() {
				return &sandbox.InspectReport{State: entry.meta.State, ExitCode: -1}, nil
			}
			p.recordDiagnostic(sandbox.OperationInspect, request.AllocationId, "the bridge holds no sandbox for this locally active allocation")
			return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.AllocationId)
		}
		return nil, err
	}
	state := sandbox.AllocationActive
	if status.State != bridgeStateRunning {
		state = sandbox.AllocationFailed
	}
	violations := make([]sandbox.BoundaryViolation, 0, len(status.Violations))
	for _, violation := range status.Violations {
		violations = append(violations, sandbox.BoundaryViolation{Kind: violation.Kind, Detail: violation.Detail})
	}
	logLines := append([]string(nil), status.LogLines...)
	if len(logLines) > maxLogLines {
		logLines = logLines[len(logLines)-maxLogLines:]
	}
	return &sandbox.InspectReport{
		State:      state,
		ExitCode:   status.ExitCode,
		Violations: violations,
		SpawnCount: status.SpawnCount,
		LogLines:   logLines,
	}, nil
}

// Signal implements sandbox.SandboxProvider through the Bridge signal
// endpoint; the closed enumeration is validated before any wire call.
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
	replayKey, err := request.Identity.ReplayKey()
	if err != nil {
		return nil, err
	}
	result, err := p.client.Signal(ctx, entry.meta.AllocationId, string(request.Signal), replayKey)
	if err != nil {
		return nil, p.mapAllocationError(sandbox.OperationSignal, request.AllocationId, err)
	}
	return &sandbox.SignalReceipt{Delivered: result.Delivered}, nil
}

// Checkpoint implements sandbox.SandboxProvider over the Bridge persist
// endpoint. The receipt carries the checkpoint id, the sha256 the Bridge
// recomputed over the snapshot bytes and the snapshot size; checkpoint
// semantics cover the staged file-system content only (SPI: "snapshot the
// staged content") — platform-internal hibernation state is never a
// checkpoint.
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
	result, err := p.client.Persist(ctx, entry.meta.AllocationId, replayKey)
	if err != nil {
		return nil, p.mapAllocationError(sandbox.OperationCheckpoint, request.AllocationId, err)
	}
	receipt := &sandbox.CheckpointReceipt{
		CheckpointId: result.CheckpointId,
		SHA256:       result.SHA256,
		SizeBytes:    result.SizeBytes,
	}
	entry.lastCheckpoint = receipt
	return receipt, nil
}

// Restore implements sandbox.SandboxProvider on top of the frozen
// sandbox.PlanRestore semantics. The default is a replacement allocation:
// Bridge create for the fresh locator, hydrate from the previous
// allocation's checkpoint (an implicit persist when none exists yet) and
// destroy of the previous sandbox; an in-place restore additionally
// re-verifies through the Bridge observation channel that no live exec
// session exists and that the container state survived. The identity must
// carry the post-restore generation.
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
	status, err := p.client.SandboxStatus(ctx, previous.meta.AllocationId)
	if err != nil {
		if errors.Is(err, ErrContainerLost) {
			p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "in-place restore rejected: the container state was lost after hibernation")
			return nil, fmt.Errorf("%w: in-place restore rejected: the container state was lost after hibernation", sandbox.ErrRestoreRejected)
		}
		return nil, err
	}
	if status.LiveSessions > 0 {
		p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "in-place restore re-verification failed: a live exec session exists")
		return nil, fmt.Errorf("%w: in-place restore re-verification failed: the previous process tree is still alive", sandbox.ErrRestoreRejected)
	}
	next.State = sandbox.AllocationActive
	previous.meta = next
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
		result, persistErr := p.client.Persist(ctx, previous.meta.AllocationId, subKey("persist"))
		if persistErr != nil {
			if errors.Is(persistErr, ErrContainerLost) {
				p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "restore rejected: no checkpoint exists and the previous container state was lost")
				return nil, fmt.Errorf("%w: no checkpoint exists and the previous container state was lost", sandbox.ErrRestoreRejected)
			}
			return nil, persistErr
		}
		checkpoint = &sandbox.CheckpointReceipt{
			CheckpointId: result.CheckpointId,
			SHA256:       result.SHA256,
			SizeBytes:    result.SizeBytes,
		}
	}
	if _, err := p.client.CreateSandbox(ctx, CreateSandboxRequest{
		SandboxId:  next.AllocationId,
		RunId:      next.RunId,
		AttemptId:  next.AttemptId,
		Generation: next.Generation,
	}, subKey("create")); err != nil {
		if errors.Is(err, ErrBridgeConflict) {
			err = fmt.Errorf("%w: the bridge observed a conflicting sandbox for %q", sandbox.ErrDuplicateActiveAllocation, next.AllocationId)
		}
		return nil, err
	}
	hydrateResult, err := p.client.Hydrate(ctx, next.AllocationId, checkpoint.CheckpointId, subKey("hydrate"))
	if err != nil {
		// Compensation: never leave a half-hydrated replacement sandbox
		// behind on a deterministic failure.
		if _, destroyErr := p.client.Destroy(ctx, next.AllocationId, subKey("cleanup")); destroyErr != nil {
			p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "post-failure cleanup of the replacement sandbox failed: "+destroyErr.Error())
		}
		return nil, err
	}
	if hydrateResult.SHA256 != checkpoint.SHA256 {
		if _, destroyErr := p.client.Destroy(ctx, next.AllocationId, subKey("cleanup")); destroyErr != nil {
			p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "post-failure cleanup of the replacement sandbox failed: "+destroyErr.Error())
		}
		p.recordDiagnostic(sandbox.OperationRestore, next.AllocationId, "hydrate digest disagrees with the checkpoint receipt")
		return nil, fmt.Errorf("cloudflare: restore: the hydrate observation disagrees with the checkpoint digest")
	}
	if _, err := p.client.Destroy(ctx, previous.meta.AllocationId, subKey("destroy")); err != nil {
		// The replacement is fully hydrated; a failed destroy of the
		// previous sandbox is a leak for the reconcile/leak-scan path, not
		// a reason to roll back the restore.
		p.recordDiagnostic(sandbox.OperationRestore, previous.meta.AllocationId, "previous sandbox destroy failed; reconcile and leak scan must recover it: "+err.Error())
	}
	next.State = sandbox.AllocationActive
	previous.meta.State = sandbox.AllocationReplaced
	p.allocations[next.AllocationId] = &allocationEntry{
		meta:           next,
		role:           previous.role,
		lastCheckpoint: checkpoint,
	}
	return &sandbox.RestoreReceipt{Allocation: next}, nil
}

// Terminate implements sandbox.SandboxProvider over the Bridge destroy
// endpoint. Terminating a terminated or replaced allocation is idempotent
// and performs no further Bridge call; an active or failed allocation is
// destroyed at the Bridge and recorded terminated. A failed allocation —
// the fail-closed outcome of a stage integrity violation — recovers through
// the identical terminal bookkeeping the deterministic fake provider
// applies, so after a successful destroy Reconcile never observes a
// lingering active allocation.
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
	if _, err := p.client.Destroy(ctx, request.AllocationId, replayKey); err != nil {
		switch {
		case errors.Is(err, ErrSandboxNotFound):
			// The platform already reclaimed the sandbox: observe the
			// terminal transition and leave the disagreement to reconcile.
			p.recordDiagnostic(sandbox.OperationTerminate, request.AllocationId, "the bridge no longer knew the sandbox; the platform reclaimed it silently")
		case errors.Is(err, ErrContainerLost):
			// The container state was already lost after hibernation: the
			// destroy can only observe the terminal transition.
			p.recordDiagnostic(sandbox.OperationTerminate, request.AllocationId, "destroy observed the terminal transition of a lost container")
		default:
			return nil, err
		}
	}
	// A successful destroy records the allocation terminated — including one
	// a fail-closed stage integrity refusal left failed — mirroring the
	// deterministic fake provider's terminal bookkeeping, so Reconcile never
	// observes a lingering active allocation after a destroy.
	entry.meta.State = sandbox.AllocationTerminated
	return &sandbox.TerminateReceipt{State: entry.meta.State}, nil
}

// Reconcile implements sandbox.SandboxProvider: it reconciles the
// provider's bookkeeping against the Bridge running-class listing for one
// (runId, attemptId) scope. Silent platform reclaim, stale-generation
// actives and Bridge-side unknowns are drift and fail closed: the report is
// returned together with an error.
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
	listed, err := p.client.ListSandboxes(ctx, request.RunId, request.AttemptId)
	if err != nil {
		return nil, err
	}
	bridgeRunning := make(map[string]bool, len(listed))
	for _, record := range listed {
		bridgeRunning[record.SandboxId] = true
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
		if bridgeRunning[meta.AllocationId] {
			report.ActiveAllocationIds = append(report.ActiveAllocationIds, meta.AllocationId)
			continue
		}
		// Locally active at the current generation but absent from the
		// Bridge running list: silent platform reclaim or loss, fail closed.
		report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
		drift = true
		p.recordDiagnostic(sandbox.OperationReconcile, meta.AllocationId, "locally active allocation missing from the bridge running list")
	}
	for _, record := range listed {
		entry, ok := p.allocations[record.SandboxId]
		if ok && entry.meta.State == sandbox.AllocationActive {
			continue
		}
		report.OrphanAllocationIds = append(report.OrphanAllocationIds, record.SandboxId)
		drift = true
		p.recordDiagnostic(sandbox.OperationReconcile, record.SandboxId, "the bridge runs a sandbox the bookkeeping does not hold active")
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
// side effect. Fencing/generation authority stays at the marshal-server
// write boundary; the provider only validates and passes the identity
// through. Callers must hold p.mu.
func (p *Provider) enterOperation(operation string, identity sandbox.OperationIdentity) error {
	if err := identity.Validate(); err != nil {
		p.recordDiagnostic(operation, identity.AllocationId, "operation identity rejected: "+err.Error())
		return err
	}
	return nil
}

// resolveActive enforces the dispatch-bound bindings fail closed: the
// identity must bind the addressed locator and the allocation's workload
// role, and must carry the allocation's current generation. Callers must
// hold p.mu.
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

// failAllocation transitions an active allocation to the failed state; it
// marks the attempt failed closed after an integrity violation. Callers
// must hold p.mu.
func (p *Provider) failAllocation(entry *allocationEntry, reason string) {
	if entry.meta.State == sandbox.AllocationActive {
		entry.meta.State = sandbox.AllocationFailed
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

// validateStageInputs admits one stage request fail closed. Every locator
// input is additionally adjudicated through sandbox.Locator.Validate so a
// locator refusal keeps the ErrInvalidLocator sentinel chain the SPI
// assigns to locator refusals (an unbound store alias, a URL-shaped or
// credential-shaped alias, a malformed digest or a non-positive size):
// sandbox.ValidateStageRequest remains the admission authority for every
// other rule, but its whole-request wrapping surfaces the locator sentinel
// only as message text, so the provider re-validates the locator shape
// first and wraps the refusal with %w (recorded per the M10-a rule that
// SPI conflicts are resolved in favor of the SPI and noted in a comment).
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
// reference is a sha256:-prefixed 64 character lowercase hex digest,
// mirroring the SPI's digest admission rule.
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
