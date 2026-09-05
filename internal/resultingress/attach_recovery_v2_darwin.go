//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type attachedRebindSessionV2 interface {
	Observation() (processsupervisor.AttachObservationV2, error)
	ExecutePreparedBindAuthority(context.Context, processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
}

type rebindTransportV2 func(context.Context, processsupervisor.AttachOptionsV2, func(attachedRebindSessionV2) error) error

type heldAttachOwnerVerifierV2 struct {
	authority processsupervisor.AttachAuthorityV2
}

func (v heldAttachOwnerVerifierV2) WithCurrentAttachOwnerV2(ctx context.Context, a processsupervisor.AttachAuthorityV2, fn func() error) error {
	if ctx == nil || ctx.Err() != nil || fn == nil || a != v.authority || a.Validate() != nil {
		return ErrPreparedExecutionConflict
	}
	return fn()
}

func productionRebindTransportV2(ctx context.Context, options processsupervisor.AttachOptionsV2, fn func(attachedRebindSessionV2) error) error {
	return processsupervisor.WithAttachedV2(ctx, options, func(s *processsupervisor.AttachedSessionV2) error { return fn(s) })
}

// The caller holds both physical owner and the live ledger transaction. This
// path retains generation end-to-end and never calls generic ReconnectV2.
func (s *DurableStore) rebindOwnerSuccessorV2Locked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, owner ControlOwnerState,
	identity AttemptIdentity, directory *os.File, fixedPath string, transport rebindTransportV2) (AttemptAuthorityState, error) {
	if transport == nil || state.SupervisorStarted.Validate() != nil || state.SupervisorMechanicsAnchor.Validate() != nil ||
		state.SupervisorMechanicsAnchor.Generation != state.SupervisorStarted.V2.Anchor.Generation {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	if state.SupervisorPendingIntentDigest != "" {
		// Never pass a v2 pending command to the legacy replay implementation.
		// Descriptor-bound receipt recovery is required before another effect.
		return AttemptAuthorityState{}, processsupervisor.ErrIntervention
	}
	acquisition := owner.Acquisition
	if state.Owner.OwnerEpoch < acquisition.OwnerEpoch {
		binding := CurrentOwnerBinding{Scope: acquisition.Scope, OwnerEpoch: acquisition.OwnerEpoch, ControlOwnerAcquiredFactDigest: owner.FactDigest}
		var err error
		state, _, err = s.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionControlOwnerBound, Identity: identity, Owner: binding})
		if err != nil {
			return AttemptAuthorityState{}, err
		}
	} else if state.Owner.OwnerEpoch != acquisition.OwnerEpoch || state.Owner.ControlOwnerAcquiredFactDigest != owner.FactDigest {
		return AttemptAuthorityState{}, ErrControlOwnerNotCurrent
	}
	if state.ControlOwnerBindingRevision != state.Revision || state.HeadDigest != state.ControlOwnerBindingDigest || state.ControlOwnerBindingRevision < 2 ||
		state.SupervisorBoundAuthorityHead == "" || state.SupervisorBoundAuthorityHead == state.HeadDigest {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	anchor := supervisorSessionAnchorV2(state.SupervisorMechanicsAnchor)
	authority := processsupervisor.AttachAuthorityV2{PreviousSupervisor: anchor, Supervisor: state.SupervisorStarted.V2.Handshake.SupervisorProcess,
		CurrentAcquisition: processsupervisor.AttachOwnerAcquisition{AuthorityNamespaceID: identity.AuthorityNamespaceRef, RepositoryIdentityDigest: acquisition.Scope.RepositoryIdentityDigest,
			OwnerEpoch: acquisition.OwnerEpoch, OwnerUID: acquisition.OwnerUID, OwnerGID: acquisition.OwnerGID, OwnerProcess: acquisition.OwnerProcess, OwnerBinary: acquisition.OwnerBinary,
			ObserverIdentity: acquisition.ObserverIdentity, ObservedAt: acquisition.ObservedAt, PreviousFactDigest: owner.PreviousFactDigest, FactDigest: owner.FactDigest},
		CurrentOwnerBoundFact: processsupervisor.AttachOwnerBoundFact{Authority: supervisorAuthorityTuple(identity), PreviousAttemptRevision: state.ControlOwnerBindingRevision - 1,
			PreviousAttemptHead: state.ControlOwnerBindingPreviousHead, AttemptRevision: state.Revision, AttemptHead: state.HeadDigest,
			ControlOwnerAcquiredFactDigest: owner.FactDigest, OwnerEpoch: acquisition.OwnerEpoch, FactDigest: state.ControlOwnerBindingDigest},
		Child: state.ProcessStartedEvidence.Outcome.Process, ChildObservationDigest: supervisorLastObservation(state)}
	if authority.Validate() != nil {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	payload := processsupervisor.BindAuthorityPayload{SupervisorStartedFactDigest: state.SupervisorStartedDigest, OwnerEpoch: anchor.Binding.OwnerEpoch,
		PreviousAuthorityHead: anchor.Binding.CurrentAuthorityHead, AuthorityHead: state.HeadDigest}
	prepared, err := processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandBindAuthority,
		CommandID: fmt.Sprintf("rebind-owner-successor-%d", state.SupervisorCommandSequence+1), Sequence: state.SupervisorCommandSequence + 1,
		PreviousCommandDigest: state.SupervisorCommandHead, CurrentAuthorityHead: anchor.Binding.CurrentAuthorityHead, Deadline: s.authorityNow().Add(20 * time.Second)}, payload)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	intent, err := NewSupervisorCommandIntentV2(prepared.Evidence())
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	options := processsupervisor.AttachOptionsV2{FixedMarshalPath: fixedPath, ControlDirectory: directory, Authority: authority, OwnerVerifier: heldAttachOwnerVerifierV2{authority: authority}}
	var outcome processsupervisor.VerifiedCommandOutcomeV2
	called := false
	err = transport(ctx, options, func(session attachedRebindSessionV2) error {
		if called || session == nil {
			return ErrPreparedExecutionConflict
		}
		called = true
		observation, err := session.Observation()
		if err != nil || observation.Validate() != nil || observation.Response.Authority != authority {
			return ErrPreparedExecutionConflict
		}
		state, _, err = s.appendRebindSupervisorIntentLocked(projection, state, intent)
		if err != nil {
			return err
		}
		outcome, err = session.ExecutePreparedBindAuthority(ctx, prepared)
		return err
	})
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	if !called {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	evidence, err := NewSupervisorCommandEvidenceV2(outcome)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	state, _, err = s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	if state.SupervisorBoundAuthorityHead != state.HeadDigest {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	return state, nil
}
