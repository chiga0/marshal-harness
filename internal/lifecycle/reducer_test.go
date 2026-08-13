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
		payload := map[string]any{"terminalReason": AbortTerminalReason, "reason": "abandoned by operator"}
		attemptID := "attempt:1"
		if to == domain.StateAborted {
			// The pre-attempt class never carries an attempt identity and
			// binds the second closed terminalReason member.
			payload["terminalReason"] = PreAttemptAbortTerminalReason
			attemptID = ""
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-1",
			RunID: state.RunID, AttemptID: attemptID, Sequence: state.Sequence + 1, Type: AbortEventType,
			StateFrom: state.State, StateTo: to, Timestamp: time.Unix(2, 0),
			Actor:   &domain.Actor{Type: "human", ID: "operator"},
			Payload: payload,
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
		{"planned to aborted", domain.StatePlanned, domain.StateAborted, true},
		{"ready to aborted", domain.StateReady, domain.StateAborted, true},
		{"created to blocked rejected", domain.StateCreated, domain.StateBlocked, false},
		{"planned to blocked rejected", domain.StatePlanned, domain.StateBlocked, false},
		{"ready to blocked rejected", domain.StateReady, domain.StateBlocked, false},
		{"verifying to blocked rejected", domain.StateVerifying, domain.StateBlocked, false},
		{"rework requested to blocked rejected", domain.StateReworkRequested, domain.StateBlocked, false},
		{"created to aborted rejected", domain.StateCreated, domain.StateAborted, false},
		{"running to aborted rejected", domain.StateRunning, domain.StateAborted, false},
		{"retry pending to aborted rejected", domain.StateRetryPending, domain.StateAborted, false},
		{"verifying to aborted rejected", domain.StateVerifying, domain.StateAborted, false},
		{"review pending to aborted rejected", domain.StateReviewPending, domain.StateAborted, false},
		{"rework requested to aborted rejected", domain.StateReworkRequested, domain.StateAborted, false},
		{"publishing to aborted rejected", domain.StatePublishing, domain.StateAborted, false},
		{"published to aborted rejected", domain.StatePublished, domain.StateAborted, false},
		{"ci pending to aborted rejected", domain.StateCIPending, domain.StateAborted, false},
		{"blocked re-abort rejected", domain.StateBlocked, domain.StateBlocked, false},
		{"blocked to aborted re-abort rejected", domain.StateBlocked, domain.StateAborted, false},
		{"accepted re-abort rejected", domain.StateAccepted, domain.StateBlocked, false},
		{"rejected re-abort rejected", domain.StateRejected, domain.StateBlocked, false},
		{"aborted re-abort rejected", domain.StateAborted, domain.StateBlocked, false},
		{"aborted to aborted re-abort rejected", domain.StateAborted, domain.StateAborted, false},
		{"no change re-abort rejected", domain.StateNoChange, domain.StateBlocked, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
			state.State = test.from
			if test.to != domain.StateAborted {
				state.CurrentAttemptID = "attempt:1"
			}
			guard := Guard{LeaseHeld: true}
			if test.to == domain.StateRunning {
				guard.BudgetAvailable = true
			}
			if test.to == domain.StateAborted {
				// The CLI proves every ADR 0029 negative fact before setting
				// this guard; at the reducer level it is an independent
				// fail-closed obligation, exercised below.
				guard.AbortAuthorized, guard.ChildrenStopped, guard.EvidenceFlushed, guard.PreAttemptAbsenceProven = true, true, true, true
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
				if test.to == domain.StateAborted && next.TerminalReason != PreAttemptAbortTerminalReason {
					t.Fatalf("pre-attempt abort terminalReason = %q", next.TerminalReason)
				}
				if test.to == domain.StateAborted && (next.CurrentAttemptID != "" || next.AttemptsUsed != 0) {
					t.Fatalf("pre-attempt abort invented attempt identity: %+v", next)
				}
			}
		})
	}
}

