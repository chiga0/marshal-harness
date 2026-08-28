//go:build darwin

package processsupervisor

import (
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func inheritedInvocationKind() (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(SupervisorBootstrapFD), &stat); err != nil {
		return "", ErrInvalid
	}
	switch uint32(stat.Mode) & unix.S_IFMT {
	case unix.S_IFSOCK:
		return "supervisor", nil
	case unix.S_IFIFO:
		return "child", nil
	default:
		return "", ErrInvalid
	}
}

func runLaunchChild() error {
	if len(os.Environ()) != 0 || os.Getppid() <= 1 {
		return ErrInvalid
	}
	specFile := os.NewFile(childSpecFD, "marshal-supervisor-child-spec")
	readyFile := os.NewFile(childReadyFD, "marshal-supervisor-child-ready")
	releaseFile := os.NewFile(childReleaseFD, "marshal-supervisor-child-release")
	working := os.NewFile(childCwdFD, "marshal-supervisor-child-cwd")
	runtime := os.NewFile(childRuntimeFD, "marshal-supervisor-child-runtime")
	marshal := os.NewFile(childMarshalFD, "marshal-supervisor-child-marshal")
	if specFile == nil || readyFile == nil || releaseFile == nil || working == nil || runtime == nil || marshal == nil {
		return ErrInvalid
	}
	defer specFile.Close()
	defer readyFile.Close()
	defer releaseFile.Close()
	defer working.Close()
	defer runtime.Close()
	defer marshal.Close()
	raw, err := io.ReadAll(io.LimitReader(specFile, MaxWireFrameBytes+1))
	if err != nil || len(raw) > MaxWireFrameBytes {
		return ErrInvalid
	}
	spec, err := decodeChildSpec(raw)
	for index := range raw {
		raw[index] = 0
	}
	if err != nil || spec.ParentPID != os.Getppid() {
		return ErrInvalid
	}
	closure := make([]*os.File, 0, len(spec.MaterialRoots)+len(spec.LaunchMaterials))
	for _, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		file := os.NewFile(uintptr(object.FD), "marshal-supervisor-child-closure")
		if file == nil {
			return ErrInvalid
		}
		closure = append(closure, file)
		defer file.Close()
	}
	if verifyHeldObject(runtime, spec.Runtime.Object) != nil || verifyHeldObject(working, spec.WorkingDirectory.Object) != nil || verifyHeldObject(marshal, spec.Marshal.Object) != nil || verifyCurrentMarshal(spec.Marshal.Object) != nil || verifyParentMarshal(spec.ParentPID, spec.Marshal.Object) != nil {
		return ErrConflict
	}
	for index, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		if verifyHeldObject(closure[index], object.Object) != nil || verifyPathObject(object.Object) != nil {
			return ErrConflict
		}
	}
	if verifyPathObject(spec.Runtime.Object) != nil || verifyPathObject(spec.WorkingDirectory.Object) != nil || unix.Fchdir(int(working.Fd())) != nil {
		return ErrConflict
	}
	if count, err := readyFile.Write([]byte{childReadyByte}); err != nil || count != 1 || readyFile.Close() != nil {
		return ErrInvalid
	}
	var release [2]byte
	count, err := io.ReadFull(releaseFile, release[:1])
	if err != nil || count != 1 || release[0] != childReleaseByte {
		return ErrInvalid
	}
	count, err = releaseFile.Read(release[1:])
	if err != io.EOF || count != 0 {
		return ErrInvalid
	}
	if verifyHeldObject(runtime, spec.Runtime.Object) != nil || verifyHeldObject(working, spec.WorkingDirectory.Object) != nil || verifyHeldObject(marshal, spec.Marshal.Object) != nil || verifyCurrentMarshal(spec.Marshal.Object) != nil || verifyParentMarshal(spec.ParentPID, spec.Marshal.Object) != nil || verifyPathObject(spec.Runtime.Object) != nil || verifyPathObject(spec.WorkingDirectory.Object) != nil {
		return ErrConflict
	}
	for index, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		if verifyHeldObject(closure[index], object.Object) != nil || verifyPathObject(object.Object) != nil {
			return ErrConflict
		}
	}
	_ = specFile.Close()
	_ = releaseFile.Close()
	_ = working.Close()
	_ = marshal.Close()
	for _, file := range closure {
		_ = file.Close()
	}
	unix.CloseOnExec(int(childRuntimeFD))
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PTRACE, uintptr(syscall.PT_TRACE_ME), 0, 0, 0, 0, 0); errno != 0 {
		return ErrIntervention
	}
	if err := syscall.Exec(spec.Runtime.Object.CanonicalPath, spec.Argv, spec.Environment); err != nil {
		return ErrIntervention
	}
	return nil
}

func verifyCurrentMarshal(expected HeldObjectSpec) error {
	path, err := os.Executable()
	if err != nil {
		return ErrConflict
	}
	file, err := openHeldObject(HeldObjectSpec{Role: expected.Role, CanonicalPath: path, Device: expected.Device, Inode: expected.Inode, FileType: expected.FileType, UID: expected.UID, GID: expected.GID, Mode: expected.Mode, LinkCount: expected.LinkCount, Size: expected.Size, RawSHA256: expected.RawSHA256})
	if err != nil {
		return err
	}
	return file.Close()
}

func verifyParentMarshal(pid int, expected HeldObjectSpec) error {
	if pid <= 1 || pid != os.Getppid() {
		return ErrConflict
	}
	path, err := processExecutablePath(pid)
	if err != nil || path != expected.CanonicalPath {
		return ErrConflict
	}
	file, observed, err := openObservedSpec("marshal", path, "regular")
	if err != nil {
		return ErrConflict
	}
	defer file.Close()
	if observed != expected {
		return ErrConflict
	}
	return nil
}
