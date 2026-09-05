package processsupervisor

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// BootstrapRequestV2 exposes the closed ADR 0079 shape, not a v1 conversion.
type BootstrapRequestV2 = bootstrapRequestV2
type HandshakeResponseV2 = handshakeResponseV2

type StartOptionsV2 struct {
	FixedMarshalPath string
	ControlDirectory *os.File
	Bootstrap        BootstrapRequestV2
}

// SessionAnchorV2 must travel intact through producer-owned evidence. Binding
// holds generation-neutral object identities; Generation has no defaults and
// is always checked as a complete set, including both genesis digests.
type SessionAnchorV2 struct {
	Generation       ProtocolGenerationContract `json:"generation"`
	Binding          HandshakeAnchor            `json:"binding"`
	ControlDirectory ControlDirectoryIdentity   `json:"controlDirectory"`
}

func (a SessionAnchorV2) Validate() error {
	b := a.Binding
	if a.Generation != DormantV2ProtocolContract() || a.ControlDirectory.validate() != nil || !validID(b.SessionID) || !validDigest(b.SessionNonceDigest) || b.Authority.validate() != nil ||
		b.OwnerEpoch == 0 || b.OwnerEpoch > maxSafeJSONInteger || !validDigest(b.CurrentAuthorityHead) || b.CommandSequence > MaxCommands || !validDigest(b.CommandHead) ||
		b.JournalSequence == 0 || b.JournalSequence > MaxCommands*2+1 || !validDigest(b.JournalHead) || b.UID == 0 || b.FixedBinary.validate() != nil || b.ControlSocket.validate() != nil || b.ControlFiles.validate() != nil {
		return ErrInvalid
	}
	if b.CommandSequence == 0 && b.CommandHead != commandGenesisDigestV2 {
		return ErrConflict
	}
	if b.JournalSequence != b.CommandSequence*2+1 && b.JournalSequence != b.CommandSequence*2+2 {
		return ErrConflict
	}
	if a.ControlDirectory.UID != b.UID || a.ControlDirectory.GID != b.GID || b.ControlSocket.UID != b.UID || b.ControlSocket.GID != b.GID ||
		b.ControlFiles.Nonce.UID != b.UID || b.ControlFiles.Nonce.GID != b.GID || b.ControlFiles.Journal.UID != b.UID || b.ControlFiles.Journal.GID != b.GID {
		return ErrConflict
	}
	return nil
}

func ValidateHandshakeBindingV2(response HandshakeResponseV2, anchor SessionAnchorV2, observed CoreIdentity) error {
	if anchor.Validate() != nil || response.validate() != nil {
		return ErrInvalid
	}
	a := anchor.Binding
	if response.SessionID != a.SessionID || response.SessionNonceDigest != a.SessionNonceDigest || response.OwnerEpoch != a.OwnerEpoch || response.CurrentAuthorityHead != a.CurrentAuthorityHead ||
		response.CommandSequence != a.CommandSequence || response.CommandHead != a.CommandHead || response.JournalSequence != a.JournalSequence || response.JournalHead != a.JournalHead ||
		response.ControlSocket != a.ControlSocket || response.ControlFiles != a.ControlFiles || observed.UID != a.UID || observed.GID != a.GID ||
		observed.Process != response.SupervisorProcess || observed.Binary != response.SupervisorBinary || !sameBinaryObject(observed.Binary, a.FixedBinary) {
		return ErrConflict
	}
	return nil
}

// ValidateInitialHandshakeBindingV2 also recomputes the only admissible
// genesis journal record from the authenticated bootstrap authority. A valid
// looking arbitrary journal hash cannot become durable started evidence.
func ValidateInitialHandshakeBindingV2(response HandshakeResponseV2, anchor SessionAnchorV2, observed CoreIdentity) error {
	if ValidateHandshakeBindingV2(response, anchor, observed) != nil || response.Reconciliation != "" || response.ReplayedResponse != nil ||
		anchor.Binding.CommandSequence != 0 || anchor.Binding.CommandHead != commandGenesisDigestV2 || anchor.Binding.JournalSequence != 1 {
		return ErrConflict
	}
	created := commandBaseV2(anchor)
	created.Kind, created.JournalSequence, created.PreviousRecordDigest = journalSessionCreated, 1, journalGenesisDigestV2
	head, err := created.detachedDigest()
	if err != nil || head != anchor.Binding.JournalHead {
		return ErrConflict
	}
	return nil
}

