package runstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/domain"
	"golang.org/x/sys/unix"
)

// maxControlJournalBytes caps the control journal at 32 MiB, including the
// record about to be appended and its trailing newline.
const maxControlJournalBytes int64 = 32 << 20

// ControlValidator is the minimal validation dependency required to append
// control records. `contract.Validator.Validate` satisfies it.
type ControlValidator interface {
	Validate(kind domain.Kind, data []byte) error
}

// ControlRecord is one typed entry of a Run's append-only control journal.
// Exactly one of Approval or Intervention is set, matching Kind.
type ControlRecord struct {
	Kind         domain.Kind
	Approval     *domain.ApprovalRecord
	Intervention *domain.InterventionRecord
}

// RecordID returns the durable ID of the wrapped control record.
func (c ControlRecord) RecordID() string {
	if c.Approval != nil {
		return c.Approval.RecordID
	}
	if c.Intervention != nil {
		return c.Intervention.RecordID
	}
	return ""
}

// AppendApproval appends an ApprovalRecord to the Run's control journal under
// the held Lease. The record must be schema-valid, bound to the current Run
// identity and State Sequence, and carry the next global control sequence.
func (s *Store) AppendApproval(lease *Lease, validator ControlValidator, record domain.ApprovalRecord) error {
	return s.appendControlRecord(lease, validator, controlEntry{
		kind:            domain.KindApprovalRecord,
		recordID:        record.RecordID,
		runID:           record.RunID,
		taskID:          record.TaskID,
		stateSequence:   record.Binding.StateSequence,
		controlSequence: record.ControlSequence,
	}, record, maxControlJournalBytes)
}

// AppendIntervention appends an InterventionRecord under the same global
// control sequence and identity rules as AppendApproval.
func (s *Store) AppendIntervention(lease *Lease, validator ControlValidator, record domain.InterventionRecord) error {
	return s.appendControlRecord(lease, validator, controlEntry{
		kind:            domain.KindInterventionRecord,
		recordID:        record.RecordID,
		runID:           record.RunID,
		taskID:          record.TaskID,
		stateSequence:   record.StateSequence,
		controlSequence: record.ControlSequence,
	}, record, maxControlJournalBytes)
}

// ReadControlRecords returns every control record of a Run in append order
// with concrete types preserved. Each call decodes the journal fresh, so
// callers never observe shared mutable state. A truncated final record fails
// closed with ErrTruncatedTail; malformed JSON, unknown kinds and sequence
// gaps or duplicates fail closed with ErrConflict.
func (s *Store) ReadControlRecords(runID string, validator ControlValidator) ([]ControlRecord, error) {
	if validator == nil {
		return nil, errors.New("control read requires a validator")
	}
	state, err := s.Inspect(runID)
	if err != nil {
		return nil, err
	}
	_, records, err := s.readControlJournal(runID, state.TaskID, state.Sequence, validator, maxControlJournalBytes)
	return records, err
}

type controlEntry struct {
	kind            domain.Kind
	recordID        string
	runID           string
	taskID          string
	stateSequence   uint64
	controlSequence uint64
}

func (s *Store) controlDir(runID string) (string, error) {
	directory, err := s.runDir(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "control"), nil
}

