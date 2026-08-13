package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ADR 0026 frozen merge-method enumeration: the GitHub API does not expose
// the merge method directly, so capture derives it deterministically from the
// merge commit parents/tree and fails closed outside this closed set.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// ADR 0026 frozen PublicationReconcileRecord enumerations.
const (
	ReconcileTypeAcceptAfterMerge = "accept-after-merge"
	ReconcileObservedStateBlocked = StateBlocked
	ReconcileDecidedStateAccepted = StateAccepted
)

var objectIDPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
var canonicalDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// SCMMergeReceipt is the ADR 0026 immutable authority record proving one SCM
// merge fact. Once written it is never rewritten: a conflicting re-capture
// fails closed. Field names and constraints align verbatim with
// schemas/scm-merge-receipt.schema.json.
type SCMMergeReceipt struct {
	APIVersion           APIVersion `json:"apiVersion"`
	Kind                 Kind       `json:"kind"`
	ReceiptID            string     `json:"receiptId"`
	AuthorityNamespaceID string     `json:"authorityNamespaceId"`
	RunID                string     `json:"runId"`
	PublicationRecordID  string     `json:"publicationRecordId"`
	RepositoryRef        string     `json:"repositoryRef"`
	PRNumber             int        `json:"prNumber"`
	HeadOid              string     `json:"headOid"`
	BaseOid              string     `json:"baseOid"`
	MergeCommitSha       string     `json:"mergeCommitSha"`
	MergedAt             time.Time  `json:"mergedAt"`
	MergedBy             string     `json:"mergedBy"`
	MergeMethod          string     `json:"mergeMethod"`
	CapturedAt           time.Time  `json:"capturedAt"`
	ReceiptDigest        string     `json:"receiptDigest"`
}

// Validate enforces the frozen field constraints beyond JSON Schema.
func (r SCMMergeReceipt) Validate() error {
	if r.APIVersion != APIVersionV1Alpha1 {
		return errors.New("SCMMergeReceipt apiVersion must be marshal.dev/v1alpha1")
	}
	if r.Kind != KindSCMMergeReceipt {
		return errors.New("SCMMergeReceipt kind mismatch")
	}
	for name, value := range map[string]string{"receiptId": r.ReceiptID, "authorityNamespaceId": r.AuthorityNamespaceID, "runId": r.RunID, "publicationRecordId": r.PublicationRecordID} {
		if ValidateID(value) != nil {
			return fmt.Errorf("SCMMergeReceipt %s is not a valid Marshal ID", name)
		}
	}
	if r.RepositoryRef == "" || len(r.RepositoryRef) > 2048 {
		return errors.New("SCMMergeReceipt repositoryRef must be a non-empty bounded string")
	}
	if r.PRNumber < 1 {
		return errors.New("SCMMergeReceipt prNumber must be at least 1")
	}
	for name, value := range map[string]string{"headOid": r.HeadOid, "baseOid": r.BaseOid, "mergeCommitSha": r.MergeCommitSha} {
		if !objectIDPattern.MatchString(value) {
			return fmt.Errorf("SCMMergeReceipt %s must be a full SHA object id", name)
		}
	}
	if r.MergedAt.IsZero() {
		return errors.New("SCMMergeReceipt mergedAt must be a valid RFC 3339 timestamp")
	}
	if r.MergedBy == "" || len(r.MergedBy) > 256 {
		return errors.New("SCMMergeReceipt mergedBy must be a non-empty bounded string")
	}
	switch r.MergeMethod {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
	default:
		return fmt.Errorf("SCMMergeReceipt mergeMethod %q is outside the closed enumeration", r.MergeMethod)
	}
	if r.CapturedAt.IsZero() {
		return errors.New("SCMMergeReceipt capturedAt must be a valid RFC 3339 timestamp")
	}
	if !canonicalDigestPattern.MatchString(r.ReceiptDigest) {
		return errors.New("SCMMergeReceipt receiptDigest must be a sha256 digest")
	}
	return nil
}

// Digest returns the canonical digest of the receipt document with the
// receiptDigest field detached (the verifier strips the field and recomputes).
func (r SCMMergeReceipt) Digest() (string, error) {
	return detachedDigest(r, "receiptDigest")
}

