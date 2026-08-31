//go:build darwin && arm64

package processsupervisor

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

func integrationDigest(label string) string { return canonical.DigestBytes([]byte("label:" + label)) }

// IntegrationTestRebindSetup starts a real in-process supervisor loop in bound
// state and returns the data needed to drive a read-only Attach +
// bind-authority(owner-successor) rebind through the real wire protocol. It is
// for integration tests in other packages only; the processsupervisor
// architecture test verifies it has no production callsite.
type IntegrationTestRebindSetup struct {
	Anchor            HandshakeAnchor
	ControlDirectory  *os.File
	StartedFact       string
	ObservationDigest string
	Child             ProcessIdentity
	Core              CoreIdentity
	Supervisor        ProcessIdentity
	Rebind            func(ctx context.Context, successorHead string) (VerifiedCommandOutcome, error)
	Cleanup           func()
}

// StartIntegrationTestRebind starts a real supervisor loop configured in bound
// state with the given startedFact and observationDigest.
func StartIntegrationTestRebind(startedFact, observationDigest string, child ProcessIdentity) (*IntegrationTestRebindSetup, error) {
	root, err := os.MkdirTemp("/private/tmp", "marshal-rebind-int-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.Chown(root, -1, os.Getegid()); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	directory, err := os.Open(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	_, directoryIdentity, err := observeControlDirectory(directory)
	if err != nil {
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	bootstrap := integrationTestBootstrap(directoryIdentity)
	mechanics := &integrationTestMechanics{child: child}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	clientFile := os.NewFile(uintptr(fds[0]), "rebind-int-client")
	bootstrapFile := os.NewFile(uintptr(fds[1]), "rebind-int-server")
	clientConn, err := net.FileConn(clientFile)
	_ = clientFile.Close()
	if err != nil {
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	bootstrapConn, ok := clientConn.(*net.UnixConn)
	if !ok {
		_ = clientConn.Close()
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, ErrInvalid
	}
	reconnectReady := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runSupervisorLoop(ctx, bootstrapFile, directory, supervisorLoopOptions{
			mechanics: mechanics,
			configureSession: func(session *Session) {
				session.mu.Lock()
				session.state = sessionBound
				session.supervisorStartedFact = startedFact
				session.lastObservation = observationDigest
				session.mu.Unlock()
			},
			observePeer:    func(*net.UnixConn) (CoreIdentity, error) { return bootstrap.Core, nil },
			observeSelf:    func() (CoreIdentity, error) { return bootstrap.Core, nil },
			reconnectReady: func() { close(reconnectReady) },
		})
	}()
	codec, err := NewProtocolCodec(bootstrapConn)
	if err != nil || codec.Write(bootstrap) != nil {
		cancel()
		_ = bootstrapConn.Close()
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	var handshake HandshakeResponse
	if err := bootstrapConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		cancel()
		_ = bootstrapConn.Close()
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	handshakeErr := codec.Read(&handshake)
	_ = bootstrapConn.SetReadDeadline(time.Time{})
	if handshakeErr != nil || handshake.Status != "ok" {
		cancel()
		_ = bootstrapConn.Close()
		loopErr := <-done
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
		if loopErr != nil && handshakeErr != nil {
			return nil, fmt.Errorf("handshake: %w (loop: %v)", handshakeErr, loopErr)
		}
		if loopErr != nil {
			return nil, loopErr
		}
		return nil, fmt.Errorf("handshake status=%s err=%v", handshake.Status, handshakeErr)
	}
	anchor := HandshakeAnchor{
		SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest,
		Authority: bootstrap.Authority, OwnerEpoch: handshake.OwnerEpoch,
		CurrentAuthorityHead: handshake.CurrentAuthorityHead, CommandSequence: handshake.CommandSequence,
		CommandHead: handshake.CommandHead, JournalSequence: handshake.JournalSequence, JournalHead: handshake.JournalHead,
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: handshake.SupervisorBinary,
		ControlSocket: handshake.ControlSocket, ControlFiles: handshake.ControlFiles,
	}
	cleanup := func() {
		cancel()
		_ = bootstrapConn.Close()
		<-done
		_ = bootstrapFile.Close()
		_ = directory.Close()
		_ = os.RemoveAll(root)
	}
	return &IntegrationTestRebindSetup{
		Anchor:            anchor,
		ControlDirectory:  directory,
		StartedFact:       startedFact,
		ObservationDigest: observationDigest,
		Child:             child,
		Core:              bootstrap.Core,
		Supervisor:        handshake.SupervisorProcess,
		Rebind: func(rebindCtx context.Context, successorHead string) (VerifiedCommandOutcome, error) {
			return doIntegrationTestRebind(rebindCtx, bootstrapConn, reconnectReady, directory, anchor, startedFact, observationDigest, successorHead, child, bootstrap.Core, bootstrap.SessionNonce, handshake.SupervisorProcess)
		},
		Cleanup: cleanup,
	}, nil
}

func doIntegrationTestRebind(ctx context.Context, bootstrapConn *net.UnixConn, reconnectReady chan struct{}, directory *os.File, anchor HandshakeAnchor, startedFact, observationDigest, successorHead string, child ProcessIdentity, core CoreIdentity, sessionNonce string, supervisor ProcessIdentity) (VerifiedCommandOutcome, error) {
	_ = bootstrapConn.Close()
	select {
	case <-reconnectReady:
	case <-time.After(5 * time.Second):
		return VerifiedCommandOutcome{}, ErrIntervention
	}
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(30 * time.Second)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(directory.Name(), controlSocket), Net: "unix"})
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, MaxWireFrameBytes+frameHeaderBytes+1)
	directoryIdentity, err := ObserveHeldControlDirectory(directory)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	acquisition := AttachOwnerAcquisition{
		AuthorityNamespaceID: anchor.Authority.AuthorityNamespaceID, RepositoryIdentityDigest: integrationDigest("repo"),
		OwnerEpoch: anchor.OwnerEpoch + 1, OwnerUID: core.UID, OwnerGID: core.GID,
		OwnerProcess: core.Process, OwnerBinary: core.Binary,
		ObserverIdentity: "darwin-owner-observer/v1", ObservedAt: "2026-08-29T00:00:00Z",
		PreviousFactDigest: integrationDigest("prev"), FactDigest: integrationDigest("fact"),
	}
	boundFact := AttachOwnerBoundFact{
		Authority: anchor.Authority, PreviousAttemptRevision: 1, PreviousAttemptHead: anchor.CurrentAuthorityHead,
		AttemptRevision: 2, AttemptHead: successorHead, ControlOwnerAcquiredFactDigest: acquisition.FactDigest,
		OwnerEpoch: acquisition.OwnerEpoch, FactDigest: integrationDigest("bound"),
	}
	attachReq := attachRequest{
		SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: sessionNonce,
		Core: core, ControlDirectoryIdentity: directoryIdentity, Authority: AttachAuthority{
			PreviousSupervisor: anchor, Supervisor: supervisor, CurrentAcquisition: acquisition,
			CurrentOwnerBoundFact: boundFact, Child: child, ChildObservationDigest: observationDigest,
		},
	}
	attachReq.RequestDigest, err = attachReq.detachedDigest()
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	if err := writeFrame(conn, attachReq, MaxWireFrameBytes); err != nil {
		return VerifiedCommandOutcome{}, err
	}
	raw, err := readFrame(reader, MaxWireFrameBytes)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	var attachResp attachResponse
	if err := strictCanonicalDecode(raw, &attachResp); err != nil || attachResp.validate(attachReq, core) != nil {
		return VerifiedCommandOutcome{}, ErrConflict
	}
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: anchor.OwnerEpoch, PreviousAuthorityHead: anchor.CurrentAuthorityHead, AuthorityHead: successorHead}
	prepared, err := PrepareCommand(anchor, CommandOptions{Command: CommandBindAuthority, CommandID: "integration-rebind", Sequence: anchor.CommandSequence + 1, PreviousCommandDigest: anchor.CommandHead, CurrentAuthorityHead: anchor.CurrentAuthorityHead, Deadline: deadline}, payload)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return VerifiedCommandOutcome{}, err
	}
	if err := writeFrame(conn, prepared.request, MaxWireFrameBytes); err != nil {
		return VerifiedCommandOutcome{}, err
	}
	rebindRaw, err := readFrame(reader, MaxWireFrameBytes)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	var rebindResp Response
	if err := strictCanonicalDecode(rebindRaw, &rebindResp); err != nil {
		return VerifiedCommandOutcome{}, err
	}
	if err := ValidateResponseBinding(rebindResp, prepared.request); err != nil {
		return VerifiedCommandOutcome{}, err
	}
	post, err := commandPostAnchor(anchor, prepared.request, rebindResp)
	if err != nil {
		return VerifiedCommandOutcome{}, err
	}
	return verifiedCommandOutcome(prepared.request, rebindResp, CommandRecoveryEvidence{PreCommand: anchor, PostCommand: post})
}

