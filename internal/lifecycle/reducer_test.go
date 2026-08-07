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

func TestExplicitAbortTransitionSources(t *testing.T) {
	t.Parallel()
	abortEvent := func(state domain.RunState, to domain.State) domain.RunEvent {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-1",
			RunID: state.RunID, AttemptID: "attempt:1", Sequence: state.Sequence + 1, Type: AbortEventType,
			StateFrom: state.State, StateTo: to, Timestamp: time.Unix(2, 0),
			Actor:   &domain.Actor{Type: "human", ID: "operator"},
			Payload: map[string]any{"terminalReason": AbortTerminalReason, "reason": "abandoned by operator"},
		}
		if to == domain.StateRunning {
			event.Type = "worker.started"
		}
		return event
	}
	tests := []struct {
		name   string
		from   domain.State
		to     domain.State
		wantOK bool
	}{
		{"retry pending to blocked", domain.StateRetryPending, domain.StateBlocked, true},
		{"retry pending to running", domain.StateRetryPending, domain.StateRunning, true},
		{"created to blocked rejected", domain.StateCreated, domain.StateBlocked, false},
		{"planned to blocked rejected", domain.StatePlanned, domain.StateBlocked, false},
		{"ready to blocked rejected", domain.StateReady, domain.StateBlocked, false},
		{"verifying to blocked rejected", domain.StateVerifying, domain.StateBlocked, false},
		{"rework requested to blocked rejected", domain.StateReworkRequested, domain.StateBlocked, false},
		{"blocked re-abort rejected", domain.StateBlocked, domain.StateBlocked, false},
		{"accepted re-abort rejected", domain.StateAccepted, domain.StateBlocked, false},
		{"rejected re-abort rejected", domain.StateRejected, domain.StateBlocked, false},
		{"aborted re-abort rejected", domain.StateAborted, domain.StateBlocked, false},
		{"no change re-abort rejected", domain.StateNoChange, domain.StateBlocked, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
			state.State = test.from
			state.CurrentAttemptID = "attempt:1"
			guard := Guard{LeaseHeld: true}
			if test.to == domain.StateRunning {
				guard.BudgetAvailable = true
			}
			next, err := Reduce(state, abortEvent(state, test.to), guard)
			if test.wantOK && err != nil {
				t.Fatalf("legal abort transition rejected: %v", err)
			}
			if !test.wantOK && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("illegal abort transition error = %v", err)
			}
			if test.wantOK {
				if next.State != test.to || next.Sequence != state.Sequence+1 {
					t.Fatalf("abort transition state = %s sequence = %d", next.State, next.Sequence)
				}
				if test.to == domain.StateBlocked && next.TerminalReason != AbortTerminalReason {
					t.Fatalf("abort terminalReason = %q", next.TerminalReason)
				}
			}
		})
	}
}

