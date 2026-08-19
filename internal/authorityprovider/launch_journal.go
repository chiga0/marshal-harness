package authorityprovider

// This file implements the provider-owned, append-only launch fact journal.
// It is deliberately a small persistence seam: it does not sign receipts,
// provide an external monotonic anchor, or execute a child. Those authorities
// remain outside Marshal and are required before a profile can be enabled.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const launchJournalSchema = "marshal.agent-production-authority.launch-journal.v1"

type LaunchJournalSnapshot struct {
	Sequence         uint64
	ProviderSequence uint64
	ChainDigest      string
	Transactions     []LaunchTransaction
}

type launchJournalRecord struct {
	SchemaVersion    string            `json:"schemaVersion"`
	Sequence         uint64            `json:"sequence"`
	Event            string            `json:"event"`
	ProviderSequence uint64            `json:"providerSequence"`
	PreviousDigest   string            `json:"previousDigest"`
	Transaction      LaunchTransaction `json:"transaction"`
	RecordDigest     string            `json:"recordDigest"`
	RecordCRC32      uint32            `json:"recordCrc32"`
}

// LaunchJournal is the minimal persistence contract consumed by the reducer.
// Implementations must make Append durable before returning nil.
type LaunchJournal interface {
	Snapshot() (LaunchJournalSnapshot, error)
	Append(event string, transaction LaunchTransaction, providerSequence uint64) error
}

// DurableLaunchJournal stores only non-secret launch identities and digests.
// The file is private to the provider, O_APPEND and fsync'd; malformed or
// truncated history fails closed rather than being silently repaired.
type DurableLaunchJournal struct {
	mu           sync.Mutex
	file         *os.File
	path         string
	state        LaunchJournalSnapshot
	transactions map[string]LaunchTransaction
}

func OpenDurableLaunchJournal(path string) (*DurableLaunchJournal, error) {
	if err := validateJournalPath(path); err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	if err := validateJournalDirectory(parent); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open launch journal: %w", err)
	}
	if err := validateJournalFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	journal := &DurableLaunchJournal{file: file, path: path, transactions: map[string]LaunchTransaction{}}
	if err := journal.replayLocked(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return journal, nil
}

