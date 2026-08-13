package authority

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type typedEdgePairCase struct {
	name   string
	source TrustDomainKind
	target TrustDomainKind
}

func legalTypedEdgePairs() []typedEdgePairCase {
	return []typedEdgePairCase{
		{name: "execution to data-capability", source: TrustDomainKindExecution, target: TrustDomainKindDataCapability},
		{name: "publication to data-capability", source: TrustDomainKindPublication, target: TrustDomainKindDataCapability},
		{name: "execution to publication", source: TrustDomainKindExecution, target: TrustDomainKindPublication},
	}
}

func illegalTypedEdgePairs() []typedEdgePairCase {
	return []typedEdgePairCase{
		{name: "execution to execution", source: TrustDomainKindExecution, target: TrustDomainKindExecution},
		{name: "publication to publication", source: TrustDomainKindPublication, target: TrustDomainKindPublication},
		{name: "data-capability to data-capability", source: TrustDomainKindDataCapability, target: TrustDomainKindDataCapability},
		{name: "data-capability to execution", source: TrustDomainKindDataCapability, target: TrustDomainKindExecution},
		{name: "data-capability to publication", source: TrustDomainKindDataCapability, target: TrustDomainKindPublication},
		{name: "publication to execution", source: TrustDomainKindPublication, target: TrustDomainKindExecution},
	}
}

func securityDomainForKind(kind TrustDomainKind, isolationDomainId string) SecurityDomainId {
	return SecurityDomainId{
		TenantNamespace:   "default",
		TrustDomainKind:   kind,
		IsolationDomainId: isolationDomainId,
	}
}

func dispatchResultCapabilityForPair(source SecurityDomainId, target SecurityDomainId, operation DispatchResultOperation) DispatchResultCapability {
	return DispatchResultCapability{
		Issuer:            validNamespace(),
		SourceActor:       source,
		TargetActor:       target,
		Operation:         operation,
		BoundAttemptId:    "attempt:1",
		BoundAllocationId: "allocation:1",
		Expiry:            "2026-12-31T00:00:00Z",
		Generation:        1,
	}
}

func materialAccessGrantForPair(source SecurityDomainId, target SecurityDomainId, operation MaterialAccessOperation) MaterialAccessGrant {
	return MaterialAccessGrant{
		Issuer:           validNamespace(),
		SourceActor:      source,
		TargetActor:      target,
		Operation:        operation,
		MaterialId:       "material:1",
		ScopeRestriction: "sandbox-stage",
		Expiry:           "2026-12-31T00:00:00Z",
		Generation:       1,
	}
}

func publicationAuthorizationForPair(source SecurityDomainId, target SecurityDomainId, operation PublicationOperation) PublicationAuthorization {
	return PublicationAuthorization{
		Issuer:                 validNamespace(),
		SourceActor:            source,
		TargetActor:            target,
		Operation:              operation,
		BoundPublicationDigest: digestBytes([]byte("publication")),
		Expiry:                 "2026-12-31T00:00:00Z",
		Generation:             1,
	}
}

func sealDispatchResultCapability(t *testing.T, edge DispatchResultCapability) DispatchResultCapability {
	t.Helper()
	digest, err := edge.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	edge.EdgeDigest = digest
	return edge
}

func sealMaterialAccessGrant(t *testing.T, grant MaterialAccessGrant) MaterialAccessGrant {
	t.Helper()
	digest, err := grant.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	grant.EdgeDigest = digest
	return grant
}

func sealPublicationAuthorization(t *testing.T, authorization PublicationAuthorization) PublicationAuthorization {
	t.Helper()
	digest, err := authorization.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	authorization.EdgeDigest = digest
	return authorization
}

func assertSentinel(t *testing.T, err error, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error wrapping the sentinel %q, got nil", sentinel)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected an error wrapping the sentinel %q, got %q", sentinel, err)
	}
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("error %q does not expose the fixed sentinel text %q", err, sentinel)
	}
}

func assertFixedError(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the fixed error %q, got nil", message)
	}
	if err.Error() != message {
		t.Fatalf("expected the fixed error %q, got %q", message, err)
	}
}

