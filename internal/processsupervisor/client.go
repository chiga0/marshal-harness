package processsupervisor

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type StartOptions struct {
	FixedMarshalPath string
	ControlDirectory *os.File
	Bootstrap        BootstrapRequest
}

type ReconnectOptions struct {
	FixedMarshalPath         string
	ControlDirectory         *os.File
	ControlDirectoryIdentity ControlDirectoryIdentity
	Plan                     ReconnectPlan
	Anchor                   HandshakeAnchor
	Pending                  *PreparedCommand
}

// ReconnectPlan is the durable owner/head transition. Previous mechanics
// values and current Core identity are derived from Anchor/kernel observation;
// raw nonce and request bytes are internal only.
type ReconnectPlan struct {
	PreviousOwnerEpoch    uint64
	OwnerEpoch            uint64
	PreviousAuthorityHead string
	CurrentAuthorityHead  string
	ControlOwnerAcquired  string
}

type reconnectWireOptions struct {
	ControlDirectory         *os.File
	ControlDirectoryIdentity ControlDirectoryIdentity
	Request                  reconnectRequest
	Anchor                   HandshakeAnchor
	PendingEvidence          *PendingReplayEvidence
}

// ConnectionEvidence is the secret-free adjacent evidence returned by a
// successful initial or reconnect handshake. SessionNonce is intentionally not
// included; authority persists only the digest already present in Handshake.
type ConnectionEvidence struct {
	Core             CoreIdentity
	ControlDirectory ControlDirectoryIdentity
	Handshake        HandshakeResponse
	Anchor           HandshakeAnchor
	ReplayedOutcome  *VerifiedCommandOutcome
	Recovery         *SessionRecoveryEvidence
}

// SessionRecoveryEvidence is the closed, secret-free reconnect decision. It
// makes exact-intent intervention and exact receipt/unchanged replay visible
// without exposing the raw request payload or session nonce.
type SessionRecoveryEvidence struct {
	Reconciliation  ReconciliationState
	Previous        HandshakeAnchor
	Current         HandshakeAnchor
	Pending         *PendingReplayEvidence
	MechanicsLocked bool
}

// CommandRecoveryEvidence binds one verified command to the exact mechanics
// anchors before and after it. Reconciliation is empty for an ordinary Do and
// is the authenticated reconnect classification for a replayed outcome.
type CommandRecoveryEvidence struct {
	Reconciliation ReconciliationState
	Replayed       bool
	PreCommand     HandshakeAnchor
	PostCommand    HandshakeAnchor
}

// VerifiedCommandOutcome is the public, typed result of exact response
// binding. It exposes all digests needed by ResultIngress composition while
// preventing adapters from decoding private MechanicsResult/process JSON.
type VerifiedCommandOutcome struct {
	Command           CommandName
	CommandID         string
	Sequence          uint64
	Status            string
	Disposition       string
	ReasonCode        string
	RequestDigest     string
	ReceiptDigest     string
	ObservationDigest string
	CommandHead       string
	TranscriptDigest  string
	StdoutBytes       uint64
	StderrBytes       uint64
	Truncated         bool
	ProcessReport     *ProcessReport
	Recovery          CommandRecoveryEvidence
}

// CommandOptions is the caller's exact durable command anchor. The Client does
// not invent sequence, command head, authority head, command ID, or deadline.
// A caller recovering a lost response must provide the same values and payload
// again; the supervisor journal decides whether that request is an exact replay.
type CommandOptions struct {
	Command               CommandName
	CommandID             string
	Sequence              uint64
	PreviousCommandDigest string
	CurrentAuthorityHead  string
	Deadline              time.Time
}

type deadlineStream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