func (s *Store) appendControlRecord(lease *Lease, validator ControlValidator, entry controlEntry, payload any, maxJournalBytes int64) error {
	if !leaseOwnerMatches(lease) {
		return errors.New("control append requires held run lease")
	}
	if lease.guard.preparedBorrowed.Load() {
		return fmt.Errorf("%w: prepared Run-start authority is borrowed", ErrConflict)
	}
	lease.guard.mu.RLock()
	defer lease.guard.mu.RUnlock()
	if !leaseHeldBySelfLocked(lease) || lease.guard.preparedBorrowed.Load() {
		return fmt.Errorf("%w: control append requires current unborrowed run lease", ErrConflict)
	}
	lease.guard.mutation.Lock()
	defer lease.guard.mutation.Unlock()
	if validator == nil {
		return errors.New("control append requires a validator")
	}
	if lease.runID != entry.runID {
		return fmt.Errorf("%w: lease belongs to run %s", ErrConflict, lease.runID)
	}
	if entry.kind != domain.KindApprovalRecord && entry.kind != domain.KindInterventionRecord {
		return fmt.Errorf("%w: unsupported control kind %q", ErrConflict, entry.kind)
	}
	if lease.beforeMutation != nil {
		if err := lease.beforeMutation(); err != nil {
			return fmt.Errorf("control mutation hook: %w", err)
		}
	}
	authority, err := openRunAuthorityLocked(lease)
	if err != nil {
		return fmt.Errorf("control mutation authority: %w", err)
	}
	defer authority.Close()
	state, err := inspectAt(int(authority.Fd()))
	if err != nil {
		return err
	}
	if state.RunID != entry.runID || state.TaskID != entry.taskID {
		return fmt.Errorf("%w: control record identity task=%s run=%s does not match run state task=%s run=%s", ErrConflict, entry.taskID, entry.runID, state.TaskID, state.RunID)
	}
	if entry.stateSequence != state.Sequence {
		return fmt.Errorf("%w: control record binds state sequence %d, run state is %d", ErrConflict, entry.stateSequence, state.Sequence)
	}
	for _, id := range []string{entry.recordID, entry.taskID, entry.runID} {
		if err := domain.ValidateID(id); err != nil {
			return fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	raw, records, err := readControlJournalAt(int(authority.Fd()), entry.runID, state.TaskID, state.Sequence, validator, maxJournalBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := uint64(len(records)) + 1
	if entry.controlSequence != next {
		return fmt.Errorf("%w: expected control sequence %d, record is %d", ErrConflict, next, entry.controlSequence)
	}
	for _, existing := range records {
		if existing.RecordID() == entry.recordID {
			return fmt.Errorf("%w: duplicate control record ID %s", ErrConflict, entry.recordID)
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode control record: %w", err)
	}
	var envelope struct {
		APIVersion domain.APIVersion `json:"apiVersion"`
		Kind       domain.Kind       `json:"kind"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode control record envelope: %w", err)
	}
	if envelope.APIVersion != domain.APIVersionV1Alpha1 || envelope.Kind != entry.kind {
		return fmt.Errorf("%w: control record envelope apiVersion=%q kind=%q is incomplete", ErrConflict, envelope.APIVersion, envelope.Kind)
	}
	if err := validator.Validate(entry.kind, data); err != nil {
		return fmt.Errorf("%w: validate control record: %v", ErrConflict, err)
	}
	line := append(append([]byte{}, data...), '\n')
	if int64(len(raw))+int64(len(line)) > maxJournalBytes {
		return fmt.Errorf("%w: control journal would exceed %d bytes", ErrConflict, maxJournalBytes)
	}
	if err := appendRegularInDirectoryAt(int(authority.Fd()), "control", "records.jsonl", line); err != nil {
		return fmt.Errorf("sync control journal: %w", err)
	}
	return nil
}

func (s *Store) readControlJournal(runID, taskID string, stateSequence uint64, validator ControlValidator, maxJournalBytes int64) ([]byte, []ControlRecord, error) {
	directory, err := s.controlDir(runID)
	if err != nil {
		return nil, nil, err
	}
	if validator == nil {
		return nil, nil, errors.New("control read requires a validator")
	}
	if info, statErr := os.Lstat(directory); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return nil, nil, fmt.Errorf("%w: control journal directory is not a directory", ErrConflict)
	} else if statErr != nil {
		return nil, nil, statErr
	}
	journal := filepath.Join(directory, "records.jsonl")
	info, err := os.Lstat(journal)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: control journal is not a regular file", ErrConflict)
	}
	if info.Size() > maxJournalBytes {
		return nil, nil, fmt.Errorf("%w: control journal exceeds %d bytes", ErrConflict, maxJournalBytes)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		return nil, nil, err
	}
	return decodeControlJournalData(data, runID, taskID, stateSequence, validator)
}

func readControlJournalAt(runFD int, runID, taskID string, stateSequence uint64, validator ControlValidator, maxJournalBytes int64) ([]byte, []ControlRecord, error) {
	controlFD, err := openDirectoryAt(runFD, "control", false)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(controlFD)
	data, err := readRegularAt(controlFD, "records.jsonl", maxJournalBytes)
	if err != nil {
		return nil, nil, err
	}
	return decodeControlJournalData(data, runID, taskID, stateSequence, validator)
}

func decodeControlJournalData(data []byte, runID, taskID string, stateSequence uint64, validator ControlValidator) ([]byte, []ControlRecord, error) {
	truncated := len(data) > 0 && data[len(data)-1] != '\n'
	reader := bufio.NewReader(bytes.NewReader(data))
	records := make([]ControlRecord, 0)
	seen := map[string]bool{}
	for {
		line, readErr := reader.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		trimmed := bytes.TrimSpace(line)
		if complete && len(trimmed) == 0 {
			return nil, nil, fmt.Errorf("%w: blank control record at line %d", ErrConflict, len(records)+1)
		}
		if len(trimmed) > 0 && complete {
			record, err := decodeControlRecord(trimmed, uint64(len(records))+1, seen, validator)
			if err != nil {
				return nil, nil, err
			}
			if recordTaskID(record) != taskID || recordRunID(record) != runID {
				return nil, nil, fmt.Errorf("%w: control record identity mismatch at record %d", ErrConflict, len(records)+1)
			}
			if recordStateSequence(record) > stateSequence {
				return nil, nil, fmt.Errorf("%w: control record binds future state sequence at record %d", ErrConflict, len(records)+1)
			}
			records = append(records, record)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	if truncated {
		return data, records, ErrTruncatedTail
	}
	return data, records, nil
}

func decodeControlRecord(line []byte, expectedSequence uint64, seen map[string]bool, validator ControlValidator) (ControlRecord, error) {
	var envelope struct {
		APIVersion domain.APIVersion `json:"apiVersion"`
		Kind       domain.Kind       `json:"kind"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return ControlRecord{}, fmt.Errorf("%w: decode control record %d: %v", ErrConflict, expectedSequence, err)
	}
	if envelope.APIVersion != domain.APIVersionV1Alpha1 {
		return ControlRecord{}, fmt.Errorf("%w: control record %d has unsupported apiVersion %q", ErrConflict, expectedSequence, envelope.APIVersion)
	}
	record := ControlRecord{Kind: envelope.Kind}
	switch envelope.Kind {
	case domain.KindApprovalRecord:
		approval := &domain.ApprovalRecord{}
		if err := json.Unmarshal(line, approval); err != nil {
			return ControlRecord{}, fmt.Errorf("decode %s %d: %w", envelope.Kind, expectedSequence, err)
		}
		record.Approval = approval
	case domain.KindInterventionRecord:
		intervention := &domain.InterventionRecord{}
		if err := json.Unmarshal(line, intervention); err != nil {
			return ControlRecord{}, fmt.Errorf("decode %s %d: %w", envelope.Kind, expectedSequence, err)
		}
		record.Intervention = intervention
	default:
		return ControlRecord{}, fmt.Errorf("%w: unknown control record kind %q at record %d", ErrConflict, envelope.Kind, expectedSequence)
	}
	if err := validator.Validate(envelope.Kind, line); err != nil {
		return ControlRecord{}, fmt.Errorf("%w: validate control record %d: %v", ErrConflict, expectedSequence, err)
	}
	if record.RecordID() == "" || seen[record.RecordID()] {
		return ControlRecord{}, fmt.Errorf("%w: duplicate or missing control record ID at record %d", ErrConflict, expectedSequence)
	}
	if record.Approval != nil && record.Approval.ControlSequence != expectedSequence ||
		record.Intervention != nil && record.Intervention.ControlSequence != expectedSequence {
		return ControlRecord{}, fmt.Errorf("%w: control sequence gap or duplicate at record %d", ErrConflict, expectedSequence)
	}
	seen[record.RecordID()] = true
	return record, nil
}

func recordTaskID(record ControlRecord) string {
	if record.Approval != nil {
		return record.Approval.TaskID
	}
	return record.Intervention.TaskID
}

func recordRunID(record ControlRecord) string {
	if record.Approval != nil {
		return record.Approval.RunID
	}
	return record.Intervention.RunID
}

func recordStateSequence(record ControlRecord) uint64 {
	if record.Approval != nil {
		return record.Approval.Binding.StateSequence
	}
	return record.Intervention.StateSequence
}
