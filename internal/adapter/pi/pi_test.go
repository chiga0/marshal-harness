package pi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	executable := fakeExecutable(t, "0.83.0", "", "exit 0")
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
	for _, test := range []struct{ version, status string }{{"0.83.0", "supported"}, {"0.84.0", "unsupported"}} {
		t.Run(test.version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, test.version, "", "exit 0"), newValidator(t))
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
		})
	}
}

func TestBuildArgsLocksHardeningFlagsAndNeverGrantsBash(t *testing.T) {
	hardening := []string{"--mode", "json", "--print", "--no-approve", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--tools", workerTools}
	for _, model := range []string{"", "provider/model"} {
		args := buildArgs(model, "完成任务")
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
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sk-ant-api03-super-secret-token", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload", "user private content: password=hunter2"}
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[]}'`
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, "0.83.0", body)
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
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"turn_start"}' '{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{}}' '{"type":"tool_execution_end","toolCallId":"t1","toolName":"read","result":{},"isError":false}' '{"type":"agent_end","messages":[{"role":"user"},{"role":"assistant","usage":{"input":120,"output":40,"cacheRead":7,"cost":0.0021}}]}'`
	fixture := newRunFixture(t, "0.83.0", successBody)
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
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":10,"output":5,"cacheRead":2,"cost":{"input":0.001,"output":0.0011,"cacheRead":0,"cacheWrite":0,"total":0.0021}}}]}'`
	fixture := newRunFixture(t, "0.83.0", body)
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
	body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[{"role":"assistant","usage":{"cost":{"unknown":1}}}]}'`
	fixture := newRunFixture(t, "0.83.0", body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
}

func TestRunRejectsPersistAndResumeBeforeWorkerLaunch(t *testing.T) {
	successBody := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[]}'`
	for _, policy := range []string{"persist", "resume"} {
		t.Run(policy, func(t *testing.T) {
			fixture := newRunFixture(t, "0.83.0", successBody)
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
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "0.84.0", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunRejectsProtocolViolations(t *testing.T) {
	agentEnd := `printf '%s\n' '{"type":"agent_start"}' '{"type":"agent_end","messages":[]}'`
	t.Run("wrong-session-version", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", `printf '%s\n' '{"type":"session","version":2,"id":"session-1","cwd":"'"$PWD"'"}'`+"\n"+agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "version") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("wrong-cwd", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", `printf '%s\n' '{"type":"session","version":3,"id":"session-1","cwd":"/elsewhere"}'`+"\n"+agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "cwd") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-header", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", agentEnd)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "session header") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("malformed-jsonl", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+`printf '%s\n' 'not-json'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-agent-end", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+`printf '%s\n' '{"type":"agent_start"}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "agent_end") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("agent-settled-after-agent-end", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+agentEnd+"\n"+`printf '%s\n' '{"type":"agent_settled"}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("error = %v", err)
		}
	})
	for _, test := range []struct {
		name   string
		events string
	}{
		{name: "settled-before-end", events: `printf '%s\n' '{"type":"agent_settled"}' '{"type":"agent_end","messages":[]}'`},
		{name: "event-after-end", events: `printf '%s\n' '{"type":"agent_end","messages":[]}' '{"type":"turn_start"}'`},
		{name: "duplicate-settled", events: `printf '%s\n' '{"type":"agent_end","messages":[]}' '{"type":"agent_settled"}' '{"type":"agent_settled"}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+test.events)
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
	t.Run("empty-stream", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("non-zero-exit", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+agentEnd+"\nexit 3")
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if !errors.Is(err, ErrProcessFailed) || !strings.Contains(err.Error(), "exit=3") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity-mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+agentEnd)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("declared-session-mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", sessionHeader("session-1")+"\n"+agentEnd)
		data := validDeclaredResult(fixture.executable)
		data["session"] = map[string]any{"id": "claimed-other", "resumable": false}
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "session") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunEnforcesOutputCapAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		body := sessionHeader("session-1") + "\n" + `printf '%s\n' '{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","delta":"` + large + `"}}' '{"type":"agent_end","messages":[]}'`
		fixture := newRunFixture(t, "0.83.0", body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unterminated-output-cap", func(t *testing.T) {
		fixture := newRunFixture(t, "0.83.0", `yes x | tr -d '\n'`)
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
		fixture := newRunFixture(t, "0.83.0", body)
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
