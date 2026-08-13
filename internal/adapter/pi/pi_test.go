package pi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	if _, err := New("pi", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	executable := fakeExecutable(t, "0.84.1", "", "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(executable, symlink); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(symlink, validator)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.executable == symlink {
		t.Fatal("symlink was not resolved through realpath")
	}
}

func TestPrepareTerminalFreezesNativeTUIWithoutJSONPrintOrPrompt(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	spec, err := fixture.adapter.PrepareTerminal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	expectedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(spec.Arguments, "\x00")
	joinedEnv := strings.Join(spec.Environment, "\n")
	if spec.AdapterID != adapterID || spec.BinaryVersion != supportedBinary || spec.Executable != fixture.executable || !strings.HasPrefix(spec.ExecutableDigest, "sha256:") || spec.WorkingDirectory != expectedWorktree {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.InitialPrompt != "完成 fixture" || spec.CompletionGate != port.TerminalCompletionSupervisedConfirmation {
		t.Fatalf("prompt/gate = %q %q", spec.InitialPrompt, spec.CompletionGate)
	}
	for _, forbidden := range []string{"--mode", "json", "--print", "完成 fixture", "bash"} {
		if containsArgument(spec.Arguments, forbidden) {
			t.Fatalf("native argv contains forbidden argument %q: %#v", forbidden, spec.Arguments)
		}
	}
	for _, required := range []string{"--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--tools", workerTools, "--no-session"} {
		if !strings.Contains(joinedArgs, required) {
			t.Fatalf("native argv lacks %q: %#v", required, spec.Arguments)
		}
	}
	if strings.Contains(joinedEnv, "GITHUB_TOKEN") || strings.Contains(joinedEnv, "publisher-secret") {
		t.Fatalf("publisher credential leaked: %s", joinedEnv)
	}
	if strings.Contains(joinedEnv, "CI=1") || !strings.Contains(joinedEnv, "TERM=xterm-256color") || !strings.Contains(joinedEnv, "COLORTERM=truecolor") {
		t.Fatalf("native TUI environment is not interactive: %s", joinedEnv)
	}
}

func containsArgument(arguments []string, target string) bool {
	for _, argument := range arguments {
		if argument == target {
			return true
		}
	}
	return false
}

func TestProbeFreezesSupportedAndUnsupportedBinary(t *testing.T) {
	for _, test := range []struct{ version, status string }{
		{"0.84.1", "supported"},
		{"0.83.0", "unsupported"},
		{"0.84.0", "unsupported"},
		{"0.85.0", "unsupported"},
		{"unknown", "unsupported"},
	} {
		t.Run(test.version, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "launched")
			adapter, err := New(fakeExecutable(t, test.version, "", "touch "+shellQuote(marker)), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(record.Data, &raw); err != nil {
				t.Fatal(err)
			}
			status, _ := raw["probeStatus"].(string)
			version, _ := raw["binaryVersion"].(string)
			digest, _ := raw["executableDigest"].(string)
			executable, _ := raw["executable"].(string)
			if status != test.status || version != test.version || !strings.HasPrefix(digest, "sha256:") || !filepath.IsAbs(executable) {
				t.Fatalf("snapshot = %s/%s/%s/%s", status, version, digest, executable)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("probe must never launch a worker process: marker stat = %v", statErr)
			}
		})
	}
}

func TestBuildArgsLocksHardeningFlagsAndNeverGrantsBash(t *testing.T) {
	hardening := []string{"--mode", "json", "--print", "--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--tools", workerTools}
	for _, model := range []string{"", "provider/model"} {
		args := buildArgs("workspace-write", model, "完成任务")
		want := append(append([]string{}, hardening...), "--no-session")
		if model != "" {
			want = append(want, "--model", model)
		}
		want = append(want, "完成任务")
		if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("args = %#v", args)
		}
		if !contains(args, "--no-session") || contains(args, "--session") {
			t.Fatalf("args must always carry --no-session and never --session: %#v", args)
		}
		if contains(args, "bash") {
			t.Fatalf("bash granted in args: %#v", args)
		}
		for _, flag := range []string{"--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-session", "--print"} {
			if count := countOccurrences(args, flag); count != 1 {
				t.Fatalf("hardening flag %s must appear exactly once, got %d: %#v", flag, count, args)
			}
		}
	}
	t.Run("read-only-grants-read-grep-find-ls-edit-only", func(t *testing.T) {
		args := buildArgs("read-only", "", "完成任务")
		if !containsSequence(args, "--tools", readOnlyTools) {
			t.Fatalf("read-only args must grant exactly %q: %#v", readOnlyTools, args)
		}
		if contains(args, "bash") || containsSequence(args, "--tools", workerTools) {
			t.Fatalf("read-only args leaked write tools: %#v", args)
		}
	})
}

func TestToolsArgForResolvesDeclaredAllowlistFailClosed(t *testing.T) {
	t.Run("undeclared-keeps-profile-defaults", func(t *testing.T) {
		for profile, want := range map[string]string{"workspace-write": workerTools, "read-only": readOnlyTools} {
			got, err := toolsArgFor(profile, nil)
			if err != nil || got != want {
				t.Fatalf("toolsArgFor(%s, nil) = %q, %v; want %q", profile, got, err, want)
			}
		}
	})
	t.Run("declared-intersects-surface-in-frozen-order", func(t *testing.T) {
		got, err := toolsArgFor("workspace-write", []string{"read", "edit", "write"})
		if err != nil || got != "read,write,edit" {
			t.Fatalf("toolsArgFor = %q, %v; want exactly read,write,edit", got, err)
		}
		for _, banned := range []string{"grep", "find", "ls", "bash"} {
			if strings.Contains(got, banned) {
				t.Fatalf("undeclared tool %q leaked into --tools: %q", banned, got)
			}
		}
	})
	t.Run("bash-declaration-fails-closed", func(t *testing.T) {
		if _, err := toolsArgFor("workspace-write", []string{"read", "bash"}); err == nil || !strings.Contains(err.Error(), "bash") {
			t.Fatalf("err = %v, want pi cannot provide bash", err)
		}
	})
	t.Run("read-only-rejects-write-declaration", func(t *testing.T) {
		if _, err := toolsArgFor("read-only", []string{"read", "write"}); err == nil || !strings.Contains(err.Error(), "write") {
			t.Fatalf("err = %v, want pi cannot provide write under read-only", err)
		}
	})
	t.Run("read-only-honors-read-edit-declaration", func(t *testing.T) {
		got, err := toolsArgFor("read-only", []string{"edit", "read"})
		if err != nil || got != "read,edit" {
			t.Fatalf("toolsArgFor = %q, %v; want read,edit", got, err)
		}
	})
}

func TestRunDeclaredToolsLockArgvToExactIntersection(t *testing.T) {
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, successBody)
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "edit", "write"}}})
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	argv := readArgv(t, fixture.argsPath)
	if !containsSequence(argv, "--tools", "read,write,edit") {
		t.Fatalf("declared allowlist did not lock --tools to the exact intersection: %#v", argv)
	}
	for _, banned := range []string{"grep", "find", "ls", "bash"} {
		if contains(argv, banned) {
			t.Fatalf("undeclared tool %q granted in argv: %#v", banned, argv)
		}
	}
}

