//go:build darwin

package processsupervisor

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type attachMechanicsV2 struct{ countingMechanicsV2 }

func (m *attachMechanicsV2) attachChildIdentity() (ProcessIdentity, error) {
	return validBootstrap().Core.Process, nil
}

func TestReadOnlyAttachV2WirePreservesOwnerJournalAndMechanics(t *testing.T) {
	testAttachV2Wire(t, false)
}

func TestAttachV2WireCommitsSingleBoundSuccessorOnSameConnection(t *testing.T) {
	testAttachV2Wire(t, true)
}

func newAttachV2WireFixture(t *testing.T) (*supervisorV2Harness, *attachMechanicsV2, CoreIdentity, attachRequestV2) {
	t.Helper()
	m := &attachMechanicsV2{}
	self := validBootstrapV2().Core
	self.Process.PID += 100
	self.Process.ProcessGroupID, self.Process.SessionID = self.Process.PID, self.Process.PID
	h := newSupervisorV2HarnessOptions(t, nil, m, func(o *supervisorLoopOptions) {
		o.observeSelf = func() (CoreIdentity, error) { return self, nil }
	})
	h.bind(t)
	h.session.core.mu.Lock()
	spawn := spawnRequestForSessionV2(t, h.session)
	h.session.core.mu.Unlock()
	h.do(t, spawn)
	h.session.core.mu.Lock()
	anchor := testAnchorV2(h.session)
	anchor.ControlDirectory = h.bootstrap.ControlDirectoryIdentity
	anchor.Binding.FixedBinary = self.Binary
	anchor.Binding.ControlSocket, anchor.Binding.ControlFiles = h.handshake.ControlSocket, h.handshake.ControlFiles
	lastObservation := h.session.core.lastObservation
	h.session.core.mu.Unlock()
	_ = h.connection.Close()
	select {
	case <-h.reconnectReady:
	case <-time.After(5 * time.Second):
		t.Fatal("listener not ready")
	}
	request := testAttachRequestV2(t)
	request.SessionNonce, request.Core = h.bootstrap.SessionNonce, h.bootstrap.Core
	a := &request.Authority
	a.PreviousSupervisor, a.Supervisor, a.ChildObservationDigest = anchor, self.Process, lastObservation
	a.Child, _ = m.attachChildIdentity()
	a.CurrentAcquisition.AuthorityNamespaceID = anchor.Binding.Authority.AuthorityNamespaceID
	a.CurrentAcquisition.OwnerUID, a.CurrentAcquisition.OwnerGID = request.Core.UID, request.Core.GID
	a.CurrentAcquisition.OwnerProcess, a.CurrentAcquisition.OwnerBinary = request.Core.Process, request.Core.Binary
	a.CurrentOwnerBoundFact.Authority = anchor.Binding.Authority
	request.RequestDigest, _ = request.detachedDigest()
	if request.validate() != nil {
		t.Fatal("invalid wire authority fixture")
	}
	return h, m, self, request
}

func testAttachV2Wire(t *testing.T, rebind bool) {
	h, m, self, request := newAttachV2WireFixture(t)
	a := &request.Authority
	anchor, lastObservation, calls := a.PreviousSupervisor, a.ChildObservationDigest, m.calls
	before, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*attachRequestV2){
		func(r *attachRequestV2) { r.Authority.Child.PID++ },
		func(r *attachRequestV2) { r.SchemaVersion = AttachSchema },
		func(r *attachRequestV2) { r.Authority.PreviousSupervisor.Binding.JournalHead = digest("wrong-journal") },
	} {
		bad := request
		mutate(&bad)
		bad.RequestDigest, _ = bad.detachedDigest()
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(h.root, controlSocket), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		codec, err := NewProtocolCodec(conn)
		if err != nil || codec.Write(bad) != nil {
			_ = conn.Close()
			t.Fatal("invalid Attach send")
		}
		var rejected attachResponseV2
		err = codec.Read(&rejected)
		_ = conn.Close()
		if err == nil {
			t.Fatal("forged Attach received success")
		}
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(h.root, controlSocket), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(request) != nil {
		t.Fatal("Attach write")
	}
	var response attachResponseV2
	if err := codec.Read(&response); err != nil || response.validate(request, self) != nil {
		t.Fatalf("Attach response: %v", err)
	}
	expectedHead, expectedCommandHead := anchor.Binding.CurrentAuthorityHead, anchor.Binding.CommandHead
	if rebind {
		h.session.core.mu.Lock()
		command := sessionRequestV2(t, h.session, CommandBindAuthority, "wire-attach-bind-v2", BindAuthorityPayload{
			SupervisorStartedFactDigest: h.session.core.supervisorStartedFact, OwnerEpoch: anchor.Binding.OwnerEpoch,
			PreviousAuthorityHead: anchor.Binding.CurrentAuthorityHead, AuthorityHead: a.CurrentOwnerBoundFact.AttemptHead})
		h.session.core.mu.Unlock()
		if codec.Write(command) != nil {
			t.Fatal("continuation write")
		}
		var result responseV2
		if err := codec.Read(&result); err != nil || result.Status != "ok" || validateV2ResponseBinding(result, command) != nil {
			t.Fatalf("continuation receipt: %+v %v", result, err)
		}
		expectedHead, expectedCommandHead = a.CurrentOwnerBoundFact.AttemptHead, result.CommandHead
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := codec.Read(&response); !errors.Is(err, io.EOF) {
		t.Fatalf("Attach did not close read-only exchange: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
	if err != nil {
		t.Fatal(err)
	}
	if !rebind && !bytes.Equal(before, after) {
		t.Fatal("read-only Attach wrote mechanics journal")
	}
	if rebind {
		journal, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, after)
		if err != nil || journal.Sequence != anchor.Binding.JournalSequence+2 {
			t.Fatal("rebind journal not exactly one pair")
		}
	}
	h.session.core.mu.Lock()
	defer h.session.core.mu.Unlock()
	if h.session.core.ownerEpoch != anchor.Binding.OwnerEpoch || h.session.core.authorityHead != expectedHead ||
		h.session.core.commandHead != expectedCommandHead || h.session.core.lastObservation != lastObservation || m.calls != calls {
		t.Fatal("Attach mutated owner/head/child or executed mechanics")
	}
}
