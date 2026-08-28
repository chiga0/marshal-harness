package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// testRepository builds one hermetic git repository with a bound Marshal
// identity record and returns its canonical root.
func testRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
		}
	}
	git("init", "-q")
	git("config", "user.name", "Marshal Server Test")
	git("config", "user.email", "server-test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("marshal-server test base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The stable shell fixture exposes the exact version JSON identity and
	// task-run argv needed by the process seam without compiling/executing an
	// anonymous temporary Go helper binary on macOS.
	fixtureCLI := `#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "${1:-}" = "version" ] && [ "${2:-}" = "--json" ]; then
  head=$(git -C "$root" rev-parse --verify HEAD)
  printf '{"version":"fixture","commit":"%s","buildDate":"fixture","goVersion":"fixture","os":"darwin","arch":"arm64","selfProfile":"darwin-local-dogfood"}\n' "$head"
  exit 0
fi
if [ "${1:-}" = "task" ] && [ "${2:-}" = "run" ]; then
  printf '%s\n' "$@" > "$root/.marshal/fixed-cli-invocation"
  if [ -n "${UNRELATED_SECRET:-}" ]; then printf 'leaked\n' >> "$root/.marshal/fixed-cli-invocation"; fi
  exit 0
fi
exit 2
`
	if err := os.WriteFile(filepath.Join(root, "bin", "marshal"), []byte(fixtureCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "base")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the repository root: %v", err)
	}
	state := repository.State{RepositoryRoot: canonicalRoot, StateRoot: filepath.Join(canonicalRoot, ".marshal")}
	if err := state.Init(); err != nil {
		t.Fatalf("bind the repository identity: %v", err)
	}
	return canonicalRoot
}

