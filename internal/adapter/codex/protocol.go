package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
)

// captureResult aggregates the bounded, decoded JSONL transcript emitted by
// `codex exec --json`. Free text such as item content never reaches
// authorization or budgets; it survives only inside the raw transcript
// evidence.
type captureResult struct {
	raw           []byte
	sessionID     string
	eventCount    int
	toolCalls     int
	inputTokens   int
	outputTokens  int
	denials       []denials.RawDenial
	toolNames     []string
	limitExceeded bool
	err           error
}

// codexEvent covers only the JSONL fields Marshal relies on. Unknown fields
// are ignored on purpose; protocol decisions rely solely on type, thread_id,
// thread/turn/item structure, and the explicit item error status.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Thread   struct {
		ID    string     `json:"id"`
		Usage codexUsage `json:"usage"`
	} `json:"thread"`
	Turn struct {
		ID     string     `json:"id"`
		Status string     `json:"status"`
		Usage  codexUsage `json:"usage"`
	} `json:"turn"`
	Item struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Role   string          `json:"role"`
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Input  json.RawMessage `json:"input"`
	} `json:"item"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// codexToolCallTypes is the closed set of item types that count as tool calls
// for allowlist reconciliation.
var codexToolCallTypes = map[string]bool{
	"command": true, "function_call": true, "custom_tool_call": true,
	"mcp_tool_call": true, "local_shell_call": true,
}

// captureJSONL reads a bounded JSONL stream and folds each complete LF
// terminated line into the capture aggregate. Raw bytes are preserved
// byte-for-byte in input order; a malformed line is a protocol error and
// terminates the process group exactly once through onLimit. Output is
// bounded: exceeding the limit keeps raw equal to the first limit input bytes
// and terminates exactly once without fabricating a protocol success.
func captureJSONL(reader io.Reader, limit int64, onLimit func()) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var consumed int64
	var line []byte
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 {
			consumed += int64(len(fragment))
			if consumed > limit {
				if !result.limitExceeded {
					result.limitExceeded = true
					onLimit()
				}
				line = nil
			} else if !result.limitExceeded {
				line = append(line, fragment...)
			}
			complete := !errors.Is(err, bufio.ErrBufferFull)
			if complete && len(line) > 0 && !result.limitExceeded {
				result.raw = append(result.raw, line...)
				if decodeErr := result.decodeEventLine(line); decodeErr != nil {
					result.err = decodeErr
					onLimit()
				}
				line = nil
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if !errors.Is(err, io.EOF) && result.err == nil {
				result.err = err
			}
			return result
		}
	}
}

// decodeTranscript parses an already-bounded JSONL transcript into the
// capture aggregate. It is pure decoding: truncation, cancellation and
// process status arrive via the attempt observation and are applied by Run.
func decodeTranscript(raw []byte) captureResult {
	result := captureResult{raw: raw}
	reader := bufio.NewReader(bytes.NewReader(raw))
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if decodeErr := result.decodeEventLine(line); decodeErr != nil {
				result.err = decodeErr
			}
		}
		if err != nil {
			return result
		}
	}
}

// decodeEventLine folds one JSONL event line into the capture aggregate and
// returns a protocol error for malformed or blank lines; callers decide how
// to fail.
func (result *captureResult) decodeEventLine(line []byte) error {
	var event codexEvent
	if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil {
		return fmt.Errorf("%w: malformed JSONL: %v", ErrProtocol, err)
	}
	result.eventCount++
	if result.sessionID == "" {
		if event.ThreadID != "" {
			result.sessionID = event.ThreadID
		} else if event.Thread.ID != "" {
			result.sessionID = event.Thread.ID
		}
	}
	if usage := event.Turn.Usage; usage.InputTokens > 0 || usage.OutputTokens > 0 {
		result.inputTokens = usage.InputTokens
		result.outputTokens = usage.OutputTokens
	} else if usage := event.Thread.Usage; usage.InputTokens > 0 || usage.OutputTokens > 0 {
		result.inputTokens = usage.InputTokens
		result.outputTokens = usage.OutputTokens
	}
	if codexToolCallTypes[event.Item.Type] {
		result.toolCalls++
		if event.Item.Status == "error" && denials.IsPermissionError(event.Item.Error) {
			result.denials = append(result.denials, denials.RawDenial{Tool: event.Item.Type, Input: event.Item.Input})
		} else {
			// Allowlist reconciliation is a read-only side channel: every
			// terminal non-denial tool completion is recorded by name so the
			// Verification tool-allowlist gate can reconcile successful calls.
			result.toolNames = append(result.toolNames, event.Item.Type)
		}
	}
	return nil
}

type streamCapture struct {
	data      []byte
	truncated bool
}

func captureStream(reader io.Reader, limit int64) streamCapture {
	var output []byte
	buffer := make([]byte, 32<<10)
	var total int64
	truncated := false
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			remaining := limit - int64(len(output))
			if remaining > 0 {
				take := int64(count)
				if take > remaining {
					take = remaining
				}
				output = append(output, buffer[:take]...)
			}
			if total > limit {
				truncated = true
			}
		}
		if err != nil {
			return streamCapture{data: output, truncated: truncated}
		}
	}
}
