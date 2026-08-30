//go:build darwin

package provider

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const heldRegistrationPathBufferSize = 4096

type heldRegistrationMutation struct {
	changeSeconds int64
	changeNanos   int64
	birthSeconds  int64
	birthNanos    int64
	generation    uint32
}

type heldRegistrationIdentity struct {
	device    uint64
	inode     uint64
	mode      uint32
	uid       uint32
	gid       uint32
	linkCount uint64
	birth     heldRegistrationMutation
}

type heldRegistrationDirectoryIdentity struct {
	path     string
	object   heldRegistrationIdentity
	mutation heldRegistrationMutation
}

type heldRegistrationReadSnapshot struct {
	identity heldRegistrationIdentity
	size     int64
	mtimeSec int64
	mtimeNS  int64
	ctimeSec int64
	ctimeNS  int64
}

type heldRegistrationFiles struct {
	parent        *os.File
	parentID      heldRegistrationDirectoryIdentity
	directory     *os.File
	directoryID   heldRegistrationDirectoryIdentity
	directoryName string
	ledger        *os.File
	ledgerID      heldRegistrationIdentity
	closed        bool
	afterSync     func() error
}

// OpenDarwinRegistrationStore is the production-only provider registration
// constructor. It duplicates the caller-held owner-private directory and
// derives its parent/name edge from the descriptor itself. No pathname is
// accepted, and the legacy NewRegistrationStore(path) remains a separate
// test/compatibility backend.
func OpenDarwinRegistrationStore(directory *os.File) (*RegistrationStore, error) {
	files, err := openHeldRegistrationFiles(directory)
	if err != nil {
		return nil, err
	}
	store := &RegistrationStore{
		held:                files,
		byRegistrationId:    map[string]ProviderRegistration{},
		byIdempotencyDigest: map[string]string{},
	}
	if err := store.recover(); err != nil {
		_ = files.close()
		return nil, err
	}
	return store, nil
}

func openHeldRegistrationFiles(input *os.File) (*heldRegistrationFiles, error) {
	// Keep the caller-held descriptor alive until every duplicate/derived
	// descriptor has been validated; finalizer-driven close during this
	// multi-step authority graph construction would otherwise fail closed
	// nondeterministically on Darwin.
	defer runtime.KeepAlive(input)
	if input == nil {
		return nil, ErrHeldRegistrationUnavailable
	}
	directoryFD, err := unix.Dup(int(input.Fd()))
	if err != nil {
		return nil, ErrHeldRegistrationUnavailable
	}
	unix.CloseOnExec(directoryFD)
	directory := os.NewFile(uintptr(directoryFD), "marshal-provider-registration-directory")
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, ErrHeldRegistrationUnavailable
	}
	defer runtime.KeepAlive(directory)
	fail := func(err error) (*heldRegistrationFiles, error) {
		_ = directory.Close()
		return nil, err
	}
	preDirectory, err := observeHeldRegistrationDirectory(directoryFD, true)
	if err != nil {
		return fail(err)
	}
	parentFD, err := unix.Openat(directoryFD, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail(ErrHeldRegistrationUnavailable)
	}
	parent := os.NewFile(uintptr(parentFD), "marshal-provider-registration-parent")
	if parent == nil {
		_ = unix.Close(parentFD)
		return fail(ErrHeldRegistrationUnavailable)
	}
	defer runtime.KeepAlive(parent)
	files := &heldRegistrationFiles{parent: parent, directory: directory}
	failFiles := func(err error) (*heldRegistrationFiles, error) {
		_ = files.close()
		return nil, err
	}
	parentID, err := observeHeldRegistrationDirectory(parentFD, true)
	directoryName := filepath.Base(preDirectory.path)
	if err != nil || directoryName == "." || directoryName == string(filepath.Separator) || filepath.Join(parentID.path, directoryName) != preDirectory.path || !sameHeldRegistrationDirectoryAt(parentFD, directoryFD, directoryName, preDirectory) {
		return failFiles(ErrHeldRegistrationUnavailable)
	}
	ledger, ledgerID, created, err := openHeldRegistrationLedger(directoryFD)
	if err != nil {
		return failFiles(err)
	}
	files.ledger = ledger
	defer runtime.KeepAlive(ledger)
	if created {
		if err := ledger.Sync(); err != nil {
			return failFiles(fmt.Errorf("%w: sync new registration ledger: %v", ErrHeldRegistrationUnavailable, err))
		}
		if err := directory.Sync(); err != nil {
			return failFiles(fmt.Errorf("%w: sync registration ledger parent: %v", ErrHeldRegistrationUnavailable, err))
		}
	}
	parentID, err = observeHeldRegistrationDirectory(parentFD, true)
	directoryID, directoryErr := observeHeldRegistrationDirectory(directoryFD, true)
	if err != nil || directoryErr != nil || !sameHeldRegistrationDirectoryAt(parentFD, directoryFD, directoryName, directoryID) {
		return failFiles(ErrHeldRegistrationUnavailable)
	}
	files.parentID = parentID
	files.directoryID = directoryID
	files.directoryName = directoryName
	files.ledgerID = ledgerID
	if err := files.verifyCurrent(); err != nil {
		return failFiles(err)
	}
	return files, nil
}

