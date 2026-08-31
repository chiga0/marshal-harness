//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"golang.org/x/sys/unix"
)

// heldAttachOwnerVerifier is the acquisition-bound pass-through that lets the
// read-only Attach exchange run while the orchestration already holds the
// repository physical owner lock via withCurrentOwnerLock. It never re-acquires
// the lock; it only proves the Attach authority still binds the exact held
// acquisition and invokes the borrowed callback exactly once (the once-only
// contract is enforced by processsupervisor.withAttachOwner).
type heldAttachOwnerVerifier struct {
	acquisition processsupervisor.AttachOwnerAcquisition
}

func (verifier heldAttachOwnerVerifier) WithCurrentAttachOwner(_ context.Context, authority processsupervisor.AttachAuthority, fn func() error) error {
	if fn == nil || authority.CurrentAcquisition != verifier.acquisition {
		return processsupervisor.ErrConflict
	}
	return fn()
}

// productionRebindTransport is the single production transport: it calls the
// real processsupervisor.WithAttached. It is wired directly into the exported
// RebindOwnerSuccessorForAttachedRecovery; tests inject a fake via the
// unexported rebindOwnerSuccessorForAttachedRecoveryWithTransport seam.
func productionRebindTransport(ctx context.Context, options processsupervisor.AttachOptions, rebind func(AttachedRebindSession) error) error {
	return processsupervisor.WithAttached(ctx, options, func(session *processsupervisor.AttachedSession) error {
		return rebind(session)
	})
}

// RebindOwnerSuccessorForAttachedRecovery implements the ADR 0067 §4
// held-owner recovery chain as one creation-once durable operation. The
// exported API has no transport parameter: it always uses the real
// processsupervisor.WithAttached. Test injection is via the unexported
// rebindOwnerSuccessorForAttachedRecoveryWithTransport seam.
//
// PRODUCTION REACHABILITY: productionruntime construction is the sole caller.
// It invokes this method while the exact phase-B repository owner remains held
// and before the Runtime becomes visible. The architecture test mechanically
// enforces that no second production callsite appears.
func (s *DurableStore) RebindOwnerSuccessorForAttachedRecovery(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity) (AttemptAuthorityState, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	return s.rebindOwnerSuccessorForAttachedRecoveryWithTransport(ctx, verifier, acquisition, identity, nil, profile.fixedMarshalPath, productionRebindTransport)
}

// rebindOwnerSuccessorForAttachedRecoveryWithTransport is the unexported test
// seam that accepts a transport injection. Production calls
// RebindOwnerSuccessorForAttachedRecovery which wires productionRebindTransport.
func (s *DurableStore) rebindOwnerSuccessorForAttachedRecoveryWithTransport(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport) (AttemptAuthorityState, error) {
	if s == nil || ctx == nil || verifier == nil || transport == nil {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	if err := acquisition.Validate(); err != nil {
		return AttemptAuthorityState{}, err
	}
	if acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	var result AttemptAuthorityState
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			key, err := identity.Key()
			if err != nil {
				return err
			}
			state, found := projection.attempts[key]
			if !found || state.Identity != identity {
				return ErrPreparedExecutionConflict
			}
			scopeKey, err := acquisition.Scope.key()
			if err != nil {
				return err
			}
			ownerState, ownerFound := projection.controlOwners[scopeKey]
			if !ownerFound || ownerState.Acquisition != acquisition {
				return ErrControlOwnerNotCurrent
			}
			if state.ProcessStartedDigest == "" || state.SupervisorStartedDigest == "" || state.SupervisorInterventionDigest != "" || state.SupervisorClosedDigest != "" {
				return ErrPreparedExecutionConflict
			}
			if controlDirectory == nil {
				profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
				if !ok || profile == nil || profile.controlRoot == nil || fixedMarshalPath != profile.fixedMarshalPath {
					return ErrPreparedExecutionUnavailable
				}
				var openErr error
				controlDirectory, openErr = openAttachedControlDirectory(profile, state)
				if openErr != nil {
					return openErr
				}
				defer controlDirectory.Close()
			}
			if state.SupervisorBoundAuthorityHead != "" && state.SupervisorBoundAuthorityHead == state.HeadDigest {
				result = state
				return nil
			}
			preAnchor := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
			if state.SupervisorPendingIntentDigest != "" {
				if validateRebindPendingIntentForReplay(state, acquisition, preAnchor) != nil {
					return ErrPreparedExecutionConflict
				}
				result, err = s.replayPendingRebindLocked(ctx, projection, state, ownerState, identity, controlDirectory, fixedMarshalPath, transport)
				return err
			}
			successorAlreadyHeld := state.HeadDigest != state.ProcessStartedDigest &&
				state.Owner.OwnerEpoch == acquisition.OwnerEpoch &&
				state.Owner.ControlOwnerAcquiredFactDigest == ownerState.FactDigest &&
				state.SupervisorBoundAuthorityHead != "" &&
				state.SupervisorBoundAuthorityHead != state.HeadDigest
			var predecessorRevision uint64
			var predecessorHead string
			if successorAlreadyHeld {
				predecessorRevision = state.Revision - 1
				predecessorHead = state.ProcessStartedDigest
			} else {
				if state.HeadDigest != state.ProcessStartedDigest {
					return ErrPreparedExecutionConflict
				}
				owner := CurrentOwnerBinding{Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: ownerState.FactDigest}
				predecessorRevision, predecessorHead = state.Revision, state.HeadDigest
				state, _, err = s.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionControlOwnerBound, Identity: identity, Owner: owner})
				if err != nil {
					return fmt.Errorf("rebind successor append: %w", err)
				}
			}
			result, err = s.executeFreshRebindLocked(ctx, projection, state, ownerState, identity, controlDirectory, fixedMarshalPath, transport, preAnchor, predecessorRevision, predecessorHead)
			return err
		})
	})
	return result, err
}

