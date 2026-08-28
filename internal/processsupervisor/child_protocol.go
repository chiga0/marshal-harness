package processsupervisor

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	SupervisorBootstrapFD  = uintptr(3)
	SupervisorControlDirFD = uintptr(4)

	childSpecFD    = uintptr(3)
	childReadyFD   = uintptr(4)
	childReleaseFD = uintptr(5)
	childCwdFD     = uintptr(6)
	childRuntimeFD = uintptr(7)
	childMarshalFD = uintptr(8)
	childClosureFD = 9

	childReadyByte   = byte('R')
	childReleaseByte = byte('G')
)

type childObject struct {
	FD     int            `json:"fd"`
	Object HeldObjectSpec `json:"object"`
}

type childSpec struct {
	ProtocolRevision string        `json:"protocolRevision"`
	ParentPID        int           `json:"parentPid"`
	Runtime          childObject   `json:"runtime"`
	WorkingDirectory childObject   `json:"workingDirectory"`
	Marshal          childObject   `json:"marshal"`
	MaterialRoots    []childObject `json:"materialRoots"`
	LaunchMaterials  []childObject `json:"launchMaterials"`
	Argv             []string      `json:"argv"`
	Environment      []string      `json:"environment"`
}

func (spec childSpec) canonical() ([]byte, error) {
	if spec.validate() != nil {
		return nil, ErrInvalid
	}
	return canonicalValue(spec)
}

func decodeChildSpec(raw []byte) (childSpec, error) {
	var spec childSpec
	if len(raw) == 0 || len(raw) > MaxWireFrameBytes || strictCanonicalDecode(raw, &spec) != nil || spec.validate() != nil {
		return childSpec{}, ErrInvalid
	}
	return spec, nil
}

func (spec childSpec) validate() error {
	if spec.ProtocolRevision != ProtocolRevision || spec.ParentPID <= 1 || uint64(spec.ParentPID) > maxSafeJSONInteger || spec.Runtime.FD != int(childRuntimeFD) || spec.WorkingDirectory.FD != int(childCwdFD) || spec.Marshal.FD != int(childMarshalFD) ||
		spec.Runtime.Object.validate("runtime", "regular") != nil || spec.WorkingDirectory.Object.validate("working-directory", "directory") != nil || spec.Marshal.Object.validate("marshal", "regular") != nil ||
		len(spec.Argv) == 0 || len(spec.Argv) > MaxArgvEntries || spec.Argv[0] != spec.Runtime.Object.CanonicalPath || !filepath.IsAbs(spec.Argv[0]) || filepath.Clean(spec.Argv[0]) != spec.Argv[0] || len(spec.Environment) > MaxEnvironmentKeys {
		return ErrInvalid
	}
	argvBytes := 0
	for _, argument := range spec.Argv {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
			return ErrInvalid
		}
		argvBytes += len(argument)
	}
	if argvBytes > MaxArgvBytes {
		return ErrInvalid
	}
	environmentBytes := 0
	previousKey := ""
	for _, entry := range spec.Environment {
		key, value, found := strings.Cut(entry, "=")
		if !found || !validEnvironmentKey(key) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || previousKey != "" && previousKey >= key {
			return ErrInvalid
		}
		previousKey = key
		environmentBytes += len(value)
	}
	if environmentBytes > MaxEnvironmentBytes {
		return ErrInvalid
	}
	next := childClosureFD
	roles := map[string]struct{}{"runtime": {}, "working-directory": {}, "marshal": {}}
	objects := map[[2]uint64]struct{}{
		{spec.Runtime.Object.Device, spec.Runtime.Object.Inode}:                   {},
		{spec.WorkingDirectory.Object.Device, spec.WorkingDirectory.Object.Inode}: {},
		{spec.Marshal.Object.Device, spec.Marshal.Object.Inode}:                   {},
	}
	if len(objects) != 3 {
		return ErrInvalid
	}
	for index, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		kind := "directory"
		if index >= len(spec.MaterialRoots) {
			kind = "regular"
		}
		identity := [2]uint64{object.Object.Device, object.Object.Inode}
		if object.FD != next || !validMaterialRole(object.Object.Role) || object.Object.validate(object.Object.Role, kind) != nil {
			return ErrInvalid
		}
		if _, exists := roles[object.Object.Role]; exists {
			return ErrInvalid
		}
		if _, exists := objects[identity]; exists {
			return ErrInvalid
		}
		roles[object.Object.Role] = struct{}{}
		objects[identity] = struct{}{}
		next++
	}
	return nil
}
