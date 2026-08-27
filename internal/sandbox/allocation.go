package sandbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// AllocationState is the closed enumeration of sandbox allocation states.
type AllocationState string

// Closed members of AllocationState.
const (
	AllocationProvisioning AllocationState = "provisioning"
	AllocationActive       AllocationState = "active"
	AllocationTerminated   AllocationState = "terminated"
	AllocationReplaced     AllocationState = "replaced"
	AllocationFailed       AllocationState = "failed"
)

var (
	// ErrInvalidAllocationState rejects values outside the closed state
	// enumeration.
	ErrInvalidAllocationState = errors.New("sandbox: invalid allocation state")
	// ErrInvalidAllocation rejects a malformed allocation record.
	ErrInvalidAllocation = errors.New("sandbox: invalid sandbox allocation")
	// ErrAllocationScopeMismatch rejects mixing allocations of different
	// (runId, attemptId) scopes in one single-active adjudication.
	ErrAllocationScopeMismatch = errors.New("sandbox: allocation runId/attemptId scope mismatch")
	// ErrDuplicateActiveAllocation freezes the single-active invariant: at
	// most one allocation holding the current generation may be active for
	// one (runId, attemptId) at any moment.
	ErrDuplicateActiveAllocation = errors.New("sandbox: single-active invariant violated, another allocation already holds the current generation")
	// ErrStaleAllocationGeneration rejects a stale allocation generation,
	// including stale handles presented after a restore.
	ErrStaleAllocationGeneration = errors.New("sandbox: stale allocation generation rejected")
	// ErrRestoreRejected freezes the restore decision rules of PlanRestore.
	ErrRestoreRejected = errors.New("sandbox: restore rejected")
	// ErrAssuranceNotMet fails closed when a hardened request carries no
	// valid conformance evidence reference; a provider must never downgrade
	// such a request to workspace-write.
	ErrAssuranceNotMet = errors.New("sandbox: hardened assurance requested without valid conformance evidence, refusing to serve the request")
	// ErrAssuranceDowngrade rejects any granted assurance level below the
	// requested minimum.
	ErrAssuranceDowngrade = errors.New("sandbox: assurance level downgrade rejected")
	// ErrAccessModeMismatch rejects any granted access mode that differs
	// from the requested access mode.
	ErrAccessModeMismatch = errors.New("sandbox: access mode mismatch rejected")
)

// Validate rejects every value outside the closed enumeration.
func (state AllocationState) Validate() error {
	switch state {
	case AllocationProvisioning, AllocationActive, AllocationTerminated, AllocationReplaced, AllocationFailed:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAllocationState, string(state))
	}
}

// IsTerminal reports whether the state ends the allocation: terminated,
// replaced and failed allocations never return to any in-flight state.
func (state AllocationState) IsTerminal() bool {
	return state == AllocationTerminated || state == AllocationReplaced || state == AllocationFailed
}

// Validate fails closed on any missing or malformed field of the allocation
// record.
func (allocation SandboxAllocation) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"allocationId", allocation.AllocationId},
		{"runId", allocation.RunId},
		{"attemptId", allocation.AttemptId},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: allocation.%s must be a non-empty string", ErrInvalidAllocation, field.name)
		}
	}
	if allocation.Generation < 1 {
		return fmt.Errorf("%w: allocation.generation must be a positive integer", ErrInvalidAllocation)
	}
	if err := allocation.State.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
	}
	if _, err := domain.ParseAccessMode(string(allocation.AccessMode)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
	}
	if _, err := domain.ParseAssuranceLevel(string(allocation.AssuranceLevel)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
	}
	if allocation.ConformanceEvidenceRef != "" {
		if err := requireSHA256("allocation.conformanceEvidenceRef", allocation.ConformanceEvidenceRef); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
	}
	for index, storeId := range allocation.AllowedStoreIds {
		if err := validateStoreAliasShape(storeId); err != nil {
			return fmt.Errorf("%w: allocation.allowedStoreIds[%d]: %v", ErrInvalidAllocation, index, err)
		}
	}
	if err := ValidateWorkDirAllowlist(allocation.WorkDirAllowlist); err != nil {
		return fmt.Errorf("%w: allocation.workDirAllowlist: %v", ErrInvalidAllocation, err)
	}
	if err := ValidateEnvironmentAllowlist(allocation.EnvironmentAllowlist); err != nil {
		return fmt.Errorf("%w: allocation.environmentAllowlist: %v", ErrInvalidAllocation, err)
	}
	return nil
}

