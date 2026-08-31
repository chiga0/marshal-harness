//go:build darwin && arm64

package resultingress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

var ErrPreparedExecutionNotCollectible = errors.New("resultingress: prepared execution is not yet collectible")

// PreparedExecutionTranscript is the descriptor-validated output of the exact
// process started by one Attempt. OutcomeFactDigest is the durable supervisor
// outcome that later result admission must cite; transcript bytes themselves
// never enter the authority ledger.
type PreparedExecutionTranscript struct {
	Identity          AttemptIdentity
	OutcomeFactDigest string
	Transcript        processsupervisor.CollectedTranscript
}

type collectedTranscriptReader func(processsupervisor.CollectedTranscriptReadOptions) (processsupervisor.CollectedTranscript, error)

// CollectPreparedExecution performs the production Attach→Collect continuation
// under the current physical owner. It accepts no path or arbitrary transport.
func (s *DurableStore) CollectPreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity) (PreparedExecutionTranscript, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionUnavailable
	}
	return s.collectPreparedExecutionWithTransport(ctx, verifier, acquisition, identity, nil, profile.fixedMarshalPath, productionRebindTransport, processsupervisor.ReadCollectedTranscript)
}

func (s *DurableStore) collectPreparedExecutionWithTransport(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string, transport rebindTransport, read collectedTranscriptReader) (PreparedExecutionTranscript, error) {
	if s == nil || ctx == nil || verifier == nil || transport == nil || read == nil {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
	}
	if err := acquisition.Validate(); err != nil || acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
	}
	var result PreparedExecutionTranscript
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			key, err := identity.Key()
			if err != nil {
				return err
			}
			state, found := projection.attempts[key]
			if !found || state.Identity != identity || state.ProcessStartedDigest == "" || state.BarrierDigest != "" || state.CommittedResultFactDigest != "" || state.SupervisorInterventionDigest != "" || state.SupervisorClosedDigest != "" || state.SupervisorPendingIntentDigest != "" || state.SupervisorBoundAuthorityHead != state.HeadDigest || state.HeadDigest != state.ControlOwnerBindingDigest {
				return ErrPreparedExecutionConflict
			}
			scopeKey, err := acquisition.Scope.key()
			if err != nil {
				return err
			}
			ownerState, found := projection.controlOwners[scopeKey]
			if !found || ownerState.Acquisition != acquisition || state.Owner.OwnerEpoch != acquisition.OwnerEpoch || state.Owner.ControlOwnerAcquiredFactDigest != ownerState.FactDigest {
				return ErrControlOwnerNotCurrent
			}
			if controlDirectory == nil {
				profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
				if !ok || profile == nil || profile.controlRoot == nil || profile.fixedMarshalPath != fixedMarshalPath {
					return ErrPreparedExecutionUnavailable
				}
				controlDirectory, err = openAttachedControlDirectory(profile, state)
				if err != nil {
					return err
				}
				defer controlDirectory.Close()
			}

			// A successful Collect may already be durable when the caller lost the
			// descriptor read result. Re-read the same sealed objects instead of
			// issuing a second command.
			if checkpoint, ok := latestSuccessfulCollect(state); ok {
				transcript, err := read(processsupervisor.CollectedTranscriptReadOptions{FixedMarshalPath: fixedMarshalPath, ControlDirectory: controlDirectory, ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory, Outcome: verifiedCollectOutcome(checkpoint.Evidence)})
				if err != nil {
					return err
				}
				result = PreparedExecutionTranscript{Identity: identity, OutcomeFactDigest: checkpoint.FactDigest, Transcript: transcript}
				return nil
			}

			pre := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
			prepared, err := processsupervisor.PrepareCommand(pre, processsupervisor.CommandOptions{
				Command: processsupervisor.CommandCollect, CommandID: fmt.Sprintf("collect-result-%d", state.SupervisorCommandSequence+1),
				Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead,
				CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(20 * time.Second),
			}, processsupervisor.CollectPayload{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)})
			if err != nil {
				return fmt.Errorf("collect prepare: %w", err)
			}
			intent, err := NewSupervisorCommandIntent(prepared.Evidence())
			if err != nil {
				return fmt.Errorf("collect intent: %w", err)
			}
			authority, options, err := currentAttachOptions(state, ownerState, identity, controlDirectory, fixedMarshalPath)
			if err != nil {
				return err
			}
			var outcome processsupervisor.VerifiedCommandOutcome
			err = transport(ctx, options, func(session AttachedRebindSession) error {
				observation, observeErr := session.Observation()
				if observeErr != nil {
					return fmt.Errorf("collect observation: %w", observeErr)
				}
				if validateRebindObservation(observation, authority) != nil {
					return fmt.Errorf("collect observation mismatch: %w", ErrPreparedExecutionConflict)
				}
				state, _, observeErr = s.appendPreparedSupervisorIntentLocked(projection, state, intent)
				if observeErr != nil {
					return fmt.Errorf("collect intent append: %w", observeErr)
				}
				outcome, observeErr = session.ExecutePreparedCollect(ctx, prepared)
				if observeErr != nil {
					return fmt.Errorf("collect execute: %w", observeErr)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("collect attach: %w", err)
			}
			evidence, err := NewSupervisorCommandEvidence(outcome)
			if err != nil {
				return fmt.Errorf("collect evidence: %w", err)
			}
			state, outcomeDigest, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
			if err != nil {
				return fmt.Errorf("collect outcome append: %w", err)
			}
			if evidence.Disposition != "ok" || evidence.Outcome.State != SupervisorTranscriptCollected {
				return ErrPreparedExecutionNotCollectible
			}
			transcript, err := read(processsupervisor.CollectedTranscriptReadOptions{FixedMarshalPath: fixedMarshalPath, ControlDirectory: controlDirectory, ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory, Outcome: outcome})
			if err != nil {
				return err
			}
			result = PreparedExecutionTranscript{Identity: identity, OutcomeFactDigest: outcomeDigest, Transcript: transcript}
			return nil
		})
	})
	return result, err
}

