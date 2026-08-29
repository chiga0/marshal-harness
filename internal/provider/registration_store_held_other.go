//go:build !darwin

package provider

import "os"

type heldRegistrationFiles struct{}

func OpenDarwinRegistrationStore(*os.File) (*RegistrationStore, error) {
	return nil, ErrHeldRegistrationUnavailable
}

func (*heldRegistrationFiles) recover(*RegistrationStore) error {
	return ErrHeldRegistrationUnavailable
}
func (*heldRegistrationFiles) append([]byte) error { return ErrHeldRegistrationUnavailable }
func (*heldRegistrationFiles) close() error        { return nil }
