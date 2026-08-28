//go:build darwin

package processsupervisor

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"golang.org/x/sys/unix"
)

const (
	nonceFileName = "session.nonce"
	controlSocket = "control.sock"
	csOpsCDHash   = 5
	maxPathBytes  = 4096
)

// RunInherited is the only hidden CLI entry. The descriptor type separates a
// Core-created supervisor bootstrap socket from the fixed-image launch child;
// no mode, path, nonce, authority, or credential is accepted through argv or
// environment.
func RunInherited(ctx context.Context) error {
	if len(os.Environ()) != 0 {
		return ErrInvalid
	}
	kind, err := inheritedInvocationKind()
	if err != nil {
		return err
	}
	if kind == "child" {
		return runLaunchChild()
	}
	return runSupervisor(ctx)
}

func runSupervisor(ctx context.Context) error {
	bootstrapFile := os.NewFile(SupervisorBootstrapFD, "marshal-supervisor-bootstrap")
	controlDirectory := os.NewFile(SupervisorControlDirFD, "marshal-supervisor-control-directory")
	if bootstrapFile == nil || controlDirectory == nil {
		return ErrInvalid
	}
	defer bootstrapFile.Close()
	defer controlDirectory.Close()
	bootstrapConnection, err := net.FileConn(bootstrapFile)
	if err != nil {
		return ErrInvalid
	}
	defer bootstrapConnection.Close()
	unixConnection, ok := bootstrapConnection.(*net.UnixConn)
	if !ok {
		return ErrInvalid
	}
	reader := bufio.NewReaderSize(unixConnection, MaxWireFrameBytes+frameHeaderBytes+1)
	raw, err := readFrame(reader, MaxWireFrameBytes)
	if err != nil {
		return ErrInvalid
	}
	if rejectBootstrapExtra(reader, unixConnection) != nil {
		return ErrInvalid
	}
	var bootstrap BootstrapRequest
	if strictCanonicalDecode(raw, &bootstrap) != nil || bootstrap.validate() != nil {
		return ErrInvalid
	}
	observedCore, err := observePeer(unixConnection)
	if err != nil || !sameCoreIdentity(bootstrap.Core, observedCore) {
		return ErrConflict
	}
	_, directoryIdentity, err := observeControlDirectory(controlDirectory)
	if err != nil || directoryIdentity != bootstrap.ControlDirectoryIdentity {
		return ErrConflict
	}
	entries, err := readHeldDirectory(controlDirectory)
	if err != nil || len(entries) != 0 || revalidateControlDirectory(controlDirectory, directoryIdentity) != nil {
		return ErrConflict
	}
	if err := writeOpenatExclusive(controlDirectory, nonceFileName, []byte(bootstrap.SessionNonce), 0o600); err != nil {
		return ErrIntervention
	}
	journalFile, err := openatExclusive(controlDirectory, JournalFileName, 0o600)
	if err != nil {
		return ErrIntervention
	}
	if err := controlDirectory.Sync(); err != nil {
		_ = journalFile.Close()
		return ErrIntervention
	}
	journal, err := OpenJournal(journalFile)
	if err != nil {
		_ = journalFile.Close()
		return err
	}
	defer journal.Close()
	mechanics, err := NewPlatformMechanics(controlDirectory)
	if err != nil {
		return err
	}
	session, err := NewSession(bootstrap, journal, mechanics, nil)
	if err != nil {
		return err
	}
	if revalidateControlDirectory(controlDirectory, directoryIdentity) != nil {
		return ErrConflict
	}
	listener, err := listenUnixAt(controlDirectory, controlSocket)
	if err != nil {
		return ErrIntervention
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := unix.Fchmodat(int(controlDirectory.Fd()), controlSocket, 0o600, 0); err != nil || controlDirectory.Sync() != nil {
		return ErrIntervention
	}
	socketIdentity, err := observeControlSocket(controlDirectory)
	if err != nil {
		return err
	}
	supervisorIdentity, err := observeSelfIdentity()
	if err != nil {
		return err
	}
	var active atomic.Bool
	active.Store(true)
	incoming, acceptErrors := acceptConnections(ctx, listener, &active)
	if observeControlSocketExact(controlDirectory, socketIdentity) != nil {
		return ErrConflict
	}
	if err := writeFrame(unixConnection, handshake(session, supervisorIdentity, socketIdentity, reconnectResolution{}), MaxWireFrameBytes); err == nil {
		terminal, _ := serveConnection(unixConnection, reader, session)
		if terminal {
			return nil
		}
	}
	_ = unixConnection.Close()
	active.Store(false)
	if revalidateControlDirectory(controlDirectory, directoryIdentity) != nil {
		return ErrConflict
	}
	for {
		var connection *net.UnixConn
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-acceptErrors:
			if err != nil {
				return ErrIntervention
			}
			return ErrIntervention
		case connection = <-incoming:
			if connection == nil {
				return ErrIntervention
			}
		}
		if connection.SetDeadline(time.Now().Add(30*time.Second)) != nil {
			_ = connection.Close()
			active.Store(false)
			continue
		}
		observed, observeErr := observePeer(connection)
		reconnectReader := bufio.NewReaderSize(connection, MaxWireFrameBytes+frameHeaderBytes+1)
		reconnectRaw, readErr := readFrame(reconnectReader, MaxWireFrameBytes)
		var reconnect ReconnectRequest
		admitErr := strictCanonicalDecode(reconnectRaw, &reconnect)
		preconditionErr := observeControlSocketExact(controlDirectory, socketIdentity)
		var resolution reconnectResolution
		var reconnectErr error
		if observeErr == nil && readErr == nil && admitErr == nil && preconditionErr == nil {
			resolution, reconnectErr = session.Reconnect(reconnect, observed)
		}
		if observeErr != nil || readErr != nil || admitErr != nil || preconditionErr != nil || reconnectErr != nil {
			_ = writeFrame(connection, HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "rejected", ReasonCode: ErrConflict.ReasonCode}, MaxWireFrameBytes)
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if connection.SetDeadline(time.Time{}) != nil {
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if err := writeFrame(connection, handshake(session, supervisorIdentity, socketIdentity, resolution), MaxWireFrameBytes); err != nil {
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if state := session.State(); state == string(sessionClosed) || state == string(sessionAborted) {
			_ = connection.Close()
			return nil
		}
		terminal, serveErr := serveConnection(connection, reconnectReader, session)
		_ = connection.Close()
		active.Store(false)
		if serveErr != nil {
			continue
		}
		if terminal {
			return nil
		}
	}
}

func readHeldDirectory(directory *os.File) ([]string, error) {
	if directory == nil {
		return nil, ErrInvalid
	}
	// Dup would share the caller-controlled directory stream offset and could
	// hide pre-existing entries. Reopen "." descriptor-relatively to obtain an
	// independent stream, then prove it is still the held directory object.
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConflict
	}
	copy := os.NewFile(uintptr(fd), "marshal-supervisor-control-directory-scan")
	defer copy.Close()
	var heldStat, copyStat unix.Stat_t
	if unix.Fstat(int(directory.Fd()), &heldStat) != nil || unix.Fstat(fd, &copyStat) != nil || heldStat.Dev != copyStat.Dev || heldStat.Ino != copyStat.Ino {
		return nil, ErrConflict
	}
	return copy.Readdirnames(-1)
}

// Darwin has no bindat(2). The supervisor therefore binds the relative socket
// name while its cwd is temporarily the already-held control directory, then
// restores cwd through another held descriptor. No attacker-controlled path
// lookup participates in creating the rendezvous object.
func listenUnixAt(directory *os.File, name string) (*net.UnixListener, error) {
	if directory == nil || !validID(name) {
		return nil, ErrInvalid
	}
	original, err := os.Open(".")
	if err != nil {
		return nil, ErrConflict
	}
	defer original.Close()
	if err := unix.Fchdir(int(directory.Fd())); err != nil {
		return nil, ErrConflict
	}
	listener, listenErr := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	restoreErr := unix.Fchdir(int(original.Fd()))
	if restoreErr != nil {
		if listener != nil {
			_ = listener.Close()
		}
		return nil, ErrIntervention
	}
	if listenErr != nil {
		return nil, ErrIntervention
	}
	return listener, nil
}

// acceptConnections owns the stable rendezvous socket for the life of the
// supervisor. Exactly one Core connection can be designated active. A second
// connection is rejected immediately instead of waiting in the kernel accept
// queue where it could be mistaken for a future reconnect owner.
func acceptConnections(ctx context.Context, listener *net.UnixListener, active *atomic.Bool) (<-chan *net.UnixConn, <-chan error) {
	incoming := make(chan *net.UnixConn, 1)
	errors := make(chan error, 1)
	go func() {
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				select {
				case errors <- err:
				default:
				}
				return
			}
			if !active.CompareAndSwap(false, true) {
				_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
				_ = writeFrame(connection, HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "rejected", ReasonCode: "process-supervisor-core-already-connected"}, MaxWireFrameBytes)
				_ = connection.Close()
				continue
			}
			select {
			case incoming <- connection:
			case <-ctx.Done():
				_ = connection.Close()
				return
			}
		}
	}()
	return incoming, errors
}

