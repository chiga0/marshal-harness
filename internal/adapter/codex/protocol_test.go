package codex

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func captureForTest(t *testing.T, input string, limit int64) (captureResult, int) {
	t.Helper()
	terminations := 0
	result := captureJSONL(strings.NewReader(input), limit, func() { terminations++ })
	return result, terminations
}

func TestCaptureJSONLAcceptsFrozenSuccess(t *testing.T) {
	input := strings.Join(successTranscriptLines(), "\n") + "\n"
	result, terminations := captureForTest(t, input, 65536)
	if result.err != nil {
		t.Fatalf("err = %v", result.err)
	}
	if terminations != 0 {
		t.Fatalf("terminations = %d", terminations)
	}
	if result.threadID != "thread-1" || !result.sawTerminal || result.eventCount != 4 || result.turnCount != 1 || result.itemCount != 1 || result.inputTokens != 11 || result.outputTokens != 7 {
		t.Fatalf("capture = %+v", result)
	}
	// raw 证据逐字节保留全部 stdout。
	if got, want := string(result.raw), input; got != want {
		t.Fatalf("raw = %q, want %q", got, want)
	}
}

func TestCaptureJSONLAcceptsCodex01450RealSmokeSequence(t *testing.T) {
	// 真实 Codex 0.145.0 exec --json smoke：turn.started 没有 turn_id，
	// agent_message 直接以 item.completed 出现，没有 item.started 前导。
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019c75c5-4773-78c2-a1c8-4ca967d81f40"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"ok"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":256,"cached_input_tokens":128,"output_tokens":3}}`,
	}, "\n") + "\n"
	result, terminations := captureForTest(t, input, 65536)
	if result.err != nil || terminations != 0 {
		t.Fatalf("err=%v terminations=%d", result.err, terminations)
	}
	if result.threadID == "" || !result.sawTerminal || result.turnCount != 1 || result.itemCount != 1 || result.inputTokens != 256 || result.outputTokens != 3 {
		t.Fatalf("capture = %+v", result)
	}
	if got := string(result.raw); got != input {
		t.Fatalf("raw stdout lost byte fidelity: got %q want %q", got, input)
	}
}

func TestCaptureJSONLAcceptsRealSequenceWithoutUsage(t *testing.T) {
	// usage 计数只从已知字段提取且可缺省：缺省保持零值而不是失败。
	input := `{"type":"thread.started","thread_id":"thread-3"}` + "\n" +
		`{"type":"turn.started","thread_id":"thread-3","turn_id":"turn-1"}` + "\n" +
		`{"type":"item.started","thread_id":"thread-3","item":{"type":"command_execution"}}` + "\n" +
		`{"type":"item.completed","thread_id":"thread-3","item":{"type":"command_execution"}}` + "\n" +
		`{"type":"turn.completed","thread_id":"thread-3"}` + "\n"
	result, terminations := captureForTest(t, input, 65536)
	if result.err != nil || terminations != 0 {
		t.Fatalf("err = %v terminations = %d", result.err, terminations)
	}
	if result.threadID != "thread-3" || !result.sawTerminal || result.eventCount != 5 || result.turnCount != 1 || result.itemCount != 1 || result.inputTokens != 0 || result.outputTokens != 0 {
		t.Fatalf("capture = %+v", result)
	}
}

func TestCaptureJSONLAcceptsItemUpdatedInClosedSet(t *testing.T) {
	// item.updated 属于 0.145.0 已证实的 item 闭集；计数只累计 item.completed。
	input := `{"type":"thread.started","thread_id":"thread-1"}` + "\n" +
		`{"type":"turn.started","thread_id":"thread-1","turn_id":"turn-1"}` + "\n" +
		`{"type":"item.started","thread_id":"thread-1"}` + "\n" +
		`{"type":"item.updated","thread_id":"thread-1"}` + "\n" +
		`{"type":"item.completed","thread_id":"thread-1"}` + "\n" +
		`{"type":"turn.completed","thread_id":"thread-1","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"
	result, terminations := captureForTest(t, input, 65536)
	if result.err != nil || terminations != 0 {
		t.Fatalf("err = %v terminations = %d", result.err, terminations)
	}
	if result.itemCount != 1 || result.eventCount != 6 {
		t.Fatalf("capture = %+v, want itemCount=1 eventCount=6", result)
	}
}

