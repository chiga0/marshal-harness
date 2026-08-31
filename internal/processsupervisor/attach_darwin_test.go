//go:build darwin

package processsupervisor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// attachFixtureMechanics is a test-only Mechanics that satisfies the
// attachChildObserver capability with a deterministic child identity. It does
// not spawn a real process: a real darwinMechanics child requires the full
// S1' source-gate spawn chain, which is out of scope for the Attach primitive
// slice. The real darwinMechanics.attachChildIdentity implementation is
// production code and is exercised by the existing mechanics tests.
type attachFixtureMechanics struct {
	fakeMechanics
	child ProcessIdentity
}

func (mechanics *attachFixtureMechanics) attachChildIdentity() (ProcessIdentity, error) {
	if mechanics == nil || mechanics.child.validate() != nil {
		return ProcessIdentity{}, ErrConflict
	}
	return mechanics.child, nil
}

type rejectingInspectAttachMechanics struct {
	attachFixtureMechanics
}

func (*rejectingInspectAttachMechanics) Inspect(context.Context, CleanupPayload) (MechanicsResult, error) {
	return MechanicsResult{}, ErrConflict
}

// TestWithAttachedRejectsInvalidOptions covers the client entry-point
// validation: any nil context, nil control directory, non-absolute/unclean
// binary path, invalid authority, nil owner verifier, or nil borrower fails
// closed with ErrInvalid before the owner verifier or wire exchange run.
func TestWithAttachedRejectsInvalidOptions(t *testing.T) {
	authority := validAttachAuthority()
	neverInvoke := attachVerifierFunc(func(context.Context, AttachAuthority, func() error) error {
		t.Fatal("invalid options invoked the owner verifier")
		return ErrConflict
	})
	for _, options := range []AttachOptions{
		{FixedMarshalPath: "/fixed/bin/marshal", ControlDirectory: nil, Authority: authority, OwnerVerifier: neverInvoke},
		{FixedMarshalPath: "relative/path", Authority: authority, OwnerVerifier: neverInvoke},
		{FixedMarshalPath: "/fixed/bin/marshal", Authority: AttachAuthority{}, OwnerVerifier: neverInvoke},
		{FixedMarshalPath: "/fixed/bin/marshal", Authority: authority, OwnerVerifier: nil},
	} {
		if err := WithAttached(context.Background(), options, func(*AttachedSession) error { return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("WithAttached invalid options = %v, want ErrInvalid", err)
		}
	}
}

// TestDarwinAttachWireIsReadOnlyAndRejectsEveryIdentityDrift drives the real
// runSupervisorLoop with a live control directory, nonce, journal and socket,
// then sends one Attach frame. The success case proves an authenticated
// observation is returned; every drift case proves the server rejects without
// appending the mechanics journal, changing the nonce, or mutating session
// owner/head/child state.
func TestDarwinAttachWireIsReadOnlyAndRejectsEveryIdentityDrift(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	observationDigest := digest("0")
	for _, test := range []struct {
		name   string
		mutate func(*attachRequest)
	}{
		{name: "success"},
		{name: "nonce", mutate: func(request *attachRequest) { request.SessionNonce = strings.Repeat("f", 64) }},
		{name: "peer-birth", mutate: func(request *attachRequest) {
			request.Core.Process.BirthMicroseconds++
			request.Authority.CurrentAcquisition.OwnerProcess = request.Core.Process
		}},
		{name: "binary-sha", mutate: func(request *attachRequest) {
			request.Core.Binary.RawSHA256 = digest("a")
			request.Authority.CurrentAcquisition.OwnerBinary = request.Core.Binary
			request.Authority.PreviousSupervisor.FixedBinary = request.Core.Binary
		}},
		{name: "cdhash", mutate: func(request *attachRequest) {
			request.Core.Binary.CDHash = strings.Repeat("a", 40)
			request.Authority.CurrentAcquisition.OwnerBinary = request.Core.Binary
			request.Authority.PreviousSupervisor.FixedBinary = request.Core.Binary
		}},
		{name: "source-head", mutate: func(request *attachRequest) {
			request.Core.Binary.SourceHead = strings.Repeat("b", 40)
			request.Authority.CurrentAcquisition.OwnerBinary = request.Core.Binary
			request.Authority.PreviousSupervisor.FixedBinary = request.Core.Binary
		}},
		{name: "supervisor-birth", mutate: func(request *attachRequest) { request.Authority.Supervisor.BirthMicroseconds++ }},
		{name: "session", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.SessionID = "other-session" }},
		{name: "child", mutate: func(request *attachRequest) { request.Authority.Child.PID++ }},
		{name: "child-observation", mutate: func(request *attachRequest) { request.Authority.ChildObservationDigest = digest("a") }},
		{name: "journal-sequence", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.JournalSequence++ }},
		{name: "journal-head", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.JournalHead = digest("a") }},
		{name: "command-head", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.CommandHead = digest("a") }},
		{name: "previous-authority-head", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.CurrentAuthorityHead = digest("b") }},
		{name: "owner-epoch", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.OwnerEpoch++ }},
		{name: "control-root", mutate: func(request *attachRequest) { request.ControlDirectoryIdentity.Inode++ }},
		{name: "control-socket", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.ControlSocket.Inode++ }},
		{name: "control-files", mutate: func(request *attachRequest) { request.Authority.PreviousSupervisor.ControlFiles.Nonce.Device++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mechanics := &attachFixtureMechanics{child: child}
			harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
				mechanics: mechanics,
				configureSession: func(session *Session) {
					session.state = sessionBound
					session.lastObservation = observationDigest
				},
			})
			connection := harness.beginReconnect(t)
			beforeNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName))
			beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
			beforeSession := snapshotAttachSession(harness.session)
			directory, err := ObserveHeldControlDirectory(harness.directory)
			if err != nil {
				t.Fatal(err)
			}
			request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: directory, Authority: attachAuthorityFromHarness(harness, child, observationDigest)}
			if test.mutate != nil {
				test.mutate(&request)
			}
			request.RequestDigest, err = request.detachedDigest()
			if err != nil || request.validate() != nil {
				t.Fatalf("attach request: %v", err)
			}
			codec, err := NewProtocolCodec(connection)
			if err != nil || codec.Write(request) != nil {
				t.Fatalf("attach write: %v", err)
			}
			var response attachResponse
			readErr := codec.Read(&response)
			if test.mutate != nil {
				if readErr == nil {
					t.Fatal("hostile Attach received successful observation")
				}
			} else if readErr != nil || response.validate(request, harness.bootstrap.Core) != nil {
				t.Fatalf("attach response=%+v err=%v", response, readErr)
			}
			_ = connection.Close()
			if after := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName)); !bytes.Equal(after, beforeNonce) {
				t.Fatal("Attach changed nonce bytes")
			}
			if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
				t.Fatal("Attach changed mechanics journal bytes")
			}
			if after := snapshotAttachSession(harness.session); after != beforeSession {
				t.Fatal("Attach changed session owner/head/child observation state")
			}
		})
	}
}

