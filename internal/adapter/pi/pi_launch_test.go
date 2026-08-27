package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// goldenSessionBody is the scripted worker stdout for the golden attempt: a
// strict session v3 stream with one successful tool call and structured
// usage, closed by agent_settled.
func goldenSessionBody(sessionID string) string {
	return sessionHeader(sessionID) + "\n" + `printf '%s\n' \
		'{"type":"agent_start"}' \
		'{"type":"turn_start"}' \
		'{"type":"tool_execution_start","toolName":"read","toolCallId":"call-1","args":{"path":"file.txt"}}' \
		'{"type":"tool_execution_end","toolName":"read","toolCallId":"call-1"}' \
		'{"type":"turn_end"}' \
		'{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":0,"totalTokens":125,"cost":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"total":3}},"stopReason":"stop"}],"willRetry":false}' \
		'{"type":"agent_settled"}'`
}

func goldenRequest(fixture runFixture) domain.Record {
	return fixture.requestWith(map[string]any{"maxOutputBytes": 1 << 20})
}

func declaredGoldenResult(t *testing.T, fixture runFixture, sessionID string) []byte {
	t.Helper()
	declared := validDeclaredResult(fixture.executable)
	declared["session"].(map[string]any)["id"] = sessionID
	data, err := json.Marshal(declared)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func resultFilePath(controlRoot string) string {
	return filepath.Join(controlRoot, "output", "worker-result.json")
}

func TestPrepareLaunchFreezesArgvEnvironmentAndPathsWithoutSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "worker-launched")
	fixture := newRunFixture(t, supportedBinary, "touch "+shellQuote(marker))
	spawnCalls := 0
	fixture.adapter.spawn = func(cmd *exec.Cmd) error {
		spawnCalls++
		return cmd.Start()
	}
	plan, err := fixture.adapter.PrepareLaunch(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	// PrepareLaunch 返回 provider-neutral sandboxbridge.LaunchPlan；
	// 测试内部还原为 *LaunchPlan 访问 pi 专有字段。
	planConcrete, ok := plan.(*LaunchPlan)
	if !ok || planConcrete == nil {
		t.Fatalf("PrepareLaunch returned non-pi LaunchPlan: %T", plan)
	}
	// 以下断言通过 concrete 访问 pi 专有字段。
	concrete := planConcrete
	if spawnCalls != 0 {
		t.Fatalf("PrepareLaunch started the worker process %d times", spawnCalls)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("PrepareLaunch executed the worker script: %v", statErr)
	}
	expectedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	expectedControlRoot, err := filepath.EvalSymlinks(fixture.controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs, err := buildArgsWithTools("workspace-write", "provider/model", "完成 fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if concrete.ExecArgv[0] != fixture.executable {
		t.Fatalf("argv[0] = %q, want inspected executable %q", concrete.ExecArgv[0], fixture.executable)
	}
	if strings.Join(concrete.ExecArgv[1:], "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %#v, want %#v", concrete.ExecArgv[1:], wantArgs)
	}
	wantEnvironment := workerEnvironment(expectedWorktree)
	if strings.Join(concrete.Environment, "\n") != strings.Join(wantEnvironment, "\n") {
		t.Fatalf("environment = %q, want %q", strings.Join(concrete.Environment, "\n"), strings.Join(wantEnvironment, "\n"))
	}
	pathLike := false
	for _, entry := range concrete.Environment {
		if strings.HasPrefix(entry, "PATH=") {
			pathLike = true
		}
	}
	if !pathLike {
		t.Fatalf("environment lacks the allowlisted PATH entry: %q", strings.Join(concrete.Environment, "\n"))
	}
	if concrete.WorkingDirectory != expectedWorktree || concrete.ControlRoot != expectedControlRoot {
		t.Fatalf("paths = %q/%q", concrete.WorkingDirectory, concrete.ControlRoot)
	}
	if concrete.ResultPath != filepath.Join(expectedControlRoot, "output", "worker-result.json") {
		t.Fatalf("result path = %q", concrete.ResultPath)
	}
	if concrete.AttemptTimeoutSeconds != 5 || concrete.MaxOutputBytes != 1024 || concrete.SessionPolicy != "ephemeral" {
		t.Fatalf("budget/policy = %d/%d/%q", concrete.AttemptTimeoutSeconds, concrete.MaxOutputBytes, concrete.SessionPolicy)
	}
}

