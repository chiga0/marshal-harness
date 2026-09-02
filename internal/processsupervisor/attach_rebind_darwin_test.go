//go:build darwin

package processsupervisor

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// rebindBoundSession configures a supervisor loop session in the exact state
// ADR 0067 §4.2 requires before a read-only Attach + bind-authority rebind:
// already bound, with a frozen supervisor-started fact, current authority head,
// command sequence/head and last observation. The rebind is a pure authority
// operation and invokes no Mechanics.
func rebindBoundSession(startedFact, observationDigest string) func(*Session) {
	return func(session *Session) {
		session.mu.Lock()
		defer session.mu.Unlock()
		session.state = sessionBound
		session.supervisorStartedFact = startedFact
		session.lastObservation = observationDigest
	}
}

func rebindAttachAuthority(harness *supervisorLoopHarness, observationDigest string) AttachAuthority {
	return attachAuthorityFromHarness(harness, ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}, observationDigest)
}

func rebindPreparedCommand(t *testing.T, authority AttachAuthority, startedFact, successorHead string) PreparedCommand {
	t.Helper()
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: authority.PreviousSupervisor.OwnerEpoch, PreviousAuthorityHead: authority.PreviousSupervisor.CurrentAuthorityHead, AuthorityHead: successorHead}
	prepared, err := PrepareCommand(authority.PreviousSupervisor, CommandOptions{Command: CommandBindAuthority, CommandID: "rebind-owner-successor", Sequence: authority.PreviousSupervisor.CommandSequence + 1, PreviousCommandDigest: authority.PreviousSupervisor.CommandHead, CurrentAuthorityHead: authority.PreviousSupervisor.CurrentAuthorityHead, Deadline: time.Now().Add(20 * time.Second)}, payload)
	if err != nil {
		t.Fatalf("prepare rebind: %v", err)
	}
	return prepared
}

func rebindHarness(t *testing.T, startedFact, observationDigest string) *supervisorLoopHarness {
	return newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics:        &attachFixtureMechanics{child: ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}},
		configureSession: rebindBoundSession(startedFact, observationDigest),
	})
}

// doRebindAttach writes one Attach frame, reads the observation and returns the
// codec plus the authority used. It is the shared prefix of the rebind wire
// tests.
func doRebindAttach(t *testing.T, harness *supervisorLoopHarness, authority AttachAuthority) (*ProtocolCodec, attachRequest, attachResponse) {
	t.Helper()
	connection := harness.beginReconnect(t)
	directory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: directory, Authority: authority}
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
	return codec, request, response
}

// TestDarwinAttachRebindExecutesBindAuthorityAndAdvancesJournal drives the real
// supervisor loop: one read-only Attach frame followed by exactly one
// bind-authority(owner-successor) rebind frame on the same connection. The
// rebind advances the session authority head, command sequence/head and
// mechanics journal by exactly one intent + one receipt, while the nonce and
// control directory are byte-for-byte unchanged.
func TestDarwinAttachRebindExecutesBindAuthorityAndAdvancesJournal(t *testing.T) {
	startedFact, observationDigest, successorHead := digest("started"), digest("obs"), digest("successor-head")
	harness := rebindHarness(t, startedFact, observationDigest)
	beforeNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName))
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	prepared := rebindPreparedCommand(t, authority, startedFact, successorHead)
	if err := codec.Write(prepared.request); err != nil {
		t.Fatalf("rebind write: %v", err)
	}
	var rebindResp Response
	if err := codec.Read(&rebindResp); err != nil {
		t.Fatalf("rebind response err=%v", err)
	}
	if rebindResp.Status != "ok" || rebindResp.Command != CommandBindAuthority || rebindResp.Sequence != prepared.request.Sequence || rebindResp.RequestDigest != prepared.request.RequestDigest {
		t.Fatalf("rebind response=%+v", rebindResp)
	}
	if err := ValidateResponseBinding(rebindResp, prepared.request); err != nil {
		t.Fatalf("rebind binding: %v", err)
	}
	conn := codec.stream.(*net.UnixConn)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var trailing [1]byte
	if count, readErr := conn.Read(trailing[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("post-rebind read count=%d err=%v", count, readErr)
	}
	_ = conn.Close()
	after := snapshotAttachSession(harness.session)
	if after.state != sessionBound || after.authorityHead != successorHead || after.commandSequence != prepared.request.Sequence || after.commandHead != rebindResp.CommandHead {
		t.Fatalf("session advanced incorrectly: %+v", after)
	}
	if afterNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName)); !bytes.Equal(afterNonce, beforeNonce) {
		t.Fatal("rebind changed nonce bytes")
	}
	afterJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	if bytes.Equal(afterJournal, beforeJournal) {
		t.Fatal("rebind did not append the mechanics journal")
	}
	snapshot := harness.session.journal.Snapshot()
	if snapshot.Sequence != 3 || snapshot.pending != nil || snapshot.currentAuthorityHead != successorHead {
		t.Fatalf("journal snapshot after rebind=%+v", snapshot)
	}
}

