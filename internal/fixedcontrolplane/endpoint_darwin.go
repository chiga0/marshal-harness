//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"golang.org/x/sys/unix"
)

const darwinSunPathLimit = 104

type objectIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	FileType  uint32 `json:"fileType"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	LinkCount uint64 `json:"linkCount"`
	Size      int64  `json:"size"`
}

type Endpoint struct {
	mu             sync.RWMutex
	authority      *productionruntime.FixedEndpointAuthority
	listener       *net.UnixListener
	tokenFile      *os.File
	token          [32]byte
	snapshot       productionruntime.FixedEndpointSnapshot
	server         processsupervisor.CoreIdentity
	socketName     string
	tokenName      string
	locator        string
	socket         objectIdentity
	tokenObject    objectIdentity
	slots          chan struct{}
	acceptStopped  bool
	closed         bool
	acceptPrepared func()
}

type AuthenticatedConnection struct {
	*net.UnixConn
	Binding RequestBinding
	Peer    processsupervisor.CoreIdentity
	recheck func(context.Context) error
	release func()
	once    sync.Once
}

func (connection *AuthenticatedConnection) Close() error {
	if connection == nil {
		return nil
	}
	err := connection.UnixConn.Close()
	connection.once.Do(connection.release)
	return err
}

// Recheck proves that the authenticated endpoint, current owner and fixed
// binary still match the handshake before or after an application operation.
func (connection *AuthenticatedConnection) Recheck(ctx context.Context) error {
	if connection == nil || ctx == nil || connection.recheck == nil {
		return ErrConflict
	}
	if err := connection.recheck(ctx); err != nil {
		return ErrConflict
	}
	return nil
}

func OpenEndpoint(ctx context.Context, authority *productionruntime.FixedEndpointAuthority) (*Endpoint, error) {
	if ctx == nil || authority == nil || authority.ControlDirectory() == nil {
		return nil, ErrInvalid
	}
	snapshot := authority.Snapshot()
	if validateSnapshot(snapshot) != nil || authority.Recheck(ctx) != nil {
		return nil, ErrInvalid
	}
	server, err := processsupervisor.ObserveCurrentCore(snapshot.FixedMarshalPath)
	if err != nil || server != expectedServer(snapshot) {
		return nil, ErrConflict
	}
	epoch := strconv.FormatUint(snapshot.Acquisition.OwnerEpoch, 36)
	endpoint := &Endpoint{authority: authority, snapshot: snapshot, server: server, socketName: "s-" + epoch, tokenName: "t-" + epoch, slots: make(chan struct{}, 64)}
	endpoint.locator = filepath.Join(snapshot.ControlPath, endpoint.socketName)
	if !filepath.IsAbs(endpoint.locator) || filepath.Clean(endpoint.locator) != endpoint.locator || len([]byte(endpoint.locator))+1 > darwinSunPathLimit || containsNUL(endpoint.locator) {
		return nil, ErrUnavailable
	}
	failed := true
	defer func() {
		if failed {
			_ = endpoint.abortSetup()
		}
	}()
	err = authority.WithControlMutation(ctx, func(control *os.File) error {
		return endpoint.publish(control)
	})
	if err != nil {
		return nil, err
	}
	if endpoint.recheck(ctx) != nil {
		return nil, ErrConflict
	}
	failed = false
	return endpoint, nil
}

func (endpoint *Endpoint) publish(control *os.File) error {
	if control == nil {
		return ErrInvalid
	}
	for _, name := range []string{endpoint.socketName, endpoint.tokenName} {
		absent, err := namedAbsent(control, name)
		if err != nil || !absent {
			return ErrConflict
		}
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.locator, Net: "unix"})
	if err != nil {
		return ErrUnavailable
	}
	listener.SetUnlinkOnClose(false)
	endpoint.listener = listener
	if err := unix.Fchmodat(int(control.Fd()), endpoint.socketName, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return ErrConflict
	}
	endpoint.socket, err = observeNamed(control, endpoint.socketName, unix.S_IFSOCK, 0o600, -1)
	if err != nil || listener.Addr().Network() != "unix" || listener.Addr().String() != endpoint.locator || unix.Fsync(int(control.Fd())) != nil {
		return ErrConflict
	}
	if _, err := io.ReadFull(rand.Reader, endpoint.token[:]); err != nil {
		return ErrUnavailable
	}
	fd, err := unix.Openat(int(control.Fd()), endpoint.tokenName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return ErrConflict
	}
	endpoint.tokenFile = os.NewFile(uintptr(fd), "marshal-fixed-endpoint-token")
	if endpoint.tokenFile == nil {
		_ = unix.Close(fd)
		return ErrUnavailable
	}
	if written, err := endpoint.tokenFile.Write(endpoint.token[:]); err != nil || written != len(endpoint.token) || endpoint.tokenFile.Sync() != nil {
		return ErrUnavailable
	}
	if _, err := endpoint.tokenFile.Seek(0, io.SeekStart); err != nil {
		return ErrUnavailable
	}
	endpoint.tokenObject, err = observeNamed(control, endpoint.tokenName, unix.S_IFREG, 0o600, int64(len(endpoint.token)))
	if err != nil || unix.Fsync(int(control.Fd())) != nil {
		return ErrConflict
	}
	return endpoint.recheckObjectsWithControl(control)
}

func (endpoint *Endpoint) Accept(ctx context.Context) (*AuthenticatedConnection, error) {
	if endpoint == nil || ctx == nil {
		return nil, ErrInvalid
	}
	endpoint.mu.Lock()
	if endpoint.closed || endpoint.acceptStopped || endpoint.listener == nil {
		endpoint.mu.Unlock()
		return nil, ErrUnavailable
	}
	listener := endpoint.listener
	if deadline, ok := ctx.Deadline(); ok {
		_ = listener.SetDeadline(deadline)
	} else {
		_ = listener.SetDeadline(time.Time{})
	}
	if endpoint.acceptPrepared != nil {
		endpoint.acceptPrepared()
	}
	endpoint.mu.Unlock()
	connection, err := listener.AcceptUnix()
	if err != nil {
		return nil, ErrUnavailable
	}
	select {
	case endpoint.slots <- struct{}{}:
	default:
		_ = connection.Close()
		return nil, ErrUnavailable
	}
	release := func() { <-endpoint.slots }
	peer, err := processsupervisor.ObserveFixedMarshalPeer(connection)
	if err != nil {
		_ = connection.Close()
		release()
		return nil, ErrConflict
	}
	authenticated, err := endpoint.authenticate(ctx, connection, peer, release)
	if err != nil {
		_ = connection.Close()
		release()
		return nil, err
	}
	return authenticated, nil
}

// StopAccept stops the accept loop without closing or unlinking the listener.
// A deadline wakes a blocked Accept while the authenticated endpoint objects
// stay published for already accepted requests to complete their authority
// rechecks during bounded drain.
func (endpoint *Endpoint) StopAccept() error {
	if endpoint == nil {
		return nil
	}
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if endpoint.closed || endpoint.acceptStopped {
		return nil
	}
	endpoint.acceptStopped = true
	if endpoint.listener == nil {
		return nil
	}
	return endpoint.listener.SetDeadline(time.Now())
}

func (endpoint *Endpoint) authenticate(ctx context.Context, connection *net.UnixConn, peer processsupervisor.CoreIdentity, release func()) (*AuthenticatedConnection, error) {
	if peer.Binary != endpoint.server.Binary || peer.UID != endpoint.server.UID || peer.GID != endpoint.server.GID || endpoint.recheck(ctx) != nil {
		return nil, ErrConflict
	}
	now := time.Now().UTC()
	var nonce [32]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, ErrUnavailable
	}
	serverDigest, err := identityDigest(endpoint.server)
	if err != nil {
		return nil, err
	}
	socketDigest, err := objectDigest(endpoint.socket)
	if err != nil {
		return nil, err
	}
	challenge := challengeFrame{SchemaVersion: "fixed-control-challenge/v1", ProtocolRevision: ProtocolRevision, Nonce: hex.EncodeToString(nonce[:]), ExpiresAt: now.Add(handshakeTimeout).Format(time.RFC3339Nano), OwnerEpoch: endpoint.snapshot.Acquisition.OwnerEpoch, OwnerFactDigest: endpoint.snapshot.OwnerFactDigest, OwnerAcquisitionDigest: endpoint.snapshot.OwnerAcquisitionDigest, RepositoryDigest: endpoint.snapshot.RepositoryDigest, AuthorityRootDigest: endpoint.snapshot.AuthorityRootDigest, ServerIdentityDigest: serverDigest, SocketIdentityDigest: socketDigest}
	challengeRaw, err := canonicalBytes(challenge)
	if err != nil {
		return nil, err
	}
	challengeDigest := canonical.DigestBytes(challengeRaw)
	_ = connection.SetDeadline(now.Add(handshakeTimeout))
	if writeFrame(connection, challengeRaw) != nil {
		return nil, ErrUnavailable
	}
	proofRaw, err := readFrame(connection)
	if err != nil {
		return nil, err
	}
	var proof proofFrame
	if decodeClosed(proofRaw, &proof) != nil || proof.SchemaVersion != "fixed-control-proof/v1" || proof.ProtocolRevision != ProtocolRevision || proof.ChallengeDigest != challengeDigest || proof.Binding.Validate(now) != nil || !exactHex(proof.Proof, sha256.Size) {
		return nil, ErrConflict
	}
	peerDigest, err := identityDigest(peer)
	if err != nil || proof.ClientIdentityDigest != peerDigest {
		return nil, ErrConflict
	}
	expected, err := proofDigest(endpoint.token[:], challenge, peerDigest, proof.Binding)
	if err != nil || !hmac.Equal([]byte(expected), []byte(proof.Proof)) || time.Now().UTC().After(now.Add(handshakeTimeout)) || endpoint.recheck(ctx) != nil {
		return nil, ErrConflict
	}
	accepted := acceptedFrame{SchemaVersion: "fixed-control-accepted/v1", ProtocolRevision: ProtocolRevision, ChallengeDigest: challengeDigest, ProofDigest: canonical.DigestBytes(proofRaw)}
	acceptedRaw, err := canonicalBytes(accepted)
	if err != nil || writeFrame(connection, acceptedRaw) != nil || endpoint.recheck(ctx) != nil {
		return nil, ErrConflict
	}
	_ = connection.SetDeadline(time.Time{})
	return &AuthenticatedConnection{UnixConn: connection, Binding: proof.Binding, Peer: peer, recheck: authenticatedPeerRecheck(connection, peer, endpoint.recheck), release: release}, nil
}

func Dial(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, binding RequestBinding) (*AuthenticatedConnection, error) {
	if ctx == nil || authority == nil || binding.Validate(time.Now().UTC()) != nil {
		return nil, ErrInvalid
	}
	if authority.Recheck(ctx) != nil {
		return nil, ErrConflict
	}
	control, snapshot, err := authority.OpenControlView()
	if err != nil {
		return nil, ErrConflict
	}
	defer control.Close()
	if validateSnapshot(snapshot) != nil {
		return nil, ErrInvalid
	}
	epoch := strconv.FormatUint(snapshot.Acquisition.OwnerEpoch, 36)
	socketName, tokenName := "s-"+epoch, "t-"+epoch
	locator := filepath.Join(snapshot.ControlPath, socketName)
	if len([]byte(locator))+1 > darwinSunPathLimit || containsNUL(locator) {
		return nil, ErrUnavailable
	}
	socket, err := observeNamed(control, socketName, unix.S_IFSOCK, 0o600, -1)
	if err != nil {
		return nil, err
	}
	tokenObject, err := observeNamed(control, tokenName, unix.S_IFREG, 0o600, 32)
	if err != nil {
		return nil, err
	}
	token, err := readNamedToken(control, tokenName, tokenObject)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "unix", locator)
	if err != nil {
		return nil, ErrUnavailable
	}
	connection, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, ErrUnavailable
	}
	fail := func(err error) (*AuthenticatedConnection, error) {
		_ = connection.Close()
		return nil, err
	}
	peer, err := processsupervisor.ObserveFixedMarshalPeer(connection)
	server := expectedServer(snapshot)
	if err != nil || peer != server {
		return fail(ErrConflict)
	}
	_ = connection.SetDeadline(time.Now().Add(handshakeTimeout))
	challengeRaw, err := readFrame(connection)
	if err != nil {
		return fail(err)
	}
	var challenge challengeFrame
	serverDigest, _ := identityDigest(server)
	socketDigest, _ := objectDigest(socket)
	expiresAt := time.Time{}
	if decodeClosed(challengeRaw, &challenge) == nil {
		expiresAt, _ = time.Parse(time.RFC3339Nano, challenge.ExpiresAt)
	}
	if challenge.SchemaVersion != "fixed-control-challenge/v1" || challenge.ProtocolRevision != ProtocolRevision || !exactHex(challenge.Nonce, 32) || !expiresAt.After(time.Now().UTC()) || challenge.OwnerEpoch != snapshot.Acquisition.OwnerEpoch || challenge.OwnerFactDigest != snapshot.OwnerFactDigest || challenge.OwnerAcquisitionDigest != snapshot.OwnerAcquisitionDigest || challenge.RepositoryDigest != snapshot.RepositoryDigest || challenge.AuthorityRootDigest != snapshot.AuthorityRootDigest || challenge.ServerIdentityDigest != serverDigest || challenge.SocketIdentityDigest != socketDigest {
		return fail(ErrConflict)
	}
	self, err := processsupervisor.ObserveCurrentCore(snapshot.FixedMarshalPath)
	if err != nil || self.Binary != server.Binary || self.UID != server.UID || self.GID != server.GID {
		return fail(ErrConflict)
	}
	clientDigest, err := identityDigest(self)
	if err != nil {
		return fail(err)
	}
	proof, err := proofDigest(token, challenge, clientDigest, binding)
	if err != nil {
		return fail(err)
	}
	proofValue := proofFrame{SchemaVersion: "fixed-control-proof/v1", ProtocolRevision: ProtocolRevision, ChallengeDigest: canonical.DigestBytes(challengeRaw), ClientIdentityDigest: clientDigest, Binding: binding, Proof: proof}
	proofRaw, err := canonicalBytes(proofValue)
	if err != nil || writeFrame(connection, proofRaw) != nil {
		return fail(ErrUnavailable)
	}
	acceptedRaw, err := readFrame(connection)
	var accepted acceptedFrame
	if err != nil || decodeClosed(acceptedRaw, &accepted) != nil || accepted.SchemaVersion != "fixed-control-accepted/v1" || accepted.ProtocolRevision != ProtocolRevision || accepted.ChallengeDigest != canonical.DigestBytes(challengeRaw) || accepted.ProofDigest != canonical.DigestBytes(proofRaw) {
		return fail(ErrConflict)
	}
	if current, err := observeNamed(control, socketName, unix.S_IFSOCK, 0o600, -1); err != nil || current != socket {
		return fail(ErrConflict)
	}
	if current, err := observeNamed(control, tokenName, unix.S_IFREG, 0o600, 32); err != nil || current != tokenObject {
		return fail(ErrConflict)
	}
	if authority.Recheck(ctx) != nil {
		return fail(ErrConflict)
	}
	_ = connection.SetDeadline(time.Time{})
	return &AuthenticatedConnection{UnixConn: connection, Binding: binding, Peer: peer, recheck: authenticatedPeerRecheck(connection, peer, authority.Recheck), release: func() {}}, nil
}

func authenticatedPeerRecheck(connection *net.UnixConn, expected processsupervisor.CoreIdentity, authority func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if connection == nil || ctx == nil || authority == nil || authority(ctx) != nil {
			return ErrConflict
		}
		observed, err := processsupervisor.ObserveFixedMarshalPeer(connection)
		if err != nil || observed != expected {
			return ErrConflict
		}
		return nil
	}
}

func (endpoint *Endpoint) recheck(ctx context.Context) error {
	if endpoint == nil {
		return ErrConflict
	}
	endpoint.mu.RLock()
	defer endpoint.mu.RUnlock()
	if endpoint.authority == nil || endpoint.listener == nil || endpoint.tokenFile == nil || endpoint.authority.Recheck(ctx) != nil || endpoint.listener.Addr().String() != endpoint.locator {
		return ErrConflict
	}
	server, err := processsupervisor.ObserveCurrentCore(endpoint.snapshot.FixedMarshalPath)
	if err != nil || server != endpoint.server {
		return ErrConflict
	}
	return endpoint.recheckObjects()
}

func (endpoint *Endpoint) recheckObjects() error {
	control := endpoint.authority.ControlDirectory()
	return endpoint.recheckObjectsWithControl(control)
}

func (endpoint *Endpoint) recheckObjectsWithControl(control *os.File) error {
	if control == nil {
		return ErrConflict
	}
	socket, err := observeNamed(control, endpoint.socketName, unix.S_IFSOCK, 0o600, -1)
	if err != nil || socket != endpoint.socket {
		return ErrConflict
	}
	tokenObject, err := observeNamed(control, endpoint.tokenName, unix.S_IFREG, 0o600, 32)
	if err != nil || tokenObject != endpoint.tokenObject {
		return ErrConflict
	}
	var held unix.Stat_t
	if unix.Fstat(int(endpoint.tokenFile.Fd()), &held) != nil || objectFromStat(held) != endpoint.tokenObject {
		return ErrConflict
	}
	var raw [33]byte
	n, err := unix.Pread(int(endpoint.tokenFile.Fd()), raw[:], 0)
	if err != nil && !errors.Is(err, io.EOF) || n != 32 || !hmac.Equal(raw[:32], endpoint.token[:]) {
		return ErrConflict
	}
	return nil
}

func (endpoint *Endpoint) Close() error {
	if endpoint == nil {
		return nil
	}
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if endpoint.closed {
		return nil
	}
	endpoint.closed = true
	var result error
	if endpoint.listener != nil {
		result = endpoint.listener.Close()
	}
	if endpoint.authority != nil {
		err := endpoint.authority.WithControlMutation(context.Background(), func(control *os.File) error {
			socket, socketErr := observeNamed(control, endpoint.socketName, unix.S_IFSOCK, 0o600, -1)
			tokenObject, tokenErr := observeNamed(control, endpoint.tokenName, unix.S_IFREG, 0o600, 32)
			if socketErr != nil || tokenErr != nil || socket != endpoint.socket || tokenObject != endpoint.tokenObject {
				return ErrConflict
			}
			if unix.Unlinkat(int(control.Fd()), endpoint.socketName, 0) != nil || unix.Unlinkat(int(control.Fd()), endpoint.tokenName, 0) != nil || unix.Fsync(int(control.Fd())) != nil {
				return ErrConflict
			}
			return nil
		})
		result = errors.Join(result, err)
	}
	if endpoint.tokenFile != nil {
		result = errors.Join(result, endpoint.tokenFile.Close())
		endpoint.tokenFile = nil
	}
	return result
}

func (endpoint *Endpoint) abortSetup() error {
	if endpoint == nil {
		return nil
	}
	if endpoint.listener != nil {
		_ = endpoint.listener.Close()
	}
	if endpoint.tokenFile != nil {
		_ = endpoint.tokenFile.Close()
	}
	return nil
}

func writeFrame(writer io.Writer, raw []byte) error {
	if len(raw) == 0 || len(raw) > maxHandshakeFrame {
		return ErrInvalid
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if err := writeFull(writer, header[:]); err != nil {
		return ErrUnavailable
	}
	if err := writeFull(writer, raw); err != nil {
		return ErrUnavailable
	}
	return nil
}

func writeFull(writer io.Writer, raw []byte) error {
	for len(raw) > 0 {
		n, err := writer.Write(raw)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(raw) {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, ErrUnavailable
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxHandshakeFrame {
		return nil, ErrInvalid
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, ErrUnavailable
	}
	return raw, nil
}

func namedAbsent(directory *os.File, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, ErrConflict
	}
	return false, nil
}

func observeNamed(directory *os.File, name string, kind, permissions uint32, size int64) (objectIdentity, error) {
	if directory == nil || filepath.Base(name) != name || containsNUL(name) {
		return objectIdentity{}, ErrInvalid
	}
	var stat unix.Stat_t
	if unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return objectIdentity{}, ErrConflict
	}
	identity := objectFromStat(stat)
	if identity.FileType != kind || identity.Mode&0o777 != permissions || identity.UID != uint32(os.Geteuid()) || identity.GID != uint32(os.Getegid()) || identity.LinkCount != 1 || size >= 0 && identity.Size != size {
		return objectIdentity{}, ErrConflict
	}
	return identity, nil
}

func objectFromStat(stat unix.Stat_t) objectIdentity {
	return objectIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, FileType: uint32(stat.Mode & unix.S_IFMT), UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink), Size: stat.Size}
}

func objectDigest(identity objectIdentity) (string, error) {
	raw, err := canonicalBytes(identity)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}

func readNamedToken(directory *os.File, name string, expected objectIdentity) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, ErrConflict
	}
	file := os.NewFile(uintptr(fd), "marshal-fixed-endpoint-client-token")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnavailable
	}
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || objectFromStat(stat) != expected {
		return nil, ErrConflict
	}
	var raw [33]byte
	n, err := file.Read(raw[:])
	if err != nil && !errors.Is(err, io.EOF) || n != 32 {
		return nil, ErrConflict
	}
	return append([]byte(nil), raw[:32]...), nil
}

func containsNUL(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] == 0 {
			return true
		}
	}
	return false
}
