//go:build darwin || linux

package review

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type secureRecord struct {
	data   []byte
	stat   unix.Stat_t
	exists bool
}

func ensureOutcomeRecords(runDirectory string, records map[string][]byte) error {
	dirFD, err := unix.Open(runDirectory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	for finalName, want := range records {
		pendingName := finalName + ".pending"
		final, err := readSecureRecordAt(dirFD, finalName, 2)
		if err != nil {
			return err
		}
		pending, err := readSecureRecordAt(dirFD, pendingName, 2)
		if err != nil {
			return err
		}
		if final.exists {
			if !bytes.Equal(final.data, want) {
				return fmt.Errorf("terminal outcome conflicts with authoritative event: %s", finalName)
			}
			if final.stat.Nlink == 2 {
				if !pending.exists || pending.stat.Dev != final.stat.Dev || pending.stat.Ino != final.stat.Ino || !bytes.Equal(pending.data, want) {
					return fmt.Errorf("terminal outcome link transaction is not bound: %s", finalName)
				}
			} else if final.stat.Nlink != 1 {
				return fmt.Errorf("terminal outcome has unsafe link count: %s", finalName)
			}
			if pending.exists {
				if final.stat.Nlink == 1 && pending.stat.Nlink != 1 {
					return fmt.Errorf("terminal pending outcome has unsafe link count: %s", pendingName)
				}
				if pending.stat.Nlink > 2 || !bytes.Equal(pending.data, want) {
					return fmt.Errorf("terminal pending outcome is unsafe: %s", pendingName)
				}
				if err := unix.Unlinkat(dirFD, pendingName, 0); err != nil {
					return err
				}
			}
			continue
		}
		if !pending.exists || pending.stat.Nlink != 1 || !bytes.Equal(pending.data, want) {
			if pending.exists && pending.stat.Nlink != 1 {
				return fmt.Errorf("terminal pending outcome has unsafe link count: %s", pendingName)
			}
			if err := writeSecureRecordAt(dirFD, pendingName, want, pending.exists); err != nil {
				return err
			}
		}
		if err := unix.Linkat(dirFD, pendingName, dirFD, finalName, 0); err != nil {
			return err
		}
		if err := unix.Unlinkat(dirFD, pendingName, 0); err != nil {
			return err
		}
	}
	return unix.Fsync(dirFD)
}

func readSecureRecordAt(dirFD int, name string, maxLinks uint64) (secureRecord, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return secureRecord{}, nil
	}
	if err != nil {
		return secureRecord{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return secureRecord{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink < 1 || uint64(stat.Nlink) > maxLinks {
		return secureRecord{}, fmt.Errorf("record is not a bounded-link regular file: %s", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return secureRecord{}, err
	}
	return secureRecord{data: data, stat: stat, exists: true}, nil
}

func writeSecureRecordAt(dirFD int, name string, data []byte, replace bool) error {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	temporary := "." + name + "-" + hex.EncodeToString(token[:]) + ".tmp"
	fd, err := unix.Openat(dirFD, temporary, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() {
		file.Close()
		_ = unix.Unlinkat(dirFD, temporary, 0)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if !replace {
		if err := unix.Linkat(dirFD, temporary, dirFD, name, 0); err != nil {
			return err
		}
		return unix.Unlinkat(dirFD, temporary, 0)
	}
	return unix.Renameat(dirFD, temporary, dirFD, name)
}
