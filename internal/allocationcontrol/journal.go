package allocationcontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	JournalSchema       = "AllocationRecoveryJournalV1"
	JournalFileName     = "allocation-recovery-v1.journal"
	journalFrameMax     = 1 << 20
	journalFileMax      = 64 << 20
	journalHeaderLength = 9
)

var JournalGenesisDigest = canonical.DigestBytes([]byte("marshal/allocation-recovery-journal/v1/genesis"))

type RecordKind string

const (
	RecordProvisionIntent   RecordKind = "provision-intent"
	RecordProvisionPrepared RecordKind = "provision-staging-prepared"
	RecordProvisionReceipt  RecordKind = "provision-receipt"
	RecordTerminateIntent   RecordKind = "terminate-intent"
	RecordTerminateReceipt  RecordKind = "terminate-receipt"
)

// CommittedAuthorityFact is an authenticated fact supplied by the stage-2
// Attempt authority integration. AllocationRecoveryJournalV1 only projects
// these facts; it never manufactures one or treats its own bytes as authority.
type CommittedAuthorityFact struct {
	RecordKind                 RecordKind          `json:"recordKind"`
	RecordID                   string              `json:"recordId"`
	RecordedAt                 string              `json:"recordedAt"`
	Binding                    AllocationBindingV1 `json:"binding"`
	ExpectedAttemptSequence    uint64              `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string              `json:"attemptAuthorityFactDigest"`
	RequestDigest              string              `json:"requestDigest"`
	TerminalizationID          string              `json:"terminalizationId,omitempty"`
	CleanupBindingDigest       string              `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalFactDigest  string              `json:"processTerminalFactDigest,omitempty"`
	AuthorityFact              json.RawMessage     `json:"authorityFact"`
}

func (fact CommittedAuthorityFact) Validate() error {
	if !validRecordKind(fact.RecordKind) || !validText(fact.RecordID) || fact.Binding.Validate() != nil || fact.ExpectedAttemptSequence == 0 || fact.ExpectedAttemptSequence > maxSafeJSONInteger || !validDigest(fact.AttemptAuthorityFactDigest) || !validDigest(fact.RequestDigest) {
		return ErrInvalid
	}
	parsed, err := time.Parse(time.RFC3339Nano, fact.RecordedAt)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != fact.RecordedAt {
		return ErrInvalid
	}
	canonicalFact, err := canonical.JSON(fact.AuthorityFact)
	if err != nil || !bytes.Equal(canonicalFact, fact.AuthorityFact) || len(canonicalFact) == 0 || len(canonicalFact) > journalFrameMax/2 {
		return ErrInvalid
	}
	termination := fact.RecordKind == RecordTerminateIntent || fact.RecordKind == RecordTerminateReceipt
	if termination {
		if !validText(fact.TerminalizationID) || !validDigest(fact.CleanupBindingDigest) || !validDigest(fact.ProcessTerminalFactDigest) {
			return ErrInvalid
		}
	} else if fact.TerminalizationID != "" || fact.CleanupBindingDigest != "" || fact.ProcessTerminalFactDigest != "" {
		return ErrInvalid
	}
	return nil
}

// JournalRecord is the exact append-only projection frame. AuthorityFact is
// retained verbatim in canonical form while AuthorityFactPayloadDigest makes
// corruption of the projected bytes independently detectable.
type JournalRecord struct {
	SchemaVersion              string          `json:"schemaVersion"`
	JournalSequence            uint64          `json:"journalSequence"`
	RecordKind                 RecordKind      `json:"recordKind"`
	RecordID                   string          `json:"recordId"`
	RecordedAt                 string          `json:"recordedAt"`
	AuthorityNamespaceID       string          `json:"authorityNamespaceId"`
	TaskID                     string          `json:"taskId"`
	RunID                      string          `json:"runId"`
	AttemptID                  string          `json:"attemptId"`
	AllocationID               string          `json:"allocationId"`
	LeaseID                    string          `json:"leaseId"`
	Generation                 int64           `json:"generation"`
	FencingTokenDigest         string          `json:"fencingTokenDigest"`
	CommandID                  string          `json:"commandId"`
	IdempotencyKey             string          `json:"idempotencyKey"`
	ExpectedAttemptSequence    uint64          `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string          `json:"attemptAuthorityFactDigest"`
	TerminalizationID          string          `json:"terminalizationId,omitempty"`
	CleanupBindingDigest       string          `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalFactDigest  string          `json:"processTerminalFactDigest,omitempty"`
	RequestDigest              string          `json:"requestDigest"`
	AuthorityFact              json.RawMessage `json:"authorityFact"`
	AuthorityFactPayloadDigest string          `json:"authorityFactPayloadDigest"`
	PreviousRecordDigest       string          `json:"previousRecordDigest"`
	RecordDigest               string          `json:"recordDigest"`
}

