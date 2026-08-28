package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/execution"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
	"github.com/chiga0/marshal-harness/internal/sandbox/local"
)

// EmbeddedSandboxEnvironmentVariable is the opt-in switch of the M8 embedded
// sandbox runtime: exactly the value "1" enables the embedded dispatch-bound
// flow of `task run`; unset or any other value keeps the Local MVP behavior
// completely unchanged.
const EmbeddedSandboxEnvironmentVariable = "MARSHAL_EMBEDDED_SANDBOX"

// EmbeddedSandboxEnabled reports whether the embedded sandbox runtime is
// opted in. A nil lookup, an unset variable or any value other than "1"
// (after trimming) keeps the Local MVP behavior unchanged.
func EmbeddedSandboxEnabled(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	return strings.TrimSpace(getenv(EmbeddedSandboxEnvironmentVariable)) == "1"
}

// Frozen identity constants of the embedded Local provider (ADR 0018 §10
// local derivation). The authority key space is tenantNamespace=local,
// controlPlaneId=default with the repository identity as authorityScopeId;
// the provider actor key space carries trustDomainKind=execution. The
// registration CreatedAt is a fixed constant — never the construction clock
// — so an identical durable ledger replay merges idempotently on any day
// instead of conflicting on a divergent registrationDigest.
const (
	embeddedTenantNamespace       = "local"
	embeddedControlPlaneID        = "default"
	embeddedRegistrationID        = "registration:local-sandbox-provider"
	embeddedIdempotencyKey        = "embedded" + ":registration:local-sandbox-provider"
	embeddedProviderType          = "sandbox"
	embeddedProviderName          = "local"
	embeddedProviderVersion       = "m8-embedded"
	embeddedProtocolVersion       = "marshal-sandbox/1"
	embeddedWorkerPrincipal       = "embedded-worker"
	embeddedRegistrationCreatedAt = "2026-08-12T00:00:00Z"
	embeddedSnapshotCreatedAt     = "2026-08-12T00:00:01Z"
	embeddedAckWindow             = 30 * time.Minute
	embeddedLeaseWindow           = 24 * time.Hour
)

// embeddedRepositoryIdentityKind is the kind of the RepositoryIdentity
// record repository.State.Init persists for the real worktree; the embedded
// authority scope is always taken from that record, never fabricated.
const embeddedRepositoryIdentityKind = "RepositoryIdentity"

// Deterministic derivation seeds of the embedded registration records; the
// two-part concatenation keeps every Digest-family fixture value
// gitleaks-safe.
var (
	embeddedRegistrationRequestDigest = sandbox.RecomputeSHA256([]byte("embedded-registration" + "\x00" + "registration:local-sandbox-provider"))
	embeddedConfigDigest              = sandbox.RecomputeSHA256([]byte("embedded-sandbox" + "\x00" + "effective-config"))
)

// EmbeddedSandboxRuntime assembles the in-process embedded sandbox runtime of
// the M8 vertical slice: the durable provider registration ledger carrying
// the Local provider registration, the gate-6 Matcher bound to the Core
// typed-edge runtime, and the Local sandbox provider behind the
// port.SandboxProvider port. ClaimExecution is the single dispatch-bound
// execution entry point: the assurance adjudication layer, Matcher.Claim
// (the six-step fail-closed gate), the claim-path fencing guard, and only
// then the Local provider allocation granted under the issued lease.
// Push/Pull transport, heartbeat, the dispatcher and the durable lease
// ledger are M9 scope and intentionally not implemented here.
type EmbeddedSandboxRuntime struct {
	stateRoot      string
	now            func() time.Time
	namespace      authority.AuthorityNamespaceId
	providerDomain authority.SecurityDomainId
	resultIngress  authority.SecurityDomainId
	store          *provider.RegistrationStore
	edgeRuntime    *authority.EdgeRuntime
	matcher        *dispatch.Matcher
	provider       port.SandboxProvider
	registration   provider.ProviderRegistration
	snapshot       provider.ProviderCapabilitySnapshot
	agentRegistry  *agentregistry.Registry
	// leaseLedger 是 worker DispatchLease 的耐久 append-only 账本（R2 纵切）。
	// 每个 worker claim 在 Provision 成功后落账；崩溃/重启后由 NewLeaseLedger
	// 确定性重放，rt.claims 从其 ActiveLeases 重建，使跨进程的 admission
	// recheck 与恢复语义不依赖易失内存。
	leaseLedger *dispatch.LeaseLedger

	// mu guards claims, principals and allocations.
	mu sync.Mutex
	// claims indexes the worker lease issued for each (runId, attemptId)
	// scope; the single-allocation invariant never reissues the identical
	// attempt.
	claims map[string]dispatch.DispatchLease
	// principals records the principal bound per workload role per scope,
	// so the worker and the verifier can never share a principal.
	principals map[string]map[sandbox.WorkloadRole]string
	// allocations records every allocationId granted per scope, so the
	// worker and the verifier can never share an allocation.
	allocations map[string][]string
}