func TestCaptureJSONLToleratesBlankLines(t *testing.T) {
	lines := successTranscriptLines()
	input := lines[0] + "\n\n   \n" + strings.Join(lines[1:], "\n") + "\n\n"
	result, _ := captureForTest(t, input, 65536)
	if result.err != nil || result.eventCount != 4 {
		t.Fatalf("blank lines must not be events: err=%v count=%d", result.err, result.eventCount)
	}
	if got := string(result.raw); got != input {
		t.Fatalf("raw stdout lost byte fidelity: got %q want %q", got, input)
	}
}

func TestCaptureJSONLPreservesWhitespaceAndFinalLineBytes(t *testing.T) {
	input := "  \n" + strings.Join(successTranscriptLines(), "\n")
	result, terminations := captureForTest(t, input, 65536)
	if result.err != nil || terminations != 0 {
		t.Fatalf("err=%v terminations=%d", result.err, terminations)
	}
	if got := string(result.raw); got != input {
		t.Fatalf("raw stdout = %q, want exact %q", got, input)
	}
}

func TestCaptureJSONLFailClosedMatrix(t *testing.T) {
	first := `{"type":"thread.started","thread_id":"thread-1"}`
	terminal := `{"type":"turn.completed","thread_id":"thread-1","usage":{"input_tokens":1,"output_tokens":1}}`
	turnStarted := `{"type":"turn.started","thread_id":"thread-1","turn_id":"turn-1"}`
	turnFailed := `{"type":"turn.failed","thread_id":"thread-1","error":"secret-provider-text"}`
	for _, test := range []struct {
		name     string
		input    string
		sentinel error
	}{
		{name: "empty-stream", input: "", sentinel: ErrProtocol},
		{name: "malformed-jsonl", input: "not-json\n", sentinel: ErrProtocol},
		{name: "first-event-not-thread-started", input: `{"type":"turn.started","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "first-event-missing-identity", input: `{"type":"thread.started"}` + "\n", sentinel: ErrProtocol},
		{name: "first-event-empty-identity", input: `{"type":"thread.started","thread_id":""}` + "\n", sentinel: ErrProtocol},
		{name: "identity-switch", input: first + "\n" + `{"type":"item.completed","thread_id":"thread-2"}` + "\n", sentinel: ErrProtocol},
		{name: "empty-thread-id-after-binding", input: first + "\n" + `{"type":"item.completed","thread_id":""}` + "\n", sentinel: ErrProtocol},
		{name: "duplicate-thread-started", input: first + "\n" + first + "\n", sentinel: ErrProtocol},
		{name: "unknown-event-type", input: first + "\n" + `{"type":"weird.event","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "unknown-item-evil", input: first + "\n" + `{"type":"item.evil","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "unknown-item-cancelled", input: first + "\n" + `{"type":"item.cancelled","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "missing-terminal", input: first + "\n" + `{"type":"item.completed","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "trailing-after-terminal", input: first + "\n" + turnStarted + "\n" + terminal + "\n" + `{"type":"item.completed","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "item-started-after-terminal", input: first + "\n" + turnStarted + "\n" + terminal + "\n" + `{"type":"item.started","thread_id":"thread-1"}` + "\n", sentinel: ErrProtocol},
		{name: "turn-failed", input: first + "\n" + turnStarted + "\n" + turnFailed + "\n", sentinel: ErrProviderFailed},
		{name: "failed-after-success-terminal", input: first + "\n" + turnStarted + "\n" + terminal + "\n" + turnFailed + "\n", sentinel: ErrProtocol},
		{name: "success-after-failed-terminal", input: first + "\n" + turnStarted + "\n" + turnFailed + "\n" + terminal + "\n", sentinel: ErrProviderFailed},
		{name: "negative-usage", input: first + "\n" + turnStarted + "\n" + `{"type":"turn.completed","thread_id":"thread-1","usage":{"input_tokens":-1,"output_tokens":1}}` + "\n", sentinel: ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, terminations := captureForTest(t, test.input, 65536)
			if !errors.Is(result.err, test.sentinel) {
				t.Fatalf("err = %v, want %v", result.err, test.sentinel)
			}
			if terminations == 0 {
				t.Fatal("protocol failure must terminate the process group")
			}
			if strings.Contains(result.err.Error(), "secret-provider-text") {
				t.Fatalf("provider free text leaked into error: %v", result.err)
			}
			if strings.Contains(result.err.Error(), "weird.event") {
				t.Fatalf("unknown provider event type leaked into error: %v", result.err)
			}
		})
	}
}

func TestCaptureJSONLRejectsOutOfOrderTurnAndItemEvents(t *testing.T) {
	thread := `{"type":"thread.started","thread_id":"thread-1"}`
	turn := `{"type":"turn.started","thread_id":"thread-1","turn_id":"turn-1"}`
	itemStart := `{"type":"item.started","thread_id":"thread-1"}`
	for _, input := range []string{
		thread + "\n" + `{"type":"item.started","thread_id":"thread-1"}` + "\n",
		thread + "\n" + turn + "\n" + `{"type":"item.updated","thread_id":"thread-1"}` + "\n",
		thread + "\n" + turn + "\n" + itemStart + "\n" + itemStart + "\n",
		thread + "\n" + turn + "\n" + itemStart + "\n" + `{"type":"turn.completed","thread_id":"thread-1"}` + "\n",
		thread + "\n" + turn + "\n" + turn + "\n",
	} {
		result, terminations := captureForTest(t, input, 65536)
		if !errors.Is(result.err, ErrProtocol) || terminations == 0 {
			t.Fatalf("input=%q err=%v terminations=%d", input, result.err, terminations)
		}
	}
}

func TestCaptureJSONLOutputLimitTerminatesUnterminatedLine(t *testing.T) {
	input := strings.Repeat("x", 5000)
	terminations := 0
	result := captureJSONL(strings.NewReader(input), 100, func() { terminations++ })
	if !result.limitExceeded || terminations != 1 {
		t.Fatalf("limitExceeded=%v terminations=%d, want immediate single termination", result.limitExceeded, terminations)
	}
	if result.err != nil {
		t.Fatalf("byte-limit termination is classified by ErrOutputLimit, not protocol: %v", result.err)
	}
	if len(result.raw) != 100 || string(result.raw) != input[:100] {
		t.Fatalf("raw must preserve the exact bounded stdout prefix, got %d bytes", len(result.raw))
	}
}

func TestCaptureJSONLOutputLimitKeepsPriorEventsBounded(t *testing.T) {
	first := `{"type":"thread.started","thread_id":"thread-1"}`
	large := `{"type":"item.completed","thread_id":"thread-1","item":{"type":"agent_message","text":"` + strings.Repeat("y", 900) + `"}}`
	result, terminations := captureForTest(t, first+"\n"+large+"\n", 512)
	if !result.limitExceeded || terminations == 0 {
		t.Fatalf("limitExceeded=%v terminations=%d", result.limitExceeded, terminations)
	}
	if int64(len(result.raw)) > 512 {
		t.Fatalf("raw exceeds limit: %d", len(result.raw))
	}
	if !strings.Contains(string(result.raw), `"thread_id":"thread-1"`) {
		t.Fatalf("events within the limit must be preserved: %s", result.raw)
	}
}

// closedReader 模拟 Marshal 终止进程组后关闭管道读端得到的错误。
type closedReader struct{}

func (closedReader) Read([]byte) (int, error) { return 0, os.ErrClosed }

func TestCaptureJSONLTreatsSupervisorClosedPipeAsBenignStop(t *testing.T) {
	// 监督侧关闭读端不是协议错误：分类交给 Run 的 context/limit 优先级，
	// 捕获本身不得把读端关闭上报为读失败或挂起。
	terminations := 0
	result := captureJSONL(closedReader{}, 65536, func() { terminations++ })
	if result.err != nil && strings.Contains(result.err.Error(), "file already closed") {
		t.Fatalf("closed pipe leaked a read error into the capture: %v", result.err)
	}
	if result.limitExceeded {
		t.Fatal("closed pipe must not be classified as an output-limit breach")
	}
}

func TestCaptureStreamTruncatesStderr(t *testing.T) {
	capture := captureStream(strings.NewReader(strings.Repeat("e", 100)), 10)
	if string(capture.data) != strings.Repeat("e", 10) || !capture.truncated {
		t.Fatalf("capture = %q/%v", capture.data, capture.truncated)
	}
	capture = captureStream(strings.NewReader("short"), 10)
	if string(capture.data) != "short" || capture.truncated {
		t.Fatalf("capture = %q/%v", capture.data, capture.truncated)
	}
}
