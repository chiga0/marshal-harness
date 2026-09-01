package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
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
	// 语义层的首个可判点依赖当前二进制的 build identity 状态：无副作用的
	// 默认测试进程（commit=="unknown"）在 contract 校验前被 identity 门禁
	// 拒绝；携带真实 source head 的测试二进制（CI make test ldflags）会通过
	// 身份门禁并按下一段 contract 校验裁决。
	semanticReason := "checker-build-identity-invalid"
	if isLowerHexCommit(buildinfo.Current().Commit) {
		semanticReason = "core-contract-invalid"
	}
	for _, test := range []struct {
		name       string
		input      string
		wantReason string
	}{
		{"ready token reaches semantic checker", "\x00{}", semanticReason},
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
	// 只有缺身份 material 的二进制才证明 unknown-identity 拒绝路径；携带真实
	// source head 的测试二进制上，同一输入的裁决属于上面的语义层覆盖。
	if isLowerHexCommit(buildinfo.Current().Commit) {
		t.Skip("test binary carries a real source head; unknown-identity rejection is covered when identity material is absent")
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
