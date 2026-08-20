package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

// TestFakeExecutableConformsToQoderCLI proves the fake executable helper
// conforms to the real Qoder CLI contract the adapter consumes: exact bare
// `--version` reporting and a well-formed non-interactive JSONL stream that
// runs end-to-end without any real provider or network. Probe stays
// fail-closed at "unsupported" because live conformance is still pending.
func TestFakeExecutableConformsToQoderCLI(t *testing.T) {
	body := successEvents("provider/model")
	executable := fakeExecutable(t, supportedBinary, body)

	version, digestValue, err := Identify(executable)
	if err != nil {
		t.Fatal(err)
	}
	if version != supportedBinary || !strings.HasPrefix(digestValue, "sha256:") {
		t.Fatalf("identify = %q %q", version, digestValue)
	}

	adapter, err := New(executable, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(probe.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["probeStatus"] != "unsupported" || snapshot["binaryVersion"] != supportedBinary {
		t.Fatalf("probe snapshot = %v, want fail-closed unsupported/%s", snapshot, supportedBinary)
	}

	fixture := newRunFixture(t, supportedBinary, body)
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
	if result.Session == nil || result.Session.ID != "sess-1" || result.Adapter.ID != adapterID {
		t.Fatalf("result = %+v", result)
	}
}

// TestFakeExecutableReportsBareVersion proves the fake binary emits the exact
// bare semantic version the real Qoder CLI reports from `--version` (e.g.
// `1.1.23`, with no `qodercli` prefix), so the end-to-end conformance test
// above exercises the real probe path rather than a fabricated tool line.
func TestFakeExecutableReportsBareVersion(t *testing.T) {
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	output, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != supportedBinary {
		t.Fatalf("fake --version = %q, want bare %q", got, supportedBinary)
	}
}

// TestRunPassesFrozenArgvToWorker proves the adapter hands the worker the real
// non-interactive argv (help-derived flags) rather than the fabricated
// `run --json --non-interactive --sandbox workspace-write` construct.
func TestRunPassesFrozenArgvToWorker(t *testing.T) {
	body := "cat > stdin-dump.txt\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > argv-dump.txt\n" +
		successEvents("provider/model")
	fixture := newRunFixture(t, supportedBinary, body)
	promptSentinel := "qoder-prompt-secret-sentinel-0001"
	if err := os.WriteFile(filepath.Join(fixture.controlRoot, "input", "prompt.md"), []byte(promptSentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.worktree, "argv-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	resolved, err := filepath.EvalSymlinks(fixture.controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--print", "--output-format", "stream-json", "--permission-mode", "accept_edits", "--no-session-persistence", "--disallowed-tools", "Agent", "--config-dir", filepath.Join(resolved, "config", "qoder"), "--setting-sources", "", "--cwd", resolvedWorktree, "--model", "provider/model"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
	stdin, err := os.ReadFile(filepath.Join(fixture.worktree, "stdin-dump.txt"))
	if err != nil || string(stdin) != promptSentinel {
		t.Fatalf("stdin = %q err=%v", stdin, err)
	}
	if strings.Contains(strings.Join(argv, "\x00"), promptSentinel) {
		t.Fatalf("prompt leaked into argv: %#v", argv)
	}
}

func TestRunPermanentFailureReturnsDoNotRetry(t *testing.T) {
	body := errorEvents("mystery_code")
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Adapter != port.AdapterIDQoder || failure.Kind != port.FailureKindProviderTerminal || failure.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("err = %v, want typed provider-terminal/do-not-retry", err)
	}
	if strings.Contains(err.Error(), "mystery_code") {
		t.Fatalf("error leaked the unknown provider code: %v", err)
	}
}

func TestRunRetriableFailureReturnsRetryable(t *testing.T) {
	body := errorEvents("connection_failed")
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Adapter != port.AdapterIDQoder || failure.Kind != port.FailureKindConnectionFailure || failure.Disposition != port.RetryDispositionRetryable {
		t.Fatalf("err = %v, want typed connection-failure/retryable", err)
	}
}

func TestRunEmptyDeclarationReturnsProtocolInvalidDoNotRetry(t *testing.T) {
	body := successEvents("provider/model") + "\nexit 0"
	fixture := newRunFixtureWithResult(t, supportedBinary, body, nil)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	failure, ok := port.AsAdapterFailure(err)
	if !errors.Is(err, ErrProtocol) || !ok || failure.Adapter != port.AdapterIDQoder || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
		t.Fatalf("err = %v, want typed protocol-invalid/do-not-retry", err)
	}
}

func TestRunEnforcesOutputCap(t *testing.T) {
	large := strings.Repeat("x", 1800)
	body := emitLines(`{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model"}`, `{"type":"assistant","session_id":"sess-1","message":{"extra":"`+large+`"}}`, `{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","session_id":"sess-1"}`)
	fixture := newRunFixture(t, supportedBinary, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"maxOutputBytes": 1024})); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunTimeoutFailsClosed(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "sleep 60")
	request := fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 1})
	if _, err := fixture.adapter.Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunCancellationCleansProcessGroup(t *testing.T) {
	handshake := t.TempDir()
	pidFile := filepath.Join(handshake, "child.pid")
	readyFile := filepath.Join(handshake, "ready")
	body := "sleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile+".tmp") + " && mv " + shellQuote(pidFile+".tmp") + " " + shellQuote(pidFile) + "\n: > " + shellQuote(readyFile+".tmp") + " && mv " + shellQuote(readyFile+".tmp") + " " + shellQuote(readyFile) + "\nwait"
	fixture := newRunFixture(t, supportedBinary, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, runErr := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 15}))
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
}

