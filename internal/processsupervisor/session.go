package processsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

const CommandGenesisDigest = "sha256:9fcab19fef05bca8fe7ea303ec7733a035d2862840af27fc72c709d1b7e80628"

type sessionState string

const (
	sessionUnbound      sessionState = "unbound"
	sessionBound        sessionState = "bound"
	sessionAborted      sessionState = "aborted"
	sessionClosed       sessionState = "closed"
	sessionIntervention sessionState = "intervention"
)

type Session struct {
	mu sync.Mutex

	journal   *Journal
	mechanics Mechanics
	now       func() time.Time

	sessionID             string
	nonceDigest           string
	authority             AuthorityTuple
	launchFact            string
	ownerEpoch            uint64
	authorityHead         string
	commandSequence       uint64
	commandHead           string
	state                 sessionState
	supervisorStartedFact string
	startedFact           string
	lastObservation       string
	cleanupBinding        string
	terminalBarrier       string
	terminalizationID     string
	terminalGeneration    uint64
}

func NewSession(bootstrap BootstrapRequest, journal *Journal, mechanics Mechanics, now func() time.Time) (*Session, error) {
	if bootstrap.validate() != nil || journal == nil || mechanics == nil {
		return nil, ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	session := &Session{
		journal: journal, mechanics: mechanics, now: now,
		sessionID: bootstrap.SessionID, nonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)),
		authority: bootstrap.Authority, launchFact: bootstrap.LaunchAuthorizedFact,
		ownerEpoch: bootstrap.OwnerEpoch, authorityHead: bootstrap.CurrentAuthorityHead,
		commandHead: CommandGenesisDigest, state: sessionUnbound,
	}
	snapshot := journal.Snapshot()
	if snapshot.Sequence == 0 {
		if err := journal.AppendSessionCreated(session.sessionID, session.nonceDigest, session.authority, session.ownerEpoch, session.authorityHead); err != nil {
			return nil, err
		}
		return session, nil
	}
	// Session construction over any pre-existing journal is a supervisor
	// restart. Even a bind-only predecessor has a different PID/birth and can
	// no longer satisfy the exact process-supervisor-started fact. Core restart
	// replay is served by the still-live Session; supervisor restart is always
	// intervention under ADR 0059.
	return nil, ErrIntervention
}

func (session *Session) State() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return string(session.state)
}

func (session *Session) Snapshot() (uint64, string, uint64, string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	snapshot := session.journal.Snapshot()
	return session.commandSequence, session.commandHead, snapshot.Sequence, snapshot.Head
}

type reconnectResolution struct {
	State    ReconciliationState
	Response *Response
}

type reconnectAttemptDisposition uint8

const (
	reconnectRejectedBeforeMechanics reconnectAttemptDisposition = iota
	reconnectResolvedWithoutMechanics
	reconnectResolvedAfterMechanics
	reconnectFailedAfterMechanics
)

// reconnectAttemptResult preserves the security-relevant point of no return.
// A plain error cannot tell the wire server whether replay mechanics may have
// run, so the disposition is carried independently and must be consumed before
// deciding whether a rejected handshake is safe to emit.
type reconnectAttemptResult struct {
	resolution  reconnectResolution
	disposition reconnectAttemptDisposition
	err         error
}

func (session *Session) reconnect(request reconnectRequest, observed CoreIdentity) (reconnectResolution, error) {
	attempt := session.reconnectAttempt(request, observed)
	return attempt.resolution, attempt.err
}

