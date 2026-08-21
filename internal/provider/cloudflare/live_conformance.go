package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file freezes the truthful live conformance harness of the Cloudflare
// Bridge provider. A live conformance run binds the real Bridge endpoint,
// the non-sensitive service identity (the Core authority namespace and the
// provider actor security domain), the run/attempt/allocation identity and
// the Provision/Terminate receipts into one closed result.
//
// The harness reports a closed three-state outcome:
//
//	live        — a genuine live run completed and bound its receipts.
//	simulated   — a run against a simulator; it can never satisfy the live
//	              gate (a simulated pass is not a live pass).
//	unavailable — no live endpoint/credentialed executor is configured, the
//	              declared identity cannot be bound, or the live run failed.
//
// When no live endpoint or credentialed executor is configured the harness
// reports unavailable before constructing anything and before issuing any
// network request — never a fabricated pass, a pre-canned transcript or a
// simulated run promoted to live.
//
// The post-termination leak scan is bookkeeping-scoped only: the official
// Bridge exposes no account-wide orphan enumeration, so the harness asserts
// the absence of known active/orphan allocations and pending intents for the
// bound (runId, attemptId) scope, and never claims global zero-leak.

// LiveConformanceStatus is the closed result state of one live conformance
// run. Exactly three members exist; Validate rejects everything else.
type LiveConformanceStatus string

// Closed members of LiveConformanceStatus.
const (
	StatusLive        LiveConformanceStatus = "live"
	StatusSimulated   LiveConformanceStatus = "simulated"
	StatusUnavailable LiveConformanceStatus = "unavailable"
)

// Validate rejects every value outside the closed enumeration.
func (status LiveConformanceStatus) Validate() error {
	switch status {
	case StatusLive, StatusSimulated, StatusUnavailable:
		return nil
	default:
		return fmt.Errorf("cloudflare live conformance: unknown result status %q", string(status))
	}
}

// SatisfiesLiveGate reports whether the status is a genuine live pass. Only
// StatusLive satisfies it; a simulated run can never satisfy the live gate.
func (status LiveConformanceStatus) SatisfiesLiveGate() bool {
	return status == StatusLive
}

// LeakScanScope is the closed enumeration of what the post-termination leak
// scan can observe. The Bridge harness can only ever observe its own
// bookkeeping scope; the global scope is a member of the enumeration only so
// that the limitation can be stated and asserted, never produced.
type LeakScanScope string

// Closed members of LeakScanScope.
const (
	LeakScanScopeBookkeeping LeakScanScope = "bookkeeping"
	LeakScanScopeGlobal      LeakScanScope = "global"
)

// Validate rejects every value outside the closed enumeration.
func (scope LeakScanScope) Validate() error {
	switch scope {
	case LeakScanScopeBookkeeping, LeakScanScopeGlobal:
		return nil
	default:
		return fmt.Errorf("cloudflare live conformance: unknown leak scan scope %q", string(scope))
	}
}

// ResourceCounts carries the closed resource accounting of one run. It is
// integer counts only: no token, URL, query, environment value, stdout,
// stderr or filesystem path ever participates.
type ResourceCounts struct {
	Provisioned       int `json:"provisioned"`
	Terminated        int `json:"terminated"`
	EffectRecords     int `json:"effectRecords"`
	ActiveAllocations int `json:"activeAllocations"`
	OrphanAllocations int `json:"orphanAllocations"`
	PendingIntents    int `json:"pendingIntents"`
	ReconcilePasses   int `json:"reconcilePasses"`
}

// LiveConformanceResult is the closed output of one run: the status, a
// fixed summary, the resource counts and the leak-scan scope. No field ever
// carries the transport credential, the endpoint URL, an environment value,
// stdout/stderr or a filesystem path.
type LiveConformanceResult struct {
	Status         LiveConformanceStatus `json:"status"`
	Summary        string                `json:"summary"`
	ResourceCounts ResourceCounts        `json:"resourceCounts"`
	LeakScanScope  LeakScanScope         `json:"leakScanScope"`
}

// SatisfiesLiveGate reports whether the result is a genuine live pass. Only
// StatusLive satisfies it; a simulated run can never satisfy the live gate.
func (result LiveConformanceResult) SatisfiesLiveGate() bool {
	return result.Status.SatisfiesLiveGate()
}

