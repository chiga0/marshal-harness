package dispatch

import "fmt"

// ZeroAttemptSideEffectQuery is the exact reservation/Run/Attempt tuple whose
// complete durable dispatch history must be absent before ResultIngress may
// cancel an active Attempt reservation. It is a query, never a capability.
type ZeroAttemptSideEffectQuery struct {
	ReservationFactDigest string `json:"reservationFactDigest"`
	RunId                 string `json:"runId"`
	ReservedAttemptId     string `json:"reservedAttemptId"`
}

// Validate rejects partial or ambiguous absence queries.
func (query ZeroAttemptSideEffectQuery) Validate() error {
	if err := requireSHA256Digest("reservationFactDigest", query.ReservationFactDigest); err != nil {
		return err
	}
	if err := requireText("runId", query.RunId); err != nil {
		return err
	}
	return requireText("reservedAttemptId", query.ReservedAttemptId)
}

// WithZeroAttemptSideEffects holds the durable lease-ledger mutex while it
// proves that the exact reservation and Run/Attempt tuple have never reached
// any dispatch index. Historical released or terminal leases are side
// effects too: cancellation is not allowed to reinterpret them as absence.
//
// The callback is deliberately invoked while l.mu is held. Production
// composition nests this hold after repository owner and Run Lease, and puts
// the ResultIngress cancellation transaction inside fn, fixing the order to
// owner -> Run Lease -> dispatch ledger -> ResultIngress.
func (l *LeaseLedger) WithZeroAttemptSideEffects(query ZeroAttemptSideEffectQuery, fn func() error) error {
	if err := l.requireBound(); err != nil {
		return err
	}
	if err := query.Validate(); err != nil || fn == nil {
		return fmt.Errorf("%w: invalid zero-side-effect query", ErrLeaseConflict)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := query.ReservationFactDigest + "\x00" + query.RunId + "\x00" + query.ReservedAttemptId
	binding := claimBindingKey(query.RunId, query.ReservedAttemptId)
	if _, exists := l.reservedClaims[key]; exists {
		return fmt.Errorf("%w: reservation already has a reserved claim", ErrLeaseConflict)
	}
	if _, exists := l.reservationBindings[query.ReservationFactDigest]; exists {
		return fmt.Errorf("%w: reservation already has dispatch history", ErrLeaseConflict)
	}
	if _, exists := l.reservedAttemptBindings[binding]; exists {
		return fmt.Errorf("%w: Run/Attempt already has reserved dispatch history", ErrLeaseConflict)
	}
	if _, exists := l.activeBindings[binding]; exists {
		return fmt.Errorf("%w: Run/Attempt already has an active dispatch binding", ErrLeaseConflict)
	}
	if _, exists := l.closedAttemptBindings[binding]; exists {
		return fmt.Errorf("%w: Run/Attempt already has terminal dispatch history", ErrLeaseConflict)
	}
	for _, historical := range l.leases {
		if historical.RunId == query.RunId && historical.AttemptId == query.ReservedAttemptId {
			return fmt.Errorf("%w: Run/Attempt already exists in durable lease history", ErrLeaseConflict)
		}
	}
	return fn()
}