func journalRecordForFact(sequence uint64, previous string, fact CommittedAuthorityFact) (JournalRecord, error) {
	if sequence == 0 || !validDigest(previous) || fact.Validate() != nil {
		return JournalRecord{}, ErrInvalid
	}
	record := JournalRecord{
		SchemaVersion: JournalSchema, JournalSequence: sequence, RecordKind: fact.RecordKind,
		RecordID: fact.RecordID, RecordedAt: fact.RecordedAt,
		AuthorityNamespaceID: fact.Binding.AuthorityNamespaceID, TaskID: fact.Binding.TaskID,
		RunID: fact.Binding.RunID, AttemptID: fact.Binding.AttemptID, AllocationID: fact.Binding.AllocationID,
		LeaseID: fact.Binding.LeaseID, Generation: fact.Binding.Generation,
		FencingTokenDigest: fact.Binding.FencingTokenDigest, CommandID: fact.Binding.CommandID,
		IdempotencyKey: fact.Binding.IdempotencyKey, ExpectedAttemptSequence: fact.ExpectedAttemptSequence,
		AttemptAuthorityFactDigest: fact.AttemptAuthorityFactDigest,
		TerminalizationID:          fact.TerminalizationID, CleanupBindingDigest: fact.CleanupBindingDigest,
		ProcessTerminalFactDigest: fact.ProcessTerminalFactDigest, RequestDigest: fact.RequestDigest,
		AuthorityFact:              append(json.RawMessage(nil), fact.AuthorityFact...),
		AuthorityFactPayloadDigest: canonical.DigestBytes(fact.AuthorityFact), PreviousRecordDigest: previous,
	}
	digest, err := record.digest()
	if err != nil {
		return JournalRecord{}, err
	}
	record.RecordDigest = digest
	if err := record.Validate(); err != nil {
		return JournalRecord{}, err
	}
	return record, nil
}

func (record JournalRecord) Validate() error {
	if record.SchemaVersion != JournalSchema || record.JournalSequence == 0 || record.JournalSequence > maxSafeJSONInteger || !validRecordKind(record.RecordKind) || !validText(record.RecordID) || !validText(record.RecordedAt) || !validDigest(record.AttemptAuthorityFactDigest) || !validDigest(record.RequestDigest) || !validDigest(record.AuthorityFactPayloadDigest) || !validDigest(record.PreviousRecordDigest) || !validDigest(record.RecordDigest) {
		return ErrInvalid
	}
	binding := AllocationBindingV1{
		AuthorityNamespaceID: record.AuthorityNamespaceID, TaskID: record.TaskID, RunID: record.RunID,
		AttemptID: record.AttemptID, AllocationID: record.AllocationID, LeaseID: record.LeaseID,
		Generation: record.Generation, FencingTokenDigest: record.FencingTokenDigest,
		CommandID: record.CommandID, IdempotencyKey: record.IdempotencyKey,
	}
	fact := CommittedAuthorityFact{
		RecordKind: record.RecordKind, RecordID: record.RecordID, RecordedAt: record.RecordedAt,
		Binding: binding, ExpectedAttemptSequence: record.ExpectedAttemptSequence,
		AttemptAuthorityFactDigest: record.AttemptAuthorityFactDigest, RequestDigest: record.RequestDigest,
		TerminalizationID: record.TerminalizationID, CleanupBindingDigest: record.CleanupBindingDigest,
		ProcessTerminalFactDigest: record.ProcessTerminalFactDigest, AuthorityFact: record.AuthorityFact,
	}
	if fact.Validate() != nil || canonical.DigestBytes(record.AuthorityFact) != record.AuthorityFactPayloadDigest {
		return ErrInvalid
	}
	want, err := record.digest()
	if err != nil || want != record.RecordDigest {
		return ErrInvalid
	}
	return nil
}

func (record JournalRecord) digest() (string, error) {
	return digestValueWithoutField(record, "recordDigest")
}

func (record JournalRecord) canonical() ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return canonicalValue(record)
}

