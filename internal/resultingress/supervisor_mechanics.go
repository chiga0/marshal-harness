package resultingress

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

var supervisorEvidenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)

// SupervisorMechanicsAnchor is the complete secret-free client anchor around
// one authenticated command. It includes the mechanics journal position;
// command sequence/head alone cannot distinguish a lost receipt from a
// journal intent that never closed.
type SupervisorMechanicsAnchor struct {
	Generation           processsupervisor.ProtocolGenerationContract `json:"generation,omitempty,omitzero"`
	ControlDirectory     processsupervisor.ControlDirectoryIdentity   `json:"controlDirectory,omitempty,omitzero"`
	SessionID            string                                       `json:"sessionId"`
	SessionNonceDigest   string                                       `json:"sessionNonceDigest"`
	Authority            processsupervisor.AuthorityTuple             `json:"authority"`
	OwnerEpoch           uint64                                       `json:"ownerEpoch"`
	CurrentAuthorityHead string                                       `json:"currentAuthorityHead"`
	CommandSequence      uint64                                       `json:"commandSequence"`
	CommandHead          string                                       `json:"commandHead"`
	JournalSequence      uint64                                       `json:"journalSequence"`
	JournalHead          string                                       `json:"journalHead"`
	UID                  uint32                                       `json:"uid"`
	GID                  uint32                                       `json:"gid"`
	FixedBinary          processsupervisor.BinaryIdentity             `json:"fixedBinary"`
	ControlSocket        processsupervisor.ControlSocketIdentity      `json:"controlSocket"`
	ControlFiles         processsupervisor.SessionControlFiles        `json:"controlFiles,omitempty,omitzero"`
}

func supervisorHandshakeAnchor(anchor SupervisorMechanicsAnchor) processsupervisor.HandshakeAnchor {
	if anchor.Generation != (processsupervisor.ProtocolGenerationContract{}) || anchor.ControlDirectory != (processsupervisor.ControlDirectoryIdentity{}) {
		return processsupervisor.HandshakeAnchor{}
	}
	return supervisorObjectBinding(anchor)
}

// This is only the generation-neutral object tuple, never a wire decoder or
// authority projection. Callers must preserve Generation and ControlDirectory.
func supervisorObjectBinding(anchor SupervisorMechanicsAnchor) processsupervisor.HandshakeAnchor {
	return processsupervisor.HandshakeAnchor{SessionID: anchor.SessionID, SessionNonceDigest: anchor.SessionNonceDigest, Authority: anchor.Authority, OwnerEpoch: anchor.OwnerEpoch, CurrentAuthorityHead: anchor.CurrentAuthorityHead, CommandSequence: anchor.CommandSequence, CommandHead: anchor.CommandHead, JournalSequence: anchor.JournalSequence, JournalHead: anchor.JournalHead, UID: anchor.UID, GID: anchor.GID, FixedBinary: anchor.FixedBinary, ControlSocket: anchor.ControlSocket, ControlFiles: anchor.ControlFiles}
}

func projectSupervisorMechanicsAnchor(anchor processsupervisor.HandshakeAnchor) SupervisorMechanicsAnchor {
	return SupervisorMechanicsAnchor{SessionID: anchor.SessionID, SessionNonceDigest: anchor.SessionNonceDigest, Authority: anchor.Authority, OwnerEpoch: anchor.OwnerEpoch, CurrentAuthorityHead: anchor.CurrentAuthorityHead, CommandSequence: anchor.CommandSequence, CommandHead: anchor.CommandHead, JournalSequence: anchor.JournalSequence, JournalHead: anchor.JournalHead, UID: anchor.UID, GID: anchor.GID, FixedBinary: anchor.FixedBinary, ControlSocket: anchor.ControlSocket, ControlFiles: anchor.ControlFiles}
}

