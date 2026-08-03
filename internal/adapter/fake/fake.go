package fake

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/domain"
)

type Transcript struct {
	Capability json.RawMessage   `json:"capability"`
	Result     json.RawMessage   `json:"result"`
	Events     []json.RawMessage `json:"events"`
}

type Adapter struct{ transcript Transcript }

func New(data []byte) (*Adapter, error) {
	var transcript Transcript
	if err := json.Unmarshal(data, &transcript); err != nil {
		return nil, fmt.Errorf("decode fake transcript: %w", err)
	}
	if len(transcript.Capability) == 0 || len(transcript.Result) == 0 {
		return nil, fmt.Errorf("fake transcript requires capability and result")
	}
	return &Adapter{transcript: transcript}, nil
}

func (a *Adapter) ID() string { return "fake" }

func (a *Adapter) Probe(context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: append(json.RawMessage(nil), a.transcript.Capability...)}, nil
}

func (a *Adapter) Run(ctx context.Context, _ domain.Record) (domain.Record, error) {
	select {
	case <-ctx.Done():
		return domain.Record{}, ctx.Err()
	default:
		return domain.Record{Kind: domain.KindWorkerResult, Data: append(json.RawMessage(nil), a.transcript.Result...)}, nil
	}
}

func (a *Adapter) Events() []json.RawMessage {
	result := make([]json.RawMessage, len(a.transcript.Events))
	for index := range a.transcript.Events {
		result[index] = append(json.RawMessage(nil), a.transcript.Events[index]...)
	}
	return result
}
