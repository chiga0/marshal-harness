package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	attemptAuthorityProtocolV2      = "attempt-authority/v2"
	attemptReservationSchemaV1      = "attempt-reservation/v1"
	attemptOpenedSchemaV2           = "attempt-opened/v2"
	attemptReservedFactType         = "attempt-reserved"
	attemptReservationConsumedType  = "attempt-reservation-consumed"
	attemptReservationCancelledType = "attempt-reservation-cancelled"
)

var (
	ErrAttemptReservationConflict  = errors.New("resultingress: attempt reservation conflict")
	ErrAttemptReservationExhausted = errors.New("resultingress: attempt budget exhausted")
	ErrAttemptReservationResolved  = errors.New("resultingress: attempt reservation already resolved")
)

type AttemptReservationStatus string

const (
	AttemptReservationActive    AttemptReservationStatus = "active"
	AttemptReservationConsumed  AttemptReservationStatus = "consumed"
	AttemptReservationCancelled AttemptReservationStatus = "cancelled"
)

// ReadyRunAuthority is the complete owner-private READY projection used for
// all three budget checks. MaxAttempts comes from the exact READY event while
// AttemptsUsed comes from the replayed RunState; neither is caller-maintained.
type ReadyRunAuthority struct {
	AuthorityNamespaceID authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string                         `json:"taskId"`
	RunID                string                         `json:"runId"`
	OrchestratorID       string                         `json:"orchestratorId"`
	ReadySequence        uint64                         `json:"readySequence"`
	ReadyAuthorityHead   string                         `json:"readyAuthorityHead"`
	AttemptsUsed         uint64                         `json:"attemptsUsed"`
	MaxAttempts          uint64                         `json:"maxAttempts"`
	SpecDigest           string                         `json:"specDigest"`
	PolicyDigest         string                         `json:"policyDigest"`
	CapabilityDigest     string                         `json:"capabilityDigest"`
	BaseSHA              string                         `json:"baseSha"`
	WorktreePath         string                         `json:"worktreePath"`
}

func (ready ReadyRunAuthority) Validate() error {
	if ready.AuthorityNamespaceID.Validate() != nil || domain.ValidateID(ready.TaskID) != nil || domain.ValidateID(ready.RunID) != nil || strings.TrimSpace(ready.OrchestratorID) == "" || ready.ReadySequence == 0 || ready.ReadySequence > maxExactJSONInteger || ready.MaxAttempts == 0 || ready.MaxAttempts > maxExactJSONInteger || ready.AttemptsUsed >= ready.MaxAttempts {
		if ready.MaxAttempts != 0 && ready.AttemptsUsed >= ready.MaxAttempts {
			return ErrAttemptReservationExhausted
		}
		return ErrAttemptReservationConflict
	}
	for _, digest := range []string{ready.ReadyAuthorityHead, ready.SpecDigest, ready.PolicyDigest, ready.CapabilityDigest} {
		if requireDigest("readyRunAuthority", digest) != nil {
			return ErrAttemptReservationConflict
		}
	}
	if !reservationGitObjectPattern.MatchString(ready.BaseSHA) || !filepath.IsAbs(ready.WorktreePath) || filepath.Clean(ready.WorktreePath) != ready.WorktreePath {
		return ErrAttemptReservationConflict
	}
	return nil
}

// Kept local to avoid importing runstore and creating an authority cycle.
var reservationGitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type CurrentReadyRunAuthorityVerifier interface {
	WithCurrentReadyRunAuthority(context.Context, ReadyRunAuthority, func() error) error
}

