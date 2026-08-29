//go:build darwin

package processsupervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"golang.org/x/sys/unix"
)

// openSpawnObjects is the sole mutation-adjacent source gate. Locators from
// SpawnPayload are used only to find the expected root and object names; the
// authorization decision is made from fresh descriptor observations and a
// role-keyed exact set comparison.
func openSpawnObjects(payload SpawnPayload) ([]*os.File, []HeldObjectSpec, string, error) {
	if payload.SourceGateRevision != SourceGateRevisionV1 {
		return nil, nil, "", ErrConflict
	}
	expected := spawnObjects(payload)
	wantByRole := make(map[string]HeldObjectSpec, len(expected))
	for _, spec := range expected {
		if _, exists := wantByRole[spec.Role]; exists {
			return nil, nil, "", ErrConflict
		}
		wantByRole[spec.Role] = spec
	}
	filesByRole := make(map[string]*os.File, len(expected))
	opened := make([]*os.File, 0, len(expected))
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			closeFiles(opened...)
		}
	}()
	add := func(spec HeldObjectSpec, file *os.File) error {
		if file == nil || spec.Role == "" {
			if file != nil {
				_ = file.Close()
			}
			return ErrConflict
		}
		if _, exists := filesByRole[spec.Role]; exists {
			_ = file.Close()
			return ErrConflict
		}
		filesByRole[spec.Role] = file
		opened = append(opened, file)
		return nil
	}
	working, err := openHeldObject(payload.WorkingDirectory)
	if err != nil || add(payload.WorkingDirectory, working) != nil {
		return nil, nil, "", ErrConflict
	}
	if payload.AllocationLiveIdentity == nil || !payload.AllocationLiveIdentity.matches(payload.WorkingDirectory) {
		return nil, nil, "", ErrConflict
	}
	runtime, err := openHeldObject(payload.Runtime)
	if err != nil || add(payload.Runtime, runtime) != nil {
		return nil, nil, "", ErrConflict
	}

	for _, root := range payload.MaterialRoots {
		rootSpec := heldMaterialRoot(root)
		rootFile, openErr := openHeldObject(rootSpec)
		if openErr != nil || add(rootSpec, rootFile) != nil {
			return nil, nil, "", ErrConflict
		}
		materials, enumErr := enumerateSpawnRoot(rootFile, root)
		if enumErr != nil {
			return nil, nil, "", ErrConflict
		}
		for _, material := range materials {
			want, exists := wantByRole[material.spec.Role]
			if !exists || material.spec != want || add(material.spec, material.file) != nil {
				for _, remaining := range materials {
					if remaining.file != nil {
						_ = remaining.file.Close()
					}
				}
				return nil, nil, "", ErrConflict
			}
		}
	}
	// Confirm that every canonical locator still denotes the same object after
	// enumeration and before the caller can create a child. The descriptors
	// returned above remain the sole execution inputs after this point.
	for _, spec := range expected {
		if verifyPathObject(spec) != nil {
			return nil, nil, "", ErrConflict
		}
	}
	if len(filesByRole) != len(expected) {
		return nil, nil, "", ErrConflict
	}
	files := make([]*os.File, 0, len(expected))
	for _, spec := range expected {
		file := filesByRole[spec.Role]
		if file == nil {
			return nil, nil, "", ErrConflict
		}
		files = append(files, file)
	}
	digest, err := digestHeldSet(expected)
	if err != nil {
		return nil, nil, "", ErrConflict
	}
	closeOnFailure = false
	return files, expected, digest, nil
}

type enumeratedSpawnMaterial struct {
	spec HeldObjectSpec
	file *os.File
}

