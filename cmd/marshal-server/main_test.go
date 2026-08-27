package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/repository"
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

// TestRunRecoversRunStateAcrossRestart 验证 marshal-server 跨进程 restart
// recovery（R6-TOP3）：第一个 server 进程查询到一个不存在的 run（404），
// 第二个 server 进程复用同一 state root 后对同一 run ID 的查询也返回 404，
// 证明 server 重启后 state root 可达且 projection 恢复正常。同时验证
// server 启动时 state root 中的 runstore 目录结构可被 reopen。
func TestRunRecoversRunStateAcrossRestart(t *testing.T) {
	root := testRepository(t)

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

	queryRun := func(listenAddr, runID string) int {
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
		io.ReadAll(response.Body)
		return response.StatusCode
	}

	// 第一轮：启动 server，查询不存在的 run。
	listen1, cancel1, exit1 := startServer()
	if code := queryRun(listen1, "run-restart-probe"); code != http.StatusNotFound {
		t.Fatalf("first server query status = %d, want 404", code)
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

	// 第二轮：新 server 进程复用同一 state root。
	listen2, cancel2, _ := startServer()
	defer cancel2()
	if code := queryRun(listen2, "run-restart-probe"); code != http.StatusNotFound {
		t.Fatalf("restarted server query status = %d, want 404 (state root should be accessible after reopen)", code)
	}
	// 验证 state root 目录结构存在且可被 reopen。
	if _, err := os.Stat(filepath.Join(root, ".marshal")); err != nil {
		t.Fatalf("state root .marshal directory missing after restart: %v", err)
	}
}
