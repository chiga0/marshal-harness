//go:build !windows

package planning

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunDirectCommandCancellationKillsProcessGroup(t *testing.T) {
	tempDir := t.TempDir()
	readyPath := filepath.Join(tempDir, "ready")
	scriptPath := filepath.Join(tempDir, "git")
	script := "#!/bin/sh\n" +
		"sleep 30 &\n" +
		"child=$!\n" +
		"printf '%s %s\\n' \"$$\" \"$child\" > \"$1\"\n" +
		"wait \"$child\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runDirectCommand(ctx, []string{scriptPath, readyPath}, baseGitEnvironment(), io.Discard)
	}()

	parentPID, childPID := waitForFixturePIDs(t, readyPath)
	t.Cleanup(func() {
		killProcessGroupByID(parentPID)
		killProcessByID(childPID)
	})

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runDirectCommand cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runDirectCommand did not return promptly after cancellation")
	}

	waitForProcessExit(t, parentPID)
	waitForProcessExit(t, childPID)
}

func waitForFixturePIDs(t *testing.T, readyPath string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(readyPath)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != 2 {
				t.Fatalf("invalid fixture readiness record %q", data)
			}
			parentPID, parentErr := strconv.Atoi(fields[0])
			childPID, childErr := strconv.Atoi(fields[1])
			if parentErr != nil || childErr != nil || parentPID <= 0 || childPID <= 0 {
				t.Fatalf("invalid fixture pids %q", data)
			}
			return parentPID, childPID
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read fixture readiness: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not publish process ids")
	return 0, 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAliveByID(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture process %d remained alive after cancellation", pid)
}
