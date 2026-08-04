//go:build darwin || linux

package terminal

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLiveCMUXTerminalSession is opt-in because it creates a visible cmux
// workspace and controls a real process group. It never invokes an AI model.
func TestLiveCMUXTerminalSession(t *testing.T) {
	if os.Getenv("MARSHAL_LIVE_CMUX") != "1" {
		t.Skip("set MARSHAL_LIVE_CMUX=1 to run the native PTY integration test")
	}
	cmuxPath := os.Getenv("MARSHAL_CMUX_PATH")
	if cmuxPath == "" {
		var err error
		cmuxPath, err = exec.LookPath("cmux")
		if err != nil {
			t.Fatal("cmux executable not found")
		}
	}
	cmuxPath, err := filepath.Abs(cmuxPath)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewCMUXBackend(cmuxPath)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := backend.Probe(context.Background())
	if err != nil || !probe.Available {
		t.Fatalf("cmux probe unavailable: diagnostic=%s error=%v", probe.Diagnostic, err)
	}
	helperExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	launcherExecutable := filepath.Join(t.TempDir(), "marshal")
	build := exec.Command("go", "build", "-o", launcherExecutable, "./cmd/marshal")
	build.Dir = repositoryRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build trusted launcher: %v: %s", buildErr, output)
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := backend.Start(ctx, StartRequest{
		StateRoot:                t.TempDir(),
		RunID:                    "live-cmux",
		AttemptID:                "attempt-01",
		WorkingDirectory:         t.TempDir(),
		LauncherExecutable:       launcherExecutable,
		Executable:               helperExecutable,
		ExpectedExecutableDigest: frozenDigest(t, helperExecutable),
		Arguments:                []string{"-test.run=^TestCMUXProcessRootHelper$"},
		Environment:              []string{"PATH=/usr/bin:/bin"},
		Title:                    "Marshal live TerminalSession test",
		Description:              "Safe local PTY lifecycle validation; no model is invoked",
		InitialPrompt:            "INITIAL_PROBE",
		Now:                      now,
		ExpiresAt:                now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	native := session.(*cmuxSession)
	defer func() {
		_ = session.Terminate(context.Background(), time.Second)
		_, _ = backend.runner.Run(context.Background(), "workspace", "close", native.workspace)
	}()

	waitForScreen(t, ctx, session, "MARSHAL_HELPER_READY")
	waitForScreen(t, ctx, session, "INITIAL_PROBE")
	waitForDescendantGroups(t, native.pid, 2)
	if groups, rootGroup, found, err := processGroups(native.pid); err == nil {
		t.Logf("live process groups=%v rootGroup=%d found=%t", groups, rootGroup, found)
	}
	if err := session.Pause(ctx); err != nil || session.State() != StatePaused {
		t.Fatalf("Pause() state=%s error=%v", session.State(), err)
	}
	state, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(native.pid)).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(state)), "T") {
		t.Fatalf("paused process state = %q, %v", state, err)
	}
	assertDescendantsStopped(t, native.pid)
	if err := session.Resume(ctx); err != nil || session.State() != StateRunning {
		t.Fatalf("Resume() state=%s error=%v", session.State(), err)
	}
	if err := session.Send(ctx, InputSourceLeadSteering, "SECOND_PROBE", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitForScreen(t, ctx, session, "SECOND_PROBE")
	if err := session.Terminate(ctx, time.Second); err != nil || session.State() != StateTerminated {
		t.Fatalf("Terminate() state=%s error=%v", session.State(), err)
	}
}

func TestCMUXProcessRootHelper(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestCMUXProcessRootHelper$" {
		t.Skip("cmux subprocess helper")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestCMUXProcessLeafHelper$")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("MARSHAL_HELPER_READY")
	if err := child.Wait(); err != nil {
		os.Exit(0)
	}
}

func TestCMUXProcessLeafHelper(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestCMUXProcessLeafHelper$" {
		t.Skip("cmux subprocess helper")
	}
	for {
		time.Sleep(time.Second)
	}
}

func waitForDescendantGroups(t *testing.T, root, minimum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		groups, _, found, err := processGroups(root)
		if err == nil && found && len(groups) >= minimum {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d did not create %d process groups", root, minimum)
}

func assertDescendantsStopped(t *testing.T, root int) {
	t.Helper()
	entries, err := processTable()
	if err != nil {
		t.Fatal(err)
	}
	descendants := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if descendants[entry.parent] && !descendants[entry.pid] {
				descendants[entry.pid], changed = true, true
			}
		}
	}
	if len(descendants) < 2 {
		t.Fatalf("descendant process tree = %v", descendants)
	}
	for _, entry := range entries {
		if descendants[entry.pid] && !strings.HasPrefix(entry.state, "T") {
			t.Fatalf("descendant %d state=%s is not stopped", entry.pid, entry.state)
		}
	}
}

func waitForScreen(t *testing.T, ctx context.Context, session Session, expected string) {
	t.Helper()
	for {
		screen, err := session.ReadScreen(ctx, 100)
		if err == nil && strings.Contains(screen, expected) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("screen did not contain %q: %v", expected, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
