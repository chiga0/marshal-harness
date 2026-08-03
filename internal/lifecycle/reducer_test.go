package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestAllLifecycleTransitions(t *testing.T) {
	t.Parallel()
	guard := allGuards()
	states := domain.States()
	for _, from := range states {
		for _, to := range states {
			from, to := from, to
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
				state.State = from
				transitionGuard := guard
				if from == domain.StatePublished && to == domain.StateAccepted {
					transitionGuard.RemoteChecksRequired = false
				}
				event := domain.RunEvent{RunID: state.RunID, Sequence: 1, StateFrom: from, StateTo: to, Timestamp: time.Unix(2, 0)}
				if to == domain.StateRunning {
					event.AttemptID = "attempt:1"
				}
				_, err := Reduce(state, event, transitionGuard)
				wantAllowed := !from.Terminal() && (to == domain.StateAborted || allowed[from][to])
				if wantAllowed && err != nil {
					t.Fatalf("legal transition rejected: %v", err)
				}
				if !wantAllowed && !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("illegal transition error = %v", err)
				}
			})
		}
	}
}

func TestLifecycleGuards(t *testing.T) {
	t.Parallel()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	state.State = domain.StateVerifying
	event := domain.RunEvent{RunID: state.RunID, Sequence: 1, StateFrom: state.State, StateTo: domain.StateReviewPending, Timestamp: time.Unix(2, 0)}
	guard := allGuards()
	guard.EvidenceCurrent = false
	if _, err := Reduce(state, event, guard); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("missing evidence error = %v", err)
	}
	guard = allGuards()
	guard.LeaseHeld = false
	if _, err := Reduce(state, event, guard); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("missing lease error = %v", err)
	}
	guard = allGuards()
	guard.RequiredGatesPass = false
	if _, err := Reduce(state, event, guard); err != nil {
		t.Fatalf("failed required gate must still reach review: %v", err)
	}
}

func TestCountersMatchReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		from  domain.State
		to    domain.State
		check func(domain.RunState) uint
	}{
		{"attempt", domain.StateReady, domain.StateRunning, func(s domain.RunState) uint { return s.AttemptsUsed }},
		{"retry", domain.StateRunning, domain.StateRetryPending, func(s domain.RunState) uint { return s.OperationalRetriesUsed }},
		{"rework", domain.StateReviewPending, domain.StateReworkRequested, func(s domain.RunState) uint { return s.ReworkRoundsUsed }},
		{"review", domain.StateVerifying, domain.StateReviewPending, func(s domain.RunState) uint { return s.ReviewRound }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
			state.State = test.from
			event := domain.RunEvent{RunID: state.RunID, Sequence: 1, StateFrom: test.from, StateTo: test.to, AttemptID: "attempt:1", Timestamp: time.Unix(2, 0)}
			reduced, err := Reduce(state, event, allGuards())
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := Replay(state, event)
			if err != nil {
				t.Fatal(err)
			}
			if test.check(reduced) != 1 || test.check(replayed) != 1 {
				t.Fatalf("counter values: Reduce=%d Replay=%d", test.check(reduced), test.check(replayed))
			}
		})
	}
}

func allGuards() Guard {
	return Guard{LeaseHeld: true, DraftValid: true, BaseResolved: true, PolicyAllowed: true, AdapterProbed: true, InputsFrozen: true, WorkerProtocolComplete: true, SnapshotRecorded: true, EvidenceCurrent: true, ReportComplete: true, RequiredGatesPass: true, DecisionCurrent: true, NoChangeAllowed: true, RemoteChecksRequired: true, PublicationCurrent: true, BudgetAvailable: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true}
}
