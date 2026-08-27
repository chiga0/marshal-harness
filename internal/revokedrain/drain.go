package revokedrain

import (
	"errors"
	"math"
	"sync"
	"time"
)

type DrainTrigger string

const (
	DrainTriggerRevoke  DrainTrigger = "revoke"
	DrainTriggerUpgrade DrainTrigger = "upgrade"
)

type DrainReason string

const (
	DrainReasonCredentialCompromise DrainReason = "credential-compromise"
	DrainReasonProtocolViolation    DrainReason = "protocol-violation"
	DrainReasonEvidenceTampering    DrainReason = "evidence-tampering"
	DrainReasonUnauthorizedAccess   DrainReason = "unauthorized-access"
	DrainReasonIncompatibleUpgrade  DrainReason = "incompatible-upgrade"
)

type DrainMode string

const (
	DrainModeRejected  DrainMode = "rejected"
	DrainModeImmediate DrainMode = "immediate"
	DrainModeDraining  DrainMode = "draining"
	DrainModeExpired   DrainMode = "expired"
)

type DrainAction string

const (
	DrainActionStopNew        DrainAction = "stop-new"
	DrainActionCancel         DrainAction = "cancel"
	DrainActionFence          DrainAction = "fence"
	DrainActionGenerationBump DrainAction = "generation-bump"
	DrainActionKill           DrainAction = "kill"
)

type DrainLeaseStatus string

const (
	DrainLeaseActive   DrainLeaseStatus = "active"
	DrainLeaseRevoked  DrainLeaseStatus = "revoked"
	DrainLeaseExpired  DrainLeaseStatus = "expired"
	DrainLeaseReplaced DrainLeaseStatus = "replaced"
)

type DrainRegistrationStatus string

const (
	DrainRegistrationActive   DrainRegistrationStatus = "active"
	DrainRegistrationInactive DrainRegistrationStatus = "inactive"
)

type DrainSnapshotStatus string

const (
	DrainSnapshotCurrent DrainSnapshotStatus = "current"
	DrainSnapshotStale   DrainSnapshotStatus = "stale"
)

type DrainReasonCode string

const (
	DrainAccepted           DrainReasonCode = "accepted"
	DrainInProgress         DrainReasonCode = "drain-in-progress"
	DrainDeadlineReached    DrainReasonCode = "drain-deadline-reached"
	DrainInvalidRequest     DrainReasonCode = "invalid-request"
	DrainAuthorityMismatch  DrainReasonCode = "authority-mismatch"
	DrainLeaseInactive      DrainReasonCode = "lease-inactive"
	DrainSuccessorInvalid   DrainReasonCode = "successor-invalid"
	DrainGenerationOverflow DrainReasonCode = "generation-overflow"
)

type DrainLease struct {
	Ref             string
	Identity        string
	Digest          string
	Generation      int64
	Deadline        time.Time
	RegistrationRef string
	SnapshotRef     string
	Status          DrainLeaseStatus
}

type DrainRegistration struct {
	Ref      string
	Identity string
	Status   DrainRegistrationStatus
}

type DrainSnapshot struct {
	Ref      string
	Identity string
	Status   DrainSnapshotStatus
	Deadline time.Time
}

type DrainLeaseAuthorityResolver interface {
	ResolveDrainLease(ref string) (DrainLease, error)
	ResolveDrainRegistration(ref string) (DrainRegistration, error)
	ResolveDrainSnapshot(ref string) (DrainSnapshot, error)
}

type DrainRequest struct {
	Trigger            DrainTrigger
	Reason             DrainReason
	Now                time.Time
	NewRegistrationRef string
	NewSnapshotRef     string
}

type DrainSuccessorFacts struct {
	Registration DrainRegistration
	Snapshot     DrainSnapshot
}

type DrainDecision struct {
	Accepted       bool
	Mode           DrainMode
	ReasonCode     DrainReasonCode
	Actions        []DrainAction
	NextGeneration int64
	DrainDeadline  time.Time
	Successor      DrainSuccessorFacts
}

type DrainDecider struct {
	mu          sync.Mutex
	resolver    DrainLeaseAuthorityResolver
	oldLeaseRef string
	pinned      DrainLease
	poisoned    bool
}

var (
	DrainErrNilResolver     = errors.New("revokedrain: drain resolver is nil")
	DrainErrInvalidOldLease = errors.New("revokedrain: old drain lease is invalid")
	DrainErrAuthorityLookup = errors.New("revokedrain: drain authority lookup failed")
)

// NewDrainDecider pins the complete old lease authority record. Every decision
// re-resolves the same key and permanently poisons the decider after any ABA,
// lookup failure, or content mismatch.
func NewDrainDecider(oldLeaseRef string, resolver DrainLeaseAuthorityResolver) (*DrainDecider, error) {
	if resolver == nil {
		return nil, DrainErrNilResolver
	}
	lease, err := resolver.ResolveDrainLease(oldLeaseRef)
	if err != nil {
		return nil, DrainErrAuthorityLookup
	}
	if !validDrainLease(oldLeaseRef, lease) {
		return nil, DrainErrInvalidOldLease
	}
	return &DrainDecider{resolver: resolver, oldLeaseRef: oldLeaseRef, pinned: lease}, nil
}

