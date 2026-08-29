package dispatch

import (
	"errors"
	"testing"
)

func zeroAttemptQuery(seed string) ZeroAttemptSideEffectQuery {
	return ZeroAttemptSideEffectQuery{
		ReservationFactDigest: fixedDigest("zero-reservation-" + seed),
		RunId:                 "run:zero-" + seed,
		ReservedAttemptId:     "attempt:zero-" + seed,
	}
}

func TestWithZeroAttemptSideEffectsHoldsExactAbsentTuple(t *testing.T) {
	ledger := newTestLeaseLedger(t)
	query := zeroAttemptQuery("absent")
	called := 0
	if err := ledger.WithZeroAttemptSideEffects(query, func() error {
		called++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("callback calls=%d, want 1", called)
	}
	if err := new(LeaseLedger).WithZeroAttemptSideEffects(query, func() error { return nil }); !errors.Is(err, ErrMemoryOnlyLeaseLedger) {
		t.Fatalf("memory-only ledger err=%v", err)
	}
}

func TestWithZeroAttemptSideEffectsRejectsEveryDurableDispatchIndex(t *testing.T) {
	query := zeroAttemptQuery("indexed")
	binding := claimBindingKey(query.RunId, query.ReservedAttemptId)
	key := query.ReservationFactDigest + "\x00" + query.RunId + "\x00" + query.ReservedAttemptId
	tests := []struct {
		name   string
		mutate func(*LeaseLedger)
	}{
		{"reserved-claim", func(ledger *LeaseLedger) { ledger.reservedClaims[key] = reservedClaimFact{} }},
		{"reservation-binding", func(ledger *LeaseLedger) { ledger.reservationBindings[query.ReservationFactDigest] = key }},
		{"reserved-attempt", func(ledger *LeaseLedger) { ledger.reservedAttemptBindings[binding] = key }},
		{"active-binding", func(ledger *LeaseLedger) { ledger.activeBindings[binding] = "lease:indexed" }},
		{"closed-attempt", func(ledger *LeaseLedger) { ledger.closedAttemptBindings[binding] = fixedDigest("terminal") }},
		{"released-history", func(ledger *LeaseLedger) {
			lease := ledgerLease("released-history")
			lease.RunId, lease.AttemptId = query.RunId, query.ReservedAttemptId
			lease.LeaseState = LeaseStateCancelled
			ledger.leases[lease.LeaseId] = lease
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTestLeaseLedger(t)
			test.mutate(ledger)
			called := false
			err := ledger.WithZeroAttemptSideEffects(query, func() error {
				called = true
				return nil
			})
			if !errors.Is(err, ErrLeaseConflict) || called {
				t.Fatalf("err=%v called=%t", err, called)
			}
		})
	}
}