// TestDarwinAttachRebindThenImmediateCollect proves that the EOF which closes
// an Attach+bind connection is also the readiness boundary for the next
// Attach. The production client opens that second connection immediately when
// cold recovery is followed by Collect. If the accept-loop active flag is
// released after Close, the peer can observe EOF first and the valid Collect
// Attach is spuriously rejected as already connected.
func TestDarwinAttachRebindThenImmediateCollect(t *testing.T) {
	startedFact, observationDigest, successorHead := digest("started"), digest("obs"), digest("successor-head")
	child := ProcessIdentity{PID: 200, BirthSeconds: 2, BirthMicroseconds: 3, SessionID: 99, ProcessGroupID: 99}
	mechanics := &collectingAttachMechanics{
		transcriptCollectMechanics: transcriptCollectMechanics{stdout: []byte("collected-stdout"), stderr: []byte("collected-stderr")},
		child:                      child,
	}
	harness := newSupervisorLoopHarness(t, supervisorLoopOptions{
		mechanics: mechanics,
		configureSession: func(session *Session) {
			session.state = sessionBound
			session.supervisorStartedFact = startedFact
			session.startedFact = startedFact
			session.lastObservation = observationDigest
		},
	})
	mechanics.directory = harness.directory

	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	preparedRebind := rebindPreparedCommand(t, authority, startedFact, successorHead)
	if err := codec.Write(preparedRebind.request); err != nil {
		t.Fatalf("rebind write: %v", err)
	}
	var rebindResponse Response
	if err := codec.Read(&rebindResponse); err != nil || ValidateResponseBinding(rebindResponse, preparedRebind.request) != nil {
		t.Fatalf("rebind response=%+v err=%v", rebindResponse, err)
	}
	postRebind, err := commandPostAnchor(authority.PreviousSupervisor, preparedRebind.request, rebindResponse)
	if err != nil {
		t.Fatalf("rebind post anchor: %v", err)
	}
	first := codec.stream.(*net.UnixConn)
	if err := first.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var trailing [1]byte
	if count, readErr := first.Read(trailing[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("post-rebind read count=%d err=%v", count, readErr)
	}
	_ = first.Close()

	// Open the successor immediately after EOF, with no scheduling delay or
	// retry. This is the exact cold CLI recovery -> Collect transition.
	second, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(harness.root, controlSocket), Net: "unix"})
	if err != nil {
		t.Fatalf("immediate Collect Attach dial: %v", err)
	}
	defer second.Close()
	directory, err := ObserveHeldControlDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	collectAuthority := authority
	collectAuthority.PreviousSupervisor = postRebind
	collectRequest := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: harness.bootstrap.SessionNonce, Core: harness.bootstrap.Core, ControlDirectoryIdentity: directory, Authority: collectAuthority}
	collectRequest.RequestDigest, err = collectRequest.detachedDigest()
	if err != nil || collectRequest.validate() != nil {
		t.Fatalf("Collect Attach request: %v", err)
	}
	collectCodec, err := NewProtocolCodec(second)
	if err != nil || collectCodec.Write(collectRequest) != nil {
		t.Fatalf("Collect Attach write: %v", err)
	}
	var collectAttachResponse attachResponse
	if err := collectCodec.Read(&collectAttachResponse); err != nil || collectAttachResponse.validate(collectRequest, harness.bootstrap.Core) != nil {
		t.Fatalf("Collect Attach response=%+v err=%v", collectAttachResponse, err)
	}
	preparedCollect, err := PrepareCommand(postRebind, CommandOptions{
		Command: CommandCollect, CommandID: "collect-after-rebind", Sequence: postRebind.CommandSequence + 1,
		PreviousCommandDigest: postRebind.CommandHead, CurrentAuthorityHead: successorHead, Deadline: time.Now().Add(20 * time.Second),
	}, CollectPayload{ProcessStartedFactDigest: startedFact, LastObservationDigest: observationDigest})
	if err != nil {
		t.Fatalf("prepare Collect: %v", err)
	}
	if err := collectCodec.Write(preparedCollect.request); err != nil {
		t.Fatalf("Collect write: %v", err)
	}
	var collectResponse Response
	if err := collectCodec.Read(&collectResponse); err != nil || ValidateResponseBinding(collectResponse, preparedCollect.request) != nil {
		t.Fatalf("Collect response=%+v err=%v", collectResponse, err)
	}
	if collectResponse.Status != "ok" || collectResponse.Command != CommandCollect {
		t.Fatalf("Collect response=%+v", collectResponse)
	}
	if err := second.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if count, readErr := second.Read(trailing[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("post-Collect read count=%d err=%v", count, readErr)
	}
	if snapshot := harness.session.journal.Snapshot(); snapshot.Sequence != 5 || snapshot.pending != nil {
		t.Fatalf("journal after rebind+Collect=%+v", snapshot)
	}
}