func TestRunDeclaredBashFailsClosedBeforeLaunch(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "bash"}}})
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "cannot provide declared tool \"bash\"") {
		t.Fatalf("err = %v, want fail-closed bash rejection", err)
	}
	if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
		t.Fatal("worker process was launched despite an unprovidable declared tool")
	}
}

func TestRunReadOnlyProfileRejectsDeclaredWriteTool(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "write"}}})
	_, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"}))
	if err == nil || !strings.Contains(err.Error(), "cannot provide declared tool \"write\"") {
		t.Fatalf("err = %v, want fail-closed write rejection under read-only", err)
	}
}

func TestRunRejectsMalformedToolsDeclarationBeforeLaunch(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools any
	}{
		{name: "outside-vocabulary", tools: []string{"read", "shell"}},
		{name: "duplicated", tools: []string{"read", "read"}},
		{name: "wrong-type", tools: "read,edit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, "exit 0")
			writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": test.tools}})
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worker tools") {
				t.Fatalf("err = %v, want fail-closed worker tools rejection", err)
			}
			if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
				t.Fatal("worker process was launched despite a malformed tools declaration")
			}
		})
	}
}

func TestPrepareTerminalAppliesDeclaredToolAllowlist(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "edit", "write"}}})
	spec, err := fixture.adapter.PrepareTerminal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(spec.Arguments, "--tools", "read,write,edit") {
		t.Fatalf("native argv did not lock --tools to the declared intersection: %#v", spec.Arguments)
	}
	fixture2 := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture2.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"tools": []string{"bash"}}})
	if _, err := fixture2.adapter.PrepareTerminal(context.Background(), fixture2.request); err == nil || !strings.Contains(err.Error(), "bash") {
		t.Fatalf("err = %v, want fail-closed bash rejection in terminal launch", err)
	}
}

func TestRunCollectsSuccessfulToolNamesIntoTranscriptMeta(t *testing.T) {
	body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{}}'` +
		` '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","result":{},"isError":false}'` +
		` '{"type":"tool_execution_start","toolCallId":"t2","toolName":"edit","args":{}}'` +
		` '{"type":"tool_execution_end","toolCallId":"t2","toolName":"edit","result":{},"isError":false}'` +
		` '{"type":"tool_execution_start","toolCallId":"t3","toolName":"read","args":{}}'` +
		` '{"type":"tool_execution_end","toolCallId":"t3","toolName":"read","result":{},"isError":false}'` +
		` '{"type":"tool_execution_start","toolCallId":"t4","toolName":"grep","args":{"path":"/outside"}}'` +
		` '{"type":"tool_execution_end","toolCallId":"t4","toolName":"grep","isError":true,"error":"permission denied"}'` +
		` '{"type":"agent_end","messages":[],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, body)
	// The denied grep probe targets an outside absolute path on purpose: it
	// grades FATAL only if the classifier sees it, and the attempt outcome
	// does not matter for the toolNames assertion below. The output cap is
	// widened so the multi-event fixture stays below the byte limit.
	if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 8192})); err != nil && !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		ToolNames []string `json:"toolNames"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if strings.Join(meta.ToolNames, ",") != "edit,read" {
		t.Fatalf("toolNames = %v, want exactly [edit read]; denied calls must not be collected", meta.ToolNames)
	}
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sk-ant-api03-super-secret-token", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload", "user private content: password=hunter2"}
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "exit=7") {
		t.Fatalf("error must carry the exit code: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	if strings.Contains(err.Error(), "stderr") {
		t.Fatalf("error references stderr contents: %v", err)
	}
	// stderr must still be persisted as a bounded evidence file.
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
	}
}

func TestWorkerEnvironmentIsolatesCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("GH_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/secrets/gcp.json")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("ANTHROPIC_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	environment := workerEnvironment(t.TempDir())
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"GITHUB_TOKEN", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "SSH_AUTH_SOCK", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "publisher-secret", "cloud-secret", "model-secret", "/secrets/gcp.json", "/tmp/agent.sock"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	for _, required := range []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes", "CI=1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing isolation environment %s: %s", required, joined)
		}
	}
}

