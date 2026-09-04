//go:build darwin && arm64

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	piadapter "github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/contract"
	controlplane "github.com/chiga0/marshal-harness/internal/control"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// sealedRepositoryApplication is the fixed-binary application adapter shared
// by direct CLI mutation and the forthcoming control-plane server mode. It
// owns repository-wide authority once and composes one short-lived Run runtime
// for each bounded transaction. The mutex intentionally serializes the first
// Mac implementation; Run leases remain the durable concurrency fence.
type sealedRepositoryApplication struct {
	mu sync.Mutex

	repositoryRoot  string
	stateRoot       string
	piRuntime       string
	piEntrypoint    string
	namespace       authority.AuthorityNamespaceId
	entryIdentity   *selfidentity.LocalSelfIdentityObservationV2
	observeIdentity productionruntime.LocalSelfIdentityObserver

	runs           *runstore.Store
	leaseLedger    *dispatch.LeaseLedger
	providerStore  *provider.RegistrationStore
	registration   provider.ProviderRegistration
	snapshot       provider.ProviderCapabilitySnapshot
	attestation    provider.Attestation
	providerDomain authority.SecurityDomainId
	allocationRoot string

	session       *productionruntime.RepositorySession
	resources     []io.Closer
	closed        bool
	statusProfile productionruntime.PiProfile
	validator     *contract.Validator
}

var _ application.PublicApplicationPort = (*sealedRepositoryApplication)(nil)

type sealedRepositoryApplicationConfig struct {
	StateRoot       string
	RepositoryRoot  string
	PiRuntime       string
	PiEntrypoint    string
	EntryIdentity   *selfidentity.LocalSelfIdentityObservationV2
	ObserveIdentity productionruntime.LocalSelfIdentityObserver
	RecoveryMode    sealedRepositoryRecoveryMode
}

type sealedRepositoryRecoveryMode uint8

const (
	sealedRepositoryRecoveryOneShot sealedRepositoryRecoveryMode = iota + 1
	sealedRepositoryRecoveryResident
)

func (mode sealedRepositoryRecoveryMode) valid() bool {
	return mode == sealedRepositoryRecoveryOneShot || mode == sealedRepositoryRecoveryResident
}

