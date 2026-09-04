//go:build darwin

package launchidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// LiveIdentity is the non-bearer identity copied from the current allocation
// provision receipt.  It intentionally has no path: the closure supplies the
// locator and this value supplies the durable allocation identity.
type LiveIdentity struct {
	Device    uint64
	Inode     uint64
	FileType  uint32
	Mode      uint32
	UID       uint32
	GID       uint32
	Size      int64
	LinkCount uint64
}

// AllocationLiveIdentity and LiveIdentityV1 are descriptive aliases for
// callers that name the source of the identity explicitly.
type AllocationLiveIdentity = LiveIdentity
type LiveIdentityV1 = LiveIdentity

func (identity LiveIdentity) validDirectory() bool {
	return identity.Device != 0 && identity.Inode != 0 && identity.FileType == unix.S_IFDIR &&
		identity.Mode&unix.S_IFMT == unix.S_IFDIR && identity.Mode&0o6000 == 0 &&
		identity.LinkCount != 0 && identity.Size >= 0
}

// Validate checks the directory identity shape copied from an allocation
// provision receipt.
func (identity LiveIdentity) Validate() error {
	if !identity.validDirectory() {
		return ErrUnavailable
	}
	return nil
}

// LiveIdentityFromObject converts an already observed directory object to the
// path-free allocation identity accepted by VerifyCurrentClosure.
func LiveIdentityFromObject(object ObjectV1) LiveIdentity {
	return LiveIdentity{Device: object.Device, Inode: object.Inode, FileType: object.FileType, Mode: object.Mode, UID: object.UID, GID: object.GID, Size: object.Size, LinkCount: object.LinkCount}
}

// CurrentClosureObservation is the closed result of VerifyCurrentClosure.
// It contains observations only; all descriptors opened by the verifier are
// closed before the result is returned.
type CurrentClosureObservation struct {
	Runtime               ObjectV1
	WorkingDirectory      ObjectV1
	MaterialRoots         []MaterialRootV1
	LaunchMaterials       []LaunchMaterialV1
	LaunchMaterialsDigest string
	AgentLaunchSpecDigest string
	// Pi0844IdentityDigest is the path-free static identity derived from the
	// observed runtime, entrypoint, roots, and complete material manifest. It
	// is empty only for the Native profile; it never includes the per-Run
	// AgentLaunchSpecDigest.
	Pi0844IdentityDigest string
}

// VerifyCurrentClosure is the Core-side, read-only source admission gate.
// It opens every current object with no-follow semantics, hashes regular
// files, enumerates each root through its held directory descriptor, compares
// the complete role/object set with the sealed closure, and closes all
// temporary descriptors before returning. It never creates, renames, writes,
// launches, or retains a descriptor for a later phase.
func VerifyCurrentClosure(closure ClosureV1, allocationLive LiveIdentity) (CurrentClosureObservation, error) {
	return verifyCurrentClosure(closure, allocationLive, exactDirectoryIdentity)
}

