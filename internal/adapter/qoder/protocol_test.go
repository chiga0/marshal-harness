package qoder

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// classifyNow is the fixed reference time for terminal classification unit
// tests, so the typed failure construction can be replayed deterministically.
var classifyNow = time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

func TestDecodeEventLineExtractsSessionUsageAndTerminal(t *testing.T) {
	stream, err := os.ReadFile("testdata/qoder-1.1.23-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	result := decodeTranscript(stream)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "sess-1" || result.model != "provider/model" || result.cliVersion != "1.1.23" || result.protocolVersion != "1.2.0" || result.permissionMode != "acceptEdits" || result.assistantCount != 1 || result.eventCount != 3 || result.inputTokens != 10 || result.outputTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want a success terminal", result.terminal)
	}
}

func TestDecodeEventLineRecordsErrorTerminalCode(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"connection_failed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || result.terminal.success || result.terminal.code != "connection_failed" {
		t.Fatalf("terminal = %+v", result.terminal)
	}
}

func TestDecodeEventLineRejectsMalformedAndBlank(t *testing.T) {
	for _, input := range []string{"not-json\n", "\n", "   \n"} {
		result := decodeTranscript([]byte(input))
		if result.err == nil {
			t.Fatalf("input %q did not produce a protocol error", input)
		}
	}
	// An empty stream is not a protocol error on its own; session and terminal
	// presence are enforced separately by Run.
	if result := decodeTranscript(nil); result.err != nil {
		t.Fatalf("empty stream must not error: %v", result.err)
	}
}

func TestDecodeEventLineRejectsDuplicateAndUnrecognizedTerminal(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "follows terminal") {
			t.Fatalf("error = %v, want event-after-terminal error", result.err)
		}
	})
	t.Run("unrecognized-status", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"weird"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "contradictory") {
			t.Fatalf("error = %v, want contradictory terminal error", result.err)
		}
	})
}

func TestDecodeEventLineRejectsFabricatedLegacyContract(t *testing.T) {
	result := decodeTranscript([]byte("{\"type\":\"session\",\"id\":\"sess-1\"}\n"))
	if result.err == nil {
		t.Fatal("legacy fabricated session event was accepted")
	}
}

func TestClassifyTerminalFailureFollowsClosedCodeTable(t *testing.T) {
	for _, test := range []struct {
		code        string
		kind        port.FailureKind
		disposition port.RetryDisposition
	}{
		{code: "connection_failed", kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{code: "rate_limited", kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{code: "quota_exceeded", kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{code: "", kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{code: "mystery_code", kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
	} {
		t.Run(test.code, func(t *testing.T) {
			failure := classifyTerminalFailure(test.code, classifyNow)
			typed, ok := port.AsAdapterFailure(failure)
			if !ok {
				t.Fatalf("classification did not produce a typed failure: %v", failure)
			}
			if typed.Adapter != port.AdapterIDQoder || typed.Kind != test.kind || typed.Disposition != test.disposition {
				t.Fatalf("classification = %s/%s/%s, want qoder %s/%s", typed.Adapter, typed.Kind, typed.Disposition, test.kind, test.disposition)
			}
			wantDisposition, _ := port.DispositionFor(test.kind)
			if typed.Disposition != wantDisposition {
				t.Fatalf("disposition %s violates the unique pairing for %s", typed.Disposition, test.kind)
			}
			// Error() never echoes the provider code: only closed enums and
			// fixed detail vocabulary may appear.
			if strings.Contains(failure.Error(), test.code) && test.code != "" {
				t.Fatalf("Error() leaked the provider code %q: %s", test.code, failure.Error())
			}
		})
	}
}

func TestClassifyTerminalFailureReturnsPermanentDefault(t *testing.T) {
	failure := classifyTerminalFailure("", classifyNow)
	typed, ok := port.AsAdapterFailure(failure)
	if !ok || typed.Kind != port.FailureKindProviderTerminal || typed.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("empty code must default to provider-terminal/do-not-retry: %v", failure)
	}
	if strings.Contains(failure.Error(), "mystery") {
		t.Fatalf("Error() leaked unknown provider input: %s", failure.Error())
	}
}
