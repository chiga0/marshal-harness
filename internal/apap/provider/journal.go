package provider

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const (
	journalName      = "provider.journal"
	failClosedName   = "provider.fail-closed"
	maxJournalRecord = 64 << 10
)

var (
	ErrConflict          = errors.New("apap provider: conflict")
	ErrInvalid           = errors.New("apap provider: invalid")
	ErrUnavailable       = errors.New("apap provider: unavailable")
	ErrReconcileRequired = errors.New("apap provider: reconcile required")
	ErrAlreadyOpen       = errors.New("apap provider: journal already open")
)

type journalRecord struct {
	Sequence             uint64          `json:"sequence"`
	Kind                 string          `json:"kind"`
	TransactionID        string          `json:"transactionId"`
	Payload              json.RawMessage `json:"payload"`
	PreviousRecordDigest string          `json:"previousRecordDigest"`
	RecordDigest         string          `json:"recordDigest"`
	CRC32C               uint32          `json:"crc32c"`
}

type Journal struct {
	mu         sync.Mutex
	file       *os.File
	dir        *os.File
	records    []journalRecord
	closed     bool
	failClosed bool
	fail       func(kind string) error
}

type AuthorityRootIdentity struct {
	Device uint64
	Inode  uint64
	UID    uint32
	Mode   uint32
}

func MeasureAuthorityRoot(directory string) (AuthorityRootIdentity, error) {
	fd, err := openAuthorityRoot(directory)
	if err != nil {
		return AuthorityRootIdentity{}, err
	}
	defer unix.Close(fd)
	return authorityRootIdentity(fd)
}

func OpenJournal(directory string, expected AuthorityRootIdentity) (*Journal, error) {
	dirfd, err := openAuthorityRoot(directory)
	if err != nil {
		return nil, ErrUnavailable
	}
	actual, identityErr := authorityRootIdentity(dirfd)
	if identityErr != nil || actual != expected {
		unix.Close(dirfd)
		return nil, ErrUnavailable
	}
	dir := os.NewFile(uintptr(dirfd), "provider-authority-root")
	if dir == nil {
		unix.Close(dirfd)
		return nil, ErrUnavailable
	}
	dirOwned := true
	defer func() {
		if dirOwned {
			_ = dir.Close()
		}
	}()
	fd, err := unix.Openat(dirfd, journalName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), journalName)
	if file == nil {
		unix.Close(fd)
		return nil, ErrUnavailable
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
		}
	}()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 || (st.Uid != uint32(os.Geteuid()) && st.Uid != 0) {
		return nil, ErrUnavailable
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, ErrAlreadyOpen
	}
	if err := unix.Fsync(dirfd); err != nil {
		return nil, ErrUnavailable
	}
	var marker unix.Stat_t
	markerErr := unix.Fstatat(dirfd, failClosedName, &marker, unix.AT_SYMLINK_NOFOLLOW)
	if markerErr != nil && !errors.Is(markerErr, unix.ENOENT) {
		return nil, ErrUnavailable
	}
	markerExists := markerErr == nil
	if markerExists && (marker.Mode&unix.S_IFMT != unix.S_IFREG || marker.Mode&0777 != 0600 || marker.Nlink != 1 || (marker.Uid != uint32(os.Geteuid()) && marker.Uid != 0)) {
		return nil, ErrUnavailable
	}
	j := &Journal{file: file, dir: dir, failClosed: markerExists}
	if err := j.load(); err != nil {
		return nil, err
	}
	failed = false
	dirOwned = false
	return j, nil
}

func authorityRootIdentity(fd int) (AuthorityRootIdentity, error) {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return AuthorityRootIdentity{}, ErrUnavailable
	}
	return AuthorityRootIdentity{Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Mode: uint32(st.Mode)}, nil
}

func openAuthorityRoot(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, ErrUnavailable
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnavailable
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(current)
		if openErr != nil {
			return -1, ErrUnavailable
		}
		current = next
	}
	var st unix.Stat_t
	if unix.Fstat(current, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0022 != 0 || (st.Uid != uint32(os.Geteuid()) && st.Uid != 0) {
		unix.Close(current)
		return -1, ErrUnavailable
	}
	return current, nil
}

func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	_ = unix.Flock(int(j.file.Fd()), unix.LOCK_UN)
	fileErr := j.file.Close()
	dirErr := j.dir.Close()
	if fileErr != nil {
		return fileErr
	}
	return dirErr
}

func (j *Journal) markFailClosed() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrUnavailable
	}
	if j.failClosed {
		return nil
	}
	fd, err := unix.Openat(int(j.dir.Fd()), failClosedName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if errors.Is(err, unix.EEXIST) {
		j.failClosed = true
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	marker := os.NewFile(uintptr(fd), failClosedName)
	if marker == nil {
		unix.Close(fd)
		return ErrUnavailable
	}
	if err := writeAll(marker, []byte("fail-closed\n")); err != nil {
		_ = marker.Close()
		return ErrUnavailable
	}
	if err := marker.Sync(); err != nil {
		_ = marker.Close()
		return ErrUnavailable
	}
	if err := marker.Close(); err != nil {
		return ErrUnavailable
	}
	if err := unix.Fsync(int(j.dir.Fd())); err != nil {
		return ErrUnavailable
	}
	j.failClosed = true
	return nil
}

func (j *Journal) snapshotRecords() []journalRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]journalRecord, len(j.records))
	copy(result, j.records)
	for i := range result {
		result[i].Payload = append(json.RawMessage(nil), result[i].Payload...)
	}
	return result
}

