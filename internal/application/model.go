package application

import (
	"regexp"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const ProtocolRevision = "public-application-port/v1"

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
	ProtocolRevision  string       `json:"protocolRevision"`
	TaskID            string       `json:"taskId"`
	RunID             string       `json:"runId"`
	AttemptID         string       `json:"attemptId"`
	State             domain.State `json:"state"`
	Sequence          uint64       `json:"sequence"`
	AuthorityHead     string       `json:"authorityHead"`
	PreparationDigest string       `json:"preparationDigest"`
}

func (prepared PreparedRunStart) Validate() error {
	if prepared.ProtocolRevision != ProtocolRevision || !validID(prepared.TaskID) || !validID(prepared.RunID) || !validID(prepared.AttemptID) || prepared.State != domain.StateReady || !validDigest(prepared.AuthorityHead) || !validDigest(prepared.PreparationDigest) {
		return NewError("start-prepared-run", ReasonInvalidRequest)
	}
	return nil
}

type InspectRunRequest struct {
	RunID string `json:"runId"`
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