func withCurrentReadyRunAuthority(ctx context.Context, verifier CurrentReadyRunAuthorityVerifier, ready ReadyRunAuthority, fn func() error) error {
	if ctx == nil || verifier == nil || fn == nil || ready.Validate() != nil {
		return ErrRunAuthorityUnauthorized
	}
	var gate sync.Mutex
	called, closed, duplicate, inFlight := false, false, false, false
	var callbackErr error
	err := verifier.WithCurrentReadyRunAuthority(ctx, ready, func() error {
		gate.Lock()
		if closed || called {
			duplicate = true
			gate.Unlock()
			return ErrRunAuthorityUnauthorized
		}
		called = true
		inFlight = true
		gate.Unlock()
		completedErr := fn()
		gate.Lock()
		inFlight = false
		callbackErr = completedErr
		gate.Unlock()
		return completedErr
	})
	gate.Lock()
	escaped := inFlight
	closed = true
	calledOnce, invokedTwice, heldErr := called, duplicate, callbackErr
	gate.Unlock()
	// Error precedence is deliberate: verifier misuse wins first; once the
	// callback ran, its exact durable/outcome-unknown error must survive instead
	// of being rewritten as an authorization error. Only a verifier failure or
	// missing callback is classified as unauthorized.
	if invokedTwice || escaped {
		return fmt.Errorf("%w: READY verifier callback was repeated or escaped", ErrRunAuthorityUnauthorized)
	}
	if heldErr != nil {
		return heldErr
	}
	if !calledOnce || err != nil {
		return fmt.Errorf("%w: READY verifier rejected or misused callback: %v", ErrRunAuthorityUnauthorized, err)
	}
	return nil
}

type AttemptReservationV1 struct {
	SchemaRevision       string            `json:"schemaRevision"`
	Ready                ReadyRunAuthority `json:"ready"`
	AttemptID            string            `json:"attemptId"`
	AttemptOrdinal       uint64            `json:"attemptOrdinal"`
	ReservationKeyDigest string            `json:"reservationKeyDigest"`
}

func (reservation AttemptReservationV1) Validate() error {
	if reservation.SchemaRevision != attemptReservationSchemaV1 || reservation.Ready.Validate() != nil || domain.ValidateID(reservation.AttemptID) != nil || reservation.AttemptOrdinal != reservation.Ready.AttemptsUsed+1 || reservation.ReservationKeyDigest != reservationKey(reservation.Ready) {
		return ErrAttemptReservationConflict
	}
	return nil
}

type AttemptReservationState struct {
	Reservation             AttemptReservationV1     `json:"reservation"`
	ReservationFactDigest   string                   `json:"reservationFactDigest"`
	Status                  AttemptReservationStatus `json:"status"`
	ResolutionFactDigest    string                   `json:"resolutionFactDigest,omitempty"`
	ResolutionBindingDigest string                   `json:"resolutionBindingDigest,omitempty"`
	RunSuccessorSequence    uint64                   `json:"runSuccessorSequence,omitempty"`
	RunSuccessorHead        string                   `json:"runSuccessorHead,omitempty"`
}

func (state AttemptReservationState) Validate() error {
	if state.Reservation.Validate() != nil || requireDigest("reservationFactDigest", state.ReservationFactDigest) != nil {
		return ErrAttemptReservationConflict
	}
	switch state.Status {
	case AttemptReservationActive:
		if state.ResolutionFactDigest != "" || state.ResolutionBindingDigest != "" || state.RunSuccessorSequence != 0 || state.RunSuccessorHead != "" {
			return ErrAttemptReservationConflict
		}
	case AttemptReservationConsumed:
		if requireDigest("resolutionFactDigest", state.ResolutionFactDigest) != nil || requireDigest("resolutionBindingDigest", state.ResolutionBindingDigest) != nil || state.RunSuccessorSequence != state.Reservation.Ready.ReadySequence+1 || requireDigest("runSuccessorHead", state.RunSuccessorHead) != nil {
			return ErrAttemptReservationConflict
		}
	case AttemptReservationCancelled:
		if requireDigest("resolutionFactDigest", state.ResolutionFactDigest) != nil || requireDigest("resolutionBindingDigest", state.ResolutionBindingDigest) != nil || state.RunSuccessorSequence != 0 || state.RunSuccessorHead != "" {
			return ErrAttemptReservationConflict
		}
	default:
		return ErrAttemptReservationConflict
	}
	return nil
}

type attemptReservationFact struct {
	ProtocolRevision             string               `json:"protocolRevision"`
	FactType                     string               `json:"factType"`
	Sequence                     int64                `json:"sequence"`
	Reservation                  AttemptReservationV1 `json:"reservation"`
	ReservationFactDigest        string               `json:"reservationFactDigest,omitempty"`
	RunSuccessorSequence         uint64               `json:"runSuccessorSequence,omitempty"`
	RunSuccessorHead             string               `json:"runSuccessorHead,omitempty"`
	ZeroSideEffectProof          *ZeroSideEffectProof `json:"zeroSideEffectProof,omitempty"`
	ZeroSideEffectProofDigest    string               `json:"zeroSideEffectProofDigest,omitempty"`
	SealedSuccessorBindingDigest string               `json:"sealedSuccessorBindingDigest,omitempty"`
	Digest                       string               `json:"digest"`
}

