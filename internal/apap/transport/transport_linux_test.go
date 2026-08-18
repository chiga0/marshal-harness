//go:build linux

package transport

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"golang.org/x/sys/unix"
)

func testDigest(b byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, 71)
	copy(result, "sha256:")
	for i := 7; i < len(result); i++ {
		result[i] = hex[b&15]
	}
	return string(result)
}

func socketPair(t *testing.T) [2]int {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) })
	return fds
}

func directPolicy() PeerPolicy {
	return PeerPolicy{PID: int32(os.Getpid()), ExecutableIdentity: ObjectIdentity{ContentSHA256: testDigest(1)}, PrincipalDigest: testDigest(2), Role: authorityprovider.PrincipalVerifierController}
}

func directPeer(policy PeerPolicy) authorityprovider.PeerIdentity {
	return authorityprovider.PeerIdentity{PrincipalDigest: policy.PrincipalDigest, Role: policy.Role}
}

func openMeasuredFile(t *testing.T, mode os.FileMode, content string) (*os.File, ObjectIdentity) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	identity, err := MeasureFD(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return file, identity
}

func openMeasuredDir(t *testing.T) (*os.File, ObjectIdentity) {
	t.Helper()
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	identity, err := MeasureFD(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return file, identity
}

func beginProbeTable(t *testing.T) ([]int, []FDExpectation) {
	t.Helper()
	exe, exeID := openMeasuredFile(t, 0700, "executable")
	scratch, scratchID := openMeasuredDir(t)
	deny, denyID := openMeasuredDir(t)
	return []int{int(exe.Fd()), int(scratch.Fd()), int(deny.Fd())}, []FDExpectation{
		{Ref: authorityprovider.FDRef{Role: authorityprovider.FDCandidateExecutable}, Identity: exeID},
		{Ref: authorityprovider.FDRef{Role: authorityprovider.FDScratchRoot}, Identity: scratchID},
		{Ref: authorityprovider.FDRef{Role: authorityprovider.FDBusinessDenyRoot, Index: 0}, Identity: denyID},
	}
}

func TestReceiveExactPacketAndFDTable(t *testing.T) {
	pair := socketPair(t)
	fds, expected := beginProbeTable(t)
	if err := Send(pair[0], []byte(`{"b":2,"a":1}`), fds); err != nil {
		t.Fatal(err)
	}
	policy := directPolicy()
	packet, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationBeginProbe, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	if string(packet.Payload) != `{"a":1,"b":2}` || len(packet.FDs) != 3 {
		t.Fatalf("unexpected packet: %s fds=%d", packet.Payload, len(packet.FDs))
	}
	for _, held := range packet.FDs {
		flags, err := unix.FcntlInt(uintptr(held.FD), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatal("received descriptor is not CLOEXEC")
		}
	}
}

func TestReceiveRejectsPacketBoundaryAttacks(t *testing.T) {
	policy := directPolicy()
	for name, payload := range map[string][]byte{
		"truncation": make([]byte, MaxPacketBytes+2),
		"coalesced":  []byte(`{} {}`),
		"trailing":   []byte("{}\n{}"),
	} {
		t.Run(name, func(t *testing.T) {
			pair := socketPair(t)
			if _, err := unix.SendmsgN(pair[0], payload, nil, nil, unix.MSG_NOSIGNAL); err != nil {
				t.Fatal(err)
			}
			if _, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationDescribe, nil); !errors.Is(err, ErrPacketRejected) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestReceiveRejectsFDCountOrderDuplicateAndCredentialRoles(t *testing.T) {
	policy := directPolicy()
	t.Run("reorder", func(t *testing.T) {
		pair := socketPair(t)
		fds, expected := beginProbeTable(t)
		fds[1], fds[2] = fds[2], fds[1]
		if err := Send(pair[0], []byte(`{}`), fds); err != nil {
			t.Fatal(err)
		}
		if _, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationBeginProbe, expected); !errors.Is(err, ErrFDRejected) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		pair := socketPair(t)
		fds, expected := beginProbeTable(t)
		fds[2] = fds[1]
		expected[2].Identity = expected[1].Identity
		if err := Send(pair[0], []byte(`{}`), fds); err != nil {
			t.Fatal(err)
		}
		if _, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationBeginProbe, expected); !errors.Is(err, ErrFDRejected) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("thirty-three", func(t *testing.T) {
		pair := socketPair(t)
		fds := make([]int, MaxFDs+1)
		for i := range fds {
			file, _ := openMeasuredFile(t, 0600, string(rune('a'+i)))
			fds[i] = int(file.Fd())
		}
		if err := Send(pair[0], []byte(`{}`), fds); !errors.Is(err, ErrPacketRejected) {
			t.Fatalf("got %v", err)
		}
		if _, err := unix.SendmsgN(pair[0], []byte(`{}`), unix.UnixRights(fds...), nil, unix.MSG_NOSIGNAL); err != nil {
			t.Fatal(err)
		}
		if _, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationDescribe, nil); !errors.Is(err, ErrFDRejected) {
			t.Fatalf("raw 33fd got %v", err)
		}
	})
	t.Run("credential role", func(t *testing.T) {
		if err := validateExpectations(authorityprovider.OperationDescribe, []FDExpectation{{Ref: authorityprovider.FDRef{Role: authorityprovider.FDCredentialRoot}}}); !errors.Is(err, ErrFDRejected) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestQueuedSCMRightsSurvivesSenderCloseAndFDReuse(t *testing.T) {
	pair := socketPair(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "original")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := MeasureFD(int(original.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	originalFD := int(original.Fd())
	if err := Send(pair[0], []byte(`{}`), []int{originalFD}); err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, _ := openMeasuredFile(t, 0600, "replacement")
	_ = replacement // the numeric descriptor may be reused; queued SCM_RIGHTS must still name original.
	policy := directPolicy()
	packet, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationStageBundleLeafBatch, []FDExpectation{{Ref: authorityprovider.FDRef{Role: authorityprovider.FDBundleLeaf}, Identity: identity}})
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	if packet.FDs[0].Identity.ContentSHA256 != identity.ContentSHA256 {
		t.Fatal("queued descriptor identity changed")
	}
}

func TestMeasureFDRejectsUnsafeObjectKindsAndHardlinks(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(dir, "hardlink")
	if err := os.Link(regular, hardlink); err != nil {
		t.Fatal(err)
	}
	file, _ := os.Open(regular)
	defer file.Close()
	if _, err := MeasureFD(int(file.Fd())); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("hardlink: %v", err)
	}

	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	symlinkFD, err := unix.Open(symlink, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(symlinkFD)
	if _, err := MeasureFD(symlinkFD); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("symlink: %v", err)
	}

	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	fifoFD, err := unix.Open(fifo, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fifoFD)
	if _, err := MeasureFD(fifoFD); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("fifo: %v", err)
	}

	deviceFD, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(deviceFD)
	if _, err := MeasureFD(deviceFD); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("device: %v", err)
	}

	pair := socketPair(t)
	if _, err := MeasureFD(pair[0]); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("socket: %v", err)
	}
}

func TestPathRenameSwapCannotSubstituteExpectedHeldObject(t *testing.T) {
	pair := socketPair(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "leaf")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	originalIdentity, err := MeasureFD(int(original.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substitute"), 0600); err != nil {
		t.Fatal(err)
	}
	substitute, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer substitute.Close()
	if err := Send(pair[0], []byte(`{}`), []int{int(substitute.Fd())}); err != nil {
		t.Fatal(err)
	}
	policy := directPolicy()
	if _, err := receive(pair[1], policy, directPeer(policy), authorityprovider.OperationStageBundleLeafBatch, []FDExpectation{{Ref: authorityprovider.FDRef{Role: authorityprovider.FDBundleLeaf}, Identity: originalIdentity}}); !errors.Is(err, ErrFDRejected) {
		t.Fatalf("path substitute accepted: %v", err)
	}
}

func currentPeerPolicy(t *testing.T) PeerPolicy {
	t.Helper()
	exe, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(exe)
	identity, err := MeasureFD(exe)
	if err != nil {
		t.Fatal(err)
	}
	return PeerPolicy{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), PID: int32(os.Getpid()), ExecutableIdentity: identity, PrincipalDigest: testDigest(3), Role: authorityprovider.PrincipalVerifierController}
}

func TestPeerAdmissionPinsCredentialPIDExecutableAndRejectsWorker(t *testing.T) {
	pair := socketPair(t)
	valid := currentPeerPolicy(t)
	peer, pidfd, exefd, err := admitPeer(pair[1], valid)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	defer unix.Close(exefd)
	if peer.Role != valid.Role || verifyPeerHold(pair[1], pidfd, exefd, valid) != nil {
		t.Fatal("valid peer did not remain pinned")
	}
	for name, mutate := range map[string]func(*PeerPolicy){
		"uid":    func(v *PeerPolicy) { v.UID++ },
		"gid":    func(v *PeerPolicy) { v.GID++ },
		"pid":    func(v *PeerPolicy) { v.PID++ },
		"exe":    func(v *PeerPolicy) { v.ExecutableIdentity.ContentSHA256 = testDigest(4) },
		"worker": func(v *PeerPolicy) { v.Role = authorityprovider.PrincipalWorker },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, heldPID, heldExe, err := admitPeer(pair[1], candidate); err == nil {
				_ = unix.Close(heldPID)
				_ = unix.Close(heldExe)
				t.Fatal("invalid peer admitted")
			}
		})
	}
}

func TestRootListenerRejectsSymlinkParentAndPreservesSwappedLeaf(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only production listener")
	}
	root := t.TempDir()
	policy := currentPeerPolicy(t)
	link := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenRoot(filepath.Join(link, "apap.sock"), policy); !errors.Is(err, ErrPeerRejected) {
		t.Fatalf("symlink parent: %v", err)
	}

	path := filepath.Join(root, "apap.sock")
	listener, err := ListenRoot(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(client)
	if err := unix.Connect(client, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := accepted.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("listener close removed a substituted path")
	}
}
