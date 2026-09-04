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
	// A normal Go process may open signal.NotifyContext's wakeup pipe on
	// descriptors 3 and 4 after startup. Descriptor type alone would then
	// falsely classify the process as an inherited launch child. Supervisors
	// carry a directory at FD 4; launch children carry a complete descriptor
	// set (spec/ready/release/cwd/runtime/marshal) at FDs 3..8. Require those
	// stable companions before treating FD 3 as protocol input.
	var controlStat unix.Stat_t
	controlErr := unix.Fstat(int(SupervisorControlDirFD), &controlStat)
	switch uint32(stat.Mode) & unix.S_IFMT {
	case unix.S_IFSOCK:
		if controlErr != nil || uint32(controlStat.Mode)&unix.S_IFMT != unix.S_IFDIR {
			return "", ErrInvalid
		}
		return "supervisor", nil
	case unix.S_IFIFO:
		if controlErr != nil || uint32(controlStat.Mode)&unix.S_IFMT != unix.S_IFIFO {
			return "", ErrInvalid
		}
		for fd, kind := range map[uintptr]uint32{
			5: unix.S_IFIFO, 6: unix.S_IFDIR, 7: unix.S_IFREG, 8: unix.S_IFREG,
		} {
			var childStat unix.Stat_t
			if unix.Fstat(int(fd), &childStat) != nil || uint32(childStat.Mode)&unix.S_IFMT != kind {
				return "", ErrInvalid
			}
		}
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
	invocation, err := decodeChildInvocation(raw)
	for index := range raw {
		raw[index] = 0
	}
	spec := invocation.spec
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
	if verifyHeldObject(runtime, spec.Runtime.Object) != nil {
		return childReject("runtime-identity-conflict", ErrConflict)
	}
	if verifyHeldObject(working, spec.WorkingDirectory.Object) != nil {
		return childReject("working-identity-conflict", ErrConflict)
	}
	if verifyHeldObject(marshal, spec.Marshal.Object) != nil {
		return childReject("marshal-descriptor-conflict", ErrConflict)
	}
	if verifyCurrentMarshal(spec.Marshal.Object) != nil {
		return childReject("marshal-self-conflict", ErrConflict)
	}
	if verifyParentMarshal(spec.ParentPID, spec.Marshal.Object) != nil {
		return childReject("marshal-parent-conflict", ErrConflict)
	}
	for index, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		if verifyHeldObject(closure[index], object.Object) != nil || verifyPathObject(object.Object) != nil {
			return childReject("closure-identity-conflict", ErrConflict)
		}
	}
	if verifyPathObject(spec.Runtime.Object) != nil || verifyPathObject(spec.WorkingDirectory.Object) != nil || unix.Fchdir(int(working.Fd())) != nil {
		return childReject("source-identity-conflict", ErrConflict)
	}
	if count, err := readyFile.Write([]byte{childReadyByte}); err != nil || count != 1 || readyFile.Close() != nil {
		return childReject("ready-write-invalid", ErrInvalid)
	}
	var release [2]byte
	count, err := io.ReadFull(releaseFile, release[:1])
	if err != nil || count != 1 || release[0] != childReleaseByte {
		return childReject("release-read-invalid", ErrInvalid)
	}
	count, err = releaseFile.Read(release[1:])
	if err != io.EOF || count != 0 {
		return childReject("release-frame-invalid", ErrInvalid)
	}
	if verifyHeldObject(runtime, spec.Runtime.Object) != nil || verifyHeldObject(working, spec.WorkingDirectory.Object) != nil || verifyHeldObject(marshal, spec.Marshal.Object) != nil || verifyCurrentMarshal(spec.Marshal.Object) != nil || verifyParentMarshal(spec.ParentPID, spec.Marshal.Object) != nil || verifyPathObject(spec.Runtime.Object) != nil || verifyPathObject(spec.WorkingDirectory.Object) != nil {
		return childReject("release-identity-conflict", ErrConflict)
	}
	for index, object := range append(append([]childObject(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		if verifyHeldObject(closure[index], object.Object) != nil || verifyPathObject(object.Object) != nil {
			return childReject("release-closure-conflict", ErrConflict)
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
	switch invocation.protocolRevision {
	case ProtocolRevision:
		if _, _, errno := syscall.RawSyscall6(syscall.SYS_PTRACE, uintptr(syscall.PT_TRACE_ME), 0, 0, 0, 0, 0); errno != 0 {
			return ErrIntervention
		}
		if err := syscall.Exec(spec.Runtime.Object.CanonicalPath, spec.Argv, spec.Environment); err != nil {
			return ErrIntervention
		}
	case protocolRevisionV2:
		if err := darwinSetexecStartSuspended(spec.Runtime.Object.CanonicalPath, spec.Argv, spec.Environment); err != nil {
			return ErrIntervention
		}
	default:
		return ErrInvalid
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
