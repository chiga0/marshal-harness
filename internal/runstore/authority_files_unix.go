//go:build unix

package runstore

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// BoundDirectory is a descriptor-relative child of the exact Run authority
// held by a Lease. Recheck proves every pathname edge still names the opened
// directory chain; callers never reopen the Run through a string path.
type BoundDirectory struct {
	lease *Lease
	files []*os.File
	names []string
}

// BoundLeaf holds the exact immutable inode first admitted under a
// descriptor-bound directory. Recheck rejects same-byte ABA replacement.
type BoundLeaf struct {
	directory *BoundDirectory
	file      *os.File
	name      string
	identity  unix.Stat_t
}

func BindLeaf(directory *BoundDirectory, name string) (*BoundLeaf, error) {
	if directory == nil || directory.File() == nil || validateRelativeComponent(name) != nil {
		return nil, errors.New("run authority leaf binding is invalid")
	}
	if err := directory.Recheck(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.File().Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	leaf := &BoundLeaf{directory: directory, file: os.NewFile(uintptr(fd), name), name: name}
	if err := unix.Fstat(fd, &leaf.identity); err != nil {
		leaf.Close()
		return nil, err
	}
	if leaf.identity.Mode&unix.S_IFMT != unix.S_IFREG || leaf.identity.Nlink != 1 {
		leaf.Close()
		return nil, errors.New("run authority leaf binding is not an immutable regular file")
	}
	if err := leaf.Recheck(); err != nil {
		leaf.Close()
		return nil, err
	}
	return leaf, nil
}

func (f *BoundLeaf) Close() error {
	if f == nil || f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *BoundLeaf) Recheck() error {
	if f == nil || f.file == nil || f.directory == nil {
		return errors.New("run authority leaf binding is closed")
	}
	if err := f.directory.Recheck(); err != nil {
		return err
	}
	var held, named unix.Stat_t
	if err := unix.Fstat(int(f.file.Fd()), &held); err != nil {
		return err
	}
	if err := unix.Fstatat(int(f.directory.File().Fd()), f.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if held.Dev != f.identity.Dev || held.Ino != f.identity.Ino || held.Mode != f.identity.Mode || held.Size != f.identity.Size || held.Nlink != 1 ||
		named.Dev != f.identity.Dev || named.Ino != f.identity.Ino || named.Mode != f.identity.Mode || named.Size != f.identity.Size || named.Nlink != 1 {
		return errors.New("run authority leaf binding changed while in use")
	}
	return nil
}

func (d *BoundDirectory) File() *os.File {
	if d == nil || len(d.files) == 0 {
		return nil
	}
	return d.files[len(d.files)-1]
}

func (d *BoundDirectory) Close() error {
	if d == nil {
		return nil
	}
	var result error
	for index := len(d.files) - 1; index >= 0; index-- {
		result = errors.Join(result, d.files[index].Close())
	}
	d.files = nil
	return result
}

func (d *BoundDirectory) Recheck() error {
	if d == nil || len(d.files) == 0 || len(d.names)+1 != len(d.files) {
		return errors.New("run authority directory chain is incomplete")
	}
	currentRoot, err := OpenRunAuthority(d.lease)
	if err != nil {
		return err
	}
	var heldRoot, namedRoot unix.Stat_t
	if err := unix.Fstat(int(d.files[0].Fd()), &heldRoot); err != nil {
		currentRoot.Close()
		return err
	}
	if err := unix.Fstat(int(currentRoot.Fd()), &namedRoot); err != nil {
		currentRoot.Close()
		return err
	}
	if err := currentRoot.Close(); err != nil {
		return err
	}
	if heldRoot.Dev != namedRoot.Dev || heldRoot.Ino != namedRoot.Ino || heldRoot.Mode != namedRoot.Mode {
		return errors.New("run authority root changed while in use")
	}
	for index, name := range d.names {
		var held, named unix.Stat_t
		if err := unix.Fstat(int(d.files[index+1].Fd()), &held); err != nil {
			return err
		}
		if err := unix.Fstatat(int(d.files[index].Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if held.Mode&unix.S_IFMT != unix.S_IFDIR || held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode {
			return errors.New("run authority directory pathname changed while in use")
		}
	}
	return nil
}

func validateRelativeComponent(component string) error {
	if component == "" || component == "." || component == ".." || strings.ContainsAny(component, `/\\`) || strings.IndexByte(component, 0) >= 0 {
		return errors.New("run authority relative component is invalid")
	}
	return nil
}

func OpenDirectoryUnderLease(lease *Lease, components ...string) (*BoundDirectory, error) {
	return openDirectoryUnderLease(lease, false, components...)
}

func OpenOrCreateDirectoryUnderLease(lease *Lease, components ...string) (*BoundDirectory, error) {
	return openDirectoryUnderLease(lease, true, components...)
}

func ListDirectoryNames(directory *BoundDirectory) ([]string, error) {
	if directory == nil || directory.File() == nil || directory.Recheck() != nil {
		return nil, errors.New("run authority directory listing is invalid")
	}
	entries, err := directory.File().ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := validateRelativeComponent(entry.Name()); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	if err := directory.Recheck(); err != nil {
		return nil, err
	}
	return names, nil
}

func openDirectoryUnderLease(lease *Lease, create bool, components ...string) (*BoundDirectory, error) {
	root, err := OpenRunAuthority(lease)
	if err != nil {
		return nil, err
	}
	bound := &BoundDirectory{lease: lease, files: []*os.File{root}}
	for _, component := range components {
		if err := validateRelativeComponent(component); err != nil {
			bound.Close()
			return nil, err
		}
		parentFD := int(bound.File().Fd())
		if create {
			if err := unix.Mkdirat(parentFD, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				bound.Close()
				return nil, err
			}
		}
		fd, err := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			bound.Close()
			return nil, err
		}
		bound.files = append(bound.files, os.NewFile(uintptr(fd), component))
		bound.names = append(bound.names, component)
		if err := bound.Recheck(); err != nil {
			bound.Close()
			return nil, err
		}
	}
	return bound, nil
}

// ReadFileUnderLease performs one bounded, single-link, nofollow read through
// the exact Run authority. All ancestors and the leaf are rebound after the
// read, rejecting symlink, hardlink, rename and ABA substitution.
func ReadFileUnderLease(lease *Lease, limit int64, components ...string) ([]byte, error) {
	return readFileUnderLease(lease, limit, nil, components...)
}

func readFileUnderLease(lease *Lease, limit int64, afterRead func(), components ...string) ([]byte, error) {
	if limit <= 0 || len(components) == 0 {
		return nil, errors.New("run authority file read is invalid")
	}
	name := components[len(components)-1]
	if err := validateRelativeComponent(name); err != nil {
		return nil, err
	}
	parent, err := OpenDirectoryUnderLease(lease, components[:len(components)-1]...)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return readFileInDirectory(parent, name, limit, afterRead)
}

func ReadFileInDirectory(directory *BoundDirectory, name string, limit int64) ([]byte, error) {
	return readFileInDirectory(directory, name, limit, nil)
}

func readFileInDirectory(directory *BoundDirectory, name string, limit int64, afterRead func()) ([]byte, error) {
	if directory == nil || directory.File() == nil || limit <= 0 {
		return nil, errors.New("run authority directory read is invalid")
	}
	if err := validateRelativeComponent(name); err != nil {
		return nil, err
	}
	if err := directory.Recheck(); err != nil {
		return nil, err
	}
	parentFD := int(directory.File().Fd())
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, named unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > limit {
		return nil, errors.New("run authority leaf is not a bounded single-link regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("run authority leaf exceeds its bound")
	}
	if afterRead != nil {
		afterRead()
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode || before.Nlink != after.Nlink ||
		before.Dev != named.Dev || before.Ino != named.Ino || before.Size != named.Size || before.Mode != named.Mode || named.Nlink != 1 {
		return nil, errors.New("run authority leaf changed while reading")
	}
	if err := directory.Recheck(); err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFileInDirectory installs one immutable single-link regular file under
// a held descriptor chain. The final name appears only after bytes and the
// temporary inode are synced; an existing final record is never replaced.
func WriteFileInDirectory(directory *BoundDirectory, name string, data []byte, mode uint32) error {
	if directory == nil || directory.File() == nil || len(data) == 0 || mode&0o077 != 0 {
		return errors.New("run authority directory write is invalid")
	}
	if err := validateRelativeComponent(name); err != nil {
		return err
	}
	if err := directory.Recheck(); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	pending := fmt.Sprintf(".%s.pending-%x", name, nonce[:])
	directoryFD := int(directory.File().Fd())
	fd, err := unix.Openat(directoryFD, pending, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, mode)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			_ = unix.Unlinkat(directoryFD, pending, 0)
		}
	}()
	file := os.NewFile(uintptr(fd), pending)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := directory.Recheck(); err != nil {
		return err
	}
	if err := unix.Linkat(directoryFD, pending, directoryFD, name, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		existing, readErr := ReadFileInDirectory(directory, name, int64(len(data)))
		if readErr != nil || !bytes.Equal(existing, data) {
			return errors.New("run authority immutable file conflicts with existing content")
		}
		if err := unix.Unlinkat(directoryFD, pending, 0); err != nil {
			return err
		}
		installed = true
		return directory.Recheck()
	}
	if err := unix.Unlinkat(directoryFD, pending, 0); err != nil {
		_ = unix.Unlinkat(directoryFD, name, 0)
		return err
	}
	installed = true
	if err := unix.Fsync(directoryFD); err != nil {
		return err
	}
	if err := directory.Recheck(); err != nil {
		return err
	}
	_, err = ReadFileInDirectory(directory, name, int64(len(data)))
	return err
}
