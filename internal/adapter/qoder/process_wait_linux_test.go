//go:build linux

package qoder

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxExitObserverRetriesEINTR(t *testing.T) {
	original := qoderWaitid
	calls := 0
	qoderWaitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error {
		calls++
		if calls == 1 {
			return unix.EINTR
		}
		return nil
	}
	t.Cleanup(func() { qoderWaitid = original })
	if err := waitProcessExitNoReap(4242); err != nil || calls != 2 {
		t.Fatalf("observer = %v after %d calls, want success after EINTR retry", err, calls)
	}
}

func TestLinuxExitObserverPreservesECHILD(t *testing.T) {
	original := qoderWaitid
	qoderWaitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error { return unix.ECHILD }
	t.Cleanup(func() { qoderWaitid = original })
	if err := waitProcessExitNoReap(4242); !errors.Is(err, unix.ECHILD) {
		t.Fatalf("observer error = %v, want ECHILD", err)
	}
}
