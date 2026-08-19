//go:build darwin

package darwin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// AuthorityEndpointIdentity is the non-secret identity of the APAP control
// socket. A socket path is only an audit label; callers must retain the
// checked identity and the peer must still authenticate every connection.
type AuthorityEndpointIdentity struct {
	Path       string
	Device     uint64
	Inode      uint64
	OwnerUID   uint32
	Mode       uint32
	SocketType uint32
}

// InspectAuthorityEndpoint accepts only a private socket owned by the
// provisioner. Every parent is checked with Lstat before the leaf is read so
// a user-writable symlink cannot redirect the endpoint between checks.
func InspectAuthorityEndpoint(path string, ownerUID uint32) (AuthorityEndpointIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || ownerUID == ^uint32(0) {
		return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint path is invalid")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint path is invalid")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint path contains a symlink")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint parent is not a directory")
		}
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint stat failed")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != ownerUID || stat.Mode&0o077 != 0 {
		return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint owner or mode is unsafe")
	}
	return AuthorityEndpointIdentity{Path: path, Device: uint64(stat.Dev), Inode: uint64(stat.Ino), OwnerUID: stat.Uid, Mode: uint32(stat.Mode), SocketType: unix.S_IFSOCK}, nil
}
