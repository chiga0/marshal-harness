package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// MetaFileName is the transcript metadata file the fake adapter writes into
// the attempt's control output directory so the Verification tool-allowlist
// gate can reconcile fake runs exactly like real adapter runs.
const MetaFileName = "fake-transcript-meta.json"

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

func (a *Adapter) Run(ctx context.Context, record domain.Record) (domain.Record, error) {
	select {
	case <-ctx.Done():
		return domain.Record{}, ctx.Err()
	default:
	}
	a.writeTranscriptMeta(record)
	return domain.Record{Kind: domain.KindWorkerResult, Data: append(json.RawMessage(nil), a.transcript.Result...)}, nil
}

func (a *Adapter) Events() []json.RawMessage {
	result := make([]json.RawMessage, len(a.transcript.Events))
	for index := range a.transcript.Events {
		result[index] = append(json.RawMessage(nil), a.transcript.Events[index]...)
	}
	return result
}

// SuccessfulToolNames derives the normalized, de-duplicated, sorted tool
// names of the successful (non-denied) tool calls replayed by Events. An
// event counts as a successful tool call when it carries type "tool", a
// non-empty tool name, and no denial marker; denied calls never enter the
// reconciliation input.
func SuccessfulToolNames(events []json.RawMessage) []string {
	var raw []string
	for _, event := range events {
		var decoded struct {
			Type   string `json:"type"`
			Tool   string `json:"tool"`
			Denied bool   `json:"denied"`
		}
		if json.Unmarshal(event, &decoded) != nil {
			continue
		}
		if decoded.Type != "tool" || decoded.Tool == "" || decoded.Denied {
			continue
		}
		raw = append(raw, decoded.Tool)
	}
	return denials.SortedToolNames(raw)
}

// writeTranscriptMeta persists the reconciliation input for a fake attempt
// into the attempt's control output directory, mirroring the real adapters'
// <adapter>-transcript-meta.json side channel. It is strictly best-effort:
// a missing or undecodable WorkerRequest keeps the historical behavior, and
// any write problem is left to the Verification gate, which fails closed on
// missing evidence when a tool allowlist is declared.
func (a *Adapter) writeTranscriptMeta(record domain.Record) {
	if record.Kind != domain.KindWorkerRequest || len(record.Data) == 0 {
		return
	}
	var request struct {
		ControlRoot string `json:"controlRoot"`
		ResultPath  string `json:"resultPath"`
	}
	if json.Unmarshal(record.Data, &request) != nil || request.ControlRoot == "" || request.ResultPath == "" {
		return
	}
	if !filepath.IsAbs(request.ControlRoot) {
		return
	}
	outputDir := filepath.Dir(filepath.Join(request.ControlRoot, filepath.FromSlash(request.ResultPath)))
	if rel, err := filepath.Rel(request.ControlRoot, outputDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	meta, err := json.MarshalIndent(map[string]any{
		"toolNames":        SuccessfulToolNames(a.Events()),
		"permissionDenied": false,
	}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(outputDir, MetaFileName), append(meta, '\n'), 0o600)
}
