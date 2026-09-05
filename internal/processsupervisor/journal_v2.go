package processsupervisor

import "strconv"

const (
	v2ControlPhaseBootstrapInitial = "bootstrap-initial"
	v2ControlPhaseRuntimeBase      = "runtime-base"
	v2ControlPhaseCollectIntent    = "collect-intent"
	v2ControlPhaseCollected        = "collected"
)

type journalRecordV2 struct {
	SchemaVersion               string             `json:"schemaVersion"`
	ProtocolRevision            string             `json:"protocolRevision"`
	LaunchChildProtocolRevision string             `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string             `json:"mechanicsIdentity"`
	JournalSequence             uint64             `json:"journalSequence"`
	Kind                        journalKind        `json:"kind"`
	SessionID                   string             `json:"sessionId"`
	SessionNonceDigest          string             `json:"sessionNonceDigest"`
	Authority                   AuthorityTuple     `json:"authority"`
	OwnerEpoch                  uint64             `json:"ownerEpoch"`
	CurrentAuthorityHead        string             `json:"currentAuthorityHead"`
	Request                     *requestProjection `json:"request,omitempty"`
	Response                    *responseV2        `json:"response,omitempty"`
	PreviousRecordDigest        string             `json:"previousRecordDigest"`
	RecordDigest                string             `json:"recordDigest,omitempty"`
}

func (record journalRecordV2) detachedDigest() (string, error) {
	record.RecordDigest = ""
	return digestValue(record)
}

func (record journalRecordV2) validate(previous string, sequence uint64) error {
	if !validV2Binding(record.SchemaVersion, journalSchemaV2, record.ProtocolRevision, record.LaunchChildProtocolRevision, record.MechanicsIdentity) ||
		record.JournalSequence != sequence || record.Kind == "" || !validID(record.SessionID) || !validDigest(record.SessionNonceDigest) || record.Authority.validate() != nil ||
		record.OwnerEpoch == 0 || record.OwnerEpoch > maxSafeJSONInteger || !validDigest(record.CurrentAuthorityHead) || record.PreviousRecordDigest != previous || !validDigest(record.RecordDigest) {
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
		if record.Request == nil || record.Response == nil || validateProjection(*record.Request) != nil || validateStoredResponseV2(*record.Response, *record.Request) != nil {
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

func validateStoredResponseV2(response responseV2, request requestProjection) error {
	if response.validate() != nil || response.Command != request.Command || response.CommandID != request.CommandID || response.Sequence != request.Sequence || response.RequestDigest != request.RequestDigest || validateV2ResponseObservation(response, request) != nil {
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

// JournalReadOnlyObservation is a path-free audit result. The decoder never
// truncates, appends, adopts or returns a writable Journal.
type JournalReadOnlyObservation struct {
	ProtocolRevision string
	SchemaVersion    string
	FileName         string
	Sequence         uint64
	Head             string
	PartialTail      bool
}

// DecodeSupervisorJournalReadOnly routes bytes by the exact generation leaf.
// A v1 leaf can only reach the v1 decoder and a v2 leaf can only reach the v2
// decoder. This is the S2 historical audit surface; production append remains
// on the existing v1 writer until S3 performs a new-session-only cutover.
func DecodeSupervisorJournalReadOnly(fileName string, data []byte) (JournalReadOnlyObservation, error) {
	switch fileName {
	case JournalFileName:
		records, truncateAt, partial, err := parseJournal(data)
		if err != nil {
			return JournalReadOnlyObservation{}, err
		}
		legacy := &Journal{head: JournalGenesisDigest, commands: make(map[string]replayedCommand)}
		if err := legacy.applyReplay(records); err != nil {
			return JournalReadOnlyObservation{}, err
		}
		if partial && validateCompleteTornV1Tail(records, data[truncateAt:]) != nil {
			return JournalReadOnlyObservation{}, ErrIntervention
		}
		head := JournalGenesisDigest
		if len(records) != 0 {
			head = records[len(records)-1].RecordDigest
		}
		return JournalReadOnlyObservation{ProtocolRevision: ProtocolRevision, SchemaVersion: JournalSchema, FileName: JournalFileName, Sequence: uint64(len(records)), Head: head, PartialTail: partial}, nil
	case journalFileNameV2:
		records, truncateAt, partial, err := parseJournalV2(data)
		if err != nil {
			return JournalReadOnlyObservation{}, err
		}
		if err := validateJournalV2Replay(records); err != nil {
			return JournalReadOnlyObservation{}, err
		}
		if partial && validateCompleteTornV2Tail(records, data[truncateAt:]) != nil {
			return JournalReadOnlyObservation{}, ErrIntervention
		}
		head := journalGenesisDigestV2
		if len(records) != 0 {
			head = records[len(records)-1].RecordDigest
		}
		return JournalReadOnlyObservation{ProtocolRevision: protocolRevisionV2, SchemaVersion: journalSchemaV2, FileName: journalFileNameV2, Sequence: uint64(len(records)), Head: head, PartialTail: partial}, nil
	default:
		return JournalReadOnlyObservation{}, ErrIntervention
	}
}

func validateCompleteTornV1Tail(records []journalRecord, tail []byte) error {
	payload, complete := completeTornPayload(tail)
	if !complete {
		return nil
	}
	var record journalRecord
	if strictCanonicalDecode(payload, &record) != nil {
		return ErrIntervention
	}
	all := append(append([]journalRecord(nil), records...), record)
	legacy := &Journal{head: JournalGenesisDigest, commands: make(map[string]replayedCommand)}
	return legacy.applyReplay(all)
}

func validateCompleteTornV2Tail(records []journalRecordV2, tail []byte) error {
	payload, complete := completeTornPayload(tail)
	if !complete {
		return nil
	}
	var record journalRecordV2
	if strictCanonicalDecode(payload, &record) != nil {
		return ErrIntervention
	}
	return validateJournalV2Replay(append(append([]journalRecordV2(nil), records...), record))
}

func completeTornPayload(tail []byte) ([]byte, bool) {
	if len(tail) < frameHeaderBytes || !validFrameHeaderPrefix(tail[:frameHeaderBytes]) || tail[8] != ':' {
		return nil, false
	}
	length64, err := strconv.ParseUint(string(tail[:8]), 16, 32)
	if err != nil || length64 == 0 || length64 > MaxJournalPayload || len(tail) != frameHeaderBytes+int(length64) {
		return nil, false
	}
	return tail[frameHeaderBytes:], true
}

func validateJournalV2Replay(records []journalRecordV2) error {
	state := newJournalStateV2()
	for _, record := range records {
		if err := state.validateNext(record); err != nil {
			return err
		}
		state.accept(record)
	}
	return nil
}

func sameJournalCommandBaseV2(left, right journalRecordV2) bool {
	return left.SessionID == right.SessionID && left.SessionNonceDigest == right.SessionNonceDigest && left.Authority == right.Authority && left.OwnerEpoch == right.OwnerEpoch && left.CurrentAuthorityHead == right.CurrentAuthorityHead
}

// ValidateSupervisorJournalLeafSet rejects generation ambiguity before any
// journal decoder or mechanics call. Empty is valid only for an initial
// control directory; a populated set must contain exactly one frozen leaf.
func ValidateSupervisorJournalLeafSet(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != JournalFileName && name != journalFileNameV2 {
			return ErrIntervention
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrIntervention
		}
		seen[name] = struct{}{}
	}
	if len(seen) > 1 {
		return ErrIntervention
	}
	return nil
}

// ValidateDormantV2ControlDirectoryEntries applies ADR 0079's exact v2
// phase sets without opening or mutating a directory. The caller must derive
// phase from a successfully verified v2 journal; names alone never select a
// generation or authorize recovery.
func ValidateDormantV2ControlDirectoryEntries(phase string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			return ErrIntervention
		}
		seen[name] = struct{}{}
	}
	base := map[string]struct{}{"session.nonce": {}, journalFileNameV2: {}, "control.sock": {}}
	stdout := cloneEntrySet(base, "stdout.bin")
	stderr := cloneEntrySet(stdout, "stderr.bin")
	collected := cloneEntrySet(stderr, "transcript.jcs")
	var allowed []map[string]struct{}
	switch phase {
	case v2ControlPhaseBootstrapInitial:
		allowed = []map[string]struct{}{{}}
	case v2ControlPhaseRuntimeBase:
		allowed = []map[string]struct{}{base}
	case v2ControlPhaseCollectIntent:
		allowed = []map[string]struct{}{base, stdout, stderr, collected}
	case v2ControlPhaseCollected:
		allowed = []map[string]struct{}{collected}
	default:
		return ErrIntervention
	}
	for _, exact := range allowed {
		if sameEntrySet(seen, exact) {
			return nil
		}
	}
	return ErrIntervention
}

func cloneEntrySet(source map[string]struct{}, added string) map[string]struct{} {
	cloned := make(map[string]struct{}, len(source)+1)
	for name := range source {
		cloned[name] = struct{}{}
	}
	cloned[added] = struct{}{}
	return cloned
}

func sameEntrySet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for name := range left {
		if _, ok := right[name]; !ok {
			return false
		}
	}
	return true
}

func parseJournalV2(data []byte) ([]journalRecordV2, int, bool, error) {
	var records []journalRecordV2
	offset := 0
	previous := journalGenesisDigestV2
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
			if len(prefix) == length {
				var record journalRecordV2
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
		var record journalRecordV2
		if strictCanonicalDecode(payload, &record) != nil || record.validate(previous, uint64(len(records)+1)) != nil {
			return nil, 0, false, ErrIntervention
		}
		records = append(records, record)
		previous = record.RecordDigest
	}
	return records, offset, false, nil
}
