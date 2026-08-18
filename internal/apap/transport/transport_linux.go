//go:build linux

package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type Listener struct {
	fd       int
	path     string
	identity ObjectIdentity
	policy   PeerPolicy
}

type Conn struct {
	fd     int
	pidfd  int
	exefd  int
	policy PeerPolicy
	peer   authorityprovider.PeerIdentity
}

// ListenRoot creates a root-owned SOCK_SEQPACKET endpoint in a root-owned,
// non-writable directory. Existing filesystem objects are never replaced.
func ListenRoot(path string, policy PeerPolicy) (*Listener, error) {
	if err := validatePolicy(policy); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("%w: root ownership required", ErrPeerRejected)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: socket path", ErrPeerRejected)
	}
	parent, leaf := filepath.Dir(path), filepath.Base(path)
	if leaf == "." || leaf == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: socket path", ErrPeerRejected)
	}
	dirfd, err := openRootOwnedDirectory(parent)
	if err != nil {
		return nil, fmt.Errorf("%w: socket parent", ErrPeerRejected)
	}
	defer unix.Close(dirfd)
	var existing unix.Stat_t
	if err := unix.Fstatat(dirfd, leaf, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil || !errors.Is(err, unix.ENOENT) {
		return nil, fmt.Errorf("%w: socket leaf exists", ErrPeerRejected)
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unix.Close(fd)
		}
	}()
	if err = unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlink(path)
		}
	}()
	if err = unix.Fchmodat(dirfd, leaf, 0600, 0); err != nil {
		return nil, err
	}
	if err = unix.Fchownat(dirfd, leaf, 0, 0, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if err = unix.Listen(fd, 128); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstatat(dirfd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != 0 || st.Nlink != 1 {
		return nil, fmt.Errorf("%w: socket identity", ErrPeerRejected)
	}
	id := statIdentity(st, "")
	ok, cleanup = true, false
	return &Listener{fd: fd, path: path, identity: id, policy: policy}, nil
}

func (l *Listener) Accept() (*Conn, error) {
	fd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC)
	if err != nil {
		return nil, err
	}
	peer, pidfd, exefd, err := admitPeer(fd, l.policy)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Conn{fd: fd, pidfd: pidfd, exefd: exefd, policy: l.policy, peer: peer}, nil
}

// NewListenerForTesting is intentionally absent: tests exercise receive on a
// socketpair and cannot weaken production root/path admission.

func (l *Listener) Close() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	err := unix.Close(l.fd)
	l.fd = -1
	var st unix.Stat_t
	if unix.Lstat(l.path, &st) == nil && statIdentity(st, "") == l.identity {
		_ = unix.Unlink(l.path)
	}
	return err
}

func (c *Conn) Close() error {
	if c == nil || c.fd < 0 {
		return nil
	}
	err := unix.Close(c.fd)
	c.fd = -1
	if c.pidfd >= 0 {
		_ = unix.Close(c.pidfd)
		c.pidfd = -1
	}
	if c.exefd >= 0 {
		_ = unix.Close(c.exefd)
		c.exefd = -1
	}
	return err
}
func (c *Conn) Receive(op authorityprovider.Operation, expected []FDExpectation) (*Packet, error) {
	if err := verifyPeerHold(c.fd, c.pidfd, c.exefd, c.policy); err != nil {
		return nil, err
	}
	return receive(c.fd, c.policy, c.peer, op, expected)
}