func assertReplayKeyIdempotent(t *testing.T, replayKey func() (string, error)) {
	t.Helper()
	first, err := replayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed: %v", err)
	}
	second, err := replayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed on repeat: %v", err)
	}
	if first != second {
		t.Fatalf("ReplayKey is not idempotent: %s != %s", first, second)
	}
	if !strings.HasPrefix(first, DigestPrefix) || len(first) != len(DigestPrefix)+64 {
		t.Fatalf("ReplayKey %q is not a sha256 hex digest", first)
	}
}

func assertDigestDeterministic(t *testing.T, name string, first func() (string, error), second func() (string, error)) {
	t.Helper()
	firstDigest, err := first()
	if err != nil {
		t.Fatalf("%s Digest failed: %v", name, err)
	}
	secondDigest, err := second()
	if err != nil {
		t.Fatalf("%s Digest failed on repeat: %v", name, err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("%s digests differ for identical input: %s != %s", name, firstDigest, secondDigest)
	}
	if !strings.HasPrefix(firstDigest, DigestPrefix) || len(firstDigest) != len(DigestPrefix)+64 {
		t.Fatalf("%s digest %q is not a sha256 hex digest", name, firstDigest)
	}
}

func TestTypedEdgesAcceptLegalPairsIdempotently(t *testing.T) {
	before := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	pairs := legalTypedEdgePairs()
	dispatchOperations := []DispatchResultOperation{
		DispatchResultOperationRead,
		DispatchResultOperationAccept,
		DispatchResultOperationRead,
	}
	materialOperations := []MaterialAccessOperation{
		MaterialAccessOperationRead,
		MaterialAccessOperationWrite,
		MaterialAccessOperationRead,
	}
	publicationOperations := []PublicationOperation{
		PublicationOperationSubmit,
		PublicationOperationChecksRead,
		PublicationOperationSubmit,
	}

	for index, tc := range pairs {
		source := securityDomainForKind(tc.source, "isolation-source")
		target := securityDomainForKind(tc.target, "isolation-target")

		t.Run("dispatchResultCapability "+tc.name, func(t *testing.T) {
			edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, dispatchOperations[index]))
			for repeat := 1; repeat <= 2; repeat++ {
				if err := edge.Validate(); err != nil {
					t.Fatalf("Validate rejected a legal edge on repeat %d: %v", repeat, err)
				}
				if err := edge.ValidAt(before); err != nil {
					t.Fatalf("ValidAt rejected a legal edge on repeat %d: %v", repeat, err)
				}
			}
			assertReplayKeyIdempotent(t, edge.ReplayKey)
		})

		t.Run("materialAccessGrant "+tc.name, func(t *testing.T) {
			grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, materialOperations[index]))
			for repeat := 1; repeat <= 2; repeat++ {
				if err := grant.Validate(); err != nil {
					t.Fatalf("Validate rejected a legal edge on repeat %d: %v", repeat, err)
				}
				if err := grant.ValidAt(before); err != nil {
					t.Fatalf("ValidAt rejected a legal edge on repeat %d: %v", repeat, err)
				}
			}
			assertReplayKeyIdempotent(t, grant.ReplayKey)
		})

		t.Run("publicationAuthorization "+tc.name, func(t *testing.T) {
			authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, publicationOperations[index]))
			for repeat := 1; repeat <= 2; repeat++ {
				if err := authorization.Validate(); err != nil {
					t.Fatalf("Validate rejected a legal edge on repeat %d: %v", repeat, err)
				}
				if err := authorization.ValidAt(before); err != nil {
					t.Fatalf("ValidAt rejected a legal edge on repeat %d: %v", repeat, err)
				}
			}
			assertReplayKeyIdempotent(t, authorization.ReplayKey)
		})
	}
}