// TestRunServesLoopbackPublicAPI starts the resident server on an ephemeral
// loopback port, proves the versioned surface answers with the frozen error
// model, and shuts down cleanly when the context cancels.
func TestRunServesLoopbackPublicAPI(t *testing.T) {
	root := testRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer
	exitCode := make(chan int, 1)
	go func() {
		exitCode <- run(ctx, []string{"--listen", "127.0.0.1:0", "--dir", root}, stdoutWriter, &stderr)
	}()

	scanner := bufio.NewScanner(stdoutReader)
	if !scanner.Scan() {
		t.Fatalf("no listen banner: %v", scanner.Err())
	}
	var banner struct {
		Listen   string `json:"listen"`
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &banner); err != nil {
		t.Fatalf("decode listen banner %q: %v", scanner.Bytes(), err)
	}
	if !strings.HasPrefix(banner.Listen, "http://127.0.0.1:") {
		t.Fatalf("the server must bind loopback, got %q", banner.Listen)
	}
	if banner.Protocol != "marshal-public-api/v1alpha1" {
		t.Fatalf("protocol = %q", banner.Protocol)
	}

	request, err := http.NewRequest(http.MethodGet, banner.Listen+"/v1alpha1/runs/run-missing/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Marshal-Request-Id", "req-main-test")
	request.Header.Set("Marshal-Protocol-Version", "marshal-public-api/v1alpha1")
	request.Header.Set("Marshal-Principal", "main-test-operator")
	request.Header.Set("Marshal-Audience", "marshal-public-api")
	request.Header.Set("Marshal-Scope", "repo:"+filepath.ToSlash(root))
	request.Header.Set("Marshal-Deadline", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request the versioned surface: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body: %s", response.StatusCode, data)
	}
	var errorBody struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Code       string `json:"code"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(data, &errorBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errorBody.APIVersion != "marshal.dev/v1alpha1" || errorBody.Kind != "Error" ||
		errorBody.Code != "NOT_FOUND" || errorBody.Reason != "run-not-found" {
		t.Fatalf("error body = %+v", errorBody)
	}

	cancel()
	select {
	case code := <-exitCode:
		if code != exitOK {
			t.Fatalf("run returned %d, stderr: %s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("marshal-server did not shut down after cancellation")
	}
}

// TestRunRejectsNonLoopbackListen proves the fail-closed transport rule: no
// wildcard or routable bind is accepted before the remote TLS baseline is
// enabled.
func TestRunRejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:0", "[::]:0", "192.168.1.10:0", "localhost:0", ":7718"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--listen", listen, "--dir", "."}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("listen %s exit = %d, want %d (stderr: %s)", listen, code, exitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "loopback") {
			t.Fatalf("listen %s stderr lacks the loopback reason: %s", listen, stderr.String())
		}
	}
}

// TestRunFailsWithoutRepository proves the server refuses to start outside a
// bound repository.
func TestRunFailsWithoutRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--listen", "127.0.0.1:0", "--dir", t.TempDir()}, &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitFailure, stderr.String())
	}
}

// TestRunUsage covers the stable usage errors.
func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--bogus"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("unknown flag exit = %d, want %d", code, exitUsage)
	}
	if code := run(context.Background(), []string{"positional"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("positional argument exit = %d, want %d", code, exitUsage)
	}
}

func TestFixedMarshalIdentityRechecksObjectAndFiltersEnvironment(t *testing.T) {
	root := testRepository(t)
	environment := marshalChildEnvironment(append(os.Environ(), "UNRELATED_SECRET=must-not-cross"))
	identity, err := bindMarshalExecutable(root, "", environment)
	if err != nil {
		t.Fatalf("bind fixed marshal: %v", err)
	}
	if err := executeRunThroughFixedCLI(context.Background(), identity, root, "run-identity", environment); err != nil {
		t.Fatalf("execute admitted fixed marshal: %v", err)
	}
	marker := filepath.Join(root, ".marshal", "fixed-cli-invocation")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "leaked") || strings.Contains(string(data), "must-not-cross") {
		t.Fatalf("unrelated environment crossed the fixed CLI boundary: %q", data)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(identity.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# drift\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := executeRunThroughFixedCLI(context.Background(), identity, root, "run-drift", environment); err == nil {
		t.Fatal("fixed marshal execution accepted object drift")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drifted executable reached task run: %v", err)
	}
}

// TestLoopbackRunStartReachesFixedCLI exercises the real process seam from a
// loopback HTTP request through marshal-server to the startup-bound CLI. The
// stable shell fixture records argv but deliberately writes no Run authority,
// so the server must both prove the invocation and reject success when Core
// progress is absent.
func TestLoopbackRunStartReachesFixedCLI(t *testing.T) {
	root := testRepository(t)
	t.Setenv("UNRELATED_SECRET", "must-not-cross")
	const runID = "run-fixed-cli-seam"
	store := runstore.New(filepath.Join(root, ".marshal"))
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(lease, domain.RunState{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunState,
		TaskID: "task-fixed-cli-seam", RunID: runID, State: domain.StateReady,
	}); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer
	exitCode := make(chan int, 1)
	go func() {
		exitCode <- run(ctx, []string{"--listen", "127.0.0.1:0", "--dir", root}, stdoutWriter, &stderr)
	}()
	scanner := bufio.NewScanner(stdoutReader)
	if !scanner.Scan() {
		t.Fatalf("no listen banner: %v (%s)", scanner.Err(), stderr.String())
	}
	var banner struct {
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &banner); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{}`)
	digest, err := canonical.DigestJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"idempotencyKey": "fixed-cli-seam", "requestDigest": digest, "payload": payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, banner.Listen+"/v1alpha1/runs/"+runID+"/start", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Marshal-Request-Id", "req-fixed-cli-seam")
	request.Header.Set("Marshal-Protocol-Version", "marshal-public-api/v1alpha1")
	request.Header.Set("Marshal-Principal", "main-test-operator")
	request.Header.Set("Marshal-Audience", "marshal-public-api")
	request.Header.Set("Marshal-Scope", "repo:"+filepath.ToSlash(root))
	request.Header.Set("Marshal-Deadline", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseData, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(responseData, []byte("run-execution-not-observed")) {
		t.Fatalf("start without Core progress = %d %s", response.StatusCode, responseData)
	}
	invocation, err := os.ReadFile(filepath.Join(root, ".marshal", "fixed-cli-invocation"))
	if err != nil {
		t.Fatal(err)
	}
	want := "task\nrun\n--run\n" + runID + "\n--json\n"
	if string(invocation) != want {
		t.Fatalf("fixed CLI invocation = %q, want %q", invocation, want)
	}
	cancel()
	select {
	case code := <-exitCode:
		if code != exitOK {
			t.Fatalf("server exit=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
}

// TestRunRecoversRunStateAcrossRestart 验证 marshal-server 跨进程 restart
// recovery（审计反馈 #7 修复）：在 state root 中创建一个真实 Run（非终态
// Ready），启动 server 查询该 Run（应返回 200 + Ready 状态），杀死 server，
// 新 server 进程复用同一 state root 后查询同一 Run（应返回同样的 200 +
// Ready 状态），证明 runstore snapshot/journal 跨进程可恢复。
func TestRunRecoversRunStateAcrossRestart(t *testing.T) {
	root := testRepository(t)
	stateRoot := filepath.Join(root, ".marshal")

	// 在 state root 中创建一个真实 Run（非终态 Ready）。
	const runID = "run-restart-real"
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatalf("acquire run lease: %v", err)
	}
	runState := domain.RunState{
		APIVersion:       domain.APIVersionV1Alpha1,
		Kind:             domain.KindRunState,
		RunID:            runID,
		TaskID:           "TASK-RESTART",
		State:            domain.StateReady,
		Sequence:         0,
		BaseSHA:          "0000000000000000000000000000000000000000",
		WorktreePath:     "/tmp/worktree-restart-test",
		CapabilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := store.WriteSnapshot(lease, runState); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	startServer := func() (string, context.CancelFunc, <-chan int) {
		ctx, cancel := context.WithCancel(context.Background())
		stdoutReader, stdoutWriter := io.Pipe()
		var stderr bytes.Buffer
		exitCode := make(chan int, 1)
		go func() {
			exitCode <- run(ctx, []string{"--listen", "127.0.0.1:0", "--dir", root}, stdoutWriter, &stderr)
		}()
		scanner := bufio.NewScanner(stdoutReader)
		if !scanner.Scan() {
			t.Fatalf("no listen banner: %v", scanner.Err())
		}
		var banner struct {
			Listen   string `json:"listen"`
			Protocol string `json:"protocol"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &banner); err != nil {
			t.Fatalf("decode listen banner %q: %v", scanner.Bytes(), err)
		}
		return banner.Listen, cancel, exitCode
	}

	queryRun := func(listenAddr, runID string) (int, domain.RunState) {
		request, err := http.NewRequest(http.MethodGet, listenAddr+"/v1alpha1/runs/"+runID+"/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Marshal-Request-Id", "req-restart-test")
		request.Header.Set("Marshal-Protocol-Version", "marshal-public-api/v1alpha1")
		request.Header.Set("Marshal-Principal", "restart-test-operator")
		request.Header.Set("Marshal-Audience", "marshal-public-api")
		request.Header.Set("Marshal-Scope", "repo:"+filepath.ToSlash(root))
		request.Header.Set("Marshal-Deadline", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		client := &http.Client{Timeout: 30 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("request the versioned surface: %v", err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		var state domain.RunState
		_ = json.Unmarshal(body, &state)
		return response.StatusCode, state
	}

	// 第一轮：启动 server，查询真实 Run——应返回 200 + Ready。
	listen1, cancel1, exit1 := startServer()
	code1, state1 := queryRun(listen1, runID)
	if code1 != http.StatusOK {
		t.Fatalf("first server query status = %d, want 200 (real Run should be found)", code1)
	}
	if state1.RunID != runID || state1.State != domain.StateReady {
		t.Fatalf("first server returned unexpected state: RunID=%q State=%q, want RunID=%q State=%q", state1.RunID, state1.State, runID, domain.StateReady)
	}
	cancel1()
	select {
	case code := <-exit1:
		if code != exitOK {
			t.Fatalf("first server exit = %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("first server did not shut down")
	}

	// 第二轮：新 server 进程复用同一 state root——应返回同样的 200 + Ready。
	listen2, cancel2, _ := startServer()
	defer cancel2()
	code2, state2 := queryRun(listen2, runID)
	if code2 != http.StatusOK {
		t.Fatalf("restarted server query status = %d, want 200 (real Run should survive restart)", code2)
	}
	if state2.RunID != runID || state2.State != domain.StateReady {
		t.Fatalf("restarted server returned unexpected state: RunID=%q State=%q, want RunID=%q State=%q", state2.RunID, state2.State, runID, domain.StateReady)
	}
	if state1.Sequence != state2.Sequence {
		t.Errorf("sequence mismatch across restart: first=%d second=%d", state1.Sequence, state2.Sequence)
	}
	// 验证 state root 目录结构存在。
	if _, err := os.Stat(filepath.Join(stateRoot, "runs", runID)); err != nil {
		t.Fatalf("run directory missing after restart: %v", err)
	}
}
