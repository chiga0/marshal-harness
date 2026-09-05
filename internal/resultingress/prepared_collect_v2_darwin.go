//go:build darwin && arm64

package resultingress

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type attachedContinuationV2 interface {
	Observation() (processsupervisor.AttachObservationV2, error)
	ExecutePreparedCollect(context.Context, processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
	ExecutePreparedInspect(context.Context, processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
	ExecutePreparedClose(context.Context, processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
}
type continuationTransportV2 func(context.Context, processsupervisor.AttachOptionsV2, func(attachedContinuationV2) error) error
type collectedTranscriptReaderV2 func(processsupervisor.CollectedTranscriptReadOptionsV2) (processsupervisor.CollectedTranscript, error)
type preparedJournalObserverV2 func(context.Context, processsupervisor.PreparedJournalOptionsV2) (processsupervisor.PreparedJournalObservationV2, error)

func productionContinuationTransportV2(ctx context.Context, options processsupervisor.AttachOptionsV2, fn func(attachedContinuationV2) error) error {
	return processsupervisor.WithAttachedV2(ctx, options, func(s *processsupervisor.AttachedSessionV2) error { return fn(s) })
}

func verifiedCollectOutcomeV2(e SupervisorCommandEvidence) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	if e.Validate() != nil || e.Command != processsupervisor.CommandCollect || e.Disposition != "ok" || e.Outcome.State != SupervisorTranscriptCollected {
		return processsupervisor.VerifiedCommandOutcomeV2{}, ErrPreparedExecutionConflict
	}
	v := e.Outcome
	report := processsupervisor.ProcessReport{State: v.MechanicsState, ObserverIdentity: v.ObserverIdentity, ObservedAt: v.ObservedAt, Process: v.Process,
		RuntimeObjectDigest: v.RuntimeObjectDigest, WorkingObjectDigest: v.WorkingObjectDigest, SourceGateRevision: v.SourceGateRevision, ExactSetDigest: v.ExactSetDigest,
		ExitCode: v.ExitCode, Signal: v.Signal, StdoutDigest: v.StdoutDigest, StderrDigest: v.StderrDigest, StdoutBytes: v.StdoutBytes, StderrBytes: v.StderrBytes, TranscriptTruncated: v.TranscriptTruncated}
	o := processsupervisor.VerifiedCommandOutcomeV2{Preparation: e.V2Preparation, JournalRequest: e.JournalRequest, PostCommand: supervisorSessionAnchorV2(e.PostCommand),
		Status: e.Disposition, ReasonCode: e.ReasonCode, ReceiptDigest: e.ReceiptDigest, ObservationDigest: e.ObservationDigest, CommandHead: e.CommandHead,
		TranscriptDigest: v.TranscriptDigest, StdoutBytes: v.StdoutBytes, StderrBytes: v.StderrBytes, Truncated: v.TranscriptTruncated, ProcessReport: &report}
	if o.Validate() != nil {
		return processsupervisor.VerifiedCommandOutcomeV2{}, ErrPreparedExecutionConflict
	}
	return o, nil
}

// Called only after the physical owner/current RB1 state has been checked by
// CollectPreparedExecution. Neither raw output bytes nor a new request enter RB1.
func (s *DurableStore) collectPreparedExecutionV2Locked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, owner ControlOwnerState,
	identity AttemptIdentity, directory *os.File, fixedPath string, transport continuationTransportV2, read collectedTranscriptReaderV2, observe preparedJournalObserverV2) (PreparedExecutionTranscript, error) {
	if transport == nil || read == nil || observe == nil || state.SupervisorStarted.Validate() != nil || state.SupervisorMechanicsAnchor.Validate() != nil ||
		state.SupervisorStarted.V2.Anchor.Generation != state.SupervisorMechanicsAnchor.Generation {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
	}
	readOutcome := func(e SupervisorCommandEvidence, fact string) (PreparedExecutionTranscript, error) {
		o, err := verifiedCollectOutcomeV2(e)
		if err != nil {
			return PreparedExecutionTranscript{}, err
		}
		transcript, err := read(processsupervisor.CollectedTranscriptReadOptionsV2{FixedMarshalPath: fixedPath, ControlDirectory: directory, Outcome: o})
		if err != nil {
			return PreparedExecutionTranscript{}, err
		}
		return PreparedExecutionTranscript{Identity: identity, OutcomeFactDigest: fact, Transcript: transcript}, nil
	}
	if checkpoint, ok := latestSuccessfulCollect(state); ok {
		return readOutcome(checkpoint.Evidence, checkpoint.FactDigest)
	}
	payload := processsupervisor.CollectPayload{ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state)}
	pending := state.SupervisorPendingIntentDigest != ""
	anchor := supervisorSessionAnchorV2(state.SupervisorMechanicsAnchor)
	var prepared processsupervisor.PreparedCommandV2
	var outcome processsupervisor.VerifiedCommandOutcomeV2
	var err error
	committed := false
	if pending {
		intent := state.SupervisorPendingIntent
		if intent.Command != processsupervisor.CommandCollect || intent.PreCommand != state.SupervisorMechanicsAnchor {
			return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
		}
		expected, err := SupervisorPreparedCommandEvidenceV2(intent)
		if err != nil {
			return PreparedExecutionTranscript{}, err
		}
		prepared, err = processsupervisor.RebuildPreparedCommandV2(expected, payload)
		if err != nil {
			return PreparedExecutionTranscript{}, err
		}
		classification, err := observe(ctx, processsupervisor.PreparedJournalOptionsV2{ControlDirectory: directory, Prepared: prepared})
		if err != nil {
			return PreparedExecutionTranscript{}, processsupervisor.ErrIntervention
		}
		switch classification.Reconciliation {
		case processsupervisor.ReconciliationUnchanged:
			if classification.Outcome != nil {
				return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
			}
		case processsupervisor.ReconciliationReceiptCommitted:
			if classification.Outcome == nil || classification.Outcome.Validate() != nil || classification.Outcome.Preparation != expected {
				return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
			}
			outcome = *classification.Outcome
			anchor = outcome.PostCommand
			committed = true
		default:
			return PreparedExecutionTranscript{}, processsupervisor.ErrIntervention
		}
	} else {
		prepared, err = processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandCollect, CommandID: fmt.Sprintf("collect-result-%d", state.SupervisorCommandSequence+1),
			Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead, CurrentAuthorityHead: state.HeadDigest, Deadline: s.authorityNow().Add(20 * time.Second)}, payload)
		if err != nil {
			return PreparedExecutionTranscript{}, err
		}
	}
	if state.ControlOwnerBindingRevision < 2 || state.ControlOwnerBindingRevision > state.Revision || state.SupervisorBoundAuthorityHead != state.ControlOwnerBindingDigest {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
	}
	authority := rebindAttachAuthorityV2(state, owner, identity, anchor)
	if committed && outcome.Status == "ok" {
		authority.ChildObservationDigest = outcome.ObservationDigest
	}
	if authority.Validate() != nil {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
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
		outcome, err = session.ExecutePreparedCollect(ctx, prepared)
		return err
	})
	if err != nil {
		return PreparedExecutionTranscript{}, err
	}
	if !called {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionConflict
	}
	evidence, err := NewSupervisorCommandEvidenceV2(outcome)
	if err != nil {
		return PreparedExecutionTranscript{}, err
	}
	_, fact, err := s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
	if err != nil {
		return PreparedExecutionTranscript{}, err
	}
	if evidence.Disposition != "ok" || evidence.Outcome.State != SupervisorTranscriptCollected {
		return PreparedExecutionTranscript{}, ErrPreparedExecutionNotCollectible
	}
	return readOutcome(evidence, fact)
}
