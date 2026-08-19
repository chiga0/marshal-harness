//go:build darwin

// Package darwin contains the small OS-owned observations shared by the Mac
// authority launchers. It deliberately does not grant execution authority:
// callers still need an accepted authority bundle and a deployed launcher.
package darwin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ExecutableIdentity is an observation of the exact inode held by the caller.
// The code-signing fields are observations only; an authority provider must
// sign and bind them before a launcher may execute the image.
type ExecutableIdentity struct {
	Path       string
	SHA256     string
	TeamID     string
	CDHash     string
	Identifier string
}

// InspectExecutable observes one already-open executable fd. The path is
// recovered from the fd with F_GETPATH only to invoke Apple's verifier; the
// source identity is rechecked after verification so a pathname replacement
// cannot silently become the observed image.
func InspectExecutable(file *os.File, expectedTeamID string) (ExecutableIdentity, error) {
	if file == nil {
		return ExecutableIdentity{}, errors.New("darwin executable fd is nil")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ExecutableIdentity{}, errors.New("darwin executable fd is not an executable regular file")
	}
	path, err := pathFromFD(int(file.Fd()))
	if err != nil {
		return ExecutableIdentity{}, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ExecutableIdentity{}, errors.New("darwin executable fd path is not absolute and clean")
	}
	if err := verifyCodeSignature(path); err != nil {
		return ExecutableIdentity{}, err
	}
	identity, err := parseCodeSignature(path)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	if expectedTeamID != "" && identity.TeamID != expectedTeamID {
		return ExecutableIdentity{}, fmt.Errorf("darwin code-sign team identity mismatch")
	}
	if err := sameOpenObject(file, path); err != nil {
		return ExecutableIdentity{}, err
	}
	digest, err := digestOpenFile(file)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	identity.Path = path
	identity.SHA256 = digest
	return identity, nil
}

func pathFromFD(fd int) (string, error) {
	if fd < 0 {
		return "", errors.New("darwin executable fd is invalid")
	}
	// Darwin's PATH_MAX is 1024 in the SDK; keep the buffer explicit because
	// x/sys/unix does not export that C macro on every supported arch.
	var path [1024]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&path[0])))
	if errno != 0 {
		return "", fmt.Errorf("darwin F_GETPATH: %w", errno)
	}
	length := 0
	for length < len(path) && path[length] != 0 {
		length++
	}
	if length == 0 {
		return "", errors.New("darwin executable fd has no path")
	}
	return string(path[:length]), nil
}

func verifyCodeSignature(path string) error {
	command := exec.Command("/usr/bin/codesign", "--verify", "--strict", "--verbose=2", path)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("darwin code-sign verification failed: %s", redactCommandOutput(output))
	}
	return nil
}

func parseCodeSignature(path string) (ExecutableIdentity, error) {
	command := exec.Command("/usr/bin/codesign", "-dv", "--verbose=4", path)
	output, err := command.CombinedOutput()
	if err != nil {
		return ExecutableIdentity{}, fmt.Errorf("darwin code-sign identity unavailable: %s", redactCommandOutput(output))
	}
	identity := ExecutableIdentity{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "Identifier":
			identity.Identifier = value
		case "TeamIdentifier":
			identity.TeamID = value
		case "CDHash":
			identity.CDHash = value
		}
	}
	if identity.Identifier == "" || identity.TeamID == "" || identity.CDHash == "" {
		return ExecutableIdentity{}, errors.New("darwin code-sign identity is incomplete")
	}
	return identity, nil
}

func sameOpenObject(file *os.File, path string) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	linked, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, linked) {
		return errors.New("darwin executable pathname was replaced during verification")
	}
	return nil
}

func digestOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := file.WriteTo(hash); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func redactCommandOutput(output []byte) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 256 {
		message = message[:256]
	}
	return message
}
