package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/port"
)

// captureResult aggregates the bounded, decoded JSONL transcript of one
// non-interactive attempt. The fields mirror Qoder 1.1.23 stream-json frames:
// system.session_id/model, assistant.message, and result subtype/is_error/
// terminal_reason/error/usage. The adapter still stays fail-closed at unsupported
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
	toolCalls       int
	inputTokens     int
	outputTokens    int
	denials         []denials.RawDenial
	toolNames       []string
	pendingTools    map[string]pendingToolCall
	terminal        terminalOutcome
	limitExceeded   bool
	err             error
}

type pendingToolCall struct {
	tool  string
	input json.RawMessage
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
	Error           string          `json:"error"`
	ToolUseResult   struct {
		IsHardFailure *bool `json:"isHardFailure"`
	} `json:"tool_use_result"`
	ToolResultMeta []struct {
		ID               string `json:"id"`
		NonExecutionKind string `json:"non_execution_kind"`
	} `json:"tool_result_meta"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type qoderMessage struct {
	Content []qoderMessagePart `json:"content"`
}

type qoderMessagePart struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   *bool           `json:"is_error"`
	Content   json.RawMessage `json:"content"`
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
// and contradictory terminal statuses. The observed hook_started/
// hook_progress/hook_response system frames are ignored as non-semantic.
// An error terminal is recognized by is_error regardless of subtype; its code
// comes from the `error` field, falling back to terminal_reason, and an error
// terminal with neither is a protocol violation. A success terminal only
// requires a valid session, is_error=false, subtype=success and at least one
// assistant message; terminal_reason is optional. It never classifies a
// terminal error into a failure kind; Run performs that classification after
// capture.
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
		switch event.Subtype {
		case "hook_started", "hook_progress", "hook_response":
			// Observed qodercli 1.1.23 system frames emitted before init; they
			// carry no session or terminal state and are ignored, while every
			// other system subtype stays fail-closed.
			return nil
		case "init":
			if result.sessionID != "" || event.SessionID == "" || event.Model == "" || event.QoderCLIVersion == "" || event.ProtocolVersion == "" || event.PermissionMode == "" {
				return fmt.Errorf("%w: invalid or duplicate system init event", ErrProtocol)
			}
			result.sessionID, result.model = event.SessionID, event.Model
			result.cliVersion, result.protocolVersion, result.permissionMode = event.QoderCLIVersion, event.ProtocolVersion, event.PermissionMode
		default:
			return fmt.Errorf("%w: unrecognized system event subtype", ErrProtocol)
		}
	case "assistant":
		if result.sessionID == "" || len(bytes.TrimSpace(event.Message)) == 0 || bytes.Equal(bytes.TrimSpace(event.Message), []byte("null")) {
			return fmt.Errorf("%w: invalid assistant message event", ErrProtocol)
		}
		var message qoderMessage
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return fmt.Errorf("%w: malformed assistant message", ErrProtocol)
		}
		if result.pendingTools == nil {
			result.pendingTools = map[string]pendingToolCall{}
		}
		for _, part := range message.Content {
			if part.Type != "tool_use" {
				continue
			}
			if part.ID == "" || part.Name == "" || len(bytes.TrimSpace(part.Input)) == 0 || bytes.Equal(bytes.TrimSpace(part.Input), []byte("null")) {
				return fmt.Errorf("%w: incomplete assistant tool_use", ErrProtocol)
			}
			if _, duplicate := result.pendingTools[part.ID]; duplicate {
				return fmt.Errorf("%w: duplicate assistant tool_use id", ErrProtocol)
			}
			result.pendingTools[part.ID] = pendingToolCall{tool: normalizeQoderToolName(part.Name), input: append(json.RawMessage(nil), part.Input...)}
			result.toolCalls++
		}
		result.assistantCount++
	case "user":
		if result.sessionID == "" || len(bytes.TrimSpace(event.Message)) == 0 || bytes.Equal(bytes.TrimSpace(event.Message), []byte("null")) {
			return fmt.Errorf("%w: invalid user message event", ErrProtocol)
		}
		var message qoderMessage
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return fmt.Errorf("%w: malformed user message", ErrProtocol)
		}
		permissionDeniedIDs, err := qoderPermissionDeniedIDs(event, message)
		if err != nil {
			return err
		}
		for _, part := range message.Content {
			if part.Type != "tool_result" {
				continue
			}
			pending, ok := result.pendingTools[part.ToolUseID]
			if part.ToolUseID == "" || !ok {
				return fmt.Errorf("%w: user tool_result has no matching tool_use", ErrProtocol)
			}
			delete(result.pendingTools, part.ToolUseID)
			_, contentErr := decodeQoderToolResultContent(part.Content)
			if contentErr != nil {
				return contentErr
			}
			_, permissionDenied := permissionDeniedIDs[part.ToolUseID]
			if permissionDenied {
				result.denials = append(result.denials, denials.RawDenial{Tool: pending.tool, Input: pending.input})
				if part.IsError == nil || !*part.IsError {
					return fmt.Errorf("%w: permission denial marker contradicts is_error", ErrProtocol)
				}
				continue
			}
			if pending.tool == "qoder-unknown" || pending.tool == "agent" {
				return fmt.Errorf("%w: tool_result references an unreviewed or forbidden Qoder tool", ErrProtocol)
			}
			// Real Qoder 1.1.23 omits is_error on successful tool_result
			// frames. An explicit non-permission error remains a failed call and
			// must never enter the successful toolNames side channel.
			if part.IsError != nil && *part.IsError {
				continue
			}
			result.toolNames = append(result.toolNames, pending.tool)
		}
		return nil
	case "result":
		if result.sessionID == "" || event.IsError == nil {
			return fmt.Errorf("%w: incomplete terminal result event", ErrProtocol)
		}
		if len(result.pendingTools) != 0 {
			return fmt.Errorf("%w: terminal result has unresolved tool_use", ErrProtocol)
		}
		result.terminal.seen = true
		result.inputTokens, result.outputTokens = event.Usage.InputTokens, event.Usage.OutputTokens
		if result.inputTokens < 0 || result.outputTokens < 0 {
			return fmt.Errorf("%w: terminal usage is negative", ErrProtocol)
		}
		switch {
		case *event.IsError:
			if event.Error == "" && event.TerminalReason == "" {
				return fmt.Errorf("%w: error terminal has no code", ErrProtocol)
			}
			result.terminal.code = event.Error
			if result.terminal.code == "" {
				result.terminal.code = event.TerminalReason
			}
		case event.Subtype == "success":
			if result.assistantCount == 0 {
				return fmt.Errorf("%w: successful result has no assistant message", ErrProtocol)
			}
			result.terminal.success = true
		default:
			return fmt.Errorf("%w: contradictory terminal result event", ErrProtocol)
		}
	default:
		return fmt.Errorf("%w: unrecognized stream-json event type", ErrProtocol)
	}
	return nil
}

func normalizeQoderToolName(name string) string {
	// Frozen from credentialed Qoder CLI 1.1.23 stream-json evidence. Matching
	// is deliberately case-sensitive: arbitrary case folding would turn an
	// unreviewed provider tool into a known authorization primitive.
	switch name {
	case "Agent":
		return "agent"
	case "Bash":
		return "bash"
	case "Edit":
		return "edit"
	case "Glob":
		return "glob"
	case "Grep":
		return "grep"
	case "Read":
		return "read"
	case "TaskCreate":
		return "task_create"
	case "TaskUpdate":
		return "task_update"
	case "Write":
		return "write"
	default:
		return "qoder-unknown"
	}
}

func decodeQoderToolResultContent(raw json.RawMessage) (string, error) {
	var content string
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &content) != nil {
		return "", fmt.Errorf("%w: tool_result content is not a JSON string", ErrProtocol)
	}
	return content, nil
}

// qoderPermissionDeniedIDs consumes only Qoder 1.1.23's structured denial
// metadata. Tool-result content is arbitrary Worker-visible data: Read may
// legitimately return source code containing permission-related text, so
// scanning it would let ordinary file bytes forge a denial and abort a valid
// attempt. Real 1.1.23 evidence represents permission-rule denials through
// tool_result_meta.non_execution_kind and represents the provider's hard
// refusal through tool_use_result.isHardFailure. Metadata is bound to the
// exact tool_result id in the same event. The event-global hard-failure bit is
// accepted only for an unambiguous single-result event.
func qoderPermissionDeniedIDs(event qoderEvent, message qoderMessage) (map[string]struct{}, error) {
	resultIDs := make(map[string]struct{})
	for _, part := range message.Content {
		if part.Type != "tool_result" {
			continue
		}
		if part.ToolUseID == "" {
			return nil, fmt.Errorf("%w: user tool_result has no id", ErrProtocol)
		}
		if _, duplicate := resultIDs[part.ToolUseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate user tool_result id", ErrProtocol)
		}
		resultIDs[part.ToolUseID] = struct{}{}
	}

	deniedIDs := make(map[string]struct{})
	metadataIDs := make(map[string]struct{})
	for _, metadata := range event.ToolResultMeta {
		if metadata.ID == "" || metadata.NonExecutionKind == "" {
			return nil, fmt.Errorf("%w: incomplete tool_result_meta", ErrProtocol)
		}
		if _, duplicate := metadataIDs[metadata.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate tool_result_meta id", ErrProtocol)
		}
		metadataIDs[metadata.ID] = struct{}{}
		if _, ok := resultIDs[metadata.ID]; !ok {
			return nil, fmt.Errorf("%w: tool_result_meta has no matching tool_result", ErrProtocol)
		}
		if metadata.NonExecutionKind != "permission-rule" {
			return nil, fmt.Errorf("%w: unreviewed tool_result_meta kind", ErrProtocol)
		}
		deniedIDs[metadata.ID] = struct{}{}
	}

	if event.ToolUseResult.IsHardFailure != nil && *event.ToolUseResult.IsHardFailure {
		if len(resultIDs) != 1 {
			return nil, fmt.Errorf("%w: ambiguous structured hard failure", ErrProtocol)
		}
		for id := range resultIDs {
			deniedIDs[id] = struct{}{}
		}
	}
	return deniedIDs, nil
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