func reservationKey(ready ReadyRunAuthority) string {
	key := struct {
		RunID    string `json:"runId"`
		Sequence uint64 `json:"readySequence"`
		Head     string `json:"readyAuthorityHead"`
	}{ready.RunID, ready.ReadySequence, ready.ReadyAuthorityHead}
	raw, _ := json.Marshal(key)
	canonicalRaw, _ := canonical.JSON(raw)
	return canonical.DigestBytes(canonicalRaw)
}

func (s *DurableStore) ReserveAttempt(ctx context.Context, verifier CurrentReadyRunAuthorityVerifier, ready ReadyRunAuthority) (AttemptReservationState, error) {
	if err := ready.Validate(); err != nil {
		return AttemptReservationState{}, err
	}
	var result AttemptReservationState
	err := withCurrentReadyRunAuthority(ctx, verifier, ready, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			key := reservationKey(ready)
			if digest, found := projection.reservationKeys[key]; found {
				state, ok := projection.reservations[digest]
				if !ok || state.Reservation.Ready != ready || state.Validate() != nil {
					return ErrAttemptReservationConflict
				}
				result = state
				return nil
			}
			attemptID, err := domain.NewID("attempt")
			if err != nil {
				return err
			}
			reservation := AttemptReservationV1{SchemaRevision: attemptReservationSchemaV1, Ready: ready, AttemptID: attemptID, AttemptOrdinal: ready.AttemptsUsed + 1, ReservationKeyDigest: key}
			if reservation.Validate() != nil {
				return ErrAttemptReservationConflict
			}
			fact := &attemptReservationFact{ProtocolRevision: attemptAuthorityProtocolV2, FactType: attemptReservedFactType, Sequence: s.nextSequence, Reservation: reservation}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyAttemptReservationFactValue(*fact, projection); err != nil {
				return err
			}
			result = projection.reservations[fact.Digest]
			return nil
		})
	})
	return result, err
}

// OpenReservedAttempt is the only fresh attempt-opened producer. It rechecks
// the exact READY budget while the verifier holds it, then appends v2 identity
// authority referencing the active reservation. The legacy generic opened API
// is replay-only.
func (s *DurableStore) OpenReservedAttempt(ctx context.Context, verifier CurrentReadyRunAuthorityVerifier, reservationFactDigest string, identity AttemptIdentity) (AttemptAppendResult, error) {
	if identity.Validate() != nil || requireDigest("reservationFactDigest", reservationFactDigest) != nil {
		return AttemptAppendResult{}, ErrAttemptReservationConflict
	}
	projection := newAuthorityProjection()
	var ready ReadyRunAuthority
	if err := s.transact(projection, func() error {
		state, found := projection.reservations[reservationFactDigest]
		if !found || state.Status != AttemptReservationActive || state.Validate() != nil {
			return ErrAttemptReservationResolved
		}
		ready = state.Reservation.Ready
		return nil
	}); err != nil {
		return AttemptAppendResult{}, err
	}
	var result AttemptAppendResult
	err := withCurrentReadyRunAuthority(ctx, verifier, ready, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			reservation, found := projection.reservations[reservationFactDigest]
			if !found || reservation.Status != AttemptReservationActive || reservation.Reservation.Ready != ready || reservation.Reservation.AttemptID != identity.AttemptID || ready.TaskID != identity.TaskID || ready.RunID != identity.RunID || ready.AuthorityNamespaceID != identity.AuthorityNamespaceID || ready.OrchestratorID != identity.OrchestratorID || ready.ReadyAuthorityHead != identity.RunAuthorityDigest {
				return ErrAttemptReservationConflict
			}
			key, err := identity.Key()
			if err != nil {
				return err
			}
			if prior, exists := projection.attempts[key]; exists {
				if prior.Identity != identity || prior.ProtocolRevision != attemptAuthorityProtocolV2 || prior.ReservationFactDigest != reservationFactDigest || prior.AttemptOrdinal != reservation.Reservation.AttemptOrdinal {
					return ErrAttemptAuthorityConflict
				}
				result = AttemptAppendResult{State: prior, TransitionDigest: prior.OpenedDigest}
				return nil
			}
			transition := AttemptTransition{Kind: AttemptTransitionOpened, Identity: identity}
			fact := &attemptAuthorityFact{ProtocolRevision: attemptAuthorityProtocolV2, SchemaRevision: attemptOpenedSchemaV2, FactType: string(AttemptTransitionOpened), Sequence: s.nextSequence, AttemptKey: key, Revision: 1, Transition: transition, ReservationFactDigest: reservationFactDigest, AttemptOrdinal: reservation.Reservation.AttemptOrdinal}
			if err := prepareAttemptFact(AttemptAuthorityState{}, false, fact, false); err != nil {
				return err
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyAttemptAuthorityFactValue(*fact, projection, false); err != nil {
				return err
			}
			result = AttemptAppendResult{State: projection.attempts[key], Appended: true, TransitionDigest: fact.Digest}
			return nil
		})
	})
	return result, err
}

