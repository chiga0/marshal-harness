// Package sandboxlaunch defines the private, fail-closed protocol used by the
// fixed Marshal executable to enter an already-open working directory and
// execute a held-and-path-revalidated workload image.
package sandboxlaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	ProtocolRevision = "marshal-sandbox-launch/v1"

	SpecFD             = 3
	ReadyFD            = 4
	ReleaseFD          = 5
	WorkingDirectoryFD = 6
	ExecutableFD       = 7
	MarshalFD          = 8
	MaterialFDBase     = 9

	MaxSpecBytes      = 64 << 10
	maxArguments      = 256
	maxEnvironment    = 256
	maxStringBytes    = 16 << 10
	maxAggregateBytes = 48 << 10
	modeTypeMask      = uint32(0o170000)
	modeRegular       = uint32(0o100000)
	modeDirectory     = uint32(0o040000)
	modeFIFO          = uint32(0o010000)
	ReadyByte         = byte('R')
	ReleaseByte       = byte('G')
)

var (
	ErrProtocolRejected = errors.New("sandbox launch protocol rejected")
	ErrUnsupported      = errors.New("sandbox launch is unsupported on this platform")
)

// ObjectBinding is an immutable identity captured from an already-open file
// descriptor. SHA256 is required for executable images and omitted for pipes
// and directories.
type ObjectBinding struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Size   int64  `json:"size"`
	Nlink  uint64 `json:"nlink"`
	SHA256 string `json:"sha256,omitempty"`
}

// Spec is delivered over SpecFD. It binds every inherited object used by the
// helper to the parent process that authorized the launch.
type Spec struct {
	ProtocolRevision string            `json:"protocolRevision"`
	LaunchID         string            `json:"launchId"`
	ParentPID        int               `json:"parentPid"`
	Arguments        []string          `json:"arguments"`
	Environment      []string          `json:"environment"`
	ExecutablePath   string            `json:"executablePath"`
	SpecPipe         ObjectBinding     `json:"specPipe"`
	ReadyPipe        ObjectBinding     `json:"readyPipe"`
	ReleasePipe      ObjectBinding     `json:"releasePipe"`
	WorkingDirectory ObjectBinding     `json:"workingDirectory"`
	Executable       ObjectBinding     `json:"executable"`
	Marshal          ObjectBinding     `json:"marshal"`
	Roots            []RootBinding     `json:"roots"`
	Materials        []MaterialBinding `json:"materials"`
}

type RootBinding struct {
	Name   string        `json:"name"`
	Path   string        `json:"path"`
	FD     int           `json:"fd"`
	Object ObjectBinding `json:"object"`
}

type MaterialBinding struct {
	Role   string        `json:"role"`
	Path   string        `json:"path"`
	FD     int           `json:"fd"`
	Object ObjectBinding `json:"object"`
}

// Canonical encodes and validates a launch spec. Callers must send exactly the
// returned bytes; Decode rejects semantically equivalent non-canonical JSON.
func (spec Spec) Canonical() ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, ErrProtocolRejected
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || len(canonicalRaw) > MaxSpecBytes {
		return nil, ErrProtocolRejected
	}
	return canonicalRaw, nil
}

