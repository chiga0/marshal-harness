package opencode

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
	if _, err := New("opencode", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	executable := fakeExecutable(t, "1.18.12", "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
}

func TestProbeFreezesSupportedAndUnsupportedBinary(t *testing.T) {
	for _, test := range []struct{ version, status string }{{"1.18.12", "supported"}, {"1.19.0", "unsupported"}} {
		t.Run(test.version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, test.version, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				Status, Version, Digest, Executable string
			}
			var raw map[string]any
			if err := json.Unmarshal(record.Data, &raw); err != nil {
				t.Fatal(err)
			}
			snapshot.Status, _ = raw["probeStatus"].(string)
			snapshot.Version, _ = raw["binaryVersion"].(string)
			snapshot.Digest, _ = raw["executableDigest"].(string)
			snapshot.Executable, _ = raw["executable"].(string)
			if snapshot.Status != test.status || snapshot.Version != test.version || !strings.HasPrefix(snapshot.Digest, "sha256:") || !filepath.IsAbs(snapshot.Executable) {
				t.Fatalf("snapshot = %+v", snapshot)
			}
		})
	}
}

func TestPermissionConfigAndEnvironmentFailClosed(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	controlRoot := t.TempDir()
	config, err := permissionConfig(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, `"permissions"`) || !strings.Contains(config, `"permission"`) || !strings.Contains(config, `"git push *":"deny"`) || !strings.Contains(config, `"external_directory":{"*":"deny"`) || !strings.Contains(config, filepath.ToSlash(filepath.Join(controlRoot, "input"))+`/**":"deny"`) {
		t.Fatalf("unsafe config: %s", config)
	}
	environment := workerEnvironment(t.TempDir(), config)
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "publisher-secret", "cloud-secret", "model-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	if !strings.Contains(joined, "OPENCODE_CONFIG_CONTENT=") || !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("missing isolation environment: %s", joined)
	}
}

func TestResolvedPermissionValidationRejectsWildcardAndMissingIndirectDenies(t *testing.T) {
	root := t.TempDir()
	config, err := permissionConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	if err := validatePermissionMap(raw.Permission, root); err != nil {
		t.Fatal(err)
	}
	raw.Permission["*"] = "allow"
	if err := validatePermissionMap(raw.Permission, root); err == nil {
		t.Fatal("allowed global wildcard accepted")
	}
}

func TestBuildArgsNeverUsesShellAndBindsSessionModelPrompt(t *testing.T) {
	args := buildArgs("resume", "session-1", "provider/model", "完成任务")
	want := []string{"run", "--pure", "--format", "json", "--title", "Marshal Worker", "--session", "session-1", "--model", "provider/model", "完成任务"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestRunNormalizesResultAndPersistsBoundedTranscript(t *testing.T) {
	fixture := newRunFixture(t, "1.18.12", `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`)
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
	if result.TaskID != "TASK-1" || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Session == nil || result.Session.ID != "session-1" || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized result = %+v", result)
	}
	if !result.StartedAt.Before(result.CompletedAt) && !result.StartedAt.Equal(result.CompletedAt) {
		t.Fatalf("invalid times: %s %s", result.StartedAt, result.CompletedAt)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"sessionID":"session-1"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 2`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "1.19.0", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunRejectsMalformedJSONLAndIdentityMismatch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		fixture := newRunFixture(t, "1.18.12", `printf '%s\n' 'not-json'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		fixture := newRunFixture(t, "1.18.12", `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{}}'`)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunEnforcesOutputCapPermissionAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		fixture := newRunFixture(t, "1.18.12", `printf '%s\n' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"`+large+`"}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unterminated-output-cap", func(t *testing.T) {
		fixture := newRunFixture(t, "1.18.12", `yes x | tr -d '\n'`)
		started := time.Now()
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unterminated line was not cancelled at the byte limit")
		}
	})
	t.Run("permission", func(t *testing.T) {
		fixture := newRunFixture(t, "1.18.12", `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"state":{"status":"error","error":"permission denied"}}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("permission-words-in-text", func(t *testing.T) {
		fixture := newRunFixture(t, "1.18.12", `printf '%s\n' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"permission denied is documentation text"}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("text caused false permission denial: %v", err)
		}
	})
	t.Run("cancel-process-group", func(t *testing.T) {
		handshake := t.TempDir()
		pidFile := filepath.Join(handshake, "child.pid")
		readyFile := filepath.Join(handshake, "ready")
		body := "sleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile+".tmp") + " && mv " + shellQuote(pidFile+".tmp") + " " + shellQuote(pidFile) + "\n: > " + shellQuote(readyFile+".tmp") + " && mv " + shellQuote(readyFile+".tmp") + " " + shellQuote(readyFile) + "\nwait"
		fixture := newRunFixture(t, "1.18.12", body)
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

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sk-ant-api03-super-secret-token", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload", "user private content: password=hunter2"}
	body := `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}'`
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, "1.18.12", body)
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
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
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
		"adapterId": "opencode", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 1024, "reviewFindings": []any{},
	}
	requestBytes, _ := json.Marshal(requestData)
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}}
}

func validDeclaredResult(executable string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1",
		"adapter": map[string]any{"id": "opencode", "executable": executable, "version": "worker-claim"},
		"session": map[string]any{"id": "session-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
}

func fakeExecutable(t *testing.T, version, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nif [ \"$1\" = \"debug\" ] && [ \"$2\" = \"config\" ]; then printf '%s\\n' \"$OPENCODE_CONFIG_CONTENT\"; exit 0; fi\n" + body + "\n"
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
