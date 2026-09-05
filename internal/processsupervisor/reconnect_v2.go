package processsupervisor

import "github.com/chiga0/marshal-harness/internal/canonical"

type reconnectResolutionV2 struct {
	State    ReconciliationState
	Response *responseV2
}

// Keep the point-of-no-return separate from the error. The transport must
// silently close on failedAfterMechanics, never claim a no-effect rejection.
type reconnectAttemptResultV2 struct {
	resolution  reconnectResolutionV2
	disposition reconnectAttemptDisposition
	err         error
}

// reconnectAttempt is only for a still-live exact Supervisor. It cannot
// recover predecessor wait rights or reconstruct a Session from a journal.
func (session *sessionV2) reconnectAttempt(request reconnectRequestV2, observed CoreIdentity) reconnectAttemptResultV2 {
	session.core.mu.Lock()
	defer session.core.mu.Unlock()
	core := &session.core
	if request.validate() != nil || request.SessionID != core.sessionID ||
		canonical.DigestBytes([]byte(request.SessionNonce)) != core.nonceDigest ||
		request.PreviousOwnerEpoch != core.ownerEpoch || request.PreviousAuthorityHead != core.authorityHead ||
		request.CurrentAuthorityHead == request.PreviousAuthorityHead || request.LastOwnerEpoch > request.PreviousOwnerEpoch ||
		!sameCoreIdentity(request.Core, observed) || core.state == sessionIntervention {
		return reconnectAttemptResultV2{disposition: reconnectRejectedBeforeMechanics, err: ErrConflict}
	}
	resolution, err := session.reconcilePendingLocked(request)
	if err != nil {
		return reconnectAttemptResultV2{disposition: reconnectRejectedBeforeMechanics, err: err}
	}
	disposition := reconnectResolvedWithoutMechanics
	if resolution.State == ReconciliationUnchanged && request.PendingRequest != nil {
		raw, err := CanonicalProtocolMessage(*request.PendingRequest)
		if err != nil {
			return reconnectAttemptResultV2{disposition: reconnectRejectedBeforeMechanics, err: ErrConflict}
		}
		disposition = reconnectResolvedAfterMechanics
		response, executeErr := session.handleLocked(raw)
		_, receiptHead, headErr := expectedPendingJournalHeadsV2(session.reconnectBase(request), request.LastJournalSequence, request.LastJournalHead, *request.PendingRequest, &response)
		sequence, head, pending := session.journal.checkpoint()
		if executeErr != nil || headErr != nil || validateV2ResponseBinding(response, *request.PendingRequest) != nil ||
			core.commandSequence != request.PendingRequest.Sequence || core.commandHead != response.CommandHead ||
			sequence != request.LastJournalSequence+2 || head != receiptHead || pending || core.state == sessionIntervention {
			core.state = sessionIntervention
			return reconnectAttemptResultV2{disposition: reconnectFailedAfterMechanics, err: ErrIntervention}
		}
		resolution.Response = &response
	}
	core.ownerEpoch, core.authorityHead = request.OwnerEpoch, request.CurrentAuthorityHead
	if resolution.State == ReconciliationIntentPending {
		core.state = sessionIntervention
	}
	return reconnectAttemptResultV2{resolution: resolution, disposition: disposition}
}

func (session *sessionV2) reconnectBase(request reconnectRequestV2) journalRecordV2 {
	base := session.journalBase()
	base.OwnerEpoch, base.CurrentAuthorityHead = request.LastOwnerEpoch, request.LastAuthorityHead
	return base
}

