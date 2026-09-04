package application

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const ProtocolRevision = "public-application-port/v1"
const PreparedRunStartProtocolRevision = "prepared-run-start/v2"
const FullLifecycleProtocolRevision = "public-application-lifecycle/v1"

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Availability string

const (
	AvailabilityReady            Availability = "ready"
	AvailabilityUnavailable      Availability = "unavailable"
	AvailabilityRecoveryRequired Availability = "recovery-required"
)

// PrepareRunStartRequest binds a start request to the exact durable Run head
// observed by the caller. The authority implementation must compare-and-read
// this tuple again; it is not a bearer capability.
type PrepareRunStartRequest struct {
	RunID                 string `json:"runId"`
	ExpectedSequence      uint64 `json:"expectedSequence"`
	ExpectedAuthorityHead string `json:"expectedAuthorityHead"`
}

// StartRunRequest binds one bounded Run-start operation to the exact durable
// head observed by the input adapter. The application implementation owns
// preparation, execution and response-loss reconciliation; callers never
// carry a PreparedRunStart across Runtime lifetimes.
type StartRunRequest struct {
	RunID                 string `json:"runId"`
	ExpectedSequence      uint64 `json:"expectedSequence"`
	ExpectedAuthorityHead string `json:"expectedAuthorityHead"`
}

func (request StartRunRequest) Validate() error {
	if !validID(request.RunID) || request.ExpectedSequence == 0 || request.ExpectedSequence > 1<<53-1 || !validDigest(request.ExpectedAuthorityHead) {
		return NewError("start-run", ReasonInvalidRequest)
	}
	return nil
}

func (request PrepareRunStartRequest) Validate() error {
	if !validID(request.RunID) || !validDigest(request.ExpectedAuthorityHead) {
		return NewError("prepare-run-start", ReasonInvalidRequest)
	}
	return nil
}

// PreparedRunStart is the secret-safe, replayable projection returned by the
// durable authority and consumed by the process bridge. PreparationDigest is
// derived by that authority from all preceding fields.
type PreparedRunStart struct {
	ProtocolRevision        string       `json:"protocolRevision"`
	TaskID                  string       `json:"taskId"`
	RunID                   string       `json:"runId"`
	AttemptID               string       `json:"attemptId"`
	ReservationFactDigest   string       `json:"reservationFactDigest"`
	AttemptOpenedFactDigest string       `json:"attemptOpenedFactDigest"`
	AttemptOrdinal          uint64       `json:"attemptOrdinal"`
	AttemptsUsedBefore      uint64       `json:"attemptsUsedBefore"`
	MaxAttempts             uint64       `json:"maxAttempts"`
	State                   domain.State `json:"state"`
	Sequence                uint64       `json:"sequence"`
	AuthorityHead           string       `json:"authorityHead"`
	PreparationDigest       string       `json:"preparationDigest"`
}

func (prepared PreparedRunStart) Validate() error {
	if prepared.ProtocolRevision != PreparedRunStartProtocolRevision || !validID(prepared.TaskID) || !validID(prepared.RunID) || !validID(prepared.AttemptID) || !validDigest(prepared.ReservationFactDigest) || !validDigest(prepared.AttemptOpenedFactDigest) || prepared.AttemptOrdinal != prepared.AttemptsUsedBefore+1 || prepared.MaxAttempts == 0 || prepared.AttemptOrdinal > prepared.MaxAttempts || prepared.State != domain.StateReady || prepared.Sequence == 0 || prepared.Sequence > 1<<53-1 || !validDigest(prepared.AuthorityHead) || !validDigest(prepared.PreparationDigest) {
		return NewError("start-prepared-run", ReasonInvalidRequest)
	}
	return nil
}

type InspectRunRequest struct {
	RunID string `json:"runId"`
}

// CurrentRunRequest is the common compare-and-read tuple for every T2
// lifecycle operation. AttemptID is mandatory because collect, verification,
// review and decision all consume evidence produced for one exact Attempt.
// The tuple is never a bearer capability: each implementation must re-read it
// from the current durable Run authority while holding the Run lease.
type CurrentRunRequest struct {
	RunID                 string `json:"runId"`
	AttemptID             string `json:"attemptId"`
	ExpectedSequence      uint64 `json:"expectedSequence"`
	ExpectedAuthorityHead string `json:"expectedAuthorityHead"`
}

func (request CurrentRunRequest) validate(operation string) error {
	if !validID(request.RunID) || !validID(request.AttemptID) || request.ExpectedSequence == 0 || request.ExpectedSequence > 1<<53-1 || !validDigest(request.ExpectedAuthorityHead) {
		return NewError(operation, ReasonInvalidRequest)
	}
	return nil
}

type CollectRunResultRequest CurrentRunRequest

func (request CollectRunResultRequest) Validate() error {
	return CurrentRunRequest(request).validate("collect-run-result")
}