func integrationTestBootstrap(directoryIdentity ControlDirectoryIdentity) BootstrapRequest {
	return BootstrapRequest{
		SchemaVersion: BootstrapSchema, ProtocolRevision: ProtocolRevision,
		SessionID: "rebind-int-session", SessionNonce: strings.Repeat("0123456789abcdef", 4), OwnerEpoch: 1,
		Authority:            AuthorityTuple{AuthorityNamespaceID: "rebind-int-ns", TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1", LeaseDigest: integrationDigest("lease"), Generation: 1, FencingTokenDigest: integrationDigest("fencing"), OrchestratorID: "orchestrator-1"},
		LaunchAuthorizedFact: integrationDigest("launch"), CurrentAuthorityHead: integrationDigest("initial"),
		ControlDirectoryIdentity: directoryIdentity,
		Core:                     CoreIdentity{UID: 501, GID: 20, Process: ProcessIdentity{PID: 200, BirthSeconds: 1_700_000_000, BirthMicroseconds: 1, SessionID: 199, ProcessGroupID: 199}, Binary: BinaryIdentity{CanonicalPath: "/fixed/bin/marshal", Device: 1, Inode: 3, FileType: "regular", UID: 501, GID: 20, Mode: 0o100755, LinkCount: 1, Size: 100, RawSHA256: integrationDigest("sha"), CDHash: strings.Repeat("5", 40), SourceHead: strings.Repeat("6", 40), SelfProfile: "darwin-local-dogfood"}},
	}
}

type integrationTestMechanics struct{ child ProcessIdentity }

func (m *integrationTestMechanics) attachChildIdentity() (ProcessIdentity, error) {
	return m.child, nil
}
func (m *integrationTestMechanics) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	return rejectedResult()
}
func (m *integrationTestMechanics) Resume(context.Context, ResumePayload) (MechanicsResult, error) {
	return rejectedResult()
}
func (m *integrationTestMechanics) Inspect(context.Context, CleanupPayload) (MechanicsResult, error) {
	return rejectedResult()
}
func (m *integrationTestMechanics) Terminate(context.Context, CleanupPayload) (MechanicsResult, error) {
	return rejectedResult()
}
func (m *integrationTestMechanics) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	return rejectedResult()
}
func (m *integrationTestMechanics) Close(context.Context, ClosePayload) (MechanicsResult, error) {
	return rejectedResult()
}

func rejectedResult() (MechanicsResult, error) {
	return MechanicsResult{Disposition: "rejected", ReasonCode: ErrInvalid.ReasonCode, ObservationDigest: canonical.DigestBytes([]byte(ErrInvalid.ReasonCode)), Payload: canonicalEmptyPayload()}, nil
}