// TestDarwinAttachDoesNotEnterGenericCommandLoop proves that after a successful
// Attach the narrow transport cannot be used to issue a command: any post-Attach
// byte is rejected and no mechanics journal/session state mutates. This is the
// wire-level Disconnect that bounds the borrowed callback.
func TestDarwinAttachDoesNotEnterGenericCommandLoop(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	observationDigest := digest("0")
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: &attachFixtureMechanics{child: child},
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.lastObservation = observationDigest
		},
	})
	connection := harness.beginReconnect(t)
	beforeNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName))
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	beforeSession := snapshotAttachSession(harness.session)
	directory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: directory, Authority: attachAuthorityFromHarness(harness, child, observationDigest)}
	request.RequestDigest, err = request.detachedDigest()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(request) != nil {
		t.Fatalf("attach write: %v", err)
	}
	var response attachResponse
	if err := codec.Read(&response); err != nil || response.validate(request, harness.bootstrap.Core) != nil {
		t.Fatalf("attach response=%+v err=%v", response, err)
	}
	if _, err := connection.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if after := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName)); !bytes.Equal(after, beforeNonce) {
		t.Fatal("post-Attach bytes changed nonce")
	}
	if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
		t.Fatal("post-Attach bytes entered the command journal")
	}
	if after := snapshotAttachSession(harness.session); after != beforeSession {
		t.Fatal("post-Attach bytes changed session state")
	}
}

