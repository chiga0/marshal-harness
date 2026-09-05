package processsupervisor

import (
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type ReconnectOptionsV2 struct {
	FixedMarshalPath string
	ControlDirectory *os.File
	Plan             ReconnectPlan
	Anchor           SessionAnchorV2
	Pending          *PreparedCommandV2
}

// Recovery retains the command's original A0 separately from the subsequent
// owner acquisition. A lost reconnect reply must not rewrite the command base.
type SessionRecoveryEvidenceV2 struct {
	Plan            ReconnectPlan
	Reconciliation  ReconciliationState
	Previous        SessionAnchorV2
	Current         SessionAnchorV2
	Pending         *PreparedCommandEvidenceV2
	ReplayedOutcome *VerifiedCommandOutcomeV2
	MechanicsLocked bool
}

func (c *ClientV2) Recovery() (SessionRecoveryEvidenceV2, bool) {
	if c == nil {
		return SessionRecoveryEvidenceV2{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.recovery == nil {
		return SessionRecoveryEvidenceV2{}, false
	}
	r := *c.recovery
	if r.Pending != nil {
		value := *r.Pending
		r.Pending = &value
	}
	if r.ReplayedOutcome != nil {
		value := *r.ReplayedOutcome
		if value.ProcessReport != nil {
			report := *value.ProcessReport
			value.ProcessReport = &report
		}
		r.ReplayedOutcome = &value
	}
	return r, true
}

func prepareReconnectRequestV2(anchor SessionAnchorV2, plan ReconnectPlan, pending *PreparedCommandV2, nonce string, core CoreIdentity) (reconnectRequestV2, error) {
	b := anchor.Binding
	if anchor.Validate() != nil || core.UID != b.UID || core.GID != b.GID || !sameBinaryObject(core.Binary, b.FixedBinary) ||
		canonical.DigestBytes([]byte(nonce)) != b.SessionNonceDigest || plan.PreviousOwnerEpoch < b.OwnerEpoch {
		return reconnectRequestV2{}, ErrConflict
	}
	r := reconnectRequestV2{SchemaVersion: reconnectSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: b.SessionID, SessionNonce: nonce, Core: core, PreviousOwnerEpoch: plan.PreviousOwnerEpoch, OwnerEpoch: plan.OwnerEpoch,
		PreviousAuthorityHead: plan.PreviousAuthorityHead, CurrentAuthorityHead: plan.CurrentAuthorityHead, ControlOwnerAcquiredFactDigest: plan.ControlOwnerAcquired,
		LastOwnerEpoch: b.OwnerEpoch, LastAuthorityHead: b.CurrentAuthorityHead, LastCommandSequence: b.CommandSequence, LastCommandHead: b.CommandHead,
		LastJournalSequence: b.JournalSequence, LastJournalHead: b.JournalHead}
	if pending != nil {
		if pending.evidence.Validate() != nil || pending.evidence.PreCommand != anchor {
			return reconnectRequestV2{}, ErrConflict
		}
		value := pending.request
		r.PendingRequest = &value
	}
	if r.validate() != nil {
		return reconnectRequestV2{}, ErrInvalid
	}
	return r, nil
}

func commandBaseV2(anchor SessionAnchorV2) journalRecordV2 {
	b := anchor.Binding
	return journalRecordV2{SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: b.SessionID, SessionNonceDigest: b.SessionNonceDigest, Authority: b.Authority, OwnerEpoch: b.OwnerEpoch, CurrentAuthorityHead: b.CurrentAuthorityHead}
}

// Only A0, its exact pending intent, or its exact committed receipt may be
// observed before reconnect. A journal from another session cannot authorize IO.
func validateReconnectJournalV2(state journalStateV2, anchor SessionAnchorV2, pending *PreparedCommandV2) error {
	b := anchor.Binding
	if anchor.Validate() != nil || state.created.SessionID != b.SessionID || state.created.SessionNonceDigest != b.SessionNonceDigest || state.created.Authority != b.Authority {
		return ErrConflict
	}
	if pending != nil && (pending.evidence.Validate() != nil || pending.evidence.PreCommand != anchor) {
		return ErrConflict
	}
	if state.sequence == b.JournalSequence && state.head == b.JournalHead && state.commandSeq == b.CommandSequence && state.commandHead == b.CommandHead && state.pending == nil {
		return nil
	}
	if pending == nil || pending.evidence.Validate() != nil || pending.evidence.PreCommand != anchor {
		return ErrConflict
	}
	intent, _, err := expectedPendingJournalHeadsV2(commandBaseV2(anchor), b.JournalSequence, b.JournalHead, pending.request, nil)
	if err != nil {
		return err
	}
	if state.sequence == b.JournalSequence+1 && state.head == intent && state.pending != nil && state.commandSeq == b.CommandSequence && state.commandHead == b.CommandHead {
		return nil
	}
	receipt, ok := state.receipts[pending.request.CommandID]
	if !ok || receipt.Response == nil || state.pending != nil {
		return ErrConflict
	}
	outcome, err := verifiedCommandOutcomeV2(*pending, *receipt.Response)
	if err != nil || state.sequence != outcome.PostCommand.Binding.JournalSequence || state.head != outcome.PostCommand.Binding.JournalHead || state.commandSeq != outcome.PostCommand.Binding.CommandSequence || state.commandHead != outcome.CommandHead {
		return ErrConflict
	}
	return nil
}

func validateReconnectHandshakeV2(response HandshakeResponseV2, anchor SessionAnchorV2, plan ReconnectPlan, pending *PreparedCommandV2, peer CoreIdentity) (SessionRecoveryEvidenceV2, error) {
	if anchor.Validate() != nil || plan.PreviousOwnerEpoch < anchor.Binding.OwnerEpoch || plan.OwnerEpoch <= plan.PreviousOwnerEpoch || plan.OwnerEpoch > maxSafeJSONInteger ||
		!validDigest(plan.PreviousAuthorityHead) || !validDigest(plan.CurrentAuthorityHead) || plan.PreviousAuthorityHead == plan.CurrentAuthorityHead || !validDigest(plan.ControlOwnerAcquired) {
		return SessionRecoveryEvidenceV2{}, ErrConflict
	}
	r := SessionRecoveryEvidenceV2{Plan: plan, Reconciliation: response.Reconciliation, Previous: anchor, Current: anchor}
	if pending != nil {
		if pending.evidence.Validate() != nil || pending.evidence.PreCommand != anchor {
			return SessionRecoveryEvidenceV2{}, ErrConflict
		}
		value := pending.evidence
		r.Pending = &value
	}
	switch response.Reconciliation {
	case ReconciliationUnchanged, ReconciliationReceiptCommitted:
		if pending == nil {
			if response.Reconciliation != ReconciliationUnchanged || response.ReplayedResponse != nil {
				return SessionRecoveryEvidenceV2{}, ErrConflict
			}
		} else {
			if response.ReplayedResponse == nil {
				return SessionRecoveryEvidenceV2{}, ErrConflict
			}
			outcome, err := verifiedCommandOutcomeV2(*pending, *response.ReplayedResponse)
			if err != nil {
				return SessionRecoveryEvidenceV2{}, err
			}
			r.ReplayedOutcome, r.Current = &outcome, outcome.PostCommand
		}
	case ReconciliationIntentPending:
		if pending == nil || response.ReplayedResponse != nil {
			return SessionRecoveryEvidenceV2{}, ErrConflict
		}
		b := anchor.Binding
		head, _, err := expectedPendingJournalHeadsV2(commandBaseV2(anchor), b.JournalSequence, b.JournalHead, pending.request, nil)
		if err != nil {
			return SessionRecoveryEvidenceV2{}, err
		}
		r.Current.Binding.JournalSequence++
		r.Current.Binding.JournalHead = head
		r.MechanicsLocked = true
	default:
		return SessionRecoveryEvidenceV2{}, ErrConflict
	}
	r.Current.Binding.OwnerEpoch, r.Current.Binding.CurrentAuthorityHead = plan.OwnerEpoch, plan.CurrentAuthorityHead
	if ValidateHandshakeBindingV2(response, r.Current, peer) != nil {
		return SessionRecoveryEvidenceV2{}, ErrConflict
	}
	return r, nil
}

func newReconnectedClientV2(stream deadlineStream, codec *ProtocolCodec, response HandshakeResponseV2, anchor SessionAnchorV2, plan ReconnectPlan, pending *PreparedCommandV2, peer CoreIdentity) (*ClientV2, error) {
	recovery, err := validateReconnectHandshakeV2(response, anchor, plan, pending, peer)
	if err != nil || codec == nil {
		return nil, ErrConflict
	}
	client, err := newClientV2(stream, response, recovery.Current, peer)
	if err != nil {
		return nil, err
	}
	client.codec, client.recovery = codec, &recovery
	if recovery.MechanicsLocked {
		value := *recovery.Pending
		client.pending = &value
	}
	return client, nil
}
