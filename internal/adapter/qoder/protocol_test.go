package qoder

import (
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// classifyNow is the fixed reference time for terminal classification unit
// tests, so the typed failure construction can be replayed deterministically.
var classifyNow = time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

func TestDecodeEventLineExtractsSessionUsageAndTerminal(t *testing.T) {
	stream := `{"type":"session","id":"sess-1"}
{"type":"usage","input_tokens":10,"output_tokens":5}
{"type":"result","status":"success"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "sess-1" || result.eventCount != 3 || result.inputTokens != 10 || result.outputTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want a success terminal", result.terminal)
	}
}

func TestDecodeEventLineRecordsErrorTerminalCode(t *testing.T) {
	stream := `{"type":"session","id":"sess-1"}
{"type":"result","status":"error","code":"connection_failed"}
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
		stream := `{"type":"session","id":"sess-1"}
{"type":"result","status":"success"}
{"type":"result","status":"success"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "duplicate") {
			t.Fatalf("error = %v, want duplicate terminal error", result.err)
		}
	})
	t.Run("unrecognized-status", func(t *testing.T) {
		stream := `{"type":"session","id":"sess-1"}
{"type":"result","status":"weird"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "unrecognized status") {
			t.Fatalf("error = %v, want unrecognized status error", result.err)
		}
	})
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
