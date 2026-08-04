//go:build darwin || linux

package terminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeProcessControllerControlsDescendantGroups(t *testing.T) {
	if os.Getenv("MARSHAL_PROCESS_TREE_HELPER") != "" {
		runProcessTreeHelper()
		return
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	root := exec.Command(os.Args[0], "-test.run=^TestNativeProcessControllerControlsDescendantGroups$")
	root.Env = append(os.Environ(), "MARSHAL_PROCESS_TREE_HELPER=root", "MARSHAL_CHILD_PID_PATH="+pidPath)
	root.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-root.Process.Pid, syscall.SIGKILL)
		_, _ = root.Process.Wait()
	}()
	childPID := waitPIDFile(t, pidPath)
	controller := nativeProcessController{}
	groups, err := controller.Pause(root.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	waitProcessState(t, root.Process.Pid, true)
	waitProcessState(t, childPID, true)
	if err := controller.Resume(root.Process.Pid, groups); err != nil {
		t.Fatal(err)
	}
	waitProcessState(t, root.Process.Pid, false)
	waitProcessState(t, childPID, false)
	if groups, rootGroup, found, err := processGroups(root.Process.Pid); err != nil || !found {
		t.Fatalf("process groups before terminate=%v root=%d found=%t error=%v", groups, rootGroup, found, err)
	} else {
		t.Logf("process groups before terminate=%v root=%d", groups, rootGroup)
	}
	if err := controller.Terminate(context.Background(), root.Process.Pid, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func runProcessTreeHelper() {
	if os.Getenv("MARSHAL_PROCESS_TREE_HELPER") == "root" {
		child := exec.Command(os.Args[0], "-test.run=^TestNativeProcessControllerControlsDescendantGroups$")
		child.Env = append(os.Environ(), "MARSHAL_PROCESS_TREE_HELPER=child")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if child.Start() != nil || os.WriteFile(os.Getenv("MARSHAL_CHILD_PID_PATH"), []byte(strconv.Itoa(child.Process.Pid)), 0o600) != nil {
			os.Exit(2)
		}
	}
	for {
		time.Sleep(time.Second)
	}
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("child PID was not published")
	return 0
}

func waitProcessState(t *testing.T, pid int, stopped bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
		isStopped := err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "T")
		if isStopped == stopped {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d stopped=%t was not observed", pid, stopped)
}
