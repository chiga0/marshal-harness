package processsupervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

// AttachSchema and AttachObservationSchema are the wire envelopes for the
// read-only Attach primitive defined by ADR 0067 §4. Attach never carries the
// raw session nonce on the wire response side; only its digest (already part
// of the previous anchor) is bound into the detached request digest.
const (
	AttachSchema             = "marshal.process-supervisor-attach.v1"
	AttachObservationSchema  = "marshal.process-supervisor-attach-observation.v1"
	attachObserverIdentityV1 = "darwin-fixed-process-supervisor-attach-v1"
)

// AttachOwnerAcquisition is the secret-free projection of the exact current
// repository-owner fact that the caller has already acquired and replayed as a
// control-owner-bound successor. The AttachOwnerVerifier, not this value, is
// the owner capability: it must keep the physical owner lock held for the
// complete borrowed callback. Attach only authenticates that the live
// Supervisor still matches this acquisition; it never acquires or changes it.
type AttachOwnerAcquisition struct {
	AuthorityNamespaceID     string          `json:"authorityNamespaceId"`
	RepositoryIdentityDigest string          `json:"repositoryIdentityDigest"`
	OwnerEpoch               uint64          `json:"ownerEpoch"`
	OwnerUID                 uint32          `json:"ownerUid"`
	OwnerGID                 uint32          `json:"ownerGid"`
	OwnerProcess             ProcessIdentity `json:"ownerProcess"`
	OwnerBinary              BinaryIdentity  `json:"ownerBinary"`
	ObserverIdentity         string          `json:"observerIdentity"`
	ObservedAt               string          `json:"observedAt"`
	PreviousFactDigest       string          `json:"previousFactDigest,omitempty"`
	FactDigest               string          `json:"factDigest"`
}

