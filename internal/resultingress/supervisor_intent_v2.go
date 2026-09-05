package resultingress

import "github.com/chiga0/marshal-harness/internal/processsupervisor"

// NewSupervisorCommandIntentV2 retains the exact producer evidence digest,
// including the complete generation and A0. No request payload enters RB1.
func NewSupervisorCommandIntentV2(e processsupervisor.PreparedCommandEvidenceV2) (SupervisorCommandIntent, error) {
	if e.Validate() != nil {
		return SupervisorCommandIntent{}, ErrAttemptAuthorityConflict
	}
	intent := SupervisorCommandIntent{ProtocolRevision: e.PreCommand.Generation.ProtocolRevision,
		PreparedEvidenceDigest: e.EvidenceDigest, SessionID: e.PreCommand.Binding.SessionID,
		Command: e.Command, CommandID: e.CommandID, Sequence: e.Sequence, PreviousCommandHead: e.PreviousCommandDigest,
		CurrentAuthorityHead: e.CurrentAuthorityHead, Deadline: e.Deadline, RequestDigest: e.RequestDigest,
		PayloadDigest: e.PayloadDigest, Rebuild: e.Projection, PreCommand: projectSupervisorMechanicsAnchorV2(e.PreCommand)}
	if intent.Validate() != nil {
		return SupervisorCommandIntent{}, ErrAttemptAuthorityConflict
	}
	return intent, nil
}

// SupervisorPreparedCommandEvidenceV2 restores only the typed redacted intent.
// A caller must still use RebuildPreparedCommandV2 with the real held payload
// before transport; a persisted hash is never an executable request.
func SupervisorPreparedCommandEvidenceV2(intent SupervisorCommandIntent) (processsupervisor.PreparedCommandEvidenceV2, error) {
	if intent.ProtocolRevision != processsupervisor.DormantV2ProtocolContract().ProtocolRevision || intent.SessionID != intent.PreCommand.SessionID {
		return processsupervisor.PreparedCommandEvidenceV2{}, ErrAttemptAuthorityConflict
	}
	e := processsupervisor.PreparedCommandEvidenceV2{PreCommand: supervisorSessionAnchorV2(intent.PreCommand),
		Command: intent.Command, CommandID: intent.CommandID, Sequence: intent.Sequence, PreviousCommandDigest: intent.PreviousCommandHead,
		CurrentAuthorityHead: intent.CurrentAuthorityHead, RequestDigest: intent.RequestDigest, PayloadDigest: intent.PayloadDigest,
		Deadline: intent.Deadline, Projection: intent.Rebuild, EvidenceDigest: intent.PreparedEvidenceDigest}
	if e.Validate() != nil {
		return processsupervisor.PreparedCommandEvidenceV2{}, ErrAttemptAuthorityConflict
	}
	return e, nil
}

func supervisorIntentRecoveryRevision(intent SupervisorCommandIntent) string {
	if intent.Validate() != nil {
		return ""
	}
	if intent.ProtocolRevision == processsupervisor.DormantV2ProtocolContract().ProtocolRevision {
		return intent.PreCommand.Generation.CommandRecoveryRevision
	}
	return supervisorCommandProtocolRevision
}

func validSupervisorRecoveryFactGeneration(fact supervisorCommandFact) bool {
	if fact.FactType == supervisorCommandIntentFactType {
		revision := supervisorIntentRecoveryRevision(fact.Intent)
		return revision != "" && fact.ProtocolRevision == revision
	}
	// Outcome/Attach producers remain closed to v2 until their complete
	// response/reconnect projections are wired. Never mark a v1 body as v2.
	return fact.ProtocolRevision == supervisorCommandProtocolRevision
}

// These are generation-neutral business identifiers only. Validate the
// originating started object before selecting them; never synthesize a v1
// handshake from a v2 session for protocol or mechanics consumers.
func supervisorStartedCommandBinding(started ProcessSupervisorStarted) (sessionID, initialHead, protocol string) {
	if started.V2 != (SupervisorStartedV2{}) {
		if started.Validate() != nil {
			return "", "", ""
		}
		h := started.V2.Handshake
		return h.SessionID, h.CurrentAuthorityHead, h.ProtocolRevision
	}
	return started.Handshake.SessionID, started.Handshake.CurrentAuthorityHead, processsupervisor.ProtocolRevision
}
