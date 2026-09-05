package processsupervisor

import (
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// sessionV2 executes the ADR 0079 wire generation in the same Supervisor
// process. It reuses command semantics/mechanics, never a v1 request decoder,
// digest, journal or response. Production transport selects it only at S3.
type sessionV2 struct {
	core    Session // Only generation-neutral state/execute/advance are shared.
	journal *journalWriterV2
}

func newSessionV2(bootstrap bootstrapRequestV2, journal *journalWriterV2, mechanics Mechanics, now func() time.Time) (*sessionV2, error) {
	if bootstrap.validate() != nil || journal == nil || mechanics == nil {
		return nil, ErrInvalid
	}
	sequence, _, _ := journal.checkpoint()
	if sequence != 0 {
		// A new Supervisor process cannot adopt a predecessor's wait rights.
		// Reconnect must instead target the still-live exact session.
		return nil, ErrIntervention
	}
	if now == nil {
		now = time.Now
	}
	session := &sessionV2{journal: journal, core: Session{
		mechanics: mechanics, now: now, sessionID: bootstrap.SessionID,
		nonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), authority: bootstrap.Authority,
		launchFact: bootstrap.LaunchAuthorizedFact, ownerEpoch: bootstrap.OwnerEpoch,
		authorityHead: bootstrap.CurrentAuthorityHead, commandHead: commandGenesisDigestV2, state: sessionUnbound,
	}}
	record := session.journalBase()
	record.Kind = journalSessionCreated
	if _, err := journal.append(record); err != nil {
		return nil, err
	}
	return session, nil
}

func (session *sessionV2) journalBase() journalRecordV2 {
	return journalRecordV2{
		SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2,
		LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: session.core.sessionID, SessionNonceDigest: session.core.nonceDigest, Authority: session.core.authority,
		OwnerEpoch: session.core.ownerEpoch, CurrentAuthorityHead: session.core.authorityHead,
	}
}

// handle returns an error without a receipt when admission fails or an effect
// cannot be durably classified. A wire caller must not invent a success or a
// no-effect receipt from that error. Exact committed replays are read-only.
func (session *sessionV2) handle(raw []byte) (responseV2, error) {
	session.core.mu.Lock()
	defer session.core.mu.Unlock()
	return session.handleLocked(raw)
}

func (session *sessionV2) handleLocked(raw []byte) (responseV2, error) {
	return session.handleWithAttachLocked(raw, nil)
}

// handleAttachContinuation is callable only after an authenticated read-only
// Attach. The frozen checkpoint is rechecked under the command lock, so a
// stale observation cannot authorize an intervening command or receipt replay.
func (session *sessionV2) handleAttachContinuation(raw []byte, authority AttachAuthorityV2) (responseV2, error) {
	if session == nil || authority.Validate() != nil {
		return responseV2{}, ErrConflict
	}
	session.core.mu.Lock()
	defer session.core.mu.Unlock()
	if !session.matchesAttachCheckpointLocked(authority) {
		return responseV2{}, ErrConflict
	}
	return session.handleWithAttachLocked(raw, &authority)
}

func (session *sessionV2) matchesAttachCheckpointLocked(a AttachAuthorityV2) bool {
	p, c, j := a.PreviousSupervisor.Binding, &session.core, session.journal.recoverySnapshot("")
	return c.state == sessionBound && c.sessionID == p.SessionID && c.nonceDigest == p.SessionNonceDigest && c.authority == p.Authority &&
		c.ownerEpoch == p.OwnerEpoch && c.authorityHead == p.CurrentAuthorityHead && c.commandSequence == p.CommandSequence && c.commandHead == p.CommandHead &&
		c.lastObservation == a.ChildObservationDigest && j.sequence == p.JournalSequence && j.head == p.JournalHead && j.pending == nil &&
		j.commandSeq == p.CommandSequence && j.commandHead == p.CommandHead && j.ownerEpoch == p.OwnerEpoch && j.authorityHead == p.CurrentAuthorityHead
}

