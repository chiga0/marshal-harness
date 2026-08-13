// Typed cross-domain edge records (ADR 0018 §7).
//
// This file is the record layer only: it defines the three typed edge
// records, their fail-closed structural validation, their detached canonical
// digests, replay keys and use-time validity. Runtime issuance, revocation,
// current-ledger recheck wiring and consumer-side authorization decisions
// based on these edges are deliberately not implemented here; they remain
// scheduled for M9.

package authority

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Sentinel errors exposed by the typed edge records. Every edge-specific
// failure wraps exactly one of them, so fixtures can assert a fixed
// sentinel string.
var (
	// ErrEdgePair rejects a source/target trustDomainKind pair that is
	// outside the closed typed-edge matrix.
	ErrEdgePair = errors.New("trustDomainKind pair outside the legal typed-edge matrix")

	// ErrEdgeOperation rejects an operation that is outside the closed
	// enumeration of its edge type.
	ErrEdgeOperation = errors.New("operation outside the closed enumeration")

	// ErrEdgeDigest rejects a record whose stored edgeDigest does not match
	// the recomputed detached canonical digest.
	ErrEdgeDigest = errors.New("edgeDigest does not match the detached canonical digest")

	// ErrEdgeRevoked rejects using an edge whose revocationGeneration is
	// greater than zero.
	ErrEdgeRevoked = errors.New("edge is revoked")

	// ErrEdgeExpired rejects using an edge past its expiry.
	ErrEdgeExpired = errors.New("edge is expired")
)

// DispatchResultOperation is the closed enumeration of
// DispatchResultCapability operations. The enumeration is frozen by this
// implementation and consists of exactly the two operations below.
type DispatchResultOperation string

const (
	// DispatchResultOperationRead authorizes the target actor to read the
	// bound dispatch result.
	DispatchResultOperationRead DispatchResultOperation = "dispatch-result-read"

	// DispatchResultOperationAccept authorizes the target actor to accept
	// the bound dispatch result.
	DispatchResultOperationAccept DispatchResultOperation = "dispatch-result-accept"
)

// Validate rejects every value outside the closed enumeration.
func (operation DispatchResultOperation) Validate() error {
	switch operation {
	case DispatchResultOperationRead, DispatchResultOperationAccept:
		return nil
	default:
		return fmt.Errorf("authority: dispatchResultCapability: operation %q: %w", string(operation), ErrEdgeOperation)
	}
}

// MaterialAccessOperation is the closed enumeration of MaterialAccessGrant
// operations. The enumeration is frozen by this implementation and consists
// of exactly the two operations below.
type MaterialAccessOperation string

const (
	// MaterialAccessOperationRead authorizes the target actor to read the
	// bound material.
	MaterialAccessOperationRead MaterialAccessOperation = "material-read"

	// MaterialAccessOperationWrite authorizes the target actor to write the
	// bound material.
	MaterialAccessOperationWrite MaterialAccessOperation = "material-write"
)

// Validate rejects every value outside the closed enumeration.
func (operation MaterialAccessOperation) Validate() error {
	switch operation {
	case MaterialAccessOperationRead, MaterialAccessOperationWrite:
		return nil
	default:
		return fmt.Errorf("authority: materialAccessGrant: operation %q: %w", string(operation), ErrEdgeOperation)
	}
}

// PublicationOperation is the closed enumeration of PublicationAuthorization
// operations. The enumeration is frozen by this implementation and consists
// of exactly the two operations below.
type PublicationOperation string

const (
	// PublicationOperationSubmit authorizes the target actor to submit the
	// bound publication.
	PublicationOperationSubmit PublicationOperation = "publication-submit"

	// PublicationOperationChecksRead authorizes the target actor to read the
	// checks of the bound publication.
	PublicationOperationChecksRead PublicationOperation = "publication-checks-read"
)

// Validate rejects every value outside the closed enumeration.
func (operation PublicationOperation) Validate() error {
	switch operation {
	case PublicationOperationSubmit, PublicationOperationChecksRead:
		return nil
	default:
		return fmt.Errorf("authority: publicationAuthorization: operation %q: %w", string(operation), ErrEdgeOperation)
	}
}

// requireTypedEdgePair enforces the closed matrix of legal source->target
// trustDomainKind pairs shared by every typed edge (ADR 0018 §7):
// execution->data-capability, publication->data-capability and
// execution->publication. Every other combination is rejected.
func requireTypedEdgePair(record string, source TrustDomainKind, target TrustDomainKind) error {
	switch {
	case source == TrustDomainKindExecution && target == TrustDomainKindDataCapability:
		return nil
	case source == TrustDomainKindPublication && target == TrustDomainKindDataCapability:
		return nil
	case source == TrustDomainKindExecution && target == TrustDomainKindPublication:
		return nil
	default:
		return fmt.Errorf("authority: %s: %s->%s: %w", record, string(source), string(target), ErrEdgePair)
	}
}