type attachSessionSnapshot struct {
	state           sessionState
	ownerEpoch      uint64
	authorityHead   string
	commandSequence uint64
	commandHead     string
	lastObservation string
}

func snapshotAttachSession(session *Session) attachSessionSnapshot {
	session.mu.Lock()
	defer session.mu.Unlock()
	return attachSessionSnapshot{state: session.state, ownerEpoch: session.ownerEpoch, authorityHead: session.authorityHead, commandSequence: session.commandSequence, commandHead: session.commandHead, lastObservation: session.lastObservation}
}

func attachAuthorityFromHarness(harness *supervisorLoopHarness, child ProcessIdentity, observationDigest string) AttachAuthority {
	anchor := HandshakeAnchor{
		SessionID: harness.bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(harness.bootstrap.SessionNonce)), Authority: harness.bootstrap.Authority,
		OwnerEpoch: harness.handshake.OwnerEpoch, CurrentAuthorityHead: harness.handshake.CurrentAuthorityHead,
		CommandSequence: harness.handshake.CommandSequence, CommandHead: harness.handshake.CommandHead, JournalSequence: harness.handshake.JournalSequence, JournalHead: harness.handshake.JournalHead,
		UID: harness.bootstrap.Core.UID, GID: harness.bootstrap.Core.GID, FixedBinary: harness.bootstrap.Core.Binary, ControlSocket: harness.handshake.ControlSocket, ControlFiles: harness.handshake.ControlFiles,
	}
	acquisition := AttachOwnerAcquisition{
		AuthorityNamespaceID: harness.bootstrap.Authority.AuthorityNamespaceID, RepositoryIdentityDigest: digest("6"), OwnerEpoch: harness.bootstrap.OwnerEpoch + 1,
		OwnerUID: harness.bootstrap.Core.UID, OwnerGID: harness.bootstrap.Core.GID, OwnerProcess: harness.bootstrap.Core.Process, OwnerBinary: harness.bootstrap.Core.Binary,
		ObserverIdentity: "darwin-current-owner-v1", ObservedAt: time.Unix(harness.bootstrap.Core.Process.BirthSeconds+1, 0).UTC().Format(time.RFC3339Nano), PreviousFactDigest: digest("5"), FactDigest: digest("4"),
	}
	bound := AttachOwnerBoundFact{Authority: harness.bootstrap.Authority, PreviousAttemptRevision: 10, PreviousAttemptHead: digest("3"), AttemptRevision: 11, AttemptHead: digest("2"), ControlOwnerAcquiredFactDigest: acquisition.FactDigest, OwnerEpoch: acquisition.OwnerEpoch, FactDigest: digest("1")}
	return AttachAuthority{PreviousSupervisor: anchor, Supervisor: harness.handshake.SupervisorProcess, CurrentAcquisition: acquisition, CurrentOwnerBoundFact: bound, Child: child, ChildObservationDigest: observationDigest}
}

func mustAttachReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// collectingAttachMechanics drives a real Collect that writes stdout.bin,
// stderr.bin and transcript.jcs into the control directory (control-entry
// growth) while also satisfying the attachChildObserver capability serveAttach
// authenticates against.
type collectingAttachMechanics struct {
	transcriptCollectMechanics
	child ProcessIdentity
}

func (mechanics *collectingAttachMechanics) attachChildIdentity() (ProcessIdentity, error) {
	if mechanics == nil || mechanics.child.validate() != nil {
		return ProcessIdentity{}, ErrConflict
	}
	return mechanics.child, nil
}

