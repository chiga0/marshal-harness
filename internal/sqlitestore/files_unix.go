//go:build darwin || linux

package sqlitestore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// SQLite opens its own files by pathname. This private local profile checks
// the held/named graph at every transaction boundary and never claims hostile
// same-UID containment. The backend owns this dedicated directory exclusively;
// execution directories, artifacts and legacy ledgers live outside it.
type privateFiles struct {
	path       string
	parent     *os.File // held for Create/Open and their final child→parent sync
	root       *os.File
	lock       *os.File
	database   *os.File
	key        [2]uint64
	registered bool
}

var openRoots = struct {
	sync.Mutex
	keys map[[2]uint64]bool
}{keys: make(map[[2]uint64]bool)}

func openPrivateFiles(directory string, create bool) (_ *privateFiles, resultErr error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	f := &privateFiles{path: directory}
	defer func() {
		if resultErr != nil {
			_ = f.close()
		}
	}()
	parentPath := filepath.Dir(directory)
	parent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || parent != parentPath {
		return nil, ErrUnavailable
	}
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f.parent = os.NewFile(uintptr(parentFD), "sqlite-parent")
	if err = f.checkParent(); err != nil {
		return nil, err
	}
	if create {
		var existing unix.Stat_t
		if err = unix.Fstatat(parentFD, filepath.Base(directory), &existing, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			if unix.Mkdirat(parentFD, filepath.Base(directory), 0o700) != nil {
				return nil, ErrUnavailable
			}
		} else if err != nil {
			return nil, ErrUnavailable
		}
	}
	real, err := filepath.EvalSymlinks(directory)
	if err != nil || real != directory {
		return nil, ErrUnavailable
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(parentFD, filepath.Base(directory), flags, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f.root = os.NewFile(uintptr(fd), "sqlite-root")
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&0o7777 != 0o700 || info.Uid != uint32(os.Geteuid()) {
		return nil, ErrUnavailable
	}
	f.key = [2]uint64{uint64(info.Dev), info.Ino}
	openRoots.Lock()
	if openRoots.keys[f.key] {
		openRoots.Unlock()
		return nil, ErrBusy
	}
	openRoots.keys[f.key] = true
	f.registered = true
	openRoots.Unlock()
	if create {
		names, err := f.root.Readdirnames(1)
		if len(names) != 0 || !errors.Is(err, io.EOF) {
			return nil, ErrUnavailable
		}
	}
	flags = unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	lockFD, err := unix.Openat(fd, "owner.lock", flags, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	f.lock = os.NewFile(uintptr(lockFD), "sqlite-owner-lock")
	if err = unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, ErrUnavailable
	}
	dbFD, err := unix.Openat(fd, databaseName, flags, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	f.database = os.NewFile(uintptr(dbFD), "sqlite-database")
	if err = f.check(); err != nil {
		return nil, err
	}
	return f, nil
}

func sameObject(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Uid == right.Uid && left.Nlink == right.Nlink
}

func (f *privateFiles) checkParent() error {
	if f.parent == nil {
		return ErrUnavailable
	}
	var held, named unix.Stat_t
	if unix.Fstat(int(f.parent.Fd()), &held) != nil || unix.Lstat(filepath.Dir(f.path), &named) != nil ||
		!sameObject(&held, &named) || named.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrUnavailable
	}
	if f.root != nil && (unix.Fstat(int(f.root.Fd()), &held) != nil ||
		unix.Fstatat(int(f.parent.Fd()), filepath.Base(f.path), &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameObject(&held, &named)) {
		return ErrUnavailable
	}
	return nil
}

func (f *privateFiles) check() error {
	var held, named unix.Stat_t
	if f == nil || f.root == nil || unix.Fstat(int(f.root.Fd()), &held) != nil || unix.Lstat(f.path, &named) != nil ||
		!sameObject(&held, &named) || named.Mode&unix.S_IFMT != unix.S_IFDIR || named.Mode&0o7777 != 0o700 || named.Uid != uint32(os.Geteuid()) {
		return ErrUnavailable
	}
	real, err := filepath.EvalSymlinks(f.path)
	if err != nil || real != f.path {
		return ErrUnavailable
	}
	if err = f.checkParent(); err != nil {
		return err
	}
	for name, file := range map[string]*os.File{"owner.lock": f.lock, databaseName: f.database} {
		if file == nil || unix.Fstat(int(file.Fd()), &held) != nil || unix.Fstatat(int(f.root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			!sameObject(&held, &named) || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o7777 != 0o600 || named.Uid != uint32(os.Geteuid()) || named.Nlink != 1 {
			return ErrUnavailable
		}
	}
	for _, name := range []string{databaseName + "-wal", databaseName + "-shm", databaseName + "-journal"} {
		err := unix.Fstatat(int(f.root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o7777 != 0o600 || named.Uid != uint32(os.Geteuid()) || named.Nlink != 1 {
			return ErrUnavailable
		}
	}
	return nil
}
func (f *privateFiles) sync() error { return f.syncDirectories((*os.File).Sync) }

// The database/WAL have already committed with synchronous=FULL. Persist their
// directory entries first, then the root's own entry in its held parent. Failed
// sync leaves uncertain state in place; neither Create nor Open resets it.
// Open repeats both syncs so an earlier interrupted/failed Create cannot bypass
// the parent's outstanding directory-entry durability obligation.
func (f *privateFiles) syncDirectories(syncDirectory func(*os.File) error) error {
	if err := f.check(); err != nil {
		return err
	}
	if syncDirectory(f.root) != nil || f.check() != nil {
		return ErrUnavailable
	}
	if syncDirectory(f.parent) != nil || f.check() != nil {
		return ErrUnavailable
	}
	return nil
}
func (f *privateFiles) close() error {
	if f == nil {
		return nil
	}
	var err error
	if f.database != nil {
		err = errors.Join(err, f.database.Close())
		f.database = nil
	}
	if f.lock != nil {
		err = errors.Join(err, f.lock.Close())
		f.lock = nil
	}
	if f.root != nil {
		err = errors.Join(err, f.root.Close())
		f.root = nil
	}
	if f.parent != nil {
		err = errors.Join(err, f.parent.Close())
		f.parent = nil
	}
	if f.registered {
		openRoots.Lock()
		delete(openRoots.keys, f.key)
		openRoots.Unlock()
		f.registered = false
	}
	return err
}
