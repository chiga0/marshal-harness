package darwin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ExecutablePolicy is the authority-owned identity of a deployed Darwin
// executable. Every field is required: a team identifier alone is not an
// executable identity, and a code-signature observation is not sufficient
// without the exact held-file digest. The same policy shape is used for the
// privileged launcher and for a candidate Qoder/Codex binary handed to it.
type ExecutablePolicy struct {
	SHA256     string `json:"sha256"`
	TeamID     string `json:"teamId"`
	CDHash     string `json:"cdHash"`
	Identifier string `json:"identifier"`
}

// LauncherPolicy is retained as the descriptive name used by the launchd
// deployment manifest. It is an alias, not a weaker policy.
type LauncherPolicy = ExecutablePolicy

func (policy ExecutablePolicy) validateShape() error {
	if policy.SHA256 == "" || policy.TeamID == "" || policy.CDHash == "" || policy.Identifier == "" {
		return errors.New("darwin launcher policy is incomplete")
	}
	return nil
}

func (policy ExecutablePolicy) validate(identity ExecutableIdentity) error {
	if err := policy.validateShape(); err != nil {
		return err
	}
	if identity.SHA256 != policy.SHA256 || identity.TeamID != policy.TeamID || identity.CDHash != policy.CDHash || identity.Identifier != policy.Identifier {
		return errors.New("darwin launcher identity does not match authority policy")
	}
	return nil
}

// HeldExecutable owns the descriptor that was verified. Callers may use the
// identity for an authority request, but cannot replace the descriptor with a
// pathname after admission. Execution is intentionally not provided here:
// Darwin needs a separately deployed, signed launcher to cross that boundary.
type HeldExecutable struct {
	file     *os.File
	identity ExecutableIdentity
}

// OpenHeldExecutable opens one absolute, non-symlink path and verifies the
// exact held Mach-O identity. It never resolves PATH or a mutable symlink.
// The returned descriptor is suitable for an externally deployed authority to
// receive via SCM_RIGHTS; this package deliberately provides no exec method.
func OpenHeldExecutable(path string, policy ExecutablePolicy) (*HeldExecutable, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errors.New("darwin launcher path must be absolute and clean")
	}
	fd, err := openExecutablePathNoFollow(path)
	if err != nil {
		return nil, errors.New("open Darwin executable")
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
	return &HeldExecutable{file: file, identity: identity}, nil
}

// openExecutablePathNoFollow pins every directory edge before opening the
// final executable. O_NOFOLLOW on only the leaf would still allow a replaced
// parent directory to redirect a privileged launcher or candidate.
func openExecutablePathNoFollow(path string) (int, error) {
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 {
		return -1, errors.New("darwin executable path has no parent")
	}
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	parents := []int{rootFD}
	closeParents := func() {
		for _, parent := range parents {
			_ = unix.Close(parent)
		}
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			closeParents()
			return -1, errors.New("darwin executable path component is invalid")
		}
		parent, err := unix.Openat(parents[len(parents)-1], part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			closeParents()
			return -1, err
		}
		parents = append(parents, parent)
	}
	leaf := parts[len(parts)-1]
	if leaf == "" || leaf == "." || leaf == ".." {
		closeParents()
		return -1, errors.New("darwin executable leaf is invalid")
	}
	fd, err := unix.Openat(parents[len(parents)-1], leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	closeParents()
	return fd, err
}

// OpenHeldLauncher opens the externally deployed privileged launcher. It is
// a descriptive wrapper around the same strict held-executable operation.
func OpenHeldLauncher(path string, policy LauncherPolicy) (*HeldExecutable, error) {
	return OpenHeldExecutable(path, policy)
}

// OpenHeldCandidate opens a Qoder/Codex candidate for delivery to the
// external launcher. Candidate admission remains authority-owned; this call
// only verifies and retains the exact executable inode.
func OpenHeldCandidate(path string, policy ExecutablePolicy) (*HeldExecutable, error) {
	return OpenHeldExecutable(path, policy)

}

func (launcher *HeldExecutable) Identity() (ExecutableIdentity, error) {
	if launcher == nil || launcher.file == nil {
		return ExecutableIdentity{}, errors.New("darwin executable is closed")
	}
	return launcher.identity, nil
}

// Duplicate returns a separate descriptor for SCM_RIGHTS delivery while the
// original held descriptor remains owned by this value. The duplicate is not
// an execution API and the caller must close it after transport handoff.
func (launcher *HeldExecutable) Duplicate() (*os.File, error) {
	if launcher == nil || launcher.file == nil {
		return nil, errors.New("darwin executable is closed")
	}
	fd, err := unix.Dup(int(launcher.file.Fd()))
	if err != nil {
		return nil, errors.New("duplicate Darwin executable descriptor")
	}
	return os.NewFile(uintptr(fd), "darwin-held-executable-duplicate"), nil
}

func (launcher *HeldExecutable) Close() error {
	if launcher == nil || launcher.file == nil {
		return nil
	}
	err := launcher.file.Close()
	launcher.file = nil
	return err
}
