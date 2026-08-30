package productionruntime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// CompositionLedger implements DurableRunAuthority over the RB1 Run store and
// the ResultIngress authority ledger. Every method requires the exact current
// owner lock; the ledger itself holds no identity beyond the composition
// inputs frozen by the fixed CLI at construction.
type CompositionLedger struct {
	ingress         *resultingress.DurableStore
	runs            *runstore.Store
	runLease        *runstore.Lease
	leaseLedger     *dispatch.LeaseLedger
	runReady        *runstore.AttemptRunAuthorityVerifier
	namespace       authority.AuthorityNamespaceId
	orchestrator    string
	provisionDomain authority.SecurityDomainId
	cleanupDomain   authority.SecurityDomainId
	registration    string
	snapshot        string
	conformance     []string
	attestation     provider.Attestation
	allocationDir   string
	closure         launchidentity.ClosureV1
	requirements    allocationcontrol.SandboxRequirementsV1
	workDirs        []string
	environment     []string
	allocation      *resultingress.AllocationAuthority
	now             func() time.Time
}

// CompositionInputs freezes the composition-time identity and location
// decisions. Nothing here is derivable from the durable ledger; everything is
// validated before the first authority call.
type CompositionInputs struct {
	Ingress              *resultingress.DurableStore
	Runs                 *runstore.Store
	RunLease             *runstore.Lease
	LeaseLedger          *dispatch.LeaseLedger
	Namespace            authority.AuthorityNamespaceId
	OrchestratorID       string
	ProvisionDomain      authority.SecurityDomainId
	CleanupDomain        authority.SecurityDomainId
	RegistrationID       string
	CapabilitySnapshot   string
	ConformanceEvidence  []string
	Attestation          provider.Attestation
	AllocationRoot       string
	LaunchClosure        launchidentity.ClosureV1
	Requirements         allocationcontrol.SandboxRequirementsV1
	WorkDirAllowlist     []string
	EnvironmentAllowlist []string
}