// validateEdgeIdentity fails closed on a forged or missing Core issuer, on
// any invalid actor triple and on any trustDomainKind pair outside the
// closed typed-edge matrix. The issuer is statically an
// AuthorityNamespaceId; SecurityDomainId is a distinct Go type and cannot
// occupy the issuer field, and a wire document shaped like a security domain
// decodes into an AuthorityNamespaceId with empty members, which Validate
// rejects.
func validateEdgeIdentity(record string, issuer AuthorityNamespaceId, sourceActor SecurityDomainId, targetActor SecurityDomainId) error {
	if err := issuer.Validate(); err != nil {
		return err
	}
	if err := sourceActor.Validate(); err != nil {
		return err
	}
	if err := targetActor.Validate(); err != nil {
		return err
	}
	return requireTypedEdgePair(record, sourceActor.TrustDomainKind, targetActor.TrustDomainKind)
}

// validateExpiry accepts an empty expiry (the edge never expires) and
// otherwise requires a non-zero RFC 3339 timestamp.
func validateExpiry(record, expiry string) error {
	if expiry == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return fmt.Errorf("authority: %s: expiry must be an RFC 3339 timestamp or empty", record)
	}
	if parsed.IsZero() {
		return fmt.Errorf("authority: %s: expiry must not be the zero time", record)
	}
	return nil
}

// validateEdgeUse rejects revoked edges and edges whose expiry is in the
// past at now. Callers must run the structural Validate first; a revoked
// edge remains a structurally valid ledger record, so revocation is a
// use-time rejection only.
func validateEdgeUse(record string, revocationGeneration uint64, expiry string, now time.Time) error {
	if revocationGeneration > 0 {
		return fmt.Errorf("authority: %s: %w", record, ErrEdgeRevoked)
	}
	if expiry == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return fmt.Errorf("authority: %s: expiry must be an RFC 3339 timestamp or empty", record)
	}
	if now.After(parsed) {
		return fmt.Errorf("authority: %s: %w", record, ErrEdgeExpired)
	}
	return nil
}

// requireMatchingEdgeDigest fails closed unless edgeDigest is a well-formed
// sha256 digest equal to the recomputed detached canonical digest.
func requireMatchingEdgeDigest(record, edgeDigest, recomputed string) error {
	if err := requireDigest(record+".edgeDigest", edgeDigest); err != nil {
		return err
	}
	if edgeDigest != recomputed {
		return fmt.Errorf("authority: %s: %w", record, ErrEdgeDigest)
	}
	return nil
}

// canonicalEdgeBytes runs raw edge JSON through the RFC 8785 admission gate
// shared by every typed edge digest (internal/canonical), which recursively
// rejects duplicate object members and maps every rejection to the fixed
// canonical.ErrRejected sentinel.
func canonicalEdgeBytes(raw []byte) ([]byte, error) {
	canonicalBytes, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("authority: edge canonical: %w", err)
	}
	return canonicalBytes, nil
}

// detachedEdgeDigest returns the sha256 digest of the canonical serialization
// of recordValue, whose own edgeDigest field the caller detached (zeroed)
// before serialization. The pipeline is json.Marshal followed by
// internal/canonical JSON followed by DigestBytes, the detached digest style
// shared with effect.go.
func detachedEdgeDigest(record string, recordValue any) (string, error) {
	raw, err := json.Marshal(recordValue)
	if err != nil {
		return "", fmt.Errorf("authority: %s: marshal: %w", record, err)
	}
	canonicalBytes, err := canonicalEdgeBytes(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalBytes), nil
}

// DispatchResultCapability is the typed cross-domain edge record by which the
// Core attaches a dispatch-result capability to a bound attempt and
// allocation (ADR 0018 §7). The issuer is always the Core
// AuthorityNamespaceId; sourceActor and targetActor are provider
// SecurityDomainIds whose trustDomainKind pair must belong to the closed
// typed-edge matrix. Generation is the issuance generation of the edge;
// revocationGeneration stays zero until the edge is revoked.
//
// Record layer only: runtime issuance, revocation, current-ledger recheck
// wiring and consumer-side authorization decisions over this edge are left
// to M9.
type DispatchResultCapability struct {
	Issuer               AuthorityNamespaceId    `json:"issuer"`
	SourceActor          SecurityDomainId        `json:"sourceActor"`
	TargetActor          SecurityDomainId        `json:"targetActor"`
	Operation            DispatchResultOperation `json:"operation"`
	BoundAttemptId       string                  `json:"boundAttemptId"`
	BoundAllocationId    string                  `json:"boundAllocationId"`
	Expiry               string                  `json:"expiry"`
	Generation           uint64                  `json:"generation"`
	RevocationGeneration uint64                  `json:"revocationGeneration"`
	EdgeDigest           string                  `json:"edgeDigest"`
}