func openHeldRegistrationLedger(directoryFD int) (*os.File, heldRegistrationIdentity, bool, error) {
	flags := unix.O_RDWR | unix.O_APPEND | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(directoryFD, ledgerFileName, flags, 0)
	created := false
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(directoryFD, ledgerFileName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		created = err == nil
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(directoryFD, ledgerFileName, flags, 0)
			created = false
		}
	}
	if err != nil {
		return nil, heldRegistrationIdentity{}, false, ErrHeldRegistrationUnavailable
	}
	file := os.NewFile(uintptr(fd), "marshal-provider-registration-ledger")
	if file == nil {
		_ = unix.Close(fd)
		return nil, heldRegistrationIdentity{}, false, ErrHeldRegistrationUnavailable
	}
	identity, err := observeHeldRegistrationRegular(fd)
	if err != nil {
		_ = file.Close()
		return nil, heldRegistrationIdentity{}, false, err
	}
	return file, identity, created, nil
}

func heldRegistrationPath(fd int) (string, error) {
	buffer := make([]byte, heldRegistrationPathBufferSize)
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buffer[0]))))
	if err != nil {
		return "", ErrHeldRegistrationUnavailable
	}
	end := bytes.IndexByte(buffer, 0)
	if end <= 0 {
		return "", ErrHeldRegistrationUnavailable
	}
	path := string(buffer[:end])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrHeldRegistrationUnavailable
	}
	return path, nil
}

func heldRegistrationMutationOf(stat unix.Stat_t) heldRegistrationMutation {
	return heldRegistrationMutation{changeSeconds: stat.Ctim.Sec, changeNanos: stat.Ctim.Nsec, birthSeconds: stat.Btim.Sec, birthNanos: stat.Btim.Nsec, generation: stat.Gen}
}

func heldRegistrationIdentityOf(stat unix.Stat_t) heldRegistrationIdentity {
	mutation := heldRegistrationMutationOf(stat)
	return heldRegistrationIdentity{device: uint64(stat.Dev), inode: stat.Ino, mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, linkCount: uint64(stat.Nlink), birth: heldRegistrationMutation{birthSeconds: mutation.birthSeconds, birthNanos: mutation.birthNanos, generation: mutation.generation}}
}

func observeHeldRegistrationDirectory(fd int, ownerOnly bool) (heldRegistrationDirectoryIdentity, error) {
	path, err := heldRegistrationPath(fd)
	var stat unix.Stat_t
	if err != nil || unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 2 || ownerOnly && (stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) || stat.Mode&0o777 != 0o700) {
		return heldRegistrationDirectoryIdentity{}, ErrHeldRegistrationUnavailable
	}
	return heldRegistrationDirectoryIdentity{path: path, object: heldRegistrationIdentityOf(stat), mutation: heldRegistrationMutationOf(stat)}, nil
}

func observeHeldRegistrationRegular(fd int) (heldRegistrationIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) || stat.Nlink != 1 {
		return heldRegistrationIdentity{}, ErrHeldRegistrationUnavailable
	}
	return heldRegistrationIdentityOf(stat), nil
}

func sameHeldRegistrationObject(stat unix.Stat_t, identity heldRegistrationIdentity, objectType uint32) bool {
	return uint64(stat.Dev) == identity.device && stat.Ino == identity.inode && uint32(stat.Mode) == identity.mode && stat.Uid == identity.uid && stat.Gid == identity.gid && uint64(stat.Nlink) == identity.linkCount && uint32(stat.Mode)&unix.S_IFMT == objectType && heldRegistrationIdentityOf(stat).birth == identity.birth
}

func sameHeldRegistrationDirectoryAt(parentFD, directoryFD int, name string, identity heldRegistrationDirectoryIdentity) bool {
	held, named := unix.Stat_t{}, unix.Stat_t{}
	if unix.Fstat(directoryFD, &held) != nil || unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	return sameHeldRegistrationObject(held, identity.object, unix.S_IFDIR) && sameHeldRegistrationObject(named, identity.object, unix.S_IFDIR) && heldRegistrationMutationOf(held) == identity.mutation && heldRegistrationMutationOf(named) == identity.mutation
}

