//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type committedCloseRecovererV2 func(context.Context, processsupervisor.CommittedCloseRecoveryOptionsV2) (processsupervisor.CommittedCloseRecoveryEvidenceV2, error)

func (s *DurableStore) closePreparedExecutionV2Locked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, owner ControlOwnerState,
	identity AttemptIdentity, directory *os.File, fixedPath string, transport continuationTransportV2, recoverClose committedCloseRecovererV2, observe preparedJournalObserverV2) (PreparedExecutionClose, error) {
	if transport == nil || recoverClose == nil || observe == nil || state.SupervisorStarted.Validate() != nil || state.SupervisorMechanicsAnchor.Validate() != nil || state.SupervisorStarted.V2.Anchor.Generation != state.SupervisorMechanicsAnchor.Generation {
		return PreparedExecutionClose{}, ErrPreparedExecutionConflict
	}
	if state.ControlOwnerBindingRevision < 2 || state.ControlOwnerBindingRevision > state.Revision || state.SupervisorBoundAuthorityHead != state.ControlOwnerBindingDigest {
		return PreparedExecutionClose{}, ErrPreparedExecutionConflict
	}
	anchor := supervisorSessionAnchorV2(state.SupervisorMechanicsAnchor)
	var prepared processsupervisor.PreparedCommandV2
	var checkpoint *SupervisorCommandCheckpoint
	var err error
	pending := state.SupervisorPendingIntentDigest != ""
	payload := processsupervisor.ClosePayload{ProcessTerminalFactDigest: state.ProcessTerminalDigest, AllocationTerminatedDigest: state.AllocationTerminalDigest, CleanupBindingDigest: state.CleanupBindingDigest}
	switch {
	case pending:
		if state.SupervisorPendingIntent.Command != processsupervisor.CommandClose || state.SupervisorPendingIntent.PreCommand != state.SupervisorMechanicsAnchor {
			return PreparedExecutionClose{}, ErrPreparedExecutionConflict
		}
		expected, restoreErr := SupervisorPreparedCommandEvidenceV2(state.SupervisorPendingIntent)
		if restoreErr != nil {
			return PreparedExecutionClose{}, restoreErr
		}
		prepared, err = processsupervisor.RebuildPreparedCommandV2(expected, payload)
	case len(state.SupervisorCommandCheckpoints) > 0 && state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.Command == processsupervisor.CommandClose:
		c := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
		checkpoint = &c
		if c.Evidence.Disposition != "ok" || c.Evidence.Outcome.State != SupervisorSessionClosed {
			return PreparedExecutionClose{}, ErrPreparedExecutionConflict
		}
		expected, restoreErr := SupervisorPreparedCommandEvidenceV2(c.Intent)
		if restoreErr != nil {
			return PreparedExecutionClose{}, restoreErr
		}
		prepared, err = processsupervisor.RebuildPreparedCommandV2(expected, payload)
	default:
		prepared, err = processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandClose, CommandID: fmt.Sprintf("close-terminal-%d", state.SupervisorCommandSequence+1),
			Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead, CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(30 * time.Second)}, payload)
	}
	if err != nil {
		return PreparedExecutionClose{}, err
	}
	options := processsupervisor.CommittedCloseRecoveryOptionsV2{FixedMarshalPath: fixedPath, ControlDirectory: directory, PreparedClose: prepared, ExpectedSupervisor: state.SupervisorStarted.V2.Handshake.SupervisorProcess}
	var recovered processsupervisor.CommittedCloseRecoveryEvidenceV2
	var recoveryErr error
	if pending || checkpoint != nil {
		recovered, recoveryErr = recoverClose(ctx, options)
	} else {
		recoveryErr = processsupervisor.ErrIntervention
	}
	if recoveryErr != nil {
		if checkpoint != nil {
			return PreparedExecutionClose{}, recoveryErr
		}
		if pending {
			classification, err := observe(ctx, processsupervisor.PreparedJournalOptionsV2{ControlDirectory: directory, Prepared: prepared})
			// A committed Close with a still-live or ambiguous Supervisor must
			// wait for absence, never replay an already closed command.
			if err != nil || classification.Reconciliation != processsupervisor.ReconciliationUnchanged || classification.Outcome != nil {
				return PreparedExecutionClose{}, processsupervisor.ErrIntervention
			}
		}
		authority := rebindAttachAuthorityV2(state, owner, identity, anchor)
		if authority.Validate() != nil {
			return PreparedExecutionClose{}, ErrPreparedExecutionConflict
		}
		attach := processsupervisor.AttachOptionsV2{FixedMarshalPath: fixedPath, ControlDirectory: directory, Authority: authority, OwnerVerifier: heldAttachOwnerVerifierV2{authority: authority}}
		called := false
		transportErr := transport(ctx, attach, func(session attachedContinuationV2) error {
			if called || session == nil {
				return ErrPreparedExecutionConflict
			}
			called = true
			observation, err := session.Observation()
			if err != nil || observation.Validate() != nil || observation.Response.Authority != authority {
				return ErrPreparedExecutionConflict
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
				pending = true
			}
			_, err = session.ExecutePreparedClose(ctx, prepared)
			return err
		})
		if !called || !pending {
			if transportErr != nil {
				return PreparedExecutionClose{}, transportErr
			}
			return PreparedExecutionClose{}, ErrPreparedExecutionConflict
		}
		// A dropped response after receipt+exit is recoverable without another
		// command. Do not turn transport EOF into a successful Close by itself.
		recovered, recoveryErr = recoverClose(ctx, options)
		if recoveryErr != nil {
			return PreparedExecutionClose{}, recoveryErr
		}
	}
	if recovered.Validate() != nil || recovered.Outcome.Preparation != prepared.Evidence() || recovered.Absence.Expected != options.ExpectedSupervisor {
		return PreparedExecutionClose{}, ErrPreparedExecutionConflict
	}
	evidence, err := NewSupervisorCommandEvidenceV2(recovered.Outcome)
	if err != nil {
		return PreparedExecutionClose{}, err
	}
	fact := ""
	if checkpoint == nil {
		_, fact, err = s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
		if err != nil {
			return PreparedExecutionClose{}, err
		}
	} else {
		if checkpoint.Evidence != evidence {
			return PreparedExecutionClose{}, ErrPreparedExecutionConflict
		}
		fact = checkpoint.FactDigest
	}
	return PreparedExecutionClose{Identity: identity, OutcomeFactDigest: fact, Evidence: evidence, RecoveryV2: &recovered}, nil
}
