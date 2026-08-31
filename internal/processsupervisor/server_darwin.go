//go:build darwin

package processsupervisor

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
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
	return runSupervisorLoop(ctx, bootstrapFile, controlDirectory, supervisorLoopOptions{})
}

// supervisorLoopOptions is an internal fault-injection seam for exercising the
// complete inherited bootstrap, listener, reconnect and wire loop. Production
// always supplies the zero value and therefore constructs platform mechanics.
// Tests may substitute mechanics or mutate a boundary only at the explicit
// post-replay point; they do not bypass admission, replay or wire emission.
type supervisorLoopOptions struct {
	mechanics             Mechanics
	configureSession      func(*Session)
	afterReconnectAttempt func(*Session, reconnectAttemptResult, sessionControlBoundary)
	reconnectReady        func()
	observePeer           func(*net.UnixConn) (CoreIdentity, error)
	observeSelf           func() (CoreIdentity, error)
}

func runSupervisorLoop(ctx context.Context, bootstrapFile, controlDirectory *os.File, options supervisorLoopOptions) error {
	if ctx == nil || bootstrapFile == nil || controlDirectory == nil {
		return ErrInvalid
	}
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
	peerObserver := options.observePeer
	if peerObserver == nil {
		peerObserver = observePeer
	}
	selfObserver := options.observeSelf
	if selfObserver == nil {
		selfObserver = observeSelfIdentity
	}
	observedCore, err := peerObserver(unixConnection)
	if err != nil || !sameCoreIdentity(bootstrap.Core, observedCore) {
		return ErrConflict
	}
	_, directoryIdentity, err := observeControlDirectory(controlDirectory)
	if err != nil || directoryIdentity != bootstrap.ControlDirectoryIdentity {
		return ErrConflict
	}
	if revalidateInitialControlDirectory(controlDirectory, directoryIdentity) != nil {
		return ErrConflict
	}
	nonceFile, err := writeHeldOpenatExclusive(controlDirectory, nonceFileName, []byte(bootstrap.SessionNonce), 0o600)
	if err != nil {
		return ErrIntervention
	}
	defer nonceFile.Close()
	journalFile, err := openatExclusive(controlDirectory, JournalFileName, 0o600)
	if err != nil {
		return ErrIntervention
	}
	if err := controlDirectory.Sync(); err != nil {
		_ = journalFile.Close()
		return ErrIntervention
	}
	nonceIdentity, nonceSize, err := observeControlFile(nonceFile)
	if err != nil || nonceSize != nonceBytes {
		_ = journalFile.Close()
		return ErrIntervention
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	if err != nil {
		_ = journalFile.Close()
		return ErrIntervention
	}
	controlFiles := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	heldFiles := &heldSessionControlFiles{nonce: nonceFile, journal: journalFile, identity: controlFiles}
	if controlFiles.validate() != nil || revalidateHeldSessionControlFiles(controlDirectory, heldFiles, controlFiles) != nil {
		_ = journalFile.Close()
		return ErrIntervention
	}
	journal, err := OpenJournal(journalFile)
	if err != nil {
		_ = journalFile.Close()
		return err
	}
	defer journal.Close()
	mechanics := options.mechanics
	if mechanics == nil {
		mechanics, err = NewPlatformMechanics(controlDirectory)
		if err != nil {
			return err
		}
	}
	session, err := NewSession(bootstrap, journal, mechanics, nil)
	if err != nil {
		return err
	}
	if options.configureSession != nil {
		options.configureSession(session)
	}
	if revalidateControlDirectoryEntries(controlDirectory, directoryIdentity, false, controlDirectorySetupFiles) != nil {
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
	// The bootstrap identity is an exact observation of the initially empty
	// directory. APFS legitimately changes a directory's link count as the
	// supervisor creates its owned objects, so freeze a fresh post-setup
	// snapshot for connection evidence and runtime boundary checks.
	_, finalDirectoryIdentity, err := observeControlDirectory(controlDirectory)
	if err != nil || !sameControlDirectoryObject(finalDirectoryIdentity, directoryIdentity) || revalidateControlDirectoryEntries(controlDirectory, finalDirectoryIdentity, false, controlDirectoryRuntimeBase) != nil {
		return ErrConflict
	}
	boundary := sessionControlBoundary{directory: controlDirectory, directoryIdentity: finalDirectoryIdentity, socket: socketIdentity, heldFiles: heldFiles, controlFiles: controlFiles}
	supervisorIdentity, err := selfObserver()
	if err != nil {
		return err
	}
	var active atomic.Bool
	active.Store(true)
	incoming, acceptErrors := acceptConnections(ctx, listener, &active)
	if boundary.revalidate(session.journal.Snapshot()) != nil {
		return ErrConflict
	}
	if err := writeFrame(unixConnection, handshake(session, supervisorIdentity, socketIdentity, controlFiles, reconnectResolution{}), MaxWireFrameBytes); err == nil {
		terminal, serveErr := serveConnection(unixConnection, reader, session, boundary)
		if errors.Is(serveErr, ErrConflict) {
			return serveErr
		}
		if serveErr == nil && terminal {
			if boundary.revalidate(session.journal.Snapshot()) != nil {
				return ErrConflict
			}
			return nil
		}
	}
	_ = unixConnection.Close()
	active.Store(false)
	if options.reconnectReady != nil {
		options.reconnectReady()
	}
	if revalidateControlDirectoryForSnapshot(controlDirectory, finalDirectoryIdentity, session.journal.Snapshot()) != nil {
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
		observed, observeErr := peerObserver(connection)
		reconnectReader := bufio.NewReaderSize(connection, MaxWireFrameBytes+frameHeaderBytes+1)
		reconnectRaw, readErr := readFrame(reconnectReader, MaxWireFrameBytes)
		// Attach is dispatched before reconnect admission so an Attach identity
		// conflict never mutates mechanics state. Its authenticated borrowed
		// transport may carry exactly one command from the closed continuation set.
		if readErr == nil && wireSchema(reconnectRaw) == AttachSchema {
			attachErr := serveAttach(connection, reconnectReader, session, boundary, supervisorIdentity, observed, reconnectRaw)
			_ = connection.Close()
			active.Store(false)
			if attachErr != nil {
				if errors.Is(attachErr, ErrConflict) {
					continue
				}
				return attachErr
			}
			if state := session.State(); state == string(sessionClosed) || state == string(sessionAborted) {
				return nil
			}
			continue
		}
		var reconnect reconnectRequest
		admitErr := strictCanonicalDecode(reconnectRaw, &reconnect)
		// A malformed peer must not mask an already-drifted control boundary.
		// Boundary conflict always wins and is a silent terminal intervention.
		if boundary.revalidate(session.journal.Snapshot()) != nil {
			session.intervene()
			_ = connection.Close()
			active.Store(false)
			return ErrConflict
		}
		if observeErr != nil || readErr != nil || admitErr != nil {
			_ = emitReconnectHandshake(connection, reconnectWireDecision{disposition: reconnectWireRejected}, session, supervisorIdentity, socketIdentity, controlFiles)
			_ = connection.Close()
			active.Store(false)
			continue
		}
		attempt := session.reconnectAttempt(reconnect, observed)
		if options.afterReconnectAttempt != nil {
			options.afterReconnectAttempt(session, attempt, boundary)
		}
		decision := decideReconnectWireAfterAttempt(session, boundary, attempt)
		if decision.disposition == reconnectWireSilentClose {
			_ = connection.Close()
			active.Store(false)
			return decision.err
		}
		if decision.disposition == reconnectWireRejected {
			_ = emitReconnectHandshake(connection, decision, session, supervisorIdentity, socketIdentity, controlFiles)
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if decision.disposition != reconnectWireAccepted {
			_ = connection.Close()
			active.Store(false)
			return ErrIntervention
		}
		if connection.SetDeadline(time.Time{}) != nil {
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if err := emitReconnectHandshake(connection, decision, session, supervisorIdentity, socketIdentity, controlFiles); err != nil {
			_ = connection.Close()
			active.Store(false)
			continue
		}
		if state := session.State(); state == string(sessionClosed) || state == string(sessionAborted) {
			_ = connection.Close()
			if boundary.revalidate(session.journal.Snapshot()) != nil {
				return ErrConflict
			}
			return nil
		}
		terminal, serveErr := serveConnection(connection, reconnectReader, session, boundary)
		_ = connection.Close()
		active.Store(false)
		if serveErr != nil {
			if errors.Is(serveErr, ErrConflict) {
				return serveErr
			}
			continue
		}
		if terminal {
			if boundary.revalidate(session.journal.Snapshot()) != nil {
				return ErrConflict
			}
			return nil
		}
	}
}

type reconnectWireDisposition uint8

const (
	reconnectWireRejected reconnectWireDisposition = iota
	reconnectWireAccepted
	reconnectWireSilentClose
)

type reconnectWireDecision struct {
	disposition reconnectWireDisposition
	resolution  reconnectResolution
	err         error
}

// decideReconnectWire is the single point that translates the internal
// reconnect phase into a wire disposition. Only a conflict proven to precede
// replay mechanics may emit a rejected handshake. Once mechanics may have
// run, every failure first rechecks the post-replay boundary and then forces a
// silent close, preserving any durable intent/receipt without exposing a
// response that Core could mistake for a side-effect-free admission failure.
func decideReconnectWireAfterAttempt(session *Session, boundary sessionControlBoundary, attempt reconnectAttemptResult) reconnectWireDecision {
	if session == nil {
		return reconnectWireDecision{disposition: reconnectWireSilentClose, err: ErrInvalid}
	}
	var postconditionErr error
	if attempt.disposition != reconnectRejectedBeforeMechanics {
		postconditionErr = boundary.revalidate(session.journal.Snapshot())
	}
	switch attempt.disposition {
	case reconnectRejectedBeforeMechanics:
		if attempt.err == nil {
			session.intervene()
			return reconnectWireDecision{disposition: reconnectWireSilentClose, err: ErrIntervention}
		}
		return reconnectWireDecision{disposition: reconnectWireRejected, err: attempt.err}
	case reconnectFailedAfterMechanics:
		session.intervene()
		return reconnectWireDecision{disposition: reconnectWireSilentClose, err: errors.Join(attempt.err, postconditionErr)}
	case reconnectResolvedWithoutMechanics, reconnectResolvedAfterMechanics:
		if attempt.err != nil || postconditionErr != nil {
			session.intervene()
			return reconnectWireDecision{disposition: reconnectWireSilentClose, err: errors.Join(attempt.err, postconditionErr)}
		}
		return reconnectWireDecision{disposition: reconnectWireAccepted, resolution: attempt.resolution}
	default:
		session.intervene()
		return reconnectWireDecision{disposition: reconnectWireSilentClose, err: ErrIntervention}
	}
}

// wireSchema peeks the schemaVersion envelope of one frame without strict
// canonical decoding, so an Attach frame can be routed before the reconnect
// decoder would reject it as an unknown shape.
func wireSchema(raw []byte) string {
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.SchemaVersion
}

// attachChildObserver is the narrow read-only capability serveAttach uses to
// authenticate the live child identity. It is intentionally separate from the
// Mechanics interface so Attach cannot be confused with a command path; only
// darwinMechanics (and test fixtures) implement it.
type attachChildObserver interface {
	attachChildIdentity() (ProcessIdentity, error)
}

func (mechanics *darwinMechanics) attachChildIdentity() (ProcessIdentity, error) {
	if mechanics == nil {
		return ProcessIdentity{}, ErrConflict
	}
	mechanics.mu.Lock()
	defer mechanics.mu.Unlock()
	if mechanics.command == nil || mechanics.process.validate() != nil || mechanics.closed {
		return ProcessIdentity{}, ErrConflict
	}
	return mechanics.process, nil
}

// serveAttach handles one read-only Attach frame on an accepted reconnect
// socket. It never sends a command, never enters the generic command loop, and
// never mutates session/mechanics state: it only authenticates the live
// Supervisor against the request authority, emits one observation response,
// then requires the peer to half-close without sending any command frame. Any
// identity drift or authenticated-peer transport failure (SetDeadline,
// writeFrame, SetReadDeadline, EOF) returns ErrConflict so the loop continues
// without intervening, consistent with reconnect/serveConnection; only
// control/journal integrity drift returns ErrIntervention to terminate.
func serveAttach(connection *net.UnixConn, reader *bufio.Reader, session *Session, boundary sessionControlBoundary, supervisor, observed CoreIdentity, raw []byte) error {
	if connection == nil || reader == nil || session == nil {
		return ErrInvalid
	}
	var request attachRequest
	if strictCanonicalDecode(raw, &request) != nil || request.validate() != nil || !sameCoreIdentity(request.Core, observed) {
		return ErrConflict
	}
	before, journalBefore, err := captureAttachControlSnapshot(boundary.directory, boundary.directoryIdentity, boundary.heldFiles)
	if err != nil || boundary.revalidate(journalBefore) != nil {
		return ErrIntervention
	}
	if validateAttachServerState(session, boundary, supervisor, request, journalBefore) != nil {
		return ErrConflict
	}
	response := attachResponse{
		SchemaVersion: AttachObservationSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-attached", RequestDigest: request.RequestDigest,
		Handshake: handshake(session, supervisor, boundary.socket, boundary.controlFiles, reconnectResolution{}), CurrentAcquisition: request.Authority.CurrentAcquisition,
		CurrentOwnerBoundFact: request.Authority.CurrentOwnerBoundFact, Child: request.Authority.Child, ChildObservationDigest: request.Authority.ChildObservationDigest,
		ObserverIdentity: attachObserverIdentityV1, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	response.ResponseDigest, err = response.detachedDigest()
	if err != nil || response.validate(request, supervisor) != nil {
		return ErrConflict
	}
	if connection.SetDeadline(time.Now().Add(handshakeTimeout)) != nil || writeFrame(connection, response, MaxWireFrameBytes) != nil {
		return ErrConflict
	}
	// Attach owns a deliberately narrow borrowed transport. After the
	// observation it accepts at most one command from the explicit continuation
	// set: bind-authority(owner-successor), Collect, Inspect, or Close. EOF alone
	// is the read-only Attach. The generic command loop is never entered.
	if connection.SetReadDeadline(time.Now().Add(handshakeTimeout)) != nil {
		return ErrConflict
	}
	frame, frameErr := readFrame(reader, MaxWireFrameBytes)
	if errors.Is(frameErr, io.EOF) {
		final, journalFinal, err := captureAttachControlSnapshot(boundary.directory, boundary.directoryIdentity, boundary.heldFiles)
		if err != nil || final != before {
			return ErrIntervention
		}
		if validateAttachServerState(session, boundary, supervisor, request, journalFinal) != nil {
			return ErrConflict
		}
		return nil
	}
	if frameErr != nil {
		return ErrConflict
	}
	var continuation Request
	if strictCanonicalDecode(frame, &continuation) != nil {
		return ErrConflict
	}
	continuationResponse := session.HandleAttachContinuation(frame)
	commandJournal := session.journal.Snapshot()
	if commandJournal.Sequence == journalBefore.Sequence {
		// Admission/decode failure appended nothing. Drop without a response so a
		// peer cannot mistake a rejection for a committed mechanics checkpoint.
		return ErrConflict
	}
	if commandJournal.pending != nil || commandJournal.Sequence != journalBefore.Sequence+2 {
		return ErrIntervention
	}
	if writeFrame(connection, continuationResponse, MaxWireFrameBytes) != nil {
		return ErrConflict
	}
	var unexpected [1]byte
	if count, readErr := connection.Read(unexpected[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		return ErrConflict
	}
	final, journalFinal, err := captureAttachControlSnapshot(boundary.directory, boundary.directoryIdentity, boundary.heldFiles)
	if err != nil || !sameAttachPostCommandBoundary(continuation.Command, final, before) {
		return ErrIntervention
	}
	if journalFinal.pending != nil || journalFinal.Sequence != journalBefore.Sequence+2 {
		return ErrIntervention
	}
	switch continuation.Command {
	case CommandBindAuthority:
		if journalFinal.currentAuthorityHead == journalBefore.currentAuthorityHead {
			return ErrIntervention
		}
	case CommandCollect, CommandInspect, CommandClose:
		if journalFinal.currentAuthorityHead != journalBefore.currentAuthorityHead {
			return ErrIntervention
		}
	default:
		return ErrConflict
	}
	return nil
}

// sameAttachControlBoundary compares the security-relevant control objects
// frozen by Attach while excluding journal size/digest, which a permitted
// bind-authority(owner-successor) rebind advances by exactly one intent and
// one receipt.
func sameAttachControlBoundary(left, right attachControlSnapshot) bool {
	return left.Directory == right.Directory && left.Socket == right.Socket && left.Files == right.Files && left.NonceSize == right.NonceSize && left.NonceDigest == right.NonceDigest
}

// sameAttachPostCommandBoundary retains the exact Attach boundary for every
// continuation except Collect. A successful Collect creates the bounded
// stdout.bin, stderr.bin and transcript.jcs entries, which makes APFS increase
// the directory LinkCount. Only that monotonic LinkCount change is admitted:
// the directory object and all other security-relevant control objects remain
// byte-for-byte equal. Transcript identities are validated separately before
// ResultIngress accepts the collected result.
func sameAttachPostCommandBoundary(command CommandName, after, before attachControlSnapshot) bool {
	if command != CommandCollect {
		return sameAttachControlBoundary(after, before)
	}
	if after.Directory.LinkCount < before.Directory.LinkCount {
		return false
	}
	afterDirectory := after.Directory
	afterDirectory.LinkCount = before.Directory.LinkCount
	after.Directory = afterDirectory
	return sameAttachControlBoundary(after, before)
}

// validateAttachServerState authenticates the live Supervisor session,
// boundary, peer, and child against the exact previous anchor carried by the
// request. Every field is re-derived from held state; the request grants none.
func validateAttachServerState(session *Session, boundary sessionControlBoundary, supervisor CoreIdentity, request attachRequest, journal JournalSnapshot) error {
	anchor := request.Authority.PreviousSupervisor
	if session == nil || request.SessionNonce == "" || canonical.DigestBytes([]byte(request.SessionNonce)) != session.nonceDigest ||
		!sameControlDirectoryObject(request.ControlDirectoryIdentity, boundary.directoryIdentity) || anchor.ControlSocket != boundary.socket || anchor.ControlFiles != boundary.controlFiles ||
		request.Authority.Supervisor != supervisor.Process || !sameBinaryObject(anchor.FixedBinary, supervisor.Binary) ||
		request.Core.UID != supervisor.UID || request.Core.GID != supervisor.GID || validateAttachJournalAnchor(journal, anchor) != nil {
		return ErrConflict
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionBound || session.sessionID != anchor.SessionID || session.nonceDigest != anchor.SessionNonceDigest || session.authority != anchor.Authority ||
		session.ownerEpoch != anchor.OwnerEpoch || session.authorityHead != anchor.CurrentAuthorityHead || session.commandSequence != anchor.CommandSequence ||
		session.commandHead != anchor.CommandHead || session.lastObservation != request.Authority.ChildObservationDigest {
		return ErrConflict
	}
	observer, ok := session.mechanics.(attachChildObserver)
	if !ok {
		return ErrConflict
	}
	child, err := observer.attachChildIdentity()
	if err != nil || child != request.Authority.Child {
		return ErrConflict
	}
	return nil
}

func emitReconnectHandshake(connection net.Conn, decision reconnectWireDecision, session *Session, supervisor CoreIdentity, socket ControlSocketIdentity, controlFiles SessionControlFiles) error {
	if connection == nil || session == nil {
		return ErrInvalid
	}
	switch decision.disposition {
	case reconnectWireRejected:
		return writeFrame(connection, HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "rejected", ReasonCode: ErrConflict.ReasonCode}, MaxWireFrameBytes)
	case reconnectWireAccepted:
		return writeFrame(connection, handshake(session, supervisor, socket, controlFiles, decision.resolution), MaxWireFrameBytes)
	case reconnectWireSilentClose:
		return nil
	default:
		return ErrIntervention
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

type sessionControlBoundary struct {
	directory         *os.File
	directoryIdentity ControlDirectoryIdentity
	socket            ControlSocketIdentity
	heldFiles         *heldSessionControlFiles
	controlFiles      SessionControlFiles
}

func (boundary sessionControlBoundary) revalidate(snapshot JournalSnapshot) error {
	if boundary.directory == nil || boundary.directoryIdentity.validate() != nil || boundary.socket.validate() != nil || boundary.controlFiles.validate() != nil || boundary.heldFiles == nil {
		return ErrInvalid
	}
	if revalidateControlDirectoryForSnapshot(boundary.directory, boundary.directoryIdentity, snapshot) != nil || observeControlSocketExact(boundary.directory, boundary.socket) != nil || revalidateHeldSessionControlFiles(boundary.directory, boundary.heldFiles, boundary.controlFiles) != nil {
		return ErrConflict
	}
	return nil
}

func serveConnection(connection net.Conn, reader *bufio.Reader, session *Session, boundary sessionControlBoundary) (bool, error) {
	for {
		raw, err := readFrame(reader, MaxWireFrameBytes)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		response, err := handleSessionCommand(session, boundary, raw)
		if err != nil {
			return false, err
		}
		if err := writeFrame(connection, response, MaxWireFrameBytes); err != nil {
			return false, err
		}
		state := session.State()
		if state == string(sessionClosed) || state == string(sessionAborted) {
			return true, nil
		}
	}
}

func handleSessionCommand(session *Session, boundary sessionControlBoundary, raw []byte) (Response, error) {
	if session == nil {
		return Response{}, ErrInvalid
	}
	if err := boundary.revalidate(session.journal.Snapshot()); err != nil {
		session.intervene()
		return Response{}, ErrConflict
	}
	response := session.Handle(raw)
	// Session.Handle returns only after a command receipt is fsynced. Never let
	// a success receipt cross the connection if any pathname/descriptor
	// identity drifted during mechanics execution.
	if err := boundary.revalidate(session.journal.Snapshot()); err != nil {
		session.intervene()
		return Response{}, ErrConflict
	}
	return response, nil
}

func handshake(session *Session, supervisor CoreIdentity, socket ControlSocketIdentity, controlFiles SessionControlFiles, resolution reconnectResolution) HandshakeResponse {
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	return HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: session.sessionID, SessionNonceDigest: session.nonceDigest, OwnerEpoch: session.ownerEpoch, CurrentAuthorityHead: session.authorityHead, CommandSequence: commandSequence, CommandHead: commandHead, JournalSequence: journalSequence, JournalHead: journalHead, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), SupervisorProcess: supervisor.Process, SupervisorBinary: supervisor.Binary, ControlSocket: socket, ControlFiles: controlFiles, Reconciliation: resolution.State, ReplayedResponse: resolution.Response}
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

type controlDirectoryEntrySet uint8

const (
	controlDirectoryNonce controlDirectoryEntrySet = 1 << iota
	controlDirectoryJournal
	controlDirectorySocket
	controlDirectoryStdout
	controlDirectoryStderr
	controlDirectoryTranscript
)

const (
	controlDirectoryEmpty       controlDirectoryEntrySet = 0
	controlDirectorySetupFiles                           = controlDirectoryNonce | controlDirectoryJournal
	controlDirectoryRuntimeBase                          = controlDirectorySetupFiles | controlDirectorySocket
	controlDirectoryCollectOne                           = controlDirectoryRuntimeBase | controlDirectoryStdout
	controlDirectoryCollectTwo                           = controlDirectoryCollectOne | controlDirectoryStderr
	controlDirectoryCollected                            = controlDirectoryCollectTwo | controlDirectoryTranscript
)

// revalidateInitialControlDirectory preserves the bootstrap contract: the
// first observation is exact, including LinkCount, and the directory is empty.
func revalidateInitialControlDirectory(file *os.File, expected ControlDirectoryIdentity) error {
	return revalidateControlDirectoryEntries(file, expected, true, controlDirectoryEmpty)
}

// revalidateControlDirectoryEntries compares the stable directory object and
// requires the descriptor-relative entry scan to match one explicitly allowed
// exact set. allowLinkCountDrift is represented by exactIdentity=false; no
// entry is admitted merely because its name belongs to the frozen vocabulary.
func revalidateControlDirectoryEntries(file *os.File, expected ControlDirectoryIdentity, exactIdentity bool, allowed ...controlDirectoryEntrySet) error {
	path, observed, err := observeControlDirectory(file)
	if err != nil || expected.validate() != nil || path != expected.CanonicalPath || exactIdentity && observed != expected || !exactIdentity && !sameControlDirectoryObject(observed, expected) || len(allowed) == 0 {
		return ErrConflict
	}
	entries, err := readHeldDirectory(file)
	if err != nil {
		return ErrConflict
	}
	var observedEntries controlDirectoryEntrySet
	for _, entry := range entries {
		bit, ok := controlDirectoryEntry(entry)
		if !ok || observedEntries&bit != 0 {
			return ErrConflict
		}
		observedEntries |= bit
	}
	matched := false
	for _, expectedEntries := range allowed {
		if observedEntries == expectedEntries {
			matched = true
			break
		}
	}
	if !matched {
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
	if validatePresentOutputObjects(file, observedEntries) != nil {
		return ErrConflict
	}
	return nil
}

// revalidateControlDirectoryForSnapshot derives the only permitted runtime
// entry set from the exact durable journal state. A pending collect permits
// only the ordered O_EXCL creation prefixes. A committed successful collect
// requires all three outputs; a rejected collect therefore falls back to the
// pre-collect set and any partial output forces intervention.
func revalidateControlDirectoryForSnapshot(file *os.File, expected ControlDirectoryIdentity, snapshot JournalSnapshot) error {
	allowed, collected, err := controlDirectoryEntriesForSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := revalidateControlDirectoryEntries(file, expected, false, allowed...); err != nil {
		return err
	}
	if collected != nil && validateStoredCollectedTranscript(file, *collected) != nil {
		return ErrConflict
	}
	return nil
}

func controlDirectoryEntriesForSnapshot(snapshot JournalSnapshot) ([]controlDirectoryEntrySet, *replayedCommand, error) {
	if snapshot.Sequence == 0 || !validDigest(snapshot.Head) {
		return nil, nil, ErrConflict
	}
	var collected *replayedCommand
	for _, command := range snapshot.commands {
		if command.Projection.Command == CommandCollect && command.Response.Status == "ok" {
			if collected != nil {
				return nil, nil, ErrConflict
			}
			copy := command
			collected = &copy
		}
	}
	if collected != nil {
		return []controlDirectoryEntrySet{controlDirectoryCollected}, collected, nil
	}
	if snapshot.pending != nil && snapshot.pending.Command == CommandCollect {
		return []controlDirectoryEntrySet{controlDirectoryRuntimeBase, controlDirectoryCollectOne, controlDirectoryCollectTwo, controlDirectoryCollected}, nil, nil
	}
	return []controlDirectoryEntrySet{controlDirectoryRuntimeBase}, nil, nil
}

// readHeldJournalSnapshot observes one immutable read transaction over the
// already-held journal descriptor. Unlike OpenJournal it never truncates a
// torn tail and therefore is safe for client/recovery admission.
func readHeldJournalSnapshot(file *os.File) (JournalSnapshot, error) {
	if file == nil {
		return JournalSnapshot{}, ErrInvalid
	}
	before, size, err := observeControlFile(file)
	if err != nil || size <= 0 || size > MaxJournalFileBytes {
		return JournalSnapshot{}, ErrConflict
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, size))
	if err != nil || int64(len(data)) != size {
		return JournalSnapshot{}, ErrIntervention
	}
	after, afterSize, err := observeControlFile(file)
	if err != nil || after != before || afterSize != size {
		return JournalSnapshot{}, ErrConflict
	}
	records, consumed, partial, err := parseJournal(data)
	if err != nil || partial || consumed != len(data) {
		return JournalSnapshot{}, ErrIntervention
	}
	replay := &Journal{head: JournalGenesisDigest, commands: make(map[string]replayedCommand)}
	if err := replay.applyReplay(records); err != nil {
		return JournalSnapshot{}, err
	}
	return replay.Snapshot(), nil
}

// sameControlDirectoryObject compares the fields that identify the held
// directory object and its security boundary. LinkCount is intentionally not
// compared: supported Darwin filesystems may change it when entries are
// created or removed. Entry names are checked descriptor-relatively by
// revalidateControlDirectoryEntries, while the nonce, journal and socket retain their
// own exact object-identity checks.
func sameControlDirectoryObject(left, right ControlDirectoryIdentity) bool {
	return left.CanonicalPath == right.CanonicalPath && left.Device == right.Device && left.Inode == right.Inode && left.FileType == right.FileType && left.UID == right.UID && left.GID == right.GID && left.Mode == right.Mode
}

func controlDirectoryEntry(name string) (controlDirectoryEntrySet, bool) {
	switch name {
	case nonceFileName:
		return controlDirectoryNonce, true
	case JournalFileName:
		return controlDirectoryJournal, true
	case controlSocket:
		return controlDirectorySocket, true
	case stdoutObjectName:
		return controlDirectoryStdout, true
	case stderrObjectName:
		return controlDirectoryStderr, true
	case transcriptObjectName:
		return controlDirectoryTranscript, true
	default:
		return 0, false
	}
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

func writeHeldOpenatExclusive(directory *os.File, name string, data []byte, mode uint32) (*os.File, error) {
	file, err := openatExclusive(directory, name, mode)
	if err != nil {
		return nil, err
	}
	if validateJournalFile(file) != nil || writeAll(file, data) != nil || file.Sync() != nil || validateJournalFile(file) != nil {
		_ = file.Close()
		return nil, ErrIntervention
	}
	if err := directory.Sync(); err != nil {
		_ = file.Close()
		return nil, ErrIntervention
	}
	return file, nil
}
