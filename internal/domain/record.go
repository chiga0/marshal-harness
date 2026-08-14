// Package domain contains provider-neutral Marshal domain types.
package domain

import (
	"encoding/json"
	"fmt"
	"slices"
)

// APIVersion identifies the durable contract version.
type APIVersion string

const (
	// APIVersionV1Alpha1 is the only contract version supported by the MVP.
	APIVersionV1Alpha1 APIVersion = "marshal.dev/v1alpha1"
)

// Kind identifies a durable Marshal record.
type Kind string

const (
	KindTask               Kind = "Task"
	KindCapabilitySnapshot Kind = "CapabilitySnapshot"
	KindPolicySnapshot     Kind = "PolicySnapshot"
	KindRunEvent           Kind = "RunEvent"
	KindRunState           Kind = "RunState"
	KindWorkerRequest      Kind = "WorkerRequest"
	KindWorkerResult       Kind = "WorkerResult"
	KindVerificationReport Kind = "VerificationReport"
	KindArtifactManifest   Kind = "ArtifactManifest"
	KindReviewPacket       Kind = "ReviewPacket"
	KindReviewDecision     Kind = "ReviewDecision"
	KindOutcome            Kind = "Outcome"
	KindPublicationIntent  Kind = "PublicationIntent"
	KindPublicationRecord  Kind = "PublicationRecord"
	KindRemoteCheckRecord  Kind = "RemoteCheckRecord"
	// KindSCMMergeReceipt and KindPublicationReconcileRecord are the ADR 0026
	// authority-ledger records that carry the immutable merge fact and the
	// append-only accept-after-merge reconciliation of a merged publication.
	KindSCMMergeReceipt            Kind = "SCMMergeReceipt"
	KindPublicationReconcileRecord Kind = "PublicationReconcileRecord"
	KindApprovalRecord             Kind = "ApprovalRecord"
	KindInterventionRecord         Kind = "InterventionRecord"
	KindSandboxRequirements        Kind = "SandboxRequirements"
	// KindCandidate is the ADR 0027 first-class immutable candidate record:
	// an append-only authority ledger fact owned by authorityNamespaceId.
	KindCandidate Kind = "Candidate"
)

// Issue #65 reserved kinds for the seven M8 gate-1/gate-2 schemas that
// freeze internal authority and provider Go types: AuthorityNamespaceId,
// ProviderRegistration, ProviderCapabilitySnapshot, ConformanceEvidence,
// SideEffectIntent, SideEffectReceipt and ReconcileRecord. Their frozen
// v1alpha1 schema documents set additionalProperties:false and declare no
// apiVersion/kind envelope, so no document can satisfy both the schema and
// the durable-record envelope match enforced by the contract Validator;
// contract.CatalogExceptions documents the resulting catalog exception.
// These constants are therefore deliberately not members of kinds: Kinds
// and ParseKind expose durable record kinds only. The other two Issue #65
// M8 ledger schemas, scm-merge-receipt and publication-reconcile-record,
// carry the durable envelope and are catalog kinds above. Promoting any
// reserved kind to a durable catalog kind first requires adding the
// apiVersion/kind envelope to its schema document.
const (
	KindAuthorityNamespace         Kind = "AuthorityNamespace"
	KindConformanceEvidence        Kind = "ConformanceEvidence"
	KindProviderCapabilitySnapshot Kind = "ProviderCapabilitySnapshot"
	KindProviderRegistration       Kind = "ProviderRegistration"
	KindReconcileRecord            Kind = "ReconcileRecord"
	KindSideEffectIntent           Kind = "SideEffectIntent"
	KindSideEffectReceipt          Kind = "SideEffectReceipt"
)

var kinds = []Kind{
	KindTask,
	KindCapabilitySnapshot,
	KindPolicySnapshot,
	KindRunEvent,
	KindRunState,
	KindWorkerRequest,
	KindWorkerResult,
	KindVerificationReport,
	KindArtifactManifest,
	KindReviewPacket,
	KindReviewDecision,
	KindOutcome,
	KindPublicationIntent,
	KindPublicationRecord,
	KindRemoteCheckRecord,
	KindSCMMergeReceipt,
	KindPublicationReconcileRecord,
	KindApprovalRecord,
	KindInterventionRecord,
	KindSandboxRequirements,
	KindCandidate,
}

// Kinds returns all durable record kinds in stable order.
func Kinds() []Kind {
	return slices.Clone(kinds)
}

// ParseKind rejects unknown record kinds.
func ParseKind(value string) (Kind, error) {
	kind := Kind(value)
	if slices.Contains(kinds, kind) {
		return kind, nil
	}
	return "", fmt.Errorf("unknown record kind %q", value)
}

// Record is an opaque, schema-validated durable record passed through Core
// ports. Provider adapters do not own its structure.
type Record struct {
	Kind Kind
	Data json.RawMessage
}
