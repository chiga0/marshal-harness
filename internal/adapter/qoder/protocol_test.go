package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/port"
)

// classifyNow is the fixed reference time for terminal classification unit
// tests, so the typed failure construction can be replayed deterministically.
var classifyNow = time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

func TestDecodeEventLineExtractsSessionUsageAndTerminal(t *testing.T) {
	stream, err := os.ReadFile("testdata/qoder-1.1.23-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	result := decodeTranscript(stream)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "sess-1" || result.model != "provider/model" || result.cliVersion != "1.1.23" || result.protocolVersion != "1.2.0" || result.permissionMode != "acceptEdits" || result.assistantCount != 1 || result.eventCount != 3 || result.inputTokens != 10 || result.outputTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want a success terminal", result.terminal)
	}
}

func TestDecodeTranscriptAcceptsFragmentedSameMessageToolUses(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"thinking","thinking":"opaque"}]}}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"opaque"}]}}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"tool_use","id":"tool-read","name":"Read","input":{"filePath":"README.md"}}]}}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-grep","name":"Grep","input":{"path":"internal"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-read","content":"ok"},{"type":"tool_result","tool_use_id":"tool-grep","content":"ok"}]}}
{"type":"assistant","message":{"id":"message-2","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"opaque"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.eventCount != 8 || result.assistantCount != 5 || result.toolCalls != 2 {
		t.Fatalf("counts = events %d assistant %d tools %d", result.eventCount, result.assistantCount, result.toolCalls)
	}
	if got := strings.Join(result.toolNames, ","); got != "read,grep" {
		t.Fatalf("toolNames = %q, want read,grep", got)
	}
	if !result.terminal.seen || !result.terminal.success || len(result.pendingTools) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDecodeTranscriptFoldsExactToolReplayWithinFragmentedMessage(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"tool_use","id":"tool-read","name":"Read","input":{"filePath":"README.md"}}]}}
{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-read","name":"Read","input":{"filePath":"README.md"}},{"type":"tool_use","id":"tool-grep","name":"Grep","input":{"path":"internal"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-read","content":"ok"},{"type":"tool_result","tool_use_id":"tool-grep","content":"ok"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.toolCalls != 2 || len(result.observedTools) != 2 {
		t.Fatalf("tool projection duplicated: calls=%d observed=%d", result.toolCalls, len(result.observedTools))
	}
}