func validRecordKind(kind RecordKind) bool {
	switch kind {
	case RecordProvisionIntent, RecordProvisionPrepared, RecordProvisionReceipt, RecordTerminateIntent, RecordTerminateReceipt:
		return true
	default:
		return false
	}
}

// RecoveryJournal owns one already-bound owner-only regular file. It accepts
// only a complete ordered authority fact list and requires its current bytes
// to be an exact prefix before appending missing projections.
type RecoveryJournal struct {
	mu      sync.Mutex
	file    *os.File
	records []JournalRecord
}

func newRecoveryJournal(file *os.File) (*RecoveryJournal, error) {
	if file == nil {
		return nil, ErrInvalid
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() < 0 || stat.Size() > journalFileMax {
		return nil, ErrJournalCorrupt
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, stat.Size()))
	if err != nil {
		return nil, err
	}
	records, truncateAt, partial, err := parseJournalFrames(data)
	if err != nil {
		return nil, err
	}
	if partial {
		if err := file.Truncate(int64(truncateAt)); err != nil {
			return nil, err
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
	}
	return &RecoveryJournal{file: file, records: records}, nil
}

func (journal *RecoveryJournal) Close() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.file == nil {
		return nil
	}
	err := journal.file.Close()
	journal.file = nil
	return err
}

func (journal *RecoveryJournal) Records() []JournalRecord {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	records := append([]JournalRecord(nil), journal.records...)
	for index := range records {
		records[index].AuthorityFact = append(json.RawMessage(nil), records[index].AuthorityFact...)
	}
	return records
}

// SyncAuthorityProjection rebuilds a missing/lagging suffix from authority
// facts. A journal that is ahead, divergent or corrupt never repairs authority
// and is rejected without truncation.
func (journal *RecoveryJournal) SyncAuthorityProjection(facts []CommittedAuthorityFact) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.file == nil {
		return ErrInvalid
	}
	if err := validateFactSequence(facts); err != nil {
		return err
	}
	if len(journal.records) > len(facts) {
		return ErrAuthorityConflict
	}
	previous := JournalGenesisDigest
	for index, fact := range facts {
		expected, err := journalRecordForFact(uint64(index+1), previous, fact)
		if err != nil {
			return err
		}
		if index < len(journal.records) {
			if !equalCanonical(journal.records[index], expected) {
				return ErrAuthorityConflict
			}
		} else if err := journal.appendLocked(expected); err != nil {
			return err
		}
		previous = expected.RecordDigest
	}
	return nil
}

func (journal *RecoveryJournal) appendLocked(record JournalRecord) error {
	payload, err := record.canonical()
	if err != nil || len(payload) == 0 || len(payload) > journalFrameMax {
		return ErrInvalid
	}
	header := fmt.Sprintf("%08x:", len(payload))
	frame := make([]byte, 0, len(header)+len(payload)+1)
	frame = append(frame, header...)
	frame = append(frame, payload...)
	frame = append(frame, '\n')
	if err := writeAll(journal.file, frame); err != nil {
		return journal.poisonLocked(err)
	}
	if err := journal.file.Sync(); err != nil {
		return journal.poisonLocked(err)
	}
	journal.records = append(journal.records, record)
	return nil
}

func (journal *RecoveryJournal) poisonLocked(cause error) error {
	if journal.file != nil {
		closeErr := journal.file.Close()
		journal.file = nil
		return errors.Join(cause, closeErr)
	}
	return cause
}

