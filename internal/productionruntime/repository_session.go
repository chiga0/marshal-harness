package productionruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// RepositorySessionInputs freezes the repository-wide resources owned by a
// long-lived fixed Marshal process. Run-specific stores, leases and provider
// inputs deliberately remain in CompositionInputs.
type RepositorySessionInputs struct {
	HeldIngressDir          *os.File
	HeldRepositoryRoot      *CanonicalRepositoryRoot
	OwnerDirectory          *os.File
	Acquisition             resultingress.ControlOwnerAcquisition
	FixedMarshalPath        string
	OwnerPrivateControlRoot *os.File
}

// RepositorySession owns one repository owner acquisition and the sealed
// ResultIngress store for the lifetime of a fixed Marshal process. Individual
// Run runtimes borrow these resources and cannot close or reacquire them.
type RepositorySession struct {
	mu          sync.RWMutex
	closed      bool
	ingress     *resultingress.DurableStore
	runs        *runstore.Store
	fixedRoot   fixedServerRoot
	owner       repositoryOwnerLock
	ownerState  resultingress.ControlOwnerState
	acquisition resultingress.ControlOwnerAcquisition
	fixedPath   string
}

type repositorySessionBorrow struct {
	once    sync.Once
	session *RepositorySession
}

var _ io.Closer = (*RepositorySession)(nil)
var _ io.Closer = (*repositorySessionBorrow)(nil)