func TestDecodeTranscriptRejectsFragmentedMessageBoundaryDrift(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`
	toolA := `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"README.md"}}]}}`
	tests := map[string]string{
		"message id changed":             init + "\n" + toolA + "\n" + `{"type":"assistant","message":{"id":"message-2","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-2","name":"Grep","input":{"path":"internal"}}]}}` + "\n",
		"message id disappeared":         init + "\n" + toolA + "\n" + `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-2","name":"Grep","input":{"path":"internal"}}]}}` + "\n",
		"conflicting replay":             init + "\n" + toolA + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"OTHER.md"}}]}}` + "\n",
		"result before stop":             init + "\n" + toolA + "\n" + `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}` + "\n",
		"terminal before stop":           init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"opaque"}]}}` + "\n" + `{"type":"result","subtype":"success","is_error":false}` + "\n",
		"post final frame":               init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"opaque"}]}}` + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"end_turn","content":[]}}` + "\n",
		"post final different id":        init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"opaque"}]}}` + "\n" + `{"type":"assistant","message":{"id":"message-2","role":"assistant","stop_reason":"end_turn","content":[]}}` + "\n",
		"duplicate replay member":        init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"README.md","filePath":"OTHER.md"}}]}}` + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"OTHER.md"}}]}}` + "\n",
		"nested duplicate replay member": init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":null,"content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"options":{"path":"README.md","path":"OTHER.md"}}}]}}` + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"options":{"path":"OTHER.md"}}}]}}` + "\n",
		"unknown stop reason":            init + "\n" + `{"type":"assistant","message":{"id":"message-1","role":"assistant","stop_reason":"future_reason","content":[{"type":"text","text":"opaque"}]}}` + "\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			result := decodeTranscript([]byte(stream))
			if !errors.Is(result.err, ErrProtocol) {
				t.Fatalf("err = %v, want ErrProtocol", result.err)
			}
		})
	}
}

func TestDecodeEventLineAssociatesToolResultsAndCapturesPermissionDenials(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"tee marshal-worker-result.json"}}]}}
{"type":"user","tool_use_result":{"isHardFailure":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"Permission confirmation required, but no interactive handler is available"}]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.toolCalls != 1 || len(result.denials) != 1 || len(result.toolNames) != 0 {
		t.Fatalf("capture = %+v, want one denied tool call and no successful tool", result)
	}
	if result.denials[0].Tool != "bash" {
		t.Fatalf("denial tool = %q, want bash", result.denials[0].Tool)
	}
	var input map[string]string
	if err := json.Unmarshal(result.denials[0].Input, &input); err != nil || input["command"] != "tee marshal-worker-result.json" {
		t.Fatalf("denial input = %s err=%v", result.denials[0].Input, err)
	}
}

func TestObservedQoderReadDenialInsideWorktreeGradesBenign(t *testing.T) {
	worktree := t.TempDir()
	target := worktree + "/README.md"
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{"filePath": target})
	if err != nil {
		t.Fatal(err)
	}
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":` + string(input) + `}]}}
{"type":"user","tool_result_meta":[{"id":"tool-1","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"Permission confirmation required, but no interactive handler is available"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || len(result.denials) != 1 {
		t.Fatalf("capture = %+v, want one bound denial", result)
	}
	records := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: worktree, ControlRoot: t.TempDir(), TempDir: os.TempDir()}, result.denials, func() time.Time { return classifyNow })
	if len(records) != 1 || records[0].Tool != "read" || records[0].Grade != string(denials.Benign) {
		t.Fatalf("records = %+v, want benign Qoder Read denial", records)
	}
}

func TestDecodeEventLineDistinguishesSuccessfulAndNonPermissionToolResults(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"/tmp/a"}},{"type":"tool_use","id":"tool-2","name":"Grep","input":{"path":"/tmp"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":false,"content":"ok"},{"type":"tool_result","tool_use_id":"tool-2","is_error":true,"content":"file not found"}]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.denials) != 0 || strings.Join(result.toolNames, ",") != "read" {
		t.Fatalf("capture = %+v, want only the successful read call", result)
	}
}

func TestNormalizeQoderToolNameUsesExactFrozenVocabulary(t *testing.T) {
	want := map[string]string{
		"Agent": "agent", "Bash": "bash", "Edit": "edit", "Glob": "glob", "Grep": "grep",
		"Read": "read", "TaskCreate": "task_create", "TaskUpdate": "task_update", "Write": "write",
	}
	for raw, normalized := range want {
		if got := normalizeQoderToolName(raw); got != normalized {
			t.Fatalf("normalizeQoderToolName(%q) = %q, want %q", raw, got, normalized)
		}
	}
	for _, raw := range []string{"rEaD", " read", "Read ", "Unknown", ""} {
		if got := normalizeQoderToolName(raw); got != "qoder-unknown" {
			t.Fatalf("unreviewed tool %q normalized to %q", raw, got)
		}
	}
}

func TestCaseVariantQoderToolDenialRemainsFatalUnknown(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"rEaD","input":{"filePath":"/tmp/a"}}]}}
{"type":"user","tool_use_result":{"isHardFailure":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"Permission confirmation required, but no interactive handler is available"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || len(result.denials) != 1 || result.denials[0].Tool != "qoder-unknown" {
		t.Fatalf("capture = %+v, want one unknown denial", result)
	}
	records := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: t.TempDir(), ControlRoot: t.TempDir(), TempDir: os.TempDir()}, result.denials, func() time.Time { return classifyNow })
	if len(records) != 1 || records[0].Kind != string(denials.KindUnknown) || records[0].Grade != string(denials.Fatal) {
		t.Fatalf("records = %+v, want fatal unknown", records)
	}
}

func TestSuccessfulQoderAgentToolIsProtocolInvalid(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Agent","input":{"description":"child"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"completed"}]}}
`
	result := decodeTranscript([]byte(stream))
	if result.err == nil || !errors.Is(result.err, ErrProtocol) || len(result.toolNames) != 0 {
		t.Fatalf("result = %+v, want forbidden Agent protocol failure", result)
	}
}