func (j *Journal) appendRecord(kind, transactionID string, payload any) (journalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || !validID(kind) || !validID(transactionID) || payload == nil {
		return journalRecord{}, ErrInvalid
	}
	if j.fail != nil {
		if err := j.fail(kind); err != nil {
			return journalRecord{}, ErrUnavailable
		}
	}
	payloadBytes, err := canonicalValue(payload)
	if err != nil {
		return journalRecord{}, ErrInvalid
	}
	previous := ""
	if len(j.records) != 0 {
		previous = j.records[len(j.records)-1].RecordDigest
	}
	record := journalRecord{Sequence: uint64(len(j.records) + 1), Kind: kind, TransactionID: transactionID, Payload: payloadBytes, PreviousRecordDigest: previous}
	record.RecordDigest, err = journalRecordDigest(record)
	if err != nil {
		return journalRecord{}, ErrInvalid
	}
	withoutCRC, err := canonicalValue(recordWithoutCRC(record))
	if err != nil {
		return journalRecord{}, ErrInvalid
	}
	record.CRC32C = crc32.Checksum(withoutCRC, crc32.MakeTable(crc32.Castagnoli))
	encoded, err := canonicalValue(record)
	if err != nil || len(encoded) > maxJournalRecord {
		return journalRecord{}, ErrInvalid
	}
	frame := make([]byte, 4+len(encoded))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
	copy(frame[4:], encoded)
	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return journalRecord{}, ErrUnavailable
	}
	if err := writeAll(j.file, frame); err != nil || j.file.Sync() != nil {
		return journalRecord{}, ErrUnavailable
	}
	j.records = append(j.records, record)
	return record, nil
}

func (j *Journal) load() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return ErrUnavailable
	}
	var offset int64
	var previous string
	for {
		var header [4]byte
		n, err := io.ReadFull(j.file, header[:])
		if errors.Is(err, io.EOF) && n == 0 {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return j.truncatePartial(offset)
		}
		if err != nil {
			return ErrUnavailable
		}
		length := binary.BigEndian.Uint32(header[:])
		if length == 0 || length > maxJournalRecord {
			return ErrUnavailable
		}
		encoded := make([]byte, length)
		if _, err := io.ReadFull(j.file, encoded); errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return j.truncatePartial(offset)
		} else if err != nil {
			return ErrUnavailable
		}
		record, err := decodeJournalRecord(encoded)
		if err != nil || record.Sequence != uint64(len(j.records)+1) || record.PreviousRecordDigest != previous {
			return ErrUnavailable
		}
		j.records = append(j.records, record)
		previous = record.RecordDigest
		offset += int64(4 + length)
	}
	return nil
}

func (j *Journal) truncatePartial(offset int64) error {
	if err := j.file.Truncate(offset); err != nil {
		return ErrUnavailable
	}
	if err := j.file.Sync(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func decodeJournalRecord(encoded []byte) (journalRecord, error) {
	var record journalRecord
	canonicalEncoded, err := canonical.JSON(encoded)
	if err != nil || !bytes.Equal(encoded, canonicalEncoded) {
		return record, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, ErrUnavailable
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return record, ErrUnavailable
	}
	digest, err := journalRecordDigest(record)
	withoutCRC, crcErr := canonicalValue(recordWithoutCRC(record))
	if err != nil || crcErr != nil || record.RecordDigest != digest || crc32.Checksum(withoutCRC, crc32.MakeTable(crc32.Castagnoli)) != record.CRC32C {
		return record, ErrUnavailable
	}
	return record, nil
}

func journalRecordDigest(record journalRecord) (string, error) {
	record.RecordDigest, record.CRC32C = "", 0
	encoded, err := canonicalValue(recordWithoutDigestAndCRC(record))
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(encoded), nil
}

func recordWithoutCRC(record journalRecord) any {
	return struct {
		Sequence             uint64          `json:"sequence"`
		Kind                 string          `json:"kind"`
		TransactionID        string          `json:"transactionId"`
		Payload              json.RawMessage `json:"payload"`
		PreviousRecordDigest string          `json:"previousRecordDigest"`
		RecordDigest         string          `json:"recordDigest"`
	}{record.Sequence, record.Kind, record.TransactionID, record.Payload, record.PreviousRecordDigest, record.RecordDigest}
}

func recordWithoutDigestAndCRC(record journalRecord) any {
	return struct {
		Sequence             uint64          `json:"sequence"`
		Kind                 string          `json:"kind"`
		TransactionID        string          `json:"transactionId"`
		Payload              json.RawMessage `json:"payload"`
		PreviousRecordDigest string          `json:"previousRecordDigest"`
	}{record.Sequence, record.Kind, record.TransactionID, record.Payload, record.PreviousRecordDigest}
}

func canonicalValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(raw)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
