package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const decodeSessionJSONL = `{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}
{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}
`

func TestResolveExecutableIdentityPinsBinaryAndFailsClosedWhenUnavailable(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := adapter.ResolveExecutableIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Path != adapter.executable || identity.Version != supportedBinary || !strings.HasPrefix(identity.Digest, "sha256:") {
		t.Fatalf("identity = %+v", identity)
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveExecutableIdentity(context.Background()); err == nil {
		t.Fatal("missing executable resolved")
	}
}

func TestPrepareAttemptIsPureConstructionAndFiltersEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.adapter.PrepareAttempt(identity, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PrepareAttempt launched a process")
	}
	if prepared.TaskID != "TASK-1" || prepared.RunID != "run-1" || prepared.AttemptID != "attempt-1" || prepared.ExecutionProfile != "workspace-write" || prepared.SessionPolicy != "ephemeral" {
		t.Fatalf("request identity = %+v", prepared)
	}
	if prepared.Identity != identity || identity.Path != fixture.executable || identity.Version != supportedBinary || !strings.HasPrefix(identity.Digest, "sha256:") {
		t.Fatalf("identity = %+v", identity)
	}
	wantArgs := []string{"run", "--pure", "--format", "json", "--title", "Marshal Worker", "--model", "provider/model", "完成 fixture"}
	if strings.Join(prepared.Arguments, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("argv = %#v", prepared.Arguments)
	}
	expectedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	expectedControlRoot, err := filepath.EvalSymlinks(fixture.controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WorkingDirectory != expectedWorktree {
		t.Fatalf("working directory = %s", prepared.WorkingDirectory)
	}
	if prepared.Timeout != 5*time.Second || prepared.OutputLimit != 1024 {
		t.Fatalf("caps = %s %d", prepared.Timeout, prepared.OutputLimit)
	}
	if prepared.ControlRoot != expectedControlRoot || prepared.TaskSpecRelPath != "input/task-spec.json" || prepared.PromptRelPath != "input/prompt.md" || prepared.ResultRelPath != "output/worker-result.json" || prepared.ResultPath != filepath.Join(expectedControlRoot, "output", "worker-result.json") {
		t.Fatalf("control paths = %+v", prepared)
	}
	joined := strings.Join(prepared.Environment, "\n")
	for _, secret := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "publisher-secret", "cloud-secret", "model-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("prepared environment leaked %s", secret)
		}
	}
	if !strings.Contains(joined, "OPENCODE_CONFIG_CONTENT=") || !strings.Contains(joined, `"task":"deny"`) || !strings.Contains(joined, "CI=1") || !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") || !strings.Contains(joined, "PWD="+expectedWorktree) {
		t.Fatalf("prepared environment missing isolation values: %s", joined)
	}
}

func TestPrepareAttemptFailsClosedOnIdentityAndRequestBoundaries(t *testing.T) {
	t.Run("unsupported-version", func(t *testing.T) {
		fixture := newRunFixture(t, "1.19.0", "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("foreign-identity", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		other, err := New(fakeExecutable(t, supportedBinary, "exit 0"), fixture.validator)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := other.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.request); err == nil || !strings.Contains(err.Error(), "does not match the configured") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("incomplete-identity", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.PrepareAttempt(ExecutableIdentity{Path: fixture.executable}, fixture.request); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("wrong-kind", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, domain.Record{Kind: domain.KindWorkerResult, Data: fixture.request.Data}); err == nil || !strings.Contains(err.Error(), "expected WorkerRequest") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("profile-mismatch", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.requestWith(map[string]any{"executionProfile": "hardened"})); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-worktree", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.requestWith(map[string]any{"worktreePath": filepath.Join(t.TempDir(), "missing")})); err == nil || !strings.Contains(err.Error(), "resolve worktree") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("prompt-escape", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.requestWith(map[string]any{"promptPath": "../outside.md"})); err == nil || !strings.Contains(err.Error(), "promptPath") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("prompt-symlink-escape", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "secret.md")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(fixture.controlRoot, "input", "escape.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.requestWith(map[string]any{"promptPath": "input/escape.md"})); err == nil || !strings.Contains(err.Error(), "resolve prompt") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("result-escape", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.PrepareAttempt(identity, fixture.requestWith(map[string]any{"resultPath": "../outside.json"})); err == nil || !strings.Contains(err.Error(), "resultPath") {
			t.Fatalf("error = %v", err)
		}
	})
}