// LiveConformanceConfig carries the inputs of one live conformance run. The
// BridgeToken is a transport credential only; ServiceIdentity and
// ProviderDomain are the non-sensitive service identity, and the harness
// binds them to the resolved Core authority context before any remote side
// effect. The harness never reads an environment variable or a secret store:
// every input arrives here as an explicit field.
type LiveConformanceConfig struct {
	// BridgeBaseURL is the absolute URL of the real Bridge endpoint. An
	// empty value means no live endpoint is configured.
	BridgeBaseURL string
	// BridgeToken is the Bridge Bearer transport credential. An empty value
	// means no credentialed executor is configured.
	BridgeToken string
	// ServiceIdentity is the non-sensitive Core authority namespace the
	// conformance run binds to; it is never the transport credential.
	ServiceIdentity authority.AuthorityNamespaceId
	// ProviderDomain is the non-sensitive provider actor security domain the
	// conformance run binds to.
	ProviderDomain authority.SecurityDomainId
	// Simulated marks an explicitly simulated run. A simulated run is never
	// promoted to live: its result is StatusSimulated and the live gate
	// stays unsatisfied.
	Simulated bool
	// Identity is the run/attempt/allocation identity every receipt must
	// bind to exactly.
	Identity sandbox.OperationIdentity
	// Requirements is the two-dimensional sandbox requirements Provision
	// serves.
	Requirements domain.SandboxRequirements
	// StateStore must be the durable file-backed state store (the production
	// composition root; the ephemeral in-memory store is refused).
	StateStore *FileStateStore
	// AuthoritySink must be the durable file-backed effect authority sink.
	AuthoritySink *FileEffectAuthoritySink
	// AuthorityResolver must be the Core-backed authority resolver.
	AuthorityResolver CoreBackedAuthorityResolver
	// HTTPClient optionally injects the transport (tests); nil takes the
	// plain client.
	HTTPClient *http.Client
	// MaxRetries / RetryDelay / RequestTimeout mirror ProviderConfig.
	MaxRetries     int
	RetryDelay     time.Duration
	RequestTimeout time.Duration
}

// LiveConformanceHarness runs one truthful live conformance probe against
// the production Bridge composition.
type LiveConformanceHarness struct {
	config LiveConformanceConfig
}

// NewLiveConformanceHarness binds one config. Nothing is constructed and no
// network request is issued here.
func NewLiveConformanceHarness(config LiveConformanceConfig) *LiveConformanceHarness {
	return &LiveConformanceHarness{config: config}
}

// reconcilePasses is the frozen number of post-termination reconciles the
// harness drives to assert convergence: the identical scope must be clean on
// the first and the second pass, so a single clean pass cannot be mistaken
// for a stable one.
const reconcilePasses = 2

// Fixed summaries. Every summary is a closed, credential-free, URL-free,
// path-free string; no dynamic error text or endpoint detail is ever spliced
// into a summary.
const (
	summaryUnavailableNoEndpoint        = "unavailable: no live bridge endpoint or credentialed executor is configured; live conformance is skipped, never fabricated"
	summaryUnavailableNoServiceIdentity = "unavailable: the non-sensitive service identity is missing or invalid; live conformance is skipped"
	summaryUnavailableInvalidIdentity   = "unavailable: the run/attempt/allocation identity is invalid; live conformance is skipped"
	summaryLivePassed                   = "live conformance passed: provision and terminate receipts bound to the run/attempt/allocation identity"
	summarySimulatedPassed              = "simulated conformance passed: the flow completed but a simulated run never satisfies the live gate"
)