func serveConnection(connection net.Conn, reader *bufio.Reader, session *Session) (bool, error) {
	for {
		raw, err := readFrame(reader, MaxWireFrameBytes)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		response := session.Handle(raw)
		if err := writeFrame(connection, response, MaxWireFrameBytes); err != nil {
			return false, err
		}
		state := session.State()
		if state == string(sessionClosed) || state == string(sessionAborted) {
			return true, nil
		}
	}
}

func handshake(session *Session, supervisor CoreIdentity, socket ControlSocketIdentity, resolution reconnectResolution) HandshakeResponse {
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	return HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: session.sessionID, SessionNonceDigest: session.nonceDigest, OwnerEpoch: session.ownerEpoch, CurrentAuthorityHead: session.authorityHead, CommandSequence: commandSequence, CommandHead: commandHead, JournalSequence: journalSequence, JournalHead: journalHead, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), SupervisorProcess: supervisor.Process, SupervisorBinary: supervisor.Binary, ControlSocket: socket, Reconciliation: resolution.State, ReplayedResponse: resolution.Response}
}

func rejectBootstrapExtra(reader *bufio.Reader, connection *net.UnixConn) error {
	if reader == nil || connection == nil || reader.Buffered() != 0 {
		return ErrInvalid
	}
	if err := connection.SetReadDeadline(time.Now()); err != nil {
		return ErrInvalid
	}
	_, err := reader.Peek(1)
	_ = connection.SetReadDeadline(time.Time{})
	if err == nil || reader.Buffered() != 0 {
		return ErrInvalid
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() || errors.Is(err, io.EOF) {
		return nil
	}
	return ErrInvalid
}

func observeControlSocket(directory *os.File) (ControlSocketIdentity, error) {
	if directory == nil {
		return ControlSocketIdentity{}, ErrInvalid
	}
	var stat unix.Stat_t
	if unix.Fstatat(int(directory.Fd()), controlSocket, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return ControlSocketIdentity{}, ErrConflict
	}
	identity := ControlSocketIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, FileType: "socket", UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink)}
	if identity.UID != uint32(os.Geteuid()) || identity.GID != uint32(os.Getegid()) || identity.validate() != nil {
		return ControlSocketIdentity{}, ErrConflict
	}
	return identity, nil
}