func TestDeniedQoderAgentToolRemainsFatalDenial(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Agent","input":{"description":"child"}}]}}
{"type":"user","tool_result_meta":[{"id":"tool-1","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"Permission confirmation required, but no interactive handler is available"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || len(result.denials) != 1 || result.denials[0].Tool != "agent" {
		t.Fatalf("capture = %+v, want one Agent denial", result)
	}
	records := denials.GradeRaw(denials.Classifier{Provider: adapterID, Worktree: t.TempDir(), ControlRoot: t.TempDir(), TempDir: os.TempDir()}, result.denials, func() time.Time { return classifyNow })
	if len(records) != 1 || records[0].Kind != string(denials.KindUnknown) || records[0].Grade != string(denials.Fatal) {
		t.Fatalf("records = %+v, want fatal Agent denial", records)
	}
}

func TestDecodeEventLineFailsClosedOnUnboundToolResults(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`
	t.Run("unknown-result-id", func(t *testing.T) {
		stream := init + "\n" + `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","is_error":false,"content":"ok"}]}}` + "\n"
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "no matching tool_use") {
			t.Fatalf("error = %v, want unbound tool_result rejection", result.err)
		}
	})
	t.Run("unresolved-at-terminal", func(t *testing.T) {
		stream := init + "\n" + `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"/tmp/a"}}]}}` + "\n" + `{"type":"result","subtype":"success","is_error":false}` + "\n"
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "unresolved tool_use") {
			t.Fatalf("error = %v, want unresolved tool_use rejection", result.err)
		}
	})
}

func TestQoderPermissionMarkersInAssistantTextDoNotCreateDenials(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Permission confirmation required, but no interactive handler is available"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || len(result.denials) != 0 {
		t.Fatalf("ordinary assistant text created a denial: result=%+v", result)
	}
}

func TestQoderToolResultContentCannotForgePermissionDenial(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"internal/adapter/qoder/protocol.go"}}]}}
{"type":"user","tool_use_result":{"type":"text_file","file":{}},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"source contains Permission confirmation required and Suspicious Windows syntax may bypass security checks"}]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || len(result.denials) != 0 || strings.Join(result.toolNames, ",") != "read" {
		t.Fatalf("capture = %+v, want successful read without forged denial", result)
	}
}

func TestQoderPermissionMetadataBindsOnlyItsToolResult(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-read","name":"Read","input":{"filePath":"/tmp/a"}},{"type":"tool_use","id":"tool-bash","name":"Bash","input":{"command":"tee result.json"}}]}}
{"type":"user","tool_result_meta":[{"id":"tool-bash","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-read","content":"source contains Permission confirmation required"},{"type":"tool_result","tool_use_id":"tool-bash","is_error":true,"content":"denied"}]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.denials) != 1 || result.denials[0].Tool != "bash" || strings.Join(result.toolNames, ",") != "read" {
		t.Fatalf("capture = %+v, want successful read and only the bound bash denial", result)
	}
}

