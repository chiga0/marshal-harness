//go:build !darwin

package processsupervisor

func ReadCollectedTranscript(CollectedTranscriptReadOptions) (CollectedTranscript, error) {
	return CollectedTranscript{}, ErrUnavailable
}

func ReadCollectedTranscriptV2(CollectedTranscriptReadOptionsV2) (CollectedTranscript, error) {
	return CollectedTranscript{}, ErrUnavailable
}