func (session *Session) reconnectAttempt(request reconnectRequest, observed CoreIdentity) reconnectAttemptResult {
	session.mu.Lock()
	defer session.mu.Unlock()
	if request.SchemaVersion != ReconnectSchema || request.ProtocolRevision != ProtocolRevision || request.SessionID != session.sessionID ||
		canonical.DigestBytes([]byte(request.SessionNonce)) != session.nonceDigest || request.PreviousOwnerEpoch != session.ownerEpoch || request.OwnerEpoch <= request.PreviousOwnerEpoch || request.OwnerEpoch > maxSafeJSONInteger ||
		request.PreviousAuthorityHead != session.authorityHead || !validDigest(request.PreviousAuthorityHead) || !validDigest(request.CurrentAuthorityHead) || request.CurrentAuthorityHead == request.PreviousAuthorityHead || !validDigest(request.ControlOwnerAcquired) || !sameCoreIdentity(request.Core, observed) ||
		request.LastOwnerEpoch == 0 || request.LastOwnerEpoch > request.PreviousOwnerEpoch || !validDigest(request.LastAuthorityHead) ||
		request.LastCommandSequence > maxSafeJSONInteger || !validDigest(request.LastCommandHead) || request.LastJournalSequence == 0 || request.LastJournalSequence > maxSafeJSONInteger || !validDigest(request.LastJournalHead) || session.state == sessionIntervention {
		return reconnectAttemptResult{disposition: reconnectRejectedBeforeMechanics, err: ErrConflict}
	}
	resolution, err := session.reconcilePendingLocked(request)
	if err != nil {
		return reconnectAttemptResult{disposition: reconnectRejectedBeforeMechanics, err: err}
	}
	disposition := reconnectResolvedWithoutMechanics
	if resolution.State == ReconciliationUnchanged && request.PendingRequest != nil {
		raw, err := CanonicalProtocolMessage(*request.PendingRequest)
		if err != nil {
			return reconnectAttemptResult{disposition: reconnectRejectedBeforeMechanics, err: ErrConflict}
		}
		// From this instruction onward Handle may append an intent, invoke
		// mechanics, and append a receipt. Every failure must therefore be a
		// silent wire close after the post-replay control boundary.
		disposition = reconnectResolvedAfterMechanics
		response := session.handleLocked(raw)
		if ValidateResponseBinding(response, *request.PendingRequest) != nil {
			session.state = sessionIntervention
			return reconnectAttemptResult{disposition: reconnectFailedAfterMechanics, err: ErrIntervention}
		}
		a0 := session.reconnectAnchor(request)
		_, receiptHead, err := expectedPendingJournalHeads(a0, *request.PendingRequest, &response)
		commandSequence, commandHead, journalSequence, journalHead := session.snapshotLocked()
		_, _, projectErr := projectRequest(*request.PendingRequest)
		if err != nil || projectErr != nil || commandSequence != request.PendingRequest.Sequence || commandHead != response.CommandHead || journalSequence != request.LastJournalSequence+2 || journalHead != receiptHead {
			session.state = sessionIntervention
			return reconnectAttemptResult{disposition: reconnectFailedAfterMechanics, err: ErrIntervention}
		}
		if session.state == sessionIntervention {
			return reconnectAttemptResult{disposition: reconnectFailedAfterMechanics, err: ErrIntervention}
		}
		response.Payload = append([]byte(nil), response.Payload...)
		resolution.Response = &response
	}
	session.ownerEpoch = request.OwnerEpoch
	session.authorityHead = request.CurrentAuthorityHead
	if resolution.State == ReconciliationIntentPending {
		// A durable intent without a receipt cannot be retried blindly. The
		// exact new owner may inspect the closed evidence, but mechanics remain
		// locked until an external intervention resolves the side effect.
		session.state = sessionIntervention
	}
	return reconnectAttemptResult{resolution: resolution, disposition: disposition}
}

func (session *Session) reconcilePendingLocked(request reconnectRequest) (reconnectResolution, error) {
	snapshot := session.journal.Snapshot()
	// Classification is anchored exclusively in Last*/A0 plus the exact
	// mechanics journal. session.authorityHead may already be the authority head
	// installed by an earlier reconnect whose handshake was lost.
	var projection requestProjection
	if request.PendingRequest != nil {
		pending := *request.PendingRequest
		pending.Payload = append([]byte(nil), request.PendingRequest.Payload...)
		if pending.Command == CommandSpawn {
			var spawn SpawnPayload
			if strictCanonicalDecode(pending.Payload, &spawn) != nil || spawn.SourceGateRevision != SourceGateRevisionV1 {
				// Historical pending spawns cannot be safely re-executed under S1;
				// leave the journal untouched and force typed intervention.
				return reconnectResolution{}, ErrIntervention
			}
		}
		value, _, err := projectRequest(pending)
		if err != nil || pending.SessionID != session.sessionID || pending.Sequence != request.LastCommandSequence+1 || pending.PreviousCommandDigest != request.LastCommandHead {
			return reconnectResolution{}, ErrConflict
		}
		projection = value
	}
	if snapshot.Sequence == request.LastJournalSequence && snapshot.Head == request.LastJournalHead && snapshot.currentOwnerEpoch == request.LastOwnerEpoch && snapshot.currentAuthorityHead == request.LastAuthorityHead && session.commandSequence == request.LastCommandSequence && session.commandHead == request.LastCommandHead && snapshot.pending == nil {
		return reconnectResolution{State: ReconciliationUnchanged}, nil
	}
	if request.PendingRequest == nil {
		return reconnectResolution{}, ErrConflict
	}
	pending := *request.PendingRequest
	pending.Payload = append([]byte(nil), request.PendingRequest.Payload...)
	intentHead, _, headErr := expectedPendingJournalHeads(session.reconnectAnchor(request), pending, nil)
	if headErr != nil {
		return reconnectResolution{}, ErrConflict
	}
	if snapshot.Sequence == request.LastJournalSequence+1 && snapshot.Head == intentHead && session.commandSequence == request.LastCommandSequence && session.commandHead == request.LastCommandHead && snapshot.pending != nil && snapshot.pendingOwnerEpoch == request.LastOwnerEpoch && snapshot.pendingAuthorityHead == request.LastAuthorityHead && snapshot.pendingPreviousHead == request.LastJournalHead && equalProjection(*snapshot.pending, projection) {
		return reconnectResolution{State: ReconciliationIntentPending}, nil
	}
	if snapshot.Sequence != request.LastJournalSequence+2 || snapshot.pending != nil || session.commandSequence != pending.Sequence {
		return reconnectResolution{}, ErrConflict
	}
	stored, ok := snapshot.commands[pending.CommandID]
	if !ok || stored.OwnerEpoch != request.LastOwnerEpoch || stored.AuthorityHead != request.LastAuthorityHead || stored.PreviousJournalHead != request.LastJournalHead || !equalProjection(stored.Projection, projection) || session.commandHead != stored.Response.CommandHead || ValidateResponseBinding(stored.Response, pending) != nil {
		return reconnectResolution{}, ErrConflict
	}
	_, receiptHead, headErr := expectedPendingJournalHeads(session.reconnectAnchor(request), pending, &stored.Response)
	if headErr != nil || snapshot.Head != receiptHead {
		return reconnectResolution{}, ErrConflict
	}
	response := stored.Response
	response.Payload = append([]byte(nil), stored.Response.Payload...)
	return reconnectResolution{State: ReconciliationReceiptCommitted, Response: &response}, nil
}

