//go:build darwin

package resultingress

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"golang.org/x/sys/unix"
)

type heldRegularIdentity struct {
	device    uint64
	inode     uint64
	mode      uint32
	uid       uint32
	gid       uint32
	linkCount uint64
}

type heldDarwinAuthorityFiles struct {
	mu             sync.Mutex
	directory      *os.File
	directoryID    processsupervisor.ControlDirectoryIdentity
	ledger         *os.File
	ledgerID       heldRegularIdentity
	coordination   *os.File
	lockID         heldRegularIdentity
	poisoned       bool
	operationWrote bool
	closed         bool
}

func openHeldDarwinAuthorityFiles(directory *os.File) (*heldDarwinAuthorityFiles, error) {
	identity, err := processsupervisor.ObserveHeldControlDirectory(directory)
	if err != nil || identity.Mode&0o777 != 0o700 || identity.UID != uint32(os.Geteuid()) || identity.GID != uint32(os.Getegid()) {
		return nil, ErrPreparedExecutionUnavailable
	}
	directoryFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	unix.CloseOnExec(directoryFD)
	retained := os.NewFile(uintptr(directoryFD), "marshal-result-ingress-directory")
	if retained == nil {
		_ = unix.Close(directoryFD)
		return nil, ErrPreparedExecutionUnavailable
	}
	retainedID, err := processsupervisor.ObserveHeldControlDirectory(retained)
	if err != nil || retainedID != identity {
		_ = retained.Close()
		return nil, ErrPreparedExecutionUnavailable
	}
	ledger, ledgerID, ledgerCreated, err := openHeldAuthorityFile(retained, resultIngressStoreFileName, false)
	if err != nil {
		_ = retained.Close()
		return nil, err
	}
	coordination, lockID, lockCreated, err := openHeldAuthorityFile(retained, resultIngressStoreLockName, true)
	if err != nil {
		_ = ledger.Close()
		_ = retained.Close()
		return nil, err
	}
	files := &heldDarwinAuthorityFiles{directory: retained, directoryID: identity, ledger: ledger, ledgerID: ledgerID, coordination: coordination, lockID: lockID}
	if ledgerCreated || lockCreated {
		if err := retained.Sync(); err != nil {
			_ = files.close()
			return nil, ErrPreparedExecutionUnavailable
		}
		// Creating the authority entries legitimately changes the directory
		// identity: Darwin 25 reports the directory link count including its
		// regular entries. Freeze the directory evidence only after the entries
		// are stable, and only for the exact same directory object.
		frozen, err := processsupervisor.ObserveHeldControlDirectory(retained)
		if err != nil || frozen.CanonicalPath != identity.CanonicalPath || frozen.Device != identity.Device || frozen.Inode != identity.Inode || frozen.Mode != identity.Mode || frozen.UID != identity.UID || frozen.GID != identity.GID {
			_ = files.close()
			return nil, ErrPreparedExecutionUnavailable
		}
		files.directoryID = frozen
	}
	if err := files.verifyCurrentNames(); err != nil {
		_ = files.close()
		return nil, err
	}
	return files, nil
}

func openHeldAuthorityFile(directory *os.File, name string, requireEmpty bool) (*os.File, heldRegularIdentity, bool, error) {
	flags := unix.O_RDWR | unix.O_APPEND | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(int(directory.Fd()), name, flags, 0)
	created := false
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(int(directory.Fd()), name, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		created = err == nil
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(int(directory.Fd()), name, flags, 0)
			created = false
		}
	}
	if err != nil {
		return nil, heldRegularIdentity{}, false, ErrPreparedExecutionUnavailable
	}
	file := os.NewFile(uintptr(fd), "marshal-result-ingress-"+name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, heldRegularIdentity{}, false, ErrPreparedExecutionUnavailable
	}
	identity, err := observeHeldRegular(directory, name, file, requireEmpty)
	if err != nil {
		_ = file.Close()
		return nil, heldRegularIdentity{}, false, err
	}
	return file, identity, created, nil
}