func (session *sessionV2) handleWithAttachLocked(raw []byte, attach *AttachAuthorityV2) (responseV2, error) {
	var request requestV2
	if len(raw) > MaxWireFrameBytes || strictCanonicalDecode(raw, &request) != nil || request.validate() != nil || request.SessionID != session.core.sessionID {
		return responseV2{}, ErrInvalid
	}
	if attach != nil {
		switch request.Command {
		case CommandBindAuthority, CommandCollect, CommandInspect, CommandClose:
		default:
			return responseV2{}, ErrConflict
		}
	}
	projection, payload, err := projectRequestV2(request)
	if err != nil {
		return responseV2{}, err
	}
	if prior, ok := session.journal.receipt(request.CommandID); ok {
		// Response-loss recovery uses the exact journal/intent recovery path,
		// not a second command through a newly observed Attach checkpoint.
		if attach != nil {
			return responseV2{}, ErrConflict
		}
		if !equalProjection(*prior.Request, projection) || validateV2ResponseBinding(*prior.Response, request) != nil {
			return responseV2{}, ErrConflict
		}
		return *prior.Response, nil
	}
	deadline, err := parseDeadline(request.Deadline)
	limit, ok := commandDeadlineLimit(request.Command)
	now := session.core.now().UTC()
	if err != nil || !ok || !deadline.After(now) || deadline.Sub(now) > limit {
		return responseV2{}, reject("process-supervisor-deadline-invalid")
	}
	if request.Sequence != session.core.commandSequence+1 || request.PreviousCommandDigest != session.core.commandHead {
		return responseV2{}, ErrConflict
	}
	if attach != nil && request.Command == CommandBindAuthority {
		value := payload.(BindAuthorityPayload)
		c := &session.core
		if request.CurrentAuthorityHead != c.authorityHead || value.OwnerEpoch != c.ownerEpoch || value.PreviousAuthorityHead != c.authorityHead ||
			value.AuthorityHead == c.authorityHead || value.AuthorityHead != attach.CurrentOwnerBoundFact.AttemptHead || value.SupervisorStartedFactDigest != c.supervisorStartedFact {
			return responseV2{}, ErrConflict
		}
	} else {
		if err := session.core.admitCommandState(request.Command, request.CurrentAuthorityHead, payload); err != nil {
			return responseV2{}, err
		}
	}
	base := session.journalBase()
	base.Kind, base.Request = journalCommandIntent, &projection
	if _, err := session.journal.append(base); err != nil {
		session.core.state = sessionIntervention
		return responseV2{}, err
	}
	result, commandErr := session.core.execute(request.Command, payload, deadline)
	response, err := commandResponseV2(request, projection, result, commandErr)
	if err != nil {
		// An invalid mechanics observation is an unresolved effect, not proof
		// that nothing ran. Preserve the durable intent for intervention.
		session.core.state = sessionIntervention
		return responseV2{}, ErrIntervention
	}
	base.Kind, base.Response = journalCommandReceipt, &response
	if _, err := session.journal.append(base); err != nil {
		session.core.state = sessionIntervention
		return responseV2{}, err
	}
	session.core.commandSequence, session.core.commandHead = request.Sequence, response.CommandHead
	if commandErr == nil {
		// Admission of the next command uses the generation-bound observation,
		// never the unwrapped mechanics report digest.
		result.ObservationDigest = response.ObservationDigest
		session.core.advance(request.Command, payload, result)
		if request.Command != CommandBindAuthority {
			session.core.authorityHead = request.CurrentAuthorityHead
		}
	} else if errors.Is(commandErr, ErrIntervention) {
		session.core.state = sessionIntervention
	}
	return response, nil
}

func projectRequestV2(request requestV2) (requestProjection, any, error) {
	if request.validate() != nil {
		return requestProjection{}, nil, ErrInvalid
	}
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest,
		CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	payload, err := decodePayload(request.Command, request.Payload, &projection)
	return projection, payload, err
}

func commandResponseV2(request requestV2, projection requestProjection, result MechanicsResult, commandErr error) (responseV2, error) {
	status := "ok"
	if commandErr != nil {
		status = "rejected"
		result = MechanicsResult{Disposition: "rejected", ReasonCode: ReasonCode(commandErr), Payload: canonicalEmptyPayload()}
	}
	source, err := v2ObservationSource(status, request.Command, result, projection)
	if err != nil || commandErr == nil && (result.Disposition != "ok" || result.ObservationDigest != source) {
		return responseV2{}, ErrIntervention
	}
	if commandErr == nil && request.Command != CommandBindAuthority && request.Command != CommandAbortUnbound {
		var report ProcessReport
		if strictCanonicalDecode(result.Payload, &report) != nil {
			return responseV2{}, ErrIntervention
		}
		switch request.Command {
		case CommandSpawn:
			if report.State != "exec-stopped" {
				return responseV2{}, ErrIntervention
			}
		case CommandResume:
			if report.State != "running" {
				return responseV2{}, ErrIntervention
			}
		case CommandTerminate, CommandCollect, CommandClose:
			if report.State != "terminal" {
				return responseV2{}, ErrIntervention
			}
		}
	}
	result.ObservationDigest, err = mechanicsObservationDigestV2(request.Command, source)
	if err != nil || validateMechanicsResult(result) != nil {
		return responseV2{}, ErrIntervention
	}
	receipt, err := mechanicsReceiptDigestV2(result)
	if err != nil {
		return responseV2{}, err
	}
	head, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, receipt})
	if err != nil {
		return responseV2{}, err
	}
	response := responseV2{
		SchemaVersion: responseSchemaV2, ProtocolRevision: protocolRevisionV2,
		LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		RequestDigest: request.RequestDigest, Status: status, ReasonCode: result.ReasonCode,
		ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: head, Payload: mustCanonical(result),
	}
	if err := validateV2ResponseBinding(response, request); err != nil {
		return responseV2{}, err
	}
	return response, nil
}
