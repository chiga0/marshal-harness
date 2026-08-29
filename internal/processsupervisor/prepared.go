package processsupervisor

import (
	"bytes"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// PreparedCommandProjection is the producer-owned, secret-free command
// projection persisted before transport. It contains digests and held-object
// identities with paths removed, never argv, environment values, stdin, raw
// nonce or transcript bytes.
type PreparedCommandProjection struct {
	SupervisorStartedFactDigest    string `json:"supervisorStartedFactDigest,omitempty"`
	OwnerEpoch                     uint64 `json:"ownerEpoch,omitempty"`
	PreviousAuthorityHead          string `json:"previousAuthorityHead,omitempty"`
	AuthorityHead                  string `json:"authorityHead,omitempty"`
	AuthorityAbsenceProofDigest    string `json:"authorityAbsenceProofDigest,omitempty"`
	LaunchAuthorizedFactDigest     string `json:"launchAuthorizedFactDigest,omitempty"`
	LaunchMaterialsDigest          string `json:"launchMaterialsDigest,omitempty"`
	AgentLaunchSpecDigest          string `json:"agentLaunchSpecDigest,omitempty"`
	RuntimeObjectDigest            string `json:"runtimeObjectDigest,omitempty"`
	WorkingObjectDigest            string `json:"workingObjectDigest,omitempty"`
	ClosureProfileID               string `json:"closureProfileId,omitempty"`
	ArgvDigest                     string `json:"argvDigest,omitempty"`
	EnvironmentDigest              string `json:"environmentDigest,omitempty"`
	StdinDigest                    string `json:"stdinDigest,omitempty"`
	ProcessStartedFactDigest       string `json:"processStartedFactDigest,omitempty"`
	TerminalizationBarrierDigest   string `json:"terminalizationBarrierDigest,omitempty"`
	TerminalizationID              string `json:"terminalizationId,omitempty"`
	TerminalGeneration             uint64 `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest           string `json:"cleanupBindingDigest,omitempty"`
	LastObservationDigest          string `json:"lastObservationDigest,omitempty"`
	ProcessTerminalFactDigest      string `json:"processTerminalFactDigest,omitempty"`
	AllocationTerminatedFactDigest string `json:"allocationTerminatedFactDigest,omitempty"`
}

// PreparedCommandEvidence is the complete, secret-free creation-once intent
// that ResultIngress persists before DoPrepared may perform transport.
type PreparedCommandEvidence struct {
	ProtocolRevision      string                    `json:"protocolRevision"`
	SessionID             string                    `json:"sessionId"`
	Command               CommandName               `json:"command"`
	CommandID             string                    `json:"commandId"`
	Sequence              uint64                    `json:"sequence"`
	PreviousCommandDigest string                    `json:"previousCommandDigest"`
	CurrentAuthorityHead  string                    `json:"currentAuthorityHead"`
	RequestDigest         string                    `json:"requestDigest"`
	PayloadDigest         string                    `json:"payloadDigest"`
	Deadline              string                    `json:"deadline"`
	Projection            PreparedCommandProjection `json:"projection"`
	PreCommand            HandshakeAnchor           `json:"preCommand"`
	EvidenceDigest        string                    `json:"evidenceDigest"`
}

func (evidence PreparedCommandEvidence) Validate() error {
	if evidence.ProtocolRevision != ProtocolRevision || !validID(evidence.SessionID) || !validCommand(evidence.Command) || !validID(evidence.CommandID) || evidence.Sequence == 0 || evidence.Sequence > maxSafeJSONInteger || !validDigest(evidence.PreviousCommandDigest) || !validDigest(evidence.CurrentAuthorityHead) || !validDigest(evidence.RequestDigest) || !validDigest(evidence.PayloadDigest) || !validDigest(evidence.EvidenceDigest) {
		return ErrInvalid
	}
	wantDigest, err := evidence.integrityDigest()
	if err != nil || evidence.EvidenceDigest != wantDigest {
		return ErrConflict
	}
	deadline, err := parseDeadline(evidence.Deadline)
	if err != nil || deadline.Format(time.RFC3339Nano) != evidence.Deadline {
		return ErrInvalid
	}
	pre := evidence.PreCommand
	if !validID(pre.SessionID) || !validDigest(pre.SessionNonceDigest) || pre.Authority.validate() != nil || pre.OwnerEpoch == 0 || pre.OwnerEpoch > maxSafeJSONInteger || !validDigest(pre.CurrentAuthorityHead) || pre.CommandSequence > maxSafeJSONInteger || !validDigest(pre.CommandHead) || pre.JournalSequence == 0 || pre.JournalSequence > maxSafeJSONInteger || !validDigest(pre.JournalHead) || pre.UID == 0 || pre.FixedBinary.validate() != nil || pre.ControlSocket.validate() != nil || pre.ControlFiles.validate() != nil {
		return ErrInvalid
	}
	if evidence.SessionID != pre.SessionID || evidence.Sequence != pre.CommandSequence+1 || evidence.PreviousCommandDigest != pre.CommandHead || evidence.CurrentAuthorityHead != pre.CurrentAuthorityHead {
		return ErrConflict
	}
	return validatePreparedProjection(evidence.Command, evidence.Projection)
}

// integrityDigest binds the entire creation-time evidence while excluding its
// own digest field from the preimage. This closes the gap where a consumer
// could otherwise accept a separately valid projection or pre-command anchor
// that no longer belonged to the producer-created request.
func (evidence PreparedCommandEvidence) integrityDigest() (string, error) {
	evidence.EvidenceDigest = ""
	return digestValue(evidence)
}

// PreparedCommand owns the private canonical Request. Callers can persist only
// Evidence; they cannot mutate request bytes after intent creation.
type PreparedCommand struct {
	request  Request
	evidence PreparedCommandEvidence
}

func (prepared PreparedCommand) Evidence() PreparedCommandEvidence {
	return clonePreparedEvidence(prepared.evidence)
}

func clonePreparedEvidence(evidence PreparedCommandEvidence) PreparedCommandEvidence {
	return evidence
}

// PrepareCommand creates an exact request from an authenticated pre-command
// anchor. The returned evidence must be durably appended before DoPrepared.
func PrepareCommand(pre HandshakeAnchor, options CommandOptions, payload any) (PreparedCommand, error) {
	return prepareCommand(pre, options, payload, true)
}

// RebuildPreparedCommand reconstructs the private request from authoritative
// held material after restart and requires byte-identical secret-free evidence.
func RebuildPreparedCommand(expected PreparedCommandEvidence, payload any) (PreparedCommand, error) {
	if expected.Validate() != nil {
		return PreparedCommand{}, ErrInvalid
	}
	deadline, err := parseDeadline(expected.Deadline)
	if err != nil {
		return PreparedCommand{}, ErrInvalid
	}
	prepared, err := prepareCommand(expected.PreCommand, CommandOptions{Command: expected.Command, CommandID: expected.CommandID, Sequence: expected.Sequence, PreviousCommandDigest: expected.PreviousCommandDigest, CurrentAuthorityHead: expected.CurrentAuthorityHead, Deadline: deadline}, payload, true)
	if err != nil {
		return PreparedCommand{}, err
	}
	want, err := canonicalValue(expected)
	if err != nil {
		return PreparedCommand{}, ErrInvalid
	}
	got, err := canonicalValue(prepared.evidence)
	if err != nil || !bytes.Equal(want, got) {
		return PreparedCommand{}, ErrConflict
	}
	return prepared, nil
}

func prepareCommand(pre HandshakeAnchor, options CommandOptions, payload any, requireControlFiles bool) (PreparedCommand, error) {
	if options.Sequence != pre.CommandSequence+1 || options.PreviousCommandDigest != pre.CommandHead || options.CurrentAuthorityHead == "" {
		return PreparedCommand{}, ErrConflict
	}
	if requireControlFiles && pre.ControlFiles.validate() != nil {
		return PreparedCommand{}, ErrInvalid
	}
	request, err := NewRequest(pre.SessionID, options.Command, options.CommandID, options.Sequence, options.PreviousCommandDigest, options.CurrentAuthorityHead, options.Deadline, payload)
	if err != nil {
		return PreparedCommand{}, err
	}
	rawPayload, err := canonicalValue(payload)
	if err != nil || !bytes.Equal(rawPayload, request.Payload) {
		return PreparedCommand{}, ErrInvalid
	}
	projection, err := projectPreparedPayload(options.Command, payload)
	if err != nil {
		return PreparedCommand{}, err
	}
	evidence := PreparedCommandEvidence{ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, RequestDigest: request.RequestDigest, PayloadDigest: canonical.DigestBytes(rawPayload), Deadline: request.Deadline, Projection: projection, PreCommand: pre}
	evidence.EvidenceDigest, err = evidence.integrityDigest()
	if err != nil {
		return PreparedCommand{}, ErrInvalid
	}
	if requireControlFiles && evidence.Validate() != nil {
		return PreparedCommand{}, ErrInvalid
	}
	return PreparedCommand{request: request, evidence: clonePreparedEvidence(evidence)}, nil
}

func projectPreparedPayload(command CommandName, payload any) (PreparedCommandProjection, error) {
	var projection PreparedCommandProjection
	switch value := payload.(type) {
	case BindAuthorityPayload:
		if command != CommandBindAuthority {
			return projection, ErrInvalid
		}
		projection.SupervisorStartedFactDigest, projection.OwnerEpoch, projection.PreviousAuthorityHead, projection.AuthorityHead = value.SupervisorStartedFactDigest, value.OwnerEpoch, value.PreviousAuthorityHead, value.AuthorityHead
	case AbortUnboundPayload:
		if command != CommandAbortUnbound {
			return projection, ErrInvalid
		}
		projection.OwnerEpoch, projection.PreviousAuthorityHead, projection.AuthorityAbsenceProofDigest = value.OwnerEpoch, value.PreviousAuthorityHead, value.AuthorityAbsenceProofDigest
	case SpawnPayload:
		if command != CommandSpawn {
			return projection, ErrInvalid
		}
		runtime, working := value.Runtime, value.WorkingDirectory
		runtime.CanonicalPath, working.CanonicalPath = "", ""
		var err error
		projection.RuntimeObjectDigest, err = digestValue(runtime)
		if err != nil {
			return projection, err
		}
		projection.WorkingObjectDigest, err = digestValue(working)
		if err != nil {
			return projection, err
		}
		projection.SupervisorStartedFactDigest, projection.LaunchAuthorizedFactDigest = value.SupervisorStartedFactDigest, value.LaunchAuthorizedFactDigest
		projection.LaunchMaterialsDigest, projection.AgentLaunchSpecDigest, projection.ClosureProfileID = value.LaunchMaterialsDigest, value.AgentLaunchSpecDigest, value.ClosureProfileID
		projection.ArgvDigest, projection.EnvironmentDigest, projection.StdinDigest = value.ArgvDigest, value.EnvironmentDigest, value.StdinDigest
	case ResumePayload:
		if command != CommandResume {
			return projection, ErrInvalid
		}
		projection.ProcessStartedFactDigest = value.ProcessStartedFactDigest
	case CleanupPayload:
		if command != CommandInspect && command != CommandTerminate {
			return projection, ErrInvalid
		}
		projection.TerminalizationBarrierDigest, projection.TerminalizationID, projection.TerminalGeneration = value.TerminalizationBarrierDigest, value.TerminalizationID, value.TerminalGeneration
		projection.CleanupBindingDigest, projection.ProcessStartedFactDigest, projection.LastObservationDigest = value.CleanupBindingDigest, value.ProcessStartedFactDigest, value.LastObservationDigest
	case CollectPayload:
		if command != CommandCollect {
			return projection, ErrInvalid
		}
		projection.ProcessStartedFactDigest, projection.LastObservationDigest = value.ProcessStartedFactDigest, value.LastObservationDigest
	case ClosePayload:
		if command != CommandClose {
			return projection, ErrInvalid
		}
		projection.ProcessTerminalFactDigest, projection.AllocationTerminatedFactDigest, projection.CleanupBindingDigest = value.ProcessTerminalFactDigest, value.AllocationTerminatedDigest, value.CleanupBindingDigest
	default:
		return projection, ErrInvalid
	}
	if validatePreparedProjection(command, projection) != nil {
		return PreparedCommandProjection{}, ErrInvalid
	}
	return projection, nil
}

func validatePreparedProjection(command CommandName, projection PreparedCommandProjection) error {
	// Build the only allowed projection for the selected command, then compare
	// canonical bytes so any cross-command or future unreviewed field fails.
	var allowed PreparedCommandProjection
	switch command {
	case CommandBindAuthority:
		if !validDigest(projection.SupervisorStartedFactDigest) || projection.OwnerEpoch == 0 || projection.OwnerEpoch > maxSafeJSONInteger || !validDigest(projection.PreviousAuthorityHead) || !validDigest(projection.AuthorityHead) || projection.PreviousAuthorityHead == projection.AuthorityHead {
			return ErrInvalid
		}
		allowed.SupervisorStartedFactDigest, allowed.OwnerEpoch, allowed.PreviousAuthorityHead, allowed.AuthorityHead = projection.SupervisorStartedFactDigest, projection.OwnerEpoch, projection.PreviousAuthorityHead, projection.AuthorityHead
	case CommandAbortUnbound:
		if projection.OwnerEpoch == 0 || projection.OwnerEpoch > maxSafeJSONInteger || !validDigest(projection.PreviousAuthorityHead) || !validDigest(projection.AuthorityAbsenceProofDigest) {
			return ErrInvalid
		}
		allowed.OwnerEpoch, allowed.PreviousAuthorityHead, allowed.AuthorityAbsenceProofDigest = projection.OwnerEpoch, projection.PreviousAuthorityHead, projection.AuthorityAbsenceProofDigest
	case CommandSpawn:
		for _, digest := range []string{projection.SupervisorStartedFactDigest, projection.LaunchAuthorizedFactDigest, projection.LaunchMaterialsDigest, projection.AgentLaunchSpecDigest, projection.RuntimeObjectDigest, projection.WorkingObjectDigest, projection.ArgvDigest, projection.EnvironmentDigest, projection.StdinDigest} {
			if !validDigest(digest) {
				return ErrInvalid
			}
		}
		if !validID(projection.ClosureProfileID) {
			return ErrInvalid
		}
		allowed.SupervisorStartedFactDigest, allowed.LaunchAuthorizedFactDigest = projection.SupervisorStartedFactDigest, projection.LaunchAuthorizedFactDigest
		allowed.LaunchMaterialsDigest, allowed.AgentLaunchSpecDigest, allowed.RuntimeObjectDigest, allowed.WorkingObjectDigest, allowed.ClosureProfileID = projection.LaunchMaterialsDigest, projection.AgentLaunchSpecDigest, projection.RuntimeObjectDigest, projection.WorkingObjectDigest, projection.ClosureProfileID
		allowed.ArgvDigest, allowed.EnvironmentDigest, allowed.StdinDigest = projection.ArgvDigest, projection.EnvironmentDigest, projection.StdinDigest
	case CommandResume:
		if !validDigest(projection.ProcessStartedFactDigest) {
			return ErrInvalid
		}
		allowed.ProcessStartedFactDigest = projection.ProcessStartedFactDigest
	case CommandInspect, CommandTerminate:
		if !validDigest(projection.ProcessStartedFactDigest) || !validDigest(projection.TerminalizationBarrierDigest) || !validID(projection.TerminalizationID) || projection.TerminalGeneration == 0 || projection.TerminalGeneration > maxSafeJSONInteger || !validDigest(projection.CleanupBindingDigest) || !validDigest(projection.LastObservationDigest) {
			return ErrInvalid
		}
		allowed.ProcessStartedFactDigest, allowed.TerminalizationBarrierDigest, allowed.TerminalizationID, allowed.TerminalGeneration = projection.ProcessStartedFactDigest, projection.TerminalizationBarrierDigest, projection.TerminalizationID, projection.TerminalGeneration
		allowed.CleanupBindingDigest, allowed.LastObservationDigest = projection.CleanupBindingDigest, projection.LastObservationDigest
	case CommandCollect:
		if !validDigest(projection.ProcessStartedFactDigest) || !validDigest(projection.LastObservationDigest) {
			return ErrInvalid
		}
		allowed.ProcessStartedFactDigest, allowed.LastObservationDigest = projection.ProcessStartedFactDigest, projection.LastObservationDigest
	case CommandClose:
		if !validDigest(projection.ProcessTerminalFactDigest) || !validDigest(projection.AllocationTerminatedFactDigest) || !validDigest(projection.CleanupBindingDigest) {
			return ErrInvalid
		}
		allowed.ProcessTerminalFactDigest, allowed.AllocationTerminatedFactDigest, allowed.CleanupBindingDigest = projection.ProcessTerminalFactDigest, projection.AllocationTerminatedFactDigest, projection.CleanupBindingDigest
	default:
		return ErrInvalid
	}
	original, err := canonicalValue(projection)
	want, wantErr := canonicalValue(allowed)
	if err != nil || wantErr != nil || !bytes.Equal(original, want) {
		return ErrInvalid
	}
	return nil
}

// ValidatePreparedCommandProjection lets a durable consumer replay the
// producer-owned, secret-free projection without re-decoding raw payload.
func ValidatePreparedCommandProjection(command CommandName, projection PreparedCommandProjection) error {
	return validatePreparedProjection(command, projection)
}