func (acquisition AttachOwnerAcquisition) validate() error {
	observedAt, err := time.Parse(time.RFC3339Nano, acquisition.ObservedAt)
	birth := time.Unix(acquisition.OwnerProcess.BirthSeconds, acquisition.OwnerProcess.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if !validID(acquisition.AuthorityNamespaceID) || !validDigest(acquisition.RepositoryIdentityDigest) ||
		acquisition.OwnerEpoch == 0 || acquisition.OwnerEpoch > maxSafeJSONInteger || acquisition.OwnerUID == 0 ||
		acquisition.OwnerProcess.validate() != nil || acquisition.OwnerBinary.validate() != nil || !validID(acquisition.ObserverIdentity) ||
		err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != acquisition.ObservedAt || observedAt.Before(birth) ||
		acquisition.PreviousFactDigest != "" && !validDigest(acquisition.PreviousFactDigest) || !validDigest(acquisition.FactDigest) {
		return ErrInvalid
	}
	return nil
}

// AttachOwnerBoundFact is the exact, already-fsynced and replayed RB1
// control-owner-bound successor. It deliberately carries both predecessor and
// successor coordinates so a sibling successor or stale head cannot be
// substituted by an integration caller. Attach re-derives it against the live
// Supervisor but never appends or replays it.
type AttachOwnerBoundFact struct {
	Authority                      AuthorityTuple `json:"authority"`
	PreviousAttemptRevision        uint64         `json:"previousAttemptRevision"`
	PreviousAttemptHead            string         `json:"previousAttemptHead"`
	AttemptRevision                uint64         `json:"attemptRevision"`
	AttemptHead                    string         `json:"attemptHead"`
	ControlOwnerAcquiredFactDigest string         `json:"controlOwnerAcquiredFactDigest"`
	OwnerEpoch                     uint64         `json:"ownerEpoch"`
	FactDigest                     string         `json:"factDigest"`
}

func (fact AttachOwnerBoundFact) validate() error {
	if fact.Authority.validate() != nil || fact.PreviousAttemptRevision == 0 || fact.PreviousAttemptRevision >= maxSafeJSONInteger ||
		fact.AttemptRevision != fact.PreviousAttemptRevision+1 || !validDigest(fact.PreviousAttemptHead) || !validDigest(fact.AttemptHead) ||
		fact.PreviousAttemptHead == fact.AttemptHead || !validDigest(fact.ControlOwnerAcquiredFactDigest) ||
		fact.OwnerEpoch == 0 || fact.OwnerEpoch > maxSafeJSONInteger || !validDigest(fact.FactDigest) {
		return ErrInvalid
	}
	return nil
}

// AttachAuthority binds one read-only Attach to both sides of recovery: the
// exact previous Supervisor mechanics authority anchor and the current durable
// owner successor. ChildObservationDigest is the durable process-started/last
// observation anchor; Child is independently checked against the held mechanics
// child identity. Every field is re-derived by the caller from durable ledgers
// and held owner state; Attach grants none of it.
type AttachAuthority struct {
	PreviousSupervisor     HandshakeAnchor        `json:"previousSupervisor"`
	Supervisor             ProcessIdentity        `json:"supervisor"`
	CurrentAcquisition     AttachOwnerAcquisition `json:"currentAcquisition"`
	CurrentOwnerBoundFact  AttachOwnerBoundFact   `json:"currentOwnerBoundFact"`
	Child                  ProcessIdentity        `json:"child"`
	ChildObservationDigest string                 `json:"childObservationDigest"`
}

func (authority AttachAuthority) validate() error {
	previous, acquisition, bound := authority.PreviousSupervisor, authority.CurrentAcquisition, authority.CurrentOwnerBoundFact
	if !validID(previous.SessionID) || !validDigest(previous.SessionNonceDigest) || previous.Authority.validate() != nil ||
		previous.OwnerEpoch == 0 || previous.OwnerEpoch > maxSafeJSONInteger || !validDigest(previous.CurrentAuthorityHead) ||
		previous.CommandSequence > maxSafeJSONInteger || !validDigest(previous.CommandHead) || previous.JournalSequence == 0 ||
		previous.JournalSequence > maxSafeJSONInteger || !validDigest(previous.JournalHead) || previous.UID == 0 ||
		previous.FixedBinary.validate() != nil || previous.ControlSocket.validate() != nil || previous.ControlFiles.validate() != nil || authority.Supervisor.validate() != nil ||
		acquisition.validate() != nil || bound.validate() != nil || authority.Child.validate() != nil || !validDigest(authority.ChildObservationDigest) {
		return ErrInvalid
	}
	if acquisition.AuthorityNamespaceID != previous.Authority.AuthorityNamespaceID || bound.Authority != previous.Authority ||
		bound.ControlOwnerAcquiredFactDigest != acquisition.FactDigest || bound.OwnerEpoch != acquisition.OwnerEpoch {
		return ErrConflict
	}
	return nil
}

// AttachOwnerVerifier is the narrow acquisition-bound capability required by
// Attach. Implementations must invoke fn synchronously and exactly once while
// holding the exact repository physical owner lock and while the complete
// acquisition/owner-bound Attempt successor in authority remains current. Any
// asynchronous, repeated, or post-return invocation fails closed.
type AttachOwnerVerifier interface {
	WithCurrentAttachOwner(ctx context.Context, authority AttachAuthority, fn func() error) error
}

// AttachOptions is the complete input to one read-only Attach. The caller
// supplies the held control directory descriptor, its frozen identity, the
// fixed Marshal binary path, the exact authority, and the still-held owner
// verifier. Attach never opens a second control root, child, or pipe.
type AttachOptions struct {
	FixedMarshalPath         string
	ControlDirectory         *os.File
	ControlDirectoryIdentity ControlDirectoryIdentity
	Authority                AttachAuthority
	OwnerVerifier            AttachOwnerVerifier
}

// AttachObservation is safe to persist. It contains no raw nonce and binds the
// fixed peer observation to the exact owner successor and previous Supervisor
// anchor admitted by the wire exchange. It is the only value a borrower may
// copy out of the callback.
type AttachObservation struct {
	SchemaVersion          string                   `json:"schemaVersion"`
	ProtocolRevision       string                   `json:"protocolRevision"`
	RequestDigest          string                   `json:"requestDigest"`
	ResponseDigest         string                   `json:"responseDigest"`
	PreviousSupervisor     HandshakeAnchor          `json:"previousSupervisor"`
	Handshake              HandshakeResponse        `json:"handshake"`
	Supervisor             ProcessIdentity          `json:"supervisor"`
	CurrentAcquisition     AttachOwnerAcquisition   `json:"currentAcquisition"`
	CurrentOwnerBoundFact  AttachOwnerBoundFact     `json:"currentOwnerBoundFact"`
	Child                  ProcessIdentity          `json:"child"`
	ChildObservationDigest string                   `json:"childObservationDigest"`
	ControlDirectory       ControlDirectoryIdentity `json:"controlDirectory"`
	Peer                   CoreIdentity             `json:"peer"`
	ObservedAt             string                   `json:"observedAt"`
}

func (observation AttachObservation) validate() error {
	observedAt, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
	authority := AttachAuthority{PreviousSupervisor: observation.PreviousSupervisor, Supervisor: observation.Supervisor, CurrentAcquisition: observation.CurrentAcquisition, CurrentOwnerBoundFact: observation.CurrentOwnerBoundFact, Child: observation.Child, ChildObservationDigest: observation.ChildObservationDigest}
	if observation.SchemaVersion != AttachObservationSchema || observation.ProtocolRevision != ProtocolRevision ||
		!validDigest(observation.RequestDigest) || !validDigest(observation.ResponseDigest) || authority.validate() != nil ||
		observation.ControlDirectory.validate() != nil || observation.Peer.UID == 0 || observation.Peer.Process.validate() != nil || observation.Peer.Binary.validate() != nil ||
		err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != observation.ObservedAt {
		return ErrInvalid
	}
	core := CoreIdentity{UID: observation.CurrentAcquisition.OwnerUID, GID: observation.CurrentAcquisition.OwnerGID, Process: observation.CurrentAcquisition.OwnerProcess, Binary: observation.CurrentAcquisition.OwnerBinary}
	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, Core: core, ControlDirectoryIdentity: observation.ControlDirectory, Authority: authority, RequestDigest: observation.RequestDigest}
	requestDigest, digestErr := request.detachedDigest()
	response := attachResponse{
		SchemaVersion: AttachObservationSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-attached", RequestDigest: observation.RequestDigest,
		Handshake: observation.Handshake, CurrentAcquisition: observation.CurrentAcquisition, CurrentOwnerBoundFact: observation.CurrentOwnerBoundFact,
		Child: observation.Child, ChildObservationDigest: observation.ChildObservationDigest, ObserverIdentity: attachObserverIdentityV1, ObservedAt: observation.ObservedAt,
	}
	response.ResponseDigest = observation.ResponseDigest
	responseDigest, responseErr := response.detachedDigest()
	if digestErr != nil || requestDigest != observation.RequestDigest || responseErr != nil || responseDigest != observation.ResponseDigest ||
		ValidateHandshakeBinding(observation.Handshake, observation.PreviousSupervisor, observation.Peer) != nil || observation.Handshake.SupervisorProcess != observation.Supervisor {
		return ErrConflict
	}
	return nil
}

