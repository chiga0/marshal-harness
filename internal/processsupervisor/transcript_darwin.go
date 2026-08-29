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
	if err != nil || directory != options.ControlDirectoryIdentity || revalidateTranscriptBoundary(options.ControlDirectory, directory, outcome.Recovery.PostCommand) != nil {
		return CollectedTranscript{}, ErrConflict
	}
	stdout, stdoutIdentity, err := readTranscriptObject(options.ControlDirectory, stdoutObjectName, MaxStdoutBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	stderr, stderrIdentity, err := readTranscriptObject(options.ControlDirectory, stderrObjectName, MaxStderrBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	manifest, manifestIdentity, err := readTranscriptObject(options.ControlDirectory, transcriptObjectName, MaxDiagnosticBytes)
	if err != nil {
		return CollectedTranscript{}, err
	}
	transcript, err := validateCollectedTranscript(outcome, stdout, stderr, manifest)
	if err != nil {
		return CollectedTranscript{}, err
	}
	for name, expected := range map[string]ControlFileIdentity{stdoutObjectName: stdoutIdentity, stderrObjectName: stderrIdentity, transcriptObjectName: manifestIdentity} {
		observed, _, observeErr := observeControlFileAt(options.ControlDirectory, name)
		if observeErr != nil || observed != expected {
			return CollectedTranscript{}, ErrConflict
		}
	}
	if revalidateTranscriptBoundary(options.ControlDirectory, directory, outcome.Recovery.PostCommand) != nil {
		return CollectedTranscript{}, ErrConflict
	}
	return transcript, nil
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
	return data, identity, nil
}

func revalidateTranscriptBoundary(directory *os.File, identity ControlDirectoryIdentity, anchor HandshakeAnchor) error {
	if revalidateControlDirectory(directory, identity) != nil || observeControlSocketExact(directory, anchor.ControlSocket) != nil {
		return ErrConflict
	}
	held, err := openHeldSessionControlFiles(directory, anchor.ControlFiles)
	if err != nil {
		return ErrConflict
	}
	defer held.close()
	return revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles)
}