func mustPrepareAttempt(t *testing.T, fixture runFixture) *PreparedAttempt {
	t.Helper()
	identity, err := fixture.adapter.ResolveExecutableIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.adapter.PrepareAttempt(identity, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func decodeObservation(raw string) ExecutionObservation {
	started := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	return ExecutionObservation{StdoutRaw: []byte(raw), StartedAt: started, CompletedAt: started.Add(time.Second)}
}

func TestDecodeAttemptFailsClosedOnObservationBoundaries(t *testing.T) {
	t.Run("malformed-jsonl", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		_, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation("not-json\n"))
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
		transcript, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript.jsonl"))
		if readErr != nil || !strings.Contains(string(transcript), "not-json") {
			t.Fatalf("transcript = %s err=%v", transcript, readErr)
		}
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"eventCount": 0`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
	})
	t.Run("nonzero-exit", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(decodeSessionJSONL)
		observation.ExitCode, observation.ProcessFailed = 7, true
		_, err := fixture.adapter.DecodeAttempt(prepared, observation)
		if !errors.Is(err, ErrProcessFailed) || !strings.Contains(err.Error(), "exit=7") {
			t.Fatalf("error = %v", err)
		}
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"exitCode": 7`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
	})
	t.Run("signal-failure", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(decodeSessionJSONL)
		observation.ExitCode, observation.Signal, observation.ProcessFailed = -1, "killed", true
		_, err := fixture.adapter.DecodeAttempt(prepared, observation)
		if !errors.Is(err, ErrProcessFailed) || !strings.Contains(err.Error(), "signal=killed") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("cancel-precedes-process-failure", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(decodeSessionJSONL)
		observation.ContextErr, observation.OutputTruncated = context.Canceled, true
		observation.ExitCode, observation.Signal, observation.ProcessFailed = -1, "killed", true
		_, err := fixture.adapter.DecodeAttempt(prepared, observation)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"contextError": "context canceled"`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript.jsonl")); statErr != nil {
			t.Fatal("evidence was not written before cancellation was reported")
		}
	})
	t.Run("truncation-beats-process-failure", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(decodeSessionJSONL)
		observation.OutputTruncated = true
		observation.ExitCode, observation.Signal, observation.ProcessFailed = -1, "killed", true
		_, err := fixture.adapter.DecodeAttempt(prepared, observation)
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"outputTruncated": true`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
	})
	t.Run("fatal-denial", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		raw := `{"type":"error","sessionID":"session-1","part":{"tool":"bash","state":{"status":"error","error":"permission denied by rule","input":{"command":"curl http://evil.example"}}}}` + "\n"
		_, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(raw))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
		assertFatalDenialLog(t, prepared.ControlRoot, "bash", "execute", "curl http://evil.example")
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"permissionDenied": true`) || !strings.Contains(string(metadata), `"denialsFatal": 1`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
	})
	t.Run("benign-denial-continues", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		raw := `{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"` + filepath.Join(prepared.WorkingDirectory, "source.go") + `"}}}}` + "\n"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(raw)); err != nil {
			t.Fatalf("benign denial must not terminate decoding: %v", err)
		}
		metadata, readErr := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"denialsBenign": 1`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
	})
	t.Run("missing-session", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		_, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(`{"type":"text","part":{"type":"text","text":"done"}}`+"\n"))
		if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "sessionID is missing") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity-drift", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, prepared.ResultPath, data)
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("session-drift", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		data := validDeclaredResult(fixture.executable)
		data["session"] = map[string]any{"id": "session-X", "resumable": false}
		writeJSON(t, prepared.ResultPath, data)
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); err == nil || !strings.Contains(err.Error(), "session does not match transcript") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing-declared-result", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		if err := os.Remove(prepared.ResultPath); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); err == nil || !strings.Contains(err.Error(), "WorkerResult declaration") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("invalid-declared-result", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		writeJSON(t, prepared.ResultPath, map[string]any{"status": "completed"})
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); err == nil || !strings.Contains(err.Error(), "WorkerResult declaration") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("nil-prepared", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.DecodeAttempt(nil, decodeObservation("")); err == nil {
			t.Fatal("nil prepared attempt accepted")
		}
	})
}

