// Package launchidentity owns the closed, replayable identity of one agent
// launch.  It deliberately contains no provider callbacks or authority I/O.
package launchidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	Pi0843DarwinARM64Profile = "pi/0.84.3/darwin-arm64/v1"
	NativeProfile            = "native/v1"
	CoreFDReserve            = 32
)

var ErrUnavailable = errors.New("launch identity unavailable")

type ObjectV1 struct {
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	FileType      uint32 `json:"fileType"`
	Mode          uint32 `json:"mode"`
	UID           uint32 `json:"uid"`
	GID           uint32 `json:"gid"`
	Size          int64  `json:"size"`
	LinkCount     uint64 `json:"linkCount"`
	RawSHA256     string `json:"rawSha256"`
}

type MaterialRootV1 struct {
	Name            string   `json:"name"`
	CanonicalPath   string   `json:"canonicalPath"`
	PackageRelative string   `json:"packageRelative"`
	Object          ObjectV1 `json:"object"`
}

type LaunchMaterialV1 struct {
	Role   string   `json:"role"`
	Object ObjectV1 `json:"object"`
}

type ClosureV1 struct {
	RuntimeExecutable     ObjectV1           `json:"runtimeExecutableV1"`
	ClosureProfileID      string             `json:"closureProfileId"`
	MaterialRoots         []MaterialRootV1   `json:"materialRoots"`
	LaunchMaterials       []LaunchMaterialV1 `json:"launchMaterials"`
	LaunchMaterialsDigest string             `json:"launchMaterialsDigest"`
	AgentLaunchSpecDigest string             `json:"agentLaunchSpecDigest"`
	Arguments             []string           `json:"arguments"`
	Environment           []string           `json:"environment"`
	WorkingDirectory      string             `json:"workingDirectory"`
}

// StoredClosureV1 is the comparable projection used by AttemptAuthorityState.
// The append-only fact still stores ClosureV1 as structured closed JSON; the
// projection stores canonical arrays so state equality and replay remain
// deterministic without losing recovery data.
type StoredClosureV1 struct {
	RuntimeExecutable     ObjectV1 `json:"runtimeExecutableV1"`
	ClosureProfileID      string   `json:"closureProfileId"`
	MaterialRootsJSON     string   `json:"materialRootsJson"`
	LaunchMaterialsJSON   string   `json:"launchMaterialsJson"`
	LaunchMaterialsDigest string   `json:"launchMaterialsDigest"`
	AgentLaunchSpecDigest string   `json:"agentLaunchSpecDigest"`
	ArgumentsJSON         string   `json:"argumentsJson"`
	EnvironmentJSON       string   `json:"environmentJson"`
	WorkingDirectory      string   `json:"workingDirectory"`
}

func (closure ClosureV1) Stored() (StoredClosureV1, error) {
	roots, err := json.Marshal(closure.MaterialRoots)
	if err != nil {
		return StoredClosureV1{}, err
	}
	materials, err := json.Marshal(closure.LaunchMaterials)
	if err != nil {
		return StoredClosureV1{}, err
	}
	canonicalRoots, err := canonical.JSON(roots)
	if err != nil {
		return StoredClosureV1{}, err
	}
	canonicalMaterials, err := canonical.JSON(materials)
	if err != nil {
		return StoredClosureV1{}, err
	}
	arguments, err := canonicalArray(closure.Arguments)
	if err != nil {
		return StoredClosureV1{}, err
	}
	environment, err := canonicalArray(closure.Environment)
	if err != nil {
		return StoredClosureV1{}, err
	}
	return StoredClosureV1{closure.RuntimeExecutable, closure.ClosureProfileID, string(canonicalRoots), string(canonicalMaterials), closure.LaunchMaterialsDigest, closure.AgentLaunchSpecDigest, arguments, environment, closure.WorkingDirectory}, nil
}

func (stored StoredClosureV1) Closure() (ClosureV1, error) {
	var roots []MaterialRootV1
	var materials []LaunchMaterialV1
	var arguments, environment []string
	if err := strictJSON([]byte(stored.MaterialRootsJSON), &roots); err != nil {
		return ClosureV1{}, err
	}
	if err := strictJSON([]byte(stored.LaunchMaterialsJSON), &materials); err != nil {
		return ClosureV1{}, err
	}
	if err := strictJSON([]byte(stored.ArgumentsJSON), &arguments); err != nil {
		return ClosureV1{}, err
	}
	if err := strictJSON([]byte(stored.EnvironmentJSON), &environment); err != nil {
		return ClosureV1{}, err
	}
	closure := ClosureV1{stored.RuntimeExecutable, stored.ClosureProfileID, roots, materials, stored.LaunchMaterialsDigest, stored.AgentLaunchSpecDigest, arguments, environment, stored.WorkingDirectory}
	return closure, closure.Validate()
}

