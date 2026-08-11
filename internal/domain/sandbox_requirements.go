package domain

import (
	"fmt"
	"slices"
)

// AccessMode is the permission axis of the frozen sandbox contract (ADR
// 0017): it answers what a workload may do inside its sandbox.
type AccessMode string

const (
	AccessModeReadOnly       AccessMode = "read-only"
	AccessModeWorkspaceWrite AccessMode = "workspace-write"
)

var accessModes = []AccessMode{
	AccessModeReadOnly,
	AccessModeWorkspaceWrite,
}

// AccessModes returns every AccessMode in stable order.
func AccessModes() []AccessMode {
	return slices.Clone(accessModes)
}

// ParseAccessMode fails closed on empty, unknown or case-mangled values.
func ParseAccessMode(value string) (AccessMode, error) {
	mode := AccessMode(value)
	if slices.Contains(accessModes, mode) {
		return mode, nil
	}
	return "", fmt.Errorf("unknown access mode %q", value)
}

// AssuranceLevel is the isolation axis of the frozen sandbox contract (ADR
// 0017): it answers how trustworthy the sandbox enforcement is.
type AssuranceLevel string

const (
	AssuranceLevelWorkspaceWrite AssuranceLevel = "workspace-write"
	AssuranceLevelHardened       AssuranceLevel = "hardened"
)

var assuranceLevels = []AssuranceLevel{
	AssuranceLevelWorkspaceWrite,
	AssuranceLevelHardened,
}

// AssuranceLevels returns every AssuranceLevel in stable order.
func AssuranceLevels() []AssuranceLevel {
	return slices.Clone(assuranceLevels)
}

// ParseAssuranceLevel fails closed on empty, unknown or case-mangled values.
func ParseAssuranceLevel(value string) (AssuranceLevel, error) {
	level := AssuranceLevel(value)
	if slices.Contains(assuranceLevels, level) {
		return level, nil
	}
	return "", fmt.Errorf("unknown assurance level %q", value)
}

// SandboxRequirements freezes the two orthogonal sandbox dimensions of a
// Run: the requested AccessMode and the minimum AssuranceLevel. All four
// combinations of the closed enumerations are legal.
type SandboxRequirements struct {
	APIVersion            APIVersion     `json:"apiVersion"`
	Kind                  Kind           `json:"kind"`
	AccessMode            AccessMode     `json:"accessMode"`
	MinimumAssuranceLevel AssuranceLevel `json:"minimumAssuranceLevel"`
}

// NewSandboxRequirements validates both dimensions and fails closed on
// empty, unknown or case-mangled values.
func NewSandboxRequirements(accessMode AccessMode, minimumAssuranceLevel AssuranceLevel) (SandboxRequirements, error) {
	mode, err := ParseAccessMode(string(accessMode))
	if err != nil {
		return SandboxRequirements{}, err
	}
	level, err := ParseAssuranceLevel(string(minimumAssuranceLevel))
	if err != nil {
		return SandboxRequirements{}, err
	}
	return SandboxRequirements{
		APIVersion:            APIVersionV1Alpha1,
		Kind:                  KindSandboxRequirements,
		AccessMode:            mode,
		MinimumAssuranceLevel: level,
	}, nil
}

// SandboxRequirementsFromLegacy is the only compatibility direction between
// the historical single-axis executionProfile and the two-dimensional
// contract. The mapping is deterministic: read-only maps to read-only x
// workspace-write, workspace-write maps to workspace-write x
// workspace-write, and hardened maps to workspace-write x hardened. Empty,
// unknown and case-mangled profiles fail closed. There is intentionally no
// reverse mapper because read-only x hardened has no legacy representation.
func SandboxRequirementsFromLegacy(profile string) (SandboxRequirements, error) {
	var accessMode AccessMode
	var assuranceLevel AssuranceLevel
	switch profile {
	case "read-only":
		accessMode, assuranceLevel = AccessModeReadOnly, AssuranceLevelWorkspaceWrite
	case "workspace-write":
		accessMode, assuranceLevel = AccessModeWorkspaceWrite, AssuranceLevelWorkspaceWrite
	case "hardened":
		accessMode, assuranceLevel = AccessModeWorkspaceWrite, AssuranceLevelHardened
	default:
		return SandboxRequirements{}, fmt.Errorf("unknown legacy execution profile %q", profile)
	}
	return NewSandboxRequirements(accessMode, assuranceLevel)
}