func TestDecodeAttemptNormalizesStagedResultWithoutLaunchingProcesses(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	prepared := mustPrepareAttempt(t, fixture)
	if err := os.Remove(fixture.executable); err != nil {
		t.Fatal(err)
	}
	record, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL))
	if err != nil {
		t.Fatalf("decode failed after executable removal: %v", err)
	}
	if record.Kind != domain.KindWorkerResult {
		t.Fatalf("record kind = %s", record.Kind)
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
		t.Fatalf("session = %+v", result.Session)
	}
	if result.StartedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		t.Fatalf("times = %s %s", result.StartedAt, result.CompletedAt)
	}
	onDisk, err := os.ReadFile(prepared.ResultPath)
	if err != nil || !bytes.Equal(onDisk, append(append([]byte{}, record.Data...), '\n')) {
		t.Fatalf("normalized result not persisted: %v", err)
	}
	for _, name := range []string{"opencode-transcript.jsonl", "opencode-transcript-meta.json", "opencode-stderr.log"} {
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(prepared.ResultPath), name)); statErr != nil {
			t.Fatalf("evidence file %s missing: %v", name, statErr)
		}
	}
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(prepared.ResultPath), "opencode-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 2`) || !strings.Contains(string(metadata), `"sessionId": "session-1"`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
}

