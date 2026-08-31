//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type committedCloseRecoverer func(context.Context, processsupervisor.CommittedCloseRecoveryOptions) (processsupervisor.CommittedCloseRecoveryEvidence, error)

// InspectPreparedExecution executes the path-free Attach→Inspect continuation
// under the current physical owner and durably records both intent and outcome.
func (s *DurableStore) InspectPreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity) (PreparedExecutionTerminalObservation, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionUnavailable
	}
	return s.inspectPreparedExecutionWithTransport(ctx, verifier, acquisition, identity, nil, profile.fixedMarshalPath, productionRebindTransport)
}

func (s *DurableStore) inspectPreparedExecutionWithTransport(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport) (PreparedExecutionTerminalObservation, error) {
	if s == nil || ctx == nil || verifier == nil || transport == nil || acquisition.Validate() != nil || acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID {
		return PreparedExecutionTerminalObservation{}, ErrPreparedExecutionConflict
	}
	var result PreparedExecutionTerminalObservation
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			state, ownerState, directory, closeDirectory, err := s.preparedTerminalState(projection, acquisition, identity, controlDirectory, fixedMarshalPath)
			if err != nil {
				return err
			}
			if closeDirectory {
				defer directory.Close()
			}
			if state.BarrierDigest == "" || state.ProcessTerminalDigest != "" || state.AllocationTerminalDigest != "" || state.SupervisorClosedDigest != "" || state.SupervisorInterventionDigest != "" || state.SupervisorPendingIntentDigest != "" && state.SupervisorPendingIntent.Command != processsupervisor.CommandInspect {
				return ErrPreparedExecutionConflict
			}
			if checkpoint, ok := latestSuccessfulTerminalInspect(state); ok {
				result = PreparedExecutionTerminalObservation{Identity: identity, OutcomeFactDigest: checkpoint.FactDigest, Evidence: checkpoint.Evidence}
				return nil
			}
			pending := state.SupervisorPendingIntentDigest != ""
			var prepared processsupervisor.PreparedCommand
			if pending {
				prepared, err = preparedTerminalCommandFromIntent(state.SupervisorPendingIntent)
			} else {
				pre := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
				prepared, err = processsupervisor.PrepareCommand(pre, processsupervisor.CommandOptions{
					Command: processsupervisor.CommandInspect, CommandID: fmt.Sprintf("inspect-terminal-%d", state.SupervisorCommandSequence+1),
					Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead,
					CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(30 * time.Second),
				}, preparedCleanupPayload(state))
			}
			if err != nil {
				return fmt.Errorf("inspect prepare: %w", err)
			}
			authority, options, err := currentAttachOptions(state, ownerState, identity, directory, fixedMarshalPath)
			if err != nil {
				return err
			}
			var outcome processsupervisor.VerifiedCommandOutcome
			err = transport(ctx, options, func(session AttachedRebindSession) error {
				observation, observeErr := session.Observation()
				if observeErr != nil || validateRebindObservation(observation, authority) != nil {
					return fmt.Errorf("inspect observation: %w", ErrPreparedExecutionConflict)
				}
				if !pending {
					intent, intentErr := NewSupervisorCommandIntent(prepared.Evidence())
					if intentErr != nil {
						return fmt.Errorf("inspect intent: %w", intentErr)
					}
					state, _, observeErr = s.appendPreparedSupervisorIntentLocked(projection, state, intent)
					if observeErr != nil {
						return fmt.Errorf("inspect intent append: %w", observeErr)
					}
				}
				outcome, observeErr = session.ExecutePreparedInspect(ctx, prepared)
				return observeErr
			})
			if err != nil {
				return fmt.Errorf("inspect attach: %w", err)
			}
			evidence, err := NewSupervisorCommandEvidence(outcome)
			if err != nil {
				return fmt.Errorf("inspect evidence: %w", err)
			}
			state, outcomeDigest, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
			if err != nil {
				return fmt.Errorf("inspect outcome append: %w", err)
			}
			if evidence.Disposition != "ok" || !terminalSupervisorState(evidence.Outcome.State) {
				return ErrPreparedExecutionNotTerminal
			}
			result = PreparedExecutionTerminalObservation{Identity: identity, OutcomeFactDigest: outcomeDigest, Evidence: evidence}
			return nil
		})
	})
	return result, err
}