func TestPreAttemptAbortGuardsFailClosed(t *testing.T) {
	t.Parallel()
	newState := func(current domain.State) domain.RunState {
		state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
		state.State = current
		return state
	}
	newEvent := func(state domain.RunState) domain.RunEvent {
		return domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-pre",
			RunID: state.RunID, AttemptID: "", Sequence: state.Sequence + 1, Type: AbortEventType,
			StateFrom: state.State, StateTo: domain.StateAborted, Timestamp: time.Unix(2, 0),
			Actor:   &domain.Actor{Type: "human", ID: "operator"},
			Payload: map[string]any{"terminalReason": PreAttemptAbortTerminalReason, "reason": "abandoned before any attempt"},
		}
	}
	fullGuard := Guard{LeaseHeld: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true, PreAttemptAbsenceProven: true}
	for _, current := range []domain.State{domain.StatePlanned, domain.StateReady} {
		current := current
		t.Run(string(current), func(t *testing.T) {
			mutations := []struct {
				name   string
				mutate func(*Guard)
			}{
				{"missing lease", func(g *Guard) { g.LeaseHeld = false }},
				{"missing absence proof", func(g *Guard) { g.PreAttemptAbsenceProven = false }},
				{"missing abort authorization", func(g *Guard) { g.AbortAuthorized = false }},
				{"missing children-stopped proof", func(g *Guard) { g.ChildrenStopped = false }},
				{"missing evidence-flushed proof", func(g *Guard) { g.EvidenceFlushed = false }},
			}
			for _, mutation := range mutations {
				mutation := mutation
				t.Run(mutation.name, func(t *testing.T) {
					state := newState(current)
					guard := fullGuard
					mutation.mutate(&guard)
					if _, err := Reduce(state, newEvent(state), guard); !errors.Is(err, ErrInvalidTransition) {
						t.Fatalf("unguarded pre-attempt abort error = %v", err)
					}
				})
			}
			state := newState(current)
			next, err := Reduce(state, newEvent(state), fullGuard)
			if err != nil {
				t.Fatalf("fully guarded pre-attempt abort rejected: %v", err)
			}
			if next.State != domain.StateAborted || next.TerminalReason != PreAttemptAbortTerminalReason {
				t.Fatalf("pre-attempt abort result = %+v", next)
			}
		})
	}
}