func TestPrepareLaunchFailClosedGatesNeverSpawn(t *testing.T) {
	declareBash := func(fixture runFixture) domain.Record {
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"tools": []string{"bash"}}})
		return fixture.request
	}
	tests := []struct {
		name    string
		version string
		record  func(runFixture) domain.Record
		want    error
	}{
		{name: "wrong-kind", record: func(f runFixture) domain.Record {
			return domain.Record{Kind: domain.KindWorkerResult, Data: f.request.Data}
		}},
		{name: "wrong-adapter-id", record: func(f runFixture) domain.Record {
			return f.requestWith(map[string]any{"adapterId": "other"})
		}},
		{name: "unknown-profile", record: func(f runFixture) domain.Record {
			return f.requestWith(map[string]any{"executionProfile": "admin"})
		}},
		{name: "session-policy-persist", record: func(f runFixture) domain.Record {
			return f.requestWith(map[string]any{"sessionPolicy": "persist"})
		}, want: ErrUnsupportedSessionPolicy},
		{name: "unsupported-binary", version: "0.84.2", record: func(f runFixture) domain.Record {
			return f.request
		}, want: ErrUnsupportedVersion},
		{name: "missing-prompt", record: func(f runFixture) domain.Record {
			return f.requestWith(map[string]any{"promptPath": "input/missing.md"})
		}},
		{name: "result-path-escape", record: func(f runFixture) domain.Record {
			return f.requestWith(map[string]any{"resultPath": "../escape.json"})
		}},
		{name: "declared-bash-unavailable", record: declareBash},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := test.version
			if version == "" {
				version = supportedBinary
			}
			marker := filepath.Join(t.TempDir(), "worker-launched")
			fixture := newRunFixture(t, version, "touch "+shellQuote(marker))
			spawnCalls := 0
			fixture.adapter.spawn = func(cmd *exec.Cmd) error {
				spawnCalls++
				return cmd.Start()
			}
			plan, err := fixture.adapter.PrepareLaunch(context.Background(), test.record(fixture))
			if err == nil {
				t.Fatal("fail-closed gate produced a plan")
			}
			if plan != nil {
				t.Fatalf("fail-closed gate returned plan %+v", plan)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if spawnCalls != 0 {
				t.Fatalf("fail-closed gate started the worker process %d times", spawnCalls)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("fail-closed gate executed the worker script: %v", statErr)
			}
		})
	}
}