type SealedRunSuccessorAuthority struct {
	ReservationFactDigest   string            `json:"reservationFactDigest"`
	Ready                   ReadyRunAuthority `json:"ready"`
	AttemptID               string            `json:"attemptId"`
	AttemptOpenedFactDigest string            `json:"attemptOpenedFactDigest"`
	AttemptOrdinal          uint64            `json:"attemptOrdinal"`
	AttemptsUsedAfter       uint64            `json:"attemptsUsedAfter"`
	RunSuccessorSequence    uint64            `json:"runSuccessorSequence"`
	RunSuccessorHead        string            `json:"runSuccessorHead"`
}

func (sealed SealedRunSuccessorAuthority) Validate() error {
	if requireDigest("reservationFactDigest", sealed.ReservationFactDigest) != nil || sealed.Ready.Validate() != nil || domain.ValidateID(sealed.AttemptID) != nil || requireDigest("attemptOpenedFactDigest", sealed.AttemptOpenedFactDigest) != nil || sealed.AttemptOrdinal != sealed.Ready.AttemptsUsed+1 || sealed.AttemptsUsedAfter != sealed.AttemptOrdinal || sealed.RunSuccessorSequence != sealed.Ready.ReadySequence+1 || requireDigest("runSuccessorHead", sealed.RunSuccessorHead) != nil {
		return ErrAttemptReservationConflict
	}
	return nil
}

type CurrentSealedRunSuccessorVerifier interface {
	WithCurrentSealedRunSuccessor(context.Context, SealedRunSuccessorAuthority, func() error) error
}

type ZeroSideEffectProof struct {
	SchemaRevision        string `json:"schemaRevision"`
	ReservationFactDigest string `json:"reservationFactDigest"`
	ReadyAuthorityHead    string `json:"readyAuthorityHead"`
	ObservationDigest     string `json:"observationDigest"`
}

func (proof ZeroSideEffectProof) Validate() error {
	if proof.SchemaRevision != "attempt-zero-side-effect-proof/v1" || requireDigest("reservationFactDigest", proof.ReservationFactDigest) != nil || requireDigest("readyAuthorityHead", proof.ReadyAuthorityHead) != nil || requireDigest("observationDigest", proof.ObservationDigest) != nil {
		return ErrAttemptReservationConflict
	}
	return nil
}

type ZeroAttemptSideEffectVerifier interface {
	// The verifier holds repository owner + Run Lease while checking dispatch,
	// allocation, bootstrap, child, command and publication are all absent. The
	// proof is produced by that verifier, not accepted from the caller.
	WithZeroAttemptSideEffects(context.Context, *DurableStore, AttemptReservationState, func(ZeroSideEffectProof) error) error
}