// TestDarwinAttachSucceedsAfterControlEntryGrowth proves that a real Collect,
// which creates stdout.bin/stderr.bin/transcript.jcs and grows the APFS control
// directory LinkCount, does not break a subsequent Attach. The stored boundary
// identity (frozen at the post-setup LinkCount) is admitted via
// sameControlDirectoryObject; only the within-Attach before/current/final
// snapshots retain the LinkCount-inclusive drift check; and the nonce/journal
// bytes are unchanged.
func TestDarwinAttachSucceedsAfterControlEntryGrowth(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	observationDigest := digest("0")
	collectAuthorityHead := digest("collect-auth")
	mechanics := &collectingAttachMechanics{
		transcriptCollectMechanics: transcriptCollectMechanics{stdout: []byte("collected-stdout"), stderr: []byte("collected-stderr")},
		child:                      child,
	}
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: mechanics,
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.startedFact = digest("d")
			session.lastObservation = observationDigest
		},
	})
	mechanics.directory = harness.directory

	// Stored identity frozen at the post-setup boundary, before Collect grew the
	// control entry set.
	storedDirectory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}

	// Drive a real Collect: appends intent+receipt to the journal and creates
	// stdout.bin/stderr.bin/transcript.jcs in the control directory.
	collectRequest := commandRequest(t, harness.bootstrap.SessionID, CommandCollect, "collect-entry-growth", 1, CommandGenesisDigest, collectAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: digest("d"), LastObservationDigest: observationDigest})
	collectResponse := harness.session.Handle(mustCanonical(collectRequest))
	if collectResponse.Status != "ok" {
		t.Fatalf("collect response=%+v", collectResponse)
	}

	// APFS grows the control directory LinkCount as Collect creates entries;
	// only LinkCount drifts, so sameControlDirectoryObject still holds.
	grownDirectory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	if storedDirectory.LinkCount == grownDirectory.LinkCount {
		t.Fatalf("control-entry growth did not change directory LinkCount: stored=%d grown=%d", storedDirectory.LinkCount, grownDirectory.LinkCount)
	}
	if !sameControlDirectoryObject(storedDirectory, grownDirectory) {
		t.Fatalf("control-entry growth changed a stable directory field: stored=%+v grown=%+v", storedDirectory, grownDirectory)
	}

	beforeNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName))
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	beforeSession := snapshotAttachSession(harness.session)

	connection := harness.beginReconnect(t)
	defer connection.Close()

	commandSequence, commandHead, journalSequence, journalHead := harness.session.Snapshot()
	anchor := HandshakeAnchor{
		SessionID: harness.bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(harness.bootstrap.SessionNonce)), Authority: harness.bootstrap.Authority,
		OwnerEpoch: harness.bootstrap.OwnerEpoch, CurrentAuthorityHead: collectAuthorityHead,
		CommandSequence: commandSequence, CommandHead: commandHead, JournalSequence: journalSequence, JournalHead: journalHead,
		UID: harness.bootstrap.Core.UID, GID: harness.bootstrap.Core.GID, FixedBinary: harness.bootstrap.Core.Binary, ControlSocket: harness.handshake.ControlSocket, ControlFiles: harness.handshake.ControlFiles,
	}
	acquisition := AttachOwnerAcquisition{
		AuthorityNamespaceID: harness.bootstrap.Authority.AuthorityNamespaceID, RepositoryIdentityDigest: digest("6"), OwnerEpoch: harness.bootstrap.OwnerEpoch + 1,
		OwnerUID: harness.bootstrap.Core.UID, OwnerGID: harness.bootstrap.Core.GID, OwnerProcess: harness.bootstrap.Core.Process, OwnerBinary: harness.bootstrap.Core.Binary,
		ObserverIdentity: "darwin-current-owner-v1", ObservedAt: time.Unix(harness.bootstrap.Core.Process.BirthSeconds+1, 0).UTC().Format(time.RFC3339Nano), PreviousFactDigest: digest("5"), FactDigest: digest("4"),
	}
	bound := AttachOwnerBoundFact{Authority: harness.bootstrap.Authority, PreviousAttemptRevision: 10, PreviousAttemptHead: digest("3"), AttemptRevision: 11, AttemptHead: digest("2"), ControlOwnerAcquiredFactDigest: acquisition.FactDigest, OwnerEpoch: acquisition.OwnerEpoch, FactDigest: digest("1")}
	authority := AttachAuthority{PreviousSupervisor: anchor, Supervisor: harness.handshake.SupervisorProcess, CurrentAcquisition: acquisition, CurrentOwnerBoundFact: bound, Child: child, ChildObservationDigest: collectResponse.ObservationDigest}

	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: grownDirectory, Authority: authority}
	request.RequestDigest, err = request.detachedDigest()
	if err != nil || request.validate() != nil {
		t.Fatalf("attach request: %v", err)
	}
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(request) != nil {
		t.Fatalf("attach write: %v", err)
	}
	var response attachResponse
	if err := codec.Read(&response); err != nil || response.validate(request, harness.bootstrap.Core) != nil {
		t.Fatalf("attach response=%+v err=%v", response, err)
	}
	_ = connection.Close()

	if after := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName)); !bytes.Equal(after, beforeNonce) {
		t.Fatal("Attach changed nonce bytes after control-entry growth")
	}
	if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
		t.Fatal("Attach changed mechanics journal bytes after control-entry growth")
	}
	if after := snapshotAttachSession(harness.session); after != beforeSession {
		t.Fatal("Attach changed session state after control-entry growth")
	}
}

