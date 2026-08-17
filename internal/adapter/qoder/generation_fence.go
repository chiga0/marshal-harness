package qoder

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const (
	authorityGenerationFenceKind = "qoder-authority-generation-fence-v1"
	authorityGenerationFenceName = "generation.json"
	authorityGenerationLockName  = "generation.lock"
	authorityGenerationLimit     = 4 << 10
)

// authorityGenerationFenceRecord is consumer-owned durable state. It stores
// no credential, private key, authority path, or evidence body.
type authorityGenerationFenceRecord struct {
	Kind                  string `json:"kind"`
	AuthorityGeneration   uint64 `json:"authorityGeneration"`
	AuthorityConfigDigest string `json:"authorityConfigDigest"`
}

func authorityConfigIdentity(config AuthorityConfig) (string, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	data, err = canonical.JSON(data)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// consumeAuthorityGeneration serializes all consumers through an owner-only
// lock in a pre-existing, consumer-owned directory. A newer generation is
// committed before its evidence leaf is resolved; restart and another process
// therefore cannot revive an older config after a missing/revoked leaf.
func consumeAuthorityGeneration(root string, generation uint64, configDigest string) error {
	if generation == 0 || !validSHA256Digest(configDigest) {
		return errors.New("qoder authority generation fence input is invalid")
	}
	directory, stat, err := openNoSymlinkPath(root, true)
	if err != nil || !privateDirectory(stat, os.Geteuid()) {
		if directory != nil {
			_ = directory.Close()
		}
		return errors.New("qoder authority generation fence root is not a private real directory")
	}
	defer directory.Close()
	dirFD := int(directory.Fd())

	lockFD, err := openAuthorityGenerationLock(dirFD)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(lockFD), authorityGenerationLockName)
	defer lock.Close()
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("qoder authority generation fence is busy")
	}
	defer func() { _ = unix.Flock(lockFD, unix.LOCK_UN) }()

	current, exists, err := readAuthorityGenerationFence(dirFD)
	if err != nil {
		return err
	}
	if exists {
		if generation < current.AuthorityGeneration {
			return errors.New("qoder authority generation rollback rejected")
		}
		if generation == current.AuthorityGeneration {
			if configDigest != current.AuthorityConfigDigest {
				return errors.New("qoder authority same-generation identity replacement rejected")
			}
			return nil
		}
	}
	record := authorityGenerationFenceRecord{
		Kind:                  authorityGenerationFenceKind,
		AuthorityGeneration:   generation,
		AuthorityConfigDigest: configDigest,
	}
	return writeAuthorityGenerationFence(dirFD, record)
}

func openAuthorityGenerationLock(dirFD int) (int, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(dirFD, authorityGenerationLockName, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(dirFD, authorityGenerationLockName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(dirFD, authorityGenerationLockName, flags, 0)
		}
	}
	if err != nil {
		return -1, errors.New("open qoder authority generation fence lock")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !privateSingleLinkRegularFile(stat, os.Geteuid()) {
		_ = unix.Close(fd)
		return -1, errors.New("qoder authority generation fence lock is not a private regular file")
	}
	return fd, nil
}

func readAuthorityGenerationFence(dirFD int) (authorityGenerationFenceRecord, bool, error) {
	fd, err := unix.Openat(dirFD, authorityGenerationFenceName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return authorityGenerationFenceRecord{}, false, nil
	}
	if err != nil {
		return authorityGenerationFenceRecord{}, false, errors.New("open qoder authority generation fence")
	}
	file := os.NewFile(uintptr(fd), authorityGenerationFenceName)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !privateSingleLinkRegularFile(stat, os.Geteuid()) {
		return authorityGenerationFenceRecord{}, false, errors.New("qoder authority generation fence is not a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, authorityGenerationLimit+1))
	if err != nil || len(data) > authorityGenerationLimit {
		return authorityGenerationFenceRecord{}, false, errors.New("qoder authority generation fence is unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record authorityGenerationFenceRecord
	if err := decoder.Decode(&record); err != nil {
		return authorityGenerationFenceRecord{}, false, errors.New("qoder authority generation fence is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return authorityGenerationFenceRecord{}, false, errors.New("qoder authority generation fence is invalid")
	}
	if record.Kind != authorityGenerationFenceKind || record.AuthorityGeneration == 0 || !validSHA256Digest(record.AuthorityConfigDigest) {
		return authorityGenerationFenceRecord{}, false, errors.New("qoder authority generation fence is invalid")
	}
	return record, true, nil
}

func writeAuthorityGenerationFence(dirFD int, record authorityGenerationFenceRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	temporary := ".generation-" + hex.EncodeToString(token[:]) + ".tmp"
	fd, err := unix.Openat(dirFD, temporary, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return errors.New("stage qoder authority generation fence")
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(dirFD, temporary, 0)
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write qoder authority generation fence: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync qoder authority generation fence: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close qoder authority generation fence: %w", err)
	}
	if err := unix.Renameat(dirFD, temporary, dirFD, authorityGenerationFenceName); err != nil {
		return errors.New("commit qoder authority generation fence")
	}
	if err := unix.Fsync(dirFD); err != nil {
		return errors.New("sync qoder authority generation fence directory")
	}
	return nil
}

func privateSingleLinkRegularFile(stat unix.Stat_t, effectiveUID int) bool {
	return privateRegularFile(stat, effectiveUID) && stat.Nlink == 1
}
