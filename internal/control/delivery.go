package control

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var (
	ErrTerminalBinding  = errors.New("terminal session is not bound to the current run attempt")
	ErrTerminalDelivery = errors.New("terminal session did not accept the intervention")
)

// ApplyIntervention coordinates a policy-checked control action with the
// active TerminalSession. The Run lease remains held from preflight through
// terminal delivery and InterventionRecord append, preventing lifecycle or
// control records from changing between those operations.
//
// Clarifications and implementation corrections are persisted only after the
// terminal accepted the input. A scope change on a running Attempt terminates
// that exact session before recording new-run-required. DeliveryAccepted on
// the caller's input is ignored; only this coordinator may assert it.
func ApplyIntervention(ctx context.Context, session port.TerminalSession, input InterventionInput, terminateGrace time.Duration) (domain.InterventionRecord, error) {
	if ctx == nil || terminateGrace < 0 || terminateGrace > time.Minute {
		return domain.InterventionRecord{}, ErrInvalidControlInput
	}
	coordinated := input
	coordinated.DeliveryAccepted = input.Category == domain.InterventionCategoryClarification ||
		input.Category == domain.InterventionCategoryImplementationCorrection
	if err := validateInterventionInput(coordinated); err != nil {
		return domain.InterventionRecord{}, err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return domain.InterventionRecord{}, err
	}
	defer lease.Release()
	record, err := prepareIntervention(store, coordinated)
	if err != nil {
		return domain.InterventionRecord{}, err
	}

	switch input.Category {
	case domain.InterventionCategoryClarification, domain.InterventionCategoryImplementationCorrection:
		if err := requireBoundRunningSession(session, input.RunID, input.AttemptID); err != nil {
			return domain.InterventionRecord{}, err
		}
		source := port.TerminalLeadSteering
		if input.SourceType == domain.ControlSourceTypeHuman {
			source = port.TerminalHumanSteering
		}
		if err := session.Send(ctx, source, input.Instruction, input.Now); err != nil {
			return domain.InterventionRecord{}, errors.Join(ErrTerminalDelivery, err)
		}
	case domain.InterventionCategoryScopeChange:
		if record.AttemptID != "" {
			if err := requireBoundSession(session, input.RunID, input.AttemptID, port.TerminalRunning, port.TerminalPaused); err != nil {
				return domain.InterventionRecord{}, err
			}
			if err := session.Terminate(ctx, terminateGrace); err != nil {
				return domain.InterventionRecord{}, errors.Join(ErrTerminalDelivery, err)
			}
		}
	case domain.InterventionCategoryManualPTY:
		// The terminal hook reports an input that already happened. Policy
		// validation above marks the Attempt for mandatory reverification.
	case domain.InterventionCategoryPause:
		if err := requireBoundSession(session, input.RunID, input.AttemptID, port.TerminalRunning); err != nil {
			return domain.InterventionRecord{}, err
		}
		if err := session.Pause(ctx); err != nil {
			return domain.InterventionRecord{}, errors.Join(ErrTerminalDelivery, err)
		}
	case domain.InterventionCategoryResume:
		if err := requireBoundSession(session, input.RunID, input.AttemptID, port.TerminalPaused); err != nil {
			return domain.InterventionRecord{}, err
		}
		if err := session.Resume(ctx); err != nil {
			return domain.InterventionRecord{}, errors.Join(ErrTerminalDelivery, err)
		}
	case domain.InterventionCategoryAbort:
		if record.AttemptID != "" {
			if err := requireBoundSession(session, input.RunID, input.AttemptID, port.TerminalRunning, port.TerminalPaused); err != nil {
				return domain.InterventionRecord{}, err
			}
			if err := session.Terminate(ctx, terminateGrace); err != nil {
				return domain.InterventionRecord{}, errors.Join(ErrTerminalDelivery, err)
			}
		}
	default:
		return domain.InterventionRecord{}, ErrInvalidControlInput
	}

	if err := store.AppendIntervention(lease, input.Validator, record); err != nil {
		return domain.InterventionRecord{}, err
	}
	if input.Category == domain.InterventionCategoryScopeChange || input.Category == domain.InterventionCategoryAbort {
		if err := abortControlledRun(store, lease, input); err != nil {
			return record, err
		}
	}
	return record, nil
}

func abortControlledRun(store *runstore.Store, lease *runstore.Lease, input InterventionInput) error {
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return err
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return err
	}
	eventType, reason := "control.abort-completed", "run aborted by an authorized control request"
	if input.Category == domain.InterventionCategoryScopeChange {
		eventType, reason = "control.scope-change-aborted", "scope change requires a new run"
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindRunEvent,
		EventID:    eventID,
		RunID:      state.RunID,
		AttemptID:  state.CurrentAttemptID,
		Sequence:   state.Sequence + 1,
		Type:       eventType,
		StateFrom:  state.State,
		StateTo:    domain.StateAborted,
		Timestamp:  input.Now.UTC(),
		Actor:      &domain.Actor{Type: "system", ID: "marshal-control-plane"},
		Payload: map[string]any{
			"terminalReason": reason,
		},
	}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{
		LeaseHeld: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true,
	})
	if err != nil {
		return err
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		return err
	}
	return store.WriteSnapshot(lease, next)
}

func requireBoundRunningSession(session port.TerminalSession, runID, attemptID string) error {
	return requireBoundSession(session, runID, attemptID, port.TerminalRunning)
}

func requireBoundSession(session port.TerminalSession, runID, attemptID string, allowed ...port.TerminalState) error {
	if session == nil {
		return ErrTerminalBinding
	}
	identity := session.Identity()
	current := session.State()
	stateAllowed := false
	for _, state := range allowed {
		stateAllowed = stateAllowed || current == state
	}
	if identity.RunID != runID || identity.AttemptID != attemptID || !stateAllowed {
		return ErrTerminalBinding
	}
	return nil
}