func (session *sessionV2) reconcilePendingLocked(request reconnectRequestV2) (reconnectResolutionV2, error) {
	var id string
	var projection requestProjection
	if request.PendingRequest != nil {
		pending := *request.PendingRequest
		value, payload, err := projectRequestV2(pending)
		if err != nil || pending.SessionID != session.core.sessionID || pending.Sequence != request.LastCommandSequence+1 || pending.PreviousCommandDigest != request.LastCommandHead {
			return reconnectResolutionV2{}, ErrConflict
		}
		if spawn, ok := payload.(SpawnPayload); ok && spawn.SourceGateRevision != SourceGateRevisionV1 {
			return reconnectResolutionV2{}, ErrIntervention
		}
		id, projection = pending.CommandID, value
	}
	state := session.journal.recoverySnapshot(id)
	core := &session.core
	if state.sequence == request.LastJournalSequence && state.head == request.LastJournalHead && state.ownerEpoch == request.LastOwnerEpoch &&
		state.authorityHead == request.LastAuthorityHead && core.commandSequence == request.LastCommandSequence && core.commandHead == request.LastCommandHead && state.pending == nil {
		return reconnectResolutionV2{State: ReconciliationUnchanged}, nil
	}
	if request.PendingRequest == nil {
		return reconnectResolutionV2{}, ErrConflict
	}
	pending := *request.PendingRequest
	intentHead, _, err := expectedPendingJournalHeadsV2(session.reconnectBase(request), request.LastJournalSequence, request.LastJournalHead, pending, nil)
	if err != nil {
		return reconnectResolutionV2{}, ErrConflict
	}
	if state.sequence == request.LastJournalSequence+1 && state.head == intentHead && state.pending != nil &&
		core.commandSequence == request.LastCommandSequence && core.commandHead == request.LastCommandHead &&
		state.pending.OwnerEpoch == request.LastOwnerEpoch && state.pending.CurrentAuthorityHead == request.LastAuthorityHead &&
		state.pending.PreviousRecordDigest == request.LastJournalHead && equalProjection(*state.pending.Request, projection) {
		return reconnectResolutionV2{State: ReconciliationIntentPending}, nil
	}
	if state.sequence != request.LastJournalSequence+2 || state.pending != nil || core.commandSequence != pending.Sequence {
		return reconnectResolutionV2{}, ErrConflict
	}
	stored, ok := state.receipts[id]
	if !ok || stored.OwnerEpoch != request.LastOwnerEpoch || stored.CurrentAuthorityHead != request.LastAuthorityHead || stored.PreviousRecordDigest != intentHead ||
		!equalProjection(*stored.Request, projection) || core.commandHead != stored.Response.CommandHead || validateV2ResponseBinding(*stored.Response, pending) != nil {
		return reconnectResolutionV2{}, ErrConflict
	}
	_, receiptHead, err := expectedPendingJournalHeadsV2(session.reconnectBase(request), request.LastJournalSequence, request.LastJournalHead, pending, stored.Response)
	if err != nil || state.head != receiptHead {
		return reconnectResolutionV2{}, ErrConflict
	}
	return reconnectResolutionV2{State: ReconciliationReceiptCommitted, Response: stored.Response}, nil
}

// Construct exact v2 heads from the last authenticated pre-command anchor A0,
// not the request's possibly newer authority head. No v1 defaults participate.
func expectedPendingJournalHeadsV2(base journalRecordV2, sequence uint64, head string, request requestV2, response *responseV2) (string, string, error) {
	projection, _, err := projectRequestV2(request)
	if err != nil || base.SessionID != request.SessionID || response != nil && validateV2ResponseBinding(*response, request) != nil {
		return "", "", ErrConflict
	}
	return expectedProjectedJournalHeadsV2(base, sequence, head, projection, response)
}

func expectedProjectedJournalHeadsV2(base journalRecordV2, sequence uint64, head string, projection requestProjection, response *responseV2) (string, string, error) {
	if validateProjection(projection) != nil || sequence == 0 || sequence > maxSafeJSONInteger-2 {
		return "", "", ErrConflict
	}
	var err error
	intent := base
	intent.JournalSequence, intent.PreviousRecordDigest = sequence+1, head
	intent.Kind, intent.Request, intent.Response = journalCommandIntent, &projection, nil
	intent.RecordDigest, err = intent.detachedDigest()
	if err != nil || intent.validate(head, sequence+1) != nil {
		return "", "", ErrConflict
	}
	if response == nil {
		return intent.RecordDigest, "", nil
	}
	if response.SessionID != base.SessionID || validateStoredResponseV2(*response, projection) != nil {
		return "", "", ErrConflict
	}
	receipt := intent
	receipt.JournalSequence, receipt.PreviousRecordDigest = sequence+2, intent.RecordDigest
	receipt.Kind, receipt.Response = journalCommandReceipt, response
	receipt.RecordDigest, err = receipt.detachedDigest()
	if err != nil || receipt.validate(intent.RecordDigest, sequence+2) != nil {
		return "", "", ErrConflict
	}
	return intent.RecordDigest, receipt.RecordDigest, nil
}
