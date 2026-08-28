//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentregistry

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireAgentLedgerLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("agentregistry: open authority lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("agentregistry: inspect authority lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("agentregistry: authority lock is not a single-link regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("agentregistry: acquire authority lock: %w", err)
	}
	return file, nil
}

func releaseAgentLedgerLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("agentregistry: release authority lock: %w", unlockErr)
	}
	return closeErr
}