// ValidateAttachObservation rechecks one secret-free observation without
// trusting the caller that obtained it. The raw nonce is intentionally absent;
// its digest is already part of the detached request and previous anchor.
func ValidateAttachObservation(observation AttachObservation) error { return observation.validate() }

// AttachedSession is a synchronous, callback-scoped view of one authenticated
// read-only Attach transport. It deliberately exposes no command or mechanics
// method. Observation may be copied and persisted, but the borrowed session
// itself becomes unusable as soon as the callback returns: any saved, repeated,
// cross-goroutine, or post-callback use fails closed.
type AttachedSession struct{ guard *attachedSessionGuard }

type attachedSessionGuard struct {
	mu                sync.Mutex
	cond              *sync.Cond
	active            bool
	observed          bool
	inFlight          int
	escaped           bool
	violated          bool
	borrowerGoroutine uint64
	observation       AttachObservation
	// rebind transport, set by WithAttached before the borrower runs. The
	// connection and codec are the same ones that carried the Attach exchange;
	// ExecutePreparedBindAuthority never reopens a second connection.
	connection       *net.UnixConn
	codec            *ProtocolCodec
	anchor           HandshakeAnchor
	commandAttempted bool
	commandExecuted  bool
	executedCommand  CommandName
	postCommand      HandshakeAnchor
}