// NewCompositionLedger is the only constructor. It validates every input and
// wires the Core provision/cleanup verifiers, which authorize the composition
// itself as the local SandboxProvider authority.
func NewCompositionLedger(inputs CompositionInputs) (*CompositionLedger, error) {
	if inputs.Ingress == nil || inputs.Runs == nil || inputs.RunLease == nil || inputs.LeaseLedger == nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	if inputs.Namespace.Validate() != nil || inputs.ProvisionDomain.Validate() != nil || inputs.CleanupDomain.Validate() != nil ||
		inputs.ProvisionDomain == inputs.CleanupDomain ||
		inputs.OrchestratorID == "" || inputs.RegistrationID == "" || inputs.AllocationRoot == "" ||
		inputs.CapabilitySnapshot == "" || inputs.LaunchClosure.Validate() != nil {
		return nil, application.NewError("composition-ledger", application.ReasonInvalidRequest)
	}
	// The allocation root is composition-owned infrastructure: create it
	// eagerly so the per-attempt staging store can open inside it.
	if err := os.MkdirAll(inputs.AllocationRoot, 0o700); err != nil {
		return nil, err
	}
	runReady, err := runstore.NewAttemptRunAuthorityVerifier(inputs.Runs, inputs.RunLease, inputs.Namespace, inputs.OrchestratorID)
	if err != nil {
		return nil, err
	}
	ledger := &CompositionLedger{
		ingress: inputs.Ingress, runs: inputs.Runs, runLease: inputs.RunLease, leaseLedger: inputs.LeaseLedger,
		runReady: runReady, namespace: inputs.Namespace, orchestrator: inputs.OrchestratorID,
		provisionDomain: inputs.ProvisionDomain, cleanupDomain: inputs.CleanupDomain,
		registration: inputs.RegistrationID, snapshot: inputs.CapabilitySnapshot,
		conformance: append([]string(nil), inputs.ConformanceEvidence...), attestation: inputs.Attestation,
		allocationDir: inputs.AllocationRoot, closure: inputs.LaunchClosure,
		requirements: inputs.Requirements, workDirs: append([]string(nil), inputs.WorkDirAllowlist...),
		environment: append([]string(nil), inputs.EnvironmentAllowlist...), now: time.Now,
	}
	allocation, err := resultingress.NewAllocationAuthority(inputs.Ingress, compositionAllocationAuthority{ledger: ledger, domain: inputs.ProvisionDomain, phase: resultingress.EffectPhaseAllocationProvision}, compositionAllocationAuthority{ledger: ledger, domain: inputs.CleanupDomain, phase: resultingress.EffectPhaseAllocationTerminate})
	if err != nil {
		return nil, err
	}
	ledger.allocation = allocation
	return ledger, nil
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
		pending, err := l.pendingRecovery()
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

func (l *CompositionLedger) pendingRecovery() (uint64, error) {
	read, err := l.runs.ReadRunStartAuthorityUnderLease(context.Background(), l.runLease)
	if err != nil {
		return 0, err
	}
	if read.Run.State == domain.StateRunning {
		return 1, nil
	}
	return 0, nil
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
	read, err := l.runs.ReadRunStartAuthorityUnderLease(ctx, l.runLease)
	if err != nil {
		return err
	}
	current := resultingress.RunAuthorityBinding{AuthorityNamespaceID: l.namespace, RunID: read.Run.RunID, OrchestratorID: l.orchestrator, RunAuthorityDigest: read.Run.AuthorityHead}
	if current != binding {
		return resultingress.ErrAttemptAuthorityConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
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
		lease, err := l.ensureAttemptLease(read.Run.TaskID, read.Run.RunID, attemptID)
		if err != nil {
			return err
		}
		identity := resultingress.AttemptIdentity{
			AuthorityNamespaceID: l.namespace, AuthorityNamespaceRef: l.namespaceRef(),
			TaskID: read.Run.TaskID, RunID: read.Run.RunID, AttemptID: attemptID,
			AllocationID: lease.AllocationId, LeaseID: lease.LeaseId, LeaseDigest: lease.LeaseDigest,
			DispatchGeneration: lease.Generation, FencingTokenDigest: lease.FencingToken,
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
			if current.AllocationProvisionReceiptDigest == "" || current.LaunchAuthorizedDigest == "" || current.HeadDigest != current.LaunchAuthorizedDigest || current.PendingEffectIntentFactDigest != "" {
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
			if err := l.provisionAllocation(ctx, identity, bound.State, read.PolicyDigest, lease.ExpiresAt); err != nil {
				return err
			}
			// The provision facts advance the attempt chain; re-resolve the exact
			// revision/head before the launch-authorization CAS.
			authorized, found, err := l.ingress.AttemptState(identity)
			if err != nil || !found {
				return fmt.Errorf("composition: attempt state after provision: found=%t err=%v", found, err)
			}
			if err := l.authorizeLaunch(ctx, identity, authorized); err != nil {
				return err
			}
		}
		created, err := l.ingress.CreatePreparedExecution(ctx, borrowed, acquisition, resultingress.PreparedExecutionCreation{Identity: identity, ExpectedRunSequence: ready.ReadySequence, ExpectedRunAuthorityHead: ready.ReadyAuthorityHead})
		if err != nil {
			return err
		}
		prepared, err = l.ingress.PrepareMacRunStart(ctx, borrowed, acquisition, created.PreparationDigest)
		return err
	})
	if err != nil {
		return application.PreparedRunStart{}, err
	}
	return prepared, nil
}

// ensureAttemptLease reuses the attempt's current live lease on replay and
// mints exactly one claimed lease otherwise. The allocation id is derived
// from the attempt id so replays resolve to the same durable allocation.
func (l *CompositionLedger) ensureAttemptLease(taskID, runID, attemptID string) (dispatch.DispatchLease, error) {
	now := l.now()
	if lease, state, _, err := l.leaseLedger.CurrentByAttempt(runID, attemptID, now); err == nil {
		if state != dispatch.LeaseStateClaimed && state != dispatch.LeaseStateActive {
			return dispatch.DispatchLease{}, fmt.Errorf("dispatch: attempt lease %s is %s", lease.LeaseId, state)
		}
		return lease, nil
	}
	minted, err := dispatch.MintClaimedLease(dispatch.MintLeaseInput{
		LeaseId:              "lease-" + attemptID[strings.LastIndexByte(attemptID, ':')+1:],
		AuthorityNamespaceId: l.namespace, SecurityDomainId: l.provisionDomain,
		RegistrationId: l.registration, ProviderCapabilitySnapshotDigest: l.snapshot,
		ConformanceEvidenceDigests: l.conformance, Attestation: l.attestation,
		TaskId: taskID, RunId: runID, AttemptId: attemptID,
		AllocationId: "allocation-" + attemptID[strings.LastIndexByte(attemptID, ':')+1:],
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

func (l *CompositionLedger) namespaceRef() string {
	digest, err := l.namespace.Digest()
	if err != nil {
		return ""
	}
	return digest
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
		WorkDirAllowlist: l.workDirs, EnvironmentAllowlist: l.environment,
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

func (l *CompositionLedger) authorizeLaunch(ctx context.Context, identity resultingress.AttemptIdentity, state resultingress.AttemptAuthorityState) error {
	request := resultingress.AttemptAuthorizationRequest{Identity: identity, CurrentRunAuthority: resultingress.RunAuthorityBinding{AuthorityNamespaceID: l.namespace, RunID: identity.RunID, OrchestratorID: l.orchestrator, RunAuthorityDigest: identity.RunAuthorityDigest}}
	transition := resultingress.AttemptTransition{Kind: resultingress.AttemptTransitionLaunchAuthorized, Identity: identity, LaunchAuthorizationID: "launch-" + identity.AttemptID, LaunchClosure: l.closure}
	_, err := l.ingress.CompareAndAppendAuthorized(ctx, l, state.Revision, state.HeadDigest, request, transition)
	return err
}
