//go:build linux

package codex

import (
	"errors"

	"golang.org/x/sys/unix"
)

func heldMountNamespaceIdentity() (uint64, uint64, error) {
	fd, err := unix.Open("/proc/self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, 0, errors.New("codex mount namespace fd is invalid")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}

func mountObjectIdentityForFD(fd int, role string, digest *string) (MountObjectIdentityV1, error) {
	var stat unix.Statx_t
	mask := unix.STATX_BASIC_STATS | unix.STATX_MNT_ID_UNIQUE
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, mask, &stat); err != nil {
		return MountObjectIdentityV1{}, err
	}
	required := uint32(unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO | unix.STATX_SIZE | unix.STATX_MNT_ID_UNIQUE)
	if stat.Mask&required != required || stat.Ino == 0 || stat.Mnt_id == 0 || role == "" {
		return MountObjectIdentityV1{}, errors.New("codex STATX_MNT_ID_UNIQUE identity is incomplete")
	}
	if digest != nil && !validDigest(*digest) {
		return MountObjectIdentityV1{}, errors.New("codex mount object content digest is invalid")
	}
	return MountObjectIdentityV1{Role: role, DeviceMajor: uint64(stat.Dev_major), DeviceMinor: uint64(stat.Dev_minor), Inode: stat.Ino, MountIDUnique: stat.Mnt_id, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid, Size: stat.Size, SHA256: digest}, nil
}