func TestTypedEdgeDigestAndReplayKeyAreDeterministic(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	dispatchFirst := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
	dispatchSecond := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
	assertDigestDeterministic(t, "dispatchResultCapability", dispatchFirst.Digest, dispatchSecond.Digest)

	materialFirst := materialAccessGrantForPair(source, target, MaterialAccessOperationRead)
	materialSecond := materialAccessGrantForPair(source, target, MaterialAccessOperationRead)
	assertDigestDeterministic(t, "materialAccessGrant", materialFirst.Digest, materialSecond.Digest)

	publicationFirst := publicationAuthorizationForPair(source, target, PublicationOperationSubmit)
	publicationSecond := publicationAuthorizationForPair(source, target, PublicationOperationSubmit)
	assertDigestDeterministic(t, "publicationAuthorization", publicationFirst.Digest, publicationSecond.Digest)

	sealedFirst := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
	sealedSecond := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
	assertDigestDeterministic(t, "sealed dispatchResultCapability", sealedFirst.Digest, sealedSecond.Digest)
	assertReplayKeyIdempotent(t, sealedFirst.ReplayKey)
	firstReplayKey, err := sealedFirst.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed: %v", err)
	}
	secondReplayKey, err := sealedSecond.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed: %v", err)
	}
	if firstReplayKey != secondReplayKey {
		t.Fatalf("replay keys differ for identical input: %s != %s", firstReplayKey, secondReplayKey)
	}

	mutated := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
	mutated.BoundAttemptId = "attempt:2"
	referenceDigest, err := dispatchFirst.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	mutatedDigest, err := mutated.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	if referenceDigest == mutatedDigest {
		t.Fatal("different edge identities produced the same digest")
	}
}

func TestTypedEdgeDigestIgnoresJSONFieldOrder(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")
	reference := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
	referenceDigest, err := reference.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	referenceReplayKey, err := reference.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed: %v", err)
	}

	document := map[string]any{
		"issuer":               reference.Issuer,
		"sourceActor":          reference.SourceActor,
		"targetActor":          reference.TargetActor,
		"operation":            reference.Operation,
		"boundAttemptId":       reference.BoundAttemptId,
		"boundAllocationId":    reference.BoundAllocationId,
		"expiry":               reference.Expiry,
		"generation":           reference.Generation,
		"revocationGeneration": reference.RevocationGeneration,
		"edgeDigest":           reference.EdgeDigest,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal shuffled document: %v", err)
	}
	var decoded DispatchResultCapability
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal shuffled document: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate rejected the shuffled edge: %v", err)
	}
	shuffledDigest, err := decoded.Digest()
	if err != nil {
		t.Fatalf("Digest failed for shuffled input: %v", err)
	}
	if shuffledDigest != referenceDigest {
		t.Fatalf("edge digest changed with JSON field order: %s != %s", shuffledDigest, referenceDigest)
	}
	shuffledReplayKey, err := decoded.ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed for shuffled input: %v", err)
	}
	if shuffledReplayKey != referenceReplayKey {
		t.Fatalf("replay key changed with JSON field order: %s != %s", shuffledReplayKey, referenceReplayKey)
	}
}

func TestTypedEdgesRejectIllegalTrustDomainPairs(t *testing.T) {
	for _, tc := range illegalTypedEdgePairs() {
		source := securityDomainForKind(tc.source, "isolation-source")
		target := securityDomainForKind(tc.target, "isolation-target")

		t.Run("dispatchResultCapability "+tc.name, func(t *testing.T) {
			edge := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
			assertSentinel(t, edge.Validate(), ErrEdgePair)
		})

		t.Run("materialAccessGrant "+tc.name, func(t *testing.T) {
			grant := materialAccessGrantForPair(source, target, MaterialAccessOperationRead)
			assertSentinel(t, grant.Validate(), ErrEdgePair)
		})

		t.Run("publicationAuthorization "+tc.name, func(t *testing.T) {
			authorization := publicationAuthorizationForPair(source, target, PublicationOperationSubmit)
			assertSentinel(t, authorization.Validate(), ErrEdgePair)
		})
	}
}

func TestTypedEdgesRejectForgedIssuer(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")
	const fixedMessage = "authority: authorityNamespaceId.tenantNamespace must be a non-empty string"

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
		edge.Issuer = AuthorityNamespaceId{}
		assertFixedError(t, edge.Validate(), fixedMessage)
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
		grant.Issuer = AuthorityNamespaceId{}
		assertFixedError(t, grant.Validate(), fixedMessage)
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
		authorization.Issuer = AuthorityNamespaceId{}
		assertFixedError(t, authorization.Validate(), fixedMessage)
	})
}

func assertImpersonatedIssuerRejected(t *testing.T, document map[string]any, validate func([]byte) error) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal impersonated edge document: %v", err)
	}
	assertFixedError(t, validate(raw), "authority: authorityNamespaceId.controlPlaneId must be a non-empty string")
}