func (s *DurableStore) ConsumeAttemptReservation(ctx context.Context, verifier CurrentSealedRunSuccessorVerifier, sealed SealedRunSuccessorAuthority) (AttemptReservationState, error) {
	if ctx == nil || verifier == nil || sealed.Validate() != nil {
		return AttemptReservationState{}, ErrAttemptReservationConflict
	}
	return s.resolveAttemptReservation(ctx, func(fn func(attemptResolutionEvidence) error) error {
		return verifier.WithCurrentSealedRunSuccessor(ctx, sealed, func() error {
			return fn(attemptResolutionEvidence{bindingDigest: canonicalDigestOrEmpty(sealed)})
		})
	}, sealed.ReservationFactDigest, attemptReservationConsumedType, sealed.RunSuccessorSequence, sealed.RunSuccessorHead, func(projection *Ingress, state AttemptReservationState) error {
		attempt, exists := projection.attemptsByReservation[sealed.ReservationFactDigest]
		if !exists || state.Reservation.Ready != sealed.Ready || state.Reservation.AttemptID != sealed.AttemptID || state.Reservation.AttemptOrdinal != sealed.AttemptOrdinal || attempt.Identity.AttemptID != sealed.AttemptID || attempt.Identity.RunID != sealed.Ready.RunID || attempt.Identity.TaskID != sealed.Ready.TaskID || attempt.Identity.RunAuthorityDigest != sealed.Ready.ReadyAuthorityHead || attempt.OpenedDigest != sealed.AttemptOpenedFactDigest || attempt.AttemptOrdinal != sealed.AttemptOrdinal {
			return ErrAttemptReservationConflict
		}
		return nil
	})
}

func (s *DurableStore) CancelAttemptReservation(ctx context.Context, verifier ZeroAttemptSideEffectVerifier, reservationFactDigest string) (AttemptReservationState, error) {
	if ctx == nil || verifier == nil || requireDigest("reservationFactDigest", reservationFactDigest) != nil {
		return AttemptReservationState{}, ErrAttemptReservationConflict
	}
	projection := newAuthorityProjection()
	var state AttemptReservationState
	if err := s.transact(projection, func() error {
		var ok bool
		state, ok = projection.reservations[reservationFactDigest]
		if !ok {
			return ErrAttemptReservationConflict
		}
		return nil
	}); err != nil {
		return AttemptReservationState{}, err
	}
	return s.resolveAttemptReservation(ctx, func(fn func(attemptResolutionEvidence) error) error {
		return verifier.WithZeroAttemptSideEffects(ctx, s, state, func(derived ZeroSideEffectProof) error {
			if derived.Validate() != nil || derived.ReservationFactDigest != reservationFactDigest || derived.ReadyAuthorityHead != state.Reservation.Ready.ReadyAuthorityHead {
				return ErrAttemptReservationConflict
			}
			return fn(attemptResolutionEvidence{bindingDigest: canonicalDigestOrEmpty(derived), zeroProof: &derived})
		})
	}, reservationFactDigest, attemptReservationCancelledType, 0, "", func(projection *Ingress, current AttemptReservationState) error {
		return requireZeroAttemptProjection(projection, current)
	})
}

// requireZeroAttemptProjection runs only inside the cold replay transaction
// that will append the cancellation fact. attemptsByReservation is the
// authoritative opened-attempt index; the remaining scans make the no-
// downstream-facts invariant explicit and fail closed if a future replay path
// ever admits an orphan allocation/effect/result/preparation/RB1 fact.
func requireZeroAttemptProjection(projection *Ingress, state AttemptReservationState) error {
	if projection == nil || state.Validate() != nil || state.Status != AttemptReservationActive && state.Status != AttemptReservationCancelled {
		return ErrAttemptReservationConflict
	}
	if _, exists := projection.attemptsByReservation[state.ReservationFactDigest]; exists {
		return ErrAttemptReservationConflict
	}
	ready := state.Reservation.Ready
	attemptID := state.Reservation.AttemptID
	matches := func(identity AttemptIdentity) bool {
		return identity.AuthorityNamespaceID == ready.AuthorityNamespaceID && identity.TaskID == ready.TaskID && identity.RunID == ready.RunID && identity.AttemptID == attemptID
	}
	for _, attempt := range projection.attempts {
		if attempt.ReservationFactDigest == state.ReservationFactDigest || matches(attempt.Identity) {
			return ErrAttemptReservationConflict
		}
	}
	for _, effect := range projection.effects {
		if matches(effect.Binding.Identity) {
			return ErrAttemptReservationConflict
		}
	}
	for _, prepared := range projection.preparedExecutions {
		if prepared.ReservationFactDigest == state.ReservationFactDigest || matches(prepared.AttemptIdentity) {
			return ErrAttemptReservationConflict
		}
	}
	namespaceDigest, err := ready.AuthorityNamespaceID.Digest()
	if err != nil {
		return ErrAttemptReservationConflict
	}
	for _, fact := range projection.existingWorktreeFacts {
		binding, err := existingWorktreeFactBinding(fact)
		if err != nil {
			return ErrAttemptReservationConflict
		}
		if binding.ReservationFactDigest == state.ReservationFactDigest || binding.AuthorityNamespaceID == namespaceDigest && binding.TaskID == ready.TaskID && binding.RunID == ready.RunID && binding.AttemptID == attemptID {
			return ErrAttemptReservationConflict
		}
	}
	logicalKey, err := reservationLogicalAttemptKey(ready, attemptID)
	if err != nil {
		return ErrAttemptReservationConflict
	}
	if _, exists := projection.allocations[logicalKey]; exists {
		return ErrAttemptReservationConflict
	}
	for _, admitted := range projection.admitted {
		if admitted.attemptKey == logicalKey {
			return ErrAttemptReservationConflict
		}
	}
	return nil
}

