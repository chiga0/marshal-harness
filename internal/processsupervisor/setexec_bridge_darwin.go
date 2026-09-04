//go:build darwin

package processsupervisor

import (
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// These values are part of Darwin's public spawn ABI (sys/spawn.h). Keep
// them local to this bridge so the production call site cannot accidentally
// compose a weaker flag set.
const (
	darwinPosixSpawnSetExec        = uint16(0x0040)
	darwinPosixSpawnStartSuspended = uint16(0x0080)
	darwinSetexecBarrierFlags      = darwinPosixSpawnSetExec | darwinPosixSpawnStartSuspended
)

var errDarwinSetexecUnexpectedReturn = errors.New("darwin setexec unexpectedly returned success")

type darwinSpawnBridge struct {
	attrInit     func(unsafe.Pointer) syscall.Errno
	attrSetFlags func(unsafe.Pointer, uint16) syscall.Errno
	attrDestroy  func(unsafe.Pointer) syscall.Errno
	spawn        func(unsafe.Pointer, unsafe.Pointer, unsafe.Pointer, unsafe.Pointer) syscall.Errno
}

var liveDarwinSpawnBridge = darwinSpawnBridge{
	attrInit:     darwinSpawnAttrInit,
	attrSetFlags: darwinSpawnAttrSetFlags,
	attrDestroy:  darwinSpawnAttrDestroy,
	spawn:        darwinPosixSpawn,
}

// darwinSetexecBridgeLinked keeps S1's dormant bridge in the fixed image and
// makes a missing libSystem symbol a typed availability failure. It does not
// call posix_spawn and does not enable the v2 producer.
func darwinSetexecBridgeLinked() bool {
	return darwinLibcPosixSpawnAddr != 0 &&
		darwinLibcPosixSpawnAttrInitAddr != 0 &&
		darwinLibcPosixSpawnAttrSetFlagsAddr != 0 &&
		darwinLibcPosixSpawnAttrDestroyAddr != 0
}

type darwinSpawnBridgeError struct {
	stage string
	code  syscall.Errno
}

func (err *darwinSpawnBridgeError) Error() string {
	return "darwin posix_spawn bridge failed at " + err.stage
}

func (err *darwinSpawnBridgeError) Unwrap() error {
	if err == nil || err.code == 0 {
		return nil
	}
	return err.code
}

// darwinSetexecStartSuspended is the dormant ADR 0079 S1 bridge. It is not
// wired to the v1 producer: activation requires the closed Supervisor v2
// protocol and journal introduced by S2. On success POSIX_SPAWN_SETEXEC
// replaces the calling inherited launch child, so this function never
// returns and the same PID enters the runtime image start-suspended.
//
// The bridge is compiled into the fixed Marshal image with CGO_ENABLED=0. It
// performs no PATH lookup and creates no helper executable.
func darwinSetexecStartSuspended(path string, argv, environment []string) error {
	return liveDarwinSpawnBridge.setexecStartSuspended(path, argv, environment)
}

func (bridge darwinSpawnBridge) setexecStartSuspended(path string, argv, environment []string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(argv) == 0 {
		return ErrInvalid
	}
	if bridge.attrInit == nil || bridge.attrSetFlags == nil || bridge.attrDestroy == nil || bridge.spawn == nil {
		return ErrUnavailable
	}
	pathPointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return ErrInvalid
	}
	argvPointers, err := darwinCStringVector(argv)
	if err != nil {
		return ErrInvalid
	}
	environmentPointers, err := darwinCStringVector(environment)
	if err != nil {
		return ErrInvalid
	}

	var attributes uintptr
	if code := bridge.attrInit(unsafe.Pointer(&attributes)); code != 0 {
		return &darwinSpawnBridgeError{stage: "attr-init", code: code}
	}
	destroy := true
	defer func() {
		if destroy {
			_ = bridge.attrDestroy(unsafe.Pointer(&attributes))
		}
	}()
	if code := bridge.attrSetFlags(unsafe.Pointer(&attributes), darwinSetexecBarrierFlags); code != 0 {
		if destroyCode := bridge.attrDestroy(unsafe.Pointer(&attributes)); destroyCode != 0 {
			destroy = false
			return &darwinSpawnBridgeError{stage: "attr-destroy-after-setflags", code: destroyCode}
		}
		destroy = false
		return &darwinSpawnBridgeError{stage: "attr-setflags", code: code}
	}

	// A successful SETEXEC does not return. Any return must destroy the opaque
	// attributes and fail closed before the caller can claim exec-stopped.
	code := bridge.spawn(
		unsafe.Pointer(pathPointer),
		unsafe.Pointer(&attributes),
		unsafe.Pointer(&argvPointers[0]),
		unsafe.Pointer(&environmentPointers[0]),
	)
	destroyCode := bridge.attrDestroy(unsafe.Pointer(&attributes))
	destroy = false
	runtime.KeepAlive(pathPointer)
	runtime.KeepAlive(argvPointers)
	runtime.KeepAlive(environmentPointers)
	if code != 0 {
		return &darwinSpawnBridgeError{stage: "setexec", code: code}
	}
	if destroyCode != 0 {
		return &darwinSpawnBridgeError{stage: "attr-destroy-after-return", code: destroyCode}
	}
	return errDarwinSetexecUnexpectedReturn
}

