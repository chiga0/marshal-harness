package publication

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// MergeDeliveryAnchor is the journal-bound delivery fact described by
// ADR 0033.  It is deliberately a value object only: the current publisher
// path does not consume it and mergePolicy remains disabled.  Keeping the
// closed payload and digest rules here prevents a future reducer from
// silently accepting a weaker sidecar representation.
type MergeDeliveryAnchor struct {
	SchemaVersion              int    `json:"schemaVersion"`
	RecordKind                 string `json:"recordKind"`
	Status                     string `json:"status"`
	AuthorityNamespaceID       string `json:"authorityNamespaceId"`
	TaskID                     string `json:"taskId"`
	RunID                      string `json:"runId"`
	JournalSequence            uint64 `json:"journalSequence"`
	ExpectedPreviousJournalSeq uint64 `json:"expectedPreviousJournalSequence"`
	LedgerSequence             uint64 `json:"ledgerSequence"`
	PreviousAnchorDigest       string `json:"previousAnchorDigest"`
	PendingAnchorDigest        string `json:"pendingAnchorDigest"`
	AnchorDigest               string `json:"anchorDigest"`
	CanonicalReplayIdentity    string `json:"canonicalReplayIdentity"`
	Operation                  string `json:"operation"`
	DeliveryAttempt            uint64 `json:"deliveryAttempt"`
	IntentDigest               string `json:"intentDigest"`
	AuthorizationDigest        string `json:"authorizationDigest"`
	PublicationDigest          string `json:"publicationDigest"`
	ReviewDecisionDigest       string `json:"reviewDecisionDigest"`
	VerificationDigest         string `json:"verificationDigest"`
	EvidenceDigest             string `json:"evidenceDigest"`
	PolicyDigest               string `json:"policyDigest"`
	ApprovalDigest             string `json:"approvalDigest"`
	RemoteCheckDigest          string `json:"remoteCheckDigest"`
	ExpiresAt                  string `json:"expiresAt"`
	ConsumedAt                 string `json:"consumedAt"`
	ProviderRequestDigest      string `json:"providerRequestDigest"`
}

const mergeDeliveryAnchorEvent = "publication.merge-mutation-fence-consumed"

// Digest returns the detached digest of the complete payload.  The digest
// field itself is blanked before hashing, matching the ADR 0033 JCS rule.
func (a MergeDeliveryAnchor) Digest() (string, error) {
	copy := a
	copy.AnchorDigest = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

// ReplayIdentity returns the canonical single-consumption identity for the
// mutation fence.  It intentionally hashes a JSON array, not a formatted
// string, so no caller can introduce delimiter or escaping ambiguity.
func (a MergeDeliveryAnchor) ReplayIdentity() (string, error) {
	data, err := json.Marshal([]any{mergeDeliveryAnchorEvent, a.AuthorityNamespaceID, a.RunID, a.PendingAnchorDigest, a.IntentDigest, a.Operation, a.DeliveryAttempt})
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

// ValidateMutationFence checks the closed ADR 0033 fence payload.  It does
// not append an event or authorize a mutation; callers must still perform the
// journal CAS, snapshot barrier, and current-ledger recheck in Core.
func (a MergeDeliveryAnchor) ValidateMutationFence(eventJournalSequence, previousJournalSequence uint64) error {
	if a.SchemaVersion != 1 || a.RecordKind != "MergeDeliveryAnchor" || a.Status != "mutation-fence-consumed" {
		return errors.New("invalid merge delivery anchor kind or status")
	}
	if a.AuthorityNamespaceID == "" || a.TaskID == "" || a.RunID == "" {
		return errors.New("merge delivery anchor identity is incomplete")
	}
	if eventJournalSequence == 0 || a.JournalSequence != eventJournalSequence || previousJournalSequence == 0 || a.ExpectedPreviousJournalSeq != previousJournalSequence || a.JournalSequence != a.ExpectedPreviousJournalSeq+1 {
		return errors.New("merge delivery anchor journal sequence is not contiguous")
	}
	if a.LedgerSequence == 0 || a.DeliveryAttempt < 1 || a.DeliveryAttempt > 3 || a.Operation != "ready" && a.Operation != "merge" {
		return errors.New("merge delivery anchor lineage is invalid")
	}
	if a.PreviousAnchorDigest == "" || a.PreviousAnchorDigest != a.PendingAnchorDigest {
		return errors.New("merge delivery anchor pending lineage is invalid")
	}
	for _, digest := range []string{a.PreviousAnchorDigest, a.PendingAnchorDigest, a.IntentDigest, a.AuthorizationDigest, a.PublicationDigest, a.ReviewDecisionDigest, a.VerificationDigest, a.EvidenceDigest, a.PolicyDigest, a.ApprovalDigest, a.RemoteCheckDigest, a.ProviderRequestDigest} {
		if !validAnchorDigest(digest) {
			return errors.New("merge delivery anchor digest is invalid")
		}
	}
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	consumedAt, consumedErr := time.Parse(time.RFC3339Nano, a.ConsumedAt)
	if expiresErr != nil || consumedErr != nil || expiresAt.Location() != time.UTC || consumedAt.Location() != time.UTC || !consumedAt.Before(expiresAt) {
		return errors.New("merge delivery anchor timestamps are invalid")
	}
	digest, err := a.Digest()
	if err != nil || digest != a.AnchorDigest {
		return errors.New("merge delivery anchor digest mismatch")
	}
	identity, err := a.ReplayIdentity()
	if err != nil || identity != a.CanonicalReplayIdentity {
		return errors.New("merge delivery anchor replay identity mismatch")
	}
	return nil
}

func validAnchorDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, ch := range value[len("sha256:"):] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
