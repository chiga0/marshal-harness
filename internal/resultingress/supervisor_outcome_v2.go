package resultingress

import "github.com/chiga0/marshal-harness/internal/processsupervisor"

// Business references retain the original session identity across owner
// rebinds. The owner/head may advance, but generation, nonce and held objects
// cannot be replaced by a self-consistent receipt from a different session.
func supervisorOutcomeMatchesStartedV2(started ProcessSupervisorStarted, evidence SupervisorCommandEvidence) bool {
	if started.V2 == (SupervisorStartedV2{}) || started.Validate() != nil || evidence.Validate() != nil {
		return false
	}
	a, b := started.V2.Anchor, evidence.V2Preparation.PreCommand
	return a.Generation == b.Generation && sameStableControlDirectoryIdentity(a.ControlDirectory, b.ControlDirectory) &&
		a.Binding.SessionID == b.Binding.SessionID && a.Binding.SessionNonceDigest == b.Binding.SessionNonceDigest &&
		a.Binding.Authority == b.Binding.Authority && a.Binding.FixedBinary == b.Binding.FixedBinary &&
		a.Binding.ControlSocket == b.Binding.ControlSocket && a.Binding.ControlFiles == b.Binding.ControlFiles
}

func NewSupervisorCommandEvidenceV2(outcome processsupervisor.VerifiedCommandOutcomeV2) (SupervisorCommandEvidence, error) {
	if outcome.Validate() != nil {
		return SupervisorCommandEvidence{}, ErrAttemptAuthorityConflict
	}
	p := outcome.Preparation
	e := SupervisorCommandEvidence{ProtocolRevision: p.PreCommand.Generation.ProtocolRevision, V2Preparation: p, JournalRequest: outcome.JournalRequest,
		SessionID: p.PreCommand.Binding.SessionID, Command: p.Command, CommandID: p.CommandID, Sequence: p.Sequence, PreviousCommandHead: p.PreviousCommandDigest,
		CurrentAuthorityHead: p.CurrentAuthorityHead, RequestDigest: p.RequestDigest, ReceiptDigest: outcome.ReceiptDigest, ObservationDigest: outcome.ObservationDigest,
		CommandHead: outcome.CommandHead, Disposition: outcome.Status, ReasonCode: outcome.ReasonCode,
		PreCommand: projectSupervisorMechanicsAnchorV2(p.PreCommand), PostCommand: projectSupervisorMechanicsAnchorV2(outcome.PostCommand)}
	if e.Command == processsupervisor.CommandBindAuthority && e.Disposition == "ok" {
		e.BoundAuthorityHead = p.Projection.AuthorityHead
	}
	if outcome.ProcessReport != nil {
		var err error
		e.Outcome, err = projectSupervisorProcessOutcome(p.Command, outcome.ReasonCode, outcome.TranscriptDigest, *outcome.ProcessReport)
		if err != nil {
			return SupervisorCommandEvidence{}, err
		}
	}
	if e.Validate() != nil {
		return SupervisorCommandEvidence{}, ErrAttemptAuthorityConflict
	}
	return e, nil
}