type activeReleaseOrderProbe struct {
	active         *atomic.Bool
	activeAtClose  bool
	closeCallCount int
}

func (probe *activeReleaseOrderProbe) Close() error {
	probe.activeAtClose = probe.active.Load()
	probe.closeCallCount++
	return nil
}

func TestReleaseActiveConnectionPublishesAvailabilityBeforeClose(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	probe := &activeReleaseOrderProbe{active: &active}
	releaseActiveConnection(&active, probe)
	if probe.closeCallCount != 1 || probe.activeAtClose || active.Load() {
		t.Fatalf("release order activeAtClose=%t closeCalls=%d activeAfter=%t", probe.activeAtClose, probe.closeCallCount, active.Load())
	}
}

// TestDarwinAttachRebindReadOnlyEOFLeavesMechanicsUnchanged proves that an
// Attach followed immediately by half-close (no rebind frame) remains the
// read-only primitive: the mechanics journal, nonce and session state are all
// byte-for-byte unchanged.
func TestDarwinAttachRebindReadOnlyEOFLeavesMechanicsUnchanged(t *testing.T) {
	startedFact, observationDigest := digest("started"), digest("obs")
	harness := rebindHarness(t, startedFact, observationDigest)
	beforeNonce := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName))
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	beforeSession := snapshotAttachSession(harness.session)
	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	conn := codec.stream.(*net.UnixConn)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var trailing [1]byte
	if count, readErr := conn.Read(trailing[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("read-only read count=%d err=%v", count, readErr)
	}
	_ = conn.Close()
	if after := mustAttachReadFile(t, filepath.Join(harness.root, nonceFileName)); !bytes.Equal(after, beforeNonce) {
		t.Fatal("read-only Attach changed nonce bytes")
	}
	if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
		t.Fatal("read-only Attach changed mechanics journal bytes")
	}
	if after := snapshotAttachSession(harness.session); after != beforeSession {
		t.Fatal("read-only Attach changed session state")
	}
}

