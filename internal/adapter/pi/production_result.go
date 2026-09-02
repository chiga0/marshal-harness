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
func ParseProductionWorkerResult(ctx context.Context, input ProductionResultInput) (domain.Record, error) {
	if err := validateProductionResultInput(input); err != nil {
		return domain.Record{}, err
	}
	capture := decodeTranscript(ctx, input.Transcript, input.Worktree, input.MaxOutputBytes)
	if capture.limitExceeded {
		return domain.Record{}, errors.New("pi: production transcript exceeds the output limit")
	}
	if capture.err != nil {
		return domain.Record{}, capture.err
	}
	if capture.providerFailed {
		return domain.Record{}, errors.New("pi: provider reported a failed terminal invocation")
	}
	if capture.sessionID == "" {
		return domain.Record{}, fmt.Errorf("%w: session id is missing", ErrProtocol)
	}

	declaredBytes, err := extractFinalWorkerResult(input.Transcript)
	if err != nil {
		return domain.Record{}, err
	}
	declaredBytes = NormalizeDeclaredWorkerResult(declaredBytes)
	validator, err := contract.NewValidator()
	if err != nil {
		return domain.Record{}, fmt.Errorf("compile WorkerResult validator: %w", err)
	}
	if err := validator.Validate(domain.KindWorkerResult, declaredBytes); err != nil {
		return domain.Record{}, fmt.Errorf("validate declared production WorkerResult: %w", err)
	}
	var declared declaredResult
	if err := json.Unmarshal(declaredBytes, &declared); err != nil {
		return domain.Record{}, fmt.Errorf("decode declared production WorkerResult: %w", err)
	}
	if declared.TaskID != input.TaskID || declared.RunID != input.RunID || declared.AttemptID != input.AttemptID || declared.Adapter.ID != adapterID {
		return domain.Record{}, errors.New("WorkerResult identity does not match production attempt")
	}
	if declared.Session != nil && declared.Session.ID != "" && declared.Session.ID != capture.sessionID {
		return domain.Record{}, errors.New("WorkerResult session does not match production transcript")
	}

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
	Role    string                  `json:"role"`
	Content []productionContentItem `json:"content"`
}

type productionContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractFinalWorkerResult(transcript []byte) ([]byte, error) {
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
		return nil, fmt.Errorf("%w: final production agent_end has no messages", ErrProtocol)
	}
	message := final.Messages[len(final.Messages)-1]
	if message.Role != "assistant" {
		return nil, fmt.Errorf("%w: final production message is not assistant", ErrProtocol)
	}
	var text string
	textItems := 0
	for _, item := range message.Content {
		switch item.Type {
		case "thinking":
			continue
		case "text":
			textItems++
			text = item.Text
		default:
			return nil, fmt.Errorf("%w: final production assistant content contains unsupported type %q", ErrProtocol, item.Type)
		}
	}
	if textItems != 1 || strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: final production assistant must contain exactly one non-empty text item", ErrProtocol)
	}
	return extractSingleWorkerResultObject(text)
}

// extractSingleWorkerResultObject implements the ADR 0075 final-message
// contract: plain prose is tolerated, but the text must contain exactly one
// complete JSON object and everything after that object must be whitespace.
// Zero or two-or-more decodable objects fail closed.
func extractSingleWorkerResultObject(text string) ([]byte, error) {
	var (
		matched    map[string]json.RawMessage
		matchedEnd int
		candidates int
	)
	for index := 0; index < len(text); index++ {
		if text[index] != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[index:]))
		var object map[string]json.RawMessage
		if err := decoder.Decode(&object); err != nil || object == nil {
			continue
		}
		candidates++
		if candidates > 1 {
			return nil, fmt.Errorf("%w: final production assistant text must contain exactly one complete JSON object", ErrProtocol)
		}
		matched = object
		matchedEnd = index + int(decoder.InputOffset())
	}
	if candidates != 1 {
		return nil, fmt.Errorf("%w: final production assistant text is not one JSON object", ErrProtocol)
	}
	if strings.TrimSpace(text[matchedEnd:]) != "" {
		return nil, fmt.Errorf("%w: final production assistant text contains trailing non-whitespace after the result object", ErrProtocol)
	}
	return json.Marshal(matched)
}
