//go:build darwin || linux

package terminal

import (
	"context"
	"os"
	"os/exec"
	"testing"
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
	path := os.Getenv("MARSHAL_HERDR_PATH")
	if path == "" {
		t.Skip("set MARSHAL_HERDR_PATH to run the live herdr probe")
	}
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
