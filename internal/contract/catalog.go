// Package contract compiles and validates Marshal's durable JSON contracts.
package contract

import (
	"fmt"
	"slices"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// Descriptor connects a stable CLI/schema name to a durable record kind and
// its embedded files.
type Descriptor struct {
	Name        string
	Kind        domain.Kind
	SchemaPath  string
	HappyPath   string
	InvalidPath string
}

var descriptors = []Descriptor{
	{Name: "approval-record", Kind: domain.KindApprovalRecord},
	{Name: "artifact-manifest", Kind: domain.KindArtifactManifest},
	{Name: "candidate-record", Kind: domain.KindCandidate},
	{Name: "capability-snapshot", Kind: domain.KindCapabilitySnapshot},
	{Name: "intervention-record", Kind: domain.KindInterventionRecord},
	{Name: "outcome", Kind: domain.KindOutcome},
	{Name: "policy-snapshot", Kind: domain.KindPolicySnapshot},
	{Name: "publication-intent", Kind: domain.KindPublicationIntent},
	{Name: "publication-reconcile-record", Kind: domain.KindPublicationReconcileRecord},
	{Name: "publication-record", Kind: domain.KindPublicationRecord},
	{Name: "remote-check-record", Kind: domain.KindRemoteCheckRecord},
	{Name: "review-decision", Kind: domain.KindReviewDecision},
	{Name: "review-packet", Kind: domain.KindReviewPacket},
	{Name: "run-event", Kind: domain.KindRunEvent},
	{Name: "run-state", Kind: domain.KindRunState},
	{Name: "sandbox-requirements", Kind: domain.KindSandboxRequirements},
	{Name: "scm-merge-intent", Kind: domain.KindSCMMergeIntent},
	{Name: "scm-merge-receipt", Kind: domain.KindSCMMergeReceipt},
	{Name: "task-spec", Kind: domain.KindTask},
	{Name: "verification-report", Kind: domain.KindVerificationReport},
	{Name: "worker-request", Kind: domain.KindWorkerRequest},
	{Name: "worker-result", Kind: domain.KindWorkerResult},
}

func init() {
	for index := range descriptors {
		descriptors[index].SchemaPath = descriptors[index].Name + ".schema.json"
		descriptors[index].HappyPath = "examples/happy-path/" + descriptors[index].Name + ".json"
		descriptors[index].InvalidPath = "examples/invalid/" + descriptors[index].Name + ".json"
	}
}

// CatalogException documents one embedded v1alpha1 schema that Issue #65
// asked to register in the durable catalog but that cannot become a
// Descriptor without changing its frozen schema document.
type CatalogException struct {
	Name   string
	Reason string
}

// catalogExceptions is the Issue #65 exception list. The issue named nine
// M8 gate-1/gate-2 schemas; scm-merge-receipt and publication-reconcile-
// record carry the durable apiVersion/kind envelope and are registered as
// Descriptors above. The seven entries below freeze internal authority and
// provider Go types (AuthorityNamespaceId, ProviderRegistration,
// ProviderCapabilitySnapshot, ConformanceEvidence, SideEffectIntent,
// SideEffectReceipt, ReconcileRecord) with additionalProperties:false and
// without the apiVersion/kind envelope. A durable Descriptor requires
// happy-path fixtures that pass both the schema and the envelope match
// enforced by Validator.Validate, and every enveloped document is rejected
// by these frozen schemas (TestIssue65ExceptionSchemasRejectDurableEnvelope
// proves the infeasibility), so the schemas cannot join the catalog while
// their documents stay frozen. They keep a schema-level fixture gate
// instead (TestIssue65ExceptionFixturesPassSchemaGate) with happy-path and
// invalid fixtures under schemas/examples. Promoting any exception to a
// Descriptor first requires adding the envelope to the schema itself.
var catalogExceptions = []CatalogException{
	{Name: "authority-namespace", Reason: "frozen schema of the internal authority.AuthorityNamespaceId key space declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "conformance-evidence", Reason: "frozen schema of the internal provider.ConformanceEvidence record declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "provider-capability-snapshot", Reason: "frozen schema of the internal provider.ProviderCapabilitySnapshot record declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "provider-registration", Reason: "frozen schema of the internal provider.ProviderRegistration record declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "reconcile-record", Reason: "frozen schema of the internal authority.ReconcileRecord record declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "side-effect-intent", Reason: "frozen schema of the internal authority.SideEffectIntent record declares no apiVersion/kind envelope and forbids additional properties"},
	{Name: "side-effect-receipt", Reason: "frozen schema of the internal authority.SideEffectReceipt record declares no apiVersion/kind envelope and forbids additional properties"},
}

// CatalogExceptions returns the documented non-catalog schemas in stable
// order.
func CatalogExceptions() []CatalogException {
	return slices.Clone(catalogExceptions)
}

// Descriptors returns the complete catalog in stable order.
func Descriptors() []Descriptor {
	return slices.Clone(descriptors)
}

// DescriptorByName resolves the stable kebab-case schema name.
func DescriptorByName(name string) (Descriptor, error) {
	for _, descriptor := range descriptors {
		if descriptor.Name == name {
			return descriptor, nil
		}
	}
	return Descriptor{}, fmt.Errorf("unknown schema %q", name)
}

// DescriptorByKind resolves a durable record kind.
func DescriptorByKind(kind domain.Kind) (Descriptor, error) {
	for _, descriptor := range descriptors {
		if descriptor.Kind == kind {
			return descriptor, nil
		}
	}
	return Descriptor{}, fmt.Errorf("no schema registered for kind %q", kind)
}