// PendingReplayEvidence is the complete secret-free projection retained after
// an ambiguous transport result. Raw payload, argv, environment and stdin are
// deliberately absent. The caller reconstructs the exact Request from its
// authoritative inputs and the digest is checked before reconnect or replay.
type PendingReplayEvidence struct {
	ProtocolRevision      string      `json:"protocolRevision"`
	SessionID             string      `json:"sessionId"`
	Command               CommandName `json:"command"`
	CommandID             string      `json:"commandId"`
	Sequence              uint64      `json:"sequence"`
	PreviousCommandDigest string      `json:"previousCommandDigest"`
	CurrentAuthorityHead  string      `json:"currentAuthorityHead"`
	RequestDigest         string      `json:"requestDigest"`
	Deadline              string      `json:"deadline"`
}

// Client is a single authenticated Core connection to one fixed supervisor.
// It owns only the connection. Disconnect never signals, waits for, or closes
// supervisor mechanics and is therefore safe while the workload is live.
type Client struct {
	mu        sync.Mutex
	stream    deadlineStream
	codec     *ProtocolCodec
	handshake HandshakeResponse
	anchor    HandshakeAnchor
	evidence  ConnectionEvidence
	pending   *PendingReplayEvidence
	replayed  *VerifiedCommandOutcome
	state     ReconciliationState
	poisoned  bool
}

func newClient(stream deadlineStream, evidence ConnectionEvidence, observed CoreIdentity) (*Client, error) {
	if stream == nil || ValidateHandshakeBinding(evidence.Handshake, evidence.Anchor, observed) != nil {
		return nil, ErrInvalid
	}
	codec, err := NewProtocolCodec(stream)
	if err != nil {
		return nil, err
	}
	evidence.Handshake = cloneHandshake(evidence.Handshake)
	evidence.ReplayedOutcome = cloneVerifiedOutcome(evidence.ReplayedOutcome)
	evidence.Recovery = cloneSessionRecovery(evidence.Recovery)
	return &Client{stream: stream, codec: codec, handshake: evidence.Handshake, anchor: evidence.Anchor, evidence: evidence, replayed: cloneVerifiedOutcome(evidence.ReplayedOutcome), state: evidence.Handshake.Reconciliation}, nil
}