func openSealedRepositoryApplication(ctx context.Context, config sealedRepositoryApplicationConfig) (_ *sealedRepositoryApplication, err error) {
	if ctx == nil || config.StateRoot == "" || config.RepositoryRoot == "" || config.PiRuntime == "" || config.PiEntrypoint == "" ||
		config.EntryIdentity == nil || config.ObserveIdentity == nil || !config.RecoveryMode.valid() {
		return nil, application.NewError("sealed-repository-application", application.ReasonInvalidRequest)
	}
	piRuntime, err := filepath.EvalSymlinks(config.PiRuntime)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: resolve Pi runtime: %w", err)
	}
	piEntrypoint, err := filepath.EvalSymlinks(config.PiEntrypoint)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: resolve Pi entrypoint: %w", err)
	}
	fixedMarshal, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: locate fixed marshal: %w", err)
	}
	fixedMarshal, err = filepath.EvalSymlinks(fixedMarshal)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: resolve fixed marshal: %w", err)
	}

	ingressDir, ledgerDir, allocationRoot, ownerDir := productionruntime.CompositionPaths(config.StateRoot)
	providerDir := filepath.Join(config.StateRoot, "provider-authority")
	controlRootPath := filepath.Join(config.StateRoot, "owner-control")
	for _, dir := range []string{ingressDir, ledgerDir, allocationRoot, ownerDir, providerDir, controlRootPath} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("sealed repository application: prepare authority directory: %w", err)
		}
	}

	applicationAdapter := &sealedRepositoryApplication{
		repositoryRoot: config.RepositoryRoot, stateRoot: config.StateRoot,
		piRuntime: piRuntime, piEntrypoint: piEntrypoint,
		entryIdentity: config.EntryIdentity, observeIdentity: config.ObserveIdentity,
		allocationRoot: allocationRoot,
	}
	applicationAdapter.validator, err = contract.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: compile contracts: %w", err)
	}
	defer func() {
		if err != nil {
			_ = applicationAdapter.Close()
		}
	}()

	openHeld := func(path string) (*os.File, error) {
		handle, openErr := os.Open(path)
		if openErr == nil {
			applicationAdapter.resources = append(applicationAdapter.resources, handle)
		}
		return handle, openErr
	}
	ownerDirectory, err := openHeld(ownerDir)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open owner directory: %w", err)
	}
	heldIngress, err := openHeld(ingressDir)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open ingress directory: %w", err)
	}
	controlRoot, err := openHeld(controlRootPath)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open control root: %w", err)
	}
	providerDirectory, err := openHeld(providerDir)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open provider directory: %w", err)
	}
	stateRootDirectory, err := openHeld(config.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open state root: %w", err)
	}
	applicationAdapter.runs, err = runstore.NewFromStateRootDescriptor(stateRootDirectory)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: bind run store: %w", err)
	}
	applicationAdapter.resources = append(applicationAdapter.resources, applicationAdapter.runs)
	repositoryDirectory, err := productionruntime.OpenCanonicalRepositoryRoot(config.RepositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: bind canonical repository root: %w", err)
	}
	applicationAdapter.resources = append(applicationAdapter.resources, repositoryDirectory)

	applicationAdapter.leaseLedger, err = dispatch.NewLeaseLedger(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open dispatch ledger: %w", err)
	}
	applicationAdapter.namespace = authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: config.RepositoryRoot}
	acquisition, err := observeCompositionAcquisition(fixedMarshal, applicationAdapter.namespace)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: observe owner acquisition: %w", err)
	}
	applicationAdapter.attestation, err = productionruntime.LocalAttestation(fixedMarshal)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: observe attestation: %w", err)
	}
	// OpenRepositorySession materializes the fixed runtime-v1 hierarchy below
	// StateRoot. Do that before binding the held provider store: the provider
	// authority deliberately freezes its parent directory mutation identity,
	// so creating a later StateRoot sibling would correctly look like an
	// authority-graph drift on the first ComposeRuntime read-back.
	applicationAdapter.session, err = productionruntime.OpenRepositorySession(ctx, productionruntime.RepositorySessionInputs{
		HeldIngressDir: heldIngress, HeldRepositoryRoot: repositoryDirectory, OwnerDirectory: ownerDirectory, Acquisition: acquisition,
		FixedMarshalPath: fixedMarshal, OwnerPrivateControlRoot: controlRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open repository session: %w", err)
	}
	applicationAdapter.providerStore, err = provider.OpenDarwinRegistrationStore(providerDirectory)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: open provider authority: %w", err)
	}
	applicationAdapter.resources = append(applicationAdapter.resources, applicationAdapter.providerStore)
	applicationAdapter.providerDomain = productionruntime.LocalProvisionDomain(applicationAdapter.namespace)
	applicationAdapter.registration, applicationAdapter.snapshot, err = productionruntime.LocalProviderAuthority(applicationAdapter.namespace, applicationAdapter.providerDomain, applicationAdapter.attestation)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: construct provider authority: %w", err)
	}
	applicationAdapter.registration, err = applicationAdapter.providerStore.Put(applicationAdapter.registration)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: persist provider authority: %w", err)
	}

	statusClosure, err := launchidentity.OpenPi0844(piRuntime, piEntrypoint, []string{piRuntime, piEntrypoint}, nil, config.RepositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: observe Pi identity: %w", err)
	}
	statusIdentity, identityErr := launchidentity.Pi0844IdentityFromClosure(statusClosure.Closure)
	statusClosure.Close()
	if identityErr != nil {
		return nil, fmt.Errorf("sealed repository application: derive Pi identity: %w", identityErr)
	}
	applicationAdapter.statusProfile, err = productionruntime.NewPi0844Profile(piRuntime, piEntrypoint, statusIdentity.IdentityDigest)
	if err != nil {
		return nil, fmt.Errorf("sealed repository application: construct Pi status profile: %w", err)
	}
	if err := recoverSealedRepositoryOnOpen(ctx, config.RecoveryMode, applicationAdapter.recoverRepositoryRuns); err != nil {
		return nil, fmt.Errorf("sealed repository application: recover repository runs: %w", err)
	}
	return applicationAdapter, nil
}

type sealedRepositoryRecoverFunc func(context.Context) error

