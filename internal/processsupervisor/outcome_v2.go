package processsupervisor

import (
	"encoding/json"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Validate replays the closed, secret-free v2 response binding. JournalRequest
// contains only the canonical redacted journal projection, whose digest was
// frozen in Preparation before transport. Raw argv/env/stdin are never needed
// to check the receipt or either journal record after a Core restart.
func (o VerifiedCommandOutcomeV2) Validate() error {
	p := o.Preparation
	if p.Validate() != nil || len(o.JournalRequest) == 0 || len(o.JournalRequest) > MaxJournalPayload || canonical.DigestBytes([]byte(o.JournalRequest)) != p.JournalRequestDigest {
		return ErrConflict
	}
	var request requestProjection
	if strictCanonicalDecode([]byte(o.JournalRequest), &request) != nil || validateProjection(request) != nil ||
		request.Command != p.Command || request.CommandID != p.CommandID || request.Sequence != p.Sequence || request.RequestDigest != p.RequestDigest ||
		request.PreviousCommandDigest != p.PreviousCommandDigest || request.CurrentAuthorityHead != p.CurrentAuthorityHead || request.Deadline != p.Deadline {
		return ErrConflict
	}
	if !preparedJournalProjectionMatchesV2(p, request) {
		return ErrConflict
	}
	result := MechanicsResult{Disposition: o.Status, ReasonCode: o.ReasonCode, ObservationDigest: o.ObservationDigest,
		TranscriptDigest: o.TranscriptDigest, StdoutBytes: o.StdoutBytes, StderrBytes: o.StderrBytes, Truncated: o.Truncated, Payload: json.RawMessage("{}")}
	if o.Status == "ok" && p.Command != CommandBindAuthority && p.Command != CommandAbortUnbound {
		if o.ProcessReport == nil || ValidateDormantV2ProcessReport(*o.ProcessReport) != nil {
			return ErrConflict
		}
		raw, err := canonicalValue(*o.ProcessReport)
		if err != nil {
			return err
		}
		result.Payload = raw
	} else if o.ProcessReport != nil {
		return ErrConflict
	}
	raw, err := canonicalValue(result)
	if err != nil {
		return err
	}
	response := responseV2{SchemaVersion: responseSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: p.PreCommand.Binding.SessionID, Command: p.Command, CommandID: p.CommandID, Sequence: p.Sequence, RequestDigest: p.RequestDigest,
		Status: o.Status, ReasonCode: o.ReasonCode, ReceiptDigest: o.ReceiptDigest, ObservationDigest: o.ObservationDigest, CommandHead: o.CommandHead, Payload: raw}
	_, journalHead, err := expectedProjectedJournalHeadsV2(commandBaseV2(p.PreCommand), p.PreCommand.Binding.JournalSequence, p.PreCommand.Binding.JournalHead, request, &response)
	if err != nil {
		return err
	}
	want := p.PreCommand
	want.Binding.CommandSequence, want.Binding.CommandHead = p.Sequence, o.CommandHead
	want.Binding.JournalSequence, want.Binding.JournalHead = p.PreCommand.Binding.JournalSequence+2, journalHead
	want.Binding.CurrentAuthorityHead = commandPostAuthorityHeadV2(p.PreCommand.Binding.CurrentAuthorityHead, request, response)
	if want.Validate() != nil || want != o.PostCommand {
		return ErrConflict
	}
	return nil
}

func preparedJournalProjectionMatchesV2(p PreparedCommandEvidenceV2, actual requestProjection) bool {
	v := p.Projection
	want := requestProjection{Command: p.Command, CommandID: p.CommandID, Sequence: p.Sequence, RequestDigest: p.RequestDigest,
		PreviousCommandDigest: p.PreviousCommandDigest, CurrentAuthorityHead: p.CurrentAuthorityHead, Deadline: p.Deadline}
	switch p.Command {
	case CommandBindAuthority:
		want.NextAuthorityHead, want.SupervisorStartedFactDigest = v.AuthorityHead, v.SupervisorStartedFactDigest
	case CommandAbortUnbound:
		want.AuthorityAbsenceProofDigest = v.AuthorityAbsenceProofDigest
	case CommandSpawn:
		want.LaunchMaterialsDigest, want.AgentLaunchSpecDigest, want.SourceGateRevision, want.ClosureProfileID = v.LaunchMaterialsDigest, v.AgentLaunchSpecDigest, v.SourceGateRevision, v.ClosureProfileID
		want.ArgvDigest, want.EnvironmentDigest, want.StdinDigest = v.ArgvDigest, v.EnvironmentDigest, v.StdinDigest
		// Keys (never values) are part of the separately frozen journal digest.
		want.EnvironmentKeys = actual.EnvironmentKeys
	case CommandResume:
		want.ProcessStartedFactDigest = v.ProcessStartedFactDigest
	case CommandInspect, CommandTerminate:
		want.ProcessStartedFactDigest, want.TerminalizationBarrierDigest, want.TerminalizationID = v.ProcessStartedFactDigest, v.TerminalizationBarrierDigest, v.TerminalizationID
		want.TerminalGeneration, want.CleanupBindingDigest, want.LastObservationDigest = v.TerminalGeneration, v.CleanupBindingDigest, v.LastObservationDigest
	case CommandCollect:
		want.ProcessStartedFactDigest, want.LastObservationDigest = v.ProcessStartedFactDigest, v.LastObservationDigest
	case CommandClose:
		want.ProcessTerminalFactDigest, want.AllocationTerminatedDigest, want.CleanupBindingDigest = v.ProcessTerminalFactDigest, v.AllocationTerminatedFactDigest, v.CleanupBindingDigest
	default:
		return false
	}
	return equalProjection(want, actual)
}
