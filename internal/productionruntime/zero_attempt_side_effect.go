//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// zeroAttemptSideEffectVerifier is the production ADR 0069 cancellation
// authority. It owns no independent state: every current verdict is obtained
// while holding the existing repository owner, exact Run Lease, durable
// dispatch ledger and the same ResultIngress/RB1 store that will append the
// cancellation fact.
type zeroAttemptSideEffectVerifier struct {
	owner       repositoryOwnerLock
	acquisition resultingress.ControlOwnerAcquisition
	run         *runstore.AttemptRunAuthorityVerifier
	dispatch    *dispatch.LeaseLedger
	store       *resultingress.DurableStore
}

var _ resultingress.ZeroAttemptSideEffectVerifier = (*zeroAttemptSideEffectVerifier)(nil)

func newZeroAttemptSideEffectVerifier(owner repositoryOwnerLock, acquisition resultingress.ControlOwnerAcquisition, run *runstore.AttemptRunAuthorityVerifier, leaseLedger *dispatch.LeaseLedger, store *resultingress.DurableStore) (*zeroAttemptSideEffectVerifier, error) {
	if owner == nil || acquisition.Validate() != nil || owner.acquisition() != acquisition || !owner.claimed() || run == nil || leaseLedger == nil || store == nil {
		return nil, application.NewError("attempt-reservation-cancel", application.ReasonAuthorityConflict)
	}
	return &zeroAttemptSideEffectVerifier{owner: owner, acquisition: acquisition, run: run, dispatch: leaseLedger, store: store}, nil
}

func (verifier *zeroAttemptSideEffectVerifier) WithZeroAttemptSideEffects(ctx context.Context, store *resultingress.DurableStore, state resultingress.AttemptReservationState, fn func(resultingress.ZeroSideEffectProof) error) error {
	if verifier == nil || ctx == nil || store == nil || store != verifier.store || fn == nil || state.Validate() != nil || state.Status != resultingress.AttemptReservationActive && state.Status != resultingress.AttemptReservationCancelled || !verifier.owner.claimed() {
		return application.NewError("attempt-reservation-cancel", application.ReasonAuthorityConflict)
	}
	query := dispatch.ZeroAttemptSideEffectQuery{
		ReservationFactDigest: state.ReservationFactDigest,
		RunId:                 state.Reservation.Ready.RunID,
		ReservedAttemptId:     state.Reservation.AttemptID,
	}
	if query.Validate() != nil {
		return application.NewError("attempt-reservation-cancel", application.ReasonAuthorityConflict)
	}

	return verifier.owner.WithCurrentOwnerLock(ctx, verifier.acquisition, func() error {
		return verifier.run.WithCurrentReadyRunAuthority(ctx, state.Reservation.Ready, func() error {
			return verifier.dispatch.WithZeroAttemptSideEffects(query, func() error {
				// OpenOwner replays the same physical ResultIngress store only after
				// owner, Run and dispatch are held. This is the final exact owner
				// acquisition check before the cancellation transaction below.
				currentOwner, found, err := verifier.store.OpenOwner(verifier.acquisition.Scope)
				if err != nil || !found || currentOwner.Acquisition != verifier.acquisition {
					return application.NewError("attempt-reservation-cancel", application.ReasonOwnerNotCurrent)
				}
				observation := zeroAttemptSideEffectObservation{
					SchemaRevision:        "attempt-zero-side-effect-observation/v1",
					OwnerScope:            currentOwner.Acquisition.Scope,
					ReservationFactDigest: state.ReservationFactDigest,
					Ready:                 state.Reservation.Ready,
					Dispatch:              query,
				}
				digest, err := canonicalObservationDigest(observation)
				if err != nil {
					return application.NewError("attempt-reservation-cancel", application.ReasonAuthorityConflict)
				}
				return fn(resultingress.ZeroSideEffectProof{
					SchemaRevision:        "attempt-zero-side-effect-proof/v1",
					ReservationFactDigest: state.ReservationFactDigest,
					ReadyAuthorityHead:    state.Reservation.Ready.ReadyAuthorityHead,
					ObservationDigest:     digest,
				})
			})
		})
	})
}

type zeroAttemptSideEffectObservation struct {
	SchemaRevision        string                              `json:"schemaRevision"`
	OwnerScope            resultingress.ControlOwnerScope     `json:"ownerScope"`
	ReservationFactDigest string                              `json:"reservationFactDigest"`
	Ready                 resultingress.ReadyRunAuthority     `json:"ready"`
	Dispatch              dispatch.ZeroAttemptSideEffectQuery `json:"dispatch"`
}

func canonicalObservationDigest(value zeroAttemptSideEffectObservation) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	digest := canonical.DigestBytes(encoded)
	if digest == "" {
		return "", errors.New("productionruntime: empty zero-side-effect observation digest")
	}
	return digest, nil
}
