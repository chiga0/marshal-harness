//go:build windows

package planning

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateCommandGroup starts the command in a new process group so console
// control events do not propagate between the harness and git. There is no
// SIGKILL process-group primitive on Windows; killCommandTree therefore kills
// the direct process only.
func isolateCommandGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killCommandTree terminates the command's direct process. Grandchildren that
// inherited the stdout pipe may outlive it on this platform; the bounded reap
// in runDirectCommand keeps the runner from hanging on them.
func killCommandTree(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}

// killProcessByID terminates a single known pid. It is used by tests to clean
// up fixture children when an assertion fails.
func killProcessByID(id int) {
	if id <= 0 {
		return
	}
	if process, err := os.FindProcess(id); err == nil {
		_ = process.Kill()
	}
}

// killProcessGroupByID falls back to killing the single known pid; Windows
// has no kill-by-process-group primitive in the standard library.
func killProcessGroupByID(id int) {
	killProcessByID(id)
}

// processAliveByID reports whether pid exists by opening it for querying. A
// terminated process whose handles are still held elsewhere may still open;
// tests only rely on this for the gone-to-gone transition after a kill.
func processAliveByID(id int) bool {
	if id <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(id))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}
