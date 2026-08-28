package processsupervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestValidateCollectedTranscriptBindsStoredBytesAndManifest(t *testing.T) {
	stdout, stderr := []byte("bounded stdout"), []byte("bounded stderr")
	report := ProcessReport{
		State: "terminal", ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Date(2026, 8, 29, 3, 3, 5, 0, time.UTC).Format(time.RFC3339Nano),
		Process: validBootstrap().Core.Process, RuntimeObjectDigest: digest("a"), WorkingObjectDigest: digest("b"),
		StdoutDigest: canonical.DigestBytes(stdout), StderrDigest: canonical.DigestBytes(stderr), StdoutBytes: uint64(len(stdout)), StderrBytes: uint64(len(stderr)), TranscriptTruncated: true,
	}
	manifest := mustCanonical(report)
	outcome := VerifiedCommandOutcome{Command: CommandCollect, Status: "ok", Disposition: "ok", ReasonCode: "transcript-collected", ObservationDigest: canonical.DigestBytes(manifest), TranscriptDigest: canonical.DigestBytes(manifest), StdoutBytes: report.StdoutBytes, StderrBytes: report.StderrBytes, Truncated: true, ProcessReport: &report}
	transcript, err := validateCollectedTranscript(outcome, stdout, stderr, manifest)
	if err != nil {
		t.Fatal(err)
	}
	stdout[0] = 'X'
	if string(transcript.Stdout) != "bounded stdout" {
		t.Fatal("validated transcript aliases caller-owned bytes")
	}
	for name, mutate := range map[string]func(*VerifiedCommandOutcome, *[]byte, *[]byte, *[]byte){
		"stdout-digest": func(_ *VerifiedCommandOutcome, value *[]byte, _ *[]byte, _ *[]byte) { (*value)[0] ^= 1 },
		"stderr-length": func(_ *VerifiedCommandOutcome, _ *[]byte, value *[]byte, _ *[]byte) {
			*value = (*value)[:len(*value)-1]
		},
		"manifest":      func(_ *VerifiedCommandOutcome, _ *[]byte, _ *[]byte, value *[]byte) { (*value)[0] ^= 1 },
		"outcome-count": func(value *VerifiedCommandOutcome, _ *[]byte, _ *[]byte, _ *[]byte) { value.StdoutBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := outcome
			candidateStdout := append([]byte(nil), []byte("bounded stdout")...)
			candidateStderr := append([]byte(nil), stderr...)
			candidateManifest := append([]byte(nil), manifest...)
			mutate(&candidate, &candidateStdout, &candidateStderr, &candidateManifest)
			if _, err := validateCollectedTranscript(candidate, candidateStdout, candidateStderr, candidateManifest); !errors.Is(err, ErrConflict) {
				t.Fatalf("validation error=%v", err)
			}
		})
	}
}
