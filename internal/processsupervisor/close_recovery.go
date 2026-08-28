package processsupervisor

import (
	"io"
	"os"
	"time"
)

const SupervisorAbsenceSchema = "process-supervisor-absence/v1"

type SupervisorAbsenceState string

const (
	SupervisorExpectedAbsent SupervisorAbsenceState = "absent"
	SupervisorPIDReused      SupervisorAbsenceState = "pid-reused"
)

// SupervisorAbsenceEvidence is a fixed-binary, kernel-adjacent observation
// that the exact supervisor birth is gone. It is independent from the child
// ProcessReport carried by the close receipt.
type SupervisorAbsenceEvidence struct {
	SchemaVersion        string                 `json:"schemaVersion"`
	State                SupervisorAbsenceState `json:"state"`
	Expected             ProcessIdentity        `json:"expected"`
	Replacement          *ProcessIdentity       `json:"replacement,omitempty"`
	Observer             CoreIdentity           `json:"observer"`
	ObservedAt           string                 `json:"observedAt"`
	ControlFiles         SessionControlFiles    `json:"controlFiles"`
	FinalJournalSequence uint64                 `json:"finalJournalSequence"`
	FinalJournalHead     string                 `json:"finalJournalHead"`
}

func (evidence SupervisorAbsenceEvidence) Validate() error {
	observed, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	birth := time.Unix(evidence.Expected.BirthSeconds, evidence.Expected.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if err != nil || observed.Location() != time.UTC || observed.Format(time.RFC3339Nano) != evidence.ObservedAt || observed.Before(birth) || evidence.SchemaVersion != SupervisorAbsenceSchema || evidence.Expected.validate() != nil || evidence.Observer.UID == 0 || evidence.Observer.Process.validate() != nil || evidence.Observer.Binary.validate() != nil || evidence.ControlFiles.validate() != nil || evidence.FinalJournalSequence == 0 || evidence.FinalJournalSequence > maxSafeJSONInteger || !validDigest(evidence.FinalJournalHead) {
		return ErrInvalid
	}
	switch evidence.State {
	case SupervisorExpectedAbsent:
		if evidence.Replacement != nil {
			return ErrInvalid
		}
	case SupervisorPIDReused:
		if evidence.Replacement == nil || evidence.Replacement.validate() != nil || evidence.Replacement.PID != evidence.Expected.PID || *evidence.Replacement == evidence.Expected {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type CommittedCloseRecoveryOptions struct {
	FixedMarshalPath         string
	ControlDirectory         *os.File
	ControlDirectoryIdentity ControlDirectoryIdentity
	PreparedClose            PreparedCommand
	ExpectedSupervisor       ProcessIdentity
}

type CommittedCloseRecoveryEvidence struct {
	Outcome VerifiedCommandOutcome    `json:"outcome"`
	Absence SupervisorAbsenceEvidence `json:"absence"`
}

func (evidence CommittedCloseRecoveryEvidence) Validate() error {
	if evidence.Absence.Validate() != nil || evidence.Outcome.Command != CommandClose || evidence.Outcome.Status != "ok" || evidence.Outcome.Disposition != "ok" || evidence.Outcome.ReasonCode != "mechanics-closed" || evidence.Outcome.ProcessReport == nil || evidence.Outcome.ProcessReport.State != "terminal" || !evidence.Outcome.Recovery.Replayed || evidence.Outcome.Recovery.Reconciliation != ReconciliationReceiptCommitted || evidence.Outcome.Recovery.PostCommand.JournalSequence != evidence.Absence.FinalJournalSequence || evidence.Outcome.Recovery.PostCommand.JournalHead != evidence.Absence.FinalJournalHead || evidence.Outcome.Recovery.PreCommand.ControlFiles != evidence.Absence.ControlFiles {
		return ErrInvalid
	}
	return nil
}

func recoverCommittedCloseFromJournal(file *os.File, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	evidence := prepared.evidence
	if file == nil || evidence.Validate() != nil || evidence.Command != CommandClose {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	stat, err := file.Stat()
	if err != nil || validateJournalFile(file) != nil || stat.Size() <= 0 || stat.Size() > MaxJournalFileBytes {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, stat.Size()))
	if err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	statAfter, err := file.Stat()
	if err != nil || statAfter.Size() != stat.Size() || !statAfter.ModTime().Equal(stat.ModTime()) || statAfter.Mode() != stat.Mode() {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	records, consumed, partial, err := parseJournal(data)
	if err != nil || partial || consumed != len(data) || uint64(len(records)) != evidence.PreCommand.JournalSequence+2 || evidence.PreCommand.JournalSequence == 0 {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	preIndex := int(evidence.PreCommand.JournalSequence) - 1
	if preIndex < 0 || preIndex+2 >= len(records) || records[preIndex].RecordDigest != evidence.PreCommand.JournalHead {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	intent, receipt := records[preIndex+1], records[preIndex+2]
	projection, _, err := projectRequest(prepared.request)
	if err != nil || intent.Kind != journalCommandIntent || receipt.Kind != journalCommandReceipt || intent.Request == nil || receipt.Request == nil || receipt.Response == nil || !equalProjection(*intent.Request, projection) || !equalProjection(*receipt.Request, projection) || !sameJournalCommandBase(intent, receipt) {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	pre := evidence.PreCommand
	if intent.SessionID != pre.SessionID || intent.SessionNonceDigest != pre.SessionNonceDigest || intent.Authority != pre.Authority || intent.OwnerEpoch != pre.OwnerEpoch || intent.CurrentAuthorityHead != pre.CurrentAuthorityHead || intent.PreviousRecordDigest != pre.JournalHead {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	intentHead, receiptHead, err := expectedPendingJournalHeads(pre, prepared.request, receipt.Response)
	if err != nil || intent.RecordDigest != intentHead || receipt.PreviousRecordDigest != intentHead || receipt.RecordDigest != receiptHead {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	post, err := commandPostAnchor(pre, prepared.request, *receipt.Response)
	if err != nil || post.JournalHead != receiptHead || post.JournalSequence != uint64(len(records)) {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	outcome, err := verifiedCommandOutcome(prepared.request, *receipt.Response, CommandRecoveryEvidence{Reconciliation: ReconciliationReceiptCommitted, Replayed: true, PreCommand: pre, PostCommand: post})
	if err != nil || outcome.Command != CommandClose || outcome.Status != "ok" || outcome.Disposition != "ok" || outcome.ReasonCode != "mechanics-closed" || outcome.ProcessReport == nil || outcome.ProcessReport.State != "terminal" {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	return outcome, nil
}