func admitPeer(fd int, policy PeerPolicy) (authorityprovider.PeerIdentity, int, int, error) {
	if err := validatePolicy(policy); err != nil {
		return authorityprovider.PeerIdentity{}, -1, -1, err
	}
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred.Uid != policy.UID || cred.Gid != policy.GID || (policy.PID != 0 && cred.Pid != policy.PID) {
		return authorityprovider.PeerIdentity{}, -1, -1, fmt.Errorf("%w: credentials", ErrPeerRejected)
	}
	pidfd, err := unix.PidfdOpen(int(cred.Pid), 0)
	if err != nil {
		return authorityprovider.PeerIdentity{}, -1, -1, fmt.Errorf("%w: pid identity", ErrPeerRejected)
	}
	exe, err := unix.Open(fmt.Sprintf("/proc/%d/exe", cred.Pid), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(pidfd)
		return authorityprovider.PeerIdentity{}, -1, -1, fmt.Errorf("%w: executable", ErrPeerRejected)
	}
	identity, err := MeasureFD(exe)
	if err != nil || identity != policy.ExecutableIdentity {
		unix.Close(exe)
		unix.Close(pidfd)
		return authorityprovider.PeerIdentity{}, -1, -1, fmt.Errorf("%w: executable", ErrPeerRejected)
	}
	var pfd = []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	if n, _ := unix.Poll(pfd, 0); n != 0 {
		unix.Close(exe)
		unix.Close(pidfd)
		return authorityprovider.PeerIdentity{}, -1, -1, fmt.Errorf("%w: exited peer", ErrPeerRejected)
	}
	return authorityprovider.PeerIdentity{PrincipalDigest: policy.PrincipalDigest, Role: policy.Role}, pidfd, exe, nil
}

func verifyPeerHold(fd, pidfd, exefd int, policy PeerPolicy) error {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred.Uid != policy.UID || cred.Gid != policy.GID || cred.Pid != policy.PID {
		return fmt.Errorf("%w: credentials changed", ErrPeerRejected)
	}
	poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 0); err != nil || n != 0 {
		return fmt.Errorf("%w: peer lifetime", ErrPeerRejected)
	}
	identity, err := MeasureFD(exefd)
	if err != nil || identity != policy.ExecutableIdentity {
		return fmt.Errorf("%w: executable changed", ErrPeerRejected)
	}
	return nil
}

func openRootOwnedDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("%w: socket parent", ErrPeerRejected)
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var root unix.Stat_t
	if unix.Fstat(current, &root) != nil || root.Uid != 0 || root.Mode&0022 != 0 && root.Mode&unix.S_ISVTX == 0 {
		unix.Close(current)
		return -1, fmt.Errorf("%w: socket parent", ErrPeerRejected)
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(current)
		if openErr != nil {
			return -1, fmt.Errorf("%w: socket parent", ErrPeerRejected)
		}
		current = next
		var st unix.Stat_t
		if unix.Fstat(current, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0 {
			unix.Close(current)
			return -1, fmt.Errorf("%w: socket parent", ErrPeerRejected)
		}
	}
	return current, nil
}

func receive(fd int, policy PeerPolicy, peer authorityprovider.PeerIdentity, op authorityprovider.Operation, expected []FDExpectation) (*Packet, error) {
	if err := validatePolicy(policy); err != nil {
		return nil, err
	}
	if peer.Role != policy.Role || peer.PrincipalDigest != policy.PrincipalDigest {
		return nil, fmt.Errorf("%w: binding", ErrPeerRejected)
	}
	if err := validateExpectations(op, expected); err != nil {
		return nil, err
	}
	data := make([]byte, MaxPacketBytes+1)
	oob := make([]byte, unix.CmsgSpace((MaxFDs+1)*4))
	n, oobn, flags, _, err := unix.Recvmsg(fd, data, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, err
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 || n > MaxPacketBytes {
		return nil, fmt.Errorf("%w: size or truncation", ErrPacketRejected)
	}
	canonicalPayload, err := canonical.JSON(data[:n])
	if err != nil {
		return nil, fmt.Errorf("%w: json", ErrPacketRejected)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("%w: control", ErrFDRejected)
	}
	var fds []int
	defer func() {
		for _, got := range fds {
			_ = unix.Close(got)
		}
	}()
	for _, msg := range msgs {
		if msg.Header.Level != unix.SOL_SOCKET || msg.Header.Type != unix.SCM_RIGHTS {
			return nil, fmt.Errorf("%w: ancillary type", ErrFDRejected)
		}
		part, err := unix.ParseUnixRights(&msg)
		if err != nil {
			return nil, fmt.Errorf("%w: ancillary", ErrFDRejected)
		}
		fds = append(fds, part...)
	}
	if len(fds) != len(expected) || len(fds) > MaxFDs {
		return nil, fmt.Errorf("%w: table length", ErrFDRejected)
	}
	held := make([]*HeldFD, 0, len(fds))
	seen := make(map[[2]uint64]struct{}, len(fds))
	for i, got := range fds {
		flags, err := unix.FcntlInt(uintptr(got), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			return nil, fmt.Errorf("%w: descriptor flags", ErrFDRejected)
		}
		identity, err := MeasureFD(got)
		if err != nil {
			return nil, err
		}
		key := [2]uint64{identity.Device, identity.Inode}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate object", ErrFDRejected)
		}
		seen[key] = struct{}{}
		if identity != expected[i].Identity {
			return nil, fmt.Errorf("%w: identity or order", ErrFDRejected)
		}
		if err := validateFDForRole(identity, expected[i].Ref.Role); err != nil {
			return nil, err
		}
		held = append(held, &HeldFD{FD: got, Ref: expected[i].Ref, Identity: identity})
	}
	fds = nil
	return &Packet{Payload: canonicalPayload, Peer: peer, FDs: held}, nil
}

