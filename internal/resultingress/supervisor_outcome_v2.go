package resultingress

import "github.com/chiga0/marshal-harness/internal/processsupervisor"

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