func TestAttachPostCommandBoundaryAllowsOnlyCollectLinkCountGrowth(t *testing.T) {
	before := attachControlSnapshot{
		Directory:   ControlDirectoryIdentity{CanonicalPath: "/control", Device: 1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: 040700, LinkCount: 2},
		Socket:      ControlSocketIdentity{Device: 1, Inode: 3, FileType: "socket", UID: 501, GID: 20, Mode: 0140600, LinkCount: 1},
		NonceSize:   32,
		NonceDigest: digest("nonce"),
	}
	after := before
	after.Directory.LinkCount = 5

	if !sameAttachPostCommandBoundary(CommandCollect, after, before) {
		t.Fatal("Collect rejected monotonic directory LinkCount growth")
	}
	for _, command := range []CommandName{CommandBindAuthority, CommandInspect, CommandClose} {
		if sameAttachPostCommandBoundary(command, after, before) {
			t.Fatalf("%s admitted Collect-only directory LinkCount growth", command)
		}
	}

	decreased := before
	decreased.Directory.LinkCount--
	if sameAttachPostCommandBoundary(CommandCollect, decreased, before) {
		t.Fatal("Collect admitted a decreasing directory LinkCount")
	}

	drifted := after
	drifted.Directory.Inode++
	if sameAttachPostCommandBoundary(CommandCollect, drifted, before) {
		t.Fatal("Collect admitted directory object drift")
	}

	drifted = after
	drifted.Socket.Inode++
	if sameAttachPostCommandBoundary(CommandCollect, drifted, before) {
		t.Fatal("Collect admitted socket drift")
	}
}

func newAttachRequest(t *testing.T, harness *supervisorLoopHarness, child ProcessIdentity, observationDigest string) attachRequest {
	t.Helper()
	directory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: directory, Authority: attachAuthorityFromHarness(harness, child, observationDigest)}
	request.RequestDigest, err = request.detachedDigest()
	if err != nil || request.validate() != nil {
		t.Fatalf("attach request: %v", err)
	}
	return request
}

// tryAttach sends one Attach request on connection and returns the validated
// response. It performs no t.Fatal so callers can retry past the supervisor's
// single-active-connection admission window.
func tryAttach(connection *net.UnixConn, request attachRequest, core CoreIdentity) (attachResponse, error) {
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return attachResponse{}, err
	}
	codec, err := NewProtocolCodec(connection)
	if err != nil {
		return attachResponse{}, err
	}
	if err := codec.Write(request); err != nil {
		return attachResponse{}, err
	}
	var response attachResponse
	if err := codec.Read(&response); err != nil {
		return attachResponse{}, err
	}
	if err := response.validate(request, core); err != nil {
		return attachResponse{}, err
	}
	_ = connection.CloseWrite()
	return response, nil
}