func validateFDForRole(identity ObjectIdentity, role authorityprovider.FDRole) error {
	kind := identity.Mode & unix.S_IFMT
	switch role {
	case authorityprovider.FDScratchRoot, authorityprovider.FDBusinessDenyRoot, authorityprovider.FDAuthorityRoot, authorityprovider.FDFenceRoot, authorityprovider.FDWorktree, authorityprovider.FDControlRoot:
		if kind != unix.S_IFDIR {
			return fmt.Errorf("%w: role kind", ErrFDRejected)
		}
	case authorityprovider.FDCandidateExecutable:
		if kind != unix.S_IFREG || identity.Mode&0111 == 0 {
			return fmt.Errorf("%w: executable kind", ErrFDRejected)
		}
	default:
		if kind != unix.S_IFREG && kind != unix.S_IFDIR {
			return fmt.Errorf("%w: role kind", ErrFDRejected)
		}
	}
	return nil
}

func MeasureFD(fd int) (ObjectIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return ObjectIdentity{}, fmt.Errorf("%w: stat", ErrFDRejected)
	}
	kind := st.Mode & unix.S_IFMT
	if kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return ObjectIdentity{}, fmt.Errorf("%w: object kind", ErrFDRejected)
	}
	if kind == unix.S_IFREG && st.Nlink != 1 {
		return ObjectIdentity{}, fmt.Errorf("%w: hard link", ErrFDRejected)
	}
	digest := ""
	if kind == unix.S_IFREG {
		var err error
		digest, err = digestFD(fd)
		if err != nil {
			return ObjectIdentity{}, fmt.Errorf("%w: content", ErrFDRejected)
		}
	}
	return statIdentity(st, digest), nil
}

func digestFD(fd int) (string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(dup), "held-object")
	if f == nil {
		_ = unix.Close(dup)
		return "", syscall.EBADF
	}
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(dup, &st); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(f, 0, st.Size)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func statIdentity(st unix.Stat_t, digest string) ObjectIdentity {
	return ObjectIdentity{Device: uint64(st.Dev), Inode: st.Ino, Mode: st.Mode, UID: st.Uid, GID: st.Gid, Size: st.Size, Links: uint64(st.Nlink), ContentSHA256: digest}
}

func Send(fd int, payload []byte, fds []int) error {
	if len(payload) == 0 || len(payload) > MaxPacketBytes || len(fds) > MaxFDs {
		return fmt.Errorf("%w: bounds", ErrPacketRejected)
	}
	if _, err := canonical.JSON(payload); err != nil {
		return fmt.Errorf("%w: json", ErrPacketRejected)
	}
	var rights []byte
	if len(fds) != 0 {
		rights = unix.UnixRights(fds...)
	}
	n, err := unix.SendmsgN(fd, payload, rights, nil, unix.MSG_NOSIGNAL)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return syscall.EIO
	}
	return nil
}

func closeFD(fd int) error { return unix.Close(fd) }