func newAttachedSession(observation AttachObservation) *AttachedSession {
	guard := &attachedSessionGuard{active: true, observation: observation}
	guard.cond = sync.NewCond(&guard.mu)
	return &AttachedSession{guard: guard}
}

// newRebindAttachedSession equips the borrowed session with the same
// connection, codec and anchor that carried the Attach exchange, so one
// already-persisted bind-authority(owner-successor) PreparedCommand can be
// executed on that transport inside the callback. The anchor is the exact
// previous Supervisor anchor re-derived from durable authority.
func newRebindAttachedSession(observation AttachObservation, connection *net.UnixConn, codec *ProtocolCodec, anchor HandshakeAnchor) *AttachedSession {
	session := newAttachedSession(observation)
	session.guard.connection = connection
	session.guard.codec = codec
	session.guard.anchor = anchor
	return session
}

// Observation returns the authenticated, secret-free observation exactly once
// during the borrowed callback. A saved session, concurrent/second use, or use
// after callback return fails closed.
func (session *AttachedSession) Observation() (AttachObservation, error) {
	if session == nil || session.guard == nil {
		return AttachObservation{}, ErrConflict
	}
	guard := session.guard
	guard.mu.Lock()
	if !guard.active || guard.observed || currentGoroutineID() != guard.borrowerGoroutine {
		guard.violated = true
		guard.mu.Unlock()
		return AttachObservation{}, ErrConflict
	}
	guard.observed = true
	guard.inFlight++
	observation := guard.observation
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.inFlight--
		if guard.inFlight == 0 {
			guard.cond.Broadcast()
		}
		guard.mu.Unlock()
	}()
	return observation, nil
}

// ExecutePreparedBindAuthority runs exactly one already-persisted
// bind-authority(owner-successor) PreparedCommand on the same authenticated
// Attach transport, within the same borrowed callback and goroutine (ADR 0067
// §4.6). It accepts only bind-authority, only a PreparedCommand whose
// PreCommand is the exact Attach anchor, and only after Observation has been
// consumed. A second call, a cross-goroutine call, a non-bind-authority
// command, or any anchor drift fails closed and poisons the session. The
// connection is never reopened; on transport failure the caller's
// post-callback snapshot check fails closed rather than guessing an outcome.
func (session *AttachedSession) ExecutePreparedBindAuthority(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	return session.executePreparedContinuation(ctx, prepared, CommandBindAuthority)
}

// ExecutePreparedCollect runs exactly one already-persisted Collect
// PreparedCommand on the same authenticated Attach transport. It is the
// path-free production result continuation frozen by ADR 0071: callers must
// consume Observation first, may execute no second command in the callback,
// and receive only the verified secret-free outcome.
func (session *AttachedSession) ExecutePreparedCollect(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	return session.executePreparedContinuation(ctx, prepared, CommandCollect)
}

