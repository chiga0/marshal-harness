//go:build !unix

package runstore

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func listExistingRunIDs(store *Store) ([]string, error) {
	if store == nil || store.rootDirectory != nil {
		return nil, ErrDescriptorBoundPathAPI
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "runs"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
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