func TestRunNormalizesResultAndPersistsBoundedTranscript(t *testing.T) {
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"turn_start"}' '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{}}' '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","result":{},"isError":false}' '{"type":"agent_end","messages":[{"role":"user"},{"role":"assistant","usage":{"input":120,"output":40,"cacheRead":7,"cost":0.0021}}],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, successBody)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.TaskID != "TASK-1" || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized result = %+v", result)
	}
	if result.Session == nil || result.Session.ID != "session-1" || result.Session.Resumable {
		t.Fatalf("ephemeral session must not be resumable: %+v", result.Session)
	}
	var usage map[string]any
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatalf("usage missing: %v", err)
	}
	if usage["inputTokens"] != float64(120) || usage["outputTokens"] != float64(40) || usage["cachedInputTokens"] != float64(7) || usage["currency"] != "USD" {
		t.Fatalf("usage = %v", usage)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"id":"session-1"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 6`) || !strings.Contains(string(metadata), `"toolCalls": 1`) || !strings.Contains(string(metadata), `"cachedInputTokens": 7`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
	argsLog, err := os.ReadFile(fixture.argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(argsLog)), "\n")
	if argv[len(argv)-1] != "完成 fixture" || !containsSequence(argv, "--tools", workerTools) || !containsSequence(argv, "--mode", "json") || !contains(argv, "--no-session") {
		t.Fatalf("observed argv = %#v", argv)
	}
	if contains(argv, "bash") {
		t.Fatalf("bash granted: %#v", argv)
	}
	for _, flag := range []string{"--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-session", "--print"} {
		if count := countOccurrences(argv, flag); count != 1 {
			t.Fatalf("observed argv: hardening flag %s must appear exactly once, got %d: %#v", flag, count, argv)
		}
	}
}

func TestRunReadOnlyProfileGrantsReadOnlyToolAllowlist(t *testing.T) {
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, successBody)
	if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"})); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture.argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !containsSequence(argv, "--tools", readOnlyTools) {
		t.Fatalf("read-only attempt did not lock the tool allowlist to %q: %#v", readOnlyTools, argv)
	}
	if containsSequence(argv, "--tools", workerTools) || contains(argv, "bash") {
		t.Fatalf("read-only attempt leaked write tools: %#v", argv)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "hardened"})); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("hardened profile accepted: %v", err)
	}
}

func TestReadDeclaredResultNormalizesInvalidOptionalSession(t *testing.T) {
	validator := newValidator(t)
	fixturePath := filepath.Join(t.TempDir(), "pi-binary")
	declared := func(mutate func(map[string]any)) declaredResult {
		t.Helper()
		data := validDeclaredResult(fixturePath)
		mutate(data)
		path := filepath.Join(t.TempDir(), "worker-result.json")
		writeJSON(t, path, data)
		result, err := readDeclaredResult(path, 1<<20, validator)
		if err != nil {
			t.Fatalf("readDeclaredResult error = %v", err)
		}
		return result
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "empty session id", mutate: func(data map[string]any) { data["session"] = map[string]any{"id": "", "resumable": false} }},
		{name: "missing session id", mutate: func(data map[string]any) { data["session"] = map[string]any{"resumable": false} }},
		{name: "missing resumable", mutate: func(data map[string]any) { data["session"] = map[string]any{"id": "session-1"} }},
		{name: "resumable wrong type", mutate: func(data map[string]any) { data["session"] = map[string]any{"id": "session-1", "resumable": "yes"} }},
		{name: "resumable null", mutate: func(data map[string]any) { data["session"] = map[string]any{"id": "session-1", "resumable": nil} }},
		{name: "session not an object", mutate: func(data map[string]any) { data["session"] = "session-1" }},
		{name: "session null", mutate: func(data map[string]any) { data["session"] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := declared(test.mutate)
			if result.Session != nil {
				t.Fatalf("invalid optional session must be dropped: %+v", result.Session)
			}
			if result.TaskID != "TASK-1" || result.Status != "completed" || result.Adapter.ID != adapterID {
				t.Fatalf("remaining fields must stay intact: %+v", result)
			}
		})
	}
	t.Run("valid session preserved", func(t *testing.T) {
		result := declared(func(data map[string]any) { data["session"] = map[string]any{"id": "session-9", "resumable": true} })
		if result.Session == nil || result.Session.ID != "session-9" || !result.Session.Resumable {
			t.Fatalf("valid session must be preserved: %+v", result.Session)
		}
	})
	t.Run("no session field unaffected", func(t *testing.T) {
		result := declared(func(data map[string]any) { delete(data, "session") })
		if result.Session != nil {
			t.Fatalf("session = %+v", result.Session)
		}
	})
}

func TestReadDeclaredResultStillFailsClosedForEverythingElse(t *testing.T) {
	validator := newValidator(t)
	t.Run("non-JSON input", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker-result.json")
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readDeclaredResult(path, 1<<20, validator); err == nil || !strings.Contains(err.Error(), "validate WorkerResult declaration") {
			t.Fatalf("non-JSON input must fail validation exactly as before: %v", err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "non-string session id", mutate: func(data map[string]any) { data["session"] = map[string]any{"id": 5, "resumable": false} }},
		{name: "session extra field", mutate: func(data map[string]any) {
			data["session"] = map[string]any{"id": "session-1", "resumable": false, "extra": true}
		}},
		{name: "missing required taskId", mutate: func(data map[string]any) { delete(data, "taskId") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := validDeclaredResult(filepath.Join(t.TempDir(), "pi-binary"))
			test.mutate(data)
			path := filepath.Join(t.TempDir(), "worker-result.json")
			writeJSON(t, path, data)
			if _, err := readDeclaredResult(path, 1<<20, validator); err == nil {
				t.Fatal("normalization must not loosen any other validation")
			}
		})
	}
}

func TestNormalizeDeclaredWorkerResultPreservesUnaffectedInput(t *testing.T) {
	for _, name := range []string{"valid session", "no session field", "non-JSON input"} {
		t.Run(name, func(t *testing.T) {
			var raw []byte
			switch name {
			case "non-JSON input":
				raw = []byte("not json")
			default:
				source := validDeclaredResult("/usr/local/bin/pi")
				if name == "no session field" {
					delete(source, "session")
				}
				data, err := json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
				raw = data
			}
			if normalized := NormalizeDeclaredWorkerResult(raw); !bytes.Equal(normalized, raw) {
				t.Fatalf("input must pass through byte-identical, got %s", normalized)
			}
		})
	}
}

func TestRunAcceptsDeclaredResultWithEmptySessionID(t *testing.T) {
	agentEnd := `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+agentEnd)
	data := validDeclaredResult(fixture.executable)
	data["session"] = map[string]any{"id": "", "resumable": false}
	writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("declared WorkerResult with an empty optional session id must be accepted: %v", err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Session == nil || result.Session.ID != "session-1" || result.Session.Resumable {
		t.Fatalf("observed session must replace the dropped declaration: %+v", result.Session)
	}
}

func TestDecodeUsageCost(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{name: "legacy number", input: `0.0021`, want: 0.0021},
		{name: "structured total", input: `{"input":0.001,"output":0.0011,"cacheRead":0,"cacheWrite":0,"total":0.0021}`, want: 0.0021},
		{name: "structured components", input: `{"input":0.001,"output":0.0011}`, want: 0.0021},
		{name: "string", input: `"0.1"`, wantErr: true},
		{name: "boolean", input: `true`, wantErr: true},
		{name: "array", input: `[0.1]`, wantErr: true},
		{name: "null", input: `null`, wantErr: true},
		{name: "negative", input: `-0.1`, wantErr: true},
		{name: "overflow", input: `1e999`, wantErr: true},
		{name: "unknown field", input: `{"other":0}`, wantErr: true},
		{name: "empty object", input: `{}`, wantErr: true},
		{name: "duplicate field", input: `{"input":0,"input":0}`, wantErr: true},
		{name: "total is authoritative", input: `{"input":0.1,"total":0.2}`, want: 0.2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeUsageCost([]byte(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeUsageCost(%s) error = %v", test.input, err)
			}
			if !test.wantErr && math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("decodeUsageCost(%s) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestRunAcceptsStructuredUsageCost(t *testing.T) {
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":10,"output":5,"cacheRead":2,"cost":{"input":0.001,"output":0.0011,"cacheRead":0,"cacheWrite":0,"total":0.0021}}}],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, body)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	var usage map[string]any
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage["cost"] != float64(0.0021) || usage["currency"] != "USD" {
		t.Fatalf("usage = %v", usage)
	}
}

func TestRunRejectsInvalidUsageCostAsProtocolViolation(t *testing.T) {
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[{"role":"assistant","usage":{"cost":{"unknown":1}}}],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
}

func TestRunRejectsPersistAndResumeBeforeWorkerLaunch(t *testing.T) {
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	for _, policy := range []string{"persist", "resume"} {
		t.Run(policy, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, successBody)
			overrides := map[string]any{"sessionPolicy": policy}
			if policy == "resume" {
				overrides["sessionId"] = "session-1"
			}
			if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(overrides)); !errors.Is(err, ErrUnsupportedSessionPolicy) {
				t.Fatalf("error = %v", err)
			}
			if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
				t.Fatal("worker process was launched despite rejected session policy")
			}
		})
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	for _, version := range []string{"0.83.0", "0.84.0", "0.85.0", "unknown"} {
		t.Run(version, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "launched")
			fixture := newRunFixture(t, version, "touch "+shellQuote(marker))
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			if !errors.Is(err, ErrUnsupportedVersion) {
				t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("unsupported worker process was launched: marker stat = %v", statErr)
			}
			if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
				t.Fatalf("unsupported worker process recorded argv: %v", statErr)
			}
		})
	}
}