func (journal *DurableLaunchJournal) Close() error {
	if journal == nil || journal.file == nil {
		return nil
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.file.Close()
}

func (journal *DurableLaunchJournal) Snapshot() (LaunchJournalSnapshot, error) {
	if journal == nil {
		return LaunchJournalSnapshot{}, errors.New("launch journal is unavailable")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.snapshotLocked(), nil
}

func (journal *DurableLaunchJournal) Append(event string, transaction LaunchTransaction, providerSequence uint64) error {
	if journal == nil || journal.file == nil {
		return errors.New("launch journal is unavailable")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := validateJournalTransition(event, transaction, journal.transactions); err != nil {
		return err
	}
	if providerSequence < journal.state.ProviderSequence || (event == "committed" && providerSequence != journal.state.ProviderSequence+1) || event != "committed" && providerSequence != journal.state.ProviderSequence {
		return errors.New("launch journal provider sequence is invalid")
	}
	record := launchJournalRecord{
		SchemaVersion: launchJournalSchema, Sequence: journal.state.Sequence + 1,
		Event: event, ProviderSequence: providerSequence,
		PreviousDigest: journal.state.ChainDigest, Transaction: transaction,
	}
	digest, err := journal.recordDigest(record)
	if err != nil {
		return err
	}
	record.RecordDigest = digest
	record.RecordCRC32 = journal.recordCRC32(record)
	raw, err := canonical.JSON(journalMustJSON(record))
	if err != nil {
		return fmt.Errorf("canonical launch journal record: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := journal.file.Write(raw); err != nil {
		return fmt.Errorf("append launch journal: %w", err)
	}
	if err := journal.file.Sync(); err != nil {
		return fmt.Errorf("sync launch journal: %w", err)
	}
	journal.applyRecordLocked(record)
	return nil
}

func (journal *DurableLaunchJournal) replayLocked() error {
	if _, err := journal.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek launch journal: %w", err)
	}
	info, err := journal.file.Stat()
	if err != nil {
		return fmt.Errorf("stat launch journal: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	reader := bufio.NewReader(io.LimitReader(journal.file, 16<<20))
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil {
			return errors.New("launch journal has incomplete final record")
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if len(line) == 0 || !bytes.Equal(line, mustCanonical(line)) {
			return errors.New("launch journal record is not canonical")
		}
		var record launchJournalRecord
		if err := decodeClosedJournal(line, &record); err != nil {
			return fmt.Errorf("decode launch journal: %w", err)
		}
		if record.SchemaVersion != launchJournalSchema || record.Sequence != journal.state.Sequence+1 || record.PreviousDigest != journal.state.ChainDigest {
			return errors.New("launch journal sequence or chain mismatch")
		}
		if record.ProviderSequence < journal.state.ProviderSequence || (record.Event == "committed" && record.ProviderSequence != journal.state.ProviderSequence+1) || record.Event != "committed" && record.ProviderSequence != journal.state.ProviderSequence {
			return errors.New("launch journal provider sequence is invalid")
		}
		if got, err := journal.recordDigest(record); err != nil || got != record.RecordDigest || journal.recordCRC32(record) != record.RecordCRC32 {
			return errors.New("launch journal record digest mismatch")
		}
		if err := validateJournalTransition(record.Event, record.Transaction, journal.transactions); err != nil {
			return err
		}
		journal.applyRecordLocked(record)
	}
	if _, err := journal.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek launch journal end: %w", err)
	}
	return nil
}

func (journal *DurableLaunchJournal) recordDigest(record launchJournalRecord) (string, error) {
	record.RecordDigest = ""
	record.RecordCRC32 = 0
	raw, err := canonical.JSON(journalMustJSON(record))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (journal *DurableLaunchJournal) recordCRC32(record launchJournalRecord) uint32 {
	record.RecordCRC32 = 0
	raw, _ := canonical.JSON(journalMustJSON(record))
	return crc32.ChecksumIEEE(raw)
}

func (journal *DurableLaunchJournal) applyRecordLocked(record launchJournalRecord) {
	journal.state.Sequence = record.Sequence
	journal.state.ProviderSequence = record.ProviderSequence
	journal.state.ChainDigest = record.RecordDigest
	key := launchAttemptKey(record.Transaction.AttemptID, record.Transaction.LaunchNonce)
	journal.transactions[key] = record.Transaction
}

func (journal *DurableLaunchJournal) snapshotLocked() LaunchJournalSnapshot {
	transactions := make([]LaunchTransaction, 0, len(journal.transactions))
	for _, transaction := range journal.transactions {
		transactions = append(transactions, transaction)
	}
	return LaunchJournalSnapshot{Sequence: journal.state.Sequence, ProviderSequence: journal.state.ProviderSequence, ChainDigest: journal.state.ChainDigest, Transactions: transactions}
}

func validateJournalTransition(event string, transaction LaunchTransaction, prior map[string]LaunchTransaction) error {
	if event != "prepared" && event != "committed" && event != "aborted" && event != "exited" {
		return errors.New("launch journal event is unknown")
	}
	if !validID(transaction.LaunchTransactionID) || !validID(transaction.AttemptID) || !validNonce(transaction.LaunchNonce) {
		return errors.New("launch journal transaction identity is invalid")
	}
	key := launchAttemptKey(transaction.AttemptID, transaction.LaunchNonce)
	previous, exists := prior[key]
	switch event {
	case "prepared":
		if exists || transaction.Status != LaunchPending {
			return errors.New("launch journal prepare transition is invalid")
		}
	case "committed":
		if !exists || previous.Status != LaunchPending || transaction.Status != LaunchReleased || previous.LaunchTransactionID != transaction.LaunchTransactionID {
			return errors.New("launch journal commit transition is invalid")
		}
	case "aborted", "exited":
		if !exists || previous.Status != LaunchPending || (transaction.Status != LaunchAborted && transaction.Status != LaunchExited) || previous.LaunchTransactionID != transaction.LaunchTransactionID {
			return errors.New("launch journal abort transition is invalid")
		}
	}
	return nil
}

func validateJournalPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasSuffix(path, string(filepath.Separator)) {
		return errors.New("launch journal path must be an absolute clean file path")
	}
	return nil
}

func validateJournalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("launch journal directory must be a private real directory")
	}
	if runtime.GOOS != "windows" && info.Sys() != nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && uint32(stat.Uid) != uint32(os.Getuid()) && uint32(stat.Uid) != 0 {
			return errors.New("launch journal directory owner is not trusted")
		}
	}
	return nil
}

func validateJournalFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || info.Mode()&0o077 != 0 || !info.Mode().IsRegular() {
		return errors.New("launch journal must be a private regular file")
	}
	return nil
}

func decodeClosedJournal(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("launch journal record has trailing data")
	}
	return nil
}

func journalMustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func mustCanonical(raw []byte) []byte {
	canonicalRaw, _ := canonical.JSON(raw)
	return canonicalRaw
}