func reservationLogicalAttemptKey(ready ReadyRunAuthority, attemptID string) (string, error) {
	logical := struct {
		AuthorityNamespaceID authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
		TaskID               string                         `json:"taskId"`
		RunID                string                         `json:"runId"`
		AttemptID            string                         `json:"attemptId"`
	}{ready.AuthorityNamespaceID, ready.TaskID, ready.RunID, attemptID}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", err
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalRaw), nil
}

func existingWorktreeFactBinding(fact allocationcontrol.ExistingWorktreeAttemptFactV1) (allocationcontrol.ExistingWorktreeBindingV1, error) {
	switch fact.Kind {
	case allocationcontrol.ExistingWorktreeFactBindIntent:
		var value allocationcontrol.ExistingWorktreeBindIntentV1
		if err := json.Unmarshal(fact.Payload, &value); err != nil {
			return allocationcontrol.ExistingWorktreeBindingV1{}, err
		}
		return value.Request.Binding, nil
	case allocationcontrol.ExistingWorktreeFactBindReceipt:
		var value allocationcontrol.ExistingWorktreeBindReceiptV1
		if err := json.Unmarshal(fact.Payload, &value); err != nil {
			return allocationcontrol.ExistingWorktreeBindingV1{}, err
		}
		return value.Binding, nil
	case allocationcontrol.ExistingWorktreeFactReleaseIntent:
		var value allocationcontrol.ExistingWorktreeReleaseIntentV1
		if err := json.Unmarshal(fact.Payload, &value); err != nil {
			return allocationcontrol.ExistingWorktreeBindingV1{}, err
		}
		return value.Request.Binding, nil
	case allocationcontrol.ExistingWorktreeFactReleaseReceipt:
		var value allocationcontrol.ExistingWorktreeReleaseReceiptV1
		if err := json.Unmarshal(fact.Payload, &value); err != nil {
			return allocationcontrol.ExistingWorktreeBindingV1{}, err
		}
		return value.Binding, nil
	default:
		return allocationcontrol.ExistingWorktreeBindingV1{}, ErrAttemptReservationConflict
	}
}

func canonicalDigestOrEmpty(value any) string {
	digest, err := canonicalDigest(value)
	if err != nil {
		return ""
	}
	return digest
}

type attemptResolutionEvidence struct {
	bindingDigest string
	zeroProof     *ZeroSideEffectProof
}