func (anchor SupervisorMechanicsAnchor) Validate() error {
	if anchor.Generation != (processsupervisor.ProtocolGenerationContract{}) {
		if supervisorSessionAnchorV2(anchor).Validate() != nil {
			return ErrAttemptAuthorityConflict
		}
		return nil
	}
	if anchor.ControlDirectory != (processsupervisor.ControlDirectoryIdentity{}) {
		return ErrAttemptAuthorityConflict
	}
	if !supervisorEvidenceID.MatchString(anchor.SessionID) || requireDigest("sessionNonceDigest", anchor.SessionNonceDigest) != nil || validateSupervisorAuthorityTuple(anchor.Authority) != nil || anchor.OwnerEpoch == 0 || anchor.OwnerEpoch > maxExactJSONInteger || requireDigest("currentAuthorityHead", anchor.CurrentAuthorityHead) != nil || anchor.CommandSequence > maxExactJSONInteger || requireDigest("commandHead", anchor.CommandHead) != nil || anchor.JournalSequence == 0 || anchor.JournalSequence > maxExactJSONInteger || requireDigest("journalHead", anchor.JournalHead) != nil || anchor.UID == 0 || validateFixedMarshalBinaryIdentity(anchor.FixedBinary) != nil || validateControlSocketIdentity(anchor.ControlSocket) != nil || anchor.ControlFiles != (processsupervisor.SessionControlFiles{}) && processsupervisor.ValidateSessionControlFiles(anchor.ControlFiles) != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

// SupervisorReconnectEvidence is the secret-free projection of an already
// authenticated Client reconnect. It is the only authority allowed to move a
// persisted mechanics anchor to a new owner epoch / Attempt head.
type SupervisorReconnectEvidence struct {
	Reconciliation  processsupervisor.ReconciliationState   `json:"reconciliation"`
	Previous        SupervisorMechanicsAnchor               `json:"previous"`
	Current         SupervisorMechanicsAnchor               `json:"current"`
	Pending         processsupervisor.PendingReplayEvidence `json:"pending,omitempty,omitzero"`
	MechanicsLocked bool                                    `json:"mechanicsLocked,omitempty"`
}

func newSupervisorReconnectEvidence(recovery processsupervisor.SessionRecoveryEvidence) (SupervisorReconnectEvidence, error) {
	evidence := SupervisorReconnectEvidence{Reconciliation: recovery.Reconciliation, Previous: projectSupervisorMechanicsAnchor(recovery.Previous), Current: projectSupervisorMechanicsAnchor(recovery.Current), MechanicsLocked: recovery.MechanicsLocked}
	if recovery.Pending != nil {
		evidence.Pending = *recovery.Pending
	}
	if err := evidence.Validate(); err != nil {
		return SupervisorReconnectEvidence{}, err
	}
	return evidence, nil
}

func (evidence SupervisorReconnectEvidence) Validate() error {
	previous, current := evidence.Previous, evidence.Current
	if previous.Generation != (processsupervisor.ProtocolGenerationContract{}) || current.Generation != (processsupervisor.ProtocolGenerationContract{}) {
		return ErrAttemptAuthorityConflict
	}
	if previous.Validate() != nil || current.Validate() != nil || previous.SessionID != current.SessionID || previous.SessionNonceDigest != current.SessionNonceDigest || previous.Authority != current.Authority || previous.UID != current.UID || previous.GID != current.GID || previous.FixedBinary != current.FixedBinary || previous.ControlSocket != current.ControlSocket || previous.ControlFiles != current.ControlFiles || current.OwnerEpoch <= previous.OwnerEpoch || current.CurrentAuthorityHead == previous.CurrentAuthorityHead {
		return fmt.Errorf("%w: reconnect mechanics identity mismatch", ErrAttemptAuthorityConflict)
	}
	pending := evidence.Pending
	hasPending := pending != (processsupervisor.PendingReplayEvidence{})
	if hasPending {
		deadline, err := time.Parse(time.RFC3339Nano, pending.Deadline)
		if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != pending.Deadline || pending.ProtocolRevision != processsupervisor.ProtocolRevision || pending.SessionID != previous.SessionID || !validSupervisorCommand(pending.Command) || !supervisorEvidenceID.MatchString(pending.CommandID) || pending.Sequence != previous.CommandSequence+1 || pending.PreviousCommandDigest != previous.CommandHead || requireDigest("pendingCurrentAuthorityHead", pending.CurrentAuthorityHead) != nil || requireDigest("pendingRequestDigest", pending.RequestDigest) != nil {
			return fmt.Errorf("%w: invalid reconnect pending projection", ErrAttemptAuthorityConflict)
		}
	}
	switch evidence.Reconciliation {
	case processsupervisor.ReconciliationUnchanged:
		if evidence.MechanicsLocked {
			return ErrAttemptAuthorityConflict
		}
		if !hasPending {
			if current.CommandSequence != previous.CommandSequence || current.CommandHead != previous.CommandHead || current.JournalSequence != previous.JournalSequence || current.JournalHead != previous.JournalHead {
				return ErrAttemptAuthorityConflict
			}
		} else if current.CommandSequence != pending.Sequence || current.CommandHead == previous.CommandHead || current.JournalSequence != previous.JournalSequence+2 || current.JournalHead == previous.JournalHead {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.ReconciliationIntentPending:
		if !hasPending || !evidence.MechanicsLocked || current.CommandSequence != previous.CommandSequence || current.CommandHead != previous.CommandHead || current.JournalSequence != previous.JournalSequence+1 || current.JournalHead == previous.JournalHead {
			return ErrAttemptAuthorityConflict
		}
	case processsupervisor.ReconciliationReceiptCommitted:
		if !hasPending || evidence.MechanicsLocked || current.CommandSequence != pending.Sequence || current.CommandHead == previous.CommandHead || current.JournalSequence != previous.JournalSequence+2 || current.JournalHead == previous.JournalHead {
			return ErrAttemptAuthorityConflict
		}
	default:
		return ErrAttemptAuthorityConflict
	}
	return nil
}

// SupervisorCommandEvidence is the secret-free, durable projection of one
// response already authenticated and validated by the process-supervisor
// client. It intentionally stores neither request payload nor response
// payload: argv, environment values, stdin and transcript bytes never enter
// the RB1 authority ledger.
type SupervisorCommandEvidence struct {
	ProtocolRevision     string                        `json:"protocolRevision"`
	SessionID            string                        `json:"sessionId"`
	Command              processsupervisor.CommandName `json:"command"`
	CommandID            string                        `json:"commandId"`
	Sequence             uint64                        `json:"sequence"`
	PreviousCommandHead  string                        `json:"previousCommandHead"`
	CurrentAuthorityHead string                        `json:"currentAuthorityHead"`
	RequestDigest        string                        `json:"requestDigest"`
	ReceiptDigest        string                        `json:"receiptDigest"`
	ObservationDigest    string                        `json:"observationDigest"`
	CommandHead          string                        `json:"commandHead"`
	Disposition          string                        `json:"disposition"`
	ReasonCode           string                        `json:"reasonCode"`
	BoundAuthorityHead   string                        `json:"boundAuthorityHead,omitempty"`
	Outcome              SupervisorProcessOutcome      `json:"outcome,omitempty,omitzero"`
	PreCommand           SupervisorMechanicsAnchor     `json:"preCommand,omitempty,omitzero"`
	PostCommand          SupervisorMechanicsAnchor     `json:"postCommand,omitempty,omitzero"`
}

// SupervisorProcessOutcome is the Core-owned typed projection of mechanics
// output. The process-supervisor client must derive it from an authenticated,
// closed response; ResultIngress never decodes the supervisor's raw payload.
type SupervisorProcessOutcome struct {
	State               SupervisorProcessState            `json:"state"`
	MechanicsState      string                            `json:"mechanicsState"`
	Process             processsupervisor.ProcessIdentity `json:"process,omitempty,omitzero"`
	ObserverIdentity    string                            `json:"observerIdentity,omitempty"`
	ObservedAt          string                            `json:"observedAt,omitempty"`
	RuntimeObjectDigest string                            `json:"runtimeObjectDigest,omitempty"`
	WorkingObjectDigest string                            `json:"workingObjectDigest,omitempty"`
	SourceGateRevision  string                            `json:"sourceGateRevision,omitempty"`
	ExactSetDigest      string                            `json:"exactSetDigest,omitempty"`
	ExitCode            int                               `json:"exitCode,omitempty"`
	Signal              string                            `json:"signal,omitempty"`
	StdoutDigest        string                            `json:"stdoutDigest,omitempty"`
	StderrDigest        string                            `json:"stderrDigest,omitempty"`
	TranscriptDigest    string                            `json:"transcriptDigest,omitempty"`
	StdoutBytes         uint64                            `json:"stdoutBytes,omitempty"`
	StderrBytes         uint64                            `json:"stderrBytes,omitempty"`
	TranscriptTruncated bool                              `json:"transcriptTruncated,omitempty"`
}

// supervisorMechanicsReport mirrors the secret-free process report carried in
// MechanicsResult.Payload. Keeping this private prevents ResultIngress from
// becoming another wire protocol owner while still letting replay recompute
// observationDigest and receiptDigest from the durable projection.
type supervisorMechanicsReport struct {
	State               string                            `json:"state"`
	ObserverIdentity    string                            `json:"observerIdentity"`
	ObservedAt          string                            `json:"observedAt"`
	Process             processsupervisor.ProcessIdentity `json:"process"`
	RuntimeObjectDigest string                            `json:"runtimeObjectDigest"`
	WorkingObjectDigest string                            `json:"workingObjectDigest"`
	SourceGateRevision  string                            `json:"sourceGateRevision,omitempty"`
	ExactSetDigest      string                            `json:"exactSetDigest,omitempty"`
	ExitCode            int                               `json:"exitCode,omitempty"`
	Signal              string                            `json:"signal,omitempty"`
	StdoutDigest        string                            `json:"stdoutDigest,omitempty"`
	StderrDigest        string                            `json:"stderrDigest,omitempty"`
	StdoutBytes         uint64                            `json:"stdoutBytes,omitempty"`
	StderrBytes         uint64                            `json:"stderrBytes,omitempty"`
	TranscriptTruncated bool                              `json:"transcriptTruncated,omitempty"`
}

type SupervisorProcessState string

const (
	SupervisorProcessExecStopped      SupervisorProcessState = "exec-stopped"
	SupervisorProcessRunning          SupervisorProcessState = "running"
	SupervisorProcessExited           SupervisorProcessState = "exited"
	SupervisorProcessAbsent           SupervisorProcessState = "absent"
	SupervisorProcessIdentityConflict SupervisorProcessState = "identity-conflict"
	SupervisorTranscriptCollected     SupervisorProcessState = "transcript-collected"
	SupervisorSessionClosed           SupervisorProcessState = "supervisor-closed"
)

// ExpectedAttemptProcessTerminal performs the read-only exact-birth probe for
// the process identity already admitted by ResultIngress. Production
// composition consumes this semantic query instead of reaching through the
// ingress boundary into process-supervisor mechanics.
func ExpectedAttemptProcessTerminal(state AttemptAuthorityState) (bool, error) {
	return processsupervisor.ExpectedProcessTerminal(state.ProcessStartedEvidence.Outcome.Process)
}

// AttemptCloseRecoveryRecorded reports whether the durable authority contains
// the exact pending or committed Close state that may be reconciled after a
// controller restart. It does not decide owner or Run eligibility.
func AttemptCloseRecoveryRecorded(state AttemptAuthorityState) bool {
	if state.SupervisorPendingIntentDigest != "" {
		return state.SupervisorPendingIntent.Command == processsupervisor.CommandClose
	}
	if len(state.SupervisorCommandCheckpoints) == 0 {
		return false
	}
	latest := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence
	return latest.Command == processsupervisor.CommandClose && latest.Disposition == "ok" && latest.Outcome.State == SupervisorSessionClosed
}

func validSupervisorCommand(command processsupervisor.CommandName) bool {
	switch command {
	case processsupervisor.CommandBindAuthority, processsupervisor.CommandAbortUnbound, processsupervisor.CommandSpawn, processsupervisor.CommandResume, processsupervisor.CommandInspect, processsupervisor.CommandTerminate, processsupervisor.CommandCollect, processsupervisor.CommandClose:
		return true
	default:
		return false
	}
}

func (e SupervisorCommandEvidence) Validate() error {
	if e.PreCommand.Generation != (processsupervisor.ProtocolGenerationContract{}) || e.PostCommand.Generation != (processsupervisor.ProtocolGenerationContract{}) {
		return ErrAttemptAuthorityConflict
	}
	if e.ProtocolRevision != processsupervisor.ProtocolRevision || !supervisorEvidenceID.MatchString(e.SessionID) || !validSupervisorCommand(e.Command) || !supervisorEvidenceID.MatchString(e.CommandID) || e.Sequence == 0 || e.Sequence > maxExactJSONInteger || e.Disposition != "ok" && e.Disposition != "rejected" || !supervisorEvidenceID.MatchString(e.ReasonCode) {
		return fmt.Errorf("%w: invalid supervisor command identity", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{
		"previousCommandHead":  e.PreviousCommandHead,
		"currentAuthorityHead": e.CurrentAuthorityHead,
		"requestDigest":        e.RequestDigest,
		"receiptDigest":        e.ReceiptDigest,
		"observationDigest":    e.ObservationDigest,
		"commandHead":          e.CommandHead,
	} {
		if err := requireDigest(name, digest); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptAuthorityConflict, err)
		}
	}
	anchorsOmitted := e.PreCommand == (SupervisorMechanicsAnchor{}) && e.PostCommand == (SupervisorMechanicsAnchor{})
	if !anchorsOmitted && (e.PreCommand.Validate() != nil || e.PostCommand.Validate() != nil || e.PreCommand.SessionID != e.SessionID || e.PostCommand.SessionID != e.SessionID || e.PreCommand.Authority != e.PostCommand.Authority || e.PreCommand.SessionNonceDigest != e.PostCommand.SessionNonceDigest || e.PreCommand.OwnerEpoch != e.PostCommand.OwnerEpoch || e.PreCommand.UID != e.PostCommand.UID || e.PreCommand.GID != e.PostCommand.GID || e.PreCommand.FixedBinary != e.PostCommand.FixedBinary || e.PreCommand.ControlSocket != e.PostCommand.ControlSocket || e.PreCommand.ControlFiles != e.PostCommand.ControlFiles || e.PreCommand.CommandSequence+1 != e.Sequence || e.PreCommand.CommandHead != e.PreviousCommandHead || e.PostCommand.CommandSequence != e.Sequence || e.PostCommand.CommandHead != e.CommandHead || e.PostCommand.JournalSequence != e.PreCommand.JournalSequence+2 || e.PostCommand.JournalHead == e.PreCommand.JournalHead) {
		return fmt.Errorf("%w: supervisor mechanics anchors are not continuous", ErrAttemptAuthorityConflict)
	}
	if (e.PreCommand == (SupervisorMechanicsAnchor{})) != (e.PostCommand == (SupervisorMechanicsAnchor{})) {
		return fmt.Errorf("%w: partial supervisor mechanics anchors", ErrAttemptAuthorityConflict)
	}
	if e.Disposition == "rejected" {
		if e.BoundAuthorityHead != "" || e.Outcome != (SupervisorProcessOutcome{}) {
			return fmt.Errorf("%w: rejected supervisor command carries success projection", ErrAttemptAuthorityConflict)
		}
	} else if e.Command == processsupervisor.CommandBindAuthority {
		if requireDigest("boundAuthorityHead", e.BoundAuthorityHead) != nil || e.BoundAuthorityHead == e.CurrentAuthorityHead || !anchorsOmitted && e.PostCommand.CurrentAuthorityHead != e.BoundAuthorityHead || e.Outcome != (SupervisorProcessOutcome{}) {
			return fmt.Errorf("%w: invalid authority-bind projection", ErrAttemptAuthorityConflict)
		}
	} else if e.BoundAuthorityHead != "" {
		return fmt.Errorf("%w: bound authority on unrelated command", ErrAttemptAuthorityConflict)
	} else if !anchorsOmitted && e.PostCommand.CurrentAuthorityHead != e.CurrentAuthorityHead {
		return fmt.Errorf("%w: non-bind authority anchor changed", ErrAttemptAuthorityConflict)
	} else if !anchorsOmitted && e.Command == processsupervisor.CommandSpawn && e.PreCommand.CurrentAuthorityHead != e.CurrentAuthorityHead {
		return fmt.Errorf("%w: spawn authority was not pre-anchored", ErrAttemptAuthorityConflict)
	}
	wantObservation, wantReceipt, err := e.boundMechanicsDigests()
	if err != nil || wantObservation != e.ObservationDigest || wantReceipt != e.ReceiptDigest {
		return fmt.Errorf("%w: supervisor typed outcome is not receipt-bound", ErrAttemptAuthorityConflict)
	}
	wantHead, err := canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{e.PreviousCommandHead, e.RequestDigest, e.ReceiptDigest})
	if err != nil || wantHead != e.CommandHead {
		return fmt.Errorf("%w: supervisor command head mismatch", ErrAttemptAuthorityConflict)
	}
	return nil
}

func (e SupervisorCommandEvidence) boundMechanicsDigests() (string, string, error) {
	if e.Disposition == "rejected" {
		observation := canonical.DigestBytes([]byte(e.ReasonCode))
		result := processsupervisor.MechanicsResult{Disposition: e.Disposition, ReasonCode: e.ReasonCode, ObservationDigest: observation, Payload: json.RawMessage("{}")}
		receipt, err := canonicalDigest(result)
		return observation, receipt, err
	}
	if e.Command == processsupervisor.CommandBindAuthority {
		payload := json.RawMessage("{}")
		result := processsupervisor.MechanicsResult{Disposition: e.Disposition, ReasonCode: e.ReasonCode, ObservationDigest: e.ObservationDigest, Payload: payload}
		receipt, err := canonicalDigest(result)
		return e.ObservationDigest, receipt, err
	}
	if err := e.Outcome.Validate(); err != nil {
		return "", "", err
	}
	report := supervisorMechanicsReport{
		State: e.Outcome.MechanicsState, ObserverIdentity: e.Outcome.ObserverIdentity, ObservedAt: e.Outcome.ObservedAt,
		Process: e.Outcome.Process, RuntimeObjectDigest: e.Outcome.RuntimeObjectDigest, WorkingObjectDigest: e.Outcome.WorkingObjectDigest,
		SourceGateRevision: e.Outcome.SourceGateRevision, ExactSetDigest: e.Outcome.ExactSetDigest,
		ExitCode: e.Outcome.ExitCode, Signal: e.Outcome.Signal, StdoutDigest: e.Outcome.StdoutDigest, StderrDigest: e.Outcome.StderrDigest,
		StdoutBytes: e.Outcome.StdoutBytes, StderrBytes: e.Outcome.StderrBytes, TranscriptTruncated: e.Outcome.TranscriptTruncated,
	}
	payload, err := processsupervisor.CanonicalProtocolMessage(report)
	if err != nil {
		return "", "", err
	}
	observation := canonical.DigestBytes(payload)
	result := processsupervisor.MechanicsResult{Disposition: e.Disposition, ReasonCode: e.ReasonCode, ObservationDigest: observation, Payload: payload}
	if e.Command == processsupervisor.CommandCollect {
		result.TranscriptDigest, result.StdoutBytes, result.StderrBytes, result.Truncated = e.Outcome.TranscriptDigest, e.Outcome.StdoutBytes, e.Outcome.StderrBytes, e.Outcome.TranscriptTruncated
	}
	receipt, err := canonicalDigest(result)
	return observation, receipt, err
}

func (outcome SupervisorProcessOutcome) Validate() error {
	switch outcome.State {
	case SupervisorProcessExecStopped, SupervisorProcessRunning, SupervisorProcessExited, SupervisorTranscriptCollected:
		if validateSupervisorProcessIdentity(outcome.Process) != nil {
			return fmt.Errorf("%w: invalid supervisor child identity", ErrAttemptAuthorityConflict)
		}
	case SupervisorProcessIdentityConflict:
		// Identity conflict still cites the previously frozen identity. It must
		// never degrade to PID-only or an empty diagnostic.
		if validateSupervisorProcessIdentity(outcome.Process) != nil {
			return fmt.Errorf("%w: identity conflict lacks frozen child identity", ErrAttemptAuthorityConflict)
		}
	case SupervisorProcessAbsent:
		// A supervised Attempt has already frozen a child identity. "Absent"
		// means that exact birth was proved absent, not that the process field is
		// unknown or that a PID scan found no match.
		if validateSupervisorProcessIdentity(outcome.Process) != nil {
			return fmt.Errorf("%w: absent outcome lacks frozen child identity", ErrAttemptAuthorityConflict)
		}
	case SupervisorSessionClosed:
		if validateSupervisorProcessIdentity(outcome.Process) != nil {
			return fmt.Errorf("%w: closed outcome lacks exact terminal child identity", ErrAttemptAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: unknown supervisor outcome %q", ErrAttemptAuthorityConflict, outcome.State)
	}
	wantMechanicsState := string(outcome.State)
	switch outcome.State {
	case SupervisorProcessExited, SupervisorProcessAbsent, SupervisorProcessIdentityConflict, SupervisorTranscriptCollected, SupervisorSessionClosed:
		wantMechanicsState = "terminal"
	}
	if outcome.MechanicsState != wantMechanicsState {
		return fmt.Errorf("%w: supervisor semantic outcome is not mechanics-bound", ErrAttemptAuthorityConflict)
	}
	if strings.TrimSpace(outcome.ObserverIdentity) == "" {
		return fmt.Errorf("%w: supervisor outcome observer is empty", ErrAttemptAuthorityConflict)
	}
	observed, err := time.Parse(time.RFC3339Nano, outcome.ObservedAt)
	if err != nil || observed.Location() != time.UTC || observed.Format(time.RFC3339Nano) != outcome.ObservedAt {
		return fmt.Errorf("%w: supervisor outcome observedAt is not canonical UTC", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{
		"runtimeObjectDigest": outcome.RuntimeObjectDigest,
		"workingObjectDigest": outcome.WorkingObjectDigest,
		"stdoutDigest":        outcome.StdoutDigest,
		"stderrDigest":        outcome.StderrDigest,
		"transcriptDigest":    outcome.TranscriptDigest,
		"exactSetDigest":      outcome.ExactSetDigest,
	} {
		if digest != "" && requireDigest(name, digest) != nil {
			return fmt.Errorf("%w: invalid %s", ErrAttemptAuthorityConflict, name)
		}
	}
	if outcome.RuntimeObjectDigest == "" || outcome.WorkingObjectDigest == "" {
		return fmt.Errorf("%w: supervisor child object identity is incomplete", ErrAttemptAuthorityConflict)
	}
	if outcome.SourceGateRevision == "" && outcome.ExactSetDigest != "" || outcome.SourceGateRevision != "" && (outcome.SourceGateRevision != processsupervisor.SourceGateRevisionV1 || requireDigest("exactSetDigest", outcome.ExactSetDigest) != nil) {
		return fmt.Errorf("%w: invalid source-gate projection", ErrAttemptAuthorityConflict)
	}
	if outcome.State == SupervisorProcessExecStopped && (outcome.RuntimeObjectDigest == "" || outcome.WorkingObjectDigest == "" || outcome.ExitCode != 0 || outcome.Signal != "" || outcome.StdoutDigest != "" || outcome.StderrDigest != "" || outcome.TranscriptDigest != "") {
		return fmt.Errorf("%w: invalid exec-stopped outcome", ErrAttemptAuthorityConflict)
	}
	if outcome.State == SupervisorTranscriptCollected && (outcome.TranscriptDigest == "" || outcome.StdoutDigest == "" || outcome.StderrDigest == "") {
		return fmt.Errorf("%w: incomplete transcript outcome", ErrAttemptAuthorityConflict)
	}
	if outcome.State != SupervisorTranscriptCollected && outcome.TranscriptDigest != "" {
		return fmt.Errorf("%w: transcript receipt on unrelated outcome", ErrAttemptAuthorityConflict)
	}
	if outcome.StdoutBytes > processsupervisor.MaxStdoutBytes || outcome.StderrBytes > processsupervisor.MaxStderrBytes || outcome.StdoutBytes+outcome.StderrBytes > processsupervisor.MaxTranscriptBytes {
		return fmt.Errorf("%w: supervisor transcript projection exceeds protocol bounds", ErrAttemptAuthorityConflict)
	}
	return nil
}

func zeroSupervisorCommandEvidence(evidence SupervisorCommandEvidence) bool {
	return evidence == (SupervisorCommandEvidence{})
}

func commandEvidenceMatchesProcess(evidence SupervisorCommandEvidence, process ProcessObservation) bool {
	identity := evidence.Outcome.Process
	return identity.PID == process.PID && identity.ProcessGroupID == process.PGID && identity.BirthSeconds == process.BirthSeconds && identity.BirthMicroseconds == process.BirthMicroseconds
}

// NewSupervisorCommandEvidence closes the protocol-to-ledger seam. It accepts
// only an already authenticated response bound to the exact request, then
// persists the closed, secret-free projection supplied by the client.
func NewSupervisorCommandEvidence(outcome processsupervisor.VerifiedCommandOutcome) (SupervisorCommandEvidence, error) {
	pre, post := outcome.Recovery.PreCommand, outcome.Recovery.PostCommand
	if pre.SessionID == "" || pre.SessionID != post.SessionID || pre.CommandSequence+1 != outcome.Sequence || post.CommandSequence != outcome.Sequence || post.CommandHead != outcome.CommandHead || outcome.Status != outcome.Disposition {
		return SupervisorCommandEvidence{}, fmt.Errorf("%w: invalid verified command recovery anchors", ErrAttemptAuthorityConflict)
	}
	currentAuthorityHead := post.CurrentAuthorityHead
	boundAuthorityHead := ""
	if outcome.Command == processsupervisor.CommandBindAuthority && outcome.Disposition == "ok" {
		currentAuthorityHead = pre.CurrentAuthorityHead
		boundAuthorityHead = post.CurrentAuthorityHead
	} else if outcome.Command == processsupervisor.CommandBindAuthority {
		currentAuthorityHead = pre.CurrentAuthorityHead
	} else if outcome.Command == processsupervisor.CommandSpawn && post.CurrentAuthorityHead != pre.CurrentAuthorityHead {
		return SupervisorCommandEvidence{}, fmt.Errorf("%w: command authority was not pre-anchored", ErrAttemptAuthorityConflict)
	}
	evidence := SupervisorCommandEvidence{
		ProtocolRevision: processsupervisor.ProtocolRevision,
		SessionID:        pre.SessionID, Command: outcome.Command, CommandID: outcome.CommandID,
		Sequence: outcome.Sequence, PreviousCommandHead: pre.CommandHead,
		CurrentAuthorityHead: currentAuthorityHead, RequestDigest: outcome.RequestDigest,
		ReceiptDigest: outcome.ReceiptDigest, ObservationDigest: outcome.ObservationDigest,
		CommandHead: outcome.CommandHead, Disposition: outcome.Disposition, ReasonCode: outcome.ReasonCode,
		BoundAuthorityHead: boundAuthorityHead,
		PreCommand:         projectSupervisorMechanicsAnchor(pre),
		PostCommand:        projectSupervisorMechanicsAnchor(post),
	}
	if outcome.ProcessReport != nil {
		projected, err := projectVerifiedSupervisorProcessOutcome(outcome)
		if err != nil {
			return SupervisorCommandEvidence{}, err
		}
		evidence.Outcome = projected
	}
	if err := evidence.Validate(); err != nil {
		return SupervisorCommandEvidence{}, err
	}
	return evidence, nil
}

// projectVerifiedSupervisorProcessOutcome preserves the distinction frozen by
// ADR 0056: an Inspect terminal observation, or a Terminate that reports the
// process was already terminal, proves absence but does not prove that this
// command signalled the process group. Only process-terminal is projected as
// ProcessTerminated. The authenticated report state alone is insufficient;
// command and reason are part of the closed semantic union.
func projectVerifiedSupervisorProcessOutcome(outcome processsupervisor.VerifiedCommandOutcome) (SupervisorProcessOutcome, error) {
	report := *outcome.ProcessReport
	projected := SupervisorProcessOutcome{MechanicsState: report.State, Process: report.Process, ObserverIdentity: report.ObserverIdentity, ObservedAt: report.ObservedAt, RuntimeObjectDigest: report.RuntimeObjectDigest, WorkingObjectDigest: report.WorkingObjectDigest, SourceGateRevision: report.SourceGateRevision, ExactSetDigest: report.ExactSetDigest, ExitCode: report.ExitCode, Signal: report.Signal, StdoutDigest: report.StdoutDigest, StderrDigest: report.StderrDigest, StdoutBytes: report.StdoutBytes, StderrBytes: report.StderrBytes, TranscriptTruncated: report.TranscriptTruncated, TranscriptDigest: outcome.TranscriptDigest}
	switch outcome.Command {
	case processsupervisor.CommandSpawn:
		if outcome.ReasonCode != "process-exec-stopped" || report.State != "exec-stopped" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged spawn mechanics outcome", ErrAttemptAuthorityConflict)
		}
		projected.State = SupervisorProcessExecStopped
	case processsupervisor.CommandResume:
		if outcome.ReasonCode != "process-resumed" || report.State != "running" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged resume mechanics outcome", ErrAttemptAuthorityConflict)
		}
		projected.State = SupervisorProcessRunning
	case processsupervisor.CommandInspect:
		if outcome.ReasonCode != "process-inspected" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged inspect mechanics reason", ErrAttemptAuthorityConflict)
		}
		switch report.State {
		case "exec-stopped":
			projected.State = SupervisorProcessExecStopped
		case "running":
			projected.State = SupervisorProcessRunning
		case "terminal":
			projected.State = SupervisorProcessAbsent
		default:
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged inspect mechanics state", ErrAttemptAuthorityConflict)
		}
	case processsupervisor.CommandTerminate:
		if report.State != "terminal" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: terminate lacks terminal mechanics report", ErrAttemptAuthorityConflict)
		}
		switch outcome.ReasonCode {
		case "process-already-terminal":
			projected.State = SupervisorProcessAbsent
		case "process-terminal":
			projected.State = SupervisorProcessExited
		default:
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged terminate mechanics reason", ErrAttemptAuthorityConflict)
		}
	case processsupervisor.CommandCollect:
		if outcome.ReasonCode != "transcript-collected" || report.State != "terminal" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged collect mechanics outcome", ErrAttemptAuthorityConflict)
		}
		projected.State = SupervisorTranscriptCollected
	case processsupervisor.CommandClose:
		if outcome.ReasonCode != "mechanics-closed" || report.State != "terminal" {
			return SupervisorProcessOutcome{}, fmt.Errorf("%w: forged close mechanics outcome", ErrAttemptAuthorityConflict)
		}
		projected.State = SupervisorSessionClosed
	default:
		return SupervisorProcessOutcome{}, fmt.Errorf("%w: command cannot carry a process report", ErrAttemptAuthorityConflict)
	}
	return projected, nil
}

