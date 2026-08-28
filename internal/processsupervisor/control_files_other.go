//go:build !darwin

package processsupervisor

import "os"

type heldSessionControlFiles struct{}

func (files *heldSessionControlFiles) close() {}

func openHeldSessionControlFiles(*os.File, SessionControlFiles) (*heldSessionControlFiles, error) {
	return nil, ErrUnavailable
}

func revalidateHeldSessionControlFiles(*os.File, *heldSessionControlFiles, SessionControlFiles) error {
	return ErrUnavailable
}

func readSessionNonce(*heldSessionControlFiles, string) (string, error) {
	return "", ErrUnavailable
}
