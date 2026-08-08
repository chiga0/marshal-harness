package cleanup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// Archive kinds and limits. Archive records are local evidence metadata, not
// contract-schema documents: they gate cleanup decisions, so they are read
// fail-closed and never followed through symlinks.
const (
	archivePatchKind = "worktree-patch"
	archiveRunKind   = "run-evidence"

	maxArchiveRecordBytes int64 = 1 << 20
	maxArchivePatchBytes  int64 = 64 << 20

	// DefaultRetentionDays applies when a terminal Run has no readable
	// PolicySnapshot or none declares a positive retentionDays.
	DefaultRetentionDays = 30
)

// ErrArchiveIdentity marks unreadable, malformed or unbound archive evidence.
var ErrArchiveIdentity = errors.New("archive record identity is not provable")

// ArchiveRecord is the durable evidence binding one Run to one archive file.
// A worktree-patch record is the sole gate that allows removing a dirty
// managed worktree; a run-evidence record marks an expired Run whose run
// directory was preserved as a tar before removal.
type ArchiveRecord struct {
	RunID       string    `json:"runId"`
	TaskID      string    `json:"taskId"`
	Kind        string    `json:"kind"`
	ArchivePath string    `json:"archivePath"`
	Digest      string    `json:"digest"`
	ExportedAt  time.Time `json:"exportedAt"`
	Actor       string    `json:"actor"`
}

func archiveDirectory(stateRoot string) string {
	return filepath.Join(stateRoot, "archive")
}

func archivePath(stateRoot, runID, kind string) string {
	if kind == archiveRunKind {
		return filepath.Join(archiveDirectory(stateRoot), runID+".tar.gz")
	}
	return filepath.Join(archiveDirectory(stateRoot), runID+".patch")
}

func archiveRecordPath(stateRoot, runID string) string {
	return filepath.Join(archiveDirectory(stateRoot), runID+".json")
}

func prepareArchiveDirectory(stateRoot string) (string, error) {
	directory := archiveDirectory(stateRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrArchiveIdentity
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	return directory, nil
}

// readArchiveRecord returns the archive record of a Run only when it exists,
// is a regular owner-readable file, and binds exactly to the Run and kind.
// Any other outcome fails closed.
func readArchiveRecord(stateRoot, runID, kind string) (ArchiveRecord, error) {
	if domain.ValidateID(runID) != nil {
		return ArchiveRecord{}, ErrArchiveIdentity
	}
	path := archiveRecordPath(stateRoot, runID)
	info, err := os.Lstat(path)
	if err != nil {
		return ArchiveRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxArchiveRecordBytes {
		return ArchiveRecord{}, ErrArchiveIdentity
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ArchiveRecord{}, err
	}
	var record ArchiveRecord
	if json.Unmarshal(data, &record) != nil {
		return ArchiveRecord{}, ErrArchiveIdentity
	}
	if record.RunID != runID || record.Kind != kind || strings.TrimSpace(record.TaskID) == "" ||
		strings.TrimSpace(record.Actor) == "" || record.ExportedAt.IsZero() ||
		record.ArchivePath != archivePath(stateRoot, runID, kind) || !validArchiveDigest(record.Digest) {
		return ArchiveRecord{}, ErrArchiveIdentity
	}
	return record, nil
}

func validArchiveDigest(digest string) bool {
	encoded, found := strings.CutPrefix(digest, "sha256:")
	if !found || len(encoded) != 64 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

// writePatchArchive persists the exported dirty-worktree patch and then the
// record that gates later removal: the record appears only after the patch is
// durably in place, so a crash in between never unlocks deletion.
func writePatchArchive(stateRoot string, record ArchiveRecord, patch []byte) (string, error) {
	if domain.ValidateID(record.RunID) != nil || int64(len(patch)) == 0 || int64(len(patch)) > maxArchivePatchBytes {
		return "", ErrArchiveIdentity
	}
	directory, err := prepareArchiveDirectory(stateRoot)
	if err != nil {
		return "", err
	}
	patchPath := filepath.Join(directory, record.RunID+".patch")
	if err := writeArchiveFile(patchPath, patch); err != nil {
		return "", err
	}
	record.ArchivePath = patchPath
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeArchiveFile(archiveRecordPath(stateRoot, record.RunID), append(data, '\n')); err != nil {
		return "", err
	}
	return patchPath, nil
}

// writeArchiveFile atomically writes one owner-only regular file, refusing to
// replace anything that is not already a regular file.
func writeArchiveFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return ErrArchiveIdentity
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".marshal-archive-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr == nil {
		writeErr = syncErr
	}
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	err = handle.Sync()
	_ = handle.Close()
	return err
}

// writeRunArchiveTar tars an entire run directory into owner-only
// .marshal/archive/<run-id>.tar.gz and proves the archive readable end to end
// before it can authorize any deletion. On any failure the partial archive is
// discarded and the run directory remains untouched.
func writeRunArchiveTar(stateRoot, runID, runDir string) (string, error) {
	if domain.ValidateID(runID) != nil {
		return "", ErrArchiveIdentity
	}
	directory, err := prepareArchiveDirectory(stateRoot)
	if err != nil {
		return "", err
	}
	finalPath := filepath.Join(directory, runID+".tar.gz")
	if info, statErr := os.Lstat(finalPath); statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", ErrArchiveIdentity
	}
	temporary, err := os.CreateTemp(directory, ".marshal-archive-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	writeErr := buildRunTar(temporary, runDir)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr == nil {
		writeErr = syncErr
	}
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", fmt.Errorf("archive run directory: %w", writeErr)
	}
	if err := verifyArchiveTar(temporaryName); err != nil {
		return "", fmt.Errorf("verify run archive: %w", err)
	}
	if err := os.Rename(temporaryName, finalPath); err != nil {
		return "", err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	err = handle.Sync()
	_ = handle.Close()
	return finalPath, err
}

func buildRunTar(output io.Writer, runDir string) error {
	gz := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gz)
	entries := 0
	walkErr := filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe run directory entry %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}
		if mode&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header.Linkname = linkTarget
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		entries++
		if !mode.IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tarWriter, file)
		return err
	})
	if walkErr != nil {
		return walkErr
	}
	if entries == 0 {
		return errors.New("run directory produced an empty archive")
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// verifyArchiveTar re-reads the whole archive through gzip and tar, proving it
// is complete and carries only safe relative entry names. Anything else
// refuses the deletion the archive would authorize.
func verifyArchiveTar(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(header.Name))
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "/") ||
			strings.HasPrefix(name, "../") || name == ".." {
			return fmt.Errorf("unsafe tar entry %q", header.Name)
		}
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			return err
		}
		entries++
	}
	if entries == 0 {
		return errors.New("archive carries no entries")
	}
	return nil
}

func digestArchiveFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrArchiveIdentity
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

// retentionDaysFor reads the frozen PolicySnapshot of one run; any missing,
// unreadable or non-positive retention falls back to DefaultRetentionDays.
func retentionDaysFor(runDir string) int {
	path := filepath.Join(runDir, "policy-snapshot.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxArchiveRecordBytes {
		return DefaultRetentionDays
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultRetentionDays
	}
	var snapshot struct {
		Effective struct {
			RetentionDays int `json:"retentionDays"`
		} `json:"effective"`
	}
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Effective.RetentionDays <= 0 {
		return DefaultRetentionDays
	}
	return snapshot.Effective.RetentionDays
}