func (session *Session) reconnectAnchor(request reconnectRequest) HandshakeAnchor {
	return HandshakeAnchor{
		SessionID: session.sessionID, SessionNonceDigest: session.nonceDigest, Authority: session.authority,
		OwnerEpoch: request.LastOwnerEpoch, CurrentAuthorityHead: request.LastAuthorityHead,
		CommandSequence: request.LastCommandSequence, CommandHead: request.LastCommandHead,
		JournalSequence: request.LastJournalSequence, JournalHead: request.LastJournalHead,
	}
}

func (session *Session) snapshotLocked() (uint64, string, uint64, string) {
	snapshot := session.journal.Snapshot()
	return session.commandSequence, session.commandHead, snapshot.Sequence, snapshot.Head
}

func (session *Session) Handle(raw []byte) Response {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.handleLocked(raw)
}

func (session *Session) handleLocked(raw []byte) Response {
	var request Request
	if err := strictCanonicalDecode(raw, &request); err != nil {
		return rejectedResponse(Request{}, ReasonCode(err))
	}
	if prior, ok := session.journal.Snapshot().commands[request.CommandID]; ok {
		projection, _, err := projectRequest(request)
		if err == nil && prior.Projection.RequestDigest == request.RequestDigest && equalProjection(prior.Projection, projection) {
			response := prior.Response
			response.Payload = append([]byte(nil), response.Payload...)
			return response
		}
		return rejectedResponse(request, ErrConflict.ReasonCode)
	}
	projection, payload, err := session.admitRequest(request)
	if err != nil {
		return rejectedResponse(request, ReasonCode(err))
	}

	base := session.journalBase()
	if err := session.journal.AppendIntent(base, projection); err != nil {
		session.state = sessionIntervention
		return rejectedResponse(request, ReasonCode(err))
	}
	deadline, _ := parseDeadline(request.Deadline)
	result, commandErr := session.execute(request.Command, payload, deadline)
	if commandErr != nil {
		result = MechanicsResult{Disposition: "rejected", ReasonCode: ReasonCode(commandErr), ObservationDigest: canonical.DigestBytes([]byte(ReasonCode(commandErr))), Payload: canonicalEmptyPayload()}
	}
	if err := validateMechanicsResult(result); err != nil {
		result = MechanicsResult{Disposition: "rejected", ReasonCode: ErrIntervention.ReasonCode, ObservationDigest: canonical.DigestBytes([]byte(ErrIntervention.ReasonCode)), Payload: canonicalEmptyPayload()}
		commandErr = ErrIntervention
	}
	receiptDigest, _ := digestValue(result)
	commandHead, _ := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, receiptDigest})
	status := "ok"
	if commandErr != nil {
		status = "rejected"
	}
	response := Response{
		SchemaVersion: ResponseSchema, ProtocolRevision: ProtocolRevision, SessionID: session.sessionID,
		Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest,
		Status: status, ReasonCode: result.ReasonCode, ReceiptDigest: receiptDigest, ObservationDigest: result.ObservationDigest,
		CommandHead: commandHead, Payload: mustCanonical(result),
	}
	if err := session.journal.AppendReceipt(base, projection, response); err != nil {
		session.state = sessionIntervention
		return rejectedResponse(request, ErrIntervention.ReasonCode)
	}
	session.commandSequence = request.Sequence
	session.commandHead = commandHead
	if commandErr == nil {
		session.advance(request.Command, payload, result)
	} else if errors.Is(commandErr, ErrIntervention) {
		session.state = sessionIntervention
	}
	if request.Command != CommandBindAuthority && commandErr == nil {
		session.authorityHead = request.CurrentAuthorityHead
	}
	return response
}

