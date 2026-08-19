//go:build !windows

package darwin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const launchdDeploymentConfigSchema = "marshal.darwin.launchd-deployment.v1"
const launchdDeploymentConfigLimit = 64 << 10

// LaunchdDeploymentConfig is public observation/policy material supplied by
// an external provisioning authority. It contains no private signing key or
// credential and cannot enable an adapter by itself.
type LaunchdDeploymentConfig struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Spec          LaunchdAuthoritySpec    `json:"spec"`
	Policy        LaunchdDeploymentPolicy `json:"policy"`
}

// LoadLaunchdDeploymentConfig reads one exact, private, canonical deployment
// record without following any path symlink. The record must require a
// root-owned APAP endpoint; user-owned endpoint policies are rejected.
func LoadLaunchdDeploymentConfig(path string) (LaunchdDeploymentConfig, error) {
	var config LaunchdDeploymentConfig
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return config, errors.New("darwin launchd deployment config path is invalid")
	}
	fd, err := openConfigNoSymlink(path)
	if err != nil {
		return config, errors.New("open darwin launchd deployment config")
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return config, errors.New("darwin launchd deployment config is not a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, launchdDeploymentConfigLimit+1))
	if err != nil || len(data) == 0 || len(data) > launchdDeploymentConfigLimit {
		return config, errors.New("darwin launchd deployment config is unreadable or oversized")
	}
	canonicalData, err := canonical.JSON(data)
	if err != nil || !bytes.Equal(canonicalData, data) {
		return config, errors.New("darwin launchd deployment config is not canonical")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return LaunchdDeploymentConfig{}, errors.New("darwin launchd deployment config is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return LaunchdDeploymentConfig{}, errors.New("darwin launchd deployment config has trailing data")
	}
	if config.SchemaVersion != launchdDeploymentConfigSchema || config.Policy.OwnerUID != 0 || config.Spec.validate() != nil || config.Policy.Service.validateShape() != nil || config.Policy.Launcher.validateShape() != nil {
		return LaunchdDeploymentConfig{}, errors.New("darwin launchd deployment config is incomplete")
	}
	return config, nil
}

func openConfigNoSymlink(path string) (int, error) {
	parts := splitAbsolutePath(path)
	if len(parts) == 0 {
		return -1, errors.New("darwin launchd deployment config path is invalid")
	}
	dirfd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errors.New("open darwin launchd deployment config root")
	}
	defer func() { _ = unix.Close(dirfd) }()
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return -1, errors.New("darwin launchd deployment config path is invalid")
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(dirfd, part, flags, 0)
		if openErr != nil {
			return -1, errors.New("open darwin launchd deployment config")
		}
		if index == len(parts)-1 {
			return fd, nil
		}
		unix.Close(dirfd)
		dirfd = fd
	}
	return -1, errors.New("open darwin launchd deployment config")
}

func splitAbsolutePath(path string) []string {
	trimmed := filepath.Clean(path)
	if trimmed == string(filepath.Separator) {
		return nil
	}
	return strings.Split(trimmed[1:], string(filepath.Separator))
}

// InspectConfiguredLaunchdDeployment combines strict config loading with the
// held-object deployment preflight. It still returns observation only.
func InspectConfiguredLaunchdDeployment(path string) (LaunchdDeploymentIdentity, error) {
	config, err := LoadLaunchdDeploymentConfig(path)
	if err != nil {
		return LaunchdDeploymentIdentity{}, err
	}
	return InspectLaunchdDeployment(config.Spec, config.Policy)
}