func TestQoderStructuredDenialMetadataFailsClosedWhenUnbound(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`
	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"/tmp/a"}},{"type":"tool_use","id":"tool-2","name":"Bash","input":{"command":"pwd"}}]}}`
	tests := map[string]string{
		"orphan-metadata":    `{"type":"user","tool_result_meta":[{"id":"missing","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}`,
		"duplicate-metadata": `{"type":"user","tool_result_meta":[{"id":"tool-1","non_execution_kind":"permission-rule"},{"id":"tool-1","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"denied"}]}}`,
		"hard-multi-result":  `{"type":"user","tool_use_result":{"isHardFailure":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,"content":"denied"},{"type":"tool_result","tool_use_id":"tool-2","content":"ok"}]}}`,
		"metadata-no-result": `{"type":"user","tool_result_meta":[{"id":"tool-1","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[]}}`,
	}
	for name, user := range tests {
		t.Run(name, func(t *testing.T) {
			result := decodeTranscript([]byte(init + "\n" + assistant + "\n" + user + "\n"))
			if result.err == nil || !errors.Is(result.err, ErrProtocol) {
				t.Fatalf("result = %+v, want protocol failure", result)
			}
		})
	}
}

func TestDecodeEventLineFailsClosedOnPermissionMarkerStatusConflict(t *testing.T) {
	for _, status := range []string{"", `,"is_error":false`} {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"tee marshal-worker-result.json"}}]}}
{"type":"user","tool_result_meta":[{"id":"tool-1","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1"` + status + `,"content":"Permission confirm\u0061tion required, but no interactive handler is available"}]}}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !errors.Is(result.err, ErrProtocol) || len(result.denials) != 1 {
			t.Fatalf("status %q result = %+v, want captured denial plus protocol conflict", status, result)
		}
	}
}

func TestDecodeEventLineRejectsStructuredToolResultContent(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"/tmp/a"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":{"text":"ok"}}]}}
`
	result := decodeTranscript([]byte(stream))
	if result.err == nil || !errors.Is(result.err, ErrProtocol) || !strings.Contains(result.err.Error(), "JSON string") {
		t.Fatalf("result = %+v, want structured content rejection", result)
	}
}

func TestDecodeEventLineAcceptsObservedMissingIsErrorSuccess(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"filePath":"/tmp/a"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil || strings.Join(result.toolNames, ",") != "read" {
		t.Fatalf("result = %+v, want observed missing-is_error success", result)
	}
}

func TestWorkerResultTransportSequenceAcceptsExactlyOneFinalSuccessfulTee(t *testing.T) {
	events := []string{`{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`}
	events = append(events, successfulWorkerResultTeeEvents("tool-result")...)
	events = append(events, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`)
	events = append(events, `{"type":"result","subtype":"success","is_error":false}`)
	result := decodeTranscript([]byte(strings.Join(events, "\n") + "\n"))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := validateWorkerResultTransportSequence(result); err != nil {
		t.Fatalf("valid final tee rejected: %v; sequence=%+v tools=%d", err, result.resultTransport, result.toolCalls)
	}
}