// ClosePreparedExecution executes or recovers the path-free Attach→Close
// continuation, durably records the exact recovered outcome, and returns the
// authenticated absence evidence. Business lifecycle transitions remain the
// responsibility of the runtime composition.
func (s *DurableStore) ClosePreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity) (PreparedExecutionClose, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return PreparedExecutionClose{}, ErrPreparedExecutionUnavailable
	}
	return s.closePreparedExecutionWithTransport(ctx, verifier, acquisition, identity, nil, profile.fixedMarshalPath, productionRebindTransport, processsupervisor.RecoverCommittedClose)
}

func (s *DurableStore) closePreparedExecutionWithTransport(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport, recoverClose committedCloseRecoverer) (PreparedExecutionClose, error) {
	if s == nil || ctx == nil || verifier == nil || transport == nil || recoverClose == nil || acquisition.Validate() != nil || acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID {
		return PreparedExecutionClose{}, ErrPreparedExecutionConflict
	}
	var result PreparedExecutionClose
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			state, ownerState, directory, closeDirectory, err := s.preparedTerminalState(projection, acquisition, identity, controlDirectory, fixedMarshalPath)
			if err != nil {
				return err
			}
			if closeDirectory {
				defer directory.Close()
			}
			if state.ProcessTerminalDigest == "" || state.AllocationTerminalDigest == "" || state.SupervisorClosedDigest != "" || state.SupervisorInterventionDigest != "" {
				return ErrPreparedExecutionNotClosable
			}

			var prepared processsupervisor.PreparedCommand
			var checkpoint *SupervisorCommandCheckpoint
			pending := false
			switch {
			case state.SupervisorPendingIntentDigest != "":
				if state.SupervisorPendingIntent.Command != processsupervisor.CommandClose {
					return ErrPreparedExecutionConflict
				}
				pending = true
				prepared, err = preparedTerminalCommandFromIntent(state.SupervisorPendingIntent)
			case len(state.SupervisorCommandCheckpoints) != 0 && state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.Command == processsupervisor.CommandClose:
				item := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
				if item.Evidence.Disposition != "ok" || item.Evidence.Outcome.State != SupervisorSessionClosed || item.Intent.Command != processsupervisor.CommandClose {
					return ErrPreparedExecutionConflict
				}
				checkpoint = &item
				prepared, err = preparedTerminalCommandFromIntent(item.Intent)
			default:
				pre := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
				prepared, err = processsupervisor.PrepareCommand(pre, processsupervisor.CommandOptions{
					Command: processsupervisor.CommandClose, CommandID: fmt.Sprintf("close-terminal-%d", state.SupervisorCommandSequence+1),
					Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead,
					CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(30 * time.Second),
				}, processsupervisor.ClosePayload{ProcessTerminalFactDigest: state.ProcessTerminalDigest, AllocationTerminatedDigest: state.AllocationTerminalDigest, CleanupBindingDigest: state.CleanupBindingDigest})
				if err == nil {
					intent, intentErr := NewSupervisorCommandIntent(prepared.Evidence())
					if intentErr != nil {
						return intentErr
					}
					authority, options, optionsErr := currentAttachOptions(state, ownerState, identity, directory, fixedMarshalPath)
					if optionsErr != nil {
						return optionsErr
					}
					transportErr := transport(ctx, options, func(session AttachedRebindSession) error {
						observation, observeErr := session.Observation()
						if observeErr != nil || validateRebindObservation(observation, authority) != nil {
							return ErrPreparedExecutionConflict
						}
						state, _, observeErr = s.appendPreparedSupervisorIntentLocked(projection, state, intent)
						if observeErr != nil {
							return observeErr
						}
						pending = true
						_, observeErr = session.ExecutePreparedClose(ctx, prepared)
						return observeErr
					})
					// Close recovery is authoritative even when the response path was
					// lost after the receipt committed.
					if transportErr != nil && state.SupervisorPendingIntentDigest == "" {
						return fmt.Errorf("close attach: %w", transportErr)
					}
				}
			}
			if err != nil {
				return fmt.Errorf("close prepare: %w", err)
			}
			if prepared.Evidence().RequestDigest == "" {
				return ErrPreparedExecutionConflict
			}
			recoveryOptions := processsupervisor.CommittedCloseRecoveryOptions{
				FixedMarshalPath: fixedMarshalPath, ControlDirectory: directory, ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory,
				PreparedClose: prepared, ExpectedSupervisor: state.SupervisorStarted.Handshake.SupervisorProcess,
			}
			recovery, err := recoverClose(ctx, recoveryOptions)
			if err != nil && pending {
				authority, options, optionsErr := pendingTerminalAttachOptions(state, ownerState, identity, directory, fixedMarshalPath)
				if optionsErr != nil {
					return optionsErr
				}
				transportErr := transport(ctx, options, func(session AttachedRebindSession) error {
					observation, observeErr := session.Observation()
					if observeErr != nil || validateRebindObservation(observation, authority) != nil {
						return ErrPreparedExecutionConflict
					}
					_, observeErr = session.ExecutePreparedClose(ctx, prepared)
					return observeErr
				})
				// A successful Close tears down its own transport. Regardless of
				// the response path, only the exact committed receipt plus process
				// absence is authoritative, so re-read it after the replay attempt.
				recovery, err = recoverClose(ctx, recoveryOptions)
				if err != nil && transportErr != nil {
					return fmt.Errorf("close replay transport: %v; recovery: %w", transportErr, err)
				}
			}
			if err != nil {
				return fmt.Errorf("close recovery: %w", err)
			}
			evidence, err := NewSupervisorCommandEvidence(recovery.Outcome)
			if err != nil {
				return fmt.Errorf("close evidence: %w", err)
			}
			outcomeDigest := ""
			if checkpoint == nil {
				state, outcomeDigest, err = s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
				if err != nil {
					return fmt.Errorf("close outcome append: %w", err)
				}
			} else {
				if checkpoint.Evidence != evidence {
					return ErrPreparedExecutionConflict
				}
				outcomeDigest = checkpoint.FactDigest
			}
			result = PreparedExecutionClose{Identity: identity, OutcomeFactDigest: outcomeDigest, Evidence: evidence, Recovery: recovery}
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) preparedTerminalState(projection *Ingress, acquisition ControlOwnerAcquisition, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string) (AttemptAuthorityState, ControlOwnerState, *os.File, bool, error) {
	key, err := identity.Key()
	if err != nil {
		return AttemptAuthorityState{}, ControlOwnerState{}, nil, false, err
	}
	state, found := projection.attempts[key]
	if !found || state.Identity != identity || state.ProcessStartedDigest == "" {
		return AttemptAuthorityState{}, ControlOwnerState{}, nil, false, ErrPreparedExecutionConflict
	}
	scopeKey, err := acquisition.Scope.key()
	if err != nil {
		return AttemptAuthorityState{}, ControlOwnerState{}, nil, false, err
	}
	ownerState, found := projection.controlOwners[scopeKey]
	if !found || ownerState.Acquisition != acquisition || state.Owner.OwnerEpoch != acquisition.OwnerEpoch || state.Owner.ControlOwnerAcquiredFactDigest != ownerState.FactDigest {
		return AttemptAuthorityState{}, ControlOwnerState{}, nil, false, ErrControlOwnerNotCurrent
	}
	if controlDirectory != nil {
		return state, ownerState, controlDirectory, false, nil
	}
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil || profile.fixedMarshalPath != fixedMarshalPath {
		return AttemptAuthorityState{}, ControlOwnerState{}, nil, false, ErrPreparedExecutionUnavailable
	}
	directory, err := openAttachedControlDirectory(profile, state)
	return state, ownerState, directory, err == nil, err
}

