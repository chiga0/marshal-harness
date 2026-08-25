package agentregistry

import "fmt"

// ── RejectReason (closed enum) ────────────────────────────────────────────────

// RejectReason is the closed set of match rejection labels returned by Match.
type RejectReason string

const (
	RejectReasonInactiveRegistration RejectReason = "inactive-registration"
	RejectReasonInactiveSnapshot     RejectReason = "inactive-snapshot"
	RejectReasonBindingMismatch      RejectReason = "binding-mismatch"
	RejectReasonCapabilityMissing    RejectReason = "capability-missing"
	RejectReasonProtocolMismatch     RejectReason = "protocol-mismatch"
	RejectReasonEvidenceInsufficient RejectReason = "evidence-insufficient"
)

// ── Requirement ───────────────────────────────────────────────────────────────

// Requirement expresses the minimum conditions for a Match to succeed.
type Requirement struct {
	ProtocolVersion         string
	RequiredCapabilities    []Capability
	MinConformanceEvidences int
}

// ── MatchResult ───────────────────────────────────────────────────────────────

// MatchResult is the outcome of a Match call.
type MatchResult struct {
	Matched bool
	Reason  RejectReason // non-empty when Matched is false
}

// ── Match ─────────────────────────────────────────────────────────────────────

// Match evaluates whether the registration+snapshot pair satisfies the
// requirement. All conditions must pass; the first failing condition sets the
// RejectReason and the call returns without checking further (fail closed).
//
// Conditions checked in order:
//  1. registration.LifecycleState == active
//  2. snapshot.SnapshotState == active
//  3. snapshot.RegistrationID == registration.RegistrationID (binding)
//  4. snapshot.ProtocolVersion == requirement.ProtocolVersion
//  5. snapshot.Capabilities ⊇ requirement.RequiredCapabilities
//  6. len(snapshot.ConformanceEvidenceDigests) >= requirement.MinConformanceEvidences
func Match(req Requirement, reg AgentRegistration, snap AgentCapabilitySnapshot) (MatchResult, error) {
	if err := reg.Validate(); err != nil {
		return MatchResult{}, fmt.Errorf("agentregistry: Match: invalid registration: %w", err)
	}
	if err := snap.Validate(); err != nil {
		return MatchResult{}, fmt.Errorf("agentregistry: Match: invalid snapshot: %w", err)
	}

	if reg.LifecycleState != LifecycleStateActive {
		return MatchResult{Matched: false, Reason: RejectReasonInactiveRegistration}, nil
	}
	if snap.SnapshotState != SnapshotStateActive {
		return MatchResult{Matched: false, Reason: RejectReasonInactiveSnapshot}, nil
	}
	if snap.RegistrationID != reg.RegistrationID {
		return MatchResult{Matched: false, Reason: RejectReasonBindingMismatch}, nil
	}
	if snap.ProtocolVersion != req.ProtocolVersion {
		return MatchResult{Matched: false, Reason: RejectReasonProtocolMismatch}, nil
	}

	// Build capability index for O(n+m) coverage check.
	capIndex := make(map[Capability]struct{}, len(snap.Capabilities))
	for _, c := range snap.Capabilities {
		capIndex[c] = struct{}{}
	}
	for _, required := range req.RequiredCapabilities {
		if _, ok := capIndex[required]; !ok {
			return MatchResult{Matched: false, Reason: RejectReasonCapabilityMissing}, nil
		}
	}

	if len(snap.ConformanceEvidenceDigests) < req.MinConformanceEvidences {
		return MatchResult{Matched: false, Reason: RejectReasonEvidenceInsufficient}, nil
	}

	return MatchResult{Matched: true}, nil
}
