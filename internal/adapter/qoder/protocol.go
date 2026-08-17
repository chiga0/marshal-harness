package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// captureResult aggregates the bounded, decoded JSONL transcript of one
// non-interactive attempt. The fields mirror Qoder 1.1.23 stream-json frames:
// system.session_id/model, assistant.message, and result subtype/is_error/
// terminal_reason/usage. The adapter still stays fail-closed at unsupported
// until an independent credentialed live run binds a conformance receipt.
// Provider free text such as item content never reaches authorization or
// budgets; it survives only inside the raw transcript evidence.
type captureResult struct {
	raw             []byte
	sessionID       string
	model           string
	cliVersion      string
	protocolVersion string
	permissionMode  string
	eventCount      int
	assistantCount  int
	inputTokens     int
	outputTokens    int
	terminal        terminalOutcome
	limitExceeded   bool
	err             error
}

// terminalOutcome is the single terminal `result` event that ends a Qoder
// non-interactive run. A run must emit exactly one result event; a missing,
// duplicated, or unrecognized terminal is a protocol violation.
type terminalOutcome struct {
	seen    bool
	success bool
	code    string
}

// qoderEvent covers only the JSONL fields Marshal relies on. Unknown fields
// are ignored on purpose; protocol decisions rely solely on the event type,
// session id, usage numbers, and the terminal result status/code.
type qoderEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	Model           string          `json:"model"`
	QoderCLIVersion string          `json:"qodercli_version"`
	ProtocolVersion string          `json:"protocol_version"`
	PermissionMode  string          `json:"permissionMode"`
	Message         json.RawMessage `json:"message"`
	IsError         *bool           `json:"is_error"`
	TerminalReason  string          `json:"terminal_reason"`
	Usage           struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// qoderTerminalCodes maps a qoder terminal `code` literal to a closed
// failure kind. Any code outside the table is an unknown signal and becomes
// provider-terminal (do-not-retry), so the default is permanent failure and
// only the explicitly reviewed retriable/blocked signals widen beyond it.
var qoderTerminalCodes = map[string]port.FailureKind{
	"connection_failed": port.FailureKindConnectionFailure,
	"rate_limited":      port.FailureKindRateLimited,
	"quota_exceeded":    port.FailureKindQuotaExhausted,
}

// qoderFailure binds a closed port.AdapterFailure to a fixed detail string.
// protocol-invalid keeps ErrProtocol identity so existing errors.Is checks
// continue to work; the typed identity (adapter/kind/disposition) is readable
// through errors.As along the error chain. Error() only contains closed
// enums and fixed detail vocabulary, so provider free text never reaches it.
type qoderFailure struct {
	failure port.AdapterFailure
	detail  string
}

func newQoderFailure(kind port.FailureKind, detail string, now time.Time) *qoderFailure {
	disposition, _ := port.DispositionFor(kind)
	// now is always non-zero and no retry hints are carried, so construction
	// cannot fail; the error is discarded only to keep the call total.
	failure, _ := port.NewAdapterFailure(port.AdapterIDQoder, kind, disposition, nil, nil, now)
	return &qoderFailure{failure: failure, detail: detail}
}

func qoderProtocolInvalid(detail string, now time.Time) *qoderFailure {
	return newQoderFailure(port.FailureKindProtocolInvalid, detail, now)
}

func (e *qoderFailure) Error() string {
	if e.detail == "" {
		return e.failure.Error()
	}
	return e.failure.Error() + ": " + e.detail
}

func (e *qoderFailure) Unwrap() []error {
	if e.failure.Kind == port.FailureKindProtocolInvalid {
		return []error{e.failure, ErrProtocol}
	}
	return []error{e.failure}
}

// classifyTerminalFailure maps a qoder terminal `code` to a typed failure.
// An empty or unknown code is provider-terminal (do-not-retry): only the
// closed table's reviewed signals may change the disposition.
func classifyTerminalFailure(code string, now time.Time) *qoderFailure {
	kind, known := qoderTerminalCodes[code]
	if !known {
		return newQoderFailure(port.FailureKindProviderTerminal, "provider reported a terminal error with an unrecognized code", now)
	}
	return newQoderFailure(kind, "", now)
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
// returns a protocol error for malformed or blank lines, duplicate terminals,
// and unrecognized terminal statuses. It never classifies a terminal error
// into a failure kind; Run performs that classification after capture.
func (result *captureResult) decodeEventLine(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return fmt.Errorf("%w: blank JSONL event", ErrProtocol)
	}
	var event qoderEvent
	if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil {
		return fmt.Errorf("%w: malformed JSONL: %v", ErrProtocol, err)
	}
	result.eventCount++
	if result.terminal.seen {
		return fmt.Errorf("%w: event follows terminal result", ErrProtocol)
	}
	if event.SessionID != "" && result.sessionID != "" && event.SessionID != result.sessionID {
		return fmt.Errorf("%w: event session id changed", ErrProtocol)
	}
	switch event.Type {
	case "system":
		if result.sessionID != "" || event.Subtype != "init" || event.SessionID == "" || event.Model == "" || event.QoderCLIVersion == "" || event.ProtocolVersion == "" || event.PermissionMode == "" {
			return fmt.Errorf("%w: invalid or duplicate system init event", ErrProtocol)
		}
		result.sessionID, result.model = event.SessionID, event.Model
		result.cliVersion, result.protocolVersion, result.permissionMode = event.QoderCLIVersion, event.ProtocolVersion, event.PermissionMode
	case "assistant":
		if result.sessionID == "" || len(bytes.TrimSpace(event.Message)) == 0 || bytes.Equal(bytes.TrimSpace(event.Message), []byte("null")) {
			return fmt.Errorf("%w: invalid assistant message event", ErrProtocol)
		}
		result.assistantCount++
	case "result":
		if result.sessionID == "" || event.IsError == nil || event.TerminalReason == "" {
			return fmt.Errorf("%w: incomplete terminal result event", ErrProtocol)
		}
		result.terminal.seen = true
		result.inputTokens, result.outputTokens = event.Usage.InputTokens, event.Usage.OutputTokens
		if result.inputTokens < 0 || result.outputTokens < 0 {
			return fmt.Errorf("%w: terminal usage is negative", ErrProtocol)
		}
		switch {
		case event.Subtype == "success" && !*event.IsError:
			if result.assistantCount == 0 {
				return fmt.Errorf("%w: successful result has no assistant message", ErrProtocol)
			}
			result.terminal.success = true
		case event.Subtype != "success" && *event.IsError:
			result.terminal.code = event.TerminalReason
		default:
			return fmt.Errorf("%w: contradictory terminal result event", ErrProtocol)
		}
	default:
		return fmt.Errorf("%w: unrecognized stream-json event type", ErrProtocol)
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
