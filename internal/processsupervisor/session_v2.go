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
	var request requestV2
	if len(raw) > MaxWireFrameBytes || strictCanonicalDecode(raw, &request) != nil || request.validate() != nil || request.SessionID != session.core.sessionID {
		return responseV2{}, ErrInvalid
	}
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest,
		CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	payload, err := decodePayload(request.Command, request.Payload, &projection)
	if err != nil {
		return responseV2{}, err
	}
	if prior, ok := session.journal.receipt(request.CommandID); ok {
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
	if err := session.core.admitCommandState(request.Command, request.CurrentAuthorityHead, payload); err != nil {
		return responseV2{}, err
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
