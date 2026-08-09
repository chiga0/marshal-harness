//go:build darwin || linux

package terminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// TestHerdrProbeUnavailableWithoutBinary asserts the herdr backend fails closed
// when no herdr executable is configured or present.
func TestHerdrProbeUnavailableWithoutBinary(t *testing.T) {
	if os.Getenv("MARSHAL_HERDR_PATH") != "" {
		t.Skip("MARSHAL_HERDR_PATH set; herdr present")
	}
	if _, err := exec.LookPath("herdr"); err == nil {
		t.Skip("herdr on PATH")
	}
	if _, err := NewHerdrBackend("relative/herdr"); err == nil {
		t.Fatal("relative path must be rejected")
	}
}

// TestHerdrProbeLive is opt-in: it exercises a real herdr control surface.
func TestHerdrProbeLive(t *testing.T) {
	path := herdrPath(t)
	backend, err := NewHerdrBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := backend.Probe(context.Background())
	if err != nil || !probe.Available {
		t.Fatalf("herdr probe unavailable: %s %v", probe.Diagnostic, err)
	}
	if probe.BackendID != HerdrBackendID {
		t.Fatalf("backend id = %s", probe.BackendID)
	}
}

// TestLiveHerdrTerminalSession is the real herdr Live E2E for the terminal-backend
// primitives: probe, workspace create, pane send-text/read, the advisory
// agent_status signal, and workspace close. The sealed-launcher-over-pane
// handshake (Start) is the remaining herdr command-surface calibration and is
// documented in docs/research/herdr-adr-supplement.md.
func TestLiveHerdrTerminalSession(t *testing.T) {
	path := herdrPath(t)
	backend, err := NewHerdrBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := backend.Probe(context.Background())
	if err != nil || !probe.Available {
		t.Fatalf("herdr probe unavailable: %s %v", probe.Diagnostic, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := backend.createWorkspace(ctx, port.TerminalStartRequest{WorkingDirectory: t.TempDir(), Title: "marshal-herdr-live"})
	if err != nil {
		t.Fatalf("createWorkspace: %v", err)
	}
	defer func() { _, _ = backend.runner.Run(context.Background(), "workspace", "close", created.Workspace) }()

	marker := "MARSHAL_HERDR_LIVE_READY"
	if _, err := backend.runner.Run(ctx, "pane", "send-text", created.Pane, "echo "+marker); err != nil {
		t.Fatalf("send-text: %v", err)
	}
	if _, err := backend.runner.Run(ctx, "pane", "send-keys", created.Pane, "enter"); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		output, err := backend.runner.Run(ctx, "pane", "read", created.Pane)
		if err == nil && strings.Contains(output, marker) {
			seen = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !seen {
		t.Fatal("marker not observed on herdr pane")
	}
	if status, err := backend.AuxiliaryStatus(ctx, created.Workspace); err == nil {
		t.Logf("herdr auxiliary agent_status=%q", status)
	}
}

func herdrPath(t *testing.T) string {
	t.Helper()
	if os.Getenv("MARSHAL_HERDR_PATH") == "" {
		t.Skip("set MARSHAL_HERDR_PATH to run the live herdr integration test")
	}
	path, err := filepath.Abs(os.Getenv("MARSHAL_HERDR_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}