// Decode admits only bounded RFC 8785 canonical JSON with the exact v1 shape.
func Decode(raw []byte) (Spec, error) {
	if len(raw) == 0 || len(raw) > MaxSpecBytes {
		return Spec{}, ErrProtocolRejected
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return Spec{}, ErrProtocolRejected
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, ErrProtocolRejected
	}
	if decoder.More() {
		return Spec{}, ErrProtocolRejected
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func (spec Spec) Validate() error {
	if spec.ProtocolRevision != ProtocolRevision || ValidateLaunchID(spec.LaunchID) != nil || spec.ParentPID <= 1 {
		return ErrProtocolRejected
	}
	if err := ValidatePayload(spec.Arguments, spec.Environment); err != nil {
		return err
	}
	if !filepath.IsAbs(spec.ExecutablePath) || filepath.Clean(spec.ExecutablePath) != spec.ExecutablePath || spec.Arguments[0] != spec.ExecutablePath {
		return ErrProtocolRejected
	}
	for _, binding := range []ObjectBinding{spec.SpecPipe, spec.ReadyPipe, spec.ReleasePipe} {
		if binding.Inode == 0 || binding.Mode&modeTypeMask != modeFIFO || binding.Size < 0 || binding.SHA256 != "" {
			return ErrProtocolRejected
		}
	}
	if spec.WorkingDirectory.Inode == 0 || spec.WorkingDirectory.Mode&modeTypeMask != modeDirectory || spec.WorkingDirectory.Nlink == 0 || spec.WorkingDirectory.Size < 0 || spec.WorkingDirectory.SHA256 != "" {
		return ErrProtocolRejected
	}
	for _, binding := range []ObjectBinding{spec.Executable, spec.Marshal} {
		if binding.Inode == 0 || binding.Size <= 0 || !validDigest(binding.SHA256) || binding.Mode&modeTypeMask != modeRegular || binding.Mode&0o111 == 0 || binding.Nlink != 1 {
			return ErrProtocolRejected
		}
	}
	nextFD := MaterialFDBase
	seenNames := map[string]bool{}
	seenRoles := map[string]bool{}
	for _, root := range spec.Roots {
		if !boundedToken(root.Name, 128) || !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || root.FD != nextFD || root.Object.Inode == 0 || root.Object.Mode&modeTypeMask != modeDirectory || root.Object.SHA256 != "" || seenNames[root.Name] {
			return ErrProtocolRejected
		}
		seenNames[root.Name] = true
		nextFD++
	}
	for _, material := range spec.Materials {
		root, _, ok := strings.Cut(material.Role, "/")
		if !ok || !seenNames[root] || !boundedToken(material.Role, 512) || strings.Contains(material.Role, "\\") || !filepath.IsAbs(material.Path) || filepath.Clean(material.Path) != material.Path || material.FD != nextFD || material.Object.Inode == 0 || material.Object.Mode&modeTypeMask != modeRegular || material.Object.Nlink != 1 || !validDigest(material.Object.SHA256) || seenRoles[material.Role] {
			return ErrProtocolRejected
		}
		seenRoles[material.Role] = true
		nextFD++
	}
	pipeIdentities := [][2]uint64{{spec.SpecPipe.Device, spec.SpecPipe.Inode}, {spec.ReadyPipe.Device, spec.ReadyPipe.Inode}, {spec.ReleasePipe.Device, spec.ReleasePipe.Inode}}
	for left := range pipeIdentities {
		for right := left + 1; right < len(pipeIdentities); right++ {
			if pipeIdentities[left] == pipeIdentities[right] {
				return ErrProtocolRejected
			}
		}
	}
	return nil
}

func ValidateLaunchID(value string) error {
	if !boundedToken(value, 128) {
		return ErrProtocolRejected
	}
	return nil
}

// ValidatePayload applies the bounded argv/environment contract before a
// launch-authorized transition is appended and again inside the helper.
func ValidatePayload(arguments, environment []string) error {
	if len(arguments) == 0 || len(arguments) > maxArguments || len(environment) > maxEnvironment {
		return ErrProtocolRejected
	}
	total := 0
	for _, argument := range arguments {
		if !boundedString(argument) {
			return ErrProtocolRejected
		}
		total += len(argument)
	}
	seenEnvironment := make(map[string]struct{}, len(environment))
	for _, item := range environment {
		if !boundedString(item) {
			return ErrProtocolRejected
		}
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" || strings.ContainsAny(name, "\x00=") {
			return ErrProtocolRejected
		}
		if _, duplicate := seenEnvironment[name]; duplicate {
			return ErrProtocolRejected
		}
		seenEnvironment[name] = struct{}{}
		total += len(item)
	}
	if total > maxAggregateBytes {
		return ErrProtocolRejected
	}
	return nil
}

func boundedString(value string) bool {
	return value != "" && len(value) <= maxStringBytes && !strings.ContainsRune(value, 0)
}

func boundedToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func protocolError(_ string, _ ...any) error {
	// Never include untrusted argv, environment, or filesystem data in the
	// terminal-facing error. The parent stores its own authoritative detail.
	return ErrProtocolRejected
}