func TestAbortEventStructuralValidation(t *testing.T) {
	t.Parallel()
	baseEvent := func(current domain.State) domain.RunEvent {
		payload := map[string]any{"terminalReason": AbortTerminalReason, "reason": "abandoned"}
		attemptID := "attempt:1"
		stateTo := domain.StateBlocked
		if current == domain.StatePlanned || current == domain.StateReady {
			payload["terminalReason"] = PreAttemptAbortTerminalReason
			attemptID = ""
			stateTo = domain.StateAborted
		}
		return domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-struct",
			RunID: "run:1", AttemptID: attemptID, Sequence: 1, Type: AbortEventType,
			StateFrom: current, StateTo: stateTo, Timestamp: time.Unix(2, 0),
			Actor:   &domain.Actor{Type: "human", ID: "operator"},
			Payload: payload,
		}
	}
	tests := []struct {
		name    string
		current domain.State
		mutate  func(*domain.RunEvent)
		wantErr bool
	}{
		{"retry pending to blocked allowed", domain.StateRetryPending, nil, false},
		{"planned to aborted allowed", domain.StatePlanned, nil, false},
		{"ready to aborted allowed", domain.StateReady, nil, false},
		{"created source rejected", domain.StateCreated, func(e *domain.RunEvent) { e.StateFrom = domain.StateCreated; e.StateTo = domain.StateAborted }, true},
		{"running source rejected", domain.StateRunning, func(e *domain.RunEvent) { e.StateFrom = domain.StateRunning; e.StateTo = domain.StateAborted }, true},
		{"publishing source rejected", domain.StatePublishing, func(e *domain.RunEvent) { e.StateFrom = domain.StatePublishing; e.StateTo = domain.StateAborted }, true},
		{"published source rejected", domain.StatePublished, func(e *domain.RunEvent) { e.StateFrom = domain.StatePublished; e.StateTo = domain.StateAborted }, true},
		{"ci pending source rejected", domain.StateCIPending, func(e *domain.RunEvent) { e.StateFrom = domain.StateCIPending; e.StateTo = domain.StateAborted }, true},
		{"terminal blocked source rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.StateFrom = domain.StateBlocked; e.StateTo = domain.StateAborted }, true},
		{"pre-attempt target must be aborted", domain.StateReady, func(e *domain.RunEvent) { e.StateTo = domain.StateBlocked }, true},
		{"retry pending target must be blocked", domain.StateRetryPending, func(e *domain.RunEvent) { e.StateTo = domain.StateAborted; e.AttemptID = "" }, true},
		{"pre-attempt attempt id rejected", domain.StateReady, func(e *domain.RunEvent) { e.AttemptID = "attempt:1" }, true},
		{"omitted actor rejected", domain.StateReady, func(e *domain.RunEvent) { e.Actor = nil }, true},
		{"system actor rejected", domain.StateReady, func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "system", ID: "marshal-worker-runner"} }, true},
		{"blank actor id rejected", domain.StateReady, func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "human", ID: "   "} }, true},
		{"wrong terminal reason rejected", domain.StateReady, func(e *domain.RunEvent) { e.Payload["terminalReason"] = AbortTerminalReason }, true},
		{"free text terminal reason rejected", domain.StateReady, func(e *domain.RunEvent) { e.Payload["terminalReason"] = "abandoned by alice" }, true},
		{"missing reason rejected", domain.StateReady, func(e *domain.RunEvent) { delete(e.Payload, "reason") }, true},
		{"blank reason rejected", domain.StateReady, func(e *domain.RunEvent) { e.Payload["reason"] = "   " }, true},
		{"extra payload field rejected", domain.StateReady, func(e *domain.RunEvent) { e.Payload["injected"] = "value" }, true},
		{"wrong run id rejected", domain.StateReady, func(e *domain.RunEvent) { e.RunID = "run:other" }, true},
		{"same sequence rejected", domain.StateReady, func(e *domain.RunEvent) { e.Sequence-- }, true},
		{"skipped sequence rejected", domain.StateReady, func(e *domain.RunEvent) { e.Sequence++ }, true},
		{"wrong from rejected", domain.StateReady, func(e *domain.RunEvent) { e.StateFrom = domain.StatePlanned }, true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			event := baseEvent(test.current)
			if test.mutate != nil {
				test.mutate(&event)
			}
			err := ValidateTransition(test.current, "run:1", 0, event)
			if test.wantErr && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("want rejection, got err = %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("abort event rejected: %v", err)
			}
		})
	}
}

func TestPreAttemptAbortReduceMatchesReplay(t *testing.T) {
	t.Parallel()
	for _, current := range []domain.State{domain.StatePlanned, domain.StateReady} {
		current := current
		t.Run(string(current), func(t *testing.T) {
			state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
			state.State = current
			state.SpecDigest, state.PolicyDigest, state.CapabilityDigest = "sha256:"+repeatHex("a", 64), "sha256:"+repeatHex("b", 64), "sha256:"+repeatHex("c", 64)
			state.BaseSHA, state.WorktreePath = repeatHex("d", 40), "/tmp/worktree"
			event := domain.RunEvent{
				APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:abort-pre-2",
				RunID: state.RunID, AttemptID: "", Sequence: state.Sequence + 1, Type: AbortEventType,
				StateFrom: state.State, StateTo: domain.StateAborted, Timestamp: time.Unix(2, 0),
				Actor:   &domain.Actor{Type: "human", ID: "operator"},
				Payload: map[string]any{"terminalReason": PreAttemptAbortTerminalReason, "reason": "abandoned before any attempt"},
			}
			reduced, err := Reduce(state, event, Guard{LeaseHeld: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true, PreAttemptAbsenceProven: true})
			if err != nil {
				t.Fatalf("pre-attempt abort rejected: %v", err)
			}
			replayed, err := Replay(state, event)
			if err != nil {
				t.Fatalf("replay rejected pre-attempt abort event: %v", err)
			}
			if reduced != replayed {
				t.Fatalf("pre-attempt abort reduce and replay diverge: %+v vs %+v", reduced, replayed)
			}
			if replayed.State != domain.StateAborted || replayed.TerminalReason != PreAttemptAbortTerminalReason {
				t.Fatalf("replayed pre-attempt abort = %+v", replayed)
			}
			if replayed.AttemptsUsed != 0 || replayed.CurrentAttemptID != "" || replayed.OperationalRetriesUsed != 0 || replayed.ReviewRound != 0 || replayed.ReworkRoundsUsed != 0 {
				t.Fatalf("pre-attempt abort mutated budget counters: %+v", replayed)
			}
			if replayed.SpecDigest != state.SpecDigest || replayed.PolicyDigest != state.PolicyDigest || replayed.CapabilityDigest != state.CapabilityDigest || replayed.BaseSHA != state.BaseSHA || replayed.WorktreePath != state.WorktreePath {
				t.Fatalf("pre-attempt abort rewrote frozen inputs: %+v", replayed)
			}
		})
	}
}

