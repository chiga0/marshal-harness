package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// ProductionResultInput binds the path-free WorkerResult emitted in Pi's
// terminal assistant message to the attempt identity observed by Marshal.
// The result transport is the held supervisor transcript; no result pathname
// is trusted or created by the worker.
type ProductionResultInput struct {
	Transcript     []byte
	Worktree       string
	TaskID         string
	RunID          string
	AttemptID      string
	Executable     string
	Version        string
	Model          string
	StartedAt      time.Time
	CompletedAt    time.Time
	MaxOutputBytes int64
}

// ParseProductionWorkerResult validates the complete Pi JSONL protocol and
// extracts exactly one WorkerResult JSON object from the final, non-retrying
// agent_end assistant message. Identity, session, provider, timing, and usage
// fields are then overwritten with Marshal-observed authority before a final
// schema validation.
func ParseProductionWorkerResult(ctx context.Context, input ProductionResultInput) (record domain.Record, err error) {
	stage := "input"
	defer func() {
		var classified *productionResultFailure
		if err != nil && !errors.As(err, &classified) {
			err = &productionResultFailure{code: stage, cause: err}
		}
	}()
	if err := validateProductionResultInput(input); err != nil {
		return domain.Record{}, err
	}
	stage = "transcript"
	capture := decodeTranscript(ctx, input.Transcript, input.Worktree, input.MaxOutputBytes)
	if capture.limitExceeded {
		stage = "output-limit"
		return domain.Record{}, errors.New("pi: production transcript exceeds the output limit")
	}
	if capture.err != nil {
		switch capture.failurePhase {
		case "read", "json", "session", "event", "agent-end", "tool", "compaction", "retry", "settled", "framing", "closure":
			stage += "-" + capture.failurePhase
		}
		return domain.Record{}, capture.err
	}
	if capture.providerFailed {
		stage = "provider-terminal"
		return domain.Record{}, errors.New("pi: provider reported a failed terminal invocation")
	}
	if capture.sessionID == "" {
		stage = "session-missing"
		return domain.Record{}, fmt.Errorf("%w: session id is missing", ErrProtocol)
	}

	stage = "final-message"
	declaredBytes, err := extractFinalWorkerResult(input.Transcript)
	if err != nil {
		return domain.Record{}, err
	}
	declaredBytes = NormalizeDeclaredWorkerResult(declaredBytes)
	stage = "validator"
	validator, err := contract.NewValidator()
	if err != nil {
		return domain.Record{}, fmt.Errorf("compile WorkerResult validator: %w", err)
	}
	stage = "declared-schema"
	if err := validator.Validate(domain.KindWorkerResult, declaredBytes); err != nil {
		return domain.Record{}, fmt.Errorf("validate declared production WorkerResult: %w", err)
	}
	stage = "declared-decode"
	var declared declaredResult
	if err := json.Unmarshal(declaredBytes, &declared); err != nil {
		return domain.Record{}, fmt.Errorf("decode declared production WorkerResult: %w", err)
	}
	if declared.TaskID != input.TaskID || declared.RunID != input.RunID || declared.AttemptID != input.AttemptID || declared.Adapter.ID != adapterID {
		stage = "declared-identity"
		return domain.Record{}, errors.New("WorkerResult identity does not match production attempt")
	}
	if declared.Session != nil && declared.Session.ID != "" && declared.Session.ID != capture.sessionID {
		stage = "declared-session"
		return domain.Record{}, errors.New("WorkerResult session does not match production transcript")
	}

	stage = "normalization"
	declared.Adapter.Executable = input.Executable
	declared.Adapter.Version = input.Version
	if input.Model != "" {
		declared.Adapter.Model = input.Model
	} else {
		declared.Adapter.Model = ""
	}
	declared.Session = &declaredSession{ID: capture.sessionID, Resumable: false}
	declared.StartedAt = input.StartedAt.UTC()
	declared.CompletedAt = input.CompletedAt.UTC()
	if capture.inputTokens > 0 || capture.outputTokens > 0 || capture.cost > 0 {
		usage := map[string]any{
			"inputTokens":       capture.inputTokens,
			"outputTokens":      capture.outputTokens,
			"cachedInputTokens": capture.cachedInputTokens,
		}
		if capture.cost > 0 {
			usage["cost"] = capture.cost
			usage["currency"] = "USD"
		}
		declared.Usage, err = json.Marshal(usage)
		if err != nil {
			return domain.Record{}, fmt.Errorf("encode observed Pi usage: %w", err)
		}
	}
	data, err := json.Marshal(declared)
	if err != nil {
		return domain.Record{}, fmt.Errorf("encode normalized production WorkerResult: %w", err)
	}
	stage = "normalized-schema"
	if err := validator.Validate(domain.KindWorkerResult, data); err != nil {
		return domain.Record{}, fmt.Errorf("validate normalized production WorkerResult: %w", err)
	}
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, nil
}

func validateProductionResultInput(input ProductionResultInput) error {
	if len(input.Transcript) == 0 {
		return errors.New("pi: production transcript is empty")
	}
	if !filepath.IsAbs(input.Worktree) || filepath.Clean(input.Worktree) != input.Worktree {
		return errors.New("pi: production worktree must be a clean absolute path")
	}
	if !filepath.IsAbs(input.Executable) || filepath.Clean(input.Executable) != input.Executable {
		return errors.New("pi: production executable must be a clean absolute path")
	}
	if input.TaskID == "" || input.RunID == "" || input.AttemptID == "" || input.Version == "" {
		return errors.New("pi: production result identity is incomplete")
	}
	if input.StartedAt.IsZero() || input.CompletedAt.IsZero() || input.CompletedAt.Before(input.StartedAt) {
		return errors.New("pi: production result timing is incomplete or unordered")
	}
	if input.MaxOutputBytes <= 0 || input.MaxOutputBytes > maxResultBytes*4 {
		return fmt.Errorf("pi: production transcript limit is outside the supported range: %d", input.MaxOutputBytes)
	}
	return nil
}