func TestTypedEdgesRejectSecurityDomainImpersonationOfIssuer(t *testing.T) {
	impersonatedIssuer, err := json.Marshal(validSecurityDomain())
	if err != nil {
		t.Fatalf("marshal impersonated issuer: %v", err)
	}

	t.Run("dispatchResultCapability", func(t *testing.T) {
		document := map[string]any{
			"issuer":               json.RawMessage(impersonatedIssuer),
			"sourceActor":          securityDomainForKind(TrustDomainKindExecution, "isolation-source"),
			"targetActor":          securityDomainForKind(TrustDomainKindDataCapability, "isolation-target"),
			"operation":            string(DispatchResultOperationRead),
			"boundAttemptId":       "attempt:1",
			"boundAllocationId":    "allocation:1",
			"expiry":               "2026-12-31T00:00:00Z",
			"generation":           1,
			"revocationGeneration": 0,
			"edgeDigest":           digestBytes([]byte("forged-edge")),
		}
		assertImpersonatedIssuerRejected(t, document, func(raw []byte) error {
			var edge DispatchResultCapability
			if err := json.Unmarshal(raw, &edge); err != nil {
				return err
			}
			return edge.Validate()
		})
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		document := map[string]any{
			"issuer":               json.RawMessage(impersonatedIssuer),
			"sourceActor":          securityDomainForKind(TrustDomainKindExecution, "isolation-source"),
			"targetActor":          securityDomainForKind(TrustDomainKindDataCapability, "isolation-target"),
			"operation":            string(MaterialAccessOperationRead),
			"materialId":           "material:1",
			"scopeRestriction":     "sandbox-stage",
			"expiry":               "2026-12-31T00:00:00Z",
			"generation":           1,
			"revocationGeneration": 0,
			"edgeDigest":           digestBytes([]byte("forged-edge")),
		}
		assertImpersonatedIssuerRejected(t, document, func(raw []byte) error {
			var grant MaterialAccessGrant
			if err := json.Unmarshal(raw, &grant); err != nil {
				return err
			}
			return grant.Validate()
		})
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		document := map[string]any{
			"issuer":                 json.RawMessage(impersonatedIssuer),
			"sourceActor":            securityDomainForKind(TrustDomainKindExecution, "isolation-source"),
			"targetActor":            securityDomainForKind(TrustDomainKindPublication, "isolation-target"),
			"operation":              string(PublicationOperationSubmit),
			"boundPublicationDigest": digestBytes([]byte("publication")),
			"expiry":                 "2026-12-31T00:00:00Z",
			"generation":             1,
			"revocationGeneration":   0,
			"edgeDigest":             digestBytes([]byte("forged-edge")),
		}
		assertImpersonatedIssuerRejected(t, document, func(raw []byte) error {
			var authorization PublicationAuthorization
			if err := json.Unmarshal(raw, &authorization); err != nil {
				return err
			}
			return authorization.Validate()
		})
	})
}

func TestTypedEdgesRejectWrongAuthorityScope(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")
	const fixedMessage = "authority: authorityNamespaceId.authorityScopeId must be a non-empty string"
	scopeCases := []struct {
		name  string
		scope string
	}{
		{name: "empty", scope: ""},
		{name: "whitespace", scope: "   "},
		{name: "tab", scope: "\t"},
	}

	for _, scopeCase := range scopeCases {
		t.Run("dispatchResultCapability "+scopeCase.name, func(t *testing.T) {
			edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
			edge.Issuer.AuthorityScopeId = scopeCase.scope
			assertFixedError(t, edge.Validate(), fixedMessage)
		})

		t.Run("materialAccessGrant "+scopeCase.name, func(t *testing.T) {
			grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
			grant.Issuer.AuthorityScopeId = scopeCase.scope
			assertFixedError(t, grant.Validate(), fixedMessage)
		})

		t.Run("publicationAuthorization "+scopeCase.name, func(t *testing.T) {
			authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
			authorization.Issuer.AuthorityScopeId = scopeCase.scope
			assertFixedError(t, authorization.Validate(), fixedMessage)
		})
	}
}

func TestTypedEdgesRejectTargetSubstitution(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")
	substitute := securityDomainForKind(TrustDomainKindDataCapability, "isolation-other")

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
		edge.TargetActor = substitute
		assertSentinel(t, edge.Validate(), ErrEdgeDigest)
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
		grant.TargetActor = substitute
		assertSentinel(t, grant.Validate(), ErrEdgeDigest)
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
		authorization.TargetActor = substitute
		assertSentinel(t, authorization.Validate(), ErrEdgeDigest)
	})
}

