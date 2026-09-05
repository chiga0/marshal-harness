package processsupervisor

import "os"

type CollectedTranscriptReadOptionsV2 struct {
	FixedMarshalPath string
	ControlDirectory *os.File
	Outcome          VerifiedCommandOutcomeV2
}