func TestRunRejectsProtocolViolations(t *testing.T) {
	agentEnd := `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[],"willRetry":false}'`
	t.Run("wrong-session-version", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"session","version":2,"id":"session-1","cwd":"'"$PWD"'"}'`+"\n"+agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "version") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("wrong-cwd", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"session","version":3,"id":"session-1","cwd":"/elsewhere"}'`+"\n"+agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "cwd") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-header", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "session header") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("malformed-jsonl", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+`printf '%s\n' 'not-json'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-agent-end", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+`printf '%s\n' '{"type":"agent_start"}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "agent_end") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("agent-settled-after-agent-end", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+agentEnd+"\n"+`printf '%s\n' '{"type":"agent_settled"}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("error = %v", err)
		}
	})
	for _, test := range []struct {
		name   string
		events string
	}{
		{name: "settled-before-end", events: `printf '%s\n' '{"type":"agent_settled"}' '{"type":"agent_end","messages":[],"willRetry":false}'`},
		{name: "event-after-end", events: `printf '%s\n' '{"type":"agent_end","messages":[],"willRetry":false}' '{"type":"turn_start"}'`},
		{name: "duplicate-settled", events: `printf '%s\n' '{"type":"agent_end","messages":[],"willRetry":false}' '{"type":"agent_settled"}' '{"type":"agent_settled"}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+test.events)
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
	t.Run("empty-stream", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("non-zero-exit", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+agentEnd+"\nexit 3")
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if !errors.Is(err, ErrProcessFailed) || !strings.Contains(err.Error(), "exit=3") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity-mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+agentEnd)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("declared-session-mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, sessionHeader("session-1")+"\n"+agentEnd)
		data := validDeclaredResult(fixture.executable)
		data["session"] = map[string]any{"id": "claimed-other", "resumable": false}
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "session") {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestRunAcceptsAutoRetryChainAndAccumulatesUsageAcrossAttempts is the Issue
// #32 regression: agent_end(willRetry=true) is a retryable checkpoint, not a
// terminal event. The full auto_retry chain must reach the normalized
// WorkerResult with usage accumulated exactly once per invocation.
func TestRunAcceptsAutoRetryChainAndAccumulatesUsageAcrossAttempts(t *testing.T) {
	body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":10,"output":1,"cacheRead":1,"cost":0.001}}],"willRetry":true}'` +
		` '{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":10,"errorMessage":"transient"}'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"auto_retry_end","success":true,"attempt":1}'` +
		` '{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop","usage":{"input":20,"output":2,"cacheRead":2,"cost":0.002}}],"willRetry":false}'`
	fixture := newRunFixture(t, supportedBinary, body)
	record, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 4096}))
	if err != nil {
		t.Fatalf("auto-retry chain must succeed: %v", err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Session == nil || result.Session.ID != "session-1" || result.Adapter.ID != adapterID {
		t.Fatalf("normalized result = %+v", result)
	}
	var usage map[string]any
	if err := json.Unmarshal(result.Usage, &usage); err != nil {
		t.Fatalf("usage missing: %v", err)
	}
	if usage["inputTokens"] != float64(30) || usage["outputTokens"] != float64(3) || usage["cachedInputTokens"] != float64(3) {
		t.Fatalf("usage = %v", usage)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `"eventCount": 7`) {
		t.Fatalf("metadata = %s", metadata)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(transcript, []byte(`"auto_retry_start"`)) || !bytes.Contains(transcript, []byte(`"auto_retry_end"`)) || !bytes.Contains(transcript, []byte(`"willRetry":true`)) {
		t.Fatalf("transcript lost the retry chain: %s", transcript)
	}
	if bytes.Contains(metadata, []byte("transient")) {
		t.Fatalf("metadata echoed retry free text: %s", metadata)
	}
}

// TestRunReturnsStableProviderFailureBeforeReadingWorkerResult locks the
// Provider-failure semantics: a failed final invocation closes the stream
// legally but must return a stable error before Marshal reads any pre-written
// WorkerResult, and never echoes provider free text.
func TestRunReturnsStableProviderFailureBeforeReadingWorkerResult(t *testing.T) {
	const sentinel = "RETRY-FREE-TEXT-SENTINEL"
	retryFailureBody := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":5,"output":1,"cacheRead":0,"cost":0}}],"willRetry":true}'` +
		` '{"type":"auto_retry_start","attempt":1,"maxAttempts":2,"delayMs":1,"errorMessage":"` + sentinel + `"}'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":6,"output":2,"cacheRead":1,"cost":0}}],"willRetry":false}'` +
		` '{"type":"auto_retry_end","success":false,"attempt":1}'` +
		` '{"type":"agent_settled"}'`
	directFailureBody := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"` + sentinel + `"}}'` +
		` '{"type":"agent_end","messages":[{"role":"assistant","stopReason":"aborted","usage":{"input":1,"output":1,"cacheRead":0,"cost":0}}],"willRetry":false}'`
	for _, test := range []struct{ name, body string }{
		{name: "retry-failure-closure", body: retryFailureBody},
		{name: "direct-failed-final-call", body: directFailureBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, test.body)
			// An invalid pre-written WorkerResult proves Marshal never reads it
			// on the provider-failure path: reading it would surface a
			// validation error instead of the stable provider failure.
			if err := os.WriteFile(filepath.Join(fixture.controlRoot, "output", "worker-result.json"), []byte("not-json"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 4096}))
			if !errors.Is(err, ErrProviderFailed) {
				t.Fatalf("error = %v, want ErrProviderFailed", err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("provider free text leaked into error: %v", err)
			}
			transcript, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript.jsonl"))
			if readErr != nil || !bytes.Contains(transcript, []byte(sentinel)) {
				t.Fatalf("raw transcript must keep the sentinel evidence: %v", readErr)
			}
			metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
			if readErr != nil || bytes.Contains(metadata, []byte(sentinel)) {
				t.Fatalf("metadata must not echo provider free text: %s", metadata)
			}
		})
	}
}

// TestRunProtocolFailureNeverEchoesUnknownEventsOrFreeText pins the
// raw/diagnostics separation: unknown event types and retry free text stay in
// the raw transcript evidence but never reach returned errors or metadata.
func TestRunProtocolFailureNeverEchoesUnknownEventsOrFreeText(t *testing.T) {
	const typeSentinel = "SECRET-TYPE-SENTINEL"
	const retrySentinel = "SECRET-RETRY-SENTINEL"
	body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
		` '{"type":"agent_start"}'` +
		` '{"type":"agent_end","messages":[],"willRetry":false}'` +
		` '{"type":"` + typeSentinel + `","errorMessage":"` + retrySentinel + `"}'`
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
	for _, leaked := range []string{typeSentinel, retrySentinel} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("diagnostics leaked %q: %v", leaked, err)
		}
	}
	transcript, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, kept := range []string{typeSentinel, retrySentinel} {
		if !bytes.Contains(transcript, []byte(kept)) {
			t.Fatalf("raw transcript lost sentinel evidence %q", kept)
		}
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, banned := range []string{typeSentinel, retrySentinel} {
		if bytes.Contains(metadata, []byte(banned)) {
			t.Fatalf("metadata echoed %q", banned)
		}
	}
}

