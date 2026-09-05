//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type supervisorV2Harness struct {
	root           string
	directory      *os.File
	connection     *net.UnixConn
	codec          *ProtocolCodec
	session        *sessionV2
	bootstrap      bootstrapRequestV2
	handshake      handshakeResponseV2
	reconnectReady chan struct{}
	cancel         context.CancelFunc
	done           chan error
	waitOnce       sync.Once
	doneErr        error
}

func newSupervisorV2Harness(t *testing.T, configure func(*sessionV2, *os.File), mechanics Mechanics) *supervisorV2Harness {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "marshal-v2-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chown(root, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	_, identity, err := observeControlDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := validBootstrapV2()
	bootstrap.ControlDirectoryIdentity = identity
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile, serverFile := os.NewFile(uintptr(fds[0]), "test-v2-client"), os.NewFile(uintptr(fds[1]), "test-v2-bootstrap")
	t.Cleanup(func() { _ = serverFile.Close() })
	conn, err := net.FileConn(clientFile)
	_ = clientFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	connection := conn.(*net.UnixConn)
	if connection.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		t.Fatal("deadline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &supervisorV2Harness{root: root, directory: directory, connection: connection, bootstrap: bootstrap,
		reconnectReady: make(chan struct{}), cancel: cancel, done: make(chan error, 1)}
	ready := make(chan *sessionV2, 1)
	options := supervisorLoopOptions{mechanics: mechanics,
		observePeer:    func(*net.UnixConn) (CoreIdentity, error) { return bootstrap.Core, nil },
		observeSelf:    func() (CoreIdentity, error) { return bootstrap.Core, nil },
		reconnectReady: func() { close(h.reconnectReady) },
		configureSessionV2: func(s *sessionV2) {
			if configure != nil {
				configure(s, directory)
			}
			ready <- s
		},
	}
	go func() { h.done <- runSupervisorLoop(ctx, serverFile, directory, options) }()
	t.Cleanup(func() { cancel(); _ = connection.Close(); _ = h.wait(t) })
	h.codec, err = NewProtocolCodec(connection)
	if err != nil || h.codec.Write(bootstrap) != nil {
		t.Fatal("bootstrap write")
	}
	if err := h.codec.Read(&h.handshake); err != nil || h.handshake.validate() != nil {
		t.Fatalf("v2 handshake: %v %+v", err, h.handshake)
	}
	h.session = <-ready
	return h
}

func (h *supervisorV2Harness) wait(t *testing.T) error {
	t.Helper()
	h.waitOnce.Do(func() {
		select {
		case h.doneErr = <-h.done:
		case <-time.After(5 * time.Second):
			h.doneErr = errors.New("v2 server failed to stop")
		}
	})
	return h.doneErr
}

func (h *supervisorV2Harness) request(t *testing.T, command CommandName, id string, payload any) requestV2 {
	h.session.core.mu.Lock()
	defer h.session.core.mu.Unlock()
	return sessionRequestV2(t, h.session, command, id, payload)
}

func (h *supervisorV2Harness) do(t *testing.T, r requestV2) responseV2 {
	t.Helper()
	if h.codec.Write(r) != nil {
		t.Fatal("command write")
	}
	var response responseV2
	if err := h.codec.Read(&response); err != nil || validateV2ResponseBinding(response, r) != nil || response.Status != "ok" {
		t.Fatalf("command %s: %v %+v", r.Command, err, response)
	}
	return response
}

func (h *supervisorV2Harness) bind(t *testing.T) {
	h.do(t, h.request(t, CommandBindAuthority, "wire-bind", BindAuthorityPayload{SupervisorStartedFactDigest: digest("wire-started"), OwnerEpoch: h.bootstrap.OwnerEpoch, PreviousAuthorityHead: h.bootstrap.CurrentAuthorityHead, AuthorityHead: digest("wire-bound")}))
}