type VerifyRunRequest CurrentRunRequest

func (request VerifyRunRequest) Validate() error {
	return CurrentRunRequest(request).validate("verify-run")
}

type BuildReviewPacketRequest CurrentRunRequest

func (request BuildReviewPacketRequest) Validate() error {
	return CurrentRunRequest(request).validate("build-review-packet")
}

// ApplyReviewDecisionRequest carries the external reviewer's exact JSON
// object, not a local pathname. The bounded HTTP decoder and the application
// contract validator both reject malformed or unknown content. DecisionDigest
// binds the caller's canonical bytes before the application independently
// validates and persists its canonical decision record.
type ApplyReviewDecisionRequest struct {
	RunID                 string          `json:"runId"`
	AttemptID             string          `json:"attemptId"`
	ExpectedSequence      uint64          `json:"expectedSequence"`
	ExpectedAuthorityHead string          `json:"expectedAuthorityHead"`
	Decision              json.RawMessage `json:"decision"`
	DecisionDigest        string          `json:"decisionDigest"`
}

func (request ApplyReviewDecisionRequest) Validate() error {
	current := CurrentRunRequest{RunID: request.RunID, AttemptID: request.AttemptID, ExpectedSequence: request.ExpectedSequence, ExpectedAuthorityHead: request.ExpectedAuthorityHead}
	if current.validate("apply-review-decision") != nil || len(request.Decision) == 0 || len(request.Decision) > 1<<20 || !json.Valid(request.Decision) || !validDigest(request.DecisionDigest) {
		return NewError("apply-review-decision", ReasonInvalidRequest)
	}
	return nil
}

func (request InspectRunRequest) Validate() error {
	if !validID(request.RunID) {
		return NewError("inspect-run", ReasonInvalidRequest)
	}
	return nil
}

// RunProjection is the public, path-free projection of the current durable
// Run. It deliberately omits worktree paths, process handles and credentials.
type RunProjection struct {
	TaskID        string       `json:"taskId"`
	RunID         string       `json:"runId"`
	AttemptID     string       `json:"attemptId,omitempty"`
	State         domain.State `json:"state"`
	Sequence      uint64       `json:"sequence"`
	AuthorityHead string       `json:"authorityHead"`
}

// RunStartProjection binds the exact durable preparation to its authoritative
// successor. It is returned both for a fresh start and for an exact replay
// after response loss.
type RunStartProjection struct {
	Prepared PreparedRunStart `json:"prepared"`
	Run      RunProjection    `json:"run"`
}

// CollectedRunProjection closes the Worker-result admission phase. The three
// evidence digests are durable ResultIngress facts, while Run is the exact
// post-terminalization current-ledger projection.
type CollectedRunProjection struct {
	ProtocolRevision    string        `json:"protocolRevision"`
	Run                 RunProjection `json:"run"`
	AdmissionFactDigest string        `json:"admissionFactDigest"`
	DRCDigest           string        `json:"drcDigest"`
	EnvelopeDigest      string        `json:"envelopeDigest"`
}

func (projection CollectedRunProjection) Validate() error {
	if projection.ProtocolRevision != FullLifecycleProtocolRevision || projection.Run.Validate() != nil || projection.Run.State != domain.StateVerifying || !validDigest(projection.AdmissionFactDigest) || !validDigest(projection.DRCDigest) || !validDigest(projection.EnvelopeDigest) {
		return NewError("collected-run-projection", ReasonAuthorityConflict)
	}
	return nil
}

type VerificationProjection struct {
	ProtocolRevision       string        `json:"protocolRevision"`
	Run                    RunProjection `json:"run"`
	Status                 string        `json:"status"`
	ReportDigest           string        `json:"reportDigest"`
	ArtifactManifestDigest string        `json:"artifactManifestDigest"`
}

func (projection VerificationProjection) Validate() error {
	if projection.ProtocolRevision != FullLifecycleProtocolRevision || projection.Run.Validate() != nil || projection.Run.State != domain.StateReviewPending || projection.Status != "pass" && projection.Status != "fail" || !validDigest(projection.ReportDigest) || !validDigest(projection.ArtifactManifestDigest) {
		return NewError("verification-projection", ReasonAuthorityConflict)
	}
	return nil
}

type ReviewPacketProjection struct {
	ProtocolRevision string              `json:"protocolRevision"`
	Run              RunProjection       `json:"run"`
	PacketDigest     string              `json:"packetDigest"`
	ReviewRound      uint                `json:"reviewRound"`
	Packet           domain.ReviewPacket `json:"packet"`
}

func (projection ReviewPacketProjection) Validate() error {
	if projection.ProtocolRevision != FullLifecycleProtocolRevision || projection.Run.Validate() != nil || projection.Run.State != domain.StateReviewPending || !validDigest(projection.PacketDigest) || projection.ReviewRound == 0 || projection.Packet.RunID != projection.Run.RunID || projection.Packet.ReviewRound != projection.ReviewRound {
		return NewError("review-packet-projection", ReasonAuthorityConflict)
	}
	return nil
}