// TestCaptureJSONLAcceptsPi0841NormalizedMessageUpdate pins the real Pi 0.84.1
// normalized message_update wire: the event keeps assistantMessageEvent whose
// text_delta carries exactly type, contentIndex and the incremental delta.
// There is no messageId, no partial, and no top-level cumulative message
// snapshot. Marshal never rewrites provider transcript lines, so the capture
// result must equal the compact LF JSONL input byte-for-byte.
func TestCaptureJSONLAcceptsPi0841NormalizedMessageUpdate(t *testing.T) {
	firstDelta := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}`
	secondDelta := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":" world"}}`
	events := []string{
		`{"type":"session","version":3,"id":"session-1","timestamp":"2026-08-10T00:00:00.000Z","cwd":"/worktree"}`,
		`{"type":"agent_start"}`,
		firstDelta,
		secondDelta,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
	}
	var stream strings.Builder
	for _, event := range events {
		stream.WriteString(event)
		stream.WriteString("\n")
	}
	for _, event := range []string{firstDelta, secondDelta} {
		var wire map[string]json.RawMessage
		if err := json.Unmarshal([]byte(event), &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire) != 2 || wire["type"] == nil || wire["assistantMessageEvent"] == nil {
			t.Fatalf("message_update must carry exactly type + assistantMessageEvent, got %s", event)
		}
		if string(wire["type"]) != `"message_update"` {
			t.Fatalf("event type = %s, want message_update", wire["type"])
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(wire["assistantMessageEvent"], &nested); err != nil {
			t.Fatal(err)
		}
		if len(nested) != 3 || nested["type"] == nil || nested["contentIndex"] == nil || nested["delta"] == nil {
			t.Fatalf("text_delta must carry exactly type/contentIndex/delta, got %s", event)
		}
		if string(nested["type"]) != `"text_delta"` || string(nested["contentIndex"]) != "0" {
			t.Fatalf("text_delta wire = %s", event)
		}
	}
	result := captureJSONL(strings.NewReader(stream.String()), "/worktree", 1<<20, func() {})
	if result.err != nil {
		t.Fatalf("capture error = %v", result.err)
	}
	if result.sessionID != "session-1" || result.eventCount != len(events) {
		t.Fatalf("session = %q eventCount = %d, want session-1/%d", result.sessionID, result.eventCount, len(events))
	}
	if !bytes.Equal(result.raw, []byte(stream.String())) {
		t.Fatalf("capture must preserve the compact JSONL verbatim:\ninput  = %q\nresult = %q", stream.String(), result.raw)
	}
	for _, fragment := range []string{`"assistantMessageEvent"`, `"type":"text_delta"`, `"contentIndex":0`, `"delta":"Hello"`, `"delta":" world"`} {
		if !bytes.Contains(result.raw, []byte(fragment)) {
			t.Fatalf("captured transcript lost %s", fragment)
		}
	}
	for _, banned := range []string{"messageId", "partial", `"message":`} {
		if bytes.Contains(result.raw, []byte(banned)) {
			t.Fatalf("captured transcript invented forbidden field %q", banned)
		}
	}
}

// retryFixtureLines is the deterministic 10-event auto-retry fixture frozen by
// the TaskSpec. The expected byte length and digest are pinned independently
// below and are never derived from these literals at runtime.
var retryFixtureLines = []string{
	`{"type":"session","version":3,"id":"retry-fixed","timestamp":"2026-08-12T00:00:00.000Z","cwd":"/workspace"}`,
	`{"type":"agent_start"}`,
	`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":101,"output":11,"cacheRead":3,"cost":0.001}}],"willRetry":true}`,
	`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":100,"errorMessage":"transient-a"}`,
	`{"type":"agent_start"}`,
	`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":202,"output":22,"cacheRead":5,"cost":0.002}}],"willRetry":true}`,
	`{"type":"auto_retry_start","attempt":2,"maxAttempts":3,"delayMs":200,"errorMessage":"transient-b"}`,
	`{"type":"agent_start"}`,
	`{"type":"auto_retry_end","success":true,"attempt":2}`,
	`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop","usage":{"input":303,"output":33,"cacheRead":7,"cost":0.004}}],"willRetry":false}`,
}

const retryFixtureDigest = "020219497a259716606045b172a3add3fed392b0b65b25557f227f498759628a"

func TestCaptureJSONLAutoRetryDeterministicFixture(t *testing.T) {
	input := strings.Join(retryFixtureLines, "\n") + "\n"
	terminations := 0
	result := captureJSONL(strings.NewReader(input), "/workspace", 1<<20, func() { terminations++ })
	if result.err != nil {
		t.Fatalf("capture error = %v", result.err)
	}
	if result.limitExceeded || result.providerFailed {
		t.Fatalf("unexpected limit/provider flags: limitExceeded=%v providerFailed=%v", result.limitExceeded, result.providerFailed)
	}
	if result.sessionID != "retry-fixed" || result.eventCount != 10 {
		t.Fatalf("session = %q eventCount = %d, want retry-fixed/10", result.sessionID, result.eventCount)
	}
	if len(result.raw) != 890 {
		t.Fatalf("raw length = %d, want 890", len(result.raw))
	}
	if !bytes.Equal(result.raw, []byte(input)) {
		t.Fatalf("raw must preserve every LF-terminated fragment byte-for-byte")
	}
	if result.inputTokens != 606 || result.outputTokens != 66 || result.cachedInputTokens != 15 {
		t.Fatalf("usage = %d/%d/%d, want 606/66/15", result.inputTokens, result.outputTokens, result.cachedInputTokens)
	}
	if math.Abs(result.cost-0.007) > 1e-12 {
		t.Fatalf("cost = %v, want 0.007", result.cost)
	}
	sum := sha256.Sum256(result.raw)
	if hex.EncodeToString(sum[:]) != retryFixtureDigest {
		t.Fatalf("digest = %s, want %s", hex.EncodeToString(sum[:]), retryFixtureDigest)
	}
	if terminations != 0 {
		t.Fatalf("terminations = %d, want 0", terminations)
	}
}

func captureSessionHeader(sessionID string) string {
	return `{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"2026-08-12T00:00:00.000Z","cwd":"/worktree"}`
}

func jsonLines(events ...string) string {
	var builder strings.Builder
	for _, event := range events {
		builder.WriteString(event)
		builder.WriteString("\n")
	}
	return builder.String()
}

func TestCaptureJSONLAutoRetryChainSuccessClosure(t *testing.T) {
	for _, settled := range []bool{false, true} {
		name := "terminal-eof"
		if settled {
			name = "agent-settled"
		}
		t.Run(name, func(t *testing.T) {
			events := []string{
				captureSessionHeader("session-1"),
				`{"type":"agent_start"}`,
				`{"type":"turn_start"}`,
				`{"type":"agent_end","messages":[{"role":"user"}],"willRetry":false}`,
			}
			if settled {
				events = append(events, `{"type":"agent_settled"}`)
			}
			terminations := 0
			result := captureJSONL(strings.NewReader(jsonLines(events...)), "/worktree", 1<<20, func() { terminations++ })
			if result.err != nil {
				t.Fatalf("capture error = %v", result.err)
			}
			if result.providerFailed || result.limitExceeded {
				t.Fatalf("providerFailed=%v limitExceeded=%v", result.providerFailed, result.limitExceeded)
			}
			if terminations != 0 {
				t.Fatalf("terminations = %d, want 0", terminations)
			}
		})
	}
}

