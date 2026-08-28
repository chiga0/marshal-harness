//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// RecoverCommittedClose is read-only. It never signals, deletes, releases or
// truncates. A live expected supervisor must use authenticated reconnect.
func RecoverCommittedClose(ctx context.Context, options CommittedCloseRecoveryOptions) (CommittedCloseRecoveryEvidence, error) {
	if ctx == nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.ControlDirectoryIdentity.validate() != nil || options.ExpectedSupervisor.validate() != nil {
		return CommittedCloseRecoveryEvidence{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return CommittedCloseRecoveryEvidence{}, ErrIntervention
	}
	preparedEvidence := options.PreparedClose.evidence
	if preparedEvidence.Validate() != nil || preparedEvidence.Command != CommandClose {
		return CommittedCloseRecoveryEvidence{}, ErrInvalid
	}
	observer, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil || observer.UID != preparedEvidence.PreCommand.UID || observer.GID != preparedEvidence.PreCommand.GID || !sameBinaryObject(observer.Binary, preparedEvidence.PreCommand.FixedBinary) {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || directory != options.ControlDirectoryIdentity || revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, preparedEvidence.PreCommand.ControlSocket) != nil {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	held, err := openHeldSessionControlFiles(options.ControlDirectory, preparedEvidence.PreCommand.ControlFiles)
	if err != nil {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	defer held.close()
	if _, err := readSessionNonce(held, preparedEvidence.PreCommand.SessionNonceDigest); err != nil {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	state, replacement, err := observeExpectedSupervisorAbsence(options.ExpectedSupervisor)
	if err != nil {
		return CommittedCloseRecoveryEvidence{}, err
	}
	outcome, err := recoverCommittedCloseFromJournal(held.journal, options.PreparedClose)
	if err != nil {
		return CommittedCloseRecoveryEvidence{}, err
	}
	stateAfter, replacementAfter, err := observeExpectedSupervisorAbsence(options.ExpectedSupervisor)
	if err != nil || stateAfter != state || !sameOptionalProcess(replacementAfter, replacement) || revalidateControlDirectory(options.ControlDirectory, directory) != nil || observeControlSocketExact(options.ControlDirectory, preparedEvidence.PreCommand.ControlSocket) != nil || revalidateHeldSessionControlFiles(options.ControlDirectory, held, preparedEvidence.PreCommand.ControlFiles) != nil {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	absence := SupervisorAbsenceEvidence{SchemaVersion: SupervisorAbsenceSchema, State: state, Expected: options.ExpectedSupervisor, Replacement: replacement, Observer: observer, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ControlFiles: preparedEvidence.PreCommand.ControlFiles, FinalJournalSequence: outcome.Recovery.PostCommand.JournalSequence, FinalJournalHead: outcome.Recovery.PostCommand.JournalHead}
	recovered := CommittedCloseRecoveryEvidence{Outcome: outcome, Absence: absence}
	if recovered.Validate() != nil {
		return CommittedCloseRecoveryEvidence{}, ErrConflict
	}
	return recovered, nil
}

func observeExpectedSupervisorAbsence(expected ProcessIdentity) (SupervisorAbsenceState, *ProcessIdentity, error) {
	observed, err := observeAnyProcessIdentity(expected.PID)
	if err == nil {
		if observed == expected {
			return "", nil, ErrConflict
		}
		return SupervisorPIDReused, &observed, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return SupervisorExpectedAbsent, nil, nil
	}
	// SysctlKinfoProc does not preserve ESRCH on every supported Darwin. A
	// kill(pid, 0) absence check distinguishes not-found from ambiguous denial;
	// it never sends a signal.
	if killErr := unix.Kill(expected.PID, 0); errors.Is(killErr, unix.ESRCH) {
		return SupervisorExpectedAbsent, nil, nil
	}
	return "", nil, ErrIntervention
}

func sameOptionalProcess(left, right *ProcessIdentity) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