// attachWithRetry dials fresh connections until one Attach completes. The
// supervisor admits only one active connection, so a connection dialled while
// the previous Attach is still being torn down is rejected; retrying proves the
// loop is alive and cycling. A terminated loop refuses every dial or never
// responds, so the retry exhausts the timeout.
func attachWithRetry(t *testing.T, harness *supervisorLoopHarness, request attachRequest, core CoreIdentity, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(harness.root, controlSocket), Net: "unix"})
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if _, err := tryAttach(connection, request, core); err != nil {
			_ = connection.Close()
			lastErr = err
			time.Sleep(2 * time.Millisecond)
			continue
		}
		_ = connection.Close()
		return
	}
	t.Fatalf("supervisor loop did not survive / accept next connection: %v", lastErr)
}

// TestDarwinAttachSurvivesPeerEarlyCloseAndAcceptsNextConnection proves that an
// authenticated peer transport failure (early close / response write failure)
// during serveAttach maps to ErrConflict so the Supervisor loop continues and
// accepts the next connection, instead of terminating with ErrIntervention.
func TestDarwinAttachSurvivesPeerEarlyCloseAndAcceptsNextConnection(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	observationDigest := digest("0")
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: &attachFixtureMechanics{child: child},
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.lastObservation = observationDigest
		},
	})
	request := newAttachRequest(t, harness, child, observationDigest)

	// First connection: send a valid Attach, then close immediately without
	// reading the response (peer early-close). The server's response write or
	// EOF read on the authenticated peer must not terminate the Supervisor.
	conn1 := harness.beginReconnect(t)
	codec1, err := NewProtocolCodec(conn1)
	if err != nil || codec1.Write(request) != nil {
		t.Fatalf("attach write: %v", err)
	}
	_ = conn1.Close()

	// The supervisor loop must survive and accept subsequent connections with
	// fresh successful Attach probes.
	attachWithRetry(t, harness, request, harness.bootstrap.Core, 4*time.Second)
	attachWithRetry(t, harness, request, harness.bootstrap.Core, 4*time.Second)
}