func TestRunDirectExitCleansForkHoldingOutputPipes(t *testing.T) {
	handshake := t.TempDir()
	pidFile := filepath.Join(handshake, "child.pid")
	body := successEvents("provider/model") + "\nsleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile)
	fixture := newRunFixture(t, supportedBinary, body)
	started := time.Now()
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatalf("Run failed after successful direct-process exit: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("Run waited %s for a forked child holding output pipes", elapsed)
	}
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("pid file = %q: %v", pidData, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forked child %d survived direct-process cleanup", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWaitProcessExitNoReapRetainsLeaderIdentityUntilCleanup exercises the
// no-descendant fast-exit boundary repeatedly. Exit observation must leave the
// leader waitable, hence its PID/PGID cannot be recycled while cleanup targets
// the captured group. Only Cmd.Wait may release that identity afterward.
func TestWaitProcessExitNoReapRetainsLeaderIdentityUntilCleanup(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		command := exec.Command("sh", "-c", "read marshal_release || exit 0")
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdin, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		pid := command.Process.Pid
		groupID, err := syscall.Getpgid(pid)
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("iteration %d: acquire process group: %v", iteration, err)
		}
		if err := stdin.Close(); err != nil {
			t.Fatal(err)
		}
		if err := waitProcessExitNoReap(pid); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("iteration %d: observe exit without reap: %v", iteration, err)
		}
		_ = syscall.Kill(-groupID, syscall.SIGKILL)
		// A successful Cmd.Wait proves the exit observer did not reap the
		// child. Until this call releases the waitable leader, its numeric PID
		// cannot be reused as an unrelated process-group identity.
		if err := command.Wait(); err != nil {
			t.Fatalf("iteration %d: reap exited leader: %v", iteration, err)
		}
		if err := waitProcessExitNoReap(pid); !errors.Is(err, syscall.ECHILD) && !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("iteration %d: leader remained waitable after reap: %v", iteration, err)
		}
	}
}

func TestSignalOwnedProcessGroupRequiresSuccessfulExitObservation(t *testing.T) {
	for _, observationErr := range []error{syscall.ECHILD, syscall.ESRCH, syscall.EPERM, errors.New("observer failed")} {
		calls := 0
		signalOwnedProcessGroup(observationErr, 1234, func(int, syscall.Signal) error {
			calls++
			return nil
		})
		if calls != 0 {
			t.Fatalf("observation error %v emitted %d stale PGID signals", observationErr, calls)
		}
	}
	var target int
	var signal syscall.Signal
	signalOwnedProcessGroup(nil, 1234, func(pid int, sent syscall.Signal) error {
		target, signal = pid, sent
		return nil
	})
	if target != -1234 || signal != syscall.SIGKILL {
		t.Fatalf("owned group signal = (%d, %v), want (-1234, SIGKILL)", target, signal)
	}
}

