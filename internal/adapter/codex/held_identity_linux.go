//go:build linux

package codex

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func heldExecutableStat(file *os.File) (heldExecutableStatV1, error) {
	var stat unix.Statx_t
	mask := unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_INO | unix.STATX_SIZE | unix.STATX_MNT_ID_UNIQUE
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, mask, &stat); err != nil {
		return heldExecutableStatV1{}, errors.New("held codex executable identity is unavailable")
	}
	if stat.Mask&uint32(unix.STATX_MNT_ID_UNIQUE) == 0 || stat.Mnt_id == 0 || stat.Ino == 0 || stat.Size == 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return heldExecutableStatV1{}, errors.New("held codex executable identity is incomplete")
	}
	return heldExecutableStatV1{
		deviceMajor: uint64(stat.Dev_major), deviceMinor: uint64(stat.Dev_minor),
		inode: stat.Ino, mountIDUnique: stat.Mnt_id, size: stat.Size, mode: uint32(stat.Mode),
	}, nil
}