func (session *Session) admitRequest(request Request) (requestProjection, any, error) {
	if request.ProtocolRevision != ProtocolRevision || request.SessionID != session.sessionID || !validCommand(request.Command) || !validID(request.CommandID) ||
		request.Sequence != session.commandSequence+1 || request.Sequence > maxSafeJSONInteger || request.PreviousCommandDigest != session.commandHead || !validDigest(request.CurrentAuthorityHead) || !validDigest(request.RequestDigest) {
		return requestProjection{}, nil, ErrConflict
	}
	deadline, err := parseDeadline(request.Deadline)
	limit, ok := commandDeadlineLimit(request.Command)
	now := session.now().UTC()
	if err != nil || !ok || !deadline.After(now) || deadline.Sub(now) > limit {
		return requestProjection{}, nil, reject("process-supervisor-deadline-invalid")
	}
	projection, payload, err := projectRequest(request)
	if err != nil {
		return requestProjection{}, nil, err
	}
	if session.state == sessionUnbound && request.Command != CommandBindAuthority && request.Command != CommandAbortUnbound || session.state == sessionBound && (request.Command == CommandBindAuthority || request.Command == CommandAbortUnbound) || session.state == sessionAborted || session.state == sessionClosed || session.state == sessionIntervention {
		return requestProjection{}, nil, ErrConflict
	}
	if (request.Command == CommandBindAuthority || request.Command == CommandAbortUnbound || request.Command == CommandSpawn) && request.CurrentAuthorityHead != session.authorityHead {
		return requestProjection{}, nil, ErrConflict
	}
	switch request.Command {
	case CommandSpawn:
		value := payload.(SpawnPayload)
		if value.SourceGateRevision != SourceGateRevisionV1 {
			// A legacy spawn may be decoded for completed-journal replay, but it
			// must never be executed again as a live mechanics command.
			return requestProjection{}, nil, ErrIntervention
		}
		if value.LaunchAuthorizedFactDigest != session.launchFact || value.SupervisorStartedFactDigest != session.supervisorStartedFact {
			return requestProjection{}, nil, ErrConflict
		}
	case CommandInspect, CommandTerminate:
		value := payload.(CleanupPayload)
		if session.startedFact != "" && value.ProcessStartedFactDigest != session.startedFact || session.lastObservation == "" || value.LastObservationDigest != session.lastObservation {
			return requestProjection{}, nil, ErrConflict
		}
		if session.cleanupBinding != "" && (value.CleanupBindingDigest != session.cleanupBinding || value.TerminalizationBarrierDigest != session.terminalBarrier || value.TerminalizationID != session.terminalizationID || value.TerminalGeneration != session.terminalGeneration) {
			return requestProjection{}, nil, ErrConflict
		}
	case CommandCollect:
		value := payload.(CollectPayload)
		if value.ProcessStartedFactDigest != session.startedFact || value.LastObservationDigest != session.lastObservation {
			return requestProjection{}, nil, ErrConflict
		}
	case CommandClose:
		if session.cleanupBinding == "" || payload.(ClosePayload).CleanupBindingDigest != session.cleanupBinding {
			return requestProjection{}, nil, ErrConflict
		}
	}
	return projection, payload, nil
}

func (session *Session) execute(command CommandName, payload any, deadline time.Time) (MechanicsResult, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	switch command {
	case CommandBindAuthority:
		value := payload.(BindAuthorityPayload)
		if value.OwnerEpoch != session.ownerEpoch || value.PreviousAuthorityHead != session.authorityHead || !validDigest(value.SupervisorStartedFactDigest) || !validDigest(value.AuthorityHead) {
			return MechanicsResult{}, ErrConflict
		}
		return successResult("authority-bound", value.SupervisorStartedFactDigest), nil
	case CommandAbortUnbound:
		value := payload.(AbortUnboundPayload)
		if value.OwnerEpoch != session.ownerEpoch || value.PreviousAuthorityHead != session.authorityHead || !validDigest(value.AuthorityAbsenceProofDigest) {
			return MechanicsResult{}, ErrConflict
		}
		return successResult("unbound-aborted", value.AuthorityAbsenceProofDigest), nil
	case CommandSpawn:
		return session.mechanics.Spawn(ctx, payload.(SpawnPayload))
	case CommandResume:
		return session.mechanics.Resume(ctx, payload.(ResumePayload))
	case CommandInspect:
		return session.mechanics.Inspect(ctx, payload.(CleanupPayload))
	case CommandTerminate:
		return session.mechanics.Terminate(ctx, payload.(CleanupPayload))
	case CommandCollect:
		return session.mechanics.Collect(ctx, payload.(CollectPayload))
	case CommandClose:
		return session.mechanics.Close(ctx, payload.(ClosePayload))
	default:
		return MechanicsResult{}, ErrInvalid
	}
}