// SupervisorBootstrapRequestProjection is the secret-free canonical image of
// the real BootstrapRequest. BootstrapRequestDigest is derived from this exact
// value; it is never an arbitrary digest echo. Only SessionNonceDigest is
// retained, never the raw nonce.
type SupervisorBootstrapRequestProjection struct {
	Generation                  processsupervisor.ProtocolGenerationContract `json:"generation,omitempty,omitzero"`
	LaunchChildProtocolRevision string                                       `json:"launchChildProtocolRevision,omitempty"`
	MechanicsIdentity           string                                       `json:"mechanicsIdentity,omitempty"`
	SchemaVersion               string                                       `json:"schemaVersion"`
	ProtocolRevision            string                                       `json:"protocolRevision"`
	SessionID                   string                                       `json:"sessionId"`
	SessionNonceDigest          string                                       `json:"sessionNonceDigest"`
	OwnerEpoch                  uint64                                       `json:"ownerEpoch"`
	Authority                   processsupervisor.AuthorityTuple             `json:"authority"`
	LaunchAuthorizedFact        string                                       `json:"launchAuthorizedFactDigest"`
	CurrentAuthorityHead        string                                       `json:"currentAuthorityHead"`
	ControlDirectoryIdentity    processsupervisor.ControlDirectoryIdentity   `json:"controlDirectoryIdentity"`
	Core                        processsupervisor.CoreIdentity               `json:"core"`
}

