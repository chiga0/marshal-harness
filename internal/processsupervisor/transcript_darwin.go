//go:build darwin

package processsupervisor

import (
	"io"
	"os"
)

const (
	stdoutObjectName     = "stdout.bin"
	stderrObjectName     = "stderr.bin"
	transcriptObjectName = "transcript.jcs"
)

func ReadCollectedTranscript(options CollectedTranscriptReadOptions) (CollectedTranscript, error) {
	outcome := options.Outcome
	if options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.ControlDirectoryIdentity.validate() != nil || outcome.Command != CommandCollect || outcome.Status != "ok" || outcome.Disposition != "ok" || outcome.ProcessReport == nil || outcome.ProcessReport.State != "terminal" || !validDigest(outcome.TranscriptDigest) || outcome.Recovery.PostCommand.ControlFiles.validate() != nil {
		return CollectedTranscript{}, ErrInvalid
	}
	core, err := ObserveCurrentCore(options.FixedMarshalPath)
	if err != nil || core.UID != outcome.Recovery.PostCommand.UID || core.GID != outcome.Recovery.PostCommand.GID || !sameBinaryObject(core.Binary, outcome.Recovery.PostCommand.FixedBinary) {
		return CollectedTranscript{}, ErrConflict
	}
	directory, err := ObserveHeldControlDirectory(options.ControlDirectory)
	if err != nil || !sameControlDirectoryObject(directory, options.ControlDirectoryIdentity) || revalidateTranscriptBoundary(options.ControlDirectory, directory, outcome.Recovery.PostCommand) != nil {
		return CollectedTranscript{}, ErrConflict
	}
	transcript, err := readAndValidateCollectedTranscript(options.ControlDirectory, outcome)
	if err != nil {
		return CollectedTranscript{}, err
	}
	if revalidateTranscriptBoundary(options.ControlDirectory, directory, outcome.Recovery.PostCommand) != nil {
		return CollectedTranscript{}, ErrConflict
	}
	return transcript, nil
}

// readAndValidateCollectedTranscript is the single Darwin implementation for
// closing output object identity/size and stored content semantics. Runtime
// phase checks and the public transcript reader both use this path.
func readAndValidateCollectedTranscript(directory *os.File, outcome VerifiedCommandOutcome) (CollectedTranscript, error) {
	stdout, _, err := readTranscriptObject(directory, stdoutObjectName, MaxStdoutBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	stderr, _, err := readTranscriptObject(directory, stderrObjectName, MaxStderrBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	manifest, _, err := readTranscriptObject(directory, transcriptObjectName, MaxDiagnosticBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	transcript, err := validateCollectedTranscript(outcome, stdout, stderr, manifest)
	if err != nil {
		return CollectedTranscript{}, err
	}
	return transcript, nil
}

func validatePresentOutputObjects(directory *os.File, entries controlDirectoryEntrySet) error {
	for _, object := range []struct {
		bit   controlDirectoryEntrySet
		name  string
		limit int
	}{
		{bit: controlDirectoryStdout, name: stdoutObjectName, limit: MaxStdoutBytes},
		{bit: controlDirectoryStderr, name: stderrObjectName, limit: MaxStderrBytes},
		{bit: controlDirectoryTranscript, name: transcriptObjectName, limit: MaxDiagnosticBytes},
	} {
		if entries&object.bit == 0 {
			continue
		}
		if _, _, err := readTranscriptObject(directory, object.name, object.limit); err != nil {
			return ErrConflict
		}
	}
	return nil
}

func validateStoredCollectedTranscript(directory *os.File, command replayedCommand) error {
	projection, response := command.Projection, command.Response
	if projection.Command != CommandCollect || response.Status != "ok" || response.ReasonCode != "transcript-collected" || validateStoredResponse(response, projection) != nil {
		return ErrConflict
	}
	var result MechanicsResult
	var report ProcessReport
	if strictCanonicalDecode(response.Payload, &result) != nil || validateMechanicsResult(result) != nil || result.Disposition != "ok" || result.ReasonCode != response.ReasonCode || strictCanonicalDecode(result.Payload, &report) != nil || ValidateProcessReport(report) != nil {
		return ErrConflict
	}
	digest, err := digestValue(report)
	if err != nil || result.ObservationDigest != digest || result.TranscriptDigest != digest || result.StdoutBytes != report.StdoutBytes || result.StderrBytes != report.StderrBytes || result.Truncated != report.TranscriptTruncated {
		return ErrConflict
	}
	outcome := VerifiedCommandOutcome{
		Command: CommandCollect, CommandID: projection.CommandID, Sequence: projection.Sequence, Status: response.Status, Disposition: result.Disposition, ReasonCode: response.ReasonCode,
		RequestDigest: projection.RequestDigest, ReceiptDigest: response.ReceiptDigest, ObservationDigest: response.ObservationDigest, CommandHead: response.CommandHead,
		TranscriptDigest: result.TranscriptDigest, StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes, Truncated: result.Truncated, ProcessReport: &report,
	}
	_, err = readAndValidateCollectedTranscript(directory, outcome)
	return err
}

func readTranscriptObject(directory *os.File, name string, limit int) ([]byte, ControlFileIdentity, error) {
	file, err := openControlFileAt(directory, name)
	if err != nil {
		return nil, ControlFileIdentity{}, err
	}
	defer file.Close()
	identity, size, err := observeControlFile(file)
	if err != nil || size < 0 || size > int64(limit) {
		return nil, ControlFileIdentity{}, ErrConflict
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, size))
	if err != nil || int64(len(data)) != size {
		return nil, ControlFileIdentity{}, ErrIntervention
	}
	after, afterSize, err := observeControlFile(file)
	if err != nil || after != identity || afterSize != size {
		return nil, ControlFileIdentity{}, ErrConflict
	}
	current, currentSize, err := observeControlFileAt(directory, name)
	if err != nil || current != identity || currentSize != size {
		return nil, ControlFileIdentity{}, ErrConflict
	}
	return data, identity, nil
}

func revalidateTranscriptBoundary(directory *os.File, identity ControlDirectoryIdentity, anchor HandshakeAnchor) error {
	if revalidateControlDirectoryEntries(directory, identity, false, controlDirectoryCollected) != nil || observeControlSocketExact(directory, anchor.ControlSocket) != nil {
		return ErrConflict
	}
	held, err := openHeldSessionControlFiles(directory, anchor.ControlFiles)
	if err != nil {
		return ErrConflict
	}
	defer held.close()
	return revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles)
}
