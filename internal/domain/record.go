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