// openAttachedControlDirectory resolves the exact durable Supervisor session
// below the already-held private control root. Production recovery never
// reopens a caller-supplied absolute path: both the root and child object are
// re-observed and matched to the identities frozen by ResultIngress.
func openAttachedControlDirectory(profile *preparedDarwinExecutionProfile, state AttemptAuthorityState) (*os.File, error) {
	currentCore, err := processsupervisor.ObserveCurrentCore(profile.fixedMarshalPath)
	if err != nil || currentCore != profile.core {
		return nil, ErrPreparedExecutionUnavailable
	}
	root, err := processsupervisor.ObserveHeldControlDirectory(profile.controlRoot)
	if err != nil || root != profile.controlIdentity {
		return nil, ErrPreparedExecutionUnavailable
	}
	sessionID := state.SupervisorStarted.Handshake.SessionID
	fd, err := unix.Openat(int(profile.controlRoot.Fd()), sessionID, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	directory := os.NewFile(uintptr(fd), "marshal-attached-control-directory")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, ErrPreparedExecutionUnavailable
	}
	observed, err := processsupervisor.ObserveHeldControlDirectory(directory)
	if err != nil || observed != state.SupervisorStarted.ControlDirectory {
		_ = directory.Close()
		return nil, ErrPreparedExecutionUnavailable
	}
	return directory, nil
}