func TestCaptureJSONLAutoRetryFailureClosureAndProviderFailure(t *testing.T) {
	for _, stopReason := range []string{"error", "aborted", "length"} {
		t.Run(stopReason, func(t *testing.T) {
			input := jsonLines(
				captureSessionHeader("session-1"),
				`{"type":"agent_start"}`,
				`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":1,"output":1,"cacheRead":0,"cost":0}}],"willRetry":true}`,
				`{"type":"auto_retry_start","attempt":1,"maxAttempts":2,"delayMs":5,"errorMessage":"transient"}`,
				`{"type":"agent_start"}`,
				`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"`+stopReason+`","usage":{"input":2,"output":1,"cacheRead":0,"cost":0}}],"willRetry":false}`,
				`{"type":"auto_retry_end","success":false,"attempt":1}`,
				`{"type":"agent_settled"}`,
			)
			result := captureJSONL(strings.NewReader(input), "/worktree", 1<<20, func() {})
			if result.err != nil {
				t.Fatalf("capture error = %v", result.err)
			}
			if !result.providerFailed {
				t.Fatal("providerFailed must be set for the failed final invocation")
			}
			if result.inputTokens != 3 || result.outputTokens != 2 || result.cachedInputTokens != 0 {
				t.Fatalf("usage = %d/%d/%d, want 3/2/0", result.inputTokens, result.outputTokens, result.cachedInputTokens)
			}
		})
	}
}

func TestCaptureJSONLAutoRetryProtocolViolations(t *testing.T) {
	header := captureSessionHeader("session-1")
	retryableEnd := `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","usage":{"input":1,"output":1,"cacheRead":0,"cost":0}}],"willRetry":true}`
	finalEnd := `{"type":"agent_end","messages":[],"willRetry":false}`
	start1 := `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":1,"errorMessage":"transient"}`
	endSuccess1 := `{"type":"auto_retry_end","success":true,"attempt":1}`
	for _, test := range []struct {
		name   string
		events []string
	}{
		{name: "start-without-retryable-end", events: []string{header, start1}},
		{name: "agent-start-instead-of-start", events: []string{header, retryableEnd, `{"type":"agent_start"}`}},
		{name: "eof-awaiting-start", events: []string{header, retryableEnd}},
		{name: "eof-awaiting-final-end", events: []string{header, retryableEnd, start1, endSuccess1}},
		{name: "eof-awaiting-failure-end", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}],"willRetry":false}`}},
		{name: "eof-retry-active", events: []string{header, retryableEnd, start1}},
		{name: "start-missing-fields", events: []string{header, retryableEnd, `{"type":"auto_retry_start"}`}},
		{name: "attempt-zero", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":0,"maxAttempts":3}`}},
		{name: "attempt-negative", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":-1,"maxAttempts":3}`}},
		{name: "maxAttempts-zero", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":1,"maxAttempts":0}`}},
		{name: "maxAttempts-too-large", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":1,"maxAttempts":4}`}},
		{name: "attempt-exceeds-maxAttempts", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":3,"maxAttempts":2}`}},
		{name: "attempt-skips", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":2,"maxAttempts":3}`}},
		{name: "attempt-repeats", events: []string{header, retryableEnd, start1, retryableEnd, `{"type":"auto_retry_start","attempt":1,"maxAttempts":3}`}},
		{name: "maxAttempts-changed", events: []string{header, retryableEnd, start1, retryableEnd, `{"type":"auto_retry_start","attempt":2,"maxAttempts":2}`}},
		{name: "budget-exhausted", events: []string{header, retryableEnd, `{"type":"auto_retry_start","attempt":1,"maxAttempts":1}`, `{"type":"agent_start"}`, retryableEnd}},
		{name: "duplicate-start", events: []string{header, retryableEnd, start1, `{"type":"auto_retry_start","attempt":2,"maxAttempts":3}`}},
		{name: "retryable-end-without-error-stop", events: []string{header, `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"length"}],"willRetry":true}`}},
		{name: "retryable-end-without-assistant", events: []string{header, `{"type":"agent_end","messages":[{"role":"user"}],"willRetry":true}`}},
		{name: "retryable-end-without-stopReason", events: []string{header, `{"type":"agent_end","messages":[{"role":"assistant"}],"willRetry":true}`}},
		{name: "agent-end-missing-willRetry", events: []string{header, `{"type":"agent_end","messages":[]}`}},
		{name: "retry-active-success-without-retry-end", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop"}],"willRetry":false}`}},
		{name: "retry-end-success-in-active", events: []string{header, `{"type":"agent_start"}`, endSuccess1}},
		{name: "retry-end-failure-in-retry-active", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"auto_retry_end","success":false,"attempt":1}`}},
		{name: "retry-end-attempt-mismatch", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"auto_retry_end","success":true,"attempt":2}`}},
		{name: "retry-end-missing-success", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"auto_retry_end","attempt":1}`}},
		{name: "retry-end-missing-attempt", events: []string{header, retryableEnd, start1, `{"type":"agent_start"}`, `{"type":"auto_retry_end","success":true}`}},
		{name: "work-event-awaiting-final-end", events: []string{header, retryableEnd, start1, endSuccess1, `{"type":"turn_start"}`}},
		{name: "retryable-end-awaiting-final-end", events: []string{header, retryableEnd, start1, endSuccess1, retryableEnd}},
		{name: "event-after-terminal", events: []string{header, finalEnd, `{"type":"turn_start"}`}},
		{name: "retry-control-after-terminal", events: []string{header, finalEnd, start1}},
		{name: "work-after-retry-failed", events: []string{header, retryableEnd, start1, `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error"}],"willRetry":false}`, `{"type":"auto_retry_end","success":false,"attempt":1}`, `{"type":"agent_start"}`}},
		{name: "duplicate-settled", events: []string{header, finalEnd, `{"type":"agent_settled"}`, `{"type":"agent_settled"}`}},
		{name: "settled-after-retryable-end", events: []string{header, retryableEnd, `{"type":"agent_settled"}`}},
		{name: "settled-in-retry-active", events: []string{header, retryableEnd, start1, `{"type":"agent_settled"}`}},
		{name: "second-session-header", events: []string{header, header}},
		{name: "blank-fragment", events: []string{header, ``}},
		{name: "whitespace-only-fragment", events: []string{header, "   "}},
		{name: "partial-json", events: []string{header, `{"type":"agent_start"`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminations := 0
			result := captureJSONL(strings.NewReader(jsonLines(test.events...)), "/worktree", 1<<20, func() { terminations++ })
			if !errors.Is(result.err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", result.err)
			}
			if terminations != 1 {
				t.Fatalf("terminations = %d, want exactly 1", terminations)
			}
		})
	}
}

func TestCaptureJSONLStopsAcceptingBytesAfterProtocolFailure(t *testing.T) {
	poison := `{"type":"agent_end","messages":[],"willRetry":true}`
	input := jsonLines(
		captureSessionHeader("session-1"),
		poison,
		`{"type":"agent_start"}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3}`,
	)
	terminations := 0
	result := captureJSONL(strings.NewReader(input), "/worktree", 1<<20, func() { terminations++ })
	if !errors.Is(result.err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", result.err)
	}
	if terminations != 1 {
		t.Fatalf("terminations = %d, want exactly 1", terminations)
	}
	if bytes.Contains(result.raw, []byte("auto_retry_start")) {
		t.Fatalf("bytes after the protocol failure were admitted into raw: %s", result.raw)
	}
	if !bytes.Contains(result.raw, []byte(poison)) {
		t.Fatalf("the failing fragment itself must remain as evidence: %s", result.raw)
	}
	if result.eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", result.eventCount)
	}
}

func TestCaptureJSONLRejectsUnknownEventEchoInDiagnostics(t *testing.T) {
	input := jsonLines(
		captureSessionHeader("session-1"),
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"zz-mystery-event","payload":"SECRET-TYPE-SENTINEL"}`,
	)
	result := captureJSONL(strings.NewReader(input), "/worktree", 1<<20, func() {})
	if !errors.Is(result.err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", result.err)
	}
	for _, banned := range []string{"zz-mystery-event", "SECRET-TYPE-SENTINEL"} {
		if strings.Contains(result.err.Error(), banned) {
			t.Fatalf("unknown event text leaked into diagnostics: %v", result.err)
		}
	}
	if !bytes.Contains(result.raw, []byte("SECRET-TYPE-SENTINEL")) {
		t.Fatalf("raw transcript lost the sentinel evidence: %s", result.raw)
	}
}

func TestCaptureJSONLRejectsNonLFTailAndKeepsRawPrefix(t *testing.T) {
	success := jsonLines(
		captureSessionHeader("session-1"),
		`{"type":"agent_start"}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"agent_settled"}`,
	)
	terminations := 0
	result := captureJSONL(strings.NewReader(success+"trailing-garbage"), "/worktree", 1<<20, func() { terminations++ })
	if !errors.Is(result.err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", result.err)
	}
	if !bytes.Equal(result.raw, []byte(success)) {
		t.Fatalf("raw must keep only the complete LF-terminated prefix:\nraw = %q", result.raw)
	}
	if terminations != 1 {
		t.Fatalf("terminations = %d, want exactly 1", terminations)
	}
}

func TestCaptureJSONLAcceptsWhitespacePaddedFragmentsByteForByte(t *testing.T) {
	input := "  " + captureSessionHeader("session-1") + "  \n\t" + `{"type":"agent_start"}` + " \n" + `{"type":"agent_end","messages":[],"willRetry":false}` + "\n"
	result := captureJSONL(strings.NewReader(input), "/worktree", 1<<20, func() {})
	if result.err != nil {
		t.Fatalf("capture error = %v", result.err)
	}
	if !bytes.Equal(result.raw, []byte(input)) {
		t.Fatalf("raw must preserve whitespace-padded fragments byte-for-byte:\nraw = %q", result.raw)
	}
	if result.eventCount != 3 {
		t.Fatalf("eventCount = %d, want 3", result.eventCount)
	}
}

func TestCaptureJSONLRejectsNegativeUsageCounters(t *testing.T) {
	header := captureSessionHeader("session-1")
	for _, test := range []struct{ name, agentEnd string }{
		{name: "negative-input", agentEnd: `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop","usage":{"input":-1,"output":1,"cacheRead":0,"cost":0}}],"willRetry":false}`},
		{name: "negative-output", agentEnd: `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop","usage":{"input":1,"output":-1,"cacheRead":0,"cost":0}}],"willRetry":false}`},
		{name: "negative-cacheRead", agentEnd: `{"type":"agent_end","messages":[{"role":"assistant","stopReason":"stop","usage":{"input":1,"output":1,"cacheRead":-1,"cost":0}}],"willRetry":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := captureJSONL(strings.NewReader(jsonLines(header, test.agentEnd)), "/worktree", 1<<20, func() {})
			if !errors.Is(result.err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", result.err)
			}
		})
	}
}

type chunkReader struct {
	data []byte
	step int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	take := r.step
	if take > len(r.data) {
		take = len(r.data)
	}
	if take > len(p) {
		take = len(p)
	}
	copy(p, r.data[:take])
	r.data = r.data[take:]
	return take, nil
}

func TestCaptureJSONLOutputLimitRawEqualsInputPrefix(t *testing.T) {
	input := jsonLines(
		captureSessionHeader("session-1"),
		`{"type":"agent_start"}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
	)
	total := int64(len(input))
	for _, test := range []struct {
		name       string
		limit      int64
		exceeded   bool
		rawLength  int64
		terminates int
	}{
		{name: "limit-1", limit: total - 1, exceeded: true, rawLength: total - 1, terminates: 1},
		{name: "limit", limit: total, exceeded: false, rawLength: total, terminates: 0},
		{name: "limit+1", limit: total + 1, exceeded: false, rawLength: total, terminates: 0},
	} {
		for _, step := range []int{1, 4096, 64 << 10} {
			t.Run(test.name+"/chunk-"+strconv.Itoa(step), func(t *testing.T) {
				terminations := 0
				result := captureJSONL(&chunkReader{data: []byte(input), step: step}, "/worktree", test.limit, func() { terminations++ })
				if result.limitExceeded != test.exceeded {
					t.Fatalf("limitExceeded = %v, want %v", result.limitExceeded, test.exceeded)
				}
				if int64(len(result.raw)) != test.rawLength || !bytes.Equal(result.raw, []byte(input[:test.rawLength])) {
					t.Fatalf("raw must equal the first %d input bytes", test.rawLength)
				}
				if terminations != test.terminates {
					t.Fatalf("terminations = %d, want %d", terminations, test.terminates)
				}
				if test.exceeded && result.err != nil {
					t.Fatalf("limitExceeded must not fabricate a protocol failure: %v", result.err)
				}
				if !test.exceeded && result.err != nil {
					t.Fatalf("unexpected capture error: %v", result.err)
				}
			})
		}
	}
}