func TestExplicitAbortRequiresOnlyLeaseGuard(t *testing.T) {
	t.Parallel()
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	state.State = domain.StateRetryPending
	state.CurrentAttemptID = "attempt:1"
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-2",
		RunID: state.RunID, AttemptID: "attempt:1", Sequence: state.Sequence + 1, Type: AbortEventType,
		StateFrom: state.State, StateTo: domain.StateBlocked, Timestamp: time.Unix(2, 0),
		Actor:   &domain.Actor{Type: "human", ID: "operator"},
		Payload: map[string]any{"terminalReason": AbortTerminalReason, "reason": "abandoned"},
	}
	if _, err := Reduce(state, event, Guard{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("abort without lease error = %v", err)
	}
	reduced, err := Reduce(state, event, Guard{LeaseHeld: true})
	if err != nil {
		t.Fatalf("abort with lease rejected: %v", err)
	}
	replayed, err := Replay(state, event)
	if err != nil {
		t.Fatalf("replay rejected abort event: %v", err)
	}
	if reduced != replayed {
		t.Fatalf("abort reduce and replay diverge: %+v vs %+v", reduced, replayed)
	}
	if replayed.State != domain.StateBlocked || replayed.TerminalReason != AbortTerminalReason {
		t.Fatalf("replayed abort = %+v", replayed)
	}
	if replayed.AttemptsUsed != state.AttemptsUsed || replayed.OperationalRetriesUsed != state.OperationalRetriesUsed || replayed.ReviewRound != state.ReviewRound {
		t.Fatalf("abort mutated budget counters: %+v", replayed)
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

func TestRepairAuditEvent(t *testing.T) {
	t.Parallel()
	baseEvent := func(state domain.RunState) domain.RunEvent {
		return domain.RunEvent{
			RunID:     state.RunID,
			Sequence:  state.Sequence + 1,
			Type:      RepairAuditEventType,
			StateFrom: state.State,
			StateTo:   state.State,
			Timestamp: time.Unix(9, 0),
		}
	}
	t.Run("validate", func(t *testing.T) {
		tests := []struct {
			name    string
			current domain.State
			mutate  func(*domain.RunEvent)
			wantErr bool
		}{
			{"non-terminal success", domain.StateRunning, nil, false},
			{"terminal success", domain.StateAccepted, nil, false},
			{"wrong type rejected", domain.StateRunning, func(e *domain.RunEvent) { e.Type = "snapshot.repaired" }, true},
			{"empty type rejected", domain.StateRunning, func(e *domain.RunEvent) { e.Type = "" }, true},
			{"wrong run id rejected", domain.StateRunning, func(e *domain.RunEvent) { e.RunID = "run:other" }, true},
			{"same sequence rejected", domain.StateRunning, func(e *domain.RunEvent) { e.Sequence-- }, true},
			{"skipped sequence rejected", domain.StateRunning, func(e *domain.RunEvent) { e.Sequence++ }, true},
			{"wrong from rejected", domain.StateRunning, func(e *domain.RunEvent) { e.StateFrom = domain.StateVerifying }, true},
			{"wrong to rejected", domain.StateRunning, func(e *domain.RunEvent) { e.StateTo = domain.StateVerifying }, true},
			{"terminal normal transition rejected", domain.StateAccepted, func(e *domain.RunEvent) {
				e.Type = "state.changed"
				e.StateTo = domain.StateCreated
			}, true},
			{"non-audit self transition rejected", domain.StateRunning, func(e *domain.RunEvent) {
				e.Type = "state.changed"
			}, true},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
				state.State = test.current
				event := baseEvent(state)
				if test.mutate != nil {
					test.mutate(&event)
				}
				err := ValidateTransition(state.State, state.RunID, state.Sequence, event)
				if test.wantErr && !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("want rejection, got err = %v", err)
				}
				if !test.wantErr && err != nil {
					t.Fatalf("repair audit event rejected: %v", err)
				}
			})
		}
	})
	t.Run("replay only advances sequence and updated at", func(t *testing.T) {
		for _, current := range []domain.State{domain.StateRunning, domain.StateAccepted} {
			current := current
			t.Run(string(current), func(t *testing.T) {
				state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
				state.State = current
				state.Sequence = 7
				state.Publication = &domain.RunPublication{Provider: "github", Repository: "org/repo", HeadBranch: "main", BaseBranch: "main", ExternalID: "pub:1", URI: "https://example.com", HeadSHA: "sha"}
				state.CurrentAttemptID = "attempt:1"
				state.ReviewRound, state.AttemptsUsed, state.OperationalRetriesUsed, state.ReworkRoundsUsed = 1, 2, 3, 4
				state.TerminalReason = "done"
				event := baseEvent(state)
				replayed, err := Replay(state, event)
				if err != nil {
					t.Fatalf("replay rejected: %v", err)
				}
				if replayed.Sequence != state.Sequence+1 {
					t.Fatalf("sequence = %d, want %d", replayed.Sequence, state.Sequence+1)
				}
				if !replayed.UpdatedAt.Equal(time.Unix(9, 0).UTC()) {
					t.Fatalf("updatedAt = %v", replayed.UpdatedAt)
				}
				replayed.Sequence, replayed.UpdatedAt = state.Sequence, state.UpdatedAt
				if replayed != state {
					t.Fatalf("replay mutated business fields: %+v vs %+v", replayed, state)
				}
			})
		}
	})
	t.Run("reduce requires lease and preserves business fields", func(t *testing.T) {
		state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
		state.State = domain.StateAccepted
		state.Sequence = 7
		state.TerminalReason = "original"
		event := baseEvent(state)
		event.Payload = map[string]any{"terminalReason": "must-not-replace"}
		if _, err := Reduce(state, event, Guard{}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("repair without lease error = %v", err)
		}
		next, err := Reduce(state, event, Guard{LeaseHeld: true})
		if err != nil {
			t.Fatal(err)
		}
		if next.TerminalReason != state.TerminalReason || next.State != state.State || next.Sequence != event.Sequence {
			t.Fatalf("repair reduce mutated business state: %+v", next)
		}
	})
}

func allGuards() Guard {
	return Guard{LeaseHeld: true, DraftValid: true, BaseResolved: true, PolicyAllowed: true, AdapterProbed: true, InputsFrozen: true, WorkerProtocolComplete: true, SnapshotRecorded: true, EvidenceCurrent: true, ReportComplete: true, RequiredGatesPass: true, DecisionCurrent: true, NoChangeAllowed: true, RemoteChecksRequired: true, PublicationCurrent: true, BudgetAvailable: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true}
}