func TestWorkerResultTransportSequenceAcceptsObservedQoderBashDescription(t *testing.T) {
	payload := []byte(`{"status":"completed"}`)
	command, err := json.Marshal(workerResultTeeCommand(payload))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"Performance","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tee-observed","name":"Bash","input":{"command":` + string(command) + `,"description":"Emit final WorkerResult via adapter-held result channel"}}]}}`,
		`{"type":"user","tool_use_result":{"kind":"completed"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tee-observed","content":"(Bash completed with no output)"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"end turn"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Task complete."}]}}`,
		`{"type":"result","subtype":"success","is_error":false}`,
	}
	result := decodeTranscript([]byte(strings.Join(events, "\n") + "\n"))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := validateWorkerResultTransportSequence(result); err != nil {
		t.Fatalf("observed Qoder description envelope rejected: %v; sequence=%+v tools=%d", err, result.resultTransport, result.toolCalls)
	}
	if string(result.resultTransport.payload) != string(payload) {
		t.Fatalf("payload = %q, want %q", result.resultTransport.payload, payload)
	}
}

func TestWorkerResultTeeCommandUsesClosedGrammar(t *testing.T) {
	valid := workerResultTeeCommand([]byte(`{"status":"completed"}`))
	if payload, ok := parseWorkerResultTeeCommand(valid); !ok || string(payload) != `{"status":"completed"}` {
		t.Fatalf("valid declaration = %q, %t", payload, ok)
	}
	if payload, ok := parseWorkerResultTeeCommand(valid + "\n"); !ok || string(payload) != `{"status":"completed"}` {
		t.Fatalf("canonical declaration with one trailing LF = %q, %t", payload, ok)
	}
	for name, command := range map[string]string{
		"command-after-delimiter": valid + "\ngit status",
		"trailing-blank-line":     valid + "\n\n",
		"trailing-cr":             valid + "\r",
		"cr-before-delimiter":     strings.Replace(valid, "\nMARSHAL_RESULT", "\r\nMARSHAL_RESULT", 1),
		"split-tee":               strings.Replace(valid, " | tee ", " | t''ee ", 1),
		"glob-tee":                strings.Replace(valid, " | tee ", " | t?? ", 1),
		"background-fd":           strings.Replace(valid, " > /dev/null\n", " 9>/dev/null &\n", 1),
		"alternate-sink":          strings.Replace(valid, "tee /dev/null", "tee ./marshal-worker-result.json", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if validWorkerResultTeeCommand(command) {
				t.Fatal("closed declaration grammar accepted alternate shell semantics")
			}
		})
	}
}

func TestWorkerResultTransportRejectsNonCanonicalJSONInput(t *testing.T) {
	valid := workerResultTeeCommand([]byte(`{"status":"completed"}`))
	canonical, err := json.Marshal(struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}{Command: valid, Description: "Emit final WorkerResult via adapter-held result channel"})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]json.RawMessage{
		"missing-description":         json.RawMessage(`{"command":` + string(mustJSON(t, valid)) + `}`),
		"empty-description":           json.RawMessage(strings.Replace(string(canonical), `"Emit final WorkerResult via adapter-held result channel"`, `""`, 1)),
		"control-description":         json.RawMessage(strings.Replace(string(canonical), `"Emit final WorkerResult via adapter-held result channel"`, `"bad\nvalue"`, 1)),
		"overlong-description":        json.RawMessage(strings.Replace(string(canonical), `"Emit final WorkerResult via adapter-held result channel"`, `"`+strings.Repeat("x", qoderBashDescriptionMaxBytes+1)+`"`, 1)),
		"unicode-semantic-equivalent": json.RawMessage(strings.Replace(string(canonical), `cat`, `\u0063at`, 1)),
		"extra-field":                 json.RawMessage(strings.TrimSuffix(string(canonical), "}") + `,"other":true}`),
		"description-wrong-type":      json.RawMessage(strings.Replace(string(canonical), `"description":"Emit final WorkerResult via adapter-held result channel"`, `"description":true`, 1)),
		"duplicate-command":           json.RawMessage(strings.TrimSuffix(string(canonical), "}") + `,"command":` + string(mustJSON(t, valid)) + `}`),
		"duplicate-description":       json.RawMessage(strings.TrimSuffix(string(canonical), "}") + `,"description":"duplicate"}`),
		"reordered-fields":            json.RawMessage(`{"description":"Emit final WorkerResult via adapter-held result channel","command":` + string(mustJSON(t, valid)) + `}`),
		"leading-whitespace":          append(json.RawMessage(" "), canonical...),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			access, accepted, payload := classifyWorkerResultTransportTool("bash", input)
			if !access || accepted || len(payload) != 0 {
				t.Fatalf("classification = access:%t accepted:%t payload:%q", access, accepted, payload)
			}
		})
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestDeniedFinalWorkerResultTeeIsNotARepeatedSequenceViolation(t *testing.T) {
	events := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`,
		workerResultTeeToolUseEvent("tee-denied"),
		`{"type":"user","tool_use_result":{"isHardFailure":true},"tool_result_meta":[{"id":"tee-denied","non_execution_kind":"permission-rule"}],"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tee-denied","is_error":true,"content":"denied"}]}}`,
		`{"type":"result","subtype":"success","is_error":false}`,
	}
	result := decodeTranscript([]byte(strings.Join(events, "\n") + "\n"))
	if result.err != nil || len(result.denials) != 1 || result.resultTransport.attempts != 1 || result.resultTransport.successes != 0 || result.resultTransport.invalidAccess || workerResultTransportSequenceViolation(result) {
		t.Fatalf("denied final tee sequence = %+v denials=%d err=%v", result.resultTransport, len(result.denials), result.err)
	}
}