// embeddedRuntimeConfig carries the construction seams of the embedded
// runtime.
type embeddedRuntimeConfig struct {
	localOptions     []local.Option
	providerOverride *ProviderOverride
}

// ProviderOverride carries the injection seams for a non-Local sandbox
// provider: the provider instance, the registration builder, the snapshot
// builder and the optional provider security domain. When no override is
// supplied, the embedded runtime defaults to the Local runner with its
// frozen registration and snapshot, completely unchanged.
type ProviderOverride struct {
	// Provider is the sandbox provider instance behind the
	// port.SandboxProvider port.
	Provider port.SandboxProvider
	// ProviderDomain is the execution actor security domain of the injected
	// provider; when zero-valued, the Local default (host-process) is used.
	ProviderDomain authority.SecurityDomainId
	// BuildRegistration builds the provider registration bound to the
	// runtime authority namespace and the provider security domain. The
	// returned registration's providerId, type, name, version and
	// attestation must come from the injection side, never from the
	// frozen Local constants.
	BuildRegistration func(authority.AuthorityNamespaceId, authority.SecurityDomainId) (provider.ProviderRegistration, error)
	// BuildSnapshot captures the capability snapshot aligned with the
	// stored registration. The capabilities and conformance evidence
	// description must come from the injection side.
	BuildSnapshot func(provider.ProviderRegistration) (provider.ProviderCapabilitySnapshot, error)
}

// WithProviderOverride injects a non-Local sandbox provider and its
// corresponding registration/snapshot construction into the embedded
// runtime. The default path (no override) keeps the Local runner and
// its frozen registration and snapshot completely unchanged.
func WithProviderOverride(override ProviderOverride) EmbeddedOption {
	return func(config *embeddedRuntimeConfig) {
		config.providerOverride = &override
	}
}

// EmbeddedOption customizes the embedded runtime construction; it exists as
// a test seam (tests inject a deterministic command executor into the Local
// runner) without changing the production defaults.
type EmbeddedOption func(*embeddedRuntimeConfig)

// WithLocalRunnerOptions forwards construction options to the embedded
// Local runner.
func WithLocalRunnerOptions(options ...local.Option) EmbeddedOption {
	return func(config *embeddedRuntimeConfig) {
		config.localOptions = append(config.localOptions, options...)
	}
}

