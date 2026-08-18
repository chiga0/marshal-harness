//go:build darwin

package qoder

import (
	"errors"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinExitObserverAcceptsRegistrationESRCHAsAlreadyExited(t *testing.T) {
	originalKill := qoderKill
	qoderKill = func(int, syscall.Signal) error { return nil }
	t.Cleanup(func() { qoderKill = originalKill })
	installDarwinObserverFakes(t, func(_ int, changes, _ []unix.Kevent_t, _ *unix.Timespec) (int, error) {
		if len(changes) > 0 {
			return 0, unix.ESRCH
		}
		return 0, errors.New("unexpected wait")
	})
	if err := waitProcessExitNoReap(4242); err != nil {
		t.Fatalf("observer error = %v, want already-exited success", err)
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

func TestDarwinExitObserverRetriesRegistrationEINTRBeforeOwnedCleanup(t *testing.T) {
	registrationCalls := 0
	installDarwinObserverFakes(t, func(_ int, changes, events []unix.Kevent_t, _ *unix.Timespec) (int, error) {
		if len(changes) > 0 {
			registrationCalls++
			if registrationCalls == 1 {
				return 0, unix.EINTR
			}
			return 0, nil
		}
		events[0] = unix.Kevent_t{Fflags: unix.NOTE_EXIT}
		return 1, nil
	})
	observationErr := waitProcessExitNoReap(4242)
	cleanupCalls := 0
	signalOwnedProcessGroup(observationErr, 4242, func(pid int, signal unix.Signal) error {
		cleanupCalls++
		if pid != -4242 || signal != unix.SIGKILL {
			t.Fatalf("cleanup target = (%d, %v), want (-4242, SIGKILL)", pid, signal)
		}
		return nil
	})
	if observationErr != nil || registrationCalls != 2 || cleanupCalls != 1 {
		t.Fatalf("observation=%v registrationCalls=%d cleanupCalls=%d, want nil/2/1", observationErr, registrationCalls, cleanupCalls)
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