// PreparedCommandEvidenceV2 is secret-free. No private request, nonce, argv,
// environment values or input bytes can be recovered from this value.
type PreparedCommandEvidenceV2 struct {
	PreCommand            SessionAnchorV2           `json:"preCommand"`
	Command               CommandName               `json:"command"`
	CommandID             string                    `json:"commandId"`
	Sequence              uint64                    `json:"sequence"`
	PreviousCommandDigest string                    `json:"previousCommandDigest"`
	CurrentAuthorityHead  string                    `json:"currentAuthorityHead"`
	RequestDigest         string                    `json:"requestDigest"`
	PayloadDigest         string                    `json:"payloadDigest"`
	JournalRequestDigest  string                    `json:"journalRequestDigest"`
	Deadline              string                    `json:"deadline"`
	Projection            PreparedCommandProjection `json:"projection"`
	EvidenceDigest        string                    `json:"evidenceDigest"`
}

func (e PreparedCommandEvidenceV2) integrityDigest() (string, error) {
	e.EvidenceDigest = ""
	return digestValue(e)
}
func (e PreparedCommandEvidenceV2) Validate() error {
	b := e.PreCommand.Binding
	if e.PreCommand.Validate() != nil || b.JournalSequence != b.CommandSequence*2+1 || !validCommand(e.Command) || !validID(e.CommandID) || e.Sequence != b.CommandSequence+1 || e.Sequence > MaxCommands ||
		e.PreviousCommandDigest != b.CommandHead || !validDigest(e.CurrentAuthorityHead) || !validDigest(e.RequestDigest) || !validDigest(e.PayloadDigest) || !validDigest(e.JournalRequestDigest) ||
		commandRequiresPreAuthorityHead(e.Command) && e.CurrentAuthorityHead != b.CurrentAuthorityHead || validatePreparedProjection(e.Command, e.Projection) != nil {
		return ErrInvalid
	}
	if e.Command == CommandSpawn && e.Projection.SourceGateRevision != SourceGateRevisionV1 {
		return ErrIntervention
	}
	if _, err := parseDeadline(e.Deadline); err != nil {
		return ErrInvalid
	}
	want, err := e.integrityDigest()
	if err != nil || !validDigest(e.EvidenceDigest) || want != e.EvidenceDigest {
		return ErrConflict
	}
	return nil
}

type PreparedCommandV2 struct {
	request  requestV2
	evidence PreparedCommandEvidenceV2
}

func (p PreparedCommandV2) Evidence() PreparedCommandEvidenceV2 { return p.evidence }

func PrepareCommandV2(anchor SessionAnchorV2, options CommandOptions, payload any) (PreparedCommandV2, error) {
	if anchor.Validate() != nil || options.Deadline.IsZero() {
		return PreparedCommandV2{}, ErrInvalid
	}
	raw, err := canonicalValue(payload)
	if err != nil {
		return PreparedCommandV2{}, err
	}
	r := requestV2{SchemaVersion: requestSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: anchor.Binding.SessionID, Command: options.Command, CommandID: options.CommandID, Sequence: options.Sequence, PreviousCommandDigest: options.PreviousCommandDigest,
		CurrentAuthorityHead: options.CurrentAuthorityHead, Deadline: options.Deadline.UTC().Format(time.RFC3339Nano), Payload: raw}
	r.RequestDigest, err = digestValue(requestDigestInputV2{SchemaVersion: r.SchemaVersion, ProtocolRevision: r.ProtocolRevision, LaunchChildProtocolRevision: r.LaunchChildProtocolRevision, MechanicsIdentity: r.MechanicsIdentity,
		SessionID: r.SessionID, Command: r.Command, CommandID: r.CommandID, Sequence: r.Sequence, PreviousCommandDigest: r.PreviousCommandDigest, CurrentAuthorityHead: r.CurrentAuthorityHead, Deadline: r.Deadline, Payload: r.Payload})
	if err != nil {
		return PreparedCommandV2{}, err
	}
	journalRequest, _, err := projectRequestV2(r)
	if err != nil {
		return PreparedCommandV2{}, err
	}
	projection, err := projectPreparedPayload(r.Command, payload)
	if err != nil {
		return PreparedCommandV2{}, err
	}
	e := PreparedCommandEvidenceV2{PreCommand: anchor, Command: r.Command, CommandID: r.CommandID, Sequence: r.Sequence, PreviousCommandDigest: r.PreviousCommandDigest,
		CurrentAuthorityHead: r.CurrentAuthorityHead, RequestDigest: r.RequestDigest, PayloadDigest: canonical.DigestBytes(raw), Deadline: r.Deadline, Projection: projection}
	e.JournalRequestDigest, err = digestValue(journalRequest)
	if err != nil {
		return PreparedCommandV2{}, err
	}
	e.EvidenceDigest, err = e.integrityDigest()
	if err != nil || e.Validate() != nil {
		return PreparedCommandV2{}, ErrInvalid
	}
	return PreparedCommandV2{request: r, evidence: e}, nil
}

