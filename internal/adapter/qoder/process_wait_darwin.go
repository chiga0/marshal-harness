//go:build darwin

package qoder

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitProcessExitNoReap uses EVFILT_PROC/NOTE_EXIT to observe termination
// without consuming wait status. This avoids unsupported direct Darwin
// syscalls while keeping exec.Cmd.Wait as the sole reaper.
func waitProcessExitNoReap(pid int) error {
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(queue)
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(queue, nil, events, nil)
		if err == nil && count > 0 {
			return nil
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