func (session *Session) advance(command CommandName, payload any, result MechanicsResult) {
	switch command {
	case CommandBindAuthority:
		session.state = sessionBound
		value := payload.(BindAuthorityPayload)
		session.authorityHead = value.AuthorityHead
		session.supervisorStartedFact = value.SupervisorStartedFactDigest
	case CommandAbortUnbound:
		session.state = sessionAborted
	case CommandResume:
		session.startedFact = payload.(ResumePayload).ProcessStartedFactDigest
		session.lastObservation = result.ObservationDigest
	case CommandSpawn:
		session.lastObservation = result.ObservationDigest
	case CommandInspect, CommandTerminate:
		value := payload.(CleanupPayload)
		if session.startedFact == "" {
			session.startedFact = value.ProcessStartedFactDigest
		}
		session.lastObservation = result.ObservationDigest
		session.cleanupBinding = value.CleanupBindingDigest
		session.terminalBarrier = value.TerminalizationBarrierDigest
		session.terminalizationID = value.TerminalizationID
		session.terminalGeneration = value.TerminalGeneration
	case CommandCollect:
		// ResultIngress advances the durable latest-observation checkpoint to
		// the successful collect receipt. Keep supervisor admission identical so
		// a later Inspect/Terminate after recovery accepts the same digest.
		session.lastObservation = result.ObservationDigest
	case CommandClose:
		session.state = sessionClosed
	}
}

func (session *Session) journalBase() journalRecord {
	return journalRecord{SchemaVersion: JournalSchema, SessionID: session.sessionID, SessionNonceDigest: session.nonceDigest, Authority: session.authority, OwnerEpoch: session.ownerEpoch, CurrentAuthorityHead: session.authorityHead}
}

