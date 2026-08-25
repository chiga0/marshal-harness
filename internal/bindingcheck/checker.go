package bindingcheck

import (
	"errors"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

// RejectionReason is the closed seven-value enum of recheck rejection labels.
type RejectionReason string

const (
	RejectionReasonProfileInvalid            RejectionReason = "profile-invalid"
	RejectionReasonAgentUnknownRegistration  RejectionReason = "agent-unknown-registration"
	RejectionReasonAgentRegistrationInactive RejectionReason = "agent-registration-inactive"
	RejectionReasonAgentSnapshotMismatch     RejectionReason = "agent-snapshot-mismatch"
	RejectionReasonSandboxUnknownAllocation  RejectionReason = "sandbox-unknown-allocation"
	RejectionReasonSandboxAllocationInactive RejectionReason = "sandbox-allocation-inactive"
	RejectionReasonSandboxGenerationMismatch RejectionReason = "sandbox-generation-mismatch"
)

// SideResult carries the conclusion for one side (Agent or Sandbox).
type SideResult struct {
	OK      bool
	Reasons []RejectionReason // non-empty when OK == false
}

// Result is the combined outcome of a Recheck call. Both sides are always
// evaluated independently. Accepted only when both Agent and Sandbox are OK.
type Result struct {
	Agent   SideResult
	Sandbox SideResult
}

// Accepted returns true when both sides passed.
func (r Result) Accepted() bool {
	return r.Agent.OK && r.Sandbox.OK
}

// Checker performs dual-independent ledger recheck of a WorkerRuntimeProfile.
type Checker struct {
	registry *agentregistry.Registry
	ledger   *SandboxLedger
}

// NewChecker constructs a Checker. Returns an error if either argument is nil
// (fail closed).
func NewChecker(registry *agentregistry.Registry, ledger *SandboxLedger) (*Checker, error) {
	if registry == nil {
		return nil, errors.New("bindingcheck: agentregistry.Registry must not be nil")
	}
	if ledger == nil {
		return nil, errors.New("bindingcheck: SandboxLedger must not be nil")
	}
	return &Checker{registry: registry, ledger: ledger}, nil
}

// Recheck performs dual-independent recheck of the profile against the current
// ledger state. Both sides are always fully evaluated; neither short-circuits
// the other. Malformed inputs (zero-value profile, empty IDs) return a typed
// error with the "bindingcheck: " prefix.
func (c *Checker) Recheck(profile runtimeprofile.WorkerRuntimeProfile) (Result, error) {
	if err := profile.Agent.Validate(); err != nil {
		return Result{}, fmt.Errorf("bindingcheck: profile invalid: %w", err)
	}
	if err := profile.Sandbox.Validate(); err != nil {
		return Result{}, fmt.Errorf("bindingcheck: profile invalid: %w", err)
	}

	agentSide := c.recheckAgent(profile.Agent)
	sandboxSide := c.recheckSandbox(profile.Sandbox)
	return Result{Agent: agentSide, Sandbox: sandboxSide}, nil
}

func (c *Checker) recheckAgent(binding runtimeprofile.AgentBinding) SideResult {
	reg, err := c.registry.Lookup(binding.RegistrationID)
	if err != nil {
		return SideResult{Reasons: []RejectionReason{RejectionReasonAgentUnknownRegistration}}
	}

	var reasons []RejectionReason

	switch reg.LifecycleState {
	case agentregistry.LifecycleStateActive:
		// ok
	default:
		reasons = append(reasons, RejectionReasonAgentRegistrationInactive)
	}

	snap, snapErr := c.registry.ActiveSnapshot(binding.RegistrationID)
	if snapErr != nil || snap.SnapshotDigest != binding.SnapshotDigest {
		reasons = append(reasons, RejectionReasonAgentSnapshotMismatch)
	}

	if len(reasons) == 0 {
		return SideResult{OK: true}
	}
	return SideResult{Reasons: reasons}
}

func (c *Checker) recheckSandbox(binding runtimeprofile.SandboxBinding) SideResult {
	entry := c.ledger.Lookup(binding.AllocationID)
	if entry == nil {
		return SideResult{Reasons: []RejectionReason{RejectionReasonSandboxUnknownAllocation}}
	}

	var reasons []RejectionReason

	if entry.state != AllocationStateActive {
		reasons = append(reasons, RejectionReasonSandboxAllocationInactive)
	}

	if int64(entry.generation) != binding.Generation {
		reasons = append(reasons, RejectionReasonSandboxGenerationMismatch)
	}

	if len(reasons) == 0 {
		return SideResult{OK: true}
	}
	return SideResult{Reasons: reasons}
}