// TestDarwinAttachInspectAuthorityAdvanceKeepsSupervisorServing reproduces the
// production terminalization boundary: an authenticated Inspect can carry a
// newer durable Attempt head than the owner-bound mechanics anchor. The
// Supervisor must commit that exact head and remain available for the following
// Close Attach instead of treating the legitimate advance as intervention.
func TestDarwinAttachInspectAuthorityAdvanceKeepsSupervisorServing(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	startedFact := digest("started")
	lastObservation := digest("last-observation")
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: &attachFixtureMechanics{child: child},
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.startedFact = startedFact
			session.lastObservation = lastObservation
		},
	})
	request := newAttachRequest(t, harness, child, lastObservation)
	connection := harness.beginReconnect(t)
	if err := connection.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(request) != nil {
		t.Fatalf("attach write: %v", err)
	}
	var observation attachResponse
	if err := codec.Read(&observation); err != nil || observation.validate(request, harness.bootstrap.Core) != nil {
		t.Fatalf("attach response=%+v err=%v", observation, err)
	}

	inspectAuthorityHead := digest("terminalization-barrier")
	cleanup := CleanupPayload{
		TerminalizationBarrierDigest: inspectAuthorityHead,
		TerminalizationID:            "terminalization-1",
		TerminalGeneration:           2,
		CleanupBindingDigest:         digest("cleanup-binding"),
		ProcessStartedFactDigest:     startedFact,
		LastObservationDigest:        lastObservation,
	}
	inspect := commandRequest(t, harness.bootstrap.SessionID, CommandInspect, "inspect-authority-advance", 1, CommandGenesisDigest, inspectAuthorityHead, time.Now().Add(20*time.Second), cleanup)
	if err := codec.Write(inspect); err != nil {
		t.Fatal(err)
	}
	var inspected Response
	if err := codec.Read(&inspected); err != nil || inspected.Status != "ok" || inspected.Command != CommandInspect {
		t.Fatalf("inspect response=%+v err=%v", inspected, err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := io.ReadAll(connection); err != nil || len(unexpected) != 0 {
		t.Fatalf("inspect attach close: bytes=%x err=%v", unexpected, err)
	}
	_ = connection.Close()

	// Rebuild the exact live anchor after Inspect and prove a second Attach is
	// accepted. Before the regression fix, serveAttach returned ErrIntervention
	// after the successful Inspect response and this retry could never connect.
	commandSequence, commandHead, journalSequence, journalHead := harness.session.Snapshot()
	next := request
	next.Authority.PreviousSupervisor.CurrentAuthorityHead = inspectAuthorityHead
	next.Authority.PreviousSupervisor.CommandSequence = commandSequence
	next.Authority.PreviousSupervisor.CommandHead = commandHead
	next.Authority.PreviousSupervisor.JournalSequence = journalSequence
	next.Authority.PreviousSupervisor.JournalHead = journalHead
	next.Authority.ChildObservationDigest = inspected.ObservationDigest
	next.RequestDigest, err = next.detachedDigest()
	if err != nil || next.validate() != nil {
		t.Fatalf("next attach request: %v", err)
	}
	attachWithRetry(t, harness, next, harness.bootstrap.Core, 4*time.Second)
}

// TestDarwinAttachRejectedInspectRetainsAuthorityAndKeepsSupervisorServing
// proves a mechanics-level rejection still commits its durable receipt without
// advancing authority, and remains a recoverable command outcome rather than a
// terminal Supervisor intervention.
func TestDarwinAttachRejectedInspectRetainsAuthorityAndKeepsSupervisorServing(t *testing.T) {
	child := ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}
	startedFact := digest("started")
	lastObservation := digest("last-observation")
	mechanics := &rejectingInspectAttachMechanics{attachFixtureMechanics: attachFixtureMechanics{child: child}}
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: mechanics,
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.startedFact = startedFact
			session.lastObservation = lastObservation
		},
	})
	request := newAttachRequest(t, harness, child, lastObservation)
	connection := harness.beginReconnect(t)
	if err := connection.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(request) != nil {
		t.Fatalf("attach write: %v", err)
	}
	var observation attachResponse
	if err := codec.Read(&observation); err != nil || observation.validate(request, harness.bootstrap.Core) != nil {
		t.Fatalf("attach response=%+v err=%v", observation, err)
	}

	cleanup := CleanupPayload{
		TerminalizationBarrierDigest: digest("terminalization-barrier"),
		TerminalizationID:            "terminalization-1",
		TerminalGeneration:           2,
		CleanupBindingDigest:         digest("cleanup-binding"),
		ProcessStartedFactDigest:     startedFact,
		LastObservationDigest:        lastObservation,
	}
	inspect := commandRequest(t, harness.bootstrap.SessionID, CommandInspect, "inspect-rejected", 1, CommandGenesisDigest, digest("new-attempt-head"), time.Now().Add(20*time.Second), cleanup)
	if err := codec.Write(inspect); err != nil {
		t.Fatal(err)
	}
	var inspected Response
	if err := codec.Read(&inspected); err != nil || inspected.Status != "rejected" || inspected.Command != CommandInspect {
		t.Fatalf("inspect response=%+v err=%v", inspected, err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := io.ReadAll(connection); err != nil || len(unexpected) != 0 {
		t.Fatalf("rejected inspect attach close: bytes=%x err=%v", unexpected, err)
	}
	_ = connection.Close()

	commandSequence, commandHead, journalSequence, journalHead := harness.session.Snapshot()
	next := request
	next.Authority.PreviousSupervisor.CommandSequence = commandSequence
	next.Authority.PreviousSupervisor.CommandHead = commandHead
	next.Authority.PreviousSupervisor.JournalSequence = journalSequence
	next.Authority.PreviousSupervisor.JournalHead = journalHead
	next.RequestDigest, err = next.detachedDigest()
	if err != nil || next.validate() != nil {
		t.Fatalf("next attach request: %v", err)
	}
	attachWithRetry(t, harness, next, harness.bootstrap.Core, 4*time.Second)
}
