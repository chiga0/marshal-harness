package domain

import (
	"encoding/json"
	"testing"
)

func TestNewSandboxRequirementsAcceptsAllCartesianCombinations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		accessMode AccessMode
		level      AssuranceLevel
	}{
		{AccessModeReadOnly, AssuranceLevelWorkspaceWrite},
		{AccessModeReadOnly, AssuranceLevelHardened},
		{AccessModeWorkspaceWrite, AssuranceLevelWorkspaceWrite},
		{AccessModeWorkspaceWrite, AssuranceLevelHardened},
	} {
		requirements, err := NewSandboxRequirements(test.accessMode, test.level)
		if err != nil {
			t.Fatalf("NewSandboxRequirements(%s, %s) error = %v", test.accessMode, test.level, err)
		}
		if requirements.APIVersion != APIVersionV1Alpha1 || requirements.Kind != KindSandboxRequirements {
			t.Fatalf("NewSandboxRequirements(%s, %s) envelope = %+v", test.accessMode, test.level, requirements)
		}
		if requirements.AccessMode != test.accessMode || requirements.MinimumAssuranceLevel != test.level {
			t.Fatalf("NewSandboxRequirements(%s, %s) = %+v", test.accessMode, test.level, requirements)
		}
	}
}

func TestNewSandboxRequirementsRejectsInvalidDimensions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		accessMode AccessMode
		level      AssuranceLevel
	}{
		{name: "empty accessMode", accessMode: "", level: AssuranceLevelWorkspaceWrite},
		{name: "unknown accessMode", accessMode: "sandboxed", level: AssuranceLevelWorkspaceWrite},
		{name: "case-mangled accessMode", accessMode: "Read-Only", level: AssuranceLevelWorkspaceWrite},
		{name: "assurance level used as accessMode", accessMode: AccessMode(AssuranceLevelHardened), level: AssuranceLevelWorkspaceWrite},
		{name: "empty minimumAssuranceLevel", accessMode: AccessModeReadOnly, level: ""},
		{name: "unknown minimumAssuranceLevel", accessMode: AccessModeReadOnly, level: "ultra"},
		{name: "case-mangled minimumAssuranceLevel", accessMode: AccessModeReadOnly, level: "HARDENED"},
		{name: "access mode used as minimumAssuranceLevel", accessMode: AccessModeReadOnly, level: AssuranceLevel(AccessModeReadOnly)},
	} {
		requirements, err := NewSandboxRequirements(test.accessMode, test.level)
		if err == nil {
			t.Fatalf("%s unexpectedly produced %+v", test.name, requirements)
		}
	}
}

func TestSandboxRequirementsFromLegacyProfileMapping(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		profile    string
		accessMode AccessMode
		level      AssuranceLevel
	}{
		{profile: "read-only", accessMode: AccessModeReadOnly, level: AssuranceLevelWorkspaceWrite},
		{profile: "workspace-write", accessMode: AccessModeWorkspaceWrite, level: AssuranceLevelWorkspaceWrite},
		{profile: "hardened", accessMode: AccessModeWorkspaceWrite, level: AssuranceLevelHardened},
	} {
		requirements, err := SandboxRequirementsFromLegacy(test.profile)
		if err != nil {
			t.Fatalf("SandboxRequirementsFromLegacy(%q) error = %v", test.profile, err)
		}
		if requirements.APIVersion != APIVersionV1Alpha1 || requirements.Kind != KindSandboxRequirements {
			t.Fatalf("SandboxRequirementsFromLegacy(%q) envelope = %+v", test.profile, requirements)
		}
		if requirements.AccessMode != test.accessMode || requirements.MinimumAssuranceLevel != test.level {
			t.Fatalf("SandboxRequirementsFromLegacy(%q) = %+v", test.profile, requirements)
		}
	}
}

func TestSandboxRequirementsFromLegacyProfileFailsClosed(t *testing.T) {
	t.Parallel()

	for _, profile := range []string{"", "Read-Only", "WORKSPACE-WRITE", "Hardened", "sandboxed", "read-only x hardened"} {
		requirements, err := SandboxRequirementsFromLegacy(profile)
		if err == nil {
			t.Fatalf("SandboxRequirementsFromLegacy(%q) unexpectedly produced %+v", profile, requirements)
		}
	}
}

func TestSandboxRequirementsWireShape(t *testing.T) {
	t.Parallel()

	requirements, err := NewSandboxRequirements(AccessModeReadOnly, AssuranceLevelHardened)
	if err != nil {
		t.Fatalf("NewSandboxRequirements() error = %v", err)
	}
	data, err := json.Marshal(requirements)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 4 {
		t.Fatalf("wire object = %v, want exactly four members", wire)
	}
	for key, want := range map[string]string{
		"apiVersion":            "marshal.dev/v1alpha1",
		"kind":                  "SandboxRequirements",
		"accessMode":            "read-only",
		"minimumAssuranceLevel": "hardened",
	} {
		got, ok := wire[key].(string)
		if !ok || got != want {
			t.Fatalf("wire member %s = %v, want %q", key, wire[key], want)
		}
	}
	var roundTrip SandboxRequirements
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip != requirements {
		t.Fatalf("round trip = %+v, want %+v", roundTrip, requirements)
	}
}

func TestSandboxRequirementDimensionsAreClosed(t *testing.T) {
	t.Parallel()

	if got := AccessModes(); len(got) != 2 || got[0] != AccessModeReadOnly || got[1] != AccessModeWorkspaceWrite {
		t.Fatalf("AccessModes() = %v", got)
	}
	if got := AssuranceLevels(); len(got) != 2 || got[0] != AssuranceLevelWorkspaceWrite || got[1] != AssuranceLevelHardened {
		t.Fatalf("AssuranceLevels() = %v", got)
	}
}
