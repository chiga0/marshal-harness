package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestProductionContentShapeDiagnosticsKeepEveryRejectionClosed(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"container-string", `"private body"`, "container-shape"},
		{"container-object", `{"private-key":"private body"}`, "container-shape"},
		{"container-number", `42`, "container-shape"},
		{"container-bool", `true`, "container-shape"},
		{"element-string", `["private body"]`, "item-shape"},
		{"element-array", `[["private body"]]`, "item-shape"},
		{"type-object", `[{"type":{"private-key":true}}]`, "type-shape"},
		{"type-number", `[{"type":42}]`, "type-shape"},
		{"text-object", `[{"type":"text","text":{"private-key":"private body"}}]`, "text-shape"},
		{"text-array", `[{"type":"text","text":["private body"]}]`, "text-shape"},
		{"thinking-text-number", `[{"type":"thinking","text":42}]`, "text-shape"},
		{"null", `null`, "text"},
		{"empty-array", `[]`, "text"},
		{"null-element", `[null]`, "type"},
		{"unknown-type", `[{"type":"private body"}]`, "type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := `{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop","content":` + tc.content + `}]}`
			started := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
			record, err := ParseProductionWorkerResult(context.Background(), ProductionResultInput{
				Transcript: []byte(jsonLines(captureSessionHeader("session-1"), `{"type":"agent_start"}`, end)),
				Worktree:   "/worktree", TaskID: "TASK-1", RunID: "run-1", AttemptID: "attempt-1",
				Executable: "/usr/local/bin/pi", Version: "0.84.4", StartedAt: started,
				CompletedAt: started.Add(time.Second), MaxOutputBytes: 1 << 20,
			})
			if code := ProductionResultFailureCode(err); code != "pi-result-final-content-"+tc.want || !errors.Is(err, ErrProtocol) || len(record.Data) != 0 {
				t.Fatalf("classification=%q, expected=%q; rejected=%v, empty=%v", code, tc.want, errors.Is(err, ErrProtocol), len(record.Data) == 0)
			}
		})
	}
	for _, err := range []error{
		errors.New("private error"),
		&json.UnmarshalTypeError{Field: "private-key", Value: "private body", Type: reflect.TypeFor[string]()},
		&json.UnmarshalTypeError{Value: "private body"},
	} {
		if productionContentDecodeFailure(err) != "final-content-shape" {
			t.Fatal("unknown decoder metadata escaped the closed fallback")
		}
	}
}

func TestProductionResultMissingFinalContentNeverBorrowsEarlierMessage(t *testing.T) {
	declared, err := json.Marshal(validDeclaredResult("worker-claim"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stop := range []string{"stop", "length"} {
		for _, history := range []bool{false, true} {
			messages := []any{}
			if history {
				messages = append(messages, map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": string(declared)}}})
			}
			messages = append(messages, map[string]any{"role": "assistant", "stopReason": stop})
			end, err := json.Marshal(map[string]any{"type": "agent_end", "willRetry": false, "messages": messages})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
			record, err := ParseProductionWorkerResult(context.Background(), ProductionResultInput{
				Transcript: []byte(jsonLines(captureSessionHeader("session-1"), `{"type":"agent_start"}`, string(end))),
				Worktree:   "/worktree", TaskID: "TASK-1", RunID: "run-1", AttemptID: "attempt-1", Executable: "/usr/local/bin/pi", Version: "0.84.4",
				StartedAt: started, CompletedAt: started.Add(time.Second), MaxOutputBytes: 1 << 20,
			})
			want := "pi-result-final-content-missing"
			if stop == "length" {
				// Provider failure takes precedence over carrier decoding.
				want = "pi-result-provider-terminal"
			}
			if ProductionResultFailureCode(err) != want || len(record.Data) != 0 || (stop == "stop" && !errors.Is(err, ErrProtocol)) {
				t.Fatalf("missing final content: stop=%s history=%t code=%s", stop, history, ProductionResultFailureCode(err))
			}
		}
	}
}

func TestProductionResultFailureClassificationDoesNotChangeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		{"valid", ""}, {"trailing", "pi-result-final-object-trailing"},
		{"multiple", "pi-result-final-object-multiple"}, {"invalid", "pi-result-final-object-invalid"},
		{"business-prefix", ""},
		{"missing", "pi-result-final-object-missing"}, {"identity", "pi-result-declared-identity"},
		{"schema", "pi-result-declared-schema-artifacts"}, {"protocol", "pi-result-transcript-json"},
		{"session", "pi-result-transcript-session"}, {"closure", "pi-result-transcript-closure"},
		{"limit", "pi-result-output-limit"}, {"input", "pi-result-input"},
		{"provider", "pi-result-provider-terminal"}, {"normalize", "pi-result-normalized-schema-adapter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declared := validDeclaredResult("worker-claim")
			if tc.name == "schema" {
				declared["declaredArtifacts"] = "sensitive-field-value"
			}
			data, err := json.Marshal(declared)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if tc.name == "trailing" {
				text += "\n``` sensitive suffix"
			} else if tc.name == "missing" {
				text = "sensitive non-result text"
			} else if tc.name == "multiple" {
				text = `{"kind":"WorkerResult","taskId":"OTHER"}` + text
			} else if tc.name == "invalid" {
				text = `{"kind":"WorkerResult","kind":"Other","private":"sensitive-field-value"}` + text
			} else if tc.name == "business-prefix" || tc.name == "identity" || tc.name == "schema" {
				text = `业务示例 {"total":12} ` + text
			}
			stop := "stop"
			if tc.name == "provider" {
				stop = "error"
			}
			end, err := json.Marshal(map[string]any{"type": "agent_end", "willRetry": false,
				"messages": []any{map[string]any{"role": "assistant", "stopReason": stop,
					"content": []any{map[string]any{"type": "text", "text": text}}}}})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
			input := ProductionResultInput{Transcript: []byte(jsonLines(captureSessionHeader("session-1"), `{"type":"agent_start"}`, string(end))),
				Worktree: "/worktree", TaskID: "TASK-1", RunID: "run-1", AttemptID: "attempt-1",
				Executable: "/usr/local/bin/pi", Version: "0.84.4", StartedAt: started, CompletedAt: started.Add(time.Second), MaxOutputBytes: 1 << 20}
			switch tc.name {
			case "identity":
				input.TaskID = "OTHER"
			case "protocol":
				input.Transcript = []byte("sensitive malformed event\n")
			case "session":
				input.Worktree = "/different-worktree"
			case "closure":
				input.Transcript = []byte(jsonLines(captureSessionHeader("session-1"), `{"type":"agent_start"}`))
			case "limit":
				input.MaxOutputBytes = 8
			case "input":
				input.Worktree = "sensitive-relative-path"
			case "normalize":
				input.Model = strings.Repeat("x", 513)
			}
			record, err := ParseProductionWorkerResult(context.Background(), input)
			if code := ProductionResultFailureCode(err); code != tc.want {
				t.Fatalf("code=%q want=%q error=%v", code, tc.want, err)
			}
			if tc.want == "" {
				if err != nil || record.Kind != "WorkerResult" {
					t.Fatal("valid result no longer admitted")
				}
			} else if err == nil || len(record.Data) != 0 {
				t.Fatal("classification admitted a rejected result")
			}
			if tc.name == "trailing" && !errors.Is(err, ErrProtocol) {
				t.Fatal("classification lost original protocol identity")
			}
		})
	}
}

func TestProductionResultDiagnosticHasClosedProvenanceAndBoundedSchemaWalk(t *testing.T) {
	for _, err := range []error{nil, errors.New("pi-result-input"), &productionResultFailure{code: "sensitive\nvalue", cause: ErrProtocol}} {
		if ProductionResultFailureCode(err) != "" {
			t.Fatal("unclassified text became a diagnostic")
		}
	}
	root := &jsonschema.ValidationError{InstanceLocation: []string{"sensitive-name"}}
	root.Causes = []*jsonschema.ValidationError{root, nil, {InstanceLocation: []string{"declaredArtifacts", "sensitive-index"}}}
	err := &productionResultFailure{code: "declared-schema", cause: root}
	if ProductionResultFailureCode(fmt.Errorf("wrapper: %w", err)) != "pi-result-declared-schema-artifacts" {
		t.Fatal("closed schema field was not preserved")
	}
	root.Causes = []*jsonschema.ValidationError{root}
	if ProductionResultFailureCode(err) != "pi-result-declared-schema" {
		t.Fatal("cyclic schema diagnostics did not remain bounded")
	}
}
