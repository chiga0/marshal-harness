package darwin

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LauncherPolicy is the authority-owned identity of a deployed Darwin
// launcher. Every field is required: a team identifier alone is not an
// executable identity, and a code-signature observation is not sufficient
// without the exact held-file digest.
type LauncherPolicy struct {
	SHA256     string
	TeamID     string
	CDHash     string
	Identifier string
}

func (policy LauncherPolicy) validate(identity ExecutableIdentity) error {
	if policy.SHA256 == "" || policy.TeamID == "" || policy.CDHash == "" || policy.Identifier == "" {
		return errors.New("darwin launcher policy is incomplete")
	}
	if identity.SHA256 != policy.SHA256 || identity.TeamID != policy.TeamID || identity.CDHash != policy.CDHash || identity.Identifier != policy.Identifier {
		return errors.New("darwin launcher identity does not match authority policy")
	}
	return nil
}

// HeldLauncher owns the descriptor that was verified. Callers may use the
// identity for an authority request, but cannot replace the descriptor with a
// pathname after admission. Execution is intentionally not provided here:
// Darwin needs a separately deployed, signed launcher to cross that boundary.
type HeldLauncher struct {
	file     *os.File
	identity ExecutableIdentity
}

// OpenHeldLauncher opens one absolute, non-symlink path and verifies the
// exact held Mach-O identity. It never resolves PATH or a mutable symlink.
func OpenHeldLauncher(path string, policy LauncherPolicy) (*HeldLauncher, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errors.New("darwin launcher path must be absolute and clean")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("open Darwin launcher")
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	identity, err := InspectExecutable(file, policy.TeamID)
	if err != nil {
		return nil, err
	}
	if err := policy.validate(identity); err != nil {
		return nil, err
	}
	closeOnError = false
	return &HeldLauncher{file: file, identity: identity}, nil
}

func (launcher *HeldLauncher) Identity() (ExecutableIdentity, error) {
	if launcher == nil || launcher.file == nil {
		return ExecutableIdentity{}, errors.New("darwin launcher is closed")
	}
	return launcher.identity, nil
}

func (launcher *HeldLauncher) Close() error {
	if launcher == nil || launcher.file == nil {
		return nil
	}
	err := launcher.file.Close()
	launcher.file = nil
	return err
}
