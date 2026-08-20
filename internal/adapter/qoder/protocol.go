package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/canonical"
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
	observedTools   []observedToolCall
	pendingTools    map[string]pendingToolCall
	assistantTurn   assistantTurnState
	closedTurns     map[string]struct{}
	resultTransport resultTransportSequence
	terminal        terminalOutcome
	limitExceeded   bool
	err             error
}

type pendingToolCall struct {
	tool                 string
	input                json.RawMessage
	ordinal              int
	resultTransport      bool
	validResultTransport bool
	resultPayload        []byte
}

// assistantTurnState binds fragmented stream-json frames to one logical
// assistant message. Qoder 1.1.23 may emit multiple distinct tool_use blocks
// for the same message id before returning their results.
type assistantTurnState struct {
	id     string
	closed bool
}

// observedToolCall is the closed, provider-structured projection consumed by
// transcript attestation. It deliberately excludes provider prose and tool
// output content.
type observedToolCall struct {
	id              string
	tool            string
	input           json.RawMessage
	ordinal         int
	status          string
	explicitSuccess bool
	resultTransport bool
}

// resultTransportSequence records only the closed WorkerResult transport
// protocol projected by Marshal. The staging bytes remain untrusted and are
// separately consumed through held descriptors, validated against Schema and
// rebound to the request identity. This sequence is evidence that Qoder used
// exactly one reviewed Bash tee and made no later tool call; it never derives
// a semantic WorkerResult from exit status, git diff, or assistant prose.
type resultTransportSequence struct {
	attempts          int
	successes         int
	successfulOrdinal int
	invalidAccess     bool
	payload           []byte
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
		Kind          string `json:"kind"`
		ExitCode      *int   `json:"exitCode"`
		Interrupted   *bool  `json:"interrupted"`
		IsHardFailure *bool  `json:"isHardFailure"`
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
	ID         string             `json:"id"`
	Role       string             `json:"role"`
	StopReason *string            `json:"stop_reason"`
	Content    []qoderMessagePart `json:"content"`
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
		if err := result.beginAssistantFrame(message); err != nil {
			return err
		}
		if result.pendingTools == nil {
			result.pendingTools = map[string]pendingToolCall{}
		}
		frameToolIDs := map[string]struct{}{}
		for _, part := range message.Content {
			switch part.Type {
			case "thinking", "text":
				continue
			case "tool_use":
			default:
				return fmt.Errorf("%w: unrecognized assistant message part", ErrProtocol)
			}
			if part.ID == "" || part.Name == "" || len(bytes.TrimSpace(part.Input)) == 0 || bytes.Equal(bytes.TrimSpace(part.Input), []byte("null")) {
				return fmt.Errorf("%w: incomplete assistant tool_use", ErrProtocol)
			}
			if _, duplicate := frameToolIDs[part.ID]; duplicate {
				return fmt.Errorf("%w: duplicate assistant tool_use id", ErrProtocol)
			}
			frameToolIDs[part.ID] = struct{}{}
			tool := normalizeQoderToolName(part.Name)
			if pending, duplicate := result.pendingTools[part.ID]; duplicate {
				if message.ID == "" || pending.tool != tool || !canonicalJSONEqual(pending.input, part.Input) {
					return fmt.Errorf("%w: duplicate assistant tool_use id", ErrProtocol)
				}
				continue
			}
			if result.hasObservedToolID(part.ID) {
				return fmt.Errorf("%w: reused assistant tool_use id", ErrProtocol)
			}
			result.toolCalls++
			transportAccess, validTransport, resultPayload := classifyWorkerResultTransportTool(tool, part.Input)
			if transportAccess {
				result.resultTransport.attempts++
				if !validTransport {
					result.resultTransport.invalidAccess = true
				}
			}
			result.pendingTools[part.ID] = pendingToolCall{
				tool: tool, input: append(json.RawMessage(nil), part.Input...), ordinal: result.toolCalls,
				resultTransport: transportAccess, validResultTransport: validTransport, resultPayload: resultPayload,
			}
			result.observedTools = append(result.observedTools, observedToolCall{id: part.ID, tool: tool, input: append(json.RawMessage(nil), part.Input...), ordinal: result.toolCalls, resultTransport: transportAccess})
		}
		if err := result.finishAssistantFrame(message); err != nil {
			return err
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
		if result.assistantTurn.id != "" && !result.assistantTurn.closed {
			return fmt.Errorf("%w: tool_result precedes assistant stop_reason", ErrProtocol)
		}
		permissionDeniedIDs, err := qoderPermissionDeniedIDs(event, message)
		if err != nil {
			return err
		}
		for _, part := range message.Content {
			if part.Type != "tool_result" {
				return fmt.Errorf("%w: unrecognized user message part", ErrProtocol)
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
				result.setObservedToolStatus(part.ToolUseID, "denied")
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
				result.setObservedToolStatus(part.ToolUseID, "failed")
				continue
			}
			explicitSuccess := event.ToolUseResult.Kind == "completed" && event.ToolUseResult.ExitCode != nil && *event.ToolUseResult.ExitCode == 0 && event.ToolUseResult.Interrupted != nil && !*event.ToolUseResult.Interrupted
			result.setObservedToolExplicitSuccess(part.ToolUseID, explicitSuccess)
			if pending.resultTransport && pending.validResultTransport {
				result.resultTransport.successes++
				result.resultTransport.successfulOrdinal = pending.ordinal
				result.resultTransport.payload = append([]byte(nil), pending.resultPayload...)
			}
			result.setObservedToolStatus(part.ToolUseID, "passed")
			result.toolNames = append(result.toolNames, pending.tool)
		}
		if result.assistantTurn.id != "" && len(result.pendingTools) == 0 {
			if result.closedTurns == nil {
				result.closedTurns = map[string]struct{}{}
			}
			result.closedTurns[result.assistantTurn.id] = struct{}{}
			result.assistantTurn = assistantTurnState{}
		}
		return nil
	case "result":
		if result.sessionID == "" || event.IsError == nil {
			return fmt.Errorf("%w: incomplete terminal result event", ErrProtocol)
		}
		if len(result.pendingTools) != 0 {
			return fmt.Errorf("%w: terminal result has unresolved tool_use", ErrProtocol)
		}
		if result.assistantTurn.id != "" && !result.assistantTurn.closed {
			return fmt.Errorf("%w: terminal result has unresolved assistant message", ErrProtocol)
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

func (result *captureResult) beginAssistantFrame(message qoderMessage) error {
	if message.ID == "" {
		if result.assistantTurn.id != "" {
			return fmt.Errorf("%w: assistant message id disappeared", ErrProtocol)
		}
		for _, part := range message.Content {
			if part.Type == "tool_use" && len(result.pendingTools) != 0 {
				return fmt.Errorf("%w: tool_use precedes prior tool_result", ErrProtocol)
			}
		}
		return nil
	}
	if message.Role != "assistant" {
		return fmt.Errorf("%w: assistant message role mismatch", ErrProtocol)
	}
	if _, duplicate := result.closedTurns[message.ID]; duplicate {
		return fmt.Errorf("%w: assistant message follows closed turn", ErrProtocol)
	}
	switch {
	case result.assistantTurn.id == "":
		if len(result.pendingTools) != 0 {
			return fmt.Errorf("%w: assistant message precedes prior tool_result", ErrProtocol)
		}
		result.assistantTurn = assistantTurnState{id: message.ID}
	case result.assistantTurn.id == message.ID:
		if result.assistantTurn.closed {
			return fmt.Errorf("%w: assistant frame follows stop_reason", ErrProtocol)
		}
	default:
		if result.assistantTurn.closed {
			return fmt.Errorf("%w: assistant message follows closed turn", ErrProtocol)
		}
		return fmt.Errorf("%w: assistant message id changed before stop_reason", ErrProtocol)
	}
	return nil
}

func (result *captureResult) finishAssistantFrame(message qoderMessage) error {
	if message.ID == "" || message.StopReason == nil {
		return nil
	}
	switch *message.StopReason {
	case "tool_use":
		if len(result.pendingTools) == 0 {
			return fmt.Errorf("%w: tool_use stop_reason has no pending tool", ErrProtocol)
		}
	case "end_turn":
		if len(result.pendingTools) != 0 {
			return fmt.Errorf("%w: end_turn has unresolved tool_use", ErrProtocol)
		}
	default:
		return fmt.Errorf("%w: unrecognized assistant stop_reason", ErrProtocol)
	}
	result.assistantTurn.closed = true
	return nil
}

func canonicalJSONEqual(left, right json.RawMessage) bool {
	leftCanonical, leftErr := canonical.JSON(left)
	if leftErr != nil {
		return false
	}
	rightCanonical, rightErr := canonical.JSON(right)
	return rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func (result *captureResult) hasObservedToolID(id string) bool {
	for _, tool := range result.observedTools {
		if tool.id == id {
			return true
		}
	}
	return false
}

func (result *captureResult) setObservedToolStatus(id, status string) {
	for index := range result.observedTools {
		if result.observedTools[index].id == id {
			result.observedTools[index].status = status
			return
		}
	}
}

func (result *captureResult) setObservedToolExplicitSuccess(id string, success bool) {
	for index := range result.observedTools {
		if result.observedTools[index].id == id {
			result.observedTools[index].explicitSuccess = success
			return
		}
	}
}

// classifyWorkerResultTransportTool recognizes the one reviewed Qoder 1.1.23
// declaration primitive after decoding the tool input as JSON. The Worker is
// never given a staging pathname or descriptor: the command tees only to
// /dev/null, and Marshal copies the strictly parsed payload into its unlinked
// held inode after the transcript and terminal result pass. Consequently no
// arbitrary shell expression can name the authority-bearing staging object.
//
// A declaration input is closed as well: one canonical `command`, Qoder's
// observed non-authoritative bounded `description`, and one fixed
// quoted-heredoc grammar. Split words, globs, background jobs, alternate
// sinks, unknown JSON fields/whitespace and unicode-escaped spellings cannot
// become a second spelling of the primitive.
func classifyWorkerResultTransportTool(tool string, input json.RawMessage) (access, valid bool, payload []byte) {
	command, canonical := decodeCanonicalQoderBashInput(input)
	if !workerResultDeclarationCandidate(command) {
		return false, false, nil
	}
	if tool != "bash" || !canonical {
		return true, false, nil
	}
	payload, valid = parseWorkerResultTeeCommand(command)
	return true, valid, payload
}

func decodeCanonicalQoderBashInput(input json.RawMessage) (string, bool) {
	type bashInput struct {
		Command     string `json:"command"`
		Description string `json:"description,omitempty"`
	}
	// Decode once without the closed-field check so a declaration candidate
	// with an extra field is still classified as an invalid attempt instead of
	// disappearing into the ordinary Bash stream.
	var candidate struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &candidate); err != nil || candidate.Command == "" {
		return "", false
	}
	var value bashInput
	if err := json.Unmarshal(input, &value); err != nil || !validQoderBashDescription(value.Description) {
		return candidate.Command, false
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bashInput{}); err != nil {
		return value.Command, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return value.Command, false
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return value.Command, false
	}
	noHTMLEscapes := bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'})
	defaultEscapes, err := json.Marshal(value)
	if err != nil {
		return value.Command, false
	}
	return value.Command, bytes.Equal(input, noHTMLEscapes) || bytes.Equal(input, defaultEscapes)
}

const (
	qoderBashDescriptionMinBytes = 1
	qoderBashDescriptionMaxBytes = 512
)

func validQoderBashDescription(value string) bool {
	if len(value) < qoderBashDescriptionMinBytes || len(value) > qoderBashDescriptionMaxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

const workerResultTeeFirstLine = "cat <<'MARSHAL_RESULT' | tee /dev/null > /dev/null"

func workerResultDeclarationCandidate(command string) bool {
	firstLine, _, _ := strings.Cut(command, "\n")
	return strings.HasPrefix(firstLine, "cat <<'MARSHAL_RESULT'")
}

func validWorkerResultTeeCommand(command string) bool {
	_, valid := parseWorkerResultTeeCommand(command)
	return valid
}

func parseWorkerResultTeeCommand(command string) ([]byte, bool) {
	if strings.ContainsAny(command, "\r\x00") || strings.HasSuffix(command, "\n") {
		return nil, false
	}
	firstLine, body, hasBody := strings.Cut(command, "\n")
	if !hasBody || firstLine != workerResultTeeFirstLine {
		return nil, false
	}
	const finalDelimiter = "\nMARSHAL_RESULT"
	if !strings.HasSuffix(body, finalDelimiter) {
		return nil, false
	}
	payload := strings.TrimSuffix(body, finalDelimiter)
	if payload == "" {
		return nil, false
	}
	for _, line := range strings.Split(payload, "\n") {
		if line == "MARSHAL_RESULT" {
			return nil, false
		}
	}
	return []byte(payload), true
}

// validateWorkerResultTransportSequence is called only for an otherwise
// successful attempt. It requires exactly one successful reviewed tee and
// makes its tool_use ordinal the final tool call. A failed/invalid first
// declaration followed by a corrected declaration therefore remains invalid
// rather than accepting the final staging bytes.
func validateWorkerResultTransportSequence(result captureResult) error {
	sequence := result.resultTransport
	if workerResultTransportSequenceViolation(result) || sequence.attempts != 1 || sequence.successes != 1 {
		return ErrProtocol
	}
	return nil
}

// workerResultTransportSequenceViolation distinguishes a structural sequence
// breach from a single tee that was denied or failed. Structural breaches are
// protocol-invalid even if a later provider/process failure is also present;
// one denied final tee keeps its truthful provider-terminal classification.
func workerResultTransportSequenceViolation(result captureResult) bool {
	sequence := result.resultTransport
	return sequence.invalidAccess || sequence.attempts > 1 || sequence.successes > 1 || sequence.successes == 1 && sequence.successfulOrdinal != result.toolCalls
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
