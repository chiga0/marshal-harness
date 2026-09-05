package processsupervisor

import "os"

type CommittedCloseRecoveryOptionsV2 struct {
	FixedMarshalPath   string
	ControlDirectory   *os.File
	PreparedClose      PreparedCommandV2
	ExpectedSupervisor ProcessIdentity
}

// The absence observation has generation-neutral kernel coordinates; its
// exact final checkpoint is bound to the complete v2 command outcome here.
type CommittedCloseRecoveryEvidenceV2 struct {
	Outcome VerifiedCommandOutcomeV2
	Absence SupervisorAbsenceEvidence
}

func (e CommittedCloseRecoveryEvidenceV2) Validate() error {
	o, a := e.Outcome, e.Absence
	if o.Validate() != nil || a.Validate() != nil || o.Preparation.Command != CommandClose || o.Status != "ok" || o.ReasonCode != "mechanics-closed" || o.ProcessReport == nil || o.ProcessReport.State != "terminal" {
		return ErrInvalid
	}
	p := o.PostCommand.Binding
	if a.ControlFiles != p.ControlFiles || a.FinalJournalSequence != p.JournalSequence || a.FinalJournalHead != p.JournalHead ||
		a.Observer.UID != p.UID || a.Observer.GID != p.GID || !sameBinaryObject(a.Observer.Binary, p.FixedBinary) {
		return ErrConflict
	}
	return nil
}