func (s *DurableStore) resolveAttemptReservation(ctx context.Context, hold func(func(attemptResolutionEvidence) error) error, reservationFactDigest, factType string, successorSequence uint64, successorHead string, validate func(*Ingress, AttemptReservationState) error) (AttemptReservationState, error) {
	var result AttemptReservationState
	var gate sync.Mutex
	called, closed, duplicate, inFlight := false, false, false, false
	var callbackErr error
	holdErr := hold(func(evidence attemptResolutionEvidence) error {
		gate.Lock()
		if closed || called {
			duplicate = true
			gate.Unlock()
			return ErrAttemptReservationConflict
		}
		called = true
		inFlight = true
		gate.Unlock()
		projection := newAuthorityProjection()
		completedErr := s.transact(projection, func() error {
			state, found := projection.reservations[reservationFactDigest]
			if !found {
				return ErrAttemptReservationConflict
			}
			if validate == nil || validate(projection, state) != nil {
				return ErrAttemptReservationConflict
			}
			if requireDigest("resolutionBindingDigest", evidence.bindingDigest) != nil {
				return ErrAttemptReservationConflict
			}
			if factType == attemptReservationConsumedType && evidence.zeroProof != nil || factType == attemptReservationCancelledType && (evidence.zeroProof == nil || evidence.zeroProof.Validate() != nil) {
				return ErrAttemptReservationConflict
			}
			if state.Status != AttemptReservationActive {
				wantStatus := AttemptReservationConsumed
				if factType == attemptReservationCancelledType {
					wantStatus = AttemptReservationCancelled
				}
				if state.Status == wantStatus && state.RunSuccessorSequence == successorSequence && state.RunSuccessorHead == successorHead && state.ResolutionBindingDigest == evidence.bindingDigest {
					result = state
					return nil
				}
				return ErrAttemptReservationResolved
			}
			if factType == attemptReservationConsumedType {
				if successorSequence != state.Reservation.Ready.ReadySequence+1 || requireDigest("runSuccessorHead", successorHead) != nil {
					return ErrAttemptReservationConflict
				}
			} else if successorSequence != 0 || successorHead != "" {
				return ErrAttemptReservationConflict
			}
			fact := &attemptReservationFact{ProtocolRevision: attemptAuthorityProtocolV2, FactType: factType, Sequence: s.nextSequence, Reservation: state.Reservation, ReservationFactDigest: reservationFactDigest, RunSuccessorSequence: successorSequence, RunSuccessorHead: successorHead}
			if factType == attemptReservationCancelledType {
				proof := *evidence.zeroProof
				fact.ZeroSideEffectProof = &proof
				fact.ZeroSideEffectProofDigest = evidence.bindingDigest
			} else {
				fact.SealedSuccessorBindingDigest = evidence.bindingDigest
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyAttemptReservationFactValue(*fact, projection); err != nil {
				return err
			}
			result = projection.reservations[reservationFactDigest]
			return nil
		})
		gate.Lock()
		inFlight = false
		callbackErr = completedErr
		gate.Unlock()
		return completedErr
	})
	gate.Lock()
	escaped := inFlight
	closed = true
	calledOnce, invokedTwice, heldErr := called, duplicate, callbackErr
	gate.Unlock()
	if invokedTwice || escaped {
		return AttemptReservationState{}, fmt.Errorf("%w: resolution verifier callback was repeated or escaped", ErrAttemptReservationConflict)
	}
	if heldErr != nil {
		return AttemptReservationState{}, heldErr
	}
	if !calledOnce || holdErr != nil {
		return AttemptReservationState{}, fmt.Errorf("%w: resolution verifier rejected or misused callback: %v", ErrAttemptReservationConflict, holdErr)
	}
	return result, nil
}

func applyAttemptReservationLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return ErrAttemptReservationConflict
	}
	var fact attemptReservationFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fact) != nil {
		return ErrAttemptReservationConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrAttemptReservationConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || stored != digest || fact.ProtocolRevision != attemptAuthorityProtocolV2 || fact.Sequence != wantSequence {
		return ErrAttemptReservationConflict
	}
	fact.Digest = stored
	return applyAttemptReservationFactValue(fact, in)
}

