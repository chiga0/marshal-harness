//go:build darwin

package launchidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const piEntrypointDigest = "sha256:840d1e8e689ed9e4937bcb00b9a810e02a8567d9afb10a47097f11ca93ea1521"

type HeldClosure struct {
	Closure   ClosureV1
	Runtime   *os.File
	Roots     []*os.File
	Materials []*os.File
}

func (held *HeldClosure) Close() {
	if held == nil {
		return
	}
	files := append([]*os.File{held.Runtime}, held.Roots...)
	files = append(files, held.Materials...)
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
	held.Runtime, held.Roots, held.Materials = nil, nil, nil
}

// OpenPi0844 builds the Core-owned two-root closure. It never follows a
// symlink and keeps every runtime/root/material descriptor live.
func OpenPi0844(runtimePath, entrypointPath string, arguments, environment []string, workingDirectory string) (*HeldClosure, error) {
	entrypointPath, err := filepath.Abs(entrypointPath)
	if err != nil {
		return nil, ErrUnavailable
	}
	packageRoot := filepath.Dir(filepath.Dir(filepath.Dir(entrypointPath)))
	roots := []struct {
		name, rel string
		count     int
		bytes     int64
	}{
		{"pi-bundle", "dist/bundle", 48, 7439808},
		{"photon-node", "node_modules/@silvia-odwyer/photon-node", 7, 2265687},
	}
	held := &HeldClosure{}
	fail := func(err error) (*HeldClosure, error) {
		held.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	runtimeFile, runtime, err := openObject(runtimePath, unix.S_IFREG, true)
	if err != nil {
		return fail(err)
	}
	held.Runtime = runtimeFile
	var rootRecords []MaterialRootV1
	var materials []LaunchMaterialV1
	materialFiles := make(map[string]*os.File)
	for _, declaration := range roots {
		rootPath := filepath.Join(packageRoot, filepath.FromSlash(declaration.rel))
		rootFile, rootObject, err := openObject(rootPath, unix.S_IFDIR, false)
		if err != nil {
			return fail(err)
		}
		held.Roots = append(held.Roots, rootFile)
		rootRecords = append(rootRecords, MaterialRootV1{Name: declaration.name, CanonicalPath: rootPath, PackageRelative: declaration.rel, Object: rootObject})
		paths, err := enumerateRegular(rootPath)
		if err != nil || len(paths) != declaration.count {
			return fail(fmt.Errorf("root %s count", declaration.name))
		}
		var total int64
		for _, path := range paths {
			file, object, err := openObject(path, unix.S_IFREG, false)
			if err != nil {
				return fail(err)
			}
			held.Materials = append(held.Materials, file)
			rel, err := filepath.Rel(rootPath, path)
			if err != nil {
				return fail(err)
			}
			role := declaration.name + "/" + filepath.ToSlash(rel)
			materials = append(materials, LaunchMaterialV1{Role: role, Object: object})
			materialFiles[role] = file
			total += object.Size
		}
		if total != declaration.bytes {
			return fail(fmt.Errorf("root %s bytes", declaration.name))
		}
	}
	sort.Slice(materials, func(i, j int) bool { return materials[i].Role < materials[j].Role })
	held.Materials = held.Materials[:0]
	for _, material := range materials {
		held.Materials = append(held.Materials, materialFiles[material.Role])
	}
	if len(materials) != 55 {
		return fail(errorsNew("material count"))
	}
	for _, material := range materials {
		if material.Role == "pi-bundle/cli.js" && (material.Object.Size != 710 || material.Object.RawSHA256 != piEntrypointDigest) {
			return fail(errorsNew("entrypoint identity"))
		}
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil || uint64(1+len(rootRecords)+len(materials)+CoreFDReserve) >= limit.Cur {
		return fail(errorsNew("fd budget"))
	}
	input := SpecInput{RuntimeExecutable: runtime, ClosureProfileID: Pi0844DarwinARM64Profile, MaterialRoots: rootRecords, LaunchMaterials: materials, Arguments: append([]string(nil), arguments...), Environment: append([]string(nil), environment...), WorkingDirectory: workingDirectory}
	closure, err := Seal(input)
	if err != nil {
		return fail(err)
	}
	held.Closure = closure
	return held, nil
}

func Reopen(closure ClosureV1) (*HeldClosure, error) {
	if err := closure.Validate(); err != nil {
		return nil, err
	}
	if closure.ClosureProfileID == Pi0844DarwinARM64Profile {
		entrypoint := ""
		for _, material := range closure.LaunchMaterials {
			if material.Role == "pi-bundle/cli.js" {
				entrypoint = material.Object.CanonicalPath
			}
		}
		rebuilt, err := OpenPi0844(closure.RuntimeExecutable.CanonicalPath, entrypoint, closure.Arguments, closure.Environment, closure.WorkingDirectory)
		if err != nil || !reflect.DeepEqual(rebuilt.Closure, closure) {
			if rebuilt != nil {
				rebuilt.Close()
			}
			return nil, ErrUnavailable
		}
		return rebuilt, nil
	}
	held := &HeldClosure{}
	fail := func(err error) (*HeldClosure, error) {
		held.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	file, object, err := openObject(closure.RuntimeExecutable.CanonicalPath, unix.S_IFREG, true)
	if err != nil || object != closure.RuntimeExecutable {
		return fail(errorsNew("runtime drift"))
	}
	held.Runtime = file
	for _, root := range closure.MaterialRoots {
		file, object, err := openObject(root.CanonicalPath, unix.S_IFDIR, false)
		if err != nil || object != root.Object {
			return fail(errorsNew("root drift"))
		}
		held.Roots = append(held.Roots, file)
	}
	for _, material := range closure.LaunchMaterials {
		file, object, err := openObject(material.Object.CanonicalPath, unix.S_IFREG, false)
		if err != nil || object != material.Object {
			return fail(errorsNew("material drift"))
		}
		held.Materials = append(held.Materials, file)
	}
	digest, err := DigestSpec(SpecInput{RuntimeExecutable: closure.RuntimeExecutable, ClosureProfileID: closure.ClosureProfileID, MaterialRoots: closure.MaterialRoots, LaunchMaterials: closure.LaunchMaterials, Arguments: closure.Arguments, Environment: closure.Environment, WorkingDirectory: closure.WorkingDirectory})
	if err != nil || digest != closure.AgentLaunchSpecDigest {
		return fail(errorsNew("spec drift"))
	}
	held.Closure = closure
	return held, nil
}

func enumerateRegular(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnavailable
		}
		if info.Mode().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Slice(paths, func(i, j int) bool {
		return strings.Compare(filepath.ToSlash(paths[i]), filepath.ToSlash(paths[j])) < 0
	})
	return paths, err
}

func openObject(path string, kind uint32, executable bool) (*os.File, ObjectV1, error) {
	real, err := filepath.Abs(path)
	if err != nil || filepath.Clean(real) != real {
		return nil, ObjectV1{}, ErrUnavailable
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW_ANY
	if kind == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(real, flags, 0)
	if err != nil {
		return nil, ObjectV1{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(real))
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || uint32(before.Mode)&unix.S_IFMT != kind {
		file.Close()
		return nil, ObjectV1{}, ErrUnavailable
	}
	object := ObjectV1{CanonicalPath: real, Device: uint64(before.Dev), Inode: before.Ino, FileType: uint32(before.Mode) & unix.S_IFMT, Mode: uint32(before.Mode), UID: before.Uid, GID: before.Gid, Size: before.Size, LinkCount: uint64(before.Nlink)}
	if kind == unix.S_IFREG {
		if before.Nlink != 1 || executable && (before.Mode&0o111 == 0 || before.Mode&0o6000 != 0) {
			file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
		h := sha256.New()
		copied, err := io.Copy(h, file)
		if err != nil || copied != before.Size {
			file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
		object.RawSHA256 = "sha256:" + hex.EncodeToString(h.Sum(nil))
		if unix.Fstat(fd, &after) != nil || before != after {
			file.Close()
			return nil, ObjectV1{}, ErrUnavailable
		}
	}
	return file, object, nil
}

func errorsNew(value string) error { return fmt.Errorf("%s", value) }
