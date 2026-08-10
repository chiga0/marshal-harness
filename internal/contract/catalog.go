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
	{Name: "capability-snapshot", Kind: domain.KindCapabilitySnapshot},
	{Name: "intervention-record", Kind: domain.KindInterventionRecord},
	{Name: "outcome", Kind: domain.KindOutcome},
	{Name: "policy-snapshot", Kind: domain.KindPolicySnapshot},
	{Name: "publication-intent", Kind: domain.KindPublicationIntent},
	{Name: "publication-record", Kind: domain.KindPublicationRecord},
	{Name: "remote-check-record", Kind: domain.KindRemoteCheckRecord},
	{Name: "review-decision", Kind: domain.KindReviewDecision},
	{Name: "review-packet", Kind: domain.KindReviewPacket},
	{Name: "run-event", Kind: domain.KindRunEvent},
	{Name: "run-state", Kind: domain.KindRunState},
	{Name: "sandbox-requirements", Kind: domain.KindSandboxRequirements},
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
