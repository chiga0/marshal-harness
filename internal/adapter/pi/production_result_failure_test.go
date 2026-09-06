package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestProductionResultFailureClassificationDoesNotChangeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		{"valid", ""}, {"trailing", "pi-result-final-object-trailing"},
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