func observeHeldRegular(directory *os.File, name string, file *os.File, requireEmpty bool) (heldRegularIdentity, error) {
	if directory == nil || file == nil {
		return heldRegularIdentity{}, ErrPreparedExecutionUnavailable
	}
	var held, named unix.Stat_t
	if unix.Fstat(int(file.Fd()), &held) != nil || unix.Fstatat(int(directory.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return heldRegularIdentity{}, ErrPreparedExecutionUnavailable
	}
	if held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode || held.Uid != named.Uid || held.Gid != named.Gid || held.Nlink != named.Nlink || uint32(held.Mode)&unix.S_IFMT != unix.S_IFREG || uint32(held.Mode)&0o777 != 0o600 || held.Uid != uint32(os.Geteuid()) || held.Gid != uint32(os.Getegid()) || held.Nlink != 1 || requireEmpty && held.Size != 0 {
		return heldRegularIdentity{}, ErrPreparedExecutionUnavailable
	}
	return heldRegularIdentity{device: uint64(held.Dev), inode: held.Ino, mode: uint32(held.Mode), uid: held.Uid, gid: held.Gid, linkCount: uint64(held.Nlink)}, nil
}

func (files *heldDarwinAuthorityFiles) identityKey() string {
	if files == nil {
		return ""
	}
	return fmt.Sprintf("darwin-held-result-ingress:%d:%d", files.directoryID.Device, files.directoryID.Inode)
}

func (files *heldDarwinAuthorityFiles) verifyCurrentNames() error {
	if files == nil || files.closed || files.directory == nil || files.ledger == nil || files.coordination == nil {
		return ErrPreparedExecutionUnavailable
	}
	current, err := processsupervisor.ObserveHeldControlDirectory(files.directory)
	if err != nil || current != files.directoryID {
		return ErrPreparedExecutionUnavailable
	}
	currentFD, err := unix.Open(files.directoryID.CanonicalPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return ErrPreparedExecutionUnavailable
	}
	var currentStat unix.Stat_t
	statErr := unix.Fstat(currentFD, &currentStat)
	_ = unix.Close(currentFD)
	if statErr != nil || uint64(currentStat.Dev) != files.directoryID.Device || currentStat.Ino != files.directoryID.Inode || uint32(currentStat.Mode) != files.directoryID.Mode || currentStat.Uid != files.directoryID.UID || currentStat.Gid != files.directoryID.GID || uint64(currentStat.Nlink) != files.directoryID.LinkCount {
		return ErrPreparedExecutionUnavailable
	}
	ledgerID, err := observeHeldRegular(files.directory, resultIngressStoreFileName, files.ledger, false)
	if err != nil || ledgerID != files.ledgerID {
		return ErrPreparedExecutionUnavailable
	}
	lockID, err := observeHeldRegular(files.directory, resultIngressStoreLockName, files.coordination, true)
	if err != nil || lockID != files.lockID {
		return ErrPreparedExecutionUnavailable
	}
	return nil
}

func (files *heldDarwinAuthorityFiles) lockExclusive() (func() error, error) {
	if files == nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	files.mu.Lock()
	if err := files.verifyCurrentNames(); err != nil {
		files.mu.Unlock()
		return nil, err
	}
	if err := unix.Flock(int(files.coordination.Fd()), unix.LOCK_EX); err != nil {
		files.mu.Unlock()
		return nil, ErrPreparedExecutionUnavailable
	}
	if err := files.verifyCurrentNames(); err != nil {
		_ = unix.Flock(int(files.coordination.Fd()), unix.LOCK_UN)
		files.mu.Unlock()
		return nil, err
	}
	files.operationWrote = false
	return func() error {
		verifyErr := files.verifyCurrentNames()
		unlockErr := unix.Flock(int(files.coordination.Fd()), unix.LOCK_UN)
		wrote := files.operationWrote
		files.operationWrote = false
		if wrote && (verifyErr != nil || unlockErr != nil) {
			files.poisoned = true
		}
		files.mu.Unlock()
		if wrote && (verifyErr != nil || unlockErr != nil) {
			return ErrResultIngressOutcomeUnknown
		}
		if verifyErr != nil {
			return verifyErr
		}
		if unlockErr != nil {
			return ErrPreparedExecutionUnavailable
		}
		return nil
	}, nil
}

func (files *heldDarwinAuthorityFiles) readLedger() ([]byte, error) {
	if err := files.verifyCurrentNames(); err != nil {
		return nil, err
	}
	stat, err := files.ledger.Stat()
	if err != nil || stat.Size() < 0 {
		return nil, ErrPreparedExecutionUnavailable
	}
	data := make([]byte, stat.Size())
	if len(data) > 0 {
		n, readErr := files.ledger.ReadAt(data, 0)
		if readErr != nil && !errors.Is(readErr, io.EOF) || n != len(data) {
			return nil, ErrPreparedExecutionUnavailable
		}
	}
	if err := files.verifyCurrentNames(); err != nil {
		return nil, err
	}
	return data, nil
}

func (files *heldDarwinAuthorityFiles) appendLedger(line []byte) error {
	if len(line) == 0 || line[len(line)-1] != '\n' || files == nil {
		return ErrPreparedExecutionUnavailable
	}
	if err := files.verifyCurrentNames(); err != nil {
		return err
	}
	if files.poisoned {
		return ErrResultIngressOutcomeUnknown
	}
	files.operationWrote = true
	if n, err := files.ledger.Write(line); err != nil || n != len(line) {
		files.poisoned = true
		return ErrResultIngressOutcomeUnknown
	}
	if files.ledger.Sync() != nil || files.directory.Sync() != nil {
		files.poisoned = true
		return ErrResultIngressOutcomeUnknown
	}
	if err := files.verifyCurrentNames(); err != nil {
		files.poisoned = true
		return ErrResultIngressOutcomeUnknown
	}
	return nil
}

func (files *heldDarwinAuthorityFiles) close() error {
	if files == nil {
		return nil
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.closed {
		return nil
	}
	files.closed = true
	var first error
	for _, file := range []*os.File{files.coordination, files.ledger, files.directory} {
		if file != nil {
			if err := file.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