func TestSupervisorV2InheritedWireAndReceiptReconnect(t *testing.T) {
	m := &countingMechanicsV2{}
	h := newSupervisorV2Harness(t, nil, m)
	h.bind(t)
	h.session.core.mu.Lock()
	spawn := spawnRequestForSessionV2(t, h.session)
	reconnect := reconnectForSessionV2(h.session, &spawn)
	h.session.core.mu.Unlock()
	response := h.do(t, spawn)
	_ = h.connection.Close()
	select {
	case <-h.reconnectReady:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect not ready")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(h.root, controlSocket), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	codec, _ := NewProtocolCodec(conn)
	if codec.Write(reconnect) != nil {
		t.Fatal("reconnect write")
	}
	var hs handshakeResponseV2
	if err := codec.Read(&hs); err != nil || hs.validate() != nil || hs.Reconciliation != ReconciliationReceiptCommitted || hs.ReplayedResponse == nil || hs.ReplayedResponse.ReceiptDigest != response.ReceiptDigest {
		t.Fatalf("reconnect: %v %+v", err, hs)
	}
	h.session.core.mu.Lock()
	calls := m.calls
	h.session.core.mu.Unlock()
	if calls != 1 {
		t.Fatalf("duplicate workload: %d", calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, JournalFileName)); !os.IsNotExist(err) {
		t.Fatal("v1 journal created")
	}
	data, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, data)
	if err != nil || observation.Head != hs.JournalHead || observation.Sequence != 5 {
		t.Fatalf("journal: %+v %v", observation, err)
	}
	h.cancel()
	if err := h.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestSupervisorV2PostEffectBoundaryDriftIsSilent(t *testing.T) {
	m := &countingMechanicsV2{}
	h := newSupervisorV2Harness(t, func(_ *sessionV2, directory *os.File) {
		m.before = func() { _ = writeOwnerObject(directory, JournalFileName, []byte("legacy-drift")) }
	}, m)
	h.bind(t)
	h.session.core.mu.Lock()
	spawn := spawnRequestForSessionV2(t, h.session)
	h.session.core.mu.Unlock()
	if h.codec.Write(spawn) != nil {
		t.Fatal("spawn write")
	}
	var response responseV2
	if h.codec.Read(&response) == nil {
		t.Fatal("receipt emitted after boundary drift")
	}
	if err := h.wait(t); !errors.Is(err, ErrIntervention) {
		t.Fatalf("drift: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("calls=%d", m.calls)
	}
	record, ok := h.session.journal.receipt(spawn.CommandID)
	if !ok || record.Response.Status != "ok" {
		t.Fatal("lost receipt evidence")
	}
}

type transcriptMechanicsV2 struct {
	countingMechanicsV2
	directory *os.File
}

func (m *transcriptMechanicsV2) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	result, err := m.result(CommandCollect, "terminal")
	if err != nil {
		return result, err
	}
	var report ProcessReport
	if strictCanonicalDecode(result.Payload, &report) != nil {
		return MechanicsResult{}, ErrInvalid
	}
	stdout, stderr := []byte("business result\n"), []byte{}
	report.StdoutBytes, report.StderrBytes = uint64(len(stdout)), uint64(len(stderr))
	report.StdoutDigest, report.StderrDigest = canonical.DigestBytes(stdout), canonical.DigestBytes(stderr)
	raw := mustCanonical(report)
	for _, object := range []struct {
		name string
		data []byte
	}{{stdoutObjectName, stdout}, {stderrObjectName, stderr}, {transcriptObjectName, raw}} {
		if err := writeOwnerObject(m.directory, object.name, object.data); err != nil {
			return MechanicsResult{}, err
		}
	}
	result.ReasonCode, result.Payload = "transcript-collected", raw
	result.ObservationDigest, result.TranscriptDigest = canonical.DigestBytes(raw), canonical.DigestBytes(raw)
	result.StdoutBytes, result.StderrBytes = report.StdoutBytes, report.StderrBytes
	return result, nil
}

func TestSupervisorV2CollectValidatesExactOutputObjects(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		name := "complete-lifecycle"
		if tamper {
			name = "tampered-output"
		}
		t.Run(name, func(t *testing.T) { testSupervisorV2Collect(t, tamper) })
	}
}

func testSupervisorV2Collect(t *testing.T, tamper bool) {
	m := &transcriptMechanicsV2{}
	h := newSupervisorV2Harness(t, func(_ *sessionV2, d *os.File) { m.directory = d }, m)
	h.bind(t)
	h.session.core.mu.Lock()
	spawn := spawnRequestForSessionV2(t, h.session)
	h.session.core.mu.Unlock()
	h.do(t, spawn)
	h.do(t, h.request(t, CommandResume, "wire-resume", ResumePayload{ProcessStartedFactDigest: digest("wire-process")}))
	h.session.core.mu.Lock()
	cleanup := CleanupPayload{ProcessStartedFactDigest: h.session.core.startedFact, LastObservationDigest: h.session.core.lastObservation,
		TerminalizationBarrierDigest: digest("wire-barrier"), TerminalizationID: "wire-terminal", TerminalGeneration: 1, CleanupBindingDigest: digest("wire-cleanup")}
	h.session.core.mu.Unlock()
	observed := h.do(t, h.request(t, CommandInspect, "wire-inspect", cleanup))
	collect := h.request(t, CommandCollect, "wire-collect", CollectPayload{ProcessStartedFactDigest: cleanup.ProcessStartedFactDigest, LastObservationDigest: observed.ObservationDigest})
	h.do(t, collect)
	record, ok := h.session.journal.receipt(collect.CommandID)
	if !ok {
		t.Fatal("no collect receipt")
	}
	transcript, err := readCollectedTranscriptV2(h.directory, record)
	if err != nil || string(transcript.Stdout) != "business result\n" {
		t.Fatalf("transcript: %v", err)
	}
	if !tamper {
		h.do(t, h.request(t, CommandClose, "wire-close", ClosePayload{CleanupBindingDigest: cleanup.CleanupBindingDigest, ProcessTerminalFactDigest: digest("wire-terminal-fact"), AllocationTerminatedDigest: digest("wire-allocation-terminal")}))
		if err := h.wait(t); err != nil {
			t.Fatalf("complete lifecycle: %v", err)
		}
		if h.session.core.State() != string(sessionClosed) || m.calls != 5 {
			t.Fatalf("terminal state/calls: %s/%d", h.session.core.State(), m.calls)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(h.root, stdoutObjectName), []byte("forged output!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	closeRequest := h.request(t, CommandClose, "wire-close", ClosePayload{CleanupBindingDigest: cleanup.CleanupBindingDigest, ProcessTerminalFactDigest: digest("wire-terminal-fact"), AllocationTerminatedDigest: digest("wire-allocation-terminal")})
	if h.codec.Write(closeRequest) != nil {
		t.Fatal("close write")
	}
	var response responseV2
	if h.codec.Read(&response) == nil {
		t.Fatal("tampered transcript accepted")
	}
	if err := h.wait(t); !errors.Is(err, ErrIntervention) {
		t.Fatalf("tamper: %v", err)
	}
}