// TestDarwinAttachRebindRejectsNonBindAuthority proves that after an Attach the
// narrow transport accepts only bind-authority: any other command frame is
// dropped without appending the journal or mutating session state.
func TestDarwinAttachRebindRejectsNonBindAuthority(t *testing.T) {
	startedFact, observationDigest := digest("started"), digest("obs")
	harness := rebindHarness(t, startedFact, observationDigest)
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	beforeSession := snapshotAttachSession(harness.session)
	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	resumeReq := commandRequest(t, harness.bootstrap.SessionID, CommandResume, "rebind-resume", authority.PreviousSupervisor.CommandSequence+1, authority.PreviousSupervisor.CommandHead, authority.PreviousSupervisor.CurrentAuthorityHead, time.Now().Add(time.Minute), ResumePayload{ProcessStartedFactDigest: startedFact})
	if err := codec.Write(resumeReq); err != nil {
		t.Fatalf("resume write: %v", err)
	}
	var resp Response
	_ = codec.Read(&resp)
	_ = codec.stream.(*net.UnixConn).Close()
	if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
		t.Fatal("non-bind-authority frame mutated the mechanics journal")
	}
	if after := snapshotAttachSession(harness.session); after != beforeSession {
		t.Fatalf("non-bind-authority frame mutated session state: %+v", after)
	}
}

// TestDarwinAttachRebindRejectsAdmissionMismatch proves that a bind-authority
// frame whose sequence does not match the live session is dropped without
// mutating mechanics state.
func TestDarwinAttachRebindRejectsAdmissionMismatch(t *testing.T) {
	startedFact, observationDigest := digest("started"), digest("obs")
	harness := rebindHarness(t, startedFact, observationDigest)
	beforeJournal := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName))
	beforeSession := snapshotAttachSession(harness.session)
	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: authority.PreviousSupervisor.OwnerEpoch, PreviousAuthorityHead: authority.PreviousSupervisor.CurrentAuthorityHead, AuthorityHead: digest("successor-head")}
	wrong := commandRequest(t, harness.bootstrap.SessionID, CommandBindAuthority, "rebind-wrong-sequence", 99, authority.PreviousSupervisor.CommandHead, authority.PreviousSupervisor.CurrentAuthorityHead, time.Now().Add(time.Minute), payload)
	if err := codec.Write(wrong); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp Response
	_ = codec.Read(&resp)
	_ = codec.stream.(*net.UnixConn).Close()
	if after := mustAttachReadFile(t, filepath.Join(harness.root, JournalFileName)); !bytes.Equal(after, beforeJournal) {
		t.Fatal("admission-mismatch rebind mutated the mechanics journal")
	}
	if after := snapshotAttachSession(harness.session); after != beforeSession {
		t.Fatalf("admission-mismatch rebind mutated session state: %+v", after)
	}
}

// TestDarwinAttachRebindExactReplayIsIdempotent proves that re-sending the same
// bind-authority(owner-successor) command ID after it is already committed
// replays the stored receipt instead of appending a second side effect. The
// second call goes directly through Session.HandleAttachRebind, which is the
// same path serveAttach uses for a same-owner response-loss re-send.
func TestDarwinAttachRebindExactReplayIsIdempotent(t *testing.T) {
	startedFact, observationDigest, successorHead := digest("started"), digest("obs"), digest("successor-head")
	harness := rebindHarness(t, startedFact, observationDigest)
	authority := rebindAttachAuthority(harness, observationDigest)
	codec, _, _ := doRebindAttach(t, harness, authority)
	prepared := rebindPreparedCommand(t, authority, startedFact, successorHead)
	if err := codec.Write(prepared.request); err != nil {
		t.Fatal(err)
	}
	var first Response
	if err := codec.Read(&first); err != nil {
		t.Fatalf("first rebind: %v", err)
	}
	conn := codec.stream.(*net.UnixConn)
	_ = conn.CloseWrite()
	var trailing [1]byte
	_, _ = conn.Read(trailing[:])
	_ = conn.Close()
	journalAfterFirst := harness.session.journal.Snapshot().Sequence
	// Re-send the exact same prepared command bytes through the rebind path.
	replayed := harness.session.HandleAttachRebind(mustCanonical(prepared.request))
	if replayed.Status != first.Status || replayed.CommandHead != first.CommandHead || replayed.ReceiptDigest != first.ReceiptDigest || replayed.ObservationDigest != first.ObservationDigest || replayed.CommandID != first.CommandID || replayed.Sequence != first.Sequence || !bytes.Equal(replayed.Payload, first.Payload) {
		t.Fatalf("replay response %+v != first %+v", replayed, first)
	}
	if harness.session.journal.Snapshot().Sequence != journalAfterFirst {
		t.Fatal("exact replay appended a second side effect to the journal")
	}
}