// PublicationReconcileRecord is the ADR 0026 append-only authority record
// reconciling a post-publication terminal BLOCKED run to ACCEPTED. Field
// names and constraints align verbatim with
// schemas/publication-reconcile-record.schema.json.
type PublicationReconcileRecord struct {
	APIVersion            APIVersion `json:"apiVersion"`
	Kind                  Kind       `json:"kind"`
	ReconcileID           string     `json:"reconcileId"`
	AuthorityNamespaceID  string     `json:"authorityNamespaceId"`
	RunID                 string     `json:"runId"`
	SCMMergeReceiptID     string     `json:"scmMergeReceiptId"`
	ReconcileType         string     `json:"reconcileType"`
	ObservedState         State      `json:"observedState"`
	DecidedState          State      `json:"decidedState"`
	EvidenceDigests       []string   `json:"evidenceDigests"`
	ReconcileReason       string     `json:"reconcileReason"`
	ReconciledBy          string     `json:"reconciledBy"`
	ReconciledAt          time.Time  `json:"reconciledAt"`
	ReconcileRecordDigest string     `json:"reconcileRecordDigest"`
}

var reconcileReasonPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*([._-][A-Za-z0-9]+)*$`)

// Validate enforces the frozen field constraints beyond JSON Schema.
func (r PublicationReconcileRecord) Validate() error {
	if r.APIVersion != APIVersionV1Alpha1 {
		return errors.New("PublicationReconcileRecord apiVersion must be marshal.dev/v1alpha1")
	}
	if r.Kind != KindPublicationReconcileRecord {
		return errors.New("PublicationReconcileRecord kind mismatch")
	}
	for name, value := range map[string]string{"reconcileId": r.ReconcileID, "authorityNamespaceId": r.AuthorityNamespaceID, "runId": r.RunID, "scmMergeReceiptId": r.SCMMergeReceiptID} {
		if ValidateID(value) != nil {
			return fmt.Errorf("PublicationReconcileRecord %s is not a valid Marshal ID", name)
		}
	}
	if r.ReconcileType != ReconcileTypeAcceptAfterMerge {
		return fmt.Errorf("PublicationReconcileRecord reconcileType %q is outside the closed enumeration", r.ReconcileType)
	}
	if r.ObservedState != ReconcileObservedStateBlocked {
		return errors.New("PublicationReconcileRecord observedState must be BLOCKED")
	}
	if r.DecidedState != ReconcileDecidedStateAccepted {
		return errors.New("PublicationReconcileRecord decidedState must be ACCEPTED")
	}
	if len(r.EvidenceDigests) == 0 {
		return errors.New("PublicationReconcileRecord evidenceDigests must not be empty")
	}
	seen := make(map[string]bool, len(r.EvidenceDigests))
	for _, digest := range r.EvidenceDigests {
		if !canonicalDigestPattern.MatchString(digest) {
			return errors.New("PublicationReconcileRecord evidenceDigests entries must be sha256 digests")
		}
		if seen[digest] {
			return errors.New("PublicationReconcileRecord evidenceDigests entries must be unique")
		}
		seen[digest] = true
	}
	if r.ReconcileReason == "" || len(r.ReconcileReason) > 160 || !reconcileReasonPattern.MatchString(r.ReconcileReason) {
		return errors.New("PublicationReconcileRecord reconcileReason must be a machine-readable reason code")
	}
	if r.ReconciledBy == "" || len(r.ReconciledBy) > 256 {
		return errors.New("PublicationReconcileRecord reconciledBy must be a non-empty bounded string")
	}
	if r.ReconciledAt.IsZero() {
		return errors.New("PublicationReconcileRecord reconciledAt must be a valid RFC 3339 timestamp")
	}
	if !canonicalDigestPattern.MatchString(r.ReconcileRecordDigest) {
		return errors.New("PublicationReconcileRecord reconcileRecordDigest must be a sha256 digest")
	}
	return nil
}

// Digest returns the canonical digest of the record document with the
// reconcileRecordDigest field detached.
func (r PublicationReconcileRecord) Digest() (string, error) {
	return detachedDigest(r, "reconcileRecordDigest")
}

// detachedDigest canonicalizes the document with the named digest field
// removed, so producers can back-fill the digest and verifiers can strip it
// and recompute over the identical canonical form.
func detachedDigest(document any, digestField string) (string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", err
	}
	delete(decoded, digestField)
	trimmed, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(trimmed)
}