func (s *DurableStore) executeFreshRebindLocked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, ownerState ControlOwnerState, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport, preAnchor processsupervisor.HandshakeAnchor, predecessorRevision uint64, predecessorHead string) (AttemptAuthorityState, error) {
	bindPayload := processsupervisor.BindAuthorityPayload{
		SupervisorStartedFactDigest: state.SupervisorStartedDigest,
		OwnerEpoch:                  ownerState.Acquisition.OwnerEpoch,
		PreviousAuthorityHead:       preAnchor.CurrentAuthorityHead,
		AuthorityHead:               state.HeadDigest,
	}
	prepared, prepareErr := processsupervisor.PrepareCommand(preAnchor, processsupervisor.CommandOptions{
		Command: processsupervisor.CommandBindAuthority, CommandID: fmt.Sprintf("rebind-owner-successor-%d", state.SupervisorCommandSequence+1),
		Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead, CurrentAuthorityHead: preAnchor.CurrentAuthorityHead,
		Deadline: s.authorityNow().Add(20 * time.Second),
	}, bindPayload)
	if prepareErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind prepare: %w", prepareErr)
	}
	intent, intentErr := NewSupervisorCommandIntent(prepared.Evidence())
	if intentErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind intent: %w", intentErr)
	}
	attachAcquisition := processsupervisor.AttachOwnerAcquisition{
		AuthorityNamespaceID: identity.AuthorityNamespaceRef, RepositoryIdentityDigest: ownerState.Acquisition.Scope.RepositoryIdentityDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, OwnerUID: ownerState.Acquisition.OwnerUID, OwnerGID: ownerState.Acquisition.OwnerGID,
		OwnerProcess: ownerState.Acquisition.OwnerProcess, OwnerBinary: ownerState.Acquisition.OwnerBinary,
		ObserverIdentity: ownerState.Acquisition.ObserverIdentity, ObservedAt: ownerState.Acquisition.ObservedAt,
		PreviousFactDigest: ownerState.PreviousFactDigest, FactDigest: ownerState.FactDigest,
	}
	attachBoundFact := processsupervisor.AttachOwnerBoundFact{
		Authority: supervisorAuthorityTuple(identity), PreviousAttemptRevision: predecessorRevision, PreviousAttemptHead: predecessorHead,
		AttemptRevision: state.Revision, AttemptHead: state.HeadDigest, ControlOwnerAcquiredFactDigest: ownerState.FactDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, FactDigest: state.ControlOwnerBindingDigest,
	}
	attachAuthority := processsupervisor.AttachAuthority{
		PreviousSupervisor: preAnchor, Supervisor: state.SupervisorStarted.Handshake.SupervisorProcess,
		CurrentAcquisition: attachAcquisition, CurrentOwnerBoundFact: attachBoundFact,
		Child: state.ProcessStartedEvidence.Outcome.Process, ChildObservationDigest: supervisorLastObservation(state),
	}
	options := processsupervisor.AttachOptions{
		FixedMarshalPath: fixedMarshalPath, ControlDirectory: controlDirectory,
		ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory, Authority: attachAuthority,
		OwnerVerifier: heldAttachOwnerVerifier{acquisition: attachAcquisition},
	}
	var outcome processsupervisor.VerifiedCommandOutcome
	transportErr := transport(ctx, options, func(session AttachedRebindSession) error {
		observation, observeErr := session.Observation()
		if observeErr != nil {
			return observeErr
		}
		if validateRebindObservation(observation, attachAuthority) != nil {
			return fmt.Errorf("rebind observation mismatch: %w", ErrPreparedExecutionConflict)
		}
		var intentAppendErr error
		state, _, intentAppendErr = s.appendRebindSupervisorIntentLocked(projection, state, intent)
		if intentAppendErr != nil {
			return fmt.Errorf("rebind intent append: %w", intentAppendErr)
		}
		outcome, observeErr = session.ExecutePreparedBindAuthority(ctx, prepared)
		return observeErr
	})
	if transportErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind transport: %w", transportErr)
	}
	evidence, evidenceErr := NewSupervisorCommandEvidence(outcome)
	if evidenceErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind evidence: %w", evidenceErr)
	}
	state, _, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
	if err != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind outcome append: %w", err)
	}
	if state.SupervisorBoundAuthorityHead != state.HeadDigest {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	return state, nil
}

