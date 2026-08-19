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

func TestDecodeEventLineRecordsErrorTerminalWithSuccessSubtype(t *testing.T) {
	// Real qodercli 1.1.23 emits an error result with subtype=success and an
	// `error` code; the adapter must classify it as a provider error rather
	// than a contradictory protocol violation, and must not leak the code.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"completed","error":"authentication_failed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || result.terminal.success || result.terminal.code != "authentication_failed" {
		t.Fatalf("terminal = %+v, want error terminal with code authentication_failed", result.terminal)
	}
	failure := classifyTerminalFailure(result.terminal.code, classifyNow)
	typed, ok := port.AsAdapterFailure(failure)
	if !ok || typed.Kind != port.FailureKindProviderTerminal || typed.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("classification = %v, want provider-terminal/do-not-retry", failure)
	}
	if strings.Contains(failure.Error(), "authentication_failed") {
		t.Fatalf("Error() leaked the provider error code: %s", failure.Error())
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
	t.Run("contradictory-status", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":false,"terminal_reason":"completed"}
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

func TestDecodeEventLineAcceptsObservedHookFramesBeforeInit(t *testing.T) {
	// Real qodercli 1.1.23 emits hook_started/hook_progress/hook_response
	// system frames before system init; they are non-semantic and must not
	// fail the transcript or disturb the init/assistant/result sequence.
	stream := `{"type":"system","subtype":"hook_started"}
{"type":"system","subtype":"hook_progress"}
{"type":"system","subtype":"hook_response"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","usage":{"input_tokens":10,"output_tokens":5}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "sess-1" || result.model != "provider/model" || result.assistantCount != 1 || result.eventCount != 6 {
		t.Fatalf("result = %+v", result)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want success terminal after hook frames", result.terminal)
	}
}

func TestDecodeEventLineRejectsUnknownSystemSubtypeAndDuplicateInit(t *testing.T) {
	t.Run("unknown-subtype", func(t *testing.T) {
		stream := `{"type":"system","subtype":"mystery"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "unrecognized system event subtype") {
			t.Fatalf("error = %v, want unrecognized system event subtype error", result.err)
		}
	})
	t.Run("duplicate-init", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "invalid or duplicate system init event") {
			t.Fatalf("error = %v, want duplicate init error", result.err)
		}
	})
	t.Run("session-change", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]},"session_id":"sess-2"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "session id changed") {
			t.Fatalf("error = %v, want session change error", result.err)
		}
	})
	t.Run("hook-after-terminal", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
{"type":"system","subtype":"hook_response"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "follows terminal") {
			t.Fatalf("error = %v, want event-after-terminal error", result.err)
		}
	})
}

func TestDecodeEventLineAcceptsSuccessWithoutTerminalReason(t *testing.T) {
	// Real logged-in qodercli 1.1.23 may emit a success result without a
	// terminal_reason field; success must not require it as long as the
	// session, subtype and at least one assistant message are present.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want success terminal without terminal_reason", result.terminal)
	}
}

func TestDecodeEventLineRejectsErrorTerminalWithoutCode(t *testing.T) {
	// An error terminal must still yield a fixed classification via `error`
	// or terminal_reason; with neither it is an incomplete protocol event.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":true}
`
	result := decodeTranscript([]byte(stream))
	if result.err == nil || !strings.Contains(result.err.Error(), "error terminal has no code") {
		t.Fatalf("error = %v, want error terminal no code error", result.err)
	}
}
