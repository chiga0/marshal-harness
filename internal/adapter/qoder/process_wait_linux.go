//go:build linux

package qoder

import (
	"errors"

	"golang.org/x/sys/unix"
)

var qoderWaitid = unix.Waitid

// waitProcessExitNoReap blocks until pid exits but deliberately retains its
// waitable process-table entry. The caller can therefore signal the captured
// process group without allowing pid/pgid reuse, then use exec.Cmd.Wait as the
// sole reaper.
func waitProcessExitNoReap(pid int) error {
	for {
		var info unix.Siginfo
		err := qoderWaitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