func currentAttachOptions(state AttemptAuthorityState, ownerState ControlOwnerState, identity AttemptIdentity, controlDirectory *os.File, fixedMarshalPath string) (processsupervisor.AttachAuthority, processsupervisor.AttachOptions, error) {
	if state.Revision < 2 || state.HeadDigest != state.ControlOwnerBindingDigest || state.SupervisorBoundAuthorityHead != state.HeadDigest {
		return processsupervisor.AttachAuthority{}, processsupervisor.AttachOptions{}, ErrPreparedExecutionConflict
	}
	acquisition := processsupervisor.AttachOwnerAcquisition{
		AuthorityNamespaceID: identity.AuthorityNamespaceRef, RepositoryIdentityDigest: ownerState.Acquisition.Scope.RepositoryIdentityDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, OwnerUID: ownerState.Acquisition.OwnerUID, OwnerGID: ownerState.Acquisition.OwnerGID,
		OwnerProcess: ownerState.Acquisition.OwnerProcess, OwnerBinary: ownerState.Acquisition.OwnerBinary,
		ObserverIdentity: ownerState.Acquisition.ObserverIdentity, ObservedAt: ownerState.Acquisition.ObservedAt,
		PreviousFactDigest: ownerState.PreviousFactDigest, FactDigest: ownerState.FactDigest,
	}
	bound := processsupervisor.AttachOwnerBoundFact{
		Authority: supervisorAuthorityTuple(identity), PreviousAttemptRevision: state.Revision - 1, PreviousAttemptHead: state.ProcessStartedDigest,
		AttemptRevision: state.Revision, AttemptHead: state.HeadDigest, ControlOwnerAcquiredFactDigest: ownerState.FactDigest,
		OwnerEpoch: ownerState.Acquisition.OwnerEpoch, FactDigest: state.ControlOwnerBindingDigest,
	}
	authority := processsupervisor.AttachAuthority{
		PreviousSupervisor: supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor), Supervisor: state.SupervisorStarted.Handshake.SupervisorProcess,
		CurrentAcquisition: acquisition, CurrentOwnerBoundFact: bound,
		Child: state.ProcessStartedEvidence.Outcome.Process, ChildObservationDigest: supervisorLastObservation(state),
	}
	options := processsupervisor.AttachOptions{FixedMarshalPath: fixedMarshalPath, ControlDirectory: controlDirectory, ControlDirectoryIdentity: state.SupervisorStarted.ControlDirectory, Authority: authority, OwnerVerifier: heldAttachOwnerVerifier{acquisition: acquisition}}
	return authority, options, nil
}

func latestSuccessfulCollect(state AttemptAuthorityState) (SupervisorCommandCheckpoint, bool) {
	if len(state.SupervisorCommandCheckpoints) == 0 {
		return SupervisorCommandCheckpoint{}, false
	}
	checkpoint := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
	evidence := checkpoint.Evidence
	return checkpoint, evidence.Command == processsupervisor.CommandCollect && evidence.Disposition == "ok" && evidence.Outcome.State == SupervisorTranscriptCollected
}

func verifiedCollectOutcome(evidence SupervisorCommandEvidence) processsupervisor.VerifiedCommandOutcome {
	outcome := evidence.Outcome
	report := &processsupervisor.ProcessReport{
		State: outcome.MechanicsState, ObserverIdentity: outcome.ObserverIdentity, ObservedAt: outcome.ObservedAt,
		Process: outcome.Process, RuntimeObjectDigest: outcome.RuntimeObjectDigest, WorkingObjectDigest: outcome.WorkingObjectDigest,
		SourceGateRevision: outcome.SourceGateRevision, ExactSetDigest: outcome.ExactSetDigest, ExitCode: outcome.ExitCode, Signal: outcome.Signal,
		StdoutDigest: outcome.StdoutDigest, StderrDigest: outcome.StderrDigest, StdoutBytes: outcome.StdoutBytes, StderrBytes: outcome.StderrBytes,
		TranscriptTruncated: outcome.TranscriptTruncated,
	}
	return processsupervisor.VerifiedCommandOutcome{
		Command: evidence.Command, CommandID: evidence.CommandID, Sequence: evidence.Sequence, Status: evidence.Disposition, Disposition: evidence.Disposition,
		ReasonCode: evidence.ReasonCode, RequestDigest: evidence.RequestDigest, ReceiptDigest: evidence.ReceiptDigest,
		ObservationDigest: evidence.ObservationDigest, CommandHead: evidence.CommandHead, TranscriptDigest: outcome.TranscriptDigest,
		StdoutBytes: outcome.StdoutBytes, StderrBytes: outcome.StderrBytes, Truncated: outcome.TranscriptTruncated, ProcessReport: report,
		Recovery: processsupervisor.CommandRecoveryEvidence{PreCommand: supervisorHandshakeAnchor(evidence.PreCommand), PostCommand: supervisorHandshakeAnchor(evidence.PostCommand)},
	}
}