func TestTypedEdgesRejectUnknownOperations(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	t.Run("dispatchResultCapability", func(t *testing.T) {
		for _, value := range []string{"", "dispatch-result-write", "DISPATCH-RESULT-READ", "dispatch-result-read ", "material-read"} {
			edge := dispatchResultCapabilityForPair(source, target, DispatchResultOperation(value))
			assertSentinel(t, edge.Validate(), ErrEdgeOperation)
			assertSentinel(t, DispatchResultOperation(value).Validate(), ErrEdgeOperation)
		}
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		for _, value := range []string{"", "material-exec", "MATERIAL-READ", "material-read ", "dispatch-result-read"} {
			grant := materialAccessGrantForPair(source, target, MaterialAccessOperation(value))
			assertSentinel(t, grant.Validate(), ErrEdgeOperation)
			assertSentinel(t, MaterialAccessOperation(value).Validate(), ErrEdgeOperation)
		}
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		for _, value := range []string{"", "publication-approve", "PUBLICATION-SUBMIT", "publication-submit ", "material-read"} {
			authorization := publicationAuthorizationForPair(source, target, PublicationOperation(value))
			assertSentinel(t, authorization.Validate(), ErrEdgeOperation)
			assertSentinel(t, PublicationOperation(value).Validate(), ErrEdgeOperation)
		}
	})
}

func TestTypedEdgesRejectMissingIdentityFields(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	dispatchCases := []struct {
		name    string
		change  func(*DispatchResultCapability)
		message string
	}{
		{
			name:    "empty issuer tenantNamespace",
			change:  func(edge *DispatchResultCapability) { edge.Issuer.TenantNamespace = "" },
			message: "authority: authorityNamespaceId.tenantNamespace must be a non-empty string",
		},
		{
			name:    "empty source tenantNamespace",
			change:  func(edge *DispatchResultCapability) { edge.SourceActor.TenantNamespace = "" },
			message: "authority: securityDomainId.tenantNamespace must be a non-empty string",
		},
		{
			name:    "empty source isolationDomainId",
			change:  func(edge *DispatchResultCapability) { edge.SourceActor.IsolationDomainId = "" },
			message: "authority: securityDomainId.isolationDomainId must be a non-empty string",
		},
		{
			name:    "empty target isolationDomainId",
			change:  func(edge *DispatchResultCapability) { edge.TargetActor.IsolationDomainId = "" },
			message: "authority: securityDomainId.isolationDomainId must be a non-empty string",
		},
		{
			name:    "empty boundAttemptId",
			change:  func(edge *DispatchResultCapability) { edge.BoundAttemptId = "" },
			message: "authority: dispatchResultCapability.boundAttemptId must be a non-empty string",
		},
		{
			name:    "empty boundAllocationId",
			change:  func(edge *DispatchResultCapability) { edge.BoundAllocationId = "" },
			message: "authority: dispatchResultCapability.boundAllocationId must be a non-empty string",
		},
	}
	for _, tc := range dispatchCases {
		t.Run("dispatchResultCapability "+tc.name, func(t *testing.T) {
			edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
			tc.change(&edge)
			assertFixedError(t, edge.Validate(), tc.message)
		})
	}

	materialCases := []struct {
		name    string
		change  func(*MaterialAccessGrant)
		message string
	}{
		{
			name:    "empty source tenantNamespace",
			change:  func(grant *MaterialAccessGrant) { grant.SourceActor.TenantNamespace = "" },
			message: "authority: securityDomainId.tenantNamespace must be a non-empty string",
		},
		{
			name:    "empty target isolationDomainId",
			change:  func(grant *MaterialAccessGrant) { grant.TargetActor.IsolationDomainId = "" },
			message: "authority: securityDomainId.isolationDomainId must be a non-empty string",
		},
		{
			name:    "empty materialId",
			change:  func(grant *MaterialAccessGrant) { grant.MaterialId = "" },
			message: "authority: materialAccessGrant.materialId must be a non-empty string",
		},
		{
			name:    "empty scopeRestriction",
			change:  func(grant *MaterialAccessGrant) { grant.ScopeRestriction = "" },
			message: "authority: materialAccessGrant.scopeRestriction must be a non-empty string",
		},
	}
	for _, tc := range materialCases {
		t.Run("materialAccessGrant "+tc.name, func(t *testing.T) {
			grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
			tc.change(&grant)
			assertFixedError(t, grant.Validate(), tc.message)
		})
	}

	publicationCases := []struct {
		name    string
		change  func(*PublicationAuthorization)
		message string
	}{
		{
			name:    "empty target tenantNamespace",
			change:  func(authorization *PublicationAuthorization) { authorization.TargetActor.TenantNamespace = "" },
			message: "authority: securityDomainId.tenantNamespace must be a non-empty string",
		},
		{
			name:    "empty boundPublicationDigest",
			change:  func(authorization *PublicationAuthorization) { authorization.BoundPublicationDigest = "" },
			message: "authority: publicationAuthorization.boundPublicationDigest must be a non-empty sha256: digest",
		},
		{
			name: "prefix-stripped boundPublicationDigest",
			change: func(authorization *PublicationAuthorization) {
				authorization.BoundPublicationDigest = strings.TrimPrefix(digestBytes([]byte("stripped")), DigestPrefix)
			},
			message: "authority: publicationAuthorization.boundPublicationDigest must be a non-empty sha256: digest",
		},
	}
	for _, tc := range publicationCases {
		t.Run("publicationAuthorization "+tc.name, func(t *testing.T) {
			authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
			tc.change(&authorization)
			assertFixedError(t, authorization.Validate(), tc.message)
		})
	}
}

