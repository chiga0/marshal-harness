package lifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func stoppedFixture() (domain.RunState, domain.RunEvent, Guard) {
	state := domain.NewRunState("task-1", "run-1", time.Unix(100, 0))
	state.State, state.Sequence, state.CurrentAttemptID, state.AttemptsUsed = domain.StateRunning, 3, "attempt-1", 1
	payload := map[string]any{"terminalReason": AbortTerminalReason}
	for _, key := range stopEvidenceFields {
		payload[key] = "sha256:" + strings.Repeat("a", 64)
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event-stop", RunID: state.RunID, AttemptID: state.CurrentAttemptID, Sequence: 4, Type: WorkerStoppedEventType, StateFrom: domain.StateRunning, StateTo: domain.StateBlocked, Timestamp: time.Unix(110, 0), Actor: &domain.Actor{Type: "system", ID: "marshal-core"}, Payload: payload}
	return state, event, Guard{LeaseHeld: true, StopAuthorized: true, ChildrenStopped: true, EvidenceCurrent: true, EvidenceFlushed: true}
}

func TestWorkerStoppedRequiresAuthorityAndDoesNotSpendRetryBudget(t *testing.T) {
	state, event, guard := stoppedFixture()
	for _, reason := range []string{AbortTerminalReason, "attempt-deadline-exceeded", "run-deadline-exceeded"} {
		event.Payload["terminalReason"] = reason
		next, err := Reduce(state, event, guard)
		if err != nil || next.State != domain.StateBlocked || next.TerminalReason != reason || next.AttemptsUsed != state.AttemptsUsed || next.OperationalRetriesUsed != state.OperationalRetriesUsed || next.ReworkRoundsUsed != state.ReworkRoundsUsed {
			t.Fatalf("unexpected stop: %+v err=%v", next, err)
		}
		replay, err := Replay(state, event)
		if err != nil || replay.State != next.State || replay.Sequence != next.Sequence {
			t.Fatalf("replay mismatch: %v", err)
		}
		if _, err := Reduce(next, event, guard); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("duplicate terminal event accepted: %v", err)
		}
	}
	for _, missing := range []string{"lease", "intent", "children", "current", "flushed"} {
		t.Run(missing, func(t *testing.T) {
			s, e, g := stoppedFixture()
			switch missing {
			case "lease":
				g.LeaseHeld = false
			case "intent":
				g.StopAuthorized = false
			case "children":
				g.ChildrenStopped = false
			case "current":
				g.EvidenceCurrent = false
			case "flushed":
				g.EvidenceFlushed = false
			}
			if _, err := Reduce(s, e, g); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("missing guard accepted: %v", err)
			}
		})
	}
}

func TestWorkerStoppedRejectsForgedEnvelopeAndEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*domain.RunState, *domain.RunEvent){
		"wrong-attempt": func(_ *domain.RunState, e *domain.RunEvent) { e.AttemptID = "other" },
		"worker-actor":  func(_ *domain.RunState, e *domain.RunEvent) { e.Actor.Type = "worker" },
		"wrong-core":    func(_ *domain.RunState, e *domain.RunEvent) { e.Actor.ID = "pretend-core" },
		"missing-time":  func(_ *domain.RunState, e *domain.RunEvent) { e.Timestamp = time.Time{} },
		"success":       func(_ *domain.RunState, e *domain.RunEvent) { e.StateTo = domain.StateAccepted },
		"wrong-state": func(s *domain.RunState, e *domain.RunEvent) {
			s.State = domain.StateReady
			e.StateFrom = domain.StateReady
		},
		"free-reason":     func(_ *domain.RunState, e *domain.RunEvent) { e.Payload["terminalReason"] = "operator free text" },
		"unknown-field":   func(_ *domain.RunState, e *domain.RunEvent) { e.Payload["pid"] = 42 },
		"missing-cleanup": func(_ *domain.RunState, e *domain.RunEvent) { delete(e.Payload, "cleanupReleasedFactDigest") },
		"bad-digest":      func(_ *domain.RunState, e *domain.RunEvent) { e.Payload["stopIntentDigest"] = "claimed" },
	} {
		t.Run(name, func(t *testing.T) {
			s, e, g := stoppedFixture()
			mutate(&s, &e)
			if _, err := Reduce(s, e, g); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("forgery accepted: %v", err)
			}
		})
	}
}
