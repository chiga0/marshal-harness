package domain

import (
	"errors"
	"fmt"
	"time"
)

// ProducerKind is the closed enumeration of Candidate producers (ADR 0027).
type ProducerKind string

const (
	// ProducerKindWorker marks the chain root: the Worker's raw observed
	// patch bytes, captured before any normalization.
	ProducerKindWorker ProducerKind = "worker"
	// ProducerKindNormalizer marks a deterministic normalization product
	// derived from a predecessor Candidate.
	ProducerKindNormalizer ProducerKind = "normalizer"
)

// ParseProducerKind rejects every value outside the closed enumeration.
func ParseProducerKind(value string) (ProducerKind, error) {
	kind := ProducerKind(value)
	if kind == ProducerKindWorker || kind == ProducerKindNormalizer {
		return kind, nil
	}
	return "", fmt.Errorf("unknown producer kind %q", value)
}

// Candidate is the ADR 0027 first-class immutable candidate record: an
// append-only authority ledger fact owned by authorityNamespaceId and
// writable only by Core. It is never rewritten in place; supersession
// appends a new record linked by predecessorCandidateDigest.
//
// The record carries two independent identities: contentDigest is the
// content-addressed identity of the candidate content bytes, while
// candidateDigest is the detached canonical digest of the record as a whole
// and is the identity used by predecessor chains and evidence bindings.
// Field names and constraints align verbatim with
// schemas/candidate-record.schema.json.
type Candidate struct {
	APIVersion                 APIVersion   `json:"apiVersion"`
	Kind                       Kind         `json:"kind"`
	TaskID                     string       `json:"taskId"`
	RunID                      string       `json:"runId"`
	AttemptID                  string       `json:"attemptId"`
	AuthorityNamespaceID       string       `json:"authorityNamespaceId"`
	BaseSHA                    string       `json:"baseSha"`
	ContentDigest              string       `json:"contentDigest"`
	ProducerKind               ProducerKind `json:"producerKind"`
	Producer                   string       `json:"producer"`
	PredecessorCandidateDigest string       `json:"predecessorCandidateDigest,omitempty"`
	CreatedAt                  time.Time    `json:"createdAt"`
	AllocationID               string       `json:"allocationId,omitempty"`
	Generation                 uint64       `json:"generation,omitempty"`
	CandidateDigest            string       `json:"candidateDigest"`
}

// Validate enforces the frozen field constraints beyond JSON Schema,
// fail-closed. It also recomputes the detached digest and requires it to
// match the stored candidateDigest, so tampering with any record field is
// rejected at validation time.
func (c Candidate) Validate() error {
	if c.APIVersion != APIVersionV1Alpha1 {
		return errors.New("Candidate apiVersion must be marshal.dev/v1alpha1")
	}
	if c.Kind != KindCandidate {
		return errors.New("Candidate kind mismatch")
	}
	for name, value := range map[string]string{"taskId": c.TaskID, "runId": c.RunID, "attemptId": c.AttemptID, "authorityNamespaceId": c.AuthorityNamespaceID, "producer": c.Producer} {
		if ValidateID(value) != nil {
			return fmt.Errorf("Candidate %s is not a valid Marshal ID", name)
		}
	}
	if !objectIDPattern.MatchString(c.BaseSHA) {
		return errors.New("Candidate baseSha must be a full SHA object id")
	}
	if !canonicalDigestPattern.MatchString(c.ContentDigest) {
		return errors.New("Candidate contentDigest must be a sha256 digest")
	}
	switch c.ProducerKind {
	case ProducerKindWorker:
		if c.PredecessorCandidateDigest != "" {
			return errors.New("Candidate worker records are chain roots and must not carry predecessorCandidateDigest")
		}
	case ProducerKindNormalizer:
		if !canonicalDigestPattern.MatchString(c.PredecessorCandidateDigest) {
			return errors.New("Candidate normalizer records require a predecessorCandidateDigest sha256 digest")
		}
	default:
		return fmt.Errorf("Candidate producerKind %q is outside the closed enumeration", c.ProducerKind)
	}
	if c.CreatedAt.IsZero() {
		return errors.New("Candidate createdAt must be a valid RFC 3339 timestamp")
	}
	if c.AllocationID != "" && ValidateID(c.AllocationID) != nil {
		return errors.New("Candidate allocationId is not a valid Marshal ID")
	}
	if !canonicalDigestPattern.MatchString(c.CandidateDigest) {
		return errors.New("Candidate candidateDigest must be a sha256 digest")
	}
	recomputed, err := c.Digest()
	if err != nil {
		return fmt.Errorf("Candidate detached digest recompute: %w", err)
	}
	if recomputed != c.CandidateDigest {
		return errors.New("Candidate candidateDigest does not match the recomputed detached digest")
	}
	return nil
}

// Digest returns the detached canonical digest of the record document: the
// candidateDigest field is stripped before canonicalization, so producers
// can back-fill the digest and verifiers can strip it and recompute over
// the identical canonical form. Record identity (this digest) is
// independent of content identity (contentDigest).
func (c Candidate) Digest() (string, error) {
	return detachedDigest(c, "candidateDigest")
}

// Equal reports whether two Candidates agree field by field, including both
// digest identities. It is the idempotent coalescing predicate for
// put-if-absent admission.
func (c Candidate) Equal(other Candidate) bool {
	return c.APIVersion == other.APIVersion &&
		c.Kind == other.Kind &&
		c.TaskID == other.TaskID &&
		c.RunID == other.RunID &&
		c.AttemptID == other.AttemptID &&
		c.AuthorityNamespaceID == other.AuthorityNamespaceID &&
		c.BaseSHA == other.BaseSHA &&
		c.ContentDigest == other.ContentDigest &&
		c.ProducerKind == other.ProducerKind &&
		c.Producer == other.Producer &&
		c.PredecessorCandidateDigest == other.PredecessorCandidateDigest &&
		c.CreatedAt.Equal(other.CreatedAt) &&
		c.AllocationID == other.AllocationID &&
		c.Generation == other.Generation &&
		c.CandidateDigest == other.CandidateDigest
}