func darwinCStringVector(values []string) ([]*byte, error) {
	result := make([]*byte, 0, len(values)+1)
	for _, value := range values {
		pointer, err := syscall.BytePtrFromString(value)
		if err != nil {
			return nil, err
		}
		result = append(result, pointer)
	}
	return append(result, nil), nil
}

func darwinSpawnAttrInit(attributes unsafe.Pointer) syscall.Errno {
	result, _, callErr := darwinLibcCall3(darwinLibcPosixSpawnAttrInitAddr, uintptr(attributes), 0, 0)
	if callErr != 0 {
		return callErr
	}
	return syscall.Errno(result)
}

func darwinSpawnAttrSetFlags(attributes unsafe.Pointer, flags uint16) syscall.Errno {
	result, _, callErr := darwinLibcCall3(darwinLibcPosixSpawnAttrSetFlagsAddr, uintptr(attributes), uintptr(flags), 0)
	if callErr != 0 {
		return callErr
	}
	return syscall.Errno(result)
}

func darwinSpawnAttrDestroy(attributes unsafe.Pointer) syscall.Errno {
	result, _, callErr := darwinLibcCall3(darwinLibcPosixSpawnAttrDestroyAddr, uintptr(attributes), 0, 0)
	if callErr != 0 {
		return callErr
	}
	return syscall.Errno(result)
}

func darwinPosixSpawn(path, attributes, argv, environment unsafe.Pointer) syscall.Errno {
	result, _, callErr := darwinLibcCall6(
		darwinLibcPosixSpawnAddr,
		0,
		uintptr(path),
		0,
		uintptr(attributes),
		uintptr(argv),
		uintptr(environment),
	)
	if callErr != 0 {
		return callErr
	}
	return syscall.Errno(result)
}

var (
	darwinLibcPosixSpawnAddr             uintptr
	darwinLibcPosixSpawnAttrInitAddr     uintptr
	darwinLibcPosixSpawnAttrSetFlagsAddr uintptr
	darwinLibcPosixSpawnAttrDestroyAddr  uintptr
)

//go:cgo_import_dynamic darwin_libc_posix_spawn posix_spawn "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic darwin_libc_posix_spawnattr_init posix_spawnattr_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic darwin_libc_posix_spawnattr_setflags posix_spawnattr_setflags "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic darwin_libc_posix_spawnattr_destroy posix_spawnattr_destroy "/usr/lib/libSystem.B.dylib"

// Implemented by the Go runtime on Darwin. These are libc ABI calls, not raw
// kernel traps. Linkage mirrors golang.org/x/sys/unix's CGO-free Darwin path.
func darwinLibcCall3(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)
func darwinLibcCall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname darwinLibcCall3 syscall.syscall
//go:linkname darwinLibcCall6 syscall.syscall6