// ExecutePreparedInspect runs exactly one already-persisted Inspect
// PreparedCommand on the same authenticated Attach transport. Keeping this
// operation in the explicit AttachedSession closed set prevents terminal
// continuation callers from obtaining a generic command channel.
func (session *AttachedSession) ExecutePreparedInspect(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	return session.executePreparedContinuation(ctx, prepared, CommandInspect)
}

// ExecutePreparedClose runs exactly one already-persisted Close
// PreparedCommand on the same authenticated Attach transport. The caller must
// separately recover the committed Close receipt and authenticate supervisor
// absence before recording the business lifecycle transition.
func (session *AttachedSession) ExecutePreparedClose(ctx context.Context, prepared PreparedCommand) (VerifiedCommandOutcome, error) {
	return session.executePreparedContinuation(ctx, prepared, CommandClose)
}

func (session *AttachedSession) executePreparedContinuation(ctx context.Context, prepared PreparedCommand, allowed CommandName) (VerifiedCommandOutcome, error) {
	if session == nil || session.guard == nil || ctx == nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	guard := session.guard
	guard.mu.Lock()
	if !guard.active || !guard.observed || guard.commandAttempted || currentGoroutineID() != guard.borrowerGoroutine ||
		guard.connection == nil || guard.codec == nil || guard.anchor.SessionID == "" {
		guard.violated = true
		guard.mu.Unlock()
		return VerifiedCommandOutcome{}, ErrConflict
	}
	if err := prepared.evidence.Validate(); err != nil || prepared.evidence.Command != allowed || prepared.evidence.PreCommand != guard.anchor {
		guard.violated = true
		guard.mu.Unlock()
		return VerifiedCommandOutcome{}, ErrConflict
	}
	guard.commandAttempted = true
	connection, codec, anchor := guard.connection, guard.codec, guard.anchor
	guard.mu.Unlock()
	request := prepared.request
	deadline, err := parseDeadline(request.Deadline)
	if err != nil {
		return VerifiedCommandOutcome{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	var response Response
	if err := codec.Write(request); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if err := codec.Read(&response); err != nil {
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	if err := ValidateResponseBinding(response, request); err != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	post, err := commandPostAnchor(anchor, request, response)
	if err != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	outcome, err := verifiedCommandOutcome(request, response, CommandRecoveryEvidence{PreCommand: anchor, PostCommand: post})
	if err != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	guard.mu.Lock()
	guard.commandExecuted = true
	guard.executedCommand = allowed
	guard.postCommand = post
	guard.mu.Unlock()
	return outcome, nil
}

// deactivateAndWait is called exactly once by callAttachedBorrower after fn
// returns. It marks the session inactive, waits for any in-flight Observation
// to drain, and then clears the observation value so no saved pointer can reach
// it. Any escape (in-flight at deactivate time), missed observation, or prior
// violation fails closed.
func (guard *attachedSessionGuard) deactivateAndWait() error {
	guard.mu.Lock()
	if guard.inFlight != 0 {
		guard.escaped = true
	}
	guard.active = false
	for guard.inFlight != 0 {
		guard.cond.Wait()
	}
	valid := guard.observed && !guard.escaped && !guard.violated
	guard.observation = AttachObservation{}
	guard.mu.Unlock()
	if !valid {
		return ErrConflict
	}
	return nil
}

// callAttachedBorrower runs the user callback with a fresh AttachedSession and
// then deactivates it. A panic inside the borrower is converted to a conflict
// so a panicking callback can never escape the borrowed boundary.
func callAttachedBorrower(session *AttachedSession, fn func(*AttachedSession) error) (err error) {
	if session == nil || session.guard == nil || fn == nil {
		return ErrInvalid
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: Attach borrower panic", ErrConflict)
		}
		if guardErr := session.guard.deactivateAndWait(); err == nil && guardErr != nil {
			err = guardErr
		}
	}()
	session.guard.mu.Lock()
	session.guard.borrowerGoroutine = currentGoroutineID()
	session.guard.mu.Unlock()
	return fn(session)
}

// currentGoroutineID returns the runtime ID of the calling goroutine. It is
// used only to prove that an AttachedSession.Observation call originates from
// the same goroutine that owns the borrowed callback: a goroutine started by
// the borrower is rejected deterministically even if it completes before the
// callback returns. The ID is parsed from runtime.Stack because Go exposes no
// public goroutine identifier; it is never persisted or compared across
// processes.
func currentGoroutineID() uint64 {
	var buffer [64]byte
	n := runtime.Stack(buffer[:], false)
	const prefix = "goroutine "
	if n <= len(prefix) || string(buffer[:len(prefix)]) != prefix {
		return 0
	}
	var id uint64
	for i := len(prefix); i < n; i++ {
		b := buffer[i]
		if b < '0' || b > '9' {
			break
		}
		id = id*10 + uint64(b-'0')
	}
	return id
}

// withAttachOwner gates the Attach wire exchange behind the owner verifier. It
// requires the verifier to invoke the inner callback synchronously and exactly
// once; asynchronous, repeated, or post-return invocation fails closed before
// any wire or control-directory observation begins.
func withAttachOwner(ctx context.Context, verifier AttachOwnerVerifier, authority AttachAuthority, fn func() error) error {
	if ctx == nil || verifier == nil || authority.validate() != nil || fn == nil {
		return ErrInvalid
	}
	var gate sync.Mutex
	called, repeated, closed := false, false, false
	var callbackErr error
	verifierErr := verifier.WithCurrentAttachOwner(ctx, authority, func() error {
		gate.Lock()
		defer gate.Unlock()
		if closed || called {
			repeated = true
			return ErrConflict
		}
		called = true
		callbackErr = fn()
		return callbackErr
	})
	gate.Lock()
	closed = true
	calledOnce, calledAgain, heldErr := called, repeated, callbackErr
	gate.Unlock()
	if calledAgain || !calledOnce || verifierErr != nil {
		if heldErr != nil {
			return heldErr
		}
		return ErrConflict
	}
	return heldErr
}

// validateAttachJournalAnchor verifies that a held journal snapshot exactly
// matches the previous Supervisor anchor: journal sequence/head, current owner
// epoch/authority head, no pending command, and the reconstructed command
// sequence/head. It is pure read-only validation used by both sides of Attach.
func validateAttachJournalAnchor(snapshot JournalSnapshot, anchor HandshakeAnchor) error {
	if snapshot.Sequence != anchor.JournalSequence || snapshot.Head != anchor.JournalHead || snapshot.currentOwnerEpoch != anchor.OwnerEpoch || snapshot.currentAuthorityHead != anchor.CurrentAuthorityHead || snapshot.pending != nil {
		return ErrConflict
	}
	sequence, head := uint64(0), CommandGenesisDigest
	for _, command := range snapshot.commands {
		if command.Projection.Sequence > sequence {
			sequence, head = command.Projection.Sequence, command.Response.CommandHead
		}
	}
	if sequence != anchor.CommandSequence || head != anchor.CommandHead {
		return ErrConflict
	}
	return nil
}

type attachRequest struct {
	SchemaVersion            string                   `json:"schemaVersion"`
	ProtocolRevision         string                   `json:"protocolRevision"`
	SessionNonce             string                   `json:"sessionNonce"`
	Core                     CoreIdentity             `json:"core"`
	ControlDirectoryIdentity ControlDirectoryIdentity `json:"controlDirectoryIdentity"`
	Authority                AttachAuthority          `json:"authority"`
	RequestDigest            string                   `json:"requestDigest"`
}

type attachRequestDigestInput struct {
	SchemaVersion            string                   `json:"schemaVersion"`
	ProtocolRevision         string                   `json:"protocolRevision"`
	SessionNonceDigest       string                   `json:"sessionNonceDigest"`
	Core                     CoreIdentity             `json:"core"`
	ControlDirectoryIdentity ControlDirectoryIdentity `json:"controlDirectoryIdentity"`
	Authority                AttachAuthority          `json:"authority"`
}

func (request attachRequest) detachedDigest() (string, error) {
	return digestValue(attachRequestDigestInput{SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision, SessionNonceDigest: request.Authority.PreviousSupervisor.SessionNonceDigest, Core: request.Core, ControlDirectoryIdentity: request.ControlDirectoryIdentity, Authority: request.Authority})
}

func (request attachRequest) validate() error {
	if request.SchemaVersion != AttachSchema || request.ProtocolRevision != ProtocolRevision || !hex64Pattern.MatchString(request.SessionNonce) ||
		request.Core.UID == 0 || request.Core.Process.validate() != nil || request.Core.Binary.validate() != nil || request.ControlDirectoryIdentity.validate() != nil ||
		request.Authority.validate() != nil || !validDigest(request.RequestDigest) {
		return ErrInvalid
	}
	digest, err := request.detachedDigest()
	if err != nil || digest != request.RequestDigest || request.Core.UID != request.Authority.CurrentAcquisition.OwnerUID ||
		request.Core.GID != request.Authority.CurrentAcquisition.OwnerGID || request.Core.Process != request.Authority.CurrentAcquisition.OwnerProcess ||
		request.Core.Binary != request.Authority.CurrentAcquisition.OwnerBinary || !sameBinaryObject(request.Core.Binary, request.Authority.PreviousSupervisor.FixedBinary) {
		return ErrConflict
	}
	return nil
}

type attachResponse struct {
	SchemaVersion          string                 `json:"schemaVersion"`
	ProtocolRevision       string                 `json:"protocolRevision"`
	Status                 string                 `json:"status"`
	ReasonCode             string                 `json:"reasonCode"`
	RequestDigest          string                 `json:"requestDigest"`
	Handshake              HandshakeResponse      `json:"handshake"`
	CurrentAcquisition     AttachOwnerAcquisition `json:"currentAcquisition"`
	CurrentOwnerBoundFact  AttachOwnerBoundFact   `json:"currentOwnerBoundFact"`
	Child                  ProcessIdentity        `json:"child"`
	ChildObservationDigest string                 `json:"childObservationDigest"`
	ObserverIdentity       string                 `json:"observerIdentity"`
	ObservedAt             string                 `json:"observedAt"`
	ResponseDigest         string                 `json:"responseDigest"`
}

type attachResponseDigestInput attachResponse

func (response attachResponse) detachedDigest() (string, error) {
	input := attachResponseDigestInput(response)
	input.ResponseDigest = ""
	return digestValue(input)
}

func (response attachResponse) validate(request attachRequest, observed CoreIdentity) error {
	observedAt, err := time.Parse(time.RFC3339Nano, response.ObservedAt)
	if response.SchemaVersion != AttachObservationSchema || response.ProtocolRevision != ProtocolRevision || response.Status != "ok" ||
		response.ReasonCode != "process-supervisor-attached" || response.RequestDigest != request.RequestDigest ||
		ValidateHandshakeBinding(response.Handshake, request.Authority.PreviousSupervisor, observed) != nil ||
		response.Handshake.SupervisorProcess != request.Authority.Supervisor ||
		response.CurrentAcquisition != request.Authority.CurrentAcquisition || response.CurrentOwnerBoundFact != request.Authority.CurrentOwnerBoundFact ||
		response.Child != request.Authority.Child || response.ChildObservationDigest != request.Authority.ChildObservationDigest ||
		response.ObserverIdentity != attachObserverIdentityV1 || err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != response.ObservedAt ||
		!validDigest(response.ResponseDigest) {
		return ErrConflict
	}
	digest, digestErr := response.detachedDigest()
	if digestErr != nil || digest != response.ResponseDigest {
		return ErrConflict
	}
	return nil
}