// VerifyCurrentClosureWithStableDirectoryIdentity verifies an existing
// worktree against the APFS-stable directory identity persisted by the RB1
// bind receipt. Directory size and link count are deliberately normalized in
// that receipt because APFS may change them without replacing the directory.
// All replacement-relevant fields remain exact, and the returned observation
// carries the current live size/link count for the mutation-adjacent
// supervisor source gate.
func VerifyCurrentClosureWithStableDirectoryIdentity(closure ClosureV1, stableDirectory LiveIdentity) (CurrentClosureObservation, error) {
	if stableDirectory.Size != 0 || stableDirectory.LinkCount != 1 {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	return verifyCurrentClosure(closure, stableDirectory, stableDirectoryIdentity)
}

type directoryIdentityMatcher func(current, expected LiveIdentity) bool

func verifyCurrentClosure(closure ClosureV1, allocationLive LiveIdentity, matches directoryIdentityMatcher) (CurrentClosureObservation, error) {
	if closure.Validate() != nil || allocationLive.Validate() != nil || matches == nil {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	runtimeFile, runtime, err := openObject(closure.RuntimeExecutable.CanonicalPath, unix.S_IFREG, true)
	if err != nil {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	defer runtimeFile.Close()
	if runtime != closure.RuntimeExecutable {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	workingFile, working, err := openObject(closure.WorkingDirectory, unix.S_IFDIR, false)
	if err != nil {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	defer workingFile.Close()
	if working.CanonicalPath != closure.WorkingDirectory || !matches(LiveIdentityFromObject(working), allocationLive) {
		return CurrentClosureObservation{}, ErrUnavailable
	}

	roots := make([]MaterialRootV1, 0, len(closure.MaterialRoots))
	for _, expected := range closure.MaterialRoots {
		rootFile, rootObject, openErr := openObject(expected.CanonicalPath, unix.S_IFDIR, false)
		if openErr != nil {
			return CurrentClosureObservation{}, ErrUnavailable
		}
		records, enumErr := enumerateCurrentRoot(rootFile, expected)
		_ = rootFile.Close()
		if enumErr != nil || rootObject != expected.Object || !sameCurrentPath(expected.CanonicalPath, expected.Object.CanonicalPath) || !sameMaterialRecords(records, closure.LaunchMaterials, expected.Name) {
			return CurrentClosureObservation{}, ErrUnavailable
		}
		// A second no-follow open closes the locator check after enumeration.
		pathFile, currentRoot, pathErr := openObject(expected.CanonicalPath, unix.S_IFDIR, false)
		if pathFile != nil {
			_ = pathFile.Close()
		}
		if pathErr != nil || currentRoot != expected.Object || len(records) != materialCountForRoot(closure.LaunchMaterials, expected.Name) {
			return CurrentClosureObservation{}, ErrUnavailable
		}
		roots = append(roots, expected)
	}

	materials := append([]LaunchMaterialV1(nil), closure.LaunchMaterials...)
	for index := range materials {
		// enumerateCurrentRoot compares each role and exact object to the
		// closure; this second pass makes the complete set check explicit and
		// keeps a changed material path from being silently omitted.
		if !safeRole(materials[index].Role) || materials[index].Object.CanonicalPath == "" {
			return CurrentClosureObservation{}, ErrUnavailable
		}
		materialFile, currentMaterial, materialErr := openObject(materials[index].Object.CanonicalPath, unix.S_IFREG, false)
		if materialFile != nil {
			_ = materialFile.Close()
		}
		if materialErr != nil || currentMaterial != materials[index].Object {
			return CurrentClosureObservation{}, ErrUnavailable
		}
	}
	materialsDigest, err := DigestMaterials(materials)
	if err != nil || materialsDigest != closure.LaunchMaterialsDigest {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	specDigest, err := DigestSpec(SpecInput{RuntimeExecutable: runtime, ClosureProfileID: closure.ClosureProfileID, MaterialRoots: roots, LaunchMaterials: materials, Arguments: closure.Arguments, Environment: closure.Environment, WorkingDirectory: closure.WorkingDirectory})
	if err != nil || specDigest != closure.AgentLaunchSpecDigest {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	currentRuntimeFile, currentRuntime, runtimeErr := openObject(closure.RuntimeExecutable.CanonicalPath, unix.S_IFREG, true)
	if currentRuntimeFile != nil {
		_ = currentRuntimeFile.Close()
	}
	currentWorkingFile, currentWorking, workingErr := openObject(closure.WorkingDirectory, unix.S_IFDIR, false)
	if currentWorkingFile != nil {
		_ = currentWorkingFile.Close()
	}
	if runtimeErr != nil || currentRuntime != closure.RuntimeExecutable || workingErr != nil || !matches(LiveIdentityFromObject(currentWorking), allocationLive) {
		return CurrentClosureObservation{}, ErrUnavailable
	}
	piIdentityDigest := ""
	if closure.ClosureProfileID == Pi0844DarwinARM64Profile {
		observedClosure := closure
		observedClosure.RuntimeExecutable = runtime
		observedClosure.MaterialRoots = append([]MaterialRootV1(nil), roots...)
		observedClosure.LaunchMaterials = append([]LaunchMaterialV1(nil), materials...)
		identity, identityErr := Pi0844IdentityFromClosure(observedClosure)
		if identityErr != nil {
			return CurrentClosureObservation{}, ErrUnavailable
		}
		piIdentityDigest = identity.IdentityDigest
	}
	return CurrentClosureObservation{
		Runtime: runtime, WorkingDirectory: currentWorking, MaterialRoots: roots,
		LaunchMaterials: materials, LaunchMaterialsDigest: materialsDigest,
		AgentLaunchSpecDigest: specDigest, Pi0844IdentityDigest: piIdentityDigest,
	}, nil
}

func exactDirectoryIdentity(left, right LiveIdentity) bool {
	return left.Device == right.Device && left.Inode == right.Inode && left.FileType == right.FileType && left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID && left.Size == right.Size && left.LinkCount == right.LinkCount
}

func stableDirectoryIdentity(current, expected LiveIdentity) bool {
	return current.validDirectory() && expected.validDirectory() && expected.Size == 0 && expected.LinkCount == 1 &&
		current.Device == expected.Device && current.Inode == expected.Inode && current.FileType == expected.FileType &&
		current.Mode == expected.Mode && current.UID == expected.UID && current.GID == expected.GID
}

func sameCurrentPath(left, right string) bool {
	return left != "" && left == right && filepath.IsAbs(left) && filepath.Clean(left) == left
}

func materialCountForRoot(materials []LaunchMaterialV1, name string) int {
	count := 0
	for _, material := range materials {
		root, _, ok := strings.Cut(material.Role, "/")
		if ok && root == name {
			count++
		}
	}
	return count
}

func sameMaterialRecords(observed, expected []LaunchMaterialV1, rootName string) bool {
	byRole := make(map[string]ObjectV1)
	for _, record := range observed {
		if _, exists := byRole[record.Role]; exists {
			return false
		}
		byRole[record.Role] = record.Object
	}
	want := 0
	for _, record := range expected {
		root, _, ok := strings.Cut(record.Role, "/")
		if !ok || root != rootName {
			continue
		}
		want++
		object, ok := byRole[record.Role]
		if !ok || object != record.Object {
			return false
		}
	}
	return want == len(byRole)
}

// enumerateCurrentRoot uses a duplicate of the held root FD and Openat for
// every descendant. The returned records are closed observations; no file
// descriptor escapes this function.
func enumerateCurrentRoot(root *os.File, expected MaterialRootV1) ([]LaunchMaterialV1, error) {
	if root == nil || expected.Name == "" || expected.CanonicalPath == "" {
		return nil, ErrUnavailable
	}
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, err
	}
	type pendingDirectory struct {
		file *os.File
		rel  string
	}
	queue := []pendingDirectory{{file: os.NewFile(uintptr(fd), "marshal-current-root"), rel: ""}}
	var records []LaunchMaterialV1
	closeAll := func() {
		for _, pending := range queue {
			if pending.file != nil {
				_ = pending.file.Close()
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
			return nil, ErrUnavailable
		}
		sort.Slice(entries, func(i, j int) bool { return strings.Compare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, entry := range entries {
			name := entry.Name()
			if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || !utf8.ValidString(name) {
				_ = current.file.Close()
				closeAll()
				return nil, ErrUnavailable
			}
			var stat unix.Stat_t
			if unix.Fstatat(int(current.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
				// The current directory descriptor is the no-follow parent for
				// this component; no path walk is used for authorization.
				_ = current.file.Close()
				closeAll()
				return nil, ErrUnavailable
			}
			rel := filepath.Join(current.rel, name)
			canonicalPath := filepath.Join(expected.CanonicalPath, rel)
			switch uint32(stat.Mode) & unix.S_IFMT {
			case unix.S_IFDIR:
				dirFD, openErr := unix.Openat(int(current.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
				if openErr != nil {
					_ = current.file.Close()
					closeAll()
					return nil, ErrUnavailable
				}
				queue = append(queue, pendingDirectory{file: os.NewFile(uintptr(dirFD), "marshal-current-directory"), rel: rel})
			case unix.S_IFREG:
				file, object, openErr := openObjectAt(current.file, name, canonicalPath, unix.S_IFREG, false)
				if openErr != nil {
					_ = current.file.Close()
					closeAll()
					return nil, ErrUnavailable
				}
				_ = file.Close()
				records = append(records, LaunchMaterialV1{Role: expected.Name + "/" + filepath.ToSlash(rel), Object: object})
			default:
				_ = current.file.Close()
				closeAll()
				return nil, ErrUnavailable
			}
		}
		_ = current.file.Close()
	}
	sort.Slice(records, func(i, j int) bool { return strings.Compare(records[i].Role, records[j].Role) < 0 })
	return records, nil
}

func openObjectAt(parent *os.File, relative, canonicalPath string, kind uint32, executable bool) (*os.File, ObjectV1, error) {
	if parent == nil || relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || !filepath.IsAbs(canonicalPath) || filepath.Clean(canonicalPath) != canonicalPath {
		return nil, ObjectV1{}, ErrUnavailable
	}
	fd, err := unix.Openat(int(parent.Fd()), relative, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, ObjectV1{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(canonicalPath))
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || uint32(before.Mode)&unix.S_IFMT != kind {
		_ = file.Close()
		return nil, ObjectV1{}, ErrUnavailable
	}
	object := ObjectV1{CanonicalPath: canonicalPath, Device: uint64(before.Dev), Inode: before.Ino, FileType: uint32(before.Mode) & unix.S_IFMT, Mode: uint32(before.Mode), UID: before.Uid, GID: before.Gid, Size: before.Size, LinkCount: uint64(before.Nlink)}
	if kind == unix.S_IFREG {
		if before.Nlink != 1 || executable && (before.Mode&0o111 == 0 || before.Mode&0o6000 != 0) {
			_ = file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
		digest, digestErr := hashObject(file)
		if digestErr != nil || unix.Fstat(fd, &after) != nil || !sameStatForContentStability(before, after) {
			_ = file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
		object.RawSHA256 = digest
	}
	return file, object, nil
}

func hashObject(file *os.File) (string, error) {
	if file == nil {
		return "", ErrUnavailable
	}
	// openObject already owns the hashing implementation. Calling it through
	// a tiny local helper keeps descriptor-relative objects on the same exact
	// before/hash/after path without retaining a second descriptor.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// sameStatForContentStability compares two Stat_t snapshots taken around a
// hash read. The digest read itself legitimately mutates only Access time
// (APFS relatime), which carries no mutation signal; everything else must
// stay byte-stable or the object was tampered with mid-read.
func sameStatForContentStability(before, after unix.Stat_t) bool {
	before.Atim = unix.Timespec{}
	after.Atim = unix.Timespec{}
	return before == after
}
