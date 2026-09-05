//go:build darwin

package processsupervisor

func ReadCollectedTranscriptV2(options CollectedTranscriptReadOptionsV2) (CollectedTranscript, error) {
	return readCollectedTranscriptWithCoreV2(options, ObserveCurrentCore)
}

func readCollectedTranscriptWithCoreV2(options CollectedTranscriptReadOptionsV2, observeCore func(string) (CoreIdentity, error)) (CollectedTranscript, error) {
	o := options.Outcome
	if options.ControlDirectory == nil || observeCore == nil || !absoluteClean(options.FixedMarshalPath) || o.Validate() != nil ||
		o.Preparation.Command != CommandCollect || o.Status != "ok" || o.ReasonCode != "transcript-collected" || o.ProcessReport == nil || o.ProcessReport.State != "terminal" {
		return CollectedTranscript{}, ErrInvalid
	}
	a := o.PostCommand
	core, err := observeCore(options.FixedMarshalPath)
	if err != nil || core.UID != a.Binding.UID || core.GID != a.Binding.GID || !sameBinaryObject(core.Binary, a.Binding.FixedBinary) {
		return CollectedTranscript{}, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || !sameControlDirectoryObject(directory, a.ControlDirectory) {
		return CollectedTranscript{}, ErrConflict
	}
	held, err := openHeldSessionControlFilesForLeaf(options.ControlDirectory, a.Binding.ControlFiles, journalFileNameV2)
	if err != nil {
		return CollectedTranscript{}, ErrConflict
	}
	defer held.close()
	boundary := sessionControlBoundary{directory: options.ControlDirectory, directoryIdentity: directory, socket: a.Binding.ControlSocket, controlFiles: a.Binding.ControlFiles, heldFiles: held}
	before, err := captureAttachControlSnapshotV2(boundary, a)
	if err != nil {
		return CollectedTranscript{}, err
	}
	state, err := readHeldJournalStateV2(held.journal)
	if err != nil || state.collected == nil || state.collected.Request == nil || state.collected.Response == nil ||
		state.collected.Request.RequestDigest != o.Preparation.RequestDigest || state.collected.Response.ReceiptDigest != o.ReceiptDigest {
		return CollectedTranscript{}, ErrConflict
	}
	transcript, err := readCollectedTranscriptV2(options.ControlDirectory, *state.collected)
	if err != nil {
		return CollectedTranscript{}, err
	}
	if transcript.Report != *o.ProcessReport || transcript.TranscriptDigest != o.TranscriptDigest || transcript.Truncated != o.Truncated {
		return CollectedTranscript{}, ErrConflict
	}
	after, err := captureAttachControlSnapshotV2(boundary, a)
	if err != nil || after != before {
		return CollectedTranscript{}, ErrConflict
	}
	return transcript, nil
}