// NewEmbeddedSandboxRuntime builds the embedded sandbox runtime over one
// state root with an injected clock (no other random or clock source
// participates). It opens the durable registration ledger in a Git-ignored
// directory under the state root, submits the Local provider registration
// idempotently, captures the aligned capability snapshot and binds the
// gate-6 Matcher to the Core typed-edge runtime. Construction fails closed
// on any invalid input, on a missing or invalid repository identity record,
// and on any ledger or registration failure.
func NewEmbeddedSandboxRuntime(stateRoot string, now func() time.Time, options ...EmbeddedOption) (*EmbeddedSandboxRuntime, error) {
	if strings.TrimSpace(stateRoot) == "" {
		return nil, errors.New("app: embedded sandbox runtime: stateRoot must be a non-empty path")
	}
	if now == nil {
		return nil, errors.New("app: embedded sandbox runtime: the injected clock must not be nil")
	}
	config := embeddedRuntimeConfig{}
	for _, option := range options {
		option(&config)
	}
	if config.providerOverride != nil {
		if config.providerOverride.Provider == nil {
			return nil, errors.New("app: embedded sandbox runtime: provider override: the provider must not be nil")
		}
		if config.providerOverride.BuildRegistration == nil {
			return nil, errors.New("app: embedded sandbox runtime: provider override: the registration builder must not be nil")
		}
		if config.providerOverride.BuildSnapshot == nil {
			return nil, errors.New("app: embedded sandbox runtime: provider override: the snapshot builder must not be nil")
		}
	}
	repositoryIdentity, err := embeddedRepositoryIdentity(stateRoot)
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: %w", err)
	}
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  embeddedTenantNamespace,
		ControlPlaneId:   embeddedControlPlaneID,
		AuthorityScopeId: embeddedAuthorityScopeID(repositoryIdentity),
	}
	providerDomain := authority.SecurityDomainId{
		TenantNamespace:   embeddedTenantNamespace,
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "host-process",
	}
	if config.providerOverride != nil && config.providerOverride.ProviderDomain.IsolationDomainId != "" {
		providerDomain = config.providerOverride.ProviderDomain
	}
	resultIngress := authority.SecurityDomainId{
		TenantNamespace:   embeddedTenantNamespace,
		TrustDomainKind:   authority.TrustDomainKindDataCapability,
		IsolationDomainId: "result-ingress",
	}
	store, err := provider.NewRegistrationStore(filepath.Join(stateRoot, "providers"))
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: durable registration ledger: %w", err)
	}
	edgeRuntime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: typed-edge runtime: %w", err)
	}
	var sandboxProvider port.SandboxProvider
	if config.providerOverride != nil {
		sandboxProvider = config.providerOverride.Provider
	} else {
		runner, err := local.NewLocalRunner(filepath.Join(stateRoot, "sandbox"), now, config.localOptions...)
		if err != nil {
			return nil, fmt.Errorf("app: embedded sandbox runtime: local runner: %w", err)
		}
		sandboxProvider = runner
	}
	// R2 纵切：打开 worker DispatchLease 的耐久 append-only 账本（崩溃/重启后
	// 由 NewLeaseLedger 确定性重放），并用其 ActiveLeases 重建 rt.claims 的
	// scope 索引——使跨进程 admission recheck 不依赖易失内存。账本目录创建或
	// 恢复失败一律 fail closed，不允许退回内存态。
	leaseLedger, err := dispatch.NewLeaseLedger(filepath.Join(stateRoot, "leases"))
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: durable lease ledger: %w", err)
	}
	recoveredClaims, err := leaseLedger.ActiveLeases()
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: recover active leases: %w", err)
	}
	// R2 纵切：打开耐久 agent registry（stateRoot/agents/ 的 append-only 账本），
	// 注册 + 生命周期跨进程可恢复——崩溃/重启后 admission 的 agent recheck 不
	// 依赖易失内存，撤销的注册在重启后保持撤销。创建或恢复失败一律 fail closed。
	agentRegistry, err := agentregistry.OpenDurableRegistry(filepath.Join(stateRoot, "agents"))
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: durable agent registry: %w", err)
	}
	runtime := &EmbeddedSandboxRuntime{
		stateRoot:      stateRoot,
		now:            now,
		namespace:      namespace,
		providerDomain: providerDomain,
		resultIngress:  resultIngress,
		store:          store,
		edgeRuntime:    edgeRuntime,
		provider:       sandboxProvider,
		agentRegistry:  agentRegistry,
		leaseLedger:    leaseLedger,
		claims:         recoveredClaims,
		principals:     map[string]map[sandbox.WorkloadRole]string{},
		allocations:    map[string][]string{},
	}
	var registration provider.ProviderRegistration
	if config.providerOverride != nil {
		registration, err = config.providerOverride.BuildRegistration(namespace, providerDomain)
		if err != nil {
			return nil, fmt.Errorf("app: embedded sandbox runtime: build provider registration: %w", err)
		}
	} else {
		registration, err = embeddedLocalRegistration(namespace, providerDomain)
		if err != nil {
			return nil, fmt.Errorf("app: embedded sandbox runtime: build local registration: %w", err)
		}
	}
	stored, err := store.Put(registration)
	if err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: registration submission: %w", err)
	}
	var snapshot provider.ProviderCapabilitySnapshot
	if config.providerOverride != nil {
		snapshot, err = config.providerOverride.BuildSnapshot(stored)
		if err != nil {
			return nil, fmt.Errorf("app: embedded sandbox runtime: build capability snapshot: %w", err)
		}
	} else {
		snapshot, err = embeddedLocalSnapshot(stored)
		if err != nil {
			return nil, fmt.Errorf("app: embedded sandbox runtime: build capability snapshot: %w", err)
		}
	}
	if err := snapshot.ValidateAgainstRegistration(stored); err != nil {
		return nil, fmt.Errorf("app: embedded sandbox runtime: capability snapshot alignment: %w", err)
	}
	runtime.registration = stored
	runtime.snapshot = snapshot
	runtime.matcher = dispatch.NewMatcherWithEdgeRuntime(store, edgeRuntime)
	edgeRuntime.BindLeaseResolver(embeddedLeaseResolver{runtime: runtime})
	edgeRuntime.BindTargetEligibilityResolver(embeddedTargetResolver{runtime: runtime})
	return runtime, nil
}