func canonicalArray(values []string) (string, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	canon, err := canonical.JSON(raw)
	return string(canon), err
}

func strictJSON(raw []byte, target any) error {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || string(canonicalRaw) != string(raw) {
		return ErrUnavailable
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrUnavailable
	}
	return nil
}

type SpecInput struct {
	RuntimeExecutable ObjectV1           `json:"runtimeExecutableV1"`
	ClosureProfileID  string             `json:"closureProfileId"`
	MaterialRoots     []MaterialRootV1   `json:"materialRoots"`
	LaunchMaterials   []LaunchMaterialV1 `json:"launchMaterials"`
	Arguments         []string           `json:"arguments"`
	Environment       []string           `json:"environment"`
	WorkingDirectory  string             `json:"workingDirectory"`
}

func DigestMaterials(materials []LaunchMaterialV1) (string, error) {
	// Empty material collections are always represented by the closed JSON
	// array [] (ADR 0058), never by null. In particular, append(nil, empty...)
	// would collapse an explicitly empty slice back to nil and make the digest
	// depend on an in-memory representation detail.
	copyOf := append([]LaunchMaterialV1{}, materials...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i].Role < copyOf[j].Role })
	raw, err := json.Marshal(copyOf)
	if err != nil {
		return "", err
	}
	canon, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canon), nil
}

func DigestSpec(input SpecInput) (string, error) {
	input = normalizeSpecInput(input)
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	canon, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canon), nil
}

func normalizeSpecInput(input SpecInput) SpecInput {
	input.MaterialRoots = append([]MaterialRootV1{}, input.MaterialRoots...)
	input.LaunchMaterials = append([]LaunchMaterialV1{}, input.LaunchMaterials...)
	input.Arguments = append([]string{}, input.Arguments...)
	input.Environment = append([]string{}, input.Environment...)
	return input
}

func (closure ClosureV1) Validate() error {
	if closure.ClosureProfileID == "" || validateObject(closure.RuntimeExecutable, true) != nil || validateSpecPayload(closure) != nil {
		return ErrUnavailable
	}
	seenRoots := map[string]bool{}
	seenObjects := map[[2]uint64]bool{{closure.RuntimeExecutable.Device, closure.RuntimeExecutable.Inode}: true}
	for _, root := range closure.MaterialRoots {
		key := [2]uint64{root.Object.Device, root.Object.Inode}
		if !safeToken(root.Name) || !safeRelative(root.PackageRelative) || !filepath.IsAbs(root.CanonicalPath) || root.CanonicalPath != filepath.Clean(root.CanonicalPath) || root.Object.CanonicalPath != root.CanonicalPath || root.Object.Device == 0 || root.Object.Inode == 0 || root.Object.FileType != 0o040000 || root.Object.Mode&0o170000 != 0o040000 || root.Object.LinkCount == 0 || root.Object.Size < 0 || root.Object.RawSHA256 != "" || seenRoots[root.Name] || seenObjects[key] {
			return ErrUnavailable
		}
		seenRoots[root.Name] = true
		seenObjects[key] = true
	}
	last := ""
	for _, material := range closure.LaunchMaterials {
		key := [2]uint64{material.Object.Device, material.Object.Inode}
		if !safeRole(material.Role) || material.Role <= last || validateObject(material.Object, false) != nil || seenObjects[key] {
			return ErrUnavailable
		}
		root, _, ok := strings.Cut(material.Role, "/")
		var rootPath string
		for _, candidate := range closure.MaterialRoots {
			if candidate.Name == root {
				rootPath = candidate.CanonicalPath
			}
		}
		if !ok || !seenRoots[root] || !withinRoot(rootPath, material.Object.CanonicalPath) {
			return ErrUnavailable
		}
		seenObjects[key] = true
		last = material.Role
	}
	digest, err := DigestMaterials(closure.LaunchMaterials)
	if err != nil || digest != closure.LaunchMaterialsDigest || !validDigest(closure.AgentLaunchSpecDigest) {
		return ErrUnavailable
	}
	specDigest, err := DigestSpec(SpecInput{closure.RuntimeExecutable, closure.ClosureProfileID, closure.MaterialRoots, closure.LaunchMaterials, closure.Arguments, closure.Environment, closure.WorkingDirectory})
	if err != nil || specDigest != closure.AgentLaunchSpecDigest {
		return ErrUnavailable
	}
	switch closure.ClosureProfileID {
	case Pi0843DarwinARM64Profile:
		if !validPi0843Shape(closure) {
			return ErrUnavailable
		}
	case NativeProfile:
		if len(closure.MaterialRoots) != 0 || len(closure.LaunchMaterials) != 0 {
			return ErrUnavailable
		}
	default:
		return ErrUnavailable
	}
	return nil
}

