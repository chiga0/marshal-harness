package productionruntime

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func TestRunningAttemptBoundToExactCurrentOwner(t *testing.T) {
	owner := resultingress.ControlOwnerState{
		Acquisition: resultingress.ControlOwnerAcquisition{OwnerEpoch: 2},
		FactDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
	}
	attempt := resultingress.AttemptAuthorityState{
		HeadDigest:                   "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		ProcessStartedDigest:         "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		SupervisorStartedDigest:      "sha256:5555555555555555555555555555555555555555555555555555555555555555",
		SupervisorBoundAuthorityHead: "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		Owner: resultingress.CurrentOwnerBinding{
			OwnerEpoch:                     2,
			ControlOwnerAcquiredFactDigest: owner.FactDigest,
		},
	}
	if !runningAttemptBoundToOwner(attempt, owner) {
		t.Fatal("exact rebound RUNNING attempt reported recovery-required")
	}

	tests := map[string]func(*resultingress.AttemptAuthorityState){
		"old-owner": func(state *resultingress.AttemptAuthorityState) { state.Owner.OwnerEpoch-- },
		"old-owner-fact": func(state *resultingress.AttemptAuthorityState) {
			state.Owner.ControlOwnerAcquiredFactDigest = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
		},
		"old-head": func(state *resultingress.AttemptAuthorityState) {
			state.SupervisorBoundAuthorityHead = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
		},
		"pending": func(state *resultingress.AttemptAuthorityState) {
			state.SupervisorPendingIntentDigest = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
		},
		"intervention": func(state *resultingress.AttemptAuthorityState) {
			state.SupervisorInterventionDigest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := attempt
			mutate(&changed)
			if runningAttemptBoundToOwner(changed, owner) {
				t.Fatal("drifted RUNNING attempt reported current")
			}
		})
	}
}
