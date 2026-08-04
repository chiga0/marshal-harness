//go:build darwin || linux

package terminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := backend.Start(ctx, StartRequest{
		StateRoot:        t.TempDir(),
		RunID:            "live-cmux",
		AttemptID:        "attempt-01",
		WorkingDirectory: t.TempDir(),
		Executable:       "/bin/cat",
		Title:            "Marshal live TerminalSession test",
		Description:      "Safe local PTY lifecycle validation; no model is invoked",
		InitialPrompt:    "INITIAL_PROBE",
		Now:              time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	native := session.(*cmuxSession)
	defer func() {
		_ = session.Terminate(context.Background(), time.Second)
		_, _ = backend.runner.Run(context.Background(), "workspace", "close", native.workspace)
	}()

	waitForScreen(t, ctx, session, "INITIAL_PROBE")
	if err := session.Pause(ctx); err != nil || session.State() != StatePaused {
		t.Fatalf("Pause() state=%s error=%v", session.State(), err)
	}
	state, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(native.pid)).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(state)), "T") {
		t.Fatalf("paused process state = %q, %v", state, err)
	}
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
