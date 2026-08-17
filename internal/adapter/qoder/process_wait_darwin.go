//go:build darwin

package qoder

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var (
	qoderKqueue = unix.Kqueue
	qoderKevent = unix.Kevent
	qoderClose  = unix.Close
)

// waitProcessExitNoReap uses EVFILT_PROC/NOTE_EXIT to observe termination
// without consuming wait status. This avoids unsupported direct Darwin
// syscalls while keeping exec.Cmd.Wait as the sole reaper.
func waitProcessExitNoReap(pid int) error {
	queue, err := qoderKqueue()
	if err != nil {
		return err
	}
	defer qoderClose(queue)
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := qoderKevent(queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := qoderKevent(queue, nil, events, nil)
		if err == nil && count > 0 {
			if events[0].Flags&unix.EV_ERROR != 0 {
				if events[0].Data == 0 {
					return errors.New("qoder process exit observer returned EV_ERROR")
				}
				return fmt.Errorf("qoder process exit observer: %w", syscallErrno(events[0].Data))
			}
			return nil
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func syscallErrno(value int64) error { return unix.Errno(value) }