func TestDecodeAttemptEnforcesObservedOutputBoundsIndependently(t *testing.T) {
	t.Run("stdout-exceeds-limit-flag-false", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(strings.Repeat("x", int(prepared.OutputLimit)*2))
		if _, err := fixture.adapter.DecodeAttempt(prepared, observation); !errors.Is(err, ErrOutputLimit) || !strings.Contains(err.Error(), "stdout") {
			t.Fatalf("error = %v", err)
		}
		evidenceDir := filepath.Dir(prepared.ResultPath)
		transcript, readErr := os.ReadFile(filepath.Join(evidenceDir, "opencode-transcript.jsonl"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if int64(len(transcript)) > prepared.OutputLimit {
			t.Fatalf("transcript evidence %d bytes exceeds output cap %d", len(transcript), prepared.OutputLimit)
		}
		metadata, readErr := os.ReadFile(filepath.Join(evidenceDir, "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"outputTruncated": true`) || !strings.Contains(string(metadata), fmt.Sprintf(`"capturedBytes": %d`, prepared.OutputLimit*2)) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
		declared, readErr := os.ReadFile(prepared.ResultPath)
		if readErr != nil || !strings.Contains(string(declared), `"worker-claim"`) {
			t.Fatalf("declared result was rewritten despite bounds violation: %s err=%v", declared, readErr)
		}
	})
	t.Run("stderr-exceeds-limit-flag-false", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		observation := decodeObservation(decodeSessionJSONL)
		observation.Stderr = []byte(strings.Repeat("e", stderrLimit+4096))
		if _, err := fixture.adapter.DecodeAttempt(prepared, observation); !errors.Is(err, ErrOutputLimit) || !strings.Contains(err.Error(), "stderr") {
			t.Fatalf("error = %v", err)
		}
		evidenceDir := filepath.Dir(prepared.ResultPath)
		evidence, readErr := os.ReadFile(filepath.Join(evidenceDir, "opencode-stderr.log"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(evidence) > stderrLimit {
			t.Fatalf("stderr evidence %d bytes exceeds stderr limit %d", len(evidence), stderrLimit)
		}
		metadata, readErr := os.ReadFile(filepath.Join(evidenceDir, "opencode-transcript-meta.json"))
		if readErr != nil || !strings.Contains(string(metadata), `"stderrTruncated": true`) || !strings.Contains(string(metadata), fmt.Sprintf(`"stderrBytes": %d`, stderrLimit+4096)) {
			t.Fatalf("metadata = %s err=%v", metadata, readErr)
		}
		transcript, readErr := os.ReadFile(filepath.Join(evidenceDir, "opencode-transcript.jsonl"))
		if readErr != nil || !strings.Contains(string(transcript), `"sessionID":"session-1"`) {
			t.Fatalf("honest transcript lost: %s err=%v", transcript, readErr)
		}
		declared, readErr := os.ReadFile(prepared.ResultPath)
		if readErr != nil || !strings.Contains(string(declared), `"worker-claim"`) {
			t.Fatalf("declared result was rewritten despite bounds violation: %s err=%v", declared, readErr)
		}
	})
	t.Run("boundary-and-cap-validation", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		if err := prepared.observationBounds(int(prepared.OutputLimit), stderrLimit); err != nil {
			t.Fatalf("at-limit observation rejected: %v", err)
		}
		if err := prepared.observationBounds(int(prepared.OutputLimit)+1, stderrLimit); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("stdout over cap accepted: %v", err)
		}
		if err := prepared.observationBounds(0, stderrLimit+1); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("stderr over cap accepted: %v", err)
		}
		for _, invalid := range []int64{0, -1} {
			if err := (&PreparedAttempt{OutputLimit: invalid}).observationBounds(0, 0); !errors.Is(err, ErrOutputLimit) || !strings.Contains(err.Error(), "positive") {
				t.Fatalf("non-positive output cap %d accepted: %v", invalid, err)
			}
		}
	})
}

func TestPreparedAttemptTamperingFailsClosed(t *testing.T) {
	t.Run("result-path-escape", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		outsideDir := t.TempDir()
		target := filepath.Join(outsideDir, "pwned.json")
		prepared.ResultPath = target
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatal("tampered result path was written outside the control root")
		}
		if entries, readErr := os.ReadDir(outsideDir); readErr != nil || len(entries) != 0 {
			t.Fatalf("files written outside the control root: entries=%+v err=%v", entries, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(prepared.ControlRoot, "output", "opencode-transcript.jsonl")); !os.IsNotExist(statErr) {
			t.Fatal("evidence was written despite failed integrity check")
		}
	})
	t.Run("control-root-retarget", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.ControlRoot = t.TempDir()
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity-swap", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.Identity.Path = prepared.Identity.Path + "-evil"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		prepared = mustPrepareAttempt(t, fixture)
		prepared.Identity.Version = "9.9.9"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("argv-mutation", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.Arguments[0] = "evil"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("env-mutation", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.Environment[0] = "PATH=/evil"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		prepared = mustPrepareAttempt(t, fixture)
		prepared.Environment = append(prepared.Environment, "GIT_TERMINAL_PROMPT=1")
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("caps-mutation", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.OutputLimit = prepared.OutputLimit << 1
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("request-identity-mutation", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		prepared.RunID = "run-X"
		if _, err := fixture.adapter.DecodeAttempt(prepared, decodeObservation(decodeSessionJSONL)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("forged-zero-integrity", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		prepared := mustPrepareAttempt(t, fixture)
		forged := &PreparedAttempt{
			TaskID: prepared.TaskID, RunID: prepared.RunID, AttemptID: prepared.AttemptID,
			ControlRoot: prepared.ControlRoot, ResultPath: prepared.ResultPath,
		}
		if _, err := fixture.adapter.DecodeAttempt(forged, decodeObservation(decodeSessionJSONL)); err == nil || !strings.Contains(err.Error(), "integrity digest is missing") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPreparedAttemptEvidenceDirectoryStaysInsideControlRoot(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	prepared := mustPrepareAttempt(t, fixture)
	dir, err := prepared.evidenceDirectory()
	if err != nil || dir != filepath.Join(prepared.ControlRoot, "output") {
		t.Fatalf("evidence directory = %q err = %v", dir, err)
	}
	escaped := *prepared
	escaped.ResultPath = filepath.Join(t.TempDir(), "worker-result.json")
	if _, err := escaped.evidenceDirectory(); err == nil {
		t.Fatal("result path outside the control root accepted")
	}
	rootAsResult := *prepared
	rootAsResult.ResultPath = prepared.ControlRoot
	if _, err := rootAsResult.evidenceDirectory(); err == nil {
		t.Fatal("result path at the control root accepted")
	}
	relative := *prepared
	relative.ResultPath = "output/worker-result.json"
	if _, err := relative.evidenceDirectory(); err == nil {
		t.Fatal("relative result path accepted")
	}
}

func TestRunLocalAttemptRejectsTamperedPreparedAttempt(t *testing.T) {
	t.Run("argv", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
		prepared := mustPrepareAttempt(t, fixture)
		prepared.Arguments[len(prepared.Arguments)-1] = "tampered prompt"
		if _, err := fixture.adapter.runLocalAttempt(context.Background(), prepared); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatal("tampered prepared attempt launched a process")
		}
	})
	t.Run("environment", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
		prepared := mustPrepareAttempt(t, fixture)
		prepared.Environment = append(prepared.Environment, "MALICIOUS=1")
		if _, err := fixture.adapter.runLocalAttempt(context.Background(), prepared); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatal("tampered prepared attempt launched a process")
		}
	})
}