// CheckSingleActive adjudicates the single-active invariant (ADR 0017 §6):
// within one (runId, attemptId) at most one allocation holding the current
// generation may be active at any moment. The candidate is rejected when its
// generation is stale, or when another non-terminal allocation already holds
// the same current generation. Re-admitting the identical allocation record
// is idempotent and allowed.
func CheckSingleActive(existing []SandboxAllocation, candidate SandboxAllocation) error {
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("sandbox: single-active check: %w", err)
	}
	var currentGeneration int64
	for _, held := range existing {
		if err := held.Validate(); err != nil {
			return fmt.Errorf("sandbox: single-active check: %w", err)
		}
		if held.RunId != candidate.RunId || held.AttemptId != candidate.AttemptId {
			return fmt.Errorf("%w: allocation %q belongs to run %q attempt %q", ErrAllocationScopeMismatch, held.AllocationId, held.RunId, held.AttemptId)
		}
		if held.Generation > currentGeneration {
			currentGeneration = held.Generation
		}
	}
	if candidate.Generation < currentGeneration {
		return fmt.Errorf("%w: candidate generation %d is behind the current generation %d", ErrStaleAllocationGeneration, candidate.Generation, currentGeneration)
	}
	if candidate.Generation == currentGeneration {
		for _, held := range existing {
			if held.Generation == candidate.Generation && held.AllocationId != candidate.AllocationId && !held.State.IsTerminal() {
				return fmt.Errorf("%w: allocation %q is not terminal at generation %d", ErrDuplicateActiveAllocation, held.AllocationId, held.Generation)
			}
		}
	}
	return nil
}

// RestoreRequest carries the caller-side inputs of the restore decision.
// InPlaceConfirmed is the caller's attestation that the previous process
// tree is terminated or never existed; the SPI layer freezes the decision
// logic and never derives that attestation itself.
type RestoreRequest struct {
	Previous         SandboxAllocation
	NextAllocationId string
	InPlaceConfirmed bool
}

// PlanRestore freezes the restore semantics: the default is a replacement
// allocation with a fresh allocationId; an in-place restore is permitted
// only when InPlaceConfirmed is set. In both modes the generation increases
// monotonically by exactly one, and the returned allocation starts in the
// provisioning state.
func PlanRestore(request RestoreRequest) (SandboxAllocation, error) {
	if err := request.Previous.Validate(); err != nil {
		return SandboxAllocation{}, fmt.Errorf("%w: previous allocation: %v", ErrRestoreRejected, err)
	}
	next := request.Previous
	next.Generation = request.Previous.Generation + 1
	next.State = AllocationProvisioning
	if request.InPlaceConfirmed {
		if strings.TrimSpace(request.NextAllocationId) != "" && request.NextAllocationId != request.Previous.AllocationId {
			return SandboxAllocation{}, fmt.Errorf("%w: an in-place restore must not carry a replacement allocationId", ErrRestoreRejected)
		}
		return next, nil
	}
	if strings.TrimSpace(request.NextAllocationId) == "" {
		return SandboxAllocation{}, fmt.Errorf("%w: a replacement restore requires a next allocationId", ErrRestoreRejected)
	}
	if request.NextAllocationId == request.Previous.AllocationId {
		return SandboxAllocation{}, fmt.Errorf("%w: a replacement restore must not reuse the previous allocationId", ErrRestoreRejected)
	}
	next.AllocationId = request.NextAllocationId
	return next, nil
}

// AssuranceRank orders the closed assurance levels: workspace-write ranks
// below hardened.
func AssuranceRank(level domain.AssuranceLevel) int {
	if level == domain.AssuranceLevelHardened {
		return 1
	}
	return 0
}

func validateRequirements(requirements domain.SandboxRequirements) error {
	if _, err := domain.ParseAccessMode(string(requirements.AccessMode)); err != nil {
		return fmt.Errorf("sandbox: requirements: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(requirements.MinimumAssuranceLevel)); err != nil {
		return fmt.Errorf("sandbox: requirements: %w", err)
	}
	return nil
}

// CheckAssuranceGate fails closed when the requested minimum assurance level
// cannot be served: a hardened request requires a valid (non-empty,
// well-formed) conformance evidence reference. Providers must refuse such a
// request outright and never downgrade it to workspace-write.
func CheckAssuranceGate(requirements domain.SandboxRequirements, conformanceEvidenceRef string) error {
	if err := validateRequirements(requirements); err != nil {
		return err
	}
	if requirements.MinimumAssuranceLevel != domain.AssuranceLevelHardened {
		return nil
	}
	if strings.TrimSpace(conformanceEvidenceRef) == "" {
		return ErrAssuranceNotMet
	}
	if err := requireSHA256("conformanceEvidenceRef", conformanceEvidenceRef); err != nil {
		return fmt.Errorf("%w: %v", ErrAssuranceNotMet, err)
	}
	return nil
}

// ValidateAllocationRequirements rejects any downgrade: the allocation's
// effective assurance level must reach the requested minimum and its access
// mode must equal the requested access mode.
func ValidateAllocationRequirements(allocation SandboxAllocation, requirements domain.SandboxRequirements) error {
	if err := allocation.Validate(); err != nil {
		return err
	}
	if err := validateRequirements(requirements); err != nil {
		return err
	}
	if AssuranceRank(allocation.AssuranceLevel) < AssuranceRank(requirements.MinimumAssuranceLevel) {
		return fmt.Errorf("%w: the allocation grants %q below the requested minimum %q", ErrAssuranceDowngrade, string(allocation.AssuranceLevel), string(requirements.MinimumAssuranceLevel))
	}
	if allocation.AccessMode != requirements.AccessMode {
		return fmt.Errorf("%w: the allocation grants %q against the requested %q", ErrAccessModeMismatch, string(allocation.AccessMode), string(requirements.AccessMode))
	}
	return nil
}