func RebuildPreparedCommandV2(expected PreparedCommandEvidenceV2, payload any) (PreparedCommandV2, error) {
	if expected.Validate() != nil {
		return PreparedCommandV2{}, ErrInvalid
	}
	deadline, err := parseDeadline(expected.Deadline)
	if err != nil {
		return PreparedCommandV2{}, err
	}
	p, err := PrepareCommandV2(expected.PreCommand, CommandOptions{Command: expected.Command, CommandID: expected.CommandID, Sequence: expected.Sequence,
		PreviousCommandDigest: expected.PreviousCommandDigest, CurrentAuthorityHead: expected.CurrentAuthorityHead, Deadline: deadline}, payload)
	if err != nil || p.evidence != expected {
		return PreparedCommandV2{}, ErrConflict
	}
	return p, nil
}

type VerifiedCommandOutcomeV2 struct {
	Preparation       PreparedCommandEvidenceV2
	JournalRequest    string
	PostCommand       SessionAnchorV2
	Status            string
	ReasonCode        string
	ReceiptDigest     string
	ObservationDigest string
	CommandHead       string
	TranscriptDigest  string
	StdoutBytes       uint64
	StderrBytes       uint64
	Truncated         bool
	ProcessReport     *ProcessReport
}

func verifiedCommandOutcomeV2(prepared PreparedCommandV2, response responseV2) (VerifiedCommandOutcomeV2, error) {
	if prepared.evidence.Validate() != nil || validateV2ResponseBinding(response, prepared.request) != nil {
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	r := prepared.request
	pre := prepared.evidence.PreCommand
	base := journalRecordV2{SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: pre.Binding.SessionID, SessionNonceDigest: pre.Binding.SessionNonceDigest, Authority: pre.Binding.Authority, OwnerEpoch: pre.Binding.OwnerEpoch, CurrentAuthorityHead: pre.Binding.CurrentAuthorityHead}
	_, head, err := expectedPendingJournalHeadsV2(base, pre.Binding.JournalSequence, pre.Binding.JournalHead, r, &response)
	if err != nil {
		return VerifiedCommandOutcomeV2{}, err
	}
	post := pre
	post.Binding.CommandSequence, post.Binding.CommandHead = r.Sequence, response.CommandHead
	post.Binding.JournalSequence, post.Binding.JournalHead = pre.Binding.JournalSequence+2, head
	projection, _, err := projectRequestV2(r)
	if err != nil {
		return VerifiedCommandOutcomeV2{}, err
	}
	post.Binding.CurrentAuthorityHead = commandPostAuthorityHeadV2(pre.Binding.CurrentAuthorityHead, projection, response)
	if post.Validate() != nil {
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil {
		return VerifiedCommandOutcomeV2{}, ErrInvalid
	}
	outcome := VerifiedCommandOutcomeV2{Preparation: prepared.evidence, PostCommand: post, Status: response.Status, ReasonCode: response.ReasonCode, ReceiptDigest: response.ReceiptDigest,
		ObservationDigest: response.ObservationDigest, CommandHead: response.CommandHead, TranscriptDigest: result.TranscriptDigest, StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes, Truncated: result.Truncated}
	journalRequest, err := canonicalValue(projection)
	if err != nil {
		return VerifiedCommandOutcomeV2{}, err
	}
	outcome.JournalRequest = string(journalRequest)
	if response.Status == "ok" && r.Command != CommandBindAuthority && r.Command != CommandAbortUnbound {
		var report ProcessReport
		if strictCanonicalDecode(result.Payload, &report) != nil || ValidateDormantV2ProcessReport(report) != nil {
			return VerifiedCommandOutcomeV2{}, ErrConflict
		}
		outcome.ProcessReport = &report
	}
	if err := outcome.Validate(); err != nil {
		return VerifiedCommandOutcomeV2{}, err
	}
	return outcome, nil
}

// ClientV2 owns a connection, not workload lifetime. The owning Core must
// persist Prepare's exact evidence before invoking DoPrepared.
type ClientV2 struct {
	mu        sync.Mutex
	stream    deadlineStream
	codec     *ProtocolCodec
	anchor    SessionAnchorV2
	handshake HandshakeResponseV2
	pending   *PreparedCommandEvidenceV2
	recovery  *SessionRecoveryEvidenceV2
	poisoned  bool
}

func newClientV2(stream deadlineStream, handshake HandshakeResponseV2, anchor SessionAnchorV2, observed CoreIdentity) (*ClientV2, error) {
	if stream == nil || ValidateHandshakeBindingV2(handshake, anchor, observed) != nil {
		return nil, ErrInvalid
	}
	codec, err := NewProtocolCodec(stream)
	if err != nil {
		return nil, err
	}
	if handshake.ReplayedResponse != nil {
		copy := *handshake.ReplayedResponse
		copy.Payload = append([]byte(nil), copy.Payload...)
		handshake.ReplayedResponse = &copy
	}
	return &ClientV2{stream: stream, codec: codec, anchor: anchor, handshake: handshake, poisoned: handshake.Reconciliation == ReconciliationIntentPending}, nil
}

func (c *ClientV2) Anchor() SessionAnchorV2 {
	if c == nil {
		return SessionAnchorV2{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.anchor
}

func (c *ClientV2) Handshake() HandshakeResponseV2 {
	if c == nil {
		return HandshakeResponseV2{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.handshake
	if h.ReplayedResponse != nil {
		r := *h.ReplayedResponse
		r.Payload = append([]byte(nil), r.Payload...)
		h.ReplayedResponse = &r
	}
	return h
}
func (c *ClientV2) Pending() (PreparedCommandEvidenceV2, bool) {
	if c == nil {
		return PreparedCommandEvidenceV2{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return PreparedCommandEvidenceV2{}, false
	}
	return *c.pending, true
}
func (c *ClientV2) Prepare(options CommandOptions, payload any) (PreparedCommandV2, error) {
	if c == nil {
		return PreparedCommandV2{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.stream == nil {
		return PreparedCommandV2{}, ErrUnavailable
	}
	return PrepareCommandV2(c.anchor, options, payload)
}
func (c *ClientV2) Disconnect() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream == nil {
		return nil
	}
	err := c.stream.Close()
	c.stream = nil
	c.poisoned = true
	return err
}

func (c *ClientV2) DoPrepared(ctx context.Context, prepared PreparedCommandV2) (VerifiedCommandOutcomeV2, error) {
	if c == nil {
		return VerifiedCommandOutcomeV2{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.stream == nil || c.codec == nil {
		return VerifiedCommandOutcomeV2{}, ErrUnavailable
	}
	if ctx == nil || ctx.Err() != nil {
		return VerifiedCommandOutcomeV2{}, ErrInvalid
	}
	if prepared.evidence.Validate() != nil || prepared.evidence.PreCommand != c.anchor {
		return VerifiedCommandOutcomeV2{}, ErrConflict
	}
	deadline, err := parseDeadline(prepared.evidence.Deadline)
	limit, ok := commandDeadlineLimit(prepared.request.Command)
	remaining := time.Until(deadline)
	if err != nil || !ok || remaining <= 0 || remaining > limit {
		return VerifiedCommandOutcomeV2{}, ErrInvalid
	}
	pending := prepared.evidence
	c.pending = &pending
	var response responseV2
	err = runBoundedTransport(ctx, c.stream, deadline, func() error {
		if err := c.codec.Write(prepared.request); err != nil {
			return err
		}
		if err := c.codec.Read(&response); err != nil {
			return err
		}
		return validateV2ResponseBinding(response, prepared.request)
	})
	if err != nil {
		c.poisoned = true
		_ = c.stream.Close()
		return VerifiedCommandOutcomeV2{}, ErrIntervention
	}
	outcome, err := verifiedCommandOutcomeV2(prepared, response)
	if err != nil {
		c.poisoned = true
		_ = c.stream.Close()
		return VerifiedCommandOutcomeV2{}, ErrIntervention
	}
	c.anchor = outcome.PostCommand
	c.pending = nil
	return outcome, nil
}