// OpenRepositorySession performs the repository-wide portion of production
// composition exactly once: Phase-A physical locking, durable owner append and
// replay, Phase-B binding, sealed ResultIngress construction, and the runtime
// claim. The held ingress is opened only after the physical owner lock wins.
func OpenRepositorySession(ctx context.Context, inputs RepositorySessionInputs) (*RepositorySession, error) {
	if ctx == nil || inputs.HeldIngressDir == nil || inputs.HeldRepositoryRoot == nil || inputs.OwnerDirectory == nil ||
		inputs.FixedMarshalPath == "" || inputs.OwnerPrivateControlRoot == nil ||
		validateCompositionAcquisitionCandidate(inputs.Acquisition) != nil {
		return nil, application.NewError("repository-session", application.ReasonInvalidRequest)
	}
	phase, err := openRepositoryOwnerScopeLock(inputs.OwnerDirectory, inputs.Acquisition.Scope)
	if err != nil {
		return nil, err
	}
	ingress, err := resultingress.OpenDarwinResultIngressStore(inputs.HeldIngressDir)
	if err != nil {
		_ = phase.Close()
		return nil, err
	}
	owner, ownerState, acquisition, err := phase.acquireAndBind(ctx, ingress, inputs.Acquisition)
	if err != nil {
		_ = ingress.Close()
		_ = phase.Close()
		return nil, err
	}
	if err := phase.Close(); err != nil {
		_ = ingress.Close()
		_ = owner.Close()
		return nil, err
	}
	// WithCurrentOwnerLock deliberately admits only the one runtime-claimed
	// owner. Claim before the first canonical-root recheck; every later
	// construction failure closes the claimed owner and releases its lock.
	if err := claimRepositorySessionOwner(owner); err != nil {
		_ = ingress.Close()
		_ = owner.Close()
		return nil, err
	}
	var fixedRoot fixedServerRoot
	err = owner.WithCurrentOwnerLock(ctx, acquisition, func() error {
		current, found, openErr := ingress.OpenOwner(acquisition.Scope)
		if openErr != nil || !found || current.Acquisition != acquisition || current.FactDigest != ownerState.FactDigest {
			return application.NewError("repository-session-root", application.ReasonOwnerNotCurrent)
		}
		var rootErr error
		fixedRoot, rootErr = openFixedServerRoot(inputs.HeldRepositoryRoot)
		return rootErr
	})
	if err != nil {
		_ = ingress.Close()
		_ = owner.Close()
		return nil, err
	}
	runs, err := runstore.NewFromStateRootDescriptor(fixedRoot.stateRoot())
	if err != nil {
		_ = fixedRoot.close()
		_ = ingress.Close()
		_ = owner.Close()
		return nil, err
	}
	cleanup := func() {
		_ = runs.Close()
		_ = fixedRoot.close()
		_ = ingress.Close()
		_ = owner.Close()
	}
	borrowed := &borrowedOwnerVerifier{acquisition: acquisition, active: true}
	_, err = resultingress.SealPi0844DarwinPreparedExecutionStore(ctx, ingress, borrowed, resultingress.CurrentOwnerBinding{
		Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: ownerState.FactDigest,
	}, inputs.FixedMarshalPath, inputs.OwnerPrivateControlRoot)
	borrowed.close()
	if err != nil {
		cleanup()
		return nil, err
	}
	session := &RepositorySession{ingress: ingress, runs: runs, fixedRoot: fixedRoot, owner: owner, ownerState: ownerState, acquisition: acquisition, fixedPath: inputs.FixedMarshalPath}
	if err := session.owner.WithCurrentOwnerLock(ctx, acquisition, func() error {
		current, found, openErr := ingress.OpenOwner(acquisition.Scope)
		if openErr != nil || !found || current.Acquisition != acquisition || current.FactDigest != ownerState.FactDigest {
			return application.NewError("repository-session-root", application.ReasonOwnerNotCurrent)
		}
		return validateFixedServerRoot(fixedRoot, 5)
	}); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (session *RepositorySession) borrow() (*repositorySessionBorrow, error) {
	if session == nil {
		return nil, application.NewError("repository-session", application.ReasonOwnerUnavailable)
	}
	session.mu.RLock()
	if session.closed || session.ingress == nil || session.owner == nil || !session.owner.claimed() {
		session.mu.RUnlock()
		return nil, application.NewError("repository-session", application.ReasonOwnerUnavailable)
	}
	return &repositorySessionBorrow{session: session}, nil
}

// OwnerProjection rechecks and returns the current repository owner bound to
// this live session. Repository-wide Run recovery belongs to the application
// assembler; this method only projects the owner fact and never infers Run
// state from the authority ledger.
func (session *RepositorySession) OwnerProjection(ctx context.Context) (OwnerProjection, error) {
	borrow, err := session.borrow()
	if err != nil {
		return OwnerProjection{}, err
	}
	defer borrow.Close()
	var projection OwnerProjection
	err = session.owner.WithCurrentOwnerLock(ctx, session.acquisition, func() error {
		state, found, openErr := session.ingress.OpenOwner(session.acquisition.Scope)
		if openErr != nil {
			return openErr
		}
		if !found || state.Acquisition != session.acquisition || state.FactDigest != session.ownerState.FactDigest {
			return application.NewError("repository-session-owner", application.ReasonOwnerNotCurrent)
		}
		projection = OwnerProjection{OwnerEpoch: state.Acquisition.OwnerEpoch, OwnerFactDigest: state.FactDigest}
		return nil
	})
	return projection, err
}

// ReconcileStartRun reads the exact current PreparedExecution/RUNNING pair
// without composing a Run runtime. Fixed-delivery reconciliation happens
// after StartRun has already released its short-lived runtime; reopening that
// runtime here would run Attach/recovery and turn a read-only response-loss
// check into a lifecycle mutation.
//
// Lock order remains Run lease -> repository owner -> ResultIngress. The
// repository owner callback lends a non-reentrant verifier to ResultIngress,
// so the complete cross-ledger join is observed under one current owner.
func (session *RepositorySession) ReconcileStartRun(ctx context.Context, request application.StartRunRequest) (result application.RunStartProjection, found bool, resultErr error) {
	if ctx == nil {
		return application.RunStartProjection{}, false, application.NewError("reconcile-start-run", application.ReasonInvalidRequest)
	}
	if err := request.Validate(); err != nil {
		return application.RunStartProjection{}, false, err
	}
	borrow, err := session.borrow()
	if err != nil {
		return application.RunStartProjection{}, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, borrow.Close()) }()

	lease, err := session.runs.AcquireExisting(request.RunID)
	if err != nil {
		return application.RunStartProjection{}, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()

	err = session.owner.WithCurrentOwnerLock(ctx, session.acquisition, func() error {
		current, currentFound, openErr := session.ingress.OpenOwner(session.acquisition.Scope)
		if openErr != nil {
			return reconcileStartRunStageError("reconcile-start-run-owner-read", openErr)
		}
		if !currentFound || current.Acquisition != session.acquisition || current.FactDigest != session.ownerState.FactDigest {
			return application.NewError("reconcile-start-run", application.ReasonOwnerNotCurrent)
		}
		verifier := &borrowedOwnerVerifier{acquisition: session.acquisition, active: true}
		defer verifier.close()

		resolved, resolveErr := session.ingress.ResolvePreparedRunStart(ctx, verifier, session.acquisition, resultingress.PreparedRunStartKey{
			RunID: request.RunID, ReadySequence: request.ExpectedSequence, ReadyAuthorityHead: request.ExpectedAuthorityHead,
		})
		if errors.Is(resolveErr, resultingress.ErrPreparedRunStartNotFound) {
			return nil
		}
		if resolveErr != nil {
			return reconcileStartRunStageError("reconcile-start-run-prepared-lookup", resolveErr)
		}
		prepared, prepareErr := session.ingress.PrepareMacRunStart(ctx, verifier, session.acquisition, resolved.PreparationDigest)
		if prepareErr != nil {
			return reconcileStartRunStageError("reconcile-start-run-prepared-projection", prepareErr)
		}
		if prepared.RunID != request.RunID || prepared.Sequence != request.ExpectedSequence || prepared.AuthorityHead != request.ExpectedAuthorityHead {
			return application.NewError("reconcile-start-run", application.ReasonAuthorityConflict)
		}
		authority, readErr := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
		if readErr != nil {
			return reconcileStartRunStageError("reconcile-start-run-runstore-read", readErr)
		}
		if authority.Run.State != domain.StateRunning || authority.PreparationDigest != prepared.PreparationDigest {
			return nil
		}
		candidate := application.RunStartProjection{Prepared: prepared, Run: authority.Run}
		if candidate.Validate() != nil {
			return application.NewError("reconcile-start-run", application.ReasonAuthorityConflict)
		}
		result, found = candidate, true
		return nil
	})
	return result, found, err
}

// reconcileStartRunStageError retains the internal error for callers while
// exposing only a closed, path-free operation label at the server diagnostic
// boundary. It changes no admission or recovery decision.
func reconcileStartRunStageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(application.NewError(operation, application.ReasonAuthorityConflict), err)
}

func (borrow *repositorySessionBorrow) Close() error {
	if borrow == nil || borrow.session == nil {
		return nil
	}
	borrow.once.Do(func() { borrow.session.mu.RUnlock() })
	return nil
}

// Close waits for every borrowed Run runtime, rejects new borrows, closes the
// sealed ingress, and releases the repository owner last.
func (session *RepositorySession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	var closeErrors []error
	if session.ingress != nil {
		closeErrors = append(closeErrors, session.ingress.Close())
	}
	if session.runs != nil {
		closeErrors = append(closeErrors, session.runs.Close())
	}
	closeErrors = append(closeErrors, session.fixedRoot.close())
	if session.owner != nil {
		closeErrors = append(closeErrors, session.owner.Close())
	}
	return errors.Join(closeErrors...)
}