// Run executes one conformance run and returns the closed result. It never
// returns a Go error: every failure is folded into a closed status plus a
// fixed reason-bearing summary, so the caller always receives one of the
// three closed states.
func (h *LiveConformanceHarness) Run(ctx context.Context) LiveConformanceResult {
	config := h.config

	// Availability is a pure config-shape check. When the endpoint or the
	// credentialed executor is absent the harness reports unavailable before
	// constructing anything, so no network request is ever issued on a
	// missing-live path.
	if strings.TrimSpace(config.BridgeBaseURL) == "" || strings.TrimSpace(config.BridgeToken) == "" {
		return LiveConformanceResult{Status: StatusUnavailable, Summary: summaryUnavailableNoEndpoint, LeakScanScope: LeakScanScopeBookkeeping}
	}
	if err := config.ServiceIdentity.Validate(); err != nil {
		return LiveConformanceResult{Status: StatusUnavailable, Summary: summaryUnavailableNoServiceIdentity, LeakScanScope: LeakScanScopeBookkeeping}
	}
	if err := config.ProviderDomain.Validate(); err != nil {
		return LiveConformanceResult{Status: StatusUnavailable, Summary: summaryUnavailableNoServiceIdentity, LeakScanScope: LeakScanScopeBookkeeping}
	}
	if err := config.Identity.Validate(); err != nil {
		return LiveConformanceResult{Status: StatusUnavailable, Summary: summaryUnavailableInvalidIdentity, LeakScanScope: LeakScanScopeBookkeeping}
	}
	if err := validateLiveRequirements(config.Requirements); err != nil {
		return h.failed(config, "invalid-requirements", ResourceCounts{})
	}

	// The production composition is the only construction path: a file-backed
	// state store, a durable file-backed effect sink and a non-nil Core-backed
	// authority resolver are all mandatory — never an in-memory fallback.
	provider, err := NewProductionProvider(ProductionProviderConfig{
		ProviderConfig: ProviderConfig{
			BridgeBaseURL:  config.BridgeBaseURL,
			BridgeToken:    config.BridgeToken,
			HTTPClient:     config.HTTPClient,
			MaxRetries:     config.MaxRetries,
			RetryDelay:     config.RetryDelay,
			RequestTimeout: config.RequestTimeout,
			StateStore:     config.StateStore,
		},
		AuthoritySink:     config.AuthoritySink,
		AuthorityResolver: config.AuthorityResolver,
	})
	if err != nil {
		return h.failed(config, classifyLiveError(err), ResourceCounts{})
	}

	// Bind the declared non-sensitive service identity to the resolved Core
	// authority context before any remote side effect. A mismatch fails
	// closed: the harness never proceeds with an identity it cannot bind.
	authorityCtx, err := config.AuthorityResolver.ResolveAuthorityContext()
	if err != nil {
		return h.failed(config, classifyLiveError(err), ResourceCounts{})
	}
	if !authorityCtx.Namespace.Equal(config.ServiceIdentity) {
		return h.failed(config, "service-identity-mismatch", ResourceCounts{})
	}
	if !authorityCtx.ProviderSecurityDomain.Equal(config.ProviderDomain) {
		return h.failed(config, "provider-domain-mismatch", ResourceCounts{})
	}

	provisionReceipt, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     config.Identity,
		Requirements: config.Requirements,
	})
	if err != nil {
		return h.failed(config, classifyLiveError(err), ResourceCounts{})
	}
	if provisionReceipt == nil || !receiptBinds(provisionReceipt.Allocation, config.Identity) {
		return h.failed(config, "provision-receipt-binding-mismatch", ResourceCounts{})
	}

	terminateReceipt, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     config.Identity,
		AllocationId: config.Identity.AllocationId,
	})
	if err != nil {
		return h.failed(config, classifyLiveError(err), ResourceCounts{Provisioned: 1})
	}
	if terminateReceipt == nil || terminateReceipt.State != sandbox.AllocationTerminated {
		return h.failed(config, "terminate-receipt-binding-mismatch", ResourceCounts{Provisioned: 1})
	}

	counts := ResourceCounts{Provisioned: 1, Terminated: 1}

	// Post-termination leak scan: repeated reconcile over the bound scope,
	// plus the durable pending-intent trace. The Bridge has no account-wide
	// enumeration, so the scan is bookkeeping-scoped and never claims global
	// zero-leak.
	leakClean := true
	for pass := 0; pass < reconcilePasses; pass++ {
		report, reconcileErr := provider.Reconcile(ctx, sandbox.ReconcileRequest{
			Identity:  config.Identity,
			RunId:     config.Identity.RunId,
			AttemptId: config.Identity.AttemptId,
		})
		counts.ReconcilePasses++
		if report != nil {
			counts.ActiveAllocations = len(report.ActiveAllocationIds)
			counts.OrphanAllocations = len(report.OrphanAllocationIds)
		}
		if reconcileErr != nil || report == nil || report.DriftDetected || len(report.ActiveAllocationIds) != 0 || len(report.OrphanAllocationIds) != 0 {
			leakClean = false
		}
	}
	if config.StateStore != nil {
		for _, intent := range config.StateStore.PendingIntents() {
			if intent.RunId == config.Identity.RunId && intent.AttemptId == config.Identity.AttemptId {
				counts.PendingIntents++
			}
		}
	}
	if !leakClean || counts.PendingIntents != 0 {
		return h.failed(config, "leak-scan-drift", counts)
	}

	// Bind the durable effect records to the identity and the service
	// identity: exactly one provision and one terminate record must exist for
	// this allocation, and each must carry the same namespace, scope and
	// actor provenance the run was configured with.
	if config.AuthoritySink != nil {
		provisionRecords, terminateRecords := 0, 0
		bound := true
		for _, record := range config.AuthoritySink.Records() {
			if record.AllocationId != config.Identity.AllocationId {
				continue
			}
			if !effectRecordBinds(record, config.Identity, authorityCtx) {
				bound = false
				continue
			}
			switch record.Operation {
			case sandbox.OperationProvision:
				provisionRecords++
			case sandbox.OperationTerminate:
				terminateRecords++
			}
		}
		counts.EffectRecords = provisionRecords + terminateRecords
		if !bound || provisionRecords != 1 || terminateRecords != 1 {
			return h.failed(config, "effect-record-binding-mismatch", counts)
		}
	}

	if config.Simulated {
		return LiveConformanceResult{Status: StatusSimulated, Summary: summarySimulatedPassed, ResourceCounts: counts, LeakScanScope: LeakScanScopeBookkeeping}
	}
	return LiveConformanceResult{Status: StatusLive, Summary: summaryLivePassed, ResourceCounts: counts, LeakScanScope: LeakScanScopeBookkeeping}
}