func TestWorkerResultTransportSequenceRejectsNonFinalAndRepeatedAccess(t *testing.T) {
	toolUse := func(id, name, input string) string {
		return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
	}
	toolResult := func(id string) string {
		return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}}`
	}
	tee := successfulWorkerResultTeeEvents("tee-1")
	corrected := successfulWorkerResultTeeEvents("tee-2")
	tests := map[string][]string{
		"tee-read": append(append([]string{}, tee...),
			toolUse("read-1", "Read", `{"file_path":"marshal-worker-result.json"}`), toolResult("read-1")),
		"tee-read-edit": append(append([]string{}, tee...),
			toolUse("read-1", "Read", `{"file_path":"marshal-worker-result.json"}`), toolResult("read-1"),
			toolUse("edit-1", "Edit", `{"file_path":"marshal-worker-result.json","old_string":"typo","new_string":"fixed"}`), toolResult("edit-1")),
		"tee-extra-bash": append(append([]string{}, tee...),
			toolUse("bash-2", "Bash", `{"command":"git diff --name-only"}`), toolResult("bash-2")),
		"tee-second-tee": append(append([]string{}, tee...), corrected...),
		"invalid-then-corrected": append([]string{
			toolUse("tee-invalid", "Bash", `{"command":"cat <<'MARSHAL_RESULT' | t''ee /dev/null > /dev/null\n{}\nMARSHAL_RESULT"}`), toolResult("tee-invalid"),
		}, corrected...),
	}
	for name, sequence := range tests {
		t.Run(name, func(t *testing.T) {
			events := []string{`{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`}
			events = append(events, sequence...)
			events = append(events, `{"type":"result","subtype":"success","is_error":false}`)
			result := decodeTranscript([]byte(strings.Join(events, "\n") + "\n"))
			if result.err != nil {
				t.Fatal(result.err)
			}
			if err := validateWorkerResultTransportSequence(result); !errors.Is(err, ErrProtocol) {
				t.Fatalf("sequence accepted: %+v", result.resultTransport)
			}
		})
	}
}

func TestDecodeEventLineRecordsErrorTerminalCode(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"connection_failed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || result.terminal.success || result.terminal.code != "connection_failed" {
		t.Fatalf("terminal = %+v", result.terminal)
	}
}

func TestDecodeEventLineRecordsErrorTerminalWithSuccessSubtype(t *testing.T) {
	// Real qodercli 1.1.23 emits an error result with subtype=success and an
	// `error` code; the adapter must classify it as a provider error rather
	// than a contradictory protocol violation, and must not leak the code.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"completed","error":"authentication_failed"}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || result.terminal.success || result.terminal.code != "authentication_failed" {
		t.Fatalf("terminal = %+v, want error terminal with code authentication_failed", result.terminal)
	}
	failure := classifyTerminalFailure(result.terminal.code, classifyNow)
	typed, ok := port.AsAdapterFailure(failure)
	if !ok || typed.Kind != port.FailureKindProviderTerminal || typed.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("classification = %v, want provider-terminal/do-not-retry", failure)
	}
	if strings.Contains(failure.Error(), "authentication_failed") {
		t.Fatalf("Error() leaked the provider error code: %s", failure.Error())
	}
}

func TestDecodeEventLineRejectsMalformedAndBlank(t *testing.T) {
	for _, input := range []string{"not-json\n", "\n", "   \n"} {
		result := decodeTranscript([]byte(input))
		if result.err == nil {
			t.Fatalf("input %q did not produce a protocol error", input)
		}
	}
	// An empty stream is not a protocol error on its own; session and terminal
	// presence are enforced separately by Run.
	if result := decodeTranscript(nil); result.err != nil {
		t.Fatalf("empty stream must not error: %v", result.err)
	}
}

func TestDecodeEventLineRejectsDuplicateAndUnrecognizedTerminal(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "follows terminal") {
			t.Fatalf("error = %v, want event-after-terminal error", result.err)
		}
	})
	t.Run("contradictory-status", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":false,"terminal_reason":"completed"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "contradictory") {
			t.Fatalf("error = %v, want contradictory terminal error", result.err)
		}
	})
}

func TestDecodeEventLineRejectsFabricatedLegacyContract(t *testing.T) {
	result := decodeTranscript([]byte("{\"type\":\"session\",\"id\":\"sess-1\"}\n"))
	if result.err == nil {
		t.Fatal("legacy fabricated session event was accepted")
	}
}

func TestClassifyTerminalFailureFollowsClosedCodeTable(t *testing.T) {
	for _, test := range []struct {
		code        string
		kind        port.FailureKind
		disposition port.RetryDisposition
	}{
		{code: "connection_failed", kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{code: "rate_limited", kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{code: "quota_exceeded", kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{code: "", kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{code: "mystery_code", kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
	} {
		t.Run(test.code, func(t *testing.T) {
			failure := classifyTerminalFailure(test.code, classifyNow)
			typed, ok := port.AsAdapterFailure(failure)
			if !ok {
				t.Fatalf("classification did not produce a typed failure: %v", failure)
			}
			if typed.Adapter != port.AdapterIDQoder || typed.Kind != test.kind || typed.Disposition != test.disposition {
				t.Fatalf("classification = %s/%s/%s, want qoder %s/%s", typed.Adapter, typed.Kind, typed.Disposition, test.kind, test.disposition)
			}
			wantDisposition, _ := port.DispositionFor(test.kind)
			if typed.Disposition != wantDisposition {
				t.Fatalf("disposition %s violates the unique pairing for %s", typed.Disposition, test.kind)
			}
			// Error() never echoes the provider code: only closed enums and
			// fixed detail vocabulary may appear.
			if strings.Contains(failure.Error(), test.code) && test.code != "" {
				t.Fatalf("Error() leaked the provider code %q: %s", test.code, failure.Error())
			}
		})
	}
}

func TestClassifyTerminalFailureReturnsPermanentDefault(t *testing.T) {
	failure := classifyTerminalFailure("", classifyNow)
	typed, ok := port.AsAdapterFailure(failure)
	if !ok || typed.Kind != port.FailureKindProviderTerminal || typed.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("empty code must default to provider-terminal/do-not-retry: %v", failure)
	}
	if strings.Contains(failure.Error(), "mystery") {
		t.Fatalf("Error() leaked unknown provider input: %s", failure.Error())
	}
}

func TestResolveAttemptFailureKeepsFatalDenialPermanentAcrossConflicts(t *testing.T) {
	for _, test := range []struct {
		name        string
		capture     captureResult
		observation attemptObservation
		wantKind    port.FailureKind
	}{
		{
			name:        "nonzero-process",
			capture:     captureResult{sessionID: "s", model: "m", terminal: terminalOutcome{seen: true, success: true}},
			observation: attemptObservation{processFailed: true, exitCode: 7},
			wantKind:    port.FailureKindProviderTerminal,
		},
		{
			name:     "retryable-terminal",
			capture:  captureResult{sessionID: "s", model: "m", terminal: terminalOutcome{seen: true, code: "connection_failed"}},
			wantKind: port.FailureKindProviderTerminal,
		},
		{
			name:     "transcript-conflict",
			capture:  captureResult{err: ErrProtocol},
			wantKind: port.FailureKindProtocolInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := resolveAttemptFailure(test.capture, test.observation, context.Background(), 1, classifyNow)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != test.wantKind || failure.Disposition != port.RetryDispositionDoNotRetry {
				t.Fatalf("error = %v, want %s/do-not-retry", err, test.wantKind)
			}
		})
	}
}

func TestResolveAttemptFailureKeepsCancellationAndOutputLimitPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resolveAttemptFailure(captureResult{}, attemptObservation{}, ctx, 1, classifyNow); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if err := resolveAttemptFailure(captureResult{limitExceeded: true}, attemptObservation{}, context.Background(), 1, classifyNow); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("error = %v, want output limit", err)
	}
}

func TestDecodeEventLineAcceptsObservedHookFramesBeforeInit(t *testing.T) {
	// Real qodercli 1.1.23 emits hook_started/hook_progress/hook_response
	// system frames before system init; they are non-semantic and must not
	// fail the transcript or disturb the init/assistant/result sequence.
	stream := `{"type":"system","subtype":"hook_started"}
{"type":"system","subtype":"hook_progress"}
{"type":"system","subtype":"hook_response"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","usage":{"input_tokens":10,"output_tokens":5}}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.sessionID != "sess-1" || result.model != "provider/model" || result.assistantCount != 1 || result.eventCount != 6 {
		t.Fatalf("result = %+v", result)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want success terminal after hook frames", result.terminal)
	}
}

