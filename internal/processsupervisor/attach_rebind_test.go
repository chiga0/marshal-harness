package processsupervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// TestAttachedSessionExecutePreparedBindAuthorityDirect exercises the borrowed
// client-side guard and same-connection execute without the full WithAttached
// binary-observation path: an in-process Unix socketpair plays the server. It
// covers the happy path plus the callback-escape, double-call, wrong-command,
// wrong-anchor, before-observation and transport-failure negatives.
func TestAttachedSessionExecutePreparedBindAuthorityDirect(t *testing.T) {
	authority := validAttachAuthority()
	anchor := authority.PreviousSupervisor
	successorHead := digest("successor-head")
	startedFact := digest("started")
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: anchor.OwnerEpoch, PreviousAuthorityHead: anchor.CurrentAuthorityHead, AuthorityHead: successorHead}
	prepared, err := PrepareCommand(anchor, CommandOptions{Command: CommandBindAuthority, CommandID: "direct-rebind", Sequence: anchor.CommandSequence + 1, PreviousCommandDigest: anchor.CommandHead, CurrentAuthorityHead: anchor.CurrentAuthorityHead, Deadline: time.Now().Add(20 * time.Second)}, payload)
	if err != nil {
		t.Fatal(err)
	}
	observation := AttachObservation{
		SchemaVersion: AttachObservationSchema, ProtocolRevision: ProtocolRevision, RequestDigest: digest("req"), ResponseDigest: digest("resp"),
		PreviousSupervisor: anchor,
		Handshake: HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: anchor.SessionID, SessionNonceDigest: anchor.SessionNonceDigest, OwnerEpoch: anchor.OwnerEpoch, CurrentAuthorityHead: anchor.CurrentAuthorityHead, CommandSequence: anchor.CommandSequence, CommandHead: anchor.CommandHead, JournalSequence: anchor.JournalSequence, JournalHead: anchor.JournalHead, ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-29T00:00:01Z", SupervisorProcess: authority.Supervisor, SupervisorBinary: anchor.FixedBinary, ControlSocket: anchor.ControlSocket, ControlFiles: anchor.ControlFiles},
		Supervisor: authority.Supervisor, CurrentAcquisition: authority.CurrentAcquisition, CurrentOwnerBoundFact: authority.CurrentOwnerBoundFact, Child: authority.Child, ChildObservationDigest: authority.ChildObservationDigest,
		ControlDirectory: ControlDirectoryIdentity{CanonicalPath: "/private/control", Device: 20, Inode: 21, FileType: "directory", UID: 501, GID: 20, Mode: 0o040700, LinkCount: 2},
		Peer:             CoreIdentity{UID: authority.CurrentAcquisition.OwnerUID, GID: authority.CurrentAcquisition.OwnerGID, Process: authority.CurrentAcquisition.OwnerProcess, Binary: authority.CurrentAcquisition.OwnerBinary}, ObservedAt: "2026-08-29T00:00:01Z",
	}

	// serveRebindOnce reads one Request on server and writes one ok bind-authority
	// Response bound to it. It is the fake server for the direct execute test.
	serveRebindOnce := func(t *testing.T, server net.Conn, done chan<- error) {
		t.Helper()
		scodec, err := NewProtocolCodec(server)
		if err != nil {
			done <- err
			return
		}
		var req Request
		if err := scodec.Read(&req); err != nil {
			done <- err
			return
		}
		result := MechanicsResult{Disposition: "ok", ReasonCode: "authority-bound", ObservationDigest: payload.SupervisorStartedFactDigest, Payload: canonicalEmptyPayload()}
		receipt, _ := digestValue(result)
		commandHead, _ := digestValue(struct {
			Previous string `json:"previousCommandDigest"`
			Request  string `json:"requestDigest"`
			Receipt  string `json:"receiptDigest"`
		}{req.PreviousCommandDigest, req.RequestDigest, receipt})
		resp := Response{SchemaVersion: ResponseSchema, ProtocolRevision: ProtocolRevision, SessionID: req.SessionID, Command: req.Command, CommandID: req.CommandID, Sequence: req.Sequence, RequestDigest: req.RequestDigest, Status: "ok", ReasonCode: result.ReasonCode, ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: commandHead, Payload: mustCanonical(result)}
		done <- scodec.Write(resp)
	}

	t.Run("happy", func(t *testing.T) {
		client, server := rebindSocketPair(t)
		defer client.Close()
		defer server.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		done := make(chan error, 1)
		go func() { serveRebindOnce(t, server, done) }()
		var outcome VerifiedCommandOutcome
		if err := callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			got, err := s.ExecutePreparedBindAuthority(context.Background(), prepared)
			outcome = got
			return err
		}); err != nil {
			t.Fatalf("borrow: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("server: %v", err)
		}
		if outcome.Command != CommandBindAuthority || outcome.Status != "ok" || outcome.Disposition != "ok" {
			t.Fatalf("outcome=%+v", outcome)
		}
		if outcome.Recovery.PostCommand.CurrentAuthorityHead != successorHead {
			t.Fatalf("post authority head=%s", outcome.Recovery.PostCommand.CurrentAuthorityHead)
		}
		session.guard.mu.Lock()
		executed := session.guard.commandExecuted
		post := session.guard.postCommand
		session.guard.mu.Unlock()
		if !executed || post.CurrentAuthorityHead != successorHead {
			t.Fatal("guard did not record the executed rebind")
		}
	})

	t.Run("double-call-rejected", func(t *testing.T) {
		client, server := rebindSocketPair(t)
		defer client.Close()
		defer server.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		go func() {
			scodec, _ := NewProtocolCodec(server)
			var req Request
			for i := 0; i < 2; i++ {
				if err := scodec.Read(&req); err != nil {
					return
				}
				result := MechanicsResult{Disposition: "ok", ReasonCode: "authority-bound", ObservationDigest: payload.SupervisorStartedFactDigest, Payload: canonicalEmptyPayload()}
				receipt, _ := digestValue(result)
				commandHead, _ := digestValue(struct {
					Previous string `json:"previousCommandDigest"`
					Request  string `json:"requestDigest"`
					Receipt  string `json:"receiptDigest"`
				}{req.PreviousCommandDigest, req.RequestDigest, receipt})
				_ = scodec.Write(Response{SchemaVersion: ResponseSchema, ProtocolRevision: ProtocolRevision, SessionID: req.SessionID, Command: req.Command, CommandID: req.CommandID, Sequence: req.Sequence, RequestDigest: req.RequestDigest, Status: "ok", ReasonCode: result.ReasonCode, ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: commandHead, Payload: mustCanonical(result)})
			}
		}()
		var second error
		err = callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			if _, err := s.ExecutePreparedBindAuthority(context.Background(), prepared); err != nil {
				return err
			}
			_, second = s.ExecutePreparedBindAuthority(context.Background(), prepared)
			return nil
		})
		if err == nil {
			t.Fatal("double-call borrow did not fail closed")
		}
		if second == nil {
			t.Fatal("second ExecutePreparedBindAuthority succeeded")
		}
	})

	t.Run("wrong-command-rejected", func(t *testing.T) {
		client, _ := rebindSocketPair(t)
		defer client.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		resumePrepared, err := PrepareCommand(anchor, CommandOptions{Command: CommandResume, CommandID: "direct-resume", Sequence: anchor.CommandSequence + 1, PreviousCommandDigest: anchor.CommandHead, CurrentAuthorityHead: anchor.CurrentAuthorityHead, Deadline: time.Now().Add(20 * time.Second)}, ResumePayload{ProcessStartedFactDigest: digest("p")})
		if err != nil {
			t.Fatal(err)
		}
		err = callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			_, err = s.ExecutePreparedBindAuthority(context.Background(), resumePrepared)
			return err
		})
		if err == nil {
			t.Fatal("non-bind-authority Execute succeeded")
		}
	})

	t.Run("wrong-anchor-rejected", func(t *testing.T) {
		client, _ := rebindSocketPair(t)
		defer client.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		drifted := anchor
		drifted.CommandHead = digest("drifted-command-head")
		driftedPrepared, err := PrepareCommand(drifted, CommandOptions{Command: CommandBindAuthority, CommandID: "drifted", Sequence: drifted.CommandSequence + 1, PreviousCommandDigest: drifted.CommandHead, CurrentAuthorityHead: drifted.CurrentAuthorityHead, Deadline: time.Now().Add(20 * time.Second)}, payload)
		if err != nil {
			t.Fatal(err)
		}
		err = callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			_, err = s.ExecutePreparedBindAuthority(context.Background(), driftedPrepared)
			return err
		})
		if err == nil {
			t.Fatal("drifted-anchor Execute succeeded")
		}
	})

	t.Run("before-observation-rejected", func(t *testing.T) {
		client, _ := rebindSocketPair(t)
		defer client.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		err = callAttachedBorrower(session, func(s *AttachedSession) error {
			_, err := s.ExecutePreparedBindAuthority(context.Background(), prepared)
			return err
		})
		if err == nil {
			t.Fatal("Execute before Observation succeeded")
		}
	})

	t.Run("cross-goroutine-rejected", func(t *testing.T) {
		client, _ := rebindSocketPair(t)
		defer client.Close()
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		goroutineErr := make(chan error, 1)
		err = callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			done := make(chan struct{})
			go func() {
				_, e := s.ExecutePreparedBindAuthority(context.Background(), prepared)
				goroutineErr <- e
				close(done)
			}()
			<-done
			return nil
		})
		if err == nil {
			t.Fatal("cross-goroutine borrow did not fail closed")
		}
		if err := <-goroutineErr; err == nil {
			t.Fatal("cross-goroutine Execute succeeded")
		}
	})

	t.Run("transport-failure", func(t *testing.T) {
		client, server := rebindSocketPair(t)
		defer client.Close()
		_ = server.Close() // close the server side so the client read fails
		codec, err := NewProtocolCodec(client)
		if err != nil {
			t.Fatal(err)
		}
		session := newRebindAttachedSession(observation, client, codec, anchor)
		execErr := make(chan error, 1)
		_ = callAttachedBorrower(session, func(s *AttachedSession) error {
			if _, err := s.Observation(); err != nil {
				return err
			}
			_, e := s.ExecutePreparedBindAuthority(context.Background(), prepared)
			execErr <- e
			return e
		})
		if err := <-execErr; err == nil {
			t.Fatal("transport failure did not surface an error")
		}
		session.guard.mu.Lock()
		executed := session.guard.commandExecuted
		session.guard.mu.Unlock()
		if executed {
			t.Fatal("transport failure recorded a command as executed")
		}
	})
}

// rebindSocketPair returns a connected Unix socketpair suitable for a
// ProtocolCodec on each end. Both ends are *net.UnixConn so the borrowed
// session's SetDeadline/CloseWrite path is exercised for real.
func rebindSocketPair(t *testing.T) (client, server *net.UnixConn) {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "rebind-pair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketPath := filepath.Join(root, "rebind.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverReady := make(chan struct{})
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			close(serverReady)
			return
		}
		server = conn
		close(serverReady)
	}()
	client, err = net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	<-serverReady
	if server == nil {
		t.Fatal("server side did not accept")
	}
	return client, server
}

var _ = canonical.DigestBytes
