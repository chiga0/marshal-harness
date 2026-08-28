//go:build !darwin

package processsupervisor

func ReadCollectedTranscript(CollectedTranscriptReadOptions) (CollectedTranscript, error) {
	return CollectedTranscript{}, ErrUnavailable
}
