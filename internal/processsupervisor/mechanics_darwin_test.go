//go:build darwin

package processsupervisor

import (
	"bufio"
	"errors"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWaitFailureNeverBecomesTerminal(t *testing.T) {
	mechanics := &darwinMechanics{waitResult: &waitOutcome{waitFailed: true}}
	if err := mechanics.proveTerminalLocked(); !errors.Is(err, ErrIntervention) || mechanics.terminal {
		t.Fatalf("wait failure proof error=%v terminal=%v", err, mechanics.terminal)
	}
}

func TestExitedRootWithLiveProcessGroupNeverProvesAbsence(t *testing.T) {
	identity := ProcessIdentity{PID: os.Getpid() + 100000, ProcessGroupID: unix.Getpgrp()}
	if err := exactProcessGroupAbsent(identity, mustSessionID()); !errors.Is(err, ErrIntervention) {
		t.Fatalf("live group absence error=%v", err)
	}
}

func TestTrackedDescendantDriftIsIdentityConflict(t *testing.T) {
	process := &unix.KinfoProc{}
	process.Proc.P_pid = 4321
	process.Proc.P_starttime.Sec = 10
	process.Proc.P_starttime.Usec = 20
	process.Eproc.Ppid = 1234
	process.Eproc.Pgid = 1234
	prior := descendantObservation{PID: 4321, ParentPID: 1234, BirthSeconds: 10, BirthMicroseconds: 20}
	if err := validateTrackedDescendant(process, prior, 1234, 99, 99); err != nil {
		t.Fatalf("valid descendant error=%v", err)
	}
	process.Eproc.Ppid = 1
	if err := validateTrackedDescendant(process, prior, 1234, 99, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("reparent error=%v", err)
	}
	process.Eproc.Ppid = 1234
	process.Eproc.Pgid = 4321
	if err := validateTrackedDescendant(process, prior, 1234, 99, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("PGID detach error=%v", err)
	}
	process.Eproc.Pgid = 1234
	if err := validateTrackedDescendant(process, prior, 1234, 99, 100); !errors.Is(err, ErrConflict) {
		t.Fatalf("session detach error=%v", err)
	}
}

func TestProcessWorkingDirectoryDriftIsIdentityConflict(t *testing.T) {
	expected := HeldObjectSpec{Role: "working-directory", CanonicalPath: "/private/repository", Device: 1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: 0o040700, LinkCount: 2}
	observed := procVnodeInfoPath{}
	copy(observed.Path[:], expected.CanonicalPath)
	observed.Info.Stat = procVinfoStat{Dev: 1, Ino: 2, UID: 501, GID: 20, Mode: 0o040700}
	if err := validateProcessWorkingDirectory(observed, expected); err != nil {
		t.Fatalf("valid cwd error=%v", err)
	}
	observed.Path = [procVnodePathMaximum]byte{}
	copy(observed.Path[:], "/private/other")
	if err := validateProcessWorkingDirectory(observed, expected); !errors.Is(err, ErrConflict) {
		t.Fatalf("cwd drift error=%v", err)
	}
}

func TestRejectBootstrapPipelinedInput(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	leftFile := os.NewFile(uintptr(fds[0]), "bootstrap-left")
	rightFile := os.NewFile(uintptr(fds[1]), "bootstrap-right")
	defer leftFile.Close()
	defer rightFile.Close()
	leftConn, err := net.FileConn(leftFile)
	if err != nil {
		t.Fatal(err)
	}
	rightConn, err := net.FileConn(rightFile)
	if err != nil {
		t.Fatal(err)
	}
	defer leftConn.Close()
	defer rightConn.Close()
	left, ok := leftConn.(*net.UnixConn)
	if !ok {
		t.Fatal("socketpair did not produce UnixConn")
	}
	if _, err := rightConn.Write([]byte("extra")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(left)
	if _, err := reader.Peek(1); err != nil {
		t.Fatal(err)
	}
	if err := rejectBootstrapExtra(reader, left); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pipelined bootstrap error=%v", err)
	}
}