func TestRunSensitiveEnvironmentNotInherited(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "qoder-gh-secret-0001")
	t.Setenv("OPENAI_API_KEY", "qoder-openai-secret-0002")
	t.Setenv("HOME", "/home/qoder-secret-home")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/qoder-ssh-secret")
	body := successEvents("provider/model") + "\nenv > env-dump.txt"
	fixture := newRunFixture(t, supportedBinary, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	dump, err := os.ReadFile(filepath.Join(fixture.worktree, "env-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(dump)
	for _, secret := range []string{
		"qoder-gh-secret-0001", "qoder-openai-secret-0002", "qoder-secret-home", "qoder-ssh-secret",
		"GITHUB_TOKEN", "OPENAI_API_KEY", "SSH_AUTH_SOCK",
	} {
		if strings.Contains(content, secret) {
			t.Fatalf("worker child environment leaked %q", secret)
		}
	}
	resolved, _ := filepath.EvalSymlinks(fixture.controlRoot)
	wantHome := filepath.Join(resolved, "config", "qoder")
	if !strings.Contains(content, "HOME="+wantHome) {
		t.Fatalf("worker child environment missing HOME=%s: %s", wantHome, content)
	}
	for _, want := range []string{"CI=1", "GIT_CONFIG_GLOBAL=/dev/null"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing isolation environment %s: %s", want, content)
		}
	}
}

// TestRunConfigIsolationHidesSystemHomeConfig proves the attempt cannot read
// user configuration from the system account home even when the ambient HOME
// points there: HOME is rebound to the Marshal-managed config dir.
func TestRunConfigIsolationHidesSystemHomeConfig(t *testing.T) {
	systemHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(systemHome, ".qoder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemHome, ".qoder", "config.json"), []byte(`{"credential":"system-home-secret-0001"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", systemHome)
	body := successEvents("provider/model") + "\nenv > env-dump.txt\nprintf '%s' \"$HOME\" > home-dump.txt"
	fixture := newRunFixture(t, supportedBinary, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	envDump, err := os.ReadFile(filepath.Join(fixture.worktree, "env-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	homeDump, err := os.ReadFile(filepath.Join(fixture.worktree, "home-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envDump), "system-home-secret-0001") || strings.Contains(string(envDump), systemHome) {
		t.Fatalf("worker environment leaked the system home config: %s", envDump)
	}
	resolved, _ := filepath.EvalSymlinks(fixture.controlRoot)
	wantHome := filepath.Join(resolved, "config", "qoder")
	if strings.TrimSpace(string(homeDump)) != wantHome {
		t.Fatalf("worker HOME = %q, want managed config dir %q", homeDump, wantHome)
	}
}

// TestRunHomeMissingStillBindsManagedConfigDir proves that even when HOME is
// unset/empty in the ambient environment, the worker still receives a
// non-empty managed HOME, so Node/Qoder cannot fall back to the system
// account home via os.homedir().
func TestRunHomeMissingStillBindsManagedConfigDir(t *testing.T) {
	t.Setenv("HOME", "")
	body := successEvents("provider/model") + "\nprintf '%s' \"$HOME\" > home-dump.txt"
	fixture := newRunFixture(t, supportedBinary, body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	homeDump, err := os.ReadFile(filepath.Join(fixture.worktree, "home-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(fixture.controlRoot)
	wantHome := filepath.Join(resolved, "config", "qoder")
	if strings.TrimSpace(string(homeDump)) != wantHome {
		t.Fatalf("worker HOME = %q, want managed config dir %q", homeDump, wantHome)
	}
}

// TestRunConfigDirSymlinkFailsClosedBeforeLaunch proves a symlinked config dir
// escapes the control root and must fail closed before the worker launches.
func TestRunConfigDirSymlinkFailsClosedBeforeLaunch(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	outside := t.TempDir()
	resolved, err := filepath.EvalSymlinks(fixture.controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(resolved, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(resolved, "config", "qoder")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want config dir symlink fail closed", err)
	}
}

// TestRunSettingSourcesDisabledHidesProjectAndLocalBait proves the attempt
// argv passes an empty --setting-sources set (so a real CLI would not read
// project or local settings) and that HOME is rebound to the managed config
// dir rather than the ambient account home.
func TestRunSettingSourcesDisabledHidesProjectAndLocalBait(t *testing.T) {
	body := successEvents("provider/model") + "\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > argv-dump.txt\nprintf '%s' \"$HOME\" > home-dump.txt"
	fixture := newRunFixture(t, supportedBinary, body)
	// Plant a project-level bait config inside the worktree that a real CLI
	// would read unless setting sources are disabled.
	writeJSON(t, filepath.Join(fixture.worktree, ".qoder", "config.json"), map[string]any{"credential": "project-bait"})
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	argvData, err := os.ReadFile(filepath.Join(fixture.worktree, "argv-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimRight(string(argvData), "\n"), "\n")
	if !containsSequence(argv, "--setting-sources", "") {
		t.Fatalf("argv must pass an empty setting-sources set: %#v", argv)
	}
	for _, bait := range []string{"managed", "user", "project", "local"} {
		if containsSequence(argv, "--setting-sources", bait) {
			t.Fatalf("argv leaked bait setting source %q: %#v", bait, argv)
		}
	}
	homeData, err := os.ReadFile(filepath.Join(fixture.worktree, "home-dump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(fixture.controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantHome := filepath.Join(resolved, "config", "qoder")
	if strings.TrimSpace(string(homeData)) != wantHome {
		t.Fatalf("worker HOME = %q, want managed config dir %q", homeData, wantHome)
	}
}

// fakeExecutable writes a fake Qoder CLI binary that answers `--version`
// with the bare semantic version (no tool prefix) and otherwise runs body as
// a shell script.
func fakeExecutable(t *testing.T, version, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(path, []byte(fakeScript(version, body)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeScript(version, body string) string {
	return "#!/bin/sh\nfor marshal_arg in \"$@\"; do if [ \"$marshal_arg\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi; done\n" + body + "\n"
}

// emitLines renders a `printf '%s\n'` shell command that writes each JSONL
// line verbatim, avoiding shell interpretation of JSON punctuation.
func emitLines(lines ...string) string {
	quoted := make([]string, len(lines))
	for i, line := range lines {
		quoted[i] = shellQuote(line)
	}
	return "printf '%s\\n' " + strings.Join(quoted, " ")
}

func workerResultTeeCommand(payload []byte) string {
	return workerResultTeeFirstLine + "\n" + string(payload) + "\nMARSHAL_RESULT"
}

func workerResultTeeToolUseEvent(id string) string {
	return workerResultTeeToolUseEventWithPayload(id, []byte("{}"))
}

func workerResultTeeToolUseEventWithPayload(id string, payload []byte) string {
	command, _ := json.Marshal(workerResultTeeCommand(payload))
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":` + string(command) + `,"description":"Emit final WorkerResult via adapter-held result channel"}}]}}`
}

func successfulWorkerResultTeeEvents(id string) []string {
	return []string{
		workerResultTeeToolUseEvent(id),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":""}]}}`,
	}
}

func successEvents(model string) string {
	return successEventsWithDeclaredPayload(model, []byte("{}"))
}

func successEventsWithDeclaredPayload(model string, payload []byte) string {
	events := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"` + model + `","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`,
		workerResultTeeToolUseEventWithPayload("tool-result", payload),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-result","content":""}]}}`,
	}
	events = append(events,
		`{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","usage":{"input_tokens":10,"output_tokens":5}}`,
	)
	return emitLines(events...)
}

func errorEvents(reason string) string {
	return emitLines(
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"provider/model","qodercli_version":"1.1.23","protocol_version":"1.2.0","permissionMode":"acceptEdits"}`,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"`+reason+`","usage":{"input_tokens":1,"output_tokens":0}}`,
	)
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
	return newRunFixtureWithResult(t, version, body, validDeclaredResultWithoutAdapterRuntimeMetadata())
}

func newRunFixtureWithResult(t *testing.T, version, body string, result map[string]any) runFixture {
	t.Helper()
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		defaultEvent := workerResultTeeToolUseEvent("tool-result")
		declaredEvent := workerResultTeeToolUseEventWithPayload("tool-result", data)
		body = strings.Replace(body, shellQuote(defaultEvent), shellQuote(declaredEvent), 1)
	}
	executable := fakeExecutable(t, version, body)
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	bindTestConformance(t, adapter)
	writeJSON(t, filepath.Join(controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model"}})
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("完成 fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestData := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "qoder", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 64 << 10, "reviewFindings": []any{},
	}
	requestBytes, _ := json.Marshal(requestData)
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}}
}

func bindTestConformance(t *testing.T, adapter *Adapter) {
	t.Helper()
	identity, err := adapter.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	pinned := identity
	adapter.pinned = &pinned
	adapter.conformance = &boundConformance{
		identity:            identity,
		evidenceDigest:      digest("d"),
		validUntil:          adapter.now().UTC().Add(24 * time.Hour),
		trustRootKeyID:      "test-root",
		probeProfileDigest:  expectedProbeProfileDigest(),
		hostFingerprint:     mustHostFingerprint(t),
		authorityGeneration: 1,
	}
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
		"adapter": map[string]any{"id": "qoder", "executable": executable, "version": "worker-claim"},
		"session": map[string]any{"id": "sess-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
}

func validDeclaredResultWithoutAdapterRuntimeMetadata() map[string]any {
	result := validDeclaredResult("/worker/claim")
	adapter := result["adapter"].(map[string]any)
	delete(adapter, "executable")
	delete(adapter, "version")
	return result
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

// containsSequence reports whether values appears as a contiguous run inside
// args. It lets argv tests assert an exact flag/value pair, including an
// empty-string value, without splitting on the delimiter.
func containsSequence(args []string, values ...string) bool {
	for i := 0; i+len(values) <= len(args); i++ {
		match := true
		for j, value := range values {
			if args[i+j] != value {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
