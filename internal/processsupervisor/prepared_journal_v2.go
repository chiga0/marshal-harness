package processsupervisor

import "os"

// This is a read-only classification, not authorization to execute or accept
// an outcome. A live recovery must authenticate the observed checkpoint via
// Attach while the current owner/ledger remains held. Close additionally
// needs the independent expected-Supervisor absence observation.
type PreparedJournalObservationV2 struct {
	Reconciliation ReconciliationState
	Outcome        *VerifiedCommandOutcomeV2
}

type PreparedJournalOptionsV2 struct {
	ControlDirectory *os.File
	Prepared         PreparedCommandV2
}

func classifyPreparedJournalV2(state journalStateV2, prepared PreparedCommandV2) (PreparedJournalObservationV2, error) {
	anchor := prepared.evidence.PreCommand
	if prepared.evidence.Validate() != nil || validateReconnectJournalV2(state, anchor, &prepared) != nil {
		return PreparedJournalObservationV2{}, ErrConflict
	}
	b := anchor.Binding
	if state.sequence == b.JournalSequence && state.head == b.JournalHead {
		if state.ownerEpoch != b.OwnerEpoch || state.authorityHead != b.CurrentAuthorityHead {
			return PreparedJournalObservationV2{}, ErrConflict
		}
		return PreparedJournalObservationV2{Reconciliation: ReconciliationUnchanged}, nil
	}
	if state.pending != nil {
		if state.ownerEpoch != b.OwnerEpoch || state.authorityHead != b.CurrentAuthorityHead {
			return PreparedJournalObservationV2{}, ErrConflict
		}
		return PreparedJournalObservationV2{Reconciliation: ReconciliationIntentPending}, nil
	}
	receipt, ok := state.receipts[prepared.evidence.CommandID]
	if !ok || receipt.Response == nil {
		return PreparedJournalObservationV2{}, ErrConflict
	}
	outcome, err := verifiedCommandOutcomeV2(prepared, *receipt.Response)
	if err != nil || outcome.PostCommand.Binding.OwnerEpoch != state.ownerEpoch || outcome.PostCommand.Binding.CurrentAuthorityHead != state.authorityHead {
		return PreparedJournalObservationV2{}, ErrConflict
	}
	return PreparedJournalObservationV2{Reconciliation: ReconciliationReceiptCommitted, Outcome: &outcome}, nil
}
