package processsupervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
)

const JournalFileName = "process-supervisor-v1.journal"

const JournalGenesisDigest = "sha256:43bf94597317b90b38cd0490e3101624e0930516d6701f6d31daba7375a678e4"

type journalKind string

const (
	journalSessionCreated journalKind = "session-created"
	journalCommandIntent  journalKind = "command-intent"
	journalCommandReceipt journalKind = "command-receipt"
)

// requestProjection is the complete journal-visible command description. It
// intentionally contains no path, argv, environment value, stdin, nonce, or
// raw transcript material.
type requestProjection struct {
	Command                      CommandName `json:"command"`
	CommandID                    string      `json:"commandId"`
	Sequence                     uint64      `json:"sequence"`
	RequestDigest                string      `json:"requestDigest"`
	PreviousCommandDigest        string      `json:"previousCommandDigest"`
	CurrentAuthorityHead         string      `json:"currentAuthorityHead"`
	NextAuthorityHead            string      `json:"nextAuthorityHead,omitempty"`
	SupervisorStartedFactDigest  string      `json:"supervisorStartedFactDigest,omitempty"`
	AuthorityAbsenceProofDigest  string      `json:"authorityAbsenceProofDigest,omitempty"`
	Deadline                     string      `json:"deadline"`
	LaunchMaterialsDigest        string      `json:"launchMaterialsDigest,omitempty"`
	AgentLaunchSpecDigest        string      `json:"agentLaunchSpecDigest,omitempty"`
	ClosureProfileID             string      `json:"closureProfileId,omitempty"`
	ArgvDigest                   string      `json:"argvDigest,omitempty"`
	EnvironmentDigest            string      `json:"environmentDigest,omitempty"`
	StdinDigest                  string      `json:"stdinDigest,omitempty"`
	EnvironmentKeys              []string    `json:"environmentKeys,omitempty"`
	ProcessStartedFactDigest     string      `json:"processStartedFactDigest,omitempty"`
	TerminalizationBarrierDigest string      `json:"terminalizationBarrierDigest,omitempty"`
	TerminalizationID            string      `json:"terminalizationId,omitempty"`
	TerminalGeneration           uint64      `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest         string      `json:"cleanupBindingDigest,omitempty"`
	LastObservationDigest        string      `json:"lastObservationDigest,omitempty"`
	ProcessTerminalFactDigest    string      `json:"processTerminalFactDigest,omitempty"`
	AllocationTerminatedDigest   string      `json:"allocationTerminatedFactDigest,omitempty"`
}

type journalRecord struct {
	SchemaVersion        string             `json:"schemaVersion"`
	JournalSequence      uint64             `json:"journalSequence"`
	Kind                 journalKind        `json:"kind"`
	SessionID            string             `json:"sessionId"`
	SessionNonceDigest   string             `json:"sessionNonceDigest"`
	Authority            AuthorityTuple     `json:"authority"`
	OwnerEpoch           uint64             `json:"ownerEpoch"`
	CurrentAuthorityHead string             `json:"currentAuthorityHead"`
	Request              *requestProjection `json:"request,omitempty"`
	Response             *Response          `json:"response,omitempty"`
	PreviousRecordDigest string             `json:"previousRecordDigest"`
	RecordDigest         string             `json:"recordDigest,omitempty"`
}

func (record journalRecord) detachedDigest() (string, error) {
	record.RecordDigest = ""
	return digestValue(record)
}

func (record journalRecord) validate(previous string, sequence uint64) error {
	if record.SchemaVersion != JournalSchema || record.JournalSequence != sequence || record.Kind == "" ||
		!validID(record.SessionID) || !validDigest(record.SessionNonceDigest) || record.Authority.validate() != nil ||
		record.OwnerEpoch == 0 || record.OwnerEpoch > maxSafeJSONInteger || !validDigest(record.CurrentAuthorityHead) || record.PreviousRecordDigest != previous ||
		!validDigest(record.RecordDigest) {
		return ErrIntervention
	}
	switch record.Kind {
	case journalSessionCreated:
		if record.Request != nil || record.Response != nil || sequence != 1 {
			return ErrIntervention
		}
	case journalCommandIntent:
		if record.Request == nil || record.Response != nil || validateProjection(*record.Request) != nil {
			return ErrIntervention
		}
	case journalCommandReceipt:
		if record.Request == nil || record.Response == nil || validateProjection(*record.Request) != nil || validateStoredResponse(*record.Response, *record.Request) != nil {
			return ErrIntervention
		}
	default:
		return ErrIntervention
	}
	want, err := record.detachedDigest()
	if err != nil || want != record.RecordDigest {
		return ErrIntervention
	}
	return nil
}

func validateProjection(request requestProjection) error {
	if !validCommand(request.Command) || !validID(request.CommandID) || request.Sequence == 0 || request.Sequence > maxSafeJSONInteger ||
		!validDigest(request.RequestDigest) || !validDigest(request.PreviousCommandDigest) ||
		!validDigest(request.CurrentAuthorityHead) {
		return ErrIntervention
	}
	if _, err := parseDeadline(request.Deadline); err != nil {
		return ErrIntervention
	}
	options := request
	options.Command, options.CommandID, options.RequestDigest, options.PreviousCommandDigest, options.CurrentAuthorityHead, options.Deadline = "", "", "", "", "", ""
	options.Sequence = 0
	switch request.Command {
	case CommandBindAuthority:
		if !validDigest(options.NextAuthorityHead) || options.NextAuthorityHead == request.CurrentAuthorityHead || !validDigest(options.SupervisorStartedFactDigest) {
			return ErrIntervention
		}
		options.NextAuthorityHead, options.SupervisorStartedFactDigest = "", ""
	case CommandAbortUnbound:
		if !validDigest(options.AuthorityAbsenceProofDigest) {
			return ErrIntervention
		}
		options.AuthorityAbsenceProofDigest = ""
	case CommandSpawn:
		if !validDigest(options.LaunchMaterialsDigest) || !validDigest(options.AgentLaunchSpecDigest) || !validID(options.ClosureProfileID) || !validDigest(options.ArgvDigest) || !validDigest(options.EnvironmentDigest) || !validDigest(options.StdinDigest) || len(options.EnvironmentKeys) > MaxEnvironmentKeys {
			return ErrIntervention
		}
		for index, key := range options.EnvironmentKeys {
			if !validEnvironmentKey(key) || index > 0 && options.EnvironmentKeys[index-1] >= key {
				return ErrIntervention
			}
		}
		options.LaunchMaterialsDigest, options.AgentLaunchSpecDigest, options.ClosureProfileID, options.ArgvDigest, options.EnvironmentDigest, options.StdinDigest = "", "", "", "", "", ""
		options.EnvironmentKeys = nil
	case CommandResume:
		if !validDigest(options.ProcessStartedFactDigest) {
			return ErrIntervention
		}
		options.ProcessStartedFactDigest = ""
	case CommandInspect, CommandTerminate:
		if !validDigest(options.ProcessStartedFactDigest) || !validDigest(options.TerminalizationBarrierDigest) || !validID(options.TerminalizationID) || options.TerminalGeneration == 0 || options.TerminalGeneration > maxSafeJSONInteger || !validDigest(options.CleanupBindingDigest) || !validDigest(options.LastObservationDigest) {
			return ErrIntervention
		}
		options.ProcessStartedFactDigest, options.TerminalizationBarrierDigest, options.TerminalizationID, options.CleanupBindingDigest, options.LastObservationDigest = "", "", "", "", ""
		options.TerminalGeneration = 0
	case CommandCollect:
		if !validDigest(options.ProcessStartedFactDigest) || !validDigest(options.LastObservationDigest) {
			return ErrIntervention
		}
		options.ProcessStartedFactDigest, options.LastObservationDigest = "", ""
	case CommandClose:
		if !validDigest(options.ProcessTerminalFactDigest) || !validDigest(options.AllocationTerminatedDigest) || !validDigest(options.CleanupBindingDigest) {
			return ErrIntervention
		}
		options.ProcessTerminalFactDigest, options.AllocationTerminatedDigest, options.CleanupBindingDigest = "", "", ""
	}
	if !projectionOptionsEmpty(options) {
		return ErrIntervention
	}
	return nil
}

func projectionOptionsEmpty(value requestProjection) bool {
	raw, err := canonicalValue(value)
	if err != nil {
		return false
	}
	empty, err := canonicalValue(requestProjection{})
	return err == nil && bytes.Equal(raw, empty)
}

func validateStoredResponse(response Response, request requestProjection) error {
	if response.SchemaVersion != ResponseSchema || response.ProtocolRevision != ProtocolRevision || response.Command != request.Command ||
		response.CommandID != request.CommandID || response.Sequence != request.Sequence || response.RequestDigest != request.RequestDigest ||
		(response.Status != "ok" && response.Status != "rejected") || !validID(response.ReasonCode) ||
		!validDigest(response.ReceiptDigest) || !validDigest(response.ObservationDigest) || !validDigest(response.CommandHead) {
		return ErrIntervention
	}
	if len(response.Payload) > MaxDiagnosticBytes {
		return ErrIntervention
	}
	var payload MechanicsResult
	if err := strictCanonicalDecode(response.Payload, &payload); err != nil || validateMechanicsResult(payload) != nil {
		return ErrIntervention
	}
	receiptDigest, err := digestValue(payload)
	if err != nil || receiptDigest != response.ReceiptDigest || payload.ReasonCode != response.ReasonCode || payload.ObservationDigest != response.ObservationDigest || response.Status == "ok" && payload.Disposition != "ok" || response.Status == "rejected" && payload.Disposition != "rejected" {
		return ErrIntervention
	}
	commandHead, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, response.ReceiptDigest})
	if err != nil || commandHead != response.CommandHead {
		return ErrIntervention
	}
	return nil
}

type replayedCommand struct {
	Projection requestProjection
	Response   Response
}

type JournalSnapshot struct {
	Sequence uint64
	Head     string
	commands map[string]replayedCommand
	pending  *requestProjection
}

// ResponseForCommand exposes a safe, immutable copy of one durable receipt to
// the authority-side reconciler without exposing journal mutation internals.
func (snapshot JournalSnapshot) ResponseForCommand(commandID string) (Response, bool) {
	command, ok := snapshot.commands[commandID]
	if !ok {
		return Response{}, false
	}
	response := command.Response
	response.Payload = append([]byte(nil), response.Payload...)
	return response, true
}

func (snapshot JournalSnapshot) PendingCommandID() (string, bool) {
	if snapshot.pending == nil {
		return "", false
	}
	return snapshot.pending.CommandID, true
}

type Journal struct {
	mu          sync.Mutex
	file        *os.File
	sequence    uint64
	head        string
	created     journalRecord
	commands    map[string]replayedCommand
	pending     *requestProjection
	pendingBase *journalRecord
}

func OpenJournal(file *os.File) (*Journal, error) {
	if file == nil {
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
	records, truncateAt, partial, err := parseJournal(data)
	if err != nil {
		return nil, err
	}
	if partial {
		if err := file.Truncate(int64(truncateAt)); err != nil || file.Sync() != nil {
			return nil, ErrIntervention
		}
	}
	journal := &Journal{file: file, head: JournalGenesisDigest, commands: make(map[string]replayedCommand)}
	if err := journal.applyReplay(records); err != nil {
		return nil, err
	}
	return journal, nil
}

func (journal *Journal) applyReplay(records []journalRecord) error {
	expectedAuthorityHead := ""
	lastOwnerEpoch := uint64(0)
	commandSequence := uint64(0)
	commandHead := CommandGenesisDigest
	var pendingRecord *journalRecord
	for index, record := range records {
		if err := record.validate(journal.head, uint64(index+1)); err != nil {
			return err
		}
		if index == 0 {
			expectedAuthorityHead = record.CurrentAuthorityHead
			lastOwnerEpoch = record.OwnerEpoch
		} else {
			if record.SessionID != journal.created.SessionID || record.SessionNonceDigest != journal.created.SessionNonceDigest || record.Authority != journal.created.Authority || record.OwnerEpoch < lastOwnerEpoch || record.OwnerEpoch == lastOwnerEpoch && record.CurrentAuthorityHead != expectedAuthorityHead {
				return ErrIntervention
			}
			if record.OwnerEpoch > lastOwnerEpoch {
				expectedAuthorityHead = record.CurrentAuthorityHead
			}
			if record.Request.Sequence != commandSequence+1 || record.Request.PreviousCommandDigest != commandHead || record.Response != nil && record.Response.SessionID != journal.created.SessionID {
				return ErrIntervention
			}
		}
		journal.sequence = record.JournalSequence
		journal.head = record.RecordDigest
		switch record.Kind {
		case journalSessionCreated:
			journal.created = record
		case journalCommandIntent:
			if journal.pending != nil {
				return ErrIntervention
			}
			projection := *record.Request
			journal.pending = &projection
			copy := record
			pendingRecord = &copy
			journal.pendingBase = &copy
		case journalCommandReceipt:
			if journal.pending == nil || pendingRecord == nil || !equalProjection(*journal.pending, *record.Request) || !sameJournalCommandBase(*pendingRecord, record) {
				return ErrIntervention
			}
			if _, ok := journal.commands[record.Request.CommandID]; ok {
				return ErrIntervention
			}
			journal.commands[record.Request.CommandID] = replayedCommand{Projection: *record.Request, Response: *record.Response}
			journal.pending = nil
			journal.pendingBase = nil
			pendingRecord = nil
			commandSequence = record.Request.Sequence
			commandHead = record.Response.CommandHead
			lastOwnerEpoch = record.OwnerEpoch
			if record.Request.Command == CommandBindAuthority {
				expectedAuthorityHead = record.Request.NextAuthorityHead
			} else {
				expectedAuthorityHead = record.Request.CurrentAuthorityHead
			}
		}
	}
	if len(records) > 0 && records[0].Kind != journalSessionCreated {
		return ErrIntervention
	}
	return nil
}

func sameJournalCommandBase(left, right journalRecord) bool {
	return left.SessionID == right.SessionID && left.SessionNonceDigest == right.SessionNonceDigest && left.Authority == right.Authority && left.OwnerEpoch == right.OwnerEpoch && left.CurrentAuthorityHead == right.CurrentAuthorityHead
}

func (journal *Journal) Snapshot() JournalSnapshot {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	commands := make(map[string]replayedCommand, len(journal.commands))
	for key, command := range journal.commands {
		commands[key] = command
	}
	var pending *requestProjection
	if journal.pending != nil {
		copy := *journal.pending
		pending = &copy
	}
	return JournalSnapshot{Sequence: journal.sequence, Head: journal.head, commands: commands, pending: pending}
}

func (journal *Journal) AppendSessionCreated(sessionID, nonceDigest string, authority AuthorityTuple, ownerEpoch uint64, authorityHead string) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.sequence != 0 {
		return ErrConflict
	}
	record := journalRecord{SchemaVersion: JournalSchema, JournalSequence: 1, Kind: journalSessionCreated, SessionID: sessionID, SessionNonceDigest: nonceDigest, Authority: authority, OwnerEpoch: ownerEpoch, CurrentAuthorityHead: authorityHead, PreviousRecordDigest: JournalGenesisDigest}
	return journal.appendLocked(&record)
}

func (journal *Journal) AppendIntent(base journalRecord, projection requestProjection) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	_, duplicate := journal.commands[projection.CommandID]
	if journal.pending != nil || duplicate || len(journal.commands) >= MaxCommands {
		return ErrIntervention
	}
	base.Kind = journalCommandIntent
	base.Request = &projection
	if err := journal.appendLocked(&base); err != nil {
		return err
	}
	copy := projection
	journal.pending = &copy
	baseCopy := base
	journal.pendingBase = &baseCopy
	return nil
}

func (journal *Journal) AppendReceipt(base journalRecord, projection requestProjection, response Response) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.pending == nil || journal.pendingBase == nil || !equalProjection(*journal.pending, projection) || !sameJournalCommandBase(*journal.pendingBase, base) {
		return ErrIntervention
	}
	base.Kind = journalCommandReceipt
	base.Request = &projection
	base.Response = &response
	if err := journal.appendLocked(&base); err != nil {
		return err
	}
	journal.commands[projection.CommandID] = replayedCommand{Projection: projection, Response: response}
	journal.pending = nil
	journal.pendingBase = nil
	return nil
}

func (journal *Journal) appendLocked(record *journalRecord) error {
	if journal.file == nil || record == nil || journal.sequence >= MaxCommands*2+1 {
		return ErrIntervention
	}
	record.JournalSequence = journal.sequence + 1
	record.PreviousRecordDigest = journal.head
	digest, err := record.detachedDigest()
	if err != nil {
		return err
	}
	record.RecordDigest = digest
	if err := record.validate(journal.head, journal.sequence+1); err != nil {
		return err
	}
	frame, err := encodeFrame(record, MaxJournalPayload)
	if err != nil {
		return err
	}
	stat, err := journal.file.Stat()
	if err != nil || stat.Size()+int64(len(frame)) > MaxJournalFileBytes {
		return ErrIntervention
	}
	if _, err := journal.file.Seek(0, io.SeekEnd); err != nil || writeAll(journal.file, frame) != nil || journal.file.Sync() != nil {
		_ = journal.file.Close()
		journal.file = nil
		return ErrIntervention
	}
	journal.sequence = record.JournalSequence
	journal.head = record.RecordDigest
	if record.Kind == journalSessionCreated {
		journal.created = *record
	}
	return nil
}

func (journal *Journal) Close() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.file == nil {
		return nil
	}
	err := journal.file.Close()
	journal.file = nil
	return err
}

func parseJournal(data []byte) ([]journalRecord, int, bool, error) {
	var records []journalRecord
	offset := 0
	previous := JournalGenesisDigest
	for offset < len(data) {
		start := offset
		if len(data)-offset < frameHeaderBytes {
			if validFrameHeaderPrefix(data[offset:]) {
				return records, start, true, nil
			}
			return nil, 0, false, ErrIntervention
		}
		header := data[offset : offset+frameHeaderBytes]
		if !validFrameHeaderPrefix(header) || header[8] != ':' {
			return nil, 0, false, ErrIntervention
		}
		length64, err := strconv.ParseUint(string(header[:8]), 16, 32)
		if err != nil || length64 == 0 || length64 > MaxJournalPayload {
			return nil, 0, false, ErrIntervention
		}
		offset += frameHeaderBytes
		length := int(length64)
		if len(data)-offset < length+1 {
			prefix := data[offset:]
			// A complete, valid record whose sole missing byte is the final LF is
			// still a legal torn-frame prefix. Validate its full journal semantics
			// before truncating it; a complete but invalid record is tampering, not
			// a recoverable partial append.
			if len(prefix) == length {
				var record journalRecord
				if strictCanonicalDecode(prefix, &record) != nil || record.validate(previous, uint64(len(records)+1)) != nil {
					return nil, 0, false, ErrIntervention
				}
				return records, start, true, nil
			}
			if !validCanonicalObjectPrefix(prefix) {
				return nil, 0, false, ErrIntervention
			}
			return records, start, true, nil
		}
		payload := data[offset : offset+length]
		offset += length
		if data[offset] != '\n' {
			return nil, 0, false, ErrIntervention
		}
		offset++
		var record journalRecord
		if err := strictCanonicalDecode(payload, &record); err != nil || record.validate(previous, uint64(len(records)+1)) != nil {
			return nil, 0, false, ErrIntervention
		}
		records = append(records, record)
		previous = record.RecordDigest
	}
	return records, offset, false, nil
}

// validCanonicalObjectPrefix distinguishes a torn final append from trailing
// garbage. It accepts only bytes that can still be completed into one compact
// JSON object, rejects duplicate completed member names, and rejects
// whitespace outside strings because no such byte can occur in JCS output.
func validCanonicalObjectPrefix(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if data[0] != '{' || hasUnquotedWhitespace(data) {
		return false
	}
	type scope struct {
		object       bool
		expectingKey bool
		keys         map[string]struct{}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	stack := make([]scope, 0, 4)
	complete := false
	completeValue := func() bool {
		if len(stack) == 0 {
			complete = true
			return true
		}
		parent := &stack[len(stack)-1]
		if parent.object {
			if parent.expectingKey {
				return false
			}
			parent.expectingKey = true
		}
		return true
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			if complete {
				return false
			}
			if errors.Is(err, io.EOF) {
				return true
			}
			var syntax *json.SyntaxError
			return errors.As(err, &syntax) && syntax.Error() == "unexpected end of JSON input"
		}
		if complete {
			return false
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				stack = append(stack, scope{object: true, expectingKey: true, keys: make(map[string]struct{})})
			case '[':
				stack = append(stack, scope{})
			case '}', ']':
				if len(stack) == 0 || delimiter == '}' && !stack[len(stack)-1].object || delimiter == ']' && stack[len(stack)-1].object {
					return false
				}
				if delimiter == '}' && !stack[len(stack)-1].expectingKey {
					return false
				}
				stack = stack[:len(stack)-1]
				if !completeValue() {
					return false
				}
			}
			continue
		}
		if len(stack) == 0 {
			return false
		}
		current := &stack[len(stack)-1]
		if current.object && current.expectingKey {
			key, ok := token.(string)
			if !ok {
				return false
			}
			if _, exists := current.keys[key]; exists {
				return false
			}
			current.keys[key] = struct{}{}
			current.expectingKey = false
			continue
		}
		if !completeValue() {
			return false
		}
	}
}

func hasUnquotedWhitespace(data []byte) bool {
	inString := false
	escaped := false
	for _, value := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			if value == '"' {
				inString = false
			}
			continue
		}
		if value == '"' {
			inString = true
			continue
		}
		if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
			return true
		}
	}
	return false
}

func validFrameHeaderPrefix(data []byte) bool {
	if len(data) > frameHeaderBytes {
		return false
	}
	for index, value := range data {
		if index < 8 && !lowerHex(value) || index == 8 && value != ':' {
			return false
		}
	}
	return true
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func equalProjection(left, right requestProjection) bool {
	a, errA := canonicalValue(left)
	b, errB := canonicalValue(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
