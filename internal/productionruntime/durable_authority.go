package productionruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// CompositionLedger implements DurableRunAuthority over the RB1 Run store and
// the ResultIngress authority ledger. Every method requires the exact current
// owner lock; the ledger itself holds no identity beyond the composition
// inputs frozen by the fixed CLI at construction.
type CompositionLedger struct {
	ingress            *resultingress.DurableStore
	ownsIngress        bool
	runs               *runstore.Store
	runLease           *runstore.Lease
	ownsRunLease       bool
	leaseLedger        *dispatch.LeaseLedger
	providerStore      *provider.RegistrationStore
	providerRecord     provider.ProviderRegistration
	providerSnapshot   provider.ProviderCapabilitySnapshot
	providerEvidence   []provider.ConformanceEvidence
	resultTarget       authority.SecurityDomainId
	matcher            *dispatch.Matcher
	resultCapabilities map[string]authority.DispatchResultCapability
	runReady           *runstore.AttemptRunAuthorityVerifier
	namespace          authority.AuthorityNamespaceId
	orchestrator       string
	provisionDomain    authority.SecurityDomainId
	cleanupDomain      authority.SecurityDomainId
	registration       string
	snapshot           string
	conformance        []string
	attestation        provider.Attestation
	allocationDir      string
	owner              repositoryOwnerLock
	closure            launchidentity.ClosureV1
	requirements       allocationcontrol.SandboxRequirementsV1
	workDirs           []string
	environment        []string
	allocation         *resultingress.AllocationAuthority
	// existingWorktreeGraph/existingWorktreeTarget are borrowed from the
	// fixed CLI for the duration of ComposeRuntime; they are never reopened
	// by pathname and never persist into authority facts.
	existingWorktreeGraph    allocationcontrol.ExistingWorktreeDescriptorGraphV1
	existingWorktreeTarget   *os.File
	existingWorktreeEnabled  bool
	launchArgvBuilder        AttemptLaunchArgvBuilder
	resultParser             AttemptResultParser
	entryLocalSelfIdentity   *selfidentity.LocalSelfIdentityObservationV2
	observeLocalSelfIdentity LocalSelfIdentityObserver
	now                      func() time.Time
}

// LocalSelfIdentityObserver is the Core-owned fresh observation seam used by
// the Darwin local-dogfood production composition. It deliberately returns a
// typed observation rather than raw evidence so the runtime can compare and
// persist one closed identity subject at dispatch and result ingress.
type LocalSelfIdentityObserver func() (selfidentity.LocalSelfIdentityObservationV2, error)

// CompositionInputs freezes the composition-time identity and location
// decisions. Nothing here is derivable from the durable ledger; everything is
// validated before the first authority call.
type CompositionInputs struct {
	Ingress        *resultingress.DurableStore
	Runs           *runstore.Store
	RunLease       *runstore.Lease
	LeaseLedger    *dispatch.LeaseLedger
	OwnerDirectory *os.File
	Acquisition    resultingress.ControlOwnerAcquisition
	RunID          string
	// HeldIngressDir, FixedMarshalPath and OwnerPrivateControlRoot select the
	// sealed Darwin fresh-start composition: the ingress is opened from the
	// held descriptor and sealed in place for the exact fixed marshal image.
	// When nil, the composition falls back to the generic store input.
	HeldIngressDir          *os.File
	FixedMarshalPath        string
	OwnerPrivateControlRoot *os.File
	Namespace               authority.AuthorityNamespaceId
	OrchestratorID          string
	ProvisionDomain         authority.SecurityDomainId
	CleanupDomain           authority.SecurityDomainId
	RegistrationID          string
	CapabilitySnapshot      string
	ConformanceEvidence     []string
	Attestation             provider.Attestation
	// ProviderStore and the typed provider records are mandatory for the
	// existing-worktree production path. They drive ClaimReserved against the
	// durable registration ledger; digest-only compatibility fields above are
	// retained solely for the legacy staging test path.
	ProviderStore        *provider.RegistrationStore
	ProviderRegistration provider.ProviderRegistration
	ProviderSnapshot     provider.ProviderCapabilitySnapshot
	ProviderEvidence     []provider.ConformanceEvidence
	ResultIngressDomain  authority.SecurityDomainId
	AllocationRoot       string
	LaunchClosure        launchidentity.ClosureV1
	Requirements         allocationcontrol.SandboxRequirementsV1
	WorkDirAllowlist     []string
	EnvironmentAllowlist []string
	// ExistingWorktreeDescriptorGraph and ExistingWorktreeTargetWorktree
	// select path B (existing-worktree binding, ADR 0069/0070). Both must be
	// non-zero to enable path B; supplying only one fails closed. The graph
	// and target descriptor are held by the fixed CLI for the full
	// ComposeRuntime/Prepare/Start lifetime and only borrowed here.
	ExistingWorktreeDescriptorGraph allocationcontrol.ExistingWorktreeDescriptorGraphV1
	ExistingWorktreeTargetWorktree  *os.File
	// LaunchArgvBuilder is the injected, pure constructor for the deterministic
	// noninteractive production argv. It is the only seam that may bridge to
	// the pi adapter; productionruntime calls it after ReserveAttempt and
	// ensureAttemptLease have fixed the precise TaskID/RunID/AttemptID, then
	// re-seals the launch closure before launch-authorized/PreparedExecution.
	// Path B requires a non-nil builder and fails closed without one; path A
	// tolerates nil for backward compatibility (the composition-time argv is
	// kept and only the working directory is re-sealed).
	LaunchArgvBuilder AttemptLaunchArgvBuilder
	// ResultParser is the adapter-owned strict transcript parser used only
	// after descriptor-validated supervisor collection.
	ResultParser AttemptResultParser
	// EntryLocalSelfIdentity and ObserveLocalSelfIdentity are a closed pair.
	// Both are nil outside darwin-local-dogfood. When present, PrepareRunStart
	// persists a fresh dispatch observation before StartPreparedRun and result
	// collection persists a fresh ingress observation before ResultIngress.
	EntryLocalSelfIdentity   *selfidentity.LocalSelfIdentityObservationV2
	ObserveLocalSelfIdentity LocalSelfIdentityObserver
}

