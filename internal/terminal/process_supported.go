//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"syscall"
	"time"
)

type nativeProcessController struct{}

func defaultProcessController() processController { return nativeProcessController{} }
func (nativeProcessController) Supported() bool   { return true }

func (nativeProcessController) GroupID(pid int) (int, error) {
	if pid <= 1 {
		return 0, ErrAmbiguousProcess
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 1 {
		return 0, ErrAmbiguousProcess
	}
	return pgid, nil
}

func (nativeProcessController) Pause(pgid int) error {
	if pgid <= 1 {
		return ErrAmbiguousProcess
	}
	return syscall.Kill(-pgid, syscall.SIGSTOP)
}

func (nativeProcessController) Resume(pgid int) error {
	if pgid <= 1 {
		return ErrAmbiguousProcess
	}
	return syscall.Kill(-pgid, syscall.SIGCONT)
}

func (nativeProcessController) Terminate(ctx context.Context, pgid int, grace time.Duration) error {
	if pgid <= 1 || grace < 0 {
		return ErrInvalidRequest
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		case <-ticker.C:
		}
	}
}
