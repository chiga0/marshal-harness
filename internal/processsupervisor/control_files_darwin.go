//go:build darwin

package processsupervisor

import (
	"io"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const nonceBytes = 64

type heldSessionControlFiles struct {
	nonce    *os.File
	journal  *os.File
	identity SessionControlFiles
}

func (files *heldSessionControlFiles) close() {
	if files == nil {
		return
	}
	if files.nonce != nil {
		_ = files.nonce.Close()
	}
	if files.journal != nil {
		_ = files.journal.Close()
	}
}

func observeControlFile(file *os.File) (ControlFileIdentity, int64, error) {
	if file == nil {
		return ControlFileIdentity{}, 0, ErrInvalid
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Size < 0 {
		return ControlFileIdentity{}, 0, ErrConflict
	}
	identity := ControlFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, FileType: "regular", UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink)}
	if identity.UID != uint32(os.Geteuid()) || identity.GID != uint32(os.Getegid()) || identity.validate() != nil {
		return ControlFileIdentity{}, 0, ErrConflict
	}
	return identity, stat.Size, nil
}

func observeControlFileAt(directory *os.File, name string) (ControlFileIdentity, int64, error) {
	if directory == nil || !validID(name) {
		return ControlFileIdentity{}, 0, ErrInvalid
	}
	var stat unix.Stat_t
	if unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Size < 0 {
		return ControlFileIdentity{}, 0, ErrConflict
	}
	identity := ControlFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, FileType: "regular", UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink)}
	if identity.UID != uint32(os.Geteuid()) || identity.GID != uint32(os.Getegid()) || identity.validate() != nil {
		return ControlFileIdentity{}, 0, ErrConflict
	}
	return identity, stat.Size, nil
}

func openControlFileAt(directory *os.File, name string) (*os.File, error) {
	if directory == nil || !validID(name) {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConflict
	}
	return os.NewFile(uintptr(fd), "marshal-supervisor-held-control-object"), nil
}

func openHeldSessionControlFiles(directory *os.File, expected SessionControlFiles) (*heldSessionControlFiles, error) {
	if expected.validate() != nil {
		return nil, ErrInvalid
	}
	nonce, err := openControlFileAt(directory, nonceFileName)
	if err != nil {
		return nil, err
	}
	journal, err := openControlFileAt(directory, JournalFileName)
	if err != nil {
		_ = nonce.Close()
		return nil, err
	}
	held := &heldSessionControlFiles{nonce: nonce, journal: journal, identity: expected}
	if err := revalidateHeldSessionControlFiles(directory, held, expected); err != nil {
		held.close()
		return nil, err
	}
	return held, nil
}

func revalidateHeldSessionControlFiles(directory *os.File, held *heldSessionControlFiles, expected SessionControlFiles) error {
	return revalidateHeldSessionControlFilesForLeaf(directory, held, expected, JournalFileName)
}

func revalidateHeldSessionControlFilesForLeaf(directory *os.File, held *heldSessionControlFiles, expected SessionControlFiles, leaf string) error {
	if leaf != JournalFileName && leaf != journalFileNameV2 {
		return ErrInvalid
	}
	if directory == nil || held == nil || expected.validate() != nil {
		return ErrInvalid
	}
	nonceHeld, _, err := observeControlFile(held.nonce)
	if err != nil || nonceHeld != expected.Nonce {
		return ErrConflict
	}
	journalHeld, _, err := observeControlFile(held.journal)
	if err != nil || journalHeld != expected.Journal {
		return ErrConflict
	}
	nonceAt, _, err := observeControlFileAt(directory, nonceFileName)
	if err != nil || nonceAt != expected.Nonce {
		return ErrConflict
	}
	journalAt, _, err := observeControlFileAt(directory, leaf)
	if err != nil || journalAt != expected.Journal {
		return ErrConflict
	}
	return nil
}

func readSessionNonce(held *heldSessionControlFiles, expectedDigest string) (string, error) {
	if held == nil || !validDigest(expectedDigest) {
		return "", ErrInvalid
	}
	_, size, err := observeControlFile(held.nonce)
	if err != nil || size != nonceBytes {
		return "", ErrConflict
	}
	buffer := make([]byte, nonceBytes+1)
	read, readErr := held.nonce.ReadAt(buffer, 0)
	if readErr != nil && readErr != io.EOF || read != nonceBytes || !hex64Pattern.Match(buffer[:nonceBytes]) || canonical.DigestBytes(buffer[:nonceBytes]) != expectedDigest {
		clear(buffer)
		return "", ErrConflict
	}
	nonce := string(buffer[:nonceBytes])
	clear(buffer)
	return nonce, nil
}