func TestTypedEdgeExpiryBounds(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")
	expiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
		if err := edge.ValidAt(expiry); err != nil {
			t.Fatalf("ValidAt rejected the edge at the exact expiry instant: %v", err)
		}
		assertSentinel(t, edge.ValidAt(expiry.Add(time.Nanosecond)), ErrEdgeExpired)

		noExpiry := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
		noExpiry.Expiry = ""
		noExpiry = sealDispatchResultCapability(t, noExpiry)
		if err := noExpiry.ValidAt(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("ValidAt rejected an edge without expiry: %v", err)
		}
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
		if err := grant.ValidAt(expiry); err != nil {
			t.Fatalf("ValidAt rejected the grant at the exact expiry instant: %v", err)
		}
		assertSentinel(t, grant.ValidAt(expiry.Add(time.Nanosecond)), ErrEdgeExpired)
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
		if err := authorization.ValidAt(expiry); err != nil {
			t.Fatalf("ValidAt rejected the authorization at the exact expiry instant: %v", err)
		}
		assertSentinel(t, authorization.ValidAt(expiry.Add(time.Nanosecond)), ErrEdgeExpired)
	})
}

func TestTypedEdgesRejectMalformedExpiry(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
		edge.Expiry = "2026-12-31"
		assertFixedError(t, edge.Validate(), "authority: dispatchResultCapability: expiry must be an RFC 3339 timestamp or empty")
		edge.Expiry = "0001-01-01T00:00:00Z"
		assertFixedError(t, edge.Validate(), "authority: dispatchResultCapability: expiry must not be the zero time")
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
		grant.Expiry = "2026-12-31"
		assertFixedError(t, grant.Validate(), "authority: materialAccessGrant: expiry must be an RFC 3339 timestamp or empty")
		grant.Expiry = "0001-01-01T00:00:00Z"
		assertFixedError(t, grant.Validate(), "authority: materialAccessGrant: expiry must not be the zero time")
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
		authorization.Expiry = "2026-12-31"
		assertFixedError(t, authorization.Validate(), "authority: publicationAuthorization: expiry must be an RFC 3339 timestamp or empty")
		authorization.Expiry = "0001-01-01T00:00:00Z"
		assertFixedError(t, authorization.Validate(), "authority: publicationAuthorization: expiry must not be the zero time")
	})
}