func projectSupervisorBootstrapRequest(request processsupervisor.BootstrapRequest) (SupervisorBootstrapRequestProjection, string, error) {
	projection := SupervisorBootstrapRequestProjection{SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(request.SessionNonce)), OwnerEpoch: request.OwnerEpoch, Authority: request.Authority, LaunchAuthorizedFact: request.LaunchAuthorizedFact, CurrentAuthorityHead: request.CurrentAuthorityHead, ControlDirectoryIdentity: request.ControlDirectoryIdentity, Core: request.Core}
	if projection.SchemaVersion != processsupervisor.BootstrapSchema || projection.ProtocolRevision != processsupervisor.ProtocolRevision || !supervisorEvidenceID.MatchString(projection.SessionID) || projection.OwnerEpoch == 0 || projection.OwnerEpoch > maxExactJSONInteger || validateSupervisorAuthorityTuple(projection.Authority) != nil || requireDigest("sessionNonceDigest", projection.SessionNonceDigest) != nil || requireDigest("launchAuthorizedFactDigest", projection.LaunchAuthorizedFact) != nil || requireDigest("currentAuthorityHead", projection.CurrentAuthorityHead) != nil || validateControlDirectoryIdentity(projection.ControlDirectoryIdentity) != nil || projection.Core.UID == 0 || validateSupervisorProcessIdentity(projection.Core.Process) != nil || validateFixedMarshalBinaryIdentity(projection.Core.Binary) != nil {
		return SupervisorBootstrapRequestProjection{}, "", fmt.Errorf("%w: invalid bootstrap request projection", ErrAttemptAuthorityConflict)
	}
	digest, err := canonicalDigest(projection)
	return projection, digest, err
}