func (s *DurableStore) replayPendingRebindLocked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, ownerState ControlOwnerState, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport) (AttemptAuthorityState, error) {
	intent := state.SupervisorPendingIntent
	deadline, deadlineErr := time.Parse(time.RFC3339Nano, intent.Deadline)
	if deadlineErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind replay deadline: %w", deadlineErr)
	}
	prepared, prepareErr := processsupervisor.PrepareCommand(supervisorHandshakeAnchor(intent.PreCommand), processsupervisor.CommandOptions{
		Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence,
		PreviousCommandDigest: intent.PreviousCommandHead, CurrentAuthorityHead: intent.CurrentAuthorityHead,
		Deadline: deadline,
	}, processsupervisor.BindAuthorityPayload{
		SupervisorStartedFactDigest: intent.Rebuild.SupervisorStartedFactDigest, OwnerEpoch: intent.Rebuild.OwnerEpoch,
		PreviousAuthorityHead: intent.Rebuild.PreviousAuthorityHead, AuthorityHead: intent.Rebuild.AuthorityHead,
	})
	if prepareErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind replay prepare: %w", prepareErr)
	}
	if prepared.Evidence().RequestDigest != intent.RequestDigest {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	attachAcquisition := processsupervisor.AttachOwnerAcquisition{
		AuthorityNamespaceID: identity.AuthorityNamespaceRef, RepositoryIdentityDigest: ownerState.Acquisition.Scope.RepositoryIdentityDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, OwnerUID: ownerState.Acquisition.OwnerUID, OwnerGID: ownerState.Acquisition.OwnerGID,
		OwnerProcess: ownerState.Acquisition.OwnerProcess, OwnerBinary: ownerState.Acquisition.OwnerBinary,
		ObserverIdentity: ownerState.Acquisition.ObserverIdentity, ObservedAt: ownerState.Acquisition.ObservedAt,
		PreviousFactDigest: ownerState.PreviousFactDigest, FactDigest: ownerState.FactDigest,
	}
	attachBoundFact := processsupervisor.AttachOwnerBoundFact{
		Authority: supervisorAuthorityTuple(identity), PreviousAttemptRevision: state.Revision - 1, PreviousAttemptHead: state.ProcessStartedDigest,
		AttemptRevision: state.Revision, AttemptHead: state.HeadDigest, ControlOwnerAcquiredFactDigest: ownerState.FactDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, FactDigest: state.ControlOwnerBindingDigest,
	}
	attachAuthority := processsupervisor.AttachAuthority{
		PreviousSupervisor: supervisorHandshakeAnchor(intent.PreCommand), Supervisor: state.SupervisorStarted.Handshake.SupervisorProcess,
		CurrentAcquisition: attachAcquisition, CurrentOwnerBoundFact: attachBoundFact,
		Child: state.ProcessStartedEvidence.Outcome.Process, ChildObservationDigest: supervisorLastObservation(state),
	}
	options := processsupervisor.AttachOptions{
		FixedMarshalPath: fixedMarshalPath, ControlDirectory: controlDirectory,
		ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory, Authority: attachAuthority,
		OwnerVerifier: heldAttachOwnerVerifier{acquisition: attachAcquisition},
	}
	var outcome processsupervisor.VerifiedCommandOutcome
	transportErr := transport(ctx, options, func(session AttachedRebindSession) error {
		observation, observeErr := session.Observation()
		if observeErr != nil {
			return observeErr
		}
		if validateRebindObservation(observation, attachAuthority) != nil {
			return fmt.Errorf("rebind replay observation mismatch: %w", ErrPreparedExecutionConflict)
		}
		outcome, observeErr = session.ExecutePreparedBindAuthority(ctx, prepared)
		return observeErr
	})
	if transportErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind replay transport: %w", transportErr)
	}
	evidence, evidenceErr := NewSupervisorCommandEvidence(outcome)
	if evidenceErr != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind replay evidence: %w", evidenceErr)
	}
	key, err := state.Identity.Key()
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	if _, _, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence); err != nil {
		return AttemptAuthorityState{}, fmt.Errorf("rebind replay outcome append: %w", err)
	}
	finalState, found := projection.attempts[key]
	if !found {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	return finalState, nil
}

func (s *DurableStore) appendRebindSupervisorIntentLocked(projection *Ingress, state AttemptAuthorityState, intent SupervisorCommandIntent) (AttemptAuthorityState, string, error) {
	key, err := state.Identity.Key()
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	current, found := projection.attempts[key]
	if !found || !samePreparedAuthorityState(current, state) || state.SupervisorPendingIntentDigest != "" || validateRebindSupervisorIntentAgainstState(state, intent) != nil {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	fact := &supervisorCommandFact{ProtocolRevision: supervisorCommandProtocolRevision, FactType: supervisorCommandIntentFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Intent: intent}
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	s.nextSequence++
	if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
		return AttemptAuthorityState{}, "", fmt.Errorf("resultingress: appended rebind supervisor intent failed projection: %w", err)
	}
	return projection.attempts[key], fact.Digest, nil
}
