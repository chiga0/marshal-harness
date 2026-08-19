package publication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// MergeAuthorityTransaction is the ADR 0033 B value contract.  It binds the
// immutable SCMMergeIntent, the current PublicationAuthorization (or its
// revocation successor), and every admission digest to one journal fact.  It
// is not persisted or consumed by the legacy sidecar merge path yet.
type MergeAuthorityTransaction struct {
	SchemaVersion              int    `json:"schemaVersion"`
	RecordKind                 string `json:"recordKind"`
	Status                     string `json:"status"`
	AuthorityNamespaceID       string `json:"authorityNamespaceId"`
	TaskID                     string `json:"taskId"`
	RunID                      string `json:"runId"`
	JournalSequence            uint64 `json:"journalSequence"`
	ExpectedPreviousJournalSeq uint64 `json:"expectedPreviousJournalSequence"`
	TransactionDigest          string `json:"transactionDigest"`
	PreviousTransactionDigest  string `json:"previousTransactionDigest"`
	IntentDigest               string `json:"intentDigest"`
	AuthorizationDigest        string `json:"authorizationDigest"`
	RevocationGeneration       uint64 `json:"revocationGeneration"`
	PublicationDigest          string `json:"publicationDigest"`
	ReviewDecisionDigest       string `json:"reviewDecisionDigest"`
	VerificationDigest         string `json:"verificationDigest"`
	EvidenceDigest             string `json:"evidenceDigest"`
	PolicyDigest               string `json:"policyDigest"`
	ApprovalDigest             string `json:"approvalDigest"`
	RemoteCheckDigest          string `json:"remoteCheckDigest"`
	PreparedAt                 string `json:"preparedAt"`
	RevokedAt                  string `json:"revokedAt,omitempty"`
}

const (
	mergeAuthorityPreparedEvent = "publication.merge-authority-prepared"
	mergeAuthorityRevokedEvent  = "publication.merge-authority-revoked"
)