func TestDecodeEventLineRejectsUnknownSystemSubtypeAndDuplicateInit(t *testing.T) {
	t.Run("unknown-subtype", func(t *testing.T) {
		stream := `{"type":"system","subtype":"mystery"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "unrecognized system event subtype") {
			t.Fatalf("error = %v, want unrecognized system event subtype error", result.err)
		}
	})
	t.Run("duplicate-init", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "invalid or duplicate system init event") {
			t.Fatalf("error = %v, want duplicate init error", result.err)
		}
	})
	t.Run("session-change", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]},"session_id":"sess-2"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "session id changed") {
			t.Fatalf("error = %v, want session change error", result.err)
		}
	})
	t.Run("hook-after-terminal", func(t *testing.T) {
		stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed"}
{"type":"system","subtype":"hook_response"}
`
		result := decodeTranscript([]byte(stream))
		if result.err == nil || !strings.Contains(result.err.Error(), "follows terminal") {
			t.Fatalf("error = %v, want event-after-terminal error", result.err)
		}
	})
}

func TestDecodeEventLineAcceptsSuccessWithoutTerminalReason(t *testing.T) {
	// Real logged-in qodercli 1.1.23 may emit a success result without a
	// terminal_reason field; success must not require it as long as the
	// session, subtype and at least one assistant message are present.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"result","subtype":"success","is_error":false}
`
	result := decodeTranscript([]byte(stream))
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.terminal.seen || !result.terminal.success {
		t.Fatalf("terminal = %+v, want success terminal without terminal_reason", result.terminal)
	}
}

func TestDecodeEventLineRejectsErrorTerminalWithoutCode(t *testing.T) {
	// An error terminal must still yield a fixed classification via `error`
	// or terminal_reason; with neither it is an incomplete protocol event.
	stream := `{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}
{"type":"result","subtype":"error_during_execution","is_error":true}
`
	result := decodeTranscript([]byte(stream))
	if result.err == nil || !strings.Contains(result.err.Error(), "error terminal has no code") {
		t.Fatalf("error = %v, want error terminal no code error", result.err)
	}
}