func TestTypedEdgesRejectRevokedUse(t *testing.T) {
	before := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead)
		edge.RevocationGeneration = 3
		edge = sealDispatchResultCapability(t, edge)
		if err := edge.Validate(); err != nil {
			t.Fatalf("Validate rejected a structurally valid revoked edge: %v", err)
		}
		assertSentinel(t, edge.ValidAt(before), ErrEdgeRevoked)
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := materialAccessGrantForPair(source, target, MaterialAccessOperationRead)
		grant.RevocationGeneration = 3
		grant = sealMaterialAccessGrant(t, grant)
		if err := grant.Validate(); err != nil {
			t.Fatalf("Validate rejected a structurally valid revoked grant: %v", err)
		}
		assertSentinel(t, grant.ValidAt(before), ErrEdgeRevoked)
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := publicationAuthorizationForPair(source, target, PublicationOperationSubmit)
		authorization.RevocationGeneration = 3
		authorization = sealPublicationAuthorization(t, authorization)
		if err := authorization.Validate(); err != nil {
			t.Fatalf("Validate rejected a structurally valid revoked authorization: %v", err)
		}
		assertSentinel(t, authorization.ValidAt(before), ErrEdgeRevoked)
	})
}

func TestTypedEdgesRejectTamperedEdgeDigest(t *testing.T) {
	source := securityDomainForKind(TrustDomainKindExecution, "isolation-source")
	target := securityDomainForKind(TrustDomainKindDataCapability, "isolation-target")

	t.Run("dispatchResultCapability", func(t *testing.T) {
		edge := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(source, target, DispatchResultOperationRead))
		edge.EdgeDigest = digestBytes([]byte("tampered-edge"))
		assertSentinel(t, edge.Validate(), ErrEdgeDigest)

		edge.EdgeDigest = ""
		assertFixedError(t, edge.Validate(), "authority: dispatchResultCapability.edgeDigest must be a non-empty sha256: digest")

		edge.EdgeDigest = strings.TrimPrefix(digestBytes([]byte("stripped")), DigestPrefix)
		assertFixedError(t, edge.Validate(), "authority: dispatchResultCapability.edgeDigest must be a non-empty sha256: digest")
	})

	t.Run("materialAccessGrant", func(t *testing.T) {
		grant := sealMaterialAccessGrant(t, materialAccessGrantForPair(source, target, MaterialAccessOperationRead))
		grant.EdgeDigest = digestBytes([]byte("tampered-edge"))
		assertSentinel(t, grant.Validate(), ErrEdgeDigest)

		grant.EdgeDigest = ""
		assertFixedError(t, grant.Validate(), "authority: materialAccessGrant.edgeDigest must be a non-empty sha256: digest")

		grant.EdgeDigest = strings.TrimPrefix(digestBytes([]byte("stripped")), DigestPrefix)
		assertFixedError(t, grant.Validate(), "authority: materialAccessGrant.edgeDigest must be a non-empty sha256: digest")
	})

	t.Run("publicationAuthorization", func(t *testing.T) {
		authorization := sealPublicationAuthorization(t, publicationAuthorizationForPair(source, target, PublicationOperationSubmit))
		authorization.EdgeDigest = digestBytes([]byte("tampered-edge"))
		assertSentinel(t, authorization.Validate(), ErrEdgeDigest)

		authorization.EdgeDigest = ""
		assertFixedError(t, authorization.Validate(), "authority: publicationAuthorization.edgeDigest must be a non-empty sha256: digest")

		authorization.EdgeDigest = strings.TrimPrefix(digestBytes([]byte("stripped")), DigestPrefix)
		assertFixedError(t, authorization.Validate(), "authority: publicationAuthorization.edgeDigest must be a non-empty sha256: digest")
	})
}

func TestTypedEdgeCanonicalRejectsDuplicateMembers(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "dispatchResultCapability", raw: `{"issuer":{"tenantNamespace":"default"},"issuer":{"tenantNamespace":"other"}}`},
		{name: "materialAccessGrant", raw: `{"materialId":"material:1","materialId":"material:2"}`},
		{name: "publicationAuthorization", raw: `{"operation":"publication-submit","operation":"publication-checks-read"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := canonicalEdgeBytes([]byte(tc.raw))
			if err == nil {
				t.Fatalf("canonicalEdgeBytes accepted duplicate object members in %s wire JSON", tc.name)
			}
			if !errors.Is(err, canonical.ErrRejected) {
				t.Fatalf("expected canonical.ErrRejected, got %q", err)
			}
			if !strings.Contains(err.Error(), canonical.ErrRejected.Error()) {
				t.Fatalf("error %q does not expose the fixed sentinel text %q", err, canonical.ErrRejected)
			}
		})
	}
}
