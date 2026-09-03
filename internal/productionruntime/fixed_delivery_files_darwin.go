//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type fixedDeliveryPublishPhase string

const (
	fixedDeliveryPhaseBeforeStageWrite fixedDeliveryPublishPhase = "before-stage-write"
	fixedDeliveryPhaseAfterStageWrite  fixedDeliveryPublishPhase = "after-stage-write"
	fixedDeliveryPhaseAfterStageSync   fixedDeliveryPublishPhase = "after-stage-sync"
	fixedDeliveryPhaseBeforeRename     fixedDeliveryPublishPhase = "before-rename"
	fixedDeliveryPhaseAfterRename      fixedDeliveryPublishPhase = "after-rename"
	fixedDeliveryPhaseBeforeParentSync fixedDeliveryPublishPhase = "before-parent-sync"
	fixedDeliveryPhaseAfterParentSync  fixedDeliveryPublishPhase = "after-parent-sync"
)

type fixedDeliveryRecordIdentity struct {
	Device    uint64
	Inode     uint64
	UID       uint32
	GID       uint32
	Mode      uint32
	LinkCount uint64
	Size      int64
}

func fixedDeliveryRecordIdentityFromStat(stat unix.Stat_t, limit int64) (fixedDeliveryRecordIdentity, error) {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > limit {
		return fixedDeliveryRecordIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedDeliveryRecordIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink), Size: stat.Size}, nil
}

func observeFixedDeliveryRecord(fd int, limit int64) (fixedDeliveryRecordIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return fixedDeliveryRecordIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedDeliveryRecordIdentityFromStat(stat, limit)
}

func observeFixedDeliveryRecordAt(directoryFD int, leaf string, limit int64) (fixedDeliveryRecordIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstatat(directoryFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return fixedDeliveryRecordIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedDeliveryRecordIdentityFromStat(stat, limit)
}

func readFixedDeliveryRecord(root fixedServerRoot, leaf string, limit int64) ([]byte, bool, error) {
	raw, _, found, err := readFixedDeliveryRecordExact(root, leaf, limit)
	return raw, found, err
}

func readFixedDeliveryRecordExact(root fixedServerRoot, leaf string, limit int64) ([]byte, fixedDeliveryRecordIdentity, bool, error) {
	if leaf == "" || limit <= 0 || validateFixedServerRoot(root, 5) != nil {
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	directory := root.deliveryRoot()
	fd, err := unix.Openat(int(directory.Fd()), leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		if validateFixedServerRoot(root, 5) != nil {
			return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
		}
		return nil, fixedDeliveryRecordIdentity{}, false, nil
	}
	if err != nil {
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	file := os.NewFile(uintptr(fd), "marshal-fixed-delivery-record")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	defer file.Close()
	before, err := observeFixedDeliveryRecord(fd, limit)
	named, namedErr := observeFixedDeliveryRecordAt(int(directory.Fd()), leaf, limit)
	if err != nil || namedErr != nil || before != named {
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) != before.Size || int64(len(raw)) > limit {
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	after, err := observeFixedDeliveryRecord(fd, limit)
	named, namedErr = observeFixedDeliveryRecordAt(int(directory.Fd()), leaf, limit)
	if err != nil || namedErr != nil || before != after || before != named || validateFixedServerRoot(root, 5) != nil {
		return nil, fixedDeliveryRecordIdentity{}, false, ErrFixedDeliveryConflict
	}
	return raw, before, true, nil
}

// adoptFixedDeliveryRecord is the sole success path for a record whose final
// name already exists. Success means the exact nofollow object and bytes
// survived a delivery-parent sync and a second complete root-chain read.
func adoptFixedDeliveryRecord(root fixedServerRoot, leaf string, expected []byte, hook func(fixedDeliveryPublishPhase) error) error {
	before, beforeIdentity, found, err := readFixedDeliveryRecordExact(root, leaf, fixedDeliveryMaxRecord)
	if err != nil || !found || !bytes.Equal(before, expected) {
		return ErrFixedDeliveryConflict
	}
	if hook != nil {
		if err := hook(fixedDeliveryPhaseBeforeParentSync); err != nil {
			return ErrFixedDeliveryUnknown
		}
	}
	if root.deliveryRoot().Sync() != nil {
		return ErrFixedDeliveryUnknown
	}
	if hook != nil {
		if err := hook(fixedDeliveryPhaseAfterParentSync); err != nil {
			return ErrFixedDeliveryUnknown
		}
	}
	after, afterIdentity, found, err := readFixedDeliveryRecordExact(root, leaf, fixedDeliveryMaxRecord)
	if err != nil || !found || beforeIdentity != afterIdentity || !bytes.Equal(after, expected) || validateFixedServerRoot(root, 5) != nil {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func publishFixedDeliveryRecord(root fixedServerRoot, leaf string, raw []byte, hook func(fixedDeliveryPublishPhase) error) error {
	if leaf == "" || len(raw) == 0 || len(raw) > fixedDeliveryMaxRecord || validateFixedServerRoot(root, 5) != nil {
		return ErrFixedDeliveryConflict
	}
	directory := root.deliveryRoot()
	if _, found, err := readFixedDeliveryRecord(root, leaf, fixedDeliveryMaxRecord); err != nil {
		return err
	} else if found {
		return adoptFixedDeliveryRecord(root, leaf, raw, hook)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return ErrFixedDeliveryUnknown
	}
	stage := ".stage-" + hex.EncodeToString(nonce)
	fd, err := unix.Openat(int(directory.Fd()), stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return ErrFixedDeliveryUnknown
	}
	file := os.NewFile(uintptr(fd), "marshal-fixed-delivery-stage")
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(directory.Fd()), stage, 0)
		return ErrFixedDeliveryUnknown
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), stage, 0)
		}
	}()
	callHook := func(phase fixedDeliveryPublishPhase) error {
		if hook == nil {
			return nil
		}
		return hook(phase)
	}
	if err := callHook(fixedDeliveryPhaseBeforeStageWrite); err != nil {
		return err
	}
	if written, err := file.Write(raw); err != nil || written != len(raw) {
		return ErrFixedDeliveryUnknown
	}
	if err := callHook(fixedDeliveryPhaseAfterStageWrite); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return ErrFixedDeliveryUnknown
	}
	if err := callHook(fixedDeliveryPhaseAfterStageSync); err != nil {
		return err
	}
	staged, err := observeFixedDeliveryRecord(fd, fixedDeliveryMaxRecord)
	named, namedErr := observeFixedDeliveryRecordAt(int(directory.Fd()), stage, fixedDeliveryMaxRecord)
	if err != nil || namedErr != nil || staged != named || staged.Size != int64(len(raw)) || validateFixedServerRoot(root, 5) != nil {
		return ErrFixedDeliveryConflict
	}
	if err := callHook(fixedDeliveryPhaseBeforeRename); err != nil {
		return err
	}
	if err := unix.RenameatxNp(int(directory.Fd()), stage, int(directory.Fd()), leaf, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return adoptFixedDeliveryRecord(root, leaf, raw, hook)
		}
		return ErrFixedDeliveryUnknown
	}
	cleanup = false
	if err := callHook(fixedDeliveryPhaseAfterRename); err != nil {
		return ErrFixedDeliveryUnknown
	}
	current, err := observeFixedDeliveryRecordAt(int(directory.Fd()), leaf, fixedDeliveryMaxRecord)
	if err != nil || current != staged {
		return ErrFixedDeliveryUnknown
	}
	return adoptFixedDeliveryRecord(root, leaf, raw, hook)
}
