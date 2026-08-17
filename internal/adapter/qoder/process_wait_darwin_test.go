//go:build darwin

package qoder

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinExitObserverRejectsRegistrationESRCH(t *testing.T) {
	installDarwinObserverFakes(t, func(_ int, changes, _ []unix.Kevent_t, _ *unix.Timespec) (int, error) {
		if len(changes) > 0 {
			return 0, unix.ESRCH
		}
		return 0, errors.New("unexpected wait")
	})
	if err := waitProcessExitNoReap(4242); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("observer error = %v, want ESRCH", err)
	}
}

func TestDarwinExitObserverRejectsEVError(t *testing.T) {
	installDarwinObserverFakes(t, func(_ int, changes, events []unix.Kevent_t, _ *unix.Timespec) (int, error) {
		if len(changes) > 0 {
			return 0, nil
		}
		events[0] = unix.Kevent_t{Flags: unix.EV_ERROR, Data: int64(unix.EPERM)}
		return 1, nil
	})
	if err := waitProcessExitNoReap(4242); !errors.Is(err, unix.EPERM) {
		t.Fatalf("observer error = %v, want EPERM", err)
	}
}

func installDarwinObserverFakes(t *testing.T, kevent func(int, []unix.Kevent_t, []unix.Kevent_t, *unix.Timespec) (int, error)) {
	t.Helper()
	originalQueue, originalEvent, originalClose := qoderKqueue, qoderKevent, qoderClose
	qoderKqueue = func() (int, error) { return 99, nil }
	qoderKevent = kevent
	qoderClose = func(int) error { return nil }
	t.Cleanup(func() {
		qoderKqueue, qoderKevent, qoderClose = originalQueue, originalEvent, originalClose
	})
}