// NewCompositionLedger is the only constructor. It validates every input and
// wires the Core provision/cleanup verifiers, which authorize the composition
// itself as the local SandboxProvider authority.
//
// Lock order (ADR 0069 §3.3/§4): repository owner → Run Lease → RB1. Path B
// (existing-worktree binding) acquires the Run Lease inside this constructor,
// after the two-phase repository owner acquisition, and rejects a caller-
// supplied lease so the order cannot be re-violated. Path A (staging
// provision) keeps accepting the caller-supplied lease for tests/compat.
func NewCompositionLedger(ctx context.Context, inputs CompositionInputs) (*CompositionLedger, error) {
	if inputs.HeldIngressDir != nil {
		if inputs.FixedMarshalPath == "" || inputs.OwnerPrivateControlRoot == nil {
			return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
		}
		held, err := resultingress.OpenDarwinResultIngressStore(inputs.HeldIngressDir)
		if err != nil {
			return nil, err
		}
		inputs.Ingress = held
	}
	ownsIngress := inputs.HeldIngressDir != nil
	if inputs.Ingress == nil || inputs.Runs == nil || inputs.LeaseLedger == nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	localIdentityConfigured := inputs.EntryLocalSelfIdentity != nil || inputs.ObserveLocalSelfIdentity != nil
	if localIdentityConfigured && (inputs.EntryLocalSelfIdentity == nil || inputs.ObserveLocalSelfIdentity == nil || selfidentity.ValidateObservation(*inputs.EntryLocalSelfIdentity) != nil) {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	if inputs.Namespace.Validate() != nil || inputs.ProvisionDomain.Validate() != nil || inputs.CleanupDomain.Validate() != nil ||
		inputs.ProvisionDomain == inputs.CleanupDomain ||
		inputs.OwnerDirectory == nil || validateCompositionAcquisitionCandidate(inputs.Acquisition) != nil || inputs.RunID == "" ||
		inputs.OrchestratorID == "" || inputs.RegistrationID == "" || inputs.AllocationRoot == "" ||
		inputs.CapabilitySnapshot == "" || inputs.LaunchClosure.Validate() != nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	// Path B (existing-worktree binding) requires both the held descriptor
	// graph and the held target worktree descriptor. A single-side config
	// fails closed: it must not silently fall back to staging provision.
	graphSupplied := inputs.ExistingWorktreeDescriptorGraph.FilesystemRoot != nil
	targetSupplied := inputs.ExistingWorktreeTargetWorktree != nil
	if graphSupplied != targetSupplied {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	existingWorktreeEnabled := graphSupplied && targetSupplied
	// Path B requires the injected production argv builder: the launch closure
	// must be re-sealed with the deterministic noninteractive argv after the
	// precise attempt identity is reserved, and productionruntime cannot import
	// adapter/pi. A nil builder fails closed so path B never launches with the
	// bare composition-time kernel argv. Path A keeps nil compatibility.
	if existingWorktreeEnabled && inputs.LaunchArgvBuilder == nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	if existingWorktreeEnabled {
		if inputs.ProviderStore == nil || inputs.ProviderRegistration.Validate() != nil ||
			inputs.ProviderSnapshot.ValidateAgainstRegistration(inputs.ProviderRegistration) != nil ||
			inputs.ResultIngressDomain.Validate() != nil ||
			inputs.ProviderRegistration.RegistrationId != inputs.RegistrationID ||
			inputs.ProviderSnapshot.ProviderCapabilitySnapshotDigest != inputs.CapabilitySnapshot {
			return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
		}
		stored, err := inputs.ProviderStore.Get(inputs.ProviderRegistration.RegistrationId)
		if err != nil || stored != inputs.ProviderRegistration {
			return nil, application.NewError("composition-ledger", application.ReasonAuthorityConflict)
		}
	}
	// ADR 0069 lock order: path B must acquire the Run Lease inside this
	// constructor, after repository owner acquisition. A caller-supplied lease
	// would re-violate the order (the CLI used to hold it across ComposeRuntime)
	// and is rejected. Path A keeps accepting the caller-supplied lease.
	if existingWorktreeEnabled {
		if inputs.RunLease != nil {
			return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
		}
	} else if inputs.RunLease == nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	// The allocation root is composition-owned infrastructure: create it
	// eagerly so the per-attempt staging store can open inside it.
	if err := os.MkdirAll(inputs.AllocationRoot, 0o700); err != nil {
		return nil, err
	}
	// Two-phase repository owner acquisition FIRST (path B lock order). The
	// phase-A scope lock admits exactly one owner append, the bound phase-B
	// lock carries the runtime claim, and closing the spent phase-A handle
	// never releases phase B.
	phase, err := openRepositoryOwnerScopeLock(inputs.OwnerDirectory, inputs.Acquisition.Scope)
	if err != nil {
		return nil, err
	}
	ownerState, acquisition, err := acquireOwner(inputs.Ingress, phase, inputs.Acquisition)
	if err != nil {
		_ = phase.Close()
		return nil, err
	}
	inputs.Acquisition = acquisition
	owner, err := phase.bindAcquisition(inputs.Ingress)
	if err != nil {
		_ = phase.Close()
		return nil, err
	}
	if err := phase.Close(); err != nil {
		_ = owner.Close()
		return nil, err
	}
	// releaseConstruction reverses the acquisition order (Run Lease → owner →
	// result-ingress) for any failure after this point. It only releases Run
	// Lease / result-ingress the ledger owns; a caller-supplied lease or store
	// (path A) stays with the caller.
	var runLease *runstore.Lease
	ownsRunLease := false
	releaseConstruction := func() {
		if ownsRunLease && runLease != nil {
			_ = runLease.Release()
		}
		_ = owner.Close()
		if ownsIngress {
			_ = inputs.Ingress.Close()
		}
	}
	// The seal consumes the held store in place behind the current owner and
	// observes the exact fixed marshal image inside that authority window. It
	// runs before the Run Lease is acquired, under the owner lock only.
	if inputs.HeldIngressDir != nil {
		// The physical phase-B lock is held by this call stack; the borrowed
		// verifier carries that fact without requiring the runtime claim that
		// newProductionRuntime performs later.
		borrowed := &borrowedOwnerVerifier{acquisition: inputs.Acquisition, active: true}
		if _, err := resultingress.SealPi0844DarwinPreparedExecutionStore(ctx, inputs.Ingress, borrowed, resultingress.CurrentOwnerBinding{Scope: inputs.Acquisition.Scope, OwnerEpoch: inputs.Acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: ownerState.FactDigest}, inputs.FixedMarshalPath, inputs.OwnerPrivateControlRoot); err != nil {
			releaseConstruction()
			return nil, err
		}
		borrowed.close()
	}
	// Run Lease: path B acquires it here, after repository owner acquisition
	// (ADR 0069). Path A borrows the caller-supplied lease. Any failure
	// releases the newly-acquired lease and the owner in reverse order.
	if existingWorktreeEnabled {
		runLease, err = inputs.Runs.AcquireExisting(inputs.RunID)
		if err != nil {
			releaseConstruction()
			return nil, err
		}
		ownsRunLease = true
	} else {
		runLease = inputs.RunLease
	}
	runReady, err := runstore.NewAttemptRunAuthorityVerifier(inputs.Runs, runLease, inputs.Namespace, inputs.OrchestratorID)
	if err != nil {
		releaseConstruction()
		return nil, err
	}
	var entryLocalSelfIdentity *selfidentity.LocalSelfIdentityObservationV2
	if inputs.EntryLocalSelfIdentity != nil {
		entry := *inputs.EntryLocalSelfIdentity
		entryLocalSelfIdentity = &entry
	}
	ledger := &CompositionLedger{
		ingress: inputs.Ingress, ownsIngress: ownsIngress, runs: inputs.Runs, runLease: runLease, ownsRunLease: ownsRunLease,
		leaseLedger: inputs.LeaseLedger, runReady: runReady, namespace: inputs.Namespace, orchestrator: inputs.OrchestratorID,
		provisionDomain: inputs.ProvisionDomain, cleanupDomain: inputs.CleanupDomain,
		registration: inputs.RegistrationID, snapshot: inputs.CapabilitySnapshot,
		conformance: append([]string(nil), inputs.ConformanceEvidence...), attestation: inputs.Attestation,
		providerStore: inputs.ProviderStore, providerRecord: inputs.ProviderRegistration,
		providerSnapshot: inputs.ProviderSnapshot, providerEvidence: append([]provider.ConformanceEvidence(nil), inputs.ProviderEvidence...),
		resultTarget: inputs.ResultIngressDomain, resultCapabilities: map[string]authority.DispatchResultCapability{},
		allocationDir: inputs.AllocationRoot, closure: inputs.LaunchClosure,
		requirements: inputs.Requirements, workDirs: append([]string(nil), inputs.WorkDirAllowlist...),
		environment:           append([]string(nil), inputs.EnvironmentAllowlist...),
		existingWorktreeGraph: inputs.ExistingWorktreeDescriptorGraph, existingWorktreeTarget: inputs.ExistingWorktreeTargetWorktree,
		existingWorktreeEnabled: existingWorktreeEnabled, launchArgvBuilder: inputs.LaunchArgvBuilder, now: time.Now,
		resultParser: inputs.ResultParser, entryLocalSelfIdentity: entryLocalSelfIdentity,
		observeLocalSelfIdentity: inputs.ObserveLocalSelfIdentity,
	}
	if existingWorktreeEnabled {
		edges, edgeErr := authority.NewEdgeRuntime(inputs.Namespace)
		if edgeErr != nil {
			releaseConstruction()
			return nil, edgeErr
		}
		edges.BindLeaseResolver(compositionLeaseResolver{ledger: inputs.LeaseLedger})
		edges.BindTargetEligibilityResolver(compositionTargetResolver{store: inputs.ProviderStore, registrationID: inputs.ProviderRegistration.RegistrationId, target: inputs.ResultIngressDomain})
		ledger.matcher = dispatch.NewMatcherWithReservedClaimLedger(inputs.ProviderStore, edges, inputs.LeaseLedger)
	}
	allocation, err := resultingress.NewAllocationAuthority(inputs.Ingress, compositionAllocationAuthority{ledger: ledger, domain: inputs.ProvisionDomain, phase: resultingress.EffectPhaseAllocationProvision}, compositionAllocationAuthority{ledger: ledger, domain: inputs.CleanupDomain, phase: resultingress.EffectPhaseAllocationTerminate})
	if err != nil {
		releaseConstruction()
		return nil, err
	}
	ledger.allocation = allocation
	ledger.owner = owner
	// A newly acquired owner must reconcile an already-RUNNING attempt before
	// the Runtime becomes visible. This is the single production caller for
	// ADR 0067 Attach/rebind; Status remains a read-only projection.
	recoveryVerifier := &borrowedOwnerVerifier{acquisition: inputs.Acquisition, active: true}
	if err := ledger.recoverRunningAttempt(ctx, recoveryVerifier, inputs.Acquisition, ownerState); err != nil {
		recoveryVerifier.close()
		releaseConstruction()
		return nil, err
	}
	recoveryVerifier.close()
	return ledger, nil
}

// validateCompositionAcquisitionCandidate admits OwnerEpoch=0 only at the
// fixed composition boundary. Phase A replaces it with the exact next durable
// epoch before any authority append; ResultIngress never accepts zero.
func validateCompositionAcquisitionCandidate(acquisition resultingress.ControlOwnerAcquisition) error {
	if acquisition.OwnerEpoch == 0 {
		acquisition.OwnerEpoch = 1
	}
	return acquisition.Validate()
}

func acquireOwner(ingress *resultingress.DurableStore, phase repositoryOwnerScopeLock, acquisition resultingress.ControlOwnerAcquisition) (resultingress.ControlOwnerState, resultingress.ControlOwnerAcquisition, error) {
	prior, found, err := ingress.OpenOwner(acquisition.Scope)
	if err != nil {
		return resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, err
	}
	epoch, previousDigest := uint64(0), ""
	if found {
		epoch, previousDigest = prior.Acquisition.OwnerEpoch, prior.FactDigest
	}
	nextEpoch := epoch + 1
	if acquisition.OwnerEpoch != 0 && acquisition.OwnerEpoch != nextEpoch {
		return resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, application.NewError("composition-owner", application.ReasonOwnerNotCurrent)
	}
	acquisition.OwnerEpoch = nextEpoch
	if _, err := phase.acquireOwner(context.Background(), ingress, epoch, previousDigest, acquisition); err != nil {
		return resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, err
	}
	state, found, err := ingress.OpenOwner(acquisition.Scope)
	if err != nil || !found || state.Acquisition != acquisition {
		return resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, fmt.Errorf("composition: owner replay after acquire: found=%t err=%v", found, err)
	}
	return state, acquisition, nil
}

// Close releases the resources the ledger owns in reverse acquisition order
// (Run Lease → result-ingress → owner, ADR 0069). Path A borrows the Run Lease
// and result-ingress from the caller, so only the bound owner lock is released
// here; the Runtime closes the borrowed resources itself. Path B acquires the
// Run Lease (and, for the held ingress, the result-ingress) inside the
// constructor, so Close releases them.
func (l *CompositionLedger) Close() error {
	if l == nil {
		return nil
	}
	var errs []error
	if l.ownsRunLease && l.runLease != nil {
		errs = append(errs, l.runLease.Release())
	}
	if l.ownsIngress && l.ingress != nil {
		errs = append(errs, l.ingress.Close())
	}
	if l.owner != nil {
		errs = append(errs, l.owner.Close())
	}
	return errors.Join(errs...)
}

// CurrentOwner resolves the exact current owner and derives PendingRecovery
// from the RB1 Run journal: a RUNNING run means an attempt is in flight
// without a terminal fact, which is exactly ADR 0056's recovery condition.
func (l *CompositionLedger) CurrentOwner(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition) (OwnerProjection, error) {
	var projection OwnerProjection
	err := verifier.WithCurrentOwnerLock(ctx, acquisition, func() error {
		state, found, err := l.ingress.OpenOwner(acquisition.Scope)
		if err != nil {
			return err
		}
		if !found || state.Acquisition != acquisition {
			return application.NewError("current-owner", application.ReasonOwnerNotCurrent)
		}
		pending, err := l.pendingRecovery(state)
		if err != nil {
			return err
		}
		projection = OwnerProjection{OwnerEpoch: state.Acquisition.OwnerEpoch, OwnerFactDigest: state.FactDigest, PendingRecovery: pending}
		return nil
	})
	if err != nil {
		return OwnerProjection{}, err
	}
	return projection, nil
}

func (l *CompositionLedger) pendingRecovery(owner resultingress.ControlOwnerState) (uint64, error) {
	_, attempt, running, err := l.currentRunningAttempt(context.Background())
	if err != nil {
		return 0, err
	}
	if running && !runningAttemptBoundToOwner(attempt, owner) && !runningAttemptReadyForCloseRecovery(attempt, owner) {
		return 1, nil
	}
	return 0, nil
}

// currentRunningAttempt joins the RB1 Run projection to exactly one
// non-terminal ResultIngress Attempt. It never reconstructs an AttemptIdentity
// from partial caller data and fails closed on zero or ambiguous matches.
func (l *CompositionLedger) currentRunningAttempt(ctx context.Context) (runstore.RunStartAuthorityProjection, resultingress.AttemptAuthorityState, bool, error) {
	read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
	if err != nil {
		return runstore.RunStartAuthorityProjection{}, resultingress.AttemptAuthorityState{}, false, err
	}
	if read.Run.State != domain.StateRunning {
		return read, resultingress.AttemptAuthorityState{}, false, nil
	}
	// Include a cleanup-released Attempt until the Run journal records
	// worker.completed. A crash at that final boundary must replay the exact
	// terminal Attempt instead of making the still-RUNNING Run unjoinable.
	states, err := l.ingress.AttemptStates()
	if err != nil {
		return runstore.RunStartAuthorityProjection{}, resultingress.AttemptAuthorityState{}, false, err
	}
	var match resultingress.AttemptAuthorityState
	matches := 0
	for _, state := range states {
		identity := state.Identity
		if identity.AuthorityNamespaceID == l.namespace && identity.OrchestratorID == l.orchestrator && identity.TaskID == read.Run.TaskID && identity.RunID == read.Run.RunID && identity.AttemptID == read.Run.AttemptID {
			match = state
			matches++
		}
	}
	if matches != 1 {
		return runstore.RunStartAuthorityProjection{}, resultingress.AttemptAuthorityState{}, false, application.NewError("recover-running-attempt", application.ReasonAuthorityConflict)
	}
	return read, match, true, nil
}

func runningAttemptBoundToOwner(attempt resultingress.AttemptAuthorityState, owner resultingress.ControlOwnerState) bool {
	return attempt.ProcessStartedDigest != "" && attempt.SupervisorStartedDigest != "" &&
		attempt.SupervisorPendingIntentDigest == "" && attempt.SupervisorInterventionDigest == "" && attempt.SupervisorClosedDigest == "" &&
		attempt.Owner.OwnerEpoch == owner.Acquisition.OwnerEpoch && attempt.Owner.ControlOwnerAcquiredFactDigest == owner.FactDigest &&
		attempt.SupervisorBoundAuthorityHead == attempt.HeadDigest
}

func runningAttemptReadyForCloseRecovery(attempt resultingress.AttemptAuthorityState, owner resultingress.ControlOwnerState) bool {
	if attempt.ProcessTerminalDigest == "" || attempt.AllocationTerminalDigest == "" || attempt.SupervisorClosedDigest != "" || attempt.SupervisorInterventionDigest != "" || attempt.Owner.OwnerEpoch != owner.Acquisition.OwnerEpoch || attempt.Owner.ControlOwnerAcquiredFactDigest != owner.FactDigest || attempt.HeadDigest != attempt.ControlOwnerBindingDigest {
		return false
	}
	return resultingress.AttemptCloseRecoveryRecorded(attempt)
}

// RehydratePreparedRunStart re-projects a durable preparation by digest under
// the current owner lock.
func (l *CompositionLedger) RehydratePreparedRunStart(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, digest string) (application.PreparedRunStart, error) {
	return l.ingress.PrepareMacRunStart(ctx, verifier, acquisition, digest)
}

// RehydrateRunStartOutcome reports whether the prepared execution already
// committed its sealed run.start-outcome successor.
func (l *CompositionLedger) RehydrateRunStartOutcome(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, digest string) (application.RunProjection, bool, error) {
	read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
	if err != nil {
		return application.RunProjection{}, false, err
	}
	if read.Run.State == domain.StateRunning && read.PreparationDigest == digest {
		return read.Run, true, nil
	}
	return application.RunProjection{}, false, nil
}

// InspectRun projects the current Run state under the held lease.
func (l *CompositionLedger) InspectRun(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, request application.InspectRunRequest) (application.RunProjection, error) {
	var projection application.RunProjection
	err := verifier.WithCurrentOwnerLock(ctx, acquisition, func() error {
		read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
		if err != nil {
			return err
		}
		if read.Run.RunID != request.RunID {
			return application.NewError("inspect-run", application.ReasonAuthorityConflict)
		}
		projection = read.Run
		return nil
	})
	if err != nil {
		return application.RunProjection{}, err
	}
	return projection, nil
}

// WithCurrentRunAuthority implements resultingress.CurrentRunAuthorityVerifier
// over the held Run lease: the binding must name the composed namespace,
// orchestrator and the exact current READY/RUNNING authority head.
func (l *CompositionLedger) WithCurrentRunAuthority(ctx context.Context, binding resultingress.RunAuthorityBinding, fn func() error) error {
	if binding.AuthorityNamespaceID != l.namespace || binding.OrchestratorID != l.orchestrator {
		return resultingress.ErrAttemptAuthorityConflict
	}
	return l.runReady.WithCurrentRunAuthority(ctx, binding, fn)
}

// compositionAllocationAuthority is one Core-side allocation authority. The
// provision and cleanup authorities must stay distinct identity objects, so
// each phase receives its own security domain through its own wrapper.
type compositionAllocationAuthority struct {
	ledger *CompositionLedger
	domain authority.SecurityDomainId
	phase  resultingress.EffectPhase
}

func (a compositionAllocationAuthority) WithCurrentAllocationProvision(ctx context.Context, check resultingress.AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if a.phase != resultingress.EffectPhaseAllocationProvision || check.Phase != resultingress.EffectPhaseAllocationProvision {
		return resultingress.ErrAllocationAuthorityConflict
	}
	if err := a.ledger.verifyAllocationCheck(ctx, check); err != nil {
		return err
	}
	return fn(a.domain)
}

func (a compositionAllocationAuthority) WithCurrentAllocationCleanup(ctx context.Context, check resultingress.AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if a.phase != resultingress.EffectPhaseAllocationTerminate || check.Phase != resultingress.EffectPhaseAllocationTerminate {
		return resultingress.ErrAllocationAuthorityConflict
	}
	if err := a.ledger.verifyAllocationCheck(ctx, check); err != nil {
		return err
	}
	return fn(a.domain)
}

func (l *CompositionLedger) verifyAllocationCheck(ctx context.Context, check resultingress.AllocationAuthorityCheck) error {
	binding := resultingress.RunAuthorityBinding{AuthorityNamespaceID: l.namespace, RunID: check.Identity.RunID, OrchestratorID: l.orchestrator, RunAuthorityDigest: check.Identity.RunAuthorityDigest}
	return l.WithCurrentRunAuthority(ctx, binding, func() error { return nil })
}

// replayGateAccepts is the pure closed-XOR decision for the PrepareRunStart
// replay gate (ADR 0069/0070). It performs no ledger mutation. Path A requires
// the staging provision effect+receipt complete and every existing-worktree
// bind/release field empty. Path B requires the bind intent fact, bind receipt
// fact and bind receipt digest complete, no release intent/receipt, a present
// reservation, and every allocation provision effect/receipt field empty.
// Exactly one path may be complete. Both require launch authorized at the
// current head with no pending effect intent.
func replayGateAccepts(current resultingress.AttemptAuthorityState) bool {
	pathAComplete := current.AllocationProvisionEffectDigest != "" && current.AllocationProvisionReceiptDigest != "" &&
		current.ExistingWorktreeBindIntentFactDigest == "" && current.ExistingWorktreeBindReceiptFactDigest == "" && current.ExistingWorktreeBindReceiptDigest == "" &&
		current.ExistingWorktreeReleaseIntentFactDigest == "" && current.ExistingWorktreeReleaseReceiptFactDigest == ""
	pathBComplete := current.ExistingWorktreeBindIntentFactDigest != "" && current.ExistingWorktreeBindReceiptFactDigest != "" && current.ExistingWorktreeBindReceiptDigest != "" &&
		current.ExistingWorktreeReleaseIntentFactDigest == "" && current.ExistingWorktreeReleaseReceiptFactDigest == "" &&
		current.ReservationFactDigest != "" &&
		current.AllocationProvisionEffectDigest == "" && current.AllocationProvisionReceiptDigest == ""
	exactlyOne := pathAComplete != pathBComplete
	common := current.LaunchAuthorizedDigest != "" && current.HeadDigest == current.LaunchAuthorizedDigest && current.PendingEffectIntentFactDigest == ""
	return exactlyOne && common
}

// PrepareRunStart drives the complete S2 producer chain under the current
// owner lock: READY read-back, creation-once reservation, attempt lease
// minting, fresh v2 open, durable local allocation provision, launch
// authorization with the frozen launch closure and prepared-execution
// creation. Every step is creation-once and replay-safe; a replay returns
// byte-identical projections without appending new facts.
func (l *CompositionLedger) PrepareRunStart(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	if err := request.Validate(); err != nil {
		return application.PreparedRunStart{}, err
	}
	var prepared application.PreparedRunStart
	err := verifier.WithCurrentOwnerLock(ctx, acquisition, func() error {
		// The owner lock is not re-entrant: every nested authority call must
		// use the one borrowed verifier, mirroring controller.withOwner.
		borrowed := &borrowedOwnerVerifier{acquisition: acquisition, active: true}
		defer borrowed.close()
		read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
		if err != nil {
			return err
		}
		if read.Run.State != domain.StateReady || read.Run.AttemptID != "" || read.Run.RunID != request.RunID ||
			read.Run.Sequence != request.ExpectedSequence || read.Run.AuthorityHead != request.ExpectedAuthorityHead {
			return application.NewError("prepare-run-start", application.ReasonAuthorityConflict)
		}
		ready := resultingress.ReadyRunAuthority{AuthorityNamespaceID: l.namespace, TaskID: read.Run.TaskID, RunID: read.Run.RunID, OrchestratorID: l.orchestrator, ReadySequence: read.Run.Sequence, ReadyAuthorityHead: read.Run.AuthorityHead, AttemptsUsed: read.AttemptsUsed, MaxAttempts: read.MaxAttempts, SpecDigest: read.SpecDigest, PolicyDigest: read.PolicyDigest, CapabilityDigest: read.CapabilityDigest, BaseSHA: read.BaseSHA, WorktreePath: read.WorktreePath}
		reservation, err := l.ingress.ReserveAttempt(ctx, l.runReady, ready)
		if err != nil {
			return err
		}
		attemptID := reservation.Reservation.AttemptID
		allocationID := l.allocationIDFor(attemptID)
		lease, err := l.ensureAttemptLease(reservation.ReservationFactDigest, read.Run.TaskID, read.Run.RunID, attemptID, allocationID)
		if err != nil {
			return err
		}
		identity := resultingress.AttemptIdentity{
			AuthorityNamespaceID: l.namespace, AuthorityNamespaceRef: l.namespaceRef(),
			TaskID: read.Run.TaskID, RunID: read.Run.RunID, AttemptID: attemptID,
			AllocationID: allocationID, LeaseID: lease.LeaseId, LeaseDigest: lease.LeaseDigest,
			DispatchGeneration: lease.Generation, FencingTokenDigest: canonical.DigestBytes([]byte(lease.FencingToken)),
			OrchestratorID: l.orchestrator, RunAuthorityDigest: ready.ReadyAuthorityHead,
		}
		// Replay discipline: a fully completed attempt chain skips straight to
		// the creation-once prepared-execution replay. A partially completed
		// chain is response-loss evidence and fails closed for recovery.
		current, attemptExists, err := l.ingress.AttemptState(identity)
		if err != nil {
			return err
		}
		if attemptExists {
			// Replay gate is a strict closed XOR (ADR 0069/0070): exactly one
			// of path A (staging provision effect+receipt, no worktree fields)
			// or path B (existing-worktree bind intent+receipt+digest, no
			// release, reservation present, no provision fields) is complete.
			// Both require launch authorized at the current head with no
			// pending effect intent. Mixed/partial/released states fail closed
			// for recovery; the gate is evaluated before ReserveAttempt's mint
			// branch, so a failure appends no sibling facts.
			if !replayGateAccepts(current) {
				return application.NewError("prepare-run-start", application.ReasonRecoveryRequired)
			}
		} else {
			opened, err := l.ingress.OpenReservedAttempt(ctx, l.runReady, reservation.ReservationFactDigest, identity)
			if err != nil {
				return err
			}
			// The fresh open does not bind the owner; the exact current owner
			// binding is its own authority fact and every later append requires it.
			ownerState, ownerFound, err := l.ingress.OpenOwner(acquisition.Scope)
			if err != nil || !ownerFound {
				return fmt.Errorf("composition: current owner after open: found=%t err=%v", ownerFound, err)
			}
			binding := resultingress.CurrentOwnerBinding{Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: ownerState.FactDigest}
			bound, err := l.ingress.BindOwnerToAttempt(ctx, borrowed, l, opened.State.Revision, opened.State.HeadDigest, resultingress.AttemptAuthorizationRequest{Identity: identity, CurrentRunAuthority: resultingress.RunAuthorityBinding{AuthorityNamespaceID: l.namespace, RunID: identity.RunID, OrchestratorID: l.orchestrator, RunAuthorityDigest: identity.RunAuthorityDigest}}, binding)
			if err != nil {
				return err
			}
			if l.existingWorktreeEnabled {
				// Path B: bind the already-held existing worktree through the
				// RB1 closed union (bind-intent → bind-receipt). ADR 0069/0070.
				if err := l.bindExistingWorktree(ctx, acquisition, ready, reservation, identity, bound.State); err != nil {
					return err
				}
				authorized, found, err := l.ingress.AttemptState(identity)
				if err != nil || !found {
					return fmt.Errorf("composition: attempt state after bind: found=%t err=%v", found, err)
				}
				// Re-seal the closure with the deterministic production argv
				// (built from the precise reserved identity) and the target
				// worktree working directory before launch-authorized. Path B
				// never has a nil builder: NewCompositionLedger rejects it.
				if err := l.resealLaunchClosure(identity, ready.WorktreePath); err != nil {
					return err
				}
				if err := l.authorizeLaunch(ctx, identity, authorized); err != nil {
					return err
				}
			} else {
				// Path A: durable local staging allocation provision. The
				// closure is re-sealed so its observed working directory
				// matches the provision receipt's live directory.
				if err := l.provisionAllocation(ctx, identity, bound.State, read.PolicyDigest, lease.ExpiresAt); err != nil {
					return err
				}
				// The provision facts advance the attempt chain; re-resolve the exact
				// revision/head before the launch-authorization CAS.
				authorized, found, err := l.ingress.AttemptState(identity)
				if err != nil || !found {
					return fmt.Errorf("composition: attempt state after provision: found=%t err=%v", found, err)
				}
				// The agent's working directory is the allocation live directory
				// (staging==live for the local host-process provider), derived
				// deterministically from the allocation id; re-seal the frozen
				// closure so its observed working directory matches the provision
				// receipt exactly, and so its argv is the deterministic
				// production argv built from the precise reserved identity.
				_, live, _, _, stagingErr := allocationcontrol.DeriveRelativeNames(allocationID)
				if stagingErr != nil {
					return stagingErr
				}
				namespaceDigest, digestErr := l.namespace.Digest()
				if digestErr != nil {
					return digestErr
				}
				scope := allocationcontrol.AllocationStoreScopeV1{AuthorityNamespaceID: namespaceDigest, TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID, AllocationID: identity.AllocationID}
				objectsRoot, rootErr := allocationcontrol.ObjectsRootPath(l.allocationDir, scope)
				if rootErr != nil {
					return rootErr
				}
				livePath := filepath.Join(objectsRoot, live)
				if err := l.resealLaunchClosure(identity, livePath); err != nil {
					return err
				}
				if err := l.authorizeLaunch(ctx, identity, authorized); err != nil {
					return err
				}
			}
		}
		created, err := l.ingress.CreatePreparedExecution(ctx, borrowed, acquisition, resultingress.PreparedExecutionCreation{Identity: identity, ExpectedRunSequence: ready.ReadySequence, ExpectedRunAuthorityHead: ready.ReadyAuthorityHead})
		if err != nil {
			return err
		}
		prepared, err = l.ingress.PrepareMacRunStart(ctx, borrowed, acquisition, created.PreparationDigest)
		if err != nil {
			return err
		}
		return l.persistLocalDispatchObservation(prepared.AttemptID)
	})
	if err != nil {
		return application.PreparedRunStart{}, err
	}
	return prepared, nil
}

// allocationIDFor derives the durable allocation id from the attempt id so
// the allocation live directory is known before the lease is minted.
func (l *CompositionLedger) allocationIDFor(attemptID string) string {
	return "allocation-" + attemptID[strings.LastIndexByte(attemptID, ':')+1:]
}

// ensureAttemptLease reuses the attempt's current live lease on replay and
// mints exactly one claimed lease otherwise. The allocation id is derived
// from the attempt id so replays resolve to the same durable allocation.
func (l *CompositionLedger) ensureAttemptLease(reservationFactDigest, taskID, runID, attemptID, allocationID string) (dispatch.DispatchLease, error) {
	now := l.now()
	var replay *dispatch.DispatchLease
	if lease, state, _, err := l.leaseLedger.CurrentByAttempt(runID, attemptID, now); err == nil {
		if state != dispatch.LeaseStateClaimed && state != dispatch.LeaseStateActive && state != dispatch.LeaseStateCompleted {
			return dispatch.DispatchLease{}, fmt.Errorf("dispatch: attempt lease %s is %s", lease.LeaseId, state)
		}
		replay = &lease
		if state == dispatch.LeaseStateCompleted {
			return lease, nil
		}
		if l.matcher != nil {
			if capability, ok := l.matcher.IssuedResultCapability(lease.LeaseId); ok && capability.Validate() == nil {
				l.resultCapabilities[lease.LeaseId] = capability
			}
		}
	}
	if l.existingWorktreeEnabled {
		if l.matcher == nil {
			return dispatch.DispatchLease{}, fmt.Errorf("dispatch: reserved matcher is unavailable")
		}
		ackDeadline := now.Add(15 * time.Minute).UTC().Format(time.RFC3339)
		expiresAt := now.Add(2 * time.Hour).UTC().Format(time.RFC3339)
		if replay != nil {
			ackDeadline, expiresAt = replay.AckDeadlineAt, replay.ExpiresAt
		}
		request, err := l.reservedClaimRequest(reservationFactDigest, taskID, runID, attemptID, allocationID, ackDeadline, expiresAt)
		if err != nil {
			return dispatch.DispatchLease{}, err
		}
		claimed, err := l.matcher.ClaimReserved(request, now)
		if err != nil {
			return dispatch.DispatchLease{}, err
		}
		l.resultCapabilities[claimed.Lease.LeaseId] = claimed.ResultCapability
		return claimed.Lease, nil
	}
	if replay != nil {
		return *replay, nil
	}
	minted, err := dispatch.MintClaimedLease(dispatch.MintLeaseInput{
		LeaseId:              "lease-" + attemptID[strings.LastIndexByte(attemptID, ':')+1:],
		AuthorityNamespaceId: l.namespace, SecurityDomainId: l.provisionDomain,
		RegistrationId: l.registration, ProviderCapabilitySnapshotDigest: l.snapshot,
		ConformanceEvidenceDigests: l.conformance, Attestation: l.attestation,
		TaskId: taskID, RunId: runID, AttemptId: attemptID,
		AllocationId: allocationID,
		Generation:   1, Now: now, AckDelay: 15 * time.Minute, Lifetime: 2 * time.Hour,
	})
	if err != nil {
		return dispatch.DispatchLease{}, err
	}
	if err := l.leaseLedger.AppendClaim(minted); err != nil {
		return dispatch.DispatchLease{}, err
	}
	return minted, nil
}

func (l *CompositionLedger) reservedClaimRequest(reservationFactDigest, taskID, runID, attemptID, allocationID, ackDeadlineAt, expiresAt string) (dispatch.ReservedClaimRequest, error) {
	requirements, err := domain.NewSandboxRequirements(domain.AccessMode(l.requirements.AccessMode), domain.AssuranceLevel(l.requirements.MinimumAssuranceLevel))
	if err != nil {
		return dispatch.ReservedClaimRequest{}, err
	}
	return dispatch.ReservedClaimRequest{
		ReservationFactDigest: reservationFactDigest,
		RunId:                 runID,
		ReservedAttemptId:     attemptID,
		Claim: dispatch.ClaimRequest{
			AuthorityNamespaceId: l.namespace,
			RegistrationId:       l.providerRecord.RegistrationId,
			Snapshot:             l.providerSnapshot,
			Evidences:            append([]provider.ConformanceEvidence(nil), l.providerEvidence...),
			Requirements:         requirements,
			TargetActor:          l.resultTarget,
			TaskId:               taskID,
			RunId:                runID,
			AttemptId:            attemptID,
			AllocationId:         allocationID,
			AckDeadlineAt:        ackDeadlineAt,
			ExpiresAt:            expiresAt,
		},
	}, nil
}

// compositionLeaseResolver rechecks an issued result capability against the
// append-only lease ledger. In-memory matcher bookkeeping is never accepted
// as current authority.
type compositionLeaseResolver struct {
	ledger *dispatch.LeaseLedger
}

func (resolver compositionLeaseResolver) LeaseActive(leaseID string, generation int64, fencingToken string) (bool, error) {
	if resolver.ledger == nil {
		return false, nil
	}
	lease, state, currentGeneration, err := resolver.ledger.Current(leaseID)
	if err != nil {
		return false, nil
	}
	if state != dispatch.LeaseStateClaimed && state != dispatch.LeaseStateActive {
		return false, nil
	}
	return currentGeneration == generation && lease.Generation == generation && lease.FencingToken == fencingToken, nil
}

// compositionTargetResolver requires both the exact result-ingress actor and
// the still-active canonical provider registration on every edge recheck.
type compositionTargetResolver struct {
	store          *provider.RegistrationStore
	registrationID string
	target         authority.SecurityDomainId
}

func (resolver compositionTargetResolver) TargetEligible(target authority.SecurityDomainId) (bool, error) {
	if resolver.store == nil || !target.Equal(resolver.target) {
		return false, nil
	}
	registration, err := resolver.store.Get(resolver.registrationID)
	if err != nil {
		return false, nil
	}
	return registration.LifecycleState == provider.LifecycleStateActive, nil
}

func (l *CompositionLedger) namespaceRef() string {
	digest, err := l.namespace.Digest()
	if err != nil {
		return ""
	}
	return digest
}

// mergeWorkDirs unions the composition working directory with the caller
// extras into one sorted, duplicate-free allowlist.
func mergeWorkDirs(primary string, extras []string) []string {
	seen := map[string]struct{}{primary: {}}
	merged := []string{primary}
	for _, extra := range extras {
		if _, ok := seen[extra]; ok || extra == "" {
			continue
		}
		seen[extra] = struct{}{}
		merged = append(merged, extra)
	}
	sort.Strings(merged)
	return merged
}

// provisionAllocation drives the durable local allocation: intent CAS,
// real staging preparation through allocationcontrol, typed receipt and
// reconcile projection. The intent deadline is bound to the attempt lease
// expiry so replays stay byte-identical.
func (l *CompositionLedger) provisionAllocation(ctx context.Context, identity resultingress.AttemptIdentity, state resultingress.AttemptAuthorityState, policyDigest, leaseExpiry string) error {
	namespaceDigest, err := identity.AuthorityNamespaceID.Digest()
	if err != nil {
		return err
	}
	staging, live, _, markerName, err := allocationcontrol.DeriveRelativeNames(identity.AllocationID)
	if err != nil {
		return err
	}
	effectID := "provision-" + identity.AttemptID
	derived, err := resultingress.DeriveAllocationEffectIdentity(identity, resultingress.EffectPhaseAllocationProvision, effectID, state.HeadDigest)
	if err != nil {
		return err
	}
	typed := allocationcontrol.AllocationProvisionIntentV1{
		SchemaVersion: allocationcontrol.ProvisionSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: allocationcontrol.AllocationBindingV1{
			AuthorityNamespaceID: namespaceDigest, TaskID: identity.TaskID, RunID: identity.RunID,
			AttemptID: identity.AttemptID, AllocationID: identity.AllocationID, LeaseID: identity.LeaseID,
			Generation: identity.DispatchGeneration, FencingTokenDigest: identity.FencingTokenDigest,
			CommandID: derived.CommandID, IdempotencyKey: derived.IdempotencyKey,
		},
		Requirements: l.requirements, AllowedStoreIDs: []string{},
		WorkDirAllowlist: mergeWorkDirs(l.closure.WorkingDirectory, l.workDirs), EnvironmentAllowlist: l.environment,
		ExpectedOwnerUID: uint32(os.Geteuid()), ExpectedDirectoryMode: 0o700, ExpectedMarkerMode: 0o600,
		StagingRelativeName: staging, LiveRelativeName: live, MarkerRelativeName: markerName,
		MarkerNonceDigest: derived.MarkerNonceDigest, ExpectedAttemptSequence: state.Revision + 1, AttemptAuthorityFactDigest: state.HeadDigest,
	}
	if err := typed.Seal(); err != nil {
		return err
	}
	generic := authority.SideEffectIntent{
		AuthorityNamespaceId: identity.AuthorityNamespaceID, EffectId: effectID, OwnerIdentity: identity.OrchestratorID,
		Port: "sandbox", Operation: string(resultingress.EffectPhaseAllocationProvision), TargetRef: identity.AllocationID,
		TargetDigest: derived.MarkerNonceDigest, RequestDigest: typed.RequestDigest, CommandId: derived.CommandID,
		IdempotencyKey: derived.IdempotencyKey, PolicyDigest: policyDigest, AuthorizationDigest: state.HeadDigest,
		Purpose: "durable local allocation", DispositionClass: authority.DispositionClassSandboxProvision, Deadline: leaseExpiry,
	}
	appended, err := l.allocation.CompareAndAppendAllocationProvisionIntent(ctx, identity, generic, typed)
	if err != nil {
		return err
	}
	store, err := allocationcontrol.OpenStore(l.allocationDir, allocationcontrol.AllocationStoreScopeV1{AuthorityNamespaceID: namespaceDigest, TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID, AllocationID: identity.AllocationID})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	// The allocationcontrol controller is the production provision driver: it
	// performs the staging mutation behind the first-mutation deadline gate,
	// records typed provider failures and replays prepared/receipt history
	// exactly once.
	controller, err := allocationcontrol.NewController(store, l.allocation)
	if err != nil {
		return err
	}
	if _, err := controller.RecoverProvision(ctx, appended.EffectKey); err != nil {
		return err
	}
	return nil
}

// resealLaunchClosure rebuilds the frozen launch closure with the
// deterministic production argv (from the injected builder) and the given
// working directory, then stores it on the ledger so authorizeLaunch appends
// the exact sealed closure. The builder receives the precise reserved
// identity; a builder error fails closed before launch-authorized and
// PreparedExecution. On path A a nil builder preserves the legacy
// composition-time argv (only the working directory is re-sealed); path B
// never has a nil builder because NewCompositionLedger rejects it.
func (l *CompositionLedger) resealLaunchClosure(identity resultingress.AttemptIdentity, workingDirectory string) error {
	argv := l.closure.Arguments
	if l.launchArgvBuilder != nil {
		built, err := l.launchArgvBuilder(AttemptLaunchIdentity{TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID})
		if err != nil {
			return err
		}
		argv = built.Argv
	}
	reSealed, err := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: l.closure.RuntimeExecutable, ClosureProfileID: l.closure.ClosureProfileID,
		MaterialRoots: l.closure.MaterialRoots, LaunchMaterials: l.closure.LaunchMaterials,
		Arguments: argv, Environment: l.closure.Environment, WorkingDirectory: workingDirectory,
	})
	if err != nil {
		return err
	}
	l.closure = reSealed
	return nil
}

func (l *CompositionLedger) authorizeLaunch(ctx context.Context, identity resultingress.AttemptIdentity, state resultingress.AttemptAuthorityState) error {
	request := resultingress.AttemptAuthorizationRequest{Identity: identity, CurrentRunAuthority: resultingress.RunAuthorityBinding{AuthorityNamespaceID: l.namespace, RunID: identity.RunID, OrchestratorID: l.orchestrator, RunAuthorityDigest: identity.RunAuthorityDigest}}
	transition := resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionLaunchAuthorized, Identity: identity, LaunchAuthorizationID: "launch-" + identity.AttemptID, LaunchClosure: l.closure}
	_, err := l.ingress.CompareAndAppendAuthorized(ctx, l, state.Revision, state.HeadDigest, request, transition)
	return err
}
