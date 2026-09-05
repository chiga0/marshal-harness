//go:build darwin

package processsupervisor

import (
	"context"
	"fmt"
)

// ObservePreparedCommandV2 reads the exact held v2 journal, never repairing a
// tail, advancing an owner or opening a writable descriptor. No PATH lookup,
// process launch, signal, reconnect or replay occurs here.
func ObservePreparedCommandV2(ctx context.Context, options PreparedJournalOptionsV2) (PreparedJournalObservationV2, error) {
	if ctx == nil || ctx.Err() != nil || options.ControlDirectory == nil || options.Prepared.evidence.Validate() != nil {
		return PreparedJournalObservationV2{}, ErrInvalid
	}
	a := options.Prepared.evidence.PreCommand
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || !sameControlDirectoryObject(directory, a.ControlDirectory) {
		return PreparedJournalObservationV2{}, fmt.Errorf("prepared journal directory: %w", ErrConflict)
	}
	held, err := openHeldSessionControlFilesForLeaf(options.ControlDirectory, a.Binding.ControlFiles, journalFileNameV2)
	if err != nil {
		return PreparedJournalObservationV2{}, fmt.Errorf("prepared journal held files: %w", ErrConflict)
	}
	defer held.close()
	if _, err := readSessionNonce(held, a.Binding.SessionNonceDigest); err != nil {
		return PreparedJournalObservationV2{}, fmt.Errorf("prepared journal nonce: %w", ErrConflict)
	}
	boundary := sessionControlBoundary{directory: options.ControlDirectory, directoryIdentity: directory, socket: a.Binding.ControlSocket, controlFiles: a.Binding.ControlFiles, heldFiles: held}
	state, err := readHeldJournalStateV2(held.journal)
	if err != nil || boundary.revalidateV2(state) != nil {
		return PreparedJournalObservationV2{}, ErrIntervention
	}
	observation, err := classifyPreparedJournalV2(state, options.Prepared)
	if err != nil {
		return PreparedJournalObservationV2{}, fmt.Errorf("prepared journal command checkpoint: %w", err)
	}
	after, err := readHeldJournalStateV2(held.journal)
	if err != nil || after.sequence != state.sequence || after.head != state.head || boundary.revalidateV2(after) != nil || ctx.Err() != nil {
		return PreparedJournalObservationV2{}, ErrIntervention
	}
	if _, err := readSessionNonce(held, a.Binding.SessionNonceDigest); err != nil {
		return PreparedJournalObservationV2{}, ErrConflict
	}
	return observation, nil
}
