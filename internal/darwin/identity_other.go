//go:build !darwin

package darwin

import (
	"errors"
	"os"
)

type ExecutableIdentity struct {
	Path       string
	SHA256     string
	TeamID     string
	CDHash     string
	Identifier string
}

func InspectExecutable(*os.File, string) (ExecutableIdentity, error) {
	return ExecutableIdentity{}, errors.New("darwin executable identity is unavailable on this platform")
}