func TestCaptureJSONLOutputLimitAcrossErrBufferFullFragments(t *testing.T) {
	large := strings.Repeat("x", 200_000)
	input := jsonLines(
		captureSessionHeader("session-1"),
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"`+large+`"}}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
	)
	const limit = int64(100_000)
	terminations := 0
	result := captureJSONL(&chunkReader{data: []byte(input), step: 64 << 10}, "/worktree", limit, func() { terminations++ })
	if !result.limitExceeded {
		t.Fatal("limitExceeded must be set")
	}
	if int64(len(result.raw)) != limit || !bytes.Equal(result.raw, []byte(input[:limit])) {
		t.Fatalf("raw must equal the first %d input bytes across ErrBufferFull fragments", limit)
	}
	if terminations != 1 {
		t.Fatalf("terminations = %d, want exactly 1", terminations)
	}
	if result.err != nil {
		t.Fatalf("no protocol failure may be fabricated: %v", result.err)
	}
}

func TestCaptureJSONLConcurrentCapturesStayIsolated(t *testing.T) {
	input := strings.Join(retryFixtureLines, "\n") + "\n"
	var group sync.WaitGroup
	for routine := 0; routine < 32; routine++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result := captureJSONL(strings.NewReader(input), "/workspace", 1<<20, func() {})
			if result.err != nil || result.eventCount != 10 || len(result.raw) != 890 {
				t.Errorf("concurrent capture = err %v eventCount %d raw %d", result.err, result.eventCount, len(result.raw))
			}
		}()
	}
	group.Wait()
}

func TestRunEnforcesOutputCapAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"` + large + `"}}' '{"type":"agent_end","messages":[],"willRetry":false}'`
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unterminated-output-cap", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `yes x | tr -d '\n'`)
		started := time.Now()
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unterminated line was not cancelled at the byte limit")
		}
	})
	t.Run("cancel-process-group", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "child.pid")
		body := sessionHeader("session-1") + "\n" +
			`printf '%s\n' '{"type":"agent_start"}'` + "\n" +
			"sleep 60 &\nchild=$!\n" +
			"printf '%s' \"$child\" > " + shellQuote(readyPath+".tmp") + "\n" +
			"mv " + shellQuote(readyPath+".tmp") + " " + shellQuote(readyPath) + "\n" +
			"wait"
		fixture := newRunFixture(t, supportedBinary, body)
		t.Cleanup(func() { killKnownProcessGroup(readyPath) })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The attempt timeout stays far above every test bound so the only
		// cancellation under test is the explicit cancel after the worker
		// provably started.
		outcome := make(chan error, 1)
		go func() {
			_, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 30}))
			outcome <- err
		}()
		readyContent := waitForFileContent(readyPath, 5*time.Second)
		childPid, parseErr := strconv.Atoi(readyContent)
		if parseErr != nil || childPid <= 1 {
			cancel()
			t.Fatalf("worker did not atomically publish its child pid within 5s: %q", readyContent)
		}
		cancel()
		select {
		case err := <-outcome:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return within 10s of cancellation")
		}
		waitForExit(t, childPid, 5*time.Second)
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metadata), `"contextError": "context canceled"`) {
			t.Fatalf("transcript metadata must record the cancellation: %s", metadata)
		}
	})
}

// waitForFileContent polls until the atomically published file exists with
// non-empty content, so the test never guesses worker startup timing with a
// fixed sleep.
func waitForFileContent(path string, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if content := strings.TrimSpace(string(data)); content != "" {
				return content
			}
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForExit(t *testing.T, pid int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled child pid %d is still alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// killKnownProcessGroup terminates the exact process group recorded in
// readyPath. Registered as cleanup before Run starts, so even a failed
// assertion never leaves orphaned worker children behind.
func killKnownProcessGroup(readyPath string) {
	data, err := os.ReadFile(readyPath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return
	}
	if group, groupErr := syscall.Getpgid(pid); groupErr == nil {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func TestRunGradesPermissionDenialsFromToolErrors(t *testing.T) {
	t.Run("benign read continues and records evidence", func(t *testing.T) {
		body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
			` '{"type":"agent_start"}'` +
			` '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{"path":"'"$PWD"'/source.go"}}'` +
			` '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","isError":true,"error":"permission denied"}'` +
			` '{"type":"agent_end","messages":[],"willRetry":false}'`
		fixture := newRunFixture(t, supportedBinary, body)
		if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("benign denial must not terminate the attempt: %v", err)
		}
		worktreeReal, err := filepath.EvalSymlinks(fixture.worktree)
		if err != nil {
			t.Fatal(err)
		}
		assertDenialLog(t, fixture.controlRoot, map[string]any{"seq": float64(1), "tool": "read", "kind": "read", "grade": "BENIGN", "path-or-cmd": filepath.Join(worktreeReal, "source.go")})
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsBenign": 1`) {
			t.Fatalf("metadata lost denial grading: %s", metadata)
		}
	})
	t.Run("fatal read closes attempt and records evidence", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
			` '{"type":"agent_start"}'` +
			` '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{"path":"` + outside + `"}}'` +
			` '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","isError":true,"error":"permission rule prevents reading this path"}'` +
			` '{"type":"agent_end","messages":[],"willRetry":false}'`
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
		assertDenialLog(t, fixture.controlRoot, map[string]any{"seq": float64(1), "tool": "read", "kind": "read", "grade": "FATAL", "path-or-cmd": outside})
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "pi-transcript-meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metadata), `"permissionDenied": true`) || !strings.Contains(string(metadata), `"denialsFatal": 1`) {
			t.Fatalf("metadata lost fatal denial state: %s", metadata)
		}
	})
	t.Run("write error always fatal", func(t *testing.T) {
		body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
			` '{"type":"agent_start"}'` +
			` '{"type":"tool_execution_start","toolCallId":"t1","toolName":"write","args":{"path":"'"$PWD"'/target.txt"}}'` +
			` '{"type":"tool_execution_end","toolCallId":"t1","toolName":"write","isError":true,"error":"permission denied"}'` +
			` '{"type":"agent_end","messages":[],"willRetry":false}'`
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("write denial must grade FATAL: %v", err)
		}
	})
	t.Run("non-permission tool error is not a denial", func(t *testing.T) {
		body := sessionHeader("session-1") + "\n" + `printf '%s\n'` +
			` '{"type":"agent_start"}'` +
			` '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{"path":"'"$PWD"'/missing.go"}}'` +
			` '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","isError":true,"error":"file not found"}'` +
			` '{"type":"agent_end","messages":[],"willRetry":false}'`
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("ordinary tool error must stay a provider concern: %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.controlRoot, "output", "denials.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("unexpected denial log: %v", err)
		}
	})
}

func assertDenialLog(t *testing.T, controlRoot string, want map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(controlRoot, "output", "denials.jsonl"))
	if err != nil {
		t.Fatalf("denial log missing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("denial log = %s", data)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("denial record %s = %v, want %v (record %+v)", key, record[key], value, record)
		}
	}
	if _, present := record["at"]; !present {
		t.Fatalf("denial record missing at: %+v", record)
	}
	info, err := os.Stat(filepath.Join(controlRoot, "output", "denials.jsonl"))
	if err == nil && info.Mode().Perm() != 0o600 {
		t.Fatalf("denial log permissions = %v, want 0600", info.Mode().Perm())
	}
}

type runFixture struct {
	adapter                           *Adapter
	validator                         *contract.Validator
	executable, worktree, controlRoot string
	argsPath                          string
	requestData                       map[string]any
	request                           domain.Record
}

func newRunFixture(t *testing.T, version, body string) runFixture {
	t.Helper()
	argsPath := filepath.Join(t.TempDir(), "args.log")
	executable := fakeExecutable(t, version, argsPath, body)
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	writeJSON(t, filepath.Join(controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model"}})
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("完成 fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(controlRoot, "output", "worker-result.json"), validDeclaredResult(executable))
	requestData := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "pi", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 1024, "reviewFindings": []any{},
	}
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, argsPath, requestData, fixtureRequest(requestData)}
}

func (f runFixture) requestWith(overrides map[string]any) domain.Record {
	data := map[string]any{}
	for key, value := range f.requestData {
		data[key] = value
	}
	for key, value := range overrides {
		data[key] = value
	}
	return fixtureRequest(data)
}

func fixtureRequest(data map[string]any) domain.Record {
	requestBytes, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}
}

func validDeclaredResult(executable string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1",
		"adapter": map[string]any{"id": "pi", "executable": executable, "version": "worker-claim"},
		"session": map[string]any{"id": "session-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
}

// sessionHeader emits the strict first line of the pi JSON stream: a session
// event with version 3 whose cwd is the actual worker cwd ($PWD, which the
// adapter pins to the resolved worktree).
func sessionHeader(sessionID string) string {
	return `printf '%s\n' '{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"2026-08-04T00:00:00.000Z","cwd":"'"$PWD"'"}'`
}

func fakeExecutable(t *testing.T, version, argsPath, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\n"
	if argsPath != "" {
		script += "for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shellQuote(argsPath) + "\n"
	}
	script += body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func countOccurrences(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func containsSequence(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}