func observeControlSocketExact(directory *os.File, expected ControlSocketIdentity) error {
	observed, err := observeControlSocket(directory)
	if err != nil || observed != expected {
		return ErrConflict
	}
	return nil
}

func observePeer(connection *net.UnixConn) (CoreIdentity, error) {
	if connection == nil {
		return CoreIdentity{}, ErrInvalid
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return CoreIdentity{}, ErrConflict
	}
	var identity CoreIdentity
	var observationErr error
	err = raw.Control(func(fd uintptr) {
		pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if err != nil {
			observationErr = ErrConflict
			return
		}
		credential, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil || credential == nil || credential.Uid == 0 || credential.Ngroups < 1 {
			observationErr = ErrConflict
			return
		}
		process, err := observeAnyProcessIdentity(pid)
		if err != nil {
			observationErr = err
			return
		}
		binary, err := observeBinaryIdentity(pid)
		if err != nil {
			observationErr = err
			return
		}
		identity = CoreIdentity{UID: credential.Uid, GID: credential.Groups[0], Process: process, Binary: binary}
	})
	if err != nil || observationErr != nil {
		return CoreIdentity{}, ErrConflict
	}
	self, err := observeSelfIdentity()
	if err != nil || !sameBinaryObject(identity.Binary, self.Binary) {
		return CoreIdentity{}, ErrConflict
	}
	return identity, nil
}

// ObserveFixedMarshalPeer returns an adjacent kernel observation for the peer
// on connection and also proves that it is the same fixed Marshal binary as
// this process. It is the client-side input to ValidateHandshakeBinding.
func ObserveFixedMarshalPeer(connection *net.UnixConn) (CoreIdentity, error) {
	return observePeer(connection)
}