func enumerateSpawnRoot(root *os.File, expected launchidentity.MaterialRootV1) ([]enumeratedSpawnMaterial, error) {
	if root == nil || expected.Name == "" || !absoluteClean(expected.CanonicalPath) {
		return nil, ErrConflict
	}
	rootFD, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, ErrConflict
	}
	type pendingDirectory struct {
		file *os.File
		rel  string
	}
	queue := []pendingDirectory{{file: os.NewFile(uintptr(rootFD), "marshal-spawn-root"), rel: ""}}
	var materials []enumeratedSpawnMaterial
	closeAll := func() {
		for _, pending := range queue {
			if pending.file != nil {
				_ = pending.file.Close()
			}
		}
		for _, material := range materials {
			if material.file != nil {
				_ = material.file.Close()
			}
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		entries, readErr := current.file.ReadDir(-1)
		if readErr != nil {
			_ = current.file.Close()
			closeAll()
			return nil, ErrConflict
		}
		sort.Slice(entries, func(i, j int) bool { return strings.Compare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, entry := range entries {
			name := entry.Name()
			if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || !utf8.ValidString(name) {
				_ = current.file.Close()
				closeAll()
				return nil, ErrConflict
			}
			var stat unix.Stat_t
			if unix.Fstatat(int(current.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
				_ = current.file.Close()
				closeAll()
				return nil, ErrConflict
			}
			rel := filepath.Join(current.rel, name)
			canonicalPath := filepath.Join(expected.CanonicalPath, rel)
			switch uint32(stat.Mode) & unix.S_IFMT {
			case unix.S_IFDIR:
				dirFD, openErr := unix.Openat(int(current.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
				if openErr != nil {
					_ = current.file.Close()
					closeAll()
					return nil, ErrConflict
				}
				queue = append(queue, pendingDirectory{file: os.NewFile(uintptr(dirFD), "marshal-spawn-directory"), rel: rel})
			case unix.S_IFREG:
				file, spec, openErr := openObservedSpecAt(current.file, name, expected.Name+"/"+filepath.ToSlash(rel), canonicalPath, "regular")
				if openErr != nil {
					_ = current.file.Close()
					closeAll()
					return nil, ErrConflict
				}
				materials = append(materials, enumeratedSpawnMaterial{spec: spec, file: file})
			default:
				_ = current.file.Close()
				closeAll()
				return nil, ErrConflict
			}
		}
		_ = current.file.Close()
	}
	sort.Slice(materials, func(i, j int) bool { return strings.Compare(materials[i].spec.Role, materials[j].spec.Role) < 0 })
	return materials, nil
}

func openObservedSpecAt(parent *os.File, name, role, canonicalPath, kind string) (*os.File, HeldObjectSpec, error) {
	if parent == nil || name == "" || strings.Contains(name, "/") || !validMaterialRole(role) || kind != "regular" || !absoluteClean(canonicalPath) {
		return nil, HeldObjectSpec{}, ErrConflict
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, HeldObjectSpec{}, ErrConflict
	}
	file := os.NewFile(uintptr(fd), "marshal-spawn-material")
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || uint32(before.Mode)&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		_ = file.Close()
		return nil, HeldObjectSpec{}, ErrConflict
	}
	spec := HeldObjectSpec{Role: role, CanonicalPath: canonicalPath, Device: uint64(before.Dev), Inode: before.Ino, FileType: kind, UID: before.Uid, GID: before.Gid, Mode: uint32(before.Mode), LinkCount: uint64(before.Nlink), Size: before.Size}
	spec.RawSHA256, err = digestOpenFile(file)
	if err != nil || unix.Fstat(fd, &after) != nil || !sameStableFileStat(before, after) {
		_ = file.Close()
		return nil, HeldObjectSpec{}, ErrConflict
	}
	return file, spec, nil
}

type heldSetDigestRecord struct {
	Role      string `json:"role"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	FileType  string `json:"fileType"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	LinkCount uint64 `json:"linkCount"`
	Size      int64  `json:"size"`
	RawSHA256 string `json:"rawSHA256,omitempty"`
}

func digestHeldSet(specs []HeldObjectSpec) (string, error) {
	records := make([]heldSetDigestRecord, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if _, exists := seen[spec.Role]; exists {
			return "", ErrConflict
		}
		seen[spec.Role] = struct{}{}
		records = append(records, heldSetDigestRecord{Role: spec.Role, Device: spec.Device, Inode: spec.Inode, FileType: spec.FileType, UID: spec.UID, GID: spec.GID, Mode: spec.Mode, LinkCount: spec.LinkCount, Size: spec.Size, RawSHA256: spec.RawSHA256})
	}
	sort.Slice(records, func(i, j int) bool { return strings.Compare(records[i].Role, records[j].Role) < 0 })
	jsonRaw, err := json.Marshal(records)
	if err != nil {
		return "", ErrConflict
	}
	raw, err := canonical.JSON(jsonRaw)
	if err != nil {
		return "", ErrConflict
	}
	return canonical.DigestBytes(raw), nil
}
