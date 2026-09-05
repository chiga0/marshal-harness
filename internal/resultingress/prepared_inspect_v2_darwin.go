//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func (s *DurableStore) inspectPreparedExecutionV2Locked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, owner ControlOwnerState,
	identity AttemptIdentity, directory *os.File, fixedPath string, transport continuationTransportV2, observe preparedJournalObserverV2) (PreparedExecutionTerminalObservation, error) {
	if transport == nil || observe == nil || state.SupervisorStarted.Validate() != nil || state.SupervisorMechanicsAnchor.Validate() != nil ||
		state.SupervisorStarted.V2.Anchor.Generation != state.SupervisorMechanicsAnchor.Generation {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	if state.ControlOwnerBindingRevision < 2 || state.ControlOwnerBindingRevision > state.Revision || state.SupervisorBoundAuthorityHead != state.ControlOwnerBindingDigest {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	pending := state.SupervisorPendingIntentDigest != ""
	anchor := supervisorSessionAnchorV2(state.SupervisorMechanicsAnchor)
	var prepared processsupervisor.PreparedCommandV2
	var outcome processsupervisor.VerifiedCommandOutcomeV2
	var err error
	committed := false
	if pending {
		intent := state.SupervisorPendingIntent
		if intent.Command != processsupervisor.CommandInspect || intent.PreCommand != state.SupervisorMechanicsAnchor {
			return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
		}
		expected, err := SupervisorPreparedCommandEvidenceV2(intent)
		if err != nil {
			return PreparedExecutionTerminalObservation{}, err
		}
		prepared, err = processsupervisor.RebuildPreparedCommandV2(expected, preparedCleanupPayload(state))
		if err != nil {
			return PreparedExecutionTerminalObservation{}, err
		}
		classification, err := observe(ctx, processsupervisor.PreparedJournalOptionsV2{ControlDirectory: directory, Prepared: prepared})
		if err != nil {
			return PreparedExecutionTerminalObservation{}, processsupervisor.ErrIntervention
		}
		switch classification.Reconciliation {
		case processsupervisor.ReconciliationUnchanged:
			if classification.Outcome != nil {
				return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
			}
		case processsupervisor.ReconciliationReceiptCommitted:
			if classification.Outcome == nil || classification.Outcome.Validate() != nil || classification.Outcome.Preparation != expected {
				return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
			}
			outcome = *classification.Outcome
			anchor = outcome.PostCommand
			committed = true
		default:
			return PreparedExecutionTerminalObservation{}, processsupervisor.ErrIntervention
		}
	} else {
		prepared, err = processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandInspect, CommandID: fmt.Sprintf("inspect-terminal-%d", state.SupervisorCommandSequence+1),
			Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead, CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(30 * time.Second)}, preparedCleanupPayload(state))
		if err != nil {
			return PreparedExecutionTerminalObservation{}, err
		}
	}
	authority := rebindAttachAuthorityV2(state, owner, identity, anchor)
	if committed && outcome.Status == "ok" {
		authority.ChildObservationDigest = outcome.ObservationDigest
	}
	if authority.Validate() != nil {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	options := processsupervisor.AttachOptionsV2{FixedMarshalPath: fixedPath, ControlDirectory: directory, Authority: authority, OwnerVerifier: heldAttachOwnerVerifierV2{authority: authority}}
	called := false
	err = transport(ctx, options, func(session attachedContinuationV2) error {
		if called || session == nil {
			return ErrPreparedExecutionConflict
		}
		called = true
		observation, err := session.Observation()
		if err != nil || observation.Validate() != nil || observation.Response.Authority != authority {
			return ErrPreparedExecutionConflict
		}
		if committed {
			return nil
		}
		if !pending {
			intent, err := NewSupervisorCommandIntentV2(prepared.Evidence())
			if err != nil {
				return err
			}
			state, _, err = s.appendPreparedSupervisorIntentLocked(projection, state, intent)
			if err != nil {
				return err
			}
		}
		outcome, err = session.ExecutePreparedInspect(ctx, prepared)
		return err
	})
	if err != nil {
		return PreparedExecutionTerminalObservation{}, err
	}
	if !called {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	evidence, err := NewSupervisorCommandEvidenceV2(outcome)
	if err != nil {
		return PreparedExecutionTerminalObservation{}, err
	}
	_, fact, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
	if err != nil {
		return PreparedExecutionTerminalObservation{}, err
	}
	if evidence.Disposition != "ok" || !terminalSupervisorState(evidence.Outcome.State) {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionNotTerminal
	}
	return PreparedExecutionTerminalObservation{Identity: identity, OutcomeFactDigest: fact, Evidence: evidence}, nil
}
