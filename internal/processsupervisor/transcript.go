package processsupervisor

import (
	"bytes"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type CollectedTranscriptReadOptions struct {
	FixedMarshalPath         string
	ControlDirectory         *os.File
	ControlDirectoryIdentity ControlDirectoryIdentity
	Outcome                  VerifiedCommandOutcome
}

// CollectedTranscript returns typed bytes only after descriptor-relative
// identity, length, digest and manifest checks. It exposes no owner-object
// filename or path authority.
type CollectedTranscript struct {
	Stdout           []byte        `json:"-"`
	Stderr           []byte        `json:"-"`
	Report           ProcessReport `json:"report"`
	TranscriptDigest string        `json:"transcriptDigest"`
	Truncated        bool          `json:"truncated"`
}

func validateCollectedTranscript(outcome VerifiedCommandOutcome, stdout, stderr, manifest []byte) (CollectedTranscript, error) {
	if outcome.Command != CommandCollect || outcome.Status != "ok" || outcome.Disposition != "ok" || outcome.ReasonCode != "transcript-collected" || outcome.ProcessReport == nil || outcome.ProcessReport.State != "terminal" || !validDigest(outcome.TranscriptDigest) || !validDigest(outcome.ObservationDigest) {
		return CollectedTranscript{}, ErrInvalid
	}
	report := *outcome.ProcessReport
	var manifestReport ProcessReport
	manifestDigest := canonical.DigestBytes(manifest)
	if ValidateProcessReport(report) != nil || uint64(len(stdout)) != report.StdoutBytes || uint64(len(stderr)) != report.StderrBytes || canonical.DigestBytes(stdout) != report.StdoutDigest || canonical.DigestBytes(stderr) != report.StderrDigest || manifestDigest != outcome.TranscriptDigest || manifestDigest != outcome.ObservationDigest || strictCanonicalDecode(manifest, &manifestReport) != nil || manifestReport != report {
		return CollectedTranscript{}, ErrConflict
	}
	canonicalReport, err := canonicalValue(report)
	if err != nil || !bytes.Equal(canonicalReport, manifest) || outcome.StdoutBytes != report.StdoutBytes || outcome.StderrBytes != report.StderrBytes || outcome.Truncated != report.TranscriptTruncated {
		return CollectedTranscript{}, ErrConflict
	}
	return CollectedTranscript{Stdout: append([]byte(nil), stdout...), Stderr: append([]byte(nil), stderr...), Report: report, TranscriptDigest: outcome.TranscriptDigest, Truncated: outcome.Truncated}, nil
}