func (d *DrainDecider) Decide(request DrainRequest) DrainDecision {
	if d == nil || d.resolver == nil {
		return rejectedDrain(DrainInvalidRequest)
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.poisoned {
		return rejectedDrain(DrainAuthorityMismatch)
	}
	// Authority freshness is checked before any request field is interpreted.
	current, err := d.resolver.ResolveDrainLease(d.oldLeaseRef)
	if err != nil || current != d.pinned {
		d.poisoned = true
		return rejectedDrain(DrainAuthorityMismatch)
	}
	if !validDrainLease(d.oldLeaseRef, current) {
		return rejectedDrain(DrainLeaseInactive)
	}
	if request.Now.IsZero() || !validDrainTriggerReason(request.Trigger, request.Reason) {
		return rejectedDrain(DrainInvalidRequest)
	}
	if current.Generation == math.MaxInt64 {
		return rejectedDrain(DrainGenerationOverflow)
	}

	if request.Trigger == DrainTriggerRevoke {
		return DrainDecision{
			Accepted: true, Mode: DrainModeImmediate, ReasonCode: DrainAccepted,
			Actions: immediateDrainActions(), NextGeneration: current.Generation + 1,
		}
	}

	registration, err := d.resolver.ResolveDrainRegistration(request.NewRegistrationRef)
	if err != nil {
		return rejectedDrain(DrainSuccessorInvalid)
	}
	snapshot, err := d.resolver.ResolveDrainSnapshot(request.NewSnapshotRef)
	if err != nil || !validDrainSuccessor(current, request, registration, snapshot) {
		return rejectedDrain(DrainSuccessorInvalid)
	}

	decision := DrainDecision{
		Accepted: true, Mode: DrainModeDraining, ReasonCode: DrainInProgress,
		Actions: []DrainAction{DrainActionStopNew}, NextGeneration: current.Generation,
		DrainDeadline: current.Deadline,
		Successor:     DrainSuccessorFacts{Registration: registration, Snapshot: snapshot},
	}
	if !request.Now.Before(current.Deadline) {
		decision.Mode = DrainModeExpired
		decision.ReasonCode = DrainDeadlineReached
		decision.Actions = []DrainAction{DrainActionStopNew, DrainActionCancel, DrainActionFence, DrainActionGenerationBump}
		decision.NextGeneration = current.Generation + 1
	}
	return decision
}

func validDrainLease(ref string, lease DrainLease) bool {
	if blank(ref) || lease.Ref != ref || blank(lease.Identity) || blank(lease.Digest) || lease.Generation <= 0 ||
		lease.Deadline.IsZero() || blank(lease.RegistrationRef) || blank(lease.SnapshotRef) || lease.Status != DrainLeaseActive {
		return false
	}
	// The authority chain must contain three distinct object references. This
	// blocks self-reference and aliasing before any successor lookup occurs.
	return allDistinct(lease.Ref, lease.RegistrationRef, lease.SnapshotRef)
}

func validDrainTriggerReason(trigger DrainTrigger, reason DrainReason) bool {
	if trigger == DrainTriggerUpgrade {
		return reason == DrainReasonIncompatibleUpgrade
	}
	if trigger != DrainTriggerRevoke {
		return false
	}
	switch reason {
	case DrainReasonCredentialCompromise, DrainReasonProtocolViolation, DrainReasonEvidenceTampering, DrainReasonUnauthorizedAccess:
		return true
	default:
		return false
	}
}

func validDrainSuccessor(old DrainLease, request DrainRequest, registration DrainRegistration, snapshot DrainSnapshot) bool {
	if blank(request.NewRegistrationRef) || blank(request.NewSnapshotRef) ||
		registration.Ref != request.NewRegistrationRef || snapshot.Ref != request.NewSnapshotRef ||
		registration.Status != DrainRegistrationActive || snapshot.Status != DrainSnapshotCurrent ||
		blank(registration.Identity) || blank(snapshot.Identity) || registration.Identity != snapshot.Identity ||
		registration.Identity == old.Identity || !snapshot.Deadline.After(old.Deadline) || !snapshot.Deadline.After(request.Now) {
		return false
	}
	return allDistinct(old.Ref, old.RegistrationRef, old.SnapshotRef, registration.Ref, snapshot.Ref)
}

func allDistinct(values ...string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if blank(value) {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func rejectedDrain(code DrainReasonCode) DrainDecision {
	return DrainDecision{Mode: DrainModeRejected, ReasonCode: code}
}

func immediateDrainActions() []DrainAction {
	return []DrainAction{DrainActionStopNew, DrainActionCancel, DrainActionFence, DrainActionGenerationBump, DrainActionKill}
}