// Validate fails closed on a forged or missing issuer, an invalid actor
// identity, a trustDomainKind pair outside the closed typed-edge matrix, an
// operation outside the closed enumeration, a missing binding field, a
// malformed expiry or a stored edgeDigest that does not match the
// recomputed detached canonical digest. A revoked edge remains structurally
// valid; use-time rejection lives in ValidAt.
func (edge DispatchResultCapability) Validate() error {
	if err := validateEdgeIdentity("dispatchResultCapability", edge.Issuer, edge.SourceActor, edge.TargetActor); err != nil {
		return err
	}
	if err := edge.Operation.Validate(); err != nil {
		return err
	}
	if err := requireText("dispatchResultCapability.boundAttemptId", edge.BoundAttemptId); err != nil {
		return err
	}
	if err := requireText("dispatchResultCapability.boundAllocationId", edge.BoundAllocationId); err != nil {
		return err
	}
	if err := validateExpiry("dispatchResultCapability", edge.Expiry); err != nil {
		return err
	}
	recomputed, err := edge.Digest()
	if err != nil {
		return err
	}
	return requireMatchingEdgeDigest("dispatchResultCapability", edge.EdgeDigest, recomputed)
}

// Digest returns the sha256 digest of the record with its own edgeDigest
// field detached (zeroed before serialization), canonicalized through RFC
// 8785 JCS. The digest is deterministic for identical field values.
func (edge DispatchResultCapability) Digest() (string, error) {
	detached := edge
	detached.EdgeDigest = ""
	return detachedEdgeDigest("dispatchResultCapability", detached)
}

// ReplayKey returns the canonical digest of the full edge identity with the
// stored edgeDigest itself detached; replayed submissions of the same edge
// therefore coalesce onto the same replay key.
func (edge DispatchResultCapability) ReplayKey() (string, error) {
	return edge.Digest()
}

// ValidAt fails closed unless the edge is structurally valid, unrevoked and
// unexpired at now. An empty expiry never expires; the edge remains usable
// up to and including the expiry instant.
func (edge DispatchResultCapability) ValidAt(now time.Time) error {
	if err := edge.Validate(); err != nil {
		return err
	}
	return validateEdgeUse("dispatchResultCapability", edge.RevocationGeneration, edge.Expiry, now)
}

// MaterialAccessGrant is the typed cross-domain edge record by which the
// Core grants access to a bound material under a scope restriction
// (ADR 0018 §7). The issuer is always the Core AuthorityNamespaceId;
// sourceActor and targetActor are provider SecurityDomainIds whose
// trustDomainKind pair must belong to the closed typed-edge matrix.
// Generation is the issuance generation of the edge; revocationGeneration
// stays zero until the edge is revoked.
//
// Record layer only: runtime issuance, revocation, current-ledger recheck
// wiring and consumer-side authorization decisions over this edge are left
// to M9.
type MaterialAccessGrant struct {
	Issuer               AuthorityNamespaceId    `json:"issuer"`
	SourceActor          SecurityDomainId        `json:"sourceActor"`
	TargetActor          SecurityDomainId        `json:"targetActor"`
	Operation            MaterialAccessOperation `json:"operation"`
	MaterialId           string                  `json:"materialId"`
	ScopeRestriction     string                  `json:"scopeRestriction"`
	Expiry               string                  `json:"expiry"`
	Generation           uint64                  `json:"generation"`
	RevocationGeneration uint64                  `json:"revocationGeneration"`
	EdgeDigest           string                  `json:"edgeDigest"`
}

// Validate fails closed on a forged or missing issuer, an invalid actor
// identity, a trustDomainKind pair outside the closed typed-edge matrix, an
// operation outside the closed enumeration, a missing binding field, a
// malformed expiry or a stored edgeDigest that does not match the
// recomputed detached canonical digest. A revoked grant remains structurally
// valid; use-time rejection lives in ValidAt.
func (grant MaterialAccessGrant) Validate() error {
	if err := validateEdgeIdentity("materialAccessGrant", grant.Issuer, grant.SourceActor, grant.TargetActor); err != nil {
		return err
	}
	if err := grant.Operation.Validate(); err != nil {
		return err
	}
	if err := requireText("materialAccessGrant.materialId", grant.MaterialId); err != nil {
		return err
	}
	if err := requireText("materialAccessGrant.scopeRestriction", grant.ScopeRestriction); err != nil {
		return err
	}
	if err := validateExpiry("materialAccessGrant", grant.Expiry); err != nil {
		return err
	}
	recomputed, err := grant.Digest()
	if err != nil {
		return err
	}
	return requireMatchingEdgeDigest("materialAccessGrant", grant.EdgeDigest, recomputed)
}

