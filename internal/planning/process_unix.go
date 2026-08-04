//go:build !windows

package planning

import (
	"os/exec"
	"syscall"
)

// isolateCommandGroup places the command in its own process group (pgid equal
// to the command's pid), so descendants such as interpreters and their child
// processes can be terminated together without touching unrelated processes.
func isolateCommandGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killCommandTree terminates the command's entire process group with SIGKILL.
// Killing the negative pgid reaches grandchildren that still hold the command
// stdout or stderr pipes, which is exactly the case that makes a bare wrapper
// kill hang a Wait.
func killCommandTree(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	killProcessGroupByID(command.Process.Pid)
}

// killProcessByID sends SIGKILL to a single known pid. It is used by tests to
// clean up fixture children when an assertion fails.
func killProcessByID(id int) {
	if id > 0 {
		_ = syscall.Kill(id, syscall.SIGKILL)
	}
}

// killProcessGroupByID sends SIGKILL to an entire known process group. It is
// used by tests to clean up fixture process groups when an assertion fails.
func killProcessGroupByID(id int) {
	if id > 0 {
		_ = syscall.Kill(-id, syscall.SIGKILL)
	}
}

// processAliveByID reports whether pid exists. EPERM means the process exists
// but belongs to another user, and therefore counts as alive.
func processAliveByID(id int) bool {
	if id <= 0 {
		return false
	}
	err := syscall.Kill(id, 0)
	return err == nil || err == syscall.EPERM
}
