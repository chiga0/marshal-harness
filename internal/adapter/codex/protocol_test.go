package codex

import (
	"strings"
	"testing"
)

func TestDecodeEventLineExtractsSessionTokensAndToolCalls(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}
{"type":"item.completed","item":{"id":"i1","type":"command","role":"assistant","status":"completed"}}
{"type":"item.completed","item":{"id":"i2","type":"function_call","role":"assistant","status":"completed"}}
{"type":"turn.completed","turn":{"id":"turn-1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "thread-1" || result.eventCount != 4 || result.toolCalls != 2 || result.inputTokens != 10 || result.outputTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(result.toolNames, ",") != "command,function_call" {
		t.Fatalf("toolNames = %v", result.toolNames)
	}
}

func TestDecodeEventLineUsesThreadIDAsSessionFallback(t *testing.T) {
	stream := `{"type":"thread.started","thread":{"id":"thread-2"}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || result.sessionID != "thread-2" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeEventLineClassifiesPermissionDenials(t *testing.T) {
	stream := `{"type":"item.completed","item":{"id":"i","type":"command","role":"assistant","status":"error","error":"permission denied","input":{"command":"curl http://evil.example"}}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.denials) != 1 || result.denials[0].Tool != "command" {
		t.Fatalf("denials = %+v", result.denials)
	}
	if len(result.toolNames) != 0 {
		t.Fatalf("denied tool must not be recorded as a success: %v", result.toolNames)
	}
}

func TestDecodeEventLineIgnoresPermissionWordsInMessageText(t *testing.T) {
	stream := `{"type":"item.completed","item":{"id":"i","type":"message","role":"assistant","content":[{"type":"output_text","text":"permission denied is documentation"}]}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.denials) != 0 || result.toolCalls != 0 {
		t.Fatalf("message item must not count as a tool call or denial: %+v", result)
	}
}

func TestDecodeEventLineRejectsMalformedAndBlank(t *testing.T) {
	for _, input := range []string{"not-json\n", "\n", "   \n"} {
		result := decodeTranscript([]byte(input))
		if result.err == nil {
			t.Fatalf("input %q did not produce a protocol error", input)
		}
	}
	// An empty stream is not a protocol error on its own; session presence is
	// enforced separately by Run.
	if result := decodeTranscript(nil); result.err != nil {
		t.Fatalf("empty stream must not error: %v", result.err)
	}
}