func (files *heldRegistrationFiles) verifyCurrent() error {
	if files == nil || files.closed || files.parent == nil || files.directory == nil || files.ledger == nil {
		return ErrHeldRegistrationUnavailable
	}
	parent, err := observeHeldRegistrationDirectory(int(files.parent.Fd()), true)
	if err != nil || parent != files.parentID {
		return ErrHeldRegistrationUnavailable
	}
	directory, err := observeHeldRegistrationDirectory(int(files.directory.Fd()), true)
	if err != nil || directory != files.directoryID || !sameHeldRegistrationDirectoryAt(int(files.parent.Fd()), int(files.directory.Fd()), files.directoryName, files.directoryID) {
		return ErrHeldRegistrationUnavailable
	}
	held, named := unix.Stat_t{}, unix.Stat_t{}
	if unix.Fstat(int(files.ledger.Fd()), &held) != nil || unix.Fstatat(int(files.directory.Fd()), ledgerFileName, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameHeldRegistrationObject(held, files.ledgerID, unix.S_IFREG) || !sameHeldRegistrationObject(named, files.ledgerID, unix.S_IFREG) {
		return ErrHeldRegistrationUnavailable
	}
	return nil
}

func heldRegistrationReadSnapshotOf(file *os.File) (heldRegistrationReadSnapshot, error) {
	var stat unix.Stat_t
	if file == nil || unix.Fstat(int(file.Fd()), &stat) != nil {
		return heldRegistrationReadSnapshot{}, ErrHeldRegistrationUnavailable
	}
	return heldRegistrationReadSnapshot{identity: heldRegistrationIdentityOf(stat), size: stat.Size, mtimeSec: stat.Mtim.Sec, mtimeNS: stat.Mtim.Nsec, ctimeSec: stat.Ctim.Sec, ctimeNS: stat.Ctim.Nsec}, nil
}

func (files *heldRegistrationFiles) recover(store *RegistrationStore) error {
	if store == nil || files == nil || files.verifyCurrent() != nil {
		return ErrHeldRegistrationUnavailable
	}
	before, err := heldRegistrationReadSnapshotOf(files.ledger)
	if err != nil || before.size < 0 {
		return ErrHeldRegistrationUnavailable
	}
	if before.size > 0 {
		last := []byte{0}
		if n, readErr := files.ledger.ReadAt(last, before.size-1); readErr != nil || n != 1 || last[0] != '\n' {
			return fmt.Errorf("%w: registration ledger has a partial tail", ErrHeldRegistrationUnavailable)
		}
	}
	reader := io.NewSectionReader(files.ledger, 0, before.size)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			return fmt.Errorf("%w: empty registration ledger line %d", ErrHeldRegistrationUnavailable, lineNumber)
		}
		if err := store.applyLedgerLine(line); err != nil {
			return fmt.Errorf("provider: held registration ledger recovery failed at line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: read registration ledger: %v", ErrHeldRegistrationUnavailable, err)
	}
	after, err := heldRegistrationReadSnapshotOf(files.ledger)
	if err != nil || before != after || files.verifyCurrent() != nil {
		return ErrHeldRegistrationUnavailable
	}
	return nil
}

func (files *heldRegistrationFiles) append(line []byte) error {
	if len(line) == 0 || line[len(line)-1] != '\n' || bytes.Count(line, []byte{'\n'}) != 1 || files.verifyCurrent() != nil {
		return ErrHeldRegistrationUnavailable
	}
	written, err := files.ledger.Write(line)
	if err != nil || written != len(line) {
		return fmt.Errorf("%w: append registration fact", ErrHeldRegistrationUnavailable)
	}
	if err := files.ledger.Sync(); err != nil {
		return fmt.Errorf("%w: sync registration fact: %v", ErrHeldRegistrationUnavailable, err)
	}
	if files.afterSync != nil {
		if err := files.afterSync(); err != nil {
			return err
		}
	}
	return files.verifyCurrent()
}

func (files *heldRegistrationFiles) close() error {
	if files == nil || files.closed {
		return nil
	}
	files.closed = true
	var closeErrors []error
	for _, file := range []*os.File{files.ledger, files.directory, files.parent} {
		if file != nil {
			closeErrors = append(closeErrors, file.Close())
		}
	}
	return errors.Join(closeErrors...)
}