func validateFactSequence(facts []CommittedAuthorityFact) error {
	if len(facts) == 0 || len(facts) > 5 {
		return ErrAuthorityConflict
	}
	wantKinds := [...]RecordKind{
		RecordProvisionIntent,
		RecordProvisionPrepared,
		RecordProvisionReceipt,
		RecordTerminateIntent,
		RecordTerminateReceipt,
	}
	recordIDs := make(map[string]string, len(facts))
	commands := make(map[string]string)
	idempotency := make(map[string]string)
	for index, fact := range facts {
		if err := fact.Validate(); err != nil {
			return err
		}
		if fact.RecordKind != wantKinds[index] {
			return ErrAuthorityConflict
		}
		if index > 0 {
			previous := facts[index-1]
			if fact.ExpectedAttemptSequence <= previous.ExpectedAttemptSequence || !sameAllocationScope(facts[0].Binding, fact.Binding) {
				return ErrAuthorityConflict
			}
			if index < 3 && (fact.Binding != facts[0].Binding || fact.RequestDigest != facts[0].RequestDigest) {
				return ErrAuthorityConflict
			}
			if index == 4 && (fact.Binding != facts[3].Binding || fact.RequestDigest != facts[3].RequestDigest || fact.TerminalizationID != facts[3].TerminalizationID || fact.CleanupBindingDigest != facts[3].CleanupBindingDigest || fact.ProcessTerminalFactDigest != facts[3].ProcessTerminalFactDigest) {
				return ErrAuthorityConflict
			}
		}
		if _, ok := recordIDs[fact.RecordID]; ok {
			return ErrAuthorityConflict
		}
		recordIDs[fact.RecordID] = fact.AttemptAuthorityFactDigest
		scope := fact.Binding.AuthorityNamespaceID + "\x00" + fact.Binding.RunID + "\x00" + fact.Binding.AttemptID
		commandKey := scope + "\x00" + fact.Binding.CommandID
		idempotencyKey := scope + "\x00" + fact.Binding.IdempotencyKey
		if prior, ok := commands[commandKey]; ok && prior != fact.RequestDigest {
			return ErrAuthorityConflict
		}
		if prior, ok := idempotency[idempotencyKey]; ok && prior != fact.RequestDigest {
			return ErrAuthorityConflict
		}
		commands[commandKey] = fact.RequestDigest
		idempotency[idempotencyKey] = fact.RequestDigest
	}
	return nil
}

func parseJournalFrames(data []byte) ([]JournalRecord, int, bool, error) {
	records := make([]JournalRecord, 0)
	offset := 0
	previous := JournalGenesisDigest
	for offset < len(data) {
		frameStart := offset
		remaining := len(data) - offset
		if remaining < journalHeaderLength {
			if validHeaderPrefix(data[offset:]) {
				return records, frameStart, true, nil
			}
			return nil, 0, false, ErrJournalCorrupt
		}
		header := data[offset : offset+journalHeaderLength]
		if !validFullHeader(header) {
			return nil, 0, false, ErrJournalCorrupt
		}
		length64, err := strconv.ParseUint(string(header[:8]), 16, 32)
		if err != nil || length64 == 0 || length64 > journalFrameMax {
			return nil, 0, false, ErrJournalCorrupt
		}
		offset += journalHeaderLength
		length := int(length64)
		if len(data)-offset < length {
			return records, frameStart, true, nil
		}
		payload := data[offset : offset+length]
		offset += length
		if offset == len(data) {
			return records, frameStart, true, nil
		}
		if data[offset] != '\n' {
			return nil, 0, false, ErrJournalCorrupt
		}
		offset++

		var record JournalRecord
		if err := strictCanonicalDecode(payload, &record); err != nil || record.Validate() != nil || record.JournalSequence != uint64(len(records)+1) || record.PreviousRecordDigest != previous {
			return nil, 0, false, ErrJournalCorrupt
		}
		for _, prior := range records {
			if prior.RecordID == record.RecordID && prior.RecordDigest != record.RecordDigest {
				return nil, 0, false, ErrJournalCorrupt
			}
		}
		records = append(records, record)
		previous = record.RecordDigest
	}
	return records, offset, false, nil
}

func validHeaderPrefix(data []byte) bool {
	if len(data) > journalHeaderLength {
		return false
	}
	for index, value := range data {
		if index < 8 {
			if !isLowerHex(value) {
				return false
			}
		} else if value != ':' {
			return false
		}
	}
	return true
}

func validFullHeader(data []byte) bool {
	return len(data) == journalHeaderLength && validHeaderPrefix(data) && data[8] == ':'
}

func isLowerHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

// EncodeFactPayload canonicalizes a typed authority fact for projection.
func EncodeFactPayload(value any) (json.RawMessage, error) {
	data, err := canonicalValue(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// DecodeJournalRecord is used by hostile fixtures without accepting relaxed
// JSON or alternate field sets.
func DecodeJournalRecord(data []byte) (JournalRecord, error) {
	var record JournalRecord
	if err := strictCanonicalDecode(data, &record); err != nil || record.Validate() != nil {
		return JournalRecord{}, ErrJournalCorrupt
	}
	return record, nil
}

func frameForRecord(record JournalRecord) ([]byte, error) {
	payload, err := record.canonical()
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%08x:%s\n", len(payload), payload)), nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