func validPi0843Shape(closure ClosureV1) bool {
	if len(closure.MaterialRoots) != 2 || len(closure.LaunchMaterials) != 55 || len(closure.Arguments) < 2 {
		return false
	}
	want := map[string]struct {
		rel   string
		count int
		bytes int64
	}{"pi-bundle": {"dist/bundle", 48, 7422432}, "photon-node": {"node_modules/@silvia-odwyer/photon-node", 7, 2265687}}
	counts := map[string]int{}
	totals := map[string]int64{}
	for _, root := range closure.MaterialRoots {
		declaration, ok := want[root.Name]
		if !ok || root.PackageRelative != declaration.rel {
			return false
		}
	}
	entry := false
	for _, material := range closure.LaunchMaterials {
		root, _, _ := strings.Cut(material.Role, "/")
		counts[root]++
		totals[root] += material.Object.Size
		if material.Role == "pi-bundle/cli.js" {
			entry = material.Object.Size == 629 && material.Object.RawSHA256 == "sha256:1c3a5094b54aae9ae98c66516ce8c6578140363d081471ca7e91f9cb8c23dc8a" && closure.Arguments[1] == material.Object.CanonicalPath
		}
	}
	for name, declaration := range want {
		if counts[name] != declaration.count || totals[name] != declaration.bytes {
			return false
		}
	}
	return entry
}

func validateSpecPayload(closure ClosureV1) error {
	if len(closure.Arguments) == 0 || len(closure.Arguments) > 256 || len(closure.Environment) > 256 || closure.Arguments[0] != closure.RuntimeExecutable.CanonicalPath || !filepath.IsAbs(closure.WorkingDirectory) || filepath.Clean(closure.WorkingDirectory) != closure.WorkingDirectory {
		return ErrUnavailable
	}
	total := 0
	for _, argument := range closure.Arguments {
		if argument == "" || len(argument) > 16<<10 || strings.ContainsRune(argument, 0) {
			return ErrUnavailable
		}
		total += len(argument)
	}
	seen := make(map[string]bool, len(closure.Environment))
	for _, item := range closure.Environment {
		if item == "" || len(item) > 16<<10 || strings.ContainsRune(item, 0) {
			return ErrUnavailable
		}
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" || strings.Contains(name, "=") || seen[name] {
			return ErrUnavailable
		}
		seen[name] = true
		total += len(item)
	}
	if total > 48<<10 {
		return ErrUnavailable
	}
	return nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func validateObject(object ObjectV1, executable bool) error {
	if !filepath.IsAbs(object.CanonicalPath) || filepath.Clean(object.CanonicalPath) != object.CanonicalPath || object.Device == 0 || object.Inode == 0 || object.FileType != 0o100000 || object.Mode&0o170000 != object.FileType || object.Size <= 0 || object.LinkCount != 1 || !validDigest(object.RawSHA256) {
		return ErrUnavailable
	}
	if executable && (object.Mode&0o111 == 0 || object.Mode&0o6000 != 0) {
		return ErrUnavailable
	}
	return nil
}

func safeToken(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\/") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return value != "." && value != ".."
}

func safeRelative(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || filepath.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !safeToken(part) {
			return false
		}
	}
	return true
}

func safeRole(value string) bool {
	if !safeRelative(value) {
		return false
	}
	return strings.Contains(value, "/")
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func Seal(input SpecInput) (ClosureV1, error) {
	input = normalizeSpecInput(input)
	materialsDigest, err := DigestMaterials(input.LaunchMaterials)
	if err != nil {
		return ClosureV1{}, err
	}
	specDigest, err := DigestSpec(input)
	if err != nil {
		return ClosureV1{}, err
	}
	closure := ClosureV1{RuntimeExecutable: input.RuntimeExecutable, ClosureProfileID: input.ClosureProfileID, MaterialRoots: input.MaterialRoots, LaunchMaterials: input.LaunchMaterials, LaunchMaterialsDigest: materialsDigest, AgentLaunchSpecDigest: specDigest, Arguments: input.Arguments, Environment: input.Environment, WorkingDirectory: input.WorkingDirectory}
	if err := closure.Validate(); err != nil {
		return ClosureV1{}, fmt.Errorf("%w: invalid sealed closure", ErrUnavailable)
	}
	return closure, nil
}
