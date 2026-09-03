//go:build darwin

package resultingress

import (
	"os"
	"time"
)

// OpenDarwinCurrentOwnerReadView opens only existing authority files with
// read-only descriptors and coordinates replay with the writer via LOCK_SH.
// Missing files fail closed and are never created.
func OpenDarwinCurrentOwnerReadView(directory *os.File) (*CurrentOwnerReadView, error) {
	files, err := openHeldDarwinCurrentOwnerReadFiles(directory)
	if err != nil {
		return nil, err
	}
	return &CurrentOwnerReadView{store: &DurableStore{dir: files.directoryID.CanonicalPath, nextSequence: 1, clock: time.Now, heldFiles: files}}, nil
}
