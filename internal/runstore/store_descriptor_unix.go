//go:build unix

package runstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func newFromStateRootDescriptor(stateRoot *os.File) (*Store, error) {
	if stateRoot == nil {
		return nil, errors.New("run store state root descriptor is nil")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(stateRoot.Fd()), &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 1 {
		return nil, errors.New("run store state root descriptor is not a directory")
	}
	root, err := duplicateDirectory(stateRoot)
	if err != nil {
		return nil, err
	}
	return &Store{rootDirectory: root}, nil
}

func duplicateDirectory(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, errors.New("directory descriptor is nil")
	}
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	result := os.NewFile(uintptr(fd), "run-store-state-root")
	if result == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create state root descriptor failed")
	}
	return result, nil
}