// recoverSealedRepositoryOnOpen keeps recovery policy explicit at every
// composition root. A one-shot CLI operation defers target recovery to its
// sole openRun call. A resident server must recover the complete repository
// before it advertises readiness.
func recoverSealedRepositoryOnOpen(ctx context.Context, mode sealedRepositoryRecoveryMode, recoverRuns sealedRepositoryRecoverFunc) error {
	if !mode.valid() || recoverRuns == nil {
		return application.NewError("sealed-repository-recovery", application.ReasonInvalidRequest)
	}
	if mode == sealedRepositoryRecoveryOneShot {
		return nil
	}
	return recoverRuns(ctx)
}

func (adapter *sealedRepositoryApplication) Close() error {
	if adapter == nil {
		return nil
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed {
		return nil
	}
	adapter.closed = true
	var closeErrors []error
	if adapter.session != nil {
		closeErrors = append(closeErrors, adapter.session.Close())
	}
	for index := len(adapter.resources) - 1; index >= 0; index-- {
		closeErrors = append(closeErrors, adapter.resources[index].Close())
	}
	adapter.resources = nil
	return errors.Join(closeErrors...)
}

func (adapter *sealedRepositoryApplication) Status(ctx context.Context, _ application.StatusRequest) (application.StatusProjection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed {
		return application.StatusProjection{}, application.NewError("status", application.ReasonBridgeUnavailable)
	}
	owner, err := adapter.session.OwnerProjection(ctx)
	if err != nil {
		return application.StatusProjection{}, err
	}
	projection := application.StatusProjection{
		ProtocolRevision: application.ProtocolRevision, Availability: application.AvailabilityReady,
		PlatformProfileID: productionruntime.DarwinLocalDogfoodProfile,
		AgentProvider:     productionruntime.PiProviderName, AgentVersion: productionruntime.PiProviderVersion,
		AgentClosureProfile: adapter.statusProfile.ClosureProfileID(), AgentIdentityDigest: adapter.statusProfile.IdentityDigest(),
		OwnerEpoch: owner.OwnerEpoch, OwnerFactDigest: owner.OwnerFactDigest,
	}
	return projection, projection.Validate()
}

// recoverRepositoryRuns enumerates the descriptor-bound Run set while the
// repository owner session is held, then composes every RUNNING Run once.
// NewCompositionLedger performs the exact attach/rebind reconciliation. Only
// after this pass succeeds may Status report the resident port as ready.
func (adapter *sealedRepositoryApplication) recoverRepositoryRuns(ctx context.Context) error {
	runIDs, err := adapter.runs.ListExistingRunIDs()
	if err != nil {
		return err
	}
	for _, runID := range runIDs {
		lease, err := adapter.runs.AcquireExisting(runID)
		if err != nil {
			return err
		}
		projection, readErr := adapter.runs.ReadCurrentRunProjectionUnderLease(lease)
		releaseErr := lease.Release()
		if readErr != nil {
			return readErr
		}
		if releaseErr != nil {
			return releaseErr
		}
		if projection.State != domain.StateRunning {
			continue
		}
		run, err := adapter.openRun(ctx, runID)
		if err != nil {
			return err
		}
		if err := run.Close(); err != nil {
			return err
		}
	}
	return nil
}

// StartRun is the shared bounded start operation used by direct CLI and fixed
// server mode. One openRun owns the path-B descriptor graph from preparation
// through execution or durable response-loss reconciliation.
func (adapter *sealedRepositoryApplication) StartRun(ctx context.Context, request application.StartRunRequest) (application.RunStartProjection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.validator == nil || adapter.entryIdentity == nil {
		return application.RunStartProjection{}, application.NewError("start-run", application.ReasonBridgeUnavailable)
	}
	state, err := inspectAndValidateDescriptorBoundLocalDogfoodBinding(adapter.runs, request.RunID, adapter.validator, adapter.entryIdentity)
	if err != nil {
		return application.RunStartProjection{}, err
	}
	if state.State == domain.StateReady {
		if err := controlplane.Require(controlplane.ApprovalInput{
			StateRoot: adapter.stateRoot, RunID: request.RunID, Gate: domain.ApprovalGatePlan,
			Validator: adapter.validator, LocalSelfIdentity: adapter.entryIdentity,
		}); err != nil {
			return application.RunStartProjection{}, application.NewError("start-run-plan-approval", application.ReasonAuthorityConflict)
		}
	}
	run, err := adapter.openRun(ctx, request.RunID)
	if err != nil {
		return application.RunStartProjection{}, err
	}
	defer run.Close()
	return run.runtime.StartRun(ctx, request)
}

// inspectAndValidateDescriptorBoundLocalDogfoodBinding keeps the current Run
// state and its frozen policy on the same held Run authority. Fixed server
// mode must not re-open StateRoot by pathname after composition has already
// bound the exact directory object.
func inspectAndValidateDescriptorBoundLocalDogfoodBinding(runs *runstore.Store, runID string, validator *contract.Validator, observation *selfidentity.LocalSelfIdentityObservationV2) (domain.RunState, error) {
	if runs == nil {
		return domain.RunState{}, application.NewError("start-run-frozen-state", application.ReasonAuthorityConflict)
	}
	lease, err := runs.AcquireExisting(runID)
	if err != nil {
		return domain.RunState{}, application.NewError("start-run-frozen-state", application.ReasonAuthorityConflict)
	}
	state, stateErr := runstore.InspectUnderLease(lease)
	var policyData []byte
	var policyErr error
	if stateErr == nil {
		policyData, policyErr = runstore.ReadFileUnderLease(lease, maxContractInputBytes, "policy-snapshot.json")
	}
	releaseErr := lease.Release()
	if errors.Join(stateErr, releaseErr) != nil {
		return domain.RunState{}, application.NewError("start-run-frozen-state", application.ReasonAuthorityConflict)
	}
	if policyErr != nil {
		return domain.RunState{}, application.NewError("start-run-frozen-policy", application.ReasonAuthorityConflict)
	}
	if err := validateFrozenLocalDogfoodBindingData(state, policyData, validator, observation); err != nil {
		return domain.RunState{}, application.NewError("start-run-frozen-policy", application.ReasonAuthorityConflict)
	}
	return state, nil
}

// inspectDescriptorBoundRunState reads the current Run through the exact
// StateRoot/Run descriptors retained by the production application. Calling
// Store.Inspect here would fall back to a pathname API that descriptor-bound
// stores deliberately reject.
func inspectDescriptorBoundRunState(runs *runstore.Store, runID string) (domain.RunState, error) {
	if runs == nil {
		return domain.RunState{}, runstore.ErrConflict
	}
	lease, err := runs.AcquireExisting(runID)
	if err != nil {
		return domain.RunState{}, err
	}
	state, readErr := runstore.InspectUnderLease(lease)
	releaseErr := lease.Release()
	return state, errors.Join(readErr, releaseErr)
}

func (adapter *sealedRepositoryApplication) ReconcileStartRun(ctx context.Context, request application.StartRunRequest) (application.RunStartProjection, bool, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed {
		return application.RunStartProjection{}, false, application.NewError("reconcile-start-run", application.ReasonBridgeUnavailable)
	}
	run, err := adapter.openRun(ctx, request.RunID)
	if err != nil {
		return application.RunStartProjection{}, false, err
	}
	defer run.Close()
	return run.runtime.ReconcileStartRun(ctx, request)
}

type sealedRunAdvancer interface {
	InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error)
	StartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, error)
	CollectRunResult(context.Context, string) (productionruntime.CollectedRunResult, error)
}