// TestCompleteLaunchMatchesRunGoldenArtifacts drives the full Run path once
// against a scripted executable, then replays the captured transcript, stderr,
// timing, and exit disposition through PrepareLaunch + CompleteLaunch. The
// returned record and every on-disk control-output artifact must equal the
// Run outputs byte-for-byte.
func TestCompleteLaunchMatchesRunGoldenArtifacts(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, goldenSessionBody("session-golden")+"\nprintf '%s\\n' 'golden-stderr-warning' >&2\n")
	fixed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	fixture.adapter.now = func() time.Time { return fixed }
	request := goldenRequest(fixture)
	declaredBytes := declaredGoldenResult(t, fixture, "session-golden")
	if err := os.WriteFile(resultFilePath(fixture.controlRoot), declaredBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	runRecord, err := fixture.adapter.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(fixture.controlRoot, "output")
	golden := map[string][]byte{}
	for _, name := range []string{"pi-transcript.jsonl", "pi-stderr.log", "pi-transcript-meta.json", "worker-result.json"} {
		data, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		golden[name] = data
	}
	if _, err := os.Stat(filepath.Join(outputDir, denials.LogFileName)); !os.IsNotExist(err) {
		t.Fatalf("golden attempt unexpectedly produced a denial log: %v", err)
	}
	if err := os.RemoveAll(outputDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultFilePath(fixture.controlRoot), declaredBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.adapter.PrepareLaunch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	completedRecord, err := fixture.adapter.CompleteLaunch(context.Background(), plan,
		golden["pi-transcript.jsonl"], false, golden["pi-stderr.log"], fixed, fixed, 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if runRecord.Kind != domain.KindWorkerResult || completedRecord.Kind != runRecord.Kind {
		t.Fatalf("kinds = %s/%s", runRecord.Kind, completedRecord.Kind)
	}
	if !bytes.Equal(completedRecord.Data, runRecord.Data) {
		t.Fatalf("WorkerResult mismatch\nrun:      %s\ncomplete: %s", runRecord.Data, completedRecord.Data)
	}
	for _, name := range []string{"pi-transcript.jsonl", "pi-stderr.log", "pi-transcript-meta.json", "worker-result.json"} {
		data, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read complete %s: %v", name, err)
		}
		if !bytes.Equal(data, golden[name]) {
			t.Fatalf("artifact %s mismatch\nrun:      %s\ncomplete: %s", name, golden[name], data)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, denials.LogFileName)); !os.IsNotExist(err) {
		t.Fatalf("complete attempt unexpectedly produced a denial log: %v", err)
	}
}

// TestCompleteLaunchReplaysProcessFailureIdentically proves the failure tail
// is byte-identical too: the same captured evidence plus a nonzero exit
// disposition yields the same artifacts and the same classified error as Run.
func TestCompleteLaunchReplaysProcessFailureIdentically(t *testing.T) {
	body := sessionHeader("session-failure") + "\n" + `printf '%s\n' \
		'{"type":"agent_start"}' \
		'{"type":"agent_end","messages":[],"willRetry":false}' \
		'{"type":"agent_settled"}'` + "\nprintf '%s\\n' 'failure-stderr' >&2\nexit 3\n"
	fixture := newRunFixture(t, supportedBinary, body)
	fixed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	fixture.adapter.now = func() time.Time { return fixed }
	request := goldenRequest(fixture)
	_, runErr := fixture.adapter.Run(context.Background(), request)
	if !errors.Is(runErr, ErrProcessFailed) {
		t.Fatalf("run error = %v, want ErrProcessFailed", runErr)
	}
	outputDir := filepath.Join(fixture.controlRoot, "output")
	golden := map[string][]byte{}
	for _, name := range []string{"pi-transcript.jsonl", "pi-stderr.log", "pi-transcript-meta.json"} {
		data, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		golden[name] = data
	}
	if err := os.RemoveAll(outputDir); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.adapter.PrepareLaunch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, completeErr := fixture.adapter.CompleteLaunch(context.Background(), plan,
		golden["pi-transcript.jsonl"], false, golden["pi-stderr.log"], fixed, fixed, 3, "", nil)
	if completeErr == nil || completeErr.Error() != runErr.Error() {
		t.Fatalf("error mismatch: run = %v, complete = %v", runErr, completeErr)
	}
	for _, name := range []string{"pi-transcript.jsonl", "pi-stderr.log", "pi-transcript-meta.json"} {
		data, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read complete %s: %v", name, err)
		}
		if !bytes.Equal(data, golden[name]) {
			t.Fatalf("artifact %s mismatch\nrun:      %s\ncomplete: %s", name, golden[name], data)
		}
	}
}