// failed folds one failure into the closed result for the configured mode: a
// simulated failure stays simulated (and never satisfies the live gate),
// while a live-mode failure degrades to unavailable — the live result was
// not achieved.
func (h *LiveConformanceHarness) failed(config LiveConformanceConfig, reason string, counts ResourceCounts) LiveConformanceResult {
	if config.Simulated {
		return LiveConformanceResult{
			Status:         StatusSimulated,
			Summary:        "simulated conformance failed: " + reason,
			ResourceCounts: counts,
			LeakScanScope:  LeakScanScopeBookkeeping,
		}
	}
	return LiveConformanceResult{
		Status:         StatusUnavailable,
		Summary:        "live conformance unavailable: " + reason,
		ResourceCounts: counts,
		LeakScanScope:  LeakScanScopeBookkeeping,
	}
}

// receiptBinds reports whether one provisioned allocation carries the exact
// run/attempt/allocation identity the run was configured with.
func receiptBinds(allocation sandbox.SandboxAllocation, identity sandbox.OperationIdentity) bool {
	return allocation.AllocationId == identity.AllocationId &&
		allocation.RunId == identity.RunId &&
		allocation.AttemptId == identity.AttemptId &&
		allocation.Generation == identity.Generation
}

// effectRecordBinds reports whether one durable effect authority record
// carries the exact run/attempt/allocation identity and the exact resolved
// service identity the run was configured with.
func effectRecordBinds(record EffectAuthorityRecord, identity sandbox.OperationIdentity, ctx AuthorityContext) bool {
	return record.RunId == identity.RunId &&
		record.AttemptId == identity.AttemptId &&
		record.AllocationId == identity.AllocationId &&
		record.Generation == identity.Generation &&
		record.Namespace.Equal(ctx.Namespace) &&
		record.Receipt.ActorProvenance.SecurityDomainId.Equal(ctx.ProviderSecurityDomain)
}

// validateLiveRequirements rejects a malformed two-dimensional requirement
// before it reaches Provision.
func validateLiveRequirements(requirements domain.SandboxRequirements) error {
	if _, err := domain.ParseAccessMode(string(requirements.AccessMode)); err != nil {
		return err
	}
	if _, err := domain.ParseAssuranceLevel(string(requirements.MinimumAssuranceLevel)); err != nil {
		return err
	}
	return nil
}

// classifyLiveError maps one error onto a fixed, closed reason code. It never
// splices the underlying error text, so a credential, URL, path, environment
// value, stdout or stderr can never surface through a summary.
func classifyLiveError(err error) string {
	switch {
	case errors.Is(err, sandbox.ErrInvalidOperationIdentity):
		return "invalid-identity"
	case errors.Is(err, sandbox.ErrInvalidRequest):
		return "invalid-request"
	case errors.Is(err, sandbox.ErrAssuranceNotMet):
		return "assurance-not-met"
	case errors.Is(err, sandbox.ErrDuplicateActiveAllocation):
		return "duplicate-active-allocation"
	case errors.Is(err, sandbox.ErrStaleAllocationGeneration):
		return "stale-generation"
	case errors.Is(err, sandbox.ErrAllocationNotFound):
		return "allocation-not-found"
	case errors.Is(err, sandbox.ErrAllocationNotActive):
		return "allocation-not-active"
	case errors.Is(err, ErrCredentialMissing):
		return "credential-missing"
	case errors.Is(err, ErrCredentialRejected):
		return "credential-rejected"
	case errors.Is(err, ErrCapacityExhausted):
		return "capacity-exhausted"
	case errors.Is(err, ErrBridgeConflict):
		return "bridge-conflict"
	case errors.Is(err, ErrSandboxNotFound):
		return "sandbox-not-found"
	case errors.Is(err, ErrContainerLost):
		return "container-lost"
	case errors.Is(err, ErrBridgeUnavailable):
		return "bridge-unavailable"
	case errors.Is(err, ErrInvalidBridgeResponse):
		return "invalid-bridge-response"
	case errors.Is(err, ErrBridgeRejected):
		return "bridge-rejected"
	case errors.Is(err, ErrProductionConfigInvalid):
		return "production-config-invalid"
	case errors.Is(err, ErrAuthorityContextUnresolved):
		return "authority-context-unresolved"
	case errors.Is(err, ErrEffectRecordInvalid):
		return "effect-record-invalid"
	case errors.Is(err, ErrStateStoreInvalid), errors.Is(err, ErrStateStoreInconsistent):
		return "state-store-invalid"
	default:
		return "provider-error"
	}
}
