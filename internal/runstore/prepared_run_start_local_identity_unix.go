//go:build darwin || linux

package runstore

import (
	"errors"
	"os"

	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"golang.org/x/sys/unix"
)

func localDispatchObservationDigestAt(runFD int, attemptID string) (string, bool, error) {
	attemptsFD, err := openDirectoryAt(runFD, "attempts", false)
	if errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer unix.Close(attemptsFD)
	attemptFD, err := openDirectoryAt(attemptsFD, attemptID, false)
	if errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer unix.Close(attemptFD)
	raw, err := readRegularAt(attemptFD, "local-self-identity-dispatch.json", 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	observation, err := selfidentity.DecodeObservation(raw)
	if err != nil {
		return "", false, err
	}
	return observation.ObservationDigest, true, nil
}