func decodePayload(command CommandName, raw json.RawMessage, projection *requestProjection) (any, error) {
	if len(raw) == 0 || len(raw) > MaxWireFrameBytes || projection == nil {
		return nil, ErrInvalid
	}
	switch command {
	case CommandBindAuthority:
		var value BindAuthorityPayload
		if strictCanonicalDecode(raw, &value) != nil || !validDigest(value.SupervisorStartedFactDigest) || value.OwnerEpoch == 0 || value.OwnerEpoch > maxSafeJSONInteger || !validDigest(value.PreviousAuthorityHead) || !validDigest(value.AuthorityHead) || value.AuthorityHead == value.PreviousAuthorityHead {
			return nil, ErrInvalid
		}
		projection.NextAuthorityHead = value.AuthorityHead
		projection.SupervisorStartedFactDigest = value.SupervisorStartedFactDigest
		return value, nil
	case CommandAbortUnbound:
		var value AbortUnboundPayload
		if strictCanonicalDecode(raw, &value) != nil || value.OwnerEpoch == 0 || value.OwnerEpoch > maxSafeJSONInteger || !validDigest(value.PreviousAuthorityHead) || !validDigest(value.AuthorityAbsenceProofDigest) {
			return nil, ErrInvalid
		}
		projection.AuthorityAbsenceProofDigest = value.AuthorityAbsenceProofDigest
		return value, nil
	case CommandSpawn:
		var value SpawnPayload
		if strictCanonicalDecode(raw, &value) != nil || validateSpawnPayload(value) != nil {
			return nil, ErrInvalid
		}
		projection.LaunchMaterialsDigest = value.LaunchMaterialsDigest
		projection.AgentLaunchSpecDigest = value.AgentLaunchSpecDigest
		projection.SourceGateRevision = value.SourceGateRevision
		projection.ClosureProfileID = value.ClosureProfileID
		projection.ArgvDigest = value.ArgvDigest
		projection.EnvironmentDigest = value.EnvironmentDigest
		projection.StdinDigest = value.StdinDigest
		projection.EnvironmentKeys = append([]string(nil), value.EnvironmentKeys...)
		return value, nil
	case CommandResume:
		var value ResumePayload
		if strictCanonicalDecode(raw, &value) != nil || !validDigest(value.ProcessStartedFactDigest) {
			return nil, ErrInvalid
		}
		projection.ProcessStartedFactDigest = value.ProcessStartedFactDigest
		return value, nil
	case CommandInspect, CommandTerminate:
		var value CleanupPayload
		if strictCanonicalDecode(raw, &value) != nil || validateCleanupPayload(value) != nil {
			return nil, ErrInvalid
		}
		projection.ProcessStartedFactDigest = value.ProcessStartedFactDigest
		projection.TerminalizationBarrierDigest = value.TerminalizationBarrierDigest
		projection.TerminalizationID = value.TerminalizationID
		projection.TerminalGeneration = value.TerminalGeneration
		projection.CleanupBindingDigest = value.CleanupBindingDigest
		projection.LastObservationDigest = value.LastObservationDigest
		return value, nil
	case CommandCollect:
		var value CollectPayload
		if strictCanonicalDecode(raw, &value) != nil || !validDigest(value.ProcessStartedFactDigest) || !validDigest(value.LastObservationDigest) {
			return nil, ErrInvalid
		}
		projection.ProcessStartedFactDigest = value.ProcessStartedFactDigest
		projection.LastObservationDigest = value.LastObservationDigest
		return value, nil
	case CommandClose:
		var value ClosePayload
		if strictCanonicalDecode(raw, &value) != nil || !validDigest(value.ProcessTerminalFactDigest) || !validDigest(value.AllocationTerminatedDigest) || !validDigest(value.CleanupBindingDigest) {
			return nil, ErrInvalid
		}
		projection.CleanupBindingDigest = value.CleanupBindingDigest
		projection.ProcessTerminalFactDigest = value.ProcessTerminalFactDigest
		projection.AllocationTerminatedDigest = value.AllocationTerminatedDigest
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

func validateSpawnPayload(value SpawnPayload) error {
	if value.SourceGateRevision != "" && value.SourceGateRevision != SourceGateRevisionV1 {
		return ErrInvalid
	}
	if value.SourceGateRevision == SourceGateRevisionV1 && (value.AllocationLiveIdentity == nil || !value.AllocationLiveIdentity.matches(value.WorkingDirectory)) {
		return ErrInvalid
	}
	for _, digest := range []string{value.LaunchAuthorizedFactDigest, value.SupervisorStartedFactDigest, value.LaunchMaterialsDigest, value.AgentLaunchSpecDigest, value.ArgvDigest, value.EnvironmentDigest, value.StdinDigest} {
		if !validDigest(digest) {
			return ErrInvalid
		}
	}
	if value.Runtime.validate("runtime", "regular") != nil || value.WorkingDirectory.validate("working-directory", "directory") != nil || len(value.Argv) == 0 || len(value.Argv) > MaxArgvEntries || value.Argv[0] != value.Runtime.CanonicalPath || value.Environment == nil || value.EnvironmentKeys == nil || value.MaterialRoots == nil || value.LaunchMaterials == nil || value.Stdin == nil || len(value.Environment) > MaxEnvironmentKeys || len(value.Stdin) > MaxStdinBytes || !validID(value.ClosureProfileID) {
		return ErrInvalid
	}
	argvBytes := 0
	for _, argument := range value.Argv {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
			return ErrInvalid
		}
		argvBytes += len(argument)
	}
	if argvBytes > MaxArgvBytes {
		return ErrInvalid
	}
	keys := make([]string, 0, len(value.Environment))
	seenKeys := make(map[string]struct{}, len(value.Environment))
	environmentBytes := 0
	for _, item := range value.Environment {
		key, rawValue, found := strings.Cut(item, "=")
		if !found || !validEnvironmentKey(key) || !utf8.ValidString(rawValue) || strings.ContainsRune(rawValue, 0) {
			return ErrInvalid
		}
		if _, exists := seenKeys[key]; exists {
			return ErrInvalid
		}
		seenKeys[key] = struct{}{}
		keys = append(keys, key)
		environmentBytes += len(rawValue)
	}
	sort.Strings(keys)
	if environmentBytes > MaxEnvironmentBytes || !equalStrings(keys, value.EnvironmentKeys) {
		return ErrInvalid
	}
	argvDigest, _ := digestValue(value.Argv)
	environmentDigest, _ := digestValue(value.Environment)
	if argvDigest != value.ArgvDigest || environmentDigest != value.EnvironmentDigest || canonical.DigestBytes(value.Stdin) != value.StdinDigest {
		return ErrInvalid
	}
	seen := map[[2]uint64]struct{}{{value.Runtime.Device, value.Runtime.Inode}: {}, {value.WorkingDirectory.Device, value.WorkingDirectory.Inode}: {}}
	roles := map[string]struct{}{"runtime": {}, "working-directory": {}, "marshal": {}}
	for _, root := range value.MaterialRoots {
		object := heldMaterialRoot(root)
		if !validMaterialRole(object.Role) || object.validate(object.Role, "directory") != nil {
			return ErrInvalid
		}
		if _, exists := roles[object.Role]; exists {
			return ErrInvalid
		}
		roles[object.Role] = struct{}{}
		identity := [2]uint64{object.Device, object.Inode}
		if _, exists := seen[identity]; exists {
			return ErrInvalid
		}
		seen[identity] = struct{}{}
	}
	for _, material := range value.LaunchMaterials {
		object := heldLaunchMaterial(material)
		if !validMaterialRole(object.Role) || object.validate(object.Role, "regular") != nil {
			return ErrInvalid
		}
		if _, exists := roles[object.Role]; exists {
			return ErrInvalid
		}
		roles[object.Role] = struct{}{}
		identity := [2]uint64{object.Device, object.Inode}
		if _, exists := seen[identity]; exists {
			return ErrInvalid
		}
		seen[identity] = struct{}{}
	}
	closure := launchidentity.ClosureV1{
		RuntimeExecutable:     launchObject(value.Runtime),
		ClosureProfileID:      value.ClosureProfileID,
		MaterialRoots:         append([]launchidentity.MaterialRootV1{}, value.MaterialRoots...),
		LaunchMaterials:       append([]launchidentity.LaunchMaterialV1{}, value.LaunchMaterials...),
		LaunchMaterialsDigest: value.LaunchMaterialsDigest,
		AgentLaunchSpecDigest: value.AgentLaunchSpecDigest,
		Arguments:             append([]string{}, value.Argv...),
		Environment:           append([]string{}, value.Environment...),
		WorkingDirectory:      value.WorkingDirectory.CanonicalPath,
	}
	if closure.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

func launchObject(object HeldObjectSpec) launchidentity.ObjectV1 {
	fileType := uint32(0o100000)
	if object.FileType == "directory" {
		fileType = 0o040000
	}
	return launchidentity.ObjectV1{CanonicalPath: object.CanonicalPath, Device: object.Device, Inode: object.Inode, FileType: fileType, Mode: object.Mode, UID: object.UID, GID: object.GID, Size: object.Size, LinkCount: object.LinkCount, RawSHA256: object.RawSHA256}
}

func heldMaterialRoot(root launchidentity.MaterialRootV1) HeldObjectSpec {
	return HeldObjectSpec{Role: root.Name, CanonicalPath: root.CanonicalPath, Device: root.Object.Device, Inode: root.Object.Inode, FileType: "directory", UID: root.Object.UID, GID: root.Object.GID, Mode: root.Object.Mode, LinkCount: root.Object.LinkCount, Size: root.Object.Size, RawSHA256: root.Object.RawSHA256}
}

func heldLaunchMaterial(material launchidentity.LaunchMaterialV1) HeldObjectSpec {
	object := material.Object
	return HeldObjectSpec{Role: material.Role, CanonicalPath: object.CanonicalPath, Device: object.Device, Inode: object.Inode, FileType: "regular", UID: object.UID, GID: object.GID, Mode: object.Mode, LinkCount: object.LinkCount, Size: object.Size, RawSHA256: object.RawSHA256}
}

func validMaterialRole(role string) bool {
	if !validID(role) || strings.HasPrefix(role, "/") || strings.HasSuffix(role, "/") {
		return false
	}
	for _, segment := range strings.Split(role, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (object HeldObjectSpec) validate(role, kind string) error {
	if !validID(role) || object.Role != role || !filepath.IsAbs(object.CanonicalPath) || filepath.Clean(object.CanonicalPath) != object.CanonicalPath || object.Device == 0 || object.Inode == 0 || !safeUint64(object.Device) || !safeUint64(object.Inode) || object.FileType != kind || object.LinkCount == 0 || !safeUint64(object.LinkCount) || object.Size < 0 || uint64(object.Size) > maxSafeJSONInteger || object.Mode&0o6000 != 0 {
		return ErrInvalid
	}
	wantType := uint32(0o100000)
	if kind == "directory" {
		wantType = 0o040000
	}
	if object.Mode&0o170000 != wantType {
		return ErrInvalid
	}
	if kind == "regular" && (object.Size < 0 || object.LinkCount != 1 || !validDigest(object.RawSHA256)) || kind == "directory" && object.RawSHA256 != "" {
		return ErrInvalid
	}
	if (role == "runtime" || role == "marshal") && object.Mode&0o111 == 0 {
		return ErrInvalid
	}
	return nil
}

func validateCleanupPayload(value CleanupPayload) error {
	if !validDigest(value.TerminalizationBarrierDigest) || !validID(value.TerminalizationID) || value.TerminalGeneration == 0 || value.TerminalGeneration > maxSafeJSONInteger || !validDigest(value.CleanupBindingDigest) || !validDigest(value.ProcessStartedFactDigest) || !validDigest(value.LastObservationDigest) {
		return ErrInvalid
	}
	return nil
}

func validateMechanicsResult(result MechanicsResult) error {
	if (result.Disposition != "ok" && result.Disposition != "rejected") || !validID(result.ReasonCode) || !validDigest(result.ObservationDigest) || result.StdoutBytes > MaxTranscriptBytes || result.StderrBytes > MaxTranscriptBytes || result.StdoutBytes+result.StderrBytes > MaxTranscriptBytes || len(result.Payload) == 0 || len(result.Payload) > MaxDiagnosticBytes {
		return ErrInvalid
	}
	if result.TranscriptDigest != "" && !validDigest(result.TranscriptDigest) {
		return ErrInvalid
	}
	var payload map[string]any
	if strictCanonicalDecode(result.Payload, &payload) != nil {
		return ErrInvalid
	}
	return nil
}

// ValidateProcessReport applies the exact v1 typed observation contract used
// by the client before any report reaches a production adapter.
func ValidateProcessReport(report ProcessReport) error {
	observedAt, err := time.Parse(time.RFC3339Nano, report.ObservedAt)
	birth := time.Unix(report.Process.BirthSeconds, report.Process.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != report.ObservedAt || observedAt.Before(birth) ||
		report.ObserverIdentity != "darwin-fixed-process-supervisor-v1" || report.Process.validate() != nil || !validDigest(report.RuntimeObjectDigest) || !validDigest(report.WorkingObjectDigest) ||
		(report.SourceGateRevision != "" && report.SourceGateRevision != SourceGateRevisionV1) || (report.SourceGateRevision == SourceGateRevisionV1 && !validDigest(report.ExactSetDigest)) || (report.SourceGateRevision == "" && report.ExactSetDigest != "") ||
		report.ExitCode < -1 || uint64(maxInt(report.ExitCode, 0)) > maxSafeJSONInteger || report.StdoutBytes > uint64(MaxStdoutBytes) || report.StderrBytes > uint64(MaxStderrBytes) || report.StdoutBytes+report.StderrBytes > uint64(MaxTranscriptBytes) {
		return ErrInvalid
	}
	switch report.State {
	case "exec-stopped", "running":
		if report.ExitCode != 0 || report.Signal != "" || report.StdoutDigest != "" || report.StderrDigest != "" || report.StdoutBytes != 0 || report.StderrBytes != 0 || report.TranscriptTruncated {
			return ErrInvalid
		}
	case "terminal":
		if report.Signal != "" && !validID(report.Signal) {
			return ErrInvalid
		}
		if (report.StdoutDigest == "") != (report.StderrDigest == "") || report.StdoutDigest == "" && (report.StdoutBytes != 0 || report.StderrBytes != 0 || report.TranscriptTruncated) || report.StdoutDigest != "" && (!validDigest(report.StdoutDigest) || !validDigest(report.StderrDigest)) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// ValidateS1ProcessReport is the authority-bearing validator for fresh S1
// mechanics. ValidateProcessReport intentionally remains legacy-compatible so
// old v1 transcript/journal bytes can be replayed without being upgraded into
// S1 evidence.
func ValidateS1ProcessReport(report ProcessReport) error {
	if report.SourceGateRevision != SourceGateRevisionV1 || !validDigest(report.ExactSetDigest) {
		return ErrInvalid
	}
	return ValidateProcessReport(report)
}

func maxInt(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}

func successResult(reason, observation string) MechanicsResult {
	return MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: observation, Payload: canonicalEmptyPayload()}
}

func rejectedResponse(request Request, reason string) Response {
	if !validID(reason) {
		reason = ErrInvalid.ReasonCode
	}
	result := MechanicsResult{Disposition: "rejected", ReasonCode: reason, ObservationDigest: canonical.DigestBytes([]byte(reason)), Payload: canonicalEmptyPayload()}
	receipt, _ := digestValue(result)
	return Response{SchemaVersion: ResponseSchema, ProtocolRevision: ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest, Status: "rejected", ReasonCode: reason, ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: CommandGenesisDigest, Payload: mustCanonical(result)}
}

func parseDeadline(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, ErrInvalid
	}
	return parsed, nil
}

func validCommand(command CommandName) bool {
	switch command {
	case CommandBindAuthority, CommandAbortUnbound, CommandSpawn, CommandResume, CommandInspect, CommandTerminate, CommandCollect, CommandClose:
		return true
	default:
		return false
	}
}

func validEnvironmentKey(value string) bool {
	if value == "" || len(value) > MaxEnvironmentKeyLen {
		return false
	}
	for index, character := range value {
		if !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func sameCoreIdentity(asserted, observed CoreIdentity) bool {
	return asserted == observed && asserted.UID != 0 && asserted.Binary.validate() == nil && asserted.Process.validate() == nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalEmptyPayload() json.RawMessage { return json.RawMessage("{}") }

func mustCanonical(value any) json.RawMessage {
	raw, err := canonicalValue(value)
	if err != nil {
		return canonicalEmptyPayload()
	}
	return raw
}

func projectRequest(request Request) (requestProjection, any, error) {
	if request.ProtocolRevision != ProtocolRevision || !validID(request.SessionID) || !validCommand(request.Command) || !validID(request.CommandID) || request.Sequence == 0 || request.Sequence > maxSafeJSONInteger || !validDigest(request.PreviousCommandDigest) || !validDigest(request.CurrentAuthorityHead) || !validDigest(request.RequestDigest) {
		return requestProjection{}, nil, ErrInvalid
	}
	if _, err := parseDeadline(request.Deadline); err != nil {
		return requestProjection{}, nil, err
	}
	digest, err := digestValue(requestDigestInput{ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload})
	if err != nil || digest != request.RequestDigest {
		return requestProjection{}, nil, ErrConflict
	}
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	payload, err := decodePayload(request.Command, request.Payload, &projection)
	if err != nil {
		return requestProjection{}, nil, err
	}
	return projection, payload, nil
}
