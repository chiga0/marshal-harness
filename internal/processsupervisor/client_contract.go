package processsupervisor

import (
	"bufio"
	"io"
	"sync"
	"time"
)

// HandshakeAnchor is the authority-side expectation for one initial or
// reconnect handshake. Callers derive every field from their current durable
// authority and mechanics-journal view; the supervisor grants none of them.
type HandshakeAnchor struct {
	SessionID            string
	SessionNonceDigest   string
	OwnerEpoch           uint64
	CurrentAuthorityHead string
	CommandSequence      uint64
	CommandHead          string
	JournalSequence      uint64
	JournalHead          string
	UID                  uint32
	GID                  uint32
	FixedBinary          BinaryIdentity
	ControlSocket        ControlSocketIdentity
}

// NewRequest builds the one canonical request shape accepted by the
// supervisor and computes requestDigest with that field detached. Production
// composition supplies sequence/head/current authority from its durable
// authority view; this helper grants no authority itself.
func NewRequest(sessionID string, command CommandName, commandID string, sequence uint64, previousCommandDigest, currentAuthorityHead string, deadline time.Time, payload any) (Request, error) {
	rawPayload, err := canonicalValue(payload)
	if err != nil {
		return Request{}, err
	}
	request := Request{ProtocolRevision: ProtocolRevision, SessionID: sessionID, Command: command, CommandID: commandID, Sequence: sequence, PreviousCommandDigest: previousCommandDigest, CurrentAuthorityHead: currentAuthorityHead, Deadline: deadline.UTC().Format(time.RFC3339Nano), Payload: rawPayload}
	request.RequestDigest, err = digestValue(requestDigestInput{ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload})
	if err != nil {
		return Request{}, err
	}
	if _, _, err := projectRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// ProtocolCodec retains its buffered reader across frames so pipelined bytes
// cannot be lost. It is safe for one concurrent reader and one concurrent
// writer, matching a Unix stream connection.
type ProtocolCodec struct {
	reader *bufio.Reader
	stream io.ReadWriter
	write  sync.Mutex
}

func NewProtocolCodec(stream io.ReadWriter) (*ProtocolCodec, error) {
	if stream == nil {
		return nil, ErrInvalid
	}
	return &ProtocolCodec{reader: bufio.NewReaderSize(stream, MaxWireFrameBytes+frameHeaderBytes+1), stream: stream}, nil
}

func (codec *ProtocolCodec) Write(value any) error {
	if codec == nil || codec.stream == nil {
		return ErrInvalid
	}
	codec.write.Lock()
	defer codec.write.Unlock()
	return writeFrame(codec.stream, value, MaxWireFrameBytes)
}

func (codec *ProtocolCodec) Read(target any) error {
	if codec == nil || codec.reader == nil || target == nil {
		return ErrInvalid
	}
	raw, err := readFrame(codec.reader, MaxWireFrameBytes)
	if err != nil {
		return err
	}
	return strictCanonicalDecode(raw, target)
}

func CanonicalProtocolMessage(value any) ([]byte, error) {
	return canonicalValue(value)
}

func ValidateHandshakeResponse(response HandshakeResponse) error {
	observedAt, err := time.Parse(time.RFC3339Nano, response.ObservedAt)
	if err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != response.ObservedAt || response.SchemaVersion != HandshakeSchema || response.ProtocolRevision != ProtocolRevision || response.Status != "ok" || !validID(response.ReasonCode) || !validID(response.SessionID) || !validDigest(response.SessionNonceDigest) || response.OwnerEpoch == 0 || response.OwnerEpoch > maxSafeJSONInteger || !validDigest(response.CurrentAuthorityHead) || response.CommandSequence > maxSafeJSONInteger || !validDigest(response.CommandHead) || response.JournalSequence == 0 || response.JournalSequence > maxSafeJSONInteger || !validDigest(response.JournalHead) || !validID(response.ObserverIdentity) || response.SupervisorProcess.validate() != nil || response.SupervisorBinary.validate() != nil || response.ControlSocket.validate() != nil {
		return ErrInvalid
	}
	return nil
}

// ValidateHandshakeBinding completes the Core side of bidirectional
// authentication. observed must come from kernel peer credentials and process
// observation on the same connected Unix socket, never from response fields.
func ValidateHandshakeBinding(response HandshakeResponse, anchor HandshakeAnchor, observed CoreIdentity) error {
	if ValidateHandshakeResponse(response) != nil || !validID(anchor.SessionID) || !validDigest(anchor.SessionNonceDigest) || anchor.OwnerEpoch == 0 || anchor.OwnerEpoch > maxSafeJSONInteger || !validDigest(anchor.CurrentAuthorityHead) || anchor.CommandSequence > maxSafeJSONInteger || !validDigest(anchor.CommandHead) || anchor.JournalSequence == 0 || anchor.JournalSequence > maxSafeJSONInteger || !validDigest(anchor.JournalHead) || anchor.UID == 0 || anchor.FixedBinary.validate() != nil || anchor.ControlSocket.validate() != nil {
		return ErrInvalid
	}
	if response.SessionID != anchor.SessionID || response.SessionNonceDigest != anchor.SessionNonceDigest || response.OwnerEpoch != anchor.OwnerEpoch || response.CurrentAuthorityHead != anchor.CurrentAuthorityHead || response.CommandSequence != anchor.CommandSequence || response.CommandHead != anchor.CommandHead || response.JournalSequence != anchor.JournalSequence || response.JournalHead != anchor.JournalHead || response.ControlSocket != anchor.ControlSocket {
		return ErrConflict
	}
	if observed.UID != anchor.UID || observed.GID != anchor.GID || observed.Process != response.SupervisorProcess || observed.Binary != response.SupervisorBinary || !sameBinaryObject(observed.Binary, anchor.FixedBinary) {
		return ErrConflict
	}
	return nil
}

func ValidateResponse(response Response) error {
	if response.SchemaVersion != ResponseSchema || response.ProtocolRevision != ProtocolRevision || !validID(response.SessionID) || !validCommand(response.Command) || !validID(response.CommandID) || response.Sequence == 0 || response.Sequence > maxSafeJSONInteger || !validDigest(response.RequestDigest) || (response.Status != "ok" && response.Status != "rejected") || !validID(response.ReasonCode) || !validDigest(response.ReceiptDigest) || !validDigest(response.ObservationDigest) || !validDigest(response.CommandHead) {
		return ErrInvalid
	}
	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil || validateMechanicsResult(result) != nil || result.ReasonCode != response.ReasonCode || result.ObservationDigest != response.ObservationDigest {
		return ErrInvalid
	}
	receipt, err := digestValue(result)
	if err != nil || receipt != response.ReceiptDigest || response.Status == "ok" && result.Disposition != "ok" || response.Status == "rejected" && result.Disposition != "rejected" {
		return ErrConflict
	}
	return nil
}

func ValidateResponseBinding(response Response, request Request) error {
	if ValidateResponse(response) != nil || response.SessionID != request.SessionID || response.Command != request.Command || response.CommandID != request.CommandID || response.Sequence != request.Sequence || response.RequestDigest != request.RequestDigest {
		return ErrConflict
	}
	commandHead, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, response.ReceiptDigest})
	if err != nil || commandHead != response.CommandHead {
		return ErrConflict
	}
	return nil
}