// Handshake returns the immutable, already authenticated handshake projection.
// The caller persists it only through the authority fact defined by ADR 0059.
func (client *Client) Handshake() HandshakeResponse {
	if client == nil {
		return HandshakeResponse{}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return cloneHandshake(client.handshake)
}

func (client *Client) Evidence() ConnectionEvidence {
	if client == nil {
		return ConnectionEvidence{}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	evidence := client.evidence
	evidence.Handshake = cloneHandshake(evidence.Handshake)
	evidence.ReplayedOutcome = cloneVerifiedOutcome(evidence.ReplayedOutcome)
	evidence.Recovery = cloneSessionRecovery(evidence.Recovery)
	return evidence
}

func (client *Client) Recovery() (SessionRecoveryEvidence, bool) {
	if client == nil {
		return SessionRecoveryEvidence{}, false
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.evidence.Recovery == nil {
		return SessionRecoveryEvidence{}, false
	}
	return *cloneSessionRecovery(client.evidence.Recovery), true
}

// Anchor returns the latest locally verified authority/mechanics anchor. It is
// advanced only after an exact response or a closed reconnect reconciliation.
func (client *Client) Anchor() HandshakeAnchor {
	if client == nil {
		return HandshakeAnchor{}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.anchor
}

func (client *Client) PendingReplayEvidence() (PendingReplayEvidence, bool) {
	if client == nil {
		return PendingReplayEvidence{}, false
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.pending == nil {
		return PendingReplayEvidence{}, false
	}
	return *client.pending, true
}

func (client *Client) ReplayedOutcome() (VerifiedCommandOutcome, bool) {
	if client == nil {
		return VerifiedCommandOutcome{}, false
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.replayed == nil {
		return VerifiedCommandOutcome{}, false
	}
	return *cloneVerifiedOutcome(client.replayed), true
}

// Prepare creates the exact private request without mutating connection state.
// Its secret-free Evidence must be durably committed before DoPrepared.
func (client *Client) Prepare(options CommandOptions, payload any) (PreparedCommand, error) {
	if client == nil {
		return PreparedCommand{}, ErrUnavailable
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.poisoned || client.stream == nil || client.codec == nil || client.state == ReconciliationIntentPending {
		return PreparedCommand{}, ErrUnavailable
	}
	return PrepareCommand(client.anchor, options, payload)
}

// Do remains a source-compatible, non-production convenience. Production
// composition must call Prepare, persist Evidence, then call DoPrepared.
func (client *Client) Do(ctx context.Context, options CommandOptions, payload any) (VerifiedCommandOutcome, error) {
	if client == nil {
		return VerifiedCommandOutcome{}, ErrUnavailable
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.poisoned || client.stream == nil || client.codec == nil {
		return VerifiedCommandOutcome{}, ErrUnavailable
	}
	if client.state == ReconciliationIntentPending {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if ctx == nil {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if options.Sequence != client.anchor.CommandSequence+1 || options.PreviousCommandDigest != client.anchor.CommandHead {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	prepared, err := prepareCommand(client.anchor, options, payload, false)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	return client.doPreparedLocked(ctx, prepared)
}

// DoPrepared executes exactly the private request frozen by Prepare. A
// prepared command for a different session/pre-anchor or a second execution
// after the anchor advances is rejected before transport.
func (client *Client) DoPrepared(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	if client == nil {
		return VerifiedCommandOutcome{}, ErrUnavailable
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if prepared.evidence.Validate() != nil || prepared.evidence.PreCommand != client.anchor {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	return client.doPreparedLocked(ctx, prepared)
}

func (client *Client) doPreparedLocked(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	if client.poisoned || client.stream == nil || client.codec == nil {
		return VerifiedCommandOutcome{}, ErrUnavailable
	}
	if client.state == ReconciliationIntentPending {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if ctx == nil {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	request := prepared.request
	if request.SessionID != client.anchor.SessionID || request.Sequence != client.anchor.CommandSequence+1 || request.PreviousCommandDigest != client.anchor.CommandHead {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	deadline, err := parseDeadline(request.Deadline)
	if err != nil {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	evidence := pendingEvidence(request)
	if client.pending != nil {
		if *client.pending != evidence {
			return VerifiedCommandOutcome{}, ErrConflict
		}
	} else {
		client.pending = &evidence
	}
	var response Response
	err = runBoundedTransport(ctx, client.stream, deadline, func() error {
		if err := client.codec.Write(request); err != nil {
			return ErrIntervention
		}
		if err := client.codec.Read(&response); err != nil {
			return ErrIntervention
		}
		return ValidateResponseBinding(response, request)
	})
	if err != nil {
		client.poisonLocked()
		return VerifiedCommandOutcome{}, err
	}
	preCommand := client.anchor
	_, receiptHead, err := expectedPendingJournalHeads(client.anchor, request, &response)
	if err != nil {
		client.poisonLocked()
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	projection, _, err := projectRequest(request)
	if err != nil {
		client.poisonLocked()
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	postCommand, err := commandPostAnchor(preCommand, request, response)
	if err != nil || postCommand.JournalHead != receiptHead || postCommand.CurrentAuthorityHead != commandPostAuthorityHead(preCommand.CurrentAuthorityHead, request, response, projection) {
		client.poisonLocked()
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	outcome, err := verifiedCommandOutcome(request, response, CommandRecoveryEvidence{PreCommand: preCommand, PostCommand: postCommand})
	if err != nil {
		client.poisonLocked()
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	client.anchor = postCommand
	client.evidence.Anchor = client.anchor
	client.pending = nil
	client.state = ""
	return outcome, nil
}

func cloneHandshake(handshake HandshakeResponse) HandshakeResponse {
	handshake.ReplayedResponse = cloneResponsePointer(handshake.ReplayedResponse)
	return handshake
}

func cloneResponsePointer(response *Response) *Response {
	if response == nil {
		return nil
	}
	copy := *response
	copy.Payload = append([]byte(nil), response.Payload...)
	return &copy
}

// AbortUnbound is deliberately only a protocol primitive. The caller must
// obtain AuthorityAbsenceProofDigest from the current durable owner authority;
// this method neither discovers nor manufactures absence.
func (client *Client) AbortUnbound(ctx context.Context, options CommandOptions, payload AbortUnboundPayload) (VerifiedCommandOutcome, error) {
	if options.Command != CommandAbortUnbound {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	return client.Do(ctx, options, payload)
}

func cloneVerifiedOutcome(outcome *VerifiedCommandOutcome) *VerifiedCommandOutcome {
	if outcome == nil {
		return nil
	}
	copy := *outcome
	if outcome.ProcessReport != nil {
		report := *outcome.ProcessReport
		copy.ProcessReport = &report
	}
	return &copy
}

func cloneSessionRecovery(recovery *SessionRecoveryEvidence) *SessionRecoveryEvidence {
	if recovery == nil {
		return nil
	}
	copy := *recovery
	if recovery.Pending != nil {
		pending := *recovery.Pending
		copy.Pending = &pending
	}
	return &copy
}

func verifiedCommandOutcome(request Request, response Response, recovery CommandRecoveryEvidence) (VerifiedCommandOutcome, error) {
	if ValidateResponseBinding(response, request) != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil || validateMechanicsResult(result) != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	report, err := verifiedProcessReport(request, response, result)
	if err != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	wantPost, err := commandPostAnchor(recovery.PreCommand, request, response)
	if err != nil || recovery.PostCommand != wantPost || recovery.PreCommand.SessionID != request.SessionID || recovery.PreCommand.CommandSequence+1 != request.Sequence || recovery.PreCommand.CommandHead != request.PreviousCommandDigest ||
		recovery.Replayed && recovery.Reconciliation != ReconciliationUnchanged && recovery.Reconciliation != ReconciliationReceiptCommitted || !recovery.Replayed && recovery.Reconciliation != "" {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	return VerifiedCommandOutcome{
		Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, Status: response.Status, Disposition: result.Disposition, ReasonCode: response.ReasonCode,
		RequestDigest: request.RequestDigest, ReceiptDigest: response.ReceiptDigest, ObservationDigest: response.ObservationDigest, CommandHead: response.CommandHead,
		TranscriptDigest: result.TranscriptDigest, StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes, Truncated: result.Truncated,
		ProcessReport: report, Recovery: recovery,
	}, nil
}

func verifiedProcessReport(request Request, response Response, result MechanicsResult) (*ProcessReport, error) {
	_, payload, err := projectRequest(request)
	if err != nil {
		return nil, ErrConflict
	}
	emptyTranscript := result.TranscriptDigest == "" && result.StdoutBytes == 0 && result.StderrBytes == 0 && !result.Truncated
	if response.Status == "rejected" {
		if string(result.Payload) != "{}" || !emptyTranscript || result.ObservationDigest != canonical.DigestBytes([]byte(result.ReasonCode)) {
			return nil, ErrConflict
		}
		return nil, nil
	}
	switch request.Command {
	case CommandBindAuthority:
		if string(result.Payload) != "{}" || !emptyTranscript || result.ObservationDigest != payload.(BindAuthorityPayload).SupervisorStartedFactDigest {
			return nil, ErrConflict
		}
		return nil, nil
	case CommandAbortUnbound:
		if string(result.Payload) != "{}" || !emptyTranscript || result.ObservationDigest != payload.(AbortUnboundPayload).AuthorityAbsenceProofDigest {
			return nil, ErrConflict
		}
		return nil, nil
	default:
		var report ProcessReport
		if strictCanonicalDecode(result.Payload, &report) != nil || ValidateProcessReport(report) != nil {
			return nil, ErrConflict
		}
		digest, err := digestValue(report)
		if err != nil || result.ObservationDigest != digest {
			return nil, ErrConflict
		}
		if request.Command == CommandCollect {
			if result.TranscriptDigest != digest || result.StdoutBytes != report.StdoutBytes || result.StderrBytes != report.StderrBytes || result.Truncated != report.TranscriptTruncated {
				return nil, ErrConflict
			}
		} else if !emptyTranscript {
			return nil, ErrConflict
		}
		return &report, nil
	}
}

// Disconnect closes only this Core connection. It never sends close, signal,
// terminate, or abort and therefore cannot kill a live or suspended child.
func (client *Client) Disconnect() error {
	if client == nil {
		return nil
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.stream == nil {
		return nil
	}
	err := client.stream.Close()
	client.stream = nil
	client.codec = nil
	client.poisoned = true
	return err
}

func pendingEvidence(request Request) PendingReplayEvidence {
	return PendingReplayEvidence{ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, RequestDigest: request.RequestDigest, Deadline: request.Deadline}
}

func (client *Client) poisonLocked() {
	if client.stream != nil {
		_ = client.stream.Close()
	}
	client.stream = nil
	client.codec = nil
	client.poisoned = true
}

func validateReconnectWireOptions(options reconnectWireOptions) error {
	request, anchor := options.Request, options.Anchor
	if options.ControlDirectory == nil || options.ControlDirectoryIdentity.validate() != nil ||
		request.SchemaVersion != ReconnectSchema || request.ProtocolRevision != ProtocolRevision || !validID(request.SessionID) || !hex64Pattern.MatchString(request.SessionNonce) ||
		request.PreviousOwnerEpoch == 0 || request.OwnerEpoch <= request.PreviousOwnerEpoch || request.OwnerEpoch > maxSafeJSONInteger ||
		!validDigest(request.PreviousAuthorityHead) || !validDigest(request.CurrentAuthorityHead) || request.PreviousAuthorityHead == request.CurrentAuthorityHead || !validDigest(request.ControlOwnerAcquired) ||
		request.Core.UID == 0 || request.Core.Process.validate() != nil || request.Core.Binary.validate() != nil ||
		request.LastOwnerEpoch == 0 || request.LastOwnerEpoch > request.PreviousOwnerEpoch || request.LastOwnerEpoch > maxSafeJSONInteger || !validDigest(request.LastAuthorityHead) ||
		request.LastCommandSequence > maxSafeJSONInteger || !validDigest(request.LastCommandHead) || request.LastJournalSequence == 0 || request.LastJournalSequence > maxSafeJSONInteger || !validDigest(request.LastJournalHead) ||
		!validID(anchor.SessionID) || !validDigest(anchor.SessionNonceDigest) || anchor.Authority.validate() != nil || anchor.OwnerEpoch == 0 || anchor.OwnerEpoch > maxSafeJSONInteger || !validDigest(anchor.CurrentAuthorityHead) ||
		anchor.CommandSequence > maxSafeJSONInteger || !validDigest(anchor.CommandHead) || anchor.JournalSequence == 0 || anchor.JournalSequence > maxSafeJSONInteger || !validDigest(anchor.JournalHead) ||
		anchor.UID == 0 || anchor.FixedBinary.validate() != nil || anchor.ControlSocket.validate() != nil {
		return ErrInvalid
	}
	if request.SessionID != anchor.SessionID || canonical.DigestBytes([]byte(request.SessionNonce)) != anchor.SessionNonceDigest ||
		request.LastOwnerEpoch != anchor.OwnerEpoch || request.LastAuthorityHead != anchor.CurrentAuthorityHead ||
		request.LastCommandSequence != anchor.CommandSequence || request.LastCommandHead != anchor.CommandHead || request.LastJournalSequence != anchor.JournalSequence || request.LastJournalHead != anchor.JournalHead ||
		request.Core.UID != anchor.UID || request.Core.GID != anchor.GID || !sameBinaryObject(request.Core.Binary, anchor.FixedBinary) {
		return ErrConflict
	}
	if request.PendingRequest != nil {
		pending := *request.PendingRequest
		evidence := pendingEvidence(pending)
		if options.PendingEvidence == nil || *options.PendingEvidence != evidence || projectRequestInvalid(pending) || pending.SessionID != anchor.SessionID || pending.Sequence != anchor.CommandSequence+1 || pending.PreviousCommandDigest != anchor.CommandHead {
			return ErrConflict
		}
	} else if options.PendingEvidence != nil {
		return ErrConflict
	}
	return nil
}

func projectRequestInvalid(request Request) bool {
	_, _, err := projectRequest(request)
	return err != nil
}

func validateReconnectHandshake(response HandshakeResponse, options reconnectWireOptions, observed CoreIdentity) (*Response, HandshakeAnchor, error) {
	expected := options.Anchor
	expected.OwnerEpoch = options.Request.OwnerEpoch
	expected.CurrentAuthorityHead = options.Request.CurrentAuthorityHead
	var replay *Response
	switch response.Reconciliation {
	case ReconciliationUnchanged:
		if options.Request.PendingRequest == nil {
			if response.ReplayedResponse != nil {
				return nil, HandshakeAnchor{}, ErrConflict
			}
			break
		}
		if response.ReplayedResponse == nil || ValidateResponseBinding(*response.ReplayedResponse, *options.Request.PendingRequest) != nil {
			return nil, HandshakeAnchor{}, ErrConflict
		}
		post, err := commandPostAnchor(options.Anchor, *options.Request.PendingRequest, *response.ReplayedResponse)
		if err != nil {
			return nil, HandshakeAnchor{}, err
		}
		expected.CommandSequence = post.CommandSequence
		expected.CommandHead = post.CommandHead
		expected.JournalSequence = post.JournalSequence
		expected.JournalHead = post.JournalHead
		replay = cloneResponsePointer(response.ReplayedResponse)
	case ReconciliationIntentPending:
		if options.Request.PendingRequest == nil {
			return nil, HandshakeAnchor{}, ErrConflict
		}
		intentHead, _, err := expectedPendingJournalHeads(options.Anchor, *options.Request.PendingRequest, nil)
		if err != nil {
			return nil, HandshakeAnchor{}, err
		}
		expected.JournalSequence++
		expected.JournalHead = intentHead
	case ReconciliationReceiptCommitted:
		if options.Request.PendingRequest == nil || response.ReplayedResponse == nil || ValidateResponseBinding(*response.ReplayedResponse, *options.Request.PendingRequest) != nil {
			return nil, HandshakeAnchor{}, ErrConflict
		}
		post, err := commandPostAnchor(options.Anchor, *options.Request.PendingRequest, *response.ReplayedResponse)
		if err != nil {
			return nil, HandshakeAnchor{}, err
		}
		expected.CommandSequence = post.CommandSequence
		expected.CommandHead = post.CommandHead
		expected.JournalSequence = post.JournalSequence
		expected.JournalHead = post.JournalHead
		replay = cloneResponsePointer(response.ReplayedResponse)
	default:
		return nil, HandshakeAnchor{}, ErrConflict
	}
	if ValidateHandshakeBinding(response, expected, observed) != nil {
		return nil, HandshakeAnchor{}, ErrConflict
	}
	return replay, expected, nil
}

func commandPostAnchor(pre HandshakeAnchor, request Request, response Response) (HandshakeAnchor, error) {
	projection, _, err := projectRequest(request)
	if err != nil || ValidateResponseBinding(response, request) != nil {
		return HandshakeAnchor{}, ErrConflict
	}
	_, receiptHead, err := expectedPendingJournalHeads(pre, request, &response)
	if err != nil {
		return HandshakeAnchor{}, ErrConflict
	}
	post := pre
	post.CommandSequence = request.Sequence
	post.CommandHead = response.CommandHead
	post.JournalSequence = pre.JournalSequence + 2
	post.JournalHead = receiptHead
	post.CurrentAuthorityHead = commandPostAuthorityHead(pre.CurrentAuthorityHead, request, response, projection)
	return post, nil
}

func expectedPendingJournalHeads(anchor HandshakeAnchor, request Request, response *Response) (string, string, error) {
	projection, _, err := projectRequest(request)
	if err != nil {
		return "", "", err
	}
	// The mechanics record base is the last authenticated pre-command anchor
	// (A0). request.CurrentAuthorityHead is the request projection authority At
	// and is allowed to differ; placing At in the record base would forge a
	// journal transition the supervisor never authenticated.
	base := journalRecord{SchemaVersion: JournalSchema, SessionID: anchor.SessionID, SessionNonceDigest: anchor.SessionNonceDigest, Authority: anchor.Authority, OwnerEpoch: anchor.OwnerEpoch, CurrentAuthorityHead: anchor.CurrentAuthorityHead}
	intent := base
	intent.JournalSequence = anchor.JournalSequence + 1
	intent.Kind = journalCommandIntent
	intent.Request = &projection
	intent.PreviousRecordDigest = anchor.JournalHead
	intent.RecordDigest, err = intent.detachedDigest()
	if err != nil || intent.validate(anchor.JournalHead, anchor.JournalSequence+1) != nil {
		return "", "", ErrConflict
	}
	if response == nil {
		return intent.RecordDigest, "", nil
	}
	receipt := base
	receipt.JournalSequence = anchor.JournalSequence + 2
	receipt.Kind = journalCommandReceipt
	receipt.Request = &projection
	receipt.Response = cloneResponsePointer(response)
	receipt.PreviousRecordDigest = intent.RecordDigest
	receipt.RecordDigest, err = receipt.detachedDigest()
	if err != nil || receipt.validate(intent.RecordDigest, anchor.JournalSequence+2) != nil {
		return "", "", ErrConflict
	}
	return intent.RecordDigest, receipt.RecordDigest, nil
}

func runBoundedTransport(ctx context.Context, stream deadlineStream, deadline time.Time, operation func() error) error {
	if ctx == nil || stream == nil || operation == nil || deadline.IsZero() {
		if stream != nil {
			_ = stream.Close()
		}
		return ErrInvalid
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := stream.SetDeadline(deadline); err != nil {
		_ = stream.Close()
		return ErrIntervention
	}
	stop := make(chan struct{})
	type deadlineResult struct {
		cancelled bool
		err       error
	}
	joined := make(chan deadlineResult, 1)
	go func() {
		select {
		case <-ctx.Done():
			err := stream.SetDeadline(time.Now())
			if err != nil {
				_ = stream.Close()
			}
			joined <- deadlineResult{cancelled: true, err: err}
		case <-stop:
			joined <- deadlineResult{}
		}
	}()
	operationErr := operation()
	close(stop)
	deadlineState := <-joined
	clearErr := stream.SetDeadline(time.Time{})
	if operationErr != nil || deadlineState.cancelled || ctx.Err() != nil || deadlineState.err != nil || clearErr != nil {
		_ = stream.Close()
		if operationErr != nil {
			return operationErr
		}
		return ErrIntervention
	}
	return nil
}

func commandPostAuthorityHead(a0 string, request Request, response Response, projection requestProjection) string {
	if request.Command == CommandBindAuthority {
		if response.Status == "ok" {
			return projection.NextAuthorityHead
		}
		return a0
	}
	// Every durable non-bind receipt advances mechanics to the request's At,
	// including a mechanics rejection. Admission-only rejections never pass
	// exact response binding and therefore cannot reach this function.
	return request.CurrentAuthorityHead
}
