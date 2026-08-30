package productionruntime

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// DarwinLocalDogfoodFactoryConfig is the fixed-CLI composition input for the
// Darwin ordinary-user profile. It intentionally accepts only already-open
// authority handles and exact runtime seams; it does not create an Attempt,
// dispatch lease, allocation, or PreparedExecution.
//
// The producer that creates those durable facts must run before this factory.
// Keeping that producer outside this constructor prevents a partially
// composed Runtime from being mistaken for an S2' production Attempt.
type DarwinLocalDogfoodFactoryConfig struct {
	Authority DurableRunAuthority
	Bridge    ProcessBridge

	// OwnerDirectory is an already-held descriptor for the canonical owner
	// directory. The factory duplicates what it needs and never reopens it by
	// pathname.
	OwnerDirectory *os.File
	OwnerScope     resultingress.ControlOwnerScope
	Store          *resultingress.DurableStore

	ExpectedOwnerEpoch      uint64
	ExpectedOwnerFactDigest string
	Acquisition             resultingress.ControlOwnerAcquisition
	FixedMarshalPath        string
	Profile                 PiProfile

	// Resources are transferred to Runtime on success and closed in reverse
	// order on Runtime.Close. The owner directory is not transferred because
	// ownership remains with the caller that supplied its descriptor.
	Resources []io.Closer
}

// NewDarwinLocalDogfoodRuntime creates the ready Runtime only after all
// mandatory composition inputs pass closed validation and the current Core
// binary exactly matches the durable owner acquisition. It does not perform
// any Attempt or allocation mutation; callers must provide a real durable
// producer chain first.
func NewDarwinLocalDogfoodRuntime(ctx context.Context, config DarwinLocalDogfoodFactoryConfig, exactProcess sandboxbridge.ExactProcessRuntime, exactAllocation sandboxbridge.ExactAllocationRuntime) (*Runtime, error) {
	if ctx == nil {
		return nil, application.NewError("production-runtime", application.ReasonInvalidRequest)
	}
	if _, err := platformProfile(); err != nil {
		return nil, err
	}
	if err := validateDarwinFactoryConfig(config, exactProcess, exactAllocation); err != nil {
		return nil, err
	}
	binder, ok := config.Bridge.(sandboxbridge.ExactRuntimeBinder)
	if !ok {
		return nil, application.NewError("production-runtime", application.ReasonCompositionIncomplete)
	}
	// Install both exact runtime seams before any durable owner fact is
	// appended. A malformed/incompatible bridge therefore fails without
	// consuming an owner epoch.
	if err := binder.BindExactRuntimes(exactProcess, exactAllocation); err != nil {
		return nil, application.NewError("production-runtime", application.ReasonCompositionIncomplete)
	}

	// Observe the fixed image immediately before owner acquisition. The
	// resulting durable fact is therefore bound to this exact Core, not to a
	// caller-supplied digest or PATH alias.
	core, err := processsupervisor.ObserveCurrentCore(config.FixedMarshalPath)
	if err != nil || core.UID != config.Acquisition.OwnerUID || core.GID != config.Acquisition.OwnerGID || core.Process != config.Acquisition.OwnerProcess || core.Binary != config.Acquisition.OwnerBinary {
		return nil, application.NewError("production-runtime", application.ReasonOwnerNotCurrent)
	}

	scopeLock, err := openRepositoryOwnerScopeLock(config.OwnerDirectory, config.OwnerScope)
	if err != nil {
		return nil, err
	}
	closeScope := true
	defer func() {
		if closeScope {
			_ = scopeLock.Close()
		}
	}()

	if _, err := scopeLock.acquireOwner(ctx, config.Store, config.ExpectedOwnerEpoch, config.ExpectedOwnerFactDigest, config.Acquisition); err != nil {
		return nil, err
	}
	ownerLock, err := scopeLock.bindAcquisition(config.Store)
	if err != nil {
		return nil, err
	}
	// The phase-A and phase-B locks share the same physical descriptor-backed
	// lock. Do not close scopeLock here: its Close method would also close the
	// physical lock now owned by ownerLock.
	closeScope = false

	controller, err := newController(config.Authority, config.Bridge, ownerLock, config.Acquisition, config.Profile)
	if err != nil {
		_ = ownerLock.Close()
		return nil, err
	}
	runtime, err := newProductionRuntime(controller, config.Resources...)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func validateDarwinFactoryConfig(config DarwinLocalDogfoodFactoryConfig, exactProcess sandboxbridge.ExactProcessRuntime, exactAllocation sandboxbridge.ExactAllocationRuntime) error {
	if config.Authority == nil || config.Bridge == nil || config.OwnerDirectory == nil || config.Store == nil || config.OwnerScope.Validate() != nil || config.Acquisition.Validate() != nil || config.Acquisition.Scope != config.OwnerScope || config.Profile.Validate() != nil || config.FixedMarshalPath == "" || !filepath.IsAbs(config.FixedMarshalPath) || filepath.Clean(config.FixedMarshalPath) != config.FixedMarshalPath || config.Acquisition.OwnerBinary.CanonicalPath != config.FixedMarshalPath {
		return application.NewError("production-runtime", application.ReasonInvalidRequest)
	}
	if _, ok := config.Bridge.(sandboxbridge.ExactRuntimeBinder); !ok {
		return application.NewError("production-runtime", application.ReasonCompositionIncomplete)
	}
	if exactProcess.Resolve == nil || exactProcess.Retain == nil || exactAllocation.Resolve == nil {
		return application.NewError("production-runtime", application.ReasonCompositionIncomplete)
	}
	if config.Acquisition.OwnerEpoch != config.ExpectedOwnerEpoch+1 {
		return application.NewError("production-runtime", application.ReasonAuthorityConflict)
	}
	if config.ExpectedOwnerEpoch == 0 && config.ExpectedOwnerFactDigest != "" {
		return application.NewError("production-runtime", application.ReasonAuthorityConflict)
	}
	if config.ExpectedOwnerEpoch > 0 && config.ExpectedOwnerFactDigest == "" {
		return application.NewError("production-runtime", application.ReasonAuthorityConflict)
	}
	return nil
}
