//go:build darwin

package processsupervisor

import (
	"errors"
	"syscall"
	"testing"
	"unsafe"
)

func TestDarwinSetexecBarrierFlagsMatchSDK(t *testing.T) {
	if darwinPosixSpawnSetExec != 0x0040 || darwinPosixSpawnStartSuspended != 0x0080 || darwinSetexecBarrierFlags != 0x00c0 {
		t.Fatalf("unexpected Darwin SETEXEC barrier flags: %#x", darwinSetexecBarrierFlags)
	}
}

func TestDarwinSetexecBridgeIsLinked(t *testing.T) {
	if !darwinSetexecBridgeLinked() {
		t.Fatal("fixed Darwin image does not contain the complete libSystem bridge")
	}
}

func TestDarwinCStringVectorIsNullTerminatedAndRejectsNUL(t *testing.T) {
	vector, err := darwinCStringVector([]string{"/usr/bin/true", "arg"})
	if err != nil || len(vector) != 3 || vector[0] == nil || vector[1] == nil || vector[2] != nil {
		t.Fatalf("unexpected vector: len=%d err=%v", len(vector), err)
	}
	if _, err := darwinCStringVector([]string{"bad\x00value"}); err == nil {
		t.Fatal("embedded NUL accepted")
	}
}

func TestDarwinSetexecBridgeRejectsInvalidInvocationBeforeLibSystem(t *testing.T) {
	tests := []struct {
		path string
		argv []string
		env  []string
	}{
		{path: "relative", argv: []string{"relative"}},
		{path: "/usr/bin/../bin/true", argv: []string{"/usr/bin/../bin/true"}},
		{path: "/usr/bin/true"},
		{path: "/usr/bin/true\x00suffix", argv: []string{"/usr/bin/true\x00suffix"}},
		{path: "/usr/bin/true", argv: []string{"/usr/bin/true", "bad\x00arg"}},
		{path: "/usr/bin/true", argv: []string{"/usr/bin/true"}, env: []string{"BAD=one\x00two"}},
	}
	for _, test := range tests {
		if err := darwinSetexecStartSuspended(test.path, test.argv, test.env); !errors.Is(err, ErrInvalid) {
			t.Fatalf("path=%q argv=%q env=%q: got %v, want ErrInvalid", test.path, test.argv, test.env, err)
		}
	}
}

func TestDarwinSpawnBridgeErrorDoesNotExposeArguments(t *testing.T) {
	err := &darwinSpawnBridgeError{stage: "setexec", code: syscall.EACCES}
	if err.Error() != "darwin posix_spawn bridge failed at setexec" || !errors.Is(err, syscall.EACCES) {
		t.Fatalf("unexpected bridge error: %v", err)
	}
}

func TestDarwinSpawnBridgeFailsClosedByStage(t *testing.T) {
	tests := []struct {
		name         string
		initCode     syscall.Errno
		setFlagsCode syscall.Errno
		spawnCode    syscall.Errno
		destroyCode  syscall.Errno
		wantStage    string
		wantInit     int
		wantSetFlags int
		wantSpawn    int
		wantDestroy  int
	}{
		{name: "init", initCode: syscall.ENOMEM, wantStage: "attr-init", wantInit: 1},
		{name: "setflags", setFlagsCode: syscall.EINVAL, wantStage: "attr-setflags", wantInit: 1, wantSetFlags: 1, wantDestroy: 1},
		{name: "setflags cleanup", setFlagsCode: syscall.EINVAL, destroyCode: syscall.EBUSY, wantStage: "attr-destroy-after-setflags", wantInit: 1, wantSetFlags: 1, wantDestroy: 1},
		{name: "spawn", spawnCode: syscall.EACCES, wantStage: "setexec", wantInit: 1, wantSetFlags: 1, wantSpawn: 1, wantDestroy: 1},
		{name: "unexpected return", wantStage: "unexpected", wantInit: 1, wantSetFlags: 1, wantSpawn: 1, wantDestroy: 1},
		{name: "unexpected return cleanup", destroyCode: syscall.EIO, wantStage: "attr-destroy-after-return", wantInit: 1, wantSetFlags: 1, wantSpawn: 1, wantDestroy: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var initCalls, setFlagsCalls, spawnCalls, destroyCalls int
			bridge := darwinSpawnBridge{
				attrInit: func(unsafe.Pointer) syscall.Errno {
					initCalls++
					return test.initCode
				},
				attrSetFlags: func(_ unsafe.Pointer, flags uint16) syscall.Errno {
					setFlagsCalls++
					if flags != darwinSetexecBarrierFlags {
						t.Fatalf("flags = %#x, want %#x", flags, darwinSetexecBarrierFlags)
					}
					return test.setFlagsCode
				},
				spawn: func(_, _, _, _ unsafe.Pointer) syscall.Errno {
					spawnCalls++
					return test.spawnCode
				},
				attrDestroy: func(unsafe.Pointer) syscall.Errno {
					destroyCalls++
					return test.destroyCode
				},
			}
			err := bridge.setexecStartSuspended("/usr/bin/true", []string{"custom-argv-zero"}, []string{"A=B"})
			if test.wantStage == "unexpected" {
				if !errors.Is(err, errDarwinSetexecUnexpectedReturn) {
					t.Fatalf("err = %v, want unexpected-return error", err)
				}
			} else {
				var bridgeErr *darwinSpawnBridgeError
				if !errors.As(err, &bridgeErr) || bridgeErr.stage != test.wantStage {
					t.Fatalf("err = %v, want stage %q", err, test.wantStage)
				}
			}
			if initCalls != test.wantInit || setFlagsCalls != test.wantSetFlags || spawnCalls != test.wantSpawn || destroyCalls != test.wantDestroy {
				t.Fatalf("calls init=%d setflags=%d spawn=%d destroy=%d; want %d/%d/%d/%d", initCalls, setFlagsCalls, spawnCalls, destroyCalls, test.wantInit, test.wantSetFlags, test.wantSpawn, test.wantDestroy)
			}
		})
	}
}

func TestDarwinSpawnBridgeRejectsIncompleteSymbolSet(t *testing.T) {
	err := (darwinSpawnBridge{}).setexecStartSuspended("/usr/bin/true", []string{"true"}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
