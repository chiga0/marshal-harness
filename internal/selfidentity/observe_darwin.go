//go:build darwin

package selfidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func observeCurrentPath(path string, afterRead func()) (CurrentPathObjectV1, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	file := os.NewFile(uintptr(fd), "marshal-current-path-object")
	if file == nil {
		unix.Close(fd)
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > maxExecutableBytes ||
		before.Mode&0o111 == 0 || (int(before.Uid) != os.Geteuid() && before.Uid != 0) {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	hash := sha256.New()
	read, err := io.CopyN(hash, file, before.Size)
	if err != nil || read != before.Size {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || (readErr != nil && readErr != io.EOF) {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	if afterRead != nil {
		afterRead()
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Size != after.Size || before.Mode != after.Mode || before.Uid != after.Uid {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	object := CurrentPathObjectV1{
		CanonicalPath: path, Device: decimalIdentity(uint64(before.Dev)), Inode: decimalIdentity(before.Ino),
		Size: before.Size, RawSHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		ObservationKind: "darwin-current-path-fd-object",
	}
	object.PathRechecked = executablePathNamesObject(path, object)
	if !object.PathRechecked {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	return object, nil
}

func openActivationFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, reject(ReasonOptInMissing)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Size <= 0 || stat.Size > maxActivationBytes || stat.Mode&0o022 != 0 || int(stat.Uid) != os.Geteuid() {
		unix.Close(fd)
		return nil, reject(ReasonOptInMissing)
	}
	file := os.NewFile(uintptr(fd), "marshal-local-dogfood-activation")
	if file == nil {
		unix.Close(fd)
		return nil, reject(ReasonOptInMissing)
	}
	return file, nil
}

func platformSupported() bool { return true }

func observePathIdentity(path string) (CurrentPathObjectV1, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return CurrentPathObjectV1{}, reject(ReasonObjectMismatch)
	}
	return CurrentPathObjectV1{Device: decimalIdentity(uint64(stat.Dev)), Inode: decimalIdentity(stat.Ino), Size: stat.Size}, nil
}

func decimalIdentity(value uint64) string { return fmt.Sprintf("%d", value) }

func executablePathNamesObject(path string, object CurrentPathObjectV1) bool {
	current, err := observePathIdentity(path)
	return err == nil && filepath.Clean(path) == path && current.Device == object.Device &&
		current.Inode == object.Inode && current.Size == object.Size
}