func TestCompleteLaunchFailClosedInputContract(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, goldenSessionBody("session-input"))
	request := goldenRequest(fixture)
	validPlan, err := fixture.adapter.PrepareLaunch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	validConcrete, ok := validPlan.(*LaunchPlan)
	if !ok || validConcrete == nil {
		t.Fatalf("PrepareLaunch returned non-pi LaunchPlan: %T", validPlan)
	}
	fixed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	complete := func(plan *LaunchPlan, started, completed time.Time, exitCode int, signal string, ctxErr error) error {
		_, err := fixture.adapter.CompleteLaunch(context.Background(), plan, nil, false, nil, started, completed, exitCode, signal, ctxErr)
		return err
	}
	if err := complete(nil, fixed, fixed, 0, "", nil); err == nil {
		t.Fatal("nil plan accepted")
	}
	if err := complete(&LaunchPlan{ExecArgv: []string{validConcrete.ExecArgv[0]}}, fixed, fixed, 0, "", nil); err == nil {
		t.Fatal("plan without PrepareLaunch bindings accepted")
	}
	for _, exitCode := range []int{-2, 256, 1 << 20} {
		if err := complete(validConcrete, fixed, fixed, exitCode, "", nil); err == nil {
			t.Fatalf("exitCode %d accepted", exitCode)
		}
	}
	if err := complete(validConcrete, fixed, fixed, 0, "killed", nil); err == nil {
		t.Fatal("signaled disposition without exitCode -1 accepted")
	}
	if err := complete(validConcrete, time.Time{}, fixed, 0, "", nil); err == nil {
		t.Fatal("zero started accepted without context error")
	}
	if err := complete(validConcrete, fixed, fixed.Add(-time.Second), 0, "", nil); err == nil {
		t.Fatal("completed before started accepted without context error")
	}
	// Missing or unordered timing evidence is only tolerated while the
	// attempt context error stays authoritative; the completion then returns
	// that error through the frozen precedence.
	if err := complete(validConcrete, time.Time{}, time.Time{}, 0, "", context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context-carrying completion = %v, want DeadlineExceeded", err)
	}
}

func TestCompleteLaunchFailClosedStreamBehaviour(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, goldenSessionBody("session-stream"))
	request := goldenRequest(fixture)
	plan, err := fixture.adapter.PrepareLaunch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	outputDir := filepath.Join(fixture.controlRoot, "output")
	// An empty transcript of a nominally successful attempt fails closed
	// through the strict decoder, exactly like the live capture.
	_, err = fixture.adapter.CompleteLaunch(context.Background(), plan, nil, false, nil, fixed, fixed, 0, "", nil)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("empty successful transcript = %v, want ErrProtocol", err)
	}
	metadata, readErr := os.ReadFile(filepath.Join(outputDir, "pi-transcript-meta.json"))
	if readErr != nil || !strings.Contains(string(metadata), `"capturedBytes": 0`) {
		t.Fatalf("metadata = %s %v", metadata, readErr)
	}
	// A malformed stream fails closed with the protocol error.
	_, err = fixture.adapter.CompleteLaunch(context.Background(), plan, []byte("garbage\n"), false, nil, fixed, fixed, 0, "", nil)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed transcript = %v, want ErrProtocol", err)
	}
	// The executor truncation signal is authoritative over an otherwise
	// intact stream and stays ahead of the strict decode verdict.
	valid := `{"type":"session","version":3,"id":"session-stream","timestamp":"2026-08-26T00:00:00.000Z","cwd":"` + plan.WorkDir() + `"}` + "\n" +
		`{"type":"agent_start"}` + "\n" +
		`{"type":"agent_end","messages":[],"willRetry":false}` + "\n" +
		`{"type":"agent_settled"}` + "\n"
	_, err = fixture.adapter.CompleteLaunch(context.Background(), plan, []byte(valid), true, nil, fixed, fixed, 0, "", nil)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("executor-truncated transcript = %v, want ErrOutputLimit", err)
	}
	metadata, readErr = os.ReadFile(filepath.Join(outputDir, "pi-transcript-meta.json"))
	if readErr != nil || !strings.Contains(string(metadata), `"outputTruncated": true`) {
		t.Fatalf("metadata = %s %v", metadata, readErr)
	}
}

func TestRunReportsStartFailureThroughSpawnSeam(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	spawnCalls := 0
	fixture.adapter.spawn = func(cmd *exec.Cmd) error {
		spawnCalls++
		return errors.New("injected start failure")
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "start pi") {
		t.Fatalf("run error = %v, want start failure", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawnCalls)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "pi-transcript.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("start failure wrote transcript artifacts: %v", statErr)
	}
}