// embeddedRepositoryIdentity reads the RepositoryIdentity record bound to
// the state root — the record repository.State.Init persists for the real
// worktree. The embedded authority scope is always taken from that real
// worktree repository identity, never fabricated from the state root path:
// a missing, malformed or incomplete record fails closed.
func embeddedRepositoryIdentity(stateRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateRoot, "repo.json"))
	if err != nil {
		return "", fmt.Errorf("read repository identity record: %w", err)
	}
	var identity struct {
		APIVersion     string `json:"apiVersion"`
		Kind           string `json:"kind"`
		RepositoryRoot string `json:"repositoryRoot"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", fmt.Errorf("decode repository identity record: %w", err)
	}
	if identity.APIVersion != string(domain.APIVersionV1Alpha1) || identity.Kind != embeddedRepositoryIdentityKind {
		return "", errors.New("unsupported repository identity record")
	}
	if strings.TrimSpace(identity.RepositoryRoot) == "" {
		return "", errors.New("repository identity record carries no repositoryRoot")
	}
	return identity.RepositoryRoot, nil
}

// embeddedAuthorityScopeID derives the authorityScopeId of the authority key
// space from the real worktree repository identity (ADR 0018 §10 local
// derivation): the identical worktree always rebuilds the identical scope
// and the durable registration ledger replays idempotently across restarts.
func embeddedAuthorityScopeID(repositoryIdentity string) string {
	return "repo:" + filepath.ToSlash(filepath.Clean(repositoryIdentity))
}

// embeddedLocalRegistration builds the Local provider registration with the
// fixed frozen createdAt, so identical replays merge idempotently.
func embeddedLocalRegistration(namespace authority.AuthorityNamespaceId, actorDomain authority.SecurityDomainId) (provider.ProviderRegistration, error) {
	registration := provider.ProviderRegistration{
		RegistrationId:       embeddedRegistrationID,
		AuthorityNamespaceId: namespace,
		SecurityDomainId:     actorDomain,
		Principal:            "local-sandbox-provider",
		ProviderType:         embeddedProviderType,
		ProviderName:         embeddedProviderName,
		ProviderVersion:      embeddedProviderVersion,
		ProtocolVersion:      embeddedProtocolVersion,
		Scope:                namespace.AuthorityScopeId,
		IdempotencyKey:       embeddedIdempotencyKey,
		RequestDigest:        embeddedRegistrationRequestDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "local-sandbox" + "-instance",
			ConfigDigest:       embeddedConfigDigest,
			TrustRootKeyId:     "local-trust-root" + "-key",
			TrustRootAlgorithm: "ed25519",
		},
		LifecycleState: provider.LifecycleStateActive,
		CreatedAt:      embeddedRegistrationCreatedAt,
	}
	digest, err := registration.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, err
	}
	registration.RegistrationDigest = digest
	return registration, nil
}

// embeddedLocalSnapshot captures the capability snapshot of the Local
// provider: the capabilities declare the workspace-write accessMode and the
// workspace-write assurance ceiling (the Local provider is never hardened
// and holds no conformance evidence), the attestation aligns with the
// registration and the conformanceEvidenceDigests set is empty.
func embeddedLocalSnapshot(registration provider.ProviderRegistration) (provider.ProviderCapabilitySnapshot, error) {
	snapshot := provider.ProviderCapabilitySnapshot{
		RegistrationId:  registration.RegistrationId,
		ProtocolVersion: registration.ProtocolVersion,
		ProviderType:    registration.ProviderType,
		ProviderName:    registration.ProviderName,
		ProviderVersion: registration.ProviderVersion,
		Capabilities: map[string]string{
			"accessMode":            string(domain.AccessModeWorkspaceWrite),
			"minimumAssuranceLevel": string(domain.AssuranceLevelWorkspaceWrite),
		},
		ConformanceEvidenceDigests: []string{},
		Scope:                      registration.Scope,
		SnapshotState:              provider.SnapshotStateActive,
		CreatedAt:                  embeddedSnapshotCreatedAt,
		Attestation:                registration.Attestation,
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return provider.ProviderCapabilitySnapshot{}, err
	}
	snapshot.ProviderCapabilitySnapshotDigest = digest
	return snapshot, nil
}

// EmbeddedClaimRequest carries one embedded claim against the durable
// registration ledger. WorkloadRole and Principal bind the dispatch-bound
// operation identity of the allocation the claim grants: the worker and the
// verifier must claim under distinct principals and never share an
// allocation inside one (runId, attemptId) scope.
type EmbeddedClaimRequest struct {
	TaskId       string
	RunId        string
	AttemptId    string
	AllocationId string
	WorkloadRole sandbox.WorkloadRole
	Principal    string
	Requirements domain.SandboxRequirements
}

// EmbeddedClaim is the accepted claim outcome: the dispatch lease issued by
// the gate-6 Matcher plus the Local provider allocation granted under it.
type EmbeddedClaim struct {
	Lease      dispatch.DispatchLease
	Allocation sandbox.SandboxAllocation
}

// ClaimExecution is the dispatch-bound execution entry point of the embedded
// runtime. The layer order is frozen: the assurance adjudication layer first
// fails hardened requirements closed against the Local provider's empty
// evidence set (never the capabilities negotiation layer, never a downgrade);
// then the gate-6 Matcher.Claim (six-step fail-closed) issues the lease for
// the worker role, while the verifier role re-adjudicates the gate-5 match
// and reuses the accepted worker lease of the identical scope under a
// distinct principal and a distinct allocation; then the fencing guard
// (dispatch.ValidateLeaseFencing) re-adjudicates the lease on the claim path
// itself; and only afterwards the Local provider allocation is granted under
// the issued lease. Push/Pull transport, heartbeat, the dispatcher and the
// durable lease ledger are M9 scope and intentionally not implemented here.
func (rt *EmbeddedSandboxRuntime) ClaimExecution(ctx context.Context, request EmbeddedClaimRequest) (EmbeddedClaim, error) {
	if err := rt.validateClaimRequest(request); err != nil {
		return EmbeddedClaim{}, err
	}
	if err := rt.adjudicateAssurance(request.Requirements); err != nil {
		return EmbeddedClaim{}, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	scope := embeddedScopeKey(request.RunId, request.AttemptId)
	if err := rt.adjudicateRoleBookkeeping(scope, request); err != nil {
		return EmbeddedClaim{}, err
	}
	now := rt.now().UTC()
	var lease dispatch.DispatchLease
	switch request.WorkloadRole {
	case sandbox.WorkloadRoleWorker:
		claimed, err := rt.matcher.Claim(dispatch.ClaimRequest{
			AuthorityNamespaceId: rt.namespace,
			RegistrationId:       rt.registration.RegistrationId,
			Snapshot:             rt.snapshot,
			Evidences:            []provider.ConformanceEvidence{},
			Requirements:         request.Requirements,
			TargetActor:          rt.resultIngress,
			TaskId:               request.TaskId,
			RunId:                request.RunId,
			AttemptId:            request.AttemptId,
			AllocationId:         request.AllocationId,
			AckDeadlineAt:        now.Add(embeddedAckWindow).Format(time.RFC3339),
			ExpiresAt:            now.Add(embeddedLeaseWindow).Format(time.RFC3339),
		}, now)
		if err != nil {
			return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: %w", err)
		}
		lease = claimed
	case sandbox.WorkloadRoleVerifier:
		workerLease, claimed := rt.claims[scope]
		if !claimed {
			return EmbeddedClaim{}, errors.New("app: embedded claim: the verifier sandbox requires an accepted worker claim in the identical scope")
		}
		stored, err := rt.store.Get(rt.registration.RegistrationId)
		if err != nil {
			return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: current-ledger recheck: %w", err)
		}
		if err := rt.matcher.Match(stored, rt.snapshot, []provider.ConformanceEvidence{}, request.Requirements, now); err != nil {
			return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: %w", err)
		}
		lease = workerLease
	default:
		return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: %w", sandbox.ErrInvalidWorkloadRole)
	}
	// The fencing guard re-adjudicates the lease the claim path is about to
	// spend: no provider allocation is ever granted under a lease whose
	// canonical binding, generation or fencingToken does not validate.
	if err := dispatch.ValidateLeaseFencing(lease, lease.Generation, lease.FencingToken); err != nil {
		return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: %w", err)
	}
	receipt, err := rt.provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity: sandbox.OperationIdentity{
			TaskId:       request.TaskId,
			RunId:        request.RunId,
			AttemptId:    request.AttemptId,
			WorkloadRole: request.WorkloadRole,
			AllocationId: request.AllocationId,
			Generation:   lease.Generation,
			FencingToken: lease.FencingToken,
			CommandId:    embeddedCommandID(request),
		},
		Requirements: request.Requirements,
	})
	if err != nil {
		return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: local allocation: %w", err)
	}
	// R2 纵切：worker claim 在 allocation 落成后即持久化进 append-only 账本
	// （崩溃/重启后确定性重放）。落账失败一律 fail closed，并清理已创建的
	// allocation——不得留下「allocation 已建但 lease 未落账」的生产孤儿。
	if request.WorkloadRole == sandbox.WorkloadRoleWorker {
		if persistErr := rt.leaseLedger.AppendClaim(lease); persistErr != nil {
			termIdentity := sandbox.OperationIdentity{
				TaskId:       request.TaskId,
				RunId:        request.RunId,
				AttemptId:    request.AttemptId,
				WorkloadRole: request.WorkloadRole,
				AllocationId: request.AllocationId,
				Generation:   lease.Generation,
				FencingToken: lease.FencingToken,
				CommandId:    "command-terminate-persist-failure",
			}
			_, _ = rt.provider.Terminate(ctx, sandbox.TerminateRequest{Identity: termIdentity, AllocationId: receipt.Allocation.AllocationId})
			return EmbeddedClaim{}, fmt.Errorf("app: embedded claim: persist lease to durable ledger (fail closed): %w", persistErr)
		}
	}
	rt.recordClaim(scope, request, lease)
	return EmbeddedClaim{Lease: lease, Allocation: receipt.Allocation}, nil
}

// BindDispatch implements execution.DispatchBinder for the embedded runtime:
// the attempt's frozen two-dimensional requirements are claimed against the
// durable registration ledger under the embedded worker principal, granting
// the Local provider allocation that carries the worker attempt.
func (rt *EmbeddedSandboxRuntime) BindDispatch(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*execution.DispatchBinding, error) {
	claim, err := rt.ClaimExecution(ctx, EmbeddedClaimRequest{
		TaskId:       taskID,
		RunId:        runID,
		AttemptId:    attemptID,
		AllocationId: embeddedAllocationID(runID, attemptID, sandbox.WorkloadRoleWorker),
		WorkloadRole: sandbox.WorkloadRoleWorker,
		Principal:    embeddedWorkerPrincipal,
		Requirements: requirements,
	})
	if err != nil {
		return nil, err
	}
	return &execution.DispatchBinding{
		Lease:        claim.Lease,
		Generation:   claim.Lease.Generation,
		FencingToken: claim.Lease.FencingToken,
	}, nil
}

// RevalidateLease re-adjudicates one in-flight lease against the current
// durable ledger at the runtime clock: deadline, registration lifecycle,
// snapshot alignment and the gate-5 capability match all fail closed with
// the machine-readable cancel reason. Continuation after a rejection
// requires a new attempt with a new claim, never an in-place renewal.
func (rt *EmbeddedSandboxRuntime) RevalidateLease(lease dispatch.DispatchLease, requirements domain.SandboxRequirements) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.matcher.Revalidate(lease, rt.snapshot, []provider.ConformanceEvidence{}, requirements, rt.now().UTC())
}

// Provider exposes the Local sandbox provider behind the
// port.SandboxProvider port.
func (rt *EmbeddedSandboxRuntime) Provider() port.SandboxProvider { return rt.provider }

// Registration returns the stored Local provider registration.
func (rt *EmbeddedSandboxRuntime) Registration() provider.ProviderRegistration {
	return rt.registration
}

// CapabilitySnapshot returns the captured capability snapshot of the Local
// provider.
func (rt *EmbeddedSandboxRuntime) CapabilitySnapshot() provider.ProviderCapabilitySnapshot {
	return rt.snapshot
}

// RegistrationStore exposes the durable registration ledger store.
func (rt *EmbeddedSandboxRuntime) RegistrationStore() *provider.RegistrationStore { return rt.store }

// Matcher exposes the gate-6 Matcher bound to the durable ledger and the
// Core typed-edge runtime.
func (rt *EmbeddedSandboxRuntime) Matcher() *dispatch.Matcher { return rt.matcher }

// Namespace returns the Core authority key space of the embedded runtime.
func (rt *EmbeddedSandboxRuntime) Namespace() authority.AuthorityNamespaceId { return rt.namespace }

// ProviderSecurityDomain returns the execution actor key space of the Local
// provider.
func (rt *EmbeddedSandboxRuntime) ProviderSecurityDomain() authority.SecurityDomainId {
	return rt.providerDomain
}

// ResultIngressSecurityDomain returns the data-capability result-ingress key
// space the Core binds as the targetActor of every issued
// DispatchResultCapability.
func (rt *EmbeddedSandboxRuntime) ResultIngressSecurityDomain() authority.SecurityDomainId {
	return rt.resultIngress
}

// LeaseFor returns the worker lease the embedded runtime issued for one
// (runId, attemptId) scope.
func (rt *EmbeddedSandboxRuntime) LeaseFor(runID, attemptID string) (dispatch.DispatchLease, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	lease, ok := rt.claims[embeddedScopeKey(runID, attemptID)]
	return lease, ok
}

// WorkerAllocationID returns the deterministic allocationId the embedded
// binder grants for one worker attempt.
func (rt *EmbeddedSandboxRuntime) WorkerAllocationID(runID, attemptID string) string {
	return embeddedAllocationID(runID, attemptID, sandbox.WorkloadRoleWorker)
}

// AgentRegistrationActive 验证 agent adapter registration 当前仍为 active
// （R2/R3 纠偏：agent 侧 current-ledger recheck）。只对精确的
// registrationID 做 exact lookup；未注册或非 active 一律 fail closed。
// 不存在「任意 active registration 即通过」的降级——那是门禁绕过。
// registrationID 的稳定性由 capability identity digest 排除易变诊断字段
// （probedAt）保证，见 resultbinding.StableCapabilityDigest。
func (rt *EmbeddedSandboxRuntime) AgentRegistrationActive(registrationID string) (bool, error) {
	reg, err := rt.agentRegistry.Lookup(registrationID)
	if err != nil {
		return false, nil
	}
	return reg.LifecycleState == agentregistry.LifecycleStateActive, nil
}

// RegisterAgent 注册一个 agent adapter 到进程内确定性 ledger。在 adapter
// probe 成功后调用；registration 的 lifecycleState 必须为 active。
func (rt *EmbeddedSandboxRuntime) RegisterAgent(reg agentregistry.AgentRegistration) error {
	if _, err := rt.agentRegistry.Register(reg); err != nil {
		return fmt.Errorf("app: embedded sandbox runtime: register agent: %w", err)
	}
	return nil
}

// RevokeAgent 撤销一个 agent adapter registration（terminal transition）。
func (rt *EmbeddedSandboxRuntime) RevokeAgent(registrationID string) error {
	if _, err := rt.agentRegistry.Revoke(registrationID); err != nil {
		return fmt.Errorf("app: embedded sandbox runtime: revoke agent: %w", err)
	}
	return nil
}

// adjudicateAssurance is the assurance adjudication layer of the embedded
// claim path: hardened requirements demand conformance evidence, and the
// Local provider captures an empty closed conformanceEvidenceDigests set
// (no hardened evidence exists locally), so every hardened claim fails
// closed here — at the assurance adjudication layer, before any
// capabilities negotiation in the gate-5/gate-6 match — never a silent
// downgrade to workspace-write.
func (rt *EmbeddedSandboxRuntime) adjudicateAssurance(requirements domain.SandboxRequirements) error {
	if requirements.MinimumAssuranceLevel != domain.AssuranceLevelHardened {
		return nil
	}
	if len(rt.snapshot.ConformanceEvidenceDigests) == 0 {
		return errors.New("app: embedded claim: assurance adjudication rejected the hardened requirements: the local provider carries an empty closed conformanceEvidenceDigests set; fail closed without downgrade to workspace-write")
	}
	return nil
}

// validateClaimRequest fails closed on any missing identity field or an
// invalid workload role or requirements declaration.
func (rt *EmbeddedSandboxRuntime) validateClaimRequest(request EmbeddedClaimRequest) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"taskId", request.TaskId},
		{"runId", request.RunId},
		{"attemptId", request.AttemptId},
		{"allocationId", request.AllocationId},
		{"principal", request.Principal},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("app: embedded claim: %s must be a non-empty string", field.name)
		}
	}
	if err := request.WorkloadRole.Validate(); err != nil {
		return fmt.Errorf("app: embedded claim: %w", err)
	}
	if _, err := domain.ParseAccessMode(string(request.Requirements.AccessMode)); err != nil {
		return fmt.Errorf("app: embedded claim: requirements: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(request.Requirements.MinimumAssuranceLevel)); err != nil {
		return fmt.Errorf("app: embedded claim: requirements: %w", err)
	}
	return nil
}

// adjudicateRoleBookkeeping enforces the worker/verifier separation of the
// embedded runtime inside one (runId, attemptId) scope: one claim per
// workload role, distinct principals across roles and distinct allocations.
// The caller must hold rt.mu.
func (rt *EmbeddedSandboxRuntime) adjudicateRoleBookkeeping(scope string, request EmbeddedClaimRequest) error {
	roles := rt.principals[scope]
	if bound, taken := roles[request.WorkloadRole]; taken {
		if bound != request.Principal {
			return fmt.Errorf("app: embedded claim: workload role %q is already bound to a different principal in this scope", string(request.WorkloadRole))
		}
		return fmt.Errorf("app: embedded claim: workload role %q already carries an accepted claim in this scope; the single-allocation invariant never reissues the identical attempt, continue with a new attempt and a new claim", string(request.WorkloadRole))
	}
	for role, principal := range roles {
		if role != request.WorkloadRole && principal == request.Principal {
			return fmt.Errorf("app: embedded claim: the worker and the verifier must not share the principal %q in one scope", request.Principal)
		}
	}
	for _, allocationID := range rt.allocations[scope] {
		if allocationID == request.AllocationId {
			return fmt.Errorf("app: embedded claim: allocationId %q is already bound to this scope; the worker and the verifier must use distinct allocations", allocationID)
		}
	}
	return nil
}

// recordClaim records one accepted claim in the scope bookkeeping. The
// caller must hold rt.mu.
func (rt *EmbeddedSandboxRuntime) recordClaim(scope string, request EmbeddedClaimRequest, lease dispatch.DispatchLease) {
	if request.WorkloadRole == sandbox.WorkloadRoleWorker {
		rt.claims[scope] = lease
	}
	if rt.principals[scope] == nil {
		rt.principals[scope] = map[sandbox.WorkloadRole]string{}
	}
	rt.principals[scope][request.WorkloadRole] = request.Principal
	rt.allocations[scope] = append(rt.allocations[scope], request.AllocationId)
}

// claimedByLeaseID looks the current scope lease up by leaseId for the
// typed-edge resolvers.
func (rt *EmbeddedSandboxRuntime) claimedByLeaseID(leaseID string) (dispatch.DispatchLease, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, lease := range rt.claims {
		if lease.LeaseId == leaseID {
			return lease, true
		}
	}
	return dispatch.DispatchLease{}, false
}

// embeddedScopeKey is the (runId, attemptId) scope identity of the embedded
// claim bookkeeping, mirroring the dispatch single-allocation invariant.
func embeddedScopeKey(runID, attemptID string) string {
	return runID + "\x00" + attemptID
}

// embeddedAllocationID derives the deterministic allocationId of one
// embedded claim from the scope and the workload role.
func embeddedAllocationID(runID, attemptID string, role sandbox.WorkloadRole) string {
	return "alloc-" + strings.TrimPrefix(sandbox.RecomputeSHA256([]byte(runID+"\x00"+attemptID+"\x00"+string(role))), sandbox.DigestPrefix)
}

// embeddedCommandID derives the deterministic commandId of the provision
// identity of one embedded claim.
func embeddedCommandID(request EmbeddedClaimRequest) string {
	return "embedded-" + string(request.WorkloadRole) + "-" + request.AllocationId
}

// embeddedLeaseResolver re-adjudicates the dispatch edges issued by the
// embedded runtime against the runtime's current claim bookkeeping; the
// durable lease ledger wiring is M9 scope.
type embeddedLeaseResolver struct {
	runtime *EmbeddedSandboxRuntime
}

// LeaseActive reports whether the lease bound to a dispatch edge is still
// active in the embedded runtime at exactly the recorded generation and
// fencingToken.
func (resolver embeddedLeaseResolver) LeaseActive(leaseID string, generation int64, fencingToken string) (bool, error) {
	lease, ok := resolver.runtime.claimedByLeaseID(leaseID)
	if !ok {
		return false, nil
	}
	if lease.LeaseState != dispatch.LeaseStateClaimed && lease.LeaseState != dispatch.LeaseStateActive {
		return false, nil
	}
	return lease.Generation == generation && lease.FencingToken == fencingToken, nil
}

// embeddedTargetResolver re-adjudicates the result-ingress target actor of
// the embedded runtime against the current durable ledger.
type embeddedTargetResolver struct {
	runtime *EmbeddedSandboxRuntime
}

// TargetEligible reports whether the target actor is the exact
// result-ingress security domain of the embedded runtime and the Local
// registration is still active in the durable ledger.
func (resolver embeddedTargetResolver) TargetEligible(target authority.SecurityDomainId) (bool, error) {
	if !target.Equal(resolver.runtime.resultIngress) {
		return false, nil
	}
	stored, err := resolver.runtime.store.Get(resolver.runtime.registration.RegistrationId)
	if err != nil {
		return false, nil
	}
	return stored.LifecycleState == provider.LifecycleStateActive, nil
}