func applyAttemptReservationFactValue(fact attemptReservationFact, in *Ingress) error {
	if fact.Reservation.Validate() != nil || fact.ProtocolRevision != attemptAuthorityProtocolV2 || requireDigest("attemptReservationFactDigest", fact.Digest) != nil {
		return ErrAttemptReservationConflict
	}
	key := fact.Reservation.ReservationKeyDigest
	switch fact.FactType {
	case attemptReservedFactType:
		if fact.ReservationFactDigest != "" || fact.RunSuccessorSequence != 0 || fact.RunSuccessorHead != "" || fact.ZeroSideEffectProof != nil || fact.ZeroSideEffectProofDigest != "" || fact.SealedSuccessorBindingDigest != "" {
			return ErrAttemptReservationConflict
		}
		if _, exists := in.reservationKeys[key]; exists {
			return ErrAttemptReservationConflict
		}
		for _, existing := range in.reservations {
			if existing.Reservation.AttemptID == fact.Reservation.AttemptID {
				return ErrAttemptReservationConflict
			}
		}
		state := AttemptReservationState{Reservation: fact.Reservation, ReservationFactDigest: fact.Digest, Status: AttemptReservationActive}
		in.reservationKeys[key] = fact.Digest
		in.reservations[fact.Digest] = state
	case attemptReservationConsumedType, attemptReservationCancelledType:
		state, exists := in.reservations[fact.ReservationFactDigest]
		if !exists || state.Status != AttemptReservationActive || state.Reservation != fact.Reservation {
			return ErrAttemptReservationConflict
		}
		state.ResolutionFactDigest = fact.Digest
		if fact.FactType == attemptReservationConsumedType {
			attempt, ok := in.attemptsByReservation[fact.ReservationFactDigest]
			ready := state.Reservation.Ready
			if !ok || attempt.ProtocolRevision != attemptAuthorityProtocolV2 || attempt.OpenedSchemaRevision != attemptOpenedSchemaV2 ||
				attempt.ReservationFactDigest != fact.ReservationFactDigest || attempt.Identity.AttemptID != state.Reservation.AttemptID ||
				attempt.Identity.AuthorityNamespaceID != ready.AuthorityNamespaceID || attempt.Identity.TaskID != ready.TaskID || attempt.Identity.RunID != ready.RunID ||
				attempt.Identity.OrchestratorID != ready.OrchestratorID || attempt.Identity.RunAuthorityDigest != ready.ReadyAuthorityHead ||
				attempt.AttemptOrdinal != state.Reservation.AttemptOrdinal || requireDigest("attemptOpenedFactDigest", attempt.OpenedDigest) != nil ||
				fact.RunSuccessorSequence != ready.ReadySequence+1 || requireDigest("runSuccessorHead", fact.RunSuccessorHead) != nil || fact.ZeroSideEffectProof != nil || fact.ZeroSideEffectProofDigest != "" || requireDigest("sealedSuccessorBindingDigest", fact.SealedSuccessorBindingDigest) != nil {
				return ErrAttemptReservationConflict
			}
			sealed := SealedRunSuccessorAuthority{ReservationFactDigest: fact.ReservationFactDigest, Ready: ready, AttemptID: state.Reservation.AttemptID, AttemptOpenedFactDigest: attempt.OpenedDigest, AttemptOrdinal: state.Reservation.AttemptOrdinal, AttemptsUsedAfter: state.Reservation.AttemptOrdinal, RunSuccessorSequence: fact.RunSuccessorSequence, RunSuccessorHead: fact.RunSuccessorHead}
			if sealed.Validate() != nil || canonicalDigestOrEmpty(sealed) != fact.SealedSuccessorBindingDigest {
				return ErrAttemptReservationConflict
			}
			state.Status = AttemptReservationConsumed
			state.ResolutionBindingDigest = fact.SealedSuccessorBindingDigest
			state.RunSuccessorSequence = fact.RunSuccessorSequence
			state.RunSuccessorHead = fact.RunSuccessorHead
		} else {
			if fact.ZeroSideEffectProof == nil || fact.ZeroSideEffectProof.Validate() != nil || fact.ZeroSideEffectProof.ReservationFactDigest != fact.ReservationFactDigest || fact.ZeroSideEffectProof.ReadyAuthorityHead != state.Reservation.Ready.ReadyAuthorityHead || canonicalDigestOrEmpty(*fact.ZeroSideEffectProof) != fact.ZeroSideEffectProofDigest || requireDigest("zeroSideEffectProofDigest", fact.ZeroSideEffectProofDigest) != nil || fact.SealedSuccessorBindingDigest != "" || fact.RunSuccessorSequence != 0 || fact.RunSuccessorHead != "" {
				return ErrAttemptReservationConflict
			}
			if _, exists := in.attemptsByReservation[fact.ReservationFactDigest]; exists {
				return ErrAttemptReservationConflict
			}
			state.Status = AttemptReservationCancelled
			state.ResolutionBindingDigest = fact.ZeroSideEffectProofDigest
		}
		in.reservations[fact.ReservationFactDigest] = state
	default:
		return ErrAttemptReservationConflict
	}
	return nil
}