type productionAgentEnd struct {
	Type      string              `json:"type"`
	Messages  []productionMessage `json:"messages"`
	WillRetry *bool               `json:"willRetry"`
}

type productionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type productionContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractFinalWorkerResult(transcript []byte) (result []byte, err error) {
	stage := "final-event-decode"
	defer func() {
		var classified *productionResultFailure
		if err != nil && !errors.As(err, &classified) {
			err = &productionResultFailure{code: stage, cause: err}
		}
	}()
	lines := bytes.Split(transcript, []byte{'\n'})
	var final *productionAgentEnd
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return nil, fmt.Errorf("%w: decode production transcript event: %v", ErrProtocol, err)
		}
		if header.Type != "agent_end" {
			continue
		}
		var event productionAgentEnd
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("%w: decode production agent_end: %v", ErrProtocol, err)
		}
		if event.WillRetry == nil {
			return nil, fmt.Errorf("%w: production agent_end is missing willRetry", ErrProtocol)
		}
		if !*event.WillRetry {
			copy := event
			final = &copy
		}
	}
	if final == nil || len(final.Messages) == 0 {
		stage = "final-event-empty"
		return nil, fmt.Errorf("%w: final production agent_end has no messages", ErrProtocol)
	}
	message := final.Messages[len(final.Messages)-1]
	if message.Role != "assistant" {
		stage = "final-role"
		return nil, fmt.Errorf("%w: final production message is not assistant", ErrProtocol)
	}
	// Pi user/custom message content may legitimately be a string. Only
	// the selected terminal assistant is a WorkerResult carrier and must
	// satisfy the assistant content-array contract. Do not decode earlier
	// user/tool messages using the assistant-only schema.
	if len(message.Content) == 0 {
		stage = "final-content-missing"
		return nil, fmt.Errorf("%w: final production assistant has no content field", ErrProtocol)
	}
	stage = "final-content-shape"
	var content []productionContentItem
	if err := json.Unmarshal(message.Content, &content); err != nil {
		stage = productionContentDecodeFailure(err)
		return nil, fmt.Errorf("%w: final production assistant content cannot decode as typed content items", ErrProtocol)
	}
	var text string
	textItems := 0
	for _, item := range content {
		switch item.Type {
		case "thinking":
			continue
		case "text":
			textItems++
			text = item.Text
		default:
			stage = "final-content-type"
			return nil, fmt.Errorf("%w: final production assistant content contains unsupported type %q", ErrProtocol, item.Type)
		}
	}
	if textItems != 1 || strings.TrimSpace(text) == "" {
		stage = "final-content-text"
		return nil, fmt.Errorf("%w: final production assistant must contain exactly one non-empty text item", ErrProtocol)
	}
	return extractSingleWorkerResultObject(text)
}

// extractSingleWorkerResultObject implements ADR 0084 typed framing. Complete
// non-result containers may precede one declaration; nested declarations are
// never selected. Malformed containers and duplicate members fail closed.
func extractSingleWorkerResultObject(text string) ([]byte, error) {
	var (
		matched    []byte
		matchedEnd int
		candidates int
		skipUntil  int
	)
	for index := 0; index < len(text); index++ {
		if (text[index] != '{' && text[index] != '[') || index < skipUntil {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[index:]))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, &productionResultFailure{code: "final-object-invalid", cause: fmt.Errorf("%w: malformed terminal JSON container", ErrProtocol)}
		}
		// Nested `{"...": {...}}` braces belong to the outer object: skip every
		// later '{' that falls inside the span just decoded so one complete
		// top-level object is counted exactly once.
		end := index + int(decoder.InputOffset())
		if end > skipUntil {
			skipUntil = end
		}
		encoded, err := canonical.JSON(raw)
		if err != nil {
			return nil, &productionResultFailure{code: "final-object-invalid", cause: fmt.Errorf("%w: ambiguous terminal JSON container", ErrProtocol)}
		}
		if text[index] != '{' {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &object); err != nil {
			return nil, &productionResultFailure{code: "final-object-invalid", cause: ErrProtocol}
		}
		var kind string
		if json.Unmarshal(object["kind"], &kind) != nil || kind != "WorkerResult" {
			continue
		}
		candidates++
		if candidates > 1 {
			return nil, &productionResultFailure{code: "final-object-multiple", cause: fmt.Errorf("%w: multiple terminal WorkerResult declarations", ErrProtocol)}
		}
		matched = encoded
		matchedEnd = end
	}
	if candidates != 1 {
		return nil, &productionResultFailure{code: "final-object-missing", cause: fmt.Errorf("%w: final production assistant text is not one JSON object", ErrProtocol)}
	}
	if strings.TrimSpace(text[matchedEnd:]) != "" {
		return nil, &productionResultFailure{code: "final-object-trailing", cause: fmt.Errorf("%w: final production assistant text contains trailing non-whitespace after the result object", ErrProtocol)}
	}
	return matched, nil
}
