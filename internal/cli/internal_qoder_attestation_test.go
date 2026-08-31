package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
)

type failingTranscriptReader struct{}

func (failingTranscriptReader) Read([]byte) (int, error) {
	return 0, errors.New("fixture read failure")
}

func TestInternalQoderTranscriptCheckIsHiddenAndFailsClosed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"help"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("help exit = %d", exit)
	}
	if strings.Contains(stdout.String(), "qoder-transcript-check") || strings.Contains(stdout.String(), "internal") {
		t.Fatal("internal transcript command must not appear in public help")
	}

	for name, input := range map[string]string{
		"unknown field":   `{"future":true}`,
		"duplicate field": `{"subject":{},"subject":{}}`,
		"trailing json":   `{} {}`,
		"non-finite":      `{"subject":{},"transcript":NaN}`,
	} {
		t.Run(name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			exit := Run([]string{"internal", "qoder-transcript-check"}, strings.NewReader(input), &stdout, &stderr)
			if exit != ExitFailure {
				t.Fatalf("exit = %d, want %d", exit, ExitFailure)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected stdout: %q", stdout.String())
			}
			var failure map[string]string
			if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil {
				t.Fatalf("decode failure: %v", err)
			}
			if len(failure) != 2 || failure["status"] != "fail" || failure["reasonCode"] != "checker-input-invalid" || strings.Count(stderr.String(), "\n") != 1 {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestInternalQoderTranscriptCheckBoundsInputAndArguments(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		input      string
		wantExit   int
		wantReason string
	}{
		{"argument", []string{"internal", "qoder-transcript-check", "extra"}, "", ExitUsage, "checker-arguments-invalid"},
		{"exact bound", []string{"internal", "qoder-transcript-check"}, strings.Repeat("x", int(qoderTranscriptCheckMaxInputBytes)), ExitFailure, "checker-input-invalid"},
		{"oversize", []string{"internal", "qoder-transcript-check"}, strings.Repeat("x", int(qoderTranscriptCheckMaxInputBytes+1)), ExitFailure, "checker-input-too-large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := Run(test.args, strings.NewReader(test.input), &stdout, &stderr); exit != test.wantExit {
				t.Fatalf("exit = %d, want %d", exit, test.wantExit)
			}
			if !strings.Contains(stderr.String(), `"reasonCode":"`+test.wantReason+`"`) {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestInternalQoderTranscriptCheckStableHandshake(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("qoder transcript build-identity checks follow the Darwin signed-binary identity contract")
	}
	for _, test := range []struct {
		name       string
		input      string
		wantReason string
	}{
		{"ready token reaches semantic checker", "\x00{}", "checker-build-identity-invalid"},
		{"missing ready token fails closed", "{}", "checker-handshake-invalid"},
		{"wrong ready token fails closed", "x{}", "checker-handshake-invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := Run([]string{"internal", "qoder-transcript-check", "--attestation-ready"}, strings.NewReader(test.input), &stdout, &stderr)
			if exit != ExitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"reasonCode":"`+test.wantReason+`"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInternalQoderTranscriptCheckRejectsUnknownBuildIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("qoder transcript build-identity checks follow the Darwin signed-binary identity contract")
	}
	input := `{"subject":{},"transcript":"","transcriptMeta":"","workerRequest":"","workerResult":"","taskSpec":"","capabilitySnapshot":"","profile":""}`
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "qoder-transcript-check"}, strings.NewReader(input), &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("exit = %d, want %d", exit, ExitFailure)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), `"reasonCode":"checker-build-identity-invalid"`) {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestInternalQoderTranscriptCheckClosesInputReadFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "qoder-transcript-check"}, failingTranscriptReader{}, &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("exit = %d, want %d", exit, ExitFailure)
	}
	if stdout.Len() != 0 || stderr.String() != "{\"status\":\"fail\",\"reasonCode\":\"checker-input-read-failed\"}\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
