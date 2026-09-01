package productionruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// RepositorySessionInputs freezes the repository-wide resources owned by a
// long-lived fixed Marshal process. Run-specific stores, leases and provider
// inputs deliberately remain in CompositionInputs.
type RepositorySessionInputs struct {
	HeldIngressDir          *os.File
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
	owner       repositoryOwnerLock
	ownerState  resultingress.ControlOwnerState
	acquisition resultingress.ControlOwnerAcquisition
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
	if ctx == nil || inputs.HeldIngressDir == nil || inputs.OwnerDirectory == nil ||
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
	ownerState, acquisition, err := acquireOwner(ingress, phase, inputs.Acquisition)
	if err != nil {
		_ = ingress.Close()
		_ = phase.Close()
		return nil, err
	}
	owner, err := phase.bindAcquisition(ingress)
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
	cleanup := func() {
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
	if err := claimRepositorySessionOwner(owner); err != nil {
		cleanup()
		return nil, err
	}
	return &RepositorySession{ingress: ingress, owner: owner, ownerState: ownerState, acquisition: acquisition}, nil
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
	if session.owner != nil {
		closeErrors = append(closeErrors, session.owner.Close())
	}
	return errors.Join(closeErrors...)
}
