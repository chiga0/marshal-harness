package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// githubLoginPattern is the canonical representation of an expected merge
// executor principal: the fixed "github-login:" prefix followed by a
// GitHub login (alphanumeric plus hyphen, no leading hyphen). It is the
// authoritative positive source for receipt.mergedBy attribution checks.
var githubLoginPattern = regexp.MustCompile(`^github-login:[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// SCMMergeIntent is the ADR 0032 immutable authority record and the sole
// pre-authorization carrier for a controlled task merge. It is written
// before any remote side effect (including Draft → ready), is never
// rewritten in place, and binds every evidence/authorization digest plus
// the expected merge executor identity. Field names and constraints align
// verbatim with schemas/scm-merge-intent.schema.json.
type SCMMergeIntent struct {
	APIVersion               APIVersion `json:"apiVersion"`
	Kind                     Kind       `json:"kind"`
	IntentID                 string     `json:"intentId"`
	AuthorityNamespaceID     string     `json:"authorityNamespaceId"`
	TaskID                   string     `json:"taskId"`
	RunID                    string     `json:"runId"`
	PublicationRecordID      string     `json:"publicationRecordId"`
	PublicationDigest        string     `json:"publicationDigest"`
	ReviewDecisionDigest     string     `json:"reviewDecisionDigest"`
	VerificationDigest       string     `json:"verificationDigest"`
	EvidenceDigest           string     `json:"evidenceDigest"`
	PolicyDigest             string     `json:"policyDigest"`
	PublishApprovalRecordID  string     `json:"publishApprovalRecordId"`
	PublishApprovalDigest    string     `json:"publishApprovalDigest"`
	RemoteCheckRecordDigest  string     `json:"remoteCheckRecordDigest"`
	RepositoryRef            string     `json:"repositoryRef"`
	PRNumber                 int        `json:"prNumber"`
	HeadOid                  string     `json:"headOid"`
	BaseOid                  string     `json:"baseOid"`
	MergeMethod              string     `json:"mergeMethod"`
	RequestedBy              string     `json:"requestedBy"`
	RequestedAt              time.Time  `json:"requestedAt"`
	ExpectedMergedBy         string     `json:"expectedMergedBy"`
	MergerSecurityDomainID   string     `json:"mergerSecurityDomainId"`
	MergerCredentialIdentity string     `json:"mergerCredentialIdentity"`
	IntentDigest             string     `json:"intentDigest"`
}

// Validate enforces the frozen field constraints beyond JSON Schema,
// fail-closed. It also recomputes the detached intent digest and requires it
// to match the stored intentDigest, so tampering with any record field is
// rejected at validation time and a self-reported digest is never trusted.
func (i SCMMergeIntent) Validate() error {
	if i.APIVersion != APIVersionV1Alpha1 {
		return errors.New("SCMMergeIntent apiVersion must be marshal.dev/v1alpha1")
	}
	if i.Kind != KindSCMMergeIntent {
		return errors.New("SCMMergeIntent kind mismatch")
	}
	for name, value := range map[string]string{
		"intentId":                i.IntentID,
		"authorityNamespaceId":    i.AuthorityNamespaceID,
		"taskId":                  i.TaskID,
		"runId":                   i.RunID,
		"publishApprovalRecordId": i.PublishApprovalRecordID,
	} {
		if ValidateID(value) != nil {
			return fmt.Errorf("SCMMergeIntent %s is not a valid Marshal ID", name)
		}
	}
	for name, value := range map[string]string{
		"publicationRecordId":      i.PublicationRecordID,
		"publicationDigest":        i.PublicationDigest,
		"reviewDecisionDigest":     i.ReviewDecisionDigest,
		"verificationDigest":       i.VerificationDigest,
		"evidenceDigest":           i.EvidenceDigest,
		"policyDigest":             i.PolicyDigest,
		"publishApprovalDigest":    i.PublishApprovalDigest,
		"remoteCheckRecordDigest":  i.RemoteCheckRecordDigest,
		"mergerCredentialIdentity": i.MergerCredentialIdentity,
		"intentDigest":             i.IntentDigest,
	} {
		if !canonicalDigestPattern.MatchString(value) {
			return fmt.Errorf("SCMMergeIntent %s must be a sha256 digest", name)
		}
	}
	// publicationRecordId == publicationDigest is the frozen dual-identity
	// invariant (identity = digest): an intent carrying two different
	// publication identities must fail closed instead of producing a
	// divergent authority reference.
	if i.PublicationRecordID != i.PublicationDigest {
		return errors.New("SCMMergeIntent publicationRecordId must equal publicationDigest")
	}
	if i.RepositoryRef == "" || len(i.RepositoryRef) > 2048 {
		return errors.New("SCMMergeIntent repositoryRef must be a non-empty bounded string")
	}
	if i.PRNumber < 1 {
		return errors.New("SCMMergeIntent prNumber must be at least 1")
	}
	for name, value := range map[string]string{"headOid": i.HeadOid, "baseOid": i.BaseOid} {
		if !objectIDPattern.MatchString(value) {
			return fmt.Errorf("SCMMergeIntent %s must be a full SHA object id", name)
		}
	}
	switch i.MergeMethod {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
	default:
		return fmt.Errorf("SCMMergeIntent mergeMethod %q is outside the closed enumeration", i.MergeMethod)
	}
	if i.RequestedBy == "" || len(i.RequestedBy) > 256 {
		return errors.New("SCMMergeIntent requestedBy must be a non-empty bounded string")
	}
	if i.RequestedAt.IsZero() {
		return errors.New("SCMMergeIntent requestedAt must be a valid RFC 3339 timestamp")
	}
	if !githubLoginPattern.MatchString(i.ExpectedMergedBy) {
		return errors.New("SCMMergeIntent expectedMergedBy must use the canonical github-login:<login> representation")
	}
	if i.ExpectedMergedBy == i.RequestedBy {
		return errors.New("SCMMergeIntent expectedMergedBy must not be the requesting operator identity")
	}
	if i.MergerSecurityDomainID == "" || len(i.MergerSecurityDomainID) > 256 {
		return errors.New("SCMMergeIntent mergerSecurityDomainId must be a non-empty bounded string")
	}
	recomputed, err := i.Digest()
	if err != nil {
		return fmt.Errorf("SCMMergeIntent detached digest recompute: %w", err)
	}
	if recomputed != i.IntentDigest {
		return errors.New("SCMMergeIntent intentDigest does not match the recomputed detached digest")
	}
	return nil
}

// Digest returns the detached canonical digest of the intent document: the
// intentDigest field is blanked to the empty string (retained, never removed)
// before canonicalization, so producers can back-fill the digest and
// verifiers can blank it and recompute over the identical canonical form.
func (i SCMMergeIntent) Digest() (string, error) {
	return blankedDigest(i, "intentDigest")
}

// DetachedIntentDigest recomputes the detached intent digest from a raw
// document: the intentDigest value is blanked to the empty string, then the
// whole document is canonicalized with RFC 8785 JCS and digested. It is the
// authoritative verifier path; a record's self-reported intentDigest is
// never trusted.
func DetachedIntentDigest(data []byte) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	document["intentDigest"] = ""
	blanked, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(blanked)
}

// blankedDigest canonicalizes the document with the named digest field set
// to the empty string (retained, unlike detachedDigest which removes it), so
// producers can back-fill the digest and verifiers can blank it and recompute
// over the identical canonical form. This is the ADR 0032 intentDigest rule
// and must not be confused with the ADR 0026 receiptDigest removal rule.
func blankedDigest(document any, digestField string) (string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", err
	}
	decoded[digestField] = ""
	blanked, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(blanked)
}
