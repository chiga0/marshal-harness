package processsupervisor

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

// journalStateV2 is shared by the exact v2 decoder and writer. Replay is
// linear in history; an append validates only its next transition, not the
// entire history again. No v1 record is translated into this state.
type journalStateV2 struct {
	sequence      uint64
	head          string
	created       journalRecordV2
	pending       *journalRecordV2
	receipts      map[string]journalRecordV2
	ownerEpoch    uint64
	authorityHead string
	commandSeq    uint64
	commandHead   string
}

func newJournalStateV2() journalStateV2 {
	return journalStateV2{head: journalGenesisDigestV2, commandHead: commandGenesisDigestV2, receipts: make(map[string]journalRecordV2)}
}

func (state *journalStateV2) validateNext(record journalRecordV2) error {
	if state.sequence >= MaxCommands*2+1 || record.validate(state.head, state.sequence+1) != nil {
		return ErrIntervention
	}
	if state.sequence == 0 {
		if record.Kind != journalSessionCreated {
			return ErrIntervention
		}
		return nil
	}
	if record.Kind == journalSessionCreated || record.SessionID != state.created.SessionID || record.SessionNonceDigest != state.created.SessionNonceDigest || record.Authority != state.created.Authority ||
		record.OwnerEpoch < state.ownerEpoch || record.OwnerEpoch == state.ownerEpoch && record.CurrentAuthorityHead != state.authorityHead || record.Request == nil ||
		record.Request.Sequence != state.commandSeq+1 || record.Request.PreviousCommandDigest != state.commandHead || record.Response != nil && record.Response.SessionID != state.created.SessionID {
		return ErrIntervention
	}
	if _, duplicate := state.receipts[record.Request.CommandID]; duplicate {
		return ErrIntervention
	}
	switch record.Kind {
	case journalCommandIntent:
		if state.pending != nil || len(state.receipts) >= MaxCommands {
			return ErrIntervention
		}
	case journalCommandReceipt:
		if state.pending == nil || !equalProjection(*state.pending.Request, *record.Request) || !sameJournalCommandBaseV2(*state.pending, record) {
			return ErrIntervention
		}
	default:
		return ErrIntervention
	}
	return nil
}

// accept is called only after validateNext and, on the write path, fsync.
func (state *journalStateV2) accept(record journalRecordV2) {
	record = cloneJournalRecordV2(record)
	state.sequence, state.head = record.JournalSequence, record.RecordDigest
	switch record.Kind {
	case journalSessionCreated:
		state.created = record
		state.ownerEpoch, state.authorityHead = record.OwnerEpoch, record.CurrentAuthorityHead
	case journalCommandIntent:
		state.pending = &record
	case journalCommandReceipt:
		state.receipts[record.Request.CommandID] = record
		state.pending = nil
		state.commandSeq, state.commandHead = record.Request.Sequence, record.Response.CommandHead
		state.ownerEpoch = record.OwnerEpoch
		state.authorityHead = record.Request.CurrentAuthorityHead
		if record.Request.Command == CommandBindAuthority && record.Response.Status == "ok" {
			state.authorityHead = record.Request.NextAuthorityHead
		}
	}
}

func cloneJournalRecordV2(record journalRecordV2) journalRecordV2 {
	if record.Request != nil {
		value := *record.Request
		value.EnvironmentKeys = append([]string(nil), value.EnvironmentKeys...)
		record.Request = &value
	}
	if record.Response != nil {
		value := *record.Response
		value.Payload = append([]byte(nil), value.Payload...)
		record.Response = &value
	}
	return record
}

// journalWriterV2 owns only a held mechanics journal. The server remains
// responsible for exclusive session ownership and descriptor-relative parent
// identity/entry-set checks. This constructor cannot admit a production launch.
type journalWriterV2 struct {
	mu    sync.Mutex
	file  *os.File
	state journalStateV2
	size  int64
}

func (journal *journalWriterV2) receipt(commandID string) (journalRecordV2, bool) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	record, ok := journal.state.receipts[commandID]
	return cloneJournalRecordV2(record), ok
}

func (journal *journalWriterV2) checkpoint() (uint64, string, bool) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.state.sequence, journal.state.head, journal.state.pending != nil
}

// recoverySnapshot copies only the checkpoint and one requested command;
// reconnect cost does not grow with the number of earlier receipts.
func (journal *journalWriterV2) recoverySnapshot(commandID string) journalStateV2 {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	state := journal.state
	state.created = cloneJournalRecordV2(state.created)
	state.receipts = make(map[string]journalRecordV2)
	if record, ok := journal.state.receipts[commandID]; ok {
		state.receipts[commandID] = cloneJournalRecordV2(record)
	}
	if state.pending != nil {
		pending := cloneJournalRecordV2(*state.pending)
		state.pending = &pending
	}
	return state
}

func openJournalWriterV2(file *os.File) (*journalWriterV2, error) {
	if file == nil || filepath.Base(file.Name()) != journalFileNameV2 {
		return nil, ErrInvalid
	}
	stat, err := file.Stat()
	if err != nil || validateJournalFile(file) != nil || stat.Size() < 0 || stat.Size() > MaxJournalFileBytes {
		return nil, ErrIntervention
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, stat.Size()))
	if err != nil {
		return nil, ErrIntervention
	}
	records, truncateAt, partial, err := parseJournalV2(data)
	if err != nil {
		return nil, err
	}
	state := newJournalStateV2()
	for _, record := range records {
		if err := state.validateNext(record); err != nil {
			return nil, err
		}
		state.accept(record)
	}
	// Validate the entire semantic prefix BEFORE repairing any tail. A fully
	// present but invalid record missing only LF is not a recoverable crash.
	if partial {
		if validateCompleteTornV2Tail(records, data[truncateAt:]) != nil {
			return nil, ErrIntervention
		}
		if err := file.Truncate(int64(truncateAt)); err != nil || file.Sync() != nil {
			return nil, ErrIntervention
		}
	}
	return &journalWriterV2{file: file, state: state, size: int64(truncateAt)}, nil
}

func (journal *journalWriterV2) append(record journalRecordV2) (journalRecordV2, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.file == nil {
		return journalRecordV2{}, ErrIntervention
	}
	record = cloneJournalRecordV2(record)
	record.JournalSequence, record.PreviousRecordDigest = journal.state.sequence+1, journal.state.head
	digest, err := record.detachedDigest()
	if err != nil {
		return journalRecordV2{}, err
	}
	record.RecordDigest = digest
	if err := journal.state.validateNext(record); err != nil {
		return journalRecordV2{}, err
	}
	frame, err := encodeFrame(record, MaxJournalPayload)
	if err != nil {
		return journalRecordV2{}, err
	}
	stat, err := journal.file.Stat()
	if err != nil || validateJournalFile(journal.file) != nil || stat.Size() != journal.size || stat.Size()+int64(len(frame)) > MaxJournalFileBytes {
		return journalRecordV2{}, ErrIntervention
	}
	if _, err := journal.file.Seek(0, io.SeekEnd); err != nil || writeAll(journal.file, frame) != nil || journal.file.Sync() != nil {
		_ = journal.file.Close()
		journal.file = nil
		return journalRecordV2{}, ErrIntervention
	}
	journal.state.accept(record)
	journal.size += int64(len(frame))
	return cloneJournalRecordV2(record), nil
}

func (journal *journalWriterV2) close() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.file == nil {
		return nil
	}
	err := journal.file.Close()
	journal.file = nil
	return err
}