// Digest computes the detached JCS digest of the full transaction payload.
func (t MergeAuthorityTransaction) Digest() (string, error) {
	copy := t
	copy.TransactionDigest = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

// ReplayIdentity is the stable identity used by a Core journal append.  A
// revocation is a successor, not an in-place mutation of the prepared fact.
func (t MergeAuthorityTransaction) ReplayIdentity(eventType string) (string, error) {
	data, err := json.Marshal([]any{eventType, t.AuthorityNamespaceID, t.RunID, t.IntentDigest, t.AuthorizationDigest, t.Status, t.RevocationGeneration})
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

// Validate checks the transaction's closed fields and digest bindings.  It
// performs no journal append and does not authorize a mutation.
func (t MergeAuthorityTransaction) Validate(eventJournalSequence, previousJournalSequence uint64) error {
	if t.SchemaVersion != 1 || t.RecordKind != "MergeAuthorityTransaction" || (t.Status != "prepared" && t.Status != "revoked") {
		return errors.New("invalid merge authority transaction kind or status")
	}
	if t.AuthorityNamespaceID == "" || t.TaskID == "" || t.RunID == "" {
		return errors.New("merge authority transaction identity is incomplete")
	}
	if eventJournalSequence == 0 || previousJournalSequence == 0 || t.JournalSequence != eventJournalSequence || t.ExpectedPreviousJournalSeq != previousJournalSequence || t.JournalSequence != t.ExpectedPreviousJournalSeq+1 {
		return errors.New("merge authority transaction journal sequence is not contiguous")
	}
	for _, digest := range []string{t.IntentDigest, t.AuthorizationDigest, t.PublicationDigest, t.ReviewDecisionDigest, t.VerificationDigest, t.EvidenceDigest, t.PolicyDigest, t.ApprovalDigest, t.RemoteCheckDigest} {
		if !validAnchorDigest(digest) {
			return errors.New("merge authority transaction digest is invalid")
		}
	}
	if t.Status == "prepared" {
		if t.RevocationGeneration != 0 || t.PreviousTransactionDigest != "" || t.RevokedAt != "" {
			return errors.New("prepared merge authority transaction cannot carry revocation fields")
		}
	} else {
		if t.RevocationGeneration == 0 || !validAnchorDigest(t.PreviousTransactionDigest) || t.RevokedAt == "" {
			return errors.New("revoked merge authority transaction lacks successor binding")
		}
	}
	if !validUTC(t.PreparedAt) || (t.RevokedAt != "" && !validUTC(t.RevokedAt)) {
		return errors.New("merge authority transaction timestamp is invalid")
	}
	digest, err := t.Digest()
	if err != nil || digest != t.TransactionDigest {
		return errors.New("merge authority transaction digest mismatch")
	}
	return nil
}

// ValidateMergeAuthoritySuccessor verifies the only allowed transition in
// the contract-only model: one prepared fact is followed by one revoked
// successor. It does not append either fact or authorize a lifecycle change.
func ValidateMergeAuthoritySuccessor(prepared, revoked MergeAuthorityTransaction) error {
	if prepared.Status != "prepared" || revoked.Status != "revoked" {
		return errors.New("merge authority successor has invalid status pair")
	}
	if revoked.JournalSequence != prepared.JournalSequence+1 || revoked.ExpectedPreviousJournalSeq != prepared.JournalSequence {
		return errors.New("merge authority successor sequence is not contiguous")
	}
	if revoked.PreviousTransactionDigest != prepared.TransactionDigest || revoked.RevocationGeneration != 1 {
		return errors.New("merge authority successor does not bind prepared transaction")
	}
	if prepared.AuthorityNamespaceID != revoked.AuthorityNamespaceID || prepared.TaskID != revoked.TaskID || prepared.RunID != revoked.RunID {
		return errors.New("merge authority successor identity changed")
	}
	if prepared.IntentDigest != revoked.IntentDigest || prepared.AuthorizationDigest != revoked.AuthorizationDigest || prepared.PublicationDigest != revoked.PublicationDigest || prepared.ReviewDecisionDigest != revoked.ReviewDecisionDigest || prepared.VerificationDigest != revoked.VerificationDigest || prepared.EvidenceDigest != revoked.EvidenceDigest || prepared.PolicyDigest != revoked.PolicyDigest || prepared.ApprovalDigest != revoked.ApprovalDigest || prepared.RemoteCheckDigest != revoked.RemoteCheckDigest {
		return errors.New("merge authority successor admission binding changed")
	}
	if err := prepared.Validate(prepared.JournalSequence, prepared.ExpectedPreviousJournalSeq); err != nil {
		return fmt.Errorf("prepared transaction invalid: %w", err)
	}
	if err := revoked.Validate(revoked.JournalSequence, revoked.ExpectedPreviousJournalSeq); err != nil {
		return fmt.Errorf("revoked transaction invalid: %w", err)
	}
	return nil
}

// DecodeMergeAuthorityTransaction rejects unknown fields before a future
// reducer can accidentally hash a lossy projection of the input.
func DecodeMergeAuthorityTransaction(data []byte) (MergeAuthorityTransaction, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var transaction MergeAuthorityTransaction
	if err := decoder.Decode(&transaction); err != nil {
		return MergeAuthorityTransaction{}, errors.New("invalid merge authority transaction payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return MergeAuthorityTransaction{}, errors.New("merge authority transaction payload has trailing data")
	}
	return transaction, nil
}

// ValidateMergeAuthorityEvent enforces Core-only producer authority and the
// named same-state allowlist for both transaction events.
func ValidateMergeAuthorityEvent(eventType, actorType, actorID, stateFrom, stateTo string, transaction MergeAuthorityTransaction, eventJournalSequence, previousJournalSequence uint64) error {
	if (eventType != mergeAuthorityPreparedEvent && eventType != mergeAuthorityRevokedEvent) || actorType != mergeCoreActorType || actorID != mergeCoreActorID || stateFrom != ciPendingState || stateTo != ciPendingState {
		return errors.New("merge authority event is not Core-owned or same-state allowlisted")
	}
	if transaction.Status == "prepared" && eventType != mergeAuthorityPreparedEvent || transaction.Status == "revoked" && eventType != mergeAuthorityRevokedEvent {
		return errors.New("merge authority event does not match transaction status")
	}
	return transaction.Validate(eventJournalSequence, previousJournalSequence)
}

func validUTC(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && !strings.Contains(value, " ")
}
