//go:build darwin

package processsupervisor

import (
	"bytes"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// revalidateV2 keeps identity and output-object checks shared, but derives
// phase solely from v2 state and never routes v2 bytes through a v1 snapshot.
func (boundary sessionControlBoundary) revalidateV2(state journalStateV2) error {
	if boundary.directory == nil || boundary.directoryIdentity.validate() != nil || boundary.socket.validate() != nil || boundary.controlFiles.validate() != nil || boundary.heldFiles == nil || state.sequence == 0 {
		return ErrInvalid
	}
	allowed := []controlDirectoryEntrySet{controlDirectoryRuntimeBase}
	if state.collected != nil {
		allowed = []controlDirectoryEntrySet{controlDirectoryCollected}
	} else if state.pending != nil && state.pending.Request.Command == CommandCollect {
		allowed = []controlDirectoryEntrySet{controlDirectoryRuntimeBase, controlDirectoryCollectOne, controlDirectoryCollectTwo, controlDirectoryCollected}
	}
	if revalidateControlDirectoryEntriesForLeaf(boundary.directory, boundary.directoryIdentity, false, journalFileNameV2, allowed...) != nil ||
		observeControlSocketExact(boundary.directory, boundary.socket) != nil ||
		revalidateHeldSessionControlFilesForLeaf(boundary.directory, boundary.heldFiles, boundary.controlFiles, journalFileNameV2) != nil {
		return ErrConflict
	}
	if state.collected != nil {
		if _, err := readCollectedTranscriptV2(boundary.directory, *state.collected); err != nil {
			return ErrConflict
		}
	}
	return nil
}

func readCollectedTranscriptV2(directory *os.File, record journalRecordV2) (CollectedTranscript, error) {
	if record.Request == nil || record.Response == nil || record.Request.Command != CommandCollect || record.Response.Status != "ok" ||
		record.Response.ReasonCode != "transcript-collected" || validateStoredResponseV2(*record.Response, *record.Request) != nil {
		return CollectedTranscript{}, ErrConflict
	}
	var result MechanicsResult
	var report ProcessReport
	if strictCanonicalDecode(record.Response.Payload, &result) != nil || strictCanonicalDecode(result.Payload, &report) != nil || ValidateDormantV2ProcessReport(report) != nil || report.State != "terminal" {
		return CollectedTranscript{}, ErrConflict
	}
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
	canonicalReport, err := canonicalValue(report)
	if err != nil || !bytes.Equal(canonicalReport, manifest) || canonical.DigestBytes(manifest) != result.TranscriptDigest ||
		uint64(len(stdout)) != report.StdoutBytes || uint64(len(stderr)) != report.StderrBytes ||
		canonical.DigestBytes(stdout) != report.StdoutDigest || canonical.DigestBytes(stderr) != report.StderrDigest {
		return CollectedTranscript{}, ErrConflict
	}
	return CollectedTranscript{Stdout: stdout, Stderr: stderr, Report: report, TranscriptDigest: result.TranscriptDigest, Truncated: result.Truncated}, nil
}