// advanceRun keeps the exact path-B launch closure and existing-worktree
// descriptor graph alive while the shared StartRun application operation
// prepares and starts the Run. Reopening a second Run runtime between those
// phases would close the source objects that execution must recheck.
func (adapter *sealedRepositoryApplication) advanceRun(ctx context.Context, runID string) (application.RunProjection, string, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return advanceSealedRunWithOpen(ctx, runID, func(ctx context.Context, runID string) (sealedRunAdvancer, func() error, error) {
		run, err := adapter.openRun(ctx, runID)
		if err != nil {
			return nil, nil, err
		}
		return run.runtime, run.Close, nil
	})
}

// driveRunToWorkerCompletion keeps one already-current repository owner, one
// Run composition and one descriptor graph alive through worker
// terminalization. The foreground READY path first crosses the single owner
// successor required by process-started, then enters this resident loop.
// Reopening one CLI process per collect would append an owner successor and
// replay an ever-growing authority ledger on every poll.
func (adapter *sealedRepositoryApplication) driveRunToWorkerCompletion(ctx context.Context, runID string) (application.RunProjection, string, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return driveSealedRunToWorkerCompletionWithOpen(ctx, runID, func(ctx context.Context, runID string) (sealedRunAdvancer, func() error, error) {
		run, err := adapter.openRun(ctx, runID)
		if err != nil {
			return nil, nil, err
		}
		return run.runtime, run.Close, nil
	}, waitForSealedRunPoll)
}