func preparedCleanupPayload(state AttemptAuthorityState) processsupervisor.CleanupPayload {
	return processsupervisor.CleanupPayload{
		TerminalizationBarrierDigest: state.BarrierDigest, TerminalizationID: state.TerminalizationID,
		TerminalGeneration: uint64(state.TerminalGeneration), CleanupBindingDigest: state.CleanupBindingDigest,
		ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state),
	}
}

func preparedTerminalCommandFromIntent(intent SupervisorCommandIntent) (processsupervisor.PreparedCommand, error) {
	deadline, err := time.Parse(time.RFC3339Nano, intent.Deadline)
	if err != nil {
		return processsupervisor.PreparedCommand{}, err
	}
	var payload any
	switch intent.Command {
	case processsupervisor.CommandCollect:
		payload = processsupervisor.CollectPayload{ProcessStartedFactDigest: intent.Rebuild.ProcessStartedFactDigest, LastObservationDigest: intent.Rebuild.LastObservationDigest}
	case processsupervisor.CommandInspect:
		payload = processsupervisor.CleanupPayload{
			TerminalizationBarrierDigest: intent.Rebuild.TerminalizationBarrierDigest, TerminalizationID: intent.Rebuild.TerminalizationID,
			TerminalGeneration: intent.Rebuild.TerminalGeneration, CleanupBindingDigest: intent.Rebuild.CleanupBindingDigest,
			ProcessStartedFactDigest: intent.Rebuild.ProcessStartedFactDigest, LastObservationDigest: intent.Rebuild.LastObservationDigest,
		}
	case processsupervisor.CommandClose:
		payload = processsupervisor.ClosePayload{ProcessTerminalFactDigest: intent.Rebuild.ProcessTerminalFactDigest, AllocationTerminatedDigest: intent.Rebuild.AllocationTerminatedFactDigest, CleanupBindingDigest: intent.Rebuild.CleanupBindingDigest}
	default:
		return processsupervisor.PreparedCommand{}, ErrPreparedExecutionConflict
	}
	prepared, err := processsupervisor.PrepareCommand(supervisorHandshakeAnchor(intent.PreCommand), processsupervisor.CommandOptions{
		Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence, PreviousCommandDigest: intent.PreviousCommandHead,
		CurrentAuthorityHead: intent.CurrentAuthorityHead, Deadline: deadline,
	}, payload)
	if err != nil || prepared.Evidence().RequestDigest != intent.RequestDigest || prepared.Evidence().PayloadDigest != intent.PayloadDigest {
		return processsupervisor.PreparedCommand{}, ErrPreparedExecutionConflict
	}
	return prepared, nil
}

func latestSuccessfulTerminalInspect(state AttemptAuthorityState) (SupervisorCommandCheckpoint, bool) {
	if len(state.SupervisorCommandCheckpoints) == 0 {
		return SupervisorCommandCheckpoint{}, false
	}
	checkpoint := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
	evidence := checkpoint.Evidence
	return checkpoint, evidence.Command == processsupervisor.CommandInspect && evidence.Disposition == "ok" && terminalSupervisorState(evidence.Outcome.State)
}

func latestSuccessfulClose(state AttemptAuthorityState) (SupervisorCommandCheckpoint, bool) {
	if len(state.SupervisorCommandCheckpoints) == 0 {
		return SupervisorCommandCheckpoint{}, false
	}
	checkpoint := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
	evidence := checkpoint.Evidence
	return checkpoint, evidence.Command == processsupervisor.CommandClose && evidence.Disposition == "ok" && evidence.Outcome.State == SupervisorSessionClosed
}

func terminalSupervisorState(state SupervisorProcessState) bool {
	return state == SupervisorProcessExited || state == SupervisorProcessAbsent || state == SupervisorProcessIdentityConflict
}
