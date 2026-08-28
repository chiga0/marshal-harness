//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// Package workerresultfile installs the raw WorkerResult outbox payload with
// no-replace, creation-once semantics inside a trusted attempt directory.
package workerresultfile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrCreationOnceViolation = errors.New("worker-result creation-once violation")

const (
	workerResultName = "worker-result.json"
	coordinationName = ".worker-result.lock"
)

// PersistOnce publishes worker-result.json without ever replacing an existing
// directory entry. A fully synced same-directory temporary inode is installed
// with linkat; EEXIST is accepted only when a stable, single-link regular file
// is byte-identical. Directory fsync closes both the install and replay paths.
func PersistOnce(attemptDir string, data []byte) error {
	if attemptDir == "" {
		return nil
	}
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		return err
	}
	dirFD, err := unix.Open(filepath.Clean(attemptDir), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("workerresultfile: open attempt directory: %w", err)
	}
	defer unix.Close(dirFD)

	lockFD, err := unix.Openat(dirFD, coordinationName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("workerresultfile: open coordination lock: %w", err)
	}
	defer unix.Close(lockFD)
	if err := requireSingleLinkRegular(lockFD, "coordination lock"); err != nil {
		return err
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX); err != nil {
		return fmt.Errorf("workerresultfile: acquire coordination lock: %w", err)
	}
	defer unix.Flock(lockFD, unix.LOCK_UN) //nolint:errcheck -- best effort after operation result is fixed

	if exists, err := compareExisting(dirFD, data); exists || err != nil {
		if err == nil {
			err = unix.Fsync(dirFD)
		}
		return err
	}

	temporaryName, temporaryFD, err := createTemporary(dirFD)
	if err != nil {
		return err
	}
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			_ = unix.Close(temporaryFD)
		}
		_ = unix.Unlinkat(dirFD, temporaryName, 0)
	}()
	if err := writeAll(temporaryFD, data); err != nil {
		return err
	}
	if err := unix.Fsync(temporaryFD); err != nil {
		return fmt.Errorf("workerresultfile: sync temporary payload: %w", err)
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstat(temporaryFD, &temporaryStat); err != nil {
		return err
	}
	if err := unix.Close(temporaryFD); err != nil {
		return err
	}
	temporaryOpen = false

	if err := unix.Linkat(dirFD, temporaryName, dirFD, workerResultName, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("workerresultfile: install payload without replacement: %w", err)
		}
		exists, compareErr := compareExisting(dirFD, data)
		if compareErr != nil {
			return compareErr
		}
		if !exists {
			return fmt.Errorf("workerresultfile: EEXIST target disappeared")
		}
	} else {
		if err := unix.Unlinkat(dirFD, temporaryName, 0); err != nil {
			return fmt.Errorf("workerresultfile: unlink installed temporary name: %w", err)
		}
		var installed unix.Stat_t
		if err := unix.Fstatat(dirFD, workerResultName, &installed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("workerresultfile: recheck installed payload: %w", err)
		}
		if !sameFile(temporaryStat, installed) || !singleLinkRegular(installed) {
			return fmt.Errorf("workerresultfile: installed payload identity changed: %w", ErrCreationOnceViolation)
		}
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("workerresultfile: sync attempt directory: %w", err)
	}
	return nil
}

func compareExisting(dirFD int, expected []byte) (bool, error) {
	fd, err := unix.Openat(dirFD, workerResultName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("workerresultfile: open existing payload: %w", err)
	}
	file := os.NewFile(uintptr(fd), workerResultName)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return true, err
	}
	if !singleLinkRegular(before) {
		return true, fmt.Errorf("workerresultfile: existing payload is not a single-link regular file: %w", ErrCreationOnceViolation)
	}
	if before.Size != int64(len(expected)) {
		return true, ErrCreationOnceViolation
	}
	actual, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil {
		return true, err
	}
	var afterFD, afterPath unix.Stat_t
	if err := unix.Fstat(fd, &afterFD); err != nil {
		return true, err
	}
	if err := unix.Fstatat(dirFD, workerResultName, &afterPath, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return true, fmt.Errorf("workerresultfile: existing payload path changed: %w", ErrCreationOnceViolation)
	}
	if !sameFile(before, afterFD) || !sameFile(before, afterPath) || !singleLinkRegular(afterPath) {
		return true, fmt.Errorf("workerresultfile: existing payload ABA/path replacement: %w", ErrCreationOnceViolation)
	}
	if !bytes.Equal(actual, expected) {
		return true, ErrCreationOnceViolation
	}
	return true, nil
}

func createTemporary(dirFD int) (string, int, error) {
	for attempts := 0; attempts < 16; attempts++ {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", -1, err
		}
		name := ".worker-result-" + hex.EncodeToString(nonce[:]) + ".pending"
		fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, fmt.Errorf("workerresultfile: create temporary payload: %w", err)
		}
		return name, fd, nil
	}
	return "", -1, errors.New("workerresultfile: temporary name collision budget exhausted")
}

func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return fmt.Errorf("workerresultfile: write temporary payload: %w", err)
		}
		if n == 0 {
			return errors.New("workerresultfile: zero-length write")
		}
		data = data[n:]
	}
	return nil
}

func requireSingleLinkRegular(fd int, label string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("workerresultfile: inspect %s: %w", label, err)
	}
	if !singleLinkRegular(stat) {
		return fmt.Errorf("workerresultfile: %s is not a single-link regular file", label)
	}
	return nil
}

func singleLinkRegular(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1
}

func sameFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}