func validateSupervisorAuthorityTuple(tuple processsupervisor.AuthorityTuple) error {
	for _, value := range []string{tuple.AuthorityNamespaceID, tuple.TaskID, tuple.RunID, tuple.AttemptID, tuple.AllocationID, tuple.LeaseID, tuple.OrchestratorID} {
		if !supervisorEvidenceID.MatchString(value) {
			return ErrAttemptAuthorityConflict
		}
	}
	if tuple.Generation == 0 || tuple.Generation > maxExactJSONInteger || requireDigest("leaseDigest", tuple.LeaseDigest) != nil || requireDigest("fencingTokenDigest", tuple.FencingTokenDigest) != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func supervisorAuthorityTuple(identity AttemptIdentity) processsupervisor.AuthorityTuple {
	return processsupervisor.AuthorityTuple{
		AuthorityNamespaceID: identity.AuthorityNamespaceRef,
		TaskID:               identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID,
		AllocationID: identity.AllocationID, LeaseID: identity.LeaseID, LeaseDigest: identity.LeaseDigest,
		Generation: uint64(identity.DispatchGeneration), FencingTokenDigest: identity.FencingTokenDigest,
		OrchestratorID: identity.OrchestratorID,
	}
}

type SupervisorBootstrapPrepared struct {
	ProtocolRevision           string                                     `json:"protocolRevision"`
	Owner                      CurrentOwnerBinding                        `json:"owner"`
	LaunchAuthorizedFactDigest string                                     `json:"launchAuthorizedFactDigest"`
	SessionID                  string                                     `json:"sessionId"`
	SessionNonceDigest         string                                     `json:"sessionNonceDigest"`
	ControlDirectory           processsupervisor.ControlDirectoryIdentity `json:"controlDirectory"`
	SupervisorBinary           processsupervisor.BinaryIdentity           `json:"supervisorBinary"`
	Request                    SupervisorBootstrapRequestProjection       `json:"request,omitempty,omitzero"`
	BootstrapRequestDigest     string                                     `json:"bootstrapRequestDigest"`
}

// SupervisorCommandRebuildProjection is the non-secret portion of a command
// payload needed to reconstruct the exact request from authority plus held
// descriptors after restart. Paths, argv, environment values, stdin and raw
// nonce are deliberately absent; their canonical payload digest remains
// frozen by SupervisorCommandIntent.
type SupervisorCommandRebuildProjection = processsupervisor.PreparedCommandProjection

// SupervisorCommandIntent is creation-once durable intent. It is appended
// before Client.Do and contains no executable payload or bearer material.
type SupervisorCommandIntent struct {
	ProtocolRevision     string                             `json:"protocolRevision"`
	SessionID            string                             `json:"sessionId"`
	Command              processsupervisor.CommandName      `json:"command"`
	CommandID            string                             `json:"commandId"`
	Sequence             uint64                             `json:"sequence"`
	PreviousCommandHead  string                             `json:"previousCommandHead"`
	CurrentAuthorityHead string                             `json:"currentAuthorityHead"`
	Deadline             string                             `json:"deadline"`
	RequestDigest        string                             `json:"requestDigest"`
	PayloadDigest        string                             `json:"payloadDigest"`
	Rebuild              SupervisorCommandRebuildProjection `json:"rebuild"`
	PreCommand           SupervisorMechanicsAnchor          `json:"preCommand"`
}

func NewSupervisorCommandIntent(evidence processsupervisor.PreparedCommandEvidence) (SupervisorCommandIntent, error) {
	if evidence.Validate() != nil {
		return SupervisorCommandIntent{}, fmt.Errorf("%w: invalid prepared supervisor command", ErrAttemptAuthorityConflict)
	}
	intent := SupervisorCommandIntent{
		ProtocolRevision: evidence.ProtocolRevision, SessionID: evidence.SessionID, Command: evidence.Command,
		CommandID: evidence.CommandID, Sequence: evidence.Sequence, PreviousCommandHead: evidence.PreviousCommandDigest,
		CurrentAuthorityHead: evidence.CurrentAuthorityHead, Deadline: evidence.Deadline, RequestDigest: evidence.RequestDigest,
		PayloadDigest: evidence.PayloadDigest, Rebuild: evidence.Projection, PreCommand: projectSupervisorMechanicsAnchor(evidence.PreCommand),
	}
	if err := intent.Validate(); err != nil {
		return SupervisorCommandIntent{}, err
	}
	return intent, nil
}

func (intent SupervisorCommandIntent) Validate() error {
	if intent.PreCommand.Generation != (processsupervisor.ProtocolGenerationContract{}) {
		return ErrAttemptAuthorityConflict
	}
	deadline, err := time.Parse(time.RFC3339Nano, intent.Deadline)
	if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != intent.Deadline || intent.ProtocolRevision != processsupervisor.ProtocolRevision || !supervisorEvidenceID.MatchString(intent.SessionID) || !validSupervisorCommand(intent.Command) || !supervisorEvidenceID.MatchString(intent.CommandID) || intent.Sequence == 0 || intent.Sequence > maxExactJSONInteger {
		return fmt.Errorf("%w: invalid supervisor command intent identity", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{"previousCommandHead": intent.PreviousCommandHead, "currentAuthorityHead": intent.CurrentAuthorityHead, "requestDigest": intent.RequestDigest, "payloadDigest": intent.PayloadDigest} {
		if requireDigest(name, digest) != nil {
			return fmt.Errorf("%w: invalid supervisor command intent %s", ErrAttemptAuthorityConflict, name)
		}
	}
	if intent.PreCommand.Validate() != nil || intent.PreCommand.SessionID != intent.SessionID || intent.PreCommand.CommandSequence+1 != intent.Sequence || intent.PreCommand.CommandHead != intent.PreviousCommandHead {
		return fmt.Errorf("%w: supervisor intent pre-command anchor mismatch", ErrAttemptAuthorityConflict)
	}
	if (intent.Command == processsupervisor.CommandBindAuthority || intent.Command == processsupervisor.CommandSpawn) && intent.PreCommand.CurrentAuthorityHead != intent.CurrentAuthorityHead {
		return fmt.Errorf("%w: supervisor intent requires a pre-anchored command", ErrAttemptAuthorityConflict)
	}
	return validateSupervisorRebuildProjection(intent.Command, intent.Rebuild)
}

func validateSupervisorRebuildProjection(command processsupervisor.CommandName, projection SupervisorCommandRebuildProjection) error {
	if processsupervisor.ValidatePreparedCommandProjection(command, projection) != nil {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

type SupervisorCommandCheckpoint struct {
	FactDigest string                    `json:"factDigest"`
	Intent     SupervisorCommandIntent   `json:"intent,omitempty,omitzero"`
	Evidence   SupervisorCommandEvidence `json:"evidence"`
}

func NewSupervisorBootstrapPrepared(owner CurrentOwnerBinding, request processsupervisor.BootstrapRequest) (SupervisorBootstrapPrepared, error) {
	projection, digest, err := projectSupervisorBootstrapRequest(request)
	if err != nil {
		return SupervisorBootstrapPrepared{}, err
	}
	prepared := SupervisorBootstrapPrepared{ProtocolRevision: processsupervisor.ProtocolRevision, Owner: owner, LaunchAuthorizedFactDigest: request.LaunchAuthorizedFact, SessionID: request.SessionID, SessionNonceDigest: projection.SessionNonceDigest, ControlDirectory: request.ControlDirectoryIdentity, SupervisorBinary: request.Core.Binary, Request: projection, BootstrapRequestDigest: digest}
	if err := prepared.Validate(); err != nil {
		return SupervisorBootstrapPrepared{}, err
	}
	return prepared, nil
}

func (prepared SupervisorBootstrapPrepared) Validate() error {
	if prepared.ProtocolRevision == processsupervisor.DormantV2ProtocolContract().ProtocolRevision {
		return validateSupervisorBootstrapPreparedV2(prepared)
	}
	if prepared.Request.Generation != (processsupervisor.ProtocolGenerationContract{}) || prepared.Request.LaunchChildProtocolRevision != "" || prepared.Request.MechanicsIdentity != "" {
		return ErrAttemptAuthorityConflict
	}
	if prepared.ProtocolRevision != processsupervisor.ProtocolRevision || prepared.Owner.Validate() != nil || !supervisorEvidenceID.MatchString(prepared.SessionID) || validateControlDirectoryIdentity(prepared.ControlDirectory) != nil || validateFixedMarshalBinaryIdentity(prepared.SupervisorBinary) != nil {
		return fmt.Errorf("%w: invalid supervisor bootstrap identity", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{"launchAuthorizedFactDigest": prepared.LaunchAuthorizedFactDigest, "sessionNonceDigest": prepared.SessionNonceDigest, "bootstrapRequestDigest": prepared.BootstrapRequestDigest} {
		if requireDigest(name, digest) != nil {
			return fmt.Errorf("%w: invalid %s", ErrAttemptAuthorityConflict, name)
		}
	}
	if prepared.Request == (SupervisorBootstrapRequestProjection{}) {
		// Historical prepared facts predate the typed request projection. Replay
		// accepts their exact bytes, while fresh mutation rejects them in the
		// authority projection because no request/current-head binding exists.
		return nil
	}
	wantDigest, err := canonicalDigest(prepared.Request)
	if err != nil || wantDigest != prepared.BootstrapRequestDigest || prepared.Request.ProtocolRevision != prepared.ProtocolRevision || prepared.Request.SessionID != prepared.SessionID || prepared.Request.SessionNonceDigest != prepared.SessionNonceDigest || prepared.Request.OwnerEpoch != prepared.Owner.OwnerEpoch || prepared.Request.LaunchAuthorizedFact != prepared.LaunchAuthorizedFactDigest || prepared.Request.ControlDirectoryIdentity != prepared.ControlDirectory || prepared.Request.Core.Binary != prepared.SupervisorBinary {
		return fmt.Errorf("%w: bootstrap request projection mismatch", ErrAttemptAuthorityConflict)
	}
	// Binary validation remains centralized in the process-supervisor
	// handshake. Requiring a self-consistent handshake before started prevents
	// this prepared fact from becoming an executable-path bearer.
	return nil
}

type SupervisorPendingCommand struct {
	SessionID            string                        `json:"sessionId"`
	Command              processsupervisor.CommandName `json:"command"`
	CommandID            string                        `json:"commandId"`
	Sequence             uint64                        `json:"sequence"`
	PreviousCommandHead  string                        `json:"previousCommandHead"`
	CurrentAuthorityHead string                        `json:"currentAuthorityHead"`
	RequestDigest        string                        `json:"requestDigest"`
}

func (pending SupervisorPendingCommand) Validate() error {
	if !supervisorEvidenceID.MatchString(pending.SessionID) || !validSupervisorCommand(pending.Command) || !supervisorEvidenceID.MatchString(pending.CommandID) || pending.Sequence == 0 || pending.Sequence > maxExactJSONInteger {
		return fmt.Errorf("%w: invalid unresolved supervisor command", ErrAttemptAuthorityConflict)
	}
	for name, digest := range map[string]string{"previousCommandHead": pending.PreviousCommandHead, "currentAuthorityHead": pending.CurrentAuthorityHead, "requestDigest": pending.RequestDigest} {
		if requireDigest(name, digest) != nil {
			return fmt.Errorf("%w: invalid unresolved %s", ErrAttemptAuthorityConflict, name)
		}
	}
	return nil
}

type SupervisorInterventionReason string

const (
	SupervisorInterventionBootstrapUnresolved SupervisorInterventionReason = "bootstrap-unresolved"
	SupervisorInterventionCommandUnresolved   SupervisorInterventionReason = "command-intent-unresolved"
	SupervisorInterventionIdentityConflict    SupervisorInterventionReason = "supervisor-identity-conflict"
	SupervisorInterventionUnavailable         SupervisorInterventionReason = "supervisor-unavailable"
)

type SupervisorIntervention struct {
	ProtocolRevision string                       `json:"protocolRevision"`
	Owner            CurrentOwnerBinding          `json:"owner"`
	SessionID        string                       `json:"sessionId"`
	Reason           SupervisorInterventionReason `json:"reason"`
	EvidenceDigest   string                       `json:"evidenceDigest"`
	Pending          SupervisorPendingCommand     `json:"pending,omitempty,omitzero"`
}

func (intervention SupervisorIntervention) Validate() error {
	if intervention.ProtocolRevision != processsupervisor.ProtocolRevision || intervention.Owner.Validate() != nil || !supervisorEvidenceID.MatchString(intervention.SessionID) || requireDigest("evidenceDigest", intervention.EvidenceDigest) != nil {
		return fmt.Errorf("%w: invalid supervisor intervention", ErrAttemptAuthorityConflict)
	}
	switch intervention.Reason {
	case SupervisorInterventionBootstrapUnresolved, SupervisorInterventionIdentityConflict, SupervisorInterventionUnavailable:
		if intervention.Pending != (SupervisorPendingCommand{}) {
			return fmt.Errorf("%w: intervention reason carries unrelated pending command", ErrAttemptAuthorityConflict)
		}
	case SupervisorInterventionCommandUnresolved:
		if intervention.Pending.Validate() != nil || intervention.Pending.SessionID != intervention.SessionID {
			return fmt.Errorf("%w: unresolved intervention lacks exact command", ErrAttemptAuthorityConflict)
		}
	default:
		return fmt.Errorf("%w: unknown supervisor intervention reason", ErrAttemptAuthorityConflict)
	}
	return nil
}