func validateSupervisorCommandEvidenceV2(e SupervisorCommandEvidence) error {
	p := e.V2Preparation
	if p.Validate() != nil || e.ProtocolRevision != p.PreCommand.Generation.ProtocolRevision || e.SessionID != p.PreCommand.Binding.SessionID ||
		e.Command != p.Command || e.CommandID != p.CommandID || e.Sequence != p.Sequence || e.PreviousCommandHead != p.PreviousCommandDigest ||
		e.CurrentAuthorityHead != p.CurrentAuthorityHead || e.RequestDigest != p.RequestDigest || e.PreCommand != projectSupervisorMechanicsAnchorV2(p.PreCommand) {
		return ErrAttemptAuthorityConflict
	}
	o := processsupervisor.VerifiedCommandOutcomeV2{Preparation: p, JournalRequest: e.JournalRequest, PostCommand: supervisorSessionAnchorV2(e.PostCommand),
		Status: e.Disposition, ReasonCode: e.ReasonCode, ReceiptDigest: e.ReceiptDigest, ObservationDigest: e.ObservationDigest, CommandHead: e.CommandHead}
	if e.Disposition == "ok" && e.Command != processsupervisor.CommandBindAuthority && e.Command != processsupervisor.CommandAbortUnbound {
		v := e.Outcome
		if v.Validate() != nil {
			return ErrAttemptAuthorityConflict
		}
		report := processsupervisor.ProcessReport{State: v.MechanicsState, Process: v.Process, ObserverIdentity: v.ObserverIdentity, ObservedAt: v.ObservedAt,
			RuntimeObjectDigest: v.RuntimeObjectDigest, WorkingObjectDigest: v.WorkingObjectDigest, SourceGateRevision: v.SourceGateRevision, ExactSetDigest: v.ExactSetDigest,
			ExitCode: v.ExitCode, Signal: v.Signal, StdoutDigest: v.StdoutDigest, StderrDigest: v.StderrDigest, StdoutBytes: v.StdoutBytes, StderrBytes: v.StderrBytes, TranscriptTruncated: v.TranscriptTruncated}
		o.ProcessReport = &report
		if e.Command == processsupervisor.CommandCollect {
			o.TranscriptDigest, o.StdoutBytes, o.StderrBytes, o.Truncated = v.TranscriptDigest, v.StdoutBytes, v.StderrBytes, v.TranscriptTruncated
		}
		projected, err := projectSupervisorProcessOutcome(e.Command, e.ReasonCode, o.TranscriptDigest, report)
		if err != nil || projected != v {
			return ErrAttemptAuthorityConflict
		}
	} else if e.Outcome != (SupervisorProcessOutcome{}) {
		return ErrAttemptAuthorityConflict
	}
	bound := ""
	if e.Disposition == "ok" && e.Command == processsupervisor.CommandBindAuthority {
		bound = p.Projection.AuthorityHead
	}
	if e.BoundAuthorityHead != bound || o.Validate() != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func supervisorOutcomeRecoveryRevision(e SupervisorCommandEvidence) string {
	if e.Validate() != nil {
		return ""
	}
	if e.ProtocolRevision == processsupervisor.DormantV2ProtocolContract().ProtocolRevision {
		return e.PreCommand.Generation.CommandRecoveryRevision
	}
	return supervisorCommandProtocolRevision
}

// Restore a validated durable v2 outcome without passing through a v1 wire or
// recovery object. It is still evidence, not authority to execute a command.
func verifiedSupervisorOutcomeV2(e SupervisorCommandEvidence) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	if e.Validate() != nil || e.V2Preparation.Validate() != nil {
		return processsupervisor.VerifiedCommandOutcomeV2{}, ErrAttemptAuthorityConflict
	}
	o := processsupervisor.VerifiedCommandOutcomeV2{Preparation: e.V2Preparation, JournalRequest: e.JournalRequest, PostCommand: supervisorSessionAnchorV2(e.PostCommand),
		Status: e.Disposition, ReasonCode: e.ReasonCode, ReceiptDigest: e.ReceiptDigest, ObservationDigest: e.ObservationDigest, CommandHead: e.CommandHead}
	if e.Outcome != (SupervisorProcessOutcome{}) {
		v := e.Outcome
		o.ProcessReport = &processsupervisor.ProcessReport{State: v.MechanicsState, ObserverIdentity: v.ObserverIdentity, ObservedAt: v.ObservedAt, Process: v.Process,
			RuntimeObjectDigest: v.RuntimeObjectDigest, WorkingObjectDigest: v.WorkingObjectDigest, SourceGateRevision: v.SourceGateRevision, ExactSetDigest: v.ExactSetDigest,
			ExitCode: v.ExitCode, Signal: v.Signal, StdoutDigest: v.StdoutDigest, StderrDigest: v.StderrDigest, StdoutBytes: v.StdoutBytes, StderrBytes: v.StderrBytes, TranscriptTruncated: v.TranscriptTruncated}
		if e.Command == processsupervisor.CommandCollect {
			o.TranscriptDigest, o.StdoutBytes, o.StderrBytes, o.Truncated = v.TranscriptDigest, v.StdoutBytes, v.StderrBytes, v.TranscriptTruncated
		}
	}
	if o.Validate() != nil {
		return processsupervisor.VerifiedCommandOutcomeV2{}, ErrAttemptAuthorityConflict
	}
	return o, nil
}

func NewProcessSupervisorClosedFromRecoveryV2(a ProcessSupervisorCloseAuthority, r processsupervisor.CommittedCloseRecoveryEvidenceV2) (ProcessSupervisorClosed, error) {
	if r.Validate() != nil {
		return ProcessSupervisorClosed{}, ErrAttemptAuthorityConflict
	}
	digest, err := canonicalDigest(r.Absence)
	if err != nil {
		return ProcessSupervisorClosed{}, err
	}
	o := r.Outcome
	closed := ProcessSupervisorClosed{ProtocolRevision: o.Preparation.PreCommand.Generation.ProtocolRevision, SessionID: o.Preparation.PreCommand.Binding.SessionID, Owner: a.Owner,
		SupervisorStartedFactDigest: a.SupervisorStartedFactDigest, TerminalizationID: a.TerminalizationID, CleanupBindingDigest: a.CleanupBindingDigest,
		ProcessTerminalFactDigest: a.ProcessTerminalFactDigest, AllocationTerminatedFactDigest: a.AllocationTerminatedFactDigest,
		CloseIntentDigest: o.Preparation.RequestDigest, CloseReceiptDigest: o.ReceiptDigest, CloseObservationDigest: o.ObservationDigest, FinalCommandHead: o.CommandHead,
		SupervisorAbsenceObservationDigest: digest, SupervisorProcess: r.Absence.Expected, ObserverIdentity: r.Absence.Observer.Binary.SelfProfile, ObservedAt: r.Absence.ObservedAt, AuthenticatedSupervisorAbsence: r.Absence}
	if closed.Validate() != nil {
		return ProcessSupervisorClosed{}, ErrAttemptAuthorityConflict
	}
	return closed, nil
}