// Digest returns the sha256 digest of the record with its own edgeDigest
// field detached (zeroed before serialization), canonicalized through RFC
// 8785 JCS. The digest is deterministic for identical field values.
func (grant MaterialAccessGrant) Digest() (string, error) {
	detached := grant
	detached.EdgeDigest = ""
	return detachedEdgeDigest("materialAccessGrant", detached)
}

// ReplayKey returns the canonical digest of the full edge identity with the
// stored edgeDigest itself detached; replayed submissions of the same grant
// therefore coalesce onto the same replay key.
func (grant MaterialAccessGrant) ReplayKey() (string, error) {
	return grant.Digest()
}

// ValidAt fails closed unless the grant is structurally valid, unrevoked and
// unexpired at now. An empty expiry never expires; the grant remains usable
// up to and including the expiry instant.
func (grant MaterialAccessGrant) ValidAt(now time.Time) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	return validateEdgeUse("materialAccessGrant", grant.RevocationGeneration, grant.Expiry, now)
}

// PublicationAuthorization is the typed cross-domain edge record by which the
// Core authorizes operations on a bound publication digest (ADR 0018 §7).
// The issuer is always the Core AuthorityNamespaceId; sourceActor and
// targetActor are provider SecurityDomainIds whose trustDomainKind pair must
// belong to the closed typed-edge matrix. Generation is the issuance
// generation of the edge; revocationGeneration stays zero until the edge is
// revoked.
//
// Record layer only: runtime issuance, revocation, current-ledger recheck
// wiring and consumer-side authorization decisions over this edge are left
// to M9.
type PublicationAuthorization struct {
	Issuer                 AuthorityNamespaceId `json:"issuer"`
	SourceActor            SecurityDomainId     `json:"sourceActor"`
	TargetActor            SecurityDomainId     `json:"targetActor"`
	Operation              PublicationOperation `json:"operation"`
	BoundPublicationDigest string               `json:"boundPublicationDigest"`
	Expiry                 string               `json:"expiry"`
	Generation             uint64               `json:"generation"`
	RevocationGeneration   uint64               `json:"revocationGeneration"`
	EdgeDigest             string               `json:"edgeDigest"`
}

// Validate fails closed on a forged or missing issuer, an invalid actor
// identity, a trustDomainKind pair outside the closed typed-edge matrix, an
// operation outside the closed enumeration, a malformed bound publication
// digest, a malformed expiry or a stored edgeDigest that does not match the
// recomputed detached canonical digest. A revoked authorization remains
// structurally valid; use-time rejection lives in ValidAt.
func (authorization PublicationAuthorization) Validate() error {
	if err := validateEdgeIdentity("publicationAuthorization", authorization.Issuer, authorization.SourceActor, authorization.TargetActor); err != nil {
		return err
	}
	if err := authorization.Operation.Validate(); err != nil {
		return err
	}
	if err := requireDigest("publicationAuthorization.boundPublicationDigest", authorization.BoundPublicationDigest); err != nil {
		return err
	}
	if err := validateExpiry("publicationAuthorization", authorization.Expiry); err != nil {
		return err
	}
	recomputed, err := authorization.Digest()
	if err != nil {
		return err
	}
	return requireMatchingEdgeDigest("publicationAuthorization", authorization.EdgeDigest, recomputed)
}

// Digest returns the sha256 digest of the record with its own edgeDigest
// field detached (zeroed before serialization), canonicalized through RFC
// 8785 JCS. The digest is deterministic for identical field values.
func (authorization PublicationAuthorization) Digest() (string, error) {
	detached := authorization
	detached.EdgeDigest = ""
	return detachedEdgeDigest("publicationAuthorization", detached)
}

// ReplayKey returns the canonical digest of the full edge identity with the
// stored edgeDigest itself detached; replayed submissions of the same
// authorization therefore coalesce onto the same replay key.
func (authorization PublicationAuthorization) ReplayKey() (string, error) {
	return authorization.Digest()
}

// ValidAt fails closed unless the authorization is structurally valid,
// unrevoked and unexpired at now. An empty expiry never expires; the
// authorization remains usable up to and including the expiry instant.
func (authorization PublicationAuthorization) ValidAt(now time.Time) error {
	if err := authorization.Validate(); err != nil {
		return err
	}
	return validateEdgeUse("publicationAuthorization", authorization.RevocationGeneration, authorization.Expiry, now)
}
