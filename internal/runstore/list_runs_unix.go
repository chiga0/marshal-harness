//go:build unix

package runstore

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/domain"
	"golang.org/x/sys/unix"
)

func listExistingRunIDs(store *Store) ([]string, error) {
	if store == nil {
		return nil, ErrConflict
	}
	store.mu.RLock()
	rootPath := store.root
	var stateRoot *os.File
	if store.rootDirectory != nil {
		var err error
		stateRoot, err = duplicateDirectory(store.rootDirectory)
		if err != nil {
			store.mu.RUnlock()
			return nil, err
		}
	}
	store.mu.RUnlock()
	var runs *os.File
	if stateRoot != nil {
		defer stateRoot.Close()
		fd, err := unix.Openat(int(stateRoot.Fd()), "runs", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				return []string{}, nil
			}
			return nil, err
		}
		runs = os.NewFile(uintptr(fd), "runs")
	} else {
		var err error
		runs, err = os.Open(filepath.Join(rootPath, "runs"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return []string{}, nil
			}
			return nil, err
		}
	}
	defer runs.Close()
	entries, err := runs.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || domain.ValidateID(entry.Name()) != nil {
			return nil, ErrConflict
		}
		runIDs = append(runIDs, entry.Name())
	}
	return runIDs, nil
}
