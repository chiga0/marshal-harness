//go:build darwin

package processsupervisor

import (
	"context"
	"fmt"
	"time"
)

func RecoverCommittedCloseV2(ctx context.Context, options CommittedCloseRecoveryOptionsV2) (CommittedCloseRecoveryEvidenceV2, error) {
	return recoverCommittedCloseWithObserversV2(ctx, options, ObserveCurrentCore, observeExpectedSupervisorAbsence)
}

// The only injected operations are kernel observation seams. The held v2
// journal/nonce/directory validation remains real and read-only in tests.
func recoverCommittedCloseWithObserversV2(ctx context.Context, options CommittedCloseRecoveryOptionsV2,
	coreObserver func(string) (CoreIdentity, error), absenceObserver func(ProcessIdentity) (SupervisorAbsenceState, *ProcessIdentity, error)) (CommittedCloseRecoveryEvidenceV2, error) {
	if ctx == nil || ctx.Err() != nil || options.ControlDirectory == nil || coreObserver == nil || absenceObserver == nil || !absoluteClean(options.FixedMarshalPath) || options.ExpectedSupervisor.validate() != nil {
		return CommittedCloseRecoveryEvidenceV2{}, ErrInvalid
	}
	p := options.PreparedClose.evidence
	if p.Validate() != nil || p.Command != CommandClose {
		return CommittedCloseRecoveryEvidenceV2{}, ErrInvalid
	}
	observer, err := coreObserver(options.FixedMarshalPath)
	if err != nil || observer.UID != p.PreCommand.Binding.UID || observer.GID != p.PreCommand.Binding.GID || !sameBinaryObject(observer.Binary, p.PreCommand.Binding.FixedBinary) {
		return CommittedCloseRecoveryEvidenceV2{}, fmt.Errorf("close recovery core identity: %w", ErrConflict)
	}
	state, replacement, err := absenceObserver(options.ExpectedSupervisor)
	if err != nil {
		return CommittedCloseRecoveryEvidenceV2{}, err
	}
	observed, err := ObservePreparedCommandV2(ctx, PreparedJournalOptionsV2{ControlDirectory: options.ControlDirectory, Prepared: options.PreparedClose})
	if err != nil {
		return CommittedCloseRecoveryEvidenceV2{}, fmt.Errorf("close recovery held journal: %w", err)
	}
	if observed.Reconciliation != ReconciliationReceiptCommitted || observed.Outcome == nil {
		return CommittedCloseRecoveryEvidenceV2{}, ErrIntervention
	}
	after, replacementAfter, err := absenceObserver(options.ExpectedSupervisor)
	if err != nil || state != after || !sameOptionalProcess(replacement, replacementAfter) || ctx.Err() != nil {
		return CommittedCloseRecoveryEvidenceV2{}, fmt.Errorf("close recovery absence drift: %w", ErrConflict)
	}
	o := *observed.Outcome
	absence := SupervisorAbsenceEvidence{SchemaVersion: SupervisorAbsenceSchema, State: state, Expected: options.ExpectedSupervisor, Replacement: replacement, Observer: observer,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ControlFiles: o.PostCommand.Binding.ControlFiles, FinalJournalSequence: o.PostCommand.Binding.JournalSequence, FinalJournalHead: o.PostCommand.Binding.JournalHead}
	recovered := CommittedCloseRecoveryEvidenceV2{Outcome: o, Absence: absence}
	if recovered.Validate() != nil {
		return CommittedCloseRecoveryEvidenceV2{}, fmt.Errorf("close recovery evidence binding: %w", ErrConflict)
	}
	return recovered, nil
}