// TestExplicitAbortRequiresOnlyLeaseGuard keeps the frozen ADR 0012
// semantics: the RETRY_PENDING exit needs exactly the lease guard and never
// the pre-attempt absence proof.
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

func reconcileGuards() Guard {
	guard := allGuards()
	guard.ReconcileAuthorized = true
	return guard
}

func reconcileEvent(state domain.RunState) domain.RunEvent {
	return domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:reconcile-1",
		RunID: state.RunID, Sequence: state.Sequence + 1, Type: PublicationReconcileEventType,
		StateFrom: state.State, StateTo: domain.StateAccepted, Timestamp: time.Unix(9, 0),
		Actor: &domain.Actor{Type: "system", ID: "marshal-reconciliation"},
		Payload: map[string]any{
			"receiptDigest":     "sha256:" + "c0" + repeatHex("c", 62),
			"reconcileId":       "reconcile:" + repeatHex("1", 64),
			"publicationDigest": "sha256:" + repeatHex("b", 64),
			"decisionDigest":    "sha256:" + repeatHex("d", 64),
			"terminalReason":    "reconciled-after-merge",
		},
	}
}

func repeatHex(fill string, count int) string {
	value := make([]byte, 0, count)
	for index := 0; index < count; index++ {
		value = append(value, fill[0])
	}
	return string(value)
}

func TestPublicationReconcileValidateTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current domain.State
		mutate  func(*domain.RunEvent)
		wantErr bool
	}{
		{"blocked to accepted allowed", domain.StateBlocked, nil, false},
		{"accepted terminal forged", domain.StateAccepted, func(e *domain.RunEvent) { e.StateFrom = domain.StateAccepted }, true},
		{"rejected terminal forged", domain.StateRejected, func(e *domain.RunEvent) { e.StateFrom = domain.StateRejected }, true},
		{"aborted terminal forged", domain.StateAborted, func(e *domain.RunEvent) { e.StateFrom = domain.StateAborted }, true},
		{"no change terminal forged", domain.StateNoChange, func(e *domain.RunEvent) { e.StateFrom = domain.StateNoChange }, true},
		{"non-terminal source rejected", domain.StateCIPending, func(e *domain.RunEvent) { e.StateFrom = domain.StateCIPending }, true},
		{"blocked to rejected rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.StateTo = domain.StateRejected }, true},
		{"blocked to blocked rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.StateTo = domain.StateBlocked }, true},
		{"omitted actor rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.Actor = nil }, true},
		{"wrong actor type rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "publisher", ID: "marshal-reconciliation"} }, true},
		{"wrong actor id rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "system", ID: "marshal-review"} }, true},
		{"wrong run id rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.RunID = "run:other" }, true},
		{"same sequence rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.Sequence-- }, true},
		{"skipped sequence rejected", domain.StateBlocked, func(e *domain.RunEvent) { e.Sequence++ }, true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
			state.State = test.current
			event := reconcileEvent(state)
			if test.mutate != nil {
				test.mutate(&event)
			}
			err := ValidateTransition(state.State, state.RunID, state.Sequence, event)
			if test.wantErr && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("want rejection, got err = %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("reconcile transition rejected: %v", err)
			}
		})
	}
}

func TestPublicationReconcileReduceGuards(t *testing.T) {
	t.Parallel()
	newState := func() domain.RunState {
		state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
		state.State = domain.StateBlocked
		state.Publication = &domain.RunPublication{Provider: "github", Repository: "org/repo", HeadBranch: "marshal/task-1", BaseBranch: "main", ExternalID: "PR_1", URI: "https://example.invalid/pr/1", HeadSHA: repeatHex("2", 40)}
		return state
	}
	t.Run("authorized reconcile accepts without required-gates guard", func(t *testing.T) {
		state := newState()
		guard := reconcileGuards()
		guard.RequiredGatesPass = false
		next, err := Reduce(state, reconcileEvent(state), guard)
		if err != nil {
			t.Fatalf("authorized reconcile rejected: %v", err)
		}
		if next.State != domain.StateAccepted || next.TerminalReason != "reconciled-after-merge" {
			t.Fatalf("reconciled state = %+v", next)
		}
		if next.Publication == nil || next.Publication.HeadSHA != state.Publication.HeadSHA {
			t.Fatalf("reconcile must preserve the publication snapshot: %+v", next.Publication)
		}
	})
	for name, mutate := range map[string]func(*Guard){
		"missing reconcile authorization": func(g *Guard) { g.ReconcileAuthorized = false },
		"missing evidence currency":       func(g *Guard) { g.EvidenceCurrent = false },
		"missing publication currency":    func(g *Guard) { g.PublicationCurrent = false },
		"missing decision currency":       func(g *Guard) { g.DecisionCurrent = false },
		"missing lease":                   func(g *Guard) { g.LeaseHeld = false },
	} {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			state := newState()
			guard := reconcileGuards()
			mutate(&guard)
			if _, err := Reduce(state, reconcileEvent(state), guard); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("unguarded reconcile error = %v", err)
			}
		})
	}
}

func TestPublicationReconcileReplay(t *testing.T) {
	t.Parallel()
	newState := func() domain.RunState {
		state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
		state.State = domain.StateBlocked
		state.Sequence = 7
		state.Publication = &domain.RunPublication{Provider: "github", Repository: "org/repo", HeadBranch: "marshal/task-1", BaseBranch: "main", ExternalID: "PR_1", URI: "https://example.invalid/pr/1", HeadSHA: repeatHex("2", 40)}
		return state
	}
	t.Run("valid event replays to accepted and preserves publication", func(t *testing.T) {
		state := newState()
		event := reconcileEvent(state)
		replayed, err := Replay(state, event)
		if err != nil {
			t.Fatalf("replay rejected: %v", err)
		}
		if replayed.State != domain.StateAccepted || replayed.Sequence != state.Sequence+1 || replayed.TerminalReason != "reconciled-after-merge" {
			t.Fatalf("replayed = %+v", replayed)
		}
		if replayed.Publication == nil || *replayed.Publication != *state.Publication {
			t.Fatalf("replay must keep the BLOCKED publication snapshot: %+v", replayed.Publication)
		}
	})
	t.Run("missing payload fields fail closed", func(t *testing.T) {
		for _, key := range []string{"receiptDigest", "reconcileId", "publicationDigest", "decisionDigest", "terminalReason"} {
			key := key
			t.Run(key, func(t *testing.T) {
				state := newState()
				event := reconcileEvent(state)
				delete(event.Payload, key)
				if _, err := Replay(state, event); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("replay without %s error = %v", key, err)
				}
			})
		}
	})
	t.Run("omitted or forged actor rejected", func(t *testing.T) {
		for name, mutate := range map[string]func(*domain.RunEvent){
			"omitted":     func(e *domain.RunEvent) { e.Actor = nil },
			"forged type": func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "publisher", ID: "marshal-reconciliation"} },
			"forged id":   func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "system", ID: "marshal-github-publisher"} },
			"wrong from":  func(e *domain.RunEvent) { e.StateFrom = domain.StateCIPending },
			"wrong to":    func(e *domain.RunEvent) { e.StateTo = domain.StateRejected },
		} {
			mutate := mutate
			t.Run(name, func(t *testing.T) {
				state := newState()
				event := reconcileEvent(state)
				mutate(&event)
				if _, err := Replay(state, event); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("replay of forged reconcile error = %v", err)
				}
			})
		}
	})
}