type sealedRunOpenFunc func(context.Context, string) (sealedRunAdvancer, func() error, error)

type sealedRunWaitFunc func(context.Context) error

// sealedForegroundApplication is the repository-application slice needed by
// the explicit CLI --wait flow. A READY Run must cross exactly one repository
// owner boundary after StartRun: process-started advanced the Attempt head, so
// the successor application supplies the current control-owner-bound fact
// required by authenticated Attach. A Run that was already RUNNING is rebound
// by its first InspectRun composition and can stay in that application.
type sealedForegroundApplication interface {
	InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error)
	StartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, error)
	driveRunToWorkerCompletion(context.Context, string) (application.RunProjection, string, error)
}

type sealedForegroundOpenFunc func(context.Context) (sealedForegroundApplication, func() error, error)

func driveSealedRunAcrossOwnerBoundary(ctx context.Context, runID string, open sealedForegroundOpenFunc) (application.RunProjection, string, error) {
	if ctx == nil || runID == "" || open == nil {
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonInvalidRequest)
	}
	current, closeCurrent, err := open(ctx)
	if err != nil {
		return application.RunProjection{}, "sealed Run 组装失败", err
	}
	if current == nil || closeCurrent == nil {
		if closeCurrent != nil {
			_ = closeCurrent()
		}
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	closed := false
	defer func() {
		if !closed {
			_ = closeCurrent()
		}
	}()

	before, err := current.InspectRun(ctx, application.InspectRunRequest{RunID: runID})
	if err != nil {
		return application.RunProjection{}, "当前 Run 权威投影不可读", err
	}
	if before.State != domain.StateReady {
		return current.driveRunToWorkerCompletion(ctx, runID)
	}
	started, err := current.StartRun(ctx, application.StartRunRequest{
		RunID: runID, ExpectedSequence: before.Sequence, ExpectedAuthorityHead: before.AuthorityHead,
	})
	if err != nil {
		return application.RunProjection{}, "sealed StartRun 失败", err
	}
	if started.Run.State != domain.StateRunning {
		return started.Run, "", nil
	}
	// StartRun committed process-started under the first owner. Close that
	// repository application before acquiring the single successor that will
	// Attach/rebind and remain resident for all terminal probes and collection.
	if err := closeCurrent(); err != nil {
		return application.RunProjection{}, "sealed owner successor 切换失败", err
	}
	closed = true
	successor, closeSuccessor, err := open(ctx)
	if err != nil {
		return application.RunProjection{}, "sealed owner successor 组装失败", err
	}
	if successor == nil || closeSuccessor == nil {
		if closeSuccessor != nil {
			_ = closeSuccessor()
		}
		return application.RunProjection{}, "sealed owner successor 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	defer closeSuccessor()
	return successor.driveRunToWorkerCompletion(ctx, runID)
}

func waitForSealedRunPoll(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// driveSealedRunToWorkerCompletionWithOpen is the testable resident-driver
// core after the repository owner is already current for the RUNNING Attempt.
// The exact Run runtime is opened once and closed once; every terminal probe
// therefore uses the same owner and held objects. Public server StartRun
// remains non-blocking—only the explicit foreground CLI --wait path calls this
// helper.
func driveSealedRunToWorkerCompletionWithOpen(ctx context.Context, runID string, open sealedRunOpenFunc, wait sealedRunWaitFunc) (application.RunProjection, string, error) {
	if open == nil {
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	if wait == nil {
		return application.RunProjection{}, "sealed Run 等待器无效", application.NewError("sealed-run-wait", application.ReasonInvalidRequest)
	}
	runtime, closeRun, err := open(ctx, runID)
	if err != nil {
		return application.RunProjection{}, "sealed Run 组装失败", err
	}
	if runtime == nil || closeRun == nil {
		if closeRun != nil {
			_ = closeRun()
		}
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	defer closeRun()
	for {
		after, stage, err := advanceSealedRun(ctx, runtime, runID)
		if err != nil || after.State != domain.StateRunning {
			return after, stage, err
		}
		if err := wait(ctx); err != nil {
			return application.RunProjection{}, "等待 sealed Attempt 终态失败", err
		}
	}
}

// advanceSealedRunWithOpen makes the one-composition boundary explicit and
// testable. The returned runtime remains alive for the complete bounded
// Inspect -> Start/Collect -> Inspect transaction.
func advanceSealedRunWithOpen(ctx context.Context, runID string, open sealedRunOpenFunc) (application.RunProjection, string, error) {
	if open == nil {
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	runtime, closeRun, err := open(ctx, runID)
	if err != nil {
		return application.RunProjection{}, "sealed Run 组装失败", err
	}
	if runtime == nil || closeRun == nil {
		if closeRun != nil {
			_ = closeRun()
		}
		return application.RunProjection{}, "sealed Run 组装失败", application.NewError("sealed-run-open", application.ReasonCompositionIncomplete)
	}
	defer closeRun()
	return advanceSealedRun(ctx, runtime, runID)
}

func advanceSealedRun(ctx context.Context, runtime sealedRunAdvancer, runID string) (application.RunProjection, string, error) {
	before, err := runtime.InspectRun(ctx, application.InspectRunRequest{RunID: runID})
	if err != nil {
		return application.RunProjection{}, "当前 Run 权威投影不可读", err
	}
	switch before.State {
	case domain.StateReady:
		started, err := runtime.StartRun(ctx, application.StartRunRequest{
			RunID: runID, ExpectedSequence: before.Sequence, ExpectedAuthorityHead: before.AuthorityHead,
		})
		if err != nil {
			return application.RunProjection{}, "sealed StartRun 失败", err
		}
		return started.Run, "", nil
	case domain.StateRunning:
		if _, err := runtime.CollectRunResult(ctx, runID); errors.Is(err, productionruntime.ErrAttemptStillRunning) {
			// The RUNNING projection was already verified by InspectRun. Do not
			// inspect again after the terminal probe: the worker may exit between
			// those operations, making a second composition observe a new owner
			// epoch before CollectRunResult has durably admitted the terminal
			// result. The next bounded advance will collect from fresh authority.
			return before, "", nil
		} else if err != nil {
			return application.RunProjection{}, "sealed CollectRunResult 失败", err
		}
	}
	after, err := runtime.InspectRun(ctx, application.InspectRunRequest{RunID: runID})
	if err != nil {
		return application.RunProjection{}, "sealed 推进后状态不可读", err
	}
	return after, "", nil
}

func (adapter *sealedRepositoryApplication) InspectRun(ctx context.Context, request application.InspectRunRequest) (application.RunProjection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	run, err := adapter.openRun(ctx, request.RunID)
	if err != nil {
		return application.RunProjection{}, err
	}
	defer run.Close()
	return run.runtime.InspectRun(ctx, request)
}

type sealedComposedRun struct {
	runtime  *productionruntime.Runtime
	closure  *launchidentity.HeldClosure
	worktree *existingWorktreeCompositionHandles
}

func (run *sealedComposedRun) Close() error {
	if run == nil {
		return nil
	}
	var closeErrors []error
	if run.runtime != nil {
		closeErrors = append(closeErrors, run.runtime.Close())
	}
	if run.closure != nil {
		run.closure.Close()
	}
	if run.worktree != nil {
		closeErrors = append(closeErrors, run.worktree.Close())
	}
	return errors.Join(closeErrors...)
}

func (adapter *sealedRepositoryApplication) openRun(ctx context.Context, runID string) (_ *sealedComposedRun, err error) {
	if adapter.closed || adapter.session == nil {
		return nil, application.NewError("sealed-repository-application", application.ReasonBridgeUnavailable)
	}
	preReadLease, err := adapter.runs.AcquireExisting(runID)
	if err != nil {
		return nil, err
	}
	preReadReleased := false
	defer func() {
		if !preReadReleased {
			_ = preReadLease.Release()
		}
	}()
	projection, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, preReadLease)
	if err != nil {
		return nil, err
	}
	// Freeze the descriptor graph while the exact Run projection is still
	// protected by its pre-read lease. Releasing first would leave a pathname
	// race between WorktreePath authority and the held target object.
	worktree, err := openExistingWorktreeComposition(adapter.repositoryRoot, projection.WorktreePath)
	if err != nil {
		return nil, err
	}
	taskData, err := runstore.ReadFileUnderLease(preReadLease, 2<<20, "task-spec.json")
	if err != nil {
		_ = worktree.Close()
		return nil, err
	}
	if err := preReadLease.Release(); err != nil {
		_ = worktree.Close()
		return nil, err
	}
	preReadReleased = true
	run := &sealedComposedRun{worktree: worktree}
	defer func() {
		if err != nil {
			_ = run.Close()
		}
	}()
	appInstance, err := app.New()
	if err != nil {
		return nil, err
	}
	task, err := appInstance.ParseTaskSpec(taskData)
	if err != nil {
		return nil, err
	}
	requirements, err := productionruntime.Pi0844Requirements(task.Worker.ExecutionProfile)
	if err != nil {
		return nil, err
	}
	heldClosure, err := launchidentity.OpenPi0844(adapter.piRuntime, adapter.piEntrypoint, []string{adapter.piRuntime, adapter.piEntrypoint}, nil, projection.WorktreePath)
	if err != nil {
		return nil, err
	}
	run.closure = heldClosure
	identity, err := launchidentity.Pi0844IdentityFromClosure(heldClosure.Closure)
	if err != nil {
		return nil, err
	}
	profile, err := productionruntime.NewPi0844Profile(heldClosure.Closure.RuntimeExecutable.CanonicalPath, adapter.piEntrypoint, identity.IdentityDigest)
	if err != nil {
		return nil, err
	}
	composed, err := productionruntime.ComposeRuntime(ctx, productionruntime.CompositionInputs{
		RepositorySession: adapter.session, Runs: adapter.runs, LeaseLedger: adapter.leaseLedger, RunID: runID,
		Namespace: adapter.namespace, OrchestratorID: "orchestrator:" + projection.Run.TaskID,
		ProvisionDomain: adapter.providerDomain, CleanupDomain: productionruntime.LocalCleanupDomain(adapter.namespace),
		RegistrationID: adapter.registration.RegistrationId, CapabilitySnapshot: adapter.snapshot.ProviderCapabilitySnapshotDigest,
		Attestation: adapter.attestation, AllocationRoot: adapter.allocationRoot,
		ProviderStore: adapter.providerStore, ProviderRegistration: adapter.registration, ProviderSnapshot: adapter.snapshot,
		ResultIngressDomain: productionruntime.LocalResultIngressDomain(adapter.namespace),
		LaunchClosure:       heldClosure.Closure, Requirements: requirements,
		WorkDirAllowlist: []string{projection.WorktreePath}, EnvironmentAllowlist: []string{"PATH"},
		ExistingWorktreeDescriptorGraph: worktree.graph, ExistingWorktreeTargetWorktree: worktree.target,
		LaunchArgvBuilder: piProductionLaunchBuilder(adapter.piRuntime, adapter.piEntrypoint, task),
		ResultParser: func(parserCtx context.Context, input productionruntime.AttemptResultInput) (domain.Record, error) {
			return piadapter.ParseProductionWorkerResult(parserCtx, piadapter.ProductionResultInput{
				Transcript: input.Transcript, Worktree: input.Worktree,
				TaskID: input.TaskID, RunID: input.RunID, AttemptID: input.AttemptID,
				Executable: input.Executable, Version: input.Version, Model: task.Worker.Model,
				StartedAt: input.StartedAt, CompletedAt: input.CompletedAt, MaxOutputBytes: input.MaxOutputBytes,
			})
		},
		EntryLocalSelfIdentity: adapter.entryIdentity, ObserveLocalSelfIdentity: adapter.observeIdentity,
	}, profile)
	if err != nil {
		return nil, err
	}
	run.runtime = composed.Runtime
	return run, nil
}