func observeSelfIdentity() (CoreIdentity, error) {
	process, err := observeAnyProcessIdentity(os.Getpid())
	if err != nil {
		return CoreIdentity{}, err
	}
	binary, err := observeBinaryIdentity(os.Getpid())
	if err != nil {
		return CoreIdentity{}, err
	}
	return CoreIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Process: process, Binary: binary}, nil
}

func observeAnyProcessIdentity(pid int) (ProcessIdentity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return ProcessIdentity{}, ErrConflict
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return ProcessIdentity{}, ErrConflict
	}
	return ProcessIdentity{PID: pid, BirthSeconds: info.Proc.P_starttime.Sec, BirthMicroseconds: int64(info.Proc.P_starttime.Usec), SessionID: sid, ProcessGroupID: int(info.Eproc.Pgid)}, nil
}

func observeBinaryIdentity(pid int) (BinaryIdentity, error) {
	path, err := processExecutablePath(pid)
	if err != nil {
		return BinaryIdentity{}, err
	}
	file, spec, err := openObservedSpec("marshal", path, "regular")
	if err != nil {
		return BinaryIdentity{}, err
	}
	_ = file.Close()
	cdHash, err := processCDHash(pid)
	if err != nil {
		return BinaryIdentity{}, err
	}
	build := buildinfo.Current()
	identity := BinaryIdentity{CanonicalPath: path, Device: spec.Device, Inode: spec.Inode, FileType: spec.FileType, UID: spec.UID, GID: spec.GID, Mode: spec.Mode, LinkCount: spec.LinkCount, Size: spec.Size, RawSHA256: spec.RawSHA256, CDHash: cdHash, SourceHead: build.Commit, SelfProfile: build.SelfProfile}
	if identity.validate() != nil {
		return BinaryIdentity{}, ErrConflict
	}
	return identity, nil
}

func processCDHash(pid int) (string, error) {
	var digest [20]byte
	//lint:ignore SA1019 x/sys/unix exposes no csops wrapper; this fixed Darwin ABI call is required to bind the running process CDHash.
	_, _, errno := syscall.RawSyscall6(unix.SYS_CSOPS, uintptr(pid), csOpsCDHash, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), 0, 0)
	if errno != 0 {
		return "", ErrConflict
	}
	return hex.EncodeToString(digest[:]), nil
}

func observeControlDirectory(file *os.File) (string, ControlDirectoryIdentity, error) {
	if file == nil {
		return "", ControlDirectoryIdentity{}, ErrInvalid
	}
	path, err := descriptorPath(int(file.Fd()))
	if err != nil || !absoluteClean(path) {
		return "", ControlDirectoryIdentity{}, ErrConflict
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || uint32(stat.Mode)&0o077 != 0 {
		return "", ControlDirectoryIdentity{}, ErrConflict
	}
	identity := ControlDirectoryIdentity{CanonicalPath: path, Device: uint64(stat.Dev), Inode: stat.Ino, FileType: "directory", UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink)}
	return path, identity, identity.validate()
}

func revalidateControlDirectory(file *os.File, expected ControlDirectoryIdentity) error {
	path, observed, err := observeControlDirectory(file)
	if err != nil || path != expected.CanonicalPath || observed != expected {
		return ErrConflict
	}
	opened, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrConflict
	}
	defer unix.Close(opened)
	var stat unix.Stat_t
	if unix.Fstat(opened, &stat) != nil || uint64(stat.Dev) != expected.Device || stat.Ino != expected.Inode {
		return ErrConflict
	}
	return nil
}

func descriptorPath(fd int) (string, error) {
	buffer := make([]byte, maxPathBytes)
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buffer[0]))))
	if err != nil {
		return "", ErrConflict
	}
	end := 0
	for end < len(buffer) && buffer[end] != 0 {
		end++
	}
	if end == 0 || end == len(buffer) {
		return "", ErrConflict
	}
	return string(buffer[:end]), nil
}

func openatExclusive(directory *os.File, name string, mode uint32) (*os.File, error) {
	if directory == nil || !validID(name) {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, ErrIntervention
	}
	return os.NewFile(uintptr(fd), "marshal-supervisor-owned-object"), nil
}

func writeOpenatExclusive(directory *os.File, name string, data []byte, mode uint32) error {
	file, err := openatExclusive(directory, name, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	if validateJournalFile(file) != nil || writeAll(file, data) != nil || file.Sync() != nil || validateJournalFile(file) != nil {
		return ErrIntervention
	}
	return directory.Sync()
}