type ReviewDecisionProjection struct {
	ProtocolRevision string        `json:"protocolRevision"`
	Run              RunProjection `json:"run"`
	Verdict          string        `json:"verdict"`
	DecisionDigest   string        `json:"decisionDigest"`
	EvidenceDigest   string        `json:"evidenceDigest"`
	OutcomeDigest    string        `json:"outcomeDigest,omitempty"`
}

func (projection ReviewDecisionProjection) Validate() error {
	if projection.ProtocolRevision != FullLifecycleProtocolRevision || projection.Run.Validate() != nil || strings.TrimSpace(projection.Verdict) == "" || !validDigest(projection.DecisionDigest) || !validDigest(projection.EvidenceDigest) {
		return NewError("review-decision-projection", ReasonAuthorityConflict)
	}
	if projection.Run.State.Terminal() != (projection.OutcomeDigest != "") || projection.OutcomeDigest != "" && !validDigest(projection.OutcomeDigest) {
		return NewError("review-decision-projection", ReasonAuthorityConflict)
	}
	return nil
}

func (projection RunStartProjection) Validate() error {
	if projection.Prepared.Validate() != nil || projection.Run.Validate() != nil ||
		projection.Run.TaskID != projection.Prepared.TaskID || projection.Run.RunID != projection.Prepared.RunID ||
		projection.Run.AttemptID != projection.Prepared.AttemptID || projection.Run.Sequence <= projection.Prepared.Sequence ||
		projection.Run.AuthorityHead == projection.Prepared.AuthorityHead {
		return NewError("run-start-projection", ReasonAuthorityConflict)
	}
	return nil
}

func (projection RunProjection) Validate() error {
	if !validID(projection.TaskID) || !validID(projection.RunID) || !validDigest(projection.AuthorityHead) || projection.AttemptID != "" && !validID(projection.AttemptID) {
		return NewError("run-projection", ReasonAuthorityConflict)
	}
	if _, err := domain.ParseState(string(projection.State)); err != nil {
		return NewError("run-projection", ReasonAuthorityConflict)
	}
	return nil
}

type StatusRequest struct{}

// StatusProjection contains only stable identities and digests. Executable,
// repository and control paths never cross the public port.
type StatusProjection struct {
	ProtocolRevision    string       `json:"protocolRevision"`
	Availability        Availability `json:"availability"`
	ReasonCode          ReasonCode   `json:"reasonCode,omitempty"`
	PlatformProfileID   string       `json:"platformProfileId"`
	AgentProvider       string       `json:"agentProvider"`
	AgentVersion        string       `json:"agentVersion"`
	AgentClosureProfile string       `json:"agentClosureProfile"`
	AgentIdentityDigest string       `json:"agentIdentityDigest"`
	OwnerEpoch          uint64       `json:"ownerEpoch,omitempty"`
	OwnerFactDigest     string       `json:"ownerFactDigest,omitempty"`
	PendingRecovery     uint64       `json:"pendingRecovery"`
}

func (projection StatusProjection) Validate() error {
	if projection.ProtocolRevision != ProtocolRevision || strings.TrimSpace(projection.PlatformProfileID) == "" || strings.TrimSpace(projection.AgentProvider) == "" || strings.TrimSpace(projection.AgentVersion) == "" || strings.TrimSpace(projection.AgentClosureProfile) == "" || !validDigest(projection.AgentIdentityDigest) {
		return NewError("status", ReasonAuthorityConflict)
	}
	switch projection.Availability {
	case AvailabilityReady:
		if projection.ReasonCode != "" || projection.OwnerEpoch == 0 || !validDigest(projection.OwnerFactDigest) || projection.PendingRecovery != 0 {
			return NewError("status", ReasonAuthorityConflict)
		}
	case AvailabilityUnavailable:
		if !validUnavailableReason(projection.ReasonCode) {
			return NewError("status", ReasonAuthorityConflict)
		}
	case AvailabilityRecoveryRequired:
		if projection.ReasonCode != ReasonRecoveryRequired || projection.PendingRecovery == 0 || projection.OwnerEpoch == 0 || !validDigest(projection.OwnerFactDigest) {
			return NewError("status", ReasonAuthorityConflict)
		}
	default:
		return NewError("status", ReasonAuthorityConflict)
	}
	return nil
}

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func validUnavailableReason(reason ReasonCode) bool {
	switch reason {
	case ReasonPlatformProfileUnavailable, ReasonOwnerUnavailable, ReasonOwnerNotCurrent, ReasonBridgeUnavailable, ReasonCompositionIncomplete, ReasonAuthorityConflict:
		return true
	default:
		return false
	}
}
